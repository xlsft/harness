//go:build unix

package server_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
)

func TestSessionFiles(t *testing.T) {
	h := startHarness(t, fg.Sequence(), nil)
	p := func(rel string) string { return filepath.Join(h.proj, rel) }
	files := map[string]string{
		".gitignore":        "build/\n*.log\n",
		"README.md":         "# Project\n",
		"src/main.go":       "package main\n",
		"src/old.go":        "package main\n",
		"build/out.bin":     "x",
		"debug.log":         "log\n",
		"node_modules/x.js": "x",
	}
	for rel, content := range files {
		if err := os.MkdirAll(filepath.Dir(p(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p(rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", h.proj, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".gitignore", "README.md", "src")
	git("commit", "-q", "-m", "init")
	// A modification, a deletion, an untracked file and an untracked directory.
	if err := os.WriteFile(p("README.md"), []byte("# Project\n\nmore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p("src/old.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p("src/new.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p("scratch/deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p("scratch/deep/a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sid, _ := h.create(&agentv1.CreateSessionRequest{})

	roots, err := h.client.ListFileRoots(h.ctx, connect.NewRequest(&agentv1.ListFileRootsRequest{SessionId: sid}))
	if err != nil || len(roots.Msg.Roots) == 0 || roots.Msg.Roots[0].Path != h.proj || roots.Msg.Roots[0].GitRoot != h.proj {
		t.Fatalf("roots: %v %+v", err, roots)
	}

	list := func(path string, showIgnored bool) map[string]*agentv1.FileEntry {
		t.Helper()
		resp, err := h.client.ListFiles(h.ctx, connect.NewRequest(&agentv1.ListFilesRequest{SessionId: sid, Path: path, ShowIgnored: showIgnored}))
		if err != nil {
			t.Fatalf("list %s: %v", path, err)
		}
		out := map[string]*agentv1.FileEntry{}
		for _, e := range resp.Msg.Entries {
			out[e.Name] = e
		}
		return out
	}
	top := list("", false)
	for _, hidden := range []string{".git", "node_modules", "build", "debug.log"} {
		if top[hidden] != nil {
			t.Errorf("%s should be hidden", hidden)
		}
	}
	if e := top["README.md"]; e == nil || e.GitStatus != agentv1.GitFileStatus_GIT_FILE_STATUS_MODIFIED || e.Kind != agentv1.FileKind_FILE_KIND_FILE || e.Size == 0 || e.Path != p("README.md") {
		t.Errorf("README.md: %+v", e)
	}
	if e := top["src"]; e == nil || e.Kind != agentv1.FileKind_FILE_KIND_DIRECTORY {
		t.Errorf("src: %+v", e)
	}
	if e := top["scratch"]; e == nil || e.GitStatus != agentv1.GitFileStatus_GIT_FILE_STATUS_UNTRACKED {
		t.Errorf("scratch: %+v", e)
	}
	all := list(h.proj, true)
	for _, n := range []string{".git", "node_modules", "build", "debug.log"} {
		if e := all[n]; e == nil || !e.Ignored {
			t.Errorf("%s with show_ignored: %+v", n, e)
		}
	}
	if all["README.md"].Ignored {
		t.Error("README.md is not ignored")
	}
	src := list("src", false)
	if e := src["old.go"]; e == nil || !e.Deleted || e.GitStatus != agentv1.GitFileStatus_GIT_FILE_STATUS_DELETED {
		t.Errorf("deleted old.go: %+v", e)
	}
	if e := src["new.go"]; e == nil || e.GitStatus != agentv1.GitFileStatus_GIT_FILE_STATUS_UNTRACKED {
		t.Errorf("new.go: %+v", e)
	}
	if e := list("scratch/deep", false)["a.txt"]; e == nil || e.GitStatus != agentv1.GitFileStatus_GIT_FILE_STATUS_UNTRACKED {
		t.Errorf("file in an untracked directory: %+v", e)
	}

	st, err := h.client.GetGitStatus(h.ctx, connect.NewRequest(&agentv1.GetGitStatusRequest{SessionId: sid}))
	if err != nil || st.Msg.Root != h.proj {
		t.Fatalf("git status: %v %+v", err, st)
	}
	want := map[string]agentv1.GitFileStatus{
		p("README.md"):  agentv1.GitFileStatus_GIT_FILE_STATUS_MODIFIED,
		p("src/old.go"): agentv1.GitFileStatus_GIT_FILE_STATUS_DELETED,
		p("src/new.go"): agentv1.GitFileStatus_GIT_FILE_STATUS_UNTRACKED,
		p("scratch"):    agentv1.GitFileStatus_GIT_FILE_STATUS_UNTRACKED,
	}
	if len(st.Msg.Files) != len(want) {
		t.Fatalf("git status files: %v", st.Msg.Files)
	}
	for k, v := range want {
		if st.Msg.Files[k] != v {
			t.Errorf("status %s = %v; want %v", k, st.Msg.Files[k], v)
		}
	}

	rf, err := h.client.ReadFile(h.ctx, connect.NewRequest(&agentv1.ReadFileRequest{SessionId: sid, Path: "README.md"}))
	if err != nil || rf.Msg.Text != "# Project\n\nmore\n" || rf.Msg.Mime != "text/markdown" || rf.Msg.IsBinary {
		t.Fatalf("read: %v %+v", err, rf)
	}

	outside, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), p("link")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(outside, "secret"), "../" + filepath.Base(outside), "link", "src/../../x"} {
		if _, err := h.client.ReadFile(h.ctx, connect.NewRequest(&agentv1.ReadFileRequest{SessionId: sid, Path: path})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("read %s: %v; want permission denied", path, err)
		}
	}
	if _, err := h.client.ListFiles(h.ctx, connect.NewRequest(&agentv1.ListFilesRequest{SessionId: sid, Path: outside})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("list outside: %v", err)
	}
	if e := list("", false)["link"]; e == nil || e.Kind != agentv1.FileKind_FILE_KIND_SYMLINK || !e.Symlink {
		t.Errorf("a symlink to outside is listed as a symlink: %+v", e)
	}
	if _, err := h.client.ListFiles(h.ctx, connect.NewRequest(&agentv1.ListFilesRequest{SessionId: "00000000-0000-0000-0000-000000000000"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown session: %v", err)
	}
}

// The pane's list and the counts of SessionInfo (the details pane, the sidebar) come from one measurement, for a
// session without a folder as well.
func TestSessionDiffAgreesWithDiffStats(t *testing.T) {
	h := startHarness(t, fg.Sequence(), nil)
	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	info := cs.Msg.Session
	if !info.NoFolder {
		t.Fatalf("expected a session without a folder: %+v", info)
	}
	sid := info.Id
	ch := h.subscribe(sid)
	if err := os.WriteFile(filepath.Join(info.Cwd, "made-by-bash.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := h.client.GetSessionDiff(h.ctx, connect.NewRequest(&agentv1.GetSessionDiffRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Msg.Files) != 1 || d.Msg.Files[0].DisplayPath != "made-by-bash.txt" || d.Msg.Additions != 2 || d.Msg.FilesChanged != 1 {
		t.Fatalf("diff: %+v", d.Msg)
	}
	up := h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetSessionUpdated().GetDiffStats().GetAdditions() == 2 })
	if ds := up[len(up)-1].GetSessionUpdated().DiffStats; ds.FilesChanged != 1 || ds.Deletions != 0 {
		t.Fatalf("diff stats: %+v", ds)
	}

	same, err := h.client.GetSessionDiff(h.ctx, connect.NewRequest(&agentv1.GetSessionDiffRequest{SessionId: sid, IfNoneMatch: d.Msg.Version}))
	if err != nil {
		t.Fatal(err)
	}
	if !same.Msg.NotModified || len(same.Msg.Files) != 0 || same.Msg.Version != d.Msg.Version || same.Msg.Additions != 2 {
		t.Fatalf("unchanged: %+v", same.Msg)
	}

	if _, err := h.client.GetSessionDiff(h.ctx, connect.NewRequest(&agentv1.GetSessionDiffRequest{SessionId: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}
}
