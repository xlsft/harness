package server_test

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
)

// BackgroundToolCall moves a running Bash command to the background: the tool
// call finishes at once with the note and the background marker, which the
// replay of the resumed session carries too.
func TestBackgroundToolCallRPC(t *testing.T) {
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("b1", "Bash", map[string]any{"description": "test", "command": "sleep 30"}))}
		}
		return fg.Reply{Events: fg.Message("m2", fg.Text("ok"))}
	}, func(o *bootstrap.Options) { o.AllowedTools = []string{"Bash"} })
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: h.proj})

	// Nothing runs yet.
	if res, err := h.client.BackgroundToolCall(h.ctx, connect.NewRequest(&agentv1.BackgroundToolCallRequest{SessionId: sid, ToolUseId: "b1"})); err != nil || res.Msg.Backgrounded {
		t.Fatalf("idle: %v %v", res, err)
	}
	if _, err := h.client.BackgroundToolCall(h.ctx, connect.NewRequest(&agentv1.BackgroundToolCallRequest{SessionId: sid, ToolUseId: "b1", Reason: agentv1.BackgroundReason_BACKGROUND_REASON_AGENT})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("AGENT reason must be rejected: %v", err)
	}
	h.send(sid, "start")
	start := time.Now()
	var fin *agentv1.ToolCallFinished
	var task *agentv1.TaskStarted
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		switch {
		case ev.GetToolCallStarted() != nil:
			go func() {
				// The command registers right after ToolCallStarted; retry until it is found.
				for i := 0; i < 100; i++ {
					res, err := h.client.BackgroundToolCall(h.ctx, connect.NewRequest(&agentv1.BackgroundToolCallRequest{SessionId: sid, ToolUseId: "b1", Reason: agentv1.BackgroundReason_BACKGROUND_REASON_USER}))
					if err == nil && res.Msg.Backgrounded {
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
			}()
		case ev.GetToolCallFinished() != nil:
			fin = ev.GetToolCallFinished()
		case ev.GetTaskStarted() != nil:
			task = ev.GetTaskStarted()
		}
		return isTurnFinished(ev)
	})
	if time.Since(start) > 10*time.Second || fin == nil || task == nil {
		t.Fatalf("fin %v task %v after %s", fin, task, time.Since(start))
	}
	bg := fin.GetBackground()
	if bg.GetTaskId() == "" || bg.GetTaskId() != task.TaskId || bg.GetReason() != agentv1.BackgroundReason_BACKGROUND_REASON_USER || fin.IsError {
		t.Fatalf("background marker %+v (task %s)", bg, task.TaskId)
	}
	reqs := h.gw.Requests()
	tr := fg.ResultText(fg.LastToolResults(reqs[1])[0])
	if !strings.Contains(tr, bg.TaskId) || !strings.Contains(tr, "The user moved this command") {
		t.Fatalf("tool result %q", tr)
	}
	h.closeSession(sid)

	if _, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid})); err != nil {
		t.Fatal(err)
	}
	var replayed *agentv1.ToolCallFinished
	h.until(h.subscribe(sid), func(ev *agentv1.SessionEvent) bool {
		if f := ev.GetToolCallFinished(); f != nil {
			replayed = f
		}
		return ev.GetSessionUpdated() != nil
	})
	if rb := replayed.GetBackground(); rb.GetTaskId() != bg.TaskId || rb.GetReason() != agentv1.BackgroundReason_BACKGROUND_REASON_USER {
		t.Fatalf("replayed marker %+v", rb)
	}
	h.closeSession(sid)
}
