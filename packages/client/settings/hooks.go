package settings

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

// The settings keys of hooks. KeyHooks has the shape of the settings files users already have (package hooks reads it);
// KeyDisableAllHooks is the switch a frontend's preference writes into the user's synced settings document.
const (
	KeyHooks                 = "hooks"
	KeyDisableAllHooks       = "disableAllHooks"
	KeyAllowManagedHooksOnly = "allowManagedHooksOnly"
)

// HookGate says what keeps the hooks of a source from running until something changes.
type HookGate int

const (
	// HookGateNone: nothing does. The user's own files and the command line.
	HookGateNone HookGate = iota
	// HookGateWorkspace: a project file (committed or local: a repository can commit both) in a folder that is not trusted.
	HookGateWorkspace
	// HookGateDevice: the server delivered it. It runs only once this exact content is trusted on this machine, so a
	// compromised server or account cannot run code on the user's devices.
	HookGateDevice
	// HookGateManaged: allowManagedHooksOnly in a policy document leaves only the policy's hooks.
	HookGateManaged
)

// HookLayer is the hooks block of one source.
type HookLayer struct {
	Source Source
	// Raw is the JSON of the block, by event name.
	Raw json.RawMessage
	// Hash identifies the content (ContentHash).
	Hash string
	Gate HookGate
}

// ContentHash is the value device trust is keyed on. It is computed here rather than taken from the document, so what is
// trusted is what would run.
func ContentHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// parseHooks reads the hook keys of one settings object into its layer.
func (s *Settings) parseHooks(src Source, raw map[string]json.RawMessage, l *layer) {
	if v, ok := raw[KeyHooks]; ok && string(bytes.TrimSpace(v)) != "null" {
		l.hooks = v
	}
	if b := s.boolean(src, raw, KeyDisableAllHooks); b != nil {
		l.hooksOff = *b
	}
	if src.Level == LevelPolicy {
		if b := s.boolean(src, raw, KeyAllowManagedHooksOnly); b != nil {
			l.managedHooksOnly = *b
		}
	}
}

// hookDocLayers returns a layer for each HOOK document of one scope.
func hookDocLayers(docs DocSource, scope commonv1.Scope, level Level, dir string) []*layer {
	var out []*layer
	for _, d := range docs.List(syncv1.DocumentKind_DOCUMENT_KIND_HOOK) {
		if d.GetScope() != scope {
			continue
		}
		out = append(out, &layer{src: Source{Level: level, DocID: d.GetId(), Dir: dir}, hooks: json.RawMessage(d.GetContent())})
	}
	return out
}

// mergeHooks collects the hook blocks, lowest precedence first: every source runs its hooks, nothing overrides, and when
// two hooks rewrite one tool call the higher level, which runs its result last, decides.
//
// disableAllHooks only ever turns off: any level can say so, a repository cannot turn back on what the user turned off,
// and only a policy document turns off the policy's own hooks.
func (s *Settings) mergeHooks(layers []*layer) {
	managedOnly := false
	for _, l := range layers {
		if l.src.Level == LevelPolicy {
			managedOnly = managedOnly || l.managedHooksOnly
			s.HooksPolicyOff = s.HooksPolicyOff || l.hooksOff
		}
		s.HooksOff = s.HooksOff || l.hooksOff
	}
	for i := len(layers) - 1; i >= 0; i-- {
		l := layers[i]
		if len(l.hooks) == 0 {
			continue
		}
		h := HookLayer{Source: l.src, Raw: l.hooks, Hash: ContentHash(l.hooks)}
		switch {
		case managedOnly && l.src.Level != LevelPolicy:
			h.Gate = HookGateManaged
			s.ignored(l.src, KeyHooks, "ignored: policy allows managed hooks only")
		case l.src.DocID != "":
			h.Gate = HookGateDevice
		case (l.src.Level == LevelProject || l.src.Level == LevelLocal) && !s.Trusted:
			h.Gate = HookGateWorkspace
			s.ignored(l.src, KeyHooks, "ignored until the workspace is trusted")
		}
		s.Hooks = append(s.Hooks, h)
	}
}
