package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportProjectCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HARNESS_HOME", t.TempDir())
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("CLAUDE.md", "Project text\n")
	write(".claude/commands/go.md", "Run\n")

	if got := runImport([]string{"--project", root, "--dry-run"}); got != 0 {
		t.Fatalf("exit %d", got)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err == nil {
		t.Fatal("a dry run wrote")
	}
	if got := runImport([]string{"--project", root}); got != 0 {
		t.Fatalf("exit %d", got)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".harness", "commands", "go.md")); err != nil {
		t.Fatal(err)
	}
	// Also spelled after the source's name, as the user-level import is.
	if got := runImport([]string{"claude", "--project", root}); got != 0 {
		t.Fatalf("exit %d", got)
	}
	if got := runImport([]string{"--project", filepath.Join(root, "CLAUDE.md")}); got != 1 {
		t.Fatalf("a file is no folder: exit %d", got)
	}
}

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
