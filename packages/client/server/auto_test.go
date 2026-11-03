package server_test

import (
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	decisionv1 "github.com/xlsft/harness/packages/client/gen/harness/decision/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/server"
)

func TestAutoModeOverAgentService(t *testing.T) {
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		last := r.Messages[len(r.Messages)-1]
		lb := last.Content[len(last.Content)-1]
		if lb.Type == llm.BlockText && strings.HasPrefix(lb.Text, "run ") {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("b"+string(rune('a'+n)), "Bash", map[string]any{"description": "test", "command": strings.TrimPrefix(lb.Text, "run ")}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	}, nil)
	h.gw.SetDecide(func(r *decisionv1.DecideRequest) *decisionv1.DecideResponse {
		return &decisionv1.DecideResponse{Decision: decisionv1.Decision_DECISION_ASK, Reason: "Not sure the generator is needed.", Provider: "llm"}
	})
	sid, ch := h.create(&agentv1.CreateSessionRequest{})

	// SetPermissionMode(AUTO) is accepted and announced.
	if _, err := h.client.SetPermissionMode(h.ctx, connect.NewRequest(&agentv1.SetPermissionModeRequest{SessionId: sid, Mode: agentv1.PermissionMode_PERMISSION_MODE_AUTO})); err != nil {
		t.Fatal(err)
	}
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		return ev.GetPermissionModeChanged().GetMode() == agentv1.PermissionMode_PERMISSION_MODE_AUTO
	})

	notice := func(ev *agentv1.SessionEvent) *server.AutoDecisionNotice {
		n := ev.GetNotice()
		if n == nil || n.Kind != "auto_decision" {
			return nil
		}
		var ad server.AutoDecisionNotice
		if err := json.Unmarshal([]byte(n.Text), &ad); err != nil {
			t.Fatalf("notice text is not JSON: %q", n.Text)
		}
		return &ad
	}

	// A safe command is auto-approved by the local rules: notice, no prompt.
	h.send(sid, "run go version")
	var allowed *server.AutoDecisionNotice
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if ev.GetPermissionRequest() != nil {
			t.Fatal("auto mode must not prompt for a fast-allowed command")
		}
		if ad := notice(ev); ad != nil {
			allowed = ad
		}
		return isTurnFinished(ev)
	})
	if allowed == nil || allowed.Decision != "allow" || allowed.Provider != "local-rules" || allowed.ToolName != "Bash" ||
		!strings.HasPrefix(allowed.Text, "Auto-approved by local-rules:") {
		t.Fatalf("allow notice %+v", allowed)
	}

	// An ambiguous one goes to the server; its ASK becomes a prompt carrying the reason.
	h.send(sid, "run ./gen.sh")
	var pr *agentv1.PermissionRequest
	var deferred *server.AutoDecisionNotice
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			pr = p
			h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: p.RequestId,
				Decision: agentv1.RespondToPermissionRequest_DECISION_DENY, Comment: "not now"}))
		}
		if ad := notice(ev); ad != nil {
			deferred = ad
		}
		return isTurnFinished(ev)
	})
	if pr == nil || !strings.Contains(pr.Reason, "Auto mode could not approve this action (llm): Not sure the generator is needed.") {
		t.Fatalf("permission request %+v", pr)
	}
	if deferred == nil || deferred.Decision != "ask" || deferred.Provider != "llm" {
		t.Fatalf("ask notice %+v", deferred)
	}
	if n := len(h.gw.DecideRequests()); n != 1 {
		t.Fatalf("%d Decide calls, want 1", n)
	}
}
