package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The command line stays as it was: a dry run needs no server and writes nothing.
func TestImportCommandDryRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HARNESS_HOME", filepath.Join(home, ".harness"))
	claude := filepath.Join(home, ".claude")
	session := filepath.Join(claude, "projects", "-w", "0b9f6c1e-4a2d-4f7e-9c3b-1d2e3f4a5b6c.jsonl")
	if err := os.MkdirAll(filepath.Dir(session), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session, []byte(`{"type":"user","cwd":"/w"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, "CLAUDE.md"), []byte("# Me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := runImport([]string{"claude", "--dry-run", "--sessions", "--claude-dir", claude, "--claude-json", filepath.Join(home, ".claude.json")}); got != 0 {
		t.Fatalf("exit %d", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".harness")); !os.IsNotExist(err) {
		t.Fatal("a dry run wrote")
	}
	if got := runImport([]string{"other"}); got != 2 {
		t.Fatalf("usage: exit %d", got)
	}
}
