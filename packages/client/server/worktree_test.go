package server_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/session"
)

func connectCode(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return connect.CodeUnknown
}

func TestCreateSessionInWorktree(t *testing.T) {
	h := startHarness(t, nil, nil)
	repo := gittest.Init(t)
	sub := filepath.Join(repo, "src", "app")

	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: sub, Worktree: &agentv1.WorktreeOptions{}}))
	if err != nil {
		t.Fatal(err)
	}
	info := cs.Msg.Session
	wt := info.GetWorktree()
	if wt == nil || wt.RepositoryPath != repo || wt.BaseRef != "main" || !strings.HasPrefix(wt.Branch, "harness/") ||
		filepath.Dir(wt.Path) != filepath.Join(repo, ".harness", "worktrees") || wt.Branch != "harness/"+filepath.Base(wt.Path) {
		t.Fatalf("worktree %+v", wt)
	}
	if info.Cwd != filepath.Join(wt.Path, "src", "app") || info.GitBranch != wt.Branch {
		t.Fatalf("cwd %q branch %q", info.Cwd, info.GitBranch)
	}
	data, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if !strings.Contains(string(data), "/.harness/worktrees/") {
		t.Fatalf("exclude:\n%s", data)
	}

	// ListSessions reports it while open and, from the transcript, after close.
	check := func(stage string) {
		t.Helper()
		ls, err := h.client.ListSessions(h.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{Cwd: info.Cwd}))
		if err != nil || len(ls.Msg.Sessions) != 1 || ls.Msg.Sessions[0].GetWorktree().GetPath() != wt.Path ||
			ls.Msg.Sessions[0].GetWorktree().GetBaseRef() != "main" {
			t.Fatalf("%s: list %v %+v", stage, err, ls)
		}
	}
	check("open")
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: info.Id})); err != nil {
		t.Fatal(err)
	}
	check("closed")
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("worktree removed on close: %v", err)
	}

	// Resume reports the recorded worktree.
	rs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: info.Cwd, ResumeSessionId: info.Id}))
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Msg.Session.GetWorktree(); got.GetPath() != wt.Path || got.GetBranch() != wt.Branch || got.GetRepositoryPath() != repo {
		t.Fatalf("resumed worktree %+v", got)
	}
	// The transcript holds one worktree record (not repeated on resume).
	entries, _ := session.Load(session.Store{Root: config.ProjectsDir()}.Path(info.Cwd, info.Id))
	n := 0
	for _, e := range entries {
		if e.Worktree != nil {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d worktree records", n)
	}

	// A fork can move into a new worktree.
	fk, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: sub, ResumeSessionId: info.Id, Fork: true,
		Worktree: &agentv1.WorktreeOptions{Name: "forked"}}))
	if err != nil {
		t.Fatal(err)
	}
	if got := fk.Msg.Session; got.GetWorktree().GetPath() != filepath.Join(repo, ".harness", "worktrees", "forked") ||
		got.Cwd != filepath.Join(got.GetWorktree().GetPath(), "src", "app") || got.ParentSessionId != info.Id {
		t.Fatalf("fork %+v", got)
	}

	// The recent folder is the repository subdirectory, not the worktree.
	rf, err := h.client.ListRecentFolders(h.ctx, connect.NewRequest(&agentv1.ListRecentFoldersRequest{}))
	if err != nil || len(rf.Msg.Folders) != 1 || rf.Msg.Folders[0].Path != sub || !rf.Msg.Folders[0].Exists || rf.Msg.Folders[0].GitBranch != "main" {
		t.Fatalf("recent %v %+v", err, rf)
	}
}

func TestCreateSessionWorktreeCustomAndErrors(t *testing.T) {
	h := startHarness(t, nil, nil)
	repo := gittest.Init(t)
	gittest.Run(t, repo, "branch", "existing")

	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: repo,
		Worktree: &agentv1.WorktreeOptions{Name: "review", Branch: "existing"}}))
	if err != nil {
		t.Fatal(err)
	}
	if wt := cs.Msg.Session.GetWorktree(); wt.Path != filepath.Join(repo, ".harness", "worktrees", "review") || wt.Branch != "existing" ||
		cs.Msg.Session.Cwd != wt.Path {
		t.Fatalf("worktree %+v cwd %s", wt, cs.Msg.Session.Cwd)
	}
	cs2, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: repo,
		Worktree: &agentv1.WorktreeOptions{Name: "review", Branch: "feature/x", BaseRef: "main"}}))
	if err != nil {
		t.Fatal(err)
	}
	if wt := cs2.Msg.Session.GetWorktree(); filepath.Base(wt.Path) != "review-2" || wt.Branch != "feature/x" || wt.BaseRef != "main" {
		t.Fatalf("second worktree %+v", wt)
	}

	cases := []struct {
		name string
		req  *agentv1.CreateSessionRequest
		code connect.Code
	}{
		{"outside repo", &agentv1.CreateSessionRequest{Cwd: h.proj, Worktree: &agentv1.WorktreeOptions{}}, connect.CodeFailedPrecondition},
		{"bad name", &agentv1.CreateSessionRequest{Cwd: repo, Worktree: &agentv1.WorktreeOptions{Name: "../x"}}, connect.CodeInvalidArgument},
		{"bad base", &agentv1.CreateSessionRequest{Cwd: repo, Worktree: &agentv1.WorktreeOptions{BaseRef: "nope"}}, connect.CodeInvalidArgument},
		{"branch in use", &agentv1.CreateSessionRequest{Cwd: repo, Worktree: &agentv1.WorktreeOptions{Branch: "existing"}}, connect.CodeFailedPrecondition},
		{"resume", &agentv1.CreateSessionRequest{Cwd: repo, ResumeSessionId: cs.Msg.Session.Id, Worktree: &agentv1.WorktreeOptions{}}, connect.CodeInvalidArgument},
	}
	for _, c := range cases {
		_, err := h.client.CreateSession(h.ctx, connect.NewRequest(c.req))
		if connectCode(err) != c.code {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if des, _ := os.ReadDir(filepath.Join(repo, ".harness", "worktrees")); len(des) != 2 {
		t.Fatalf("worktrees left behind: %v", des)
	}
}

// writeTranscript writes a transcript for cwd with the given mtime.
func writeTranscript(t *testing.T, id, cwd string, wt *session.Worktree, mod time.Time) {
	t.Helper()
	p := session.Store{Root: config.ProjectsDir()}.Path(cwd, id)
	os.MkdirAll(filepath.Dir(p), 0o700)
	var lines []string
	if wt != nil {
		b, _ := json.Marshal(session.Entry{Type: session.TypeMeta, SessionID: id, Worktree: wt})
		lines = append(lines, string(b))
	}
	b, _ := json.Marshal(session.Entry{Type: session.TypeUser, SessionID: id, Cwd: cwd, UUID: id + "-u", Message: session.UserText("hi")})
	lines = append(lines, string(b))
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, mod, mod)
}

func TestListRecentFolders(t *testing.T) {
	h := startHarness(t, nil, nil)
	repo := gittest.Init(t)
	gittest.Run(t, repo, "remote", "add", "origin", "git@github.com:Owner/Repo.git")
	plain, _ := filepath.EvalSymlinks(t.TempDir())
	gone := filepath.Join(plain, "deleted")
	now := time.Now().Truncate(time.Second)
	at := func(minutes int) time.Time { return now.Add(-time.Duration(minutes) * time.Minute) }

	writeTranscript(t, "11111111-0000-4000-8000-000000000001", plain, nil, at(50))
	writeTranscript(t, "11111111-0000-4000-8000-000000000002", plain, nil, at(5)) // newer session, same folder
	writeTranscript(t, "11111111-0000-4000-8000-000000000003", gone, nil, at(20))
	wtPath := filepath.Join(repo, ".harness", "worktrees", "calm-otter-ab12")
	writeTranscript(t, "11111111-0000-4000-8000-000000000004", filepath.Join(wtPath, "src"),
		&session.Worktree{Path: wtPath, Branch: "harness/calm-otter-ab12", BaseRef: "main", RepositoryPath: repo}, at(10))
	writeTranscript(t, "11111111-0000-4000-8000-000000000005", repo, nil, at(30))
	// A worktree path without metadata maps back too.
	writeTranscript(t, "11111111-0000-4000-8000-000000000006", filepath.Join(repo, ".harness", "worktrees", "other"), nil, at(40))

	rf, err := h.client.ListRecentFolders(h.ctx, connect.NewRequest(&agentv1.ListRecentFoldersRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range rf.Msg.Folders {
		got = append(got, f.Path)
	}
	want := []string{plain, filepath.Join(repo, "src"), gone, repo}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("folders\n got %v\nwant %v", got, want)
	}
	f := rf.Msg.Folders
	if f[0].LastUsedAt != at(5).UTC().Format(time.RFC3339) || f[0].Name != filepath.Base(plain) || !f[0].Exists || f[0].GitBranch != "" {
		t.Fatalf("plain %+v", f[0])
	}
	if f[2].Exists || f[2].Name != "deleted" {
		t.Fatalf("gone %+v", f[2])
	}
	if f[3].GitBranch != "main" || f[3].Repo != "github.com/owner/repo" || f[3].Name != filepath.Base(repo) {
		t.Fatalf("repo %+v", f[3])
	}
	if !f[1].Exists || f[1].GitBranch != "main" {
		t.Fatalf("repo subfolder %+v", f[1])
	}

	lim, err := h.client.ListRecentFolders(h.ctx, connect.NewRequest(&agentv1.ListRecentFoldersRequest{Limit: 2}))
	if err != nil || len(lim.Msg.Folders) != 2 || lim.Msg.Folders[0].Path != plain {
		t.Fatalf("limit %v %+v", err, lim)
	}
}

func TestListDirectory(t *testing.T) {
	h := startHarness(t, nil, nil)
	home, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", home)
	for _, d := range []string{"b", "A", ".hidden", "repo/.git", "c/nested"} {
		os.MkdirAll(filepath.Join(home, d), 0o755)
	}
	os.WriteFile(filepath.Join(home, "file.txt"), nil, 0o644)
	os.Symlink(filepath.Join(home, "c"), filepath.Join(home, "link"))
	os.Symlink(filepath.Join(home, "file.txt"), filepath.Join(home, "filelink"))

	list := func(path string, hidden bool) (*agentv1.ListDirectoryResponse, error) {
		r, err := h.client.ListDirectory(h.ctx, connect.NewRequest(&agentv1.ListDirectoryRequest{Path: path, IncludeHidden: hidden}))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}
	names := func(r *agentv1.ListDirectoryResponse) string {
		var out []string
		for _, e := range r.Entries {
			n := e.Name
			if e.GitRepository {
				n += "(git)"
			}
			out = append(out, n)
		}
		return strings.Join(out, ",")
	}

	r, err := list("", false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != home || r.Home != home || r.Parent != filepath.Dir(home) || names(r) != "A,b,c,link,repo(git)" {
		t.Fatalf("home listing %+v (%s)", r, names(r))
	}
	if r, err = list(home, true); err != nil || names(r) != ".hidden,A,b,c,link,repo(git)" {
		t.Fatalf("hidden %v %s", err, names(r))
	}
	if r, err = list("~/c", false); err != nil || r.Path != filepath.Join(home, "c") || names(r) != "nested" || r.Parent != home {
		t.Fatalf("~/c %v %+v", err, r)
	}
	if r, err = list("~", false); err != nil || r.Path != home {
		t.Fatalf("~ %v %+v", err, r)
	}
	if r, err = list(home+"/b/../c/", false); err != nil || r.Path != filepath.Join(home, "c") {
		t.Fatalf("clean %v %+v", err, r)
	}
	if r, err = list("/", false); err != nil || r.Path != "/" || r.Parent != "" {
		t.Fatalf("root %v %+v", err, r)
	}
	for path, code := range map[string]connect.Code{
		filepath.Join(home, "missing"):  connect.CodeNotFound,
		filepath.Join(home, "file.txt"): connect.CodeInvalidArgument,
		"relative/dir":                  connect.CodeInvalidArgument,
		"~someone/x":                    connect.CodeInvalidArgument,
	} {
		if _, err := list(path, false); connectCode(err) != code {
			t.Errorf("%s: %v", path, err)
		}
	}
	if os.Getuid() != 0 {
		locked := filepath.Join(home, "locked")
		os.MkdirAll(locked, 0o755)
		os.Chmod(locked, 0o000)
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
		if _, err := list(locked, false); connectCode(err) != connect.CodePermissionDenied {
			t.Errorf("unreadable: %v", err)
		}
	}
}

func TestListDirectoryCapsEntries(t *testing.T) {
	h := startHarness(t, nil, nil)
	dir := t.TempDir()
	for i := 0; i < 520; i++ {
		os.Mkdir(filepath.Join(dir, fmt.Sprintf("d%03d", i)), 0o755)
	}
	r, err := h.client.ListDirectory(h.ctx, connect.NewRequest(&agentv1.ListDirectoryRequest{Path: dir}))
	if err != nil || len(r.Msg.Entries) != 500 || r.Msg.Entries[0].Name != "d000" {
		t.Fatalf("%v %d", err, len(r.Msg.GetEntries()))
	}
}

func TestListBranches(t *testing.T) {
	h := startHarness(t, nil, nil)
	repo := gittest.Init(t)
	gittest.Run(t, repo, "branch", "topic")
	gittest.Run(t, repo, "branch", "busy")
	gittest.Run(t, repo, "worktree", "add", "-q", filepath.Join(t.TempDir(), "busy"), "busy")

	r, err := h.client.ListBranches(h.ctx, connect.NewRequest(&agentv1.ListBranchesRequest{Cwd: filepath.Join(repo, "src")}))
	if err != nil {
		t.Fatal(err)
	}
	m := r.Msg
	if m.RepositoryPath != repo || m.Current != "main" || len(m.Branches) != 3 {
		t.Fatalf("branches %+v", m)
	}
	co := map[string]bool{}
	for _, b := range m.Branches {
		co[b.Name] = b.CheckedOut
		if _, err := time.Parse(time.RFC3339, b.CommittedAt); err != nil || !strings.HasSuffix(b.CommittedAt, "Z") {
			t.Fatalf("committed_at %q", b.CommittedAt)
		}
	}
	if !co["main"] || !co["busy"] || co["topic"] {
		t.Fatalf("checked out %v", co)
	}

	out, err := h.client.ListBranches(h.ctx, connect.NewRequest(&agentv1.ListBranchesRequest{Cwd: h.proj}))
	if err != nil || out.Msg.RepositoryPath != "" || len(out.Msg.Branches) != 0 {
		t.Fatalf("outside %v %+v", err, out)
	}
	if _, err := h.client.ListBranches(h.ctx, connect.NewRequest(&agentv1.ListBranchesRequest{})); connectCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty cwd: %v", err)
	}
}
