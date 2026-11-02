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
			name: "root to cwd, local after main, .claude/CLAUDE.md",
			files: map[string]string{
				"repo/CLAUDE.md":             "root",
				"repo/CLAUDE.local.md":       "root local",
				"repo/a/.claude/CLAUDE.md":   "a dotclaude",
				"repo/a/b/CLAUDE.md":         "b",
				"repo/a/b/AGENTS.md":         "ignored agents",
				"repo/a/b/c/CLAUDE.md":       "below cwd (lazy)",
				"repo/a/b/CLAUDE.local.md":   "b local",
				"repo/a/b/.claude/AGENTS.md": "ignored",
			},
			want: []string{"repo/CLAUDE.md=root", "repo/CLAUDE.local.md=root local", "repo/a/.claude/CLAUDE.md=a dotclaude", "repo/a/b/CLAUDE.md=b", "repo/a/b/CLAUDE.local.md=b local"},
		},
		{
			name:  "AGENTS.md fallback when no CLAUDE file",
			files: map[string]string{"repo/AGENTS.md": "agents root", "repo/a/b/.claude/AGENTS.md": "agents b"},
			want:  []string{"repo/AGENTS.md=agents root", "repo/a/b/.claude/AGENTS.md=agents b"},
		},
		{
			name:  "CLAUDE.local.md alone disables AGENTS.md",
			files: map[string]string{"repo/AGENTS.md": "agents", "repo/a/CLAUDE.local.md": "local"},
			want:  []string{"repo/a/CLAUDE.local.md=local"},
		},
		{
			name:  "claude-md-and-agents-md reads both",
			mode:  ModeClaudeAndAgents,
			files: map[string]string{"repo/AGENTS.md": "agents", "repo/CLAUDE.md": "claude"},
			want:  []string{"repo/CLAUDE.md=claude", "repo/AGENTS.md=agents"},
		},
		{
			name:  "claude-md never reads AGENTS.md",
			mode:  ModeClaudeOnly,
			files: map[string]string{"repo/AGENTS.md": "agents"},
			want:  nil,
		},
		{
			name:  "managed-only",
			mode:  ModeManagedOnly,
			files: map[string]string{"repo/CLAUDE.md": "claude", "managed/CLAUDE.md": "managed"},
			want:  []string{"managed/CLAUDE.md=managed"},
		},
		{
			name:  "html comments stripped outside code only",
			files: map[string]string{"repo/CLAUDE.md": "keep <!-- drop -->this\n<!--\nmulti\n-->\n`<!-- span -->`\n```\n<!-- fenced -->\n```"},
			want:  []string{"repo/CLAUDE.md=keep this\n\n`<!-- span -->`\n```\n<!-- fenced -->\n```"},
		},
		{
			name:  "oversized file skipped",
			files: map[string]string{"repo/CLAUDE.md": strings.Repeat("x", MaxFileBytes+1), "repo/a/CLAUDE.md": "small"},
			want:  []string{"repo/a/CLAUDE.md=small"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, tc.files)
			cwd := filepath.Join(root, "repo", "a", "b")
			os.MkdirAll(cwd, 0o755)
			managed := []string{filepath.Join(root, "managed", "CLAUDE.md")}
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
		"managed/CLAUDE.md":             "managed",
		"home/.claude/CLAUDE.md":        "local user",
		"home/.claude/rules/style.md":   "user rule",
		"home/CLAUDE.md":                "home project file",
		"home/repo/CLAUDE.md":           "project",
		"harness/memory/k/MEMORY.md":    "local auto memory",
		"home/repo/.claude/rules/x.md":  "always rule",
		"home/repo/.claude/rules/y.md":  "---\npaths: [\"src/**/*.go\"]\n---\ngo rule",
		"home/repo/.claude/settings.md": "not a rule",
	})
	cwd := filepath.Join(home, "repo")
	base := Options{Cwd: cwd, Home: home, ManagedFiles: []string{filepath.Join(root, "managed", "CLAUDE.md")}, AutoMemoryDir: filepath.Join(root, "harness", "memory", "k"), ProjectKey: "k"}

	// Before the first sync: local ~/.claude/CLAUDE.md and ~/.claude/rules.
	got := contents(root, New(base).Initial())
	want := []string{"managed/CLAUDE.md=managed", "home/.claude/CLAUDE.md=local user", "home/.claude/rules/style.md=user rule",
		"home/CLAUDE.md=home project file", "home/repo/CLAUDE.md=project", "home/repo/.claude/rules/x.md=always rule",
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
	for _, g := range got {
		if strings.Contains(g, "local user") || strings.Contains(g, "user rule") {
			t.Fatalf("local user files must not load after sync: %q", got)
		}
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
				"repo/CLAUDE.md":      "see @docs/a.md for more",
				"repo/docs/a.md":      "A imports @sub/b.md",
				"repo/docs/sub/b.md":  "B",
				"repo/docs/unused.md": "unused",
			},
			want: []string{"repo/CLAUDE.md=see @docs/a.md for more", "repo/docs/a.md=A imports @sub/b.md", "repo/docs/sub/b.md=B"},
		},
		{
			name: "max depth 4",
			files: map[string]string{
				"repo/CLAUDE.md": "@1.md", "repo/1.md": "one @2.md", "repo/2.md": "two @3.md",
				"repo/3.md": "three @4.md", "repo/4.md": "four @5.md", "repo/5.md": "five",
			},
			want: []string{"repo/CLAUDE.md=@1.md", "repo/1.md=one @2.md", "repo/2.md=two @3.md", "repo/3.md=three @4.md", "repo/4.md=four @5.md"},
		},
		{
			name: "code spans, fences, quotes, emails are not imports",
			files: map[string]string{
				"repo/CLAUDE.md": "`@a.md` and \"@b.md\" and @\"c.md\" and me@d.md\n```\n@e.md\n```\n~~~\n@f.md\n~~~\nreal @g.md.",
				"repo/a.md":      "A", "repo/b.md": "B", "repo/c.md": "C", "repo/d.md": "D", "repo/e.md": "E", "repo/f.md": "F", "repo/g.md": "G",
			},
			want: []string{"repo/CLAUDE.md=`@a.md` and \"@b.md\" and @\"c.md\" and me@d.md\n```\n@e.md\n```\n~~~\n@f.md\n~~~\nreal @g.md.", "repo/g.md=G"},
		},
		{
			name:  "escaped spaces and cycles",
			files: map[string]string{"repo/CLAUDE.md": `@my\ notes.md`, "repo/my notes.md": "notes @CLAUDE.md"},
			want:  []string{`repo/CLAUDE.md=@my\ notes.md`, "repo/my notes.md=notes @CLAUDE.md"},
		},
		{
			name:    "external import waits for approval",
			files:   map[string]string{"repo/CLAUDE.md": "@../outside.md and @~/x.md", "outside.md": "outside", "home/x.md": "home x"},
			want:    []string{"repo/CLAUDE.md=@../outside.md and @~/x.md"},
			pending: []string{"home/x.md", "outside.md"},
		},
		{
			name:  "missing import ignored",
			files: map[string]string{"repo/CLAUDE.md": "@nope.md @README"},
			want:  []string{"repo/CLAUDE.md=@nope.md @README"},
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
	write(t, root, map[string]string{"repo/CLAUDE.md": "@../shared.md @../denied.md", "shared.md": "shared", "denied.md": "denied"})
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
	write(t, root, map[string]string{"repo/sub/CLAUDE.md": "@../docs.md", "repo/docs.md": "docs"})
	l3 := New(Options{Cwd: filepath.Join(root, "repo", "sub"), ProjectRoot: filepath.Join(root, "repo"), Home: filepath.Join(root, "home"), ManagedFiles: []string{}, Approvals: ap})
	if c := contents(root, l3.Initial()); c[len(c)-1] != "repo/docs.md=docs" {
		t.Fatalf("project root import: %q", c)
	}
}

func TestLazySubdirsAndRules(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"repo/CLAUDE.md":                     "root",
		"repo/pkg/CLAUDE.md":                 "pkg",
		"repo/pkg/deep/CLAUDE.local.md":      "deep local",
		"repo/pkg/deep/file.go":              "package deep",
		"repo/pkg/deep/.claude/rules/r.md":   "deep rule",
		"repo/other/AGENTS.md":               "not read (CLAUDE.md exists)",
		"repo/.claude/rules/api.md":          "---\ndescription: x\npaths:\n  - \"src/api/**/*.ts\"\n  - '*.sql'\n---\napi rule",
		"repo/.claude/rules/nested/tests.md": "---\npaths: \"**/*_test.go\"\n---\ntest rule",
	})
	cwd := filepath.Join(root, "repo")
	l := New(Options{Cwd: cwd, Home: filepath.Join(root, "home"), ManagedFiles: []string{}})
	if got := contents(root, l.Initial()); !reflect.DeepEqual(got, []string{"repo/CLAUDE.md=root"}) {
		t.Fatalf("initial %q", got)
	}
	got := contents(root, l.ForPath(filepath.Join(cwd, "pkg", "deep", "file.go")))
	want := []string{"repo/pkg/CLAUDE.md=pkg", "repo/pkg/deep/CLAUDE.local.md=deep local", "repo/pkg/deep/.claude/rules/r.md=deep rule"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lazy: got %q want %q", got, want)
	}
	if again := l.ForPath(filepath.Join(cwd, "pkg", "deep", "file.go")); len(again) != 0 {
		t.Fatalf("second ForPath must return nothing: %+v", again)
	}
	if got := l.ForPath(filepath.Join(cwd, "other", "x.txt")); len(got) != 0 {
		t.Fatalf("AGENTS.md read despite CLAUDE.md: %+v", got)
	}
	in := l.ForPath(filepath.Join(cwd, "src", "api", "v1", "users.ts"))
	if len(in) != 1 || in[0].Content != "api rule" || in[0].Trigger != TriggerPath || !reflect.DeepEqual(in[0].Globs, []string{"src/api/**/*.ts", "*.sql"}) {
		t.Fatalf("rule: %+v", in)
	}
	if got := contents(root, l.ForPath(filepath.Join(cwd, "pkg", "x_test.go"))); !reflect.DeepEqual(got, []string{"repo/.claude/rules/nested/tests.md=test rule"}) {
		t.Fatalf("nested rule: %q", got)
	}
	if got := l.ForPath(filepath.Join(root, "elsewhere", "a.ts")); len(got) != 0 {
		t.Fatalf("outside: %+v", got)
	}
}

func TestLazyAgentsFallback(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"repo/AGENTS.md": "root agents", "repo/sub/AGENTS.md": "sub agents"})
	l := New(Options{Cwd: filepath.Join(root, "repo"), Home: filepath.Join(root, "home"), ManagedFiles: []string{}})
	if !l.AgentsFallback() {
		t.Fatal("fallback expected")
	}
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
