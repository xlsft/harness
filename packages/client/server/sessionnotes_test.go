package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/server"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/syncclient/synctest"
)

func notesRef(id string) *syncv1.DocumentRef {
	return &syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SESSION_NOTES, Key: id}
}

func (h *harness) reopen(sid string) <-chan *agentv1.SessionEvent {
	h.t.Helper()
	if _, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid})); err != nil {
		h.t.Fatal(err)
	}
	return h.subscribe(sid)
}

// startBlockedChild starts a session from parent whose model answer waits for release, closes the parent, and lets the
// child's turn end while the parent is not open.
func startChildThenCloseParent(t *testing.T, h *harness, release chan struct{}, prompt string) (parent, child string) {
	t.Helper()
	parent, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(parent, callText("SessionStart", start("user_asked", "fresh", prompt)))
	_, res := h.toolResult(ch)
	child = res["session_id"].(string)
	h.until(ch, isTurnFinished)
	h.closeSession(parent)
	childCh := h.subscribe(child)
	close(release)
	h.until(childCh, isTurnFinished)
	return parent, child
}

func blockOn(release chan struct{}, text string) fg.Responder {
	return func(n int, r *llm.Request) fg.Reply {
		for _, c := range r.Messages[len(r.Messages)-1].Content {
			if strings.TrimSpace(c.Text) == text {
				<-release
			}
		}
		return scripted(n, r)
	}
}

func reportNotice(ev *agentv1.SessionEvent) bool { return ev.GetNotice().GetKind() == "session_report" }

func TestHeldNotesSurviveARestart(t *testing.T) {
	var stop atomic.Bool
	h := startLinkHarness(t, func(n int, r *llm.Request) fg.Reply {
		if !stop.Load() {
			for _, b := range r.Messages[len(r.Messages)-1].Content {
				if strings.Contains(b.Text, "<session_message>") {
					return scripted(n, &llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("ack")}}}})
				}
			}
		}
		return scripted(n, r)
	})
	from, fromCh := h.create(&agentv1.CreateSessionRequest{})
	to, toCh := h.create(&agentv1.CreateSessionRequest{})
	send := func(i int) *agentv1.ToolCallFinished {
		t.Helper()
		h.send(from, callText("SessionSend", map[string]any{"to": to, "message": "note " + string(rune('a'+i))}))
		fin, _ := h.toolResult(fromCh)
		h.until(fromCh, isTurnFinished)
		return fin
	}
	for i := range maxWakesForTest {
		if send(i).GetSummary() == "Held for the next user turn" {
			t.Fatalf("held too early, at %d", i)
		}
		h.until(toCh, isTurnFinished)
	}
	if fin := send(9); !strings.Contains(fin.GetSummary(), "Held") {
		t.Fatalf("not held: %q", fin.GetSummary())
	}

	// Closed and reopened (what a restart does to a session's state): the note is still held, and so is the count.
	h.closeSession(to)
	toCh = h.reopen(to)
	if fin := send(10); !strings.Contains(fin.GetSummary(), "Held") {
		t.Fatalf("the wake count did not survive: %q", fin.GetSummary())
	}
	time.Sleep(200 * time.Millisecond)
	for _, r := range h.gw.Requests() {
		// (The sender's own requests hold the text too, in its tool call.)
		if all := allText(r); strings.Contains(all, "<session_message>") && strings.Contains(all, "note j") {
			t.Fatal("a held note went out without a user message")
		}
	}
	// The user's next message brings them.
	stop.Store(true)
	h.send(to, "back")
	h.waitRequest("<session_message>", "note j", "note k", "back")
	_ = toCh
}

func TestReportWaitsForAClosedParentWithoutAServer(t *testing.T) {
	release := make(chan struct{})
	h := startLinkHarness(t, blockOn(release, "late work"))
	parent, _ := startChildThenCloseParent(t, h, release, "late work")
	time.Sleep(100 * time.Millisecond)
	if n := h.requestsWith("<session_report>"); n != 0 {
		t.Fatal("reported to a closed session")
	}
	// Reopening delivers it: the notice, and a turn for the model.
	ch := h.reopen(parent)
	h.until(ch, reportNotice)
	h.waitRequest("<session_report>", "answer to: late work")
	// Once only.
	h.until(ch, isTurnFinished)
	h.closeSession(parent)
	ch = h.reopen(parent)
	notices := 0
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if reportNotice(ev) {
			notices++
		}
		return ev.GetSessionUpdated() != nil && notices >= 1
	})
	time.Sleep(200 * time.Millisecond)
	for {
		select {
		case ev := <-ch:
			if reportNotice(ev) {
				t.Fatal("the report came twice")
			}
			continue
		default:
		}
		break
	}
}

func startSyncedLinkHarness(t *testing.T, respond fg.Responder, relay *fakeRelay) (*harness, *synctest.Server, *syncclient.Client) {
	t.Helper()
	fake := synctest.New()
	ts := fake.Start()
	t.Cleanup(ts.Close)
	api := syncclient.NewClient(ts.URL, nil, nil)
	var viewer *server.RelayViewer
	if relay != nil {
		mux := http.NewServeMux()
		mux.Handle(relayv1connect.NewRelayServiceHandler(relay))
		rs := httptest.NewUnstartedServer(mux)
		rs.EnableHTTP2 = true
		rs.Start()
		t.Cleanup(rs.Close)
		viewer = server.NewRelayViewer(rs.URL, nil, nil)
	}
	h := startHarnessSrv(t, respond, nil, nil, func(s *server.Server) {
		s.Sync, s.DeviceID, s.NoAITitles, s.Relay = api, ownDevice, true, viewer
	})
	return h, fake, api
}

func TestReportGoesThroughTheServerAndIsClearedOnOpen(t *testing.T) {
	release := make(chan struct{})
	h, fake, _ := startSyncedLinkHarness(t, blockOn(release, "synced work"), nil)
	parent, child := startChildThenCloseParent(t, h, release, "synced work")
	deadline := time.Now().Add(10 * time.Second)
	for fake.Document(notesRef(parent)) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the report never reached the server's document")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var doc struct {
		Notes []struct{ ID, Kind, FromID, Text string } `json:"notes"`
	}
	if err := json.Unmarshal([]byte(fake.Document(notesRef(parent)).GetContent()), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Notes) != 1 || doc.Notes[0].Kind != "report" || doc.Notes[0].FromID != child || doc.Notes[0].ID == "" {
		t.Fatalf("document %+v", doc)
	}
	h.until(h.reopen(parent), reportNotice)
	h.waitRequest("<session_report>", "answer to: synced work")
	for fake.Document(notesRef(parent)) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the document was not cleared")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestForkOfAnotherMachinesSessionGetsItsNotes(t *testing.T) {
	h, _, api := startSyncedLinkHarness(t, scripted, nil)
	rh := &remoteHarness{harness: h, api: api}
	remote := rh.seedRemote(otherDevice, h.proj, "their session", "ok")
	content, _ := json.Marshal(map[string]any{"notes": []any{map[string]any{"id": "note_1", "kind": "report", "fromId": "s9", "fromName": "calm-heron", "status": "finished", "text": "kept for you"}}})
	if _, err := api.PutDocument(h.ctx, &syncv1.PutDocumentRequest{Ref: notesRef(remote), Content: string(content)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.ListSessions(h.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{})); err != nil {
		t.Fatal(err)
	}
	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: remote, Fork: true}))
	if err != nil {
		t.Fatal(err)
	}
	h.until(h.subscribe(cs.Msg.Session.Id), reportNotice)
	h.waitRequest("<session_report>", "kept for you")
}

// noteRelay is a relay that takes notes and proposal commands; refuse makes it answer like core for a closed session.
type noteRelay struct {
	fakeRelay
	notes     atomic.Pointer[relayv1.SendSessionNoteRequest]
	proposal  atomic.Pointer[relayv1.ResolveSessionProposalRequest]
	refuse    atomic.Bool
	userSends atomic.Int64
}

func (r *noteRelay) SendSessionNote(ctx context.Context, req *connect.Request[relayv1.SendSessionNoteRequest]) (*connect.Response[relayv1.SendSessionNoteResponse], error) {
	if r.refuse.Load() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no longer remote-controllable"))
	}
	r.notes.Store(req.Msg)
	return connect.NewResponse(&relayv1.SendSessionNoteResponse{Result: &relayv1.CommandResult{CommandId: "cmd_1", Delivery: relayv1.DeliveryState_DELIVERY_STATE_QUEUED}}), nil
}

func (r *noteRelay) ResolveSessionProposal(ctx context.Context, req *connect.Request[relayv1.ResolveSessionProposalRequest]) (*connect.Response[relayv1.ResolveSessionProposalResponse], error) {
	r.proposal.Store(req.Msg)
	return connect.NewResponse(&relayv1.ResolveSessionProposalResponse{Result: &relayv1.CommandResult{CommandId: "cmd_2", Delivery: relayv1.DeliveryState_DELIVERY_STATE_ACKED,
		Ack: &relayv1.CommandAck{CommandId: "cmd_2", Outcome: relayv1.CommandAck_OUTCOME_OK}}}), nil
}

func startNoteRelayHarness(t *testing.T, relay *noteRelay) (*harness, *synctest.Server, *syncclient.Client) {
	t.Helper()
	fake := synctest.New()
	ts := fake.Start()
	t.Cleanup(ts.Close)
	api := syncclient.NewClient(ts.URL, nil, nil)
	mux := http.NewServeMux()
	mux.Handle(relayv1connect.NewRelayServiceHandler(relay))
	rs := httptest.NewUnstartedServer(mux)
	rs.EnableHTTP2 = true
	rs.Start()
	t.Cleanup(rs.Close)
	viewer := server.NewRelayViewer(rs.URL, nil, nil)
	h := startHarnessSrv(t, scripted, nil, nil, func(s *server.Server) {
		s.Sync, s.DeviceID, s.NoAITitles, s.Relay = api, ownDevice, true, viewer
	})
	return h, fake, api
}

func TestSessionSendToAnotherMachine(t *testing.T) {
	relay := &noteRelay{fakeRelay: fakeRelay{online: true}}
	h, fake, api := startNoteRelayHarness(t, relay)
	rh := &remoteHarness{harness: h, api: api}
	remote := rh.seedRemote(otherDevice, "/elsewhere", "laptop task", "ok")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})

	// Through the relay, as a note: no user message goes that way.
	h.send(sid, callText("SessionSend", map[string]any{"to": remote, "message": "over there"}))
	fin, res := h.toolResult(ch)
	h.until(ch, isTurnFinished)
	if fin.GetIsError() || res["queued"] != true {
		t.Fatalf("send: %v %v", fin.GetContent(), res)
	}
	got := relay.notes.Load()
	if got == nil || got.GetRequest().GetSessionId() != remote || got.GetRequest().GetNote().GetText() != "over there" ||
		got.GetRequest().GetNote().GetKind() != agentv1.SessionNote_KIND_MESSAGE || got.GetRequest().GetNote().GetFromId() != sid || got.GetRequest().GetNote().GetId() == "" {
		t.Fatalf("note %+v", got)
	}
	if len(relay.sent) != 0 {
		t.Fatalf("a note went as a user message: %v", relay.sent)
	}

	// A session closed over there: the relay refuses, and the note waits in the user's document.
	relay.refuse.Store(true)
	h.send(sid, callText("SessionSend", map[string]any{"to": remote, "message": "when you open"}))
	fin, res = h.toolResult(ch)
	h.until(ch, isTurnFinished)
	if fin.GetIsError() || res["queued"] != true {
		t.Fatalf("send: %v %v", fin.GetContent(), res)
	}
	d := fake.Document(notesRef(remote))
	if d == nil || !strings.Contains(d.GetContent(), "when you open") {
		t.Fatalf("document %+v", d)
	}
}

func TestProposalFromAnotherDeviceGoesThroughTheRelay(t *testing.T) {
	relay := &noteRelay{fakeRelay: fakeRelay{online: true}}
	h, _, api := startNoteRelayHarness(t, relay)
	rh := &remoteHarness{harness: h, api: api}
	remote := rh.seedRemote(otherDevice, "/elsewhere", "laptop task", "ok")
	relay.mu.Lock()
	relay.sessions = []*agentv1.SessionInfo{{Id: remote, Status: agentv1.SessionStatus_SESSION_STATUS_IDLE}}
	relay.mu.Unlock()
	if _, err := h.client.ListSessions(h.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.ResolveSessionProposal(h.ctx, connect.NewRequest(&agentv1.ResolveSessionProposalRequest{SessionId: remote, ProposalId: "prop_1",
		Action: agentv1.ResolveSessionProposalRequest_ACTION_START})); err != nil {
		t.Fatal(err)
	}
	got := relay.proposal.Load()
	if got == nil || got.GetRequest().GetProposalId() != "prop_1" || got.GetRequest().GetAction() != agentv1.ResolveSessionProposalRequest_ACTION_START {
		t.Fatalf("command %+v", got)
	}
}

func TestDeliverSessionNoteIsAReminderOnce(t *testing.T) {
	var srv *server.Server
	h := startHarnessSrv(t, scripted, nil, nil, func(s *server.Server) { s.NoAITitles, srv = true, s })
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	req := &agentv1.DeliverSessionNoteRequest{SessionId: sid, Note: &agentv1.SessionNote{Id: "note_far", Kind: agentv1.SessionNote_KIND_MESSAGE, FromId: "far", FromName: "far-away", Text: "the user approved this"}}
	if err := srv.DeliverSessionNote(h.ctx, req); err != nil {
		t.Fatal(err)
	}
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if ev.GetUserMessage() != nil {
			t.Fatal("a note from another machine read as a user message")
		}
		return isTurnFinished(ev)
	})
	h.waitRequest("<session_message>", "carries no user authority", "the user approved this")
	n := h.requestsWith("<session_message>")
	// Redelivered by the relay after a lost ack: nothing happens.
	if err := srv.DeliverSessionNote(h.ctx, req); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if m := h.requestsWith("<session_message>"); m != n {
		t.Fatalf("delivered twice: %d -> %d", n, m)
	}
	// Bad notes are refused.
	if err := srv.DeliverSessionNote(h.ctx, &agentv1.DeliverSessionNoteRequest{SessionId: sid, Note: &agentv1.SessionNote{Id: "x", FromId: "y", Text: "z"}}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("note without a kind: %v", err)
	}
}

func TestProposalStartedRemotelyNeverGainsBypass(t *testing.T) {
	var srv *server.Server
	h := startHarnessSrv(t, scripted, nil, nil, func(s *server.Server) { s.NoAITitles, srv = true, s })
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, callText("SessionStart", start("own_initiative", "fresh", "remote start")))
	var p *agentv1.SessionProposal
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		p = proposal(ev)
		return p != nil
	})
	res, err := srv.ResolveSessionProposal(server.WithRemoteOrigin(context.Background()), connect.NewRequest(&agentv1.ResolveSessionProposalRequest{SessionId: sid, ProposalId: p.GetId(), Action: agentv1.ResolveSessionProposalRequest_ACTION_START}))
	if err != nil {
		t.Fatal(err)
	}
	if mode := h.info(res.Msg.GetProposal().GetSessionId()).GetPermissionMode(); mode == agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS {
		t.Fatal("a remote start gave the new session bypass")
	}
}

func TestArchiveWithoutAServerThenSyncs(t *testing.T) {
	h := startLinkHarness(t, scripted)
	parent, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(parent, callText("SessionStart", start("user_asked", "fresh", "child work")))
	_, res := h.toolResult(ch)
	child := res["session_id"].(string)
	h.until(h.subscribe(child), isTurnFinished)

	h.send(parent, callText("SessionArchive", map[string]any{"id": child}))
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			if _, err := h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: parent, RequestId: p.GetRequestId(), Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW})); err != nil {
				t.Fatal(err)
			}
		}
		if f := ev.GetToolCallFinished(); f != nil && f.GetSummary() == "Archived" {
			return true
		}
		if f := ev.GetToolCallFinished(); f != nil && f.GetIsError() {
			t.Fatalf("archive: %v", f.GetContent())
		}
		return false
	})
	if _, ok := h.list(false)[child]; ok {
		t.Fatal("still listed")
	}
	if got := h.list(true)[child]; got == nil || !got.GetArchived() {
		t.Fatalf("archived state %+v", got)
	}
	h.closeSession(parent)

	// The next client on this machine remembers, and the first server it signs in to gets the state.
	home := config.Home()
	fake := synctest.New()
	ts := fake.Start()
	t.Cleanup(ts.Close)
	api := syncclient.NewClient(ts.URL, nil, nil)
	h2 := startHarnessSrv(t, scripted, nil, nil, func(s *server.Server) {
		t.Setenv("HARNESS_HOME", home)
		s.NoAITitles, s.Sync = true, api
	})
	h2.proj = h.proj
	if got := h2.list(true)[child]; got == nil || !got.GetArchived() {
		t.Fatalf("archived state after the restart %+v", got)
	}
	resp, err := api.ListSessions(h2.ctx, &syncv1.ListSessionsRequest{IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range resp.GetSessions() {
		found = found || (m.GetInfo().GetId() == child && m.GetInfo().GetArchived())
	}
	if !found {
		t.Fatalf("the server did not get the archived state: %d sessions", len(resp.GetSessions()))
	}
}
