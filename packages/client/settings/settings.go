// Package settings merges the settings hierarchy of a session:
//
//	policy (sync POLICY documents; always wins)
//	→ CLI flags
//	→ .harness/settings.local.json
//	→ .harness/settings.json
//	→ user (sync SETTINGS + PERMISSION_RULES at user scope)
//	→ org defaults (sync SETTINGS + PERMISSION_RULES at org scope)
//
// Scalars are taken from the highest level that sets them; permission rule
// lists and additionalDirectories are merged across levels, keeping the
// source of every entry. Allow rules, env and other permission-widening
// keys of the committed project files apply only in a trusted workspace;
// deny and ask rules always apply. Validation problems are collected, never
// fatal.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/memory"
)

// Level is a settings source level; a higher value has higher precedence.
type Level int

const (
	LevelOrg Level = iota + 1
	LevelUser
	LevelProject // committed .harness/settings.json
	LevelLocal   // .harness/settings.local.json
	LevelCLI
	LevelPolicy
	// LevelPlugin is the hooks of an installed plugin (package plugins). It is no settings file and holds nothing else; it
	// sits out of order on purpose, as nothing merges by it.
	LevelPlugin
)

func (l Level) String() string {
	switch l {
	case LevelOrg:
		return "org"
	case LevelUser:
		return "user"
	case LevelProject:
		return "project"
	case LevelLocal:
		return "local"
	case LevelCLI:
		return "cli"
	case LevelPolicy:
		return "policy"
	case LevelPlugin:
		return "plugin"
	}
	return "unknown"
}

// Source identifies where a value came from.
type Source struct {
	Level Level
	// Path is the settings file, empty for sync documents and CLI flags.
	Path string
	// DocID is the sync document id (doc_...), empty for files.
	DocID string
	// Dir is the base directory for relative path rules and directories:
	// the directory that contains .harness for files, the home
	// directory for user documents, the cwd for CLI flags, empty for org
	// and policy documents.
	Dir string
	// Plugin is the plugin id (name@marketplace) at LevelPlugin.
	Plugin string
}

func (s Source) String() string {
	switch {
	case s.Path != "":
		return s.Level.String() + ":" + s.Path
	case s.DocID != "":
		return s.Level.String() + ":" + s.DocID
	}
	return s.Level.String()
}

// Rule is a permission rule ("Tool(spec)") with its source.
type Rule struct {
	Rule   string
	Source Source
}

// Dir is an additional working directory with its source.
type Dir struct {
	// Path as written in the settings.
	Path string
	// Abs is Path resolved against the source directory (or the cwd).
	Abs    string
	Source Source
}

// Permissions is the merged permissions block.
type Permissions struct {
	Allow, Ask, Deny      []Rule
	DefaultMode           string
	DefaultModeSource     Source
	AdditionalDirectories []Dir
	// DisableBypass is set by policy (permissions.disableBypassPermissionsMode: "disable").
	DisableBypass bool
	// DisableAutoMode is set by permissions.disableAutoMode: "disable" (or a
	// top-level disableAutoMode) at any level.
	DisableAutoMode bool
	// DisabledModes is set by policy (permissions.disabledModes, the server's
	// permission mode policy): modes nobody may use. Never contains "default".
	DisabledModes []string
}

// Attribution is the commit / PR attribution text ("" disables it).
type Attribution struct {
	Commit *string
	PR     *string
}

// LSPServer is one entry of the lspServers setting. For a language the client knows, what is set replaces the built-in
// value; another language needs Command and Extensions.
type LSPServer struct {
	// Command is a bare name looked up on PATH or an absolute path.
	Command string   `json:"command"`
	Args    []string `json:"args"`
	// Extensions are the file extensions the server is for; LanguageID is the protocol's id for them.
	Extensions []string `json:"extensions"`
	LanguageID string   `json:"languageId"`
	// RootMarkers name the files or directories that mark a language root, by priority.
	RootMarkers           []string        `json:"rootMarkers"`
	InitializationOptions json.RawMessage `json:"initializationOptions"`
	// Disabled keeps the language's server from starting.
	Disabled bool `json:"disabled"`
}

// Problem is a validation problem or an ignored value.
type Problem struct {
	Source  Source
	Key     string
	Message string
}

func (p Problem) Error() string {
	if p.Key == "" {
		return p.Source.String() + ": " + p.Message
	}
	return p.Source.String() + ": " + p.Key + ": " + p.Message
}

// Settings is the merged result.
type Settings struct {
	Model                  string
	EffortLevel            string
	OutputStyle            string
	AutoCompactWindow      int
	CleanupPeriodDays      int
	IncludeGitInstructions bool
	// InterruptReply lets the model answer a stopped turn with a short
	// message (interruptReply, default true).
	InterruptReply bool
	// CompactIdleSessions lets the client compact sessions in the background after they were idle for a while
	// (compactIdleSessions, default true; a per-user preference, see UserPreferences in agent.proto).
	CompactIdleSessions bool
	// PRMonitorWakesPerHour caps how often a monitored pull request wakes a session (prMonitorWakesPerHour, default
	// DefaultPRMonitorWakesPerHour, at most MaxPRMonitorWakesPerHour; a per-user preference, see UserPreferences).
	PRMonitorWakesPerHour int
	// SubagentMaxDepth is how deep subagents nest and SubagentMaxRunning how many of a session run at once
	// (subagentMaxDepth, subagentMaxRunning; per-user preferences, see UserPreferences).
	SubagentMaxDepth   int
	SubagentMaxRunning int
	// GoalMaxTurns is how many turns `/goal` may take when it names no cap (goalMaxTurns; a per-user preference).
	GoalMaxTurns int
	// WorkflowMaxAgents is the hard cap on agent() calls in one run of the Workflow tool (workflowMaxAgents; a per-user
	// preference, see UserPreferences).
	WorkflowMaxAgents int
	ClaudeMdMode      memory.Mode
	// AutoMemoryEnabled lets the agent keep memory files per project and loads their index into new sessions
	// (autoMemoryEnabled, default true). A per-user preference; a project file can only turn it off.
	AutoMemoryEnabled bool
	// LSPEnabled offers the LSP tool and allows diagnostics after edits (lspEnabled, default true); LSPDiagnostics reports
	// those errors (lspDiagnostics, default true); LSPDiagnosticsStartServer lets an edit start a server rather than only
	// use one that runs (lspDiagnosticsStartServer, default false). Per-user preferences, see UserPreferences; a project
	// file can only turn them off.
	LSPEnabled                bool
	LSPDiagnostics            bool
	LSPDiagnosticsStartServer bool
	// LSPServers are command overrides and added languages by language id (lspServers). A project file's entries apply
	// only in a trusted workspace: a repository must not choose a binary that runs on open.
	LSPServers  map[string]LSPServer
	Attribution Attribution
	Env         map[string]string
	Permissions Permissions
	// AvailableModels is the policy allowlist of models (empty: any).
	AvailableModels []string
	// RemoteControlPolicyOff: a policy document says "remoteControl": false, so sessions are never mirrored to the
	// server and the per-session switch cannot turn it on.
	RemoteControlPolicyOff bool
	// Sandbox is the OS sandbox of the model's commands (sandbox.go).
	Sandbox Sandbox
	// Hooks are the hook blocks of every source, lowest precedence first (hooks.go). HooksOff: disableAllHooks at any level;
	// HooksPolicyOff: in a policy document, which also stops the policy's own hooks.
	Hooks                    []HookLayer
	HooksOff, HooksPolicyOff bool
	// HooksManagedOnly: allowManagedHooksOnly in a policy document, which also keeps plugins' hooks from running.
	HooksManagedOnly bool
	// Extras are further settings (extras.go).
	Extras Extras

	// Raw holds every top-level key of the winning level (for keys this
	// package does not model). Values are byte-exact JSON.
	Raw map[string]json.RawMessage

	// Sources lists the loaded sources from highest to lowest precedence.
	Sources []Source
	// Problems are validation errors (bad JSON, wrong types, bad values).
	Problems []Problem
	// Ignored lists values dropped by the trust gate or by policy.
	Ignored []Problem

	Trusted bool
	// Synced reports whether user/org settings came from the server.
	Synced bool
}

// DocSource is the synced document cache (implemented by syncclient.Cache).
type DocSource interface {
	Synced() bool
	List(kind syncv1.DocumentKind) []*syncv1.Document
}

// CLI holds settings given on the command line.
type CLI struct {
	Model                 string
	EffortLevel           string
	PermissionMode        string
	OutputStyle           string
	Allow, Ask, Deny      []string
	AdditionalDirectories []string
	// Settings is the content of --settings (a JSON object), if any;
	// SettingsPath labels its source.
	Settings     []byte
	SettingsPath string
	// PermissionProfile is --permission-profile: the profile to use, over the permissionProfile setting.
	PermissionProfile string
}

// Options configure Load.
type Options struct {
	Cwd string
	// Home defaults to os.UserHomeDir.
	Home string
	CLI  CLI
	// Docs supplies policy, user and org documents. Nil, or not yet synced:
	// no user level.
	Docs DocSource
	// Trusted is the workspace trust decision for Cwd.
	Trusted bool
}

// Defaults.
const (
	DefaultCleanupPeriodDays = 30
	// DefaultPRMonitorWakesPerHour and MaxPRMonitorWakesPerHour bound Settings.PRMonitorWakesPerHour. The maximum is
	// one wake per fastest poll (15 s): the cap must still be able to stop a loop.
	DefaultPRMonitorWakesPerHour = 30
	MaxPRMonitorWakesPerHour     = 240
)

// ValidPRMonitorWakesPerHour reports whether n can be Settings.PRMonitorWakesPerHour.
func ValidPRMonitorWakesPerHour(n int) bool { return n >= 1 && n <= MaxPRMonitorWakesPerHour }

// KeyCompactIdleSessions is the settings key of Settings.CompactIdleSessions. The frontends' preference toggle
// writes it into the user's synced settings document (UserSettingsKey).
const KeyCompactIdleSessions = "compactIdleSessions"

// KeyDisabledSkills is the settings key of the skills and custom commands the user turned off, by name. It is synced,
// like every preference: a skill keeps its switch from one machine to the next.
const KeyDisabledSkills = "disabledSkills"

// KeyDisableSkillShell turns off running the `!`command“ of skill and command bodies.
const KeyDisableSkillShell = "disableSkillShellExecution"

// Bounds of Settings.SubagentMaxDepth and Settings.SubagentMaxRunning. The maximums keep a model from fanning out past
// what a machine and a usage limit can carry.
const (
	DefaultSubagentMaxDepth   = 2
	MaxSubagentMaxDepth       = 5
	DefaultSubagentMaxRunning = 8
	MaxSubagentMaxRunning     = 32
	DefaultWorkflowMaxAgents  = 30
	MaxWorkflowMaxAgents      = 200
)

// ValidSubagentMaxDepth reports whether n can be Settings.SubagentMaxDepth.
func ValidSubagentMaxDepth(n int) bool { return n >= 1 && n <= MaxSubagentMaxDepth }

// ValidSubagentMaxRunning reports whether n can be Settings.SubagentMaxRunning.
func ValidSubagentMaxRunning(n int) bool { return n >= 1 && n <= MaxSubagentMaxRunning }

// ValidWorkflowMaxAgents reports whether n can be Settings.WorkflowMaxAgents.
func ValidWorkflowMaxAgents(n int) bool { return n >= 1 && n <= MaxWorkflowMaxAgents }

// The settings keys of Settings.SubagentMaxDepth, Settings.SubagentMaxRunning and Settings.WorkflowMaxAgents.
const (
	KeySubagentMaxDepth   = "subagentMaxDepth"
	KeySubagentMaxRunning = "subagentMaxRunning"
	KeyWorkflowMaxAgents  = "workflowMaxAgents"
)

// KeySchedulesEnabled and KeyGoalMaxTurns are the settings keys of the same preferences (UserPreferences).
const (
	KeySchedulesEnabled = "schedulesEnabled"
	KeyGoalMaxTurns     = "goalMaxTurns"
	// DefaultGoalMaxTurns and MaxGoalMaxTurns bound the turns `/goal` may take when it names no cap.
	DefaultGoalMaxTurns = 20
	MaxGoalMaxTurns     = 200
)

// ValidGoalMaxTurns reports whether n can be the cap of a goal.
func ValidGoalMaxTurns(n int) bool { return n >= 1 && n <= MaxGoalMaxTurns }

// KeyPRMonitorWakesPerHour is the settings key of Settings.PRMonitorWakesPerHour, written the same way.
const KeyPRMonitorWakesPerHour = "prMonitorWakesPerHour"

// KeyInterruptReply is the settings key of Settings.InterruptReply.
const KeyInterruptReply = "interruptReply"

// KeyAutoMemoryEnabled is the settings key of Settings.AutoMemoryEnabled.
const KeyAutoMemoryEnabled = "autoMemoryEnabled"

// The settings keys of the language server settings.
const (
	KeyLSPEnabled                = "lspEnabled"
	KeyLSPDiagnostics            = "lspDiagnostics"
	KeyLSPDiagnosticsStartServer = "lspDiagnosticsStartServer"
	KeyLSPServers                = "lspServers"
)

// KeyRemoteControl is the settings key of the remote control default (true when absent): false keeps new and
// resumed sessions off the relay until the session switch turns it on; false in a policy document turns it off
// for good (Settings.RemoteControlPolicyOff).
const KeyRemoteControl = "remoteControl"

// UserSettingsKey is the key of the user-scope SETTINGS sync document (the one `import claude` writes).
const UserSettingsKey = "settings"

var (
	effortLevels = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}
	modes        = map[string]bool{"default": true, "acceptEdits": true, "plan": true, "dontAsk": true, "bypassPermissions": true, "auto": true}
	// wideningModes may not come from untrusted committed settings.
	wideningModes = map[string]bool{"acceptEdits": true, "dontAsk": true, "bypassPermissions": true, "auto": true}
)

// layer is one parsed source.
type layer struct {
	src Source
	raw map[string]json.RawMessage

	model, effort, outputStyle, claudeMdMode, defaultMode     *string
	autoCompactWindow, cleanupPeriodDays, prWakes             *int
	subagentDepth, subagentRunning, workflowAgents, goalTurns *int
	includeGit, interruptReply, compactIdle                   *bool
	lspEnabled, lspDiagnostics, lspStartServer                *bool
	autoMemory                                                *bool
	lspServers                                                map[string]LSPServer
	attrCommit, attrPR                                        *string
	env                                                       map[string]string
	allow, ask, deny, addDirs                                 []string
	disableBypass, disableAuto                                bool
	disabledModes                                             []string

	availableModels  []string
	managedRulesOnly bool
	// remoteControlOff: a policy document says "remoteControl": false.
	remoteControlOff bool
	sandbox          *sandboxLayer
	// hooks is the source's hooks block; hooksOff its disableAllHooks; managedHooksOnly a policy's allowManagedHooksOnly.
	hooks                      json.RawMessage
	hooksOff, managedHooksOnly bool
	// extra are further layers of the same level (permission rule documents).
	extra []*layer
}

// Load reads and merges all settings sources.
func Load(ctx context.Context, o Options) (*Settings, error) {
	_ = ctx
	cwd := o.Cwd
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return nil, err
		}
	}
	cwd, _ = filepath.Abs(cwd)
	home := o.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	s := &Settings{Trusted: o.Trusted, Env: map[string]string{}, Raw: map[string]json.RawMessage{},
		CleanupPeriodDays: DefaultCleanupPeriodDays, IncludeGitInstructions: true, InterruptReply: true,
		CompactIdleSessions: true, AutoMemoryEnabled: true, LSPEnabled: true, LSPDiagnostics: true, PRMonitorWakesPerHour: DefaultPRMonitorWakesPerHour,
		SubagentMaxDepth: DefaultSubagentMaxDepth, SubagentMaxRunning: DefaultSubagentMaxRunning, WorkflowMaxAgents: DefaultWorkflowMaxAgents, GoalMaxTurns: DefaultGoalMaxTurns,
		Sandbox: Sandbox{Enabled: true, AllowLocalhost: true}}
	var layers []*layer // highest precedence first
	add := func(l *layer) {
		if l != nil {
			layers = append(layers, l)
		}
	}

	synced := o.Docs != nil && o.Docs.Synced()
	s.Synced = synced

	// Policy documents: always applied when cached, even before a full sync.
	if o.Docs != nil {
		pol := o.Docs.List(syncv1.DocumentKind_DOCUMENT_KIND_POLICY)
		sort.SliceStable(pol, func(i, j int) bool {
			if (pol[i].GetKey() == "default") != (pol[j].GetKey() == "default") {
				return pol[j].GetKey() == "default" // role-targeted policies above "default"
			}
			return pol[i].GetKey() < pol[j].GetKey()
		})
		for _, d := range pol {
			if d.GetScope() != commonv1.Scope_SCOPE_ORG {
				continue
			}
			add(s.parse(Source{Level: LevelPolicy, DocID: d.GetId()}, []byte(d.GetContent())))
		}
	}

	// The machine's own policies come below what the server delivers, and above everything else.
	for _, p := range DevicePolicies() {
		add(s.parse(Source{Level: LevelPolicy, Path: p.Origin}, p.Content))
	}

	add(s.cliLayer(o.CLI, cwd))

	for _, f := range []struct {
		rel   string
		level Level
	}{
		{".harness/settings.local.json", LevelLocal},
		{".harness/settings.json", LevelProject},
	} {
		p := filepath.Join(cwd, filepath.FromSlash(f.rel))
		add(s.parseFile(Source{Level: f.level, Path: p, Dir: cwd}, p))
	}

	if synced {
		add(s.docLayer(o.Docs, commonv1.Scope_SCOPE_USER, LevelUser, home))
		add(s.docLayer(o.Docs, commonv1.Scope_SCOPE_ORG, LevelOrg, ""))
	} else if o.Docs != nil {
		// Org defaults may already be cached from an interrupted sync.
		add(s.docLayer(o.Docs, commonv1.Scope_SCOPE_ORG, LevelOrg, ""))
	}

	layers = flatten(layers)
	layers = s.profileLayers(layers, o.CLI.PermissionProfile)
	for _, l := range layers {
		s.Sources = append(s.Sources, l.src)
	}
	s.merge(layers, cwd, home)
	s.applyExtras(layers)
	return s, nil
}

func (s *Settings) problem(src Source, key, format string, args ...any) {
	s.Problems = append(s.Problems, Problem{Source: src, Key: key, Message: fmt.Sprintf(format, args...)})
}

func (s *Settings) ignored(src Source, key, msg string) {
	s.Ignored = append(s.Ignored, Problem{Source: src, Key: key, Message: msg})
}

func (s *Settings) parseFile(src Source, path string) *layer {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			s.problem(src, "", "read: %v", err)
		}
		return nil
	}
	return s.parse(src, data)
}

// docLayer merges the SETTINGS and PERMISSION_RULES documents of one scope
// into a layer.
func (s *Settings) docLayer(docs DocSource, scope commonv1.Scope, level Level, dir string) *layer {
	var out *layer
	for _, d := range docs.List(syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS) {
		if d.GetScope() != scope {
			continue
		}
		if l := s.parse(Source{Level: level, DocID: d.GetId(), Dir: dir}, []byte(d.GetContent())); l != nil {
			out = l
			break
		}
	}
	for _, d := range docs.List(syncv1.DocumentKind_DOCUMENT_KIND_PERMISSION_RULES) {
		if d.GetScope() != scope {
			continue
		}
		src := Source{Level: level, DocID: d.GetId(), Dir: dir}
		var rules struct {
			Allow, Ask, Deny json.RawMessage
		}
		if err := json.Unmarshal([]byte(d.GetContent()), &rules); err != nil {
			s.problem(src, "", "invalid JSON: %v", err)
			continue
		}
		// Permission rules get their own layer so that their source is the
		// rules document, not the settings document.
		pl := &layer{src: src}
		pl.allow = s.stringList(src, "allow", rules.Allow)
		pl.ask = s.stringList(src, "ask", rules.Ask)
		pl.deny = s.stringList(src, "deny", rules.Deny)
		if out == nil {
			out = pl
		} else {
			out.extra = append(out.extra, pl)
		}
	}
	for _, hl := range hookDocLayers(docs, scope, level, dir) {
		if out == nil {
			out = hl
		} else {
			out.extra = append(out.extra, hl)
		}
	}
	return out
}

func (s *Settings) cliLayer(c CLI, cwd string) *layer {
	src := Source{Level: LevelCLI, Path: c.SettingsPath, Dir: cwd}
	var l *layer
	if len(c.Settings) > 0 {
		l = s.parse(src, c.Settings)
	}
	if l == nil {
		l = &layer{src: src}
	}
	setStr := func(dst **string, v, key string) {
		if v != "" {
			*dst = &v
			l.raw = setRaw(l.raw, key, v)
		}
	}
	setStr(&l.model, c.Model, "model")
	setStr(&l.outputStyle, c.OutputStyle, "outputStyle")
	if c.EffortLevel != "" {
		if effortLevels[c.EffortLevel] {
			setStr(&l.effort, c.EffortLevel, "effortLevel")
		} else {
			s.problem(src, "effortLevel", "unknown effort level %q", c.EffortLevel)
		}
	}
	if c.PermissionMode != "" {
		if modes[c.PermissionMode] {
			setStr(&l.defaultMode, c.PermissionMode, "")
		} else {
			s.problem(src, "permissions.defaultMode", "unknown permission mode %q", c.PermissionMode)
		}
	}
	l.allow = append(l.allow, c.Allow...)
	l.ask = append(l.ask, c.Ask...)
	l.deny = append(l.deny, c.Deny...)
	l.addDirs = append(l.addDirs, c.AdditionalDirectories...)
	if l.raw == nil && l.model == nil && l.outputStyle == nil && l.effort == nil && l.defaultMode == nil && len(l.allow)+len(l.ask)+len(l.deny)+len(l.addDirs) == 0 {
		return nil
	}
	return l
}

func setRaw(m map[string]json.RawMessage, key, v string) map[string]json.RawMessage {
	if key == "" {
		return m
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	b, _ := json.Marshal(v)
	m[key] = b
	return m
}
