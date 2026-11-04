package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A command running while the session moves finishes where it is and does not pull the shell back.
func TestRunningCommandDoesNotUndoAMove(t *testing.T) {
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	first, second, sub := t.TempDir(), t.TempDir(), ""
	sub = filepath.Join(first, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	second, _ = filepath.EvalSymlinks(second)
	env := NewEnv(first, t.TempDir())
	release := filepath.Join(t.TempDir(), "release")
	done := make(chan error, 1)
	go func() {
		_, err := env.Shell.Run(context.Background(), "cd sub && until [ -e '"+release+"' ]; do sleep 0.05; done", ExecOptions{Timeout: 20 * time.Second})
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	env.Relocate(second, nil)
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := env.Shell.Cwd(); got != second {
		t.Fatalf("shell in %q, want %q", got, second)
	}
	if env.CurrentDir() != second || len(env.WorkingDirs()) != 1 {
		t.Fatalf("env %q %v", env.CurrentDir(), env.WorkingDirs())
	}
	// The next command starts there and may cd within the new folder.
	res, err := env.Shell.Exec(context.Background(), "pwd", 5*time.Second)
	if err != nil || res.StartCwd != second {
		t.Fatalf("%v %+v", err, res)
	}
}
