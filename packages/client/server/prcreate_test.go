package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/server"
	"github.com/xlsft/harness/packages/client/vcs"
)

// creatingHost is a code host that opens PRs.
type creatingHost struct {
	fakePRHost
	created []vcs.PRInput
	failed  error
	createM sync.Mutex
}

func (c *creatingHost) Snapshot(ctx context.Context, repo vcs.Repo, branch string) (*vcs.Snapshot, error) {
	c.createM.Lock()
	defer c.createM.Unlock()
	if len(c.created) == 0 {
		return &vcs.Snapshot{}, nil
	}
	return c.fakePRHost.Snapshot(ctx, repo, branch)
}

func (c *creatingHost) CreatePR(_ context.Context, repo vcs.Repo, in vcs.PRInput) (*vcsv1.PullRequest, error) {
	c.createM.Lock()
	defer c.createM.Unlock()
	if c.failed != nil {
		return nil, c.failed
	}
	c.created = append(c.created, in)
	return &vcsv1.PullRequest{RepositoryKey: repo.Key, Number: 12, Url: prURL, Title: in.Title, State: agentv1.PullRequest_STATE_OPEN}, nil
}

// prWorkspace is a repository on a feature branch with an origin on a code host's URL (pushes go to a local bare one).
func prWorkspace(t *testing.T) string {
	t.Helper()
	dir := gittest.Init(t)
	bare := t.TempDir()
	gittest.Run(t, bare, "init", "-q", "--bare", "-b", "main")
	gittest.Run(t, dir, "remote", "add", "origin", "https://github.com/o/r.git")
	gittest.Run(t, dir, "config", "url."+bare+".pushInsteadOf", "https://github.com/o/r.git")
	gittest.Run(t, dir, "push", "-q", "origin", "main")
	gittest.Run(t, dir, "update-ref", "refs/remotes/origin/main", "main")
	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	gittest.Write(t, dir, "feature.txt", "new\n")
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "-q", "-m", "Add the feature")
	return dir
}

func TestCreatePRButtonFlow(t *testing.T) {
	dir := prWorkspace(t)
	host := &creatingHost{fakePRHost: fakePRHost{state: agentv1.PullRequest_STATE_OPEN, updatedAt: "t0"}}
	var draftReq *llm.Request
	var mu sync.Mutex
	script := func(n int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.RequestClass == "auxiliary" {
			mu.Lock()
			draftReq = r
			mu.Unlock()
			out, _ := json.Marshal(map[string]string{"title": "Add the feature", "body": "It adds the feature."})
			return fg.Reply{Events: fg.Message("m", fg.Text(string(out)))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	}
	h := startHarnessSrv(t, script, nil, func(o *bootstrap.Options) {
		o.VCSProviders, o.VCSSchedule = []vcs.Provider{host}, fastPolls
	}, func(*server.Server) {})
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: dir})

	draft, err := h.client.DraftPullRequest(h.ctx, connect.NewRequest(&agentv1.DraftPullRequestRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	d := draft.Msg
	if d.GetTitle() != "Add the feature" || d.GetBody() != "It adds the feature." || !d.GetDrafted() {
		t.Fatalf("draft: %+v", d)
	}
	if d.GetHeadBranch() != "feat" || d.GetBaseBranch() != "main" || d.GetCommits() != 1 || !d.GetNeedsPush() || d.GetProvider() != "gh" || d.GetRepositoryKey() != "github.com/o/r" {
		t.Fatalf("draft facts: %+v", d)
	}
	mu.Lock()
	sent := allText(draftReq)
	mu.Unlock()
	for _, want := range []string{"feat", "Add the feature", "feature.txt"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("the prompt misses %q:\n%s", want, sent)
		}
	}
	if len(draftReq.Tools) != 0 {
		t.Fatalf("drafting must offer no tools: %+v", draftReq.Tools)
	}

	created, err := h.client.CreatePullRequest(h.ctx, connect.NewRequest(&agentv1.CreatePullRequestRequest{SessionId: sid, Title: "Edited title", Body: "Edited body", Draft: true}))
	if err != nil {
		t.Fatal(err)
	}
	if created.Msg.GetNumber() != 12 || created.Msg.GetUrl() != prURL || !created.Msg.GetPushed() {
		t.Fatalf("created: %+v", created.Msg)
	}
	host.createM.Lock()
	in := host.created[0]
	host.createM.Unlock()
	if in.Title != "Edited title" || in.Body != "Edited body" || !in.Draft || in.Head != "feat" || in.Base != "main" || in.SessionID != sid {
		t.Fatalf("what the user kept did not arrive: %+v", in)
	}
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetSessionUpdated().GetPullRequest().GetNumber() == 12 })
}

func TestDraftPullRequestFallsBackToTheCommitsAndSaysSo(t *testing.T) {
	dir := prWorkspace(t)
	host := &creatingHost{fakePRHost: fakePRHost{state: agentv1.PullRequest_STATE_OPEN, updatedAt: "t0"}}
	script := func(int, *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m", fg.Text("I would rather chat about it"))}
	}
	h := startHarnessSrv(t, script, nil, func(o *bootstrap.Options) {
		o.VCSProviders = []vcs.Provider{host}
	}, func(*server.Server) {})
	sid, _ := h.create(&agentv1.CreateSessionRequest{Cwd: dir})
	draft, err := h.client.DraftPullRequest(h.ctx, connect.NewRequest(&agentv1.DraftPullRequestRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if d := draft.Msg; d.GetDrafted() || d.GetTitle() != "Add the feature" || !strings.Contains(d.GetNote(), "commit messages") {
		t.Fatalf("draft: %+v", d)
	}
}

func TestPullRequestRPCsRefuseWhatTheyCannotDo(t *testing.T) {
	dir := prWorkspace(t)
	host := &creatingHost{fakePRHost: fakePRHost{state: agentv1.PullRequest_STATE_OPEN, updatedAt: "t0"}, failed: errors.New("the host said no")}
	h := startHarnessSrv(t, prResponder, nil, func(o *bootstrap.Options) {
		o.VCSProviders = []vcs.Provider{host}
	}, func(*server.Server) {})
	if _, err := h.client.DraftPullRequest(h.ctx, connect.NewRequest(&agentv1.DraftPullRequestRequest{SessionId: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}
	sid, _ := h.create(&agentv1.CreateSessionRequest{Cwd: dir})
	gittest.Run(t, dir, "switch", "-q", "main")
	if _, err := h.client.DraftPullRequest(h.ctx, connect.NewRequest(&agentv1.DraftPullRequestRequest{SessionId: sid})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("on the base branch: %v", err)
	}
	gittest.Run(t, dir, "switch", "-q", "feat")
	_, err := h.client.CreatePullRequest(h.ctx, connect.NewRequest(&agentv1.CreatePullRequestRequest{SessionId: sid, Title: "x"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "the host said no") {
		t.Fatalf("host refusal: %v", err)
	}
	if _, err := h.client.CreatePullRequest(h.ctx, connect.NewRequest(&agentv1.CreatePullRequestRequest{SessionId: sid})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("no title: %v", err)
	}
}

func TestDeliverAutoFixJobWakesTheSessionAndReopensAClosedOne(t *testing.T) {
	dir := prWorkspace(t)
	host := &creatingHost{fakePRHost: fakePRHost{state: agentv1.PullRequest_STATE_OPEN, updatedAt: "t0"}}
	var srv *server.Server
	h := startHarnessSrv(t, prResponder, nil, func(o *bootstrap.Options) {
		o.VCSProviders = []vcs.Provider{host}
	}, func(s *server.Server) { srv = s })
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: dir})
	h.send(sid, "hello")
	h.until(ch, isTurnFinished)

	job := func(id, comment string) *agentv1.DeliverAutoFixJobRequest {
		return &agentv1.DeliverAutoFixJobRequest{SessionId: sid, Job: &agentv1.AutoFixJob{
			Id: id, Kind: agentv1.AutoFixJob_KIND_REVIEW_COMMENT, RepositoryKey: "github.com/o/r", PrNumber: 12, PrUrl: prURL,
			Comments: []*agentv1.AutoFixJob_Comment{{Author: "alice", Body: comment}},
		}}
	}
	if err := srv.DeliverAutoFixJob(h.ctx, job("afx_1", "please rename this")); err != nil {
		t.Fatal(err)
	}
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if ev.GetUserMessage() != nil {
			t.Fatal("a job read as a user message")
		}
		return isTurnFinished(ev)
	})
	reqs := h.gw.Requests()
	wake := allText(reqs[len(reqs)-1])
	for _, want := range []string{"#12", "please rename this", "<pr-external-data>"} {
		if !strings.Contains(wake, want) {
			t.Fatalf("the wake misses %q:\n%s", want, wake)
		}
	}
	n := len(reqs)

	// A redelivered job does nothing.
	if err := srv.DeliverAutoFixJob(h.ctx, job("afx_1", "please rename this")); err != nil {
		t.Fatal(err)
	}
	if got := len(h.gw.Requests()); got != n {
		t.Fatalf("a repeat woke the session: %d, was %d", got, n)
	}

	// A closed session reopens for the job.
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	if err := srv.DeliverAutoFixJob(h.ctx, job("afx_2", "and this one too")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		reqs := h.gw.Requests()
		return len(reqs) > n && strings.Contains(allText(reqs[len(reqs)-1]), "and this one too")
	})

	// An unknown session fails, so the server can mark the job.
	err := srv.DeliverAutoFixJob(h.ctx, &agentv1.DeliverAutoFixJobRequest{SessionId: "gone", Job: &agentv1.AutoFixJob{Id: "x"}})
	if err == nil {
		t.Fatal("a job for no session went nowhere without an error")
	}
	if err := srv.DeliverAutoFixJob(h.ctx, &agentv1.DeliverAutoFixJobRequest{SessionId: sid}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("no job: %v", err)
	}
}

func TestGetVcsStatusReportsTheCLIs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", filepath.Join(dir, "nothing-here"))
	h := startHarnessSrv(t, prResponder, nil, func(*bootstrap.Options) {}, func(*server.Server) {})
	res, err := h.client.GetVcsStatus(h.ctx, connect.NewRequest(&agentv1.GetVcsStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	clis := res.Msg.GetClis()
	if len(clis) != 2 || clis[0].GetName() != "gh" || clis[1].GetName() != "glab" {
		t.Fatalf("clis: %+v", clis)
	}
	if clis[0].GetInstalled() || clis[0].GetLoginCommand() != "gh auth login" {
		t.Fatalf("gh: %+v", clis[0])
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
