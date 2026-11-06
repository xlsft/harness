//go:build windows

package tools

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// These run the real PowerShell, on CI's windows-latest (client.yml).

func TestPowerShellOnWindows(t *testing.T) {
	if powerShellPath() == "" {
		t.Skip("no PowerShell here")
	}
	if (&PowerShell{}).HiddenFromModel() {
		t.Fatal("hidden on Windows")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := NewEnv(root, filepath.Join(root, "scratch"))
	run := func(command string) Result {
		t.Helper()
		return call(t, env, &PowerShell{}, map[string]any{"command": command, "description": "test command"})
	}
	if r := run(`Write-Output "héllo $(1+1)"`); r.IsError || !strings.Contains(r.Content[0].Text, "héllo 2") {
		t.Fatalf("%+v", r)
	}
	if r := run(`cmd /c "exit 4"`); !r.IsError || !strings.Contains(r.Content[0].Text, "Exit code 4") {
		t.Fatalf("%+v", r)
	}
	if r := run(`throw "boom"`); !r.IsError || !strings.Contains(r.Content[0].Text, "boom") {
		t.Fatalf("%+v", r)
	}
	if r := run(`Get-Item C:\no\such\path -ErrorAction Stop`); !r.IsError {
		t.Fatalf("%+v", r)
	}
	if r := run(`New-Item -ItemType Directory sub | Out-Null; Set-Location sub`); r.IsError {
		t.Fatalf("%+v", r)
	}
	if got := env.Shell.Cwd(); !strings.EqualFold(got, filepath.Join(root, "sub")) {
		t.Fatalf("cwd %s", got)
	}
	if r := run(`(Get-Location).Path`); !strings.Contains(strings.ToLower(r.Content[0].Text), strings.ToLower(filepath.Join(root, "sub"))) {
		t.Fatalf("%+v", r)
	}
	// The harness's variables stay its own.
	if r := run(`if ($env:__HARNESS_CMD) { "leaked" } else { "clean" }`); !strings.Contains(r.Content[0].Text, "clean") {
		t.Fatalf("%+v", r)
	}
	input, _ := json.Marshal(map[string]string{"command": "x"})
	if (&PowerShell{}).ReadOnly(input) {
		t.Fatal("read-only")
	}
}
