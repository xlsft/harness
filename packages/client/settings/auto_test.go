package settings

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

func TestAutoModeGuards(t *testing.T) {
	load := func(t *testing.T, project, local, user string, cli CLI, docs DocSource) *Settings {
		t.Helper()
		root := t.TempDir()
		cwd, home := filepath.Join(root, "p"), filepath.Join(root, "h")
		if project != "" {
			writeJSON(t, filepath.Join(cwd, ".claude", "settings.json"), project)
		}
		if local != "" {
			writeJSON(t, filepath.Join(cwd, ".claude", "settings.local.json"), local)
		}
		if user != "" {
			writeJSON(t, filepath.Join(home, ".claude", "settings.json"), user)
		}
		s, err := Load(context.Background(), Options{Cwd: cwd, Home: home, Docs: docs, Trusted: true, CLI: cli})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	auto := `{"permissions": {"defaultMode": "auto"}}`
	policy := func(content string) DocSource {
		return &fakeDocs{docs: []*syncv1.Document{doc("p", commonv1.Scope_SCOPE_ORG, syncv1.DocumentKind_DOCUMENT_KIND_POLICY, "default", content)}}
	}

	// Project and local files cannot turn auto on, even in a trusted workspace.
	for name, s := range map[string]*Settings{
		"project": load(t, auto, "", "", CLI{}, nil),
		"local":   load(t, "", auto, "", CLI{}, nil),
	} {
		if s.Permissions.DefaultMode == "auto" {
			t.Errorf("%s settings enabled auto mode", name)
		}
		if len(s.Ignored) == 0 || !strings.Contains(s.Ignored[0].Message, "auto mode can only be enabled") {
			t.Errorf("%s: ignored %v", name, s.Ignored)
		}
	}
	// A lower layer still applies when the project one is ignored.
	if s := load(t, auto, "", `{"permissions": {"defaultMode": "acceptEdits"}}`, CLI{}, nil); s.Permissions.DefaultMode != "acceptEdits" {
		t.Errorf("user default under ignored project auto: %q", s.Permissions.DefaultMode)
	}
	// User settings, the CLI and policy can.
	if s := load(t, "", "", auto, CLI{}, nil); s.Permissions.DefaultMode != "auto" {
		t.Errorf("user auto: %q", s.Permissions.DefaultMode)
	}
	if s := load(t, "", "", "", CLI{PermissionMode: "auto"}, nil); s.Permissions.DefaultMode != "auto" {
		t.Errorf("cli auto: %q", s.Permissions.DefaultMode)
	}
	if s := load(t, "", "", "", CLI{}, policy(auto)); s.Permissions.DefaultMode != "auto" {
		t.Errorf("policy auto: %q", s.Permissions.DefaultMode)
	}

	// disableAutoMode (Claude Code form under permissions, or top level) wins over every enabler.
	s := load(t, "", "", auto, CLI{PermissionMode: "auto"}, policy(`{"permissions": {"disableAutoMode": "disable"}}`))
	if s.Permissions.DefaultMode != "default" || !s.PermissionInputs().DisableAutoMode {
		t.Errorf("policy disableAutoMode: mode %q disabled %v", s.Permissions.DefaultMode, s.PermissionInputs().DisableAutoMode)
	}
	if s := load(t, "", "", `{"disableAutoMode": true, "permissions": {"defaultMode": "auto"}}`, CLI{}, nil); s.Permissions.DefaultMode != "default" || !s.Permissions.DisableAutoMode {
		t.Errorf("user top-level disableAutoMode: %q %v", s.Permissions.DefaultMode, s.Permissions.DisableAutoMode)
	}
	if s := load(t, `{"permissions": {"disableAutoMode": "disable"}}`, "", "", CLI{}, nil); !s.Permissions.DisableAutoMode {
		t.Error("a project file may turn auto mode off")
	}
	if s := load(t, "", "", `{"permissions": {"disableAutoMode": "enable"}}`, CLI{}, nil); s.Permissions.DisableAutoMode {
		t.Error("only \"disable\" or true disables")
	}
}
