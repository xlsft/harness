package server_test

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
)

// /delegate makes the session that agreed on the work lead the helpers, with the conversation as the brief.
func TestDelegateLeadsFromTheConversation(t *testing.T) {
	h := startHarnessGW(t, func(int, *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("ok"))} }, projectTools, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: h.proj})
	if s := slashText(h, ch, sid, "/delegate status").GetText(); !strings.Contains(s, "Nothing is delegated") {
		t.Fatalf("status before: %q", s)
	}
	if s := slashText(h, ch, sid, "/delegate --workers 2 --budget 5 keep the API stable").GetText(); !strings.Contains(s, "Delegating") {
		t.Fatalf("start: %q", s)
	}
	list, err := h.client.ListProjectRuns(h.ctx, connect.NewRequest(&agentv1.ListProjectRunsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var run *agentv1.ProjectRun
	for _, r := range list.Msg.GetRuns() {
		if r.GetCoordinatorSessionId() == sid {
			run = r
		}
	}
	if run == nil || run.GetLimits().GetMaxWorkers() != 2 || run.GetLimits().GetBudgetUsd() != 5 {
		t.Fatalf("run: %+v", run)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(h.gw.Requests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(h.gw.Requests()) == 0 {
		t.Fatal("no turn after /delegate")
	}
	brief := allRequestText(h.gw.Requests()[0])
	if !strings.Contains(brief, "You now lead run "+run.GetId()) || !strings.Contains(brief, "keep the API stable") || !strings.Contains(brief, "Delegate: keep the API stable") {
		t.Fatalf("brief: %s", brief)
	}
	if s := slashText(h, ch, sid, "/delegate more").GetText(); !strings.Contains(s, "already delegated") {
		t.Fatalf("second: %q", s)
	}
	if s := slashText(h, ch, sid, "/delegate stop").GetText(); !strings.Contains(s, "Stopped") {
		t.Fatalf("stop: %q", s)
	}
	h.waitRun(run.GetId(), "stopped", func(r *agentv1.ProjectRun) bool { return r.GetState() == agentv1.ProjectRunState_PROJECT_RUN_STATE_STOPPED })
}
