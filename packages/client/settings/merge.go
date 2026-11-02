package settings

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

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
		l.availableModels = s.stringList(src, "availableModels", raw["availableModels"])
		if b := s.boolean(src, raw, "allowManagedPermissionRulesOnly"); b != nil {
			l.managedRulesOnly = *b
		}
	}
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
	}
	gated := func(l *layer) bool { return l.src.Level == LevelProject && !s.Trusted }

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
