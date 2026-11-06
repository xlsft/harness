package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
)

// ---- a fake server ----

type fakeAPI struct {
	relayv1connect.UnimplementedHostAgentServiceHandler
	mu       sync.Mutex
	queue    []*relayv1.HostWork
	updates  []*relayv1.UpdateHostWorkRequest
	reports  []*relayv1.ReportAgentHostRequest
	prs      []*relayv1.OpenHostPullRequestRequest
	regs     []*relayv1.RegisterAgentHostRequest
	cloneURL string
	credErr  error
	password string
	kick     chan struct{}
	// supervisor side
	supervisor bool
	wanted     []*relayv1.WantedWorker
	stops      []string
	issued     []string
}

func newFakeAPI(t *testing.T) (*fakeAPI, string) {
	t.Helper()
	f := &fakeAPI{password: "s3cret-token", kick: make(chan struct{}, 8)}
	mux := http.NewServeMux()
	path, h := relayv1connect.NewHostAgentServiceHandler(f)
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *fakeAPI) RegisterAgentHost(_ context.Context, req *connect.Request[relayv1.RegisterAgentHostRequest]) (*connect.Response[relayv1.RegisterAgentHostResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.regs = append(f.regs, req.Msg)
	return connect.NewResponse(&relayv1.RegisterAgentHostResponse{Host: &relayv1.AgentHost{Id: "ahs_1", Name: req.Msg.GetName()}, Supervisor: f.supervisor}), nil
}

func (f *fakeAPI) ReportAgentHost(_ context.Context, req *connect.Request[relayv1.ReportAgentHostRequest]) (*connect.Response[relayv1.ReportAgentHostResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, req.Msg)
	return connect.NewResponse(&relayv1.ReportAgentHostResponse{}), nil
}

func (f *fakeAPI) ClaimHostWork(ctx context.Context, req *connect.Request[relayv1.ClaimHostWorkRequest]) (*connect.Response[relayv1.ClaimHostWorkResponse], error) {
	for {
		f.mu.Lock()
		if len(f.queue) > 0 {
			w := f.queue[0]
			f.queue = f.queue[1:]
			f.mu.Unlock()
			return connect.NewResponse(&relayv1.ClaimHostWorkResponse{Work: w, GitName: "Alice", GitEmail: "alice@test.local"}), nil
		}
		f.mu.Unlock()
		select {
		case <-ctx.Done():
			return connect.NewResponse(&relayv1.ClaimHostWorkResponse{}), nil
		case <-f.kick:
		case <-time.After(100 * time.Millisecond):
			return connect.NewResponse(&relayv1.ClaimHostWorkResponse{}), nil
		}
	}
}

func (f *fakeAPI) add(w *relayv1.HostWork) {
	f.mu.Lock()
	f.queue = append(f.queue, w)
	f.mu.Unlock()
	select {
	case f.kick <- struct{}{}:
	default:
	}
}

func (f *fakeAPI) UpdateHostWork(_ context.Context, req *connect.Request[relayv1.UpdateHostWorkRequest]) (*connect.Response[relayv1.UpdateHostWorkResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, req.Msg)
	return connect.NewResponse(&relayv1.UpdateHostWorkResponse{}), nil
}

func (f *fakeAPI) IssueGitCredentials(context.Context, *connect.Request[relayv1.IssueGitCredentialsRequest]) (*connect.Response[relayv1.IssueGitCredentialsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.credErr != nil {
		return nil, f.credErr
	}
	return connect.NewResponse(&relayv1.IssueGitCredentialsResponse{CloneUrl: f.cloneURL, Username: "x-access-token", Password: f.password}), nil
}

func (f *fakeAPI) OpenHostPullRequest(_ context.Context, req *connect.Request[relayv1.OpenHostPullRequestRequest]) (*connect.Response[relayv1.OpenHostPullRequestResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs = append(f.prs, req.Msg)
	return connect.NewResponse(&relayv1.OpenHostPullRequestResponse{Url: "https://code.test/pr/1", Number: 1}), nil
}

func (f *fakeAPI) ListWantedWorkers(ctx context.Context, _ *connect.Request[relayv1.ListWantedWorkersRequest]) (*connect.Response[relayv1.ListWantedWorkersResponse], error) {
	f.mu.Lock()
	res := &relayv1.ListWantedWorkersResponse{Workers: f.wanted, StopUserIds: f.stops}
	f.stops = nil
	f.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Millisecond):
	}
	return connect.NewResponse(res), nil
}

func (f *fakeAPI) IssueWorkerToken(_ context.Context, req *connect.Request[relayv1.IssueWorkerTokenRequest]) (*connect.Response[relayv1.IssueWorkerTokenResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issued = append(f.issued, req.Msg.GetUserId())
	return connect.NewResponse(&relayv1.IssueWorkerTokenResponse{DeviceId: "dev_" + req.Msg.GetUserId(), DeviceToken: "hdt_worker_" + req.Msg.GetUserId()}), nil
}

func (f *fakeAPI) updateWith(pred func(*relayv1.UpdateHostWorkRequest) bool) *relayv1.UpdateHostWorkRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.updates {
		if pred(u) {
			return u
		}
	}
	return nil
}

// ---- a fake agent ----

type fakeSession struct {
	id     string
	req    *agentv1.CreateSessionRequest
	sent   []string
	jobs   []*agentv1.AutoFixJob
	title  string
	status agentv1.SessionStatus
	subs   []chan *agentv1.SessionEvent
	seq    int64
}

type fakeSessions struct {
	mu        sync.Mutex
	sessions  map[string]*fakeSession
	published []*agentv1.HostWorkUpdated
	failFork  bool
	onTurn    func(s *fakeSession) // what the "agent" does when told
	n         int
}

func newFakeSessions() *fakeSessions { return &fakeSessions{sessions: map[string]*fakeSession{}} }

func (f *fakeSessions) Create(_ context.Context, req *agentv1.CreateSessionRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.GetFork() && f.failFork {
		return "", connect.NewError(connect.CodeNotFound, os.ErrNotExist)
	}
	if id := req.GetResumeSessionId(); id != "" && !req.GetFork() {
		f.sessions[id] = &fakeSession{id: id, req: req, status: agentv1.SessionStatus_SESSION_STATUS_IDLE}
		return id, nil
	}
	f.n++
	id := "s" + string(rune('0'+f.n))
	f.sessions[id] = &fakeSession{id: id, req: req, status: agentv1.SessionStatus_SESSION_STATUS_IDLE}
	return id, nil
}

func (f *fakeSessions) SetTitle(_ context.Context, id, title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[id].title = title
	return nil
}

func (f *fakeSessions) runTurn(id string) {
	f.mu.Lock()
	s := f.sessions[id]
	hook := f.onTurn
	f.mu.Unlock()
	if hook != nil {
		hook(s)
	}
	f.mu.Lock()
	s.seq++
	ev := &agentv1.SessionEvent{Seq: s.seq, Event: &agentv1.SessionEvent_TurnFinished{TurnFinished: &agentv1.TurnFinished{}}}
	subs := append([]chan *agentv1.SessionEvent(nil), s.subs...)
	f.mu.Unlock()
	for _, c := range subs {
		c <- ev
	}
}

func (f *fakeSessions) Send(_ context.Context, id, text string) error {
	f.mu.Lock()
	f.sessions[id].sent = append(f.sessions[id].sent, text)
	f.mu.Unlock()
	go f.runTurn(id)
	return nil
}

func (f *fakeSessions) DeliverJob(_ context.Context, id string, job *agentv1.AutoFixJob) error {
	f.mu.Lock()
	f.sessions[id].jobs = append(f.sessions[id].jobs, job)
	f.mu.Unlock()
	go f.runTurn(id)
	return nil
}

func (f *fakeSessions) Close(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.sessions[id]; s != nil {
		for _, c := range s.subs {
			close(c)
		}
	}
	delete(f.sessions, id)
	return nil
}

func (f *fakeSessions) Live() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.sessions))
	for id := range f.sessions {
		out = append(out, id)
	}
	return out
}

func (f *fakeSessions) Status(id string) (agentv1.SessionStatus, time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[id]
	if s == nil {
		return 0, time.Time{}, false
	}
	return s.status, time.Now().Add(-time.Hour), true
}

func (f *fakeSessions) Events(id string, after int64) (<-chan *agentv1.SessionEvent, int64, func(), bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[id]
	if s == nil {
		return nil, 0, nil, false
	}
	c := make(chan *agentv1.SessionEvent, 8)
	s.subs = append(s.subs, c)
	return c, max(after, 0), func() {}, true
}

func (f *fakeSessions) Publish(_ string, u *agentv1.HostWorkUpdated) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, u)
}

func (f *fakeSessions) session(id string) *fakeSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[id]
}

// ---- git helpers ----

func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// bareRemote makes a code host's repository with a commit on main, and returns its file URL and path.
func bareRemote(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	bare := filepath.Join(base, "remote.git")
	testGit(t, base, "init", "--bare", "-b", "main", bare)
	seed := filepath.Join(base, "seed")
	testGit(t, base, "clone", bare, seed)
	testGit(t, seed, "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, seed, "add", ".")
	testGit(t, seed, "commit", "-m", "first")
	testGit(t, seed, "push", "origin", "main")
	testGit(t, seed, "checkout", "-b", "fix-me")
	if err := os.WriteFile(filepath.Join(seed, "fix.txt"), []byte("needs fixing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, seed, "add", ".")
	testGit(t, seed, "commit", "-m", "fix branch")
	testGit(t, seed, "push", "origin", "fix-me")
	return "file://" + bare, bare
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func startRunner(t *testing.T, api *fakeAPI, url string, sessions Sessions, mod func(*Options)) (*Runner, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	base := t.TempDir()
	o := Options{ServerURL: url, Sessions: sessions, Name: "box", Root: filepath.Join(base, "root"), StateDir: filepath.Join(base, "state"), Tick: 10 * time.Millisecond, ReportEvery: 20 * time.Millisecond, PollSeconds: 1, Git: Git{AllowFile: true}, Logger: nil}
	if mod != nil {
		mod(&o)
	}
	r := New(o)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return r, cancel
}

// ---- tests ----

func TestNewSessionWorksInAFreshWorktreeAndLandsAsAPullRequest(t *testing.T) {
	cloneURL, bare := bareRemote(t)
	api, url := newFakeAPI(t)
	api.cloneURL = cloneURL
	s := newFakeSessions()
	s.onTurn = func(fs *fakeSession) {
		// The agent's edits, left uncommitted.
		if err := os.WriteFile(filepath.Join(fs.req.GetCwd(), "new.txt"), []byte("work\n"), 0o644); err != nil {
			t.Error(err)
		}
	}
	r, _ := startRunner(t, api, url, s, nil)
	api.add(&relayv1.HostWork{Id: "hwk_a", RepositoryKey: "code.test/acme/widgets", Prompt: "do it", Title: "Do it"})
	var running *relayv1.UpdateHostWorkRequest
	waitFor(t, "the session", func() bool {
		running = api.updateWith(func(u *relayv1.UpdateHostWorkRequest) bool { return u.GetState() == relayv1.HostWorkState_HOST_WORK_STATE_RUNNING })
		return running != nil
	})
	if running.GetBranch() != "harness/hwk_a" || running.GetSessionId() == "" {
		t.Fatalf("running: %v", running)
	}
	fs := s.session(running.GetSessionId())
	if got := fs.req.GetCwd(); got != filepath.Join(r.o.Root, "work", "hwk_a") {
		t.Fatalf("cwd %s", got)
	}
	if fs.title != "Do it" {
		t.Fatalf("title %q", fs.title)
	}
	waitFor(t, "the pull request", func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return len(api.prs) == 1
	})
	pr := api.prs[0]
	if pr.GetHeadBranch() != "harness/hwk_a" || pr.GetBaseBranch() != "main" || !pr.GetDraft() || pr.GetTitle() != "Do it" {
		t.Fatalf("pr: %v", pr)
	}
	if got := testGit(t, bare, "log", "-1", "--format=%an <%ae>", "harness/hwk_a"); got != "Alice <alice@test.local>" {
		t.Fatalf("author %q", got)
	}
	if got := testGit(t, bare, "show", "harness/hwk_a:new.txt"); got != "work" {
		t.Fatalf("pushed content %q", got)
	}
	// main stays as it was.
	if got := testGit(t, bare, "rev-list", "--count", "main"); got != "1" {
		t.Fatalf("main moved: %s commits", got)
	}
	waitFor(t, "the viewers are told", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.published) > 0 && s.published[len(s.published)-1].GetPullRequestUrl() != ""
	})
	// The token is nowhere on disk.
	assertNoToken(t, r.o.Root, api.password)
	assertNoToken(t, r.o.StateDir, api.password)
}

func assertNoToken(t *testing.T, dir, token string) {
	t.Helper()
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Size() > 1<<20 {
			return nil
		}
		b, _ := os.ReadFile(path)
		if strings.Contains(string(b), token) {
			t.Errorf("the token is in %s", path)
		}
		return nil
	})
}

func TestAFollowUpLandsOnTheSameBranchAndPullRequest(t *testing.T) {
	cloneURL, bare := bareRemote(t)
	api, url := newFakeAPI(t)
	api.cloneURL = cloneURL
	s := newFakeSessions()
	turn := 0
	s.onTurn = func(fs *fakeSession) {
		turn++
		if err := os.WriteFile(filepath.Join(fs.req.GetCwd(), "new.txt"), []byte(strings.Repeat("x", turn)), 0o644); err != nil {
			t.Error(err)
		}
	}
	startRunner(t, api, url, s, nil)
	api.add(&relayv1.HostWork{Id: "hwk_b", RepositoryKey: "code.test/a/b", Prompt: "one"})
	waitFor(t, "the first push", func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return len(api.prs) == 1
	})
	id := api.updateWith(func(u *relayv1.UpdateHostWorkRequest) bool { return u.GetSessionId() != "" }).GetSessionId()
	if err := s.Send(context.Background(), id, "two"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second push", func() bool { return testGit(t, bare, "rev-list", "--count", "harness/hwk_b") == "3" })
	time.Sleep(50 * time.Millisecond)
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.prs) != 1 {
		t.Fatalf("%d pull requests", len(api.prs))
	}
}

func TestAJobGoesToANewSessionOnTheHeadBranchAndForksTheTranscript(t *testing.T) {
	cloneURL, bare := bareRemote(t)
	api, url := newFakeAPI(t)
	api.cloneURL = cloneURL
	s := newFakeSessions()
	s.onTurn = func(fs *fakeSession) {
		if err := os.WriteFile(filepath.Join(fs.req.GetCwd(), "fix.txt"), []byte("fixed\n"), 0o644); err != nil {
			t.Error(err)
		}
	}
	startRunner(t, api, url, s, nil)
	job := &agentv1.AutoFixJob{Id: "afx_1", Kind: agentv1.AutoFixJob_KIND_MENTION}
	api.add(&relayv1.HostWork{Id: "hwk_c", Kind: relayv1.HostWorkKind_HOST_WORK_KIND_MENTION, RepositoryKey: "code.test/a/b", Branch: "fix-me", BaseRef: "main", Landing: relayv1.HostWorkLanding_HOST_WORK_LANDING_BRANCH, Job: job, ForkSessionId: "old"})
	var running *relayv1.UpdateHostWorkRequest
	waitFor(t, "the session", func() bool {
		running = api.updateWith(func(u *relayv1.UpdateHostWorkRequest) bool { return u.GetState() == relayv1.HostWorkState_HOST_WORK_STATE_RUNNING })
		return running != nil
	})
	fs := s.session(running.GetSessionId())
	if fs.req.GetResumeSessionId() != "old" || !fs.req.GetFork() {
		t.Fatalf("not forked: %v", fs.req)
	}
	waitFor(t, "the job", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(fs.jobs) == 1
	})
	if len(fs.sent) != 0 {
		t.Fatal("a job must not be a user message")
	}
	waitFor(t, "the push", func() bool { return testGit(t, bare, "show", "fix-me:fix.txt") == "fixed" })
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.prs) != 0 {
		t.Fatal("a branch landing opens no pull request")
	}
}

func TestAFreshCloneAndACredentialFailure(t *testing.T) {
	cloneURL, _ := bareRemote(t)
	api, url := newFakeAPI(t)
	api.cloneURL = cloneURL
	s := newFakeSessions()
	r, _ := startRunner(t, api, url, s, nil)
	api.add(&relayv1.HostWork{Id: "hwk_d", RepositoryKey: "code.test/a/b", Prompt: "x", FreshClone: true, Landing: relayv1.HostWorkLanding_HOST_WORK_LANDING_NONE})
	var running *relayv1.UpdateHostWorkRequest
	waitFor(t, "the session", func() bool {
		running = api.updateWith(func(u *relayv1.UpdateHostWorkRequest) bool { return u.GetState() == relayv1.HostWorkState_HOST_WORK_STATE_RUNNING })
		return running != nil
	})
	dir := s.session(running.GetSessionId()).req.GetCwd()
	if st, err := os.Stat(filepath.Join(dir, ".git")); err != nil || !st.IsDir() {
		t.Fatalf("a fresh clone has its own .git: %v", err)
	}
	if _, err := os.Stat(cacheDir(r.o.Root, "code.test/a/b")); err == nil {
		t.Fatal("no cache for a fresh clone")
	}
	if got := testGit(t, dir, "config", "user.email"); got != "alice@test.local" {
		t.Fatalf("identity %q", got)
	}
	api.mu.Lock()
	api.credErr = connect.NewError(connect.CodeFailedPrecondition, errConnectAccount)
	api.mu.Unlock()
	api.add(&relayv1.HostWork{Id: "hwk_e", RepositoryKey: "code.test/a/b", Prompt: "y"})
	waitFor(t, "the failure", func() bool {
		return api.updateWith(func(u *relayv1.UpdateHostWorkRequest) bool {
			return u.GetWorkId() == "hwk_e" && u.GetState() == relayv1.HostWorkState_HOST_WORK_STATE_FAILED && strings.Contains(u.GetError(), "connect your account")
		}) != nil
	})
}

var errConnectAccount = &errString{"connect your account"}

type errString struct{ s string }

func (e *errString) Error() string { return e.s }

func TestASessionReopensAfterARestartAndAnOldOneIsCleanedUp(t *testing.T) {
	cloneURL, _ := bareRemote(t)
	api, url := newFakeAPI(t)
	api.cloneURL = cloneURL
	s := newFakeSessions()
	base := t.TempDir()
	mod := func(o *Options) {
		o.Root, o.StateDir = filepath.Join(base, "root"), filepath.Join(base, "state")
		o.IdleClose = 10 * time.Minute // fakeSessions says an hour ago: closes when asked to
		o.KeepWorkspaces = time.Millisecond
	}
	api.add(&relayv1.HostWork{Id: "hwk_f", RepositoryKey: "code.test/a/b", Prompt: "x", Landing: relayv1.HostWorkLanding_HOST_WORK_LANDING_NONE})
	_, cancel := startRunner(t, api, url, s, func(o *Options) { mod(o); o.IdleClose = 24 * time.Hour })
	var running *relayv1.UpdateHostWorkRequest
	waitFor(t, "the session", func() bool {
		running = api.updateWith(func(u *relayv1.UpdateHostWorkRequest) bool { return u.GetState() == relayv1.HostWorkState_HOST_WORK_STATE_RUNNING })
		return running != nil
	})
	id := running.GetSessionId()
	dir := s.session(id).req.GetCwd()
	cancel()
	time.Sleep(20 * time.Millisecond)

	// The next run of the daemon: the process lost its sessions, and the record brings them back.
	s2 := newFakeSessions()
	startRunner(t, api, url, s2, mod)
	waitFor(t, "the reopen, the idle close and the workspace sweep", func() bool {
		_, err := os.Stat(dir)
		return os.IsNotExist(err)
	})
	if api.updateWith(func(u *relayv1.UpdateHostWorkRequest) bool { return u.GetState() == relayv1.HostWorkState_HOST_WORK_STATE_DONE }) == nil {
		t.Fatal("closing it should finish the work")
	}
}

func TestReportsCountOnlyWorkingSessions(t *testing.T) {
	cloneURL, _ := bareRemote(t)
	api, url := newFakeAPI(t)
	api.cloneURL = cloneURL
	s := newFakeSessions()
	s.sessions["w"] = &fakeSession{id: "w", status: agentv1.SessionStatus_SESSION_STATUS_WORKING}
	s.sessions["n"] = &fakeSession{id: "n", status: agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT}
	s.sessions["i"] = &fakeSession{id: "i", status: agentv1.SessionStatus_SESSION_STATUS_IDLE}
	startRunner(t, api, url, s, nil)
	waitFor(t, "a report", func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return len(api.reports) > 0
	})
	api.mu.Lock()
	defer api.mu.Unlock()
	if got := api.reports[0].GetWorkingSessions(); got != 2 {
		t.Fatalf("working %d", got)
	}
	if api.regs[0].GetIsolation() != relayv1.AgentHostIsolation_AGENT_HOST_ISOLATION_NONE {
		t.Fatal("a personal host promises no isolation")
	}
}

// ---- what an agent could leave in the repository ----

func TestLandIgnoresWhatTheAgentPutInTheRepository(t *testing.T) {
	cloneURL, bare := bareRemote(t)
	g := Git{AllowFile: true}
	root := t.TempDir()
	cred := Credentials{CloneURL: cloneURL, Username: "x-access-token", Password: "tok-secret"}
	ctx := context.Background()
	p, err := g.Prepare(ctx, root, Checkout{RepositoryKey: "h/o/r", Branch: "harness/x", Dir: filepath.Join(root, "work", "x"), Name: "A", Email: "a@t"}, cred)
	if err != nil {
		t.Fatal(err)
	}
	if p.Base != "main" {
		t.Fatalf("base %q", p.Base)
	}
	// A hook and a credential helper that would catch the token, in the repository's own configuration.
	canary := filepath.Join(root, "canary")
	hook := "#!/bin/sh\nenv > " + canary + "\n"
	common := testGit(t, p.Dir, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(common) {
		common = filepath.Join(p.Dir, common)
	}
	for _, name := range []string{"pre-commit", "pre-push", "post-commit", "commit-msg"} {
		if err := os.WriteFile(filepath.Join(common, "hooks", name), []byte(hook), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	testGit(t, p.Dir, "config", "credential.helper", "!f() { cat > "+canary+"; }; f")
	if err := os.WriteFile(filepath.Join(p.Dir, "agent.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := g.Land(ctx, p.Dir, p.Branch, p.Base, "msg", cred)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Committed || !res.Pushed || res.Ahead != 1 {
		t.Fatalf("land: %+v", res)
	}
	if _, err := os.Stat(canary); err == nil {
		t.Fatal("something the agent left in the repository ran")
	}
	if got := testGit(t, bare, "show", "harness/x:agent.txt"); got != "x" {
		t.Fatalf("pushed %q", got)
	}
	// Nothing new: no push.
	res, err = g.Land(ctx, p.Dir, p.Branch, p.Base, "msg", cred)
	if err != nil || res.Pushed || res.Committed {
		t.Fatalf("second land: %+v %v", res, err)
	}
	// The base branch is never a target, and rewritten URLs refuse.
	if _, err := g.Land(ctx, p.Dir, "main", "main", "msg", cred); err == nil {
		t.Fatal("pushed to the base branch")
	}
	testGit(t, p.Dir, "config", "url.https://evil.test/.pushInsteadOf", cloneURL)
	if _, err := g.Land(ctx, p.Dir, p.Branch, p.Base, "msg", cred); err == nil || !strings.Contains(err.Error(), "rewrites") {
		t.Fatalf("rewrite: %v", err)
	}
	assertNoToken(t, root, cred.Password)
}

func TestTheCredentialHelperAnswersFromTheEnvironmentOnly(t *testing.T) {
	cmd := exec.Command("git", "-c", "credential.helper=", "-c", "credential.helper="+credentialHelper, "credential", "fill")
	cmd.Env = append(gitEnv(false), "HARNESS_GIT_USERNAME=u", "HARNESS_GIT_PASSWORD=p")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=code.test\n\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "username=u\n") || !strings.Contains(string(out), "password=p\n") {
		t.Fatalf("fill: %s", out)
	}
	if strings.Contains(credentialHelper, "p\n") {
		t.Fatal("the helper holds a secret")
	}
}

func TestCheckRefNameRefusesOptionsAndExpressions(t *testing.T) {
	for _, bad := range []string{"", "-x", "a b", "a..b", "a:b", "a^", "a~1", "a?"} {
		if checkRefName(bad) == nil {
			t.Errorf("%q passed", bad)
		}
	}
	if err := checkRefName("harness/hwk_1"); err != nil {
		t.Error(err)
	}
}

// ---- team host ----

type fakeWorker struct {
	spec    WorkerSpec
	stopped chan struct{}
	once    sync.Once
}

func (w *fakeWorker) Wait() error {
	<-w.stopped
	return nil
}

func (w *fakeWorker) Stop() { w.once.Do(func() { close(w.stopped) }) }

type fakeLauncher struct {
	mu      sync.Mutex
	started []*fakeWorker
}

func (l *fakeLauncher) Start(_ context.Context, spec WorkerSpec) (Worker, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := &fakeWorker{spec: spec, stopped: make(chan struct{})}
	l.started = append(l.started, w)
	return w, nil
}

func (l *fakeLauncher) list() []*fakeWorker {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*fakeWorker(nil), l.started...)
}

func TestASupervisorKeepsOneWorkerPerMemberUnderTheirOwnUser(t *testing.T) {
	api, url := newFakeAPI(t)
	api.supervisor = true
	api.wanted = []*relayv1.WantedWorker{{UserId: "usr_a"}, {UserId: "usr_b"}}
	l := &fakeLauncher{}
	base := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	s := NewSupervisor(SupervisorOptions{ServerURL: url, Name: "team", BaseDir: base, Launcher: l, UIDBase: 20000, UIDCount: 2, PollSeconds: 1, ReportEvery: 20 * time.Millisecond, RestartDelay: time.Millisecond})
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	waitFor(t, "two workers", func() bool { return len(l.list()) == 2 })
	got := map[string]*fakeWorker{}
	for _, w := range l.list() {
		got[w.spec.UserID] = w
	}
	a, b := got["usr_a"], got["usr_b"]
	if a == nil || b == nil || a.spec.UID == b.spec.UID || a.spec.UID < 20000 || a.spec.Home == b.spec.Home {
		t.Fatalf("workers: %+v %+v", a, b)
	}
	if a.spec.Token != "hdt_worker_usr_a" {
		t.Fatalf("token %q", a.spec.Token)
	}
	// A worker that dies is started again, with a fresh token and the same user.
	a.Stop()
	waitFor(t, "a restart", func() bool { return len(l.list()) == 3 })
	again := l.list()[2]
	if again.spec.UserID != "usr_a" || again.spec.UID != a.spec.UID {
		t.Fatalf("restart: %+v", again.spec)
	}
	// Access removed: stopped, and not started again.
	api.mu.Lock()
	api.wanted = []*relayv1.WantedWorker{{UserId: "usr_a"}}
	api.stops = []string{"usr_b"}
	api.mu.Unlock()
	waitFor(t, "the stop", func() bool {
		select {
		case <-b.stopped:
			return true
		default:
			return false
		}
	})
	time.Sleep(50 * time.Millisecond)
	if n := len(l.list()); n != 3 {
		t.Fatalf("%d starts", n)
	}
	// The uids are kept across runs, and the range is a limit.
	if uid, err := s.uidOf("usr_a"); err != nil || uid != a.spec.UID {
		t.Fatalf("uid %d %v", uid, err)
	}
	if _, err := s.uidOf("usr_c"); err == nil {
		t.Fatal("the range is full")
	}
	cancel()
	<-done
	select {
	case <-again.stopped:
	default:
		t.Fatal("workers stop with the supervisor")
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.regs[0].GetIsolation() != relayv1.AgentHostIsolation_AGENT_HOST_ISOLATION_UID {
		t.Fatal("a supervisor promises uid separation")
	}
}

func TestASupervisorAndARunnerRefuseTheOthersToken(t *testing.T) {
	api, url := newFakeAPI(t)
	s := NewSupervisor(SupervisorOptions{ServerURL: url, BaseDir: t.TempDir(), Launcher: &fakeLauncher{}})
	if err := s.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "without --shared") {
		t.Fatalf("supervisor: %v", err)
	}
	api.mu.Lock()
	api.supervisor = true
	api.mu.Unlock()
	r := New(Options{ServerURL: url, Sessions: newFakeSessions(), Root: t.TempDir(), StateDir: t.TempDir()})
	if err := r.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "--shared") {
		t.Fatalf("runner: %v", err)
	}
}

func TestSharedIsRefusedWithoutRootOnLinux(t *testing.T) {
	err := CheckShared()
	if os.Geteuid() == 0 && isLinux() {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	if err == nil {
		t.Fatal("CheckShared passed without root on Linux")
	}
}

func TestAWorkerEnvironmentCarriesNothingOfTheSupervisors(t *testing.T) {
	t.Setenv("HARNESS_DEVICE_TOKEN", "hdt_supervisor")
	t.Setenv("GH_TOKEN", "gh_x")
	t.Setenv("SECRET_THING", "x")
	env := strings.Join(workerEnv("https://h.test", "/home/w"), "\n")
	for _, bad := range []string{"hdt_supervisor", "gh_x", "SECRET_THING"} {
		if strings.Contains(env, bad) {
			t.Fatalf("%s reached a worker:\n%s", bad, env)
		}
	}
	if !strings.Contains(env, "HARNESS_HOME=/home/w/.harness") {
		t.Fatalf("env:\n%s", env)
	}
}

var _ io.Writer = logWriter{}
