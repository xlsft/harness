// Package bootstrap wires the client together for one session: credentials,
// gateway and prompt adapters, model selection, the frozen prompt bundle,
// workspace context, tools, permissions, transcript, file checkpoints,
// background tasks and the agent. Frontends (headless SDK mode, the TUI in
// packages/cli, AgentService) start here.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/decision"
	"github.com/xlsft/harness/packages/client/gateway"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/mcp"
	"github.com/xlsft/harness/packages/client/memory"
	"github.com/xlsft/harness/packages/client/outputstyle"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/vcs"
	"github.com/xlsft/harness/packages/client/web"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Options configure a session.
type Options struct {
	Cwd            string
	AdditionalDirs []string

	// Server overrides; when Gateway/Prompts are nil the HTTPS adapters are
	// created from stored credentials (or HARNESS_SERVER_URL + HARNESS_ACCESS_TOKEN).
	ServerURL string
	Gateway   gateway.Gateway
	Prompts   prompts.Source
	HTTP      *http.Client

	Model    string
	Effort   string
	MaxTurns int
	// OutputStyle selects the output style (--output-style): a built-in id or
	// a user/project style name; "default" clears it. Empty keeps the style
	// recorded in a resumed transcript, else the outputStyle setting.
	OutputStyle string

	// Permissions. AllowedTools/DisallowedTools/AskTools are CLI-level rules
	// (source "cliArg"); PermissionSources carries the settings hierarchy
	// (policy, local, project, user) filled by the settings loader.
	PermissionMode           permissions.Mode
	AllowedTools             []string
	DisallowedTools          []string
	AskTools                 []string
	PermissionSources        []permissions.Source
	DisableBypassPermissions bool
	// DisableAutoMode refuses the auto permission mode (also set by the
	// disableAutoMode setting).
	DisableAutoMode bool
	// DecisionService is the remote auto-mode decider. When nil and the
	// server adapters are created here, it is the DecisionService client of
	// the same server; otherwise auto mode runs on the local rules only.
	DecisionService permissions.Decider
	// Search backs the WebSearch tool. When nil and the server adapters are
	// created here, it is the SearchService client of the same server;
	// otherwise WebSearch reports that search is not configured.
	Search web.Searcher
	// Trusted overrides the workspace trust lookup (~/.harness/trust.json).
	Trusted *bool
	// LocalSettingsPath overrides where "allow always" rules are written.
	LocalSettingsPath string

	Continue      bool
	Resume        string
	SessionID     string
	NoPersistence bool
	Entrypoint    string
	// Fork copies the resumed session into a new one (optionally at a checkpoint).
	Fork             bool
	ForkAtCheckpoint string
	// Worktree records that Cwd is inside a git worktree created for this
	// session (AgentService CreateSession with worktree options). It is
	// written to the transcript; a resumed session reports the recorded one.
	Worktree *session.Worktree

	// Unattended applies the background task time limit (headless runs).
	Unattended bool
	// AutoCompactPct overrides the auto-compaction threshold (0 = default).
	AutoCompactPct float64
	// NoSync disables the server sync client (settings, memory and transcripts then come from local
	// files only). HARNESS_NO_SYNC=1 has the same effect.
	NoSync bool
	// MCP is the McpService client. When nil and the server adapters are
	// created here, it is the McpService client of the same server; with
	// injected adapters (tests) MCP stays off unless set. NoMCP (or
	// HARNESS_NO_MCP=1) disables MCP servers.
	MCP   *mcp.Client
	NoMCP bool

	OnEvent func(agent.Event)
	Logger  *slog.Logger
}

// Session is a ready-to-run session.
type Session struct {
	ID          string
	Cwd         string
	Model       string
	Catalog     *llm.Catalog
	Bundle      *prompts.Bundle
	Agent       *agent.Agent
	Permissions *permissions.Engine
	// Decider is the auto-mode decider (local rules, then DecisionService;
	// session cache cleared on mode changes).
	Decider     *decision.Cache
	Tools       *tools.Registry
	ToolEnv     *tools.Env
	Transcript  *session.Writer
	FileHistory *session.FileHistory
	Tasks       *tools.TaskManager
	Gateway     gateway.Gateway
	User        *auth.User
	Resumed     bool
	// ParentSessionID is set for forks.
	ParentSessionID string
	// Worktree is the git worktree the session runs in (nil when none).
	Worktree *session.Worktree
	// Trusted reports whether the workspace was trusted at open.
	Trusted bool
	// Settings is the merged settings hierarchy (nil if it could not be loaded).
	Settings *settings.Settings
	// Memory loads project instructions; ForPath returns subdirectory files lazily.
	Memory *memory.Loader
	// MCP holds the session's MCP servers and tools (nil when MCP is off).
	MCP *mcp.Manager
	// Sync and Uploader are nil when sync is disabled or the server is unreachable at start.
	Sync     *syncclient.Syncer
	Uploader *syncclient.Uploader
	// VCS polls PR/CI state through gh/glab and uploads it (nil without sync or HARNESS_NO_VCS=1).
	// Poke it after a push or PR creation for an immediate poll.
	VCS *vcs.Watcher
	// Warnings are problems found while opening (e.g. an unknown output
	// style); frontends show them as notices.
	Warnings  []string
	stopSync  context.CancelFunc
	stopVCS   context.CancelFunc
	styleOpts outputstyle.Options
}

// Close kills background tasks, closes the transcript and flushes the transcript upload.
func (s *Session) Close() error {
	if s.Tasks != nil {
		s.Tasks.StopAll(tools.TaskSessionClosed)
	}
	s.MCP.Close()
	var err error
	if s.Transcript != nil {
		err = s.Transcript.Close()
	}
	if s.Uploader != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.Uploader.UpdateMeta(s.ID, func(m *syncclient.SessionMeta) { m.Status = agentv1.SessionStatus_SESSION_STATUS_IDLE })
		_ = s.Uploader.Flush(ctx, s.ID)
		cancel()
	}
	if s.stopSync != nil {
		s.stopSync()
	}
	if s.stopVCS != nil {
		s.stopVCS()
	}
	return err
}

// ToolNames returns the tools offered to the model.
func (s *Session) ToolNames() []string {
	var out []string
	b := s.Agent.Bundle()
	for _, n := range s.Tools.Names() {
		if _, ok := b.Tools[n]; ok && !s.Permissions.IsDisabled(n) {
			out = append(out, n)
		}
	}
	for _, d := range s.MCP.ToolDefs() {
		out = append(out, d.Name)
	}
	return out
}

// Tokens resolves a token source and server URL from the environment or the
// credential store.
func Tokens(serverOverride string) (auth.TokenSource, string, *auth.User, error) {
	if t := os.Getenv("HARNESS_ACCESS_TOKEN"); t != "" {
		url := serverOverride
		if url == "" {
			url = os.Getenv("HARNESS_SERVER_URL")
		}
		if url == "" {
			return nil, "", nil, errors.New("HARNESS_ACCESS_TOKEN requires HARNESS_SERVER_URL")
		}
		return auth.StaticToken(t), url, nil, nil
	}
	store := auth.DefaultStore(config.CredentialsFile())
	creds, err := store.Load()
	if err != nil {
		return nil, "", nil, err
	}
	if creds == nil {
		return nil, "", nil, auth.ErrNotLoggedIn
	}
	url := creds.Server
	if serverOverride != "" {
		url = serverOverride
	}
	m, err := auth.NewManager(auth.NewConnectService(url, nil), store)
	if err != nil {
		return nil, "", nil, err
	}
	u := creds.User
	return m, url, &u, nil
}

// PlansDir is where ExitPlanMode writes the plans of a session.
func PlansDir(sessionID string) string { return filepath.Join(config.Home(), "plans", sessionID) }

// FileHistoryDir holds the file checkpoints of a session.
func FileHistoryDir(sessionID string) string {
	return filepath.Join(config.Home(), "file-history", sessionID)
}

// Open builds a session.
func Open(ctx context.Context, o Options) (*Session, error) {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	cwd := o.Cwd
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return nil, err
		}
	}
	cwd, _ = filepath.Abs(cwd)
	s := &Session{Cwd: cwd}

	gw, src := o.Gateway, o.Prompts
	var startVCSPoller func()
	if gw == nil || src == nil {
		tokens, url, user, err := Tokens(o.ServerURL)
		if err != nil {
			return nil, err
		}
		s.User = user
		hc := o.HTTP
		if hc == nil {
			hc = rpc.HTTPClient(url)
		}
		s.Sync, s.Uploader, s.stopSync = startSync(ctx, o, url, hc, tokens, user, log)
		// Started once Open succeeds, so a failed Open leaves no poller behind.
		startVCSPoller = func() { s.VCS, s.stopVCS = startVCS(s, url, hc, tokens, log) }
		if gw == nil {
			gw = connectgw.New(url, hc, tokens)
		}
		if src == nil {
			src = prompts.NewConnectSource(url, hc, tokens)
		}
		if o.DecisionService == nil {
			o.DecisionService = decision.NewClient(url, hc, tokens)
		}
		if o.Search == nil {
			o.Search = web.NewSearchClient(url, hc, tokens)
		}
		if o.MCP == nil {
			o.MCP = mcp.NewClient(url, hc, tokens)
		}
	}
	s.Gateway = gw

	// Session identity and history.
	store := session.Store{Root: config.ProjectsDir()}
	var entries []session.Entry
	var path string
	switch {
	case o.Resume != "" || o.Continue:
		id := o.Resume
		if id == "" {
			var err error
			if id, err = store.Latest(cwd); err != nil {
				return nil, err
			}
		}
		p, err := store.Find(cwd, id)
		if err != nil {
			return nil, err
		}
		if o.Fork {
			newID := o.SessionID
			if newID == "" {
				newID = session.NewID()
			}
			dst := store.Path(cwd, newID)
			if err := session.Fork(p, dst, id, newID, o.ForkAtCheckpoint); err != nil {
				return nil, fmt.Errorf("fork session: %w", err)
			}
			if err := session.CopyDir(FileHistoryDir(id), FileHistoryDir(newID)); err != nil {
				log.Warn("copy file history failed", "err", err)
			}
			id, p = newID, dst
		}
		entries, err = session.Load(p)
		if err != nil {
			return nil, fmt.Errorf("load session: %w", err)
		}
		s.ID, path, s.Resumed = id, p, true
	default:
		s.ID = o.SessionID
		if s.ID == "" {
			s.ID = session.NewID()
		}
		path = store.Path(cwd, s.ID)
		if _, err := os.Stat(path); err == nil {
			return nil, fmt.Errorf("session %s already exists; use --resume", s.ID)
		}
	}
	conv := session.Rebuild(entries)
	s.ParentSessionID = conv.ForkedFrom
	s.Worktree = conv.Worktree
	if o.Worktree != nil {
		w := *o.Worktree
		s.Worktree = &w
	}

	// Workspace trust and the settings hierarchy (policy > CLI > local > project > user > org).
	if o.Trusted != nil {
		s.Trusted = *o.Trusted
	} else {
		s.Trusted = permissions.IsTrusted(cwd)
	}
	var docs settings.DocSource
	var memDocs memory.DocSource
	if s.Sync != nil {
		docs, memDocs = s.Sync.Cache(), s.Sync.Cache()
	}
	st, settingsSources := loadSettings(ctx, o, cwd, s.Trusted, docs, log)
	s.Settings = st
	applySettingsEnv(st)
	if st != nil {
		if o.Model == "" {
			o.Model = st.Model
		}
		if o.Effort == "" {
			o.Effort = st.EffortLevel
		}
		in := st.PermissionInputs()
		if o.PermissionMode == "" && in.DefaultMode != "" {
			o.PermissionMode = permissions.Mode(in.DefaultMode)
		}
		o.AdditionalDirs = mergeDirs(o.AdditionalDirs, in.AdditionalDirectories)
		o.DisableBypassPermissions = o.DisableBypassPermissions || in.DisableBypass
		o.DisableAutoMode = o.DisableAutoMode || in.DisableAutoMode
		if len(o.PermissionSources) == 0 {
			o.PermissionSources = settingsSources
		}
	}

	// Model.
	catalog, err := gw.Models(ctx)
	if err != nil {
		log.Warn("model catalog unavailable", "err", err)
	}
	s.Catalog = catalog
	s.Model = o.Model
	if s.Model == "" {
		s.Model = conv.Model
	}
	if s.Model == "" && catalog != nil {
		s.Model = catalog.Slots["main"]
		if s.Model == "" && len(catalog.Models) > 0 {
			s.Model = catalog.Models[0].ID
		}
	}
	if s.Model == "" {
		return nil, errors.New("no model selected: pass --model or configure a main slot on the server")
	}
	info, _ := catalog.Find(s.Model)

	// Prompt bundle, frozen per session.
	loader := &prompts.Loader{Source: src, Dir: config.PromptCacheDir(), Logger: log}
	if s.Resumed && conv.PromptBundleVersion != "" {
		s.Bundle, err = loader.Frozen(ctx, s.Model, conv.PromptBundleVersion)
	} else {
		s.Bundle, err = loader.Get(ctx, s.Model)
	}
	if err != nil {
		return nil, err
	}

	// Workspace context.
	env := workspace.Detect(ctx, cwd)
	env.ModelID, env.ModelDisplayName = s.Model, info.DisplayName
	env.AdditionalDirs = o.AdditionalDirs
	env.ScratchpadDir = filepath.Join(config.TempRoot(), session.Slug(cwd), s.ID, "scratchpad")
	gitForContext := ""
	if !s.Bundle.UsesPlaceholder("git_status") {
		gitForContext = env.GitStatus
	}
	s.Memory = projectMemory(ctx, cwd, o.AdditionalDirs, st, memDocs)
	var styleDocs outputstyle.DocSource
	if s.Sync != nil {
		styleDocs = s.Sync.Cache()
	}
	s.styleOpts = styleOptions(styleDocs, workspace.GitRoot(ctx, cwd), cwd)
	style, styleID := s.resolveOutputStyle(o, st, conv, log)
	projectContext := memory.ProjectContext(s.Memory.Initial(), gitForContext)

	// Tools.
	scratch := filepath.Join(config.TempRoot(), session.Slug(cwd), s.ID, "scratchpad")
	plans := PlansDir(s.ID)
	s.ToolEnv = tools.NewEnv(cwd, scratch)
	s.ToolEnv.AdditionalDirs = o.AdditionalDirs
	s.ToolEnv.SessionID = s.ID
	s.ToolEnv.PlansDir = plans
	if os.Getenv("HARNESS_DISABLE_BACKGROUND_TASKS") != "1" {
		s.Tasks = tools.NewTaskManager(filepath.Join(scratch, "tasks"))
		s.Tasks.Unattended = o.Unattended
		s.ToolEnv.Tasks = s.Tasks
	}
	s.Tools = tools.Default()
	s.registerWebTools(o)

	// Permissions.
	trusted := s.Trusted
	if o.PermissionMode == permissions.ModeAuto && o.DisableAutoMode {
		log.Warn("auto permission mode is disabled by settings (disableAutoMode); using default")
	}
	s.Decider = newDecider(ctx, o, s.User, log)
	s.Permissions = permissions.New(permissions.Config{
		Cwd: cwd, AdditionalDirectories: o.AdditionalDirs, DefaultMode: o.PermissionMode,
		Allow: o.AllowedTools, Ask: o.AskTools, Deny: o.DisallowedTools, Sources: o.PermissionSources,
		Trusted: trusted, DisableBypassPermissions: o.DisableBypassPermissions, DisableAutoMode: o.DisableAutoMode,
		Decider: s.Decider, LocalSettingsPath: o.LocalSettingsPath,
		ScratchDirs: []string{scratch}, ReadableDirs: []string{plans},
	})
	s.openMCP(ctx, o, info.ContextWindow, conv, log)
	disabled := map[string]bool{}
	for _, n := range s.Tools.Names() {
		if s.Permissions.IsDisabled(n) {
			disabled[n] = true
		}
	}

	// Transcript.
	branch := ""
	if env.IsGit {
		branch = workspace.GitBranch(ctx, cwd)
	}
	s.Transcript, err = session.OpenWriter(path, session.WriterOptions{
		SessionID: s.ID, Cwd: cwd, GitBranch: branch, Version: config.Version,
		Entrypoint: o.Entrypoint, LastUUID: conv.LastUUID, Disabled: o.NoPersistence, Initial: entries,
	})
	if err != nil {
		return nil, err
	}
	if !s.Resumed || conv.PromptBundleVersion != s.Bundle.Version || conv.Model != s.Model {
		_, _ = s.Transcript.Append(session.Entry{Type: session.TypeMeta, PromptBundleVersion: s.Bundle.Version, Model: s.Model})
	}
	if recorded := conv.OutputStyle; styleID != recorded && (recorded != "" || styleID != outputstyle.Default) {
		_, _ = s.Transcript.Append(session.Entry{Type: session.TypeMeta, OutputStyle: styleID})
	}
	if o.Worktree != nil && (conv.Worktree == nil || *conv.Worktree != *o.Worktree) {
		// The cwd lets readers map the session to its folder before any message is written.
		_, _ = s.Transcript.Append(session.Entry{Type: session.TypeMeta, Cwd: cwd, Worktree: s.Worktree})
	}
	trackUpload(ctx, s.Uploader, s, path, branch)
	onEvent := o.OnEvent
	if s.Uploader != nil {
		up, id := s.Uploader, s.ID
		onEvent = func(e agent.Event) {
			if o.OnEvent != nil {
				o.OnEvent(e)
			}
			if e.Kind == agent.EventAssistant || e.Kind == agent.EventCompacted || e.Kind == agent.EventRewound {
				up.Notify(id)
			}
		}
	}
	s.FileHistory = session.NewFileHistory(FileHistoryDir(s.ID), s.Transcript, conv.Snapshots)
	s.ToolEnv.BeforeEdit = s.FileHistory.TrackEdit

	var todos []tools.Todo
	if len(conv.Todos) > 0 {
		todos, _ = tools.ParseTodos([]byte(`{"todos":` + string(conv.Todos) + `}`))
	}

	maxTokens := 32000
	if info.MaxOutputTokens > 0 && info.MaxOutputTokens < maxTokens {
		maxTokens = info.MaxOutputTokens
	}
	model := s.Model
	s.Agent = agent.New(agent.Config{
		Gateway: gw, Bundle: s.Bundle, Tools: s.Tools, ToolEnv: s.ToolEnv,
		Permissions: s.Permissions, Modes: s.Permissions, Transcript: s.Transcript, FileHistory: s.FileHistory,
		Model: s.Model, ModelDisplayName: info.DisplayName, MaxTokens: maxTokens, ContextWindow: info.ContextWindow,
		AutoCompactPct: o.AutoCompactPct,
		RefreshBundle:  func(ctx context.Context) (*prompts.Bundle, error) { return loader.Get(ctx, model) },
		MaxTurns:       o.MaxTurns, Effort: o.Effort, SessionID: s.ID,
		Vars: env.Vars(), ProjectContext: projectContext, History: conv, Todos: todos,
		DisabledTools: disabled, OutputStyle: style, ExtraTools: s.mcpExtraTools(),
		OnEvent: onEvent, Logger: log,
	})
	s.Permissions.OnModeChange(func(m permissions.Mode) {
		s.Agent.Emit(agent.Event{Kind: agent.EventModeChanged, Mode: string(m)})
	})
	s.ToolEnv.Reminders = func(k string) string { return s.Agent.Bundle().Reminder(k, "") }
	if s.Tasks != nil {
		s.Tasks.OnEvent = s.Agent.OnTaskEvent
	}
	if startVCSPoller != nil {
		startVCSPoller()
	}
	s.startMCP(conv)
	return s, nil
}

// resolveOutputStyle picks the initial output style: --output-style, else
// the style recorded in a resumed transcript, else the outputStyle setting
// (policy settings win over the flag, as for every setting). An unknown name
// logs a warning and falls back to the default.
func (s *Session) resolveOutputStyle(o Options, st *settings.Settings, conv session.Conversation, log *slog.Logger) (*agent.OutputStyle, string) {
	want := ""
	switch {
	case o.OutputStyle != "":
		want = o.OutputStyle
		if st != nil && st.OutputStyle != "" {
			want = st.OutputStyle
		}
	case s.Resumed && conv.OutputStyle != "":
		want = conv.OutputStyle
	case st != nil:
		want = st.OutputStyle
	}
	if outputstyle.IsDefault(want) {
		return nil, outputstyle.Default
	}
	o2 := s.styleOpts
	o2.Bundle = s.Bundle
	found, ok := outputstyle.Discover(o2).Lookup(want)
	if !ok {
		msg := fmt.Sprintf("unknown output style %q; using the default style", want)
		log.Warn(msg)
		s.Warnings = append(s.Warnings, msg)
		return nil, outputstyle.Default
	}
	if outputstyle.IsDefault(found.ID) {
		return nil, outputstyle.Default
	}
	return agentStyle(found), found.ID
}

func mergeDirs(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range append(append([]string{}, a...), b...) {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}
