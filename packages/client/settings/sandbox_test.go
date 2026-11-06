package settings

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

type sandboxDocs []*syncv1.Document

func (sandboxDocs) Synced() bool { return true }
func (d sandboxDocs) List(kind syncv1.DocumentKind) []*syncv1.Document {
	var out []*syncv1.Document
	for _, x := range d {
		if x.GetKind() == kind {
			out = append(out, x)
		}
	}
	return out
}

func sandboxDoc(id string, scope commonv1.Scope, kind syncv1.DocumentKind, content string) *syncv1.Document {
	return &syncv1.Document{Id: id, Scope: scope, Kind: kind, Key: "settings", Content: content}
}

func userDoc(content string) *syncv1.Document {
	return sandboxDoc("doc_user", commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, content)
}

func policyDoc(content string) *syncv1.Document {
	return sandboxDoc("doc_policy", commonv1.Scope_SCOPE_ORG, syncv1.DocumentKind_DOCUMENT_KIND_POLICY, content)
}

// loadSandbox loads with files as cwd-relative settings files.
func loadSandbox(t *testing.T, files map[string]string, docs ...*syncv1.Document) *Settings {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := Load(context.Background(), Options{Cwd: dir, Home: t.TempDir(), Docs: sandboxDocs(docs), Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSandboxDefaultsOn(t *testing.T) {
	s := loadSandbox(t, nil).Sandbox
	if !s.Enabled || !s.AllowLocalhost || s.Forced || s.AutoAllowBash || s.OutsideWithoutPrompt || s.OutsideRefused {
		t.Fatalf("defaults: %+v", s)
	}
}

func TestSandboxUserTurnsOff(t *testing.T) {
	s := loadSandbox(t, nil, userDoc(`{"sandbox":{"enabled":false,"allowedHosts":["example.com"],"allowedWritePaths":["~/x","/y","relative"]}}`))
	if s.Sandbox.Enabled || !slices.Equal(s.Sandbox.AllowedHosts, []string{"example.com"}) || !slices.Equal(s.Sandbox.AllowedWritePaths, []string{"~/x", "/y"}) {
		t.Fatalf("user: %+v", s.Sandbox)
	}
	if len(s.Problems) != 1 {
		t.Fatalf("a relative path should be a problem: %+v", s.Problems)
	}
}

func TestSandboxDeviceOverride(t *testing.T) {
	// The local file is the device's say over the synced document, both ways.
	s := loadSandbox(t, map[string]string{".harness/settings.local.json": `{"sandbox":{"enabled":true,"allowedHosts":["b.test"]}}`},
		userDoc(`{"sandbox":{"enabled":false,"allowedHosts":["a.test"]}}`)).Sandbox
	if !s.Enabled || !slices.Contains(s.AllowedHosts, "a.test") || !slices.Contains(s.AllowedHosts, "b.test") {
		t.Fatalf("on: %+v", s)
	}
	s = loadSandbox(t, map[string]string{".harness/settings.local.json": `{"sandbox":{"enabled":false}}`}, userDoc(`{"sandbox":{"enabled":true}}`)).Sandbox
	if s.Enabled {
		t.Fatalf("off: %+v", s)
	}
}

func TestSandboxProjectCannotWiden(t *testing.T) {
	st := loadSandbox(t, map[string]string{".harness/settings.json": `{"sandbox":{"enabled":false,"allowLocalhost":true,"autoAllowBash":true,"outsideWithoutPrompt":true,"allowedHosts":["evil.test"],"allowedWritePaths":["~/.ssh"]}}`})
	s := st.Sandbox
	if !s.Enabled || s.AutoAllowBash || s.OutsideWithoutPrompt || len(s.AllowedHosts)+len(s.AllowedWritePaths) != 0 {
		t.Fatalf("a committed file widened the sandbox: %+v", s)
	}
	closed := loadSandbox(t, map[string]string{".harness/settings.json": `{"sandbox":{"allowLocalhost":false}}`}).Sandbox
	if closed.AllowLocalhost {
		t.Fatalf("a committed file could not close localhost: %+v", closed)
	}
	if len(st.Ignored) == 0 {
		t.Fatal("the ignored values should be listed")
	}
	// It can turn it on over a user who turned it off.
	s = loadSandbox(t, map[string]string{".harness/settings.json": `{"sandbox":{"enabled":true}}`}, userDoc(`{"sandbox":{"enabled":false}}`)).Sandbox
	if !s.Enabled {
		t.Fatalf("project on: %+v", s)
	}
}

func TestSandboxPolicyForces(t *testing.T) {
	s := loadSandbox(t, map[string]string{".harness/settings.local.json": `{"sandbox":{"enabled":false}}`},
		userDoc(`{"sandbox":{"enabled":false}}`), policyDoc(`{"sandbox":{"enabled":true,"allowOutside":false}}`)).Sandbox
	if !s.Enabled || !s.Forced || !s.OutsideRefused {
		t.Fatalf("policy: %+v", s)
	}
	// A policy cannot turn it off.
	st := loadSandbox(t, nil, policyDoc(`{"sandbox":{"enabled":false}}`))
	if !st.Sandbox.Enabled || st.Sandbox.Forced || len(st.Ignored) == 0 {
		t.Fatalf("policy off: %+v", st.Sandbox)
	}
}

func TestPolicySandbox(t *testing.T) {
	if r, o := PolicySandbox([]*syncv1.Document{policyDoc(`{"sandbox":{"enabled":true,"allowOutside":false}}`)}); !r || !o {
		t.Fatal(r, o)
	}
	if r, o := PolicySandbox([]*syncv1.Document{policyDoc(`{"sandbox":{"enabled":false}}`), policyDoc(`nope`)}); r || o {
		t.Fatal(r, o)
	}
}
