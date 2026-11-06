package server_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/server"
	"github.com/xlsft/harness/packages/client/session"
)

// bashScript answers every prompt with one Bash call running the command next returns, then "done"; it
// records the tool results and the full text of each request that carried a result.
type bashScript struct {
	mu       sync.Mutex
	next     string
	results  []string
	requests []string
}

func (b *bashScript) respond(n int, r *llm.Request) fg.Reply {
	b.mu.Lock()
	defer b.mu.Unlock()
	if res := lastResultText(r); res != "" {
		b.results = append(b.results, res)
		b.requests = append(b.requests, allText(r))
		return fg.Reply{Events: fg.Message("done", fg.Text("done"))}
	}
	return fg.Reply{Events: fg.Message("bash", fg.ToolUse("b1", "Bash", map[string]any{"description": "test", "command": b.next}))}
}

func (b *bashScript) run(h *harness, sid string, ch <-chan *agentv1.SessionEvent, cmd string) (result, request string) {
	h.t.Helper()
	b.mu.Lock()
	b.next = cmd
	n := len(b.results)
	b.mu.Unlock()
	h.send(sid, "go")
	h.until(ch, isTurnFinished)
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.results) != n+1 {
		h.t.Fatalf("results %q", b.results)
	}
	return b.results[n], b.requests[n]
}

func tempDir(t *testing.T) string {
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestEnvironmentAppliesToNewAndRunningSessions(t *testing.T) {
	script := &bashScript{}
	h := startHarness(t, script.respond, nil)
	extra := tempDir(t)

	got, err := h.client.GetEnvironment(h.ctx, connect.NewRequest(&agentv1.GetEnvironmentRequest{Folder: h.proj}))
	if err != nil || got.Msg.Environment.GetExists() || got.Msg.Environment.GetFolder() != h.proj {
		t.Fatalf("defaults: %v %+v", err, got)
	}
	up, err := h.client.UpdateEnvironment(h.ctx, connect.NewRequest(&agentv1.UpdateEnvironmentRequest{Environment: &agentv1.Environment{
		Folder: h.proj, Name: "proj", Worktree: true, BaseRef: "main",
		AdditionalFolders: []*agentv1.EnvironmentFolder{{Path: extra}, {Path: filepath.Join(extra, "missing")}},
		Env:               "# token\nAPI_TOKEN=\"tok 1\"\n",
	}}))
	if err != nil {
		t.Fatal(err)
	}
	env := up.Msg.Environment
	if !env.Exists || env.UpdatedAt == "" || len(up.Msg.AppliedSessionIds) != 0 || strings.Join(env.VariableNames, ",") != "API_TOKEN" ||
		len(env.AdditionalFolders) != 2 || !env.AdditionalFolders[0].Exists || env.AdditionalFolders[1].Exists {
		t.Fatalf("update: %+v", up.Msg)
	}
	got, err = h.client.GetEnvironment(h.ctx, connect.NewRequest(&agentv1.GetEnvironmentRequest{Folder: h.proj}))
	if err != nil || got.Msg.Environment.Env != "# token\nAPI_TOKEN=\"tok 1\"\n" || got.Msg.Environment.Name != "proj" || !got.Msg.Environment.Worktree {
		t.Fatalf("get: %v %+v", err, got)
	}
	// Nothing in plain text under HARNESS_HOME.
	filepath.WalkDir(config.Home(), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, _ := os.ReadFile(p); strings.Contains(string(data), "tok 1") {
				t.Errorf("%s holds the value in plain text", p)
			}
		}
		return nil
	})

	// A new session in the folder gets the variables and the additional folder.
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	res, _ := script.run(h, sid, ch, `echo "tok=$API_TOKEN other=$OTHER"; cd `+extra+` && pwd`)
	if !strings.Contains(res, "tok=tok 1 other=") || !strings.Contains(res, extra) || strings.Contains(res, "reset") {
		t.Fatalf("new session: %q", res)
	}
	sf, err := h.client.GetEnvironment(h.ctx, connect.NewRequest(&agentv1.GetEnvironmentRequest{SessionId: sid}))
	if err != nil || sf.Msg.Environment.Folder != h.proj || !sf.Msg.Environment.Exists {
		t.Fatalf("by session: %v %+v", err, sf)
	}

	// An update applies to the running session from its next tool call, and the model is told.
	up, err = h.client.UpdateEnvironment(h.ctx, connect.NewRequest(&agentv1.UpdateEnvironmentRequest{SessionId: sid, Environment: &agentv1.Environment{
		Folder: h.proj, Env: "API_TOKEN=tok2\nexport OTHER='x y'\n",
	}}))
	if err != nil || len(up.Msg.AppliedSessionIds) != 1 || up.Msg.AppliedSessionIds[0] != sid {
		t.Fatalf("update live: %v %+v", err, up)
	}
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetNotice().GetKind() == "environment_applied" })
	res, req := script.run(h, sid, ch, `echo "tok=$API_TOKEN other=$OTHER"; cd `+extra+` && pwd`)
	if !strings.Contains(res, "tok=tok2 other=x y") || !strings.Contains(res, "reset") {
		t.Fatalf("running session after update: %q", res)
	}
	reminder, _, _ := strings.Cut(req[strings.Index(req, "Environment changed."):], "</system-reminder>")
	if !strings.Contains(reminder, "Variables: API_TOKEN, OTHER") || !strings.Contains(reminder, h.proj) || strings.Contains(reminder, "tok2") {
		t.Fatalf("reminder:\n%s", req)
	}

	// AddSessionDirectory stores the folder in the environment and applies it.
	add, err := h.client.AddSessionDirectory(h.ctx, connect.NewRequest(&agentv1.AddSessionDirectoryRequest{SessionId: sid, Path: extra}))
	if err != nil || len(add.Msg.Environment.AdditionalFolders) != 1 || add.Msg.Environment.AdditionalFolders[0].Path != extra ||
		add.Msg.Environment.Env != "API_TOKEN=tok2\nexport OTHER='x y'\n" || len(add.Msg.AppliedSessionIds) != 1 {
		t.Fatalf("add directory: %v %+v", err, add)
	}
	res, _ = script.run(h, sid, ch, `cd `+extra+` && pwd`)
	if strings.Contains(res, "reset") || !strings.Contains(res, extra) {
		t.Fatalf("after add: %q", res)
	}
	if _, err := h.client.AddSessionDirectory(h.ctx, connect.NewRequest(&agentv1.AddSessionDirectoryRequest{SessionId: sid, Path: filepath.Join(extra, "nope")})); connectCode(err) != connect.CodeNotFound {
		t.Fatalf("missing directory: %v", err)
	}
}

func TestUpdateEnvironmentValidation(t *testing.T) {
	h := startHarness(t, nil, nil)
	_, err := h.client.UpdateEnvironment(h.ctx, connect.NewRequest(&agentv1.UpdateEnvironmentRequest{Environment: &agentv1.Environment{
		Folder: h.proj, Env: "OK=1\n2BAD=x\nOPEN=\"secret\n",
	}}))
	var ce *connect.Error
	if connectCode(err) != connect.CodeInvalidArgument || !errorsAs(err, &ce) {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("value in the error: %v", err)
	}
	var lines []int32
	for _, d := range ce.Details() {
		v, derr := d.Value()
		if derr != nil {
			t.Fatal(derr)
		}
		for _, e := range v.(*agentv1.EnvironmentErrors).GetErrors() {
			lines = append(lines, e.Line)
		}
	}
	if len(lines) != 2 || lines[0] != 2 || lines[1] != 3 {
		t.Fatalf("lines %v", lines)
	}

	// validate_only checks without storing.
	v, err := h.client.UpdateEnvironment(h.ctx, connect.NewRequest(&agentv1.UpdateEnvironmentRequest{ValidateOnly: true, Environment: &agentv1.Environment{Folder: h.proj, Env: "A=1"}}))
	if err != nil || strings.Join(v.Msg.Environment.VariableNames, ",") != "A" || v.Msg.Environment.Exists {
		t.Fatalf("validate: %v %+v", err, v)
	}
	g, _ := h.client.GetEnvironment(h.ctx, connect.NewRequest(&agentv1.GetEnvironmentRequest{Folder: h.proj}))
	if g.Msg.Environment.Exists {
		t.Fatal("validate_only stored the environment")
	}
	for name, e := range map[string]*agentv1.Environment{
		"relative folder":   {Folder: "relative/path"},
		"relative extra":    {Folder: h.proj, AdditionalFolders: []*agentv1.EnvironmentFolder{{Path: "x"}}},
		"bad base ref":      {Folder: h.proj, BaseRef: "-x"},
		"reserved variable": {Folder: h.proj, Env: "__HARNESS_CWD_FILE=/tmp/x"},
	} {
		if _, err := h.client.UpdateEnvironment(h.ctx, connect.NewRequest(&agentv1.UpdateEnvironmentRequest{Environment: e})); connectCode(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := h.client.UpdateEnvironment(h.ctx, connect.NewRequest(&agentv1.UpdateEnvironmentRequest{})); connectCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("no environment: %v", err)
	}
}

func errorsAs(err error, ce **connect.Error) bool {
	e, ok := err.(*connect.Error)
	if ok {
		*ce = e
	}
	return ok
}

func TestGetFolderInfo(t *testing.T) {
	h := startHarness(t, nil, nil)
	repo := gittest.Init(t)
	gittest.Run(t, repo, "remote", "add", "origin", "git@github.com:Acme/Widget.git")
	info, err := h.client.GetFolderInfo(h.ctx, connect.NewRequest(&agentv1.GetFolderInfoRequest{Path: filepath.Join(repo, "src", "app")}))
	if err != nil {
		t.Fatal(err)
	}
	m := info.Msg
	if !m.Exists || m.RepositoryPath != repo || m.CurrentBranch != "main" || m.DefaultBranch != "main" ||
		m.RemoteUrl != "git@github.com:Acme/Widget.git" || m.WebUrl != "https://github.com/Acme/Widget" || m.Repo != "github.com/acme/widget" {
		t.Fatalf("repo info %+v", m)
	}
	plain, err := h.client.GetFolderInfo(h.ctx, connect.NewRequest(&agentv1.GetFolderInfoRequest{Path: h.proj}))
	if err != nil || !plain.Msg.Exists || plain.Msg.RepositoryPath != "" || plain.Msg.WebUrl != "" {
		t.Fatalf("plain folder %v %+v", err, plain)
	}
	missing, err := h.client.GetFolderInfo(h.ctx, connect.NewRequest(&agentv1.GetFolderInfoRequest{Path: filepath.Join(h.proj, "gone")}))
	if err != nil || missing.Msg.Exists {
		t.Fatalf("missing folder %v %+v", err, missing)
	}
	if _, err := h.client.GetFolderInfo(h.ctx, connect.NewRequest(&agentv1.GetFolderInfoRequest{Path: "rel"})); connectCode(err) != connect.CodeInvalidArgument {
		t.Fatalf("relative: %v", err)
	}
}

func TestSetSessionFolder(t *testing.T) {
	script := &bashScript{}
	h := startHarness(t, script.respond, nil)
	other := tempDir(t)
	if _, err := h.client.UpdateEnvironment(h.ctx, connect.NewRequest(&agentv1.UpdateEnvironmentRequest{Environment: &agentv1.Environment{Folder: other, Env: "WHERE=other"}})); err != nil {
		t.Fatal(err)
	}
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	if res, _ := script.run(h, sid, ch, `pwd; echo "where=$WHERE"`); !strings.Contains(res, h.proj) || !strings.Contains(res, "where=\n") && !strings.HasSuffix(strings.TrimSpace(res), "where=") {
		t.Fatalf("before: %q", res)
	}

	moved, err := h.client.SetSessionFolder(h.ctx, connect.NewRequest(&agentv1.SetSessionFolderRequest{SessionId: sid, Cwd: other}))
	if err != nil {
		t.Fatal(err)
	}
	if moved.Msg.Session.Id != sid || moved.Msg.Session.Cwd != other || moved.Msg.Session.PermissionMode != agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS {
		t.Fatalf("moved %+v", moved.Msg.Session)
	}
	evs := h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetSessionEnded() != nil })
	if r := evs[len(evs)-1].GetSessionEnded().Reason; r != "folder_changed" {
		t.Fatalf("ended: %s", r)
	}
	store := session.Store{Root: config.ProjectsDir()}
	if _, err := os.Stat(store.Path(other, sid)); err != nil {
		t.Fatalf("transcript not moved: %v", err)
	}
	if _, err := os.Stat(store.Path(h.proj, sid)); !os.IsNotExist(err) {
		t.Fatalf("old transcript left: %v", err)
	}

	// The conversation continues in the new folder with its environment; the history is kept.
	ch = h.subscribe(sid)
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetSessionUpdated() != nil }) // the replayed history comes first
	res, req := script.run(h, sid, ch, `pwd; echo "where=$WHERE"`)
	if !strings.Contains(res, other) || !strings.Contains(res, "where=other") {
		t.Fatalf("after: %q", res)
	}
	if strings.Count(req, "where=") < 2 {
		t.Fatalf("history lost:\n%s", req)
	}

	// Listed under the new folder, also after the session closes.
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{other, h.proj, ""} {
		ls, err := h.client.ListSessions(h.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{Cwd: cwd}))
		if err != nil {
			t.Fatal(err)
		}
		found := ""
		for _, s := range ls.Msg.Sessions {
			if s.Id == sid {
				found = s.Cwd
			}
		}
		if want := map[string]string{other: other, h.proj: "", "": other}[cwd]; found != want {
			t.Fatalf("list %q: cwd %q, want %q", cwd, found, want)
		}
	}

	// A closed session can be moved too; a worktree session and a missing folder cannot.
	back, err := h.client.SetSessionFolder(h.ctx, connect.NewRequest(&agentv1.SetSessionFolderRequest{SessionId: sid, Cwd: h.proj}))
	if err != nil || back.Msg.Session.Cwd != h.proj {
		t.Fatalf("move closed: %v %+v", err, back)
	}
	if _, err := h.client.SetSessionFolder(h.ctx, connect.NewRequest(&agentv1.SetSessionFolderRequest{SessionId: sid, Cwd: filepath.Join(other, "gone")})); connectCode(err) != connect.CodeNotFound {
		t.Fatalf("missing folder: %v", err)
	}
	repo := gittest.Init(t)
	wt, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: repo, Worktree: &agentv1.WorktreeOptions{}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.SetSessionFolder(h.ctx, connect.NewRequest(&agentv1.SetSessionFolderRequest{SessionId: wt.Msg.Session.Id, Cwd: other})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("worktree session: %v", err)
	}
}

func TestEnvironmentOfRemoteSessionIsReadOnly(t *testing.T) {
	h := startRemoteHarness(t, nil)
	remote := h.seedRemote(otherDevice, "/remote/proj", "hello", "hi")
	h.listAll()
	g, err := h.client.GetEnvironment(h.ctx, connect.NewRequest(&agentv1.GetEnvironmentRequest{SessionId: remote}))
	if err != nil || !g.Msg.Environment.ReadOnly || g.Msg.Environment.Folder != "/remote/proj" || g.Msg.Environment.Env != "" {
		t.Fatalf("get: %v %+v", err, g)
	}
	if _, err := h.client.UpdateEnvironment(h.ctx, connect.NewRequest(&agentv1.UpdateEnvironmentRequest{SessionId: remote, Environment: &agentv1.Environment{Folder: h.proj}})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("update: %v", err)
	}
	if _, err := h.client.SetSessionFolder(h.ctx, connect.NewRequest(&agentv1.SetSessionFolderRequest{SessionId: remote, Cwd: h.proj})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("set folder: %v", err)
	}
	if _, err := h.client.AddSessionDirectory(h.ctx, connect.NewRequest(&agentv1.AddSessionDirectoryRequest{SessionId: remote, Path: h.proj})); connectCode(err) != connect.CodeFailedPrecondition {
		t.Fatalf("add directory: %v", err)
	}
}

func TestInitializeReportsServerVersion(t *testing.T) {
	h := startHarnessSrv(t, nil, nil, nil, func(s *server.Server) {
		s.ServerVersion = func(context.Context) string { return "1.2.3" }
	})
	init, err := h.client.Initialize(h.ctx, connect.NewRequest(&agentv1.InitializeRequest{}))
	if err != nil || init.Msg.ServerVersion != "1.2.3" {
		t.Fatalf("%v %+v", err, init)
	}
}

func TestReadServerVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			fmt.Fprint(w, `{"ok":true,"version":"1.2.3"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if got := server.ReadServerVersion(context.Background(), srv.Client(), srv.URL); got != "1.2.3" {
		t.Fatalf("got %q", got)
	}
	// A server from before the first release has no version in its health; a dead one no answer. Neither is an error.
	if got := server.ReadServerVersion(context.Background(), srv.Client(), srv.URL+"/nope"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestInitializeReportsWebURL(t *testing.T) {
	h := startHarnessSrv(t, nil, nil, nil, func(s *server.Server) { s.WebURL = "https://harness.example.com" })
	init, err := h.client.Initialize(h.ctx, connect.NewRequest(&agentv1.InitializeRequest{}))
	if err != nil || init.Msg.WebUrl != "https://harness.example.com" {
		t.Fatalf("%v %+v", err, init)
	}
}
