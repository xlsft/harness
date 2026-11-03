package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBashExtraEnv(t *testing.T) {
	t.Setenv("HARNESS_TEST_CLIENT_VAR", "client")
	env := newEnv(t)
	env.SetExtraEnv([]string{"HARNESS_TEST_CLIENT_VAR=session", "MULTI=a\nb", "__HARNESS_CMD=ignored", "HARNESS=0"})
	r := call(t, env, &Bash{}, map[string]any{"command": `printf '%s|%s|%s' "$HARNESS_TEST_CLIENT_VAR" "$MULTI" "$HARNESS"`})
	if got := strings.TrimSpace(text(r)); got != "session|a\nb|1" {
		t.Fatalf("env: %q", got)
	}
	// A change applies to the next command; background tasks get it too.
	env.SetExtraEnv([]string{"HARNESS_TEST_CLIENT_VAR=changed"})
	if got := strings.TrimSpace(text(call(t, env, &Bash{}, map[string]any{"command": `echo "$HARNESS_TEST_CLIENT_VAR"`}))); got != "changed" {
		t.Fatalf("after change: %q", got)
	}
	m := NewTaskManager(filepath.Join(env.ScratchDir, "tasks"))
	info, err := m.Start(env.Shell, `echo "bg=$HARNESS_TEST_CLIENT_VAR"`, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	done, _ := m.Done(info.ID)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("background task did not finish")
	}
	if data, _ := os.ReadFile(info.OutputPath); strings.TrimSpace(string(data)) != "bg=changed" {
		t.Fatalf("background output %q", data)
	}
}

func TestSetAdditionalDirs(t *testing.T) {
	env := newEnv(t)
	extra, _ := filepath.EvalSymlinks(t.TempDir())
	if r := call(t, env, &Bash{}, map[string]any{"command": "cd " + shellQuote(extra)}); !strings.Contains(text(r), "reset") {
		t.Fatalf("cd outside the working directories must reset: %q", text(r))
	}
	env.SetAdditionalDirs([]string{extra})
	if r := call(t, env, &Bash{}, map[string]any{"command": "cd " + shellQuote(extra)}); strings.Contains(text(r), "reset") || env.Shell.Cwd() != extra {
		t.Fatalf("cd into an added directory: %q (cwd %s)", text(r), env.Shell.Cwd())
	}
	if got := env.WorkingDirs(); len(got) != 2 || got[1] != extra {
		t.Fatalf("working dirs %v", got)
	}
}
