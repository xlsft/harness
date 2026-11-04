package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
)

// ---- fake RelayService ----

type fakeRelay struct {
	relayv1connect.UnimplementedRelayServiceHandler

	mu         sync.Mutex
	cond       *sync.Cond
	hostRegs   int
	streams    int
	epochs     map[string]int64
	stored     map[string]map[int64]*relayv1.EventEnvelope // durable events by seq
	acked      map[string]int64
	deltas     map[string][]*relayv1.EventEnvelope
	batches    []int
	acks       []*relayv1.CommandAck
	heartbeats []*relayv1.HeartbeatRequest
	unregs     []string
	// failPublish makes the next n PublishEvents calls fail with UNAVAILABLE.
	failPublish int
	// rejectEpochOnce makes the next PublishEvents fail with FAILED_PRECONDITION.
	rejectEpochOnce bool
	// initialAcked is returned by the first RegisterSession of a session (a resumed history).
	initialAcked map[string]int64
	// dropSeq is never stored (simulates an event lost on the server).
	dropSeq    map[int64]bool
	superseded []*relayv1.SessionSuperseded
	push       chan *relayv1.OpenHostStreamResponse
	kill       chan struct{}
}

func newFakeRelay() *fakeRelay {
	f := &fakeRelay{epochs: map[string]int64{}, stored: map[string]map[int64]*relayv1.EventEnvelope{}, acked: map[string]int64{},
		deltas: map[string][]*relayv1.EventEnvelope{}, initialAcked: map[string]int64{}, dropSeq: map[int64]bool{},
		push: make(chan *relayv1.OpenHostStreamResponse, 64), kill: make(chan struct{}, 1)}
	f.cond = sync.NewCond(&f.mu)
	return f
}

func (f *fakeRelay) RegisterHost(_ context.Context, _ *connect.Request[relayv1.RegisterHostRequest]) (*connect.Response[relayv1.RegisterHostResponse], error) {
	f.mu.Lock()
	f.hostRegs++
	f.mu.Unlock()
	return connect.NewResponse(&relayv1.RegisterHostResponse{HostId: "hst_test", HeartbeatIntervalSeconds: 20}), nil
}

func (f *fakeRelay) OpenHostStream(ctx context.Context, _ *connect.Request[relayv1.OpenHostStreamRequest], s *connect.ServerStream[relayv1.OpenHostStreamResponse]) error {
	f.mu.Lock()
	f.streams++
	f.cond.Broadcast()
	f.mu.Unlock()
	if err := s.Send(&relayv1.OpenHostStreamResponse{Event: &relayv1.OpenHostStreamResponse_Heartbeat{Heartbeat: &relayv1.Heartbeat{}}}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-f.kill:
			return connect.NewError(connect.CodeUnavailable, errors.New("killed"))
		case m := <-f.push:
			if err := s.Send(m); err != nil {
				return err
			}
		}
	}
}

func (f *fakeRelay) RegisterSession(_ context.Context, req *connect.Request[relayv1.RegisterSessionRequest]) (*connect.Response[relayv1.RegisterSessionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := req.Msg.GetSession().GetId()
	if f.epochs[id] == 0 {
		if a := f.initialAcked[id]; a > 0 {
			f.acked[id] = a
		}
	}
	f.epochs[id]++
	f.cond.Broadcast()
	return connect.NewResponse(&relayv1.RegisterSessionResponse{Epoch: f.epochs[id], AckedSeq: f.acked[id]}), nil
}

func (f *fakeRelay) UnregisterSession(_ context.Context, req *connect.Request[relayv1.UnregisterSessionRequest]) (*connect.Response[relayv1.UnregisterSessionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unregs = append(f.unregs, req.Msg.GetSessionId())
	f.cond.Broadcast()
	return connect.NewResponse(&relayv1.UnregisterSessionResponse{}), nil
}

func (f *fakeRelay) PublishEvents(_ context.Context, req *connect.Request[relayv1.PublishEventsRequest]) (*connect.Response[relayv1.PublishEventsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	defer f.cond.Broadcast()
	id := req.Msg.GetSessionId()
	if f.failPublish > 0 {
		f.failPublish--
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("network down"))
	}
	if f.rejectEpochOnce || req.Msg.GetEpoch() != f.epochs[id] {
		f.rejectEpochOnce = false
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("stale epoch"))
	}
	f.batches = append(f.batches, len(req.Msg.GetEvents()))
	if f.stored[id] == nil {
		f.stored[id] = map[int64]*relayv1.EventEnvelope{}
	}
	dups := 0
	for _, e := range req.Msg.GetEvents() {
		if _, ok := e.GetEvent().GetEvent().(*agentv1.SessionEvent_AssistantDelta); ok {
			f.deltas[id] = append(f.deltas[id], e)
			continue
		}
		seq := e.GetEvent().GetSeq()
		if f.dropSeq[seq] {
			delete(f.dropSeq, seq)
			continue
		}
		if _, ok := f.stored[id][seq]; ok {
			dups++
			continue
		}
		f.stored[id][seq] = e
	}
	for f.stored[id][f.acked[id]+1] != nil {
		f.acked[id]++
	}
	return connect.NewResponse(&relayv1.PublishEventsResponse{AckedSeq: f.acked[id], Duplicates: int32(dups)}), nil
}

func (f *fakeRelay) AckCommands(_ context.Context, req *connect.Request[relayv1.AckCommandsRequest]) (*connect.Response[relayv1.AckCommandsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks = append(f.acks, req.Msg.GetAcks()...)
	f.cond.Broadcast()
	return connect.NewResponse(&relayv1.AckCommandsResponse{}), nil
}

func (f *fakeRelay) Heartbeat(_ context.Context, req *connect.Request[relayv1.HeartbeatRequest]) (*connect.Response[relayv1.HeartbeatResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeats = append(f.heartbeats, req.Msg)
	sup := f.superseded
	f.superseded = nil
	f.cond.Broadcast()
	return connect.NewResponse(&relayv1.HeartbeatResponse{Superseded: sup}), nil
}

// waitFor blocks until cond holds (checked under f.mu) or fails the test.
func (f *fakeRelay) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		f.mu.Lock()
		for !cond() {
			f.cond.Wait()
		}
		f.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		f.mu.Lock()
		f.cond.Broadcast()
		state := fmt.Sprintf("acked=%v epochs=%v batches=%v", f.acked, f.epochs, f.batches)
		f.mu.Unlock()
		t.Fatalf("timed out waiting for %s (%s)", what, state)
	}
}

func (f *fakeRelay) ackFor(id string) *relayv1.CommandAck {
	for _, a := range f.acks {
		if a.GetCommandId() == id {
			return a
		}
	}
	return nil
}

func (f *fakeRelay) storedSeqs(id string) []int64 {
	var out []int64
	for s := range f.stored[id] {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ---- fake backend ----

type fakeFeed struct {
	mu     sync.Mutex
	events []*agentv1.SessionEvent
	subs   map[chan *agentv1.SessionEvent]bool
	seq    int64
	closed bool
	info   *agentv1.SessionInfo
	// relayOff is the per-session remote control switch turned off.
	relayOff atomic.Bool
}

func newFakeFeed(id string) *fakeFeed {
	return &fakeFeed{subs: map[chan *agentv1.SessionEvent]bool{}, info: &agentv1.SessionInfo{Id: id, Cwd: "/w", Status: agentv1.SessionStatus_SESSION_STATUS_IDLE}}
}

func (f *fakeFeed) emit(durable bool, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ev := &agentv1.SessionEvent{SessionId: f.info.Id}
	if durable {
		f.seq++
		ev.Event = &agentv1.SessionEvent_Notice{Notice: &agentv1.Notice{Text: text}}
		f.events = append(f.events, ev)
	} else {
		ev.Event = &agentv1.SessionEvent_AssistantDelta{AssistantDelta: &agentv1.AssistantDelta{Delta: &agentv1.AssistantDelta_Text{Text: text}}}
	}
	ev.Seq = f.seq
	for ch := range f.subs {
		ch <- ev
	}
}

func (f *fakeFeed) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	for ch := range f.subs {
		close(ch)
		delete(f.subs, ch)
	}
}

func (f *fakeFeed) Subscribe(after int64) ([]*agentv1.SessionEvent, <-chan *agentv1.SessionEvent, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var replay []*agentv1.SessionEvent
	for _, e := range f.events {
		if e.GetSeq() > after {
			replay = append(replay, e)
		}
	}
	ch := make(chan *agentv1.SessionEvent, 4096)
	if f.closed {
		close(ch)
		return replay, ch, func() {}
	}
	f.subs[ch] = true
	return replay, ch, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.subs[ch] {
			delete(f.subs, ch)
			close(ch)
		}
	}
}

// waitSubscribed waits until the host follows the feed live (deltas are never replayed).
func waitSubscribed(t *testing.T, f *fakeFeed) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.subs)
		f.mu.Unlock()
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("host never subscribed to the feed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *fakeFeed) Closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}
func (f *fakeFeed) Info() *agentv1.SessionInfo { return f.info }
func (f *fakeFeed) RemoteControl() bool {
	return f.info.GetTitle() != "no-remote" && !f.relayOff.Load()
}

func (b *fakeBackend) SetKeepAwake(_ context.Context, req *connect.Request[agentv1.SetKeepAwakeRequest]) (*connect.Response[agentv1.SetKeepAwakeResponse], error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.keepAwake = append(b.keepAwake, req.Msg)
	return connect.NewResponse(&agentv1.SetKeepAwakeResponse{}), nil
}

type fakeBackend struct {
	keepAwake []*agentv1.SetKeepAwakeRequest
	agentv1connect.UnimplementedAgentServiceHandler
	mu       sync.Mutex
	feeds    map[string]*fakeFeed
	messages []*agentv1.SendUserMessageRequest
	modes    []agentv1.PermissionMode
	created  []*agentv1.CreateSessionRequest
}

func newFakeBackend() *fakeBackend { return &fakeBackend{feeds: map[string]*fakeFeed{}} }

func (b *fakeBackend) SessionFeed(id string) (Feed, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, ok := b.feeds[id]
	if !ok {
		return nil, false
	}
	return f, true
}

func (b *fakeBackend) SendUserMessage(_ context.Context, req *connect.Request[agentv1.SendUserMessageRequest]) (*connect.Response[agentv1.SendUserMessageResponse], error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.messages = append(b.messages, req.Msg)
	return connect.NewResponse(&agentv1.SendUserMessageResponse{}), nil
}

func (b *fakeBackend) SetPermissionMode(_ context.Context, req *connect.Request[agentv1.SetPermissionModeRequest]) (*connect.Response[agentv1.SetPermissionModeResponse], error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.modes = append(b.modes, req.Msg.GetMode())
	return connect.NewResponse(&agentv1.SetPermissionModeResponse{}), nil
}

func (b *fakeBackend) RespondToPermission(_ context.Context, req *connect.Request[agentv1.RespondToPermissionRequest]) (*connect.Response[agentv1.RespondToPermissionResponse], error) {
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no pending permission request %s", req.Msg.GetRequestId()))
}

func (b *fakeBackend) SuggestFiles(_ context.Context, req *connect.Request[agentv1.SuggestFilesRequest]) (*connect.Response[agentv1.SuggestFilesResponse], error) {
	return connect.NewResponse(&agentv1.SuggestFilesResponse{Paths: []string{"main.go", req.Msg.GetQuery()}}), nil
}

func (b *fakeBackend) CreateSession(_ context.Context, req *connect.Request[agentv1.CreateSessionRequest]) (*connect.Response[agentv1.CreateSessionResponse], error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.created = append(b.created, req.Msg)
	return connect.NewResponse(&agentv1.CreateSessionResponse{Session: &agentv1.SessionInfo{Id: "spawned-1", Cwd: req.Msg.GetCwd()}}), nil
}

func (b *fakeBackend) messageCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.messages)
}

// ---- harness ----

type rig struct {
	fake    *fakeRelay
	backend *fakeBackend
	host    *Host
	ctx     context.Context
}

func newRig(t *testing.T, mod func(*Options)) *rig {
	t.Helper()
	fake := newFakeRelay()
	path, h := relayv1connect.NewRelayServiceHandler(fake)
	mux := httptestMux(path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	be := newFakeBackend()
	o := Options{ServerURL: srv.URL, Backend: be, FlushInterval: 20 * time.Millisecond, MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
		HeartbeatInterval: 50 * time.Millisecond, InstanceID: "inst-1"}
	if mod != nil {
		mod(&o)
	}
	host := New(o)
	go func() { _ = host.Run(ctx) }()
	return &rig{fake: fake, backend: be, host: host, ctx: ctx}
}

func (r *rig) addSession(id string) *fakeFeed {
	f := newFakeFeed(id)
	r.backend.mu.Lock()
	r.backend.feeds[id] = f
	r.backend.mu.Unlock()
	r.host.AddSession(r.ctx, id)
	return f
}

func (r *rig) send(c *relayv1.Command) {
	r.fake.push <- &relayv1.OpenHostStreamResponse{Event: &relayv1.OpenHostStreamResponse_Command{Command: c}}
}

func httptestMux(path string, h http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle(path, h)
	return mux
}

func future() string { return time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano) }

// ---- tests ----

func TestPublishesInBatchesWithSeqAndDeltas(t *testing.T) {
	r := newRig(t, nil)
	f := r.addSession("s1")
	waitSubscribed(t, f)
	for i := 1; i <= 150; i++ {
		f.emit(true, fmt.Sprint("e", i))
		if i%10 == 0 {
			f.emit(false, "delta")
		}
	}
	r.fake.waitFor(t, "150 events", func() bool { return r.fake.acked["s1"] == 150 })
	r.fake.mu.Lock()
	defer r.fake.mu.Unlock()
	for _, n := range r.fake.batches {
		if n > 64 {
			t.Fatalf("batch of %d events exceeds 64", n)
		}
	}
	if len(r.fake.deltas["s1"]) == 0 {
		t.Fatal("no deltas were published")
	}
	for _, d := range r.fake.deltas["s1"] {
		if d.GetEvent().GetSeq()%10 != 0 {
			t.Fatalf("delta carries seq %d, want the seq of the durable event before it", d.GetEvent().GetSeq())
		}
	}
	// durable envelope uuids are stable per (instance, session, local seq)
	if u := r.fake.stored["s1"][7].GetUuid(); u == "" || u != (&hostSession{h: r.host, id: "s1"}).envelope(&agentv1.SessionEvent{Seq: 7}, 0).GetUuid() {
		t.Fatalf("unexpected uuid %q", u)
	}
}

func TestResumedHistoryContinuesServerSeq(t *testing.T) {
	r := newRig(t, nil)
	r.fake.mu.Lock()
	r.fake.initialAcked["s1"] = 50
	r.fake.mu.Unlock()
	f := r.addSession("s1")
	f.emit(true, "a")
	f.emit(true, "b")
	r.fake.waitFor(t, "seq 52", func() bool { return r.fake.acked["s1"] == 52 })
	if got := r.fake.stored["s1"][51].GetEvent().GetNotice().GetText(); got != "a" {
		t.Fatalf("seq 51 = %q, want the first local event", got)
	}
}

func TestRetriesWithoutDeltasAndResendsFromAcked(t *testing.T) {
	r := newRig(t, nil)
	f := r.addSession("s1")
	f.emit(true, "1")
	r.fake.waitFor(t, "seq 1", func() bool { return r.fake.acked["s1"] == 1 })

	// network failure: the batch is retried without its deltas
	r.fake.mu.Lock()
	r.fake.failPublish = 2
	r.fake.mu.Unlock()
	f.emit(false, "lost-delta")
	f.emit(true, "2")
	r.fake.waitFor(t, "seq 2", func() bool { return r.fake.acked["s1"] == 2 })
	r.fake.mu.Lock()
	for _, d := range r.fake.deltas["s1"] {
		if d.GetEvent().GetAssistantDelta().GetText() == "lost-delta" {
			t.Fatal("a delta from a failed batch was resent")
		}
	}
	// the server loses seq 3: acked_seq stays at 2 and the host resends from there
	r.fake.dropSeq[3] = true
	r.fake.mu.Unlock()
	f.emit(true, "3")
	f.emit(true, "4")
	r.fake.waitFor(t, "seq 4 after resend", func() bool { return r.fake.acked["s1"] == 4 })

	// the epoch is lost: the host registers again and continues after acked_seq
	r.fake.mu.Lock()
	r.fake.rejectEpochOnce = true
	r.fake.mu.Unlock()
	f.emit(true, "5")
	r.fake.waitFor(t, "seq 5 after re-register", func() bool { return r.fake.acked["s1"] == 5 && r.fake.epochs["s1"] == 2 })
	if got := r.fake.storedSeqs("s1"); len(got) != 5 {
		t.Fatalf("stored %v", got)
	}
}

func TestReconnectsHostStream(t *testing.T) {
	r := newRig(t, nil)
	r.fake.waitFor(t, "first stream", func() bool { return r.fake.streams == 1 })
	r.fake.kill <- struct{}{}
	r.fake.waitFor(t, "second stream", func() bool { return r.fake.streams == 2 && r.fake.hostRegs >= 2 })
}

func TestCommandsRunOnceAndAckWithOutcomes(t *testing.T) {
	r := newRig(t, nil)
	r.addSession("s1")
	r.fake.waitFor(t, "registration", func() bool { return r.fake.epochs["s1"] == 1 })
	epoch := int64(1)
	msg := &relayv1.Command{CommandId: "cmd_1", SessionId: "s1", Epoch: epoch, ExpiresAt: future(),
		Payload: &relayv1.Command_SendUserMessage{SendUserMessage: &agentv1.SendUserMessageRequest{SessionId: "other", Uuid: "u1"}}}
	r.send(msg)
	r.fake.waitFor(t, "ack 1", func() bool { return r.fake.ackFor("cmd_1") != nil })
	if a := r.fake.ackFor("cmd_1"); a.GetOutcome() != relayv1.CommandAck_OUTCOME_OK {
		t.Fatalf("ack = %v", a)
	}
	// redelivery: acked again, not executed again
	r.fake.mu.Lock()
	r.fake.acks = nil
	r.fake.mu.Unlock()
	r.send(msg)
	r.fake.waitFor(t, "re-ack", func() bool { return r.fake.ackFor("cmd_1") != nil })
	if n := r.backend.messageCount(); n != 1 {
		t.Fatalf("SendUserMessage ran %d times", n)
	}
	if got := r.backend.messages[0].GetSessionId(); got != "s1" {
		t.Fatalf("command ran on session %q, want s1", got)
	}

	cases := []struct {
		cmd  *relayv1.Command
		want relayv1.CommandAck_Outcome
	}{
		{&relayv1.Command{CommandId: "cmd_exp", SessionId: "s1", Epoch: epoch, ExpiresAt: time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano),
			Payload: &relayv1.Command_SendUserMessage{SendUserMessage: &agentv1.SendUserMessageRequest{}}}, relayv1.CommandAck_OUTCOME_REJECTED},
		{&relayv1.Command{CommandId: "cmd_bypass", SessionId: "s1", Epoch: epoch, ExpiresAt: future(),
			Payload: &relayv1.Command_SetPermissionMode{SetPermissionMode: &agentv1.SetPermissionModeRequest{Mode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS}}},
			relayv1.CommandAck_OUTCOME_REJECTED},
		{&relayv1.Command{CommandId: "cmd_mode", SessionId: "s1", Epoch: epoch, ExpiresAt: future(),
			Payload: &relayv1.Command_SetPermissionMode{SetPermissionMode: &agentv1.SetPermissionModeRequest{Mode: agentv1.PermissionMode_PERMISSION_MODE_PLAN}}},
			relayv1.CommandAck_OUTCOME_OK},
		{&relayv1.Command{CommandId: "cmd_awake", SessionId: "s1", Epoch: epoch, ExpiresAt: future(),
			Payload: &relayv1.Command_SetKeepAwake{SetKeepAwake: &agentv1.SetKeepAwakeRequest{SessionId: "other", Enabled: true}}},
			relayv1.CommandAck_OUTCOME_OK},
		{&relayv1.Command{CommandId: "cmd_perm", SessionId: "s1", Epoch: epoch, ExpiresAt: future(),
			Payload: &relayv1.Command_RespondToPermission{RespondToPermission: &agentv1.RespondToPermissionRequest{RequestId: "perm_x"}}},
			relayv1.CommandAck_OUTCOME_ALREADY_RESOLVED},
		{&relayv1.Command{CommandId: "cmd_stop_agent", SessionId: "s1", Epoch: epoch, ExpiresAt: future(),
			Payload: &relayv1.Command_StopSubagent{StopSubagent: &agentv1.StopSubagentRequest{AgentId: "nope"}}}, relayv1.CommandAck_OUTCOME_FAILED},
		{&relayv1.Command{CommandId: "cmd_agent_transcript", SessionId: "s1", Epoch: epoch, ExpiresAt: future(),
			Payload: &relayv1.Command_GetSubagentTranscript{GetSubagentTranscript: &agentv1.GetSubagentTranscriptRequest{AgentId: "nope"}}}, relayv1.CommandAck_OUTCOME_FAILED},
		{&relayv1.Command{CommandId: "cmd_stop", SessionId: "s1", Epoch: epoch, ExpiresAt: future(),
			Payload: &relayv1.Command_StopTask{StopTask: &agentv1.StopTaskRequest{TaskId: "t"}}}, relayv1.CommandAck_OUTCOME_FAILED},
	}
	for _, c := range cases {
		r.send(c.cmd)
	}
	for _, c := range cases {
		r.fake.waitFor(t, c.cmd.GetCommandId(), func() bool { return r.fake.ackFor(c.cmd.GetCommandId()) != nil })
		if got := r.fake.ackFor(c.cmd.GetCommandId()).GetOutcome(); got != c.want {
			t.Errorf("%s: outcome %v, want %v", c.cmd.GetCommandId(), got, c.want)
		}
	}
	r.backend.mu.Lock()
	modes, awake := r.backend.modes, r.backend.keepAwake
	r.backend.mu.Unlock()
	if len(awake) != 1 || awake[0].GetSessionId() != "s1" || !awake[0].GetEnabled() {
		t.Errorf("SetKeepAwake reached the backend as %v", awake)
	}
	if len(modes) != 1 || modes[0] != agentv1.PermissionMode_PERMISSION_MODE_PLAN {
		t.Fatalf("modes applied: %v (bypass must never reach the backend)", modes)
	}

	// queries return their result in the ack
	r.send(&relayv1.Command{CommandId: "cmd_q", SessionId: "s1", Epoch: epoch, ExpiresAt: future(),
		Payload: &relayv1.Command_SuggestFiles{SuggestFiles: &agentv1.SuggestFilesRequest{Query: "ma"}}})
	r.fake.waitFor(t, "query ack", func() bool { return r.fake.ackFor("cmd_q") != nil })
	if p := r.fake.ackFor("cmd_q").GetSuggestFiles().GetPaths(); len(p) != 2 || p[1] != "ma" {
		t.Fatalf("query result %v", p)
	}

	// commands for a session this host does not mirror are left alone
	r.send(&relayv1.Command{CommandId: "cmd_other", SessionId: "nope", Epoch: 1, ExpiresAt: future(),
		Payload: &relayv1.Command_Interrupt{Interrupt: &agentv1.InterruptRequest{}}})
	time.Sleep(100 * time.Millisecond)
	r.fake.mu.Lock()
	defer r.fake.mu.Unlock()
	if r.fake.ackFor("cmd_other") != nil {
		t.Fatal("acked a command for a foreign session")
	}
}

func TestSupersededStopsMirroring(t *testing.T) {
	r := newRig(t, nil)
	f := r.addSession("s1")
	f.emit(true, "1")
	r.fake.waitFor(t, "seq 1", func() bool { return r.fake.acked["s1"] == 1 })
	r.fake.push <- &relayv1.OpenHostStreamResponse{Event: &relayv1.OpenHostStreamResponse_SessionSuperseded{
		SessionSuperseded: &relayv1.SessionSuperseded{SessionId: "s1", OldEpoch: 1, NewEpoch: 2, NewDeviceId: "dev_x"}}}
	deadline := time.Now().Add(2 * time.Second)
	for r.host.session("s1") != nil {
		if time.Now().After(deadline) {
			t.Fatal("session still mirrored after SessionSuperseded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.emit(true, "2")
	r.send(&relayv1.Command{CommandId: "cmd_late", SessionId: "s1", Epoch: 1, ExpiresAt: future(),
		Payload: &relayv1.Command_Interrupt{Interrupt: &agentv1.InterruptRequest{}}})
	time.Sleep(150 * time.Millisecond)
	r.fake.mu.Lock()
	defer r.fake.mu.Unlock()
	if r.fake.acked["s1"] != 1 || r.fake.ackFor("cmd_late") != nil {
		t.Fatalf("superseded host kept working: acked=%d ack=%v", r.fake.acked["s1"], r.fake.ackFor("cmd_late"))
	}
}

func TestHeartbeatsReportSessionsAndSupersede(t *testing.T) {
	r := newRig(t, nil)
	r.addSession("s1")
	r.fake.waitFor(t, "heartbeat with the session", func() bool {
		for _, hb := range r.fake.heartbeats {
			for _, s := range hb.GetSessions() {
				if s.GetSessionId() == "s1" && s.GetEpoch() == 1 && s.GetStatus() == agentv1.SessionStatus_SESSION_STATUS_IDLE {
					return true
				}
			}
		}
		return false
	})
	r.fake.mu.Lock()
	r.fake.superseded = []*relayv1.SessionSuperseded{{SessionId: "s1", OldEpoch: 1, NewEpoch: 2}}
	r.fake.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for r.host.session("s1") != nil {
		if time.Now().After(deadline) {
			t.Fatal("heartbeat supersede ignored")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSessionEndUnregisters(t *testing.T) {
	r := newRig(t, nil)
	f := r.addSession("s1")
	f.emit(true, "bye")
	f.close()
	r.fake.waitFor(t, "unregister", func() bool { return len(r.fake.unregs) == 1 && r.fake.acked["s1"] == 1 })
}

func TestRemoteControlDisabledBySettings(t *testing.T) {
	r := newRig(t, nil)
	f := newFakeFeed("s2")
	f.info.Title = "no-remote"
	r.backend.mu.Lock()
	r.backend.feeds["s2"] = f
	r.backend.mu.Unlock()
	r.host.AddSession(r.ctx, "s2")
	if r.host.session("s2") != nil {
		t.Fatal("session with remoteControl=false was mirrored")
	}
}

func TestSpawnOnlyUnderAllowedRoots(t *testing.T) {
	root := t.TempDir()
	r := newRig(t, func(o *Options) { o.Daemon = true; o.AllowedRoots = []string{root} })
	r.fake.waitFor(t, "stream", func() bool { return r.fake.streams >= 1 })
	r.send(&relayv1.Command{CommandId: "sp_bad", ExpiresAt: future(),
		Payload: &relayv1.Command_SpawnSession{SpawnSession: &relayv1.SpawnSessionCommand{Create: &agentv1.CreateSessionRequest{Cwd: "/etc"}}}})
	r.send(&relayv1.Command{CommandId: "sp_ok", ExpiresAt: future(),
		Payload: &relayv1.Command_SpawnSession{SpawnSession: &relayv1.SpawnSessionCommand{Create: &agentv1.CreateSessionRequest{Cwd: root + "/proj"},
			InitialMessage: &agentv1.SendUserMessageRequest{Uuid: "first"}}}})
	r.fake.waitFor(t, "spawn acks", func() bool { return r.fake.ackFor("sp_bad") != nil && r.fake.ackFor("sp_ok") != nil })
	if a := r.fake.ackFor("sp_bad"); a.GetOutcome() != relayv1.CommandAck_OUTCOME_REJECTED {
		t.Fatalf("spawn outside roots: %v", a)
	}
	a := r.fake.ackFor("sp_ok")
	if a.GetOutcome() != relayv1.CommandAck_OUTCOME_OK || a.GetSpawnSession().GetSession().GetId() != "spawned-1" {
		t.Fatalf("spawn ack: %v", a)
	}
	if r.backend.messageCount() != 1 || r.backend.messages[0].GetSessionId() != "spawned-1" {
		t.Fatal("initial message not sent to the spawned session")
	}
}

func TestUnderRoots(t *testing.T) {
	for _, c := range []struct {
		dir  string
		ok   bool
		root string
	}{{"/work/a", true, "/work"}, {"/work", true, "/work"}, {"/workshop", false, "/work"}, {"/work/../etc", false, "/work"}, {"rel", false, "/work"}, {"/x", false, ""}} {
		if got := UnderRoots(c.dir, []string{c.root}); got != c.ok {
			t.Errorf("UnderRoots(%q, %q) = %v", c.dir, c.root, got)
		}
	}
}
