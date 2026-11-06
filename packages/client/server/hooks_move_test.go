package server_test

import (
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
)

// A session that moves runs the hooks of the folder it moved to, and says it moved.
func TestHooksFollowTheSessionsFolder(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	moved := filepath.Join(dir, "moved.json")
	var sub string
	h := startHooked(t, fg.Sequence(
		func(r *llm.Request) fg.Reply {
			sub = filepath.Join(fg.Cwd(r), "sub")
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("c", "ChangeDirectory", map[string]any{"path": sub}))}
		},
		call("b", "Bash", map[string]any{"command": "echo hi", "description": "x"}),
		say("done"),
	), hookSettings(t, nil, hookCfg{event: "PreToolUse", command: "echo here >> '" + log + "'"}))
	writeProjectFile(t, h.proj, "sub/.claude/settings.json", hookSettings(t, nil,
		hookCfg{event: "PreToolUse", command: "echo there >> '" + log + "'"},
		hookCfg{event: "CwdChanged", command: "cat > '" + moved + "'"}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "go")
	var done, finished bool
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if r := ev.GetHookRun(); r != nil && r.Event == "CwdChanged" && r.Status != agentv1.HookRun_STATUS_RUNNING {
			done = true
		}
		finished = finished || isTurnFinished(ev)
		return done && finished
	})
	if got := readFile(t, log); got != "here\nthere\n" {
		t.Fatalf("log: %q; results %v", got, results(h))
	}
	if got := readFile(t, moved); !strings.Contains(got, `"new_cwd":"`+sub+`"`) {
		t.Fatalf("stdin: %s", got)
	}
}
