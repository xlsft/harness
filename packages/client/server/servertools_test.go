package server_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/sync/v1/syncv1connect"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/syncclient/synctest"
)

// libraryToolNames are the tools that need the server's sync service.
var libraryToolNames = []string{"ArtifactPublish", "KnowledgeSearch", "KnowledgeRead", "TaskTracker"}

// signedInHarness is the desktop's setup: serve with shared adapters and the signed-in server's credentials, the
// gateway, prompts and sync service at one address, and a bundle that has prompts for the library tools.
func signedInHarness(t *testing.T, respond fg.Responder, mod func(*bootstrap.Options)) (*harness, *synctest.Server) {
	t.Helper()
	t.Setenv("HARNESS_NO_SYNC", "")
	fs := synctest.New()
	h := startHarnessGW(t, respond, func(gw *fg.Server) {
		gw.Mount = func(mux *http.ServeMux) { mux.Handle(syncv1connect.NewSyncServiceHandler(fs)) }
		for _, n := range libraryToolNames {
			gw.Bundle.Tools[n] = prompts.ToolPrompt{Description: n + ".", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}
		}
	}, func(o *bootstrap.Options) {
		tok := auth.StaticToken("hat_test")
		o.Tokens, o.User = tok, &auth.User{ID: "usr_1"}
		o.SyncHub = &bootstrap.SyncHub{URL: o.ServerURL, Tokens: tok, User: o.User}
		if mod != nil {
			mod(o)
		}
	})
	return h, fs
}


func toolDefNames(r *llm.Request) []string {
	var out []string
	for _, d := range r.Tools {
		out = append(out, d.Name)
	}
	return out
}

func (h *harness) lastMainRequest() *llm.Request {
	h.t.Helper()
	reqs := h.gw.Requests()
	if len(reqs) == 0 {
		h.t.Fatal("no model request")
	}
	return reqs[len(reqs)-1]
}

func (h *harness) assertLibraryTools(what string) {
	h.t.Helper()
	got := toolDefNames(h.lastMainRequest())
	for _, n := range libraryToolNames {
		if !slices.Contains(got, n) {
			h.t.Fatalf("%s: %s is not offered to the model; tools %v", what, n, got)
		}
	}
}

// turnOn sends msg to a session and waits for the turn to end.
func (h *harness) turnOn(sid, msg string) []*agentv1.SessionEvent {
	h.t.Helper()
	ch := h.subscribe(sid)
	h.send(sid, msg)
	return h.until(ch, isTurnFinished)
}

// The desktop's sessions (serve with shared adapters) got no sync client, so the model was never offered
// ArtifactPublish and the other tools that need the server.
func TestSignedInSessionsOfferServerTools(t *testing.T) {
	h, _ := signedInHarness(t, okReply, nil)

	// A folder session.
	sid, _ := h.create(&agentv1.CreateSessionRequest{})
	h.turnOn(sid, "hi")
	h.assertLibraryTools("folder session")

	// A session without a folder (~/.harness/chats/<id>).
	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !cs.Msg.Session.NoFolder {
		t.Fatalf("not a folderless session: %+v", cs.Msg.Session)
	}
	chat := cs.Msg.Session.Id
	for _, ev := range h.turnOn(chat, "hi") {
		if n := ev.GetNotice(); n != nil && n.GetKind() == "session_warning" {
			t.Fatalf("unexpected warning %q", n.GetText())
		}
	}
	h.assertLibraryTools("session without a folder")

	// Resumed after it was closed, and forked.
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: chat})); err != nil {
		t.Fatal(err)
	}
	rs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: chat}))
	if err != nil {
		t.Fatal(err)
	}
	h.turnOn(rs.Msg.Session.Id, "again")
	h.assertLibraryTools("resumed session")

	fk, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid, Fork: true, Cwd: h.proj}))
	if err != nil {
		t.Fatal(err)
	}
	h.turnOn(fk.Msg.Session.Id, "fork")
	h.assertLibraryTools("forked session")
}
