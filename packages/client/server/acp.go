package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/xlsft/harness/packages/client/acp"
	"github.com/xlsft/harness/packages/client/acp/registry"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Sessions on ACP agents (packages/client/acp). They live next to the harness sessions: acpRouted serves the
// session RPCs of ACP sessions and hands everything else to the Server. ACP state is device-local: nothing about
// agents, their sign-in or their subscription limits is sent to the harness server.

// enginePrefix starts the engine id of ACP sessions.
const enginePrefix = "acp:"

// acpState is the Server's ACP part (Server.acp).
type acpState struct {
	once sync.Once
	reg  *registry.Manager
	mu   sync.Mutex
	live map[string]*acpLive
	// probes finds out the agents' models (acp_models.go).
	probes acpProbes
	// limits asks the agents with a limits source for their limits (acp_limits.go).
	limits acpLimits
}

// AcpRegistry returns the ACP agent manager (HARNESS_HOME/acp); tests may set it with SetAcpRegistry first.
func (s *Server) AcpRegistry() *registry.Manager {
	s.acp.once.Do(func() {
		if s.acp.reg == nil {
			s.acp.reg = registry.New(filepath.Join(config.Home(), "acp"))
		}
	})
	return s.acp.reg
}

// SetAcpRegistry replaces the ACP agent manager (tests: a local registry URL).
func (s *Server) SetAcpRegistry(m *registry.Manager) {
	s.acp.reg = m
	s.acp.once.Do(func() {})
}

func (s *Server) acpLive(id string) *acpLive {
	s.acp.mu.Lock()
	defer s.acp.mu.Unlock()
	return s.acp.live[id]
}

// closeAcp stops every ACP session (client shutdown).
func (s *Server) closeAcp() {
	s.acp.mu.Lock()
	live := s.acp.live
	s.acp.live = nil
	s.acp.mu.Unlock()
	for _, l := range live {
		l.close("host_shutdown")
	}
	s.acp.probes.wait()
	s.acp.limits.wait()
}

// acpLive is an open ACP session.
type acpLive struct {
	srv   *Server
	id    string
	hub   *hub
	sess  *acp.Session
	log   *acpLog
	files fileCache
	dirs  []string

	infoMu sync.Mutex
	mu     sync.Mutex
	meta   acpMeta
	branch string
	diff   *agentv1.DiffStats
	seen   map[string]bool
	closed bool
	diffAt time.Time
	diffGo bool
	// reported is the last models and modes this session told the agent's catalog about.
	reported string
}

// ---- acp.Sink ----

func (l *acpLive) Publish(ev *agentv1.SessionEvent) {
	l.hub.publish(ev)
	l.log.append(ev)
	l.mu.Lock()
	l.meta.UpdatedAt = time.Now().UTC()
	if um := ev.GetUserMessage(); um != nil && l.meta.Title == "" {
		l.meta.Title = titleOf(pbconv.FromBlocks(um.GetContent()))
	}
	meta := l.meta
	l.mu.Unlock()
	if ev.GetTurnFinished() != nil || ev.GetUserMessage() != nil {
		_ = writeAcpMeta(meta)
	}
	if ev.GetTurnFinished() != nil {
		l.srv.refreshAcpLimits(meta.AgentID)
	}
}

func (l *acpLive) PublishEphemeral(ev *agentv1.SessionEvent) { l.hub.publishEphemeral(ev) }

func (l *acpLive) Changed() {
	l.observeState()
	l.publishInfo()
}

// FilesChanged recomputes the diff stats (at most every second) for the Changes pane and the session list.
func (l *acpLive) FilesChanged() {
	l.mu.Lock()
	if l.diffGo || l.closed {
		l.mu.Unlock()
		return
	}
	l.diffGo = true
	wait := time.Second - time.Since(l.diffAt)
	l.mu.Unlock()
	go func() {
		if wait > 0 {
			time.Sleep(wait)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		d := sessionDiffStats(ctx, acpDiffSources(l.meta.ID, l.meta.Cwd, l.dirs))
		cancel()
		l.mu.Lock()
		if d != nil {
			l.diff = d
		}
		l.diffAt, l.diffGo = time.Now(), false
		l.mu.Unlock()
		l.publishInfo()
	}()
}

// setDiffStats records the session's change counts and publishes them when they changed.
func (l *acpLive) setDiffStats(ds *agentv1.DiffStats) {
	l.mu.Lock()
	changed := !proto.Equal(ds, l.diff)
	l.diff = ds
	l.mu.Unlock()
	if changed {
		l.publishInfo()
	}
}

func (l *acpLive) info() *agentv1.SessionInfo {
	st := l.sess.State()
	l.mu.Lock()
	m := l.meta
	title := m.Title
	if st.Title != "" && !m.CustomTitle {
		title = st.Title
	}
	state := l.srv.acpSessionState(l.sess.Config(), st)
	info := &agentv1.SessionInfo{
		Id: l.id, Title: title, Cwd: m.Cwd, GitBranch: l.branch, Model: m.AgentName, Status: st.Status,
		PermissionMode: acp.PermissionModeOf(st.CurrentMode), UpdatedAt: m.UpdatedAt.UTC().Format(time.RFC3339),
		DiffStats: l.diff, NoFolder: m.NoFolder, Engine: m.Engine, UnsupportedFeatures: acp.UnsupportedFeatures(len(state.GetEfforts()) > 0),
		Acp: state, DeviceId: l.srv.DeviceID,
		UsageLimit: st.UsageLimit, KeepAwake: l.srv.keepAwake().Wanted(l.id),
	}
	l.mu.Unlock()
	if st.CurrentModel != "" {
		info.Model = st.CurrentModel
	}
	l.srv.applyState(info)
	return info
}

func (l *acpLive) publishInfo() {
	l.infoMu.Lock()
	defer l.infoMu.Unlock()
	info := l.info()
	l.srv.keepAwake().SetBusy(l.id, statusBusy(info.GetStatus()))
	l.hub.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SessionUpdated{SessionUpdated: info}})
}

func (l *acpLive) close(reason string) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	meta := l.meta
	l.mu.Unlock()
	l.srv.keepAwake().Forget(l.id)
	l.sess.Close()
	_ = writeAcpMeta(meta)
	l.hub.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SessionEnded{SessionEnded: &agentv1.SessionEnded{Reason: reason}}})
	l.hub.close()
	l.log.close()
}

// storedAcpInfo is the listing of a recorded ACP session that is not open.
func (s *Server) storedAcpInfo(m acpMeta) *agentv1.SessionInfo {
	var efforts []acp.Effort
	defaultEffort := ""
	if c := s.AcpRegistry().State(m.AgentID).Catalog; c != nil {
		efforts = c.Efforts
		defaultEffort = acpDefaultEffort(c, acp.EffortsToProto(efforts))
	}
	info := &agentv1.SessionInfo{Id: m.ID, Title: m.Title, Cwd: m.Cwd, Model: m.AgentName, Status: agentv1.SessionStatus_SESSION_STATUS_IDLE,
		UpdatedAt: m.UpdatedAt.UTC().Format(time.RFC3339), NoFolder: m.NoFolder, Engine: m.Engine, UnsupportedFeatures: acp.UnsupportedFeatures(len(efforts) > 0),
		Acp: &agentv1.AcpSessionState{AgentId: m.AgentID, AgentName: m.AgentName, AgentVersion: m.AgentVersion,
			Efforts: acp.EffortsToProto(efforts), CurrentEffortId: m.Effort, DefaultEffortId: defaultEffort}, DeviceId: s.DeviceID}
	s.applyState(info)
	return info
}

// ---- opening ----

// openAcp starts (or resumes, with meta) an ACP session. The agent starts in the background; the session is
// returned at once with acp.starting set.
func (s *Server) openAcp(ctx context.Context, req *agentv1.CreateSessionRequest, agentID string, resume *acpMeta) (*agentv1.SessionInfo, error) {
	if !s.Base.ModePolicy.Managed().ProviderAllowed("acp", enginePrefix+agentID) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("your organization's policy does not allow the ACP agent %s", agentID))
	}
	reg := s.AcpRegistry()
	st := reg.State(agentID)
	if st.Installed == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("ACP agent %s is not installed on this machine; install it first (InstallAcpAgent)", agentID))
	}
	launch, err := reg.Launch(agentID)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if req.Worktree != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("ACP sessions cannot be started in a new worktree yet; start the session in the worktree's folder"))
	}
	name := agentID
	if a, ok := reg.Cached().Find(agentID); ok {
		name = a.Name
	}
	var meta acpMeta
	var chat chatFolder
	if resume != nil {
		meta = *resume
		meta.AgentName, meta.AgentVersion = name, st.Installed.Version
		if meta.NoFolder {
			_ = os.MkdirAll(meta.Cwd, 0o700)
		}
	} else {
		chat, err = s.chatFolder(req, nil)
		if err != nil {
			return nil, err
		}
		id := chat.id
		if id == "" {
			id = uuid.NewString()
		}
		if chat.cwd != "" {
			if fi, err := os.Stat(chat.cwd); err != nil || !fi.IsDir() {
				chat.rollback()
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is not a directory", chat.cwd))
			}
		}
		now := time.Now().UTC()
		meta = acpMeta{ID: id, Engine: enginePrefix + agentID, AgentID: agentID, AgentName: name, AgentVersion: st.Installed.Version,
			Model: strings.TrimSpace(req.GetModel()), Mode: strings.TrimSpace(req.GetAcpModeId()), Effort: strings.TrimSpace(req.GetAcpEffortId()),
			Cwd: chat.cwd, NoFolder: chat.noFolder, CreatedAt: now, UpdatedAt: now}
	}
	if err := writeAcpMeta(meta); err != nil {
		chat.rollback()
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	logw, err := openAcpLog(meta.ID)
	if err != nil {
		chat.rollback()
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// The folder's environment: its variables and additional folders.
	opts := bootstrap.Options{Cwd: meta.Cwd, AdditionalDirs: req.GetAdditionalDirectories()}
	envFolder := meta.Cwd
	s.prepareEnvironment(envFolder, &opts)

	l := &acpLive{srv: s, id: meta.ID, hub: newHub(), log: logw, meta: meta, seen: map[string]bool{},
		dirs: append([]string{meta.Cwd}, mergeDirList(meta.Cwd, opts.AdditionalDirs)...)}
	l.hub.sessionID = meta.ID
	l.branch = workspace.GitBranch(ctx, meta.Cwd)
	contextUsed := meta.ContextUsed
	if resume != nil {
		for _, ev := range readAcpEvents(meta.ID) {
			l.hub.publish(ev)
			// Sessions from before the meta kept it: the last turn's request is the next best.
			if u := ev.GetTurnFinished().GetUsage(); u != nil && meta.ContextUsed == 0 {
				contextUsed = u.GetInputTokens() + u.GetCacheReadInputTokens() + u.GetCacheCreationInputTokens()
			}
		}
	}
	cfg := acp.Config{
		AgentID: agentID, AgentName: name, AgentVersion: st.Installed.Version, Launch: launch,
		Cwd: meta.Cwd, AdditionalDirs: opts.AdditionalDirs, Env: opts.Env, ResumeAgentSessionID: meta.AgentSessionID,
		Model: name, InitialModel: meta.Model, InitialMode: meta.Mode, InitialEffort: meta.Effort, InitialLimits: st.Limits, InitialContextUsed: contextUsed, InitialContextSize: meta.ContextSize, Sink: l, Logger: s.log(),
		OnAuth: func(state registry.AuthState, methods []registry.AuthMethod) {
			_ = reg.SetAuth(agentID, state, methods)
		},
		OnLimits: func(lim *registry.Limits) { _ = reg.SetLimits(agentID, lim) }, LimitsSource: s.acpLimitsSource(agentID),
		OnAgentSession: func(id string) {
			l.mu.Lock()
			l.meta.AgentSessionID = id
			m := l.meta
			l.mu.Unlock()
			_ = writeAcpMeta(m)
		},
	}
	l.sess = acp.New(cfg)
	s.acp.mu.Lock()
	if s.acp.live == nil {
		s.acp.live = map[string]*acpLive{}
	}
	if old := s.acp.live[meta.ID]; old != nil {
		s.acp.mu.Unlock()
		l.sess.Close()
		logw.close()
		return old.info(), nil
	}
	s.acp.live[meta.ID] = l
	s.acp.mu.Unlock()
	// A fetch that finished after st was read skipped this session, which wasn't live yet; it stored its report first.
	if lim := reg.State(agentID).Limits; lim != nil {
		l.sess.ApplyLimits(lim)
	}
	diffCtx, diffDone := context.WithTimeout(ctx, sessionOpenDiffTimeout)
	diff := sessionDiffStats(diffCtx, acpDiffSources(meta.ID, meta.Cwd, l.dirs))
	l.mu.Lock()
	l.diff = diff
	l.mu.Unlock()
	diffDone()
	l.publishInfo()
	go func() {
		if err := l.sess.Open(s.ctx); err == nil {
			s.refreshAcpLimits(agentID)
		}
	}()
	s.sessionOpened(meta.ID)
	return l.info(), nil
}

// ---- acpRouted ----

// acpRouted serves AgentService: session RPCs of ACP sessions are answered here, the rest by routed.
type acpRouted struct{ routed }

func unsupported(what string) error {
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s is not available in sessions that run on an ACP agent", what))
}

// acpTarget returns the open ACP session id names, or an error for a recorded ACP session that is not open.
// (nil, nil): id is not an ACP session.
func (r acpRouted) acpTarget(id string) (*acpLive, error) {
	if l := r.acpLive(id); l != nil {
		return l, nil
	}
	if readAcpMeta(id) != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s is not open; call CreateSession first", id))
	}
	return nil, nil
}

// isAcp reports whether id is an ACP session (open or recorded).
func (r acpRouted) isAcp(id string) bool { return r.acpLive(id) != nil || readAcpMeta(id) != nil }

func (r acpRouted) CreateSession(ctx context.Context, req *connect.Request[agentv1.CreateSessionRequest]) (*connect.Response[agentv1.CreateSessionResponse], error) {
	m := req.Msg
	if id := m.GetResumeSessionId(); id != "" {
		if meta := readAcpMeta(id); meta != nil {
			if m.GetFork() {
				return nil, unsupported("Forking")
			}
			if l := r.acpLive(id); l != nil {
				return connect.NewResponse(&agentv1.CreateSessionResponse{Session: l.info()}), nil
			}
			info, err := r.openAcp(ctx, m, meta.AgentID, meta)
			if err != nil {
				return nil, err
			}
			return connect.NewResponse(&agentv1.CreateSessionResponse{Session: info}), nil
		}
		return r.routed.CreateSession(ctx, req)
	}
	engine := strings.TrimSpace(m.GetEngine())
	switch {
	case engine == "" || engine == "harness":
		return r.routed.CreateSession(ctx, req)
	case strings.HasPrefix(engine, enginePrefix) && len(engine) > len(enginePrefix):
		info, err := r.openAcp(ctx, m, strings.TrimPrefix(engine, enginePrefix), nil)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(&agentv1.CreateSessionResponse{Session: info}), nil
	}
	return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown engine %q (use \"harness\" or \"acp:<agent id>\")", engine))
}

func (r acpRouted) ListSessions(ctx context.Context, req *connect.Request[agentv1.ListSessionsRequest]) (*connect.Response[agentv1.ListSessionsResponse], error) {
	res, err := r.routed.ListSessions(ctx, req)
	if err != nil {
		return nil, err
	}
	cwd := req.Msg.GetCwd()
	byID := map[string]int{}
	for i, s := range res.Msg.Sessions {
		byID[s.Id] = i
	}
	for _, m := range listAcpMeta() {
		if cwd != "" && m.Cwd != cwd {
			continue
		}
		var info *agentv1.SessionInfo
		if l := r.acpLive(m.ID); l != nil {
			info = l.info()
		} else {
			info = r.storedAcpInfo(m)
		}
		if info.Archived && !req.Msg.GetIncludeArchived() {
			continue
		}
		if i, ok := byID[m.ID]; ok {
			res.Msg.Sessions[i] = info
		} else {
			res.Msg.Sessions = append(res.Msg.Sessions, info)
		}
	}
	sortSessions(res.Msg.Sessions)
	return res, nil
}

func sortSessions(ss []*agentv1.SessionInfo) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j].UpdatedAt > ss[j-1].UpdatedAt; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}

func (r acpRouted) Subscribe(ctx context.Context, req *connect.Request[agentv1.SubscribeRequest], stream *connect.ServerStream[agentv1.SubscribeResponse]) error {
	l, err := r.acpTarget(req.Msg.GetSessionId())
	if err != nil {
		return err
	}
	if l == nil {
		return r.routed.Subscribe(ctx, req, stream)
	}
	return r.serveHub(ctx, l.hub, l.info, req.Msg, stream)
}

// serveHub streams a session's hub like Subscribe does for harness sessions.
func (s *Server) serveHub(ctx context.Context, h *hub, info func() *agentv1.SessionInfo, req *agentv1.SubscribeRequest, stream *connect.ServerStream[agentv1.SubscribeResponse]) error {
	replay, ch, unsub := h.subscribe(req.GetAfterSeq())
	defer unsub()
	control := req.GetControl()
	if control {
		if err := stream.Send(&agentv1.SubscribeResponse{LogId: h.id}); err != nil {
			return err
		}
	}
	for _, ev := range replay {
		if err := stream.Send(&agentv1.SubscribeResponse{Event: ev}); err != nil {
			return err
		}
	}
	var beat <-chan time.Time
	if control {
		if err := stream.Send(&agentv1.SubscribeResponse{LogId: h.id, Session: info()}); err != nil {
			return err
		}
		every := s.Heartbeat
		if every <= 0 {
			every = DefaultHeartbeat
		}
		t := time.NewTicker(every)
		defer t.Stop()
		beat = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.ctx.Done():
			return nil
		case <-beat:
			if err := stream.Send(&agentv1.SubscribeResponse{LogId: h.id}); err != nil {
				return err
			}
		case ev, ok := <-ch:
			if !ok {
				if h.isClosed() {
					return nil
				}
				return connect.NewError(connect.CodeResourceExhausted, errors.New("subscriber too slow; resubscribe with after_seq"))
			}
			if err := stream.Send(&agentv1.SubscribeResponse{Event: ev}); err != nil {
				return err
			}
		}
	}
}

// acpError maps runtime errors.
func acpError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, acp.ErrAuthRequired), errors.Is(err, acp.ErrNotReady):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, acp.ErrNoPermission):
		return connect.NewError(connect.CodeNotFound, err)
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	return connect.NewError(connect.CodeFailedPrecondition, err)
}

func (r acpRouted) SendUserMessage(ctx context.Context, req *connect.Request[agentv1.SendUserMessageRequest]) (*connect.Response[agentv1.SendUserMessageResponse], error) {
	l, err := r.acpTarget(req.Msg.GetSessionId())
	if err != nil || l == nil {
		if err != nil {
			return nil, err
		}
		return r.routed.SendUserMessage(ctx, req)
	}
	m := req.Msg
	if len(m.GetContent()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("content is empty"))
	}
	if id := m.GetUuid(); id != "" {
		l.mu.Lock()
		dup := l.seen[id]
		l.seen[id] = true
		l.mu.Unlock()
		if dup {
			// The same message again, now: a queued one starts the next turn ("Send now").
			if m.GetDelivery() == agentv1.SendUserMessageRequest_DELIVERY_NOW && len(l.sess.State().Queue) > 0 {
				l.sess.Interrupt()
			}
			return connect.NewResponse(&agentv1.SendUserMessageResponse{}), nil
		}
	}
	if r.acpSlash(ctx, l, req) {
		return connect.NewResponse(&agentv1.SendUserMessageResponse{}), nil
	}
	if _, err := l.sess.Submit(m.GetUuid(), m.GetContent(), m.GetDelivery() == agentv1.SendUserMessageRequest_DELIVERY_NOW); err != nil {
		if m.GetUuid() != "" {
			l.mu.Lock()
			delete(l.seen, m.GetUuid())
			l.mu.Unlock()
		}
		return nil, acpError(err)
	}
	return connect.NewResponse(&agentv1.SendUserMessageResponse{}), nil
}

func (r acpRouted) RemoveQueuedMessage(ctx context.Context, req *connect.Request[agentv1.RemoveQueuedMessageRequest]) (*connect.Response[agentv1.RemoveQueuedMessageResponse], error) {
	l, err := r.acpTarget(req.Msg.GetSessionId())
	if err != nil || l == nil {
		if err != nil {
			return nil, err
		}
		return r.routed.RemoveQueuedMessage(ctx, req)
	}
	return connect.NewResponse(&agentv1.RemoveQueuedMessageResponse{Removed: l.sess.RemoveQueued(req.Msg.GetUuid())}), nil
}

func (r acpRouted) Interrupt(ctx context.Context, req *connect.Request[agentv1.InterruptRequest]) (*connect.Response[agentv1.InterruptResponse], error) {
	l, err := r.acpTarget(req.Msg.GetSessionId())
	if err != nil || l == nil {
		if err != nil {
			return nil, err
		}
		return r.routed.Interrupt(ctx, req)
	}
	idle := l.sess.Interrupt()
	if idle {
		l.publishInfo()
	}
	return connect.NewResponse(&agentv1.InterruptResponse{Idle: idle}), nil
}

func (r acpRouted) RespondToPermission(ctx context.Context, req *connect.Request[agentv1.RespondToPermissionRequest]) (*connect.Response[agentv1.RespondToPermissionResponse], error) {
	l, err := r.acpTarget(req.Msg.GetSessionId())
	if err != nil || l == nil {
		if err != nil {
			return nil, err
		}
		return r.routed.RespondToPermission(ctx, req)
	}
	if req.Msg.GetDecision() == agentv1.RespondToPermissionRequest_DECISION_UNSPECIFIED {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("decision is required"))
	}
	if err := l.sess.RespondPermission(req.Msg.GetRequestId(), req.Msg.GetDecision(), "user"); err != nil {
		return nil, acpError(err)
	}
	return connect.NewResponse(&agentv1.RespondToPermissionResponse{}), nil
}

func (r acpRouted) SetPermissionMode(ctx context.Context, req *connect.Request[agentv1.SetPermissionModeRequest]) (*connect.Response[agentv1.SetPermissionModeResponse], error) {
	l, err := r.acpTarget(req.Msg.GetSessionId())
	if err != nil || l == nil {
		if err != nil {
			return nil, err
		}
		return r.routed.SetPermissionMode(ctx, req)
	}
	if req.Msg.GetMode() == agentv1.PermissionMode_PERMISSION_MODE_AUTO {
		return nil, unsupported("Auto mode (the harness classifier)")
	}
	if err := l.sess.SetPermissionMode(ctx, req.Msg.GetMode()); err != nil {
		return nil, acpError(err)
	}
	return connect.NewResponse(&agentv1.SetPermissionModeResponse{}), nil
}

func (r acpRouted) SetAcpMode(ctx context.Context, req *connect.Request[agentv1.SetAcpModeRequest]) (*connect.Response[agentv1.SetAcpModeResponse], error) {
	l, err := r.acpTarget(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	if l == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s does not run on an ACP agent", req.Msg.GetSessionId()))
	}
	if err := l.sess.SetMode(ctx, req.Msg.GetModeId()); err != nil {
		return nil, acpError(err)
	}
	return connect.NewResponse(&agentv1.SetAcpModeResponse{}), nil
}

func (r acpRouted) SetModel(ctx context.Context, req *connect.Request[agentv1.SetModelRequest]) (*connect.Response[agentv1.SetModelResponse], error) {
	l, err := r.acpTarget(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	if l == nil {
		return r.setHarnessModel(ctx, req)
	}
	if err := l.modelRefusal(req.Msg.GetEngine(), req.Msg.GetModel()); err != nil {
		return nil, err
	}
	if err := l.sess.SetModel(ctx, req.Msg.GetModel()); err != nil {
		return nil, acpError(err)
	}
	return connect.NewResponse(&agentv1.SetModelResponse{}), nil
}

func (r acpRouted) CloseSession(ctx context.Context, req *connect.Request[agentv1.CloseSessionRequest]) (*connect.Response[agentv1.CloseSessionResponse], error) {
	id := req.Msg.GetSessionId()
	if l := r.acpLive(id); l != nil {
		r.acp.mu.Lock()
		delete(r.acp.live, id)
		r.acp.mu.Unlock()
		l.close("closed")
		r.terminals().KillSession(id)
		return connect.NewResponse(&agentv1.CloseSessionResponse{}), nil
	}
	return r.routed.CloseSession(ctx, req)
}

func (r acpRouted) DeleteSession(ctx context.Context, req *connect.Request[agentv1.DeleteSessionRequest]) (*connect.Response[agentv1.DeleteSessionResponse], error) {
	id := req.Msg.GetSessionId()
	if !r.isAcp(id) {
		return r.routed.DeleteSession(ctx, req)
	}
	if l := r.acpLive(id); l != nil {
		r.acp.mu.Lock()
		delete(r.acp.live, id)
		r.acp.mu.Unlock()
		l.close("deleted")
		r.terminals().KillSession(id)
	}
	if r.Sync != nil {
		cctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
		_, _ = r.Sync.DeleteSession(cctx, &syncv1.DeleteSessionRequest{SessionId: id})
		cancel()
	}
	_ = os.RemoveAll(bootstrap.FolderBaselineDir(id))
	if err := os.RemoveAll(acpSessionDir(id)); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	r.removeChatDir(id)
	r.setState(id, userState{})
	return connect.NewResponse(&agentv1.DeleteSessionResponse{}), nil
}

// UpdateSession: a rename is recorded locally; pin / unread / archive are per-user state on the harness server
// like for harness sessions. The metadata sent along never carries the ACP state (agent, sign-in, limits).
func (r acpRouted) UpdateSession(ctx context.Context, req *connect.Request[agentv1.UpdateSessionRequest]) (*connect.Response[agentv1.UpdateSessionResponse], error) {
	m := req.Msg
	id := m.GetSessionId()
	meta := readAcpMeta(id)
	if meta == nil {
		return r.routed.UpdateSession(ctx, req)
	}
	l := r.acpLive(id)
	title := ""
	if m.Title != nil {
		if title = truncateRunes(strings.Join(strings.Fields(m.GetTitle()), " "), maxCustomTitle); title == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("title must not be empty"))
		}
	}
	flags := m.Pinned != nil || m.Unread != nil || m.Archived != nil
	if !flags && m.Title == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nothing to update"))
	}
	if flags && r.Sync == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("pin, unread and archive are stored on the harness server, and this client does not sync with it (not signed in, or sync disabled)"))
	}
	st := r.stateOf(id)
	var paths []string
	if m.Pinned != nil {
		st.pinned = m.GetPinned()
		paths = append(paths, "info.pinned")
	}
	if m.Unread != nil {
		st.unread = m.GetUnread()
		paths = append(paths, "info.unread")
	}
	if m.Archived != nil {
		st.archived = m.GetArchived()
		paths = append(paths, "info.archived")
	}
	if flags {
		var info *agentv1.SessionInfo
		if l != nil {
			info = l.info()
		} else {
			info = r.storedAcpInfo(*meta)
		}
		// Device-local: the agent's state and its models never leave the client.
		info.Acp, info.Model = nil, meta.AgentName
		info.Pinned, info.Unread, info.Archived = st.pinned, st.unread, st.archived
		cctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
		_, err := r.Sync.UpsertSession(cctx, &syncv1.UpsertSessionRequest{Session: &syncv1.SessionMetadata{Info: info}, UpdatePaths: paths})
		cancel()
		if err != nil {
			return nil, syncError(err)
		}
		r.setState(id, st)
	}
	if m.Title != nil {
		if l != nil {
			l.mu.Lock()
			l.meta.Title, l.meta.CustomTitle = title, true
			mm := l.meta
			l.mu.Unlock()
			_ = writeAcpMeta(mm)
		} else {
			meta.Title, meta.CustomTitle = title, true
			_ = writeAcpMeta(*meta)
		}
	}
	if m.GetArchived() && l != nil {
		r.acp.mu.Lock()
		delete(r.acp.live, id)
		r.acp.mu.Unlock()
		l.close("archived")
	}
	var info *agentv1.SessionInfo
	if l2 := r.acpLive(id); l2 != nil {
		l2.publishInfo()
		info = l2.info()
	} else {
		info = r.storedAcpInfo(*readAcpMeta(id))
	}
	return connect.NewResponse(&agentv1.UpdateSessionResponse{Session: info}), nil
}

func (r acpRouted) SuggestFiles(ctx context.Context, req *connect.Request[agentv1.SuggestFilesRequest]) (*connect.Response[agentv1.SuggestFilesResponse], error) {
	l, err := r.acpTarget(req.Msg.GetSessionId())
	if err != nil || l == nil {
		if err != nil {
			return nil, err
		}
		return r.routed.SuggestFiles(ctx, req)
	}
	limit := int(req.Msg.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	return connect.NewResponse(&agentv1.SuggestFilesResponse{Paths: FuzzyFilter(l.files.list(ctx, l.meta.Cwd), req.Msg.GetQuery(), limit)}), nil
}

// ---- unsupported features ----

func (r acpRouted) ListCheckpoints(ctx context.Context, req *connect.Request[agentv1.ListCheckpointsRequest]) (*connect.Response[agentv1.ListCheckpointsResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return connect.NewResponse(&agentv1.ListCheckpointsResponse{}), nil
	}
	return r.routed.ListCheckpoints(ctx, req)
}

func (r acpRouted) Rewind(ctx context.Context, req *connect.Request[agentv1.RewindRequest]) (*connect.Response[agentv1.RewindResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return nil, unsupported("Rewind")
	}
	return r.routed.Rewind(ctx, req)
}

func (r acpRouted) Compact(ctx context.Context, req *connect.Request[agentv1.CompactRequest]) (*connect.Response[agentv1.CompactResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return nil, unsupported("Compaction")
	}
	return r.routed.Compact(ctx, req)
}

func (r acpRouted) GetContextUsage(ctx context.Context, req *connect.Request[agentv1.GetContextUsageRequest]) (*connect.Response[agentv1.GetContextUsageResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return nil, unsupported("The context breakdown")
	}
	return r.routed.GetContextUsage(ctx, req)
}

func (r acpRouted) AnswerQuestion(ctx context.Context, req *connect.Request[agentv1.AnswerQuestionRequest]) (*connect.Response[agentv1.AnswerQuestionResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return nil, unsupported("Questions")
	}
	return r.routed.AnswerQuestion(ctx, req)
}

func (r acpRouted) RespondToPlan(ctx context.Context, req *connect.Request[agentv1.RespondToPlanRequest]) (*connect.Response[agentv1.RespondToPlanResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return nil, unsupported("Plan approval")
	}
	return r.routed.RespondToPlan(ctx, req)
}

func (r acpRouted) SetEffort(ctx context.Context, req *connect.Request[agentv1.SetEffortRequest]) (*connect.Response[agentv1.SetEffortResponse], error) {
	l, err := r.acpTarget(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	if l == nil {
		return r.routed.SetEffort(ctx, req)
	}
	level := req.Msg.GetLevelId()
	if level == "" {
		level = pbconv.FromEffort(req.Msg.GetEffort())
	}
	if err := l.effortRefusal(level); err != nil {
		return nil, err
	}
	if err := l.sess.SetEffort(ctx, level); err != nil {
		return nil, acpError(err)
	}
	return connect.NewResponse(&agentv1.SetEffortResponse{}), nil
}

func (r acpRouted) SetOutputStyle(ctx context.Context, req *connect.Request[agentv1.SetOutputStyleRequest]) (*connect.Response[agentv1.SetOutputStyleResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return nil, unsupported("Output styles")
	}
	return r.routed.SetOutputStyle(ctx, req)
}

func (r acpRouted) SetAutoContinue(ctx context.Context, req *connect.Request[agentv1.SetAutoContinueRequest]) (*connect.Response[agentv1.SetAutoContinueResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return nil, unsupported("Continuing when limits return")
	}
	return r.routed.SetAutoContinue(ctx, req)
}

func (r acpRouted) ListOutputStyles(ctx context.Context, req *connect.Request[agentv1.ListOutputStylesRequest]) (*connect.Response[agentv1.ListOutputStylesResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return nil, unsupported("Output styles")
	}
	return r.routed.ListOutputStyles(ctx, req)
}

func (r acpRouted) ListTasks(ctx context.Context, req *connect.Request[agentv1.ListTasksRequest]) (*connect.Response[agentv1.ListTasksResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return connect.NewResponse(&agentv1.ListTasksResponse{}), nil
	}
	return r.routed.ListTasks(ctx, req)
}

func (r acpRouted) StopTask(ctx context.Context, req *connect.Request[agentv1.StopTaskRequest]) (*connect.Response[agentv1.StopTaskResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return nil, unsupported("Background tasks")
	}
	return r.routed.StopTask(ctx, req)
}

func (r acpRouted) RunShellCommand(ctx context.Context, req *connect.Request[agentv1.RunShellCommandRequest]) (*connect.Response[agentv1.RunShellCommandResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return nil, unsupported("Shell mode")
	}
	return r.routed.RunShellCommand(ctx, req)
}

func (r acpRouted) StopShellCommand(ctx context.Context, req *connect.Request[agentv1.StopShellCommandRequest]) (*connect.Response[agentv1.StopShellCommandResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return nil, unsupported("Shell mode")
	}
	return r.routed.StopShellCommand(ctx, req)
}

func (r acpRouted) SetSessionFolder(ctx context.Context, req *connect.Request[agentv1.SetSessionFolderRequest]) (*connect.Response[agentv1.SetSessionFolderResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return nil, unsupported("Moving the session to another folder")
	}
	return r.routed.SetSessionFolder(ctx, req)
}

func (r acpRouted) AddSessionDirectory(ctx context.Context, req *connect.Request[agentv1.AddSessionDirectoryRequest]) (*connect.Response[agentv1.AddSessionDirectoryResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return nil, unsupported("Adding a working directory to a running session")
	}
	return r.routed.AddSessionDirectory(ctx, req)
}

func (r acpRouted) ListSideChats(ctx context.Context, req *connect.Request[agentv1.ListSideChatsRequest]) (*connect.Response[agentv1.ListSideChatsResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return connect.NewResponse(&agentv1.ListSideChatsResponse{}), nil
	}
	return r.routed.ListSideChats(ctx, req)
}

func (r acpRouted) CreateSideChat(ctx context.Context, req *connect.Request[agentv1.CreateSideChatRequest]) (*connect.Response[agentv1.CreateSideChatResponse], error) {
	if r.isAcp(req.Msg.GetSessionId()) {
		return nil, unsupported("Side chats")
	}
	return r.routed.CreateSideChat(ctx, req)
}

func (r acpRouted) AskSideChat(ctx context.Context, req *connect.Request[agentv1.AskSideChatRequest], stream *connect.ServerStream[agentv1.AskSideChatResponse]) error {
	if r.isAcp(req.Msg.GetSessionId()) {
		return unsupported("Side chats")
	}
	return r.routed.AskSideChat(ctx, req, stream)
}

// acpScope is the file scope of an open ACP session (fileScope, terminals, environment).
func (s *Server) acpScope(id string) (cwd string, dirs []string, ok bool) {
	l := s.acpLive(id)
	if l == nil {
		return "", nil, false
	}
	return l.meta.Cwd, l.dirs, true
}
