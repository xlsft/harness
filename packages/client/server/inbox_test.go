package server_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

func (h *harness) inbox(g agentv1.InboxGroup) *agentv1.ListInboxResponse {
	h.t.Helper()
	res, err := h.client.ListInbox(h.ctx, connect.NewRequest(&agentv1.ListInboxRequest{Group: g}))
	if err != nil {
		h.t.Fatal(err)
	}
	return res.Msg
}

func inboxItem(r *agentv1.ListInboxResponse, sid string) *agentv1.InboxItem {
	for _, it := range r.GetItems() {
		if it.GetSessionId() == sid {
			return it
		}
	}
	return nil
}

func TestInboxLocalSessions(t *testing.T) {
	h := startHarness(t, chatResponder, nil)
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	watch, err := h.client.WatchInbox(ctx, connect.NewRequest(&agentv1.WatchInboxRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	changes := make(chan *agentv1.WatchInboxResponse, 256)
	go func() {
		for watch.Receive() {
			changes <- watch.Msg()
		}
	}()

	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "run mkdir build")
	var req *agentv1.PermissionRequest
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		req = ev.GetPermissionRequest()
		return req != nil
	})
	needs := h.inbox(agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT)
	it := inboxItem(needs, sid)
	if it == nil || len(it.GetRequests()) != 1 || it.GetRequests()[0].GetRequestId() != req.GetRequestId() || it.GetRequests()[0].GetSummary() != "mkdir build" {
		t.Fatalf("needs input: %+v", needs)
	}
	if !it.GetUnread() || it.GetRemote() || !it.GetOpen() {
		t.Fatalf("item flags: %+v", it)
	}
	if c := needs.GetCounts(); len(c) != 4 || c[0].GetTotal() != 1 {
		t.Fatalf("counts %+v", c)
	}
	// The live stream saw it too.
	deadline := time.After(5 * time.Second)
	for seen := false; !seen; {
		select {
		case r := <-changes:
			seen = r.GetChange().GetItem().GetSessionId() == sid && r.GetChange().GetItem().GetGroup() == agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT
		case <-deadline:
			t.Fatal("no live change")
		}
	}

	// Answered from the inbox: the same RPC, by session id.
	if _, err := h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: req.GetRequestId(), Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW})); err != nil {
		t.Fatal(err)
	}
	h.until(ch, isTurnFinished)
	if inboxItem(h.inbox(agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT), sid) != nil {
		t.Fatal("answered request still listed")
	}
	done := inboxItem(h.inbox(agentv1.InboxGroup_INBOX_GROUP_FINISHED), sid)
	if done == nil || done.GetReport() != "ok" || !done.GetUnread() {
		t.Fatalf("finished: %+v", done)
	}
	if _, err := h.client.MarkInboxRead(h.ctx, connect.NewRequest(&agentv1.MarkInboxReadRequest{SessionIds: []string{sid}, Read: true})); err != nil {
		t.Fatal(err)
	}
	if done = inboxItem(h.inbox(agentv1.InboxGroup_INBOX_GROUP_FINISHED), sid); done == nil || done.GetUnread() {
		t.Fatalf("read: %+v", done)
	}
	// Closed and read: it leaves the inbox.
	h.closeSession(sid)
	if inboxItem(h.inbox(agentv1.InboxGroup_INBOX_GROUP_FINISHED), sid) != nil {
		t.Fatal("closed read session still listed")
	}
	if _, err := h.client.ListInbox(h.ctx, connect.NewRequest(&agentv1.ListInboxRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("no group: %v", err)
	}
}

func TestInboxPagesLocally(t *testing.T) {
	h := startHarness(t, chatResponder, nil)
	var ids []string
	for i := 0; i < 5; i++ {
		sid, ch := h.create(&agentv1.CreateSessionRequest{})
		h.send(sid, "hello")
		h.until(ch, isTurnFinished)
		ids = append(ids, sid)
	}
	seen := map[string]bool{}
	token := ""
	for page := 0; page < 10; page++ {
		res, err := h.client.ListInbox(h.ctx, connect.NewRequest(&agentv1.ListInboxRequest{Group: agentv1.InboxGroup_INBOX_GROUP_FINISHED, PageSize: 2, PageToken: token}))
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Msg.GetItems()) > 2 {
			t.Fatalf("page of %d", len(res.Msg.GetItems()))
		}
		for _, it := range res.Msg.GetItems() {
			if seen[it.GetSessionId()] {
				t.Fatalf("%s twice", it.GetSessionId())
			}
			seen[it.GetSessionId()] = true
		}
		if token = res.Msg.GetNextPageToken(); token == "" {
			break
		}
	}
	for _, id := range ids {
		if !seen[id] {
			t.Fatalf("%s not listed", id)
		}
	}
}
