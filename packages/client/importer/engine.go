package importer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/memory"
)

// EngineOptions configure ScanSource and RunSource.
type EngineOptions struct {
	Env Env
	// Putter uploads user-level documents; required for a run that has any.
	Putter Putter
	// Exists says whether the server has a document (a scan marks those); nil: nothing is known to exist.
	Exists func(ctx context.Context, ref *syncv1.DocumentRef) (bool, error)
	// MCP receives user-level MCP servers; nil: they are listed for manual setup.
	MCP MCPTarget
	// Trusted is the folder trust decision for a root; nil: not trusted.
	Trusted func(root string) bool
	// IncludeSecrets stores values that look like secrets; Force replaces what is already there.
	IncludeSecrets, Force bool
	// ForceHint and SecretsHint name the switches in notes; defaults: the CLI's flags.
	ForceHint, SecretsHint string
	// OnProgress follows a run; it is called on RunSource's goroutine.
	OnProgress func(SourceProgress)
}

// SourceItem is one line of a scan or a run's report.
type SourceItem struct {
	// ID identifies the item between a scan and a run: RunSource takes the IDs the user chose.
	ID      string
	Section string
	Kind    Kind
	Name    string
	// Source is the file it was read from; Target where it goes ("AGENTS.md", "rule gemini/GEMINI.md",
	// "this device").
	Source, Target string
	// Status: StatusPlanned (an import brings it), StatusExists, StatusSkipped, StatusNoted (set up by hand),
	// StatusError; after a run StatusUploaded or StatusCopied for what came.
	Status Status
	Note   string
	// NeedsTrust: it holds hooks or allow rules, which apply only once the folder is trusted.
	NeedsTrust bool
	// HasSecrets: it has values that look like secrets, so Include secrets changes what it brings.
	HasSecrets bool
}

// SourceScan is what a scan found, or what a run did.
type SourceScan struct {
	// Root is the folder imported into ("" at user level).
	Root string
	// Found says the source had anything at all.
	Found bool
	Items []SourceItem
	// ServerChecked: every planned document was looked up on the server.
	ServerChecked bool
	// Trusted is the folder's trust decision.
	Trusted bool
}

// SourceProgress is one step of a run: the item started, or, with Item, finished.
type SourceProgress struct {
	Current     string
	Done, Total int
	Item        *SourceItem
}

// entry is a found item on its way: the line the user sees and what produced it.
type entry struct {
	item  SourceItem
	found Found
}

type engine struct {
	o     EngineOptions
	src   SourceInfo
	root  string
	pr    *projectRun // folder level
	mcp   map[string]ExistingMCP
	items []entry
}

// ScanSource previews an import of src: user level when folder is "", else the folder's project. It writes nothing.
func ScanSource(ctx context.Context, src Source, folder string, o EngineOptions) (*SourceScan, error) {
	e, err := newEngine(ctx, src, folder, o, true)
	if err != nil {
		return nil, err
	}
	res := e.result()
	if folder == "" && o.Exists != nil {
		res.ServerChecked = e.markExisting(ctx)
		res.Items = e.lines()
	}
	return res, ctx.Err()
}

// RunSource imports the items of src whose IDs are chosen (the source is read again: what changed since the scan is
// what is imported). Items not chosen are left out of the report.
func RunSource(ctx context.Context, src Source, folder string, ids []string, o EngineOptions) (*SourceScan, error) {
	chosen := map[string]bool{}
	for _, id := range ids {
		chosen[id] = true
	}
	if len(chosen) == 0 {
		return nil, errors.New("nothing was chosen to import")
	}
	e, err := newEngine(ctx, src, folder, o, false)
	if err != nil {
		return nil, err
	}
	var todo []int
	for i := range e.items {
		if chosen[e.items[i].item.ID] {
			todo = append(todo, i)
		}
	}
	err = e.apply(ctx, todo)
	res := e.result()
	res.Items = nil
	for _, i := range todo {
		res.Items = append(res.Items, e.items[i].item)
	}
	if err == nil {
		err = ctx.Err()
	}
	return res, err
}

func newEngine(ctx context.Context, src Source, folder string, o EngineOptions, scan bool) (*engine, error) {
	if o.ForceHint == "" {
		o.ForceHint = "--force"
	}
	if o.SecretsHint == "" {
		o.SecretsHint = "--include-secrets"
	}
	e := &engine{o: o, src: src.Info()}
	var found []Found
	if folder == "" {
		if !e.src.User {
			return nil, fmt.Errorf("%s imports from a folder: choose one", e.src.Name)
		}
		list, err := src.ScanUser(ctx, o.Env)
		if err != nil {
			return nil, err
		}
		found = list
		for _, f := range found {
			if f.Kind == KindMCP && o.MCP != nil {
				if e.mcp, err = existingMCP(ctx, o.MCP); err != nil {
					return nil, fmt.Errorf("listing MCP servers: %w", err)
				}
				break
			}
		}
	} else {
		if !e.src.Folder {
			return nil, fmt.Errorf("%s imports user-level files only", e.src.Name)
		}
		abs, err := filepath.Abs(folder)
		if err != nil {
			return nil, err
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("%s is not a folder", folder)
		}
		root := ProjectRoot(abs, o.Env.Home)
		if o.Env.Home != "" && samePath(root, o.Env.Home) {
			return nil, errors.New("that is your home folder: choose a project folder in it")
		}
		real, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, err
		}
		trusted := o.Trusted != nil && o.Trusted(root)
		e.root = root
		e.pr = &projectRun{o: ProjectOptions{Root: root, Home: o.Env.Home, Trusted: trusted, DryRun: true, Force: o.Force},
			rootReal: real, rep: &ProjectReport{Root: root}, pending: map[string]string{}}
		found = scanFolder(ctx, e.pr, src.FolderPatterns())
	}
	seen := map[string]int{}
	for _, f := range found {
		it := e.line(f)
		if n := seen[it.ID]; n > 0 {
			it.ID = fmt.Sprintf("%s#%d", it.ID, n+1)
		}
		seen[it.ID]++
		e.items = append(e.items, entry{item: it, found: f})
	}
	// A scan says what each would do; a run works it out again for each chosen item as it goes.
	if scan {
		for i := range e.items {
			e.plan(ctx, &e.items[i], false)
		}
	}
	return e, ctx.Err()
}

func (e *engine) result() *SourceScan {
	res := &SourceScan{Root: e.root, Found: len(e.items) > 0, Items: e.lines()}
	if e.pr != nil {
		res.Trusted = e.pr.o.Trusted
	}
	return res
}

func (e *engine) lines() []SourceItem {
	out := make([]SourceItem, 0, len(e.items))
	for _, it := range e.items {
		out = append(out, it.item)
	}
	return out
}

// line is the item as found, before anything is known about its target.
func (e *engine) line(f Found) SourceItem {
	it := SourceItem{Section: f.Kind.Section(), Kind: f.Kind, Name: f.Name, Source: f.Source, Status: f.Status, Note: f.Note}
	it.ID = string(f.Kind) + ":" + f.Source + "#" + f.Name
	return it
}

// docKey is the user-level document of an item; ok is false for kinds without one.
func (e *engine) docKey(f Found) (syncv1.DocumentKind, string, bool) {
	switch f.Kind {
	case KindInstructions:
		key := e.src.ID + "/" + f.Name
		if !strings.HasSuffix(strings.ToLower(key), ".md") {
			key += ".md"
		}
		return syncv1.DocumentKind_DOCUMENT_KIND_RULE, key, true
	case KindRule:
		return syncv1.DocumentKind_DOCUMENT_KIND_RULE, e.src.ID + "/" + f.Name + ".md", true
	case KindCommand:
		return syncv1.DocumentKind_DOCUMENT_KIND_COMMAND, f.Name, true
	case KindAgent:
		return syncv1.DocumentKind_DOCUMENT_KIND_AGENT, f.Name, true
	case KindSkill:
		return syncv1.DocumentKind_DOCUMENT_KIND_SKILL, f.Name, true
	}
	return 0, "", false
}

var docLabel = map[syncv1.DocumentKind]string{
	syncv1.DocumentKind_DOCUMENT_KIND_RULE:    "rule",
	syncv1.DocumentKind_DOCUMENT_KIND_COMMAND: "command",
	syncv1.DocumentKind_DOCUMENT_KIND_AGENT:   "agent",
	syncv1.DocumentKind_DOCUMENT_KIND_SKILL:   "skill",
}

// plan works out an item's target and status; with apply it also does it, except for user-level documents, which
// apply uploads together.
func (e *engine) plan(ctx context.Context, en *entry, apply bool) {
	f := en.found
	it := &en.item
	if it.Status != "" && it.Status != StatusPlanned {
		return // the reader already said why not
	}
	it.Status = StatusPlanned
	if f.Kind == KindSession {
		it.Status, it.Note = StatusNoted, joinNote(f.Note, "sessions from this tool are not imported yet")
		return
	}
	if e.pr != nil {
		e.pr.o.DryRun = !apply
		e.planFolder(en)
		return
	}
	switch f.Kind {
	case KindMCP:
		if f.MCP == nil {
			it.Status, it.Note = StatusError, joinNote(f.Note, "not a server entry this reader understands")
			return
		}
		st, note, secrets := mcpUser(ctx, e.o.MCP, e.mcp, f.Name, f.MCP, apply, e.o.IncludeSecrets, e.o.Force, e.o.Env.Home, e.o.ForceHint, e.o.SecretsHint)
		it.Status, it.Note, it.HasSecrets, it.Target = st, joinNote(f.Note, note), secrets, "this device"
		if e.o.MCP != nil {
			it.Target = "MCP server " + MCPName(f.Name) + " on this device"
		}
	case KindSettings:
		it.Status, it.Note = StatusNoted, joinNote(f.Note, "user-level settings are set by hand in Settings")
	default:
		kind, key, ok := e.docKey(f)
		if !ok {
			it.Status, it.Note = StatusSkipped, joinNote(f.Note, "nothing in harness takes this")
			return
		}
		it.Target = docLabel[kind] + " " + key
		if strings.TrimSpace(f.Text) == "" {
			it.Status, it.Note = StatusSkipped, joinNote(f.Note, "empty")
		}
	}
}

// markExisting asks the server about the planned documents of a scan.
func (e *engine) markExisting(ctx context.Context) bool {
	var items []Item
	var idx []int
	for i, en := range e.items {
		if en.item.Status != StatusPlanned {
			continue
		}
		if kind, key, ok := e.docKey(en.found); ok && en.found.Kind != KindMCP {
			r := &run{label: "import:" + e.src.ID, rep: &Report{}}
			r.doc(kind, docLabel[kind], key, "", nil, "")
			items = append(items, r.rep.Items[0])
			idx = append(idx, i)
		}
	}
	ok := markExisting(ctx, items, e.o.Exists)
	for j, i := range idx {
		if items[j].Status == StatusExists {
			e.items[i].item.Status = StatusExists
			e.items[i].item.Note = joinNote(e.items[i].item.Note, "already on the server")
		}
	}
	return ok
}

func (e *engine) progress(p SourceProgress) {
	if e.o.OnProgress != nil {
		e.o.OnProgress(p)
	}
}

// apply runs the chosen items, in order. User-level documents go up in batches at the end, with the Claude Code
// import's uploader.
func (e *engine) apply(ctx context.Context, todo []int) error {
	total := len(todo)
	done := 0
	finish := func(i int) {
		done++
		it := e.items[i].item
		e.progress(SourceProgress{Current: string(it.Kind) + " " + it.Name, Done: done, Total: total, Item: &it})
	}
	r := &run{label: "import:" + e.src.ID, rep: &Report{}, o: Options{Force: e.o.Force, Putter: e.o.Putter, ForceHint: e.o.ForceHint}}
	var docs []int
	for _, i := range todo {
		if err := ctx.Err(); err != nil {
			return err
		}
		en := &e.items[i]
		e.progress(SourceProgress{Current: string(en.item.Kind) + " " + en.item.Name, Done: done, Total: total})
		e.plan(ctx, en, true)
		if kind, key, ok := e.docKey(en.found); ok && e.pr == nil && en.item.Status == StatusPlanned {
			r.doc(kind, docLabel[kind], key, en.found.Text, docFiles(en.found.Files), en.item.Note)
			docs = append(docs, i)
			continue
		}
		finish(i)
	}
	if len(docs) == 0 {
		return nil
	}
	if e.o.Putter == nil {
		for _, i := range docs {
			e.items[i].item.Status, e.items[i].item.Note = StatusError, joinNote(e.items[i].item.Note, "no server to upload to")
			finish(i)
		}
		return errors.New("importing documents needs a harness server; this client is not connected to one")
	}
	err := r.upload(ctx)
	for j, i := range docs {
		got := r.rep.Items[j]
		if got.Status == StatusPlanned {
			got.Status, got.Note = StatusSkipped, joinNote(got.Note, "not reached: the run stopped before it")
		}
		e.items[i].item.Status, e.items[i].item.Note = got.Status, got.Note
		finish(i)
	}
	return err
}

func docFiles(files []File) []*syncv1.DocumentFile {
	var out []*syncv1.DocumentFile
	for _, f := range files {
		out = append(out, &syncv1.DocumentFile{Path: f.Path, Content: f.Data, Executable: f.Executable})
	}
	return out
}

// --- folder level ---

var safeSegment = regexp.MustCompile(`^[A-Za-z0-9_@+][A-Za-z0-9 _.@+-]*$`)

// safeName checks an item name used as a path below .harness: slash-separated segments, none of them "." or "..".
func safeName(name string) (string, bool) {
	name = strings.ReplaceAll(name, ":", "/")
	if name == "" || strings.HasPrefix(name, "/") {
		return "", false
	}
	for _, seg := range strings.Split(name, "/") {
		if !safeSegment.MatchString(seg) || seg == "." || seg == ".." {
			return "", false
		}
	}
	return path.Clean(name), true
}

func (e *engine) planFolder(en *entry) {
	f := en.found
	it := &en.item
	r := e.pr
	dir := filepath.Join(r.o.Root, filepath.FromSlash(f.Dir))
	harness := filepath.Join(dir, ".harness")
	set := func(st Status, target, note string) {
		it.Status, it.Target, it.Note = st, target, joinNote(f.Note, note)
	}
	switch f.Kind {
	case KindInstructions:
		dst := filepath.Join(dir, "AGENTS.md")
		st, note := e.folderBlock(f, dst)
		set(st, r.rel(dst), note)
	case KindMCP:
		dst := filepath.Join(r.o.Root, ".mcp.json")
		st, note, secrets := e.folderMCP(f, dst)
		set(st, r.rel(dst), note)
		it.HasSecrets = secrets
		it.NeedsTrust = st == StatusPlanned || st == StatusCopied
	case KindSettings:
		dst := filepath.Join(harness, "settings.json")
		st, note, trust := e.folderSettings(f, dst)
		set(st, r.rel(dst), note)
		it.NeedsTrust = trust
	case KindRule, KindCommand, KindAgent, KindSkill:
		name, ok := safeName(f.Name)
		if !ok {
			set(StatusError, "", "the name "+f.Name+" can't be a file name")
			return
		}
		if strings.TrimSpace(f.Text) == "" {
			set(StatusSkipped, "", "empty")
			return
		}
		sub := map[Kind]string{KindRule: "rules", KindCommand: "commands", KindAgent: "agents", KindSkill: "skills"}[f.Kind]
		var files []plannedFile
		dst := filepath.Join(harness, sub, filepath.FromSlash(name)+".md")
		if f.Kind == KindSkill {
			dst = filepath.Join(harness, sub, filepath.FromSlash(name))
			files = append(files, plannedFile{dst: filepath.Join(dst, "SKILL.md"), data: []byte(f.Text), mode: 0o644})
			for _, x := range f.Files {
				p, ok := safeName(x.Path)
				if !ok {
					continue
				}
				mode := os.FileMode(0o644)
				if x.Executable {
					mode = 0o755
				}
				files = append(files, plannedFile{dst: filepath.Join(dst, filepath.FromSlash(p)), data: x.Data, mode: mode})
			}
		} else {
			files = append(files, plannedFile{dst: dst, data: []byte(f.Text), mode: 0o644})
		}
		st, more, err := r.place(files)
		switch {
		case err != nil:
			set(StatusError, r.rel(dst), err.Error())
		case st == StatusExists:
			set(st, r.rel(dst), "already there")
		case st == StatusSkipped:
			set(st, r.rel(dst), r.rel(dst)+" differs and was left as it is; use "+e.o.ForceHint+" to replace it")
		default:
			set(st, r.rel(dst), joinNotes(more))
		}
	default:
		set(StatusSkipped, "", "nothing in harness takes this")
	}
}

// folderBlock adds instructions to an AGENTS.md as one block marked with its source.
func (e *engine) folderBlock(f Found, dst string) (Status, string) {
	r := e.pr
	content := strings.TrimSpace(f.Text)
	if strings.TrimSpace(memory.StripComments(content)) == "" {
		return StatusSkipped, "empty"
	}
	if st, err := os.Lstat(dst); err == nil && st.Mode()&fs.ModeSymlink != 0 {
		return StatusError, r.rel(dst) + " is a symbolic link; replace it with a file first"
	}
	if err := r.checkTarget(filepath.Dir(dst)); err != nil {
		return StatusError, err.Error()
	}
	cur, _ := r.readTarget(dst)
	next, status, note := mergeBlock(cur, f.Source, content, r.o.Force)
	if status != StatusPlanned {
		return status, note
	}
	if err := r.write(dst, []byte(next), 0o644); err != nil {
		return StatusError, err.Error()
	}
	return r.doneStatus(), note
}

// folderMCP merges a server into the folder's .mcp.json.
func (e *engine) folderMCP(f Found, dst string) (Status, string, bool) {
	r := e.pr
	if f.MCP == nil {
		return StatusError, "not a server entry this reader understands", false
	}
	name := MCPName(f.Name)
	entry, left, secrets, err := mcpProjectEntry(f.MCP, e.o.IncludeSecrets)
	if err != nil {
		return StatusNoted, err.Error(), false
	}
	var notes []string
	if name != f.Name {
		notes = append(notes, "named "+name+" here")
	}
	if len(left) > 0 {
		notes = append(notes, "secret values left empty: "+strings.Join(left, ", ")+" (use "+e.o.SecretsHint+")")
	}
	if err := r.checkTarget(dst); err != nil {
		return StatusError, err.Error(), secrets
	}
	cur := map[string]any{}
	if text, ok := r.readTarget(dst); ok {
		if cur, err = decodeObject([]byte(text)); err != nil {
			return StatusError, r.rel(dst) + " is not valid JSON and was left as it is", secrets
		}
	}
	servers, _ := cur["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	if old, ok := servers[name]; ok {
		if canonical(old) == canonical(entry) {
			return StatusExists, joinNotes(append(notes, "already there")), secrets
		}
		if !e.o.Force {
			return StatusExists, joinNotes(append(notes, "a server of that name is already there; use "+e.o.ForceHint+" to replace it")), secrets
		}
		notes = append(notes, "replaces the one already there")
	}
	servers[name] = entry
	cur["mcpServers"] = servers
	out, err := marshalSettings(cur)
	if err == nil {
		err = r.write(dst, out, 0o644)
	}
	if err != nil {
		return StatusError, err.Error(), secrets
	}
	notes = append(notes, "used once the folder is trusted and the server approved on this device")
	return r.doneStatus(), joinNotes(notes), secrets
}

// folderSettings merges settings into .harness/settings.json.
func (e *engine) folderSettings(f Found, dst string) (Status, string, bool) {
	r := e.pr
	if len(f.Settings) == 0 {
		return StatusSkipped, "nothing that maps to harness settings", false
	}
	in := map[string]any{}
	for k, v := range f.Settings {
		in[k] = v
	}
	var notes []string
	trust := false
	if d := describeSettings(in); d != "" {
		notes, trust = append(notes, d), true
	}
	if err := r.checkTarget(dst); err != nil {
		return StatusError, err.Error(), trust
	}
	cur := map[string]any{}
	if text, ok := r.readTarget(dst); ok {
		var err error
		if cur, err = decodeObject([]byte(text)); err != nil {
			return StatusError, r.rel(dst) + " is not valid JSON and was left as it is", trust
		}
	}
	merged := mergeObjects(cur, in, r.o.Force)
	if canonical(merged) == canonical(cur) {
		return StatusExists, joinNotes(append(notes, "already there")), trust
	}
	out, err := marshalSettings(merged)
	if err == nil {
		err = r.write(dst, out, 0o644)
	}
	if err != nil {
		return StatusError, err.Error(), trust
	}
	return r.doneStatus(), joinNotes(notes), trust
}

// scanFolder walks the folder once and hands every file a pattern matches to its reader. A file two patterns match is
// read by the first.
func scanFolder(ctx context.Context, r *projectRun, patterns []FolderPattern) []Found {
	var out []Found
	seen := map[string]bool{}
	walkProjectDirs(ctx, r.o.Root, r.o.Home, func(dir string) {
		names := map[string]bool{}
		if list, err := os.ReadDir(dir); err == nil {
			for _, d := range list {
				names[d.Name()] = true
			}
		}
		relDir := r.rel(dir)
		if relDir == "." {
			relDir = ""
		}
		for _, pat := range patterns {
			first, _, _ := strings.Cut(pat.Glob, "/")
			if !strings.ContainsAny(first, `*?[\`) && !names[first] {
				continue
			}
			matches, _ := filepath.Glob(filepath.Join(dir, filepath.FromSlash(pat.Glob)))
			sort.Strings(matches)
			for _, p := range matches {
				rel := r.rel(p)
				if seen[rel] {
					continue
				}
				st, err := os.Stat(p)
				if err != nil || st.IsDir() {
					continue
				}
				seen[rel] = true
				data, _, err := r.readSource(p, memory.MaxFileBytes)
				if err != nil {
					out = append(out, Found{Kind: KindInstructions, Name: path.Base(rel), Source: rel, Dir: relDir, Status: StatusSkipped, Note: err.Error()})
					continue
				}
				ff := FolderFile{Rel: rel, Dir: relDir, Data: data, Tree: func(sub string) ([]File, []string) { return r.tree(sub) }}
				for _, f := range pat.Read(ff) {
					if f.Source == "" {
						f.Source = rel
					}
					if f.Dir == "" {
						f.Dir = relDir
					}
					out = append(out, f)
				}
			}
		}
	})
	return out
}

// tree reads a folder of the project for a skill, under the project import's rules.
func (r *projectRun) tree(rel string) ([]File, []string) {
	src := filepath.Join(r.o.Root, filepath.FromSlash(rel))
	files, skipped, err := r.plan(src, src, false)
	if err != nil {
		return nil, []string{err.Error()}
	}
	var out []File
	for _, f := range files {
		p, _ := filepath.Rel(src, f.dst)
		out = append(out, File{Path: filepath.ToSlash(p), Data: f.data, Executable: f.mode&0o111 != 0})
	}
	return out, skipped
}
