package vcs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/auth"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/vcs/v1/vcsv1connect"
)

// fakeVcsServer is VcsService as the server provider uses it.
type fakeVcsServer struct {
	vcsv1connect.UnimplementedVcsServiceHandler
	mu          sync.Mutex
	connections []*vcsv1.UserConnection
	knowsSess   bool
	creates     []*vcsv1.CreatePullRequestRequest
	listErr     error
	prs         []*vcsv1.PullRequest
}

func (f *fakeVcsServer) ListUserConnections(context.Context, *connect.Request[vcsv1.ListUserConnectionsRequest]) (*connect.Response[vcsv1.ListUserConnectionsResponse], error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return connect.NewResponse(&vcsv1.ListUserConnectionsResponse{Connections: f.connections}), nil
}

func (f *fakeVcsServer) ListPullRequests(context.Context, *connect.Request[vcsv1.ListPullRequestsRequest]) (*connect.Response[vcsv1.ListPullRequestsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&vcsv1.ListPullRequestsResponse{PullRequests: f.prs}), nil
}

func (f *fakeVcsServer) GetPullRequest(_ context.Context, req *connect.Request[vcsv1.GetPullRequestRequest]) (*connect.Response[vcsv1.GetPullRequestResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, pr := range f.prs {
		if pr.GetNumber() == req.Msg.GetNumber() {
			return connect.NewResponse(&vcsv1.GetPullRequestResponse{PullRequest: pr}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, errors.New("no such pull request"))
}

func (f *fakeVcsServer) ListChecks(context.Context, *connect.Request[vcsv1.ListChecksRequest]) (*connect.Response[vcsv1.ListChecksResponse], error) {
	return connect.NewResponse(&vcsv1.ListChecksResponse{Checks: []*vcsv1.Check{{Id: "1", Name: "build", State: vcsv1.CheckState_CHECK_STATE_FAILURE}}}), nil
}

func (f *fakeVcsServer) ListReviewComments(context.Context, *connect.Request[vcsv1.ListReviewCommentsRequest]) (*connect.Response[vcsv1.ListReviewCommentsResponse], error) {
	return connect.NewResponse(&vcsv1.ListReviewCommentsResponse{Comments: []*vcsv1.ReviewComment{
		{Id: "r:1", Author: "ann", Body: "open", Path: "a.go", Line: 3},
		{Id: "r:2", Author: "ann", Body: "done", Path: "a.go", Line: 4, Resolved: true},
		{Id: "c:3", Author: "ann", Body: "chat"},
	}}), nil
}

func (f *fakeVcsServer) CreatePullRequest(_ context.Context, req *connect.Request[vcsv1.CreatePullRequestRequest]) (*connect.Response[vcsv1.CreatePullRequestResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Msg.GetSessionId() != "" && !f.knowsSess {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("Session s1 not found"))
	}
	f.creates = append(f.creates, req.Msg)
	pr := &vcsv1.PullRequest{RepositoryKey: req.Msg.GetRepositoryKey(), Number: 31, Url: "https://github.com/o/r/pull/31", Title: req.Msg.GetTitle(), State: agentv1.PullRequest_STATE_OPEN}
	f.prs = append(f.prs, pr)
	return connect.NewResponse(&vcsv1.CreatePullRequestResponse{PullRequest: pr}), nil
}

func startVcsServer(t *testing.T, f *fakeVcsServer) *ServerProvider {
	t.Helper()
	mux := http.NewServeMux()
	path, h := vcsv1connect.NewVcsServiceHandler(f)
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &ServerProvider{c: NewClient(srv.URL, srv.Client(), auth.StaticToken("hat_test"))}
}

func TestServerProviderClaimsOnlyWhatItCanAct(t *testing.T) {
	p := startVcsServer(t, &fakeVcsServer{connections: []*vcsv1.UserConnection{
		{ConnectionId: "a", Host: "GitHub.com", CanWrite: true, Login: "me"},
		{ConnectionId: "b", Host: "gitlab.example.com"},
	}})
	d, err := p.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !covers(d, "github.com") || covers(d, "gitlab.example.com") || d.GetHosts()[0].GetLogin() != "me" {
		t.Fatalf("hosts: %+v", d.GetHosts())
	}
}

func TestServerProviderStepsAsideWhenTheServerCannotHelp(t *testing.T) {
	p := startVcsServer(t, &fakeVcsServer{listErr: connect.NewError(connect.CodeUnimplemented, errors.New("old server"))})
	d, err := p.Detect(context.Background())
	if err != nil || len(d.GetHosts()) != 0 {
		t.Fatalf("hosts %+v err %v", d.GetHosts(), err)
	}
}

func TestCreatePRGoesThroughTheServerBeforeTheCLI(t *testing.T) {
	dir, _ := prRepo(t)
	gh, cli := githubProvider(t)
	f := &fakeVcsServer{connections: []*vcsv1.UserConnection{{Host: "github.com", CanWrite: true, Login: "me"}}}
	p := startVcsServer(t, f)
	tr := prTracker(t, dir, "https://github.com/o/r.git", p, gh)

	facts, err := tr.Facts(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Provider != "server" {
		t.Fatalf("provider %q", facts.Provider)
	}
	got, err := tr.CreatePR(context.Background(), CreatePRRequest{Title: "T", Body: "B", Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 31 || !got.Pushed {
		t.Fatalf("created: %+v", got)
	}
	for _, c := range cli.calls() {
		if strings.HasPrefix(c, "pr ") {
			t.Fatalf("the CLI was used: %s", c)
		}
	}
	if c := f.creates[0]; c.GetHeadBranch() != "feat" || c.GetBaseBranch() != "main" || !c.GetDraft() || c.GetSessionId() != "" {
		t.Fatalf("create: %+v (a session the server does not know is left out)", c)
	}
	info := tr.Info()
	if info == nil || info.GetNumber() != 31 {
		t.Fatalf("not bound: %+v", info)
	}
	// The binding reads back through the server too: its checks and open threads.
	if info.GetChecksFailed() != 1 || info.GetUnresolvedComments() != 1 {
		t.Fatalf("info: %+v", info)
	}
}

func TestCreatePRLinksASessionTheServerKnows(t *testing.T) {
	f := &fakeVcsServer{knowsSess: true}
	p := startVcsServer(t, f)
	repo := Repo{Key: "github.com/o/r", Host: "github.com", Path: "o/r"}
	if _, err := p.CreatePR(context.Background(), repo, PRInput{Head: "h", Base: "b", Title: "T", SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	if f.creates[0].GetSessionId() != "s1" {
		t.Fatalf("create: %+v", f.creates[0])
	}
}

func TestCLIStatus(t *testing.T) {
	gh, cli := githubProvider(t)
	f := newFakeEnv(t)
	missing := &GLab{Runner: f.runner()}
	p := startVcsServer(t, &fakeVcsServer{})
	out := CLIStatus(context.Background(), []Provider{p, gh, missing})
	if len(out) != 2 {
		t.Fatalf("the server is no CLI: %+v", out)
	}
	if out[0].GetName() != "gh" || !out[0].GetInstalled() || out[0].GetHosts()[0].GetLogin() != "me" {
		t.Fatalf("gh: %+v", out[0])
	}
	if out[1].GetName() != "glab" || out[1].GetInstalled() || out[1].GetLoginCommand() == "" {
		t.Fatalf("glab: %+v", out[1])
	}
	cli.set("version", "", "boom\n", 1)
	if got := CLIStatus(context.Background(), []Provider{gh}); !got[0].GetInstalled() || got[0].GetProblem() == "" {
		t.Fatalf("a CLI that fails: %+v", got[0])
	}
}
