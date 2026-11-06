//go:build darwin

package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
)

// The whole way, in the mode that decides least: bypass neither turns the sandbox off nor lets the model out of it. A
// session is sandboxed by default, a refusal reaches the model with its cause, and running outside takes the user.
func TestBypassModeStaysInTheSandbox(t *testing.T) {
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("no sandbox-exec")
	}
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("HOME", t.TempDir())
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	target := filepath.Join(outside, "written")
	bash := func(id string, extra map[string]any) fg.BlockSpec {
		in := map[string]any{"command": "touch " + target, "description": "a file"}
		for k, v := range extra {
			in[k] = v
		}
		return fg.ToolUse(id, "Bash", in)
	}
	h := startHarness(t, byMessage(map[string]fg.BlockSpec{
		"run-inside":  bash("toolu_in", nil),
		"run-outside": bash("toolu_out", map[string]any{"run_outside_sandbox": true}),
	}), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})

	// No prompt: bypass answers that. Refused all the same.
	if asked := h.turn(sid, ch, "run-inside", agentv1.RespondToPermissionRequest_DECISION_DENY); len(asked) != 0 {
		t.Fatalf("asked: %v", asked)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("bypass mode wrote outside the sandbox")
	}
	if !anyRequestHolds(h, "[sandbox]") || !anyRequestHolds(h, "run_outside_sandbox") {
		t.Fatal("the model was not told what the sandbox refused")
	}

	// Asking to leave goes to the user, in bypass too, and no "always allow" is on offer.
	h.send(sid, "run-outside")
	var prompt *agentv1.PermissionRequest
	var result string
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			prompt = p
			h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: p.RequestId, Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW}))
		}
		if e := ev.GetToolCallFinished(); e != nil && e.GetToolUseId() == "toolu_out" {
			result = e.GetStructuredResultJson()
		}
		return isTurnFinished(ev)
	})
	if prompt == nil || !strings.Contains(prompt.GetReason(), "outside the sandbox") || len(prompt.SuggestedRules) != 0 {
		t.Fatalf("prompt: %+v", prompt)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("approved, but not written:", err)
	}
	if !strings.Contains(result, `"sandbox":"outside"`) {
		t.Fatalf("result: %s", result)
	}
}
