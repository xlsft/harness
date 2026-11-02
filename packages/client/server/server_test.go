package server_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/server"
)

// shortSocket returns a socket path short enough for macOS (104 bytes).
func shortSocket(t *testing.T) string {
	dir, err := os.MkdirTemp("/tmp", "hs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "a.sock")
}

func text(s string) []*llmv1.ContentBlock {
	return []*llmv1.ContentBlock{{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{Text: s}}}}
}

func TestAgentServiceOverUnixSocket(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	var target string
	gw, url := fg.Start(t, fg.Sequence(
		func(r *llm.Request) fg.Reply {
			target = filepath.Join(fg.Cwd(r), "hello.txt")
			return fg.Reply{Events: fg.Message("m1", fg.Text("Writing the file."), fg.ToolUse("t1", "Write", map[string]any{"file_path": target, "content": "hello\n"}))}
		},
		func(r *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m2", fg.Text("Done."))} },
	))
	tok := auth.StaticToken("hat_test")
	srv := server.New(bootstrap.Options{Gateway: connectgw.New(url, nil, tok), Prompts: prompts.NewConnectSource(url, nil, tok)})
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

	init, err := client.Initialize(ctx, connect.NewRequest(&agentv1.InitializeRequest{Frontend: "test"}))
	if err != nil || len(init.Msg.ModelIds) != 1 {
		t.Fatalf("initialize: %v %+v", err, init)
	}
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	cs, err := client.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: proj}))
	if err != nil {
		t.Fatal(err)
	}
	sid := cs.Msg.Session.Id

	sctx, scancel := context.WithCancel(ctx)
	stream, err := client.Subscribe(sctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendUserMessage(ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "u1", Content: text("write hello.txt")})); err != nil {
		t.Fatal(err)
	}
	var events []*agentv1.SessionEvent
	var finished *agentv1.TurnFinished
	sawDelta := false
	for finished == nil && stream.Receive() {
		ev := stream.Msg().Event
		events = append(events, ev)
		switch e := ev.Event.(type) {
		case *agentv1.SessionEvent_PermissionRequest:
			if e.PermissionRequest.ToolName != "Write" {
				t.Fatalf("permission for %s", e.PermissionRequest.ToolName)
			}
			_, err := client.RespondToPermission(ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{
				SessionId: sid, RequestId: e.PermissionRequest.RequestId, Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW,
			}))
			if err != nil {
				t.Fatal(err)
			}
		case *agentv1.SessionEvent_AssistantDelta:
			sawDelta = true
		case *agentv1.SessionEvent_TurnFinished:
			finished = e.TurnFinished
		}
	}
	if finished == nil || finished.Result != agentv1.TurnFinished_RESULT_SUCCESS || finished.NumTurns != 2 {
		t.Fatalf("turn finished: %+v (err %v)", finished, stream.Err())
	}
	if data, _ := os.ReadFile(target); string(data) != "hello\n" {
		t.Fatalf("file %q", data)
	}
	kinds := map[string]bool{}
	for i, ev := range events {
		if i > 0 && ev.Seq != events[i-1].Seq+1 {
			t.Fatalf("seq gap at %d: %d after %d", i, ev.Seq, events[i-1].Seq)
		}
		switch ev.Event.(type) {
		case *agentv1.SessionEvent_UserMessage:
			kinds["user"] = true
		case *agentv1.SessionEvent_AssistantMessage:
			kinds["assistant"] = true
		case *agentv1.SessionEvent_ToolCallStarted:
			kinds["tool_started"] = true
		case *agentv1.SessionEvent_ToolCallFinished:
			kinds["tool_finished"] = true
		case *agentv1.SessionEvent_SessionUpdated:
			kinds["session"] = true
		}
	}
	for _, k := range []string{"user", "assistant", "tool_started", "tool_finished", "session"} {
		if !kinds[k] {
			t.Fatalf("missing %s event; got %v", k, kinds)
		}
	}
	if !sawDelta {
		t.Fatal("no assistant deltas streamed")
	}
	scancel()

	// Resubscribe from the middle: replay continues exactly after after_seq.
	mid := events[len(events)/2].Seq
	rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	defer rcancel()
	replay, err := client.Subscribe(rctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid, AfterSeq: mid}))
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Receive() || replay.Msg().Event.Seq != mid+1 {
		t.Fatalf("replay start: %v", replay.Err())
	}
	rcancel()

	// Mode / model commands.
	if _, err := client.SetPermissionMode(ctx, connect.NewRequest(&agentv1.SetPermissionModeRequest{SessionId: sid, Mode: agentv1.PermissionMode_PERMISSION_MODE_PLAN})); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetModel(ctx, connect.NewRequest(&agentv1.SetModelRequest{SessionId: sid, Model: "nope"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown model: %v", err)
	}
	list, err := client.ListSessions(ctx, connect.NewRequest(&agentv1.ListSessionsRequest{Cwd: proj}))
	if err != nil || len(list.Msg.Sessions) != 1 || list.Msg.Sessions[0].PermissionMode != agentv1.PermissionMode_PERMISSION_MODE_PLAN || list.Msg.Sessions[0].Title != "write hello.txt" {
		t.Fatalf("list: %v %+v", err, list)
	}
	if _, err := client.AnswerQuestion(ctx, connect.NewRequest(&agentv1.AnswerQuestionRequest{SessionId: sid})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if n := len(gw.Requests()); n != 2 {
		t.Fatalf("gateway requests %d", n)
	}
}

func TestQueueAndNowDelivery(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	_, url := fg.Start(t, func(n int, r *llm.Request) fg.Reply {
		last := r.Messages[len(r.Messages)-1]
		if last.Content[len(last.Content)-1].Text == "start" {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("b", "Bash", map[string]any{"command": "sleep 30"}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ack"))}
	})
	tok := auth.StaticToken("hat_test")
	srv := server.New(bootstrap.Options{Gateway: connectgw.New(url, nil, tok), Prompts: prompts.NewConnectSource(url, nil, tok), AllowedTools: []string{"Bash"}})
	socket := shortSocket(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.ServeUnix(ctx, socket)
	time.Sleep(100 * time.Millisecond)
	client := agentv1connect.NewAgentServiceClient(rpc.UnixHTTPClient(socket), "http://agent", connect.WithGRPC())
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	cs, err := client.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: proj}))
	if err != nil {
		t.Fatal(err)
	}
	sid := cs.Msg.Session.Id
	stream, _ := client.Subscribe(ctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid}))
	client.SendUserMessage(ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Content: text("start")}))
	var results []agentv1.TurnFinished_Result
	start := time.Now()
	for len(results) < 2 && stream.Receive() {
		switch e := stream.Msg().Event.Event.(type) {
		case *agentv1.SessionEvent_ToolCallStarted:
			client.SendUserMessage(ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "q1", Content: text("stop that"), Delivery: agentv1.SendUserMessageRequest_DELIVERY_NOW}))
		case *agentv1.SessionEvent_QueueChanged:
			_ = e
		case *agentv1.SessionEvent_TurnFinished:
			results = append(results, e.TurnFinished.Result)
		}
	}
	if len(results) != 2 || results[0] != agentv1.TurnFinished_RESULT_INTERRUPTED || results[1] != agentv1.TurnFinished_RESULT_SUCCESS || time.Since(start) > 10*time.Second {
		t.Fatalf("NOW delivery: %v after %s", results, time.Since(start))
	}
}
