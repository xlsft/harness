package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/relay"
	"github.com/xlsft/harness/packages/client/server"
)

// relayStub is a minimal harness.relay.v1.RelayService: it records registrations, unregistrations and
// published events per session.
type relayStub struct {
	relayv1connect.UnimplementedRelayServiceHandler
	mu     sync.Mutex
	epochs map[string]int64
	acked  map[string]int64
	unregs map[string]int
	// publishes counts PublishEvents calls per session.
	publishes map[string]int
	infos     map[string]*agentv1.SessionInfo
}

func newRelayStub(t *testing.T) (*relayStub, string) {
	s := &relayStub{epochs: map[string]int64{}, acked: map[string]int64{}, unregs: map[string]int{}, publishes: map[string]int{}, infos: map[string]*agentv1.SessionInfo{}}
	path, h := relayv1connect.NewRelayServiceHandler(s)
	mux := http.NewServeMux()
	mux.Handle(path, h)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, ts.URL
}

func (s *relayStub) RegisterHost(context.Context, *connect.Request[relayv1.RegisterHostRequest]) (*connect.Response[relayv1.RegisterHostResponse], error) {
	return connect.NewResponse(&relayv1.RegisterHostResponse{HostId: "hst_1", HeartbeatIntervalSeconds: 20}), nil
}

func (s *relayStub) OpenHostStream(ctx context.Context, _ *connect.Request[relayv1.OpenHostStreamRequest], st *connect.ServerStream[relayv1.OpenHostStreamResponse]) error {
	if err := st.Send(&relayv1.OpenHostStreamResponse{Event: &relayv1.OpenHostStreamResponse_Heartbeat{Heartbeat: &relayv1.Heartbeat{}}}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func (s *relayStub) Heartbeat(context.Context, *connect.Request[relayv1.HeartbeatRequest]) (*connect.Response[relayv1.HeartbeatResponse], error) {
	return connect.NewResponse(&relayv1.HeartbeatResponse{}), nil
}

func (s *relayStub) AckCommands(context.Context, *connect.Request[relayv1.AckCommandsRequest]) (*connect.Response[relayv1.AckCommandsResponse], error) {
	return connect.NewResponse(&relayv1.AckCommandsResponse{}), nil
}

func (s *relayStub) RegisterSession(_ context.Context, req *connect.Request[relayv1.RegisterSessionRequest]) (*connect.Response[relayv1.RegisterSessionResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := req.Msg.GetSession().GetId()
	s.epochs[id]++
	s.infos[id] = req.Msg.GetSession()
	return connect.NewResponse(&relayv1.RegisterSessionResponse{Epoch: s.epochs[id], AckedSeq: s.acked[id]}), nil
}

func (s *relayStub) UnregisterSession(_ context.Context, req *connect.Request[relayv1.UnregisterSessionRequest]) (*connect.Response[relayv1.UnregisterSessionResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unregs[req.Msg.GetSessionId()]++
	return connect.NewResponse(&relayv1.UnregisterSessionResponse{}), nil
}

func (s *relayStub) PublishEvents(_ context.Context, req *connect.Request[relayv1.PublishEventsRequest]) (*connect.Response[relayv1.PublishEventsResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := req.Msg.GetSessionId()
	if req.Msg.GetEpoch() != s.epochs[id] {
		return nil, connect.NewError(connect.CodeFailedPrecondition, nil)
	}
	s.publishes[id]++
	for _, e := range req.Msg.GetEvents() {
		if e.GetEvent().GetAssistantDelta() != nil {
			continue
		}
		if seq := e.GetEvent().GetSeq(); seq == s.acked[id]+1 {
			s.acked[id] = seq
		}
	}
	return connect.NewResponse(&relayv1.PublishEventsResponse{AckedSeq: s.acked[id]}), nil
}

func (s *relayStub) state(id string) (epoch, acked int64, unregs, publishes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.epochs[id], s.acked[id], s.unregs[id], s.publishes[id]
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func okReply(int, *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("ok"))} }

func (h *harness) setRelay(sid string, on bool) (*agentv1.SessionInfo, error) {
	res, err := h.client.SetSessionRelay(h.ctx, connect.NewRequest(&agentv1.SetSessionRelayRequest{SessionId: sid, Enabled: on}))
	if err != nil {
		return nil, err
	}
	return res.Msg.Session, nil
}

func (h *harness) info(sid string) *agentv1.SessionInfo {
	h.t.Helper()
	return h.listed("", sid)
}

// startRelayHarness serves a harness whose sessions are mirrored through a relay host to stub.
func startRelayHarness(t *testing.T) (*harness, *relayStub) {
	t.Helper()
	stub, url := newRelayStub(t)
	h := startHarnessSrv(t, okReply, nil, nil, func(srv *server.Server) {
		srv.NoAITitles = true
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		relay.Attach(ctx, srv, relay.Options{ServerURL: url, InstanceID: "inst", FlushInterval: 10 * time.Millisecond,
			MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond, HeartbeatInterval: time.Second})
	})
	return h, stub
}

// The session relay switch end to end: SetSessionRelay over AgentService drives the relay host against a
// RelayService, the choice is published (SessionUpdated) and survives a resume.
func TestSessionRelaySwitch(t *testing.T) {
	h, stub := startRelayHarness(t)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	if i := h.info(sid); !i.RelayAvailable || !i.RelayEnabled || i.RelayUnavailableReason != "" {
		t.Fatalf("new session: %+v", i)
	}
	eventually(t, "registration", func() bool { e, a, _, _ := stub.state(sid); return e == 1 && a > 0 })

	// Off: unregistered upstream, published to frontends, nothing more is mirrored.
	info, err := h.setRelay(sid, false)
	if err != nil || info.RelayEnabled || !info.RelayAvailable {
		t.Fatalf("off: %v %+v", err, info)
	}
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		u := ev.GetSessionUpdated()
		return u != nil && !u.RelayEnabled && u.RelayAvailable
	})
	eventually(t, "unregistration", func() bool { _, _, u, _ := stub.state(sid); return u == 1 })
	_, acked, _, pubs := stub.state(sid)
	h.send(sid, "hello")
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetTurnFinished() != nil })
	time.Sleep(100 * time.Millisecond)
	if e, a, _, p := stub.state(sid); e != 1 || a != acked || p != pubs {
		t.Fatalf("mirrored while off: epoch %d acked %d->%d publishes %d->%d", e, acked, a, pubs, p)
	}

	// On: registered again and caught up with the turn that ran while off.
	if info, err := h.setRelay(sid, true); err != nil || !info.RelayEnabled {
		t.Fatalf("on: %v %+v", err, info)
	}
	eventually(t, "catch up", func() bool { e, a, _, _ := stub.state(sid); return e == 2 && a > acked+3 })
	stub.mu.Lock()
	registered := stub.infos[sid]
	stub.mu.Unlock()
	if !registered.GetRelayEnabled() {
		t.Fatalf("registered info %+v", registered)
	}

	// Off again, then close and resume: the choice is read back from the transcript.
	if _, err := h.setRelay(sid, false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "second unregistration", func() bool { _, _, u, _ := stub.state(sid); return u == 2 })
	h.closeSession(sid)
	rs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if i := rs.Msg.Session; i.RelayEnabled || !i.RelayAvailable {
		t.Fatalf("resumed: %+v", i)
	}
	time.Sleep(100 * time.Millisecond)
	if e, _, _, _ := stub.state(sid); e != 2 {
		t.Fatalf("resumed session switched off was registered (epoch %d)", e)
	}
	if info, err := h.setRelay(sid, true); err != nil || !info.RelayEnabled {
		t.Fatalf("on after resume: %v %+v", err, info)
	}
	eventually(t, "registration after resume", func() bool { e, _, _, _ := stub.state(sid); return e == 3 })
}

// "remoteControl": false in the settings is the default of sessions without a choice; the switch overrides it.
func TestSessionRelaySettingsDefault(t *testing.T) {
	h, stub := startRelayHarness(t)
	if err := os.MkdirAll(filepath.Join(h.proj, ".harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.proj, ".harness", "settings.json"), []byte(`{"remoteControl": false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sid, _ := h.create(&agentv1.CreateSessionRequest{})
	if i := h.info(sid); !i.RelayAvailable || i.RelayEnabled {
		t.Fatalf("settings off: %+v", i)
	}
	time.Sleep(100 * time.Millisecond)
	if e, _, _, _ := stub.state(sid); e != 0 {
		t.Fatal("session with remoteControl=false was registered")
	}
	if info, err := h.setRelay(sid, true); err != nil || !info.RelayEnabled {
		t.Fatalf("on: %v %+v", err, info)
	}
	eventually(t, "registration", func() bool { e, _, _, _ := stub.state(sid); return e == 1 })
}

// Without a relay host the switch is unavailable with a reason; turning it on is refused, off is recorded.
func TestSessionRelayUnavailable(t *testing.T) {
	const reason = "Remote control is turned off on this machine (HARNESS_REMOTE_CONTROL=0)"
	h := startHarnessSrv(t, okReply, nil, nil, func(srv *server.Server) { srv.NoAITitles = true; srv.RelayOffReason = reason })
	sid, _ := h.create(&agentv1.CreateSessionRequest{})
	if i := h.info(sid); i.RelayAvailable || i.RelayEnabled || i.RelayUnavailableReason != reason {
		t.Fatalf("unavailable: %+v", i)
	}
	if _, err := h.setRelay(sid, true); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("on without a relay host: %v", err)
	}
	if i, err := h.setRelay(sid, false); err != nil || i.RelayEnabled {
		t.Fatalf("off without a relay host: %v %+v", err, i)
	}
	if _, err := h.setRelay("nope", false); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}

	// No reason given: not signed in.
	h2 := startHarnessSrv(t, okReply, nil, nil, func(srv *server.Server) { srv.NoAITitles = true })
	sid2, _ := h2.create(&agentv1.CreateSessionRequest{})
	if i := h2.info(sid2); i.RelayAvailable || i.RelayUnavailableReason == "" {
		t.Fatalf("no relay host: %+v", i)
	}
}
