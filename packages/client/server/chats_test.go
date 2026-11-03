package server_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/llm"
)

// chatResponder answers "write <path>" with a Write call, "run <command>" with a Bash call and anything else
// (tool results included) with "ok".
func chatResponder(n int, r *llm.Request) fg.Reply {
	last := r.Messages[len(r.Messages)-1]
	lb := last.Content[len(last.Content)-1]
	if lb.Type == llm.BlockText {
		id := "t" + string(rune('a'+n))
		if p, ok := strings.CutPrefix(lb.Text, "write "); ok {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse(id, "Write", map[string]any{"file_path": p, "content": "hello\n"}))}
		}
		if c, ok := strings.CutPrefix(lb.Text, "run "); ok {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse(id, "Bash", map[string]any{"command": c}))}
		}
	}
	return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
}

// turn sends msg and answers every permission request with decision; it returns the tool names asked about
// and the last tool result text.
func (h *harness) turn(sid string, ch <-chan *agentv1.SessionEvent, msg string, decision agentv1.RespondToPermissionRequest_Decision) (asked []string) {
	h.t.Helper()
	h.send(sid, msg)
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			asked = append(asked, p.ToolName)
			h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: p.RequestId, Decision: decision}))
		}
		return isTurnFinished(ev)
	})
	return asked
}

func TestSessionWithoutFolder(t *testing.T) {
	h := startHarness(t, chatResponder, nil)
	// HARNESS_HOME inside a repository: the private directories must still be no repository.
	repo := gittest.Init(t)
	t.Setenv("HARNESS_HOME", filepath.Join(repo, "home"))

	if _, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Worktree: &agentv1.WorktreeOptions{}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("worktree without a folder: %v", err)
	}
	if _, err := os.Stat(config.ChatsDir()); err == nil {
		if entries, _ := os.ReadDir(config.ChatsDir()); len(entries) != 0 {
			t.Fatalf("a refused request left %d directories", len(entries))
		}
	}

	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS}))
	if err != nil {
		t.Fatal(err)
	}
	info := cs.Msg.Session
	sid, dir := info.Id, config.ChatDir(info.Id)
	if !info.NoFolder || info.Cwd != dir || info.GitBranch != "" || info.Repo != "" {
		t.Fatalf("info %+v", info)
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		t.Fatalf("private directory: %v %v", st, err)
	}
	ch := h.subscribe(sid)

	// Writes inside the private directory need no approval; outside it they do.
	inside := filepath.Join(dir, "notes.md")
	if asked := h.turn(sid, ch, "write "+inside, agentv1.RespondToPermissionRequest_DECISION_DENY); len(asked) != 0 {
		t.Fatalf("write inside asked: %v", asked)
	}
	if data, err := os.ReadFile(inside); err != nil || string(data) != "hello\n" {
		t.Fatalf("written: %q %v", data, err)
	}
	outside := filepath.Join(h.proj, "outside.md")
	if asked := h.turn(sid, ch, "write "+outside, agentv1.RespondToPermissionRequest_DECISION_DENY); len(asked) != 1 || asked[0] != "Write" {
		t.Fatalf("write outside asked: %v", asked)
	}
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside file: %v", err)
	}
	// Bash runs in the private directory, which git does not see as part of the repository around it.
	h.turn(sid, ch, "run pwd > where.txt; git rev-parse --show-toplevel > top.txt 2>&1 || echo norepo >> top.txt", agentv1.RespondToPermissionRequest_DECISION_ALLOW)
	if where, _ := os.ReadFile(filepath.Join(dir, "where.txt")); !sameDir(t, strings.TrimSpace(string(where)), dir) {
		t.Fatalf("pwd %q, want %s", where, dir)
	}
	if top, _ := os.ReadFile(filepath.Join(dir, "top.txt")); !strings.Contains(string(top), "norepo") {
		t.Fatalf("git sees a repository: %q", top)
	}

	// Listed with the flag; not offered as a recent folder.
	if l := h.listed("", sid); !l.NoFolder || l.Cwd != dir {
		t.Fatalf("listed live %+v", l)
	}
	folders, err := h.client.ListRecentFolders(h.ctx, connect.NewRequest(&agentv1.ListRecentFoldersRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range folders.Msg.Folders {
		if config.IsChatDir(f.Path) {
			t.Fatalf("recent folder %s", f.Path)
		}
	}
	h.closeSession(sid)
	if l := h.listed("", sid); !l.NoFolder || l.Cwd != dir {
		t.Fatalf("listed closed %+v", l)
	}
	if l := h.listed(dir, sid); !l.NoFolder {
		t.Fatalf("listed by folder %+v", l)
	}

	// Resume without a cwd: back in the private directory.
	rs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if rs.Msg.Session.Id != sid || rs.Msg.Session.Cwd != dir || !rs.Msg.Session.NoFolder {
		t.Fatalf("resumed %+v", rs.Msg.Session)
	}

	// The environment is per-session: keyed by the private directory.
	env, err := h.client.GetEnvironment(h.ctx, connect.NewRequest(&agentv1.GetEnvironmentRequest{SessionId: sid}))
	if err != nil || !sameDir(t, env.Msg.Environment.GetFolder(), dir) {
		t.Fatalf("environment: %v %+v", err, env)
	}
	if _, err := h.client.UpdateEnvironment(h.ctx, connect.NewRequest(&agentv1.UpdateEnvironmentRequest{Environment: &agentv1.Environment{
		Folder: dir, Env: "CHAT=1\n"}})); err != nil {
		t.Fatal(err)
	}

	// A fork asked for in the parent's private directory gets its own, with a copy of the files.
	fs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid, Fork: true, Cwd: dir}))
	if err != nil {
		t.Fatal(err)
	}
	fork := fs.Msg.Session
	if fork.Id == sid || !fork.NoFolder || fork.Cwd != config.ChatDir(fork.Id) || fork.ParentSessionId != sid {
		t.Fatalf("fork %+v", fork)
	}
	if data, err := os.ReadFile(filepath.Join(fork.Cwd, "notes.md")); err != nil || string(data) != "hello\n" {
		t.Fatalf("fork files: %q %v", data, err)
	}

	// Deleting removes the private directory and its environment, and nothing of the fork.
	envFiles := func() int {
		n, _ := filepath.Glob(filepath.Join(config.Home(), "environments", "*.json"))
		return len(n)
	}
	before := envFiles()
	if _, err := h.client.DeleteSession(h.ctx, connect.NewRequest(&agentv1.DeleteSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private directory after delete: %v", err)
	}
	if after := envFiles(); before == 0 || after != before-1 {
		t.Fatalf("environment files %d -> %d", before, after)
	}
	if _, err := os.Stat(filepath.Join(fork.Cwd, "notes.md")); err != nil {
		t.Fatalf("fork files after the parent's delete: %v", err)
	}
	if _, ok := h.list(true)[sid]; ok {
		t.Fatal("deleted session listed")
	}
}

// sameDir compares directories with symlinks resolved (macOS temp directories live under /private).
func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}
