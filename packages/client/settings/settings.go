// Package settings merges the settings hierarchy of a session:
//
//	policy (sync POLICY documents; always wins)
//	→ CLI flags
//	→ .harness/settings.local.json, .claude/settings.local.json
//	→ .harness/settings.json, .claude/settings.json
//	→ user (sync SETTINGS + PERMISSION_RULES at user scope; ~/.claude/settings.json before the first sync)
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
	LevelProject // committed .claude/settings.json, .harness/settings.json
	LevelLocal   // .claude/settings.local.json, .harness/settings.local.json
	LevelCLI
	LevelPolicy
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
	// the directory that contains .claude/.harness for files, the home
	// directory for user documents, the cwd for CLI flags, empty for org
	// and policy documents.
	Dir string
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
}

// Attribution is the commit / PR attribution text ("" disables it).
type Attribution struct {
	Commit *string
	PR     *string
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
	ClaudeMdMode           memory.Mode
	Attribution            Attribution
	Env                    map[string]string
	Permissions            Permissions
	// AvailableModels is the policy allowlist of models (empty: any).
	AvailableModels []string

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
}

// Options configure Load.
type Options struct {
	Cwd string
	// Home defaults to os.UserHomeDir.
	Home string
	CLI  CLI
	// Docs supplies policy, user and org documents. Nil, or not yet synced,
	// falls back to ~/.claude/settings.json for the user level.
	Docs DocSource
	// Trusted is the workspace trust decision for Cwd.
	Trusted bool
}

// Defaults.
const (
	DefaultCleanupPeriodDays = 30
)

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

	model, effort, outputStyle, claudeMdMode, defaultMode *string
	autoCompactWindow, cleanupPeriodDays                  *int
	includeGit                                            *bool
	attrCommit, attrPR                                    *string
	env                                                   map[string]string
	allow, ask, deny, addDirs                             []string
	disableBypass, disableAuto                            bool

	availableModels  []string
	managedRulesOnly bool
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
		CleanupPeriodDays: DefaultCleanupPeriodDays, IncludeGitInstructions: true}
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

	add(s.cliLayer(o.CLI, cwd))

	for _, f := range []struct {
		rel   string
		level Level
	}{
		{".harness/settings.local.json", LevelLocal},
		{".claude/settings.local.json", LevelLocal},
		{".harness/settings.json", LevelProject},
		{".claude/settings.json", LevelProject},
	} {
		p := filepath.Join(cwd, filepath.FromSlash(f.rel))
		add(s.parseFile(Source{Level: f.level, Path: p, Dir: cwd}, p))
	}

	if synced {
		add(s.docLayer(o.Docs, commonv1.Scope_SCOPE_USER, LevelUser, home))
		add(s.docLayer(o.Docs, commonv1.Scope_SCOPE_ORG, LevelOrg, ""))
	} else {
		p := filepath.Join(home, ".claude", "settings.json")
		add(s.parseFile(Source{Level: LevelUser, Path: p, Dir: home}, p))
		if o.Docs != nil {
			// Org defaults may already be cached from an interrupted sync.
			add(s.docLayer(o.Docs, commonv1.Scope_SCOPE_ORG, LevelOrg, ""))
		}
	}

	layers = flatten(layers)
	for _, l := range layers {
		s.Sources = append(s.Sources, l.src)
	}
	s.merge(layers, cwd, home)
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
