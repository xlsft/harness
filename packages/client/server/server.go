// Package server implements harness.agent.v1.AgentService on top of the agent
// loop. Frontends (desktop over a local socket, SSH over forwarded stdio, the
// relay later) talk to the client through it.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xlsft/harness/packages/client/browser"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/computeruse"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/environment"
	"github.com/xlsft/harness/packages/client/extensions"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	"github.com/xlsft/harness/packages/client/keepawake"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/localmodels"
	"github.com/xlsft/harness/packages/client/mcp"
	"github.com/xlsft/harness/packages/client/memory"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/preview"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/skills"
	"github.com/xlsft/harness/packages/client/speech"
	"github.com/xlsft/harness/packages/client/sshhost"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/terminal"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/usagelimit"
	"github.com/xlsft/harness/packages/client/vcs"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Server implements agentv1connect.AgentServiceHandler.
type Server struct {
	agentv1connect.UnimplementedAgentServiceHandler

	// Base holds defaults applied to every session (gateway and prompt
	// adapters, allowed tools, …). Per-session fields are overridden.
	Base bootstrap.Options
	// UserEmail is reported by Initialize.
	UserEmail string
	Logger    *slog.Logger
	// NoAITitles keeps the provisional titles (first prompt) instead of
	// asking the small-slot model for a title after the first turn.
	NoAITitles bool
	// HistoryTurns and HistoryEvents cap the history replayed into the
	// event log of a resumed session (0: defaults).
	HistoryTurns, HistoryEvents int
	// Sync stores the per-user session state (pinned, unread, archived, renames) and deletes server copies of
	// sessions; nil without sync (see sessionstate.go).
	Sync SessionSync
	// Preferences reads and writes the user's synced settings document (GetUserPreferences, preferences.go);
	// nil without sync.
	Preferences PreferenceDocs
	// MCPClient is the McpService client of the signed-in server (nil: only device-local MCP servers), used by Settings >
	// MCP. MCPInventory overrides what it is built from (tests).
	MCPClient *mcp.Client
	// SkillSearch is external skill search through the xlsft`s harness server; nil: not connected to one.
	SkillSearch  extensions.SkillSearch
	MCPInventory *mcp.Inventory
	// ImportTo is the user's server for imports (import.go); nil without sync.
	ImportTo *syncclient.Client
	// SkillDocs reads the skills and commands stored on the server, for a listing without a session (slash.go); nil
	// without sync.
	SkillDocs SkillDocsSource
	// The last read of SkillDocs.
	skillDocsMu    sync.Mutex
	skillDocsCache skills.Documents
	skillDocsAt    time.Time
	// NoPanes says the frontends cannot open panes (the terminal UI): the
	// Show and Visual tools then report that nothing was shown.
	NoPanes bool
	// Heartbeat is the interval of control heartbeats on Subscribe streams
	// that asked for them (0: DefaultHeartbeat).
	Heartbeat time.Duration

	ctx    context.Context
	mu     sync.Mutex
	live   map[string]*liveSession
	cancel context.CancelFunc
	// observer is told about opened sessions (SetSessionObserver).
	observer func(sessionID string)
	// Core, when set, forwards the settings screens' server calls (CoreProxy).
	Core *CoreProxy
	// AutoMemory keeps auto memory on the server (nil: on this machine only); Transcripts searches the uploaded transcripts.
	AutoMemory  memory.AutoDocs
	Transcripts TranscriptSearcher
	// Objects, when set, serves the object store's bytes to frontends (ObjectProxy).
	Objects *ObjectProxy

	// importing holds the one running import (import.go).
	importing atomic.Bool

	// DeviceID is this machine's device id (dev_...): sessions the server lists for another device are remote
	// (remote.go). Empty when unknown; then only the local transcripts decide.
	DeviceID string
	// Relay, when set, tells which of the user's machines are online and drives remote sessions open on them.
	Relay *RelayViewer
	// RelayOffReason says why no relay host mirrors this client's sessions (SessionInfo.relay_unavailable_reason;
	// empty: "not signed in"). relayHost is the attached host (SetRelayHost, relay.go).
	RelayOffReason string
	relayHost      atomic.Pointer[RelayHost]
	// Inhibitor overrides how sleep is held off (tests); awake is the keep-awake switch of the sessions, deviceMu
	// guards the device settings file (controltools.go).
	Inhibitor keepawake.Inhibitor
	awakeOnce sync.Once
	awake     *keepawake.Holder
	deviceMu  sync.Mutex

	// Environments stores the folder environments (environment.go); nil: HARNESS_HOME/environments.
	Environments *environment.Store
	// WebURL is the base URL of a web UI that opens sessions (InitializeResponse.web_url); empty when none.
	WebURL string
	// ServerVersion reads the version of the signed-in server (InitializeResponse.server_version); nil: unknown.
	ServerVersion func(context.Context) string

	// states is the last known user state of sessions, by id (stateMu; never held while taking ls.mu).
	stateMu sync.Mutex
	states  map[string]userState
	// localStates is the state kept in HARNESS_HOME/session-state.json without a server, until one takes it (stateMu).
	localStates map[string]userState
	localLoaded bool
	// listed is the last known server copy of the caller's sessions, by id (stateMu); presence is the last
	// relay presence (stateMu).
	listed   map[string]*agentv1.SessionInfo
	presence relayPresence

	// terms are the client's terminals (terminals.go); termOnce creates the manager.
	termOnce sync.Once
	terms    *terminal.Manager
	// SSHClient runs the user's ssh for SSH hosts (sshhosts.go); nil: the default, under HARNESS_HOME/ssh. SSHBinaries finds a
	// harness-client to upload to a host (nil: this very binary, for a host of the same platform).
	SSHClient   *sshhost.Client
	SSHBinaries sshhost.Binaries
	sshOnce     sync.Once
	ssh         *sshHostService
	// Browsers runs the sessions' browsers (browsers.go); nil: created on first use under HARNESS_HOME/browser.
	Browsers    *browser.Manager
	previewOnce sync.Once
	previewMgr  *preview.Manager
	browserOnce sync.Once
	// ComputerUse is computer use on this client (computeruse.go); nil: created on first use under HARNESS_HOME/computer-use.
	ComputerUse  *computeruse.Manager
	computerOnce sync.Once

	// sides tracks the running side chat answers (sidechats.go).
	sides sideChatRuns

	// SpeechCore reaches the server's global speech providers (speech.go); nil without a server connection.
	SpeechCore *speech.Core
	// SpeechLocal runs this machine's speech engines; nil: created on first use under HARNESS_HOME/speech.
	SpeechLocal *speech.Local
	speech      speechState
	// LocalModels holds this machine's local LLM providers (localmodels.go); nil: localmodels.Default().
	LocalModels *localmodels.Registry
	// acp holds the ACP agents and the sessions running on them (acp.go).
	acp acpState
	// prResume reopens sessions watching their PR once (prs.go).
	prResume sync.Once
	// UsageWatch tunes the watch over sessions whose provider is out of usage (usagelimit.go).
	UsageWatch UsageWatchTimings

	// ScheduleSync follows and writes the schedule documents (schedules.go); nil: schedules stay on this machine.
	ScheduleSync *syncclient.Syncer
	// CoreURL is the signed-in server, for the URL of a webhook.
	CoreURL string
	// ScheduleEvery and ScheduleFilePoll tune the scheduler's loop; ScheduleClock replaces its clock (tests).
	ScheduleEvery, ScheduleFilePoll time.Duration
	ScheduleClock                   func() time.Time
	schedOnce, schedStart           sync.Once
	sched                           atomic.Pointer[scheduleHost]
}

var _ agentv1connect.AgentServiceHandler = (*Server)(nil)

// DefaultHeartbeat is the interval of Subscribe control heartbeats.
const DefaultHeartbeat = 15 * time.Second

// New creates a server. Base.Gateway and Base.Prompts should be set so all
// sessions share one connection to the harness server.
func New(base bootstrap.Options) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{Base: base, ctx: ctx, cancel: cancel, live: map[string]*liveSession{}}
	bootstrap.IsolateChats() // terminals and file panes of folderless sessions see no repository above them
	go s.runIdleCompaction()
	return s
}

func (s *Server) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Handler returns the HTTP handler (path prefix and handler).
func (s *Server) Handler() (string, http.Handler) {
	return agentv1connect.NewAgentServiceHandler(acpRouted{routed{s}})
}

// Close interrupts all sessions and releases them.
func (s *Server) Close() {
	s.cancel()
	s.StopScheduler()
	s.mu.Lock()
	live := s.live
	s.live = map[string]*liveSession{}
	s.mu.Unlock()
	for _, ls := range live {
		ls.close("host_shutdown")
	}
	s.closeAcp()
	s.terminals().Close()
	s.browsers().Close()
	s.previews().Close()
	s.computerUse().StopAll()
}

// ServeUnix serves AgentService over h2c on a unix socket until ctx ends.
func (s *Server) ServeUnix(ctx context.Context, socket string) error {
	if len(socket) >= 104 {
		return fmt.Errorf("socket path too long (%d bytes; unix sockets allow ~103): %s", len(socket), socket)
	}
	_ = os.Remove(socket)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return err
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	_ = os.Chmod(socket, 0o600)
	defer os.Remove(socket)
	return s.Serve(ctx, ln)
}

// Serve serves AgentService over h2c (and HTTP/1.1 for Connect) on ln.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	mux := s.mux()
	// No browser reaches a socket listener: any Origin is refused.
	s.registerTerminalSocket(mux, nil)
	s.registerBrowserSocket(mux, nil)
	return s.serveHandler(ctx, ln, mux)
}

func (s *Server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	path, h := s.Handler()
	mux.Handle(path, h)
	mux.Handle(agentv1connect.NewRealBrowserServiceHandler(&realBrowserService{s: s}))
	s.sshOnce.Do(func() { s.ssh = &sshHostService{s: s} })
	mux.Handle(agentv1connect.NewSshHostServiceHandler(s.ssh))
	pluginsPath, pluginsHandler := agentv1connect.NewPluginServiceHandler(s)
	mux.Handle(pluginsPath, pluginsHandler)
	extensionsPath, extensionsHandler := agentv1connect.NewExtensionsServiceHandler(s)
	mux.Handle(extensionsPath, extensionsHandler)
	s.registerLibrary(mux)
	if s.Core != nil {
		s.Core.register(mux)
	}
	if s.Objects != nil {
		mux.Handle(ObjectProxyPath, s.Objects)
		mux.Handle(ArtifactProxyPath, s.Objects.artifactHandler())
	}
	return mux
}

// serveHandler serves h over HTTP/1.1 and h2c on ln until ctx ends, then
// closes all sessions.
func (s *Server) serveHandler(ctx context.Context, ln net.Listener, h http.Handler) error {
	sweepFolderBaselines()
	go s.prResume.Do(s.resumePRWatches)
	s.startScheduler()
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: h, Protocols: p, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(sctx)
	}()
	err := srv.Serve(ln)
	s.Close()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// ---- RPCs ----

func (s *Server) Initialize(ctx context.Context, req *connect.Request[agentv1.InitializeRequest]) (*connect.Response[agentv1.InitializeResponse], error) {
	resp := &agentv1.InitializeResponse{ClientVersion: config.Version, UserEmail: s.UserEmail, WebUrl: s.WebURL}
	if s.ServerVersion != nil {
		resp.ServerVersion = s.ServerVersion(ctx)
	}
	policy := s.Base.ModePolicy
	for _, m := range permissions.ModeCycle() {
		if policy.Allowed(m) {
			resp.PermissionModes = append(resp.PermissionModes, modeToProto(m))
		}
	}
	for _, m := range policy.Disabled() {
		resp.DisabledPermissionModes = append(resp.DisabledPermissionModes, modeToProto(m))
	}
	if s.Base.Gateway != nil {
		if cat, err := s.Base.Gateway.Models(ctx); err == nil {
			for _, m := range cat.Models {
				resp.ModelIds = append(resp.ModelIds, m.ID)
				resp.Models = append(resp.Models, modelInfo(m))
			}
			resp.DefaultModel = cat.Slots["main"]
		}
	}
	return connect.NewResponse(resp), nil
}

// modelInfo converts a catalog entry for frontends.
func modelInfo(m llm.Model) *agentv1.ModelInfo {
	mi := &agentv1.ModelInfo{
		Id:            m.ID,
		DisplayName:   m.DisplayName,
		Provider:      m.Provider,
		ProviderName:  m.ProviderName,
		DisplayId:     m.DisplayID,
		ContextWindow: int64(m.ContextWindow),
		Thinking:      m.Capabilities.Thinking,
		Images:        m.Capabilities.Images,
		Local:         localmodels.IsLocal(m.ID),
		SpeedTiers:    m.Capabilities.SpeedTiers,
		CacheTtls:     m.Capabilities.CacheTTLs,
	}
	for _, e := range m.Capabilities.Effort {
		if pe := pbconv.Effort(e); pe != llmv1.Effort_EFFORT_UNSPECIFIED {
			mi.Efforts = append(mi.Efforts, pe)
		}
	}
	slices.Sort(mi.Efforts)
	mi.DefaultEffort = pbconv.Effort(m.RecommendedEffort())
	if p := m.Pricing; p != nil {
		mi.InputPrice, mi.OutputPrice = &p.Input, &p.Output
	}
	return mi
}

func (s *Server) CreateSession(ctx context.Context, req *connect.Request[agentv1.CreateSessionRequest]) (*connect.Response[agentv1.CreateSessionResponse], error) {
	m := req.Msg
	if m.GetFork() && m.GetResumeSessionId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("fork requires resume_session_id"))
	}
	if m.Worktree != nil && m.GetResumeSessionId() != "" && !m.GetFork() {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("worktree cannot be combined with resume_session_id unless fork is set"))
	}
	// A session of another machine: only a fork of its server transcript can run here (remote.go).
	remote := s.remoteInfo(m.GetResumeSessionId())
	if remote != nil && !m.GetFork() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s runs on %s; fork it to continue here", remote.Id, deviceLabel(remote)))
	}
	if remote != nil && m.GetForkAtCheckpointId() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a session of another machine can only be forked at its end"))
	}
	if id := m.GetResumeSessionId(); id != "" && !m.GetFork() {
		s.mu.Lock()
		ls := s.live[id]
		s.mu.Unlock()
		if ls != nil {
			return connect.NewResponse(&agentv1.CreateSessionResponse{Session: ls.info()}), nil
		}
	}
	mode, err := modeFromProto(m.GetPermissionMode())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if m.GetPermissionMode() != agentv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED && !s.Base.ModePolicy.Allowed(mode) {
		return nil, connect.NewError(connect.CodePermissionDenied, &permissions.ModeDisabledError{Mode: mode, Policy: true})
	}
	opts := s.Base
	chat, err := s.chatFolder(m, remote)
	if err != nil {
		return nil, err
	}
	opts.Cwd, opts.SessionID, opts.NoFolder = chat.cwd, chat.id, chat.noFolder
	moved := ""
	if id := m.GetResumeSessionId(); id != "" && !m.GetFork() && remote == nil {
		if moved = movedFolder(id); moved != "" { // it moved itself: it resumes where it went, whatever the request says
			opts.Cwd, opts.NoFolder = moved, config.IsChatDir(moved)
		}
	}
	if m.GetModel() != "" {
		opts.Model = m.GetModel()
	}
	// UNSPECIFIED keeps the base mode, or leaves it to settings (permissions.defaultMode).
	if m.GetPermissionMode() != agentv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED {
		opts.PermissionMode = mode
	}
	if e := pbconv.FromEffort(m.GetEffort()); e != "" {
		opts.Effort = e
	}
	// A new session nobody chose an effort for; a session that exists keeps its own.
	opts.StartingEffort = m.GetResumeSessionId() == ""
	if n := m.GetMaxTurns(); n > 0 {
		opts.MaxTurns = int(n)
	}
	for _, r := range append(append([]string(nil), m.GetAllowedTools()...), m.GetDisallowedTools()...) {
		if err := permissions.ValidateRule(r); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	opts.AllowedTools = append(append([]string(nil), opts.AllowedTools...), m.GetAllowedTools()...)
	opts.DisallowedTools = append(append([]string(nil), opts.DisallowedTools...), m.GetDisallowedTools()...)
	opts.AdditionalDirs = append(append([]string(nil), opts.AdditionalDirs...), m.GetAdditionalDirectories()...)
	opts.Resume = m.GetResumeSessionId()
	opts.Fork = m.GetFork()
	opts.ForkAtCheckpoint = m.GetForkAtCheckpointId()
	if opts.Entrypoint == "" {
		opts.Entrypoint = "agent-service"
	}
	wt, wtMeta, err := createWorktree(ctx, m)
	if err != nil {
		return nil, err
	}
	if wt != nil {
		opts.Cwd, opts.Worktree = wt.Cwd, wtMeta
	}
	envFolder := m.GetCwd()
	if chat.noFolder || moved != "" {
		envFolder = opts.Cwd // per-session: keyed by the private directory
	}
	senv := s.prepareEnvironment(envFolder, &opts)
	if remote != nil {
		id, err := s.forkRemote(ctx, remote, opts.Cwd, opts.SessionID)
		if err != nil {
			s.rollbackWorktree(wt)
			chat.rollback()
			return nil, err
		}
		opts.Resume, opts.Fork = id, false
	}
	ls := newLiveSession(s)
	if opts.Browser == nil {
		opts.Browser = s.browsers()
	}
	if opts.Preview == nil {
		opts.Preview = s.previews()
	}
	// The terminal UI has no access card or indicator.
	if opts.ComputerUse == nil && !s.NoPanes {
		opts.ComputerUse = s.computerUse()
	}
	if opts.Terminals == nil && !s.NoPanes {
		opts.Terminals = s.terminals()
	}
	opts.OnEvent = ls.onEvent
	opts.Logger = s.log()
	usage := s.newUsageMonitor()
	opts.UsageMonitor = usage
	ls.usage.monitor = usage
	sess, err := bootstrap.Open(ctx, opts)
	if err != nil {
		s.rollbackWorktree(wt)
		chat.rollback()
		return nil, modeError(err, connect.CodeFailedPrecondition)
	}
	ls.sess = sess
	sess.SetPlaceHooks(ls.placeHooks())
	ls.attachEnvironment(senv)
	ls.branch = workspace.GitBranch(ctx, sess.Cwd)
	ls.hub.sessionID = sess.ID
	entries := sess.Transcript.Entries()
	ls.initTitle(session.ReadTitles(entries))
	ls.relayChoice = session.ReadRemoteControl(entries)
	// A fork starts by itself: it continues nothing and keeps no messages of the session it came from.
	ls.autoContinue = !m.GetFork() && session.ReadAutoContinue(entries)
	if u := lastUsageLimit(entries); u != nil {
		// The state on record: SessionInfo shows it, and the monitor publishes only what changes from it.
		u.AutoContinue = ls.autoContinue
		u.Interrupted = u.Interrupted && !m.GetFork()
		ls.usageLimit = u
		usage.Seed(pbconv.FromUsageLimit(u))
	}
	diffCtx, diffDone := context.WithTimeout(ctx, sessionOpenDiffTimeout)
	ls.diff = sessionDiffStats(diffCtx, ls.diffSources())
	diffDone()
	if sess.Resumed {
		// The history goes first (seq 1..n), so Subscribe(after_seq=0) shows the conversation so far.
		bundle := sess.Agent.Bundle()
		interrupts := []string{agent.InterruptedFallback, bundle.Reminder("interrupted", ""), bundle.Reminder("interrupted_tool_use", "")}
		for _, ev := range replayHistory(entries, replayOptions{Cwd: sess.Cwd, Interrupts: interrupts, MaxTurns: s.HistoryTurns, MaxEvents: s.HistoryEvents}) {
			ls.hub.publish(ev)
			ls.history++
		}
		// Track (at open) replaced the synced metadata; restore what the transcript knows.
		if t := ls.info().Title; t != "" {
			ls.syncMeta(func(m *syncclient.SessionMeta) { m.Title = t })
		}
	}
	if sess.Resumed && !m.GetFork() {
		ls.loadGoal()
		if list := sess.ToolEnv.TaskGraph.List(); len(list) > 0 {
			ls.hub.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TaskGraphUpdated{TaskGraphUpdated: taskGraphProto(list)}})
			ls.history++
		}
	}
	if sess.Resumed {
		for _, a := range sess.Subagents.List() {
			ls.hub.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SubagentUpdated{SubagentUpdated: &agentv1.SubagentUpdated{Agent: subagentToProto(a)}}})
			ls.history++
		}
		for _, w := range sess.Workflows.List() {
			ls.hub.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_WorkflowUpdated{WorkflowUpdated: &agentv1.WorkflowUpdated{Workflow: workflowToProto(w)}}})
			ls.history++
		}
	}
	if m.GetFork() {
		ls.titleFork()
	}
	ls.syncDiffStats(ls.diff)
	sess.Permissions.SetAsker(ls)
	sess.Agent.SetInteractor(ls)
	ls.watchMcp()
	ls.driver = agent.NewDriver(s.ctx, sess.Agent, ls.onResult)
	sess.Agent.SetWake(ls.driver.Wake)
	if sess.Resumed && !m.GetFork() {
		ls.restoreHeld(entries)
	}
	ls.driver.SetHeld(usage.Current().State == usagelimit.Reached)
	s.mu.Lock()
	s.live[sess.ID] = ls
	s.mu.Unlock()
	sess.Agent.WakeIfNotified()
	if p := s.Base.ModePolicy; p != nil {
		// The policy may have changed while the session opened (before it was live to follow it).
		ls.applyModePolicy(p.Disabled())
	}
	ls.publishInfo()
	ls.links.origin = m
	ls.publishProposals()
	if remote != nil {
		ls.links.forkOf = remote.Id
	}
	ls.drainNotes()
	for _, w := range sess.Warnings {
		ls.notice(agentv1.Notice_LEVEL_WARNING, "session_warning", w)
	}
	if todos := sess.Agent.Todos(); len(todos) > 0 {
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TodosUpdated{TodosUpdated: &agentv1.TodosUpdated{Todos: todosToProto(todos)}}})
	}
	ls.startUsageWatch()
	ls.startPR()
	sess.StartHooks()
	s.sessionOpened(sess.ID)
	return connect.NewResponse(&agentv1.CreateSessionResponse{Session: ls.info()}), nil
}

func (s *Server) ListSessions(ctx context.Context, req *connect.Request[agentv1.ListSessionsRequest]) (*connect.Response[agentv1.ListSessionsResponse], error) {
	cwd := req.Msg.GetCwd()
	out := map[string]*agentv1.SessionInfo{}
	if cwd != "" {
		store := session.Store{Root: config.ProjectsDir()}
		files, _ := filepath.Glob(filepath.Join(store.ProjectDir(cwd), "*.jsonl"))
		for _, f := range files {
			st, err := os.Stat(f)
			if err != nil {
				continue
			}
			id := strings.TrimSuffix(filepath.Base(f), ".jsonl")
			title, parent := listedTranscript(f)
			out[id] = &agentv1.SessionInfo{Id: id, Cwd: cwd, Title: title, Status: agentv1.SessionStatus_SESSION_STATUS_IDLE,
				UpdatedAt: st.ModTime().UTC().Format(time.RFC3339), ParentSessionId: parent, Worktree: transcriptWorktree(f)}
		}
	} else {
		// No cwd: the most recent sessions of every project, so a frontend's session list survives a
		// client restart. Each transcript records its own cwd.
		for _, f := range recentTranscripts(maxListedSessions) {
			tcwd, wt := transcriptHead(f.path, false)
			if tcwd == "" {
				continue // never used: no message, no folder
			}
			id := strings.TrimSuffix(filepath.Base(f.path), ".jsonl")
			title, parent := listedTranscript(f.path)
			out[id] = &agentv1.SessionInfo{Id: id, Cwd: tcwd, Title: title, Status: agentv1.SessionStatus_SESSION_STATUS_IDLE,
				UpdatedAt: f.mod.UTC().Format(time.RFC3339), ParentSessionId: parent, Worktree: worktreeToProto(wt)}
		}
	}
	s.mu.Lock()
	for id, ls := range s.live {
		if cwd == "" || ls.sess.Dir() == cwd {
			info := ls.info()
			if old, ok := out[id]; ok && info.Title == "" {
				info.Title = old.Title
			}
			out[id] = info
		}
	}
	s.mu.Unlock()
	s.refreshListing(ctx, cwd)
	for _, info := range s.remoteSessions(cwd, out) {
		out[info.Id] = info
	}
	resp := &agentv1.ListSessionsResponse{}
	for _, v := range out {
		s.applyState(v)
		v.NoFolder = v.NoFolder || isNoFolder(v.Id, v.Cwd)
		if v.PullRequest == nil && !v.Remote {
			v.PullRequest = vcs.LoadPRInfo(bootstrap.PRStateDir(), v.Id)
		}
		if v.Archived && !req.Msg.GetIncludeArchived() {
			continue
		}
		resp.Sessions = append(resp.Sessions, v)
	}
	sort.Slice(resp.Sessions, func(i, j int) bool { return resp.Sessions[i].UpdatedAt > resp.Sessions[j].UpdatedAt })
	return connect.NewResponse(resp), nil
}

// listedTranscripts remembers what the session list shows of a transcript while the file stays the same: frontends
// poll the list, and imported histories run to gigabytes.
var listedTranscripts = struct {
	sync.Mutex
	byPath map[string]listedTranscriptInfo
}{byPath: map[string]listedTranscriptInfo{}}

type listedTranscriptInfo struct {
	size          int64
	mod           time.Time
	title, parent string
}

func listedTranscript(path string) (title, parent string) {
	st, err := os.Stat(path)
	if err != nil {
		return sessionTitle(path), forkedFrom(path)
	}
	listedTranscripts.Lock()
	info, ok := listedTranscripts.byPath[path]
	listedTranscripts.Unlock()
	if ok && info.size == st.Size() && info.mod.Equal(st.ModTime()) {
		return info.title, info.parent
	}
	title, parent = sessionTitle(path), forkedFrom(path)
	listedTranscripts.Lock()
	listedTranscripts.byPath[path] = listedTranscriptInfo{st.Size(), st.ModTime(), title, parent}
	listedTranscripts.Unlock()
	return title, parent
}

// sessionTitle returns the recorded custom or AI title of a transcript, else
// the first user prompt (truncated) as a provisional title.
func sessionTitle(path string) string {
	entries, err := session.Load(path)
	if err != nil {
		return ""
	}
	t := session.ReadTitles(entries)
	if b := t.Best(); b != "" {
		return b
	}
	return provisionalTitle(t.FirstPrompt)
}

// titleOf is the provisional title of a prompt.
func titleOf(blocks []llm.ContentBlock) string {
	for _, b := range blocks {
		// An attached text file (composer: messageContent) is not what the session is about.
		if b.Type == llm.BlockText && strings.TrimSpace(b.Text) != "" && !strings.HasPrefix(b.Text, "<file name=") {
			return provisionalTitle(b.Text)
		}
	}
	return ""
}

func forkedFrom(path string) string {
	entries, err := session.Load(path)
	if err != nil {
		return ""
	}
	return session.ParentOf(entries)
}

func (s *Server) get(id string) (*liveSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ls := s.live[id]
	if ls == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s is not open; call CreateSession first", id))
	}
	return ls, nil
}

func (s *Server) Subscribe(ctx context.Context, req *connect.Request[agentv1.SubscribeRequest], stream *connect.ServerStream[agentv1.SubscribeResponse]) error {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		if info := s.remoteInfo(req.Msg.GetSessionId()); info != nil {
			return s.subscribeRemote(ctx, info, req.Msg.GetAfterSeq(), stream)
		}
		return err
	}
	replay, ch, unsub := ls.hub.subscribe(req.Msg.GetAfterSeq())
	defer unsub()
	control := req.Msg.GetControl()
	logID := ls.hub.id
	if control {
		// First, so a frontend holding seqs of another log resets before the replay.
		if err := stream.Send(&agentv1.SubscribeResponse{LogId: logID}); err != nil {
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
		if err := stream.Send(&agentv1.SubscribeResponse{LogId: logID, Session: ls.info()}); err != nil {
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
			if err := stream.Send(&agentv1.SubscribeResponse{LogId: logID}); err != nil {
				return err
			}
		case ev, ok := <-ch:
			if !ok {
				if ls.hub.isClosed() {
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

// deliverNow gets the queued messages read as soon as possible. Running Bash commands move to the background (the
// model gets the queued_message note) so they are read in this turn right after the tool calls; otherwise the turn
// is interrupted and they start the next one. An open question keeps waiting: the message stays queued and follows
// the answer, or becomes it ("answer with message").
func (ls *liveSession) deliverNow() {
	if !ls.sess.ToolEnv.Shell.BackgroundRunning(tools.BackgroundQueuedMessage) && !ls.questionPending() {
		ls.driver.Interrupt()
	}
}

// queuedMessageGrace is how long a running command may keep a queued message waiting before it moves to the background.
const queuedMessageGrace = time.Second

func (s *Server) SendUserMessage(ctx context.Context, req *connect.Request[agentv1.SendUserMessageRequest]) (*connect.Response[agentv1.SendUserMessageResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	ls.userSpoke()
	s.noteMessageOrigin(ctx, ls.sess.ID, req.Peer().Addr)
	ls.drivenByRelay.Store(isRelayOrigin(ctx))
	if s.runSlash(ctx, ls, req) {
		return connect.NewResponse(&agentv1.SendUserMessageResponse{}), nil
	}
	return s.submitMessage(ls, req)
}

// submitMessage hands a message to the session: what the user sends, and the brief of a session another one starts.
func (s *Server) submitMessage(ls *liveSession, req *connect.Request[agentv1.SendUserMessageRequest]) (*connect.Response[agentv1.SendUserMessageResponse], error) {
	content := pbconv.FromBlocks(req.Msg.GetContent())
	if len(content) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("content is empty"))
	}
	if id := req.Msg.GetUuid(); id != "" {
		ls.mu.Lock()
		dup := ls.seen[id]
		ls.seen[id] = true
		ls.mu.Unlock()
		if dup {
			// The same message again, now: a queued one is delivered as soon as possible ("Send now"), a held one
			// tries the provider at once.
			if req.Msg.GetDelivery() == agentv1.SendUserMessageRequest_DELIVERY_NOW && ls.sess.Agent.QueueLen() > 0 {
				if ls.driver.Held() {
					ls.sendHeldNow()
				} else {
					ls.deliverNow()
				}
			}
			return connect.NewResponse(&agentv1.SendUserMessageResponse{}), nil
		}
	}
	content, err := ls.expandMcpMessage(ls.ctx, content)
	if err != nil {
		return nil, err
	}
	// "/compact [instructions]" compacts instead of starting a turn.
	if len(content) == 1 && content[0].Type == llm.BlockText {
		if instructions, ok := agent.CompactCommand(content[0].Text); ok {
			if err := ls.compactCommand(instructions); err != nil {
				return nil, err
			}
			return connect.NewResponse(&agentv1.SendUserMessageResponse{}), nil
		}
	}
	ls.mu.Lock()
	provisional := ""
	if ls.title == "" {
		ls.title = titleOf(content)
		provisional = ls.title
	}
	ls.mu.Unlock()
	if provisional != "" {
		ls.syncMeta(func(m *syncclient.SessionMeta) { m.Title = provisional })
	}
	id := req.Msg.GetUuid()
	if id == "" {
		id = uuid.NewString()
	}
	// Under ls.mu: the queue's copy here is in place before the agent's queue can drain and clear it.
	ls.mu.Lock()
	queued := ls.driver.Submit(id, content)
	held := false
	var q []*agentv1.UserMessage
	if queued {
		held = ls.driver.Held()
		ls.queue = append(ls.queue, &agentv1.UserMessage{Uuid: id, Content: req.Msg.GetContent(), QueuedAt: time.Now().UTC().Format(time.RFC3339Nano), Held: held})
		q = append(q, ls.queue...)
	}
	ls.mu.Unlock()
	if queued {
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_QueueChanged{QueueChanged: &agentv1.QueueChanged{Queued: q}}})
		if held {
			ls.saveHeld()
			if req.Msg.GetDelivery() == agentv1.SendUserMessageRequest_DELIVERY_NOW {
				ls.sendHeldNow()
			} else {
				ls.pokeUsageWatch()
			}
		} else if req.Msg.GetDelivery() == agentv1.SendUserMessageRequest_DELIVERY_NOW {
			ls.deliverNow()
		} else {
			// A queued message is read right after the tool calls: a command that keeps it waiting moves to the
			// background. Quick commands get to finish by themselves.
			time.AfterFunc(queuedMessageGrace, func() {
				if ls.ctx.Err() == nil && ls.sess.Agent.QueueLen() > 0 && !ls.questionPending() {
					ls.sess.ToolEnv.Shell.BackgroundRunning(tools.BackgroundQueuedMessage)
				}
			})
		}
	}
	return connect.NewResponse(&agentv1.SendUserMessageResponse{}), nil
}

func (s *Server) RemoveQueuedMessage(ctx context.Context, req *connect.Request[agentv1.RemoveQueuedMessageRequest]) (*connect.Response[agentv1.RemoveQueuedMessageResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	id := req.Msg.GetUuid()
	if !ls.sess.Agent.RemoveQueued(id) {
		return connect.NewResponse(&agentv1.RemoveQueuedMessageResponse{}), nil
	}
	ls.mu.Lock()
	for i, m := range ls.queue {
		if m.GetUuid() == id {
			ls.queue = append(ls.queue[:i], ls.queue[i+1:]...)
			break
		}
	}
	q := append([]*agentv1.UserMessage(nil), ls.queue...)
	ls.mu.Unlock()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_QueueChanged{QueueChanged: &agentv1.QueueChanged{Queued: q}}})
	ls.saveHeld()
	return connect.NewResponse(&agentv1.RemoveQueuedMessageResponse{Removed: true}), nil
}

func (s *Server) Interrupt(ctx context.Context, req *connect.Request[agentv1.InterruptRequest]) (*connect.Response[agentv1.InterruptResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	idle := !ls.driver.Busy()
	ls.driver.Interrupt()
	if idle {
		// Nothing to stop: the frontend missed the end of a turn (or never saw
		// it start); tell it the session is idle.
		ls.settleIdle()
	}
	return connect.NewResponse(&agentv1.InterruptResponse{Idle: idle}), nil
}

func (s *Server) RespondToPermission(ctx context.Context, req *connect.Request[agentv1.RespondToPermissionRequest]) (*connect.Response[agentv1.RespondToPermissionResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	m := req.Msg
	if m.GetDecision() == agentv1.RespondToPermissionRequest_DECISION_UNSPECIFIED {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("decision is required"))
	}
	ls.mu.Lock()
	p := ls.pendingPerm[m.GetRequestId()]
	delete(ls.pendingPerm, m.GetRequestId())
	ls.mu.Unlock()
	if p == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no pending permission request %s", m.GetRequestId()))
	}
	var d permissions.Decision
	switch m.GetDecision() {
	case agentv1.RespondToPermissionRequest_DECISION_ALLOW, agentv1.RespondToPermissionRequest_DECISION_ALLOW_ALWAYS:
		d.Behavior = permissions.Allow
		d.Comment = m.GetComment()
		if j := m.GetUpdatedInputJson(); j != "" {
			if !json.Valid([]byte(j)) {
				ls.mu.Lock()
				ls.pendingPerm[m.GetRequestId()] = p
				ls.mu.Unlock()
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("updated_input_json is not valid JSON"))
			}
			d.UpdatedInput = []byte(j)
		}
		if m.GetDecision() == agentv1.RespondToPermissionRequest_DECISION_ALLOW_ALWAYS {
			rules := p.suggestions
			if r := strings.TrimSpace(m.GetRule()); r != "" {
				rules = []string{r}
			}
			if err := ls.sess.Permissions.AllowAlways(rules); err != nil {
				ls.notice(agentv1.Notice_LEVEL_WARNING, "permission_rule_not_saved", err.Error())
			}
		}
	case agentv1.RespondToPermissionRequest_DECISION_DENY:
		d.Behavior = permissions.Deny
		d.Message = m.GetComment()
		// Without a comment the turn stops; with one the model continues.
		d.Interrupt = m.GetComment() == ""
	}
	d.ResolvedBy = "user"
	p.ch <- permissionAnswer{d: d, decision: m.GetDecision()}
	return connect.NewResponse(&agentv1.RespondToPermissionResponse{}), nil
}

func (s *Server) RespondToPlan(ctx context.Context, req *connect.Request[agentv1.RespondToPlanRequest]) (*connect.Response[agentv1.RespondToPlanResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	var d tools.PlanDecision
	switch req.Msg.GetDecision() {
	case agentv1.RespondToPlanRequest_DECISION_APPROVE_ACCEPT_EDITS:
		d = tools.PlanApproveAcceptEdits
	case agentv1.RespondToPlanRequest_DECISION_APPROVE_MANUAL:
		d = tools.PlanApproveManual
	case agentv1.RespondToPlanRequest_DECISION_KEEP_PLANNING:
		d = tools.PlanKeepPlanning
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("decision is required"))
	}
	ls.mu.Lock()
	ch := ls.pendingPlan[req.Msg.GetRequestId()]
	delete(ls.pendingPlan, req.Msg.GetRequestId())
	ls.mu.Unlock()
	if ch == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no pending plan %s", req.Msg.GetRequestId()))
	}
	ch <- planAnswer{resp: tools.PlanResponse{Decision: d, Feedback: req.Msg.GetFeedback()}, decision: req.Msg.GetDecision()}
	return connect.NewResponse(&agentv1.RespondToPlanResponse{}), nil
}

func (s *Server) SetModel(ctx context.Context, req *connect.Request[agentv1.SetModelRequest]) (*connect.Response[agentv1.SetModelResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	id := req.Msg.GetModel()
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("model is required"))
	}
	if _, err := s.switchModel(ctx, ls, id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.SetModelResponse{}), nil
}

// switchModel moves a session to a model the catalog knows and returns its display name.
func (s *Server) switchModel(ctx context.Context, ls *liveSession, id string) (string, error) {
	display, window, thinking := id, 0, false
	if ls.sess.Catalog != nil {
		m, ok := ls.sess.Catalog.Find(id)
		if !ok {
			// A local model detected after the session opened.
			var err error
			if m, ok, err = s.localCatalogModel(ctx, id); err != nil {
				return "", err
			}
		}
		if !ok {
			return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown model %s", id))
		}
		display, window, thinking = m.DisplayName, m.ContextWindow, m.Capabilities.Thinking
	}
	// The session keeps its frozen prompt bundle until the next compaction.
	prev := ls.sess.Agent.Model()
	ls.sess.Agent.SetModel(id, display, window)
	ls.sess.Agent.SetModelThinking(thinking)
	ls.mu.Lock()
	ls.sess.Model = id
	ls.mu.Unlock()
	ls.publishInfo()
	ls.usageOnModelChange(prev, id)
	return display, nil
}

func (s *Server) SetEffort(ctx context.Context, req *connect.Request[agentv1.SetEffortRequest]) (*connect.Response[agentv1.SetEffortResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	ls.sess.Agent.SetEffort(pbconv.FromEffort(req.Msg.GetEffort()))
	ls.publishInfo()
	return connect.NewResponse(&agentv1.SetEffortResponse{}), nil
}

func (s *Server) SetPermissionMode(ctx context.Context, req *connect.Request[agentv1.SetPermissionModeRequest]) (*connect.Response[agentv1.SetPermissionModeResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	mode, err := modeFromProto(req.Msg.GetMode())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := ls.sess.Permissions.SetMode(mode); err != nil {
		return nil, modeError(err, connect.CodeFailedPrecondition)
	}
	return connect.NewResponse(&agentv1.SetPermissionModeResponse{}), nil
}

// SetOutputStyle switches the session's output style; the text reaches the
// model with the next request (see agent.OutputStyle).
func (s *Server) SetOutputStyle(ctx context.Context, req *connect.Request[agentv1.SetOutputStyleRequest]) (*connect.Response[agentv1.SetOutputStyleResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	if err := ls.applyOutputStyle(req.Msg.GetStyle()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentv1.SetOutputStyleResponse{}), nil
}

func (ls *liveSession) applyOutputStyle(style string) error {
	deferred, err := ls.sess.SetOutputStyle(style)
	if err != nil {
		return err
	}
	if deferred {
		ls.notice(agentv1.Notice_LEVEL_INFO, "output_style_deferred",
			"The style applies now; its change to the coding instructions in the system prompt takes effect at the next compaction.")
	}
	ls.publishInfo()
	return nil
}

// ListOutputStyles returns the built-in, user and project styles and the active one.
func (s *Server) ListOutputStyles(ctx context.Context, req *connect.Request[agentv1.ListOutputStylesRequest]) (*connect.Response[agentv1.ListOutputStylesResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	resp := &agentv1.ListOutputStylesResponse{Active: ls.sess.ActiveOutputStyle()}
	for _, st := range ls.sess.OutputStyles().List() {
		resp.Styles = append(resp.Styles, &agentv1.OutputStyleInfo{Id: st.ID, Name: st.Name, Description: st.Description, Source: st.Source})
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) SuggestFiles(ctx context.Context, req *connect.Request[agentv1.SuggestFilesRequest]) (*connect.Response[agentv1.SuggestFilesResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	limit := int(req.Msg.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	files := ls.files.list(ctx, ls.sess.Dir())
	return connect.NewResponse(&agentv1.SuggestFilesResponse{Paths: FuzzyFilter(files, req.Msg.GetQuery(), limit)}), nil
}

func (s *Server) ListCheckpoints(ctx context.Context, req *connect.Request[agentv1.ListCheckpointsRequest]) (*connect.Response[agentv1.ListCheckpointsResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	resp := &agentv1.ListCheckpointsResponse{}
	for _, c := range ls.sess.Agent.Checkpoints() {
		resp.Checkpoints = append(resp.Checkpoints, checkpointToProto(c))
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) Rewind(ctx context.Context, req *connect.Request[agentv1.RewindRequest]) (*connect.Response[agentv1.RewindResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	var mode agent.RewindMode
	switch req.Msg.GetMode() {
	case agentv1.RewindRequest_MODE_CODE_AND_CONVERSATION:
		mode = agent.RewindCodeAndConversation
	case agentv1.RewindRequest_MODE_CONVERSATION:
		mode = agent.RewindConversation
	case agentv1.RewindRequest_MODE_CODE:
		mode = agent.RewindCode
	case agentv1.RewindRequest_MODE_SUMMARIZE_FROM:
		mode = agent.RewindSummarizeFrom
	case agentv1.RewindRequest_MODE_SUMMARIZE_UP_TO:
		mode = agent.RewindSummarizeUpTo
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("mode is required"))
	}
	var res agent.RewindResult
	var rerr error
	if err := ls.driver.Exclusive(func() {
		res, rerr = ls.sess.Agent.Rewind(ctx, req.Msg.GetCheckpointId(), mode, req.Msg.GetSummaryInstructions())
	}); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if rerr != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, rerr)
	}
	return connect.NewResponse(&agentv1.RewindResponse{Result: rewoundToProto(res)}), nil
}

func (s *Server) Compact(ctx context.Context, req *connect.Request[agentv1.CompactRequest]) (*connect.Response[agentv1.CompactResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	info, err := ls.runCompaction(func() (agent.CompactInfo, error) { return ls.sess.Agent.Compact(ctx, req.Msg.GetInstructions()) })
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&agentv1.CompactResponse{TokensBefore: int64(info.TokensBefore), TokensAfter: int64(info.TokensAfter)}), nil
}

func (s *Server) ListTasks(ctx context.Context, req *connect.Request[agentv1.ListTasksRequest]) (*connect.Response[agentv1.ListTasksResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	resp := &agentv1.ListTasksResponse{}
	if ls.sess.Tasks != nil {
		for _, t := range ls.sess.Tasks.List() {
			resp.Tasks = append(resp.Tasks, &agentv1.BackgroundTask{TaskId: t.ID, Command: t.Command, Description: t.Description,
				OutputPath: t.OutputPath, Running: t.Running, ExitCode: int32(t.ExitCode), StartedAt: t.StartedAt.UTC().Format(time.RFC3339)})
		}
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) StopTask(ctx context.Context, req *connect.Request[agentv1.StopTaskRequest]) (*connect.Response[agentv1.StopTaskResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	if ls.sess.Tasks == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no background tasks in this session"))
	}
	if _, err := ls.sess.Tasks.Stop(req.Msg.GetTaskId()); err != nil {
		code := connect.CodeFailedPrecondition
		if _, ok := ls.sess.Tasks.Get(req.Msg.GetTaskId()); !ok {
			code = connect.CodeNotFound
		}
		return nil, connect.NewError(code, err)
	}
	return connect.NewResponse(&agentv1.StopTaskResponse{}), nil
}

func (s *Server) CloseSession(ctx context.Context, req *connect.Request[agentv1.CloseSessionRequest]) (*connect.Response[agentv1.CloseSessionResponse], error) {
	if !s.closeLive(req.Msg.GetSessionId(), "closed") {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s is not open", req.Msg.GetSessionId()))
	}
	return connect.NewResponse(&agentv1.CloseSessionResponse{}), nil
}

// closeLive ends a live session with reason (SessionEnded); false when it is not live.
func (s *Server) closeLive(id, reason string) bool {
	s.mu.Lock()
	ls := s.live[id]
	delete(s.live, id)
	s.mu.Unlock()
	if ls == nil {
		return false
	}
	// TODO(worktree): offer to remove the session's worktree (ls.sess.Worktree) here when it is clean and its
	// branch has no commits beyond base_ref; worktrees are never removed automatically today.
	ls.sess.PR.Park()
	ls.close(reason)
	s.terminals().KillSession(id)
	return true
}

func modeFromProto(m agentv1.PermissionMode) (permissions.Mode, error) {
	switch m {
	case agentv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED, agentv1.PermissionMode_PERMISSION_MODE_DEFAULT:
		return permissions.ModeDefault, nil
	case agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS:
		return permissions.ModeAcceptEdits, nil
	case agentv1.PermissionMode_PERMISSION_MODE_PLAN:
		return permissions.ModePlan, nil
	case agentv1.PermissionMode_PERMISSION_MODE_DONT_ASK:
		return permissions.ModeDontAsk, nil
	case agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS:
		return permissions.ModeBypassPermissions, nil
	case agentv1.PermissionMode_PERMISSION_MODE_AUTO:
		return permissions.ModeAuto, nil
	}
	return "", fmt.Errorf("unknown permission mode %v", m)
}

func modeToProto(m permissions.Mode) agentv1.PermissionMode {
	switch m {
	case permissions.ModeAuto:
		return agentv1.PermissionMode_PERMISSION_MODE_AUTO
	case permissions.ModeAcceptEdits:
		return agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS
	case permissions.ModePlan:
		return agentv1.PermissionMode_PERMISSION_MODE_PLAN
	case permissions.ModeDontAsk:
		return agentv1.PermissionMode_PERMISSION_MODE_DONT_ASK
	case permissions.ModeBypassPermissions:
		return agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS
	}
	return agentv1.PermissionMode_PERMISSION_MODE_DEFAULT
}

func todosToProto(ts []tools.Todo) []*agentv1.Todo {
	out := make([]*agentv1.Todo, 0, len(ts))
	for _, t := range ts {
		st := agentv1.Todo_STATUS_PENDING
		switch t.Status {
		case "in_progress":
			st = agentv1.Todo_STATUS_IN_PROGRESS
		case "completed":
			st = agentv1.Todo_STATUS_COMPLETED
		}
		out = append(out, &agentv1.Todo{Content: t.Content, ActiveForm: t.ActiveForm, Status: st, AgentIds: t.AgentIDs, TaskIds: t.TaskIDs})
	}
	return out
}

func checkpointToProto(c agent.Checkpoint) *agentv1.Checkpoint {
	return &agentv1.Checkpoint{Id: c.ID, UserMessageUuid: c.UserMessageUUID, PromptPreview: strings.ToValidUTF8(c.PromptPreview, ""),
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339), HasCodeChanges: c.HasCodeChanges}
}

func rewoundToProto(r agent.RewindResult) *agentv1.Rewound {
	modes := map[agent.RewindMode]agentv1.RewindRequest_Mode{
		agent.RewindCodeAndConversation: agentv1.RewindRequest_MODE_CODE_AND_CONVERSATION,
		agent.RewindConversation:        agentv1.RewindRequest_MODE_CONVERSATION,
		agent.RewindCode:                agentv1.RewindRequest_MODE_CODE,
		agent.RewindSummarizeFrom:       agentv1.RewindRequest_MODE_SUMMARIZE_FROM,
		agent.RewindSummarizeUpTo:       agentv1.RewindRequest_MODE_SUMMARIZE_UP_TO,
	}
	return &agentv1.Rewound{CheckpointId: r.CheckpointID, Mode: modes[r.Mode], FilesRestored: int32(r.FilesRestored),
		FilesSkipped: int32(r.FilesSkipped), RestoredPrompt: r.RestoredPrompt}
}

// newUUID returns a request id with a prefix.
func newUUID(prefix string) string { return prefix + uuid.NewString() }
