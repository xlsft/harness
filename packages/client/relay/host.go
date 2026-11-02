// Package relay is the host side of harness.relay.v1.RelayService (remote
// control, docs/PLAN.md §1.6): it registers this client process with the
// harness server, mirrors the live event log of every open session upstream
// and executes commands sent by viewers (web, mobile, another desktop)
// through the same AgentService code paths local frontends use.
//
// Connections are outbound only: one server stream (OpenHostStream) carries
// commands down, unary calls carry events, acks and heartbeats up.
package relay

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
)

// Feed is read access to one live session's event log (server.Feed).
type Feed interface {
	// Subscribe returns stored events after local seq `after` and a channel
	// of live ones; the channel closes when the session ends (Closed) or the
	// subscriber fell behind (resubscribe).
	Subscribe(after int64) ([]*agentv1.SessionEvent, <-chan *agentv1.SessionEvent, func())
	Closed() bool
	Info() *agentv1.SessionInfo
	// RemoteControl is false when settings disable remote control for the session.
	RemoteControl() bool
}

// historyFeed is implemented by feeds whose log starts with replayed
// history (server.Feed of a resumed session).
type historyFeed interface {
	// HistoryLen is the number of leading events replayed from the transcript.
	HistoryLen() int64
}

// Backend is the local agent: AgentService commands plus session feeds.
// server.Server implements it through Attach.
type Backend interface {
	agentv1connect.AgentServiceHandler
	SessionFeed(sessionID string) (Feed, bool)
}

// Options configure a Host.
type Options struct {
	ServerURL string
	HTTP      *http.Client
	Tokens    rpc.Tokens
	Backend   Backend
	Logger    *slog.Logger

	// InstanceID identifies this process across reconnects (random by default).
	InstanceID string
	// Daemon hosts accept SpawnSession under AllowedRoots.
	Daemon       bool
	AllowedRoots []string

	// FlushInterval and MaxBatch bound event batching (default 100 ms / 64 events).
	FlushInterval time.Duration
	MaxBatch      int
	// HeartbeatInterval overrides the server-recommended interval.
	HeartbeatInterval time.Duration
	// MinBackoff / MaxBackoff bound reconnect delays (default 500 ms / 30 s).
	MinBackoff, MaxBackoff time.Duration
}

// Host is one relay host (one per client process).
type Host struct {
	o   Options
	rpc relayv1connect.RelayServiceClient
	log *slog.Logger

	mu       sync.Mutex
	hostID   string
	interval time.Duration
	// conn is closed and replaced whenever the host (re)connects.
	conn     chan struct{}
	sessions map[string]*hostSession
	// done holds acks of finished commands (re-sent on redelivery); running
	// holds commands being executed.
	done    map[string]*relayv1.CommandAck
	doneLRU []string
	running map[string]bool
	acks    []*relayv1.CommandAck
	ackKick chan struct{}
}

const doneCap = 4096

// New creates a host; call Run to connect and AddSession for each session.
func New(o Options) *Host {
	if o.InstanceID == "" {
		o.InstanceID = uuid.NewString()
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = 100 * time.Millisecond
	}
	if o.MaxBatch <= 0 {
		o.MaxBatch = 64
	}
	if o.MinBackoff <= 0 {
		o.MinBackoff = 500 * time.Millisecond
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 30 * time.Second
	}
	if o.HTTP == nil {
		o.HTTP = rpc.HTTPClient(o.ServerURL)
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Host{
		o:   o,
		rpc: relayv1connect.NewRelayServiceClient(o.HTTP, strings.TrimRight(o.ServerURL, "/"), rpc.ClientOptions()...),
		log: log.With("component", "relay"), conn: make(chan struct{}),
		sessions: map[string]*hostSession{}, done: map[string]*relayv1.CommandAck{}, running: map[string]bool{},
		ackKick: make(chan struct{}, 1),
	}
}

// call runs a unary RPC with the bearer token, refreshing it once on
// UNAUTHENTICATED. Errors keep their Connect code (codeOf).
func call[Req, Res any](ctx context.Context, h *Host, fn func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error), msg *Req) (*Res, error) {
	do := func(token string) (*Res, error) {
		req := connect.NewRequest(msg)
		rpc.SetBearer(req.Header(), token)
		res, err := fn(ctx, req)
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	if h.o.Tokens == nil {
		return do("")
	}
	token, err := h.o.Tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	out, err := do(token)
	if connect.CodeOf(err) == connect.CodeUnauthenticated {
		if token, rerr := h.o.Tokens.Refresh(ctx); rerr == nil {
			out, err = do(token)
		}
	}
	return out, err
}

func codeOf(err error) connect.Code { return connect.CodeOf(err) }

// backoff returns the delay before attempt n (0-based): exponential with jitter.
func (h *Host) backoff(n int) time.Duration {
	d := h.o.MinBackoff << min(n, 16)
	if d <= 0 || d > h.o.MaxBackoff {
		d = h.o.MaxBackoff
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// connected returns the current host id (empty while disconnected) and a
// channel closed at the next (re)connect.
func (h *Host) connected() (string, chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hostID, h.conn
}

func (h *Host) setConnected(id string, interval time.Duration) {
	h.mu.Lock()
	h.hostID = id
	h.interval = interval
	close(h.conn)
	h.conn = make(chan struct{})
	h.mu.Unlock()
}

// waitHost blocks until the host is connected and returns its id.
func (h *Host) waitHost(ctx context.Context) (string, error) {
	for {
		id, ch := h.connected()
		if id != "" {
			return id, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ch:
		}
	}
}

// Run connects and keeps the host connected until ctx ends: RegisterHost,
// OpenHostStream (commands), heartbeats and ack delivery, reconnecting with
// exponential backoff and jitter.
func (h *Host) Run(ctx context.Context) error {
	go h.ackLoop(ctx)
	for attempt := 0; ctx.Err() == nil; attempt++ {
		if attempt > 0 && !sleep(ctx, h.backoff(attempt-1)) {
			break
		}
		reg, err := call(ctx, h, h.rpc.RegisterHost, &relayv1.RegisterHostRequest{
			InstanceId: h.o.InstanceID, ClientVersion: config.Version, Daemon: h.o.Daemon, AllowedRoots: h.o.AllowedRoots,
		})
		if err != nil {
			h.log.Debug("register host failed", "err", err)
			continue
		}
		interval := time.Duration(reg.GetHeartbeatIntervalSeconds()) * time.Second
		if h.o.HeartbeatInterval > 0 {
			interval = h.o.HeartbeatInterval
		}
		if interval <= 0 {
			interval = 20 * time.Second
		}
		if h.serve(ctx, reg.GetHostId(), interval) {
			attempt = 0
		}
	}
	h.mu.Lock()
	h.hostID = ""
	h.mu.Unlock()
	return ctx.Err()
}

// serve runs one host stream until it fails; it reports whether the stream
// delivered anything (a healthy connection resets the backoff).
func (h *Host) serve(ctx context.Context, hostID string, interval time.Duration) (healthy bool) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	token := ""
	if h.o.Tokens != nil {
		t, err := h.o.Tokens.Token(cctx)
		if err != nil {
			return false
		}
		token = t
	}
	req := connect.NewRequest(&relayv1.OpenHostStreamRequest{HostId: hostID})
	rpc.SetBearer(req.Header(), token)
	stream, err := h.rpc.OpenHostStream(cctx, req)
	if err != nil {
		h.log.Debug("open host stream failed", "err", err)
		return false
	}
	defer stream.Close()
	h.setConnected(hostID, interval)
	defer func() {
		h.mu.Lock()
		if h.hostID == hostID {
			h.hostID = ""
		}
		h.mu.Unlock()
	}()
	h.kickAcks()
	go h.heartbeatLoop(cctx, hostID, interval)
	for stream.Receive() {
		healthy = true
		switch ev := stream.Msg().GetEvent().(type) {
		case *relayv1.OpenHostStreamResponse_Command:
			h.dispatch(ctx, ev.Command)
		case *relayv1.OpenHostStreamResponse_SessionSuperseded:
			h.superseded(ev.SessionSuperseded)
		case *relayv1.OpenHostStreamResponse_Heartbeat:
		}
	}
	err = stream.Err()
	if codeOf(err) == connect.CodeUnauthenticated && h.o.Tokens != nil {
		_, _ = h.o.Tokens.Refresh(ctx)
	}
	if ctx.Err() == nil {
		h.log.Debug("host stream ended", "err", err)
	}
	return healthy
}

func (h *Host) heartbeatLoop(ctx context.Context, hostID string, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		req := &relayv1.HeartbeatRequest{HostId: hostID}
		h.mu.Lock()
		for id, hs := range h.sessions {
			if epoch := hs.currentEpoch(); epoch > 0 {
				req.Sessions = append(req.Sessions, &relayv1.HostSessionState{SessionId: id, Epoch: epoch, Status: hs.feed.Info().GetStatus()})
			}
		}
		h.mu.Unlock()
		res, err := call(ctx, h, h.rpc.Heartbeat, req)
		if err != nil {
			h.log.Debug("heartbeat failed", "err", err)
			continue
		}
		for _, s := range res.GetSuperseded() {
			h.superseded(s)
		}
	}
}

// superseded stops mirroring a session another host took over.
func (h *Host) superseded(s *relayv1.SessionSuperseded) {
	h.mu.Lock()
	hs := h.sessions[s.GetSessionId()]
	h.mu.Unlock()
	// Ignore stale notices: this host re-registered after the takeover.
	if hs == nil || hs.currentEpoch() > s.GetOldEpoch() {
		return
	}
	h.log.Info("session taken over by another device", "session", s.GetSessionId(), "device", s.GetNewDeviceId())
	hs.supersede()
}

// AddSession starts mirroring a live session (no-op when it is already
// mirrored or settings disable remote control for it).
func (h *Host) AddSession(ctx context.Context, sessionID string) {
	feed, ok := h.o.Backend.SessionFeed(sessionID)
	if !ok || !feed.RemoteControl() {
		return
	}
	h.mu.Lock()
	if _, dup := h.sessions[sessionID]; dup {
		h.mu.Unlock()
		return
	}
	hs := newHostSession(h, sessionID, feed)
	h.sessions[sessionID] = hs
	h.mu.Unlock()
	go func() {
		hs.run(ctx)
		h.mu.Lock()
		if h.sessions[sessionID] == hs {
			delete(h.sessions, sessionID)
		}
		h.mu.Unlock()
	}()
}

func (h *Host) session(id string) *hostSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[id]
}

// ---- acks ----

// ack queues an ack for delivery (retried until the server accepts it).
func (h *Host) ack(a *relayv1.CommandAck) {
	h.mu.Lock()
	delete(h.running, a.GetCommandId())
	if _, ok := h.done[a.GetCommandId()]; !ok {
		h.doneLRU = append(h.doneLRU, a.GetCommandId())
		if len(h.doneLRU) > doneCap {
			delete(h.done, h.doneLRU[0])
			h.doneLRU = h.doneLRU[1:]
		}
	}
	h.done[a.GetCommandId()] = a
	h.acks = append(h.acks, a)
	h.mu.Unlock()
	h.kickAcks()
}

func (h *Host) kickAcks() {
	select {
	case h.ackKick <- struct{}{}:
	default:
	}
}

func (h *Host) ackLoop(ctx context.Context) {
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.ackKick:
		}
		for {
			h.mu.Lock()
			batch := h.acks
			h.acks = nil
			hostID := h.hostID
			h.mu.Unlock()
			if len(batch) == 0 {
				break
			}
			var err error
			if hostID == "" {
				err = errors.New("not connected")
			} else {
				_, err = call(ctx, h, h.rpc.AckCommands, &relayv1.AckCommandsRequest{HostId: hostID, Acks: batch})
			}
			if err == nil {
				failures = 0
				continue
			}
			h.mu.Lock()
			h.acks = append(batch, h.acks...)
			h.mu.Unlock()
			if hostID == "" {
				break // resent by kickAcks after the next connect
			}
			failures++
			if !sleep(ctx, h.backoff(failures-1)) {
				return
			}
		}
	}
}
