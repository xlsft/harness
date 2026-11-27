package sources

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/importer"
)

func TestClineUser(t *testing.T) {
	f := userFound(t, "cline")
	if r := want(t, f, "rule/style"); !strings.Contains(r.Text, `"src/**"`) {
		t.Errorf("rule: %s", r.Text)
	}
	want(t, f, "command/release")
	if m := want(t, f, "mcp/events").MCP; m.Transport != "sse" {
		t.Errorf("a bare url is SSE in Cline: %+v", m)
	}
	// The CLI's file comes first; the same name in the extension's file is not imported twice.
	home := testdataHome(t)
	found, _ := Get("cline").ScanUser(context.Background(), importer.Env{Home: home, GOOS: "darwin"})
	var fs []importer.Found
	for _, x := range found {
		if x.Kind == importer.KindMCP && x.Name == "fs" {
			fs = append(fs, x)
		}
	}
	if len(fs) != 2 || fs[0].Status != "" || fs[0].MCP.Command != "fs-mcp" || !fs[0].MCP.Disabled || fs[1].Status != importer.StatusSkipped {
		t.Errorf("fs: %+v", fs)
	}
}

func TestRooUser(t *testing.T) {
	f := userFound(t, "roo")
	want(t, f, "rule/general")
	if r := want(t, f, "rule/code/tests"); !strings.Contains(r.Note, "one mode") {
		t.Errorf("mode rule: %+v", r)
	}
	rev := want(t, f, "agent/reviewer")
	if !strings.Contains(rev.Text, "read_only: true") || !strings.Contains(rev.Text, "You review code.\n\nBe brief.") || !strings.Contains(rev.Text, "description: Reviews") {
		t.Errorf("reviewer: %s", rev.Text)
	}
	docs := want(t, f, "agent/docs")
	if strings.Contains(docs.Text, "read_only") || !strings.Contains(docs.Note, `edit (\.md$)`) {
		t.Errorf("docs: %s / %s", docs.Text, docs.Note)
	}
	if m := want(t, f, "mcp/roo-db").MCP; m.URL == "" {
		t.Errorf("roo-db: %+v", m)
	}
}

func TestKiloUser(t *testing.T) {
	f := userFound(t, "kilo")
	m := want(t, f, "mcp/kilo-search").MCP
	if m.Transport != "http" || m.Headers["Authorization"] != "Bearer ${KILO_TOKEN}" {
		t.Errorf("kilo-search: %+v", m)
	}
	if a := want(t, f, "agent/planner"); !strings.Contains(a.Text, "Plan it.") || !strings.Contains(a.Note, "mode") {
		t.Errorf("planner: %s / %s", a.Text, a.Note)
	}
	want(t, f, "rule/legacy")
}

func TestOpenCodeUser(t *testing.T) {
	f := userFound(t, "opencode")
	m := want(t, f, "mcp/local-fs").MCP
	if m.Command != "npx" || len(m.Args) != 2 || m.Env["KEY"] != "${FS_KEY}" || !m.Disabled {
		t.Errorf("local-fs: %+v", m)
	}
	if a := want(t, f, "agent/build"); a.Status != importer.StatusSkipped {
		t.Errorf("an override of a built-in agent is skipped: %+v", a)
	}
	if a := want(t, f, "agent/writer"); !strings.Contains(a.Text, "Write well.") {
		t.Errorf("writer: %s", a.Text)
	}
	want(t, f, "command/test")
	want(t, f, "instructions/AGENTS.md")
	if r := want(t, f, "instructions/team-rules.md"); r.Text != "Team rules from OpenCode" {
		t.Errorf("team rules: %q", r.Text)
	}
	if r := want(t, f, "instructions/docs/rules.md"); r.Status != importer.StatusNoted {
		t.Errorf("a project path is noted: %+v", r)
	}
	if r := want(t, f, "instructions/https://example.com/r.md"); r.Status != importer.StatusNoted {
		t.Errorf("a URL is noted: %+v", r)
	}
	want(t, f, "agent/nested-helper")
	if c := want(t, f, "command/git:commit"); !strings.Contains(c.Note, "agent") || !strings.Contains(c.Text, "Commit $ARGUMENTS") {
		t.Errorf("commit: %s / %s", c.Text, c.Note)
	}
}

func TestKiroUser(t *testing.T) {
	f := userFound(t, "kiro")
	if r := want(t, f, "rule/product"); !strings.Contains(r.Text, `"app/**"`) {
		t.Errorf("product: %s", r.Text)
	}
	want(t, f, "mcp/kiro-aws")
	if h := want(t, f, "settings/hook lint on save"); h.Status != importer.StatusNoted || !strings.Contains(h.Note, "PostFileSave") {
		t.Errorf("hook: %+v", h)
	}
}

func TestContinueUser(t *testing.T) {
	f := userFound(t, "continue")
	want(t, f, "rule/rule-1")
	if r := want(t, f, "rule/go-style"); !strings.Contains(r.Text, `"**/*.go"`) || !strings.Contains(r.Text, "Use gofmt") {
		t.Errorf("go style: %s", r.Text)
	}
	if r := want(t, f, "rule/someone/rules"); r.Status != importer.StatusNoted {
		t.Errorf("hub block: %+v", r)
	}
	want(t, f, "command/write-tests")
	if m := want(t, f, "mcp/cont-sqlite").MCP; m.Env["TOKEN"] != "${SQLITE_TOKEN}" {
		t.Errorf("sqlite: %+v", m)
	}
	if m := want(t, f, "mcp/cont-remote").MCP; m.Headers["X-Key"] != "${C_KEY}" || m.URL == "" {
		t.Errorf("remote: %+v", m)
	}
	want(t, f, "settings/Continue settings")
	if _, ok := f["instructions/systemMessage"]; ok {
		t.Error("config.json was read although config.yaml exists")
	}
}

func TestZedUser(t *testing.T) {
	f := userFound(t, "zed")
	want(t, f, "instructions/AGENTS.md")
	if m := want(t, f, "mcp/zed-new").MCP; m.Command != "zed-mcp" || len(m.Args) != 1 {
		t.Errorf("zed-new: %+v", m)
	}
	if m := want(t, f, "mcp/zed-old").MCP; m.Command != "old-mcp" {
		t.Errorf("zed-old: %+v", m)
	}
	if m := want(t, f, "mcp/zed-ext"); m.Status != importer.StatusNoted {
		t.Errorf("zed-ext: %+v", m)
	}
	want(t, f, "mcp/zed-url")
}

func TestJunieUser(t *testing.T) {
	f := userFound(t, "junie")
	want(t, f, "instructions/AGENTS.md")
	want(t, f, "mcp/junie-db")
}

// The wave's folder patterns, each source on testdata/w2project.
func TestWave2Folders(t *testing.T) {
	_, root := copyTree(t, "w2project")
	cases := map[string][]string{
		"cline":    {"command/deploy"},
		"roo":      {"agent/proj", "mcp/proj-roo"},
		"opencode": {"mcp/proj-oc", "command/review", "instructions/CONTRIBUTING.md"},
		"kilo":     {"agent/fixer", "agent/kmode"},
		"kiro":     {"mcp/proj-kiro", "settings/hook on save", "instructions/.kiro/specs/login/requirements.md"},
		"continue": {"command/explain-code", "mcp/proj-cont"},
		"zed":      {"mcp/proj-zed"},
		"junie":    {"mcp/proj-junie"},
	}
	for id, keys := range cases {
		scan, err := importer.ScanSource(context.Background(), Get(id), root, importer.EngineOptions{})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		got := map[string]importer.SourceItem{}
		for _, it := range scan.Items {
			got[string(it.Kind)+"/"+it.Name] = it
		}
		for _, k := range keys {
			it, ok := got[k]
			if !ok {
				t.Errorf("%s: no %s in %v", id, k, got)
				continue
			}
			if it.Status == importer.StatusError {
				t.Errorf("%s: %s: %s", id, k, it.Note)
			}
		}
	}
}

func testdataHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.Abs(filepath.Join("testdata", "user"))
	if err != nil {
		t.Fatal(err)
	}
	return home
}
