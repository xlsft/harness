package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/sandbox"
)

func TestPowerShellIsNeverReadOnly(t *testing.T) {
	p := &PowerShell{}
	for _, c := range []string{"Get-ChildItem", "git status", "echo (Remove-Item x)"} {
		input, _ := json.Marshal(map[string]string{"command": c})
		if p.ReadOnly(input) {
			t.Errorf("%s", c)
		}
	}
}

func TestPowerShellOfferedOnlyOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" && !(&PowerShell{}).HiddenFromModel() {
		t.Fatal("offered where Bash is the shell")
	}
}

func TestPowerShellScriptShape(t *testing.T) {
	argv := powerShellArgv(`C:\pwsh.exe`)
	if argv[0] != `C:\pwsh.exe` || argv[len(argv)-2] != "-Command" || argv[len(argv)-1] != powerShellScript {
		t.Fatalf("%q", argv)
	}
	// The command travels in the environment, so nothing of it is parsed before Invoke-Expression has it.
	for _, want := range []string{"-NoProfile", "-NonInteractive"} {
		if !strings.Contains(strings.Join(argv, " "), want) {
			t.Errorf("no %s", want)
		}
	}
	for _, want := range []string{"$env:__HARNESS_CMD", "$env:__HARNESS_CWD_FILE", "Invoke-Expression $__harnessCmd", "exit $global:LASTEXITCODE", "ProviderPath"} {
		if !strings.Contains(powerShellScript, want) {
			t.Errorf("no %s in the script", want)
		}
	}
}

func TestPowerShellInputChecks(t *testing.T) {
	env := NewEnv(t.TempDir(), t.TempDir())
	for _, in := range []map[string]any{{"description": "x"}, {"command": "x"}} {
		if r := call(t, env, &PowerShell{}, in); !r.IsError {
			t.Errorf("%v was accepted", in)
		}
	}
}

// A stand-in for PowerShell where there is none: a program that takes the same command line and environment, runs the command
// with sh and leaves its directory in the file named there. It tests everything of the tool but PowerShell.
func fakePowerShell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in is a shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "pwsh")
	script := `#!/bin/sh
for a in "$@"; do
  case "$a" in
    -NoProfile) np=1 ;;
    -Command) c=1 ;;
  esac
done
if [ -z "$np" ] || [ -z "$c" ]; then echo "bad command line" >&2; exit 99; fi
if [ -n "$__HARNESS_REFUSED" ]; then echo "harness: $__HARNESS_REFUSED" >&2; exit 126; fi
cmd=$__HARNESS_CMD; file=$__HARNESS_CWD_FILE; unset __HARNESS_CMD __HARNESS_CWD_FILE
eval "$cmd"; status=$?
if [ -n "$file" ]; then pwd -P > "$file"; fi
exit $status
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := powerShellFind
	powerShellFind = func() string { return path }
	t.Cleanup(func() { powerShellFind = old })
	return path
}

func TestPowerShellRunsThroughTheShell(t *testing.T) {
	fakePowerShell(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(filepath.Join(work, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := NewEnv(work, filepath.Join(root, "scratch"))
	env.Tasks = NewTaskManager(filepath.Join(root, "scratch", "tasks"))
	t.Cleanup(func() { env.Tasks.StopAll(TaskSessionClosed) })
	run := func(command string, extra map[string]any) Result {
		t.Helper()
		in := map[string]any{"command": command, "description": "test command"}
		for k, v := range extra {
			in[k] = v
		}
		return call(t, env, &PowerShell{}, in)
	}

	r := run("echo hello; echo oops >&2", nil)
	if r.IsError || !strings.Contains(r.Content[0].Text, "hello") || !strings.Contains(r.Content[0].Text, "oops") {
		t.Fatalf("%+v", r)
	}
	if r := run("exit 3", nil); !r.IsError || !strings.Contains(r.Content[0].Text, "Exit code 3") {
		t.Fatalf("%+v", r)
	}
	// The directory carries over, as Bash's does, and Bash sees it too.
	if r := run("cd sub", nil); r.IsError {
		t.Fatalf("%+v", r)
	}
	if got := env.Shell.Cwd(); got != filepath.Join(work, "sub") {
		t.Fatalf("cwd %s", got)
	}
	// In the background: a task, as for Bash.
	r = run("echo in the background", map[string]any{"run_in_background": true})
	if r.IsError || !strings.Contains(r.Summary, "background") {
		t.Fatalf("%+v", r)
	}
}

// noDriver is a machine without a sandbox.
type noDriver struct{}

func (noDriver) Info() sandbox.Info {
	return sandbox.Info{Platform: "test", Reason: "no sandbox on this test machine"}
}
func (noDriver) Command(sandbox.Policy, []string) ([]string, error) {
	return nil, sandbox.ErrUnsupported
}

// A policy document requires the sandbox and the machine has none: the command does not run, and the result says why.
func TestPowerShellTakesTheSandboxRefusal(t *testing.T) {
	fakePowerShell(t)
	work := t.TempDir()
	env := NewEnv(work, t.TempDir())
	env.Sandbox = &sandbox.Session{Driver: noDriver{}, Settings: func() sandbox.Settings { return sandbox.Settings{Enabled: true, Forced: true} }}
	marker := filepath.Join(work, "ran")
	r := call(t, env, &PowerShell{}, map[string]any{"command": "touch " + marker, "description": "test command"})
	if !r.IsError || !strings.Contains(r.Content[0].Text, "no sandbox on this test machine") {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran")
	}
}
