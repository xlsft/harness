// Command relayviewer is the viewer half of the remote-control end-to-end
// test (scripts/e2e/run.sh). A local frontend opens a session on a running
// `harness-client serve`; everything else goes through the harness server's
// RelayService as a web viewer would: wait for the session to be mirrored,
// subscribe, send the prompt, answer the permission prompt, and check that the
// turn's events arrive in order (history, FlushComplete, then live, no gaps).
//
// Usage: relayviewer --socket <agent.sock> --cwd <project> [--server <url>] [--model <id>]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "relayviewer:", err)
		os.Exit(1)
	}
	fmt.Println("relayviewer: ok")
}

func run() error {
	socket := flag.String("socket", "", "AgentService unix socket of harness-client serve")
	cwd := flag.String("cwd", "", "project directory for the session")
	server := flag.String("server", "", "harness server URL (default: stored credentials)")
	model := flag.String("model", "", "model id")
	flag.Parse()
	if *socket == "" || *cwd == "" {
		return errors.New("--socket and --cwd are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	tokens, url, _, err := bootstrap.Tokens(*server)
	if err != nil {
		return err
	}
	relay := relayv1connect.NewRelayServiceClient(rpc.HTTPClient(url), url, rpc.ClientOptions()...)
	auth := func(h interface{ Set(string, string) }) error {
		t, err := tokens.Token(ctx)
		if err != nil {
			return err
		}
		h.Set("Authorization", "Bearer "+t)
		h.Set("X-Harness-Frontend", "e2e")
		return nil
	}

	// The local frontend opens a session in default mode, so the Bash call needs a permission answer.
	agent := agentv1connect.NewAgentServiceClient(rpc.UnixHTTPClient(*socket), "http://local", rpc.ClientOptions()...)
	created, err := agent.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{
		Cwd: *cwd, Model: *model, PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_DEFAULT,
	}))
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	sid := created.Msg.GetSession().GetId()

	// Wait until the host mirrors it.
	var hostID string
	for hostID == "" {
		req := connect.NewRequest(&relayv1.ListHostsRequest{})
		if err := auth(req.Header()); err != nil {
			return err
		}
		res, err := relay.ListHosts(ctx, req)
		if err != nil {
			return fmt.Errorf("list hosts: %w", err)
		}
		for _, h := range res.Msg.GetHosts() {
			for _, s := range h.GetSessions() {
				if s.GetId() == sid && h.GetOnline() {
					hostID = h.GetHostId()
				}
			}
		}
		if hostID == "" {
			select {
			case <-ctx.Done():
				return fmt.Errorf("session %s never appeared in ListHosts", sid)
			case <-time.After(200 * time.Millisecond):
			}
		}
	}

	// Bypass mode can never be enabled remotely.
	bypass := connect.NewRequest(&relayv1.SetPermissionModeRequest{Request: &agentv1.SetPermissionModeRequest{
		SessionId: sid, Mode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS}})
	if err := auth(bypass.Header()); err != nil {
		return err
	}
	if _, err := relay.SetPermissionMode(ctx, bypass); connect.CodeOf(err) != connect.CodePermissionDenied {
		return fmt.Errorf("bypass via relay: want PERMISSION_DENIED, got %v", err)
	}

	sub := connect.NewRequest(&relayv1.SubscribeSessionRequest{SessionId: sid, ViewerId: "e2e-" + uuid.NewString()})
	if err := auth(sub.Header()); err != nil {
		return err
	}
	stream, err := relay.SubscribeSession(ctx, sub)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	defer stream.Close()

	send := func() error {
		req := connect.NewRequest(&relayv1.SendUserMessageRequest{
			Request: &agentv1.SendUserMessageRequest{SessionId: sid, Uuid: uuid.NewString(), Content: []*llmv1.ContentBlock{
				{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{Text: "create out.txt with a marker"}}}}},
			Options: &relayv1.CommandOptions{WaitForAck: true, Frontend: "e2e"},
		})
		if err := auth(req.Header()); err != nil {
			return err
		}
		res, err := relay.SendUserMessage(ctx, req)
		if err != nil {
			return fmt.Errorf("send via relay: %w", err)
		}
		if r := res.Msg.GetResult(); r.GetDelivery() != relayv1.DeliveryState_DELIVERY_STATE_ACKED || r.GetAck().GetOutcome() != relayv1.CommandAck_OUTCOME_OK {
			return fmt.Errorf("send via relay: delivery %v, ack %v", r.GetDelivery(), r.GetAck())
		}
		return nil
	}

	var last int64
	flushed, sent, sawTool, sawPermission := false, false, false, false
	for stream.Receive() {
		switch ev := stream.Msg().GetEvent().(type) {
		case *relayv1.SubscribeSessionResponse_FlushComplete:
			flushed = true
			if ev.FlushComplete.GetLastSeq() != last {
				return fmt.Errorf("FlushComplete.last_seq %d, replay ended at %d", ev.FlushComplete.GetLastSeq(), last)
			}
			if !sent {
				sent = true
				if err := send(); err != nil {
					return err
				}
			}
		case *relayv1.SubscribeSessionResponse_SessionEvent:
			e := ev.SessionEvent
			if _, delta := e.GetEvent().(*agentv1.SessionEvent_AssistantDelta); delta {
				if !flushed {
					return errors.New("delta before FlushComplete")
				}
				continue
			}
			if e.GetSeq() != last+1 {
				return fmt.Errorf("durable seq %d after %d (gap or duplicate)", e.GetSeq(), last)
			}
			last = e.GetSeq()
			switch x := e.GetEvent().(type) {
			case *agentv1.SessionEvent_PermissionRequest:
				sawPermission = true
				req := connect.NewRequest(&relayv1.RespondToPermissionRequest{Request: &agentv1.RespondToPermissionRequest{
					SessionId: sid, RequestId: x.PermissionRequest.GetRequestId(), Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW}})
				if err := auth(req.Header()); err != nil {
					return err
				}
				res, err := relay.RespondToPermission(ctx, req)
				if err != nil {
					return fmt.Errorf("answer permission via relay: %w", err)
				}
				if res.Msg.GetResult().GetAck().GetOutcome() != relayv1.CommandAck_OUTCOME_OK {
					return fmt.Errorf("permission answer outcome: %v", res.Msg.GetResult())
				}
			case *agentv1.SessionEvent_ToolCallFinished:
				if x.ToolCallFinished.GetIsError() {
					return fmt.Errorf("tool failed: %v", x.ToolCallFinished.GetContent())
				}
				sawTool = true
			case *agentv1.SessionEvent_TurnFinished:
				if x.TurnFinished.GetResult() != agentv1.TurnFinished_RESULT_SUCCESS {
					return fmt.Errorf("turn finished with %v: %s", x.TurnFinished.GetResult(), x.TurnFinished.GetErrorMessage())
				}
				if !sawTool {
					return errors.New("turn finished without a tool call")
				}
				out, err := os.ReadFile(*cwd + "/out.txt")
				if err != nil || strings.TrimSpace(string(out)) != "harness-e2e" {
					return fmt.Errorf("out.txt: %q %v", out, err)
				}
				fmt.Printf("relayviewer: turn ran on the host; %d durable events, permission answered remotely: %v\n", last, sawPermission)
				return nil
			}
		}
	}
	return fmt.Errorf("stream ended: %v", stream.Err())
}
