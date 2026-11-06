package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseReadsTheFormat(t *testing.T) {
	s := Parse(`---
name: deploy
description: >-
  Ships the core
  to production
when_to_use: after a release
argument-hint: "[env]"
arguments: [env, tag]
allowed-tools: Bash(git add *), Read Grep
model: fast
effort: HIGH
disable-model-invocation: true
user-invocable: false
context: fork
agent: Explore
paths: "src/**/*.go, {a,b}/*.ts"
hooks:
  PreToolUse: []
license: MIT
mystery: 1
---
Body`, "folder", KindSkill)
	if s.Name != "deploy" || s.Description != "Ships the core to production" || s.WhenToUse != "after a release" || s.ArgumentHint != "[env]" {
		t.Fatalf("%+v", s)
	}
	if strings.Join(s.Arguments, ",") != "env,tag" || strings.Join(s.AllowedTools, "|") != "Bash(git add *)|Read|Grep" {
		t.Fatalf("lists %+v", s)
	}
	if s.Model != "fast" || s.Effort != "high" || !s.DisableModelInvocation || s.UserInvocable || !s.Fork || s.Agent != "Explore" {
		t.Fatalf("fields %+v", s)
	}
	if strings.Join(s.Paths, "|") != "src/**/*.go|{a,b}/*.ts" {
		t.Fatalf("paths %q", s.Paths)
	}
	// What is read but not acted on is reported; what only describes the skill isn't.
	if strings.Join(s.Skipped, ",") != "mystery" || len(s.Hooks) == 0 {
		t.Fatalf("skipped %q", s.Skipped)
	}
}

func TestParseToleratesWhatYAMLRefuses(t *testing.T) {
	s := Parse("---\nname: x\ndescription: Use when: anything goes\nallowed-tools:\n  - Read\n  - Grep\n---\nBody", "folder", KindSkill)
	if s.Name != "x" || s.Description != "Use when: anything goes" || strings.Join(s.AllowedTools, ",") != "Read,Grep" {
		t.Fatalf("%+v", s)
	}
	if len(s.Problems) != 1 {
		t.Fatalf("problems %q", s.Problems)
	}
}

func TestParseDefaults(t *testing.T) {
	s := Parse("# Heading line\n\nSecond", "note", KindCommand)
	if s.Name != "note" || s.Description != "Heading line" || !s.UserInvocable || s.DisableModelInvocation {
		t.Fatalf("%+v", s)
	}
	// A command's name is its path: the folder is its namespace.
	if c := Parse("---\nname: other\n---\nb", "ns:cmd", KindCommand); c.Name != "ns:cmd" {
		t.Fatalf("command name %q", c.Name)
	}
	if b := Parse("---\nname: \"bad name!\"\n---\nb", "fallback", KindSkill); b.Name != "fallback" || len(b.Problems) != 1 {
		t.Fatalf("bad name %+v", b)
	}
	if i := Parse("---\nmodel: inherit\n---\nb", "x", KindSkill); i.Model != "" {
		t.Fatalf("inherit %q", i.Model)
	}
}

func TestDiscoverPrecedenceAndNamespaces(t *testing.T) {
	hh, repo := t.TempDir(), t.TempDir()
	cwd := filepath.Join(repo, "pkg", "app")
	skill := func(dir, desc string) {
		write(t, filepath.Join(dir, SkillFile), "---\ndescription: "+desc+"\n---\nbody of "+desc)
	}
	skill(filepath.Join(hh, "skills/both"), "personal harness")
	skill(filepath.Join(cwd, ".harness/skills/both"), "project")
	skill(filepath.Join(cwd, ".harness/skills/near"), "near harness")
	skill(filepath.Join(repo, ".harness/skills/near"), "far")
	skill(filepath.Join(repo, ".harness/skills/rootonly"), "from the root")
	skill(filepath.Join(cwd, ".harness/skills/group/inner"), "grouped")
	skill(filepath.Join(cwd, ".harness/skills/group/inner/sub/hidden"), "inside a skill")
	write(t, filepath.Join(cwd, ".harness/commands/fe/test.md"), "---\ndescription: frontend tests\n---\nrun")
	write(t, filepath.Join(cwd, ".harness/commands/one.md"), "plain text one")
	write(t, filepath.Join(cwd, ".harness/commands/near.md"), "a command named like a skill")
	write(t, filepath.Join(cwd, ".harness/commands/notes.txt"), "not markdown")

	c := Discover(Options{HarnessHome: hh, Cwd: cwd, GitRoot: repo, Trusted: true})
	want := map[string]string{"both": "personal harness", "near": "near harness", "rootonly": "from the root", "inner": "grouped", "fe:test": "frontend tests", "one": "plain text one"}
	for name, desc := range want {
		s, ok := c.Lookup(name)
		if !ok || s.Description != desc {
			t.Fatalf("%s: %+v (%v)", name, s, ok)
		}
	}
	if _, ok := c.Lookup("hidden"); ok {
		t.Fatal("a skill's own subfolders hold no skills")
	}
	if _, ok := c.Lookup("notes"); ok {
		t.Fatal("only Markdown is a command")
	}
	if s, _ := c.Lookup("/both"); s.Source != SourcePersonal {
		t.Fatalf("a leading slash is fine: %+v", s)
	}
	shadowed := 0
	for _, s := range c.All {
		if s.ShadowedBy != "" {
			shadowed++
		}
	}
	// The project copy of both; of near, the far one and the command.
	if shadowed != 3 {
		t.Fatalf("shadowed %d: %+v", shadowed, c.All)
	}
}

func TestDiscoverKeepsAnUntrustedFolderOut(t *testing.T) {
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".harness/skills/evil/SKILL.md"), "body")
	c := Discover(Options{Cwd: cwd, Trusted: false})
	if len(c.All) != 0 || !c.UntrustedProject {
		t.Fatalf("%+v", c)
	}
}

func TestDiscoverReadsDocumentsAndMaterializes(t *testing.T) {
	docs := Documents{
		{Id: "doc_1", Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SKILL, Key: "imp", ContentHash: "abcdef0123456789abcdef", Content: "---\ndescription: from the server\n---\nthe body",
			Files: []*syncv1.DocumentFile{{Path: "refs/more.md", Content: []byte("more")}, {Path: "run.sh", Content: []byte("#!/bin/sh"), Executable: true}}},
		{Id: "doc_2", Scope: commonv1.Scope_SCOPE_ORG, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SKILL, Key: "org", Content: "org body"},
		{Id: "doc_3", Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_COMMAND, Key: "ns:c", Content: "command body"},
		{Id: "doc_4", Scope: commonv1.Scope_SCOPE_PROJECT, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SKILL, Key: "proj", Content: "p"},
	}
	home := t.TempDir()
	write(t, filepath.Join(home, "skills/imp/SKILL.md"), "---\ndescription: the local copy\n---\nlocal")
	c := Discover(Options{Docs: docs})
	if _, ok := c.Lookup("proj"); ok {
		t.Fatal("a project document has no project here")
	}
	s, ok := c.Lookup("imp")
	if !ok || s.Source != SourceImported || s.Path != "sync:doc_1" {
		t.Fatalf("%+v", s)
	}
	if o, _ := c.Lookup("org"); o.Source != SourceOrganization {
		t.Fatalf("%+v", o)
	}
	if cmd, _ := c.Lookup("ns:c"); cmd.Kind != KindCommand {
		t.Fatalf("%+v", cmd)
	}
	body, err := s.LoadBody()
	if err != nil || body != "the body" {
		t.Fatalf("%q %v", body, err)
	}
	cache := t.TempDir()
	dir, err := s.Materialize(cache)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "refs/more.md")); string(got) != "more" {
		t.Fatalf("supporting file %q", got)
	}
	if st, _ := os.Stat(filepath.Join(dir, "run.sh")); st.Mode()&0o100 == 0 {
		t.Fatal("not executable")
	}
	again, err := s.Materialize(cache)
	if err != nil || again != dir {
		t.Fatalf("again %q %v", again, err)
	}
	// A local personal copy wins over the imported one on this machine.
	both := Discover(Options{HarnessHome: home, Docs: docs})
	if w, _ := both.Lookup("imp"); w.Source != SourcePersonal {
		t.Fatalf("%+v", w)
	}
}

func TestMaterializeKeepsFilesInside(t *testing.T) {
	for _, bad := range []string{"../evil", "a/../../evil", "/abs"} {
		c := Discover(Options{Docs: Documents{{Id: "d", Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SKILL, Key: "x", Content: "b",
			Files: []*syncv1.DocumentFile{{Path: bad, Content: []byte("x")}}}}})
		s, _ := c.Lookup("x")
		root := t.TempDir()
		cache := filepath.Join(root, "cache")
		if _, err := s.Materialize(cache); err == nil {
			t.Fatalf("%q accepted", bad)
		}
		if _, err := os.Stat(filepath.Join(root, "evil")); err == nil {
			t.Fatalf("%q written outside", bad)
		}
	}
}

func TestExpandPlaceholders(t *testing.T) {
	s := Skill{Arguments: []string{"env", "tag"}}
	got := Expand(context.Background(), s, "all=$ARGUMENTS one=$1 idx=$ARGUMENTS[1] env=$env tag=$tag none=$other id=${CLAUDE_SESSION_ID} dir=${HARNESS_SKILL_DIR}",
		ExpandOptions{Arguments: `prod "v 1"`, SessionID: "sid", Dir: "/d"})
	want := `all=prod "v 1" one=prod idx=v 1 env=prod tag=v 1 none=$other id=sid dir=/d`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if got := Expand(context.Background(), Skill{}, "no placeholder", ExpandOptions{Arguments: "x y"}); got != "no placeholder\n\nARGUMENTS: x y" {
		t.Fatalf("%q", got)
	}
	if got := Expand(context.Background(), Skill{}, "no placeholder", ExpandOptions{}); got != "no placeholder" {
		t.Fatalf("%q", got)
	}
	// What arguments bring in isn't read again.
	if got := Expand(context.Background(), Skill{}, "$ARGUMENTS", ExpandOptions{Arguments: "$1 $ARGUMENTS"}); got != "$1 $ARGUMENTS" {
		t.Fatalf("%q", got)
	}
}

func TestExpandCommands(t *testing.T) {
	var ran []string
	shell := func(_ context.Context, c string) string {
		ran = append(ran, c)
		return "out(" + c + ")$1"
	}
	body := "Status: !`git status`\n\n```!\nls\npwd\n```\n\nexample:\n```sh\n!`not run`\n```\nmid!`word` stays\narg: !`echo $1`\n"
	got := Expand(context.Background(), Skill{}, body, ExpandOptions{Arguments: "a'b; rm -rf /", Shell: shell})
	// A value is one quoted word, whatever it holds.
	if strings.Join(ran, "|") != "git status|ls\npwd|echo 'ab; rm -rf /'" {
		t.Fatalf("ran %q", ran)
	}
	if !strings.Contains(got, "Status: out(git status)$1") {
		t.Fatalf("output is read again: %s", got)
	}
	if !strings.Contains(got, "!`not run`") || !strings.Contains(got, "mid!`word` stays") {
		t.Fatalf("examples must stay: %s", got)
	}
	// Text an argument brings can't become a command.
	ran = nil
	got = Expand(context.Background(), Skill{}, "$ARGUMENTS", ExpandOptions{Arguments: "!`rm -rf /`", Shell: shell})
	if len(ran) != 0 || got != "!`rm -rf /`" {
		t.Fatalf("%q ran %q", got, ran)
	}
	if got := Expand(context.Background(), Skill{}, "x !`ls`", ExpandOptions{}); !strings.Contains(got, disabledShell) {
		t.Fatalf("%q", got)
	}
}

func TestParseSlash(t *testing.T) {
	for _, c := range []struct {
		in, name, args string
		ok             bool
	}{
		{"/deploy", "deploy", "", true},
		{"  /deploy prod now ", "deploy", "prod now", true},
		{"/fe:test a\nb", "fe:test", "a\nb", true},
		{"/usr/bin/env is odd", "", "", false},
		{"not a command", "", "", false},
		{"/", "", "", false},
		{"// comment", "", "", false},
	} {
		name, args, ok := ParseSlash(c.in)
		if name != c.name || args != c.args || ok != c.ok {
			t.Errorf("%q: %q %q %v", c.in, name, args, ok)
		}
	}
}

func manager(o Options) *Manager {
	return NewManager(ManagerOptions{Place: func() (string, bool) { return o.Cwd, o.Trusted }, Options: func() Options { return o }})
}

func TestListingHoldsNamesAndDescriptionsOnly(t *testing.T) {
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".harness/skills/a/SKILL.md"), "---\ndescription: does a\nwhen_to_use: when a\nargument-hint: <x>\n---\nSECRET BODY")
	write(t, filepath.Join(cwd, ".harness/skills/hidden/SKILL.md"), "---\ndescription: user only\ndisable-model-invocation: true\n---\nb")
	write(t, filepath.Join(cwd, ".harness/skills/off/SKILL.md"), "---\ndescription: turned off\n---\nb")
	m := manager(Options{Cwd: cwd, Trusted: true, Disabled: map[string]bool{"off": true}})
	text, rep := m.Listing(0, true)
	if text != "- a <x>: does a when a" {
		t.Fatalf("%q", text)
	}
	if !rep.Listed["a"] || rep.Listed["off"] || rep.Budget != DefaultListingBudget || rep.Used != len(text) {
		t.Fatalf("%+v", rep)
	}
	m.SetEnabled("off", true)
	if text, _ := m.Listing(0, true); !strings.Contains(text, "off") {
		t.Fatalf("switched on: %q", text)
	}
	m.SetEnabled("a", false)
	if text, _ := m.Listing(0, true); strings.Contains(text, "does a") {
		t.Fatalf("switched off: %q", text)
	}
}

func TestListingStaysWithinItsBudget(t *testing.T) {
	var list []Skill
	for i := range 60 {
		list = append(list, Skill{Name: "skill-" + itoa(100+i), Description: strings.Repeat("words ", 200)})
	}
	roomy, rep := renderListing(list, 200000)
	if rep.Used != len(roomy) || len(rep.Listed) != 60 || !strings.Contains(roomy, "…") {
		t.Fatalf("a description is capped even with room: %d", rep.Used)
	}
	tight, rep := renderListing(list, 4000)
	if len(tight) > 4000 || len(rep.Listed) != 60 || !strings.Contains(tight, ": words") {
		t.Fatalf("descriptions shrink before names go: %d, %d listed", len(tight), len(rep.Listed))
	}
	names, rep := renderListing(list, 1000)
	if len(names) > 1000 || len(rep.Listed) != 60 || strings.Contains(names, "words") {
		t.Fatalf("names only: %d, %d listed\n%s", len(names), len(rep.Listed), names)
	}
	few, rep := renderListing(list, 300)
	if len(rep.Listed) == 0 || len(rep.Listed) == 60 || !strings.Contains(few, "more") {
		t.Fatalf("the rest are counted: %d\n%s", len(rep.Listed), few)
	}
	if got := BudgetFor(1_000_000); got != 40000 {
		t.Fatalf("1%% of a million-token window: %d", got)
	}
	if got := BudgetFor(1000); got != minListingBudget {
		t.Fatalf("floor: %d", got)
	}
	t.Setenv("HARNESS_SKILL_LISTING_CHARS", "1234")
	if got := BudgetFor(1000); got != 1234 {
		t.Fatalf("override: %d", got)
	}
}

func TestPathsAndNestedFoldersComeWithFiles(t *testing.T) {
	cwd, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, filepath.Join(cwd, ".harness/skills/go/SKILL.md"), "---\ndescription: go work\npaths: \"**/*.go\"\n---\nb")
	write(t, filepath.Join(cwd, "pkg/sub/.harness/skills/deep/SKILL.md"), "---\ndescription: deep work\n---\nb")
	write(t, filepath.Join(cwd, "node_modules/x/.harness/skills/nope/SKILL.md"), "---\ndescription: from a dependency\n---\nb")
	m := manager(Options{Cwd: cwd, Trusted: true})
	if text, _ := m.Listing(0, true); text != "" {
		t.Fatalf("nothing applies yet: %q", text)
	}
	m.NoteFile(filepath.Join(cwd, "README.md"))
	if text, _ := m.Listing(0, false); text != "" {
		t.Fatalf("no match: %q", text)
	}
	m.NoteFile("pkg/main.go")
	if text, _ := m.Listing(0, false); !strings.Contains(text, "go work") {
		t.Fatalf("relative path: %q", text)
	}
	m.NoteFile(filepath.Join(cwd, "pkg/sub/a/b.txt"))
	if text, _ := m.Listing(0, false); !strings.Contains(text, "deep work") {
		t.Fatalf("nested: %q", text)
	}
	m.NoteFile(filepath.Join(cwd, "node_modules/x/index.js"))
	if text, _ := m.Listing(0, true); strings.Contains(text, "dependency") {
		t.Fatalf("a dependency's folder: %q", text)
	}
	// A refresh keeps what files brought in.
	if text, _ := m.Listing(0, true); !strings.Contains(text, "deep work") || !strings.Contains(text, "go work") {
		t.Fatalf("after refresh: %q", text)
	}
}

func TestBodyIsReadWhenUsed(t *testing.T) {
	cwd := t.TempDir()
	p := filepath.Join(cwd, ".harness/skills/a/SKILL.md")
	write(t, p, "---\ndescription: d\n---\nfirst")
	c := Discover(Options{Cwd: cwd, Trusted: true})
	s, _ := c.Lookup("a")
	write(t, p, "---\ndescription: d\n---\nsecond")
	if b, err := s.LoadBody(); err != nil || b != "second" {
		t.Fatalf("%q %v", b, err)
	}
}

// What sits under .claude is not read, in any of the places it used to be.
func TestClaudeFoldersAreNotRead(t *testing.T) {
	home, hh, repo := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	cwd := filepath.Join(repo, "app")
	for _, root := range []string{home, repo, cwd, filepath.Join(cwd, "sub")} {
		write(t, filepath.Join(root, ".claude/skills/nope/SKILL.md"), "---\ndescription: no\n---\nb")
		write(t, filepath.Join(root, ".claude/commands/nope2.md"), "no")
	}
	c := Discover(Options{HarnessHome: hh, Cwd: cwd, GitRoot: repo, Nested: []string{filepath.Join(cwd, "sub")}, Trusted: true})
	if len(c.All) != 0 {
		t.Fatalf("%+v", c.All)
	}
	if u := Discover(Options{HarnessHome: hh, Cwd: cwd, GitRoot: repo}); u.UntrustedProject {
		t.Fatal(".claude folders count as project files")
	}
}
