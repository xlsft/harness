package server_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// A session on an ACP agent lists the agent's commands, which go to the agent, and the built-ins that act on the session.
func TestAcpSlashCommands(t *testing.T) {
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

	srv := server.New(bootstrap.Options{SkillHome: t.TempDir()})
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
	inst, err := client.InstallAcpAgent(ctx, connect.NewRequest(&agentv1.InstallAcpAgentRequest{AgentId: "fake"}))
	if err != nil {
		t.Fatal(err)
	}
	for inst.Receive() {
	}
	if inst.Err() != nil {
		t.Fatal(inst.Err())
	}
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	writeFile(t, filepath.Join(proj, ".claude/skills/local/SKILL.md"), "A skill of the client's own loop")
	cs, err := client.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: proj, Engine: "acp:fake"}))
	if err != nil {
		t.Fatal(err)
	}
	sid := cs.Msg.GetSession().GetId()

	// The agent advertises its commands after the session opens.
	var list map[string]*agentv1.SlashCommand
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err := client.ListSlashCommands(ctx, connect.NewRequest(&agentv1.ListSlashCommandsRequest{SessionId: sid}))
		if err != nil {
			t.Fatal(err)
		}
		list = map[string]*agentv1.SlashCommand{}
		for _, c := range res.Msg.GetCommands() {
			list[c.GetName()] = c
		}
		if list["review"] != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c := list["review"]; c == nil || c.GetKind() != agentv1.SlashCommandKind_SLASH_COMMAND_KIND_AGENT || c.GetSource() != "agent" || c.GetSwitchable() {
		t.Fatalf("%+v", c)
	}
	if list["clear"] == nil || list["model"] == nil {
		t.Fatalf("built-ins: %+v", list)
	}
	if list["compact"] != nil || list["init"] != nil || list["local"] != nil {
		t.Fatalf("what the client's own loop does isn't on an agent: %+v", list)
	}

	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	stream, err := client.Subscribe(sctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	send := func(id, msg string) {
		t.Helper()
		if _, err := client.SendUserMessage(ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: id, Content: text(msg)})); err != nil {
			t.Fatal(err)
		}
	}
	result := func() *agentv1.SlashCommandResult {
		t.Helper()
		for stream.Receive() {
			if r := stream.Msg().GetEvent().GetSlashCommandResult(); r != nil {
				return r
			}
		}
		t.Fatal("stream ended")
		return nil
	}
	send("a", "/model")
	if r := result(); r.GetUuid() != "a" || r.GetAction() != agentv1.SlashCommandResult_ACTION_PICK_MODEL {
		t.Fatalf("%+v", r)
	}
	send("b", "/help")
	if r := result(); r.GetText() == "" {
		t.Fatalf("%+v", r)
	}
	send("c", "/clear")
	r := result()
	if r.GetAction() != agentv1.SlashCommandResult_ACTION_OPEN_SESSION || r.GetNewSessionId() == "" {
		t.Fatalf("%+v", r)
	}
	next, err := client.ListSessions(ctx, connect.NewRequest(&agentv1.ListSessionsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	engine := ""
	for _, s := range next.Msg.GetSessions() {
		if s.GetId() == r.GetNewSessionId() {
			engine = s.GetEngine()
		}
	}
	if engine != "acp:fake" {
		t.Fatalf("the new session runs on the same agent: %q", engine)
	}

	// The agent's own command goes to it, as the prompt's text.
	send("d", "/review the diff")
	for stream.Receive() {
		ev := stream.Msg().GetEvent()
		if ev.GetSlashCommandResult() != nil {
			t.Fatal("the agent's command was taken for a built-in")
		}
		if ev.GetTurnFinished() != nil {
			break
		}
	}
	scancel()
	if _, err := client.CloseSession(ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CloseSession(ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: r.GetNewSessionId()})); err != nil {
		t.Fatal(err)
	}
}
