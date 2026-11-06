package settings

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

func TestPolicyDisabledModes(t *testing.T) {
	pol := func(key, content string) *syncv1.Document {
		return doc("p-"+key, commonv1.Scope_SCOPE_ORG, syncv1.DocumentKind_DOCUMENT_KIND_POLICY, key, content)
	}
	docs := []*syncv1.Document{
		pol("default", `{"permissions": {"disabledModes": ["dontAsk", "default", "bogus"]}}`),
		pol("admins", `{"disableAutoMode": "disable", "permissions": {"disableBypassPermissionsMode": "disable"}}`),
		doc("u", commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_POLICY, "default", `{"permissions": {"disabledModes": ["plan"]}}`),
		pol("broken", `not json`),
	}
	if got := PolicyDisabledModes(docs); !slices.Equal(got, []string{"auto", "bypassPermissions", "dontAsk"}) {
		t.Fatalf("got %v", got)
	}

	// Session settings: policy disabledModes apply, a disabled defaultMode falls back to default.
	root := t.TempDir()
	cwd, home := filepath.Join(root, "p"), filepath.Join(root, "h")
	writeJSON(t, filepath.Join(cwd, ".harness", "settings.local.json"), `{"permissions": {"defaultMode": "acceptEdits", "disabledModes": ["plan"]}}`)
	s, err := Load(context.Background(), Options{Cwd: cwd, Home: home, Trusted: true,
		Docs: &fakeDocs{docs: []*syncv1.Document{pol("default", `{"permissions": {"disabledModes": ["acceptEdits", "default"]}}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.PermissionInputs().DisabledModes, []string{"acceptEdits"}) {
		t.Fatalf("disabled %v (only policy may disable modes)", s.PermissionInputs().DisabledModes)
	}
	if s.Permissions.DefaultMode != "default" || len(s.Problems) == 0 {
		t.Fatalf("mode %q problems %v", s.Permissions.DefaultMode, s.Problems)
	}
}
