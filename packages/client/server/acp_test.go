package server_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/acp/registry"
	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/server"
)

var (
	fakeOnce sync.Once
	fakeBin  string
	fakeErr  error
)

// fakeAgent builds the test ACP agent (packages/client/acp/internal/fakeagent) once.
func fakeAgent(t *testing.T) string {
	t.Helper()
	fakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "acp-fake-")
		if err != nil {
			fakeErr = err
			return
		}
		fakeBin = filepath.Join(dir, "fakeagent")
		out, err := exec.Command("go", "build", "-o", fakeBin, "../acp/internal/fakeagent").CombinedOutput()
		if err != nil {
			fakeErr = fmt.Errorf("%v: %s", err, out)
		}
	})
	if fakeErr != nil {
		t.Fatal(fakeErr)
	}
	return fakeBin
}

func TestAcpSessionEndToEnd(t *testing.T) {
	bin := fakeAgent(t)
	home := t.TempDir()
	t.Setenv("HARNESS_HOME", home)
	t.Setenv("HARNESS_KEYRING", "0")
	body, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	var reg *httptest.Server
	reg = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/registry.json":
			fmt.Fprintf(w, `{"version":"1","agents":[{"id":"fake","name":"Fake Agent","version":"1.0.0","description":"test",
			  "distribution":{"binary":{%q:{"archive":"%s/fakeagent","cmd":"./fakeagent"}}}}]}`, registry.Platform(), reg.URL)
		case "/fakeagent":
			w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer reg.Close()

	srv := server.New(bootstrap.Options{})
	srv.SetAcpRegistry(&registry.Manager{Dir: filepath.Join(home, "acp"), URL: reg.URL + "/registry.json"})
	socket := shortSocket(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.ServeUnix(ctx, socket)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	client := agentv1connect.NewAgentServiceClient(rpc.UnixHTTPClient(socket), "http://agent", connect.WithGRPC())

	// Registry and install.
	list, err := client.ListAcpAgents(ctx, connect.NewRequest(&agentv1.ListAcpAgentsRequest{}))
	if err != nil || len(list.Msg.Agents) != 1 || list.Msg.Agents[0].Installed || !list.Msg.Agents[0].Available {
		t.Fatalf("list: %v %v", err, list)
	}
	inst, err := client.InstallAcpAgent(ctx, connect.NewRequest(&agentv1.InstallAcpAgentRequest{AgentId: "fake"}))
	if err != nil {
		t.Fatal(err)
	}
	var last *agentv1.InstallAcpAgentResponse
	for inst.Receive() {
		last = inst.Msg()
	}
	if inst.Err() != nil || last == nil || !last.Done || !last.Agent.Installed || last.Agent.InstalledVersion != "1.0.0" {
		t.Fatalf("install: %v %v", inst.Err(), last)
	}

	// A session on the agent.
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	cs, err := client.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: proj, Engine: "acp:fake"}))
	if err != nil {
		t.Fatal(err)
	}
	info := cs.Msg.Session
	if info.Engine != "acp:fake" || len(info.UnsupportedFeatures) == 0 || info.Acp.GetAgentName() != "Fake Agent" {
		t.Fatalf("session: %v", info)
	}
	sid := info.Id
	sctx, scancel := context.WithCancel(ctx)
	stream, err := client.Subscribe(sctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendUserMessage(ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "u1", Content: text("tool")})); err != nil {
		t.Fatal(err)
	}
	var finished *agentv1.TurnFinished
	tools := 0
	for finished == nil && stream.Receive() {
		switch e := stream.Msg().Event.Event.(type) {
		case *agentv1.SessionEvent_PermissionRequest:
			if _, err := client.RespondToPermission(ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{
				SessionId: sid, RequestId: e.PermissionRequest.RequestId, Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW_ALWAYS})); err != nil {
				t.Fatal(err)
			}
		case *agentv1.SessionEvent_ToolCallFinished:
			tools++
		case *agentv1.SessionEvent_TurnFinished:
			finished = e.TurnFinished
		}
	}
	scancel()
	if finished == nil || finished.Result != agentv1.TurnFinished_RESULT_SUCCESS || tools != 2 {
		t.Fatalf("turn: %v tools=%d err=%v", finished, tools, stream.Err())
	}
	if b, _ := os.ReadFile(filepath.Join(proj, "out.txt")); string(b) != "one\ntwo\n" {
		t.Fatalf("out.txt: %q", b)
	}

	// Harness-only features fail cleanly.
	if _, err := client.Rewind(ctx, connect.NewRequest(&agentv1.RewindRequest{SessionId: sid, CheckpointId: "x", Mode: agentv1.RewindRequest_MODE_CODE})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("rewind: %v", err)
	}
	if _, err := client.Compact(ctx, connect.NewRequest(&agentv1.CompactRequest{SessionId: sid})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("compact: %v", err)
	}
	if _, err := client.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid, Fork: true})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("fork: %v", err)
	}
	// The file pane sees the session's folder.
	roots, err := client.ListFileRoots(ctx, connect.NewRequest(&agentv1.ListFileRootsRequest{SessionId: sid}))
	if err != nil || len(roots.Msg.Roots) == 0 || roots.Msg.Roots[0].Path != proj {
		t.Fatalf("file roots: %v %v (want %s)", err, roots.Msg, proj)
	}
	// Listed with its engine.
	ls, err := client.ListSessions(ctx, connect.NewRequest(&agentv1.ListSessionsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range ls.Msg.Sessions {
		found = found || (s.Id == sid && s.Engine == "acp:fake" && s.Title == "tool")
	}
	if !found {
		t.Fatalf("not listed: %v", ls.Msg.Sessions)
	}

	// Close and resume: the history comes back, the agent starts fresh (no session/load).
	if _, err := client.CloseSession(ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid})); err != nil {
		t.Fatal(err)
	}
	sctx, scancel = context.WithCancel(ctx)
	defer scancel()
	stream, err = client.Subscribe(sctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	sawUser, sawFresh := false, false
	deadline := time.AfterFunc(10*time.Second, scancel)
	defer deadline.Stop()
	for !(sawUser && sawFresh) && stream.Receive() {
		ev := stream.Msg().Event
		if ev.GetUserMessage().GetUuid() == "u1" {
			sawUser = true
		}
		if ev.GetNotice().GetKind() == "acp_fresh_session" {
			sawFresh = true
		}
	}
	if !sawUser || !sawFresh {
		t.Fatalf("resume: user=%v fresh=%v", sawUser, sawFresh)
	}

	list, err = client.ListAcpAgents(ctx, connect.NewRequest(&agentv1.ListAcpAgentsRequest{}))
	if err != nil || list.Msg.Agents[0].AuthState != agentv1.AcpAuthState_ACP_AUTH_STATE_NOT_REQUIRED || len(list.Msg.Agents[0].AuthMethods) != 2 {
		t.Fatalf("auth state: %v %v", err, list)
	}

	if _, err := client.DeleteSession(ctx, connect.NewRequest(&agentv1.DeleteSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "acp", "sessions", sid)); !os.IsNotExist(err) {
		t.Fatalf("session record kept: %v", err)
	}
	if _, err := client.UninstallAcpAgent(ctx, connect.NewRequest(&agentv1.UninstallAcpAgentRequest{AgentId: "fake"})); err != nil {
		t.Fatal(err)
	}
}

func TestAcpEngineErrors(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	srv := server.New(bootstrap.Options{})
	defer srv.Close()
	path, h := srv.Handler()
	mux := http.NewServeMux()
	mux.Handle(path, h)
	hs := httptest.NewServer(mux)
	defer hs.Close()
	client := agentv1connect.NewAgentServiceClient(hs.Client(), hs.URL)
	ctx := context.Background()
	if _, err := client.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: t.TempDir(), Engine: "acp:missing"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("not installed: %v", err)
	}
	if _, err := client.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: t.TempDir(), Engine: "bogus"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bogus engine: %v", err)
	}
}
