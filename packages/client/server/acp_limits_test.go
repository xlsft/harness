package server_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/acp"
	"github.com/xlsft/harness/packages/client/acp/registry"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// stubLimits is a limits source that counts its fetches and holds them until released.
type stubLimits struct {
	fetches atomic.Int32
	gate    chan struct{}
	report  func() *registry.Limits
	err     error
	mu      sync.Mutex
	mayAsk  func() bool
}

func (s *stubLimits) Event(any, *registry.Limits, time.Time) *registry.Limits { return nil }

func (s *stubLimits) Fetch(ctx context.Context, cfg acp.Config, mayAsk func() bool) (*registry.Limits, error) {
	s.fetches.Add(1)
	s.mu.Lock()
	s.mayAsk = mayAsk
	s.mu.Unlock()
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.report != nil {
		return s.report(), nil
	}
	return nil, nil
}

// acpLimitsClock is a clock the test moves.
type acpLimitsClock struct{ at atomic.Int64 }

func (c *acpLimitsClock) now() time.Time { return time.Unix(c.at.Load(), 0) }

func (c *acpLimitsClock) add(d time.Duration) { c.at.Add(int64(d / time.Second)) }

func (a *acpModels) withLimits(src acp.LimitsSource) *acpLimitsClock {
	a.t.Helper()
	c := &acpLimitsClock{}
	c.at.Store(1_800_000_000)
	a.srv.SetAcpLimitsClock(c.now)
	a.srv.SetAcpLimitsSource("fake", src)
	a.install()
	a.settled()
	return c
}

func (a *acpModels) openSession() string {
	a.t.Helper()
	proj, _ := filepath.EvalSymlinks(a.t.TempDir())
	cs, err := a.client.CreateSession(a.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: proj, Engine: "acp:fake"}))
	if err != nil {
		a.t.Fatal(err)
	}
	return cs.Msg.Session.Id
}

func (a *acpModels) turn(sid, uuid, msg string) {
	a.t.Helper()
	if _, err := a.client.SendUserMessage(a.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: uuid, Content: text(msg)})); err != nil {
		a.t.Fatal(err)
	}
}

func TestAcpLimitsAreFetchedOnOpenAndThrottledPerAgent(t *testing.T) {
	a := newAcpModels(t)
	src := &stubLimits{report: func() *registry.Limits {
		return &registry.Limits{Windows: []registry.LimitWindow{{Label: "5-hour limit", UsedPercent: 1}}}
	}}
	clock := a.withLimits(src)
	sid := a.openSession()
	a.waitFor("the fetch at open", func() bool { return src.fetches.Load() == 1 })

	// Turns within the five minutes do not ask again, nor does another session of the agent.
	a.turn(sid, "u1", "hello")
	a.waitFor("the turn", func() bool {
		return a.sessionStatus(sid) == agentv1.SessionStatus_SESSION_STATUS_IDLE && a.count("prompt") == 1
	})
	a.openSession()
	time.Sleep(100 * time.Millisecond)
	if n := src.fetches.Load(); n != 1 {
		t.Fatalf("fetches within the interval: %d", n)
	}
	// Past them, the next turn does.
	clock.add(5*time.Minute + time.Second)
	a.turn(sid, "u2", "hello")
	a.waitFor("the fetch after the turn", func() bool { return src.fetches.Load() == 2 })
}

func TestAcpLimitsOneFetchAtATime(t *testing.T) {
	a := newAcpModels(t)
	src := &stubLimits{gate: make(chan struct{})}
	clock := a.withLimits(src)
	sid := a.openSession()
	a.waitFor("the fetch at open", func() bool { return src.fetches.Load() == 1 })
	// Time for another, but the first one is still out.
	clock.add(time.Hour)
	a.turn(sid, "u1", "hello")
	a.waitFor("the turn", func() bool {
		return a.count("prompt") == 1 && a.sessionStatus(sid) == agentv1.SessionStatus_SESSION_STATUS_IDLE
	})
	time.Sleep(100 * time.Millisecond)
	if n := src.fetches.Load(); n != 1 {
		t.Fatalf("fetches at once: %d", n)
	}
	close(src.gate)
}

func TestAcpLimitsWaitForAnOpenPermissionPrompt(t *testing.T) {
	a := newAcpModels(t)
	src := &stubLimits{}
	clock := a.withLimits(src)
	sid := a.openSession()
	a.waitFor("the fetch at open", func() bool { return src.fetches.Load() == 1 })
	clock.add(time.Hour)
	// Another session of the agent opens while this one waits for an answer.
	a.turn(sid, "u1", "tool")
	a.waitFor("the permission prompt", func() bool { return a.sessionStatus(sid) == agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT })
	a.openSession()
	time.Sleep(200 * time.Millisecond)
	if n := src.fetches.Load(); n != 1 {
		t.Fatalf("fetches during a permission prompt: %d", n)
	}
	// What the source is told just before it would ask.
	src.mu.Lock()
	mayAsk := src.mayAsk
	src.mu.Unlock()
	if mayAsk() {
		t.Fatal("mayAsk during a permission prompt")
	}
}

func TestAcpLimitsThatCostTokensAreNotAskedAgain(t *testing.T) {
	a := newAcpModels(t)
	src := &stubLimits{err: acp.ErrLimitsCostTokens}
	clock := a.withLimits(src)
	sid := a.openSession()
	a.waitFor("the fetch at open", func() bool { return src.fetches.Load() == 1 })
	time.Sleep(50 * time.Millisecond)
	clock.add(time.Hour)
	a.turn(sid, "u1", "hello")
	a.waitFor("the turn", func() bool {
		return a.count("prompt") == 1 && a.sessionStatus(sid) == agentv1.SessionStatus_SESSION_STATUS_IDLE
	})
	time.Sleep(100 * time.Millisecond)
	if n := src.fetches.Load(); n != 1 {
		t.Fatalf("fetches: %d", n)
	}
}

// The Claude source against the fake agent: the report reaches the agent's state and every live session, the user's
// session sees no prompt of it, and a failure keeps the numbers.
func TestAcpLimitsFromClaudeSourceReachLiveSessions(t *testing.T) {
	a := newAcpModels(t)
	usage := filepath.Join(t.TempDir(), "usage")
	report := "## Usage\n\n> Claude max subscription usage\n\n### Limits\n\n" +
		"**5-hour limit** — **89%** · Resets Nov 4, 12:30 PM GMT+3\n\n`██████████████████░░`\n\n" +
		"**Weekly · all models** — **87%** · Resets Nov 8, 12:00 AM GMT+3\n\n`█████████████████░░░`\n\n---\n\n### This session\n"
	if err := os.WriteFile(usage, []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_USAGE_FILE", usage)
	clock := a.withLimits(acp.LimitsSourceFor("claude-acp"))
	sid := a.openSession()
	other := a.openSession()
	limitsOf := func(id string) *agentv1.AcpAgentLimits { return a.sessionState(id).GetLimits() }
	a.waitFor("the report in the sessions", func() bool {
		return len(limitsOf(sid).GetWindows()) == 2 && len(limitsOf(other).GetWindows()) == 2
	})
	l := limitsOf(sid)
	if l.GetPlanName() != "max" || l.GetWindows()[0].GetLabel() != "5-hour limit" || l.GetWindows()[0].GetUsedPercent() != 89 || l.GetWindows()[0].GetResetsAt() == "" {
		t.Fatalf("limits: %v", l)
	}
	if got := a.list(false).GetLimits(); len(got.GetWindows()) != 2 {
		t.Fatalf("stored limits: %v", got)
	}
	if a.count("usage prompt") != 1 {
		t.Fatalf("agent log: %v", a.logged())
	}

	// A reply that stopped being a report keeps the numbers.
	if err := os.WriteFile(usage, []byte("something else"), 0o600); err != nil {
		t.Fatal(err)
	}
	clock.add(time.Hour)
	a.turn(sid, "u1", "hello")
	a.waitFor("the second ask", func() bool { return a.count("usage prompt") == 2 })
	a.waitFor("the helper to close", func() bool { return a.count("session/close") >= 2 })
	if got := limitsOf(sid); len(got.GetWindows()) != 2 || got.GetWindows()[0].GetUsedPercent() != 89 {
		t.Fatalf("limits after a bad reply: %v", got)
	}
}

// An agent without a source is never asked, whatever it advertises.
func TestAcpAgentWithoutSourceIsNotAsked(t *testing.T) {
	a := newAcpModels(t)
	usage := filepath.Join(t.TempDir(), "usage")
	if err := os.WriteFile(usage, []byte("**5-hour limit** — **89%**"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_USAGE_FILE", usage)
	a.install()
	a.settled()
	sid := a.openSession()
	a.turn(sid, "u1", "hello")
	a.waitFor("the turn", func() bool {
		return a.count("prompt") == 1 && a.sessionStatus(sid) == agentv1.SessionStatus_SESSION_STATUS_IDLE
	})
	time.Sleep(100 * time.Millisecond)
	if a.count("usage prompt") != 0 {
		t.Fatalf("agent log: %v", a.logged())
	}
}
