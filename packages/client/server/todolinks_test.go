package server_test

import (
	"slices"
	"sync/atomic"
	"testing"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
)

// The ids reach a frontend on TodosUpdated, live and again when the session is resumed.
func TestTodoLinksOverEventsAndResume(t *testing.T) {
	var main atomic.Int32
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		if isAgentRequest(r) {
			return fg.Reply{Events: fg.Message("c", fg.Text("agent done"))}
		}
		switch main.Add(1) {
		case 1:
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("t1", "TodoWrite", map[string]any{"todos": []any{
				map[string]any{"content": "Work", "activeForm": "Working", "status": "in_progress"},
				map[string]any{"content": "Later", "activeForm": "Waiting", "status": "pending"},
			}}))}
		case 2:
			return fg.Reply{Events: spawnCall("a1", "help", nil)}
		case 3:
			return fg.Reply{Events: fg.Message("m3", fg.ToolUse("b1", "Bash", map[string]any{"command": "echo hi", "description": "bg", "run_in_background": true}))}
		}
		return fg.Reply{Events: fg.Message("end", fg.Text("ok"))}
	}, func(o *bootstrap.Options) { o.AllowedTools = []string{"Bash(echo *)"} })
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")

	linked := func(ev *agentv1.SessionEvent) *agentv1.Todo {
		if td := ev.GetTodosUpdated(); td != nil && len(td.Todos) == 2 && len(td.Todos[0].AgentIds) == 1 && len(td.Todos[0].TaskIds) == 1 {
			return td.Todos[0]
		}
		return nil
	}
	var live *agentv1.Todo
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if l := linked(ev); l != nil {
			live = l
		}
		return live != nil
	})
	h.closeSession(sid)

	_, resumed := h.create(&agentv1.CreateSessionRequest{ResumeSessionId: sid})
	var back *agentv1.Todo
	h.until(resumed, func(ev *agentv1.SessionEvent) bool {
		if l := linked(ev); l != nil {
			back = l
		}
		return back != nil
	})
	if !slices.Equal(back.AgentIds, live.AgentIds) || !slices.Equal(back.TaskIds, live.TaskIds) {
		t.Fatalf("resumed %+v, live %+v", back, live)
	}
}
