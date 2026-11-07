package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	"github.com/xlsft/harness/packages/client/server"
)

// fakeInbox is the server's InboxService: a page of items of the user's other machines and the read markers it got.
type fakeInbox struct {
	relayv1connect.UnimplementedInboxServiceHandler
	mu    sync.Mutex
	items []*agentv1.InboxItem
	reads []string
	push  chan *agentv1.WatchInboxResponse
}

func (f *fakeInbox) ListInbox(ctx context.Context, req *connect.Request[relayv1.ListInboxRequest]) (*connect.Response[relayv1.ListInboxResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*agentv1.InboxItem
	for _, it := range f.items {
		if it.GetGroup() == req.Msg.GetRequest().GetGroup() {
			out = append(out, it)
		}
	}
	return connect.NewResponse(&relayv1.ListInboxResponse{Response: &agentv1.ListInboxResponse{Items: out,
		Counts: []*agentv1.InboxCount{{Group: agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT, Total: int32(len(out)), Unread: 1}}}}), nil
}

func (f *fakeInbox) MarkInboxRead(ctx context.Context, req *connect.Request[relayv1.MarkInboxReadRequest]) (*connect.Response[relayv1.MarkInboxReadResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, req.Msg.GetRequest().GetSessionIds()...)
	return connect.NewResponse(&relayv1.MarkInboxReadResponse{}), nil
}

func (f *fakeInbox) WatchInbox(ctx context.Context, req *connect.Request[relayv1.WatchInboxRequest], st *connect.ServerStream[relayv1.WatchInboxResponse]) error {
	if err := st.Send(&relayv1.WatchInboxResponse{Response: &agentv1.WatchInboxResponse{Event: &agentv1.WatchInboxResponse_CaughtUp{CaughtUp: true}}}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case r := <-f.push:
			if err := st.Send(&relayv1.WatchInboxResponse{Response: r}); err != nil {
				return err
			}
		}
	}
}

func TestInboxMergesOtherMachines(t *testing.T) {
	relay := &fakeRelay{online: true, outcome: relayv1.CommandAck_OUTCOME_OK}
	inbox := &fakeInbox{push: make(chan *agentv1.WatchInboxResponse, 4)}
	mux := http.NewServeMux()
	mux.Handle(relayv1connect.NewRelayServiceHandler(relay))
	mux.Handle(relayv1connect.NewInboxServiceHandler(inbox))
	rs := httptest.NewUnstartedServer(mux)
	rs.EnableHTTP2 = true
	rs.Start()
	t.Cleanup(rs.Close)
	viewer := server.NewRelayViewer(rs.URL, nil, nil)
	h := startHarnessSrv(t, chatResponder, nil, nil, func(s *server.Server) { s.DeviceID, s.Relay = ownDevice, viewer })

	local, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(local, "run mkdir build")
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetPermissionRequest() != nil })

	remote := uuid.NewString()
	on := true
	inbox.mu.Lock()
	inbox.items = []*agentv1.InboxItem{
		{SessionId: remote, Title: "on the laptop", DeviceId: otherDevice, DeviceName: "laptop", Group: agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT, Unread: true,
			Open: true, DeviceOnline: &on, Requests: []*agentv1.InboxRequest{{Kind: agentv1.InboxRequest_KIND_PERMISSION, RequestId: "r1", ToolName: "Bash"}}},
		// The server's stale copy of the local session: the local item wins.
		{SessionId: local, Title: "stale", DeviceId: ownDevice, Group: agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT},
	}
	inbox.mu.Unlock()
	res := h.inbox(agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT)
	if len(res.GetItems()) != 2 {
		t.Fatalf("items %+v", res.GetItems())
	}
	if it := inboxItem(res, local); it == nil || it.GetTitle() == "stale" || it.GetRemote() {
		t.Fatalf("local %+v", it)
	}
	if it := inboxItem(res, remote); it == nil || !it.GetRemote() || it.GetDeviceName() != "laptop" {
		t.Fatalf("remote %+v", it)
	}
	// Answered from the inbox: it goes through the relay to the laptop.
	if _, err := h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: remote, RequestId: "r1", Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW})); err != nil {
		t.Fatalf("remote answer: %v", err)
	}
	if _, err := h.client.MarkInboxRead(h.ctx, connect.NewRequest(&agentv1.MarkInboxReadRequest{SessionIds: []string{remote}, Read: true})); err != nil {
		t.Fatal(err)
	}
	inbox.mu.Lock()
	reads := append([]string(nil), inbox.reads...)
	inbox.mu.Unlock()
	if len(reads) != 1 || reads[0] != remote {
		t.Fatalf("reads %v", reads)
	}

	// Live: a change of another machine comes through, one of a local session does not.
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	watch, err := h.client.WatchInbox(ctx, connect.NewRequest(&agentv1.WatchInboxRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan *agentv1.WatchInboxResponse, 16)
	go func() {
		for watch.Receive() {
			got <- watch.Msg()
		}
	}()
	time.Sleep(200 * time.Millisecond)
	inbox.push <- &agentv1.WatchInboxResponse{Event: &agentv1.WatchInboxResponse_Change{Change: &agentv1.InboxChange{Item: &agentv1.InboxItem{SessionId: local, Title: "stale"}}}}
	inbox.push <- &agentv1.WatchInboxResponse{Event: &agentv1.WatchInboxResponse_Change{Change: &agentv1.InboxChange{RemovedSessionId: remote}}}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case r := <-got:
			if r.GetChange().GetItem().GetSessionId() == local && r.GetChange().GetItem().GetTitle() == "stale" {
				t.Fatal("server change of a local session forwarded")
			}
			if r.GetChange().GetRemovedSessionId() == remote {
				return
			}
		case <-deadline:
			t.Fatal("no server change")
		}
	}
}
