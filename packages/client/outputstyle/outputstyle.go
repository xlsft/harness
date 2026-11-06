// Package outputstyle discovers output styles with Claude Code semantics.
//
// An output style changes how the assistant communicates (tone, verbosity,
// teaching) without changing the tools. Styles come from three sources; on the
// same name a later source wins:
//
//  1. built-ins from the prompt bundle (PromptBundle.output_styles);
//  2. user styles: ~/.harness/output-styles/*.md, then sync documents of kind
//     OUTPUT_STYLE in scope USER;
//  3. project styles: <dir>/.harness/output-styles/*.md for the git root, then
//     the cwd.
//
// Custom style files are markdown with optional YAML frontmatter: name
// (default: the file stem), description and keep-coding-instructions
// (default false, as in Claude Code). The body is the style text.
package outputstyle

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/memory"
	"github.com/xlsft/harness/packages/client/prompts"
)

// Sources of a style.
const (
	SourceBuiltin = "builtin"
	SourceUser    = "user"
	SourceProject = "project"
	SourcePlugin  = "plugin"
)

// Default is the id of the style that adds nothing; "" means the same.
const Default = "default"

// Style is one output style.
type Style struct {
	// ID is the lookup key: the bundle id for built-ins, the name for custom styles.
	ID          string
	Name        string
	Description string
	// Text is the style instructions (markdown).
	Text string
	// KeepCodingInstructions keeps the coding system sections (doing_tasks,
	// tool_policy) while the style is applied to the system prompt.
	KeepCodingInstructions bool
	// Source is builtin, user or project.
	Source string
	// Origin is the file path, "sync:<document id>" or "bundle".
	Origin string
}

// IsDefault reports whether name selects no style.
func IsDefault(name string) bool {
	n := strings.TrimSpace(name)
	return n == "" || strings.EqualFold(n, Default)
}

// Parse reads a custom style file. fallbackName (the file stem or document
// key) is used when the frontmatter has no name.
func Parse(markdown, fallbackName string) Style {
	st := Style{Name: fallbackName}
	_, body, ok := memory.SplitFrontmatter(markdown)
	if ok {
		if n := memory.FrontmatterString(markdown, "name"); n != "" {
			st.Name = n
		}
		st.Description = memory.FrontmatterString(markdown, "description")
		st.KeepCodingInstructions = parseBool(memory.FrontmatterString(markdown, "keep-coding-instructions"))
	}
	st.ID = st.Name
	st.Text = strings.TrimSpace(body)
	return st
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

// DocSource is the synced document cache (implemented by syncclient.Cache).
type DocSource interface {
	Synced() bool
	List(kind syncv1.DocumentKind) []*syncv1.Document
}

// Options configure Discover.
type Options struct {
	// Bundle supplies the built-ins (may be nil).
	Bundle *prompts.Bundle
	// Docs is the sync cache.
	Docs DocSource
	// HarnessHome is ~/.harness (config.Home()); empty skips it.
	HarnessHome string
	// ProjectDirs are searched in order for .harness/output-styles (typically
	// the git root, then the cwd).
	ProjectDirs []string
	// Plugins lists the styles of the installed plugins allowed to run. They come last, and their ids hold the plugin's name,
	// so they replace nothing.
	Plugins func() []PluginStyle
}

// PluginStyle is a style file of a plugin, under the name it goes by.
type PluginStyle struct {
	ID, Path string
}

// Catalog is the set of styles available to a session.
type Catalog struct {
	styles []Style
	index  map[string]int
}

func key(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func (c *Catalog) add(st Style) {
	if st.ID == "" {
		return
	}
	// A later style named like an earlier one (by id or display name) replaces it.
	for i, old := range c.styles {
		if key(old.ID) == key(st.ID) || key(old.Name) == key(st.Name) || key(old.ID) == key(st.Name) || key(old.Name) == key(st.ID) {
			delete(c.index, key(old.ID))
			c.styles[i] = st
			c.index[key(st.ID)] = i
			return
		}
	}
	c.index[key(st.ID)] = len(c.styles)
	c.styles = append(c.styles, st)
}

// Discover collects the styles of all sources.
func Discover(o Options) *Catalog {
	c := &Catalog{index: map[string]int{}}
	if o.Bundle != nil {
		ids := make([]string, 0, len(o.Bundle.OutputStyles))
		for id := range o.Bundle.OutputStyles {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			return builtinOrder(ids[i]) < builtinOrder(ids[j]) || (builtinOrder(ids[i]) == builtinOrder(ids[j]) && ids[i] < ids[j])
		})
		for _, id := range ids {
			b := o.Bundle.OutputStyles[id]
			name := b.Name
			if name == "" {
				name = id
			}
			c.add(Style{ID: id, Name: name, Description: b.Description, Text: b.Text,
				KeepCodingInstructions: b.KeepCodingInstructions, Source: SourceBuiltin, Origin: "bundle"})
		}
	}
	if o.HarnessHome != "" {
		c.addDir(filepath.Join(o.HarnessHome, "output-styles"), SourceUser)
	}
	if o.Docs != nil && o.Docs.Synced() {
		for _, d := range o.Docs.List(syncv1.DocumentKind_DOCUMENT_KIND_OUTPUT_STYLE) {
			if d.GetScope() != commonv1.Scope_SCOPE_USER {
				continue
			}
			st := Parse(d.GetContent(), d.GetKey())
			st.Source, st.Origin = SourceUser, "sync:"+d.GetId()
			c.add(st)
		}
	}
	seen := map[string]bool{}
	for _, d := range o.ProjectDirs {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		c.addDir(filepath.Join(d, ".harness", "output-styles"), SourceProject)
	}
	if o.Plugins != nil {
		for _, ps := range o.Plugins() {
			data, err := os.ReadFile(ps.Path)
			if err != nil {
				continue
			}
			st := Parse(string(data), ps.ID)
			st.ID, st.Name, st.Source, st.Origin = ps.ID, ps.ID, SourcePlugin, ps.Path
			c.add(st)
		}
	}
	return c
}

// builtinOrder keeps the Claude Code order of built-ins in pickers.
func builtinOrder(id string) int {
	switch id {
	case Default:
		return 0
	case "proactive":
		return 1
	case "concise":
		return 2
	case "explanatory":
		return 3
	case "learning":
		return 4
	}
	return 10
}

func (c *Catalog) addDir(dir, source string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		st := Parse(string(data), strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
		st.Source, st.Origin = source, p
		c.add(st)
	}
}

// List returns the styles: built-ins first (in bundle order), then custom
// styles in discovery order. A "default" entry is always present.
func (c *Catalog) List() []Style {
	out := append([]Style(nil), c.styles...)
	if _, ok := c.index[Default]; !ok {
		out = append([]Style{{ID: Default, Name: "Default", Source: SourceBuiltin, KeepCodingInstructions: true, Origin: "bundle"}}, out...)
	}
	return out
}

// Lookup finds a style by id or name (case-insensitive).
func (c *Catalog) Lookup(name string) (Style, bool) {
	k := key(name)
	if i, ok := c.index[k]; ok {
		return c.styles[i], true
	}
	for _, st := range c.styles {
		if key(st.Name) == k {
			return st, true
		}
	}
	if k == Default {
		return Style{ID: Default, Name: "Default", Source: SourceBuiltin, KeepCodingInstructions: true, Origin: "bundle"}, true
	}
	return Style{}, false
}

// Template is the starter file printed by the "New style…" picker entry.
func Template(name string) string {
	if name == "" {
		name = "my-style"
	}
	return "---\nname: " + name + "\ndescription: One line shown in the /output-style picker\nkeep-coding-instructions: false\n---\n\n" +
		"Describe how the assistant should communicate: tone, length, structure, what to explain.\n"
}
