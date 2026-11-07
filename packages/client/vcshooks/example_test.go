//go:build unix

package vcshooks

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/hooks"
	"github.com/xlsft/harness/packages/client/settings"
)

// The Jujutsu example of docs/examples/vcs-hooks/jujutsu, run as a user would configure it: one script for the four
// events. TestJujutsuExampleWithFakeJJ drives it with a stand-in for jj that answers like jj does; TestJujutsuExample
// runs it with the real jj, only when jj is on PATH (it is never installed for the test).

func exampleScript(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on PATH")
	}
	_, file, _, _ := runtime.Caller(0)
	p := filepath.Join(filepath.Dir(file), "..", "..", "..", "docs", "examples", "vcs-hooks", "jujutsu", "jj-hooks.py")
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	return p
}

// exampleHooks configures the script for every VCS event, as docs/examples/vcs-hooks/jujutsu/settings.json does.
func exampleHooks(t *testing.T, scriptPath string) Hooks {
	t.Helper()
	cmd := "python3 '" + scriptPath + "'"
	cfg := map[string]any{}
	for _, ev := range []string{"WorktreeCreate", "WorktreeRemove", "VcsStatus", "VcsDiff"} {
		cfg[ev] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd}}}}
	}
	raw, _ := json.Marshal(cfg)
	st := &settings.Settings{Hooks: []settings.HookLayer{{Source: settings.Source{Level: settings.LevelUser}, Raw: raw}}}
	e := hooks.New(hooks.Build(st, nil), hooks.Options{SessionID: "s1"})
	t.Cleanup(e.Close)
	return Of(e)
}

const fakeJJ = `#!/bin/sh
while [ $# -gt 0 ]; do case "$1" in --no-pager|--color=never) shift;; *) break;; esac; done
printf '%s\n' "$*" >> "$FAKEJJ_LOG"
case "$1" in
root)
  here=$(pwd -P); top=$(cd "$FAKEJJ_ROOT" && pwd -P)
  case "$here/" in "$top"/*) echo "$FAKEJJ_ROOT" ;; *) echo "Error: There is no jj repo in \".\"" >&2; exit 1 ;; esac ;;
workspace)
  if [ "$2" = add ]; then eval dest=\${$#}; mkdir -p "$dest/sub"; fi ;;
log) echo qpvuntsm ;;
diff) printf 'diff --git a/a.txt b/a.txt\nindex 1..2 100644\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1,2 @@\n-x\n+y\n+z\ndiff --git a/new.bin b/new.bin\nnew file mode 100644\nBinary files /dev/null and b/new.bin differ\n' ;;
status) ;;
esac
`

func TestJujutsuExampleWithFakeJJ(t *testing.T) {
	scriptPath := exampleScript(t)
	bin, repo, log := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "jj.log")
	if err := os.WriteFile(filepath.Join(bin, "jj"), []byte(fakeJJ), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKEJJ_LOG", log)
	t.Setenv("FAKEJJ_ROOT", repo)
	t.Setenv("HARNESS_JJ_WORKSPACES", filepath.Join(t.TempDir(), "ws"))
	_ = os.MkdirAll(filepath.Join(repo, "sub"), 0o755)
	h := exampleHooks(t, scriptPath)
	ctx := context.Background()

	c, err := h.Create(ctx, CreateRequest{Source: filepath.Join(repo, "sub"), Name: "feat", BaseRevision: "main"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(os.Getenv("HARNESS_JJ_WORKSPACES"), "feat")
	if c.Path != want || c.Cwd != filepath.Join(want, "sub") || c.Base != "qpvuntsm" {
		t.Fatalf("created %+v", c)
	}
	st, err := h.Status(ctx, repo)
	if err != nil || st == nil {
		t.Fatalf("status %v %v", st, err)
	}
	if st.Root != repo || st.Base != "@-" || len(st.Files) != 2 {
		t.Fatalf("status %+v", st)
	}
	if f := st.Files[0]; f.Path != "a.txt" || f.Status != "modified" || *f.Additions != 2 || *f.Deletions != 1 {
		t.Errorf("a.txt %+v", f)
	}
	if f := st.Files[1]; f.Status != "added" || !f.Binary || f.Additions != nil {
		t.Errorf("new.bin %+v", f)
	}
	out, err := h.Diff(ctx, repo, []string{`we"ird.txt`})
	if err != nil || !strings.HasPrefix(string(out), "diff --git a/a.txt") {
		t.Fatalf("diff %q %v", out, err)
	}
	if err := h.Remove(ctx, RemoveRequest{Path: c.Path, Source: repo, Name: "feat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Path); !os.IsNotExist(err) {
		t.Errorf("the workspace folder should be gone: %v", err)
	}
	calls := readLines(t, log)
	for _, w := range []string{"workspace add --name feat --revision main " + want, `diff --git -r @ root-file:"we\"ird.txt"`, "status", "workspace forget feat"} {
		if !containsLine(calls, w) {
			t.Errorf("jj was not called with %q: %q", w, calls)
		}
	}

	// Outside a Jujutsu repository the script prints nothing: declined, and not this hook's folder.
	elsewhere := t.TempDir()
	if _, err := h.Create(ctx, CreateRequest{Source: elsewhere}); err != ErrDeclined {
		t.Errorf("create elsewhere: %v", err)
	}
	if st, err := h.Status(ctx, elsewhere); st != nil || err != nil {
		t.Errorf("status elsewhere: %+v %v", st, err)
	}
}

func readLines(t *testing.T, p string) []string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

// TestJujutsuExample runs the example against the real jj, in a repository of its own with its own configuration.
func TestJujutsuExample(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not on PATH")
	}
	scriptPath := exampleScript(t)
	home := t.TempDir()
	cfg := filepath.Join(home, "jj.toml")
	_ = os.WriteFile(cfg, []byte("[user]\nname = \"Test\"\nemail = \"test@example.invalid\"\n"), 0o644)
	t.Setenv("JJ_CONFIG", cfg)
	t.Setenv("HOME", home)
	t.Setenv("HARNESS_JJ_WORKSPACES", filepath.Join(t.TempDir(), "ws"))
	repo := filepath.Join(t.TempDir(), "repo")
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("jj", append([]string{"--no-pager", "--color=never"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("jj %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	_ = os.MkdirAll(repo, 0o755)
	run(repo, "git", "init")
	_ = os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x\n"), 0o644)
	run(repo, "commit", "-m", "first")

	h := exampleHooks(t, scriptPath)
	ctx := context.Background()
	c, err := h.Create(ctx, CreateRequest{Source: repo, Name: "feat"})
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(c.Path, "a.txt")); err != nil || string(data) != "x\n" {
		t.Fatalf("the workspace should hold the repository's files: %q %v", data, err)
	}
	_ = os.WriteFile(filepath.Join(c.Path, "a.txt"), []byte("y\n"), 0o644)
	_ = os.WriteFile(filepath.Join(c.Path, "b.txt"), []byte("new\n"), 0o644)
	st, err := h.Status(ctx, c.Path)
	if err != nil || st == nil || len(st.Files) != 2 {
		t.Fatalf("status %+v %v", st, err)
	}
	out, err := h.Diff(ctx, c.Path, []string{"a.txt"})
	if err != nil || !strings.Contains(string(out), "+y") || strings.Contains(string(out), "b.txt") {
		t.Fatalf("diff %q %v", out, err)
	}
	if err := h.Remove(ctx, RemoveRequest{Path: c.Path, Source: repo, Name: "feat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Path); !os.IsNotExist(err) {
		t.Errorf("the workspace folder should be gone: %v", err)
	}
	if strings.Contains(run(repo, "workspace", "list"), "feat") {
		t.Error("the workspace should be forgotten")
	}
}
