package sources

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	"github.com/xlsft/harness/packages/client/importer"
)

// Every test reads fixtures under testdata (copied to a temporary folder when a run writes); none reads this machine's
// real home.

type recorder struct {
	created []*mcpv1.Server
	secrets []*mcpv1.SecretChanges
}

func (r *recorder) Servers(context.Context) ([]importer.ExistingMCP, error) { return nil, nil }

func (r *recorder) Create(_ context.Context, s *mcpv1.Server, sc *mcpv1.SecretChanges) error {
	r.created, r.secrets = append(r.created, s), append(r.secrets, sc)
	return nil
}

func (r *recorder) Replace(ctx context.Context, _ string, s *mcpv1.Server, sc *mcpv1.SecretChanges, _ int64) error {
	return r.Create(ctx, s, sc)
}

func testHome(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "home"))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// copyTree copies testdata/<name> into a new temporary home, as home/<name>, with a .git so it is a repository.
func copyTree(t *testing.T, name string) (home, root string) {
	t.Helper()
	home = t.TempDir()
	root = filepath.Join(home, name)
	src := filepath.Join("testdata", name)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Made here rather than in testdata, which git would ignore: the walk must skip it.
	nm := filepath.Join(root, "node_modules", "pkg")
	if err := os.MkdirAll(nm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nm, "GEMINI.md"), []byte("never read"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, root
}

func TestRegistry(t *testing.T) {
	all := All()
	if len(all) < 10 || all[len(all)-1].Info().ID != FolderID {
		t.Fatalf("sources: %d, last %s", len(all), all[len(all)-1].Info().ID)
	}
	seen := map[string]bool{}
	for _, s := range all {
		info := s.Info()
		if info.ID == "" || info.Name == "" || info.Description == "" || seen[info.ID] || (!info.User && !info.Folder) {
			t.Errorf("bad source %+v", info)
		}
		seen[info.ID] = true
	}
	if Get("claude-desktop") == nil || Get("nope") != nil {
		t.Error("Get")
	}
}

func TestClaudeDesktopPaths(t *testing.T) {
	home := testHome(t)
	src := Get("claude-desktop")
	for goos, want := range map[string]string{"darwin": "filesystem", "linux": "linux-only"} {
		env := importer.Env{Home: home, GOOS: goos}
		if !src.Detect(env) {
			t.Errorf("%s: not detected", goos)
		}
		found, _ := src.ScanUser(context.Background(), env)
		if len(found) == 0 || found[0].Name != want || found[0].MCP == nil {
			t.Errorf("%s: %+v", goos, found)
		}
	}
	// The Windows fixture has a trailing comma: reported, not fatal.
	found, _ := src.ScanUser(context.Background(), importer.Env{Home: home, GOOS: "windows"})
	if len(found) != 1 || found[0].Status != importer.StatusError {
		t.Errorf("windows: %+v", found)
	}
	if src.Detect(importer.Env{Home: t.TempDir(), GOOS: "darwin"}) {
		t.Error("detected in an empty home")
	}
}

func TestClaudeDesktopImport(t *testing.T) {
	ctx := context.Background()
	target := &recorder{}
	o := importer.EngineOptions{Env: importer.Env{Home: testHome(t), GOOS: "darwin"}, MCP: target, SecretsHint: "Include secrets"}
	scan, err := importer.ScanSource(ctx, Get("claude-desktop"), "", o)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Items) != 2 {
		t.Fatalf("items: %+v", scan.Items)
	}
	weather := scan.Items[1]
	if weather.Name != "weather api" || weather.Status != importer.StatusPlanned || !weather.HasSecrets || !strings.Contains(weather.Note, "WEATHER_API_KEY") ||
		!strings.Contains(weather.Note, "named weather-api here") || weather.Source != "~/Library/Application Support/Claude/claude_desktop_config.json" {
		t.Errorf("weather: %+v", weather)
	}
	var ids []string
	for _, it := range scan.Items {
		ids = append(ids, it.ID)
	}
	rep, err := importer.RunSource(ctx, Get("claude-desktop"), "", ids, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range rep.Items {
		if it.Status != importer.StatusUploaded {
			t.Errorf("%s: %s %s", it.Name, it.Status, it.Note)
		}
	}
	w := target.created[1]
	if w.GetName() != "weather-api" || w.GetStdio().GetEnv()["WEATHER_API_KEY"] != "${secret:WEATHER_API_KEY}" || w.GetStdio().GetEnv()["UNITS"] != "metric" || target.secrets[1] != nil {
		t.Errorf("weather: %v %v", w, target.secrets[1])
	}
	if f := target.created[0]; f.GetStdio().GetCommand() != "npx" || len(f.GetStdio().GetArgs()) != 3 {
		t.Errorf("filesystem: %v", f)
	}
	// With secrets opted in, the value is stored as the server's secret.
	o.IncludeSecrets = true
	target.created, target.secrets = nil, nil
	if _, err := importer.RunSource(ctx, Get("claude-desktop"), "", ids[1:], o); err != nil {
		t.Fatal(err)
	}
	if got := target.secrets[0].GetSet()["WEATHER_API_KEY"]; got != "test-not-a-real-key" {
		t.Errorf("secret: %q", got)
	}
}

func TestAnyFolder(t *testing.T) {
	ctx := context.Background()
	home, root := copyTree(t, "project")
	o := importer.EngineOptions{Env: importer.Env{Home: home}}
	scan, err := importer.ScanSource(ctx, Get(FolderID), root, o)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]importer.SourceItem{}
	for _, it := range scan.Items {
		got[it.Source+" "+it.Name] = it
		if it.Status != importer.StatusPlanned {
			t.Errorf("%s %s: %s %s", it.Source, it.Name, it.Status, it.Note)
		}
	}
	want := map[string]string{
		".cursor/rules/always.mdc always":                           ".harness/rules/always.md",
		".cursor/rules/frontend/components.mdc frontend/components": ".harness/rules/frontend/components.md",
		".cursorrules .cursorrules":                                 "AGENTS.md",
		".cursor/mcp.json docs":                                     ".mcp.json",
		".windsurf/rules/tests.md tests":                            ".harness/rules/tests.md",
		".windsurfrules .windsurfrules":                             "AGENTS.md",
		".github/copilot-instructions.md copilot-instructions.md":   "AGENTS.md",
		".github/instructions/python.instructions.md python":        ".harness/rules/python.md",
		".vscode/mcp.json github":                                   ".mcp.json",
		"GEMINI.md GEMINI.md":                                       "AGENTS.md",
		"web/GEMINI.md GEMINI.md":                                   "web/AGENTS.md",
		".gemini/settings.json events":                              ".mcp.json",
		".gemini/settings.json stream":                              ".mcp.json",
		".clinerules/api.md api":                                    ".harness/rules/api.md",
		".roo/rules/general.md general":                             ".harness/rules/general.md",
		".roo/rules-docs/style.md docs/style":                       ".harness/rules/docs/style.md",
		".kilocode/rules/naming.md naming":                          ".harness/rules/naming.md",
		".kiro/steering/components.md components":                   ".harness/rules/components.md",
		".continue/rules/01-general.md 01-general":                  ".harness/rules/01-general.md",
		".junie/guidelines.md guidelines.md":                        "AGENTS.md",
		".rules .rules":                                             "AGENTS.md",
		"CONVENTIONS.md CONVENTIONS.md":                             "AGENTS.md",
	}
	for k, target := range want {
		if it, ok := got[k]; !ok || it.Target != target {
			t.Errorf("%s: %+v", k, it)
		}
	}
	if len(got) != len(want) {
		t.Errorf("found %d items, want %d: %v", len(got), len(want), got)
	}

	var ids []string
	for _, it := range scan.Items {
		ids = append(ids, it.ID)
	}
	rep, err := importer.RunSource(ctx, Get(FolderID), root, ids, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range rep.Items {
		if it.Status != importer.StatusCopied {
			t.Errorf("run %s: %s %s", it.Source, it.Status, it.Note)
		}
	}
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			t.Errorf("%s: %v", p, err)
		}
		return string(b)
	}
	if s := read(".harness/rules/frontend/components.md"); !strings.Contains(s, `- "src/components/**/*.tsx"`) || !strings.Contains(s, `- "src/ui/**/*.tsx"`) || strings.Contains(s, "alwaysApply") {
		t.Errorf("cursor rule: %s", s)
	}
	if s := read(".harness/rules/always.md"); strings.Contains(s, "paths:") || !strings.Contains(s, "make test") {
		t.Errorf("always rule: %s", s)
	}
	if s := read(".harness/rules/python.md"); !strings.Contains(s, `- "tests/**/*.py"`) {
		t.Errorf("copilot rule: %s", s)
	}
	if s := read(".harness/rules/components.md"); !strings.Contains(s, `- "components/**/*.tsx"`) {
		t.Errorf("kiro rule: %s", s)
	}
	agents := read("AGENTS.md")
	for _, part := range []string{"Native to harness", "Prefer early returns.", "Keep functions short.", "imperative", "Answer briefly.", "Kotlin", "Zed rules", "Type hints"} {
		if !strings.Contains(agents, part) {
			t.Errorf("AGENTS.md lacks %q:\n%s", part, agents)
		}
	}
	if s := read("web/AGENTS.md"); !strings.Contains(s, "Nuxt") {
		t.Errorf("web/AGENTS.md: %s", s)
	}
	mcp := read(".mcp.json")
	for _, part := range []string{`"docs"`, `"${DOCS_KEY}"`, `"github"`, `"Bearer ${gh-token}"`, `"type": "sse"`, `"https://stream.example.com/mcp"`} {
		if !strings.Contains(mcp, part) {
			t.Errorf(".mcp.json lacks %s:\n%s", part, mcp)
		}
	}
	if strings.Contains(mcp, "test-token-value") {
		t.Error(".mcp.json got a secret without Include secrets")
	}
	if _, err := os.Stat(filepath.Join(root, ".harness", "rules", "ignored.md")); err == nil {
		t.Error("a plain .md under .cursor/rules was imported")
	}
}

func TestToolFolderSource(t *testing.T) {
	home, root := copyTree(t, "project")
	scan, err := importer.ScanSource(context.Background(), Get("cursor"), root, importer.EngineOptions{Env: importer.Env{Home: home}})
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Items) != 4 {
		t.Errorf("cursor items: %+v", scan.Items)
	}
	if _, err := importer.ScanSource(context.Background(), Get("cursor"), "", importer.EngineOptions{}); err == nil {
		t.Error("a folder-only source must ask for a folder")
	}
}

func TestStripJSONC(t *testing.T) {
	in := `{"a": "x // not a comment", /* c */ "b": [1, 2,], // end
}`
	if got := string(stripJSONC([]byte(in))); strings.Contains(got, "/* c */") || !strings.Contains(got, "x // not a comment") || strings.Contains(got, "2,]") {
		t.Errorf("%s", got)
	}
}
