package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// contents returns "relpath=content" for each instruction (paths relative to root).
func contents(root string, in []Instruction) []string {
	var out []string
	for _, i := range in {
		p := i.Path
		if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
			p = filepath.ToSlash(r)
		}
		out = append(out, p+"="+i.Content)
	}
	return out
}

type fakeDocs struct {
	synced bool
	docs   map[string]*syncv1.Document
}

func (f *fakeDocs) Synced() bool { return f.synced }
func (f *fakeDocs) Lookup(scope commonv1.Scope, kind syncv1.DocumentKind, key, projectKey string) (*syncv1.Document, bool) {
	d, ok := f.docs[fmt.Sprint(scope, kind, key, projectKey)]
	return d, ok
}

func (f *fakeDocs) List(kind syncv1.DocumentKind) []*syncv1.Document {
	var out []*syncv1.Document
	for _, d := range f.docs {
		if d.GetKind() == kind {
			out = append(out, d)
		}
	}
	return out
}

func memDoc(scope commonv1.Scope, projectKey, id, content string) (string, *syncv1.Document) {
	return fmt.Sprint(scope, syncv1.DocumentKind_DOCUMENT_KIND_MEMORY, "user", projectKey),
		&syncv1.Document{Id: id, Scope: scope, Kind: syncv1.DocumentKind_DOCUMENT_KIND_MEMORY, Key: "user", Content: content, ProjectKey: projectKey}
}

func TestStartupHierarchy(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		mode  Mode
		want  []string
	}{
		{
			name: "root to cwd, local after main, .harness/AGENTS.md",
			files: map[string]string{
				"repo/AGENTS.md":                    "root",
				"repo/.harness/AGENTS.local.md":     "root local",
				"repo/a/.harness/AGENTS.md":         "a dotharness",
				"repo/a/b/AGENTS.md":                "b",
				"repo/a/b/c/AGENTS.md":              "below cwd (lazy)",
				"repo/a/b/.harness/AGENTS.local.md": "b local",
			},
			want: []string{"repo/AGENTS.md=root", "repo/.harness/AGENTS.local.md=root local", "repo/a/.harness/AGENTS.md=a dotharness", "repo/a/b/AGENTS.md=b", "repo/a/b/.harness/AGENTS.local.md=b local"},
		},
		{
			name:  "the values of the setting that chose between files read the same",
			mode:  "claude-md",
			files: map[string]string{"repo/AGENTS.md": "agents"},
			want:  []string{"repo/AGENTS.md=agents"},
		},
		{
			name:  "managed-only",
			mode:  ModeManagedOnly,
			files: map[string]string{"repo/AGENTS.md": "claude", "managed/AGENTS.md": "managed"},
			want:  []string{"managed/AGENTS.md=managed"},
		},
		{
			name:  "html comments stripped outside code only",
			files: map[string]string{"repo/AGENTS.md": "keep <!-- drop -->this\n<!--\nmulti\n-->\n`<!-- span -->`\n```\n<!-- fenced -->\n```"},
			want:  []string{"repo/AGENTS.md=keep this\n\n`<!-- span -->`\n```\n<!-- fenced -->\n```"},
		},
		{
			name:  "oversized file skipped",
			files: map[string]string{"repo/AGENTS.md": strings.Repeat("x", MaxFileBytes+1), "repo/a/AGENTS.md": "small"},
			want:  []string{"repo/a/AGENTS.md=small"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, tc.files)
			cwd := filepath.Join(root, "repo", "a", "b")
			os.MkdirAll(cwd, 0o755)
			managed := []string{filepath.Join(root, "managed", "AGENTS.md")}
			l := New(Options{Cwd: cwd, Mode: tc.mode, Home: filepath.Join(root, "home"), ManagedFiles: managed})
			got := contents(root, l.Initial())
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestManagedUserAndAutoMemoryOrder(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	write(t, root, map[string]string{
		"managed/AGENTS.md":              "managed",
		"home/AGENTS.md":                 "home project file",
		"home/repo/AGENTS.md":            "project",
		"harness/memory/k/MEMORY.md":     "local auto memory",
		"home/repo/.harness/rules/x.md":  "always rule",
		"home/repo/.harness/rules/y.md":  "---\npaths: [\"src/**/*.go\"]\n---\ngo rule",
		"home/repo/.harness/settings.md": "not a rule",
	})
	cwd := filepath.Join(home, "repo")
	base := Options{Cwd: cwd, Home: home, ManagedFiles: []string{filepath.Join(root, "managed", "AGENTS.md")}, AutoMemoryDir: filepath.Join(root, "harness", "memory", "k"), ProjectKey: "k"}

	// Before the first sync: no user memory.
	got := contents(root, New(base).Initial())
	want := []string{"managed/AGENTS.md=managed",
		"home/AGENTS.md=home project file", "home/repo/AGENTS.md=project", "home/repo/.harness/rules/x.md=always rule",
		"harness/memory/k/MEMORY.md=local auto memory"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unsynced:\n got  %q\n want %q", got, want)
	}

	// After sync: user memory and auto memory come from documents.
	docs := &fakeDocs{synced: true, docs: map[string]*syncv1.Document{}}
	k, d := memDoc(commonv1.Scope_SCOPE_USER, "", "doc_u", "synced user <!-- x -->")
	docs.docs[k] = d
	idx := strings.Repeat("line\n", AutoMemoryMaxLines+50)
	k, d = memDoc(commonv1.Scope_SCOPE_PROJECT, "k", "doc_p", idx)
	docs.docs[k] = d
	o := base
	o.Docs = docs
	l := New(o)
	in := l.Initial()
	got = contents(root, in)
	if got[1] != "sync:doc_u=synced user" {
		t.Fatalf("synced user memory: %q", got)
	}
	last := in[len(in)-1]
	if last.Origin != OriginAutoMemory || strings.Count(last.Content, "\n")+1 != AutoMemoryMaxLines {
		t.Fatalf("auto memory truncation: origin %s lines %d", last.Origin, strings.Count(last.Content, "\n")+1)
	}
	out := Render(in)
	if !strings.Contains(out, "Contents of sync:doc_u (user's private global instructions") {
		t.Fatalf("render: %s", out)
	}
	if !strings.HasSuffix(ProjectContext(in[:1], "Current branch: main"), "gitStatus:\nCurrent branch: main") {
		t.Fatal("project context")
	}
}

func TestTruncateIndexBytes(t *testing.T) {
	s := strings.Repeat(strings.Repeat("y", 199)+"\n", 150) // 30000 bytes, 150 lines
	got := TruncateIndex(s)
	if len(got) > AutoMemoryMaxBytes || !strings.HasSuffix(got, "\n") {
		t.Fatalf("len %d", len(got))
	}
}

func TestImports(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		want    []string
		pending []string
	}{
		{
			name: "relative to importing file, nested, after importer",
			files: map[string]string{
				"repo/AGENTS.md":      "see @docs/a.md for more",
				"repo/docs/a.md":      "A imports @sub/b.md",
				"repo/docs/sub/b.md":  "B",
				"repo/docs/unused.md": "unused",
			},
			want: []string{"repo/AGENTS.md=see @docs/a.md for more", "repo/docs/a.md=A imports @sub/b.md", "repo/docs/sub/b.md=B"},
		},
		{
			name: "max depth 4",
			files: map[string]string{
				"repo/AGENTS.md": "@1.md", "repo/1.md": "one @2.md", "repo/2.md": "two @3.md",
				"repo/3.md": "three @4.md", "repo/4.md": "four @5.md", "repo/5.md": "five",
			},
			want: []string{"repo/AGENTS.md=@1.md", "repo/1.md=one @2.md", "repo/2.md=two @3.md", "repo/3.md=three @4.md", "repo/4.md=four @5.md"},
		},
		{
			name: "code spans, fences, quotes, emails are not imports",
			files: map[string]string{
				"repo/AGENTS.md": "`@a.md` and \"@b.md\" and @\"c.md\" and me@d.md\n```\n@e.md\n```\n~~~\n@f.md\n~~~\nreal @g.md.",
				"repo/a.md":      "A", "repo/b.md": "B", "repo/c.md": "C", "repo/d.md": "D", "repo/e.md": "E", "repo/f.md": "F", "repo/g.md": "G",
			},
			want: []string{"repo/AGENTS.md=`@a.md` and \"@b.md\" and @\"c.md\" and me@d.md\n```\n@e.md\n```\n~~~\n@f.md\n~~~\nreal @g.md.", "repo/g.md=G"},
		},
		{
			name:  "escaped spaces and cycles",
			files: map[string]string{"repo/AGENTS.md": `@my\ notes.md`, "repo/my notes.md": "notes @AGENTS.md"},
			want:  []string{`repo/AGENTS.md=@my\ notes.md`, "repo/my notes.md=notes @AGENTS.md"},
		},
		{
			name:    "external import waits for approval",
			files:   map[string]string{"repo/AGENTS.md": "@../outside.md and @~/x.md", "outside.md": "outside", "home/x.md": "home x"},
			want:    []string{"repo/AGENTS.md=@../outside.md and @~/x.md"},
			pending: []string{"home/x.md", "outside.md"},
		},
		{
			name:  "missing import ignored",
			files: map[string]string{"repo/AGENTS.md": "@nope.md @README"},
			want:  []string{"repo/AGENTS.md=@nope.md @README"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, tc.files)
			l := New(Options{Cwd: filepath.Join(root, "repo"), Home: filepath.Join(root, "home"), ManagedFiles: []string{}})
			got := contents(root, l.Initial())
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
			var pend []string
			for _, p := range l.PendingImports() {
				r, _ := filepath.Rel(root, p.Path)
				pend = append(pend, filepath.ToSlash(r))
			}
			if !reflect.DeepEqual(pend, tc.pending) {
				t.Fatalf("pending %q want %q", pend, tc.pending)
			}
		})
	}
}

func TestImportApproval(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"repo/AGENTS.md": "@../shared.md @../denied.md", "shared.md": "shared", "denied.md": "denied"})
	ap := &FileApprovals{Path: filepath.Join(root, "approvals.json"), Project: "p"}
	l := New(Options{Cwd: filepath.Join(root, "repo"), Home: filepath.Join(root, "home"), ManagedFiles: []string{}, Approvals: ap})
	if n := len(l.PendingImports()); n != 2 {
		t.Fatalf("pending %d", n)
	}
	got, err := l.Resolve(filepath.Join(root, "shared.md"), true)
	if err != nil || len(got) != 1 || got[0].Content != "shared" || got[0].Trigger != TriggerInclude {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	if got, _ := l.Resolve(filepath.Join(root, "denied.md"), false); got != nil {
		t.Fatalf("denied returned %+v", got)
	}
	// A new loader remembers both decisions.
	l2 := New(Options{Cwd: filepath.Join(root, "repo"), Home: filepath.Join(root, "home"), ManagedFiles: []string{}, Approvals: ap})
	c := contents(root, l2.Initial())
	if len(l2.PendingImports()) != 0 || len(c) != 2 || c[1] != "shared.md=shared" {
		t.Fatalf("persisted: %q pending %v", c, l2.PendingImports())
	}
	// Imports inside the project root need no approval.
	write(t, root, map[string]string{"repo/sub/AGENTS.md": "@../docs.md", "repo/docs.md": "docs"})
	l3 := New(Options{Cwd: filepath.Join(root, "repo", "sub"), ProjectRoot: filepath.Join(root, "repo"), Home: filepath.Join(root, "home"), ManagedFiles: []string{}, Approvals: ap})
	if c := contents(root, l3.Initial()); c[len(c)-1] != "repo/docs.md=docs" {
		t.Fatalf("project root import: %q", c)
	}
}

func TestLazySubdirsAndRules(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"repo/AGENTS.md":                         "root",
		"repo/pkg/AGENTS.md":                     "pkg",
		"repo/pkg/deep/.harness/AGENTS.local.md": "deep local",
		"repo/pkg/deep/file.go":                  "package deep",
		"repo/pkg/deep/.harness/rules/r.md":      "deep rule",
		"repo/other/CLAUDE.md":                   "never read",
		"repo/.harness/rules/api.md":             "---\ndescription: x\npaths:\n  - \"src/api/**/*.ts\"\n  - '*.sql'\n---\napi rule",
		"repo/.harness/rules/nested/tests.md":    "---\npaths: \"**/*_test.go\"\n---\ntest rule",
	})
	cwd := filepath.Join(root, "repo")
	l := New(Options{Cwd: cwd, Home: filepath.Join(root, "home"), ManagedFiles: []string{}})
	if got := contents(root, l.Initial()); !reflect.DeepEqual(got, []string{"repo/AGENTS.md=root"}) {
		t.Fatalf("initial %q", got)
	}
	got := contents(root, l.ForPath(filepath.Join(cwd, "pkg", "deep", "file.go")))
	want := []string{"repo/pkg/AGENTS.md=pkg", "repo/pkg/deep/.harness/AGENTS.local.md=deep local", "repo/pkg/deep/.harness/rules/r.md=deep rule"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lazy: got %q want %q", got, want)
	}
	if again := l.ForPath(filepath.Join(cwd, "pkg", "deep", "file.go")); len(again) != 0 {
		t.Fatalf("second ForPath must return nothing: %+v", again)
	}
	if got := l.ForPath(filepath.Join(cwd, "other", "x.txt")); len(got) != 0 {
		t.Fatalf("CLAUDE.md read: %+v", got)
	}
	in := l.ForPath(filepath.Join(cwd, "src", "api", "v1", "users.ts"))
	if len(in) != 1 || in[0].Content != "api rule" || in[0].Trigger != TriggerPath || !reflect.DeepEqual(in[0].Globs, []string{"src/api/**/*.ts", "*.sql"}) {
		t.Fatalf("rule: %+v", in)
	}
	if got := contents(root, l.ForPath(filepath.Join(cwd, "pkg", "x_test.go"))); !reflect.DeepEqual(got, []string{"repo/.harness/rules/nested/tests.md=test rule"}) {
		t.Fatalf("nested rule: %q", got)
	}
	if got := l.ForPath(filepath.Join(root, "elsewhere", "a.ts")); len(got) != 0 {
		t.Fatalf("outside: %+v", got)
	}
}

func TestLazyAgents(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"repo/AGENTS.md": "root agents", "repo/sub/AGENTS.md": "sub agents"})
	l := New(Options{Cwd: filepath.Join(root, "repo"), Home: filepath.Join(root, "home"), ManagedFiles: []string{}})
	if got := contents(root, l.ForPath(filepath.Join(root, "repo", "sub", "f.txt"))); !reflect.DeepEqual(got, []string{"repo/sub/AGENTS.md=sub agents"}) {
		t.Fatalf("got %q", got)
	}
}

func TestFrontmatterList(t *testing.T) {
	cases := map[string][]string{
		"paths: a/**":         {"a/**"},
		"paths: \"a\", 'b'":   {"a", "b"},
		"paths: [x, \"y,z\"]": {"x", "y,z"},
		"paths:\n  - one\n  # c\n  - \"two\"\nname: q": {"one", "two"},
		"name: q": nil,
	}
	for fm, want := range cases {
		if got := frontmatterList(fm, "paths"); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %q want %q", fm, got, want)
		}
	}
	if FrontmatterString("---\nname: \"x\"\n---\nbody", "name") != "x" {
		t.Error("FrontmatterString")
	}
}

func TestNoAncestors(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"AGENTS.md":              "root instructions",
		"chats/s1/AGENTS.md":     "own instructions",
		"chats/s1/sub/AGENTS.md": "nested instructions",
	})
	cwd := filepath.Join(root, "chats", "s1")
	l := New(Options{Cwd: cwd, Home: filepath.Join(root, "home"), ManagedFiles: []string{}, NoAncestors: true})
	var got []string
	for _, in := range l.Initial() {
		got = append(got, in.Content)
	}
	if strings.Join(got, "|") != "own instructions" {
		t.Fatalf("initial %q", got)
	}
	nested := l.ForPath(filepath.Join(cwd, "sub", "x.go"))
	if len(nested) != 1 || nested[0].Content != "nested instructions" {
		t.Fatalf("nested %+v", nested)
	}
}

// Nothing of Claude Code's is read: not its names, not its folder, not through an @import, not through a link.
func TestClaudeFilesAreNotRead(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"home/.claude/CLAUDE.md":      "user claude",
		"home/.claude/rules/style.md": "user rule",
		"home/.claude/note.md":        "user note",
		"repo/CLAUDE.md":              "project claude",
		"repo/CLAUDE.local.md":        "local claude",
		"repo/.claude/CLAUDE.md":      "dot claude",
		"repo/.claude/AGENTS.md":      "dot agents",
		"repo/.claude/rules/r.md":     "rule",
		"repo/.claude/extra.md":       "extra",
		"repo/sub/CLAUDE.md":          "nested claude",
		"repo/sub/.claude/rules/r.md": "nested rule",
		"repo/AGENTS.md":              "live @CLAUDE.md @CLAUDE.local.md @.claude/extra.md @~/.claude/note.md @docs.md",
		"repo/docs.md":                "docs",
	})
	cwd, home := filepath.Join(root, "repo"), filepath.Join(root, "home")
	l := New(Options{Cwd: cwd, Home: home, ManagedFiles: []string{}})
	got := contents(root, l.Initial())
	want := []string{"repo/AGENTS.md=live @CLAUDE.md @CLAUDE.local.md @.claude/extra.md @~/.claude/note.md @docs.md", "repo/docs.md=docs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if p := l.PendingImports(); len(p) != 0 {
		t.Fatalf("offered for approval: %+v", p)
	}
	if got := l.ForPath(filepath.Join(cwd, "sub", "x.go")); len(got) != 0 {
		t.Fatalf("nested: %+v", got)
	}
}

func TestLinkToCLAUDEFileIsNotRead(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"repo/CLAUDE.md": "claude"})
	if err := os.Symlink("CLAUDE.md", filepath.Join(root, "repo", "AGENTS.md")); err != nil {
		t.Skip(err)
	}
	if got := New(Options{Cwd: filepath.Join(root, "repo"), Home: filepath.Join(root, "home"), ManagedFiles: []string{}}).Initial(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// A checkout may sit below some .claude (worktrees do): the project still loads, the folder's own files do not.
func TestProjectBelowAClaudeFolder(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"main/AGENTS.md":                     "above",
		"main/.claude/AGENTS.md":             "not this",
		"main/.claude/worktrees/AGENTS.md":   "nor this",
		"main/.claude/worktrees/w/AGENTS.md": "worktree @docs.md",
		"main/.claude/worktrees/w/docs.md":   "docs",
	})
	cwd := filepath.Join(root, "main", ".claude", "worktrees", "w")
	got := contents(root, New(Options{Cwd: cwd, ProjectRoot: cwd, Home: filepath.Join(root, "home"), ManagedFiles: []string{}}).Initial())
	want := []string{"main/AGENTS.md=above", "main/.claude/worktrees/w/AGENTS.md=worktree @docs.md", "main/.claude/worktrees/w/docs.md=docs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

// The data directory is the user's, even when the walk from the root passes through the home directory.
func TestHarnessHomeIsNotAProject(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"home/.harness/AGENTS.md":  "not a project file",
		"home/.harness/rules/r.md": "not a project rule",
		"home/repo/AGENTS.md":      "project",
	})
	got := contents(root, New(Options{Cwd: filepath.Join(root, "home", "repo"), Home: filepath.Join(root, "home"), ManagedFiles: []string{}}).Initial())
	if !reflect.DeepEqual(got, []string{"home/repo/AGENTS.md=project"}) {
		t.Fatalf("got %q", got)
	}
}

func TestSyncedUserRules(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"repo/x.go": "package x"})
	docs := &fakeDocs{synced: true, docs: map[string]*syncv1.Document{
		"a": {Id: "doc_a", Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_RULE, Key: "a.md", Content: "always"},
		"b": {Id: "doc_b", Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_RULE, Key: "b.md", Content: "---\npaths: \"**/*.go\"\n---\ngo only"},
		"o": {Id: "doc_o", Scope: commonv1.Scope_SCOPE_ORG, Kind: syncv1.DocumentKind_DOCUMENT_KIND_RULE, Key: "o.md", Content: "org"},
	}}
	cwd := filepath.Join(root, "repo")
	l := New(Options{Cwd: cwd, Home: filepath.Join(root, "home"), ManagedFiles: []string{}, Docs: docs})
	if got := contents(root, l.Initial()); !reflect.DeepEqual(got, []string{"sync:doc_a=always"}) {
		t.Fatalf("got %q", got)
	}
	if got := contents(root, l.ForPath(filepath.Join(cwd, "x.go"))); !reflect.DeepEqual(got, []string{"sync:doc_b=go only"}) {
		t.Fatalf("got %q", got)
	}
}

func TestRewriteImports(t *testing.T) {
	got := RewriteImports("a @x.md `@y.md` me@z.md\n```\n@w.md\n```\n@v\\ w.md", func(raw string) string { return "<" + raw + ">" })
	if want := "a <x.md> `@y.md` me@z.md\n```\n@w.md\n```\n<v w.md>"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
