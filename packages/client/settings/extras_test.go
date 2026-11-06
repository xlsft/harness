package settings

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeJSONFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func deviceRoot(t *testing.T) string {
	t.Helper()
	root, prevMac, prevWin, prevRoot := t.TempDir(), MacPreferences, WindowsPolicyKey, ManagedRoot
	ManagedRoot, MacPreferences, WindowsPolicyKey = root, "", ""
	ResetDevicePolicyCache()
	t.Cleanup(func() {
		ManagedRoot, MacPreferences, WindowsPolicyKey = prevRoot, prevMac, prevWin
		ResetDevicePolicyCache()
	})
	return root
}

func TestDevicePolicyFile(t *testing.T) {
	root, cwd := deviceRoot(t), t.TempDir()
	writeJSONFile(t, filepath.Join(root, "settings.json"), `{"model":"from-file","permissions":{"deny":["Bash(curl *)"]}}`)
	writeJSONFile(t, filepath.Join(root, "settings.d", "10-extra.json"), `{"permissions":{"deny":["WebFetch"]},"remoteControl":false}`)
	writeJSONFile(t, filepath.Join(cwd, ".harness", "settings.local.json"), `{"model":"from-local"}`)
	ResetDevicePolicyCache()
	s, err := Load(context.Background(), Options{Cwd: cwd, Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.Model != "from-file" {
		t.Fatalf("model %q: the policy file does not win", s.Model)
	}
	var deny []string
	for _, r := range s.Permissions.Deny {
		if r.Source.Level != LevelPolicy {
			t.Errorf("rule %s from level %s", r.Rule, r.Source.Level)
		}
		deny = append(deny, r.Rule)
	}
	if !slices.Contains(deny, "Bash(curl *)") || !slices.Contains(deny, "WebFetch") {
		t.Fatalf("deny %v", deny)
	}
	if !s.RemoteControlPolicyOff {
		t.Fatal("drop-in not read")
	}
	docs := DevicePolicyDocs()
	if len(docs) != 2 || !strings.HasPrefix(docs[0].GetKey(), "device:") {
		t.Fatalf("docs %v", docs)
	}
	if m := PolicyManaged(docs); len(m.DenyRules) != 2 {
		t.Fatalf("managed %+v", m)
	}
}

func TestDevicePolicyBrokenFileIsReported(t *testing.T) {
	root := deviceRoot(t)
	writeJSONFile(t, filepath.Join(root, "settings.json"), `not json`)
	ResetDevicePolicyCache()
	s, err := Load(context.Background(), Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Problems) == 0 {
		t.Fatal("no problem reported")
	}
}

func TestParseRegQuery(t *testing.T) {
	out := "\r\nHKEY_LOCAL_MACHINE\\SOFTWARE\\Policies\\xlsft\\harness\r\n    Settings    REG_SZ    {\"model\":\"m\"}\r\n\r\n"
	got, ok := parseRegQuery(out)
	if !ok || string(got) != `{"model":"m"}` {
		t.Fatalf("%q %v", got, ok)
	}
	if _, ok := parseRegQuery("nothing here"); ok {
		t.Fatal("found something")
	}
}

func TestFlattenPlist(t *testing.T) {
	if got := flattenPlist([]byte(`{"Settings":"{\"model\":\"a\"}"}`)); string(got) != `{"model":"a"}` {
		t.Fatalf("string form %s", got)
	}
	if got := flattenPlist([]byte(`{"Settings":{"model":"b"}}`)); string(got) != `{"model":"b"}` {
		t.Fatalf("dictionary form %s", got)
	}
	if got := flattenPlist([]byte(`{"model":"c"}`)); string(got) != `{"model":"c"}` {
		t.Fatalf("top level %s", got)
	}
}

func TestPermissionProfiles(t *testing.T) {
	deviceRoot(t)
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, ".harness", "settings.json"), `{
		"permissions": {"deny": ["Bash(rm *)"]},
		"permissionProfile": "ci",
		"permissionProfiles": {
			"ci": {"allow": ["Bash(go test *)"], "defaultMode": "acceptEdits"},
			"review": {"allow": ["Read"], "defaultMode": "plan"}
		}
	}`)
	load := func(trusted bool, profile string) *Settings {
		s, err := Load(context.Background(), Options{Cwd: cwd, Trusted: trusted, CLI: CLI{PermissionProfile: profile}})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	rules := func(list []Rule) []string {
		var out []string
		for _, r := range list {
			out = append(out, r.Rule)
		}
		return out
	}
	s := load(true, "")
	if s.Extras.PermissionProfile != "ci" || !slices.Contains(rules(s.Permissions.Allow), "Bash(go test *)") || s.Permissions.DefaultMode != "acceptEdits" {
		t.Fatalf("named in the file: %+v %+v", s.Extras, s.Permissions)
	}
	if !slices.Contains(rules(s.Permissions.Deny), "Bash(rm *)") {
		t.Fatalf("the level's own rules are gone: %v", rules(s.Permissions.Deny))
	}
	s = load(true, "review")
	if s.Extras.PermissionProfile != "review" || s.Permissions.DefaultMode != "plan" || slices.Contains(rules(s.Permissions.Allow), "Bash(go test *)") {
		t.Fatalf("from the flag: %+v %+v", s.Extras, s.Permissions)
	}
	if !slices.Equal(s.Extras.PermissionProfileNames, []string{"ci", "review"}) {
		t.Fatalf("names %v", s.Extras.PermissionProfileNames)
	}
	// In an untrusted folder a repository's profile widens nothing.
	s = load(false, "ci")
	if slices.Contains(rules(s.Permissions.Allow), "Bash(go test *)") || s.Permissions.DefaultMode == "acceptEdits" {
		t.Fatalf("untrusted: %+v", s.Permissions)
	}
	s = load(true, "nope")
	if s.Extras.PermissionProfile != "" || len(s.Problems) == 0 {
		t.Fatalf("unknown: %+v %v", s.Extras, s.Problems)
	}
}

func TestShellEnvironmentFilter(t *testing.T) {
	environ := []string{"PATH=/bin", "HOME=/h", "MY_API_KEY=x", "AWS_SECRET_ACCESS_KEY=y", "FOO=1", "BAR=2", "lc_all=C"}
	if got := (*ShellEnvironment)(nil).Filter(environ); !slices.Equal(got, environ) {
		t.Fatalf("nil: %v", got)
	}
	cases := []struct {
		p    ShellEnvironment
		want []string
	}{
		{ShellEnvironment{ExcludeSecrets: true}, []string{"PATH=/bin", "HOME=/h", "FOO=1", "BAR=2", "lc_all=C"}},
		{ShellEnvironment{Exclude: []string{"f*"}}, []string{"PATH=/bin", "HOME=/h", "MY_API_KEY=x", "AWS_SECRET_ACCESS_KEY=y", "BAR=2", "lc_all=C"}},
		{ShellEnvironment{Inherit: "core"}, []string{"PATH=/bin", "HOME=/h", "lc_all=C"}},
		{ShellEnvironment{Inherit: "none", Set: map[string]string{"A": "1"}}, []string{"A=1"}},
		{ShellEnvironment{Include: []string{"PATH", "FOO"}}, []string{"PATH=/bin", "FOO=1"}},
	}
	for i, c := range cases {
		if got := c.p.Filter(environ); !slices.Equal(got, c.want) {
			t.Errorf("%d: %v, want %v", i, got, c.want)
		}
	}
}

func TestExtrasLevels(t *testing.T) {
	root, cwd := deviceRoot(t), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, ".harness", "settings.json"), `{
		"permissions": {"restrictReads": true},
		"shellEnvironmentPolicy": {"set": {"FROM_REPO": "1"}},
		"bashMemoryLimitMB": 4096,
		"webSearchMode": "disabled"
	}`)
	s, err := Load(context.Background(), Options{Cwd: cwd, Trusted: false})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Extras.RestrictReads || s.Extras.WebSearchMode != WebSearchDisabled || s.Extras.BashMemoryLimitMB != 4096 {
		t.Fatalf("restrictions apply untrusted: %+v", s.Extras)
	}
	if s.Extras.ShellEnvironment != nil {
		t.Fatalf("an untrusted repository sets the environment: %+v", s.Extras.ShellEnvironment)
	}
	writeJSONFile(t, filepath.Join(root, "settings.json"), `{"bashMemoryLimitMB": 1024, "webSearchMode": "cached"}`)
	ResetDevicePolicyCache()
	s, err = Load(context.Background(), Options{Cwd: cwd, Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.Extras.BashMemoryLimitMB != 1024 || s.Extras.ShellEnvironment == nil {
		t.Fatalf("%+v", s.Extras)
	}
	if s.Extras.WebSearchMode != WebSearchDisabled {
		t.Fatalf("any level can disable search: %s", s.Extras.WebSearchMode)
	}
	writeJSONFile(t, filepath.Join(cwd, ".harness", "settings.local.json"), `{"webSearchMode":"sometimes","bashMemoryLimitMB":-1}`)
	s, _ = Load(context.Background(), Options{Cwd: cwd, Trusted: true})
	if len(s.Problems) != 2 {
		t.Fatalf("problems %v", s.Problems)
	}
}

func TestDebugReportHidesEnvValues(t *testing.T) {
	deviceRoot(t)
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, ".harness", "settings.local.json"), `{"env":{"API_TOKEN":"hunter2"},"model":"m1","permissions":{"allow":["Read"]}}`)
	s, err := Load(context.Background(), Options{Cwd: cwd, Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	r := s.DebugReport()
	if strings.Contains(r, "hunter2") {
		t.Fatalf("a value leaked: %s", r)
	}
	for _, want := range []string{"API_TOKEN", "Model: m1", "Read (local:", "settings.local.json"} {
		if !strings.Contains(r, want) {
			t.Errorf("no %q in:\n%s", want, r)
		}
	}
}

func TestFeatures(t *testing.T) {
	t.Setenv("HARNESS_NO_MCP", "1")
	t.Setenv("HARNESS_NO_HOOKS", "")
	s := &Settings{HooksOff: true, LSPEnabled: true}
	got := map[string]bool{}
	for _, f := range Features(s) {
		got[f.Name] = f.On
	}
	if got["mcp"] || got["hooks"] || !got["lsp"] {
		t.Fatalf("%v", got)
	}
	if !strings.Contains(FeatureReport(s), "off  mcp") {
		t.Fatalf("%s", FeatureReport(s))
	}
}
