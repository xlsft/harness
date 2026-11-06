package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xlsft/harness/packages/client/settings"
)

func inFolder(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	t.Setenv("HARNESS_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(dir, ".harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".harness", "settings.local.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	prevRoot := settings.ManagedRoot
	settings.ManagedRoot = t.TempDir()
	settings.ResetDevicePolicyCache()
	t.Cleanup(func() { settings.ManagedRoot = prevRoot; settings.ResetDevicePolicyCache() })
}

func TestPermissionsCheck(t *testing.T) {
	inFolder(t, `{"permissions":{"allow":["Bash(go test *)"],"deny":["Bash(rm *)"]},"permissionProfiles":{"open":{"allow":["Bash(make *)"]}}}`)
	ctx := context.Background()
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"check", "Bash", "--command", "go test ./..."}, 0},
		{[]string{"check", "Bash", "--command", "rm -rf x"}, 1},
		{[]string{"check", "Bash", "--command", "make build"}, 3},
		{[]string{"check", "Bash", "--command", "make build", "--permission-profile", "open"}, 0},
		{[]string{"check", "Bash", "--command", "make build", "--mode", "dontAsk"}, 3},
		{[]string{"check", "Bash", "--command", "make build", "--mode", "nonsense"}, 1},
		{[]string{"check"}, 2},
		{nil, 2},
	}
	for _, c := range cases {
		if got := runPermissions(ctx, c.args); got != c.want {
			t.Errorf("%v: exit %d, want %d", c.args, got, c.want)
		}
	}
}

func TestConfigCommands(t *testing.T) {
	inFolder(t, `{"model":"m1"}`)
	ctx := context.Background()
	for args, want := range map[string]int{"debug": 0, "features": 0, "nonsense": 2} {
		if got := runConfig(ctx, []string{args}); got != want {
			t.Errorf("%s: exit %d", args, got)
		}
	}
	if got := runConfig(ctx, nil); got != 0 {
		t.Errorf("no verb: exit %d", got)
	}
}

func TestStartupSwitches(t *testing.T) {
	names := []string{"HARNESS_NO_HOOKS", "HARNESS_NO_MCP", "HARNESS_NO_PLUGINS", "HARNESS_NO_SKILLS", "HARNESS_NO_INSTRUCTIONS"}
	for _, n := range names {
		t.Setenv(n, "")
	}
	applyStartup(false, false)
	if os.Getenv("HARNESS_NO_HOOKS") != "" {
		t.Fatal("no flag, no switch")
	}
	applyStartup(false, true)
	if os.Getenv("HARNESS_NO_HOOKS") != "1" || os.Getenv("HARNESS_NO_SKILLS") != "1" || os.Getenv("HARNESS_NO_INSTRUCTIONS") == "1" {
		t.Fatal("safe mode")
	}
	applyStartup(true, false)
	if os.Getenv("HARNESS_NO_INSTRUCTIONS") != "1" {
		t.Fatal("bare")
	}
}
