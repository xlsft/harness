//go:build unix

package server_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
)

// copyHooks are fake VCS hooks for a folder outside git: WorktreeCreate copies the source into copies/<name>, WorktreeRemove
// deletes the copy, VcsStatus lists new.txt when it exists. Each records its input as copies/<event>.json.
func copyHooks(t *testing.T, copies string) []hookCfg {
	t.Helper()
	dir := t.TempDir()
	field := func(name string) string {
		return `$(printf '%s' "$in" | sed -n 's/.*"` + name + `":"\([^"]*\)".*/\1/p')`
	}
	create := script(t, dir, "create.sh", `in=$(cat); printf '%s' "$in" > '`+copies+`/create.json'
name=`+field("name")+`; src=`+field("source_dir")+`
mkdir -p '`+copies+`'/"$name" && cp -R "$src/." '`+copies+`'/"$name/"
printf '{"hookSpecificOutput":{"hookEventName":"WorktreeCreate","worktreePath":"%s","baseRevision":"r1"}}' '`+copies+`'/"$name"`)
	remove := script(t, dir, "remove.sh", `in=$(cat); printf '%s' "$in" > '`+copies+`/remove.json'; rm -rf "`+field("worktree_path")+`"`)
	status := script(t, dir, "status.sh", `cat > /dev/null
if [ -f new.txt ]; then printf '{"base":"r1","files":[{"path":"new.txt","status":"added","additions":1,"deletions":0}]}'; else printf '{"base":"r1","files":[]}'; fi`)
	diff := script(t, dir, "diff.sh", `cat > /dev/null; printf 'diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+work\n'`)
	return []hookCfg{{event: "WorktreeCreate", command: create}, {event: "WorktreeRemove", command: remove}, {event: "VcsStatus", command: status},
		{event: "VcsDiff", command: diff}}
}

func readJSON(t *testing.T, p string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(readFile(t, p)), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWorktreeHooksReplaceGitInTheTools(t *testing.T) {
	copies := t.TempDir()
	feat, feat2 := filepath.Join(copies, "feat"), filepath.Join(copies, "feat2")
	h := startHooked(t, fg.Sequence(
		call("enter", "EnterWorktree", map[string]any{"name": "feat"}),
		call("inside", "Bash", map[string]any{"command": "pwd", "description": "working directory"}),
		call("leave", "ExitWorktree", map[string]any{"action": "remove"}),
		call("enter2", "EnterWorktree", map[string]any{"name": "feat2"}),
		call("write", "Write", map[string]any{"file_path": filepath.Join(feat2, "new.txt"), "content": "work\n"}),
		call("refused", "ExitWorktree", map[string]any{"action": "remove"}),
		call("keep", "ExitWorktree", map[string]any{"action": "keep"}),
		say("done"),
	), nil)
	writeProjectFile(t, h.proj, ".harness/settings.json", hookSettings(t, nil, copyHooks(t, copies)...))
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: h.proj, PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	h.send(sid, "go")
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if r := ev.GetPermissionRequest(); r != nil {
			t.Fatalf("unexpected permission request for %s", r.ToolName)
		}
		return isTurnFinished(ev)
	})
	res := results(h)
	if !strings.Contains(res["enter"], feat) || !strings.Contains(res["enter"], "WorktreeCreate hook") {
		t.Fatalf("enter: %s", res["enter"])
	}
	if !strings.Contains(res["inside"], "feat") {
		t.Errorf("the shell should be in the copy: %s", res["inside"])
	}
	in := readJSON(t, filepath.Join(copies, "create.json"))
	if in["session_id"] != sid || in["worktree_kind"] != "session" {
		t.Errorf("create input %v", in)
	}
	if !strings.Contains(res["leave"], "removed") {
		t.Errorf("leave: %s", res["leave"])
	}
	if _, err := os.Stat(feat); !os.IsNotExist(err) {
		t.Errorf("the WorktreeRemove hook should have removed %s: %v", feat, err)
	}
	if rm := readJSON(t, filepath.Join(copies, "remove.json")); rm["force"] != true || rm["name"] != "feat2" && rm["name"] != "feat" {
		t.Errorf("remove input %v", rm)
	}
	if !strings.Contains(res["refused"], "1 changed file") || !strings.Contains(res["refused"], "new.txt") {
		t.Errorf("a copy with changes should not go without discard_changes: %s", res["refused"])
	}
	if _, err := os.Stat(filepath.Join(feat2, "new.txt")); err != nil {
		t.Errorf("kept copy: %v", err)
	}
}

func TestCreateSessionInAHookWorktree(t *testing.T) {
	copies := t.TempDir()
	h := startHooked(t, fg.Sequence(say("hi")), nil)
	writeProjectFile(t, h.proj, ".harness/settings.json", hookSettings(t, nil, copyHooks(t, copies)...))
	resp, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, Worktree: &agentv1.WorktreeOptions{Name: "s1"}}))
	if err != nil {
		t.Fatal(err)
	}
	info := resp.Msg.Session
	copyDir := filepath.Join(copies, "s1")
	if w := info.GetWorktree(); w == nil || w.Path != copyDir || w.BaseRef != "r1" || w.Branch != "" {
		t.Fatalf("worktree %+v", w)
	}
	if in := readJSON(t, filepath.Join(copies, "create.json")); in["session_id"] != info.Id || in["source_dir"] != h.proj {
		t.Errorf("create input %v", in)
	}

	// The Changes pane of the copy asks the hooks.
	writeProjectFile(t, copyDir, "new.txt", []byte("work\n"))
	diff, err := h.client.GetSessionDiff(h.ctx, connect.NewRequest(&agentv1.GetSessionDiffRequest{SessionId: info.Id}))
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Msg.Files) != 1 || diff.Msg.Files[0].Source != agentv1.FileDiffSource_FILE_DIFF_SOURCE_VCS || diff.Msg.BaseLabel != "r1" ||
		len(diff.Msg.Files[0].Hunks) != 1 {
		t.Fatalf("diff %+v", diff.Msg)
	}
}

func TestCreateSessionShowsAFailingWorktreeHook(t *testing.T) {
	h := startHooked(t, fg.Sequence(say("hi")), nil)
	bad := script(t, t.TempDir(), "bad.sh", "echo 'jj: no such revision' >&2; exit 1")
	writeProjectFile(t, h.proj, ".harness/settings.json", hookSettings(t, nil, hookCfg{event: "WorktreeCreate", command: bad}))
	_, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, Worktree: &agentv1.WorktreeOptions{}}))
	if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "no such revision") {
		t.Fatalf("error %v", err)
	}
	// Starting in place is the user's next step, not something the client does by itself.
	if list := h.list(false); len(list) != 0 {
		t.Fatalf("no session should have opened: %v", list)
	}
}

func TestUntrustedFolderKeepsItsWorktreeHooksOff(t *testing.T) {
	h := startHookedTrust(t, fg.Sequence(say("hi")), nil, false)
	marker := filepath.Join(t.TempDir(), "ran")
	writeProjectFile(t, h.proj, ".harness/settings.json", hookSettings(t, nil, hookCfg{event: "WorktreeCreate", command: "touch '" + marker + "'"}))
	_, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, Worktree: &agentv1.WorktreeOptions{}}))
	if err == nil || !strings.Contains(err.Error(), "git") {
		t.Fatalf("an untrusted folder outside git gets no worktree at all: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the hook of an untrusted folder ran")
	}
}

func TestVcsWithoutHooksIsPointedOutOnce(t *testing.T) {
	h := startHarness(t, fg.Sequence(say("hi"), say("hi")), nil)
	if err := os.Mkdir(filepath.Join(h.proj, ".hg"), 0o755); err != nil {
		t.Fatal(err)
	}
	suggested := func() *agentv1.VcsHooksSuggested {
		sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: h.proj})
		h.send(sid, "hi")
		var got *agentv1.VcsHooksSuggested
		h.until(ch, func(ev *agentv1.SessionEvent) bool {
			if s := ev.GetVcsHooksSuggested(); s != nil {
				got = s
			}
			return isTurnFinished(ev)
		})
		h.closeSession(sid)
		return got
	}
	first := suggested()
	if first == nil || first.Vcs != "mercurial" || first.Root != h.proj {
		t.Fatalf("first session: %+v", first)
	}
	if again := suggested(); again != nil {
		t.Fatalf("the notice should come once per folder: %+v", again)
	}
}

// An isolated agent gets a working copy from the WorktreeCreate hook; one that changed nothing (VcsStatus lists nothing)
// goes through WorktreeRemove when the session closes.
func TestSubagentIsolationThroughHooks(t *testing.T) {
	copies := t.TempDir()
	var cwd atomic.Value
	trusted := true
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		if isAgentRequest(r) {
			if hasToolResult(r) {
				cwd.Store(fg.ResultText(fg.LastToolResults(r)[0]))
				return fg.Reply{Events: fg.Message("c2", fg.Text("done"))}
			}
			return fg.Reply{Events: fg.Message("c1", fg.ToolUse("cb", "Bash", map[string]any{"command": "pwd", "description": "x"}))}
		}
		if hasToolResult(r) || strings.Contains(lastUserText(r), "<agent_report>") {
			return fg.Reply{Events: fg.Message("p", fg.Text("ok"))}
		}
		return fg.Reply{Events: spawnCall("a1", "in a copy", map[string]any{"isolation": "worktree"})}
	}, func(o *bootstrap.Options) { o.PermissionMode = permissions.ModeBypassPermissions; o.Trusted = &trusted })
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HARNESS_NO_HOOKS", "")
	writeProjectFile(t, h.proj, ".harness/settings.json", hookSettings(t, nil, copyHooks(t, copies)...))
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: h.proj})
	h.send(sid, "go")
	info := waitAgentState(h, ch, agentv1.SubagentState_SUBAGENT_STATE_FINISHED)
	if !strings.HasPrefix(info.WorktreePath, copies) || info.WorktreeBranch != "" {
		t.Fatalf("agent %+v", info)
	}
	if got, _ := cwd.Load().(string); !strings.Contains(got, info.WorktreePath) {
		t.Fatalf("pwd %q, copy %s", got, info.WorktreePath)
	}
	if in := readJSON(t, filepath.Join(copies, "create.json")); in["worktree_kind"] != "agent" || in["agent_id"] == "" {
		t.Errorf("create input %v", in)
	}
	h.closeSession(sid)
	if _, err := os.Stat(info.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("an untouched copy should go: %v", err)
	}
	if rm := readJSON(t, filepath.Join(copies, "remove.json")); rm["force"] != false {
		t.Errorf("remove input %v", rm)
	}
}
