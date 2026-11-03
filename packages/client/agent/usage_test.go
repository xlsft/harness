package agent_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/bootstrap"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/usagelimit"
)

// fakeUsage reports a fixed evaluation through a real Tracker (state changes only).
type fakeUsage struct {
	mu      sync.Mutex
	eval    usagelimit.Status
	tracker usagelimit.Tracker
	calls   int
	errs    []error
}

func (f *fakeUsage) Check(_ context.Context, _ string, err error) (*usagelimit.Status, usagelimit.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err != nil {
		f.errs = append(f.errs, err)
		if !usagelimit.IsReached(err) {
			return nil, f.tracker.Last()
		}
		return f.tracker.Observe(usagelimit.Status{State: usagelimit.Reached, ProviderID: f.eval.ProviderID, ProviderName: f.eval.ProviderName}), f.tracker.Last()
	}
	return f.tracker.Observe(f.eval), f.tracker.Last()
}

func readTool(r *llm.Request, id string) fg.Reply {
	return fg.Reply{Events: fg.Message("m_"+id, fg.ToolUse(id, "Read", map[string]any{"file_path": filepath.Join(fg.Cwd(r), "CLAUDE.md")}))}
}

func countReminder(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "nearly used up") {
				n++
			}
		}
	}
	return n
}

func TestUsageWrapUpReminderOncePerTurn(t *testing.T) {
	// Turn 1: three tool rounds, then the answer (requests 0..3); turn 2: one tool round (4), then the answer.
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		if n <= 2 || n == 4 {
			return readTool(r, "t"+string(rune('1'+n)))
		}
		return fg.Reply{Events: fg.Message("done", fg.Text("done"))}
	})
	mon := &fakeUsage{eval: usagelimit.Status{State: usagelimit.WrapUp, ProviderID: "anthropic", ProviderName: "Anthropic",
		WindowLabel: "5h", UsedPercent: 98, ResetsAt: "2026-11-03T23:00:00Z"}}
	var mu sync.Mutex
	var events []usagelimit.Status
	s := e.open(t, func(o *bootstrap.Options) {
		o.UsageMonitor = mon
		o.OnEvent = func(ev agent.Event) {
			if ev.Kind == agent.EventUsageLimit {
				mu.Lock()
				events = append(events, *ev.UsageLimit)
				mu.Unlock()
			}
		}
	})
	res := s.Agent.RunTurn(context.Background(), prompt("do the work"))
	if res.Subtype != agent.SubtypeSuccess {
		t.Fatalf("result %+v", res)
	}
	reqs := e.srv.Requests()
	if len(reqs) != 4 {
		t.Fatalf("want 4 requests, got %d", len(reqs))
	}
	// The reminder rides on the second request (after the first response) and stays in history only once.
	if countReminder(reqs[0].Messages) != 0 || countReminder(reqs[1].Messages) != 1 || countReminder(reqs[3].Messages) != 1 {
		t.Fatalf("reminder counts: %d %d %d", countReminder(reqs[0].Messages), countReminder(reqs[1].Messages), countReminder(reqs[3].Messages))
	}
	last := reqs[1].Messages[len(reqs[1].Messages)-1].Content
	text := last[len(last)-1].Text
	if !strings.Contains(text, "Anthropic") || !strings.Contains(text, "5h: 98% used") || !strings.Contains(text, "the limit resets at") {
		t.Fatalf("reminder text: %q", text)
	}
	mu.Lock()
	got := append([]usagelimit.Status(nil), events...)
	mu.Unlock()
	if len(got) != 2 || got[0].State != usagelimit.WrapUp || got[0].WrappedUp || !got[1].WrappedUp || got[1].State != usagelimit.WrapUp {
		t.Fatalf("events: %+v", got)
	}
	// Both are on record for replay.
	var recorded []usagelimit.Status
	for _, en := range s.Transcript.Entries() {
		if en.Type == session.TypeSystem && en.Subtype == session.SubtypeUsageLimit && en.UsageLimit != nil {
			recorded = append(recorded, *en.UsageLimit)
		}
	}
	if len(recorded) != 2 || !recorded[1].WrappedUp {
		t.Fatalf("transcript: %+v", recorded)
	}

	// The next turn gets its own reminder (still WRAP_UP), but no new state event.
	s.Agent.RunTurn(context.Background(), prompt("one more thing"))
	reqs = e.srv.Requests()
	if countReminder(reqs[len(reqs)-1].Messages) != 2 {
		t.Fatalf("second turn reminder count: %d", countReminder(reqs[len(reqs)-1].Messages))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 3 || !events[2].WrappedUp {
		t.Fatalf("events after the second turn: %+v", events)
	}
}

func TestUsageNoReminderWhenTurnEnds(t *testing.T) {
	e := setup(t, nil)
	mon := &fakeUsage{eval: usagelimit.Status{State: usagelimit.WrapUp, ProviderName: "OpenAI", WindowLabel: "tokens/min", UsedPercent: 99}}
	var events []usagelimit.Status
	s := e.open(t, func(o *bootstrap.Options) {
		o.UsageMonitor = mon
		o.OnEvent = func(ev agent.Event) {
			if ev.Kind == agent.EventUsageLimit {
				events = append(events, *ev.UsageLimit)
			}
		}
	})
	s.Agent.RunTurn(context.Background(), prompt("hi"))
	s.Agent.RunTurn(context.Background(), prompt("again"))
	for _, r := range e.srv.Requests() {
		if countReminder(r.Messages) != 0 {
			t.Fatal("a turn without tool calls must not get (or carry over) the wrap-up reminder")
		}
	}
	if len(events) != 1 || events[0].WrappedUp {
		t.Fatalf("events: %+v", events)
	}
}

func TestUsageReachedFromRateLimitError(t *testing.T) {
	e := setup(t, fg.Sequence(func(*llm.Request) fg.Reply {
		return fg.Reply{Err: rpc.NewError(connect.CodeResourceExhausted, commonv1.ErrorType_ERROR_TYPE_QUOTA_EXCEEDED, "insufficient_quota: you exceeded your current quota", 0)}
	}))
	mon := &fakeUsage{eval: usagelimit.Status{ProviderID: "openai", ProviderName: "OpenAI"}}
	var events []usagelimit.Status
	s := e.open(t, func(o *bootstrap.Options) {
		o.UsageMonitor = mon
		o.OnEvent = func(ev agent.Event) {
			if ev.Kind == agent.EventUsageLimit {
				events = append(events, *ev.UsageLimit)
			}
		}
	})
	res := s.Agent.RunTurn(context.Background(), prompt("hi"))
	if !res.IsError {
		t.Fatalf("want an error turn, got %+v", res)
	}
	if len(events) != 1 || events[0].State != usagelimit.Reached || events[0].ProviderName != "OpenAI" || events[0].WrappedUp {
		t.Fatalf("events: %+v (errors seen: %v)", events, mon.errs)
	}
}
