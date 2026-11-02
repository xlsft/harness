package server_test

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/rpc"
)

const (
	interruptMarker = "[Request interrupted by user]"
	replyText       = "Stopped while reading. What should I change?"
)

// isReplyRequest reports whether req is the interrupt reply request.
func isReplyRequest(req *llm.Request) bool {
	if req.Metadata == nil || req.Metadata.RequestClass != "auxiliary" || len(req.Messages) == 0 {
		return false
	}
	last := req.Messages[len(req.Messages)-1].Content
	return len(last) > 0 && strings.Contains(last[len(last)-1].Text, "The user stopped you")
}

func (h *harness) interrupt(sid string) {
	h.t.Helper()
	if _, err := h.client.Interrupt(h.ctx, connect.NewRequest(&agentv1.InterruptRequest{SessionId: sid})); err != nil {
		h.t.Fatal(err)
	}
}

// startInterrupted sends "start", interrupts the held first reply on its first
// delta and returns the events up to the interrupted TurnFinished.
func (h *harness) startInterrupted(sid string, ch <-chan *agentv1.SessionEvent) []*agentv1.SessionEvent {
	h.t.Helper()
	h.send(sid, "start")
	stopped := false
	got := h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if ev.GetAssistantDelta() != nil && !stopped {
			stopped = true
			h.interrupt(sid)
		}
		return isTurnFinished(ev)
	})
	if r := got[len(got)-1].GetTurnFinished().Result; r != agentv1.TurnFinished_RESULT_INTERRUPTED {
		h.t.Fatalf("first turn: %v", r)
	}
	return got
}

// heldFirst holds the first main request open until it is interrupted.
func heldFirst(n int) fg.Reply {
	return fg.Reply{Events: fg.Partial("msg_work", false, fg.Text("Reading the code")), Hold: true}
}

// After an interrupt the model's short reply is streamed after the
// interrupted TurnFinished, kept in the history before the next prompt and
// replayed in the same order on resume.
func TestInterruptReplyAppendedAndReplayed(t *testing.T) {
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		switch n {
		case 0:
			return heldFirst(n)
		case 1:
			return fg.Reply{Events: fg.Message("msg_reply", fg.Text(replyText))}
		}
		return fg.Reply{Events: fg.Message("msg_next", fg.Text("ok"))}
	}, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.startInterrupted(sid, ch)

	var deltas strings.Builder
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if d := ev.GetAssistantDelta(); d != nil && d.MessageId == "msg_reply" {
			deltas.WriteString(d.GetText())
		}
		if ev.GetTurnStarted() != nil || ev.GetTurnFinished() != nil {
			t.Fatalf("turn event during the reply: %v", ev)
		}
		am := ev.GetAssistantMessage()
		if am == nil {
			return false
		}
		if am.MessageId != "msg_reply" || am.Content[0].GetText().GetText() != replyText {
			t.Fatalf("reply message: %+v", am)
		}
		return true
	})
	if deltas.String() != replyText {
		t.Fatalf("streamed %q", deltas.String())
	}

	// The interrupt marker reports activity after the turn ended; the session still settles to idle.
	deadline := time.Now().Add(2 * time.Second)
	for h.listed(h.proj, sid).Status != agentv1.SessionStatus_SESSION_STATUS_IDLE {
		if time.Now().After(deadline) {
			t.Fatalf("status after the reply: %v", h.listed(h.proj, sid).Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	reqs := h.gw.Requests()
	if len(reqs) != 2 || !isReplyRequest(reqs[1]) {
		t.Fatalf("reply request missing: %d requests", len(reqs))
	}
	rr := reqs[1]
	if rr.Model != "fake-model" || rr.MaxTokens > 200 || rr.Thinking != nil || rr.ToolChoice == nil || rr.ToolChoice.Type != "none" {
		t.Fatalf("reply request: model %q max %d thinking %v choice %+v", rr.Model, rr.MaxTokens, rr.Thinking, rr.ToolChoice)
	}
	last := rr.Messages[len(rr.Messages)-1]
	if last.Role != llm.RoleUser || last.Content[0].Text != interruptMarker {
		t.Fatalf("reply request does not follow the marker: %+v", last)
	}

	h.send(sid, "try again")
	h.until(ch, isTurnFinished)
	reqs = h.gw.Requests()
	msgs := reqs[len(reqs)-1].Messages
	n := len(msgs)
	if n < 4 || msgs[n-3].Role != llm.RoleUser || msgs[n-3].Content[len(msgs[n-3].Content)-1].Text != interruptMarker ||
		msgs[n-2].Role != llm.RoleAssistant || msgs[n-2].Text() != replyText ||
		msgs[n-1].Role != llm.RoleUser || msgs[n-1].Content[len(msgs[n-1].Content)-1].Text != "try again" {
		t.Fatalf("history after the reply: %+v", msgs)
	}
	if strings.Contains(allText(reqs[len(reqs)-1]), "The user stopped you") {
		t.Fatal("the reply instruction leaked into the history")
	}
	h.closeSession(sid)

	if _, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid})); err != nil {
		t.Fatal(err)
	}
	got := h.until(h.subscribe(sid), func(ev *agentv1.SessionEvent) bool { return ev.GetSessionUpdated() != nil })
	var ks []string
	for _, ev := range got {
		k := evKind(ev)
		if am := ev.GetAssistantMessage(); am != nil && am.Content[0].GetText().GetText() == replyText {
			k = "reply"
		}
		if tf := ev.GetTurnFinished(); tf != nil && tf.Result == agentv1.TurnFinished_RESULT_INTERRUPTED {
			k = "interrupted"
		}
		ks = append(ks, k)
	}
	if s := strings.Join(ks, " "); s != "mode user turn asst user interrupted reply user turn asst done updated" {
		t.Fatalf("replayed: %s", s)
	}
	h.send(sid, "after resume")
	h.until(h.subscribe(sid), isTurnFinished)
}

// A new message or a second Interrupt while the reply streams cancels it:
// nothing is added and the next turn sees the marker followed by the prompt.
func TestInterruptReplyCancelled(t *testing.T) {
	for _, by := range []string{"message", "interrupt"} {
		t.Run(by, func(t *testing.T) {
			h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
				switch {
				case n == 0:
					return heldFirst(n)
				case isReplyRequest(r):
					return fg.Reply{Events: fg.Partial("msg_reply", false, fg.Text("Stopped")), Hold: true}
				}
				return fg.Reply{Events: fg.Message("msg_next", fg.Text("ok"))}
			}, nil)
			sid, ch := h.create(&agentv1.CreateSessionRequest{})
			h.startInterrupted(sid, ch)

			cancelled := false
			got := h.until(ch, func(ev *agentv1.SessionEvent) bool {
				if d := ev.GetAssistantDelta(); d != nil && d.MessageId == "msg_reply" && !cancelled {
					cancelled = true
					if by == "message" {
						h.send(sid, "new plan")
					} else {
						h.interrupt(sid)
						h.send(sid, "new plan")
					}
				}
				return isTurnFinished(ev)
			})
			for _, ev := range got {
				if am := ev.GetAssistantMessage(); am != nil && am.MessageId == "msg_reply" {
					t.Fatal("a cancelled reply was published")
				}
			}
			if r := got[len(got)-1].GetTurnFinished().Result; r != agentv1.TurnFinished_RESULT_SUCCESS {
				t.Fatalf("next turn: %v", r)
			}
			reqs := h.gw.Requests()
			if len(reqs) != 3 || !isReplyRequest(reqs[1]) {
				t.Fatalf("requests: %d", len(reqs))
			}
			msgs := reqs[2].Messages
			last := msgs[len(msgs)-1]
			if last.Role != llm.RoleUser || last.Content[0].Text != interruptMarker || last.Content[len(last.Content)-1].Text != "new plan" {
				t.Fatalf("next turn history: %+v", msgs)
			}
		})
	}
}

// A failed reply request adds nothing and leaves the session idle.
func TestInterruptReplyFailure(t *testing.T) {
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		switch {
		case n == 0:
			return heldFirst(n)
		case isReplyRequest(r):
			return fg.Reply{Err: rpc.NewError(connect.CodeInvalidArgument, commonv1.ErrorType_ERROR_TYPE_INVALID_ARGUMENT, "bad request", 0)}
		}
		return fg.Reply{Events: fg.Message("msg_next", fg.Text("ok"))}
	}, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.startInterrupted(sid, ch)
	// The reply request is not retried; wait until it was made.
	for i := 0; len(h.gw.Requests()) < 2; i++ {
		if i > 500 {
			t.Fatal("no reply request")
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.send(sid, "go on")
	got := h.until(ch, isTurnFinished)
	for _, ev := range got {
		if ev.GetAssistantMessage() != nil && ev.GetAssistantMessage().MessageId != "msg_next" {
			t.Fatalf("unexpected assistant message: %+v", ev.GetAssistantMessage())
		}
	}
	reqs := h.gw.Requests()
	if len(reqs) != 3 || !isReplyRequest(reqs[1]) || isReplyRequest(reqs[2]) {
		t.Fatalf("requests: %d", len(reqs))
	}
	msgs := reqs[2].Messages
	last := msgs[len(msgs)-1]
	if last.Content[0].Text != interruptMarker || last.Content[len(last.Content)-1].Text != "go on" {
		t.Fatalf("history after a failed reply: %+v", msgs)
	}
	info := h.listed(h.proj, sid)
	if info.Status != agentv1.SessionStatus_SESSION_STATUS_IDLE {
		t.Fatalf("status %v", info.Status)
	}
}

// HARNESS_NO_INTERRUPT_REPLY=1 turns the reply off.
func TestInterruptReplyDisabled(t *testing.T) {
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return heldFirst(n)
		}
		return fg.Reply{Events: fg.Message("msg_next", fg.Text("ok"))}
	}, nil)
	t.Setenv("HARNESS_NO_INTERRUPT_REPLY", "1")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.startInterrupted(sid, ch)
	h.send(sid, "go on")
	h.until(ch, isTurnFinished)
	for _, r := range h.gw.Requests() {
		if isReplyRequest(r) {
			t.Fatal("reply requested although disabled")
		}
	}
}
