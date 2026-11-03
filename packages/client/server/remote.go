package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/session"
)

// Sessions of the user's other machines ("remote sessions").
//
// The harness server keeps a copy of every session of the user (SyncService: metadata and the durable transcript,
// uploaded by the machine that runs it). ListSessions reads that listing anyway for the per-user state
// (sessionstate.go); every listed session that is not on this machine is merged into the result as remote:
// SessionInfo.remote, with device_id / device_name of its machine. A session is on this machine when it is live
// here, a local transcript of it is listed, or the server lists it with this machine's device id (an older
// transcript beyond the list cap); so a session never appears twice. The last listing is kept, so a server that
// does not answer leaves the remote sessions as they were.
//
// When the client has a relay viewer (RelayService), ListHosts tells which machines are online and which of their
// sessions are open there (device_online, remote_controllable); without it, or when the server does not answer for
// a while, presence is unknown and device_online stays unset.
//
// Opening a remote session (Subscribe) follows it live through the relay when it is remote-controllable: the
// relay replays the events its host published, then streams live ones, so the frontend renders it exactly like a
// local session. Otherwise the durable transcript is read from the server and replayed into the events a resumed
// session would show (replayHistory), followed by a SessionUpdated with the remote info, and the stream ends:
// a read-only snapshot. Commands for a remote-controllable session go to the relay (routed); for any other remote
// session they fail with FAILED_PRECONDITION. Pin, unread, archive and rename are written to the server copy;
// delete removes the server copy. CreateSession can fork a remote session: its transcript is downloaded into a
// new local session in the chosen folder ("Continue here").

const (
	// presenceTTL is how long relay presence is trusted when the server stops answering ListHosts.
	presenceTTL = 2 * time.Minute
	// remoteTranscriptPage / remoteTranscriptPages bound one read of a remote transcript (main thread only;
	// subagent transcripts are not shown or copied).
	remoteTranscriptPage    = 2000
	remoteTranscriptPages   = 100
	remoteTranscriptTimeout = 30 * time.Second
	// relayCommandTimeout bounds a command forwarded to the relay (acked commands wait up to ~10 s there).
	relayCommandTimeout = 20 * time.Second
	// interruptedToolUsePrefix is the documented start of the interrupted_tool_use reminder; a remote transcript
	// is replayed without the session's prompt bundle, so the full text is not known here.
	interruptedToolUsePrefix = "[Request interrupted by user for tool use]"
	// relayFrontend labels commands this client forwards (CommandOptions.frontend).
	relayFrontend = "harness-client"
)

// relayPresence is what RelayService.ListHosts said last.
type relayPresence struct {
	at time.Time
	// online and names are by device id; a device is online when any of its hosts is.
	online map[string]bool
	names  map[string]string
	// live are the sessions open on an online host, by session id (the host's view of the session).
	live map[string]*agentv1.SessionInfo
}

// RelayViewer is the viewer side of harness.relay.v1.RelayService: presence of the user's machines and remote
// control of the sessions open on them.
type RelayViewer struct {
	rpc    relayv1connect.RelayServiceClient
	tokens rpc.Tokens
}

// NewRelayViewer creates a viewer for the harness server at baseURL. hc may be nil; tokens may be nil for
// unauthenticated test servers.
func NewRelayViewer(baseURL string, hc *http.Client, tokens rpc.Tokens) *RelayViewer {
	if hc == nil {
		hc = rpc.HTTPClient(baseURL)
	}
	return &RelayViewer{rpc: relayv1connect.NewRelayServiceClient(hc, strings.TrimRight(baseURL, "/"), rpc.ClientOptions()...), tokens: tokens}
}

// relayCall runs a unary RelayService call with the bearer token, refreshing it once on UNAUTHENTICATED. Errors
// keep their Connect code.
func relayCall[Req, Res any](ctx context.Context, v *RelayViewer, fn func(relayv1connect.RelayServiceClient, context.Context, *connect.Request[Req]) (*connect.Response[Res], error), msg *Req) (*Res, error) {
	do := func(token string) (*Res, error) {
		req := connect.NewRequest(msg)
		rpc.SetBearer(req.Header(), token)
		res, err := fn(v.rpc, ctx, req)
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	if v.tokens == nil {
		return do("")
	}
	token, err := v.tokens.Token(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	out, err := do(token)
	if connect.CodeOf(err) == connect.CodeUnauthenticated {
		if token, rerr := v.tokens.Refresh(ctx); rerr == nil {
			out, err = do(token)
		}
	}
	return out, err
}

func (v *RelayViewer) subscribe(ctx context.Context, msg *relayv1.SubscribeSessionRequest) (*connect.ServerStreamForClient[relayv1.SubscribeSessionResponse], error) {
	req := connect.NewRequest(msg)
	if v.tokens != nil {
		token, err := v.tokens.Token(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		}
		rpc.SetBearer(req.Header(), token)
	}
	return v.rpc.SubscribeSession(ctx, req)
}

// relayError maps a RelayService failure for frontends.
func relayError(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeUnknown, connect.CodeDeadlineExceeded:
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("harness server relay unreachable: %w", err))
	case connect.CodeNotFound:
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("the session is not open on its machine: %w", err))
	}
	return connect.NewError(connect.CodeOf(err), err)
}

// refreshListing reads the server's session listing (refreshStates) and, in parallel, relay presence.
func (s *Server) refreshListing(ctx context.Context, cwd string) {
	var wg sync.WaitGroup
	if s.Relay != nil && s.Sync != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.refreshPresence(ctx)
		}()
	}
	s.refreshStates(ctx, cwd)
	wg.Wait()
}

// refreshPresence reads relay presence; on failure the last known presence stays (until presenceTTL).
func (s *Server) refreshPresence(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
	defer cancel()
	res, err := relayCall(ctx, s.Relay, relayv1connect.RelayServiceClient.ListHosts, &relayv1.ListHostsRequest{})
	if err != nil {
		s.log().Debug("remote sessions: relay presence unavailable", "err", err)
		return
	}
	p := relayPresence{at: time.Now(), online: map[string]bool{}, names: map[string]string{}, live: map[string]*agentv1.SessionInfo{}}
	for _, h := range res.GetHosts() {
		dev := h.GetDeviceId()
		p.online[dev] = p.online[dev] || h.GetOnline()
		if h.GetDeviceName() != "" {
			p.names[dev] = h.GetDeviceName()
		}
		if !h.GetOnline() {
			continue
		}
		for _, si := range h.GetSessions() {
			p.live[si.GetId()] = si
		}
	}
	s.stateMu.Lock()
	s.presence = p
	s.stateMu.Unlock()
}

// isRemote reports whether a session the server lists belongs to another machine. checkFile also looks for a
// local transcript (the listing has one when it was built from them).
func (s *Server) isRemote(info *agentv1.SessionInfo, checkFile bool) bool {
	id := info.GetId()
	if !validSessionID(id) {
		return false
	}
	s.mu.Lock()
	live := s.live[id] != nil
	s.mu.Unlock()
	if live || (s.DeviceID != "" && info.GetDeviceId() == s.DeviceID) {
		return false
	}
	if checkFile || s.DeviceID == "" || info.GetDeviceId() == "" {
		if _, err := (session.Store{Root: config.ProjectsDir()}).Find("", id); err == nil {
			return false
		}
	}
	return true
}

// remoteSessions are the listed server sessions (of cwd, or all) that are neither in local nor on this machine,
// with their origin.
func (s *Server) remoteSessions(cwd string, local map[string]*agentv1.SessionInfo) []*agentv1.SessionInfo {
	s.stateMu.Lock()
	var cands []*agentv1.SessionInfo
	for id, info := range s.listed {
		if _, ok := local[id]; ok || (cwd != "" && info.GetCwd() != cwd) {
			continue
		}
		cands = append(cands, info)
	}
	s.stateMu.Unlock()
	var out []*agentv1.SessionInfo
	for _, info := range cands {
		if s.isRemote(info, false) {
			out = append(out, s.withOrigin(info))
		}
	}
	return out
}

// remoteInfo is the remote session id with its origin, or nil when id is not a known session of another machine.
func (s *Server) remoteInfo(id string) *agentv1.SessionInfo {
	if !validSessionID(id) {
		return nil
	}
	s.stateMu.Lock()
	info := s.listed[id]
	s.stateMu.Unlock()
	if info == nil || !s.isRemote(info, true) {
		return nil
	}
	return s.withOrigin(info)
}

// withOrigin returns a copy of a listed session marked remote, with what relay presence knows about it.
func (s *Server) withOrigin(listed *agentv1.SessionInfo) *agentv1.SessionInfo {
	info := proto.Clone(listed).(*agentv1.SessionInfo)
	info.Remote, info.DeviceOnline, info.RemoteControllable = true, nil, false
	// Another profile on this machine (a different HARNESS_HOME signs in as another device) reads as this
	// computer, not as its host name.
	if host, err := os.Hostname(); err == nil && info.GetDeviceName() == host {
		info.DeviceName = "this computer"
	}
	s.stateMu.Lock()
	p := s.presence
	s.stateMu.Unlock()
	if s.Relay == nil || p.at.IsZero() || time.Since(p.at) > presenceTTL {
		return info
	}
	online := p.online[info.GetDeviceId()]
	if info.DeviceName == "" {
		info.DeviceName = p.names[info.GetDeviceId()]
	}
	if live := p.live[info.GetId()]; live != nil {
		// Open on an online host (possibly another machine of the user that took it over).
		online, info.RemoteControllable = true, true
		if live.GetStatus() != agentv1.SessionStatus_SESSION_STATUS_UNSPECIFIED {
			info.Status = live.GetStatus()
		}
	} else if !online && (info.Status == agentv1.SessionStatus_SESSION_STATUS_WORKING || info.Status == agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT) {
		info.Status = agentv1.SessionStatus_SESSION_STATUS_WAITING_FOR_CONNECTION
	}
	info.DeviceOnline = &online
	return info
}

// forgetListed drops a deleted session from the last listing.
func (s *Server) forgetListed(id string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	delete(s.listed, id)
}

// deviceLabel names the machine of a remote session in messages.
func deviceLabel(info *agentv1.SessionInfo) string {
	if n := info.GetDeviceName(); n != "" {
		return n
	}
	return "another machine"
}

// updateRemote writes per-user state (pin, unread, archive, rename) of a remote session to its server copy.
func (s *Server) updateRemote(ctx context.Context, info *agentv1.SessionInfo, m *agentv1.UpdateSessionRequest, title string) (*connect.Response[agentv1.UpdateSessionResponse], error) {
	if s.Sync == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this session is stored on the harness server, and this client does not sync with it"))
	}
	st := s.stateOf(info.Id)
	patch := &agentv1.SessionInfo{Id: info.Id}
	var paths []string
	if m.Pinned != nil {
		st.pinned, patch.Pinned = m.GetPinned(), m.GetPinned()
		paths = append(paths, "info.pinned")
	}
	if m.Unread != nil {
		st.unread, patch.Unread = m.GetUnread(), m.GetUnread()
		paths = append(paths, "info.unread")
	}
	if m.Archived != nil {
		st.archived, patch.Archived = m.GetArchived(), m.GetArchived()
		paths = append(paths, "info.archived")
	}
	if m.Title != nil {
		patch.Title = title
		paths = append(paths, "info.title")
	}
	detach := m.GetDetachFromParent()
	if detach {
		paths = append(paths, "info.parent_session_id")
	}
	if len(paths) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nothing to update"))
	}
	cctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
	_, err := s.Sync.UpsertSession(cctx, &syncv1.UpsertSessionRequest{Session: &syncv1.SessionMetadata{Info: patch}, UpdatePaths: paths})
	cancel()
	if err != nil {
		return nil, syncError(err)
	}
	s.setState(info.Id, st)
	if m.Title != nil {
		s.stateMu.Lock()
		if l := s.listed[info.Id]; l != nil {
			l = proto.Clone(l).(*agentv1.SessionInfo)
			l.Title = title
			s.listed[info.Id] = l
		}
		s.stateMu.Unlock()
		info.Title = title
	}
	if detach {
		s.stateMu.Lock()
		if l := s.listed[info.Id]; l != nil {
			l = proto.Clone(l).(*agentv1.SessionInfo)
			l.ParentSessionId = ""
			s.listed[info.Id] = l
		}
		s.stateMu.Unlock()
		info.ParentSessionId = ""
	}
	s.applyState(info)
	return connect.NewResponse(&agentv1.UpdateSessionResponse{Session: info}), nil
}

// remoteEntries reads the main-thread transcript of a session from the server, in order.
func (s *Server) remoteEntries(ctx context.Context, id string) ([]*syncv1.TranscriptEntry, error) {
	if s.Sync == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this client does not sync with the harness server"))
	}
	ctx, cancel := context.WithTimeout(ctx, remoteTranscriptTimeout)
	defer cancel()
	var out []*syncv1.TranscriptEntry
	after := int64(0)
	for range remoteTranscriptPages {
		res, err := s.Sync.GetSessionTranscript(ctx, &syncv1.GetSessionTranscriptRequest{SessionId: id, AfterSeq: after, PageSize: remoteTranscriptPage})
		if err != nil {
			return nil, syncError(err)
		}
		out = append(out, res.GetEntries()...)
		if n := len(res.GetEntries()); n > 0 {
			after = res.GetEntries()[n-1].GetSeq()
		}
		if !res.GetHasMore() || len(res.GetEntries()) == 0 {
			return out, nil
		}
	}
	return out, nil
}

// subscribeRemote serves Subscribe for a remote session: live through the relay when it can be driven, else a
// replay of its server transcript (the stream then ends).
func (s *Server) subscribeRemote(ctx context.Context, info *agentv1.SessionInfo, after int64, stream *connect.ServerStream[agentv1.SubscribeResponse]) error {
	if info.RemoteControllable && s.Relay != nil {
		return s.followRelay(ctx, info, after, stream)
	}
	return s.replayRemote(ctx, info, after, stream)
}

// remoteHistory rebuilds the events of a remote session from its server transcript, ending with a SessionUpdated
// that carries its remote info. Seq is numbered from 1.
func (s *Server) remoteHistory(ctx context.Context, info *agentv1.SessionInfo) ([]*agentv1.SessionEvent, error) {
	raw, err := s.remoteEntries(ctx, info.Id)
	if err != nil {
		return nil, err
	}
	entries := make([]session.Entry, 0, len(raw))
	for _, e := range raw {
		var se session.Entry
		if json.Unmarshal([]byte(e.GetEntryJson()), &se) == nil {
			entries = append(entries, se)
		}
	}
	evs := replayHistory(entries, replayOptions{Cwd: info.Cwd, Interrupts: []string{agent.InterruptedFallback},
		InterruptPrefixes: []string{interruptedToolUsePrefix}, MaxTurns: s.HistoryTurns, MaxEvents: s.HistoryEvents})
	s.applyState(info)
	evs = append(evs, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_SessionUpdated{SessionUpdated: info}})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i, ev := range evs {
		ev.Seq, ev.SessionId = int64(i+1), info.Id
		if ev.Timestamp == "" {
			ev.Timestamp = now
		}
	}
	return evs, nil
}

func (s *Server) replayRemote(ctx context.Context, info *agentv1.SessionInfo, after int64, stream *connect.ServerStream[agentv1.SubscribeResponse]) error {
	evs, err := s.remoteHistory(ctx, info)
	if err != nil {
		return err
	}
	for _, ev := range evs {
		if ev.Seq <= after {
			continue
		}
		if err := stream.Send(&agentv1.SubscribeResponse{Event: ev}); err != nil {
			return err
		}
	}
	return nil
}

// followRelay forwards the relay's view of a remote session. SessionUpdated events keep the remote origin, and the
// host going offline is reported as a SessionUpdated (seq 0, like a delta: never deduplicated).
func (s *Server) followRelay(ctx context.Context, info *agentv1.SessionInfo, after int64, stream *connect.ServerStream[agentv1.SubscribeResponse]) error {
	sub, err := s.Relay.subscribe(ctx, &relayv1.SubscribeSessionRequest{SessionId: info.Id, AfterSeq: after, ViewerId: newUUID("viewer_")})
	if err != nil {
		return relayError(err)
	}
	defer sub.Close()
	for sub.Receive() {
		var ev *agentv1.SessionEvent
		switch m := sub.Msg().GetEvent().(type) {
		case *relayv1.SubscribeSessionResponse_SessionEvent:
			ev = m.SessionEvent
			if u := ev.GetSessionUpdated(); u != nil {
				ev.Event = &agentv1.SessionEvent_SessionUpdated{SessionUpdated: s.liveRemoteInfo(info, u)}
			}
		case *relayv1.SubscribeSessionResponse_HostState:
			online := m.HostState.GetOnline()
			cur := proto.Clone(info).(*agentv1.SessionInfo)
			cur.DeviceOnline, cur.RemoteControllable = &online, online
			if !online {
				cur.Status = agentv1.SessionStatus_SESSION_STATUS_WAITING_FOR_CONNECTION
			}
			s.applyState(cur)
			ev = &agentv1.SessionEvent{SessionId: info.Id, Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
				Event: &agentv1.SessionEvent_SessionUpdated{SessionUpdated: cur}}
		default:
			continue
		}
		if err := stream.Send(&agentv1.SubscribeResponse{Event: ev}); err != nil {
			return err
		}
	}
	if err := sub.Err(); err != nil && ctx.Err() == nil {
		return relayError(err)
	}
	return nil
}

// liveRemoteInfo is the host's SessionInfo of a remote session with its origin and the user state kept here.
func (s *Server) liveRemoteInfo(origin, live *agentv1.SessionInfo) *agentv1.SessionInfo {
	info := proto.Clone(live).(*agentv1.SessionInfo)
	online := true
	info.Remote, info.DeviceOnline, info.RemoteControllable = true, &online, true
	if info.DeviceId == "" {
		info.DeviceId = origin.GetDeviceId()
	}
	if info.DeviceName == "" {
		info.DeviceName = origin.GetDeviceName()
	}
	if info.Title == "" {
		info.Title = origin.GetTitle()
	}
	s.applyState(info)
	return info
}

// forkRemote downloads the transcript of a remote session into a new local session in cwd and returns its id
// (CreateSession then resumes it). Entries are rewritten for this machine: their cwd becomes cwd and the origin's
// worktree record is dropped.
func (s *Server) forkRemote(ctx context.Context, info *agentv1.SessionInfo, cwd string) (string, error) {
	raw, err := s.remoteEntries(ctx, info.Id)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 {
		return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s has no transcript on the harness server yet", info.Id))
	}
	tmp, err := os.CreateTemp("", "harness-remote-*.jsonl")
	if err != nil {
		return "", connect.NewError(connect.CodeInternal, err)
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriter(tmp)
	for _, e := range raw {
		if line, ok := localizeEntry(e.GetEntryJson(), cwd); ok {
			w.WriteString(line)
			w.WriteByte('\n')
		}
	}
	if err := errors.Join(w.Flush(), tmp.Close()); err != nil {
		return "", connect.NewError(connect.CodeInternal, err)
	}
	id := session.NewID()
	if err := session.Fork(tmp.Name(), session.Store{Root: config.ProjectsDir()}.Path(cwd, id), info.Id, id, ""); err != nil {
		return "", connect.NewError(connect.CodeInternal, fmt.Errorf("copy the transcript: %w", err))
	}
	return id, nil
}

// localizeEntry rewrites one transcript line of another machine for a fork in cwd.
func localizeEntry(line, cwd string) (string, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(line), &m) != nil {
		return "", false
	}
	if _, ok := m["cwd"]; ok {
		m["cwd"], _ = json.Marshal(cwd)
	}
	var typ string
	_ = json.Unmarshal(m["type"], &typ)
	if typ == session.TypeMeta {
		delete(m, "worktree")
	}
	out, err := json.Marshal(m)
	if err != nil {
		return "", false
	}
	return string(out), true
}

// ---- Commands for remote sessions ----

// routed serves AgentService: commands for a remote session go to the relay, everything else to the Server.
type routed struct{ *Server }

// remoteTarget returns the remote session id names, nil when id is handled here (a local session or unknown), and
// an error for a remote session that cannot be driven from here.
func (s *Server) remoteTarget(id string) (*agentv1.SessionInfo, error) {
	s.mu.Lock()
	live := s.live[id] != nil
	s.mu.Unlock()
	if live {
		return nil, nil
	}
	info := s.remoteInfo(id)
	switch {
	case info == nil:
		return nil, nil
	case s.Relay == nil:
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s runs on %s, and remote control is off on this client", id, deviceLabel(info)))
	case !info.RemoteControllable && info.DeviceOnline != nil && !*info.DeviceOnline:
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s runs on %s, which is offline", id, deviceLabel(info)))
	case !info.RemoteControllable:
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s is not open on %s, so it cannot be driven from here", id, deviceLabel(info)))
	}
	return info, nil
}

func commandOptions(key string) *relayv1.CommandOptions {
	return &relayv1.CommandOptions{IdempotencyKey: key, Frontend: relayFrontend}
}

// commandOutcome maps the delivery and host ack of a forwarded command.
func commandOutcome(r *relayv1.CommandResult) error {
	if r.GetDelivery() == relayv1.DeliveryState_DELIVERY_STATE_EXPIRED {
		return connect.NewError(connect.CodeUnavailable, errors.New("the session's machine did not pick the command up in time"))
	}
	ack := r.GetAck()
	msg := ack.GetErrorMessage()
	switch ack.GetOutcome() {
	case relayv1.CommandAck_OUTCOME_ALREADY_RESOLVED:
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("already answered on another device"))
	case relayv1.CommandAck_OUTCOME_REJECTED, relayv1.CommandAck_OUTCOME_FAILED:
		if msg == "" {
			msg = "the session's machine refused the command"
		}
		return connect.NewError(connect.CodeFailedPrecondition, errors.New(msg))
	}
	return nil
}

// forward runs a command on a remote session through the relay, or local when id is handled here. out converts a
// query's result; nil for fire-and-forget commands, whose ack is checked instead.
func forward[Req, Res, RReq, RRes any](ctx context.Context, r routed, id string, req *connect.Request[Req],
	local func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error),
	call func(relayv1connect.RelayServiceClient, context.Context, *connect.Request[RReq]) (*connect.Response[RRes], error),
	wrap func(*Req) *RReq, out func(*RRes) *Res,
) (*connect.Response[Res], error) {
	info, err := r.remoteTarget(id)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return local(ctx, req)
	}
	cctx, cancel := context.WithTimeout(ctx, relayCommandTimeout)
	defer cancel()
	res, err := relayCall(cctx, r.Relay, call, wrap(req.Msg))
	if err != nil {
		return nil, relayError(err)
	}
	if out != nil {
		if v := out(res); v != nil {
			return connect.NewResponse(v), nil
		}
		return connect.NewResponse(new(Res)), nil
	}
	if c, ok := any(res).(interface{ GetResult() *relayv1.CommandResult }); ok {
		if err := commandOutcome(c.GetResult()); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(new(Res)), nil
}

func (r routed) SendUserMessage(ctx context.Context, req *connect.Request[agentv1.SendUserMessageRequest]) (*connect.Response[agentv1.SendUserMessageResponse], error) {
	if res, ok, err := r.legacyShell(ctx, req); ok {
		return res, err
	}
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.SendUserMessage, relayv1connect.RelayServiceClient.SendUserMessage,
		func(m *agentv1.SendUserMessageRequest) *relayv1.SendUserMessageRequest {
			return &relayv1.SendUserMessageRequest{Request: m, Options: commandOptions(m.GetUuid())}
		}, nil)
}

func (r routed) Interrupt(ctx context.Context, req *connect.Request[agentv1.InterruptRequest]) (*connect.Response[agentv1.InterruptResponse], error) {
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.Interrupt, relayv1connect.RelayServiceClient.Interrupt,
		func(m *agentv1.InterruptRequest) *relayv1.InterruptRequest {
			return &relayv1.InterruptRequest{Request: m, Options: commandOptions("")}
		}, nil)
}

func (r routed) RespondToPermission(ctx context.Context, req *connect.Request[agentv1.RespondToPermissionRequest]) (*connect.Response[agentv1.RespondToPermissionResponse], error) {
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.RespondToPermission, relayv1connect.RelayServiceClient.RespondToPermission,
		func(m *agentv1.RespondToPermissionRequest) *relayv1.RespondToPermissionRequest {
			return &relayv1.RespondToPermissionRequest{Request: m, Options: commandOptions(m.GetRequestId())}
		}, nil)
}

func (r routed) AnswerQuestion(ctx context.Context, req *connect.Request[agentv1.AnswerQuestionRequest]) (*connect.Response[agentv1.AnswerQuestionResponse], error) {
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.AnswerQuestion, relayv1connect.RelayServiceClient.AnswerQuestion,
		func(m *agentv1.AnswerQuestionRequest) *relayv1.AnswerQuestionRequest {
			return &relayv1.AnswerQuestionRequest{Request: m, Options: commandOptions(m.GetRequestId())}
		}, nil)
}

func (r routed) RespondToPlan(ctx context.Context, req *connect.Request[agentv1.RespondToPlanRequest]) (*connect.Response[agentv1.RespondToPlanResponse], error) {
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.RespondToPlan, relayv1connect.RelayServiceClient.RespondToPlan,
		func(m *agentv1.RespondToPlanRequest) *relayv1.RespondToPlanRequest {
			return &relayv1.RespondToPlanRequest{Request: m, Options: commandOptions(m.GetRequestId())}
		}, nil)
}

func (r routed) RemoveQueuedMessage(ctx context.Context, req *connect.Request[agentv1.RemoveQueuedMessageRequest]) (*connect.Response[agentv1.RemoveQueuedMessageResponse], error) {
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.RemoveQueuedMessage, relayv1connect.RelayServiceClient.RemoveQueuedMessage,
		func(m *agentv1.RemoveQueuedMessageRequest) *relayv1.RemoveQueuedMessageRequest {
			return &relayv1.RemoveQueuedMessageRequest{Request: m, Options: &relayv1.CommandOptions{WaitForAck: true, Frontend: relayFrontend}}
		}, func(res *relayv1.RemoveQueuedMessageResponse) *agentv1.RemoveQueuedMessageResponse {
			return res.GetResult().GetAck().GetRemoveQueuedMessage()
		})
}

func (r routed) SetModel(ctx context.Context, req *connect.Request[agentv1.SetModelRequest]) (*connect.Response[agentv1.SetModelResponse], error) {
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.SetModel, relayv1connect.RelayServiceClient.SetModel,
		func(m *agentv1.SetModelRequest) *relayv1.SetModelRequest {
			return &relayv1.SetModelRequest{Request: m, Options: commandOptions("")}
		}, nil)
}

func (r routed) SetEffort(ctx context.Context, req *connect.Request[agentv1.SetEffortRequest]) (*connect.Response[agentv1.SetEffortResponse], error) {
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.SetEffort, relayv1connect.RelayServiceClient.SetEffort,
		func(m *agentv1.SetEffortRequest) *relayv1.SetEffortRequest {
			return &relayv1.SetEffortRequest{Request: m, Options: commandOptions("")}
		}, nil)
}

func (r routed) SetPermissionMode(ctx context.Context, req *connect.Request[agentv1.SetPermissionModeRequest]) (*connect.Response[agentv1.SetPermissionModeResponse], error) {
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.SetPermissionMode, relayv1connect.RelayServiceClient.SetPermissionMode,
		func(m *agentv1.SetPermissionModeRequest) *relayv1.SetPermissionModeRequest {
			return &relayv1.SetPermissionModeRequest{Request: m, Options: commandOptions("")}
		}, nil)
}

func (r routed) Compact(ctx context.Context, req *connect.Request[agentv1.CompactRequest]) (*connect.Response[agentv1.CompactResponse], error) {
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.Compact, relayv1connect.RelayServiceClient.Compact,
		func(m *agentv1.CompactRequest) *relayv1.CompactRequest {
			return &relayv1.CompactRequest{Request: m, Options: commandOptions("")}
		}, nil)
}

func (r routed) StopTask(ctx context.Context, req *connect.Request[agentv1.StopTaskRequest]) (*connect.Response[agentv1.StopTaskResponse], error) {
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.StopTask, relayv1connect.RelayServiceClient.StopTask,
		func(m *agentv1.StopTaskRequest) *relayv1.StopTaskRequest {
			return &relayv1.StopTaskRequest{Request: m, Options: commandOptions("")}
		}, nil)
}

func (r routed) SuggestFiles(ctx context.Context, req *connect.Request[agentv1.SuggestFilesRequest]) (*connect.Response[agentv1.SuggestFilesResponse], error) {
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.SuggestFiles, relayv1connect.RelayServiceClient.SuggestFiles,
		func(m *agentv1.SuggestFilesRequest) *relayv1.SuggestFilesRequest {
			return &relayv1.SuggestFilesRequest{Request: m}
		},
		func(res *relayv1.SuggestFilesResponse) *agentv1.SuggestFilesResponse { return res.GetResponse() })
}

func (r routed) ListTasks(ctx context.Context, req *connect.Request[agentv1.ListTasksRequest]) (*connect.Response[agentv1.ListTasksResponse], error) {
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.ListTasks, relayv1connect.RelayServiceClient.ListTasks,
		func(m *agentv1.ListTasksRequest) *relayv1.ListTasksRequest {
			return &relayv1.ListTasksRequest{Request: m}
		},
		func(res *relayv1.ListTasksResponse) *agentv1.ListTasksResponse { return res.GetResponse() })
}

func (r routed) ListCheckpoints(ctx context.Context, req *connect.Request[agentv1.ListCheckpointsRequest]) (*connect.Response[agentv1.ListCheckpointsResponse], error) {
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.ListCheckpoints, relayv1connect.RelayServiceClient.ListCheckpoints,
		func(m *agentv1.ListCheckpointsRequest) *relayv1.ListCheckpointsRequest {
			return &relayv1.ListCheckpointsRequest{Request: m}
		},
		func(res *relayv1.ListCheckpointsResponse) *agentv1.ListCheckpointsResponse { return res.GetResponse() })
}

func (r routed) ListOutputStyles(ctx context.Context, req *connect.Request[agentv1.ListOutputStylesRequest]) (*connect.Response[agentv1.ListOutputStylesResponse], error) {
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.ListOutputStyles, relayv1connect.RelayServiceClient.ListOutputStyles,
		func(m *agentv1.ListOutputStylesRequest) *relayv1.ListOutputStylesRequest {
			return &relayv1.ListOutputStylesRequest{Request: m}
		},
		func(res *relayv1.ListOutputStylesResponse) *agentv1.ListOutputStylesResponse {
			return res.GetResponse()
		})
}

func (r routed) SetOutputStyle(ctx context.Context, req *connect.Request[agentv1.SetOutputStyleRequest]) (*connect.Response[agentv1.SetOutputStyleResponse], error) {
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.SetOutputStyle, relayv1connect.RelayServiceClient.SetOutputStyle,
		func(m *agentv1.SetOutputStyleRequest) *relayv1.SetOutputStyleRequest {
			return &relayv1.SetOutputStyleRequest{Request: m, Options: commandOptions("")}
		}, nil)
}
