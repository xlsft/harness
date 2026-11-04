package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func bashRun(t *testing.T, env *Env, command string) (string, map[string]any) {
	t.Helper()
	r := call(t, env, &Bash{}, map[string]any{"description": "test", "command": command})
	sm, _ := r.Structured.(map[string]any)
	return text(r), sm
}

func TestBashCwdNote(t *testing.T) {
	env := newEnv(t)
	sub := filepath.Join(env.Cwd, "sub")
	if err := os.MkdirAll(filepath.Join(sub, "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside, _ := filepath.EvalSymlinks(t.TempDir())

	for _, cmd := range []string{"echo hi", "true", "cd sub && cd ..", "cd sub; pwd; cd -", "(cd sub && pwd)", "cd sub && cd deep && cd ../.."} {
		if out, sm := bashRun(t, env, cmd); strings.Contains(out, "Shell") || sm["cwdNote"] != nil {
			t.Errorf("%q: no note expected: %q", cmd, out)
		}
		if env.Shell.Cwd() != env.Cwd {
			t.Fatalf("%q moved the shell to %s", cmd, env.Shell.Cwd())
		}
	}

	want := "Shell now stays in " + sub + "."
	out, sm := bashRun(t, env, "cd sub && echo there")
	if !strings.HasSuffix(out, "there\n"+want) || sm["cwdNote"] != want || env.Shell.Cwd() != sub {
		t.Fatalf("moved: %q %+v", out, sm)
	}
	if out, _ := bashRun(t, env, "pwd"); out != sub {
		t.Fatalf("a note must not repeat while the shell stays: %q", out)
	}

	// A silent command: the note is the whole result.
	if out, _ := bashRun(t, env, "cd deep"); out != "Shell now stays in "+filepath.Join(sub, "deep")+"." {
		t.Fatalf("silent: %q", out)
	}
	if out, _ := bashRun(t, env, "cd "+shellQuote(env.Cwd)); out != "Shell now stays in "+env.Cwd+"." {
		t.Fatalf("back to the root: %q", out)
	}

	// Leaving the working directories, from the root and from a subdirectory.
	want = "Shell cwd was reset to " + env.Cwd + ": " + outside + " is outside the working directories."
	out, sm = bashRun(t, env, "cd "+shellQuote(outside)+" && echo there")
	if !strings.HasSuffix(out, "there\n"+want) || sm["cwdNote"] != want || env.Shell.Cwd() != env.Cwd {
		t.Fatalf("outside: %q %+v", out, sm)
	}
	bashRun(t, env, "cd sub")
	if out, _ := bashRun(t, env, "cd "+shellQuote(outside)); out != want || env.Shell.Cwd() != env.Cwd {
		t.Fatalf("outside from sub: %q", out)
	}
}

// A symlinked start directory is the same directory as its resolved form.
func TestBashCwdNoteThroughSymlink(t *testing.T) {
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	real, _ := filepath.EvalSymlinks(t.TempDir())
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	env := NewEnv(link, filepath.Join(t.TempDir(), "scratch"))
	if out, sm := bashRun(t, env, "echo hi"); out != "hi" || sm["cwdNote"] != nil {
		t.Fatalf("%q %+v", out, sm)
	}
}

func TestBashCwdNoteFailedCommand(t *testing.T) {
	env := newEnv(t)
	sub := filepath.Join(env.Cwd, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// The cd happened: the command failing does not undo it, and the note closes the result.
	out, sm := bashRun(t, env, "cd sub && echo boom >&2; false")
	if !strings.HasSuffix(out, "boom\nExit code 1\nShell now stays in "+sub+".") || sm["cwdNote"] == nil || env.Shell.Cwd() != sub {
		t.Fatalf("failed: %q %+v", out, sm)
	}
	// exit, too, runs the trap.
	if out, _ := bashRun(t, env, "cd .. && exit 3"); !strings.HasSuffix(out, "Exit code 3\nShell now stays in "+env.Cwd+".") {
		t.Fatalf("exit: %q", out)
	}
	// A failed cd stays where it was.
	if out, sm := bashRun(t, env, "cd /nonexistent-dir-of-a-test"); strings.Contains(out, "Shell") || sm["cwdNote"] != nil {
		t.Fatalf("failed cd: %q", out)
	}
}

func TestBashCwdStoppedCommandStays(t *testing.T) {
	env := newEnv(t)
	if err := os.Mkdir(filepath.Join(env.Cwd, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := call(t, env, &Bash{}, map[string]any{"description": "test", "command": "cd sub && sleep 30", "timeout": 100})
	if !r.IsError || strings.Contains(text(r), "Shell") || env.Shell.Cwd() != env.Cwd {
		t.Fatalf("timed out: %q cwd %s", text(r), env.Shell.Cwd())
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	raw, _ := json.Marshal(map[string]any{"description": "test", "command": "cd sub && sleep 30"})
	r = (&Bash{}).Run(ctx, env, Call{ID: "t", Input: raw})
	if !r.IsError || strings.Contains(text(r), "Shell") || env.Shell.Cwd() != env.Cwd {
		t.Fatalf("interrupted: %q cwd %s", text(r), env.Shell.Cwd())
	}
}

func TestBashCwdBackgroundKeepsDirectory(t *testing.T) {
	env := newEnv(t)
	env.Tasks = NewTaskManager(filepath.Join(env.ScratchDir, "tasks"))
	defer env.Tasks.StopAll(TaskSessionClosed)
	if err := os.Mkdir(filepath.Join(env.Cwd, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := call(t, env, &Bash{}, map[string]any{"description": "test", "command": "cd sub && sleep 5", "run_in_background": true})
	if !strings.Contains(text(r), "Session cwd remains "+env.Cwd) || env.Shell.Cwd() != env.Cwd {
		t.Fatalf("run_in_background: %q", text(r))
	}
	r = call(t, env, &Bash{}, map[string]any{"description": "test", "command": "sleep 5", "run_in_background": true})
	if strings.Contains(text(r), "Session cwd") {
		t.Fatalf("no directory change, no note: %q", text(r))
	}
	// A foreground command that a `&` inside leaves behind does not move the shell either.
	if out, sm := bashRun(t, env, "(cd sub && sleep 0.05) & wait"); strings.Contains(out, "Shell") || sm["cwdNote"] != nil {
		t.Fatalf("subshell: %q", out)
	}
}

func TestBashCwdDirectoryGone(t *testing.T) {
	env := newEnv(t)
	sub := filepath.Join(env.Cwd, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	bashRun(t, env, "cd sub")
	if err := os.Remove(sub); err != nil {
		t.Fatal(err)
	}
	out, _ := bashRun(t, env, "pwd")
	if want := env.Cwd + "\nShell cwd was reset to " + env.Cwd + ": " + sub + " no longer exists."; out != want {
		t.Fatalf("got %q want %q", out, want)
	}
	if out, _ := bashRun(t, env, "pwd"); out != env.Cwd {
		t.Fatalf("said once: %q", out)
	}
}
