//go:build linux

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func isLinux() bool { return true }

// The real thing, where the tests run as root: the worker has the uid it was given, reads the token from the pipe, and a
// second worker can't read the first one's home.
func TestUIDLauncherSeparatesWorkers(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	script := filepath.Join(t.TempDir(), "fake-worker")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nid -u > \"$HOME/uid\"\ncat <&3 > \"$HOME/token\"\ncat /proc/self/environ | tr '\\0' '\\n' > \"$HOME/env\"\ncat \"$OTHER_FILE\" > \"$HOME/stolen\" 2>/dev/null\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	l := UIDLauncher{Executable: script, ServerURL: "https://h.test", Name: "n"}
	run := func(uid int) string {
		home := filepath.Join(base, "u"+strings.Repeat("x", uid-29999))
		w, err := l.Start(context.Background(), WorkerSpec{UserID: "u", Token: "tok-pipe", UID: uid, GID: uid, Home: home})
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Wait(); err != nil {
			t.Fatalf("worker: %v", err)
		}
		return home
	}
	a := run(30000)
	got, err := os.ReadFile(filepath.Join(a, "uid"))
	if err != nil || strings.TrimSpace(string(got)) != "30000" {
		t.Fatalf("uid %q %v", got, err)
	}
	if tok, _ := os.ReadFile(filepath.Join(a, "token")); string(tok) != "tok-pipe" {
		t.Fatalf("token %q", tok)
	}
	if env, _ := os.ReadFile(filepath.Join(a, "env")); strings.Contains(string(env), "tok-pipe") {
		t.Fatal("the token is in the environment")
	}
	if st, err := os.Stat(a); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("home mode %v %v", st.Mode(), err)
	}
	// Uid 30001 reading uid 30000's file through the shell, for good measure.
	cmd := exec.Command("sh", "-c", "cat "+filepath.Join(a, "token"))
	cmd.SysProcAttr = workerAttr(30001, 30001)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("another worker read it: %s", out)
	}
}
