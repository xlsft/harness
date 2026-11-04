package server_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/internal/gittest"
)

func TestPlanModeAllowsOnlyWhatChangesNothing(t *testing.T) {
	h := startLinkHarness(t, scripted)
	other, _ := h.create(&agentv1.CreateSessionRequest{})
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_PLAN})
	denied := func(tool string, input map[string]any) {
		t.Helper()
		h.send(sid, callText(tool, input))
		fin, _ := h.toolResult(ch)
		h.until(ch, isTurnFinished)
		text := fin.GetContent()[0].GetText().GetText()
		if !fin.GetIsError() || !strings.Contains(text, "Plan mode") {
			t.Fatalf("%s in plan mode: %q", tool, text)
		}
	}
	denied("SessionStart", start("user_asked", "fresh", "planned work"))
	denied("SessionHandoff", map[string]any{"title": "x", "remaining_plan": "y"})
	denied("SessionSend", map[string]any{"to": other, "message": "hi"})
	// A proposal starts nothing, so it stays available; what it starts later runs in the mode of that moment.
	h.send(sid, callText("SessionStart", start("own_initiative", "fresh", "later work")))
	if fin, _ := h.toolResult(ch); fin.GetIsError() {
		t.Fatalf("proposal in plan mode: %v", fin.GetContent())
	}
}

func TestStartedSessionKeepsParentsModeAndCanUseAWorktree(t *testing.T) {
	h := startLinkHarness(t, scripted)
	repo := gittest.Init(t)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: repo, PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	in := start("user_asked", "fresh", "isolated work")
	in["worktree"] = true
	h.send(sid, callText("SessionStart", in))
	fin, res := h.toolResult(ch)
	if fin.GetIsError() {
		t.Fatalf("start: %v", fin.GetContent())
	}
	info := h.info(res["session_id"].(string))
	if info.GetPermissionMode() != agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS {
		t.Fatalf("mode %v", info.GetPermissionMode())
	}
	wt := info.GetWorktree()
	if wt == nil || wt.GetRepositoryPath() != repo || filepath.Dir(wt.GetPath()) != filepath.Join(repo, ".harness", "worktrees") || res["worktree_path"] != wt.GetPath() {
		t.Fatalf("worktree %+v, result %v", wt, res)
	}
	// Both stop before the repository's temporary directory goes.
	h.until(h.subscribe(info.GetId()), isTurnFinished)
	h.closeSession(info.GetId())
	h.closeSession(sid)
}

func TestDetachedSessionNoLongerReports(t *testing.T) {
	h := startLinkHarness(t, scripted)
	parent, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(parent, callText("SessionStart", start("user_asked", "fresh", "first work")))
	_, res := h.toolResult(ch)
	child := res["session_id"].(string)
	h.waitRequest("<session_report>", "answer to: first work")
	h.until(ch, isTurnFinished)

	if _, err := h.client.UpdateSession(h.ctx, connect.NewRequest(&agentv1.UpdateSessionRequest{SessionId: child, DetachFromParent: ptr(true)})); err != nil {
		t.Fatal(err)
	}
	h.send(child, "second work")
	h.waitPrompt("second work")
	time.Sleep(300 * time.Millisecond)
	if n := h.requestsWith("answer to: second work"); n != 0 {
		t.Fatalf("the former parent got the report (%d requests)", n)
	}
}
