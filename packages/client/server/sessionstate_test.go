package server_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/server"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/syncclient/synctest"
)

func startStateHarness(t *testing.T) (*harness, *synctest.Server, *syncclient.Client) {
	t.Helper()
	fake := synctest.New()
	ts := fake.Start()
	t.Cleanup(ts.Close)
	api := syncclient.NewClient(ts.URL, nil, nil)
	h := startHarnessSrv(t, fg.Sequence(), nil, nil, func(s *server.Server) { s.Sync = api })
	return h, fake, api
}

func ptr[T any](v T) *T { return &v }

func (h *harness) update(req *agentv1.UpdateSessionRequest) (*agentv1.SessionInfo, error) {
	resp, err := h.client.UpdateSession(h.ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg.Session, nil
}

func (h *harness) list(includeArchived bool) map[string]*agentv1.SessionInfo {
	h.t.Helper()
	resp, err := h.client.ListSessions(h.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{Cwd: h.proj, IncludeArchived: includeArchived}))
	if err != nil {
		h.t.Fatal(err)
	}
	out := map[string]*agentv1.SessionInfo{}
	for _, s := range resp.Msg.Sessions {
		out[s.Id] = s
	}
	return out
}

func TestUpdateSessionStoresUserStateOnServer(t *testing.T) {
	h, fake, api := startStateHarness(t)
	sid, _ := h.create(&agentv1.CreateSessionRequest{})

	info, err := h.update(&agentv1.UpdateSessionRequest{SessionId: sid, Pinned: ptr(true), Unread: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if !info.Pinned || !info.Unread || info.Archived {
		t.Fatalf("update response: %+v", info)
	}
	ss := fake.Session(sid)
	if ss == nil || !ss.Meta.Info.Pinned || !ss.Meta.Info.Unread || ss.Meta.Info.Cwd != h.proj {
		t.Fatalf("server copy: %+v", ss)
	}
	if got := ss.Upserts[len(ss.Upserts)-1]; !slices.Equal(got, []string{"info.pinned", "info.unread"}) {
		t.Fatalf("update paths %v", got)
	}

	// A rename of a live session: recorded as a custom title, sent to the server, listed.
	if info, err = h.update(&agentv1.UpdateSessionRequest{SessionId: sid, Title: ptr("  My   session ")}); err != nil || info.Title != "My session" || !info.Pinned {
		t.Fatalf("rename: %v %+v", err, info)
	}
	if _, custom := titleEntries(t, transcriptPath(h.proj, sid)); !slices.Equal(custom, []string{"My session"}) {
		t.Fatalf("custom titles %v", custom)
	}
	if got := fake.Session(sid).Meta.Info.Title; got != "My session" {
		t.Fatalf("server title %q", got)
	}
	if l := h.list(false)[sid]; l == nil || l.Title != "My session" || !l.Pinned || !l.Unread {
		t.Fatalf("listed: %+v", l)
	}

	// The server is the source of truth: a change made there shows up in the next list.
	if _, err := api.UpsertSession(h.ctx, &syncv1.UpsertSessionRequest{Session: &syncv1.SessionMetadata{Info: &agentv1.SessionInfo{Id: sid}}, UpdatePaths: []string{"info.unread"}}); err != nil {
		t.Fatal(err)
	}
	if l := h.list(false)[sid]; l.Unread || !l.Pinned {
		t.Fatalf("after server change: %+v", l)
	}

	// A rename of a session that is not live goes into its transcript file.
	h.closeSession(sid)
	if info, err = h.update(&agentv1.UpdateSessionRequest{SessionId: sid, Title: ptr("Renamed later")}); err != nil || info.Title != "Renamed later" || !info.Pinned {
		t.Fatalf("rename closed: %v %+v", err, info)
	}
	if l := h.list(false)[sid]; l.Title != "Renamed later" {
		t.Fatalf("listed title %q", l.Title)
	}

	// Server unreachable: nothing changes, and the error says so.
	fake.SetFail(func(p string) error { return connect.NewError(connect.CodeUnavailable, errors.New("down")) })
	if _, err := h.update(&agentv1.UpdateSessionRequest{SessionId: sid, Pinned: ptr(false)}); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("offline update: %v", err)
	}
	if l := h.list(false)[sid]; !l.Pinned {
		t.Fatalf("offline list lost the last known state: %+v", l)
	}
	fake.SetFail(nil)

	for _, req := range []*agentv1.UpdateSessionRequest{
		{SessionId: "nope", Pinned: ptr(true)},
		{SessionId: sid, Title: ptr("   ")},
		{SessionId: sid},
	} {
		if _, err := h.update(req); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%+v: %v", req, err)
		}
	}
	if _, err := h.update(&agentv1.UpdateSessionRequest{SessionId: "0b5a2c1e-1111-4222-8333-444455556666", Pinned: ptr(true)}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}
}

func TestArchiveStopsAndHidesSession(t *testing.T) {
	h, fake, _ := startStateHarness(t)
	sid, events := h.create(&agentv1.CreateSessionRequest{})
	info, err := h.update(&agentv1.UpdateSessionRequest{SessionId: sid, Archived: ptr(true)})
	if err != nil || !info.Archived {
		t.Fatalf("archive: %v %+v", err, info)
	}
	ended := false
	for ev := range events {
		if e, ok := ev.Event.(*agentv1.SessionEvent_SessionEnded); ok && e.SessionEnded.Reason == "archived" {
			ended = true
		}
	}
	if !ended {
		t.Fatal("archived session did not end")
	}
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("still live: %v", err)
	}
	if !fake.Session(sid).Meta.Info.Archived {
		t.Fatal("server copy not archived")
	}
	if _, ok := h.list(false)[sid]; ok {
		t.Fatal("archived session in the default list")
	}
	if l := h.list(true)[sid]; l == nil || !l.Archived {
		t.Fatalf("include_archived: %+v", l)
	}
	if _, err := h.update(&agentv1.UpdateSessionRequest{SessionId: sid, Archived: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if l := h.list(false)[sid]; l == nil || l.Archived {
		t.Fatalf("unarchived: %+v", l)
	}
}

func TestDeleteSessionRemovesFilesAndServerCopy(t *testing.T) {
	h, fake, _ := startStateHarness(t)
	sid, _ := h.create(&agentv1.CreateSessionRequest{})
	if _, err := h.update(&agentv1.UpdateSessionRequest{SessionId: sid, Pinned: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	h.closeSession(sid)
	path := transcriptPath(h.proj, sid)
	sub := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "agent-1.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bootstrap.FileHistoryDir(sid), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syncclient.QueueFile("", sid, path); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(syncclient.DefaultDir(), "sessions", sid+".json")
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("upload state: %v", err)
	}

	// Server unreachable: the transcript stays.
	fake.SetFail(func(p string) error {
		if p == "DeleteSession" {
			return connect.NewError(connect.CodeUnavailable, errors.New("down"))
		}
		return nil
	})
	if _, err := h.client.DeleteSession(h.ctx, connect.NewRequest(&agentv1.DeleteSessionRequest{SessionId: sid})); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("offline delete: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("transcript deleted while offline: %v", err)
	}
	fake.SetFail(nil)

	if _, err := h.client.DeleteSession(h.ctx, connect.NewRequest(&agentv1.DeleteSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, strings.TrimSuffix(path, ".jsonl"), bootstrap.FileHistoryDir(sid), stateFile} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists: %v", p, err)
		}
	}
	if fake.Session(sid) != nil {
		t.Fatal("server copy not deleted")
	}
	if _, ok := h.list(true)[sid]; ok {
		t.Fatal("deleted session listed")
	}
	if _, err := h.client.DeleteSession(h.ctx, connect.NewRequest(&agentv1.DeleteSessionRequest{SessionId: sid})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("delete again: %v", err)
	}
	if _, err := h.client.DeleteSession(h.ctx, connect.NewRequest(&agentv1.DeleteSessionRequest{SessionId: "../../etc"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad id: %v", err)
	}

	// A live session is stopped first; one that never reached the server is still deleted locally.
	live, events := h.create(&agentv1.CreateSessionRequest{})
	if _, err := h.client.DeleteSession(h.ctx, connect.NewRequest(&agentv1.DeleteSessionRequest{SessionId: live})); err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	if _, err := os.Stat(transcriptPath(h.proj, live)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("live transcript: %v", err)
	}
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: live})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("still live: %v", err)
	}
}

func TestUpdateSessionWithoutSync(t *testing.T) {
	h := startHarness(t, fg.Sequence(), nil)
	sid, _ := h.create(&agentv1.CreateSessionRequest{})
	if info, err := h.update(&agentv1.UpdateSessionRequest{SessionId: sid, Pinned: ptr(true)}); err != nil || !info.Pinned {
		t.Fatalf("pin without sync: %v %+v", err, info)
	}
	if info, err := h.update(&agentv1.UpdateSessionRequest{SessionId: sid, Title: ptr("Local name")}); err != nil || info.Title != "Local name" {
		t.Fatalf("rename without sync: %v %+v", err, info)
	}
	if _, err := h.client.DeleteSession(context.Background(), connect.NewRequest(&agentv1.DeleteSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.list(true)[sid]; ok {
		t.Fatal("deleted session listed")
	}
}
