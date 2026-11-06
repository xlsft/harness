package importer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/memory"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/skills"
)

func writeTree(t *testing.T, root string, files map[string]string) {
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

func readT(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// treeHash is what is under dir, for proving it did not change.
func treeHash(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, _ := d.Info()
		data, _ := os.ReadFile(p)
		lines = append(lines, fmt.Sprintf("%s %x %v %v", p, sha256.Sum256(data), info.Mode(), info.ModTime()))
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func runProject(t *testing.T, root string, trusted, force bool) *ProjectReport {
	t.Helper()
	rep, err := RunProject(context.Background(), ProjectOptions{Root: root, Home: filepath.Join(filepath.Dir(root), "home"), Trusted: trusted, Force: force})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func item(t *testing.T, rep *ProjectReport, source string) ProjectItem {
	t.Helper()
	for _, it := range rep.Items {
		if it.Source == source {
			return it
		}
	}
	t.Fatalf("no item for %s: %+v", source, rep.Items)
	return ProjectItem{}
}

func fullProject(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	writeTree(t, root, map[string]string{
		"CLAUDE.md":                    "# Project\nRun make.\n",
		".claude/CLAUDE.md":            "Dot claude text.\n",
		".claude/AGENTS.md":            "Dot agents text.\n",
		"CLAUDE.local.md":              "My own notes.\n",
		".claude/rules/go.md":          "---\npaths: \"**/*.go\"\n---\nGo rule\n",
		".claude/rules/all.md":         "Always rule\n",
		".claude/skills/ship/SKILL.md": "---\ndescription: Ship it\n---\nShip body\n",
		".claude/skills/ship/run.sh":   "#!/bin/sh\n",
		".claude/commands/fe/test.md":  "---\ndescription: Frontend\n---\nRun tests\n",
		".claude/agents/rev.md":        "---\nname: rev\ndescription: Reviews\n---\nReview it\n",
		".claude/output-styles/t.md":   "Terse\n",
		".claude/workflows/w.js":       "export default 1\n",
		".claude/hooks/fmt.sh":         "#!/bin/sh\n",
		".claude/settings.json": `{"model":"m","apiKeyHelper":"x","permissions":{"allow":["Bash(make *)"],"deny":["Read(.env)"]},
			"hooks":{"Stop":[{"hooks":[{"type":"command","command":"\"$CLAUDE_PROJECT_DIR\"/.claude/hooks/fmt.sh"}]}]}}`,
		".claude/settings.local.json": `{"permissions":{"allow":["Edit(/src/**)"]}}`,
		"sub/CLAUDE.md":               "Nested text.\n",
		"sub/.claude/rules/r.md":      "Nested rule\n",
		"node_modules/x/CLAUDE.md":    "Dependency\n",
	})
	if err := os.Chmod(filepath.Join(root, ".claude/skills/ship/run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestProjectDryRunWritesNothing(t *testing.T) {
	root := fullProject(t)
	before := treeHash(t, root)
	rep, err := RunProject(context.Background(), ProjectOptions{Root: root, Trusted: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Planned() == 0 {
		t.Fatalf("%+v", rep.Items)
	}
	if treeHash(t, root) != before {
		t.Fatal("a dry run wrote")
	}
	if it := item(t, rep, ".claude/settings.json"); !it.NeedsTrust || it.Status != StatusPlanned || !strings.Contains(it.Note, "1 allow rules") || !strings.Contains(it.Note, "hooks on Stop") {
		t.Fatalf("%+v", it)
	}
}

func TestProjectImportBringsEverythingAcross(t *testing.T) {
	root := fullProject(t)
	claude := treeHash(t, filepath.Join(root, ".claude"))
	claudeMD := readT(t, filepath.Join(root, "CLAUDE.md"))
	rep := runProject(t, root, true, false)
	if rep.HasErrors() {
		t.Fatalf("%+v", rep.Items)
	}
	if treeHash(t, filepath.Join(root, ".claude")) != claude || readT(t, filepath.Join(root, "CLAUDE.md")) != claudeMD {
		t.Fatal("the sources changed")
	}
	agents := readT(t, filepath.Join(root, "AGENTS.md"))
	for _, want := range []string{"Run make.", "Dot claude text."} {
		if !strings.Contains(agents, want) {
			t.Fatalf("AGENTS.md lacks %q:\n%s", want, agents)
		}
	}
	for path, want := range map[string]string{
		".harness/AGENTS.md":            "Dot agents text.",
		".harness/AGENTS.local.md":      "My own notes.",
		".harness/rules/go.md":          "Go rule",
		".harness/skills/ship/SKILL.md": "Ship body",
		".harness/commands/fe/test.md":  "Run tests",
		".harness/agents/rev.md":        "Review it",
		".harness/output-styles/t.md":   "Terse",
		".harness/workflows/w.js":       "export default 1",
		".harness/hooks/fmt.sh":         "#!/bin/sh",
		"sub/AGENTS.md":                 "Nested text.",
		"sub/.harness/rules/r.md":       "Nested rule",
	} {
		if !strings.Contains(readT(t, filepath.Join(root, path)), want) {
			t.Errorf("%s lacks %q", path, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "node_modules/x/AGENTS.md")); err == nil {
		t.Error("a dependency's folder was imported")
	}
	if st, _ := os.Stat(filepath.Join(root, ".harness/skills/ship/run.sh")); st.Mode()&0o100 == 0 {
		t.Error("the script lost its executable bit")
	}

	// It takes effect through the live readers, which see none of the sources.
	st, err := settings.Load(context.Background(), settings.Options{Cwd: root, Home: filepath.Join(root, "nohome"), Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Model != "m" || len(st.Permissions.Allow) != 2 || len(st.Hooks) != 1 {
		t.Fatalf("settings %+v", st)
	}
	var cfg struct {
		APIKeyHelper string `json:"apiKeyHelper"`
		Hooks        map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(readT(t, filepath.Join(root, ".harness/settings.json"))), &cfg); err != nil {
		t.Fatal(err)
	}
	if cmd := cfg.Hooks["Stop"][0].Hooks[0].Command; cmd != `"$CLAUDE_PROJECT_DIR"/.harness/hooks/fmt.sh` {
		t.Fatalf("hook command %q: the variable stays, the path moves", cmd)
	}
	if cfg.APIKeyHelper != "" {
		t.Fatal("apiKeyHelper came along")
	}
	l := memory.New(memory.Options{Cwd: root, Home: filepath.Join(root, "nohome"), ManagedFiles: []string{}})
	rendered := memory.Render(l.Initial())
	for _, want := range []string{"Run make.", "Dot agents text.", "My own notes.", "Always rule"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("not loaded: %q in\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "harness:import") {
		t.Errorf("the marks reach the model:\n%s", rendered)
	}
	cat := skills.Discover(skills.Options{Cwd: root, Trusted: true})
	for _, name := range []string{"ship", "fe:test"} {
		if _, ok := cat.Lookup(name); !ok {
			t.Errorf("%s not found after import", name)
		}
	}
}

func TestProjectImportTwiceDuplicatesNothing(t *testing.T) {
	root := fullProject(t)
	runProject(t, root, true, false)
	agents := readT(t, filepath.Join(root, "AGENTS.md"))
	sett := readT(t, filepath.Join(root, ".harness/settings.json"))
	rep := runProject(t, root, true, false)
	for _, it := range rep.Items {
		if it.Status != StatusExists {
			t.Errorf("second run: %+v", it)
		}
	}
	if got := readT(t, filepath.Join(root, "AGENTS.md")); got != agents {
		t.Fatalf("AGENTS.md changed:\n%s", got)
	}
	if got := readT(t, filepath.Join(root, ".harness/settings.json")); got != sett {
		t.Fatalf("settings changed:\n%s", got)
	}
}

func TestProjectImportAppendsToExistingAgents(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	writeTree(t, root, map[string]string{"AGENTS.md": "Mine.\n", "CLAUDE.md": "Theirs.\n"})
	runProject(t, root, false, false)
	got := readT(t, filepath.Join(root, "AGENTS.md"))
	if !strings.HasPrefix(got, "Mine.\n") || !strings.Contains(got, "Theirs.") {
		t.Fatalf("%q", got)
	}
	if strings.Count(got, "Theirs.") != 1 {
		t.Fatalf("%q", got)
	}
}

func TestProjectImportKnowsTextAlreadyThere(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	writeTree(t, root, map[string]string{"AGENTS.md": "Intro\n\nThe same text.\n", "CLAUDE.md": "The same text.\n"})
	rep := runProject(t, root, true, false)
	if it := item(t, rep, "CLAUDE.md"); it.Status != StatusExists {
		t.Fatalf("%+v", it)
	}
	if got := readT(t, filepath.Join(root, "AGENTS.md")); got != "Intro\n\nThe same text.\n" {
		t.Fatalf("%q", got)
	}
}

func TestProjectImportChangedSource(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	writeTree(t, root, map[string]string{"CLAUDE.md": "First $1 version.\n"})
	runProject(t, root, true, false)
	writeTree(t, root, map[string]string{"CLAUDE.md": "Second $1 version.\n"})
	rep := runProject(t, root, true, false)
	if it := item(t, rep, "CLAUDE.md"); it.Status != StatusSkipped || !strings.Contains(it.Note, "changed") {
		t.Fatalf("%+v", it)
	}
	if !strings.Contains(readT(t, filepath.Join(root, "AGENTS.md")), "First $1") {
		t.Fatal("replaced without force")
	}
	runProject(t, root, true, true)
	got := readT(t, filepath.Join(root, "AGENTS.md"))
	if strings.Contains(got, "First") || !strings.Contains(got, "Second $1 version.") {
		t.Fatalf("%q", got)
	}
}

func TestProjectImportResolvesImports(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	writeTree(t, base, map[string]string{
		"outside.md":           "SECRET OUTSIDE",
		"home/.claude/note.md": "SECRET HOME",
	})
	writeTree(t, root, map[string]string{
		"CLAUDE.md":         "Top @AGENTS.md\nsee @docs/a.md\nand @.claude/more.md\nand @../outside.md\nand @~/.claude/note.md\nmail me@x.md\n`@.claude/more.md`\n",
		"AGENTS.md":         "Existing\n",
		"docs/a.md":         "Doc\n",
		".claude/more.md":   "More text @nested.md @more.md\n",
		".claude/nested.md": "Nested text\n",
		".claude/CLAUDE.md": "@../docs/a.md and @more.md\n",
	})
	rep := runProject(t, root, true, false)
	got := readT(t, filepath.Join(root, "AGENTS.md"))
	for _, want := range []string{"see @docs/a.md", "More text Nested text", "@../outside.md", "@~/.claude/note.md", "me@x.md", "`@.claude/more.md`"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"SECRET", "@AGENTS.md", "and @.claude/more.md"} {
		if strings.Contains(got, bad) {
			t.Errorf("has %q:\n%s", bad, got)
		}
	}
	// From .claude/CLAUDE.md a reference to a project file is adjusted to where the text now lives.
	if !strings.Contains(got, "@docs/a.md and More text") {
		t.Errorf("relative reference:\n%s", got)
	}
	if it := item(t, rep, "CLAUDE.md"); !strings.Contains(it.Note, "outside this folder") {
		t.Errorf("%+v", it)
	}
	// What is left reads as it should at run time: the project file loads, nothing of .claude does.
	in := memory.New(memory.Options{Cwd: root, Home: filepath.Join(base, "home"), ManagedFiles: []string{}}).Initial()
	if rendered := memory.Render(in); !strings.Contains(rendered, "Doc") || strings.Contains(rendered, "SECRET HOME") {
		t.Errorf("%s", rendered)
	}
}

func TestProjectImportOnlyReferences(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	writeTree(t, root, map[string]string{"AGENTS.md": "Real.\n", "CLAUDE.md": "@AGENTS.md\n"})
	rep := runProject(t, root, true, false)
	if it := item(t, rep, "CLAUDE.md"); it.Status != StatusSkipped {
		t.Fatalf("%+v", it)
	}
	if got := readT(t, filepath.Join(root, "AGENTS.md")); got != "Real.\n" {
		t.Fatalf("%q", got)
	}
}

func TestProjectImportLinks(t *testing.T) {
	t.Run("CLAUDE.md links to AGENTS.md", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "repo")
		writeTree(t, root, map[string]string{"AGENTS.md": "Real.\n"})
		if err := os.Symlink("AGENTS.md", filepath.Join(root, "CLAUDE.md")); err != nil {
			t.Skip(err)
		}
		rep := runProject(t, root, true, false)
		if it := item(t, rep, "CLAUDE.md"); it.Status != StatusExists {
			t.Fatalf("%+v", it)
		}
		if got := readT(t, filepath.Join(root, "AGENTS.md")); got != "Real.\n" {
			t.Fatalf("%q", got)
		}
	})
	t.Run("AGENTS.md links to CLAUDE.md", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "repo")
		writeTree(t, root, map[string]string{"CLAUDE.md": "Real.\n"})
		if err := os.Symlink("CLAUDE.md", filepath.Join(root, "AGENTS.md")); err != nil {
			t.Skip(err)
		}
		runProject(t, root, true, false)
		if st, _ := os.Lstat(filepath.Join(root, "AGENTS.md")); st.Mode()&fs.ModeSymlink != 0 {
			t.Fatal("still a link")
		}
		if got := readT(t, filepath.Join(root, "CLAUDE.md")); got != "Real.\n" {
			t.Fatalf("the source changed: %q", got)
		}
		if !strings.Contains(readT(t, filepath.Join(root, "AGENTS.md")), "Real.") {
			t.Fatal("text lost")
		}
	})
	t.Run(".harness links into .claude", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "repo")
		writeTree(t, root, map[string]string{".claude/rules/a.md": "A\n", ".claude/settings.json": `{"model":"m"}`})
		if err := os.Symlink(".claude", filepath.Join(root, ".harness")); err != nil {
			t.Skip(err)
		}
		before := treeHash(t, filepath.Join(root, ".claude"))
		rep := runProject(t, root, true, false)
		if !rep.HasErrors() {
			t.Fatalf("%+v", rep.Items)
		}
		if treeHash(t, filepath.Join(root, ".claude")) != before {
			t.Fatal(".claude was written through the link")
		}
	})
	t.Run(".harness leads out of the folder", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "repo")
		writeTree(t, root, map[string]string{".claude/agents/a.md": "A\n"})
		if err := os.Symlink(filepath.Join(base, "elsewhere"), filepath.Join(root, ".harness")); err != nil {
			t.Skip(err)
		}
		os.MkdirAll(filepath.Join(base, "elsewhere"), 0o755)
		runProject(t, root, true, false)
		if _, err := os.Stat(filepath.Join(base, "elsewhere", "agents", "a.md")); err == nil {
			t.Fatal("wrote outside the folder")
		}
	})
	t.Run("sources leading out of the folder stay behind", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "repo")
		writeTree(t, base, map[string]string{"secret": "SECRET"})
		writeTree(t, root, map[string]string{".claude/skills/s/SKILL.md": "Skill\n"})
		for link, target := range map[string]string{
			"CLAUDE.md":             filepath.Join(base, "secret"),
			".claude/skills/s/key":  filepath.Join(base, "secret"),
			".claude/commands/c.md": filepath.Join(base, "secret"),
			".claude/settings.json": filepath.Join(base, "secret"),
		} {
			os.MkdirAll(filepath.Dir(filepath.Join(root, link)), 0o755)
			if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
				t.Skip(err)
			}
		}
		runProject(t, root, true, false)
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && d.Type()&fs.ModeSymlink == 0 && strings.Contains(readT(t, p), "SECRET") {
				t.Errorf("%s holds the secret", p)
			}
			return nil
		})
	})
}

func TestProjectLocalSettingsWaitForTrust(t *testing.T) {
	root := fullProject(t)
	rep := runProject(t, root, false, false)
	it := item(t, rep, ".claude/settings.local.json")
	if it.Status != StatusSkipped || !it.NeedsTrust {
		t.Fatalf("%+v", it)
	}
	if _, err := os.Stat(filepath.Join(root, ".harness/settings.local.json")); err == nil {
		t.Fatal("local settings came over without trust: they would apply at once")
	}
	// What did come over applies only once trusted.
	st, err := settings.Load(context.Background(), settings.Options{Cwd: root, Home: filepath.Join(root, "nohome"), Trusted: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Permissions.Allow) != 0 {
		t.Fatalf("allow rules before trust: %+v", st.Permissions.Allow)
	}
	for _, l := range st.Hooks {
		if l.Gate != settings.HookGateWorkspace {
			t.Fatalf("hook runs before trust: %+v", l)
		}
	}
	// Trusting it and running again brings the rest.
	rep = runProject(t, root, true, false)
	if it := item(t, rep, ".claude/settings.local.json"); it.Status != StatusCopied {
		t.Fatalf("%+v", it)
	}
}

func TestProjectSettingsMerge(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	writeTree(t, root, map[string]string{
		".claude/settings.json":  `{"model":"theirs","effortLevel":"low","permissions":{"allow":["A","B"]},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`,
		".harness/settings.json": `{"model":"mine","permissions":{"allow":["B","C"]},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"y"}]}]}}`,
	})
	runProject(t, root, true, false)
	var got struct {
		Model, EffortLevel string
		Permissions        struct{ Allow []string }
		Hooks              map[string][]any
	}
	if err := json.Unmarshal([]byte(readT(t, filepath.Join(root, ".harness/settings.json"))), &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "mine" || got.EffortLevel != "low" || strings.Join(got.Permissions.Allow, ",") != "B,C,A" || len(got.Hooks["Stop"]) != 2 {
		t.Fatalf("%+v", got)
	}
	writeTree(t, root, map[string]string{".harness/settings.json": `{not json`})
	rep := runProject(t, root, true, false)
	if it := item(t, rep, ".claude/settings.json"); it.Status != StatusError {
		t.Fatalf("%+v", it)
	}
	if got := readT(t, filepath.Join(root, ".harness/settings.json")); got != `{not json` {
		t.Fatalf("%q", got)
	}
}

func TestProjectCopiesKeepDifferences(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	writeTree(t, root, map[string]string{".claude/agents/a.md": "theirs\n", ".harness/agents/a.md": "mine\n"})
	rep := runProject(t, root, true, false)
	if it := item(t, rep, ".claude/agents/a.md"); it.Status != StatusSkipped {
		t.Fatalf("%+v", it)
	}
	if got := readT(t, filepath.Join(root, ".harness/agents/a.md")); got != "mine\n" {
		t.Fatalf("%q", got)
	}
	runProject(t, root, true, true)
	if got := readT(t, filepath.Join(root, ".harness/agents/a.md")); got != "theirs\n" {
		t.Fatalf("%q", got)
	}
}

func TestProjectPrivateFilesStayOutOfGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := filepath.Join(t.TempDir(), "repo")
	writeTree(t, root, map[string]string{"CLAUDE.local.md": "Private\n", ".claude/settings.local.json": `{"env":{"TOKEN":"x"}}`, "CLAUDE.md": "Shared\n"})
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	runProject(t, root, true, false)
	runProject(t, root, true, false)
	status := exec.Command("git", "status", "--porcelain", "--untracked-files=all")
	status.Dir = root
	out, err := status.Output()
	if err != nil {
		t.Fatal(err)
	}
	if s := string(out); strings.Contains(s, "AGENTS.local.md") || strings.Contains(s, ".harness/settings.local.json") || !strings.Contains(s, "AGENTS.md") {
		t.Fatalf("git sees:\n%s", s)
	}
	exclude := readT(t, filepath.Join(root, ".git/info/exclude"))
	if strings.Count(exclude, "/.harness/AGENTS.local.md") != 1 {
		t.Fatalf("exclude:\n%s", exclude)
	}
}

func TestProjectHomeFolderIsTheUsers(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	writeTree(t, home, map[string]string{".claude/CLAUDE.md": "user\n", "CLAUDE.md": "home\n", "work/CLAUDE.md": "work\n"})
	rep, err := RunProject(context.Background(), ProjectOptions{Root: home, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 0 {
		t.Fatalf("%+v", rep.Items)
	}
	// A dotfiles repository at home does not make a project folder of everything in it.
	if err := os.MkdirAll(filepath.Join(home, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(home, "work")
	if got := ProjectRoot(work, home); got != work {
		t.Fatalf("%q", got)
	}
}

func TestHasHarnessFolder(t *testing.T) {
	root := t.TempDir()
	if HasHarnessFolder(root) {
		t.Fatal("none yet")
	}
	writeTree(t, root, map[string]string{".harness/settings.local.json": "{}", ".harness/worktrees/w/x": "x"})
	if HasHarnessFolder(root) {
		t.Fatal("what the client put there itself does not count")
	}
	writeTree(t, root, map[string]string{".harness/settings.json": "{}"})
	if !HasHarnessFolder(root) {
		t.Fatal("a real one")
	}
}

func TestOfferStore(t *testing.T) {
	s := OfferStore{Path: filepath.Join(t.TempDir(), "sub", "offers.json")}
	root := t.TempDir()
	if s.Dismissed(root) {
		t.Fatal("not yet")
	}
	if err := s.SetDismissed(root, true); err != nil {
		t.Fatal(err)
	}
	if !(OfferStore{Path: s.Path}).Dismissed(root) {
		t.Fatal("not remembered")
	}
	if err := s.SetDismissed(root, false); err != nil {
		t.Fatal(err)
	}
	if s.Dismissed(root) {
		t.Fatal("not forgotten")
	}
}
