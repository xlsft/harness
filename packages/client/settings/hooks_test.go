package settings

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

type hookDocs []*syncv1.Document

func (d hookDocs) Synced() bool { return true }

func (d hookDocs) List(kind syncv1.DocumentKind) []*syncv1.Document {
	var out []*syncv1.Document
	for _, doc := range d {
		if doc.GetKind() == kind {
			out = append(out, doc)
		}
	}
	return out
}

func writeSettings(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const stopHook = `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"a"}]}]}}`

func TestHooksFromEveryProjectFile(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	writeSettings(t, cwd, ".claude/settings.json", stopHook)
	writeSettings(t, cwd, ".claude/settings.local.json", stopHook)
	writeSettings(t, cwd, ".harness/settings.json", stopHook)
	writeSettings(t, cwd, ".harness/settings.local.json", stopHook)
	writeSettings(t, home, ".claude/settings.json", stopHook)

	st, err := Load(context.Background(), Options{Cwd: cwd, Home: home, Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Hooks) != 2 {
		t.Fatalf("layers: %+v", st.Hooks)
	}
	if st.Hooks[0].Source.Level != LevelProject || st.Hooks[len(st.Hooks)-1].Source.Level != LevelLocal {
		t.Fatalf("lowest level first: %+v", st.Hooks)
	}
	for _, l := range st.Hooks {
		if l.Gate != HookGateNone {
			t.Fatalf("trusted: %+v", l)
		}
	}

	st, err = Load(context.Background(), Options{Cwd: cwd, Home: home, Trusted: false})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range st.Hooks {
		want := HookGateWorkspace
		if l.Source.Level == LevelUser {
			want = HookGateNone
		}
		if l.Gate != want {
			t.Fatalf("untrusted: %+v", l)
		}
	}
}

func TestHooksFromTheServerNeedThisDevice(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	writeSettings(t, home, ".claude/settings.json", stopHook) // a synced user's file is not read, as for every other key
	hook := `{"Stop":[{"hooks":[{"type":"command","command":"synced"}]}]}`
	docs := hookDocs{
		{Id: "doc_hook", Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_HOOK, Key: "user", Content: hook},
		{Id: "doc_settings", Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, Key: UserSettingsKey, Content: stopHook},
		{Id: "doc_org", Scope: commonv1.Scope_SCOPE_ORG, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, Key: UserSettingsKey, Content: stopHook},
		{Id: "doc_policy", Scope: commonv1.Scope_SCOPE_ORG, Kind: syncv1.DocumentKind_DOCUMENT_KIND_POLICY, Key: "default", Content: stopHook},
	}
	st, err := Load(context.Background(), Options{Cwd: cwd, Home: home, Docs: docs, Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Hooks) != 4 {
		t.Fatalf("layers: %+v", st.Hooks)
	}
	for _, l := range st.Hooks {
		if l.Gate != HookGateDevice || l.Hash == "" {
			t.Fatalf("%+v", l)
		}
	}
	var fromHookDoc bool
	for _, l := range st.Hooks {
		if l.Source.DocID == "doc_hook" {
			fromHookDoc = l.Hash == ContentHash([]byte(hook))
		}
	}
	if !fromHookDoc {
		t.Fatal("the HOOK document's hash must be of its content")
	}
}

func TestManagedHooksOnly(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	writeSettings(t, cwd, ".harness/settings.json", stopHook)
	policy := `{"allowManagedHooksOnly":true,"hooks":{"Stop":[{"hooks":[{"type":"command","command":"p"}]}]}}`
	docs := hookDocs{{Id: "doc_policy", Scope: commonv1.Scope_SCOPE_ORG, Kind: syncv1.DocumentKind_DOCUMENT_KIND_POLICY, Key: "default", Content: policy}}
	st, err := Load(context.Background(), Options{Cwd: cwd, Home: home, Docs: docs, Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range st.Hooks {
		switch l.Source.Level {
		case LevelPolicy:
			if l.Gate != HookGateDevice {
				t.Fatalf("%+v", l)
			}
		default:
			if l.Gate != HookGateManaged {
				t.Fatalf("%+v", l)
			}
		}
	}
	// Only a policy can say it: a repository cannot lock out the user's hooks.
	writeSettings(t, cwd, ".harness/settings.local.json", `{"allowManagedHooksOnly":true}`)
	st, err = Load(context.Background(), Options{Cwd: cwd, Home: home, Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range st.Hooks {
		if l.Gate == HookGateManaged {
			t.Fatalf("%+v", l)
		}
	}
}

func TestDisableAllHooksOnlyEverTurnsOff(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	user := hookDocs{{Id: "u", Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, Key: UserSettingsKey, Content: `{"disableAllHooks":true}`}}
	writeSettings(t, cwd, ".harness/settings.local.json", `{"disableAllHooks":false}`)
	st, err := Load(context.Background(), Options{Cwd: cwd, Home: home, Docs: user, Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	if !st.HooksOff || st.HooksPolicyOff {
		t.Fatalf("a project file cannot turn back on what the user turned off: %v %v", st.HooksOff, st.HooksPolicyOff)
	}
	docs := hookDocs{{Id: "p", Scope: commonv1.Scope_SCOPE_ORG, Kind: syncv1.DocumentKind_DOCUMENT_KIND_POLICY, Key: "default", Content: `{"disableAllHooks":true}`}}
	st, err = Load(context.Background(), Options{Cwd: cwd, Home: home, Docs: docs, Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	if !st.HooksOff || !st.HooksPolicyOff {
		t.Fatalf("policy: %v %v", st.HooksOff, st.HooksPolicyOff)
	}
}
