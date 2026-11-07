package clientlog

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriterRotatesAndKeepsAFew(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Path: filepath.Join(dir, "client.log"), MaxBytes: 100, Keep: 2}
	line := strings.Repeat("x", 39) + "\n"
	for i := 0; i < 20; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		st, _ := os.Stat(filepath.Join(dir, e.Name()))
		if st.Size() > 100 {
			t.Fatalf("%s is %d bytes", e.Name(), st.Size())
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", e.Name(), st.Mode().Perm())
		}
	}
	if strings.Join(names, ",") != "client.log,client.log.1,client.log.2" {
		t.Fatalf("files %v", names)
	}
}

func TestNoSecretsOrPromptsInTheLog(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	var file, stderr bytes.Buffer
	log := New(Options{Mode: "serve", Stderr: &stderr, File: &file})
	log.Info("session opened", "session", "s1", "token", "hat_abcdef123456", "prompt", "my secret plan", "context_window", 200000)
	log.Warn("sync failed", "err", errors.New("rpc: Authorization: Bearer hat_zzzzzzzzzzzz refused"), "access_token", "x", "url", "https://h.example/?k=hrt_qqqqqqqqqq")
	got := file.String()
	for _, bad := range []string{"hat_abcdef123456", "my secret plan", "hat_zzzzzzzzzzzz", "hrt_qqqqqqqqqq"} {
		if strings.Contains(got, bad) {
			t.Fatalf("%q in the log:\n%s", bad, got)
		}
	}
	for _, want := range []string{"session opened", "session=s1", "context_window=200000", "mode=serve", "sync failed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing:\n%s", want, got)
		}
	}
	if strings.Contains(stderr.String(), "session opened") || !strings.Contains(stderr.String(), "sync failed") {
		t.Fatalf("stderr keeps to warnings: %s", stderr.String())
	}
}
