package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

func TestPolicySchemaParses(t *testing.T) {
	s := Schema()
	if s.Harness.Domain != "com.xlsft.harness" || s.Harness.RegistryJSONValue != "Settings" {
		t.Fatalf("x-harness %+v", s.Harness)
	}
	if !strings.HasSuffix(macPreferencesPath, "/"+s.Harness.Domain+".plist") {
		t.Fatalf("the plist the client reads (%s) is not of the schema's domain %s", macPreferencesPath, s.Harness.Domain)
	}
	if `HKLM\`+s.Harness.RegistryKey != windowsPolicyKey {
		t.Fatalf("the registry key the client reads (%s) is not the schema's %s", windowsPolicyKey, s.Harness.RegistryKey)
	}
	for _, k := range s.Keys() {
		if k.Title == "" || k.Description == "" || k.Type == "" {
			t.Errorf("%s: title, description and type are needed by the templates", k.Path)
		}
		if k.Harness.Scope != "machine" && k.Harness.Scope != "policy" {
			t.Errorf("%s: scope %q", k.Path, k.Harness.Scope)
		}
		if len(k.Examples) == 0 {
			t.Errorf("%s: no example", k.Path)
		}
		if k.Harness.Scope == "machine" && strings.Contains(k.Path, ".") {
			t.Errorf("%s: machine keys are top-level", k.Path)
		}
	}
	if s.Lookup("sandbox.enabled") == nil || s.Lookup("sandbox") != nil || s.Lookup("nope") != nil {
		t.Fatal("Lookup")
	}
}

// Every key of the schema is one the client reads: a quoted name in its sources. A key nobody reads would be a template
// that does nothing.
func TestPolicySchemaNamesOnlyKeysTheClientReads(t *testing.T) {
	var sources bytes.Buffer
	root := ".."
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "gen" || d.Name() == "testdata" || d.Name() == "policytemplates") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		sources.Write(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var walk func(prefix string, props map[string]*SchemaProperty)
	walk = func(prefix string, props map[string]*SchemaProperty) {
		for name, p := range props {
			if !bytes.Contains(sources.Bytes(), []byte(`"`+name+`"`)) {
				t.Errorf("%s%s: no Go source of the client reads this key", prefix, name)
			}
			if p.Type == "object" && prefix == "" {
				walk(name+".", p.Properties)
			}
		}
	}
	walk("", Schema().Properties)
}

// The examples of the policy keys, all in one POLICY document, load without a problem: the schema says what the loader
// accepts.
func TestPolicySchemaExamplesLoad(t *testing.T) {
	policy := map[string]any{}
	for _, k := range Schema().Keys() {
		if k.Harness.Scope != "policy" {
			continue
		}
		var v any
		if err := json.Unmarshal(k.Examples[0], &v); err != nil {
			t.Fatalf("%s: %v", k.Path, err)
		}
		setPath(policy, strings.Split(k.Path, "."), v)
	}
	content, _ := json.Marshal(policy)
	root := t.TempDir()
	s, err := Load(context.Background(), Options{Cwd: filepath.Join(root, "p"), Home: filepath.Join(root, "h"), Trusted: true,
		Docs: &fakeDocs{docs: []*syncv1.Document{doc("p", commonv1.Scope_SCOPE_ORG, syncv1.DocumentKind_DOCUMENT_KIND_POLICY, "default", string(content))}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range s.Problems {
		t.Errorf("example policy: %v", p)
	}
	if !s.RemoteControlPolicyOff || s.Extras.WebSearchMode != WebSearchCached {
		t.Fatalf("the examples did not apply: %s", content)
	}
}

func TestParseRegQuerySingleValues(t *testing.T) {
	out := "\r\nHKEY_LOCAL_MACHINE\\SOFTWARE\\Policies\\xlsft\\harness\r\n" +
		"    Settings    REG_SZ    {\"permissions\":{\"deny\":[\"WebFetch\"]},\"disableUpdates\":false}\r\n" +
		"    serverUrl    REG_SZ    https://harness.example.com\r\n" +
		"    disableUpdates    REG_DWORD    0x1\r\n" +
		"    sandbox.enabled    REG_DWORD    0x0\r\n" +
		"    permissions.defaultMode    REG_SZ    plan\r\n" +
		"    unknownThing    REG_SZ    x\r\n" +
		"    disableAllHooks    REG_SZ    yes\r\n" +
		"\r\nHKEY_LOCAL_MACHINE\\SOFTWARE\\Policies\\xlsft\\harness\\sub\r\n"
	got, ok := parseRegQuery(out)
	if !ok {
		t.Fatal("nothing read")
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	want := `{"disableUpdates":true,"permissions":{"defaultMode":"plan","deny":["WebFetch"]},"sandbox":{"enabled":false},"serverUrl":"https://harness.example.com"}`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got, ok := parseRegQuery("    serverUrl    REG_SZ    https://h\r\n"); !ok || string(got) != `{"serverUrl":"https://h"}` {
		t.Fatalf("only a single value: %s %v", got, ok)
	}
	if got, ok := parseRegQuery("    Settings    REG_SZ    {broken\r\n"); !ok || string(got) != `{broken` {
		t.Fatalf("a broken Settings value is passed on for the loader to report: %s %v", got, ok)
	}
}

func TestMachineConfig(t *testing.T) {
	m := machineFrom([]DevicePolicy{
		{Origin: "mdm", Content: []byte(`{"serverUrl":" https://a.example.com/ ","disableUpdates":false}`)},
		{Origin: "file", Content: []byte(`not json`)},
		{Origin: "drop-in", Content: []byte(`{"serverUrl":"https://b.example.com","updateMirrorUrl":"https://m.example.com/h/","disableUpdates":true}`)},
		{Origin: "later", Content: []byte(`{"disableUpdates":false}`)},
	})
	if m.ServerURL != "https://a.example.com" || m.Origins[KeyServerURL] != "mdm" {
		t.Fatalf("server %+v", m)
	}
	if m.UpdateMirrorURL != "https://m.example.com/h" || !m.DisableUpdates || m.Origins[KeyDisableUpdates] != "drop-in" {
		t.Fatalf("updates %+v", m)
	}
	if m := machineFrom(nil); m.ServerURL != "" || m.Origins != nil {
		t.Fatalf("empty %+v", m)
	}
}

func TestMachineConfigFromTheSystemFolder(t *testing.T) {
	root := deviceRoot(t)
	writeJSONFile(t, filepath.Join(root, "settings.d", "00-server.json"), `{"serverUrl":"https://harness.example.com"}`)
	ResetDevicePolicyCache()
	if m := MachineConfig(); m.ServerURL != "https://harness.example.com" {
		t.Fatalf("%+v", m)
	}
}
