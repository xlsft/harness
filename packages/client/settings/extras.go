package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
)

// Keys of the settings this file adds.
const (
	KeyPermissionProfile  = "permissionProfile"
	KeyPermissionProfiles = "permissionProfiles"
	KeyShellEnvironment   = "shellEnvironmentPolicy"
	KeyBashMemoryLimit    = "bashMemoryLimitMB"
	KeyWebSearchMode      = "webSearchMode"
)

// Modes of WebSearch (webSearchMode).
const (
	WebSearchLive     = "live"
	WebSearchCached   = "cached"
	WebSearchDisabled = "disabled"
)

// ShellEnvironment is shellEnvironmentPolicy: what the model's commands see of the client's environment.
type ShellEnvironment struct {
	// Inherit is "all" (default), "core" (a short list: PATH, HOME and the like) or "none".
	Inherit string `json:"inherit"`
	// Exclude takes names away, by glob; a variable that looks like a secret goes with IgnoreDefaultExcludes false.
	Exclude []string `json:"exclude"`
	// Include keeps only names matching a glob, of what Inherit and Exclude left. Empty keeps all.
	Include []string `json:"include"`
	// ExcludeSecrets drops names that look like credentials (*KEY*, *SECRET*, *TOKEN*, *PASSWORD*, *CREDENTIAL*).
	ExcludeSecrets bool `json:"excludeSecrets"`
	// Set is added last, and wins.
	Set map[string]string `json:"set"`
}

// Active reports whether the policy changes anything.
func (p *ShellEnvironment) Active() bool {
	return p != nil && (p.Inherit != "" && p.Inherit != "all" || len(p.Exclude) > 0 || len(p.Include) > 0 || p.ExcludeSecrets || len(p.Set) > 0)
}

var coreEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TEMP", "TMP", "LANG", "LC_*", "TERM", "TZ", "PWD", "SYSTEMROOT", "COMSPEC", "PATHEXT", "WINDIR", "USERPROFILE", "PROGRAMDATA", "APPDATA", "LOCALAPPDATA"}

var secretEnv = []string{"*KEY*", "*SECRET*", "*TOKEN*", "*PASSWORD*", "*PASSWD*", "*CREDENTIAL*"}

func envMatch(patterns []string, name string) bool {
	upper := strings.ToUpper(name)
	for _, p := range patterns {
		if ok, _ := path.Match(strings.ToUpper(p), upper); ok {
			return true
		}
	}
	return false
}

// Filter applies the policy to NAME=value entries. A nil policy leaves them as they are.
func (p *ShellEnvironment) Filter(environ []string) []string {
	if !p.Active() {
		return environ
	}
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch {
		case p.Inherit == "none", p.Inherit == "core" && !envMatch(coreEnv, name):
			continue
		case envMatch(p.Exclude, name), p.ExcludeSecrets && envMatch(secretEnv, name):
			continue
		case len(p.Include) > 0 && !envMatch(p.Include, name):
			continue
		}
		out = append(out, kv)
	}
	names := make([]string, 0, len(p.Set))
	for k := range p.Set {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		out = append(out, k+"="+p.Set[k])
	}
	return out
}

// FilterOrNil is Filter as a function, nil when the policy changes nothing.
func (p *ShellEnvironment) FilterOrNil() func([]string) []string {
	if !p.Active() {
		return nil
	}
	return p.Filter
}

// Extras are the settings resolved after the merge, as what each level may say differs from the rest.
type Extras struct {
	// RestrictReads: permissions.restrictReads. Reads outside the working directories are refused, not asked about. Any
	// level can turn it on, as it only takes away; none can turn off what another turned on.
	RestrictReads bool
	// ShellEnvironment is the highest level that sets it; a committed project file counts only in a trusted workspace, as
	// its set adds to what commands run with.
	ShellEnvironment *ShellEnvironment
	// BashMemoryLimitMB caps the address space of a command on Linux (0: none). The smallest of the levels wins.
	BashMemoryLimitMB int
	// WebSearchMode is live (default), cached or disabled. Any level can disable it; a policy's value stands.
	WebSearchMode string
	// PermissionProfile is the profile in force ("": none), and PermissionProfileNames those that exist.
	PermissionProfile      string
	PermissionProfileNames []string
}

// applyExtras resolves Extras from the layers (highest precedence first).
func (s *Settings) applyExtras(layers []*layer) {
	for _, l := range layers {
		gated := l.src.Level == LevelProject && !s.Trusted
		if v, ok := l.raw["permissions"]; ok {
			var p struct {
				RestrictReads bool `json:"restrictReads"`
			}
			if json.Unmarshal(v, &p) == nil && p.RestrictReads {
				s.Extras.RestrictReads = true
			}
		}
		if v, ok := l.raw[KeyShellEnvironment]; ok && s.Extras.ShellEnvironment == nil {
			switch {
			case gated:
				s.ignored(l.src, KeyShellEnvironment, "ignored until the workspace is trusted")
			default:
				var p ShellEnvironment
				if err := json.Unmarshal(v, &p); err != nil {
					s.problem(l.src, KeyShellEnvironment, "must be an object: %v", err)
				} else if p.Inherit != "" && p.Inherit != "all" && p.Inherit != "core" && p.Inherit != "none" {
					s.problem(l.src, KeyShellEnvironment, "inherit must be all, core or none")
				} else {
					s.Extras.ShellEnvironment = &p
				}
			}
		}
		if n := s.integer(l.src, l.raw, KeyBashMemoryLimit); n != nil {
			switch {
			case *n <= 0:
				s.problem(l.src, KeyBashMemoryLimit, "must be a positive number of megabytes")
			case s.Extras.BashMemoryLimitMB == 0 || *n < s.Extras.BashMemoryLimitMB:
				s.Extras.BashMemoryLimitMB = *n
			}
		}
		if v, ok := l.raw[KeyWebSearchMode]; ok {
			var m string
			switch {
			case json.Unmarshal(v, &m) != nil || (m != WebSearchLive && m != WebSearchCached && m != WebSearchDisabled):
				s.problem(l.src, KeyWebSearchMode, "must be live, cached or disabled")
			case m == WebSearchDisabled:
				s.Extras.WebSearchMode = m
			case s.Extras.WebSearchMode == "":
				s.Extras.WebSearchMode = m
			}
		}
	}
	if s.Extras.WebSearchMode == "" {
		s.Extras.WebSearchMode = WebSearchLive
	}
}

// profileLayers finds the profile in force and puts a layer of it right above the level that defines it, so it is gated
// and merged as that level would be: a project's profile allows nothing in an untrusted folder, and rule lists add up.
func (s *Settings) profileLayers(layers []*layer, wanted string) []*layer {
	names := map[string]bool{}
	name := strings.TrimSpace(wanted)
	for _, l := range layers {
		if name == "" {
			if v, ok := l.raw[KeyPermissionProfile]; ok {
				_ = json.Unmarshal(v, &name)
			}
		}
		if v, ok := l.raw[KeyPermissionProfiles]; ok {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(v, &m); err != nil {
				s.problem(l.src, KeyPermissionProfiles, "must be an object of profiles by name")
				continue
			}
			for n := range m {
				names[n] = true
			}
		}
	}
	for n := range names {
		s.Extras.PermissionProfileNames = append(s.Extras.PermissionProfileNames, n)
	}
	sort.Strings(s.Extras.PermissionProfileNames)
	if name == "" {
		return layers
	}
	for i, l := range layers {
		v, ok := l.raw[KeyPermissionProfiles]
		if !ok {
			continue
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(v, &m) != nil || m[name] == nil {
			continue
		}
		wrapped, err := json.Marshal(map[string]json.RawMessage{"permissions": m[name]})
		if err != nil {
			continue
		}
		pl := s.parse(l.src, wrapped)
		if pl == nil {
			continue
		}
		s.Extras.PermissionProfile = name
		return slices.Insert(slices.Clone(layers), i, pl)
	}
	s.problem(Source{Level: LevelCLI}, KeyPermissionProfile, "there is no permission profile %q (profiles: %s)", name, strings.Join(s.Extras.PermissionProfileNames, ", "))
	return layers
}

// Feature is one switch of the client, for the list a user can check against.
type Feature struct {
	Name string
	// On: it works in a session started now, as far as this client's environment and settings go.
	On bool
	// Switch is what turns it off or on.
	Switch      string
	Description string
}

// Features lists the client's switches, from the environment and these settings (nil: environment only).
func Features(s *Settings) []Feature {
	env := func(name string) bool { return os.Getenv(name) != "1" }
	flag := func(pick func(*Settings) bool) bool { return s == nil || pick(s) }
	list := []Feature{
		{"hooks", env("HARNESS_NO_HOOKS") && flag(func(s *Settings) bool { return !s.HooksOff }), "HARNESS_NO_HOOKS=1, disableAllHooks", "Run hooks from settings"},
		{"mcp", env("HARNESS_NO_MCP"), "HARNESS_NO_MCP=1", "Connect MCP servers"},
		{"plugins", env("HARNESS_NO_PLUGINS"), "HARNESS_NO_PLUGINS=1", "Load installed plugins"},
		{"skills", env("HARNESS_NO_SKILLS"), "HARNESS_NO_SKILLS=1", "Discover skills and custom commands"},
		{"instructions", env("HARNESS_NO_INSTRUCTIONS"), "HARNESS_NO_INSTRUCTIONS=1", "Load AGENTS.md and rules"},
		{"auto-memory", env("HARNESS_NO_INSTRUCTIONS") && flag(func(s *Settings) bool { return s.AutoMemoryEnabled }), "autoMemoryEnabled", "Keep and load memory per project"},
		{"sync", env("HARNESS_NO_SYNC"), "HARNESS_NO_SYNC=1", "Sync settings and sessions with the server"},
		{"vcs", env("HARNESS_NO_VCS"), "HARNESS_NO_VCS=1", "Follow pull requests and CI"},
		{"browser", env("HARNESS_NO_BROWSER"), "HARNESS_NO_BROWSER=1", "Browser tools"},
		{"computer-use", env("HARNESS_NO_COMPUTER_USE"), "HARNESS_NO_COMPUTER_USE=1", "Computer tools"},
		{"local-models", env("HARNESS_NO_LOCAL_MODELS"), "HARNESS_NO_LOCAL_MODELS=1", "Local model runtimes"},
		{"shell-snapshot", env("HARNESS_NO_SHELL_SNAPSHOT"), "HARNESS_NO_SHELL_SNAPSHOT=1", "Commands start from a snapshot of the login shell"},
		{"interrupt-reply", env("HARNESS_NO_INTERRUPT_REPLY") && flag(func(s *Settings) bool { return s.InterruptReply }), "HARNESS_NO_INTERRUPT_REPLY=1, interruptReply", "A short reply when a turn is interrupted"},
		{"lsp", flag(func(s *Settings) bool { return s.LSPEnabled }), "lspEnabled", "LSP tool and diagnostics after edits"},
		{"idle-compaction", flag(func(s *Settings) bool { return s.CompactIdleSessions }), KeyCompactIdleSessions, "Compact idle sessions in the background"},
		{"sandbox", flag(func(s *Settings) bool { return s.Sandbox.Enabled }), "sandbox.enabled", "Run the model's commands in the OS sandbox"},
		{"web-search", flag(func(s *Settings) bool { return s.Extras.WebSearchMode != WebSearchDisabled }), KeyWebSearchMode, "WebSearch tool"},
		{"remote-control", flag(func(s *Settings) bool { return !s.RemoteControlPolicyOff }), "HARNESS_REMOTE_CONTROL=0, remoteControl in a policy", "Mirror sessions to the server"},
	}
	if os.Getenv("HARNESS_REMOTE_CONTROL") == "0" {
		for i := range list {
			if list[i].Name == "remote-control" {
				list[i].On = false
			}
		}
	}
	return list
}

// FeatureReport is Features as text.
func FeatureReport(s *Settings) string {
	var b strings.Builder
	for _, f := range Features(s) {
		state := "on "
		if !f.On {
			state = "off"
		}
		fmt.Fprintf(&b, "%s  %-16s %s (%s)\n", state, f.Name, f.Description, f.Switch)
	}
	return strings.TrimRight(b.String(), "\n")
}

// DebugReport says where a session's settings come from: the sources in order, the values in force, and what was refused or
// wrong. It prints names and rules but never a value of env, which may hold secrets.
func (s *Settings) DebugReport() string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	line("Sources, highest precedence first:")
	if len(s.Sources) == 0 {
		line("  (none)")
	}
	for _, src := range s.Sources {
		line("  %s", src)
	}
	line("Workspace trusted: %v. Synced with the server: %v.", s.Trusted, s.Synced)
	if s.Permissions.DefaultMode != "" {
		line("Default mode: %s (%s)", s.Permissions.DefaultMode, s.Permissions.DefaultModeSource)
	}
	if s.Extras.PermissionProfile != "" {
		line("Permission profile: %s", s.Extras.PermissionProfile)
	}
	if len(s.Extras.PermissionProfileNames) > 0 {
		line("Profiles: %s", strings.Join(s.Extras.PermissionProfileNames, ", "))
	}
	if s.Model != "" {
		line("Model: %s", s.Model)
	}
	if s.EffortLevel != "" {
		line("Effort: %s", s.EffortLevel)
	}
	if s.OutputStyle != "" {
		line("Output style: %s", s.OutputStyle)
	}
	for _, list := range []struct {
		name  string
		rules []Rule
	}{{"Deny", s.Permissions.Deny}, {"Ask", s.Permissions.Ask}, {"Allow", s.Permissions.Allow}} {
		if len(list.rules) == 0 {
			continue
		}
		line("%s rules:", list.name)
		for _, r := range list.rules {
			line("  %s (%s)", r.Rule, r.Source)
		}
	}
	if len(s.Permissions.AdditionalDirectories) > 0 {
		line("Additional directories:")
		for _, d := range s.Permissions.AdditionalDirectories {
			line("  %s (%s)", d.Abs, d.Source)
		}
	}
	if len(s.Permissions.DisabledModes) > 0 {
		line("Modes turned off by policy: %s", strings.Join(s.Permissions.DisabledModes, ", "))
	}
	line("Sandbox: enabled %v, required by policy %v.", s.Sandbox.Enabled, s.Sandbox.Forced)
	line("Reads outside working directories: %s.", map[bool]string{true: "refused", false: "asked about"}[s.Extras.RestrictReads])
	if s.Extras.ShellEnvironment.Active() {
		p := s.Extras.ShellEnvironment
		line("Command environment: inherit %q, %d exclude, %d include, excludeSecrets %v, %d set.", p.Inherit, len(p.Exclude), len(p.Include), p.ExcludeSecrets, len(p.Set))
	}
	if s.Extras.BashMemoryLimitMB > 0 {
		line("Command memory limit: %d MB.", s.Extras.BashMemoryLimitMB)
	}
	line("Web search: %s.", s.Extras.WebSearchMode)
	if s.SpeedTier != "" || s.CacheTTL != "" {
		line("Speed tier: %q. Prompt cache lifetime: %q.", s.SpeedTier, s.CacheTTL)
	}
	if len(s.Env) > 0 {
		names := make([]string, 0, len(s.Env))
		for k := range s.Env {
			names = append(names, k)
		}
		sort.Strings(names)
		line("Environment set, values not shown: %s", strings.Join(names, ", "))
	}
	if len(s.Hooks) > 0 {
		line("Hooks:")
		for _, h := range s.Hooks {
			line("  %s %s", h.Source, hookGateName(h.Gate))
		}
	}
	if len(s.Problems) > 0 {
		line("Problems:")
		for _, p := range s.Problems {
			line("  %s", p.Error())
		}
	}
	if len(s.Ignored) > 0 {
		line("Ignored:")
		for _, p := range s.Ignored {
			line("  %s", p.Error())
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func hookGateName(g HookGate) string {
	switch g {
	case HookGateWorkspace:
		return "(waits for the workspace to be trusted)"
	case HookGateDevice:
		return "(from the server: runs once trusted on this device)"
	case HookGateManaged:
		return "(off: policy allows managed hooks only)"
	}
	return "(runs)"
}
