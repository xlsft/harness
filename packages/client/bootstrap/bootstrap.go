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
	"github.com/xlsft/harness/packages/client/browser"
	"github.com/xlsft/harness/packages/client/computeruse"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/decision"
	"github.com/xlsft/harness/packages/client/gateway"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/hooks"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/localmodels"
	"github.com/xlsft/harness/packages/client/lsp"
	"github.com/xlsft/harness/packages/client/mcp"
	"github.com/xlsft/harness/packages/client/memory"
	"github.com/xlsft/harness/packages/client/outputstyle"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/preview"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/sandbox"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/skills"
	"github.com/xlsft/harness/packages/client/subagent"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/terminal"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/vcs"
	"github.com/xlsft/harness/packages/client/web"
	"github.com/xlsft/harness/packages/client/workflow"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Options configure a session.
type Options struct {
	Cwd            string
	AdditionalDirs []string
	// Env holds NAME=value entries added to the environment of the processes tools spawn (the session
	// environment's variables, AgentService environments).
	Env []string

	// Server overrides; when Gateway/Prompts are nil the HTTPS adapters are
	// created from stored credentials (or HARNESS_SERVER_URL + HARNESS_ACCESS_TOKEN).
	ServerURL string
	Gateway   gateway.Gateway
	Prompts   prompts.Source
	HTTP      *http.Client

	Model  string
	Effort string
	// StartingEffort starts the session on the model's starting level (llm.Model.StartingEffort) when neither Effort nor
	// the effortLevel setting says one. Without it such a session sends no effort.
	StartingEffort bool
	MaxTurns       int
	// MaxBudgetUSD stops the run once it has spent this much, subagents included (agent.Config.MaxBudgetUSD).
	MaxBudgetUSD float64
	// SystemPrompt replaces the bundle's system prompt for the main agent; AppendSystemPrompt goes after it.
	SystemPrompt       string
	AppendSystemPrompt string
	// OutputStyle selects the output style (--output-style): a built-in id or
	// a user/project style name; "default" clears it. Empty keeps the style
	// recorded in a resumed transcript, else the outputStyle setting.
	OutputStyle string

	// Permissions. AllowedTools/DisallowedTools/AskTools are CLI-level rules
	// (source "cliArg"); PermissionSources carries the settings hierarchy
	// (policy, local, project, user) filled by the settings loader.
	PermissionMode permissions.Mode
	// FallbackPermissionMode applies when neither PermissionMode nor settings
	// (permissions.defaultMode) choose a mode. Interactive frontends set auto;
	// empty keeps default (headless runs and the SDK, as in Claude Code).
	FallbackPermissionMode permissions.Mode
	AllowedTools           []string
	DisallowedTools        []string
	// Agents are subagent types for this run only (--agents); they win over definitions of the same name.
	Agents                   []subagent.Type
	AskTools                 []string
	PermissionSources        []permissions.Source
	DisableBypassPermissions bool
	// DisableAutoMode refuses the auto permission mode (also set by the
	// disableAutoMode setting).
	DisableAutoMode bool
	// ModePolicy is the client-wide live permission mode policy of the server
	// (WatchModePolicy); it replaces the policy's permissions.disabledModes
	// read from this session's settings. Nil: those settings apply. An
	// explicitly requested PermissionMode it disables fails Open.
	ModePolicy *permissions.ModePolicy
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
	// NoFolder says Cwd is the private working directory of a session started without a folder
	// (config.ChatDir): it is trusted, belongs to no repository (git does not look above config.ChatsDir) and
	// instruction files of its ancestors are not read.
	NoFolder bool

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
	// Preview, when set, gives the session the Preview* tools: dev servers the client runs, across the session's
	// close and resume.
	Preview *preview.Manager
	// Browser, when set, gives the session a browser (the Browser* tools; HARNESS_NO_BROWSER=1 leaves them out).
	// The session's browser stops when the session closes.
	Browser *browser.Manager
	// VCSProviders are the pull request tracker's providers: the server's connections and gh and glab when the adapters are
	// created here (serve passes them with its shared adapters). VCSSchedule overrides the poll intervals (tests).
	VCSProviders []vcs.Provider
	VCSSchedule  vcs.Schedule
	// ComputerUse, when set, gives the session the Computer* tools (HARNESS_NO_COMPUTER_USE=1 leaves them out). They are
	// only offered while the device-local switch is on. The session's grants end when the session closes.
	ComputerUse *computeruse.Manager
	// Terminals, when set, gives the agent the tabs of the Terminal pane (the Terminal* tools).
	Terminals *terminal.Manager
	// SkillDocs replaces the sync cache as the source of the skills and commands stored on the server (tests).
	SkillDocs skills.DocSource
	// SkillHome replaces the data directory that holds personal skills and commands (tests).
	SkillHome string

	// UsageMonitor follows the usage limits of the session provider (agent.Config.UsageMonitor; may be nil).
	UsageMonitor agent.UsageMonitor

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
	// Subagents runs the agents the model starts with the Agent tool; SpawnSubagent is its entry point for code.
	Subagents *subagent.Manager
	// Workflows runs the scripts of the Workflow tool.
	Workflows *workflow.Manager
	// LSP runs the session's language servers (the LSP tool and the diagnostics after edits).
	LSP *lsp.Manager
	// MCP holds the session's MCP servers and tools (nil when MCP is off).
	MCP *mcp.Manager
	// Sync and Uploader are nil when sync is disabled or the server is unreachable at start.
	Sync     *syncclient.Syncer
	Uploader *syncclient.Uploader
	// VCS polls PR/CI state through gh/glab and uploads it (nil without sync or HARNESS_NO_VCS=1).
	// Poke it after a push or PR creation for an immediate poll.
	VCS *vcs.Watcher
	// PR is the session's bound pull request (never nil).
	PR *vcs.Tracker
	// Warnings are problems found while opening (e.g. an unknown output
	// style); frontends show them as notices.
	Warnings  []string
	stopSync  context.CancelFunc
	stopVCS   context.CancelFunc
	styleOpts outputstyle.Options
	browsers  *browser.Manager

	// Moving the running session (move.go): open is what it was opened with (fixedSources: with its own permission
	// sources, which then stay); moveMu orders moves; placeMu guards Cwd, Worktree, allowedDirs, Trusted and hooks once
	// it runs. instructions is the text of the project instructions the model has.
	open         Options
	fixedSources bool
	log          *slog.Logger
	moveMu       sync.Mutex
	placeMu      sync.RWMutex
	allowedDirs  []string
	hooks        PlaceHooks
	instructions string
	computer     *computeruse.Manager
	// Sandbox confines the commands the model runs (sandbox.go).
	Sandbox *sandbox.Session
	// Skills is the session's skills and custom commands (skills.go).
	Skills *skills.Manager
	// skillForks holds the grants of skills running in subagents; promptLoader finds prompts a frozen bundle lacks.
	skillForks   skillForkGrants
	promptLoader *prompts.Loader
	// agentHookGrants holds the hooks of agent definitions, by agent id, until the agent ends.
	agentHookGrants skillForkGrants
	// Hooks runs the hooks of the settings (hooks.go; never nil once Open returns). endReason is what SessionEnd reports.
	Hooks      *hooks.Engine
	endReason  string
	hooksEnded sync.Once
}

// computerSettingsGuard is the permission engine's view of the computer use settings. A session without computer use
// (the terminal UI) still keeps the file protected: it belongs to the same machine.
func computerSettingsGuard(m *computeruse.Manager) func() (string, bool) {
	return func() (string, bool) {
		if m != nil && m.Dir != "" {
			return m.SettingsGuard()
		}
		return filepath.Join(config.Home(), "computer-use"), true
	}
}

// Close kills background tasks, closes the transcript and flushes the transcript upload.
func (s *Session) Close() error {
	if s.Workflows != nil {
		s.Workflows.Shutdown()
	}
	if s.Subagents != nil {
		s.Subagents.Shutdown()
	}
	s.endHooks()
	tools.SetNotificationListener(s.ID, nil)
	if s.Tasks != nil {
		s.Tasks.StopAll(tools.TaskSessionClosed)
	}
	s.MCP.Close()
	s.LSP.Close()
	if s.computer != nil {
		s.computer.CloseSession(s.ID)
	}
	if s.browsers != nil {
		s.browsers.CloseSession(s.ID)
	}
	var err error
	if s.Transcript != nil {
		err = s.Transcript.Close()
	}
	if s.Uploader != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.Uploader.UpdateMeta(s.ID, func(m *syncclient.SessionMeta) { m.Status = agentv1.SessionStatus_SESSION_STATUS_IDLE })
		_ = s.Uploader.Flush(ctx, s.ID)
		s.Uploader.Objects().CloseScratch(s.ID)
		s.Uploader.Objects().Flush(ctx)
		cancel()
	}
	if s.stopSync != nil {
		s.stopSync()
	}
	s.Sandbox.Close()
	if s.stopVCS != nil {
		s.stopVCS()
	}
	if s.PR != nil {
		s.PR.Stop()
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
	for _, d := range s.browserFallbackDefs() {
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
	// CI: a device token from a secret (`harness-client ci login`), kept in memory only.
	if t := os.Getenv("HARNESS_DEVICE_TOKEN"); t != "" {
		url := serverOverride
		if url == "" {
			url = os.Getenv("HARNESS_SERVER_URL")
		}
		if url == "" {
			return nil, "", nil, errors.New("HARNESS_DEVICE_TOKEN requires HARNESS_SERVER_URL")
		}
		m, err := auth.NewManager(auth.NewConnectService(url, nil), auth.NewMemoryStore(&auth.Credentials{Server: url, DeviceToken: t}))
		if err != nil {
			return nil, "", nil, err
		}
		return m, url, nil, nil
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

// FolderBaselineDir holds the snapshots of a session's working directories outside git.
func FolderBaselineDir(sessionID string) string {
	return filepath.Join(config.Home(), "folder-baselines", sessionID)
}

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
	explicitMode := o.PermissionMode

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
		// A server connection goes before the local CLIs: the server holds the token, and the session needs no gh or glab.
		if len(o.VCSProviders) == 0 {
			o.VCSProviders = vcs.WithServerProvider(vcs.SharedServerProvider(url, hc, tokens))
		}
		// Started once Open succeeds, so a failed Open leaves no poller behind.
		startVCSPoller = func() { s.VCS, s.stopVCS = startVCS(s, url, hc, tokens, log) }
		if gw == nil {
			// Local model ids go to the providers on this machine (localmodels), the rest to the server.
			gw = localmodels.Wrap(connectgw.New(url, hc, tokens))
			localmodels.StartUsageReporting(ctx, url, hc, tokens, log)
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
			if err := session.CopyDir(FolderBaselineDir(id), FolderBaselineDir(newID)); err != nil {
				log.Warn("copy folder baselines failed", "err", err)
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
	s.allowedDirs = conv.AllowedDirs
	o.AdditionalDirs = slices.DeleteFunc(mergeDirs(o.AdditionalDirs, conv.AllowedDirs), func(d string) bool { return filepath.Clean(d) == filepath.Clean(cwd) })

	// Workspace trust and the settings hierarchy (policy > CLI > local > project > user > org).
	if o.NoFolder {
		IsolateChats()
	}
	if o.Trusted != nil {
		s.Trusted = *o.Trusted
	} else if o.NoFolder {
		s.Trusted = true // created by the client for this session alone
	} else {
		s.Trusted = permissions.IsTrusted(cwd)
	}
	var docs settings.DocSource
	var memDocs memory.DocSource
	if s.Sync != nil {
		docs, memDocs = s.Sync.Cache(), s.Sync.Cache()
	}
	s.fixedSources = len(o.PermissionSources) > 0
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
	disabledModes := o.ModePolicy.Disabled()
	if o.ModePolicy == nil && st != nil {
		for _, m := range st.Permissions.DisabledModes {
			disabledModes = append(disabledModes, permissions.Mode(m))
		}
	}
	if slices.Contains(disabledModes, explicitMode) {
		return nil, &permissions.ModeDisabledError{Mode: explicitMode, Policy: true}
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
	if o.StartingEffort && o.Effort == "" {
		o.Effort = info.StartingEffort()
	}

	// Prompt bundle, frozen per session.
	loader := &prompts.Loader{Source: src, Dir: config.PromptCacheDir(), Logger: log}
	s.promptLoader = loader
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
	s.Memory = projectMemory(ctx, cwd, o.AdditionalDirs, st, memDocs, o.NoFolder)
	s.instructions = instructionText(s.Memory.Initial())
	var styleDocs outputstyle.DocSource
	if s.Sync != nil {
		styleDocs = s.Sync.Cache()
	}
	s.styleOpts = styleOptions(styleDocs, workspace.GitRoot(ctx, cwd), cwd)
	s.styleOpts.Plugins = func() []outputstyle.PluginStyle { return pluginStyles(s.Dir(), s.IsTrusted()) }
	style, styleID := s.resolveOutputStyle(o, st, conv, log)
	projectContext := memory.ProjectContext(s.Memory.Initial(), gitForContext)

	// Tools.
	scratch := filepath.Join(config.TempRoot(), session.Slug(cwd), s.ID, "scratchpad")
	plans := PlansDir(s.ID)
	s.ToolEnv = tools.NewEnv(cwd, scratch)
	s.ToolEnv.AdditionalDirs = o.AdditionalDirs
	s.ToolEnv.SetExtraEnv(o.Env)
	s.ToolEnv.SessionID = s.ID
	s.ToolEnv.PlansDir = plans
	s.ToolEnv.SetMover(mover{s})
	s.Sandbox = &sandbox.Session{Home: homeDir(), HarnessHome: config.Home(), Settings: s.sandboxSettings}
	s.ToolEnv.Sandbox = s.Sandbox
	if os.Getenv("HARNESS_DISABLE_BACKGROUND_TASKS") != "1" {
		s.Tasks = tools.NewTaskManager(filepath.Join(scratch, "tasks"))
		s.Tasks.Unattended = o.Unattended
		s.ToolEnv.Tasks = s.Tasks
	}
	s.Tools = tools.Default()
	tools.RegisterSessionTools(s.Tools)
	s.Tools.Register(&tools.Memory{Store: s.autoMemoryStore, Enabled: s.autoMemoryEnabled})
	if s.Sync != nil {
		s.ToolEnv.Library = s.Sync.Client()
		tools.RegisterLibraryTools(s.Tools)
	}
	s.syncAutoMemory()
	tools.RegisterWakeupTools(s.Tools)
	tools.RegisterGraphTools(s.Tools)
	s.ToolEnv.TaskGraph = tools.NewTaskGraph(graphPath(path, o.NoPersistence))
	s.openSkills(o)
	s.Warnings = append(s.Warnings, pluginWarnings(cwd, s.Trusted)...)
	s.registerWebTools(o)
	s.LSP = lsp.New(lsp.Options{Config: s.lspConfig, Trusted: s.lspTrusted, ReadDenied: func(p string) bool { return s.ToolEnv.ReadDenied != nil && s.ToolEnv.ReadDenied(p) }, Logger: log})
	s.Tools.Register(s.LSP.Tool())
	s.ToolEnv.Edits = s.LSP
	s.registerPRTools(o, log)
	if o.Preview != nil {
		for _, t := range o.Preview.Tools() {
			s.Tools.Register(t)
		}
	}
	useBrowser := o.Browser != nil && os.Getenv("HARNESS_NO_BROWSER") != "1"
	if useBrowser {
		for _, t := range o.Browser.Tools() {
			s.Tools.Register(t)
		}
	}
	if o.Terminals != nil {
		for _, t := range o.Terminals.Tools() {
			s.Tools.Register(t)
		}
	}

	if o.ComputerUse != nil && os.Getenv("HARNESS_NO_COMPUTER_USE") != "1" {
		for _, t := range o.ComputerUse.Tools() {
			s.Tools.Register(t)
		}
		s.computer = o.ComputerUse
	}

	// Permissions.
	trusted := s.Trusted
	chosenMode := o.PermissionMode // by the request or settings; the fallback may silently fall back further
	if o.PermissionMode == "" {
		o.PermissionMode = o.FallbackPermissionMode
	}
	if o.PermissionMode == permissions.ModeAuto && o.DisableAutoMode {
		log.Warn("auto permission mode is disabled by settings (disableAutoMode); using default")
	}
	s.Decider = newDecider(ctx, o, s.User, log)
	// Rules about this device's own MCP servers stay on this device, so they are not in the synced settings.
	mcpAllow, mcpAsk, mcpDeny := (&mcp.LocalStore{Path: MCPLocalFile()}).Rules()
	s.Permissions = permissions.New(permissions.Config{
		Cwd: cwd, AdditionalDirectories: o.AdditionalDirs, DefaultMode: o.PermissionMode,
		Allow: append(slices.Clip(o.AllowedTools), mcpAllow...), Ask: append(slices.Clip(o.AskTools), mcpAsk...), Deny: append(slices.Clip(o.DisallowedTools), mcpDeny...), Sources: o.PermissionSources,
		Trusted: trusted, DisableBypassPermissions: o.DisableBypassPermissions, DisableAutoMode: o.DisableAutoMode,
		DisabledModes: disabledModes, Decider: s.Decider, LocalSettingsPath: o.LocalSettingsPath,
		ComputerSettings: computerSettingsGuard(o.ComputerUse), Sandbox: s.sandboxState,
		ScratchDirs: []string{scratch}, ReadableDirs: []string{plans},
	})
	if useBrowser {
		if b, err := o.Browser.Session(s.ID); err == nil {
			b.SetSites(s.Permissions.AllowedSites)
			s.browsers = o.Browser
		}
	}
	if chosenMode != "" && s.Permissions.Mode() != chosenMode {
		msg := fmt.Sprintf("permission mode %q is disabled; the session starts in %q mode", chosenMode, s.Permissions.Mode())
		log.Warn(msg)
		s.Warnings = append(s.Warnings, msg)
	}
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
	if s.Uploader != nil {
		// A nil queue must not become a non-nil interface.
		if q := s.Uploader.Objects(); q != nil {
			s.ToolEnv.Objects = q
			_ = q.TrackScratch(s.ID, s.ToolEnv.ScratchDir)
		}
	}
	onEvent := o.OnEvent
	if s.Uploader != nil {
		up, id := s.Uploader, s.ID
		onEvent = func(e agent.Event) {
			if o.OnEvent != nil {
				o.OnEvent(e)
			}
			if e.Kind == agent.EventAssistant || e.Kind == agent.EventCompacted || e.Kind == agent.EventRewound || e.Kind == agent.EventSubagent {
				up.Notify(id)
			}
		}
	}
	onEvent = s.watchPRCommands(onEvent)
	onEvent = s.watchSkillForks(onEvent)
	s.FileHistory = session.NewFileHistory(FileHistoryDir(s.ID), s.Transcript, conv.Snapshots)
	s.ToolEnv.BeforeEdit = s.FileHistory.TrackEdit

	todos := tools.RestoreTodos(conv.Todos, conv.TodoLinks)

	maxTokens := 32000
	if info.MaxOutputTokens > 0 && info.MaxOutputTokens < maxTokens {
		maxTokens = info.MaxOutputTokens
	}
	model := s.Model
	s.log = log
	s.Hooks = s.newHookEngine(st)
	s.Permissions.SetPromptHook(s.permissionPromptHook)
	s.Agent = agent.New(agent.Config{
		Gateway: gw, Bundle: s.Bundle, Tools: s.Tools, ToolEnv: s.ToolEnv,
		Permissions: s.Permissions, Modes: s.Permissions, Transcript: s.Transcript, FileHistory: s.FileHistory,
		Model: s.Model, ModelDisplayName: info.DisplayName, MaxTokens: maxTokens, ContextWindow: info.ContextWindow,
		ThinkingCapable: info.Capabilities.Thinking,
		AutoCompactPct:  o.AutoCompactPct,
		RefreshBundle:   func(ctx context.Context) (*prompts.Bundle, error) { return loader.Get(ctx, model) },
		MaxTurns:        o.MaxTurns, Effort: o.Effort, SessionID: s.ID,
		MaxBudgetUSD: o.MaxBudgetUSD, OtherSpendUSD: s.subagentSpend,
		SystemPrompt: o.SystemPrompt, AppendSystemPrompt: o.AppendSystemPrompt,
		Vars: env.Vars(), ProjectContext: projectContext, MCPInstructions: s.mcpInstructions(), History: conv, Todos: todos,
		DisabledTools: disabled, OutputStyle: style, ExtraTools: s.extraTools(useBrowser),
		DisableInterruptReply: (st != nil && !st.InterruptReply) || os.Getenv("HARNESS_NO_INTERRUPT_REPLY") == "1",
		UsageMonitor:          o.UsageMonitor, Skills: s.skillSource(), Hooks: s.Hooks,
		OnEvent: onEvent, Logger: log,
	})
	tools.SetNotificationListener(s.ID, s.Agent.OnNotification)
	s.ToolEnv.RestoreFindings(conv.Findings)
	s.ToolEnv.ReadDenied = func(p string) bool {
		return s.Permissions.Evaluate(permissions.Request{ToolName: "Read", FilePath: p, ReadOnly: true}).Behavior == permissions.Deny
	}
	s.Permissions.OnModeChange(func(m permissions.Mode) {
		s.Agent.Emit(agent.Event{Kind: agent.EventModeChanged, Mode: string(m)})
	})
	s.ToolEnv.Reminders = func(k string) string { return s.Agent.Bundle().Reminder(k, "") }
	s.Subagents = subagent.New(subagent.Config{
		SessionID: s.ID, Dir: filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents"), NoPersistence: o.NoPersistence,
		LeadTranscript: s.Transcript, Limits: s.subagentLimits,
		Cwd: cwd, GitBranch: branch, Version: config.Version, Entrypoint: o.Entrypoint,
		Gateway: gw, Catalog: catalog,
		Bundle: func(ctx context.Context, m string) (*prompts.Bundle, error) {
			if m == s.Agent.Model() {
				return s.Agent.Bundle(), nil
			}
			return loader.Get(ctx, m)
		},
		LeadEnv: s.ToolEnv, Tools: s.Tools, DisabledTools: disabled, ExtraTools: s.extraTools(useBrowser),
		Permissions: s.Permissions, Mode: func() string { return string(s.Permissions.Mode()) }, AddWorkDir: s.Permissions.AddWorkDir,
		Vars: env.Vars(), ProjectContext: projectContext, MCPInstructions: s.mcpInstructions(), AutoCompactPct: o.AutoCompactPct, UsageMonitor: o.UsageMonitor, Skills: s.skillSource(), Hooks: s.Hooks,
		CustomTypes: append(append(append([]subagent.Type(nil), o.Agents...), subagent.LoadDefinitionsOver(s.syncedAgents(), subagent.DefinitionDirs(config.Home(), cwd, s.Trusted))...), pluginAgents(cwd, s.Trusted)...),
		AgentHooks:  s.agentHooks, Logger: log,
		ModeAllowed: s.Permissions.ModeAllowed, AgentMCP: s.agentMCP(o, log), SkillText: s.agentSkillText,
	})
	s.Subagents.Attach(s.Agent)
	s.ToolEnv.Agents = s.Subagents
	s.ToolEnv.TaskGraph.SetOnChange(func(list []tools.GraphTask) {
		s.Agent.Emit(agent.Event{Kind: agent.EventTaskGraph, TaskGraph: list})
		s.Subagents.KickTeams()
	})
	s.Workflows = workflow.New(workflow.Config{
		Dir: filepath.Join(strings.TrimSuffix(path, ".jsonl"), "workflows"), NoPersistence: o.NoPersistence,
		LeadTranscript: s.Transcript, Agents: s.Subagents, Env: s.ToolEnv, MaxAgents: s.workflowMaxAgents,
		SearchDirs: func() []string { return workflow.SearchDirs(config.Home(), s.Dir(), s.Trusted) },
		Logger:     log,
	})
	s.Workflows.Attach(s.Agent)
	s.ToolEnv.Workflows = s.Workflows
	if s.Tasks != nil {
		s.Tasks.OnEvent = s.Agent.OnTaskEvent
	}
	if startVCSPoller != nil {
		startVCSPoller()
	}
	s.ConnectPR(nil)
	s.startMCP(conv)
	s.open, s.log = o, log
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

// syncedAgents are the agent definitions of the user's synced documents.
func (s *Session) syncedAgents() []subagent.Document {
	if s.Sync == nil {
		return nil
	}
	var out []subagent.Document
	for _, d := range s.Sync.Cache().List(syncv1.DocumentKind_DOCUMENT_KIND_AGENT) {
		if d.GetScope() == commonv1.Scope_SCOPE_USER {
			out = append(out, subagent.Document{Key: d.GetKey(), Content: d.GetContent()})
		}
	}
	return out
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
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
