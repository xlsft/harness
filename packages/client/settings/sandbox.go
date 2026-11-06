package settings

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

// KeySandbox is the settings key of the sandbox block. The frontends' Settings section writes it into the user's synced
// settings document (UserSettingsKey).
const KeySandbox = "sandbox"

// Sandbox is the sandbox block, merged. Who may say what follows from what each level is:
//
//   - user and org documents: everything, as defaults on every machine.
//   - .harness/settings.local.json, .claude/settings.local.json and --settings: everything. Those are the overrides of one
//     device; a sandboxed command cannot write the local files, and the file tools ask the user first.
//   - committed project files: only turning it on. A repository must not widen its own sandbox, or switch it off.
//   - policy documents: enabled true requires it (Forced), and allowOutside false takes the way around it away. A policy
//     cannot turn it off for users who want it.
//
// Lists add up across the levels that may set them.
type Sandbox struct {
	// Enabled is sandbox.enabled: true unless a level turned it off. On by default because a safety that must be turned on
	// is off where it matters most, unattended and in bypass mode; a refused command says what was refused and how to get
	// past it, so the cost of a wrong guess is one prompt.
	Enabled bool
	Forced  bool
	// AllowedHosts and AllowedWritePaths are added to the built-in ones.
	AllowedHosts      []string
	AllowedWritePaths []string
	AllowLocalhost    bool
	// AutoAllowBash lets sandboxed commands run where they would otherwise ask.
	AutoAllowBash bool
	// OutsideWithoutPrompt drops the user-only prompt of running outside the sandbox.
	OutsideWithoutPrompt bool
	// OutsideRefused: a policy document says "allowOutside": false.
	OutsideRefused bool
}

// sandboxLayer is the sandbox block of one source.
type sandboxLayer struct {
	enabled, allowLocalhost, autoAllow, outsideNoPrompt, allowOutside *bool
	hosts, paths                                                      []string
}

func (s *Settings) parseSandbox(src Source, raw map[string]json.RawMessage) *sandboxLayer {
	v, ok := raw[KeySandbox]
	if !ok || string(v) == "null" {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(v, &m); err != nil {
		s.problem(src, KeySandbox, "must be an object")
		return nil
	}
	l := &sandboxLayer{
		enabled:         s.boolean(src, m, "enabled"),
		allowLocalhost:  s.boolean(src, m, "allowLocalhost"),
		autoAllow:       s.boolean(src, m, "autoAllowBash"),
		outsideNoPrompt: s.boolean(src, m, "outsideWithoutPrompt"),
		allowOutside:    s.boolean(src, m, "allowOutside"),
		hosts:           s.stringList(src, KeySandbox+".allowedHosts", m["allowedHosts"]),
	}
	for _, p := range s.stringList(src, KeySandbox+".allowedWritePaths", m["allowedWritePaths"]) {
		if !ValidSandboxPath(p) {
			s.problem(src, KeySandbox+".allowedWritePaths", "%q must be absolute or start with ~/", p)
			continue
		}
		l.paths = append(l.paths, p)
	}
	return l
}

// ValidSandboxPath reports whether p can be an entry of sandbox.allowedWritePaths.
func ValidSandboxPath(p string) bool {
	return p == "~" || strings.HasPrefix(p, "~/") || filepath.IsAbs(p)
}

// mergeSandbox combines the layers (highest precedence first).
func (s *Settings) mergeSandbox(layers []*layer) {
	for i := len(layers) - 1; i >= 0; i-- {
		l := layers[i]
		sb := l.sandbox
		if sb == nil {
			continue
		}
		if l.src.Level == LevelPolicy {
			if sb.allowOutside != nil && !*sb.allowOutside {
				s.Sandbox.OutsideRefused = true
			}
			if sb.enabled != nil && !*sb.enabled {
				s.ignored(l.src, KeySandbox+".enabled", "a policy can require the sandbox, not turn it off")
				sb.enabled = nil
			}
		}
		if l.src.Level == LevelProject {
			if sb.enabled != nil && !*sb.enabled {
				s.ignored(l.src, KeySandbox+".enabled", "a committed settings file cannot turn the sandbox off")
				sb.enabled = nil
			}
			if len(sb.hosts)+len(sb.paths) > 0 || sb.allowLocalhost != nil || sb.autoAllow != nil || sb.outsideNoPrompt != nil {
				s.ignored(l.src, KeySandbox, "a committed settings file cannot widen the sandbox")
			}
			if sb.enabled != nil {
				s.Sandbox.Enabled = *sb.enabled
			}
			continue
		}
		set := func(dst *bool, v *bool) {
			if v != nil {
				*dst = *v
			}
		}
		set(&s.Sandbox.Enabled, sb.enabled)
		set(&s.Sandbox.AllowLocalhost, sb.allowLocalhost)
		set(&s.Sandbox.AutoAllowBash, sb.autoAllow)
		set(&s.Sandbox.OutsideWithoutPrompt, sb.outsideNoPrompt)
		s.Sandbox.AllowedHosts = appendNew(s.Sandbox.AllowedHosts, sb.hosts)
		s.Sandbox.AllowedWritePaths = appendNew(s.Sandbox.AllowedWritePaths, sb.paths)
		if l.src.Level == LevelPolicy && sb.enabled != nil && *sb.enabled {
			s.Sandbox.Forced = true
		}
	}
	if s.Sandbox.Forced {
		s.Sandbox.Enabled = true
	}
}

func appendNew(dst, items []string) []string {
	for _, it := range items {
		if !slices.Contains(dst, it) {
			dst = append(dst, it)
		}
	}
	return dst
}

// PolicySandbox reads the POLICY documents (org scope only): whether they require the sandbox and whether they refuse
// running outside it. It backs what the frontends show, without a session to merge settings for.
func PolicySandbox(docs []*syncv1.Document) (required, outsideRefused bool) {
	for _, d := range docs {
		if d.GetScope() != commonv1.Scope_SCOPE_ORG {
			continue
		}
		var raw struct {
			Sandbox struct {
				Enabled      *bool `json:"enabled"`
				AllowOutside *bool `json:"allowOutside"`
			} `json:"sandbox"`
		}
		if json.Unmarshal([]byte(d.GetContent()), &raw) != nil {
			continue
		}
		required = required || (raw.Sandbox.Enabled != nil && *raw.Sandbox.Enabled)
		outsideRefused = outsideRefused || (raw.Sandbox.AllowOutside != nil && !*raw.Sandbox.AllowOutside)
	}
	return required, outsideRefused
}
