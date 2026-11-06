//go:build darwin

package terminal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/sandbox"
)

// TerminalRun's tab runs its whole shell in the sandbox; TerminalOpen's is the user's.
func TestToolRunInTheSandbox(t *testing.T) {
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("no sandbox-exec")
	}
	r := newRig(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(root, "home")
	r.env.Sandbox = &sandbox.Session{Home: home, HarnessHome: filepath.Join(home, ".harness"), Settings: func() sandbox.Settings { return sandbox.Settings{Enabled: true} }, Getenv: func(string) string { return "" }}
	t.Cleanup(r.env.Sandbox.Close)
	outside := filepath.Join(root, "from-tab")

	res := r.call(ToolRun, map[string]any{"command": "echo in-tab-$HARNESS_SANDBOX; echo x > " + outside, "title": "sandboxed"})
	if text := resultText(res); !strings.Contains(text, "in-tab-1") || !strings.Contains(strings.ToLower(text), "not permitted") {
		t.Fatalf("result: %s", text)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("the tab wrote outside")
	}

	// Approved to run outside.
	res = r.call(ToolRun, map[string]any{"command": "echo tab-[$HARNESS_SANDBOX]; echo x > " + outside, "run_outside_sandbox": true})
	if text := resultText(res); !strings.Contains(text, "tab-[]") {
		t.Fatalf("outside: %s", text)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("not written:", err)
	}

	// A tab opened for the user: not confined.
	res = r.call(ToolOpen, map[string]any{"title": "yours"})
	term := r.m.Get(tabOf(t, res))
	if term == nil || !term.Info().Running {
		t.Fatalf("tab: %+v", term)
	}
}
