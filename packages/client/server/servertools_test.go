package server_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
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
var libraryToolNames = []string{"ArtifactPublish", "KnowledgeSearch", "KnowledgeRead", "Issues"}

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

	// What the model is offered, as the RPC and /tools say it, is what the request carried.
	lt, err := h.client.ListSessionTools(h.ctx, connect.NewRequest(&agentv1.ListSessionToolsRequest{SessionId: fk.Msg.Session.Id}))
	if err != nil {
		t.Fatal(err)
	}
	var offered []string
	for _, tl := range lt.Msg.GetTools() {
		if tl.GetOffered() {
			offered = append(offered, tl.GetName())
		}
	}
	if sent := toolDefNames(h.lastMainRequest()); !slices.Equal(offered, sent) {
		t.Fatalf("ListSessionTools says %v, the request carried %v", offered, sent)
	}
	if lt.Msg.GetSyncOff() != "" {
		t.Fatalf("sync off: %q", lt.Msg.GetSyncOff())
	}
}

// A subagent of a signed-in session is offered the server's tools too.
func TestSubagentOfSignedInSessionGetsServerTools(t *testing.T) {
	var mu sync.Mutex
	var subTools []string
	h, _ := signedInHarness(t, func(n int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.RequestClass == "subagent" {
			mu.Lock()
			subTools = toolDefNames(r)
			mu.Unlock()
			return fg.Reply{Events: fg.Message("s", fg.Text("agent report"))}
		}
		if strings.Contains(allText(r), "agent report") || len(fg.LastToolResults(r)) > 0 {
			return fg.Reply{Events: fg.Message("z", fg.Text("done"))}
		}
		return fg.Reply{Events: fg.Message("a", fg.ToolUse("a1", "Agent", map[string]any{"description": "look", "prompt": "look around", "subagent_type": "general-purpose"}))}
	}, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "go")
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		u := ev.GetSubagentUpdated()
		return u != nil && u.Agent.State == agentv1.SubagentState_SUBAGENT_STATE_FINISHED
	})
	mu.Lock()
	defer mu.Unlock()
	for _, n := range []string{"ArtifactPublish", "KnowledgeSearch"} {
		if !slices.Contains(subTools, n) {
			t.Fatalf("the subagent lacks %s: %v", n, subTools)
		}
	}
}

// slashText runs a built-in command and returns its reply.
func (h *harness) slashText(sid, cmd string) string {
	h.t.Helper()
	ch := h.subscribe(sid)
	h.send(sid, cmd)
	var out string
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if r := ev.GetSlashCommandResult(); r != nil {
			out = r.GetText()
			return true
		}
		return false
	})
	return out
}

// Signed in, but sync is off: the session says which tools are off and why, /tools lists them with the reason, and a
// tool without a prompt in the bundle or denied by a rule is reported as such.
func TestSessionWithoutSyncWarnsAndExplainsTools(t *testing.T) {
	h, _ := signedInHarness(t, okReply, func(o *bootstrap.Options) { o.DisallowedTools = []string{"WebFetch"} })
	delete(h.gw.Bundle.Tools, "Visual") // before any session asked for the bundle
	t.Setenv("HARNESS_NO_SYNC", "1")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	var warning string
	h.send(sid, "hi")
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if n := ev.GetNotice(); n != nil && n.GetKind() == "session_warning" && strings.Contains(n.GetText(), "ArtifactPublish") {
			warning = n.GetText()
		}
		return isTurnFinished(ev)
	})
	if !strings.Contains(warning, "HARNESS_NO_SYNC=1") || !strings.Contains(warning, "KnowledgeSearch") {
		t.Fatalf("warning %q", warning)
	}
	if got := toolDefNames(h.lastMainRequest()); slices.Contains(got, "ArtifactPublish") {
		t.Fatalf("offered without sync: %v", got)
	}

	lt, err := h.client.ListSessionTools(h.ctx, connect.NewRequest(&agentv1.ListSessionToolsRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, tl := range lt.Msg.GetTools() {
		if !tl.GetOffered() {
			reasons[tl.GetName()] = tl.GetReason()
		}
	}
	for name, want := range map[string]string{"ArtifactPublish": bootstrap.ToolReasonNeedsSync, "Issues": bootstrap.ToolReasonNeedsSync, "WebFetch": bootstrap.ToolReasonDisabled, "Visual": bootstrap.ToolReasonNoPrompt} {
		if reasons[name] != want {
			t.Fatalf("%s: reason %q, want %q (all: %v)", name, reasons[name], want, reasons)
		}
	}
	if !strings.Contains(lt.Msg.GetSyncOff(), "HARNESS_NO_SYNC") {
		t.Fatalf("sync_off %q", lt.Msg.GetSyncOff())
	}

	out := h.slashText(sid, "/tools")
	for _, want := range []string{"Offered to the model", "ArtifactPublish: sync is turned off (HARNESS_NO_SYNC=1) (needs_sync)", "WebFetch: a permission rule denies it (disabled)", "sync: off"} {
		if !strings.Contains(out, want) {
			t.Fatalf("/tools lacks %q:\n%s", want, out)
		}
	}

	d, err := h.client.GetDiagnostics(h.ctx, connect.NewRequest(&agentv1.GetDiagnosticsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Msg.GetText(), sid) || !strings.Contains(d.Msg.GetText(), "ArtifactPublish (needs_sync)") || strings.Contains(d.Msg.GetText(), "hat_test") || d.Msg.GetLogsDir() == "" {
		t.Fatalf("diagnostics:\n%s", d.Msg.GetText())
	}
}
