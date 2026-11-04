// Package permissions decides whether a tool call may run, with Claude Code
// semantics: rules in `Tool(specifier)` syntax evaluated deny → ask → allow
// (first match wins), compound Bash commands checked per subcommand with
// wrappers stripped, gitignore-style path rules, a built-in read-only Bash
// allowlist, protected paths, permission modes, workspace trust and "allow
// always" persistence to .claude/settings.local.json.
//
// The engine takes its rule lists and default mode as plain inputs (Config);
// loading the settings hierarchy is the caller's job.
package permissions

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/xlsft/harness/packages/client/netguard"
)

// Mode is a permission mode.
type Mode string

// Supported modes.
const (
	ModeDefault           Mode = "default"
	ModeAcceptEdits       Mode = "acceptEdits"
	ModeBypassPermissions Mode = "bypassPermissions"
	ModePlan              Mode = "plan"
	// ModeDontAsk denies everything that would otherwise prompt.
	ModeDontAsk Mode = "dontAsk"
	// ModeAuto sends everything that would otherwise prompt to the Decider
	// (harness.decision.v1.DecisionService with local rules as fallback).
	ModeAuto Mode = "auto"
)

// ParseMode validates a mode name ("manual" is an alias of default).
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case "", "manual":
		return ModeDefault, nil
	case ModeDefault, ModeAcceptEdits, ModeBypassPermissions, ModePlan, ModeDontAsk, ModeAuto:
		return Mode(s), nil
	}
	return "", fmt.Errorf("unknown permission mode %q (want default, acceptEdits, plan, auto, dontAsk or bypassPermissions)", s)
}

// ModeCycle is the order frontends cycle through with Shift+Tab.
func ModeCycle() []Mode { return []Mode{ModeDefault, ModeAcceptEdits, ModePlan, ModeAuto} }

// NextMode returns the mode after cur in ModeCycle, skipping auto when it is
// not available. Modes outside the cycle (dontAsk, bypassPermissions) go
// back to default.
func NextMode(cur Mode, autoAvailable bool) Mode {
	cycle := ModeCycle()
	for i, m := range cycle {
		if m != cur {
			continue
		}
		next := cycle[(i+1)%len(cycle)]
		if next == ModeAuto && !autoAvailable {
			next = cycle[0]
		}
		return next
	}
	return ModeDefault
}

// Behavior is the outcome of a check.
type Behavior string

// Behaviors.
const (
	Allow Behavior = "allow"
	Deny  Behavior = "deny"
	Ask   Behavior = "ask"
)

// Request describes a pending tool call.
type Request struct {
	ToolName  string
	ToolUseID string
	Input     json.RawMessage
	// ReadOnly is the tool's own classification of this call.
	ReadOnly bool
	// FilePath / Command are pre-extracted for matching (may be empty).
	FilePath string
	Command  string
	// Cwd is the shell working directory for Bash (defaults to the engine cwd).
	Cwd string
	// AlwaysAsk marks a tool that needs an explicit user confirmation for
	// every call (MCP servers with requires_user_interaction): only deny
	// rules apply, allow rules and permissive modes do not, and the auto-mode
	// Decider is never consulted.
	AlwaysAsk bool
	// URL is the page a browser tool acts on or loads (tools.PermissionHints; "" for other tools or no page).
	URL string
	// Sensitive, when set, says why only the user may approve this call (a browser tool typing into a password
	// or payment field): deny rules apply, allow rules, permissive modes and the auto-mode Decider do not, and
	// no "always allow" rule is offered.
	Sensitive string

	// Intent context for the auto-mode Decider (filled by the agent loop):
	// the session id, the latest user prompts (newest last) and the
	// assistant text right before the tool call.
	SessionID          string
	RecentUserMessages []string
	Rationale          string

	// Set by the engine before calling the Asker.
	// Suggestions are rules the user may choose to always allow.
	Suggestions []string
	// Reason explains why the call needs approval.
	Reason string
}

// Decision is the result of a permission check.
type Decision struct {
	Behavior Behavior
	// Message explains a denial (sent to the model).
	Message string
	// UpdatedInput replaces the tool input when set (from an interactive approver).
	UpdatedInput json.RawMessage
	// Interrupt asks the loop to stop the turn after denying (deny without comment).
	Interrupt bool
	// Comment is user feedback sent to the model after the tool result (allow).
	Comment string
	// Rule is the rule that decided, when one did.
	Rule string
	// ResolvedBy is "rule", "mode", "user", "auto", ... (informational).
	ResolvedBy string
	// Auto is the auto-mode Decider's verdict when it was consulted
	// (also set when its ASK fell back to the user).
	Auto *AutoResult
	// UserOnly marks an Ask that must go to the user and never to the
	// auto-mode Decider (protected paths, explicit ask rules).
	UserOnly bool
}

// Checker decides on tool calls. It must be safe for concurrent use.
type Checker interface {
	Check(ctx context.Context, req Request) Decision
}

// Asker resolves Ask decisions interactively (AgentService, TUI prompt).
type Asker interface {
	Ask(ctx context.Context, req Request) (Decision, error)
}

// Source is one origin of permission rules, in priority order:
// policySettings, cliArg, session, localSettings, projectSettings, userSettings.
type Source struct {
	// Name identifies the source ("cliArg", "localSettings", ...).
	Name string
	// Root is the base directory for "/path" specifiers. For
	// .claude/settings*.json this is the directory that contains .claude/;
	// for user settings the home directory; for CLI flags the cwd.
	Root             string
	Allow, Ask, Deny []string
	// RequiresTrust marks committed project settings: their allow rules
	// apply only once the workspace is trusted (deny and ask always apply).
	RequiresTrust bool
}

// Config is the engine input. Settings loaders fill Sources; Allow/Ask/Deny
// are shorthand for a "cliArg" source rooted at Cwd.
type Config struct {
	Cwd                   string
	AdditionalDirectories []string
	DefaultMode           Mode
	Allow, Ask, Deny      []string
	Sources               []Source
	// Trusted reports whether the workspace is trusted (see TrustStore).
	Trusted bool
	// DisableBypassPermissions refuses switching to bypassPermissions.
	DisableBypassPermissions bool
	// DisableAutoMode refuses switching to auto (settings disableAutoMode).
	DisableAutoMode bool
	// DisabledModes are the modes the server's policy disables at start
	// (SetPolicyDisabled updates them live).
	DisabledModes []Mode
	// Decider decides what would otherwise prompt in auto mode (may be nil:
	// auto then behaves like default).
	Decider Decider
	// LocalSettingsPath is where "allow always" rules are persisted;
	// default <repo root>/.claude/settings.local.json.
	LocalSettingsPath string
	// ScratchDirs may be read and written without asking (session scratch).
	ScratchDirs []string
	// ReadableDirs may be read without asking (plans, task output).
	ReadableDirs []string
	// Home overrides the home directory (tests).
	Home string
	// NetGuard classifies WebFetch targets as private (default netguard.Default).
	NetGuard *netguard.Guard
}

// Engine is the rule engine. It implements Checker.
type Engine struct {
	mu   sync.RWMutex
	mode Mode
	// The mode plan mode was entered from, restored when a plan is approved.
	prePlan       Mode
	cwd           string
	workDirs      []string
	scratch       []string
	readable      []string
	home          string
	trusted       bool
	disableBypass bool
	disableAuto   bool
	policy        map[Mode]bool
	decider       Decider
	localPath     string
	localRoot     string
	sources       []Source
	allow         []Rule
	ask           []Rule
	deny          []Rule
	asker         Asker
	onMode        func(Mode)
	netGuard      *netguard.Guard
	// sites are the hosts the user approved for the browser tools in this session (browser.go).
	sites map[string]bool
}

// New builds an engine from cfg.
func New(cfg Config) *Engine {
	e := &Engine{
		mode: cfg.DefaultMode, cwd: cfg.Cwd, home: cfg.Home, trusted: cfg.Trusted,
		disableBypass: cfg.DisableBypassPermissions, scratch: cfg.ScratchDirs, readable: cfg.ReadableDirs,
		disableAuto: cfg.DisableAutoMode, decider: cfg.Decider, netGuard: cfg.NetGuard,
	}
	e.policy = modeSet(cfg.DisabledModes)
	if e.mode == "" || e.disabledLocked(e.mode) != nil {
		e.mode = ModeDefault
	}
	if e.home == "" {
		e.home = homeDir()
	}
	if e.cwd == "" {
		e.cwd, _ = filepath.Abs(".")
	}
	e.workDirs = append([]string{e.cwd}, cfg.AdditionalDirectories...)
	e.localPath = cfg.LocalSettingsPath
	if e.localPath == "" {
		e.localPath = filepath.Join(RepoRoot(e.cwd), ".claude", "settings.local.json")
	}
	e.localRoot = filepath.Dir(filepath.Dir(e.localPath))
	if len(cfg.Allow)+len(cfg.Ask)+len(cfg.Deny) > 0 {
		e.sources = append(e.sources, Source{Name: "cliArg", Root: e.cwd, Allow: cfg.Allow, Ask: cfg.Ask, Deny: cfg.Deny})
	}
	e.sources = append(e.sources, cfg.Sources...)
	e.rebuild()
	return e
}

func (e *Engine) rebuild() {
	e.allow, e.ask, e.deny = nil, nil, nil
	for _, s := range e.sources {
		root := s.Root
		if root == "" {
			root = e.cwd
		}
		tag := func(items []string) []Rule {
			rs := ParseRules(items)
			for i := range rs {
				rs[i].Source, rs[i].Root = s.Name, root
			}
			return rs
		}
		e.deny = append(e.deny, tag(s.Deny)...)
		e.ask = append(e.ask, tag(s.Ask)...)
		if !s.RequiresTrust || e.trusted {
			e.allow = append(e.allow, tag(s.Allow)...)
		}
	}
}

// SetAsker installs an interactive approver; without it Ask becomes Deny.
func (e *Engine) SetAsker(a Asker) {
	e.mu.Lock()
	e.asker = a
	e.mu.Unlock()
}

// OnModeChange registers a listener called after every mode change.
func (e *Engine) OnModeChange(f func(Mode)) {
	e.mu.Lock()
	e.onMode = f
	e.mu.Unlock()
}

// SetDecider installs the auto-mode Decider (nil removes it).
func (e *Engine) SetDecider(d Decider) {
	e.mu.Lock()
	e.decider = d
	e.mu.Unlock()
}

func modeSet(modes []Mode) map[Mode]bool {
	out := map[Mode]bool{}
	for _, m := range modes {
		if m != ModeDefault {
			out[m] = true
		}
	}
	return out
}

// disabledLocked reports why m may not be used (nil: it may). Caller holds mu.
func (e *Engine) disabledLocked(m Mode) error {
	switch {
	case m == ModeDefault:
		return nil
	case e.policy[m]:
		return &ModeDisabledError{Mode: m, Policy: true}
	case m == ModeBypassPermissions && e.disableBypass, m == ModeAuto && e.disableAuto:
		return &ModeDisabledError{Mode: m}
	}
	return nil
}

// ModeAllowed reports whether m may be switched on (settings and the server's policy).
func (e *Engine) ModeAllowed(m Mode) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.disabledLocked(m) == nil
}

// AutoModeAvailable reports whether auto mode may be switched on.
func (e *Engine) AutoModeAvailable() bool { return e.ModeAllowed(ModeAuto) }

// NextMode returns the next allowed mode of the Shift+Tab cycle from the
// current one; modes outside the cycle go back to default.
func (e *Engine) NextMode() Mode {
	cur, cycle := e.Mode(), ModeCycle()
	i := slices.Index(cycle, cur)
	if i < 0 {
		return ModeDefault
	}
	for k := 1; k < len(cycle); k++ {
		if m := cycle[(i+k)%len(cycle)]; e.ModeAllowed(m) {
			return m
		}
	}
	return ModeDefault
}

// SetMode changes the mode at runtime. A change clears the Decider's
// session cache. A disabled mode fails with *ModeDisabledError.
func (e *Engine) SetMode(m Mode) error {
	e.mu.Lock()
	if err := e.disabledLocked(m); err != nil {
		e.mu.Unlock()
		return err
	}
	e.switchLocked(m)
	return nil
}

// PrePlanMode is the mode plan mode was last entered from (default when unknown).
func (e *Engine) PrePlanMode() Mode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.prePlan == "" || e.prePlan == ModePlan {
		return ModeDefault
	}
	return e.prePlan
}

// switchLocked sets the mode, releases mu and notifies on a change.
func (e *Engine) switchLocked(m Mode) {
	changed := e.mode != m
	if m == ModePlan && e.mode != ModePlan {
		e.prePlan = e.mode
	}
	e.mode = m
	f, d := e.onMode, e.decider
	e.mu.Unlock()
	if changed {
		if r, ok := d.(Resetter); ok {
			r.Reset()
		}
		if f != nil {
			f(m)
		}
	}
}

// SetPolicyDisabled replaces the modes the server's policy disables. When
// the current mode becomes disabled the session switches to the closest
// allowed one (ClosestAllowed) and reports it: from is the old mode, to the
// new one.
func (e *Engine) SetPolicyDisabled(modes []Mode) (from, to Mode, switched bool) {
	e.mu.Lock()
	e.policy = modeSet(modes)
	from = e.mode
	if e.disabledLocked(from) == nil {
		e.mu.Unlock()
		return from, from, false
	}
	to = ClosestAllowed(from, func(m Mode) bool { return e.disabledLocked(m) == nil })
	e.switchLocked(to)
	return from, to, true
}

// Mode returns the current mode.
func (e *Engine) Mode() Mode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode
}

// SetTrusted applies or withdraws trust-gated allow rules.
func (e *Engine) SetTrusted(t bool) {
	e.mu.Lock()
	e.trusted = t
	e.rebuild()
	e.mu.Unlock()
}

// WorkingDirs returns the cwd plus additional directories.
func (e *Engine) WorkingDirs() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]string(nil), e.workDirs...)
}

// SetAdditionalDirectories replaces the additional working directories (the cwd stays first).
func (e *Engine) SetAdditionalDirectories(dirs []string) {
	e.mu.Lock()
	e.workDirs = append([]string{e.cwd}, dirs...)
	e.mu.Unlock()
}

// AddReadableDir lets the model read dir without asking.
func (e *Engine) AddReadableDir(dir string) {
	e.mu.Lock()
	e.readable = append(e.readable, dir)
	e.mu.Unlock()
}

// AddAllow appends session-only allow rules.
func (e *Engine) AddAllow(rules ...string) {
	e.mu.Lock()
	e.addSourceRules("session", e.cwd, rules)
	e.mu.Unlock()
}

func (e *Engine) addSourceRules(name, root string, rules []string) {
	for i := range e.sources {
		if e.sources[i].Name == name && e.sources[i].Root == root {
			e.sources[i].Allow = append(e.sources[i].Allow, rules...)
			e.rebuild()
			return
		}
	}
	e.sources = append(e.sources, Source{Name: name, Root: root, Allow: rules})
	e.rebuild()
}

// AllowAlways adds rules for the rest of the session and persists them to
// the local settings file (merging with its other keys).
func (e *Engine) AllowAlways(rules []string) error {
	var valid []string
	for _, r := range rules {
		if err := ValidateRule(r); err != nil {
			return err
		}
		valid = append(valid, strings.TrimSpace(r))
	}
	if len(valid) == 0 {
		return nil
	}
	e.mu.Lock()
	e.addSourceRules("localSettings", e.localRoot, valid)
	path := e.localPath
	e.mu.Unlock()
	return PersistAllowRules(path, valid)
}

// LocalSettingsPath returns where AllowAlways persists rules.
func (e *Engine) LocalSettingsPath() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.localPath
}

// IsDisabled reports whether a bare deny rule removes the tool from the
// model's tool list.
func (e *Engine) IsDisabled(tool string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, r := range e.deny {
		if r.Spec == "" && r.toolMatches(tool, false) {
			return true
		}
	}
	return false
}

// Check implements Checker.
func (e *Engine) Check(ctx context.Context, req Request) Decision {
	d := e.Evaluate(req)
	if d.Behavior != Ask {
		return d
	}
	e.mu.RLock()
	asker, mode, decider := e.asker, e.mode, e.decider
	e.mu.RUnlock()
	if mode == ModeDontAsk {
		return Decision{Behavior: Deny, Message: fmt.Sprintf("Permission to use %s was not granted (dontAsk mode denies anything not pre-approved).", req.ToolName), ResolvedBy: "mode"}
	}
	reason := d.Message
	var auto *AutoResult
	if mode == ModeAuto && decider != nil && !d.UserOnly {
		res := e.decide(ctx, decider, req)
		auto = &res
		switch res.Behavior {
		case Allow:
			return Decision{Behavior: Allow, ResolvedBy: "auto", Auto: auto}
		case Deny:
			return Decision{Behavior: Deny, ResolvedBy: "auto", Auto: auto, Message: fmt.Sprintf(
				"Permission to use %s was denied by auto mode (%s): %s Do not retry it; find a safer way to reach the goal, or ask the user to run it themselves.",
				req.ToolName, res.Provider, sentence(res.Reason))}
		}
		if asker == nil {
			return Decision{Behavior: Deny, ResolvedBy: "auto", Auto: auto, Message: fmt.Sprintf(
				"Permission to use %s was not granted: auto mode could not approve it (%s: %s) and nobody can be asked in this non-interactive session. Allow it with --allowedTools if it is expected.",
				req.ToolName, res.Provider, strings.TrimSuffix(strings.TrimSpace(res.Reason), "."))}
		}
		reason = "Auto mode could not approve this action (" + res.Provider + "): " + sentence(res.Reason)
		if d.Message != "" {
			reason += " " + d.Message
		}
	}
	// Every other mode but bypass: the classifier guards what would prompt. Its deny blocks the action; an
	// allow or an unsure answer still goes to the user, so the mode keeps its meaning. Without anyone to ask
	// the action is refused anyway, so the classifier is not consulted.
	if mode != ModeAuto && mode != ModeBypassPermissions && decider != nil && asker != nil && !d.UserOnly {
		res := e.decide(ctx, decider, req)
		auto = &res
		if res.Behavior == Deny {
			return Decision{Behavior: Deny, ResolvedBy: "auto", Auto: auto, Message: fmt.Sprintf(
				"Permission to use %s was denied by the classifier (%s): %s Do not retry it; find a safer way to reach the goal, or ask the user to run it themselves.",
				req.ToolName, res.Provider, sentence(res.Reason))}
		}
		if res.Behavior == Allow && res.Reason != "" {
			reason = "The classifier sees no risk (" + res.Provider + "): " + sentence(res.Reason)
			if d.Message != "" {
				reason += " " + d.Message
			}
		}
	}
	if asker == nil {
		return Decision{Behavior: Deny, Message: fmt.Sprintf("Permission to use %s was not granted (non-interactive mode). Allow it with --allowedTools or --permission-mode.", req.ToolName), ResolvedBy: "mode"}
	}
	req.Suggestions = e.Suggest(req)
	req.Reason = reason
	ad, err := asker.Ask(ctx, req)
	if err != nil {
		return Decision{Behavior: Deny, Message: fmt.Sprintf("Permission request for %s failed: %v", req.ToolName, err), Interrupt: ctx.Err() != nil}
	}
	if ad.Behavior != Allow {
		ad.Behavior = Deny
		if ad.Message == "" {
			ad.Message = fmt.Sprintf("The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). STOP what you are doing and wait for the user to tell you how to proceed.")
			ad.Interrupt = true
		} else {
			ad.Message = "The user doesn't want to proceed with this tool use. The tool use was rejected. The user said:\n" + ad.Message
		}
	}
	if ad.ResolvedBy == "" {
		ad.ResolvedBy = "user"
	}
	ad.Auto = auto
	if ad.Behavior == Allow && req.Sensitive == "" {
		e.grantSite(req)
	}
	return ad
}

// decide consults the Decider; an error becomes ASK with the error as reason.
func (e *Engine) decide(ctx context.Context, d Decider, req Request) AutoResult {
	e.mu.RLock()
	ar := AutoRequest{Request: req, Cwd: req.Cwd, Home: e.home}
	if ar.Cwd == "" {
		ar.Cwd = e.cwd
	}
	ar.WorkDirs = append([]string(nil), e.workDirs...)
	ar.AdditionalDirs = append([]string(nil), e.workDirs[1:]...)
	e.mu.RUnlock()
	res, err := d.Decide(ctx, ar)
	if err != nil {
		return AutoResult{Behavior: Ask, Provider: "none", Reason: "the auto-mode decision failed: " + err.Error()}
	}
	switch res.Behavior {
	case Allow, Deny, Ask:
	default:
		res.Behavior = Ask
	}
	if res.Provider == "" {
		res.Provider = "none"
	}
	if strings.TrimSpace(res.Reason) == "" {
		switch res.Behavior {
		case Allow:
			res.Reason = "Judged safe."
		case Deny:
			res.Reason = "Judged unsafe."
		default:
			res.Reason = "No provider was confident enough to decide."
		}
	}
	return res
}

// sentence trims s and makes sure it ends with a period.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}

func (e *Engine) planDenial(tool string) Decision {
	return Decision{Behavior: Deny, ResolvedBy: "mode", Message: fmt.Sprintf("Plan mode is active: %s is not allowed. Only read-only tools may be used until the user approves a plan (call ExitPlanMode with the plan).", tool)}
}

// Evaluate returns the static decision without consulting the asker.
func (e *Engine) Evaluate(req Request) Decision {
	e.mu.RLock()
	defer e.mu.RUnlock()
	input := decodeInput(req.Input)
	if req.ToolName == "Bash" {
		return e.evalBash(req, input)
	}
	mc := matchContext{cwd: e.cwd, home: e.home}
	for _, r := range e.deny {
		if r.matchesCall(req, input, mc, false) {
			return Decision{Behavior: Deny, Rule: r.String(), ResolvedBy: "rule", Message: fmt.Sprintf("Permission to use %s has been denied by rule %s.", req.ToolName, r)}
		}
	}
	if req.Sensitive != "" {
		if e.mode == ModePlan && !req.ReadOnly {
			return e.planDenial(req.ToolName)
		}
		return Decision{Behavior: Ask, UserOnly: true, Message: req.Sensitive}
	}
	if req.AlwaysAsk {
		if e.mode == ModePlan && !req.ReadOnly {
			return e.planDenial(req.ToolName)
		}
		return Decision{Behavior: Ask, UserOnly: true, Message: fmt.Sprintf("%s requires the user's confirmation for every call.", req.ToolName)}
	}
	for _, r := range e.ask {
		if r.matchesCall(req, input, mc, false) {
			if e.mode == ModeBypassPermissions {
				break
			}
			return Decision{Behavior: Ask, Rule: r.String(), UserOnly: true, Message: fmt.Sprintf("Rule %s requires confirmation.", r)}
		}
	}
	if e.mode == ModeBypassPermissions {
		return Decision{Behavior: Allow, ResolvedBy: "mode"}
	}
	path := req.FilePath
	if path == "" {
		path = e.cwd
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.cwd, path)
	}
	path = filepath.Clean(path)
	allowRule := func(implied bool) (Rule, bool) {
		for _, r := range e.allow {
			if r.matchesCall(req, input, mc, implied) {
				return r, true
			}
		}
		return Rule{}, false
	}

	switch {
	case isWebTool(req.ToolName):
		return e.evalWeb(req, input, mc)
	case readTools[req.ToolName] || (pathObservers[req.ToolName] && req.FilePath != ""):
		if inAny(path, e.workDirs) || inAny(path, e.scratch) || inAny(path, e.readable) {
			return Decision{Behavior: Allow, ResolvedBy: "mode"}
		}
		if r, ok := allowRule(true); ok {
			return Decision{Behavior: Allow, Rule: r.String(), ResolvedBy: "rule"}
		}
		return Decision{Behavior: Ask, Message: fmt.Sprintf("%s is outside the working directories.", path)}
	case req.ReadOnly:
		return Decision{Behavior: Allow, ResolvedBy: "mode"}
	}
	if e.mode == ModePlan {
		return e.planDenial(req.ToolName)
	}
	if sessionTools[req.ToolName] {
		return Decision{Behavior: Allow, ResolvedBy: "mode"}
	}
	if isBrowserTool(req.ToolName) {
		return e.evalBrowser(req, input, mc)
	}
	if editTools[req.ToolName] {
		if isProtected(path, e.workDirs, e.home) {
			return Decision{Behavior: Ask, UserOnly: true, Message: fmt.Sprintf("%s is a protected path; edits always need approval.", path)}
		}
		if inAny(path, e.scratch) {
			return Decision{Behavior: Allow, ResolvedBy: "mode"}
		}
		if r, ok := allowRule(false); ok {
			return Decision{Behavior: Allow, Rule: r.String(), ResolvedBy: "rule"}
		}
		if e.mode == ModeAcceptEdits && inAny(path, e.workDirs) {
			return Decision{Behavior: Allow, ResolvedBy: "mode"}
		}
		return Decision{Behavior: Ask}
	}
	if r, ok := allowRule(false); ok {
		return Decision{Behavior: Allow, Rule: r.String(), ResolvedBy: "rule"}
	}
	return Decision{Behavior: Ask}
}

// bashCommands returns the normalized subcommands of a command line plus
// those inside substitutions (for deny / ask matching).
func bashCommands(cmd string, depth int) (top []SimpleCommand, all []SimpleCommand) {
	for _, c := range SplitCommand(cmd) {
		n := c.Normalize()
		top = append(top, n)
		all = append(all, n, c)
		if depth < 3 {
			for _, s := range c.Substitutions {
				_, inner := bashCommands(s, depth+1)
				all = append(all, inner...)
			}
		}
	}
	return
}

// bashRuleMatches matches a Bash rule against one subcommand. Bare and
// param rules are matched by wholeBashRule.
func bashRuleMatches(r Rule, c SimpleCommand) bool {
	if r.Spec == "" || !r.toolMatches("Bash", false) {
		return false
	}
	return matchBashSpec(r.Spec, c.Text())
}

// wholeBashRule reports whether a rule covers the whole call: a bare Bash
// rule, a tool glob or a param:value rule.
func wholeBashRule(r Rule, input map[string]any) bool {
	if !r.toolMatches("Bash", false) {
		return false
	}
	if r.Spec == "" {
		return true
	}
	if k, v, ok := paramSpec("Bash", r.Spec, input); ok && k != "command" {
		return globMatch(v, valueString(input[k]))
	}
	return false
}

func (e *Engine) evalBash(req Request, input map[string]any) Decision {
	top, all := bashCommands(req.Command, 0)
	for _, r := range e.deny {
		if wholeBashRule(r, input) {
			return Decision{Behavior: Deny, Rule: r.String(), ResolvedBy: "rule", Message: fmt.Sprintf("Permission to use Bash has been denied by rule %s.", r)}
		}
		for _, c := range all {
			if bashRuleMatches(r, c) {
				return Decision{Behavior: Deny, Rule: r.String(), ResolvedBy: "rule", Message: fmt.Sprintf("Permission to use Bash with command %s has been denied by rule %s.", c.Text(), r)}
			}
		}
	}
	if e.mode != ModeBypassPermissions {
		for _, r := range e.ask {
			if wholeBashRule(r, input) {
				return Decision{Behavior: Ask, Rule: r.String(), UserOnly: true, Message: fmt.Sprintf("Rule %s requires confirmation.", r)}
			}
			for _, c := range all {
				if bashRuleMatches(r, c) {
					return Decision{Behavior: Ask, Rule: r.String(), UserOnly: true, Message: fmt.Sprintf("Rule %s requires confirmation.", r)}
				}
			}
		}
	}
	if e.mode == ModeBypassPermissions {
		return Decision{Behavior: Allow, ResolvedBy: "mode"}
	}
	if len(top) == 0 {
		return Decision{Behavior: Ask}
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd = e.cwd
	}
	// ask is the fall-through Ask; in auto mode writes to protected paths
	// (redirections, cp/mv/tee/sed -i targets) are reserved for the user.
	ask := func() Decision {
		if e.mode == ModeAuto {
			for _, p := range CommandWritePaths(req.Command, cwd, e.home) {
				if isProtected(p, e.workDirs, e.home) {
					return Decision{Behavior: Ask, UserOnly: true, Message: fmt.Sprintf("%s is a protected path; writes always need approval.", p)}
				}
			}
		}
		return Decision{Behavior: Ask}
	}
	readOnly := true
	for _, c := range top {
		if !isReadOnlyCommand(c) {
			readOnly = false
			break
		}
	}
	if readOnly {
		return Decision{Behavior: Allow, ResolvedBy: "mode"}
	}
	if e.mode == ModePlan {
		return e.planDenial("Bash (" + req.Command + ")")
	}
	for _, r := range e.allow {
		if wholeBashRule(r, input) {
			return Decision{Behavior: Allow, Rule: r.String(), ResolvedBy: "rule"}
		}
	}
	var rule string
	for _, c := range top {
		if e.subcommandAllowed(c, cwd, &rule) {
			continue
		}
		return ask()
	}

	return Decision{Behavior: Allow, Rule: rule, ResolvedBy: "rule"}
}

func (e *Engine) subcommandAllowed(c SimpleCommand, cwd string, rule *string) bool {
	if isReadOnlyCommand(c) {
		return true
	}
	if c.Unsafe {
		return false
	}
	for _, r := range e.allow {
		if bashRuleMatches(r, c) {
			*rule = r.String()
			return true
		}
	}
	return e.mode == ModeAcceptEdits && e.acceptEditsCommand(c, cwd, e.workDirs)
}

// Suggest returns the rules offered for "always allow" (up to 5 for
// compound Bash commands).
func (e *Engine) Suggest(req Request) []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	switch {
	case req.ToolName == "Bash":
		top, _ := bashCommands(req.Command, 0)
		cwd := req.Cwd
		if cwd == "" {
			cwd = e.cwd
		}
		var out []string
		seen := map[string]bool{}
		for _, c := range top {
			var r string
			if c.Unsafe || e.subcommandAllowed(c, cwd, &r) {
				continue
			}
			s := "Bash(" + commandPrefix(c) + ")"
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
			if len(out) == 5 {
				break
			}
		}
		return out
	case editTools[req.ToolName] || readTools[req.ToolName]:
		p := req.FilePath
		if p == "" {
			return []string{req.ToolName}
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(e.cwd, p)
		}
		dir := filepath.Dir(filepath.Clean(p))
		tool := "Read"
		if editTools[req.ToolName] {
			tool = "Edit"
		}
		if within(dir, e.localRoot) {
			rel, _ := filepath.Rel(e.localRoot, dir)
			if rel == "." {
				return []string{tool + "(/**)"}
			}
			return []string{tool + "(/" + filepath.ToSlash(rel) + "/**)"}
		}
		return []string{tool + "(/" + filepath.ToSlash(dir) + "/**)"}
	case isWebTool(req.ToolName):
		return suggestWeb(req)
	case req.Sensitive != "":
		return nil
	case isBrowserTool(req.ToolName):
		return suggestBrowser(req)
	}
	return []string{req.ToolName}
}

// subcommandTools take a subcommand that belongs in the rule prefix.
var subcommandTools = map[string]bool{
	"git": true, "npm": true, "pnpm": true, "yarn": true, "bun": true, "go": true, "cargo": true,
	"docker": true, "kubectl": true, "gh": true, "make": true, "pip": true, "pip3": true, "uv": true,
	"poetry": true, "dotnet": true, "mvn": true, "gradle": true, "brew": true, "npx": true, "bunx": true,
	"deno": true, "terraform": true, "helm": true, "rustup": true, "composer": true, "mix": true,
}

var runSubcommands = map[string]bool{"run": true, "exec": true, "x": true, "dlx": true, "compose": true}

var interpreters = map[string]bool{"python": true, "python3": true, "node": true, "ruby": true, "bash": true, "sh": true, "zsh": true, "perl": true, "php": true}

// commandPrefix proposes a prefix rule such as "npm run test *".
func commandPrefix(c SimpleCommand) string {
	ws := c.Words
	if len(ws) == 0 {
		return ""
	}
	parts := []string{ws[0].Raw}
	rest := ws[1:]
	name := filepath.Base(ws[0].Value)
	switch {
	case subcommandTools[name]:
		if len(rest) > 0 && !strings.HasPrefix(rest[0].Value, "-") {
			parts = append(parts, rest[0].Raw)
			if runSubcommands[rest[0].Value] && len(rest) > 1 && !strings.HasPrefix(rest[1].Value, "-") {
				parts = append(parts, rest[1].Raw)
			}
		}
	case interpreters[name]:
		if len(rest) > 1 && rest[0].Value == "-m" {
			parts = append(parts, rest[0].Raw, rest[1].Raw)
		} else if len(rest) > 0 && !strings.HasPrefix(rest[0].Value, "-") {
			parts = append(parts, rest[0].Raw)
		}
	}
	return strings.Join(parts, " ") + " *"
}
