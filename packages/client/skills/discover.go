package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/memory"
)

func itoa(n int) string     { return strconv.Itoa(n) }
func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 6, 64) }

// Scan limits. A folder that goes past them is someone's data, not a set of skills.
const (
	// maxFileBytes bounds what is read of one Markdown file.
	maxFileBytes = 1 << 20
	// maxEntries bounds the skills and commands read from one folder.
	maxEntries = 500
	// maxDepth bounds how deep SKILL.md is looked for below a skills folder, and files below a commands folder.
	maxDepth = 5
)

// SkillFile is the name that makes a folder a skill.
const SkillFile = "SKILL.md"

// DocSource is the synced document cache (implemented by syncclient.Cache).
type DocSource interface {
	List(kind syncv1.DocumentKind) []*syncv1.Document
}

// Documents is a fixed set of documents as a DocSource.
type Documents []*syncv1.Document

// List implements DocSource.
func (d Documents) List(kind syncv1.DocumentKind) []*syncv1.Document {
	var out []*syncv1.Document
	for _, doc := range d {
		if doc.GetKind() == kind {
			out = append(out, doc)
		}
	}
	return out
}

// Options say where to look.
type Options struct {
	// HarnessHome is ~/.harness; empty: no personal files.
	HarnessHome string
	// Cwd is the session's folder; empty: nothing project-level. GitRoot bounds the walk up from it (empty: Cwd only).
	Cwd, GitRoot string
	// AdditionalDirs are the session's other working directories.
	AdditionalDirs []string
	// Nested are folders below Cwd whose own .harness/skills apply because a file in them was touched.
	Nested []string
	// Trusted lets project-level files load. They give instructions to the model and may grant it tools, so a folder
	// the user has not trusted gets no say.
	Trusted bool
	// Docs holds what Settings > Import and the organization stored on the server; nil: none. Device-local things stay
	// out of it: skills on disk are never uploaded by being read here.
	Docs DocSource
	// Disabled are the names the user turned off.
	Disabled map[string]bool
	// Plugins are the installed plugins allowed to run here (package plugins decides which).
	Plugins []PluginSource
	// Allowed is the organization's word on a name; nil allows all. What it refuses stays listed and can't run.
	Allowed func(name string) bool
}

// PluginSource is what one plugin brings. Everything it holds is named below its prefix.
type PluginSource struct {
	// Prefix goes before every name, with a colon; ID is the plugin's id; Root and Data its folders.
	Prefix, ID, Root, Data string
	// SkillDirs hold <name>/SKILL.md folders, or are a skill themselves.
	SkillDirs []string
	Commands  []PluginCommand
}

// PluginCommand is one command: a Markdown file or a folder of them, or (with Name) one named entry of the manifest.
type PluginCommand struct {
	// Path is a file or folder; "" when Content holds the text.
	Path, Content string
	// Name is the name below the prefix of a named entry; "" takes it from the file.
	Name string
	// What a named entry sets over the file's frontmatter.
	Description, ArgumentHint, Model string
	AllowedTools                     []string
}

// Catalog is what Discover found.
type Catalog struct {
	// All holds every entry, shadowed ones included, in display order.
	All []Skill
	// UntrustedProject: project files exist but the folder isn't trusted.
	UntrustedProject bool
	byName           map[string]int
	disabled         map[string]bool
	allowed          func(string) bool
}

// Lookup returns the entry that wins for name (case-sensitive, a leading slash ignored).
func (c *Catalog) Lookup(name string) (Skill, bool) {
	if c == nil {
		return Skill{}, false
	}
	i, ok := c.byName[strings.TrimPrefix(name, "/")]
	if !ok {
		return Skill{}, false
	}
	return c.All[i], true
}

// Enabled reports the user's switch.
func (c *Catalog) Enabled(name string) bool {
	return c == nil || (!c.disabled[name] && !c.BlockedByPolicy(name))
}

// BlockedByPolicy reports that the organization keeps a name from running. No switch of the user's changes that.
func (c *Catalog) BlockedByPolicy(name string) bool {
	return c != nil && c.allowed != nil && !c.allowed(name)
}

var sourceRank = map[string]int{SourceOrganization: 0, SourcePersonal: 1, SourceImported: 2, SourceProject: 3, SourcePlugin: 4}

func (s Skill) rank() [3]int {
	k := 0
	if s.Kind == KindCommand {
		k = 1
	}
	return [3]int{sourceRank[s.Source], k, s.order}
}

// scanner collects entries in the order they rank.
type scanner struct {
	// held says a folder waits for this device's approval: it stays out of the catalog.
	held  func(dir string) bool
	found []Skill
}

func (sc *scanner) add(s Skill) {
	s.order = len(sc.found)
	sc.found = append(sc.found, s)
}

// Discover reads the frontmatter of everything Options point at.
func Discover(o Options) *Catalog {
	sc := &scanner{}
	if o.HarnessHome != "" {
		sc.held = HeldExternal(o.HarnessHome)
	}
	sc.docs(o.Docs)
	if o.HarnessHome != "" {
		sc.skillsDir(filepath.Join(o.HarnessHome, "skills"), SourcePersonal)
		sc.commandsDir(filepath.Join(o.HarnessHome, "commands"), SourcePersonal)
	}
	c := &Catalog{disabled: o.Disabled}
	c.allowed = o.Allowed
	roots := projectDirs(o)
	for _, d := range roots {
		if !o.Trusted {
			if hasProjectFiles(d) {
				c.UntrustedProject = true
			}
			continue
		}
		sc.skillsDir(filepath.Join(d, ".harness", "skills"), SourceProject)
		sc.commandsDir(filepath.Join(d, ".harness", "commands"), SourceProject)
	}
	if o.Trusted {
		for _, d := range o.Nested {
			sc.skillsDir(filepath.Join(d, ".harness", "skills"), SourceProject)
		}
	}
	for _, p := range o.Plugins {
		sc.plugin(p)
	}
	c.resolve(sc.found)
	return c
}

// ScanPlugin lists what a plugin holds, for showing it before anything may run.
func ScanPlugin(p PluginSource) []Skill {
	sc := &scanner{}
	sc.plugin(p)
	return sc.found
}

// plugin reads a plugin's skills and commands and puts its prefix on them.
func (sc *scanner) plugin(p PluginSource) {
	start := len(sc.found)
	for _, dir := range p.SkillDirs {
		md := filepath.Join(dir, SkillFile)
		if st, err := os.Stat(md); err == nil && st.Mode().IsRegular() {
			if text, err := readHead(md); err == nil {
				s := Parse(text, filepath.Base(dir), KindSkill)
				s.Source, s.Path, s.Dir = SourcePlugin, md, dir
				sc.add(s)
			}
			continue
		}
		sc.skillsDir(dir, SourcePlugin)
	}
	for _, c := range p.Commands {
		sc.pluginCommand(p, c)
	}
	for i := start; i < len(sc.found); i++ {
		s := &sc.found[i]
		s.Name = p.Prefix + ":" + s.Name
		s.Plugin, s.PluginRoot, s.PluginData = p.ID, p.Root, p.Data
		if !ValidName(s.Name) {
			s.Problems = append(s.Problems, "The name \""+s.Name+"\" can't follow a slash.")
			s.UserInvocable, s.DisableModelInvocation = false, true
		}
	}
}

func (sc *scanner) pluginCommand(p PluginSource, c PluginCommand) {
	if c.Path != "" {
		st, err := os.Stat(c.Path)
		if err != nil {
			return
		}
		if st.IsDir() {
			sc.commandsDir(c.Path, SourcePlugin)
			return
		}
		text, err := readHead(c.Path)
		if err != nil {
			return
		}
		name := c.Name
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(c.Path), ".md")
		}
		s := Parse(text, name, KindCommand)
		s.Source, s.Path = SourcePlugin, c.Path
		sc.add(c.apply(s))
		return
	}
	s := Parse(c.Content, c.Name, KindCommand)
	s.Source, s.Path = SourcePlugin, "plugin:"+p.ID+"/"+c.Name
	s.doc = docBody{content: c.Content, id: "plugin:" + p.ID + "/" + c.Name}
	sc.add(c.apply(s))
}

func (c PluginCommand) apply(s Skill) Skill {
	if c.Description != "" {
		s.Description = c.Description
	}
	if c.ArgumentHint != "" {
		s.ArgumentHint = c.ArgumentHint
	}
	if c.Model != "" {
		s.Model = c.Model
	}
	if len(c.AllowedTools) > 0 {
		s.AllowedTools = ruleList(toAny(c.AllowedTools))
	}
	return s
}

func toAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// FromServer reports whether the skill's text came from a document of the server.
func (s Skill) FromServer() bool { return s.doc.id != "" && s.Plugin == "" }

// resolve keeps a winner per name and orders the entries for display.
func (c *Catalog) resolve(found []Skill) {
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i].rank(), found[j].rank()
		for k := range a {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return false
	})
	winners := map[string]Skill{}
	seenPath := map[string]bool{}
	var kept []Skill
	for _, s := range found {
		// One folder reached twice (the cwd is also an additional directory, a symlink).
		if real := realPath(s.Path); seenPath[real] {
			continue
		} else {
			seenPath[real] = true
		}
		if w, ok := winners[s.Name]; ok {
			s.ShadowedBy = w.Source
		} else {
			winners[s.Name] = s
		}
		kept = append(kept, s)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if ri, rj := sourceRank[kept[i].Source], sourceRank[kept[j].Source]; ri != rj {
			return ri < rj
		}
		if kept[i].Name != kept[j].Name {
			return kept[i].Name < kept[j].Name
		}
		return kept[i].rank()[1] < kept[j].rank()[1]
	})
	c.All = kept
	c.byName = map[string]int{}
	for i, s := range kept {
		if s.ShadowedBy == "" {
			c.byName[s.Name] = i
		}
	}
}

func realPath(p string) string {
	if strings.HasPrefix(p, "sync:") || strings.HasPrefix(p, "plugin:") {
		return p
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// projectDirs lists the folders whose project files apply, nearest first: the cwd, its parents up to the repository's
// root, then the session's other directories.
func projectDirs(o Options) []string {
	var out []string
	seen := map[string]bool{}
	add := func(d string) {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	if o.Cwd != "" {
		add(o.Cwd)
		if o.GitRoot != "" && within(o.Cwd, o.GitRoot) {
			for d := o.Cwd; d != o.GitRoot; d = filepath.Dir(d) {
				add(d)
				if d == filepath.Dir(d) {
					break
				}
			}
			add(o.GitRoot)
		}
	}
	for _, d := range o.AdditionalDirs {
		add(d)
	}
	return out
}

func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func hasProjectFiles(d string) bool {
	for _, sub := range []string{".harness/skills", ".harness/commands"} {
		if entries, err := os.ReadDir(filepath.Join(d, filepath.FromSlash(sub))); err == nil && len(entries) > 0 {
			return true
		}
	}
	return false
}

// skip says a folder holds no skills: hidden, or someone else's tree.
func skip(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor"
}

// skillsDir finds SKILL.md folders under root. A folder holding one is a skill and is not looked into further: its
// subfolders are its own files. Others may group skills.
func (sc *scanner) skillsDir(root, source string) {
	count := 0
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if count >= maxEntries || skip(e.Name()) {
				continue
			}
			p := filepath.Join(dir, e.Name())
			// Stat, not the entry's type: skills are often symlinked in.
			if st, err := os.Stat(p); err != nil || !st.IsDir() {
				continue
			}
			md := filepath.Join(p, SkillFile)
			if st, err := os.Stat(md); err == nil && st.Mode().IsRegular() {
				if sc.held != nil && sc.held(p) {
					continue
				}
				text, err := readHead(md)
				if err != nil {
					continue
				}
				s := Parse(text, e.Name(), KindSkill)
				s.Source, s.Path, s.Dir = source, md, p
				sc.add(s)
				count++
				continue
			}
			if depth < maxDepth {
				walk(p, depth+1)
			}
		}
	}
	walk(root, 1)
}

// commandsDir reads the Markdown files under root; each folder on the way is a namespace.
func (sc *scanner) commandsDir(root, source string) {
	count := 0
	var walk func(dir string, prefix []string)
	walk = func(dir string, prefix []string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if count >= maxEntries || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			st, err := os.Stat(p)
			if err != nil {
				continue
			}
			if st.IsDir() {
				if len(prefix)+1 < maxDepth {
					walk(p, append(append([]string(nil), prefix...), e.Name()))
				}
				continue
			}
			stem, ok := strings.CutSuffix(e.Name(), ".md")
			if !ok || !st.Mode().IsRegular() {
				continue
			}
			name := strings.Join(append(append([]string(nil), prefix...), stem), ":")
			text, err := readHead(p)
			if err != nil {
				continue
			}
			s := Parse(text, name, KindCommand)
			s.Source, s.Path = source, p
			if !ValidName(name) {
				s.Problems = append(s.Problems, "The name \""+name+"\" can't follow a slash.")
				s.UserInvocable, s.DisableModelInvocation = false, true
			}
			sc.add(s)
			count++
		}
	}
	walk(root, nil)
}

func readHead(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes))
	return string(b), err
}

// docs reads what the server stores. A project-scoped document belongs to a project key this client doesn't resolve, and
// is left out.
func (sc *scanner) docs(src DocSource) {
	if src == nil {
		return
	}
	read := func(kind syncv1.DocumentKind, k Kind) {
		list := src.List(kind)
		sort.Slice(list, func(i, j int) bool { return list[i].GetKey() < list[j].GetKey() })
		for _, d := range list {
			source := ""
			switch d.GetScope() {
			case commonv1.Scope_SCOPE_ORG:
				source = SourceOrganization
			case commonv1.Scope_SCOPE_USER:
				source = SourceImported
			default:
				continue
			}
			s := Parse(d.GetContent(), d.GetKey(), k)
			if !ValidName(d.GetKey()) && k == KindCommand {
				continue
			}
			s.Source, s.Path, s.doc = source, "sync:"+d.GetId(), docBody{content: d.GetContent(), files: d.GetFiles(), hash: d.GetContentHash(), id: d.GetId()}
			sc.add(s)
		}
	}
	read(syncv1.DocumentKind_DOCUMENT_KIND_SKILL, KindSkill)
	read(syncv1.DocumentKind_DOCUMENT_KIND_COMMAND, KindCommand)
}

// docBody is a skill stored on the server.
type docBody struct {
	content string
	files   []*syncv1.DocumentFile
	hash    string
	id      string
}

// LoadBody reads the text after the frontmatter from the source, so an edit since discovery counts.
func (s Skill) LoadBody() (string, error) {
	text := s.doc.content
	if s.doc.id == "" {
		b, err := readHead(s.Path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", s.Path, err)
		}
		text = b
	}
	_, body, _ := memory.SplitFrontmatter(text)
	return strings.TrimSpace(body), nil
}

// Materialize gives a skill stored on the server a folder, under cacheRoot, so its supporting files can be read and run
// like those of any other. It returns the folder; a skill with its own folder or without files needs none. The name of
// the folder holds the document's hash: a changed skill gets a new one, and an old one is never half overwritten.
func (s Skill) Materialize(cacheRoot string) (string, error) {
	if s.Dir != "" || s.doc.id == "" || s.Kind != KindSkill {
		return s.Dir, nil
	}
	sum := s.doc.hash
	if sum == "" {
		h := sha256.Sum256([]byte(s.doc.content))
		sum = hex.EncodeToString(h[:])
	}
	if len(sum) > 16 {
		sum = sum[:16]
	}
	dir := filepath.Join(cacheRoot, s.Name+"-"+sum)
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return dir, nil
	}
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(cacheRoot, ".tmp-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := os.WriteFile(filepath.Join(tmp, SkillFile), []byte(s.doc.content), 0o600); err != nil {
		return "", err
	}
	for _, f := range s.doc.files {
		rel := filepath.FromSlash(f.GetPath())
		// The path comes from a server: it stays inside the folder.
		if rel == "" || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !filepath.IsLocal(rel) {
			return "", errors.New("the skill's document holds a file outside its folder: " + f.GetPath())
		}
		p := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return "", err
		}
		mode := os.FileMode(0o600)
		if f.GetExecutable() {
			mode = 0o700
		}
		if err := os.WriteFile(p, f.GetContent(), mode); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		// Another session made it first.
		if st, serr := os.Stat(dir); serr == nil && st.IsDir() {
			return dir, nil
		}
		return "", err
	}
	return dir, nil
}
