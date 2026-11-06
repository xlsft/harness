//go:build darwin

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/sandbox"
)

// sandboxedEnv is a session with the real sandbox over temp directories: HOME, HARNESS_HOME and the project.
func sandboxedEnv(t *testing.T) (*Env, *sandbox.Settings, string) {
	t.Helper()
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("no sandbox-exec")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
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
	st := &sandbox.Settings{Enabled: true}
	env.Sandbox = &sandbox.Session{Home: home, HarnessHome: filepath.Join(home, ".harness"), Settings: func() sandbox.Settings { return *st }, Getenv: func(string) string { return "" }}
	t.Cleanup(env.Sandbox.Close)
	env.Tasks = NewTaskManager(filepath.Join(root, "scratch", "tasks"))
	t.Cleanup(func() { env.Tasks.StopAll(TaskSessionClosed) })
	return env, st, root
}

func sandboxBash(t *testing.T, env *Env, input map[string]any) (string, map[string]any, bool) {
	t.Helper()
	input["description"] = "test command"
	r := call(t, env, &Bash{}, input)
	text := ""
	for _, b := range r.Content {
		text += b.Text
	}
	sm, _ := r.Structured.(map[string]any)
	return text, sm, r.IsError
}

func TestBashInTheSandbox(t *testing.T) {
	env, st, root := sandboxedEnv(t)
	outside := filepath.Join(root, "outside.txt")

	// Inside works, and says nothing about the sandbox.
	out, sm, isErr := sandboxBash(t, env, map[string]any{"command": "echo in > f.txt && cat f.txt && echo $HARNESS_SANDBOX"})
	if isErr || !strings.Contains(out, "in\n1") || sm["sandbox"] != nil || sm["sandboxNote"] != nil {
		t.Fatalf("inside: %q %v", out, sm)
	}

	// Outside is refused; the result names what, tells the model not to retry and how to go on.
	out, sm, isErr = sandboxBash(t, env, map[string]any{"command": "echo x > " + outside})
	if !isErr || !strings.Contains(out, "[sandbox]") || !strings.Contains(out, outside) || !strings.Contains(out, "do not retry") || !strings.Contains(out, OutsideSandboxParam) {
		t.Fatalf("refused: %q", out)
	}
	if note, _ := sm["sandboxNote"].(string); !strings.HasSuffix(out, note) || note == "" {
		t.Fatalf("the note closes the text: %q vs %q", out, note)
	}
	if denied, _ := sm["sandboxDenied"].(string); !strings.Contains(denied, outside) {
		t.Fatalf("short form: %v", sm)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("written")
	}

	// The cwd still carries over, inside the sandbox.
	if _, _, isErr := sandboxBash(t, env, map[string]any{"command": "mkdir -p sub && cd sub"}); isErr || env.Shell.Cwd() != filepath.Join(env.Cwd, "sub") {
		t.Fatalf("cwd: %s", env.Shell.Cwd())
	}

	// Approved to run outside: it runs there, and the row can say so.
	out, sm, isErr = sandboxBash(t, env, map[string]any{"command": "echo x > " + outside + " && echo '<'$HARNESS_SANDBOX'>'", OutsideSandboxParam: true})
	if isErr || !strings.Contains(out, "<>") || sm["sandbox"] != "outside" {
		t.Fatalf("outside: %q %v", out, sm)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("not written", err)
	}

	// With the sandbox off nothing is confined, and nothing is marked.
	st.Enabled = false
	out, sm, isErr = sandboxBash(t, env, map[string]any{"command": "echo y > " + outside + ".2 && echo '<'$HARNESS_SANDBOX'>'", OutsideSandboxParam: true})
	if isErr || !strings.Contains(out, "<>") || sm["sandbox"] != nil {
		t.Fatalf("off: %q %v", out, sm)
	}
}

func TestBashSandboxRefusesWhenRequiredAndImpossible(t *testing.T) {
	env, st, root := sandboxedEnv(t)
	st.Forced = true
	env.Sandbox.Driver = stub{}
	out, _, isErr := sandboxBash(t, env, map[string]any{"command": "echo x > " + filepath.Join(root, "no")})
	if !isErr || !strings.Contains(out, "administrator") {
		t.Fatalf("%q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "no")); err == nil {
		t.Fatal("it ran")
	}
}

type stub struct{}

func (stub) Info() sandbox.Info { return sandbox.Info{Platform: "test", Reason: "no driver"} }
func (stub) Command(sandbox.Policy, []string) ([]string, error) {
	return nil, sandbox.ErrUnsupported
}

func TestUserShellIsNotConfined(t *testing.T) {
	env, _, root := sandboxedEnv(t)
	target := filepath.Join(root, "typed-by-the-user")
	res := env.Shell.RunUser(context.Background(), "echo x > "+target, nil)
	if res.ExitCode != 0 {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("a command the user typed should not be confined")
	}
}

func TestBackgroundAndMonitorInTheSandbox(t *testing.T) {
	env, _, root := sandboxedEnv(t)
	target := filepath.Join(root, "bg.txt")

	r := call(t, env, &Bash{}, map[string]any{"description": "bg", "command": "echo started; echo x > " + target, "run_in_background": true})
	sm := r.Structured.(map[string]any)
	id, _ := sm["backgroundTaskId"].(string)
	sandboxTaskDone(t, env, id)
	data, err := os.ReadFile(sm["outputFile"].(string))
	if err != nil {
		t.Fatal(err)
	}
	// Its output went to the file outside its writable tree of the time, and the file says what happened.
	if s := string(data); !strings.Contains(s, "started") || !strings.Contains(s, "[sandbox]") || !strings.Contains(s, target) {
		t.Fatalf("output file: %q", s)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("a background command wrote outside")
	}

	// Monitor with a command starts it the same way.
	in := map[string]any{"description": "watch", "command": "echo x > " + target + ".m; echo done", "pattern": "done"}
	b, _ := json.Marshal(in)
	h := (&Monitor{}).PermissionHints(context.Background(), env, Call{Input: b})
	if h.CheckAs != "Bash" || h.OutsideSandbox {
		t.Fatalf("hints: %+v", h)
	}
	in[OutsideSandboxParam] = true
	b, _ = json.Marshal(in)
	if h := (&Monitor{}).PermissionHints(context.Background(), env, Call{Input: b}); !h.OutsideSandbox {
		t.Fatalf("hints: %+v", h)
	}
	delete(in, OutsideSandboxParam)
	call(t, env, &Monitor{}, in)
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(target + ".m"); err == nil {
		t.Fatal("a monitored command wrote outside")
	}
}

func TestPreviewCommandIsAServerInTheSandbox(t *testing.T) {
	env, _, root := sandboxedEnv(t)
	// Listening works where a plain command cannot.
	script := `python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); s.listen(1); print("listening")'`
	out, _, isErr := sandboxBash(t, env, map[string]any{"command": script})
	if !isErr || !strings.Contains(out, "local port") {
		t.Fatalf("a plain command listened: %q", out)
	}
	o, err := env.Shell.Command(script, env.Cwd).CombinedOutput()
	if err != nil || !strings.Contains(string(o), "listening") {
		t.Fatalf("server: %v %s", err, o)
	}
	// Its files are as confined as anything.
	if err := env.Shell.Command("echo x > "+filepath.Join(root, "from-server"), env.Cwd).Run(); err == nil {
		t.Fatal("a server wrote outside")
	}
}

func TestSubagentStyleEnvKeepsItsWorktreeAndScratch(t *testing.T) {
	env, _, root := sandboxedEnv(t)
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	sub := NewEnv(wt, filepath.Join(root, "scratch", "agents", "a1"))
	sub.Sandbox = env.Sandbox
	if out, _, isErr := sandboxBash(t, sub, map[string]any{"command": `echo x > here && echo y > "$TMPDIR/t"`}); isErr {
		t.Fatalf("%q", out)
	}
	if out, _, isErr := sandboxBash(t, sub, map[string]any{"command": "echo x > " + filepath.Join(env.Cwd, "lead-file")}); !isErr {
		t.Fatalf("the lead's folder is not this agent's: %q", out)
	}
}

func sandboxTaskDone(t *testing.T, env *Env, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if info, ok := env.Tasks.Get(id); ok && !info.Running {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %s did not finish", id)
}
