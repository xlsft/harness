package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWhatAScriptHoldsIsNoTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	file, err := os.Create(filepath.Join(t.TempDir(), "answers"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for name, f := range map[string]*os.File{"a pipe": r, "the null device": devNull, "a file": file} {
		if isTerminal(f) {
			t.Errorf("%s counts as a terminal", name)
		}
	}
}

func TestApproveWithoutATerminalIsRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HARNESS_HOME", home)
	stdin := os.Stdin
	t.Cleanup(func() { os.Stdin = stdin })
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	os.Stdin = devNull
	if code := runPlugin([]string{"approve", "nothing@local"}); code == 0 {
		t.Fatal("approved without a terminal")
	}
}
