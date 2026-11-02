// Package prompts fetches, caches and renders the server-side prompt bundle
// (docs/protocol/prompts.md). The client has no prompt texts of its own.
package prompts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xlsft/harness/packages/client/llm"
)

// DynamicBoundary separates static (cacheable) and dynamic system sections.
const DynamicBoundary = "__dynamic_boundary__"

// Section is one system prompt section.
type Section struct {
	ID   string `json:"id"`
	Text string `json:"text,omitempty"`
}

// ToolPrompt is the description and schema of one tool.
type ToolPrompt struct {
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Instruction is a single-instruction prompt (compaction, title).
type Instruction struct {
	Instruction string `json:"instruction"`
}

// Bundle is the prompt bundle for one model.
type Bundle struct {
	Version    string                `json:"version"`
	Model      string                `json:"model"`
	System     []Section             `json:"system"`
	Tools      map[string]ToolPrompt `json:"tools"`
	Reminders  map[string]string     `json:"reminders"`
	Compaction *Instruction          `json:"compaction,omitempty"`
	Title      *Instruction          `json:"title,omitempty"`
}

// Reminder returns the reminder template by key, or fallback when missing.
func (b *Bundle) Reminder(key, fallback string) string {
	if b != nil {
		if s, ok := b.Reminders[key]; ok && s != "" {
			return s
		}
	}
	return fallback
}

// Source is the server-side prompt registry API. ConnectSource implements it
// with harness.prompts.v1.PromptService.
type Source interface {
	// Fetch returns the bundle for model. When etag (the cached version)
	// is current, notModified is true and bundle is nil.
	Fetch(ctx context.Context, model, etag string) (bundle *Bundle, newETag string, notModified bool, err error)
}

// Loader combines a Source with the on-disk cache
// <dir>/<model>/<version>.json plus <dir>/<model>/latest.json.
type Loader struct {
	Source Source
	Dir    string
	Logger *slog.Logger
}

type latest struct {
	Version string `json:"version"`
	ETag    string `json:"etag"`
}

func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == 0 {
			return '_'
		}
		return r
	}, s)
}

func (l *Loader) modelDir(model string) string { return filepath.Join(l.Dir, safe(model)) }

func (l *Loader) log() *slog.Logger {
	if l.Logger != nil {
		return l.Logger
	}
	return slog.Default()
}

// Cached loads a cached bundle by version.
func (l *Loader) Cached(model, version string) (*Bundle, error) {
	data, err := os.ReadFile(filepath.Join(l.modelDir(model), safe(version)+".json"))
	if err != nil {
		return nil, err
	}
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func (l *Loader) readLatest(model string) (latest, error) {
	var lt latest
	data, err := os.ReadFile(filepath.Join(l.modelDir(model), "latest.json"))
	if err != nil {
		return lt, err
	}
	return lt, json.Unmarshal(data, &lt)
}

func (l *Loader) save(model string, b *Bundle, etag string) error {
	dir := l.modelDir(model)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, safe(b.Version)+".json"), data); err != nil {
		return err
	}
	lt, _ := json.Marshal(latest{Version: b.Version, ETag: etag})
	return writeAtomic(filepath.Join(dir, "latest.json"), lt)
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Get returns the current bundle for model: revalidated with ETag, falling
// back to the last cached bundle when the server is unreachable.
func (l *Loader) Get(ctx context.Context, model string) (*Bundle, error) {
	lt, _ := l.readLatest(model)
	var cached *Bundle
	if lt.Version != "" {
		cached, _ = l.Cached(model, lt.Version)
	}
	etag := ""
	if cached != nil {
		etag = lt.ETag
	}
	b, newTag, notModified, err := l.Source.Fetch(ctx, model, etag)
	if err != nil {
		if cached != nil {
			l.log().Warn("prompt bundle fetch failed, using cached bundle", "model", model, "version", cached.Version, "err", err)
			return cached, nil
		}
		return nil, fmt.Errorf("fetch prompt bundle: %w", err)
	}
	if notModified {
		if cached == nil {
			return nil, errors.New("prompt bundle: server returned 304 without a cached copy")
		}
		return cached, nil
	}
	if b.Version == "" {
		b.Version = strings.Trim(newTag, `"W/`)
	}
	if b.Model == "" {
		b.Model = model
	}
	if err := l.save(model, b, newTag); err != nil {
		l.log().Warn("prompt bundle cache write failed", "err", err)
	}
	return b, nil
}

// Frozen returns the bundle pinned to version (sessions freeze the bundle).
// If that version is not cached, the current bundle is fetched; if it
// differs, a warning is logged and the current bundle is used.
func (l *Loader) Frozen(ctx context.Context, model, version string) (*Bundle, error) {
	if version != "" {
		if b, err := l.Cached(model, version); err == nil {
			return b, nil
		}
	}
	b, err := l.Get(ctx, model)
	if err != nil {
		return nil, err
	}
	if version != "" && b.Version != version {
		l.log().Warn("frozen prompt bundle version unavailable; using current", "want", version, "got", b.Version)
	}
	return b, nil
}

var placeholderRe = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.]+)\s*\}\}`)

// Render substitutes {{name}} placeholders. Unknown placeholders become empty
// and are reported through warn (may be nil).
func Render(tmpl string, vars map[string]string, warn func(name string)) string {
	return placeholderRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		name := placeholderRe.FindStringSubmatch(m)[1]
		if v, ok := vars[name]; ok {
			return v
		}
		if warn != nil {
			warn(name)
		}
		return ""
	})
}

// Placeholders lists placeholder names used in tmpl.
func Placeholders(tmpl string) []string {
	var out []string
	for _, m := range placeholderRe.FindAllStringSubmatch(tmpl, -1) {
		out = append(out, m[1])
	}
	return out
}

// UsesPlaceholder reports whether any system section references name.
func (b *Bundle) UsesPlaceholder(name string) bool {
	for _, s := range b.System {
		for _, p := range Placeholders(s.Text) {
			if p == name {
				return true
			}
		}
	}
	return false
}

// BuildSystem assembles the system blocks: static sections (before the
// dynamic boundary) are emitted verbatim with cache:true on the last one;
// dynamic sections are rendered with vars and are not cached. Without a
// boundary all sections are static (and also rendered).
func (b *Bundle) BuildSystem(vars map[string]string, warn func(string)) []llm.SystemBlock {
	boundary := -1
	for i, s := range b.System {
		if s.ID == DynamicBoundary {
			boundary = i
			break
		}
	}
	var out []llm.SystemBlock
	lastStatic := -1
	for i, s := range b.System {
		if s.ID == DynamicBoundary || s.Text == "" {
			continue
		}
		text := s.Text
		if boundary < 0 || i > boundary {
			text = Render(text, vars, warn)
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, llm.SystemBlock{Type: "text", Text: text})
		if boundary < 0 || i < boundary {
			lastStatic = len(out) - 1
		}
	}
	if lastStatic >= 0 {
		out[lastStatic].Cache = true
	}
	return out
}

// ToolDefs returns definitions for the given tool names that exist in the
// bundle, in the given order. Tools missing from the bundle are skipped.
func (b *Bundle) ToolDefs(names []string) []llm.ToolDef {
	var out []llm.ToolDef
	for _, n := range names {
		tp, ok := b.Tools[n]
		if !ok {
			continue
		}
		schema := tp.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, llm.ToolDef{Name: n, Description: tp.Description, InputSchema: schema})
	}
	return out
}
