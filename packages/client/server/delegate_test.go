package server_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
)

// /delegate has the agent write a brief and start a lead session (SessionStart with lead), which runs the helpers;
// the session that delegated follows and steers the lead's run.
func TestDelegateStartsALeadSession(t *testing.T) {
	h := startHarnessGW(t, func(_ int, r *llm.Request) fg.Reply {
		text := projText(r)
		if lb := lastBlock(r); lb.Type == llm.BlockToolResult {
			return fg.Reply{Events: fg.Message("m", fg.Text("handed off"))}
		}
		if strings.Contains(text, "call SessionStart with lead: true") && !strings.Contains(text, "BRIEF") {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("st", "SessionStart", map[string]any{"title": "API work", "prompt": "BRIEF: keep the API stable",
				"initiation": "user_asked", "context": "fresh", "worktree": false, "lead": true, "max_helpers": 2, "budget_usd": 5}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	}, func(gw *fg.Server) {
		projectTools(gw)
		gw.Bundle.Tools["SessionStart"] = prompts.ToolPrompt{Description: "SessionStart", InputSchema: json.RawMessage(`{"type":"object"}`)}
	}, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: h.proj})
	if s := slashText(h, ch, sid, "/delegate status").GetText(); !strings.Contains(s, "Nothing is delegated") {
		t.Fatalf("status before: %q", s)
	}
	if s := slashText(h, ch, sid, "/delegate --workers 2 --budget 5 keep the API stable").GetText(); !strings.Contains(s, "lead session") {
		t.Fatalf("start: %q", s)
	}
	var run *agentv1.ProjectRun
	deadline := time.Now().Add(15 * time.Second)
	for run == nil && time.Now().Before(deadline) {
		list, err := h.client.ListProjectRuns(h.ctx, connect.NewRequest(&agentv1.ListProjectRunsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range list.Msg.GetRuns() {
			run = r
		}
		time.Sleep(30 * time.Millisecond)
	}
	if run == nil || run.GetCoordinatorSessionId() == sid || run.GetLimits().GetMaxWorkers() != 2 || run.GetLimits().GetBudgetUsd() != 5 || run.GetGoal() != "BRIEF: keep the API stable" {
		t.Fatalf("run: %+v", run)
	}
	brief := ""
	for _, r := range h.gw.Requests() {
		if txt := projText(r); strings.Contains(txt, "keep the API stable") && strings.Contains(txt, "call SessionStart with lead: true") {
			brief = txt
		}
	}
	if !strings.Contains(brief, "max_helpers 2, budget_usd 5") || !strings.Contains(brief, "Delegate: keep the API stable") {
		t.Fatalf("delegating turn: %s", brief)
	}
	if s := slashText(h, ch, sid, "/delegate status").GetText(); !strings.Contains(s, "$0.00 of $5.00") {
		t.Fatalf("status: %q", s)
	}
	if s := slashText(h, ch, sid, "/delegate more").GetText(); !strings.Contains(s, "already delegated") {
		t.Fatalf("second: %q", s)
	}
	if s := slashText(h, ch, sid, "/delegate stop").GetText(); !strings.Contains(s, "Stopped") {
		t.Fatalf("stop: %q", s)
	}
	h.waitRun(run.GetId(), "stopped", func(r *agentv1.ProjectRun) bool {
		return r.GetState() == agentv1.ProjectRunState_PROJECT_RUN_STATE_STOPPED
	})
}
