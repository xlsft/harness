//go:build darwin

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/documents"
)

// fakeSofficeMarking stands in for LibreOffice and leaves a mark when something starts it.
func fakeSofficeMarking(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	mark := filepath.Join(dir, "started")
	p := filepath.Join(dir, "soffice")
	script := "#!/bin/sh\ntouch " + shellQuote(mark) + "\nwhile [ $# -gt 0 ]; do case \"$1\" in --outdir) out=\"$2\"; shift;; esac; shift; done\nprintf '%%PDF-1.4 fake' > \"$out/document.pdf\"\n"
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	old := documents.FindSoffice
	documents.FindSoffice = func() string { return p }
	t.Cleanup(func() { documents.FindSoffice = old })
	return p, mark
}

// LibreOffice aborts under Seatbelt, leaving crash reports, so with the sandbox active it must not even be started.
func TestEngineIsNotStartedUnderSeatbelt(t *testing.T) {
	_, mark := fakeSofficeMarking(t)
	env, _, _ := sandboxedEnv(t)
	env.SessionID = t.Name()
	p := filepath.Join(env.CurrentDir(), "a.docx")
	data, err := documents.Create(p, documents.CreateSpec{Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	r := call(t, env, &DocumentRead{}, map[string]any{"file_path": p, "operation": "render"})
	if !r.IsError || !strings.Contains(resultText(r), "not available") || !strings.Contains(resultText(r), "run_outside_sandbox") {
		t.Fatalf("%s", resultText(r))
	}
	r = call(t, env, &DocumentRead{}, map[string]any{"file_path": p, "operation": "check", "use_engine": true})
	if r.IsError || !strings.Contains(resultText(r), "Could not verify") {
		t.Fatalf("no engine must not read as a pass: %s", resultText(r))
	}
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("the engine was started under the sandbox")
	}
}

// Leaving the sandbox goes through the permission check as a command, and only then starts the engine, unconfined.
func TestEngineOutsideSandboxAsksAndRuns(t *testing.T) {
	_, mark := fakeSofficeMarking(t)
	env, _, _ := sandboxedEnv(t)
	input := func(m map[string]any) Call {
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return Call{Input: raw}
	}
	p := filepath.Join(env.CurrentDir(), "a.docx")
	h := (&DocumentRead{}).PermissionHints(context.Background(), env, input(map[string]any{"file_path": p, "operation": "render", "run_outside_sandbox": true}))
	if h.CheckAs != "Bash" || !h.OutsideSandbox || !strings.Contains(h.Command, "soffice") {
		t.Fatalf("%+v", h)
	}
	for _, m := range []map[string]any{
		{"file_path": p, "operation": "render"},
		{"file_path": p, "operation": "read", "run_outside_sandbox": true},
		{"file_path": filepath.Join(env.CurrentDir(), "a.pdf"), "operation": "render", "run_outside_sandbox": true},
	} {
		if h := (&DocumentRead{}).PermissionHints(context.Background(), env, input(m)); h.OutsideSandbox {
			t.Errorf("%v must not ask: %+v", m, h)
		}
	}
	if err := os.MkdirAll(env.ScratchDir, 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := documentEngine(env, true).ToPDF(context.Background(), "a.docx", []byte("d"))
	if err != nil || !strings.HasPrefix(string(out), "%PDF") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := os.Stat(mark); err != nil {
		t.Error("the engine did not run")
	}
}
