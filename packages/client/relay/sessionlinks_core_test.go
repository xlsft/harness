package relay_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/relay"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/server"
	"github.com/xlsft/harness/packages/client/syncclient"
)

// TestSessionLinksAgainstCore runs the real relay host and client server against a real core: core's
// test/session-links-core.test.ts starts one and runs this test with its address and two device tokens. A device other
// than the host's sends what SessionSend and a proposal's card send from another machine.
func TestSessionLinksAgainstCore(t *testing.T) {
	core := os.Getenv("HARNESS_E2E_CORE_URL")
	if core == "" {
		t.Skip("needs a core: run through packages/core/test/session-links-core.test.ts")
	}
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	t.Setenv("HARNESS_KEYRING", "0")

	release := make(chan struct{})
	var calls atomic.Int64
	gw := fg.New(func(n int, r *llm.Request) fg.Reply {
		id := func() string { return fmt.Sprintf("x%d", calls.Add(1)) }
		said := ""
		for _, b := range r.Messages[len(r.Messages)-1].Content {
			t := strings.TrimSpace(b.Text)
			if rest, ok := strings.CutPrefix(t, "call "); ok {
				tool, raw, _ := strings.Cut(rest, " ")
				var in map[string]any
				_ = json.Unmarshal([]byte(raw), &in)
				return fg.Reply{Events: fg.Message(id(), fg.ToolUse(id(), tool, in))}
			}
			if t == "late work" {
				<-release
			}
			if t != "" && !strings.HasPrefix(t, "<") {
				said = t
			}
		}
		return fg.Reply{Events: fg.Message(id(), fg.Text("answer to: "+said))}
	})
	gwURL, stopGW, err := gw.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopGW)
	gwTok := auth.StaticToken("hat_test")
	hostTok := auth.StaticToken(os.Getenv("HARNESS_E2E_TOKEN"))
	viewerToken := os.Getenv("HARNESS_E2E_VIEWER_TOKEN")

	srv := server.New(bootstrap.Options{Gateway: connectgw.New(gwURL, nil, gwTok), Prompts: prompts.NewConnectSource(gwURL, nil, gwTok)})
	t.Cleanup(srv.Close)
	api := syncclient.NewClient(core, nil, hostTok)
	srv.Sync, srv.Preferences, srv.NoAITitles = api, api, true

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	attach := func() context.CancelFunc {
		hctx, stop := context.WithCancel(ctx)
		relay.Attach(hctx, srv, relay.Options{ServerURL: core, Tokens: hostTok, InstanceID: uuid.NewString(), FlushInterval: 20 * time.Millisecond})
		return stop
	}
	stopHost := attach()
	defer func() { stopHost() }()

	bearer := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+viewerToken)
			return next(ctx, req)
		}
	})
	far := relayv1connect.NewRelayServiceClient(rpc.HTTPClient(core), strings.TrimRight(core, "/"), append(rpc.ClientOptions(), connect.WithInterceptors(bearer))...)

	proj, _ := filepath.EvalSymlinks(t.TempDir())
	open := func(req *agentv1.CreateSessionRequest) (string, <-chan *agentv1.SessionEvent) {
		t.Helper()
		res, err := srv.CreateSession(ctx, connect.NewRequest(req))
		if err != nil {
			t.Fatal(err)
		}
		id := res.Msg.GetSession().GetId()
		feed, ok := srv.SessionFeed(id)
		if !ok {
			t.Fatalf("session %s is not live", id)
		}
		_, ch, unsub := feed.Subscribe(0)
		t.Cleanup(unsub)
		return id, ch
	}
	until := func(ch <-chan *agentv1.SessionEvent, what string, f func(*agentv1.SessionEvent) bool) {
		t.Helper()
		timeout := time.After(30 * time.Second)
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					t.Fatalf("stream ended waiting for %s", what)
				}
				if f(ev) {
					return
				}
			case <-timeout:
				t.Fatalf("timeout waiting for %s", what)
			}
		}
	}
	registered := func(id string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			res, err := far.ListHosts(ctx, connect.NewRequest(&relayv1.ListHostsRequest{}))
			if err == nil {
				for _, h := range res.Msg.GetHosts() {
					for _, s := range h.GetSessions() {
						if s.GetId() == id {
							return
						}
					}
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("session %s never registered", id)
	}
	say := func(id, text string) {
		t.Helper()
		if _, err := srv.SendUserMessage(ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: id, Uuid: uuid.NewString(),
			Content: []*llmv1.ContentBlock{{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{Text: text}}}}})); err != nil {
			t.Fatal(err)
		}
	}
	note := func(sid, text string) *relayv1.SendSessionNoteRequest {
		id := "note_" + uuid.NewString()
		return &relayv1.SendSessionNoteRequest{Request: &agentv1.DeliverSessionNoteRequest{SessionId: sid,
			Note: &agentv1.SessionNote{Id: id, Kind: agentv1.SessionNote_KIND_MESSAGE, FromId: "far-session", FromName: "far-away", Text: text}},
			Options: &relayv1.CommandOptions{IdempotencyKey: id}}
	}
	noteArrives := func(ch <-chan *agentv1.SessionEvent, text string) {
		t.Helper()
		until(ch, "the note "+text, func(ev *agentv1.SessionEvent) bool {
			if ev.GetUserMessage() != nil {
				t.Fatal("a note read as a user message")
			}
			return ev.GetNotice().GetKind() == "session_message" && strings.Contains(ev.GetNotice().GetText(), text)
		})
	}

	sid, ch := open(&agentv1.CreateSessionRequest{Cwd: proj})
	registered(sid)

	// A note from another device arrives as a notice and a reminder, not as a user message.
	if _, err := far.SendSessionNote(ctx, connect.NewRequest(note(sid, "from far away"))); err != nil {
		t.Fatal(err)
	}
	noteArrives(ch, "from far away")

	// The machine is offline: the note waits in core and arrives when it reconnects.
	stopHost()
	time.Sleep(300 * time.Millisecond)
	res, err := far.SendSessionNote(ctx, connect.NewRequest(note(sid, "while you were out")))
	if err != nil {
		t.Fatalf("offline note: %v", err)
	}
	if d := res.Msg.GetResult().GetDelivery(); d != relayv1.DeliveryState_DELIVERY_STATE_QUEUED {
		t.Fatalf("offline delivery %v", d)
	}
	stopHost = attach()
	noteArrives(ch, "while you were out")

	// A proposal is started from the other device.
	in, _ := json.Marshal(map[string]any{"title": "Far start", "prompt": "proposed from afar", "initiation": "own_initiative", "context": "fresh", "worktree": false})
	say(sid, "call SessionStart "+string(in))
	var prop *agentv1.SessionProposal
	until(ch, "the proposal", func(ev *agentv1.SessionEvent) bool {
		prop = ev.GetSessionProposal()
		return prop != nil
	})
	resolved, err := far.ResolveSessionProposal(ctx, connect.NewRequest(&relayv1.ResolveSessionProposalRequest{Request: &agentv1.ResolveSessionProposalRequest{
		SessionId: sid, ProposalId: prop.GetId(), Action: agentv1.ResolveSessionProposalRequest_ACTION_START}}))
	if err != nil {
		t.Fatal(err)
	}
	if o := resolved.Msg.GetResult().GetAck().GetOutcome(); o != relayv1.CommandAck_OUTCOME_OK {
		t.Fatalf("proposal ack %v: %s", o, resolved.Msg.GetResult().GetAck().GetErrorMessage())
	}
	until(ch, "the started proposal", func(ev *agentv1.SessionEvent) bool {
		return ev.GetSessionProposal().GetState() == agentv1.SessionProposal_STATE_STARTED && ev.GetSessionProposal().GetSessionId() != ""
	})

	// A report for a closed parent goes into core's document and comes out when the parent opens.
	parent, pch := open(&agentv1.CreateSessionRequest{Cwd: proj})
	in, _ = json.Marshal(map[string]any{"title": "Late", "prompt": "late work", "initiation": "user_asked", "context": "fresh", "worktree": false})
	say(parent, "call SessionStart "+string(in))
	var child string
	until(pch, "the started session", func(ev *agentv1.SessionEvent) bool {
		f := ev.GetToolCallFinished()
		if f == nil {
			return false
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(f.GetStructuredResultJson()), &m)
		child, _ = m["session_id"].(string)
		return true
	})
	until(pch, "the parent's turn", func(ev *agentv1.SessionEvent) bool { return ev.GetTurnFinished() != nil })
	if _, err := srv.CloseSession(ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: parent})); err != nil {
		t.Fatal(err)
	}
	close(release)
	ref := &syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SESSION_NOTES, Key: parent}
	readDoc := func() *syncv1.Document {
		res, err := api.GetDocument(ctx, &syncv1.GetDocumentRequest{Selector: &syncv1.GetDocumentRequest_Ref{Ref: ref}})
		if err != nil {
			return nil
		}
		return res.GetDocument()
	}
	deadline := time.Now().Add(20 * time.Second)
	for readDoc() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the report never reached core's document")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if d := readDoc(); !strings.Contains(d.GetContent(), child) || !strings.Contains(d.GetContent(), "late work") {
		t.Fatalf("document %q", d.GetContent())
	}
	_, pch = open(&agentv1.CreateSessionRequest{ResumeSessionId: parent})
	until(pch, "the report", func(ev *agentv1.SessionEvent) bool { return ev.GetNotice().GetKind() == "session_report" })
	deadline = time.Now().Add(15 * time.Second)
	for readDoc() != nil {
		if time.Now().After(deadline) {
			t.Fatal("the document was not cleared")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
