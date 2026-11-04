package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/server"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/syncclient/synctest"
)

const (
	ownDevice   = "dev_me"
	otherDevice = "dev_laptop"
)

// fakeRelay is the viewer side of RelayService: one host with its online state and open sessions, a scripted
// SubscribeSession, and the commands it received.
type fakeRelay struct {
	relayv1connect.UnimplementedRelayServiceHandler

	mu       sync.Mutex
	online   bool
	sessions []*agentv1.SessionInfo
	events   []*relayv1.SubscribeSessionResponse
	sent     []*agentv1.SendUserMessageRequest
	awake    []*agentv1.SetKeepAwakeRequest
	outcome  relayv1.CommandAck_Outcome
}

func (f *fakeRelay) SetKeepAwake(ctx context.Context, req *connect.Request[relayv1.SetKeepAwakeRequest]) (*connect.Response[relayv1.SetKeepAwakeResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.awake = append(f.awake, req.Msg.GetRequest())
	return connect.NewResponse(&relayv1.SetKeepAwakeResponse{Result: &relayv1.CommandResult{CommandId: "cmd_2", Delivery: relayv1.DeliveryState_DELIVERY_STATE_QUEUED}}), nil
}

func (f *fakeRelay) ListHosts(ctx context.Context, req *connect.Request[relayv1.ListHostsRequest]) (*connect.Response[relayv1.ListHostsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&relayv1.ListHostsResponse{Hosts: []*relayv1.Host{{
		HostId: "hst_1", DeviceId: otherDevice, DeviceName: "laptop", Online: f.online, Sessions: f.sessions,
	}}}), nil
}

func (f *fakeRelay) SubscribeSession(ctx context.Context, req *connect.Request[relayv1.SubscribeSessionRequest], stream *connect.ServerStream[relayv1.SubscribeSessionResponse]) error {
	f.mu.Lock()
	evs := f.events
	f.mu.Unlock()
	for _, ev := range evs {
		if se := ev.GetSessionEvent(); se != nil && se.Seq <= req.Msg.GetAfterSeq() {
			continue
		}
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeRelay) SendUserMessage(ctx context.Context, req *connect.Request[relayv1.SendUserMessageRequest]) (*connect.Response[relayv1.SendUserMessageResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, req.Msg.GetRequest())
	return connect.NewResponse(&relayv1.SendUserMessageResponse{Result: &relayv1.CommandResult{CommandId: "cmd_1", Delivery: relayv1.DeliveryState_DELIVERY_STATE_QUEUED}}), nil
}

func (f *fakeRelay) RespondToPermission(ctx context.Context, req *connect.Request[relayv1.RespondToPermissionRequest]) (*connect.Response[relayv1.RespondToPermissionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&relayv1.RespondToPermissionResponse{Result: &relayv1.CommandResult{
		CommandId: "cmd_2", Delivery: relayv1.DeliveryState_DELIVERY_STATE_ACKED, Ack: &relayv1.CommandAck{CommandId: "cmd_2", Outcome: f.outcome},
	}}), nil
}

type remoteHarness struct {
	*harness
	fake  *synctest.Server
	api   *syncclient.Client
	relay *fakeRelay
}

// startRemoteHarness runs a server with sync (and a relay viewer when relay is set) as device ownDevice.
func startRemoteHarness(t *testing.T, relay *fakeRelay) *remoteHarness {
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
	h := startHarnessSrv(t, fg.Sequence(), nil, nil, func(s *server.Server) {
		s.Sync, s.DeviceID, s.Relay = api, ownDevice, viewer
	})
	return &remoteHarness{harness: h, fake: fake, api: api, relay: relay}
}

// seedRemote stores a session of another machine on the fake server with a one-turn transcript.
func (h *remoteHarness) seedRemote(device, cwd, prompt, answer string) string {
	h.t.Helper()
	id := uuid.NewString()
	info := &agentv1.SessionInfo{Id: id, Title: prompt, Cwd: cwd, DeviceId: device, DeviceName: "laptop",
		Status: agentv1.SessionStatus_SESSION_STATUS_IDLE, UpdatedAt: "2026-11-01T10:00:05Z", Model: "m"}
	if _, err := h.api.UpsertSession(h.ctx, &syncv1.UpsertSessionRequest{Session: &syncv1.SessionMetadata{Info: info}}); err != nil {
		h.t.Fatal(err)
	}
	line := func(v map[string]any) *syncv1.TranscriptEntry {
		v["sessionId"], v["cwd"] = id, cwd
		b, _ := json.Marshal(v)
		return &syncv1.TranscriptEntry{Uuid: v["uuid"].(string), Type: v["type"].(string), EntryJson: string(b)}
	}
	entries := []*syncv1.TranscriptEntry{
		line(map[string]any{"type": "user", "uuid": "u1", "parentUuid": nil, "promptId": "p1", "timestamp": "2026-11-01T10:00:00.000Z",
			"message": map[string]any{"role": "user", "content": prompt}}),
		line(map[string]any{"type": "assistant", "uuid": "a1", "parentUuid": "u1", "timestamp": "2026-11-01T10:00:02.000Z",
			"message": map[string]any{"id": "msg_1", "role": "assistant", "model": "m", "stop_reason": "end_turn",
				"content": []map[string]any{{"type": "text", "text": answer}}, "usage": map[string]any{"input_tokens": 3, "output_tokens": 2}}}),
	}
	if _, err := h.api.AppendSessionEvents(h.ctx, &syncv1.AppendSessionEventsRequest{SessionId: id, Entries: entries}); err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h *remoteHarness) listAll() map[string]*agentv1.SessionInfo {
	h.t.Helper()
	resp, err := h.client.ListSessions(h.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{}))
	if err != nil {
		h.t.Fatal(err)
	}
	out := map[string]*agentv1.SessionInfo{}
	for _, s := range resp.Msg.Sessions {
		if _, dup := out[s.Id]; dup {
			h.t.Fatalf("session %s listed twice", s.Id)
		}
		out[s.Id] = s
	}
	return out
}

// drain reads a Subscribe stream to its end.
func (h *remoteHarness) drain(sid string, after int64) ([]*agentv1.SessionEvent, error) {
	stream, err := h.client.Subscribe(h.ctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid, AfterSeq: after}))
	if err != nil {
		return nil, err
	}
	var got []*agentv1.SessionEvent
	for stream.Receive() {
		got = append(got, stream.Msg().Event)
	}
	return got, stream.Err()
}

func kinds(evs []*agentv1.SessionEvent) string {
	var out []string
	for _, ev := range evs {
		out = append(out, evKind(ev))
	}
	return strings.Join(out, " ")
}

func TestListSessionsMergesSessionsOfOtherMachines(t *testing.T) {
	h := startRemoteHarness(t, nil)
	local, _ := h.create(&agentv1.CreateSessionRequest{})
	remote := h.seedRemote(otherDevice, "/remote/proj", "hello from the laptop", "hi")
	// The server's copy of a local session (possibly with stale device data) and an older session of this
	// machine whose transcript is not listed: neither is shown as remote.
	if _, err := h.api.UpsertSession(h.ctx, &syncv1.UpsertSessionRequest{Session: &syncv1.SessionMetadata{Info: &agentv1.SessionInfo{Id: local, Cwd: h.proj, DeviceId: otherDevice}}}); err != nil {
		t.Fatal(err)
	}
	own := h.seedRemote(ownDevice, "/old/proj", "older", "x")

	all := h.listAll()
	if l := all[local]; l == nil || l.Remote {
		t.Fatalf("local session: %+v", l)
	}
	r := all[remote]
	if r == nil || !r.Remote || r.DeviceId != otherDevice || r.DeviceName != "laptop" || r.Cwd != "/remote/proj" || r.Title != "hello from the laptop" {
		t.Fatalf("remote session: %+v", r)
	}
	if r.DeviceOnline != nil || r.RemoteControllable {
		t.Fatalf("presence without a relay must be unknown: %+v", r)
	}
	if all[own] != nil {
		t.Fatalf("a session of this device is not remote: %+v", all[own])
	}

	// A cwd listing shows the remote sessions of that folder only.
	if h.list(false)[remote] != nil {
		t.Fatal("remote session of another folder in a cwd listing")
	}

	// The server stops answering: the last listing stays.
	h.fake.SetFail(func(string) error { return connect.NewError(connect.CodeUnavailable, errors.New("down")) })
	if h.listAll()[remote] == nil {
		t.Fatal("remote session lost while the server is unreachable")
	}
	h.fake.SetFail(nil)
}

func TestSubscribeRemoteSessionReplaysServerTranscript(t *testing.T) {
	h := startRemoteHarness(t, nil)
	remote := h.seedRemote(otherDevice, "/remote/proj", "hello from the laptop", "hi there")
	h.listAll()

	evs, err := h.drain(remote, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(evs); got != "user turn asst done updated" {
		t.Fatalf("events: %s", got)
	}
	for i, ev := range evs {
		if ev.Seq != int64(i+1) || ev.SessionId != remote {
			t.Fatalf("event %d: seq %d session %q", i, ev.Seq, ev.SessionId)
		}
	}
	if txt := evs[2].GetAssistantMessage().GetContent()[0].GetText().GetText(); txt != "hi there" {
		t.Fatalf("assistant text %q", txt)
	}
	if u := evs[4].GetSessionUpdated(); !u.Remote || u.DeviceName != "laptop" {
		t.Fatalf("session updated: %+v", u)
	}
	// after_seq skips what the frontend has.
	if evs, err = h.drain(remote, 3); err != nil || kinds(evs) != "done updated" {
		t.Fatalf("after_seq 3: %v %s", err, kinds(evs))
	}

	// Without remote control nothing can be sent to it, and it cannot be resumed here.
	_, err = h.client.SendUserMessage(h.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: remote, Content: text("hi")}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "laptop") {
		t.Fatalf("send to remote: %v", err)
	}
	_, err = h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: remote}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("resume remote: %v", err)
	}
}

func TestRemoteSessionStateAndDeleteGoToServer(t *testing.T) {
	h := startRemoteHarness(t, nil)
	remote := h.seedRemote(otherDevice, "/remote/proj", "hello", "hi")
	h.listAll()

	info, err := h.update(&agentv1.UpdateSessionRequest{SessionId: remote, Pinned: ptr(true), Title: ptr("Laptop work")})
	if err != nil {
		t.Fatal(err)
	}
	if !info.Pinned || info.Title != "Laptop work" || !info.Remote {
		t.Fatalf("update response: %+v", info)
	}
	ss := h.fake.Session(remote)
	if !ss.Meta.Info.Pinned || ss.Meta.Info.Title != "Laptop work" || ss.Meta.Info.Cwd != "/remote/proj" || ss.Meta.Info.DeviceId != otherDevice {
		t.Fatalf("server copy: %+v", ss.Meta.Info)
	}
	if l := h.listAll()[remote]; !l.Pinned || l.Title != "Laptop work" {
		t.Fatalf("listed: %+v", l)
	}
	if _, err := h.update(&agentv1.UpdateSessionRequest{SessionId: remote, Archived: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if h.listAll()[remote] != nil {
		t.Fatal("archived remote session listed")
	}

	if _, err := h.client.DeleteSession(h.ctx, connect.NewRequest(&agentv1.DeleteSessionRequest{SessionId: remote})); err != nil {
		t.Fatal(err)
	}
	if h.fake.Session(remote) != nil {
		t.Fatal("server copy not deleted")
	}
	resp, err := h.client.ListSessions(h.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{IncludeArchived: true}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range resp.Msg.Sessions {
		if s.Id == remote {
			t.Fatal("deleted remote session listed")
		}
	}
}

func TestForkRemoteSessionContinuesHere(t *testing.T) {
	h := startRemoteHarness(t, nil)
	remote := h.seedRemote(otherDevice, "/remote/proj", "hello from the laptop", "hi there")
	h.listAll()

	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: remote, Fork: true}))
	if err != nil {
		t.Fatal(err)
	}
	info := cs.Msg.Session
	if info.Id == remote || info.Remote || info.ParentSessionId != remote || info.Cwd != h.proj {
		t.Fatalf("fork: %+v", info)
	}
	evs := h.until(h.subscribe(info.Id), func(ev *agentv1.SessionEvent) bool { return ev.GetSessionUpdated() != nil })
	if got := kinds(evs); !strings.HasPrefix(got, "user turn asst done") {
		t.Fatalf("fork history: %s", got)
	}
	data, err := os.ReadFile(transcriptPath(h.proj, info.Id))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/remote/proj") || !strings.Contains(string(data), `"cwd":"`+h.proj+`"`) {
		t.Fatalf("transcript keeps the other machine's folder:\n%s", data)
	}
	// The fork is local; the original stays remote.
	all := h.listAll()
	if all[info.Id] == nil || all[info.Id].Remote || all[remote] == nil || !all[remote].Remote {
		t.Fatalf("after fork: %+v / %+v", all[info.Id], all[remote])
	}
}

func TestRemoteControlThroughRelay(t *testing.T) {
	relay := &fakeRelay{online: true}
	h := startRemoteHarness(t, relay)
	remote := h.seedRemote(otherDevice, "/remote/proj", "hello", "hi")
	idle := h.seedRemote(otherDevice, "/remote/proj", "closed there", "ok")
	relay.sessions = []*agentv1.SessionInfo{{Id: remote, Status: agentv1.SessionStatus_SESSION_STATUS_WORKING}}
	live := &agentv1.SessionInfo{Id: remote, Title: "hello", Cwd: "/remote/proj", Status: agentv1.SessionStatus_SESSION_STATUS_WORKING}
	relay.events = []*relayv1.SubscribeSessionResponse{
		{Event: &relayv1.SubscribeSessionResponse_SessionEvent{SessionEvent: &agentv1.SessionEvent{Seq: 1, SessionId: remote,
			Event: &agentv1.SessionEvent_UserMessage{UserMessage: &agentv1.UserMessage{Uuid: "u1", Content: text("hello")}}}}},
		{Event: &relayv1.SubscribeSessionResponse_SessionEvent{SessionEvent: &agentv1.SessionEvent{Seq: 2, SessionId: remote,
			Event: &agentv1.SessionEvent_SessionUpdated{SessionUpdated: live}}}},
		{Event: &relayv1.SubscribeSessionResponse_FlushComplete{FlushComplete: &relayv1.FlushComplete{LastSeq: 2}}},
		{Event: &relayv1.SubscribeSessionResponse_HostState{HostState: &relayv1.HostState{HostId: "hst_1", DeviceName: "laptop", Online: false}}},
	}

	all := h.listAll()
	r := all[remote]
	if r == nil || !r.Remote || r.DeviceOnline == nil || !*r.DeviceOnline || !r.RemoteControllable || r.Status != agentv1.SessionStatus_SESSION_STATUS_WORKING {
		t.Fatalf("live remote session: %+v", r)
	}
	if i := all[idle]; i == nil || i.DeviceOnline == nil || !*i.DeviceOnline || i.RemoteControllable {
		t.Fatalf("remote session not open on its online machine: %+v", i)
	}

	// Subscribe follows the relay; SessionUpdated keeps the origin, the host going offline is reported.
	evs, err := h.drain(remote, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(evs); got != "user updated updated" {
		t.Fatalf("relay events: %s", got)
	}
	if u := evs[1].GetSessionUpdated(); !u.Remote || !u.RemoteControllable || u.DeviceName != "laptop" || u.DeviceId != otherDevice {
		t.Fatalf("relayed session updated: %+v", u)
	}
	if u := evs[2].GetSessionUpdated(); evs[2].Seq != 0 || u.RemoteControllable || *u.DeviceOnline || u.Status != agentv1.SessionStatus_SESSION_STATUS_WAITING_FOR_CONNECTION {
		t.Fatalf("host offline: seq %d %+v", evs[2].Seq, u)
	}

	// Commands go to the relay.
	h.send(remote, "from the desktop")
	relay.mu.Lock()
	if len(relay.sent) != 1 || relay.sent[0].GetSessionId() != remote || relay.sent[0].GetContent()[0].GetText().GetText() != "from the desktop" {
		t.Fatalf("relayed messages: %+v", relay.sent)
	}
	relay.mu.Unlock()

	// The keep-awake switch keeps the session's own machine awake.
	if _, err := h.client.SetKeepAwake(h.ctx, connect.NewRequest(&agentv1.SetKeepAwakeRequest{SessionId: remote, Enabled: true})); err != nil {
		t.Fatal(err)
	}
	relay.mu.Lock()
	if len(relay.awake) != 1 || relay.awake[0].GetSessionId() != remote || !relay.awake[0].GetEnabled() {
		t.Fatalf("relayed keep awake: %+v", relay.awake)
	}
	relay.mu.Unlock()
	if _, err := h.client.SetKeepAwake(h.ctx, connect.NewRequest(&agentv1.SetKeepAwakeRequest{SessionId: idle, Enabled: true})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("keep awake of a remote session not open there: %v", err)
	}

	relay.mu.Lock()
	relay.outcome = relayv1.CommandAck_OUTCOME_ALREADY_RESOLVED
	relay.mu.Unlock()
	_, err = h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: remote, RequestId: "perm_1", Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "already answered") {
		t.Fatalf("already resolved: %v", err)
	}

	// A session that is not open on its machine is read from the server instead.
	if evs, err := h.drain(idle, 0); err != nil || kinds(evs) != "user turn asst done updated" {
		t.Fatalf("idle remote: %v %s", err, kinds(evs))
	}
	_, err = h.client.SendUserMessage(h.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: idle, Content: text("hi")}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "not open") {
		t.Fatalf("send to idle remote: %v", err)
	}

	// The machine goes offline.
	relay.mu.Lock()
	relay.online = false
	relay.mu.Unlock()
	if r := h.listAll()[remote]; r.DeviceOnline == nil || *r.DeviceOnline || r.RemoteControllable || r.Status != agentv1.SessionStatus_SESSION_STATUS_IDLE {
		t.Fatalf("offline: %+v", r)
	}
	_, err = h.client.SendUserMessage(h.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: remote, Content: text("hi")}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("send to offline remote: %v", err)
	}
}
