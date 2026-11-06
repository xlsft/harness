package settings

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/memory"
)

// parse decodes one settings JSON object into a layer. It returns nil (and
// records a problem) when the content is not a JSON object.
func (s *Settings) parse(src Source, data []byte) *layer {
	if len(bytes.TrimSpace(data)) == 0 {
		return &layer{src: src}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		s.problem(src, "", "invalid JSON: %v", err)
		return nil
	}
	l := &layer{src: src, raw: raw}
	l.model = s.str(src, raw, "model")
	if l.effort = s.str(src, raw, "effortLevel"); l.effort != nil && !effortLevels[*l.effort] {
		s.problem(src, "effortLevel", "unknown effort level %q", *l.effort)
		l.effort = nil
	}
	l.outputStyle = s.str(src, raw, "outputStyle")
	if l.claudeMdMode = s.str(src, raw, "claudeMdMode"); l.claudeMdMode != nil && !memory.ValidMode(*l.claudeMdMode) {
		s.problem(src, "claudeMdMode", "unknown mode %q", *l.claudeMdMode)
		l.claudeMdMode = nil
	}
	if l.autoCompactWindow = s.integer(src, raw, "autoCompactWindow"); l.autoCompactWindow != nil && *l.autoCompactWindow <= 0 {
		s.problem(src, "autoCompactWindow", "must be positive")
		l.autoCompactWindow = nil
	}
	if l.cleanupPeriodDays = s.integer(src, raw, "cleanupPeriodDays"); l.cleanupPeriodDays != nil && *l.cleanupPeriodDays < 0 {
		s.problem(src, "cleanupPeriodDays", "must not be negative")
		l.cleanupPeriodDays = nil
	}
	l.includeGit = s.boolean(src, raw, "includeGitInstructions")
	l.interruptReply = s.boolean(src, raw, "interruptReply")
	l.compactIdle = s.boolean(src, raw, KeyCompactIdleSessions)
	l.lspEnabled = s.boolean(src, raw, KeyLSPEnabled)
	l.lspDiagnostics = s.boolean(src, raw, KeyLSPDiagnostics)
	l.lspStartServer = s.boolean(src, raw, KeyLSPDiagnosticsStartServer)
	if v, ok := raw[KeyLSPServers]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &l.lspServers); err != nil {
			s.problem(src, KeyLSPServers, "must be an object of server settings by language id: %v", err)
			l.lspServers = nil
		}
	}
	if l.prWakes = s.integer(src, raw, KeyPRMonitorWakesPerHour); l.prWakes != nil && !ValidPRMonitorWakesPerHour(*l.prWakes) {
		s.problem(src, KeyPRMonitorWakesPerHour, "must be between 1 and %d", MaxPRMonitorWakesPerHour)
		l.prWakes = nil
	}
	if l.subagentDepth = s.integer(src, raw, KeySubagentMaxDepth); l.subagentDepth != nil && !ValidSubagentMaxDepth(*l.subagentDepth) {
		s.problem(src, KeySubagentMaxDepth, "must be between 1 and %d", MaxSubagentMaxDepth)
		l.subagentDepth = nil
	}
	if l.subagentRunning = s.integer(src, raw, KeySubagentMaxRunning); l.subagentRunning != nil && !ValidSubagentMaxRunning(*l.subagentRunning) {
		s.problem(src, KeySubagentMaxRunning, "must be between 1 and %d", MaxSubagentMaxRunning)
		l.subagentRunning = nil
	}
	if l.workflowAgents = s.integer(src, raw, KeyWorkflowMaxAgents); l.workflowAgents != nil && !ValidWorkflowMaxAgents(*l.workflowAgents) {
		s.problem(src, KeyWorkflowMaxAgents, "must be between 1 and %d", MaxWorkflowMaxAgents)
		l.workflowAgents = nil
	}
	if v, ok := raw["attribution"]; ok {
		var a map[string]json.RawMessage
		if err := json.Unmarshal(v, &a); err != nil {
			s.problem(src, "attribution", "must be an object")
		} else {
			l.attrCommit = s.str(src, a, "commit")
			l.attrPR = s.str(src, a, "pr")
		}
	}
	if v, ok := raw["env"]; ok {
		var env map[string]any
		if err := json.Unmarshal(v, &env); err != nil {
			s.problem(src, "env", "must be an object of strings")
		} else {
			l.env = map[string]string{}
			for k, val := range env {
				switch x := val.(type) {
				case string:
					l.env[k] = x
				case float64:
					l.env[k] = strconv.FormatFloat(x, 'f', -1, 64)
				case bool:
					l.env[k] = strconv.FormatBool(x)
				default:
					s.problem(src, "env."+k, "must be a string")
				}
			}
		}
	}
	if v, ok := raw["permissions"]; ok {
		var p map[string]json.RawMessage
		if err := json.Unmarshal(v, &p); err != nil {
			s.problem(src, "permissions", "must be an object")
		} else {
			l.allow = s.stringList(src, "permissions.allow", p["allow"])
			l.ask = s.stringList(src, "permissions.ask", p["ask"])
			l.deny = s.stringList(src, "permissions.deny", p["deny"])
			l.addDirs = s.stringList(src, "permissions.additionalDirectories", p["additionalDirectories"])
			if l.defaultMode = s.str(src, p, "defaultMode"); l.defaultMode != nil && !modes[*l.defaultMode] {
				s.problem(src, "permissions.defaultMode", "unknown permission mode %q", *l.defaultMode)
				l.defaultMode = nil
			}
			if d := s.str(src, p, "disableBypassPermissionsMode"); d != nil && *d == "disable" {
				l.disableBypass = true
			}
			if disableFlag(p["disableAutoMode"]) {
				l.disableAuto = true
			}
		}
	}
	if disableFlag(raw["disableAutoMode"]) {
		l.disableAuto = true
	}
	if src.Level == LevelPolicy {
		if v, ok := raw["permissions"]; ok {
			var p map[string]json.RawMessage
			if json.Unmarshal(v, &p) == nil {
				for _, m := range s.stringList(src, "permissions.disabledModes", p["disabledModes"]) {
					switch {
					case !modes[m]:
						s.problem(src, "permissions.disabledModes", "unknown permission mode %q", m)
					case m == "default":
						s.problem(src, "permissions.disabledModes", "the default mode cannot be disabled")
					default:
						l.disabledModes = append(l.disabledModes, m)
					}
				}
			}
		}
		l.availableModels = s.stringList(src, "availableModels", raw["availableModels"])
		if b := s.boolean(src, raw, "allowManagedPermissionRulesOnly"); b != nil {
			l.managedRulesOnly = *b
		}
		if b := s.boolean(src, raw, KeyRemoteControl); b != nil && !*b {
			l.remoteControlOff = true
		}
	}
	l.sandbox = s.parseSandbox(src, raw)
	return l
}

func (s *Settings) str(src Source, m map[string]json.RawMessage, key string) *string {
	v, ok := m[key]
	if !ok || string(v) == "null" {
		return nil
	}
	var out string
	if err := json.Unmarshal(v, &out); err != nil {
		s.problem(src, key, "must be a string")
		return nil
	}
	return &out
}

func (s *Settings) integer(src Source, m map[string]json.RawMessage, key string) *int {
	v, ok := m[key]
	if !ok || string(v) == "null" {
		return nil
	}
	var f float64
	if err := json.Unmarshal(v, &f); err != nil || f != float64(int(f)) {
		s.problem(src, key, "must be an integer")
		return nil
	}
	n := int(f)
	return &n
}

func (s *Settings) boolean(src Source, m map[string]json.RawMessage, key string) *bool {
	v, ok := m[key]
	if !ok || string(v) == "null" {
		return nil
	}
	var b bool
	if err := json.Unmarshal(v, &b); err != nil {
		s.problem(src, key, "must be a boolean")
		return nil
	}
	return &b
}

func (s *Settings) stringList(src Source, key string, v json.RawMessage) []string {
	if len(v) == 0 || string(v) == "null" {
		return nil
	}
	var items []any
	if err := json.Unmarshal(v, &items); err != nil {
		s.problem(src, key, "must be an array of strings")
		return nil
	}
	var out []string
	for i, it := range items {
		str, ok := it.(string)
		if !ok || strings.TrimSpace(str) == "" {
			s.problem(src, key+"["+strconv.Itoa(i)+"]", "must be a non-empty string")
			continue
		}
		out = append(out, strings.TrimSpace(str))
	}
	return out
}

// flatten expands extra layers right after their parent.
func flatten(layers []*layer) []*layer {
	var out []*layer
	for _, l := range layers {
		out = append(out, l)
		out = append(out, l.extra...)
	}
	return out
}

// merge combines layers (highest precedence first) into s.
func (s *Settings) merge(layers []*layer, cwd, home string) {
	managedOnly := false
	for _, l := range layers {
		if l.src.Level == LevelPolicy && l.managedRulesOnly {
			managedOnly = true
		}
		if l.src.Level == LevelPolicy && l.remoteControlOff {
			s.RemoteControlPolicyOff = true
		}
	}
	gated := func(l *layer) bool { return l.src.Level == LevelProject && !s.Trusted }
	// Language server settings gate the local project files too: a repository can commit those as well.
	projectFile := func(l *layer) bool { return l.src.Level == LevelProject || l.src.Level == LevelLocal }

	// Lists: highest precedence first.
	for _, l := range layers {
		if managedOnly && l.src.Level != LevelPolicy && len(l.allow)+len(l.ask)+len(l.deny) > 0 {
			s.ignored(l.src, "permissions", "permission rules ignored: policy allows managed rules only")
		} else {
			if gated(l) && len(l.allow) > 0 {
				s.ignored(l.src, "permissions.allow", "ignored until the workspace is trusted")
			} else {
				s.Permissions.Allow = appendRules(s.Permissions.Allow, l.allow, l.src)
			}
			s.Permissions.Ask = appendRules(s.Permissions.Ask, l.ask, l.src)
			s.Permissions.Deny = appendRules(s.Permissions.Deny, l.deny, l.src)
		}
		if gated(l) && len(l.addDirs) > 0 {
			s.ignored(l.src, "permissions.additionalDirectories", "ignored until the workspace is trusted")
		} else {
			for _, d := range l.addDirs {
				abs := resolveDir(d, l.src.Dir, cwd, home)
				if !slices.ContainsFunc(s.Permissions.AdditionalDirectories, func(x Dir) bool { return x.Abs == abs }) {
					s.Permissions.AdditionalDirectories = append(s.Permissions.AdditionalDirectories, Dir{Path: d, Abs: abs, Source: l.src})
				}
			}
		}
		if l.disableBypass {
			if l.src.Level == LevelPolicy {
				s.Permissions.DisableBypass = true
			}
		}
		for _, m := range l.disabledModes {
			if !slices.Contains(s.Permissions.DisabledModes, m) {
				s.Permissions.DisabledModes = append(s.Permissions.DisabledModes, m)
			}
		}
		// Turning auto mode off only restricts, so every level may do it.
		if l.disableAuto {
			s.Permissions.DisableAutoMode = true
		}
		if l.src.Level == LevelPolicy && len(l.availableModels) > 0 && len(s.AvailableModels) == 0 {
			s.AvailableModels = l.availableModels
		}
	}

	// Scalars: lowest precedence first, later layers override.
	for i := len(layers) - 1; i >= 0; i-- {
		l := layers[i]
		set := func(dst *string, v *string) {
			if v != nil {
				*dst = *v
			}
		}
		set(&s.Model, l.model)
		set(&s.EffortLevel, l.effort)
		set(&s.OutputStyle, l.outputStyle)
		if l.claudeMdMode != nil {
			s.ClaudeMdMode = memory.Mode(*l.claudeMdMode)
		}
		if l.autoCompactWindow != nil {
			s.AutoCompactWindow = *l.autoCompactWindow
		}
		if l.cleanupPeriodDays != nil {
			s.CleanupPeriodDays = *l.cleanupPeriodDays
		}
		if l.includeGit != nil {
			s.IncludeGitInstructions = *l.includeGit
		}
		if l.interruptReply != nil {
			s.InterruptReply = *l.interruptReply
		}
		if l.compactIdle != nil {
			s.CompactIdleSessions = *l.compactIdle
		}
		lspSwitch := func(dst *bool, v *bool, key string) {
			switch {
			case v == nil:
			case *v && projectFile(l):
				s.ignored(l.src, key, "only the user's own settings can turn this on")
			default:
				*dst = *v
			}
		}
		lspSwitch(&s.LSPEnabled, l.lspEnabled, KeyLSPEnabled)
		lspSwitch(&s.LSPDiagnostics, l.lspDiagnostics, KeyLSPDiagnostics)
		lspSwitch(&s.LSPDiagnosticsStartServer, l.lspStartServer, KeyLSPDiagnosticsStartServer)
		if len(l.lspServers) > 0 {
			if projectFile(l) && !s.Trusted {
				s.ignored(l.src, KeyLSPServers, "ignored until the workspace is trusted")
			} else {
				if s.LSPServers == nil {
					s.LSPServers = map[string]LSPServer{}
				}
				for id, srv := range l.lspServers {
					s.LSPServers[id] = srv
				}
			}
		}
		if l.prWakes != nil {
			s.PRMonitorWakesPerHour = *l.prWakes
		}
		if l.subagentDepth != nil {
			s.SubagentMaxDepth = *l.subagentDepth
		}
		if l.subagentRunning != nil {
			s.SubagentMaxRunning = *l.subagentRunning
		}
		if l.workflowAgents != nil {
			s.WorkflowMaxAgents = *l.workflowAgents
		}
		if l.attrCommit != nil {
			v := *l.attrCommit
			s.Attribution.Commit = &v
		}
		if l.attrPR != nil {
			v := *l.attrPR
			s.Attribution.PR = &v
		}
		if l.defaultMode != nil {
			if *l.defaultMode == "auto" && !autoModeLevels[l.src.Level] {
				s.ignored(l.src, "permissions.defaultMode", "auto mode can only be enabled by the user, the command line or policy settings")
			} else if gated(l) && wideningModes[*l.defaultMode] {
				s.ignored(l.src, "permissions.defaultMode", "ignored until the workspace is trusted")
			} else {
				s.Permissions.DefaultMode = *l.defaultMode
				s.Permissions.DefaultModeSource = l.src
			}
		}
		if len(l.env) > 0 {
			if gated(l) {
				s.ignored(l.src, "env", "ignored until the workspace is trusted")
			} else {
				for k, v := range l.env {
					s.Env[k] = v
				}
			}
		}
		for k, v := range l.raw {
			s.Raw[k] = v
		}
	}

	s.mergeSandbox(layers)

	// Policy enforcement after the merge.
	if s.Permissions.DisableBypass && s.Permissions.DefaultMode == "bypassPermissions" {
		s.ignored(s.Permissions.DefaultModeSource, "permissions.defaultMode", "bypassPermissions is disabled by policy")
		s.Permissions.DefaultMode = "default"
		s.Permissions.DefaultModeSource = Source{Level: LevelPolicy}
	}
	if s.Permissions.DisableAutoMode && s.Permissions.DefaultMode == "auto" {
		s.ignored(s.Permissions.DefaultModeSource, "permissions.defaultMode", "auto mode is disabled by settings (disableAutoMode)")
		s.Permissions.DefaultMode = "default"
		s.Permissions.DefaultModeSource = Source{Level: LevelPolicy}
	}
	if slices.Contains(s.Permissions.DisabledModes, s.Permissions.DefaultMode) {
		s.ignored(s.Permissions.DefaultModeSource, "permissions.defaultMode", s.Permissions.DefaultMode+" is disabled by policy (permissions.disabledModes)")
		s.Permissions.DefaultMode = "default"
		s.Permissions.DefaultModeSource = Source{Level: LevelPolicy}
	}
	if len(s.AvailableModels) > 0 && s.Model != "" && !slices.Contains(s.AvailableModels, s.Model) {
		s.ignored(Source{Level: LevelPolicy}, "model", "model "+strconv.Quote(s.Model)+" is not allowed by policy")
		s.Model = s.AvailableModels[0]
	}
}

func appendRules(dst []Rule, rules []string, src Source) []Rule {
	for _, r := range rules {
		dup := false
		for _, x := range dst {
			if x.Rule == r && x.Source == src {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, Rule{Rule: r, Source: src})
		}
	}
	return dst
}

func resolveDir(d, base, cwd, home string) string {
	switch {
	case d == "~" || strings.HasPrefix(d, "~/"):
		return filepath.Join(home, strings.TrimPrefix(d, "~"))
	case filepath.IsAbs(d):
		return filepath.Clean(d)
	case base != "":
		return filepath.Join(base, d)
	default:
		return filepath.Join(cwd, d)
	}
}

// PermissionInputs are the inputs of the permission engine.
type PermissionInputs struct {
	// Rules keep their source; Rule.Source.Dir is the base for relative
	// path specs ("/src/**" in a project file means <project>/src/**).
	Allow, Ask, Deny []Rule
	DefaultMode      string
	// AdditionalDirectories are absolute paths.
	AdditionalDirectories []string
	DisableBypass         bool
	// DisableAutoMode refuses auto mode (disableAutoMode at any level).
	DisableAutoMode bool
	// DisabledModes are the modes policy disables (permissions.disabledModes).
	DisabledModes []string
}

// autoModeLevels may switch auto mode on through defaultMode; committed and
// local project files may not (a repository must not opt its users in).
var autoModeLevels = map[Level]bool{LevelPolicy: true, LevelCLI: true, LevelUser: true, LevelOrg: true}

// disableFlag reads a disable switch: "disable" (Claude Code form) or true.
func disableFlag(v json.RawMessage) bool {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s == "disable"
	}
	var b bool
	return json.Unmarshal(v, &b) == nil && b
}

// PermissionInputs returns the permission engine inputs.
func (s *Settings) PermissionInputs() PermissionInputs {
	in := PermissionInputs{
		Allow:           slices.Clone(s.Permissions.Allow),
		Ask:             slices.Clone(s.Permissions.Ask),
		Deny:            slices.Clone(s.Permissions.Deny),
		DefaultMode:     s.Permissions.DefaultMode,
		DisableBypass:   s.Permissions.DisableBypass,
		DisableAutoMode: s.Permissions.DisableAutoMode,
		DisabledModes:   slices.Clone(s.Permissions.DisabledModes),
	}

	for _, d := range s.Permissions.AdditionalDirectories {
		in.AdditionalDirectories = append(in.AdditionalDirectories, d.Abs)
	}
	return in
}

// RuleStrings returns the rule texts (for engines that do not track sources).
func RuleStrings(rules []Rule) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = r.Rule
	}
	return out
}

// PolicyDisabledModes returns the permission modes the given POLICY documents
// disable (org scope only): permissions.disabledModes, plus bypassPermissions
// for permissions.disableBypassPermissionsMode and auto for disableAutoMode.
// Lists are unioned across documents; the result is sorted and never
// contains "default". It backs the client-wide live mode policy.
func PolicyDisabledModes(docs []*syncv1.Document) []string {
	set := map[string]bool{}
	for _, d := range docs {
		if d.GetScope() != commonv1.Scope_SCOPE_ORG {
			continue
		}
		var raw map[string]json.RawMessage
		if json.Unmarshal([]byte(d.GetContent()), &raw) != nil {
			continue
		}
		var p map[string]json.RawMessage
		_ = json.Unmarshal(raw["permissions"], &p)
		var list []string
		_ = json.Unmarshal(p["disabledModes"], &list)
		for _, m := range list {
			if modes[m] && m != "default" {
				set[m] = true
			}
		}
		var bypass string
		if json.Unmarshal(p["disableBypassPermissionsMode"], &bypass) == nil && bypass == "disable" {
			set["bypassPermissions"] = true
		}
		if disableFlag(p["disableAutoMode"]) || disableFlag(raw["disableAutoMode"]) {
			set["auto"] = true
		}
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}
