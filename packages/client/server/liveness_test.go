package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/server"
	"github.com/xlsft/harness/packages/client/session"
)

// startBrowserServer serves AgentService on TCP (Connect over HTTP/1.1, as
// connect-web in a browser or WebView uses it) and returns a client.
func startBrowserServer(t *testing.T, url string, heartbeat time.Duration) (*server.Server, agentv1connect.AgentServiceClient) {
	t.Helper()
	tok := auth.StaticToken("hat_test")
	srv := server.New(bootstrap.Options{Gateway: connectgw.New(url, nil, tok), Prompts: prompts.NewConnectSource(url, nil, tok)})
	srv.NoAITitles = true
	srv.Heartbeat = heartbeat
	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- srv.ServeTCP(ctx, server.TCPOptions{Addr: "127.0.0.1:0", Token: testToken, AllowedOrigins: server.DefaultCORSOrigins,
			OnListen: func(a net.Addr) { addrCh <- a }})
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	select {
	case a := <-addrCh:
		return srv, browserClient("http://"+a.String(), func(p *http.Protocols) { p.SetHTTP1(true) })
	case err := <-done:
		t.Fatalf("serve: %v", err)
	}
	return nil, nil
}

func livenessEnv(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
}

// recv reads the next stream message, failing after d.
func recv(t *testing.T, stream *connect.ServerStreamForClient[agentv1.SubscribeResponse], d time.Duration) *agentv1.SubscribeResponse {
	t.Helper()
	ch := make(chan bool, 1)
	go func() { ch <- stream.Receive() }()
	select {
	case ok := <-ch:
		if !ok {
			t.Fatalf("stream ended: %v", stream.Err())
		}
		return stream.Msg()
	case <-time.After(d):
		t.Fatalf("nothing arrived within %s", d)
	}
	return nil
}

// With control, a stream starts with the log id, follows the replay with the
// session as it is now, and then beats; every event frame (also the last one
// of a turn, with nothing after it) reaches the frontend at once.
func TestSubscribeControlMessagesAndFlush(t *testing.T) {
	livenessEnv(t)
	_, url := fg.Start(t, fg.Sequence(func(r *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m1", fg.Text("Hello."))} }))
	_, client := startBrowserServer(t, url, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	cs, err := client.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: proj}))
	if err != nil {
		t.Fatal(err)
	}
	sid := cs.Msg.Session.Id
	stream, err := client.Subscribe(ctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid, Control: true}))
	if err != nil {
		t.Fatal(err)
	}
	hello := recv(t, stream, 2*time.Second)
	if hello.Event != nil || !strings.HasPrefix(hello.LogId, "log_") || hello.Session != nil {
		t.Fatalf("first message must be the log id: %+v", hello)
	}
	var lastSeq int64
	for {
		m := recv(t, stream, 2*time.Second)
		if m.Event != nil {
			lastSeq = m.Event.Seq
			continue
		}
		if m.LogId != hello.LogId || m.Session == nil || m.Session.Status != agentv1.SessionStatus_SESSION_STATUS_IDLE {
			t.Fatalf("caught-up message: %+v", m)
		}
		break
	}
	if _, err := client.SendUserMessage(ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "u1", Content: text("hi")})); err != nil {
		t.Fatal(err)
	}
	for {
		m := recv(t, stream, 5*time.Second)
		if m.GetEvent().GetTurnFinished() != nil {
			break
		}
	}
	// Interrupt with nothing running reports idle and publishes SessionUpdated:
	// the last frame, with nothing behind it to push it through, still arrives.
	time.Sleep(200 * time.Millisecond)
	ir, err := client.Interrupt(ctx, connect.NewRequest(&agentv1.InterruptRequest{SessionId: sid}))
	if err != nil || !ir.Msg.Idle {
		t.Fatalf("interrupt when idle: %v %+v", err, ir)
	}
	for {
		m := recv(t, stream, 2*time.Second)
		if u := m.GetEvent().GetSessionUpdated(); u != nil && u.Status == agentv1.SessionStatus_SESSION_STATUS_IDLE {
			break
		}
	}

	// A resubscription with the seqs of the same log continues it.
	again, err := client.Subscribe(ctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid, AfterSeq: lastSeq, Control: true}))
	if err != nil {
		t.Fatal(err)
	}
	if m := recv(t, again, 2*time.Second); m.LogId != hello.LogId {
		t.Fatalf("same log expected: %q vs %q", m.LogId, hello.LogId)
	}
	if m := recv(t, again, 2*time.Second); m.GetEvent().GetSeq() != lastSeq+1 {
		t.Fatalf("replay must continue after %d: %+v", lastSeq, m)
	}

	// Without control nothing changes for older frontends: events only.
	plain, err := client.Subscribe(ctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if m := recv(t, plain, 2*time.Second); m.Event == nil || m.LogId != "" {
		t.Fatalf("plain stream got a control message: %+v", m)
	}
}

func TestSubscribeHeartbeats(t *testing.T) {
	livenessEnv(t)
	_, url := fg.Start(t, nil)
	_, client := startBrowserServer(t, url, 100*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	cs, err := client.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: proj}))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Subscribe(ctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: cs.Msg.Session.Id, Control: true}))
	if err != nil {
		t.Fatal(err)
	}
	beats := 0
	for beats < 3 {
		m := recv(t, stream, time.Second)
		if m.Event == nil && m.Session == nil && m.LogId != "" {
			beats++
		}
	}
}

// The client stopped in the middle of a turn (here: during a tool call). The
// reopened session is a new event log, its replay ends the lost turn as
// interrupted, its status is idle, and the next turn works.
func TestRestartMidTurnReopensIdle(t *testing.T) {
	livenessEnv(t)
	gw, url := fg.Start(t, fg.Sequence(
		func(r *llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("t_wait", "Read", map[string]any{"file_path": filepath.Join(fg.Cwd(r), "x")}))}
		},
	))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	proj, _ := filepath.EvalSymlinks(t.TempDir())

	_, first := startBrowserServer(t, url, time.Hour)
	cs, err := first.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: proj}))
	if err != nil {
		t.Fatal(err)
	}
	sid := cs.Msg.Session.Id
	s1, err := first.Subscribe(ctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid, Control: true}))
	if err != nil {
		t.Fatal(err)
	}
	oldLog := recv(t, s1, 2*time.Second).LogId
	if _, err := first.SendUserMessage(ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "u1", Content: text("go")})); err != nil {
		t.Fatal(err)
	}
	var oldSeq int64
	for {
		m := recv(t, s1, 5*time.Second)
		if m.Event != nil {
			oldSeq = m.Event.Seq
		}
		if m.GetEvent().GetTurnFinished() != nil {
			break
		}
	}
	// Make the transcript look like the process died while the tool ran.
	path := filepath.Join(session.Store{Root: config.ProjectsDir()}.ProjectDir(proj), sid+".jsonl")
	cutAfterToolUse(t, path)

	// A new process: the frontend still holds seqs of the old log.
	_, second := startBrowserServer(t, url, time.Hour)
	if _, err := second.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: proj, ResumeSessionId: sid})); err != nil {
		t.Fatal(err)
	}
	s2, err := second.Subscribe(ctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid, AfterSeq: oldSeq, Control: true}))
	if err != nil {
		t.Fatal(err)
	}
	if m := recv(t, s2, 2*time.Second); m.LogId == "" || m.LogId == oldLog {
		t.Fatalf("a reopened session must start a new log: %q (old %q)", m.LogId, oldLog)
	}
	// The frontend resets and reads the new log from the start.
	s3, err := second.Subscribe(ctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid, Control: true}))
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s3, 2*time.Second)
	var finished *agentv1.TurnFinished
	var started bool
	for {
		m := recv(t, s3, 2*time.Second)
		if m.Event == nil {
			if m.Session.GetStatus() != agentv1.SessionStatus_SESSION_STATUS_IDLE {
				t.Fatalf("reopened session status %v", m.Session.GetStatus())
			}
			break
		}
		if m.Event.GetTurnStarted() != nil {
			started, finished = true, nil
		}
		if f := m.Event.GetTurnFinished(); f != nil {
			finished = f
		}
	}
	if !started || finished == nil || finished.Result != agentv1.TurnFinished_RESULT_INTERRUPTED {
		t.Fatalf("the lost turn must be replayed as interrupted: started=%v finished=%+v", started, finished)
	}
	if _, err := second.SendUserMessage(ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "u2", Content: text("again")})); err != nil {
		t.Fatal(err)
	}
	for {
		m := recv(t, s3, 5*time.Second)
		if f := m.GetEvent().GetTurnFinished(); f != nil {
			if f.Result != agentv1.TurnFinished_RESULT_SUCCESS {
				t.Fatalf("next turn: %+v", f)
			}
			break
		}
	}
	reqs := gw.Requests()
	last := reqs[len(reqs)-1].Messages
	for i, m := range last {
		for _, u := range m.ToolUses() {
			paired := false
			if i+1 < len(last) {
				for _, b := range last[i+1].Content {
					paired = paired || (b.Type == llm.BlockToolResult && b.ToolUseID == u.ID)
				}
			}
			if !paired {
				t.Fatalf("tool_use %s sent without a result", u.ID)
			}
		}
	}
}

// cutAfterToolUse truncates a transcript right after its first assistant
// entry with a tool_use.
func cutAfterToolUse(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		keep = append(keep, sc.Text())
		var e session.Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Type == session.TypeAssistant && len((llm.Message{Content: e.Blocks()}).ToolUses()) > 0 {
			break
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(keep, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
