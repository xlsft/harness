// Package memory loads a session's instructions: the managed file, user memory and rules from synced documents, AGENTS.md
// and .harness/AGENTS.md from the filesystem root down to the working directory, the private .harness/AGENTS.local.md, @path
// imports, lazily loaded subdirectory files, path-scoped .harness/rules and the per-project auto memory index.
//
// CLAUDE.md, CLAUDE.local.md and everything under .claude are import sources only (package importer): nothing here reads
// them, not even through an @import.
//
// The result is an ordered list of instruction sources and a rendered string
// for the prompt bundle's project_instructions reminder.
package memory

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

// Origin classifies an instruction source.
type Origin string

const (
	OriginManaged    Origin = "managed"     // the organization-managed AGENTS.md
	OriginUser       Origin = "user"        // user memory (synced documents)
	OriginProject    Origin = "project"     // an imported file with no origin of its own
	OriginLocal      Origin = "local"       // .harness/AGENTS.local.md
	OriginAgents     Origin = "agents"      // AGENTS.md, .harness/AGENTS.md
	OriginRule       Origin = "rule"        // .harness/rules/**/*.md, synced user rules
	OriginAutoMemory Origin = "auto_memory" // per-project MEMORY.md index
)

// Trigger says why an instruction was loaded.
type Trigger string

const (
	TriggerStartup Trigger = "session_start"
	TriggerNested  Trigger = "nested_traversal" // subdirectory file, loaded when a file below it is touched
	TriggerPath    Trigger = "path_glob_match"  // rule whose paths: glob matched a touched file
	TriggerInclude Trigger = "include"          // @path import
)

// Mode is the setting claudeMdMode. The key and its values stay what settings files in that format already hold, so none of
// them reads as a problem; only managed-only still changes anything, as the other values chose between files no longer read.
type Mode string

const (
	// ModeDefault reads the project's instruction files.
	ModeDefault Mode = "claude-md-or-agents-md"
	// ModeManagedOnly reads only the managed files.
	ModeManagedOnly Mode = "managed-only"
)

// ValidMode reports whether m is a known mode ("" counts as the default).
func ValidMode(m string) bool {
	switch Mode(m) {
	case "", ModeDefault, ModeManagedOnly, "claude-md-and-agents-md", "claude-md":
		return true
	}
	return false
}

// Limits.
const (
	// MaxFileBytes skips larger instruction files.
	MaxFileBytes = 4 << 20
	// MaxImportDepth is the maximum number of import hops.
	MaxImportDepth = 4
	// AutoMemoryMaxLines and AutoMemoryMaxBytes cap the MEMORY.md index.
	AutoMemoryMaxLines = 200
	AutoMemoryMaxBytes = 25 << 10
)

// Instruction is one loaded instruction source.
type Instruction struct {
	// Path is the file path, or "sync:<doc id>" for content that came from
	// a synced document.
	Path    string
	Origin  Origin
	Trigger Trigger
	Content string
	// ImportedFrom is the importing file for Trigger == TriggerInclude.
	ImportedFrom string
	// Globs are the paths: globs of a path-scoped rule.
	Globs []string
}

// PendingImport is an import outside the working directories that waits for
// the user's approval. It is not loaded until approved with Resolve.
type PendingImport struct {
	Path         string
	ImportedFrom string
}

// DocSource is the synced document cache (implemented by syncclient.Cache).
type DocSource interface {
	// Synced reports whether at least one full sync with the server has
	// completed; before that, there is no user memory.
	Synced() bool
	Lookup(scope commonv1.Scope, kind syncv1.DocumentKind, key, projectKey string) (*syncv1.Document, bool)
	List(kind syncv1.DocumentKind) []*syncv1.Document
}

// Approvals stores the user's decisions about external imports.
type Approvals interface {
	Decision(path string) (approved, decided bool)
	Record(path string, approved bool) error
}

// Options configure a Loader.
type Options struct {
	// Cwd is the working directory (required).
	Cwd string
	// ProjectRoot (usually the git root) and WorkingDirs (--add-dir) are,
	// together with Cwd, the directories whose files may be imported
	// without approval.
	ProjectRoot string
	WorkingDirs []string
	Mode        Mode
	// Home is the user's home directory; defaults to os.UserHomeDir.
	Home string
	// HarnessHome is the harness data directory; defaults to ~/.harness. Relative @imports of synced user memory resolve
	// against it.
	HarnessHome string
	// ManagedFiles overrides DefaultManagedFiles (tests).
	ManagedFiles []string
	// Docs supplies synced user memory, user rules and auto memory. Nil, or a source that has not completed its first sync:
	// no user memory or rules, and auto memory from AutoMemoryDir.
	Docs DocSource
	// ProjectKey selects the project-scope auto memory document.
	ProjectKey string
	// AutoMemoryDir is the local auto memory directory (MEMORY.md is read
	// from it when there is no synced document). Empty disables auto memory.
	AutoMemoryDir string
	// Approvals decides external imports; nil leaves them all pending.
	Approvals Approvals
	// ProjectOnly loads only the project files (no managed files, user
	// memory, user rules or auto memory).
	ProjectOnly bool
	// NoAncestors reads instruction files of Cwd and below only, not of its ancestors (sessions without a
	// folder: their private directory belongs to no project).
	NoAncestors bool
}

// DefaultManagedFiles returns where an organization puts instructions for this OS.
func DefaultManagedFiles() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/Library/Application Support/harness/AGENTS.md"}
	case "windows":
		return []string{`C:\Program Files\harness\AGENTS.md`}
	default:
		return []string{"/etc/harness/AGENTS.md"}
	}
}

// LocalName is the private project file: not checked in, like .harness/settings.local.json.
var LocalName = filepath.Join(".harness", "AGENTS.local.md")

var instructionNames = []string{"AGENTS.md", filepath.Join(".harness", "AGENTS.md"), LocalName}

func originOf(name string) Origin {
	if name == LocalName {
		return OriginLocal
	}
	return OriginAgents
}

type rule struct {
	path    string
	root    string // directory rule globs are relative to
	globs   []string
	content string
	origin  Origin
	loaded  bool
}

// Loader loads instructions for one session. It is safe for concurrent use.
type Loader struct {
	opts        Options
	home        string
	harnessHome string
	cwd         string
	trusted     []string // dirs whose files may be imported without approval

	mu      sync.Mutex
	seen    map[string]bool // loaded files
	visited map[string]bool // directories whose instruction files were checked
	rules   []*rule
	pending map[string]PendingImport
	initial []Instruction
}

// New creates a loader and loads the startup instructions.
func New(o Options) *Loader {
	l := &Loader{opts: o, seen: map[string]bool{}, visited: map[string]bool{}, pending: map[string]PendingImport{}}
	l.cwd = clean(o.Cwd)
	l.home = o.Home
	if l.home == "" {
		l.home, _ = os.UserHomeDir()
	}
	if l.home != "" {
		l.home = clean(l.home)
	}
	switch {
	case o.HarnessHome != "":
		l.harnessHome = clean(o.HarnessHome)
	case l.home != "":
		l.harnessHome = filepath.Join(l.home, ".harness")
	}
	l.trusted = []string{l.cwd}
	if o.ProjectRoot != "" {
		l.trusted = append(l.trusted, clean(o.ProjectRoot))
	}
	for _, d := range o.WorkingDirs {
		l.trusted = append(l.trusted, clean(d))
	}
	l.mu.Lock()
	l.initial = l.loadStartup()
	l.mu.Unlock()
	return l
}

func clean(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
}

// Initial returns the instructions loaded at session start, in order.
func (l *Loader) Initial() []Instruction {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Instruction(nil), l.initial...)
}

// PendingImports returns external imports waiting for approval, sorted by path.
func (l *Loader) PendingImports() []PendingImport {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]PendingImport, 0, len(l.pending))
	for _, p := range l.pending {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Resolve records the user's decision for a pending import. When approved,
// the file (and its own imports) is loaded and returned.
func (l *Loader) Resolve(path string, approved bool) ([]Instruction, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.pending[path]
	if !ok {
		return nil, nil
	}
	delete(l.pending, path)
	var err error
	if l.opts.Approvals != nil {
		err = l.opts.Approvals.Record(path, approved)
	}
	if !approved {
		return nil, err
	}
	var out []Instruction
	l.addFile(&out, path, l.originOfImporter(p.ImportedFrom), TriggerInclude, p.ImportedFrom, 1, false)
	return out, err
}

func (l *Loader) originOfImporter(path string) Origin {
	for _, in := range l.initial {
		if in.Path == path {
			return in.Origin
		}
	}
	return OriginProject
}

// ancestors returns the directories whose instruction files apply at startup: the filesystem root down to
// the working directory, or the working directory alone with NoAncestors.
func (l *Loader) ancestors() []string {
	if l.opts.NoAncestors {
		return []string{l.cwd}
	}
	return ancestors(l.cwd)
}

// ancestors returns the directories from the filesystem root down to dir.
func ancestors(dir string) []string {
	var dirs []string
	d := filepath.Clean(dir)
	for {
		dirs = append(dirs, d)
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

// isUserConfig reports whether p lives in the harness data directory, which holds user-level configuration rather than
// project files of the home directory.
func (l *Loader) isUserConfig(p string) bool {
	return l.harnessHome != "" && within(p, l.harnessHome)
}

// isImportSource reports whether p is a file only the importer reads: named like Claude Code's instruction files, or below a
// .claude folder. What counts is the part of the path inside a project (a repository may be checked out below some .claude),
// and all of it for a path outside them.
func (l *Loader) isImportSource(p string) bool {
	if IsImportSourceName(filepath.Base(p)) {
		return true
	}
	rel := p
	best := ""
	for _, d := range l.trusted {
		if within(p, d) && len(d) > len(best) {
			best = d
		}
	}
	if best != "" {
		rel, _ = filepath.Rel(best, p)
	}
	return slices.Contains(strings.Split(rel, string(filepath.Separator)), ".claude")
}

// linksToImportSource reports whether p is a link to a CLAUDE.md or into a .claude next to it: that would still be reading them.
func linksToImportSource(p string) bool {
	if st, err := os.Lstat(p); err != nil || st.Mode()&os.ModeSymlink == 0 {
		return false
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	if IsImportSourceName(filepath.Base(real)) {
		return true
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, real)
	return err == nil && slices.Contains(strings.Split(rel, string(filepath.Separator)), ".claude")
}

// IsImportSourceName reports whether a file name is one of Claude Code's instruction files.
func IsImportSourceName(name string) bool { return name == "CLAUDE.md" || name == "CLAUDE.local.md" }

func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// readFile reads an instruction file; it returns ok=false for missing,
// non-regular, oversized or unreadable files.
func readFile(p string) (string, bool) {
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() || st.Size() > MaxFileBytes {
		return "", false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return string(data), true
}

func (l *Loader) loadStartup() []Instruction {
	var out []Instruction
	if !l.opts.ProjectOnly {
		managed := l.opts.ManagedFiles
		if managed == nil {
			managed = DefaultManagedFiles()
		}
		for _, p := range managed {
			l.addFile(&out, p, OriginManaged, TriggerStartup, "", 0, true)
		}
	}
	if l.opts.Mode == ModeManagedOnly {
		return out
	}
	if !l.opts.ProjectOnly {
		l.addUserMemory(&out)
	}
	for _, d := range l.ancestors() {
		l.visited[d] = true
		l.addDir(&out, d, TriggerStartup)
	}
	if !l.opts.ProjectOnly {
		l.addAutoMemory(&out)
	}
	return out
}

func (l *Loader) synced() bool { return l.opts.Docs != nil && l.opts.Docs.Synced() }

func (l *Loader) addUserMemory(out *[]Instruction) {
	if !l.synced() {
		return
	}
	doc, ok := l.opts.Docs.Lookup(commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_MEMORY, "user", "")
	if ok && doc.GetContent() != "" && len(doc.GetContent()) <= MaxFileBytes {
		path := "sync:" + doc.GetId()
		l.seen[path] = true
		content := stripComments(doc.GetContent())
		*out = append(*out, Instruction{Path: path, Origin: OriginUser, Trigger: TriggerStartup, Content: strings.TrimSpace(content)})
		if l.harnessHome != "" {
			l.addImports(out, content, filepath.Join(l.harnessHome, "AGENTS.md"), path, OriginUser, 1, true)
		}
	}
	docs := l.opts.Docs.List(syncv1.DocumentKind_DOCUMENT_KIND_RULE)
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].GetKey() < docs[j].GetKey() })
	for _, d := range docs {
		if d.GetScope() == commonv1.Scope_SCOPE_USER && len(d.GetContent()) <= MaxFileBytes {
			l.addRule(out, "sync:"+d.GetId(), l.cwd, d.GetContent(), OriginUser, TriggerStartup)
		}
	}
}

// addDir loads the instruction files and rules of one directory. The walk from the root passes through a .claude folder when
// the project is checked out below one; nothing above the project there is read.
func (l *Loader) addDir(out *[]Instruction, d string, trig Trigger) {
	if l.isImportSource(d) {
		return
	}
	for _, n := range instructionNames {
		p := filepath.Join(d, n)
		if l.isUserConfig(p) {
			continue
		}
		l.addFile(out, p, originOf(n), trig, "", 0, false)
	}
	harnessDir := filepath.Join(d, ".harness")
	if !l.isUserConfig(harnessDir) {
		l.addRulesDir(out, harnessDir, d, OriginRule, trig)
	}
}

// addFile loads p (if present and not yet loaded) and its imports. depth is
// the number of import hops that led to p.
func (l *Loader) addFile(out *[]Instruction, p string, origin Origin, trig Trigger, from string, depth int, trusted bool) {
	p = filepath.Clean(p)
	if l.seen[p] {
		return
	}
	if linksToImportSource(p) {
		return
	}
	raw, ok := readFile(p)
	if !ok {
		return
	}
	l.seen[p] = true
	content := stripComments(raw)
	if text := strings.TrimSpace(content); text != "" {
		*out = append(*out, Instruction{Path: p, Origin: origin, Trigger: trig, Content: text, ImportedFrom: from})
	}
	l.addImports(out, content, p, p, origin, depth+1, trusted)
}

// addImports loads the @imports of content. base is the file relative to
// which paths resolve; label is the importer reported in ImportedFrom.
func (l *Loader) addImports(out *[]Instruction, content, base, label string, origin Origin, depth int, trusted bool) {
	if depth > MaxImportDepth {
		return
	}
	for _, raw := range findImports(content) {
		p := ResolveImport(raw, filepath.Dir(base), l.home)
		if p == "" || l.seen[p] || l.isImportSource(p) {
			continue
		}
		if !trusted && !l.inTrusted(p) {
			if l.opts.Approvals != nil {
				approved, decided := l.opts.Approvals.Decision(p)
				if decided && !approved {
					continue
				}
				if !decided {
					l.pending[p] = PendingImport{Path: p, ImportedFrom: label}
					continue
				}
			} else {
				l.pending[p] = PendingImport{Path: p, ImportedFrom: label}
				continue
			}
		}
		l.addFile(out, p, origin, TriggerInclude, label, depth, trusted)
	}
}

// ResolveImport maps an import token, relative to dir, to an existing file path, or "". Trailing punctuation is tolerated
// ("see @README.md.").
func ResolveImport(raw, dir, home string) string {
	for _, cand := range []string{raw, strings.TrimRight(raw, ".,;:!?)]}'\"")} {
		if cand == "" {
			continue
		}
		var p string
		switch {
		case cand == "~" || strings.HasPrefix(cand, "~/"):
			if home == "" {
				continue
			}
			p = filepath.Join(home, strings.TrimPrefix(cand, "~"))
		case filepath.IsAbs(cand):
			p = cand
		default:
			p = filepath.Join(dir, cand)
		}
		p = filepath.Clean(p)
		if isFile(p) {
			return p
		}
	}
	return ""
}

func (l *Loader) inTrusted(p string) bool {
	for _, d := range l.trusted {
		if within(p, d) {
			return true
		}
	}
	return false
}

// addRulesDir registers <dir>/rules/**/*.md. Rules without paths:
// frontmatter are loaded immediately; path-scoped ones wait for ForPath.
func (l *Loader) addRulesDir(out *[]Instruction, dir, root string, origin Origin, trig Trigger) {
	rulesDir := filepath.Join(dir, "rules")
	st, err := os.Stat(rulesDir)
	if err != nil || !st.IsDir() {
		return
	}
	var files []string
	_ = filepath.WalkDir(rulesDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".md") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	for _, p := range files {
		if l.seen[p] {
			continue
		}
		raw, ok := readFile(p)
		if !ok {
			continue
		}
		l.addRule(out, p, root, raw, origin, trig)
	}
}

// addRule registers one rule; path names it (a file, or "sync:<doc id>").
func (l *Loader) addRule(out *[]Instruction, path, root, raw string, origin Origin, trig Trigger) {
	l.seen[path] = true
	body := raw
	var globs []string
	if fm, b, ok := frontmatter(raw); ok {
		body = b
		globs = frontmatterList(fm, "paths")
	}
	r := &rule{path: path, root: root, globs: globs, content: stripComments(body), origin: origin}
	l.rules = append(l.rules, r)
	if len(globs) == 0 {
		l.loadRule(out, r, trig)
	}
}

func (l *Loader) loadRule(out *[]Instruction, r *rule, trig Trigger) {
	r.loaded = true
	if text := strings.TrimSpace(r.content); text != "" {
		*out = append(*out, Instruction{Path: r.path, Origin: OriginRule, Trigger: trig, Content: text, Globs: r.globs})
	}
	l.addImports(out, r.content, r.path, r.path, OriginRule, 1, r.origin == OriginUser)
}

func (r *rule) matches(path string) bool {
	rel, err := filepath.Rel(r.root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	rel = filepath.ToSlash(rel)
	for _, g := range r.globs {
		g = strings.TrimPrefix(g, "./")
		if ok, _ := doublestar.Match(g, rel); ok {
			return true
		}
		if !strings.Contains(g, "/") {
			if ok, _ := doublestar.Match(g, filepath.Base(rel)); ok {
				return true
			}
		}
	}
	return false
}

func (l *Loader) addAutoMemory(out *[]Instruction) {
	if l.opts.AutoMemoryDir == "" {
		return
	}
	path := filepath.Join(l.opts.AutoMemoryDir, AutoMemoryIndex)
	var content string
	found := false
	// What this machine wrote and has not sent yet is newer than any document.
	if l.synced() && l.opts.ProjectKey != "" && !HasPendingAutoMemory(l.opts.AutoMemoryDir) {
		if doc, ok := l.opts.Docs.Lookup(commonv1.Scope_SCOPE_PROJECT, syncv1.DocumentKind_DOCUMENT_KIND_MEMORY, "user", l.opts.ProjectKey); ok {
			content, found = doc.GetContent(), true
		}
	}
	if !found {
		raw, ok := readFile(path)
		if !ok {
			return
		}
		content = raw
	}
	content = strings.TrimSpace(TruncateIndex(content))
	if content == "" {
		return
	}
	l.seen[path] = true
	*out = append(*out, Instruction{Path: path, Origin: OriginAutoMemory, Trigger: TriggerStartup, Content: content})
}

// TruncateIndex keeps the first AutoMemoryMaxLines lines of a MEMORY.md
// index, capped at AutoMemoryMaxBytes (cut at a line boundary).
func TruncateIndex(s string) string {
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > AutoMemoryMaxLines {
		lines = lines[:AutoMemoryMaxLines]
	}
	s = strings.Join(lines, "")
	if len(s) > AutoMemoryMaxBytes {
		cut := s[:AutoMemoryMaxBytes]
		if i := strings.LastIndexByte(cut, '\n'); i > 0 {
			cut = cut[:i+1]
		}
		s = cut
	}
	return s
}

// ForPath returns the instructions that become applicable when the model
// touches path (reads, edits or lists it): AGENTS.md files of
// subdirectories between the working directory and path, and path-scoped
// rules whose globs match. Each source is returned at most once per Loader.
func (l *Loader) ForPath(path string) []Instruction {
	if path == "" || l.opts.Mode == ModeManagedOnly {
		return nil
	}
	p := clean(path)
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Instruction
	if within(p, l.cwd) && p != l.cwd {
		dir := p
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			dir = filepath.Dir(p)
		}
		rel, _ := filepath.Rel(l.cwd, dir)
		d := l.cwd
		if rel != "." {
			for _, part := range strings.Split(rel, string(filepath.Separator)) {
				d = filepath.Join(d, part)
				if l.visited[d] {
					continue
				}
				l.visited[d] = true
				l.addDir(&out, d, TriggerNested)
			}
		}
	}
	for _, r := range l.rules {
		if !r.loaded && r.matches(p) {
			l.loadRule(&out, r, TriggerPath)
		}
	}
	return out
}
