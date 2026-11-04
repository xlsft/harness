package acp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sdk "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	"github.com/xlsft/harness/packages/client/acp/registry"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	"github.com/xlsft/harness/packages/client/usagelimit"
)

// Sink receives a session's events.
type Sink interface {
	// Publish emits a durable event (stored, replayed).
	Publish(ev *agentv1.SessionEvent)
	// PublishEphemeral emits an AssistantDelta (never stored).
	PublishEphemeral(ev *agentv1.SessionEvent)
	// Changed says State() changed (status, modes, commands, sign-in, context, title).
	Changed()
	// FilesChanged says files in the working directories may have changed (a write, or a finished turn).
	FilesChanged()
}

// Config starts a session engine.
type Config struct {
	AgentID, AgentName, AgentVersion string
	Launch                           registry.Launch
	// Cwd is the session's folder; AdditionalDirs its other working directories.
	Cwd            string
	AdditionalDirs []string
	// Env holds the session's environment variables (KEY=VALUE), on top of the client's.
	Env []string
	// ResumeAgentSessionID is the agent's session to restore (session/load) when the agent supports it.
	ResumeAgentSessionID string
	// Model is shown as the model of turns and messages; the agent name when empty.
	Model string
	// InitialModel and InitialMode are the agent's model and mode to start on; the agent's defaults when empty.
	// One the agent no longer offers leaves its default in place, with a notice.
	InitialModel, InitialMode string
	Sink                      Sink
	Logger                    *slog.Logger
	// OnAuth records what was learned about the agent's sign-in (device-local state).
	OnAuth func(state registry.AuthState, methods []registry.AuthMethod)
	// OnLimits records a subscription-limit snapshot the agent reported (device-local state).
	OnLimits func(l *registry.Limits)
	// OnAgentSession records the agent's session id once it is known (for a later session/load).
	OnAgentSession func(id string)
	// Now overrides time.Now (tests).
	Now func() time.Time
}

// Mode is one of the agent's session modes.
type Mode = registry.Mode

// Command is a slash command the agent advertises.
type Command struct{ Name, Description, InputHint string }

// Model is one of the agent's model choices.
type Model = registry.Model

// State is a snapshot of the engine.
type State struct {
	Status         agentv1.SessionStatus
	Starting       bool
	NeedsAuth      bool
	AuthMethods    []registry.AuthMethod
	StartError     string
	Modes          []Mode
	CurrentMode    string
	Commands       []Command
	Models         []Model
	CurrentModel   string
	ContextUsed    int64
	ContextSize    int64
	AgentSessionID string
	LoadSupported  bool
	Fresh          bool
	Title          string
	Queue          []*agentv1.UserMessage
	Limits         *registry.Limits
	// UsageLimit is the latest usage-limit state published (nil before any).
	UsageLimit *agentv1.UsageLimit
}

// ErrAuthRequired is returned while the agent waits for the user to sign in.
var ErrAuthRequired = errors.New("the agent needs you to sign in first")

// ErrNotReady is returned for commands that need a running agent session.
var ErrNotReady = errors.New("the agent session is not running")

type queued struct {
	// waited: it was published in QueueChanged (it did not start a turn right away).
	waited   bool
	uuid     string
	content  []*llmv1.ContentBlock
	queuedAt time.Time
}

type pendingPerm struct {
	ch      chan sdk.RequestPermissionOutcome
	options []sdk.PermissionOption
	toolID  string
}

type writeRecord struct {
	original, current string
	existed           bool
}

type block struct {
	thinking bool
	text     strings.Builder
}

// Session is one harness session running on an ACP agent.
type Session struct {
	cfg Config
	log *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	terms  terminals

	// Events are built under mu into outbox and delivered in order by one caller of unlock at a time (delivering).
	delivering bool

	mu        sync.Mutex
	outbox    []func()
	proc      *process
	caps      sdk.AgentCapabilities
	sessionID sdk.SessionId
	ready     bool
	starting  bool
	loading   bool
	needsAuth bool
	authM     []registry.AuthMethod
	startErr  string
	fresh     bool
	modes     []Mode
	curMode   string
	commands  []Command
	models    []Model
	curModel  string
	modelOpt  string
	// wantModel and wantMode are the choices to apply once the agent session is open.
	wantModel  string
	wantMode   string
	ctxUsed    int64
	ctxSize    int64
	title      string
	queue      []queued
	busy       bool
	cancelled  bool
	turnStart  time.Time
	msgID      string
	msgSeq     int
	blocks     []*block
	tools      map[string]*toolState
	toolOrder  []string
	pendingTU  []string // tool ids to append to the current message as tool_use blocks
	perms      map[string]*pendingPerm
	writes     map[string]writeRecord
	closed     bool
	lastUsage  *sdk.Usage
	turnTokens int64
	limits     *registry.Limits
	// usage publishes usage-limit state changes (usage.go); usageLimit is the latest one published.
	usage      usagelimit.Tracker
	usageLimit *agentv1.UsageLimit
}

// New creates the engine; Open starts the agent. Messages submitted before it runs wait in the queue.
func New(cfg Config) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{cfg: cfg, log: cfg.Logger, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1),
		tools: map[string]*toolState{}, perms: map[string]*pendingPerm{}, writes: map[string]writeRecord{}, starting: true,
		wantModel: cfg.InitialModel, wantMode: cfg.InitialMode}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.cfg.Model == "" {
		s.cfg.Model = cfg.AgentName
	}
	go s.loop()
	return s
}

func (s *Session) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

// ---- event delivery ----

// emit queues an event built under s.mu.
func (s *Session) emitLocked(ev *agentv1.SessionEvent, ephemeral bool) {
	sink := s.cfg.Sink
	if sink == nil {
		return
	}
	if ephemeral {
		s.outbox = append(s.outbox, func() { sink.PublishEphemeral(ev) })
	} else {
		s.outbox = append(s.outbox, func() { sink.Publish(ev) })
	}
}

func (s *Session) changedLocked() {
	if sink := s.cfg.Sink; sink != nil {
		s.outbox = append(s.outbox, sink.Changed)
	}
}

func (s *Session) filesChangedLocked() {
	if sink := s.cfg.Sink; sink != nil {
		s.outbox = append(s.outbox, sink.FilesChanged)
	}
}

// unlock releases s.mu and delivers the events built under it, in order. One caller delivers at a time, outside
// s.mu (the sink calls back into State): another caller's events are picked up by the one already delivering.
func (s *Session) unlock() {
	if s.delivering {
		s.mu.Unlock()
		return
	}
	s.delivering = true
	for len(s.outbox) > 0 {
		out := s.outbox
		s.outbox = nil
		s.mu.Unlock()
		for _, f := range out {
			f()
		}
		s.mu.Lock()
	}
	s.delivering = false
	s.mu.Unlock()
}

func noticeEvent(level agentv1.Notice_Level, kind, text string) *agentv1.SessionEvent {
	return &agentv1.SessionEvent{Event: &agentv1.SessionEvent_Notice{Notice: &agentv1.Notice{Level: level, Kind: kind, Text: text}}}
}

// Notice publishes a notice.
func (s *Session) Notice(level agentv1.Notice_Level, kind, text string) {
	s.mu.Lock()
	s.emitLocked(noticeEvent(level, kind, text), false)
	s.unlock()
}

// ---- state ----

// State returns a snapshot.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := State{Starting: s.starting, NeedsAuth: s.needsAuth, AuthMethods: s.authM, StartError: s.startErr,
		Modes: s.modes, CurrentMode: s.curMode, Commands: s.commands, Models: s.models, CurrentModel: s.curModel,
		ContextUsed: s.ctxUsed, ContextSize: s.ctxSize, AgentSessionID: string(s.sessionID), LoadSupported: s.caps.LoadSession,
		Fresh: s.fresh, Title: s.title, Limits: s.limits, UsageLimit: s.usageLimit}
	if s.wantModel != "" {
		st.CurrentModel = s.wantModel
	}
	if s.wantMode != "" {
		st.CurrentMode = s.wantMode
	}
	for _, q := range s.queue {
		st.Queue = append(st.Queue, &agentv1.UserMessage{Uuid: q.uuid, Content: q.content, QueuedAt: q.queuedAt.UTC().Format(time.RFC3339Nano)})
	}
	switch {
	case len(s.perms) > 0:
		st.Status = agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT
	case s.busy:
		st.Status = agentv1.SessionStatus_SESSION_STATUS_WORKING
	case s.needsAuth:
		st.Status = agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT
	case s.starting:
		st.Status = agentv1.SessionStatus_SESSION_STATUS_WAITING_FOR_CONNECTION
	case s.startErr != "":
		st.Status = agentv1.SessionStatus_SESSION_STATUS_ERROR
	default:
		st.Status = agentv1.SessionStatus_SESSION_STATUS_IDLE
	}
	return st
}

// Busy reports whether a turn runs.
func (s *Session) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

// ---- lifecycle ----

// Open spawns the agent, initializes it and opens (or restores) its session. With ErrAuthRequired the agent
// keeps running so Authenticate can use it.
func (s *Session) Open(ctx context.Context) error {
	s.mu.Lock()
	s.starting, s.startErr = true, ""
	s.changedLocked()
	s.unlock()
	err := s.start(ctx)
	if err == nil {
		err = s.openSession(ctx)
	}
	s.finishOpen(err)
	return err
}

func (s *Session) finishOpen(err error) {
	s.mu.Lock()
	s.starting = false
	switch {
	case err == nil:
	case errors.Is(err, ErrAuthRequired):
		s.emitLocked(noticeEvent(agentv1.Notice_LEVEL_WARNING, "acp_auth_required",
			s.cfg.AgentName+" needs you to sign in before it can run. Choose a sign-in method, then send your message again."), false)
	default:
		s.startErr = err.Error()
		s.emitLocked(noticeEvent(agentv1.Notice_LEVEL_ERROR, "acp_start_failed", s.cfg.AgentName+" could not start: "+err.Error()), false)
	}
	s.changedLocked()
	s.unlock()
	s.poke()
}

// start spawns the agent and runs initialize (stopping a previous process first).
func (s *Session) start(ctx context.Context) error {
	s.mu.Lock()
	old := s.proc
	s.proc, s.ready, s.needsAuth = nil, false, false
	s.mu.Unlock()
	old.stop()
	p, err := spawn(s.cfg.Launch, s.cfg.Cwd, s.cfg.Env, &client{s: s})
	if err != nil {
		return err
	}
	p.conn.SetLogger(s.log.With("acp_agent", s.cfg.AgentID))
	ictx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	res, err := p.conn.Initialize(ictx, sdk.InitializeRequest{
		ProtocolVersion: sdk.ProtocolVersionNumber,
		ClientCapabilities: sdk.ClientCapabilities{
			Fs:       sdk.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
			Terminal: true,
			Auth:     sdk.AuthCapabilities{Terminal: true},
		},
		ClientInfo: &sdk.Implementation{Name: "harness", Version: config.Version},
	})
	if err != nil {
		err = p.failure(ictx, err)
		p.stop()
		return fmt.Errorf("initialize: %w", err)
	}
	methods := authMethods(res.AuthMethods)
	s.mu.Lock()
	s.proc, s.caps, s.authM = p, res.AgentCapabilities, methods
	s.mu.Unlock()
	go func() {
		<-p.done
		s.processExited(p)
	}()
	return nil
}

// processExited ends a turn and marks the session not ready when its agent dies unexpectedly.
func (s *Session) processExited(p *process) {
	s.mu.Lock()
	if s.proc != p || s.closed {
		s.mu.Unlock()
		return
	}
	s.ready = false
	s.startErr = p.exitError().Error()
	for id, pp := range s.perms {
		delete(s.perms, id)
		close(pp.ch)
	}
	s.emitLocked(noticeEvent(agentv1.Notice_LEVEL_ERROR, "acp_agent_exited", s.cfg.AgentName+" stopped: "+s.startErr), false)
	s.changedLocked()
	s.unlock()
}

func isAuthRequired(err error) bool {
	var re *sdk.RequestError
	return errors.As(err, &re) && re.Code == -32000
}

// openSession runs session/load (when restoring and supported) or session/new.
func (s *Session) openSession(ctx context.Context) error {
	s.mu.Lock()
	p := s.proc
	caps := s.caps
	resume := s.cfg.ResumeAgentSessionID
	if s.sessionID != "" {
		resume = string(s.sessionID)
	}
	s.mu.Unlock()
	if p == nil {
		return ErrNotReady
	}
	octx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	dirs := append([]string(nil), s.cfg.AdditionalDirs...)
	var addl []string
	if caps.SessionCapabilities.AdditionalDirectories != nil {
		addl = dirs
	}
	var err error
	if resume != "" && caps.LoadSession {
		s.mu.Lock()
		s.loading = true
		s.mu.Unlock()
		var res sdk.LoadSessionResponse
		res, err = p.conn.LoadSession(octx, sdk.LoadSessionRequest{SessionId: sdk.SessionId(resume), Cwd: s.cfg.Cwd, McpServers: []sdk.McpServer{}, AdditionalDirectories: addl})
		s.mu.Lock()
		s.loading = false
		if err == nil {
			s.sessionID = sdk.SessionId(resume)
			s.applyModesLocked(res.Modes)
			s.applyConfigLocked(res.ConfigOptions)
		}
		s.mu.Unlock()
		if err != nil && !isAuthRequired(err) {
			s.log.Warn("acp session/load failed; starting a new agent session", "agent", s.cfg.AgentID, "err", err)
			resume = "" // fall through to session/new
			err = nil
			s.mu.Lock()
			s.sessionID = ""
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	have := s.sessionID != ""
	s.mu.Unlock()
	if err == nil && !have {
		var res sdk.NewSessionResponse
		res, err = p.conn.NewSession(octx, sdk.NewSessionRequest{Cwd: s.cfg.Cwd, McpServers: []sdk.McpServer{}, AdditionalDirectories: addl})
		if err == nil {
			s.mu.Lock()
			s.sessionID = res.SessionId
			s.applyModesLocked(res.Modes)
			s.applyConfigLocked(res.ConfigOptions)
			if s.cfg.ResumeAgentSessionID != "" {
				s.fresh = true
				s.emitLocked(noticeEvent(agentv1.Notice_LEVEL_INFO, "acp_fresh_session",
					s.cfg.AgentName+" cannot restore its earlier session, so it starts a new conversation: the history above is not sent to it."), false)
			}
			s.mu.Unlock()
		}
	}
	if err != nil {
		if isAuthRequired(err) {
			s.mu.Lock()
			s.needsAuth = true
			methods := s.authM
			s.mu.Unlock()
			if s.cfg.OnAuth != nil {
				s.cfg.OnAuth(registry.AuthRequired, methods)
			}
			return ErrAuthRequired
		}
		return p.failure(octx, err)
	}
	// Before the session is ready: the first prompt must not reach the agent ahead of the chosen model.
	s.applyWanted(octx, p)
	s.mu.Lock()
	s.ready, s.needsAuth = true, false
	id := string(s.sessionID)
	methods := s.authM
	again := s.wantModel != "" || s.wantMode != ""
	s.mu.Unlock()
	if again {
		s.applyWanted(octx, p)
	}
	if s.cfg.OnAuth != nil {
		s.cfg.OnAuth(registry.AuthNotRequired, methods)
	}
	if s.cfg.OnAgentSession != nil {
		s.cfg.OnAgentSession(id)
	}
	return nil
}

// Authenticate signs the agent in with an agent-handled method and opens the session.
func (s *Session) Authenticate(ctx context.Context, methodID string) error {
	s.mu.Lock()
	p := s.proc
	s.mu.Unlock()
	if p == nil || !p.alive() {
		if err := s.start(ctx); err != nil {
			s.finishOpen(err)
			return err
		}
		s.mu.Lock()
		p = s.proc
		s.mu.Unlock()
	}
	if _, err := p.conn.Authenticate(ctx, sdk.AuthenticateRequest{MethodId: methodID}); err != nil {
		return p.failure(ctx, err)
	}
	if s.cfg.OnAuth != nil {
		s.cfg.OnAuth(registry.AuthAuthenticated, nil)
	}
	err := s.openSession(ctx)
	s.finishOpen(err)
	return err
}

// Reopen restarts the agent (e.g. after a terminal login) and opens the session again.
func (s *Session) Reopen(ctx context.Context) error {
	return s.Open(ctx)
}

// TerminalLogin returns the command of a terminal auth method: the agent's own launch with the method's args
// and env.
func (s *Session) TerminalLogin(methodID string) (registry.Launch, error) {
	s.mu.Lock()
	methods := s.authM
	s.mu.Unlock()
	return TerminalLogin(s.cfg.Launch, methods, methodID)
}

// TerminalLogin returns the command of a terminal auth method of an agent launched with l.
func TerminalLogin(l registry.Launch, methods []registry.AuthMethod, methodID string) (registry.Launch, error) {
	for _, m := range methods {
		if m.ID != methodID {
			continue
		}
		if m.Kind != "terminal" {
			return registry.Launch{}, fmt.Errorf("auth method %s is not a terminal login", methodID)
		}
		env := map[string]string{}
		for k, v := range l.Env {
			env[k] = v
		}
		for k, v := range m.Env {
			env[k] = v
		}
		return registry.Launch{Command: l.Command, Args: append(append([]string(nil), l.Args...), m.Args...), Env: env}, nil
	}
	return registry.Launch{}, fmt.Errorf("unknown auth method %s", methodID)
}

// Close stops the agent and its terminals; pending permission prompts are cancelled.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	p := s.proc
	for id, pp := range s.perms {
		delete(s.perms, id)
		close(pp.ch)
	}
	s.mu.Unlock()
	s.cancel()
	s.terms.closeAll()
	p.stop()
}

// ---- modes, models ----

func (s *Session) applyModesLocked(m *sdk.SessionModeState) {
	if m == nil {
		return
	}
	s.modes = nil // State() snapshots share the old list
	for _, x := range m.AvailableModes {
		d := ""
		if x.Description != nil {
			d = *x.Description
		}
		s.modes = append(s.modes, Mode{ID: string(x.Id), Name: x.Name, Description: d})
	}
	s.curMode = string(m.CurrentModeId)
}

// applyConfigLocked picks the model option out of the agent's config options.
func (s *Session) applyConfigLocked(opts []sdk.SessionConfigOption) {
	for _, o := range opts {
		sel := o.Select
		if sel == nil || sel.Category == nil || *sel.Category != sdk.SessionConfigOptionCategoryModel {
			continue
		}
		s.modelOpt, s.curModel, s.models = string(sel.Id), string(sel.CurrentValue), nil
		add := func(o sdk.SessionConfigSelectOption) {
			d := ""
			if o.Description != nil {
				d = *o.Description
			}
			s.models = append(s.models, Model{ID: string(o.Value), Name: o.Name, Description: d})
		}
		if u := sel.Options.Ungrouped; u != nil {
			for _, o := range *u {
				add(o)
			}
		}
		if g := sel.Options.Grouped; g != nil {
			for _, grp := range *g {
				for _, o := range grp.Options {
					add(o)
				}
			}
		}
		return
	}
}

// waitingToOpenLocked reports whether a choice can still wait for the agent session: it starts or waits for a sign-in.
func (s *Session) waitingToOpenLocked() bool {
	return !s.ready && !s.closed && (s.starting || s.needsAuth)
}

// applyWanted applies the model and mode chosen before the agent session opened. What cannot be applied becomes a
// notice: the session runs on the agent's default rather than not at all.
func (s *Session) applyWanted(ctx context.Context, p *process) {
	s.mu.Lock()
	model, mode := s.wantModel, s.wantMode
	s.wantModel, s.wantMode = "", ""
	cur, curMode, opt := s.curModel, s.curMode, s.modelOpt
	offered := hasModel(s.models, model)
	offeredMode := false
	for _, m := range s.modes {
		offeredMode = offeredMode || m.ID == mode
	}
	s.mu.Unlock()
	name := s.cfg.AgentName
	if model != "" && model != cur && !(opt == "" && model == DefaultModelID) {
		switch {
		case !offered:
			s.Notice(agentv1.Notice_LEVEL_WARNING, "acp_model_unavailable", fmt.Sprintf("%s does not offer the model %s any more; the session uses its default.", name, model))
		default:
			if err := s.setModelOption(ctx, p, model); err != nil {
				s.Notice(agentv1.Notice_LEVEL_WARNING, "acp_model_unavailable", fmt.Sprintf("%s could not switch to the model %s: %v", name, model, err))
			}
		}
	}
	if mode != "" && mode != curMode {
		switch {
		case !offeredMode:
			s.Notice(agentv1.Notice_LEVEL_WARNING, "acp_mode_unavailable", fmt.Sprintf("%s does not offer the mode %s any more; the session uses its default.", name, mode))
		default:
			if err := s.setModeOption(ctx, p, mode); err != nil {
				s.Notice(agentv1.Notice_LEVEL_WARNING, "acp_mode_unavailable", fmt.Sprintf("%s could not switch to the mode %s: %v", name, mode, err))
			}
		}
	}
	s.mu.Lock()
	s.changedLocked()
	s.unlock()
}

func hasModel(models []Model, id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}

// setModeOption sends session/set_mode.
func (s *Session) setModeOption(ctx context.Context, p *process, modeID string) error {
	s.mu.Lock()
	id := s.sessionID
	s.mu.Unlock()
	if _, err := p.conn.SetSessionMode(ctx, sdk.SetSessionModeRequest{SessionId: id, ModeId: sdk.SessionModeId(modeID)}); err != nil {
		return p.failure(ctx, err)
	}
	s.setCurrentMode(modeID)
	return nil
}

// setModelOption sets the agent's model config option.
func (s *Session) setModelOption(ctx context.Context, p *process, modelID string) error {
	s.mu.Lock()
	id, opt := s.sessionID, s.modelOpt
	s.mu.Unlock()
	res, err := p.conn.SetSessionConfigOption(ctx, sdk.SetSessionConfigOptionRequest{ValueId: &sdk.SetSessionConfigOptionValueId{
		SessionId: id, ConfigId: sdk.SessionConfigId(opt), Value: sdk.SessionConfigValueId(modelID)}})
	if err != nil {
		return p.failure(ctx, err)
	}
	s.mu.Lock()
	s.curModel = modelID
	s.applyConfigLocked(res.ConfigOptions)
	s.changedLocked()
	s.unlock()
	return nil
}

// SetMode switches the agent's session mode. While the agent session is not open yet, the choice waits for it.
func (s *Session) SetMode(ctx context.Context, modeID string) error {
	s.mu.Lock()
	if s.waitingToOpenLocked() {
		s.wantMode = modeID
		s.changedLocked()
		s.unlock()
		return nil
	}
	p, ready := s.proc, s.ready
	known := false
	for _, m := range s.modes {
		known = known || m.ID == modeID
	}
	s.mu.Unlock()
	if !ready || p == nil {
		return ErrNotReady
	}
	if !known {
		return fmt.Errorf("%s has no mode %q", s.cfg.AgentName, modeID)
	}
	return s.setModeOption(ctx, p, modeID)
}

// SetPermissionMode switches to the agent mode matching a harness permission mode.
func (s *Session) SetPermissionMode(ctx context.Context, pm agentv1.PermissionMode) error {
	s.mu.Lock()
	id := modeFor(pm, s.modes)
	s.mu.Unlock()
	if id == "" {
		return fmt.Errorf("%s has no mode matching %s; use its own modes", s.cfg.AgentName, strings.TrimPrefix(pm.String(), "PERMISSION_MODE_"))
	}
	return s.SetMode(ctx, id)
}

func (s *Session) setCurrentMode(modeID string) {
	s.mu.Lock()
	if s.curMode != modeID {
		s.curMode = modeID
		s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_PermissionModeChanged{PermissionModeChanged: &agentv1.PermissionModeChanged{Mode: PermissionModeOf(modeID)}}}, false)
		s.changedLocked()
	}
	s.unlock()
}

// SetModel picks one of the agent's models (its model config option). While the agent session is not open yet, the
// choice waits for it.
func (s *Session) SetModel(ctx context.Context, modelID string) error {
	s.mu.Lock()
	if s.waitingToOpenLocked() {
		s.wantModel = modelID
		s.changedLocked()
		s.unlock()
		return nil
	}
	p, ready, opt := s.proc, s.ready, s.modelOpt
	known := hasModel(s.models, modelID)
	s.mu.Unlock()
	if !ready || p == nil {
		return ErrNotReady
	}
	if opt == "" {
		// The single "Default" entry of an agent without a model option: already on it.
		if modelID == DefaultModelID {
			return nil
		}
		return fmt.Errorf("%s does not offer a model choice", s.cfg.AgentName)
	}
	if !known {
		return fmt.Errorf("%s has no model %q", s.cfg.AgentName, modelID)
	}
	return s.setModelOption(ctx, p, modelID)
}

// ---- prompts ----

func (s *Session) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Submit sends a prompt, or queues it while a turn runs or the agent starts. now interrupts the running turn
// so the message starts the next one. Returns whether it was queued.
func (s *Session) Submit(id string, content []*llmv1.ContentBlock, now bool) (bool, error) {
	if id == "" {
		id = uuid.NewString()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false, ErrNotReady
	}
	if s.startErr != "" && !s.starting && !s.ready {
		err := s.startErr
		s.mu.Unlock()
		return false, fmt.Errorf("%s is not running: %s", s.cfg.AgentName, err)
	}
	if s.needsAuth && !s.starting {
		s.mu.Unlock()
		return false, ErrAuthRequired
	}
	wait := s.busy || s.starting || len(s.queue) > 0
	s.queue = append(s.queue, queued{waited: wait, uuid: id, content: content, queuedAt: s.now()})
	if wait {
		s.emitQueueLocked()
	}
	interrupt := now && s.busy
	s.unlock()
	if interrupt {
		s.Interrupt()
	}
	s.poke()
	return wait, nil
}

func (s *Session) emitQueueLocked() {
	q := make([]*agentv1.UserMessage, 0, len(s.queue))
	for _, m := range s.queue {
		q = append(q, &agentv1.UserMessage{Uuid: m.uuid, Content: m.content, QueuedAt: m.queuedAt.UTC().Format(time.RFC3339Nano)})
	}
	s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_QueueChanged{QueueChanged: &agentv1.QueueChanged{Queued: q}}}, false)
}

// RemoveQueued takes back a queued message.
func (s *Session) RemoveQueued(id string) bool {
	s.mu.Lock()
	for i, q := range s.queue {
		if q.uuid == id {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			s.emitQueueLocked()
			s.unlock()
			return true
		}
	}
	s.mu.Unlock()
	return false
}

// Interrupt cancels the running turn (session/cancel); pending permission prompts are answered "cancelled".
// Returns true when no turn was running.
func (s *Session) Interrupt() bool {
	s.mu.Lock()
	if !s.busy {
		s.mu.Unlock()
		return true
	}
	s.cancelled = true
	p, id := s.proc, s.sessionID
	for rid, pp := range s.perms {
		delete(s.perms, rid)
		close(pp.ch)
		s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_PermissionResolved{PermissionResolved: &agentv1.PermissionResolved{
			RequestId: rid, Decision: agentv1.RespondToPermissionRequest_DECISION_DENY, ResolvedBy: "interrupt"}}}, false)
	}
	s.changedLocked()
	s.unlock()
	if p != nil {
		_ = p.conn.Cancel(s.ctx, sdk.CancelNotification{SessionId: id})
	}
	return false
}

func (s *Session) loop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		}
		for {
			s.mu.Lock()
			if s.closed || !s.ready || s.busy || len(s.queue) == 0 {
				s.mu.Unlock()
				break
			}
			next := s.queue[0]
			s.queue = s.queue[1:]
			s.busy, s.cancelled = true, false
			s.mu.Unlock()
			s.runTurn(next)
		}
	}
}

func (s *Session) runTurn(q queued) {
	s.mu.Lock()
	p, id, caps := s.proc, s.sessionID, s.caps.PromptCapabilities
	s.turnStart = s.now()
	s.writes = map[string]writeRecord{}
	s.lastUsage = nil
	s.turnTokens = 0
	if q.waited {
		s.emitQueueLocked()
	}
	s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_UserMessage{UserMessage: &agentv1.UserMessage{Uuid: q.uuid, Content: q.content, ClientUuids: []string{q.uuid}}}}, false)
	s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TurnStarted{TurnStarted: &agentv1.TurnStarted{UserMessageUuid: q.uuid, Model: s.cfg.Model}}}, false)
	s.changedLocked()
	s.unlock()

	blocks, dropped := promptBlocks(q.content, caps)
	if dropped {
		s.Notice(agentv1.Notice_LEVEL_WARNING, "acp_prompt_content_dropped", s.cfg.AgentName+" does not accept some of the attached content (images or documents); it was left out.")
	}
	var res sdk.PromptResponse
	var err error
	if len(blocks) == 0 {
		err = errors.New("nothing to send: the message has no content the agent accepts")
	} else {
		res, err = p.conn.Prompt(s.ctx, sdk.PromptRequest{SessionId: id, Prompt: blocks})
		err = p.failure(s.ctx, err)
	}

	s.mu.Lock()
	cancelled := s.cancelled
	s.flushMessageLocked(res.StopReason)
	s.finishOpenToolsLocked(cancelled || err != nil)
	result, msg := turnResult(res.StopReason)
	if cancelled {
		result = agentv1.TurnFinished_RESULT_INTERRUPTED
	}
	if err != nil && !cancelled {
		result, msg = agentv1.TurnFinished_RESULT_ERROR_DURING_EXECUTION, err.Error()
		if isAuthRequired(err) {
			s.needsAuth, s.ready = true, false
			msg = ErrAuthRequired.Error()
		}
	}
	s.turnUsageLocked(err, cancelled)
	usage := usageProto(res.Usage)
	if usage == nil {
		usage = usageProto(s.lastUsage)
	}
	s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TurnFinished{TurnFinished: &agentv1.TurnFinished{
		Result: result, NumTurns: 1, Usage: usage, CostUsd: 0, DurationMs: s.now().Sub(s.turnStart).Milliseconds(), ErrorMessage: msg}}}, false)
	s.busy, s.cancelled = false, false
	s.tools, s.toolOrder, s.pendingTU = map[string]*toolState{}, nil, nil
	s.changedLocked()
	s.filesChangedLocked()
	s.unlock()
}

// ---- message assembly ----

func (s *Session) ensureMessageLocked() {
	if s.msgID == "" {
		s.msgSeq++
		s.msgID = fmt.Sprintf("acp_%s_%d", uuid.NewString()[:8], s.msgSeq)
		s.blocks = nil
	}
}

// appendChunkLocked adds a text or thinking chunk to the current message and emits its delta.
func (s *Session) appendChunkLocked(text string, thinking bool) {
	if text == "" {
		return
	}
	s.ensureMessageLocked()
	if n := len(s.blocks); n == 0 || s.blocks[n-1].thinking != thinking {
		s.blocks = append(s.blocks, &block{thinking: thinking})
	}
	idx := len(s.blocks) - 1
	s.blocks[idx].text.WriteString(text)
	d := &agentv1.AssistantDelta{MessageId: s.msgID, Index: int32(idx)}
	if thinking {
		d.Delta = &agentv1.AssistantDelta_Thinking{Thinking: text}
	} else {
		d.Delta = &agentv1.AssistantDelta_Text{Text: text}
	}
	s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_AssistantDelta{AssistantDelta: d}}, true)
}

// flushMessageLocked publishes the current assistant message with the tool_use blocks started since.
func (s *Session) flushMessageLocked(reason sdk.StopReason) {
	if s.msgID == "" && len(s.pendingTU) == 0 {
		return
	}
	s.ensureMessageLocked()
	var content []*llmv1.ContentBlock
	for _, b := range s.blocks {
		if b.thinking {
			content = append(content, &llmv1.ContentBlock{Block: &llmv1.ContentBlock_Thinking{Thinking: &llmv1.ThinkingBlock{Thinking: b.text.String()}}})
		} else {
			content = append(content, &llmv1.ContentBlock{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{Text: b.text.String()}}})
		}
	}
	stop := stopReason(reason)
	for _, id := range s.pendingTU {
		t := s.tools[id]
		if t == nil {
			continue
		}
		name, input, _ := t.view()
		content = append(content, &llmv1.ContentBlock{Block: &llmv1.ContentBlock_ToolUse{ToolUse: &llmv1.ToolUseBlock{Id: id, Name: name, InputJson: mustJSON(input)}}})
		stop = llmv1.StopReason_STOP_REASON_TOOL_USE
	}
	s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_AssistantMessage{AssistantMessage: &agentv1.AssistantMessage{
		MessageId: s.msgID, Model: s.cfg.Model, Content: content, StopReason: stop}}}, false)
	s.msgID, s.blocks, s.pendingTU = "", nil, nil
}

// startToolLocked publishes a tool call's start: the message so far goes out with its tool_use block first.
func (s *Session) startToolLocked(t *toolState) {
	if t.started {
		return
	}
	t.started = true
	t.startedAt = s.now().UnixMilli()
	s.pendingTU = append(s.pendingTU, t.id)
	s.flushMessageLocked("")
	name, input, summary := t.view()
	s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_ToolCallStarted{ToolCallStarted: &agentv1.ToolCallStarted{
		ToolUseId: t.id, Name: name, InputJson: mustJSON(input), Summary: summary}}}, false)
}

// startDeferredLocked publishes the starts held back for calls still pending without input.
func (s *Session) startDeferredLocked() {
	for _, id := range s.toolOrder {
		if t := s.tools[id]; t != nil && !t.started {
			s.startToolLocked(t)
		}
	}
}

func (s *Session) finishToolLocked(t *toolState, interrupted bool) {
	if t.finished {
		return
	}
	s.startToolLocked(t)
	t.finished = true
	f := t.result(s.terms.snapshot, s.writes, interrupted, s.now().UnixMilli()-t.startedAt)
	s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_ToolCallFinished{ToolCallFinished: f}}, false)
}

func (s *Session) finishOpenToolsLocked(interrupted bool) {
	for _, id := range s.toolOrder {
		if t := s.tools[id]; t != nil && !t.finished {
			s.finishToolLocked(t, interrupted)
		}
	}
}

func (s *Session) toolLocked(id string) *toolState {
	t := s.tools[id]
	if t == nil {
		t = &toolState{id: id}
		s.tools[id] = t
		s.toolOrder = append(s.toolOrder, id)
	}
	return t
}

// ---- agent -> client ----

// update handles a session/update notification.
func (s *Session) update(n sdk.SessionNotification) {
	s.mu.Lock()
	defer s.unlock()
	if n.SessionId != s.sessionID && s.sessionID != "" {
		return
	}
	u := n.Update
	if n.Meta != nil {
		s.limitsLocked(n.Meta)
	}
	if u.UsageUpdate != nil && u.UsageUpdate.Meta != nil {
		s.limitsLocked(u.UsageUpdate.Meta)
	}
	// While session/load replays the conversation, only state updates apply: the client shows its own record.
	if s.loading {
		switch {
		case u.AvailableCommandsUpdate != nil, u.CurrentModeUpdate != nil, u.ConfigOptionUpdate != nil, u.UsageUpdate != nil:
		default:
			return
		}
	}
	switch {
	case u.AgentMessageChunk != nil:
		s.startDeferredLocked()
		s.appendChunkLocked(blockText(u.AgentMessageChunk.Content), false)
	case u.AgentThoughtChunk != nil:
		s.startDeferredLocked()
		s.appendChunkLocked(blockText(u.AgentThoughtChunk.Content), true)
	case u.UserMessageChunk != nil:
		// Only sent while loading (handled above).
	case u.ToolCall != nil:
		t := s.toolLocked(string(u.ToolCall.ToolCallId))
		t.mergeStart(u.ToolCall)
		if t.ready() {
			s.startToolLocked(t)
		}
		if t.status == sdk.ToolCallStatusCompleted || t.status == sdk.ToolCallStatusFailed {
			s.finishToolLocked(t, false)
		}
	case u.ToolCallUpdate != nil:
		x := u.ToolCallUpdate
		t := s.toolLocked(string(x.ToolCallId))
		t.mergeUpdate(x.Title, x.Kind, x.Status, x.RawInput, x.RawOutput, x.Locations, x.Content)
		s.startToolLocked(t)
		if t.status == sdk.ToolCallStatusCompleted || t.status == sdk.ToolCallStatusFailed {
			s.finishToolLocked(t, false)
		}
	case u.Plan != nil:
		todos := make([]*agentv1.Todo, 0, len(u.Plan.Entries))
		for _, e := range u.Plan.Entries {
			todos = append(todos, &agentv1.Todo{Content: e.Content, ActiveForm: e.Content, Status: todoStatus(e.Status)})
		}
		s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TodosUpdated{TodosUpdated: &agentv1.TodosUpdated{Todos: todos}}}, false)
	case u.PlanRemoved != nil:
		s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TodosUpdated{TodosUpdated: &agentv1.TodosUpdated{}}}, false)
	case u.AvailableCommandsUpdate != nil:
		s.commands = s.commands[:0]
		for _, c := range u.AvailableCommandsUpdate.AvailableCommands {
			hint := ""
			if c.Input != nil && c.Input.Unstructured != nil {
				hint = c.Input.Unstructured.Hint
			}
			s.commands = append(s.commands, Command{Name: c.Name, Description: c.Description, InputHint: hint})
		}
		s.changedLocked()
	case u.CurrentModeUpdate != nil:
		id := string(u.CurrentModeUpdate.CurrentModeId)
		if s.curMode != id {
			s.curMode = id
			s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_PermissionModeChanged{PermissionModeChanged: &agentv1.PermissionModeChanged{Mode: PermissionModeOf(id)}}}, false)
			s.changedLocked()
		}
	case u.ConfigOptionUpdate != nil:
		s.applyConfigLocked(u.ConfigOptionUpdate.ConfigOptions)
		s.changedLocked()
	case u.UsageUpdate != nil:
		s.ctxUsed, s.ctxSize = int64(u.UsageUpdate.Used), int64(u.UsageUpdate.Size)
		s.changedLocked()
	case u.SessionInfoUpdate != nil:
		if t := u.SessionInfoUpdate.Title; t != nil && strings.TrimSpace(*t) != "" {
			s.title = strings.TrimSpace(*t)
			s.changedLocked()
		}
	}
}

// requestPermission handles session/request_permission: it becomes a harness PermissionRequest.
func (s *Session) requestPermission(ctx context.Context, p sdk.RequestPermissionRequest) (sdk.RequestPermissionResponse, error) {
	cancelled := sdk.RequestPermissionResponse{Outcome: sdk.NewRequestPermissionOutcomeCancelled()}
	s.mu.Lock()
	if s.cancelled || s.closed || !s.busy {
		s.mu.Unlock()
		return cancelled, nil
	}
	tc := p.ToolCall
	t := s.toolLocked(string(tc.ToolCallId))
	t.mergeUpdate(tc.Title, tc.Kind, tc.Status, tc.RawInput, tc.RawOutput, tc.Locations, tc.Content)
	s.startToolLocked(t)
	rid := "perm_" + uuid.NewString()
	pp := &pendingPerm{ch: make(chan sdk.RequestPermissionOutcome, 1), options: p.Options, toolID: t.id}
	s.perms[rid] = pp
	name, input, summary := t.view()
	oneTime := true
	for _, o := range p.Options {
		if o.Kind == sdk.PermissionOptionKindAllowAlways {
			oneTime = false
		}
	}
	s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_PermissionRequest{PermissionRequest: &agentv1.PermissionRequest{
		RequestId: rid, ToolUseId: t.id, ToolName: name, InputJson: mustJSON(input), Reason: summary, OneTimeOnly: oneTime}}}, false)
	s.changedLocked()
	s.unlock()

	select {
	case out, ok := <-pp.ch:
		if !ok {
			return cancelled, nil
		}
		return sdk.RequestPermissionResponse{Outcome: out}, nil
	case <-ctx.Done():
		s.mu.Lock()
		if _, still := s.perms[rid]; still {
			delete(s.perms, rid)
			s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_PermissionResolved{PermissionResolved: &agentv1.PermissionResolved{
				RequestId: rid, Decision: agentv1.RespondToPermissionRequest_DECISION_DENY, ResolvedBy: "agent"}}}, false)
			s.changedLocked()
		}
		s.unlock()
		return cancelled, nil
	}
}

// ErrNoPermission is returned for an unknown permission request.
var ErrNoPermission = errors.New("no pending permission request")

// RespondPermission answers a pending permission request with the closest option the agent offered:
// allow -> allow_once (else allow_always), allow always -> allow_always (else allow_once), deny -> reject_once
// (else reject_always).
func (s *Session) RespondPermission(requestID string, d agentv1.RespondToPermissionRequest_Decision, by string) error {
	s.mu.Lock()
	pp := s.perms[requestID]
	if pp == nil {
		s.mu.Unlock()
		return ErrNoPermission
	}
	var prefer []sdk.PermissionOptionKind
	switch d {
	case agentv1.RespondToPermissionRequest_DECISION_ALLOW:
		prefer = []sdk.PermissionOptionKind{sdk.PermissionOptionKindAllowOnce, sdk.PermissionOptionKindAllowAlways}
	case agentv1.RespondToPermissionRequest_DECISION_ALLOW_ALWAYS:
		prefer = []sdk.PermissionOptionKind{sdk.PermissionOptionKindAllowAlways, sdk.PermissionOptionKindAllowOnce}
	default:
		prefer = []sdk.PermissionOptionKind{sdk.PermissionOptionKindRejectOnce, sdk.PermissionOptionKindRejectAlways}
	}
	var chosen *sdk.PermissionOption
	for _, k := range prefer {
		for i := range pp.options {
			if pp.options[i].Kind == k {
				chosen = &pp.options[i]
				break
			}
		}
		if chosen != nil {
			break
		}
	}
	delete(s.perms, requestID)
	out := sdk.NewRequestPermissionOutcomeCancelled()
	if chosen != nil {
		out = sdk.NewRequestPermissionOutcomeSelected(chosen.OptionId)
	}
	pp.ch <- out
	if by == "" {
		by = "user"
	}
	s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_PermissionResolved{PermissionResolved: &agentv1.PermissionResolved{
		RequestId: requestID, Decision: d, ResolvedBy: by}}}, false)
	s.changedLocked()
	s.unlock()
	return nil
}

// PendingPermission reports whether requestID waits for an answer.
func (s *Session) PendingPermission(requestID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.perms[requestID] != nil
}

// readFile handles fs/read_text_file (line is 1-based; limit counts lines).
func (s *Session) readFile(p sdk.ReadTextFileRequest) (sdk.ReadTextFileResponse, error) {
	if !filepath.IsAbs(p.Path) {
		return sdk.ReadTextFileResponse{}, sdk.NewInvalidParams(map[string]any{"path": p.Path, "error": "path must be absolute"})
	}
	b, err := os.ReadFile(p.Path)
	if err != nil {
		return sdk.ReadTextFileResponse{}, sdk.NewInternalError(map[string]any{"path": p.Path, "error": err.Error()})
	}
	text := string(b)
	if (p.Line != nil && *p.Line > 1) || p.Limit != nil {
		lines := strings.SplitAfter(text, "\n")
		start := 0
		if p.Line != nil && *p.Line > 1 {
			start = min(*p.Line-1, len(lines))
		}
		end := len(lines)
		if p.Limit != nil && *p.Limit >= 0 {
			end = min(start+*p.Limit, len(lines))
		}
		text = strings.Join(lines[start:end], "")
	}
	return sdk.ReadTextFileResponse{Content: text}, nil
}

// writeFile handles fs/write_text_file: the file is written and recorded for the turn's diffs, and the
// session's file panes are told.
func (s *Session) writeFile(p sdk.WriteTextFileRequest) (sdk.WriteTextFileResponse, error) {
	if !filepath.IsAbs(p.Path) {
		return sdk.WriteTextFileResponse{}, sdk.NewInvalidParams(map[string]any{"path": p.Path, "error": "path must be absolute"})
	}
	path := filepath.Clean(p.Path)
	old, rerr := os.ReadFile(path)
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return sdk.WriteTextFileResponse{}, sdk.NewInternalError(map[string]any{"path": p.Path, "error": err.Error()})
	}
	if err := os.WriteFile(path, []byte(p.Content), mode); err != nil {
		return sdk.WriteTextFileResponse{}, sdk.NewInternalError(map[string]any{"path": p.Path, "error": err.Error()})
	}
	s.mu.Lock()
	w, seen := s.writes[path]
	if !seen {
		w = writeRecord{original: string(old), existed: rerr == nil}
	}
	w.current = p.Content
	s.writes[path] = w
	s.filesChangedLocked()
	s.unlock()
	return sdk.WriteTextFileResponse{}, nil
}

// limitsLocked records a rate-limit snapshot found in v (an agent's _meta or extension params).
func (s *Session) limitsLocked(v any) bool {
	l := registry.ParseLimits(v, s.now())
	if l == nil {
		return false
	}
	s.limits = l
	if f := s.cfg.OnLimits; f != nil {
		s.outbox = append(s.outbox, func() { f(l) })
	}
	s.observeUsageLocked(s.evaluateUsageLocked())
	s.changedLocked()
	return true
}

// extension handles an extension notification or request ("_..."): rate-limit snapshots are recorded.
func (s *Session) extension(params any) bool {
	s.mu.Lock()
	ok := s.limitsLocked(params)
	s.unlock()
	return ok
}
