package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/server"
)

// fakeHosts lists one host of the user's.
type fakeHosts struct {
	relayv1connect.UnimplementedHostsServiceHandler
}

func (fakeHosts) ListAgentHosts(context.Context, *connect.Request[relayv1.ListAgentHostsRequest]) (*connect.Response[relayv1.ListAgentHostsResponse], error) {
	return connect.NewResponse(&relayv1.ListAgentHostsResponse{Hosts: []*relayv1.AgentHost{{Id: "ahs_box", Name: "box"}}}), nil
}

func startMoveHarness(t *testing.T) (*harness, *server.Server) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(relayv1connect.NewHostsServiceHandler(fakeHosts{}))
	core := httptest.NewServer(mux)
	t.Cleanup(core.Close)
	var srv *server.Server
	h := startHarnessSrv(t, fg.Sequence(say("done")), nil, nil, func(s *server.Server) {
		s.Core = &server.CoreProxy{URL: core.URL, HTTP: core.Client()}
		srv = s
	})
	return h, srv
}

func moveCodes(c *agentv1.SessionMoveCheck) string {
	var codes []string
	for _, p := range c.GetProblems() {
		codes = append(codes, p.GetCode())
	}
	return strings.Join(codes, ",")
}

// A move is refused, with its reasons, before anything is written (docs/design/hosts.md, decision 25).
func TestCheckSessionMoveRefusals(t *testing.T) {
	h, _ := startMoveHarness(t)

	// Not git.
	sid, _ := h.create(&agentv1.CreateSessionRequest{})
	res, err := h.client.CheckSessionMove(h.ctx, connect.NewRequest(&agentv1.CheckSessionMoveRequest{SessionId: sid, HostId: "ahs_box"}))
	if err != nil {
		t.Fatal(err)
	}
	if c := res.Msg.GetCheck(); c.GetOk() || !strings.Contains(moveCodes(c), "not_git") || !strings.Contains(moveCodes(c), "no_transcript") {
		t.Fatalf("not git: %v", c)
	}

	// A repository with a secret, no origin, and a host that is not the user's.
	repo := gittest.Init(t)
	gittest.Write(t, repo, ".env", "TOKEN=x\n")
	sid, _ = h.create(&agentv1.CreateSessionRequest{Cwd: repo})
	res, err = h.client.CheckSessionMove(h.ctx, connect.NewRequest(&agentv1.CheckSessionMoveRequest{SessionId: sid, HostId: "ahs_other"}))
	if err != nil {
		t.Fatal(err)
	}
	c := res.Msg.GetCheck()
	if codes := moveCodes(c); !strings.Contains(codes, "host") || !strings.Contains(codes, "no_remote") {
		t.Fatalf("codes = %s", codes)
	}

	// With an origin on a code host, the secret is named; the host is known.
	gittest.Run(t, repo, "remote", "add", "origin", "https://github.com/acme/widgets.git")
	gittest.Run(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	res, err = h.client.CheckSessionMove(h.ctx, connect.NewRequest(&agentv1.CheckSessionMoveRequest{SessionId: sid, HostId: "ahs_box"}))
	if err != nil {
		t.Fatal(err)
	}
	c = res.Msg.GetCheck()
	var secrets []string
	for _, p := range c.GetProblems() {
		if p.GetCode() == "secrets" {
			secrets = p.GetPaths()
		}
		if p.GetCode() == "host" || p.GetCode() == "no_remote" {
			t.Errorf("unexpected problem %v", p)
		}
	}
	if len(secrets) != 1 || secrets[0] != ".env" {
		t.Fatalf("secrets = %v (%v)", secrets, c)
	}
	if c.GetHostName() != "box" || c.GetBranch() != "harness/"+sid || c.GetBase() != "main" || c.GetRepositoryKey() != "github.com/acme/widgets" || c.GetUncommittedFiles() != 1 {
		t.Fatalf("check = %v", c)
	}

	// The move itself is refused with the same reasons.
	_, err = h.client.MoveSession(h.ctx, connect.NewRequest(&agentv1.MoveSessionRequest{SessionId: sid, HostId: "ahs_box"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), ".env") {
		t.Fatalf("move: %v", err)
	}
}

// A moved session is read-only here, and says where it went (decision 24).
func TestMovedSessionIsReadOnly(t *testing.T) {
	h, srv := startMoveHarness(t)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	srv.MarkMoved(sid, "box")
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetSessionMoved() != nil })
	_, err := h.client.SendUserMessage(h.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "u1", Content: text("hi")}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "moved to box") {
		t.Fatalf("send: %v", err)
	}
	if _, err := h.client.RunShellCommand(h.ctx, connect.NewRequest(&agentv1.RunShellCommandRequest{SessionId: sid, Command: "ls"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("shell: %v", err)
	}
	if _, err := h.client.Compact(h.ctx, connect.NewRequest(&agentv1.CompactRequest{SessionId: sid})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("compact: %v", err)
	}
	// It stays so when it opens again.
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	res, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid, Cwd: h.proj}))
	if err != nil {
		t.Fatal(err)
	}
	if mv := res.Msg.GetSession().GetMoved(); mv.GetHostName() != "box" || mv.GetBranch() != "harness/"+sid {
		t.Fatalf("moved = %v", mv)
	}
	_, err = h.client.SendUserMessage(h.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "u2", Content: text("hi")}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("send after reopening: %v", err)
	}
	// A fork of it is not read-only.
	fork, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid, Fork: true, Cwd: h.proj}))
	if err != nil {
		t.Fatal(err)
	}
	if fork.Msg.GetSession().GetMoved() != nil {
		t.Fatal("the fork is read-only too")
	}
}

// An attempt runs its group's test command when a turn ends, in its own folder, and shows the result (decision 30).
func TestAttemptRunsItsTestCommand(t *testing.T) {
	h, srv := startMoveHarness(t)
	repo := gittest.Init(t)
	res, err := srv.CreateSession(server.WithAttempt(h.ctx, server.AttemptSpec{GroupID: "atg_1", Index: 1, Count: 3, TestCommand: "cat README.md; exit 3"}),
		connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: repo}))
	if err != nil {
		t.Fatal(err)
	}
	info := res.Msg.GetSession()
	if a := info.GetAttempt(); a.GetGroupId() != "atg_1" || a.GetIndex() != 1 || a.GetCount() != 3 {
		t.Fatalf("attempt = %v", a)
	}
	ch := h.subscribe(info.GetId())
	h.send(info.GetId(), "go")
	var check *agentv1.AttemptCheck
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		check = ev.GetAttemptChecked()
		return check != nil
	})
	if check.GetExitCode() != 3 || !strings.Contains(check.GetOutput(), "hello") || check.GetHead() == "" || check.GetRefused() {
		t.Fatalf("check = %v", check)
	}
}
