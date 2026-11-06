package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/xlsft/harness/packages/client/memory"
	"github.com/xlsft/harness/packages/client/permissions"
)

// Project import: a folder's Claude Code files brought into .harness and AGENTS.md, on the user's say. This is the only code
// that reads them; once imported they are ordinary project files, and what they enable passes the same gates as any other
// (hooks and committed allow rules need the folder trusted). Sources are only ever read.
//
//	CLAUDE.md, .claude/CLAUDE.md       → AGENTS.md (appended, each as one marked block)
//	.claude/AGENTS.md                  → .harness/AGENTS.md
//	CLAUDE.local.md                    → .harness/AGENTS.local.md (kept out of git)
//	.claude/rules/**                   → .harness/rules/**
//	.claude/settings.json              → .harness/settings.json (merged)
//	.claude/settings.local.json        → .harness/settings.local.json (merged, kept out of git, only in a trusted folder)
//	.claude/hooks/**                   → .harness/hooks/** (commands that name them are rewritten)
//	.claude/{skills,commands,agents,output-styles,workflows}/** → the same under .harness
//
// Subfolders with such files of their own get the same treatment next to them. .mcp.json is not touched: it is the project's
// own format and stays live.

const (
	projectMaxDepth   = 8
	projectMaxEntries = 50000
)

// projectSkipDirs hold no one's project instructions, and can be huge.
var projectSkipDirs = map[string]bool{"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true}

// ProjectOptions configure RunProject.
type ProjectOptions struct {
	// Root is the folder to import into (ProjectRoot finds it).
	Root string
	// Home is the user's home: its .claude is user-level, and stays out of this.
	Home string
	// Trusted is the folder trust decision. Project-local settings apply without it, so they wait for it.
	Trusted bool
	// DryRun says what would happen and writes nothing.
	DryRun bool
	// Force replaces what an earlier import or the user put in place; otherwise differing files stay.
	Force bool
}

// ProjectItem is one thing a project import brings.
type ProjectItem struct {
	// Kind: "instructions", "rule", "settings", "hook-script", "skill", "command", "agent", "output-style", "workflow".
	Kind string
	// Source and Target are slash paths relative to the root.
	Source, Target string
	// Status: StatusPlanned (a run brings it), StatusCopied (it did), StatusExists, StatusSkipped or StatusError.
	Status Status
	Note   string
	// NeedsTrust: it holds hooks or allow rules, which take effect only in a trusted folder.
	NeedsTrust bool
}

// ProjectReport is what a project import found or did.
type ProjectReport struct {
	Root  string
	Items []ProjectItem
}

// Planned counts the items a run would bring.
func (r *ProjectReport) Planned() int {
	n := 0
	for _, it := range r.Items {
		if it.Status == StatusPlanned {
			n++
		}
	}
	return n
}

// HasErrors reports whether any item failed.
func (r *ProjectReport) HasErrors() bool {
	return slices.ContainsFunc(r.Items, func(it ProjectItem) bool { return it.Status == StatusError })
}

// ProjectRoot is where a folder's import goes: its repository's root, or the folder itself when there is no repository or the
// home directory is one.
func ProjectRoot(dir, home string) string {
	root := permissions.RepoRoot(dir)
	if home != "" && samePath(root, home) {
		return filepath.Clean(dir)
	}
	return root
}

func samePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// HasHarnessFolder reports whether root has its own .harness. What the client itself puts there while the user works (session
// worktrees, "always allow" rules) doesn't count: nobody chose that over an import.
func HasHarnessFolder(root string) bool {
	entries, err := os.ReadDir(filepath.Join(root, ".harness"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		switch e.Name() {
		case "worktrees", "settings.local.json", ".DS_Store":
		default:
			return true
		}
	}
	return false
}

type projectRun struct {
	o        ProjectOptions
	rootReal string
	rep      *ProjectReport
	// pending holds what earlier items of a dry run would have written, so that later ones see it.
	pending map[string]string
}

// RunProject imports a folder's Claude Code files; with DryRun it only reports. It never writes below a .claude folder, nor
// through a symbolic link.
func RunProject(ctx context.Context, o ProjectOptions) (*ProjectReport, error) {
	if o.Root == "" {
		return nil, errors.New("importer: Root is required")
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return nil, err
	}
	o.Root = filepath.Clean(root)
	real, err := filepath.EvalSymlinks(o.Root)
	if err != nil {
		return nil, err
	}
	r := &projectRun{o: o, rootReal: real, rep: &ProjectReport{Root: o.Root}, pending: map[string]string{}}
	if o.Home != "" && samePath(o.Root, o.Home) {
		return r.rep, nil // the home folder's .claude is the user's: Settings > Import, `import claude`
	}
	for _, d := range r.projectDirs(ctx) {
		if err := ctx.Err(); err != nil {
			return r.rep, err
		}
		r.dir(d)
	}
	return r.rep, ctx.Err()
}

// projectDirs lists the root and the subfolders with something to import, root first.
func (r *projectRun) projectDirs(ctx context.Context) []string {
	var out []string
	entries := 0
	_ = filepath.WalkDir(r.o.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entries++; entries > projectMaxEntries || ctx.Err() != nil {
			return filepath.SkipAll
		}
		if !d.IsDir() {
			return nil
		}
		if p != r.o.Root {
			rel, _ := filepath.Rel(r.o.Root, p)
			name := d.Name()
			if strings.HasPrefix(name, ".") || projectSkipDirs[name] || strings.Count(rel, string(filepath.Separator)) >= projectMaxDepth ||
				(r.o.Home != "" && samePath(p, r.o.Home)) || exists(filepath.Join(p, ".git")) {
				return filepath.SkipDir // a repository of its own gets its own import
			}
		}
		if exists(filepath.Join(p, ".claude")) || exists(filepath.Join(p, "CLAUDE.md")) || exists(filepath.Join(p, "CLAUDE.local.md")) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func (r *projectRun) dir(d string) {
	claude := filepath.Join(d, ".claude")
	harness := filepath.Join(d, ".harness")
	r.instructions(filepath.Join(d, "CLAUDE.md"), filepath.Join(d, "AGENTS.md"))
	r.instructions(filepath.Join(claude, "CLAUDE.md"), filepath.Join(d, "AGENTS.md"))
	r.instructions(filepath.Join(claude, "AGENTS.md"), filepath.Join(harness, "AGENTS.md"))
	r.instructions(filepath.Join(d, "CLAUDE.local.md"), filepath.Join(d, memory.LocalName))
	r.entries("rule", filepath.Join(claude, "rules"), filepath.Join(harness, "rules"), true)
	r.settings(filepath.Join(claude, "settings.json"), filepath.Join(harness, "settings.json"), false)
	r.settings(filepath.Join(claude, "settings.local.json"), filepath.Join(harness, "settings.local.json"), true)
	r.entries("hook-script", filepath.Join(claude, "hooks"), filepath.Join(harness, "hooks"), false)
	r.entries("skill", filepath.Join(claude, "skills"), filepath.Join(harness, "skills"), false)
	r.entries("command", filepath.Join(claude, "commands"), filepath.Join(harness, "commands"), false)
	r.entries("agent", filepath.Join(claude, "agents"), filepath.Join(harness, "agents"), false)
	r.entries("output-style", filepath.Join(claude, "output-styles"), filepath.Join(harness, "output-styles"), false)
	r.entries("workflow", filepath.Join(claude, "workflows"), filepath.Join(harness, "workflows"), false)
}

func (r *projectRun) rel(p string) string {
	rel, err := filepath.Rel(r.o.Root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
}

func (r *projectRun) add(kind, src, dst string, st Status, note string) *ProjectItem {
	r.rep.Items = append(r.rep.Items, ProjectItem{Kind: kind, Source: r.rel(src), Target: r.rel(dst), Status: st, Note: note})
	return &r.rep.Items[len(r.rep.Items)-1]
}

// readSource reads a file to import. A link leading out of the folder is not followed: a repository must not be able to pull
// a file of the user's into itself by way of an import.
func (r *projectRun) readSource(p string, limit int64) ([]byte, os.FileMode, error) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil, 0, err
	}
	if !withinDir(real, r.rootReal) {
		return nil, 0, errors.New("a link to outside this folder")
	}
	st, err := os.Stat(real)
	if err != nil {
		return nil, 0, err
	}
	if !st.Mode().IsRegular() {
		return nil, 0, errors.New("not a regular file")
	}
	if st.Size() > limit {
		return nil, 0, errors.New("too large")
	}
	data, err := os.ReadFile(real)
	return data, st.Mode(), err
}

func withinDir(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// checkTarget refuses a target reached through a symbolic link: .harness may be a link into .claude, or out of the folder.
func (r *projectRun) checkTarget(p string) error {
	rel, err := filepath.Rel(r.o.Root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("outside this folder")
	}
	if rel == "." {
		return nil
	}
	cur := r.o.Root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if err != nil {
			return nil
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; replace it with a folder or a file first", r.rel(cur))
		}
	}
	return nil
}

func (r *projectRun) readTarget(p string) (string, bool) {
	if s, ok := r.pending[p]; ok {
		return s, true
	}
	data, err := os.ReadFile(p)
	return string(data), err == nil
}

func (r *projectRun) write(p string, data []byte, mode os.FileMode) error {
	if r.o.DryRun {
		r.pending[p] = string(data)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".import-tmp"
	if err := os.WriteFile(tmp, data, mode.Perm()); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (r *projectRun) doneStatus() Status {
	if r.o.DryRun {
		return StatusPlanned
	}
	return StatusCopied
}

// instructions brings one instruction file into its target.
func (r *projectRun) instructions(src, dst string) {
	const kind = "instructions"
	st, err := os.Lstat(src)
	if err != nil {
		return
	}
	raw, _, err := r.readSource(src, memory.MaxFileBytes)
	if err != nil {
		r.add(kind, src, dst, StatusSkipped, err.Error())
		return
	}
	// A link either way between source and target: CLAUDE.md → AGENTS.md has nothing to bring; AGENTS.md → CLAUDE.md must become
	// a file of its own, as the link's target is no longer read.
	linked := false
	if dt, err := os.Lstat(dst); err == nil && dt.Mode()&fs.ModeSymlink != 0 {
		real, err := filepath.EvalSymlinks(dst)
		srcReal, _ := filepath.EvalSymlinks(src)
		if err != nil || real != srcReal {
			r.add(kind, src, dst, StatusError, r.rel(dst)+" is a symbolic link; replace it with a file first")
			return
		}
		linked = true
	} else if st.Mode()&fs.ModeSymlink != 0 && samePath(src, dst) {
		r.add(kind, src, dst, StatusExists, "a link to "+r.rel(dst))
		return
	}
	if err := r.checkTarget(filepath.Dir(dst)); err != nil {
		r.add(kind, src, dst, StatusError, err.Error())
		return
	}
	var notes []string
	content := strings.TrimSpace(r.rewriteRefs(string(raw), src, dst, map[string]bool{src: true}, 1, &notes))
	if strings.TrimSpace(memory.StripComments(content)) == "" {
		r.add(kind, src, dst, StatusSkipped, joinNotes(append(notes, "nothing but references to what is already there")))
		return
	}
	cur := ""
	if !linked {
		cur, _ = r.readTarget(dst)
	}
	next, status, note := mergeBlock(cur, r.rel(src), content, r.o.Force)
	if note != "" {
		notes = append(notes, note)
	}
	if status != StatusPlanned {
		r.add(kind, src, dst, status, joinNotes(notes))
		return
	}
	if err := r.write(dst, []byte(next), 0o644); err != nil {
		r.add(kind, src, dst, StatusError, err.Error())
		return
	}
	if filepath.Base(dst) == filepath.Base(memory.LocalName) {
		notes = r.excluded(dst, notes)
	}
	r.add(kind, src, dst, r.doneStatus(), joinNotes(notes))
}

func joinNotes(notes []string) string { return strings.Join(notes, "; ") }

// mergeBlock adds content to an instructions file as one block marked with its source, in comments the loader drops (no
// backtick in them: that would read as code, and the comment would stay). A second run finds the mark, or finds the text itself
// when the user moved it out of the block. status is StatusPlanned when next differs from cur.
func mergeBlock(cur, label, content string, force bool) (next string, status Status, note string) {
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:8])
	block := fmt.Sprintf("<!-- harness:import %s %s -->\n%s\n<!-- /harness:import %s -->", label, hash, content, label)
	re := regexp.MustCompile(`(?s)<!-- harness:import ` + regexp.QuoteMeta(label) + ` ([0-9a-f]+) -->.*?<!-- /harness:import ` + regexp.QuoteMeta(label) + ` -->`)
	if m := re.FindStringSubmatch(cur); m != nil {
		switch {
		case m[1] == hash:
			return cur, StatusExists, "already imported"
		case !force:
			return cur, StatusSkipped, "changed since it was imported; import with force to replace that part"
		}
		// Not ReplaceAllString: the content is text, not a template.
		loc := re.FindStringIndex(cur)
		return cur[:loc[0]] + block + cur[loc[1]:], StatusPlanned, "replaces the earlier import"
	}
	if strings.Contains(cur, content) {
		return cur, StatusExists, "already there"
	}
	if strings.TrimSpace(cur) == "" {
		return block + "\n", StatusPlanned, ""
	}
	return strings.TrimRight(cur, "\n") + "\n\n" + block + "\n", StatusPlanned, "added to the end"
}

// rewriteRefs makes the @imports of an instruction file work from where it lands. A file that will not be read any more (below
// .claude, or a CLAUDE.md) has its text taken in; one elsewhere in the project stays a reference, its path adjusted; the target
// itself is dropped. Files outside the folder are never taken in: a reference to one stays and asks for approval when loaded,
// as it would in any AGENTS.md.
func (r *projectRun) rewriteRefs(content, src, dst string, seen map[string]bool, depth int, notes *[]string) string {
	return memory.RewriteImports(content, func(raw string) string {
		keep := "@" + strings.ReplaceAll(raw, " ", `\ `)
		target := memory.ResolveImport(raw, filepath.Dir(src), r.o.Home)
		if target == "" {
			return keep // not a file: an email address, a handle
		}
		real, err := filepath.EvalSymlinks(target)
		if err != nil || !withinDir(real, r.rootReal) {
			*notes = append(*notes, keep+" is outside this folder and was not copied")
			return keep
		}
		if target == dst || samePath(target, dst) {
			return "" // an AGENTS.md in its own imports
		}
		rel := r.rel(target)
		if !isClaudeSource(rel) {
			if filepath.Dir(src) == filepath.Dir(dst) {
				return keep
			}
			back, err := filepath.Rel(filepath.Dir(dst), target)
			if err != nil {
				return keep
			}
			return "@" + strings.ReplaceAll(filepath.ToSlash(back), " ", `\ `)
		}
		if seen[target] {
			return ""
		}
		if depth > memory.MaxImportDepth {
			*notes = append(*notes, keep+" is nested too deep and was left out")
			return ""
		}
		data, _, err := r.readSource(target, memory.MaxFileBytes)
		if err != nil {
			*notes = append(*notes, keep+" could not be read: "+err.Error())
			return ""
		}
		seen[target] = true
		return strings.TrimSpace(r.rewriteRefs(string(data), target, dst, seen, depth+1, notes))
	})
}

// isClaudeSource says a path relative to the root is one the client no longer reads.
func isClaudeSource(rel string) bool {
	parts := strings.Split(rel, "/")
	return memory.IsImportSourceName(parts[len(parts)-1]) || slices.Contains(parts, ".claude")
}

// entries brings each thing in a folder as one item: a file, or a folder with what is in it. transform runs the rule files'
// references through rewriteRefs.
func (r *projectRun) entries(kind, srcDir, dstDir string, transform bool) {
	list, err := os.ReadDir(srcDir)
	if err != nil {
		return
	}
	for _, e := range list {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		r.entry(kind, filepath.Join(srcDir, e.Name()), filepath.Join(dstDir, e.Name()), transform)
	}
}

type plannedFile struct {
	dst  string
	data []byte
	mode os.FileMode
}

func (r *projectRun) entry(kind, src, dst string, transform bool) {
	files, skipped, err := r.plan(src, dst, transform)
	if err != nil {
		r.add(kind, src, dst, StatusSkipped, err.Error())
		return
	}
	var notes []string
	if len(skipped) > 0 {
		notes = append(notes, "left out "+strings.Join(skipped, ", "))
	}
	if len(files) == 0 {
		r.add(kind, src, dst, StatusSkipped, joinNotes(append(notes, "nothing to bring")))
		return
	}
	same, differ := 0, false
	for _, f := range files {
		if err := r.checkTarget(f.dst); err != nil {
			r.add(kind, src, dst, StatusError, err.Error())
			return
		}
		if cur, ok := r.readTarget(f.dst); ok {
			if cur == string(f.data) {
				same++
			} else {
				differ = true
			}
		}
	}
	switch {
	case same == len(files):
		r.add(kind, src, dst, StatusExists, joinNotes(append(notes, "already there")))
		return
	case differ && !r.o.Force:
		r.add(kind, src, dst, StatusSkipped, joinNotes(append(notes, r.rel(dst)+" differs and was left as it is; import with force to replace it")))
		return
	}
	for _, f := range files {
		if err := r.write(f.dst, f.data, f.mode); err != nil {
			r.add(kind, src, dst, StatusError, err.Error())
			return
		}
	}
	if len(files) > 1 {
		notes = append(notes, fmt.Sprintf("%d files", len(files)))
	}
	r.add(kind, src, dst, r.doneStatus(), joinNotes(notes))
}

// plan lists the files of src, a file or a folder. Links and oversized files stay behind, and are named.
func (r *projectRun) plan(src, dst string, transform bool) (files []plannedFile, skipped []string, err error) {
	st, err := os.Lstat(src)
	if err != nil {
		return nil, nil, err
	}
	if st.Mode()&fs.ModeSymlink != 0 {
		return nil, nil, errors.New("a symbolic link, not copied")
	}
	total := 0
	addFile := func(p, to, name string) {
		data, mode, err := r.readSource(p, MaxSkillFileBytes)
		if err != nil {
			skipped = append(skipped, name+" ("+err.Error()+")")
			return
		}
		if total += len(data); total > MaxSkillTotalBytes {
			skipped = append(skipped, name+" (too large)")
			return
		}
		if transform && strings.EqualFold(filepath.Ext(p), ".md") {
			var notes []string
			data = []byte(r.rewriteRefs(string(data), p, to, map[string]bool{p: true}, 1, &notes))
			skipped = append(skipped, notes...)
		}
		files = append(files, plannedFile{dst: to, data: data, mode: mode})
	}
	if !st.IsDir() {
		addFile(src, dst, filepath.Base(src))
		return files, skipped, nil
	}
	_ = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == src {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			skipped = append(skipped, filepath.ToSlash(rel)+" (symbolic link)")
		case !d.IsDir():
			addFile(p, filepath.Join(dst, rel), filepath.ToSlash(rel))
		}
		return nil
	})
	return files, skipped, nil
}

// settings merges a settings file into its .harness counterpart.
func (r *projectRun) settings(src, dst string, local bool) {
	const kind = "settings"
	if !exists(src) {
		return
	}
	raw, _, err := r.readSource(src, memory.MaxFileBytes)
	if err != nil {
		r.add(kind, src, dst, StatusSkipped, err.Error())
		return
	}
	in, err := decodeObject(raw)
	if err != nil {
		r.add(kind, src, dst, StatusError, "invalid JSON: "+err.Error())
		return
	}
	var notes []string
	if _, ok := in["apiKeyHelper"]; ok {
		delete(in, "apiKeyHelper")
		notes = append(notes, "apiKeyHelper dropped (keys live on the server)")
	}
	notes = append(notes, rewriteCommands(in)...)
	needsTrust := false
	if d := describeSettings(in); d != "" {
		notes = append(notes, d)
		needsTrust = true
	}
	// A project-local file applies without folder trust, and a repository can commit one. Whatever is in this one must not get
	// past the trust that its source, as a live file, would have needed: it waits for the folder to be trusted.
	if local && !r.o.Trusted {
		it := r.add(kind, src, dst, StatusSkipped, joinNotes(append(notes, "trust this folder, then import again: local settings apply without trust")))
		it.NeedsTrust = needsTrust
		return
	}
	if err := r.checkTarget(dst); err != nil {
		r.add(kind, src, dst, StatusError, err.Error())
		return
	}
	cur := map[string]any{}
	if text, ok := r.readTarget(dst); ok {
		if cur, err = decodeObject([]byte(text)); err != nil {
			r.add(kind, src, dst, StatusError, r.rel(dst)+" is not valid JSON and was left as it is")
			return
		}
	}
	merged := mergeObjects(cur, in, r.o.Force)
	if canonical(merged) == canonical(cur) {
		it := r.add(kind, src, dst, StatusExists, joinNotes(append(notes, "already there")))
		it.NeedsTrust = needsTrust
		return
	}
	out, err := marshalSettings(merged)
	if err == nil {
		err = r.write(dst, out, 0o644)
	}
	if err != nil {
		r.add(kind, src, dst, StatusError, err.Error())
		return
	}
	if local {
		notes = r.excluded(dst, notes)
	}
	it := r.add(kind, src, dst, r.doneStatus(), joinNotes(notes))
	it.NeedsTrust = needsTrust
}

func decodeObject(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	out := map[string]any{}
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func marshalSettings(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func canonical(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// mergeObjects returns dst with src added: objects merge key by key, lists gain what they lack, and a value both set stays
// dst's unless force.
func mergeObjects(dst, src map[string]any, force bool) map[string]any {
	out := make(map[string]any, len(dst)+len(src))
	for k, v := range dst {
		out[k] = v
	}
	for k, sv := range src {
		dv, ok := out[k]
		if !ok {
			out[k] = sv
			continue
		}
		switch d := dv.(type) {
		case map[string]any:
			if s, ok := sv.(map[string]any); ok {
				out[k] = mergeObjects(d, s, force)
				continue
			}
		case []any:
			if s, ok := sv.([]any); ok {
				list := slices.Clone(d)
				have := map[string]bool{}
				for _, x := range list {
					have[canonical(x)] = true
				}
				for _, x := range s {
					if c := canonical(x); !have[c] {
						have[c] = true
						list = append(list, x)
					}
				}
				out[k] = list
				continue
			}
		}
		if force {
			out[k] = sv
		}
	}
	return out
}

var claudeDirRef = regexp.MustCompile(`\.claude/(hooks|skills|commands|agents|output-styles|workflows|rules)/`)

// rewriteCommands points hook and status line commands at the copies of what they run. Anything else under .claude that they
// name is not copied, and the note says so.
func rewriteCommands(m map[string]any) []string {
	var left []string
	var visit func(v any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if s, ok := val.(string); ok && k == "command" {
					s = claudeDirRef.ReplaceAllString(s, ".harness/$1/")
					x[k] = s
					if strings.Contains(s, ".claude/") {
						left = append(left, s)
					}
					continue
				}
				visit(val)
			}
		case []any:
			for _, e := range x {
				visit(e)
			}
		}
	}
	visit(m["hooks"])
	visit(m["statusLine"])
	if len(left) == 0 {
		return nil
	}
	sort.Strings(left)
	return []string{"still names a file under .claude: " + strings.Join(left, ", ")}
}

// describeSettings says what in the settings needs the folder trusted; "" when nothing does.
func describeSettings(m map[string]any) string {
	var parts []string
	if perms, ok := m["permissions"].(map[string]any); ok {
		if list, ok := perms["allow"].([]any); ok && len(list) > 0 {
			parts = append(parts, fmt.Sprintf("%d allow rules", len(list)))
		}
	}
	if hooks, ok := m["hooks"].(map[string]any); ok && len(hooks) > 0 {
		names := make([]string, 0, len(hooks))
		for k := range hooks {
			names = append(names, k)
		}
		sort.Strings(names)
		parts = append(parts, "hooks on "+strings.Join(names, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " and ") + ", which apply only once this folder is trusted"
}

// excluded keeps a private file out of git through info/exclude, which stays on this machine like the file itself.
func (r *projectRun) excluded(p string, notes []string) []string {
	if r.o.DryRun {
		return notes
	}
	if err := excludeFromGit(r.o.Root, r.rel(p)); err != nil {
		return append(notes, "not excluded from git: "+err.Error())
	}
	return notes
}

func excludeFromGit(root, rel string) error {
	if !exists(filepath.Join(root, ".git")) {
		return nil
	}
	cmd := exec.Command("git", "rev-parse", "--git-path", "info/exclude")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	line := "/" + rel
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if slices.Contains(strings.Split(string(data), "\n"), line) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		line = "\n" + line
	}
	_, err = f.WriteString(line + "\n")
	return err
}

// OfferStore is this device's memory of the folders whose offer was dismissed. Device-local: never synced.
type OfferStore struct {
	Path string
}

var offerMu sync.Mutex

type offerFile struct {
	Dismissed map[string]bool `json:"dismissed"`
}

func (s OfferStore) load() offerFile {
	var f offerFile
	if data, err := os.ReadFile(s.Path); err == nil {
		_ = json.Unmarshal(data, &f)
	}
	if f.Dismissed == nil {
		f.Dismissed = map[string]bool{}
	}
	return f
}

func offerKey(root string) string {
	if real, err := filepath.EvalSymlinks(root); err == nil {
		return real
	}
	return filepath.Clean(root)
}

// Dismissed reports whether the offer for root was dismissed here.
func (s OfferStore) Dismissed(root string) bool {
	offerMu.Lock()
	defer offerMu.Unlock()
	return s.load().Dismissed[offerKey(root)]
}

// SetDismissed remembers, or forgets, a dismissal.
func (s OfferStore) SetDismissed(root string, dismissed bool) error {
	offerMu.Lock()
	defer offerMu.Unlock()
	f := s.load()
	if dismissed {
		f.Dismissed[offerKey(root)] = true
	} else {
		delete(f.Dismissed, offerKey(root))
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}
