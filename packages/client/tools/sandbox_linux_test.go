//go:build linux

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/sandbox"
)

// Bash in the sandbox of this machine, whichever backend it allows. sandbox/linux_test.go tests each in depth; this is the
// way a session uses it. The folders are outside /tmp, which is writable by policy.
func TestBashInTheLinuxSandbox(t *testing.T) {
	if info := sandbox.NewDriver().Info(); !info.Supported {
		if os.Getenv("HARNESS_SANDBOX_REQUIRE") != "" {
			t.Fatalf("a sandbox is required here, and: %s", info.Reason)
		}
		t.Skipf("no sandbox here: %s", info.Reason)
	}
	base, err := os.UserCacheDir()
	if err != nil {
		t.Skip(err)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Skip(err)
	}
	root, err := os.MkdirTemp(base, "hsbt")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	for _, d := range []string{home, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	env := NewEnv(work, filepath.Join(root, "scratch"))
	env.Sandbox = &sandbox.Session{Home: home, HarnessHome: filepath.Join(home, ".harness"), Settings: func() sandbox.Settings { return sandbox.Settings{Enabled: true} }, Getenv: func(string) string { return "" }}
	t.Cleanup(env.Sandbox.Close)
	bash := func(command string) (string, bool) {
		t.Helper()
		r := call(t, env, &Bash{}, map[string]any{"command": command, "description": "test command"})
		return r.Content[0].Text, r.IsError
	}
	if out, isErr := bash("echo in > f.txt && cat f.txt && echo $HARNESS_SANDBOX"); isErr || !strings.Contains(out, "in\n1") {
		t.Fatalf("%s", out)
	}
	outside := filepath.Join(root, "outside")
	out, isErr := bash("echo x > " + outside)
	if !isErr || !strings.Contains(out, "[sandbox]") {
		t.Fatalf("%s", out)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("a file appeared outside")
	}
	// The directory comes back through the file the shell leaves in the scratch directory.
	if out, isErr := bash("mkdir -p sub && cd sub"); isErr || env.Shell.Cwd() != filepath.Join(work, "sub") {
		t.Fatalf("%s %s", out, env.Shell.Cwd())
	}
	// The guard's report arrives the same way as a refusal.
	if out, _ := bash("cd .. && echo '{}' > .mcp.json"); !strings.Contains(out, "[sandbox]") {
		t.Fatalf("%s", out)
	}
	if _, err := os.Stat(filepath.Join(work, ".mcp.json")); err == nil {
		t.Fatal(".mcp.json stayed")
	}
}
