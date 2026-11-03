package settings

import (
	"context"
	"path/filepath"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

func TestRemoteControlPolicyOff(t *testing.T) {
	root := t.TempDir()
	cwd, home := filepath.Join(root, "p"), filepath.Join(root, "h")
	load := func(docs ...*syncv1.Document) *Settings {
		t.Helper()
		s, err := Load(context.Background(), Options{Cwd: cwd, Home: home, Docs: &fakeDocs{synced: true, docs: docs}, Trusted: true})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	// A user document only sets the default.
	if s := load(doc("u", commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, UserSettingsKey, `{"remoteControl": false}`)); s.RemoteControlPolicyOff {
		t.Fatal("a user setting must not force remote control off")
	}
	if s := load(doc("p", commonv1.Scope_SCOPE_ORG, syncv1.DocumentKind_DOCUMENT_KIND_POLICY, "default", `{"remoteControl": true}`)); s.RemoteControlPolicyOff {
		t.Fatal("policy true is not off")
	}
	if s := load(doc("p", commonv1.Scope_SCOPE_ORG, syncv1.DocumentKind_DOCUMENT_KIND_POLICY, "default", `{"remoteControl": false}`)); !s.RemoteControlPolicyOff {
		t.Fatal("policy false must force remote control off")
	}
}
