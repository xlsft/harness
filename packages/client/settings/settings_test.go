package settings

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

type fakeDocs struct {
	synced bool
	docs   []*syncv1.Document
}

func (f *fakeDocs) Synced() bool { return f.synced }
func (f *fakeDocs) List(kind syncv1.DocumentKind) []*syncv1.Document {
	var out []*syncv1.Document
	for _, d := range f.docs {
		if d.GetKind() == kind {
			out = append(out, d)
		}
	}
	return out
}

func doc(id string, scope commonv1.Scope, kind syncv1.DocumentKind, key, content string) *syncv1.Document {
	return &syncv1.Document{Id: id, Scope: scope, Kind: kind, Key: key, Content: content}
}

func writeJSON(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ruleTexts(rs []Rule) []string { return RuleStrings(rs) }

func ruleLevels(rs []Rule) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Rule+"@"+r.Source.Level.String())
	}
	return out
}

type fixture struct {
	cwd, home string
	docs      *fakeDocs
}

func newFixture(t *testing.T) fixture {
	root := t.TempDir()
	f := fixture{cwd: filepath.Join(root, "proj"), home: filepath.Join(root, "home")}
	writeJSON(t, filepath.Join(f.cwd, ".harness", "settings.json"), `{
		"model": "project-model", "effortLevel": "high", "outputStyle": "Explanatory",
		"env": {"PROJECT_VAR": "p", "SHARED": "project"},
		"permissions": {"allow": ["Bash(make *)"], "ask": ["Bash(git push *)"], "deny": ["Read(./.env)"],
			"defaultMode": "acceptEdits", "additionalDirectories": ["../shared"]},
		"attribution": {"commit": "project commit"}
	}`)
	writeJSON(t, filepath.Join(f.cwd, ".harness", "settings.local.json"), `{
		"effortLevel": "low", "env": {"SHARED": "local"}, "outputStyle": "harness-local",
		"permissions": {"allow": ["Edit(/src/**)"]}
	}`)
	writeJSON(t, filepath.Join(f.home, ".claude", "settings.json"), `{
		"model": "local-user-model", "cleanupPeriodDays": 7,
		"permissions": {"allow": ["WebFetch"], "deny": ["Bash(rm -rf *)"]}
	}`)
	f.docs = &fakeDocs{synced: true, docs: []*syncv1.Document{
		doc("doc_us", commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, "user",
			`{"model": "user-model", "cleanupPeriodDays": 14, "autoCompactWindow": 150000, "attribution": {"pr": "user pr"}, "env": {"USER_VAR": "u", "SHARED": "user"}}`),
		doc("doc_ur", commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_PERMISSION_RULES, "user",
			`{"allow": ["Bash(go test *)"], "deny": ["Bash(curl *)"]}`),
		doc("doc_os", commonv1.Scope_SCOPE_ORG, syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, "user",
			`{"model": "org-model", "includeGitInstructions": false, "interruptReply": false, "claudeMdMode": "claude-md"}`),
		doc("doc_or", commonv1.Scope_SCOPE_ORG, syncv1.DocumentKind_DOCUMENT_KIND_PERMISSION_RULES, "user",
			`{"ask": ["Bash(docker *)"]}`),
		doc("doc_pol", commonv1.Scope_SCOPE_ORG, syncv1.DocumentKind_DOCUMENT_KIND_POLICY, "default",
			`{"permissions": {"deny": ["Bash(sudo *)"], "disableBypassPermissionsMode": "disable"}, "outputStyle": "policy-style"}`),
	}}
	return f
}

func TestPrecedenceTrusted(t *testing.T) {
	f := newFixture(t)
	s, err := Load(context.Background(), Options{Cwd: f.cwd, Home: f.home, Docs: f.docs, Trusted: true,
		CLI: CLI{Model: "cli-model", Allow: []string{"Read"}, Deny: []string{"Bash(nc *)"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Problems) != 0 {
		t.Fatalf("problems: %v", s.Problems)
	}
	if s.Model != "cli-model" {
		t.Errorf("model %q (CLI beats project/user/org)", s.Model)
	}
	if s.OutputStyle != "policy-style" {
		t.Errorf("outputStyle %q (policy always wins)", s.OutputStyle)
	}
	if s.EffortLevel != "low" {
		t.Errorf("effort %q (local beats project)", s.EffortLevel)
	}
	if s.CleanupPeriodDays != 14 || s.AutoCompactWindow != 150000 {
		t.Errorf("user doc scalars: %d %d (local ~/.claude must be ignored after sync)", s.CleanupPeriodDays, s.AutoCompactWindow)
	}
	if s.IncludeGitInstructions || s.InterruptReply || s.ClaudeMdMode != "claude-md" {
		t.Errorf("org defaults: %v %v %q", s.IncludeGitInstructions, s.InterruptReply, s.ClaudeMdMode)
	}
	if *s.Attribution.Commit != "project commit" || *s.Attribution.PR != "user pr" {
		t.Errorf("attribution %q %q", *s.Attribution.Commit, *s.Attribution.PR)
	}
	wantEnv := map[string]string{"PROJECT_VAR": "p", "SHARED": "local", "USER_VAR": "u"}
	if !reflect.DeepEqual(s.Env, wantEnv) {
		t.Errorf("env %v", s.Env)
	}
	if got, want := ruleLevels(s.Permissions.Allow), []string{"Read@cli", "Edit(/src/**)@local", "Bash(make *)@project", "Bash(go test *)@user"}; !reflect.DeepEqual(got, want) {
		t.Errorf("allow %q want %q", got, want)
	}
	if got, want := ruleLevels(s.Permissions.Deny), []string{"Bash(sudo *)@policy", "Bash(nc *)@cli", "Read(./.env)@project", "Bash(curl *)@user"}; !reflect.DeepEqual(got, want) {
		t.Errorf("deny %q want %q", got, want)
	}
	if got, want := ruleTexts(s.Permissions.Ask), []string{"Bash(git push *)", "Bash(docker *)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ask %q", got)
	}
	if s.Permissions.DefaultMode != "acceptEdits" || s.Permissions.DefaultModeSource.Level != LevelProject {
		t.Errorf("mode %q from %v", s.Permissions.DefaultMode, s.Permissions.DefaultModeSource)
	}
	in := s.PermissionInputs()
	if len(in.AdditionalDirectories) != 1 || in.AdditionalDirectories[0] != filepath.Join(filepath.Dir(f.cwd), "shared") {
		t.Errorf("dirs %v", in.AdditionalDirectories)
	}
	// Path rules keep the directory they resolve against.
	if in.Allow[1].Source.Dir != f.cwd || !strings.HasSuffix(in.Allow[1].Source.Path, "settings.local.json") {
		t.Errorf("rule source %+v", in.Allow[1].Source)
	}
	if in.Allow[3].Source.DocID != "doc_ur" || in.Allow[3].Source.Dir != f.home {
		t.Errorf("doc rule source %+v", in.Allow[3].Source)
	}
	if !in.DisableBypass {
		t.Error("policy disableBypass")
	}
	if got := s.Sources[0]; got.Level != LevelPolicy || got.DocID != "doc_pol" {
		t.Errorf("first source %v", got)
	}
}

func TestTrustGate(t *testing.T) {
	f := newFixture(t)
	s, _ := Load(context.Background(), Options{Cwd: f.cwd, Home: f.home, Docs: f.docs, Trusted: false})
	for _, r := range s.Permissions.Allow {
		if r.Source.Level == LevelProject {
			t.Fatalf("project allow rule applied in untrusted workspace: %v", r)
		}
	}
	if got := ruleLevels(s.Permissions.Deny); !strings.Contains(strings.Join(got, " "), "Read(./.env)@project") {
		t.Errorf("project deny must apply: %q", got)
	}
	if got := ruleTexts(s.Permissions.Ask); got[0] != "Bash(git push *)" {
		t.Errorf("project ask must apply: %q", got)
	}
	if _, ok := s.Env["PROJECT_VAR"]; ok {
		t.Error("project env applied in untrusted workspace")
	}
	if s.Env["SHARED"] != "local" {
		t.Errorf("local env still applies: %v", s.Env)
	}
	if s.Permissions.DefaultMode != "" {
		t.Errorf("widening defaultMode applied: %q", s.Permissions.DefaultMode)
	}
	if len(s.Permissions.AdditionalDirectories) != 0 {
		t.Errorf("dirs %v", s.Permissions.AdditionalDirectories)
	}
	if len(s.Ignored) < 4 {
		t.Errorf("ignored %v", s.Ignored)
	}
	// Non-permission scalars still apply.
	if s.Model != "project-model" {
		t.Errorf("model %q", s.Model)
	}
}

func TestBeforeFirstSync(t *testing.T) {
	f := newFixture(t)
	f.docs.synced = false
	s, _ := Load(context.Background(), Options{Cwd: f.cwd, Home: f.home, Docs: f.docs, Trusted: true})
	if s.Synced {
		t.Error("synced")
	}
	// No user level yet: ~/.claude/settings.json does not stand in for it.
	if s.CleanupPeriodDays != DefaultCleanupPeriodDays {
		t.Errorf("cleanupPeriodDays %d", s.CleanupPeriodDays)
	}
	if got := ruleLevels(s.Permissions.Allow); strings.Contains(strings.Join(got, " "), "@user") {
		t.Errorf("allow %q", got)
	}
	// Policy applies even before the first full sync.
	if s.OutputStyle != "policy-style" || ruleTexts(s.Permissions.Deny)[0] != "Bash(sudo *)" {
		t.Errorf("policy not applied: %q %q", s.OutputStyle, ruleTexts(s.Permissions.Deny))
	}
	// No docs at all.
	s, _ = Load(context.Background(), Options{Cwd: f.cwd, Home: f.home, Trusted: true})
	if s.Model != "project-model" || s.CleanupPeriodDays != DefaultCleanupPeriodDays {
		t.Errorf("no docs: %q %d", s.Model, s.CleanupPeriodDays)
	}
}

func TestPolicyEnforcement(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "p")
	writeJSON(t, filepath.Join(cwd, ".harness", "settings.local.json"), `{"permissions": {"defaultMode": "bypassPermissions", "allow": ["Bash"]}}`)
	docs := &fakeDocs{synced: true, docs: []*syncv1.Document{
		doc("doc_a", commonv1.Scope_SCOPE_ORG, syncv1.DocumentKind_DOCUMENT_KIND_POLICY, "default",
			`{"permissions": {"disableBypassPermissionsMode": "disable", "allow": ["Read"]}, "availableModels": ["m1", "m2"], "allowManagedPermissionRulesOnly": true}`),
		doc("doc_b", commonv1.Scope_SCOPE_ORG, syncv1.DocumentKind_DOCUMENT_KIND_POLICY, "engineers", `{"model": "m3"}`),
		// Policy documents at other scopes are ignored.
		doc("doc_c", commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_POLICY, "default", `{"model": "evil"}`),
	}}
	s, _ := Load(context.Background(), Options{Cwd: cwd, Home: filepath.Join(root, "h"), Docs: docs, Trusted: true})
	if s.Permissions.DefaultMode != "default" {
		t.Errorf("bypass must be disabled: %q", s.Permissions.DefaultMode)
	}
	if got := ruleLevels(s.Permissions.Allow); !reflect.DeepEqual(got, []string{"Read@policy"}) {
		t.Errorf("managed rules only: %q", got)
	}
	if s.Model != "m1" {
		t.Errorf("model %q must be replaced by an allowed one", s.Model)
	}
	if s.Sources[0].DocID != "doc_b" {
		t.Errorf("role policy should rank above default: %v", s.Sources)
	}
}

func TestPRMonitorWakesPerHour(t *testing.T) {
	root := t.TempDir()
	cwd, home := filepath.Join(root, "p"), filepath.Join(root, "h")
	load := func(content string) *Settings {
		t.Helper()
		user := doc("doc_u", commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, UserSettingsKey, content)
		s, err := Load(context.Background(), Options{Cwd: cwd, Home: home, Docs: &fakeDocs{synced: true, docs: []*syncv1.Document{user}}, Trusted: true})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := load(`{}`); s.PRMonitorWakesPerHour != 30 {
		t.Fatalf("default: %d", s.PRMonitorWakesPerHour)
	}
	if s := load(`{"prMonitorWakesPerHour": 60}`); s.PRMonitorWakesPerHour != 60 {
		t.Fatalf("set: %d", s.PRMonitorWakesPerHour)
	}
	for _, bad := range []string{`0`, `-1`, `241`, `1.5`, `"many"`} {
		if s := load(`{"prMonitorWakesPerHour": ` + bad + `}`); s.PRMonitorWakesPerHour != 30 {
			t.Fatalf("%s: %d", bad, s.PRMonitorWakesPerHour)
		}
	}
}

func TestCompactIdleSessions(t *testing.T) {
	root := t.TempDir()
	cwd, home := filepath.Join(root, "p"), filepath.Join(root, "h")
	load := func(docs DocSource) *Settings {
		t.Helper()
		s, err := Load(context.Background(), Options{Cwd: cwd, Home: home, Docs: docs, Trusted: true})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	// On by default.
	if s := load(&fakeDocs{synced: true}); !s.CompactIdleSessions {
		t.Fatal("compactIdleSessions should default to true")
	}
	// The user's synced settings document (where UpdateUserPreferences writes it) turns it off.
	user := doc("doc_u", commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, UserSettingsKey, `{"compactIdleSessions": false}`)
	if s := load(&fakeDocs{synced: true, docs: []*syncv1.Document{user}}); s.CompactIdleSessions {
		t.Fatal("the user document should turn it off")
	}
	// ~/.claude/settings.json is not read, before a sync or after one.
	writeJSON(t, filepath.Join(home, ".claude", "settings.json"), `{"compactIdleSessions": false}`)
	if s := load(nil); !s.CompactIdleSessions {
		t.Fatal("~/.claude/settings.json was read")
	}
	// A wrong type is a problem and keeps the default.
	user.Content = `{"compactIdleSessions": "no"}`
	if s := load(&fakeDocs{synced: true, docs: []*syncv1.Document{user}}); !s.CompactIdleSessions || len(s.Problems) == 0 {
		t.Fatalf("invalid value: %v %v", s.CompactIdleSessions, s.Problems)
	}
}

func TestValidationCollected(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "p")
	writeJSON(t, filepath.Join(cwd, ".harness", "settings.json"), `{not json`)
	writeJSON(t, filepath.Join(cwd, ".harness", "settings.local.json"), `{
		"model": 3, "effortLevel": "turbo", "cleanupPeriodDays": -1, "autoCompactWindow": 1.5,
		"env": {"A": 1, "B": {"x": 1}}, "permissions": {"allow": ["Read", 5, ""], "defaultMode": "yolo"},
		"claudeMdMode": "both", "includeGitInstructions": "yes", "outputStyle": "ok"
	}`)
	s, err := Load(context.Background(), Options{Cwd: cwd, Home: filepath.Join(root, "h"), Trusted: true,
		CLI: CLI{EffortLevel: "bogus", PermissionMode: "plan", Settings: []byte(`{"outputStyle": "cli-style"}`), SettingsPath: "/tmp/x.json"}})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, p := range s.Problems {
		keys[p.Key] = true
	}
	for _, k := range []string{"", "model", "effortLevel", "cleanupPeriodDays", "autoCompactWindow", "env.B", "permissions.allow[1]", "permissions.allow[2]", "permissions.defaultMode", "claudeMdMode", "includeGitInstructions"} {
		if !keys[k] {
			t.Errorf("missing problem for %q; got %v", k, s.Problems)
		}
	}
	if s.Env["A"] != "1" || s.OutputStyle != "cli-style" || s.Permissions.DefaultMode != "plan" || ruleTexts(s.Permissions.Allow)[0] != "Read" {
		t.Errorf("valid values must survive: env %v style %q mode %q", s.Env, s.OutputStyle, s.Permissions.DefaultMode)
	}
	if s.CleanupPeriodDays != DefaultCleanupPeriodDays || !s.IncludeGitInstructions || !s.InterruptReply {
		t.Errorf("defaults: %d %v %v", s.CleanupPeriodDays, s.IncludeGitInstructions, s.InterruptReply)
	}
}

func TestLSPSettings(t *testing.T) {
	load := func(t *testing.T, trusted bool, project, local, user string) *Settings {
		t.Helper()
		root := t.TempDir()
		cwd, home := filepath.Join(root, "p"), filepath.Join(root, "h")
		for path, content := range map[string]string{
			filepath.Join(cwd, ".harness", "settings.json"):       project,
			filepath.Join(cwd, ".harness", "settings.local.json"): local,
		} {
			if content != "" {
				writeJSON(t, path, content)
			}
		}
		o := Options{Cwd: cwd, Home: home, Trusted: trusted}
		if user != "" {
			o.Docs = &fakeDocs{synced: true, docs: []*syncv1.Document{doc("doc_u", commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, UserSettingsKey, user)}}
		}
		s, err := Load(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	server := `{"lspServers": {"go": {"command": "/evil/gopls"}}}`

	s := load(t, true, "", "", "")
	if !s.LSPEnabled || !s.LSPDiagnostics || s.LSPDiagnosticsStartServer || s.LSPServers != nil {
		t.Fatalf("defaults: %+v", s)
	}
	s = load(t, false, "", "", `{"lspEnabled": false, "lspDiagnostics": false, "lspDiagnosticsStartServer": true, "lspServers": {"go": {"command": "/opt/gopls", "args": ["-v"]}, "zig": {"command": "zls", "extensions": [".zig"]}}}`)
	if s.LSPEnabled || s.LSPDiagnostics || !s.LSPDiagnosticsStartServer {
		t.Fatalf("user switches: %+v", s)
	}
	if s.LSPServers["go"].Command != "/opt/gopls" || s.LSPServers["go"].Args[0] != "-v" || s.LSPServers["zig"].Extensions[0] != ".zig" {
		t.Fatalf("user servers: %+v", s.LSPServers)
	}

	// A repository chooses no binary until it is trusted, through either project file.
	for name, files := range map[string][2]string{"project": {server, ""}, "local": {"", server}} {
		s = load(t, false, files[0], files[1], "")
		if s.LSPServers != nil || len(s.Ignored) == 0 {
			t.Fatalf("%s, untrusted: %+v %v", name, s.LSPServers, s.Ignored)
		}
		s = load(t, true, files[0], files[1], "")
		if s.LSPServers["go"].Command != "/evil/gopls" {
			t.Fatalf("%s, trusted: %+v", name, s.LSPServers)
		}
	}
	// Entries merge by language: a project's replaces the user's for that language only.
	s = load(t, true, server, "", `{"lspServers": {"go": {"command": "/opt/gopls"}, "python": {"disabled": true}}}`)
	if s.LSPServers["go"].Command != "/evil/gopls" || !s.LSPServers["python"].Disabled {
		t.Fatalf("merge: %+v", s.LSPServers)
	}
	// The user's own, kept while the project's is ignored.
	s = load(t, false, server, "", `{"lspServers": {"go": {"command": "/opt/gopls"}}}`)
	if s.LSPServers["go"].Command != "/opt/gopls" {
		t.Fatalf("untrusted merge: %+v", s.LSPServers)
	}

	// A project can turn the switches off, never on against the user.
	s = load(t, false, `{"lspEnabled": false, "lspDiagnostics": false}`, "", "")
	if s.LSPEnabled || s.LSPDiagnostics {
		t.Fatalf("project off: %+v", s)
	}
	s = load(t, true, `{"lspEnabled": true, "lspDiagnosticsStartServer": true}`, `{"lspDiagnostics": true}`, `{"lspEnabled": false, "lspDiagnostics": false}`)
	if s.LSPEnabled || s.LSPDiagnostics || s.LSPDiagnosticsStartServer || len(s.Ignored) != 3 {
		t.Fatalf("project on: %+v %v", s, s.Ignored)
	}

	s = load(t, true, "", "", `{"lspEnabled": "no", "lspServers": ["gopls"]}`)
	if !s.LSPEnabled || s.LSPServers != nil || len(s.Problems) != 2 {
		t.Fatalf("invalid: %+v %v", s, s.Problems)
	}
}

// Project and user files under .claude are import sources: present or not, they change nothing.
func TestClaudeSettingsAreNotRead(t *testing.T) {
	root := t.TempDir()
	cwd, home := filepath.Join(root, "p"), filepath.Join(root, "h")
	hook := `{"model": "claude-file", "env": {"FROM_CLAUDE": "1"}, "permissions": {"allow": ["Bash"], "deny": ["Read"]}, "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "x"}]}]}}`
	writeJSON(t, filepath.Join(cwd, ".claude", "settings.json"), hook)
	writeJSON(t, filepath.Join(cwd, ".claude", "settings.local.json"), hook)
	writeJSON(t, filepath.Join(home, ".claude", "settings.json"), hook)
	for _, docs := range []DocSource{nil, &fakeDocs{synced: true}} {
		s, err := Load(context.Background(), Options{Cwd: cwd, Home: home, Docs: docs, Trusted: true})
		if err != nil {
			t.Fatal(err)
		}
		if s.Model != "" || len(s.Env) != 0 || len(s.Permissions.Allow)+len(s.Permissions.Deny) != 0 || len(s.Hooks) != 0 || len(s.Sources) != 0 {
			t.Fatalf("%+v", s)
		}
	}
}
