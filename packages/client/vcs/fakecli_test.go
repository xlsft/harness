package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeEnv is a temp dir with fake gh/glab executables on an isolated PATH (real CLIs on the machine
// are never reachable). Each fake answers from per-command fixture files and records its arguments
// and the environment variables that must disable prompts.
type fakeEnv struct {
	t    *testing.T
	bin  string
	data string
}

const fakeScript = `#!/bin/sh
d="__DATA__/__NAME__"
printf '%s\n' "$*" >> "$d/calls"
tty=no
[ -t 0 ] && tty=yes
printf 'GH_PROMPT_DISABLED=%s NO_PROMPT=%s NO_COLOR=%s GH_PAGER=%s TTY=%s\n' "$GH_PROMPT_DISABLED" "$NO_PROMPT" "$NO_COLOR" "$GH_PAGER" "$tty" >> "$d/env"
case "$*" in
  "--version") c=version ;;
  "auth status --json hosts") c=auth-json ;;
  "auth status --all") c=auth-all ;;
  "auth status") c=auth ;;
  "pr list "*) c=pr-list ;;
  "api --hostname "*"/jobs"*) c=api-jobs ;;
  "api --hostname "*"/merge_requests?"*) c=api-mrs ;;
  "api --hostname "*"/merge_requests/"*) c=api-mr ;;
  *) echo "unknown command: $*" >&2; exit 2 ;;
esac
if [ -f "$d/$c.sleep" ]; then
  read s < "$d/$c.sleep"
  exec __SLEEP__ "$s"
fi
[ -f "$d/$c.out" ] && __CAT__ "$d/$c.out"
[ -f "$d/$c.err" ] && __CAT__ "$d/$c.err" >&2
code=0
[ -f "$d/$c.code" ] && read code < "$d/$c.code"
exit "$code"
`

func newFakeEnv(t *testing.T) *fakeEnv {
	t.Helper()
	root := t.TempDir()
	f := &fakeEnv{t: t, bin: filepath.Join(root, "bin"), data: filepath.Join(root, "data")}
	for _, d := range []string{f.bin, f.data} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// runner returns a Runner whose PATH contains only the fake bin dir.
func (f *fakeEnv) runner() *Runner {
	return &Runner{Timeout: 5 * time.Second, Env: []string{"PATH=" + f.bin, "HOME=" + f.data}}
}

func mustLook(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not available: %v", name, err)
	}
	return p
}

// install creates the fake executable name.
func (f *fakeEnv) install(name string) *fakeCLI {
	f.t.Helper()
	dir := filepath.Join(f.data, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	s := strings.NewReplacer("__DATA__", f.data, "__NAME__", name,
		"__SLEEP__", mustLook(f.t, "sleep"), "__CAT__", mustLook(f.t, "cat")).Replace(fakeScript)
	if err := os.WriteFile(filepath.Join(f.bin, name), []byte(s), 0o755); err != nil {
		f.t.Fatal(err)
	}
	return &fakeCLI{t: f.t, dir: dir}
}

type fakeCLI struct {
	t   *testing.T
	dir string
}

func (c *fakeCLI) write(file, content string) {
	c.t.Helper()
	if err := os.WriteFile(filepath.Join(c.dir, file), []byte(content), 0o644); err != nil {
		c.t.Fatal(err)
	}
}

// set answers command case cmd with stdout, stderr and an exit code; it clears a previous sleep.
func (c *fakeCLI) set(cmd, stdout, stderr string, code int) {
	c.t.Helper()
	_ = os.Remove(filepath.Join(c.dir, cmd+".sleep"))
	c.write(cmd+".out", stdout)
	c.write(cmd+".err", stderr)
	c.write(cmd+".code", strconv.Itoa(code))
}

// hang makes command case cmd sleep for secs seconds.
func (c *fakeCLI) hang(cmd string, secs int) { c.write(cmd+".sleep", strconv.Itoa(secs)) }

func (c *fakeCLI) lines(file string) []string {
	b, _ := os.ReadFile(filepath.Join(c.dir, file))
	s := strings.TrimSpace(string(b))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func (c *fakeCLI) calls() []string { return c.lines("calls") }

// assertNonInteractive checks every invocation got the no-prompt environment and no terminal stdin.
func (c *fakeCLI) assertNonInteractive() {
	c.t.Helper()
	env := c.lines("env")
	if len(env) == 0 {
		c.t.Fatal("fake CLI was never invoked")
	}
	for _, l := range env {
		if l != "GH_PROMPT_DISABLED=1 NO_PROMPT=1 NO_COLOR=1 GH_PAGER=cat TTY=no" {
			c.t.Fatalf("interactive environment: %q", l)
		}
	}
}
