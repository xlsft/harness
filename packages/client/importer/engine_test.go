package importer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/syncclient/synctest"
)

// fakeMCP records what an import creates.
type fakeMCP struct {
	have    []ExistingMCP
	created []*mcpv1.Server
	secrets []*mcpv1.SecretChanges
	replace []string
}

func (f *fakeMCP) Servers(context.Context) ([]ExistingMCP, error) { return f.have, nil }

func (f *fakeMCP) Create(_ context.Context, s *mcpv1.Server, sc *mcpv1.SecretChanges) error {
	f.created = append(f.created, s)
	f.secrets = append(f.secrets, sc)
	return nil
}

func (f *fakeMCP) Replace(_ context.Context, id string, s *mcpv1.Server, sc *mcpv1.SecretChanges) error {
	f.replace = append(f.replace, id)
	return f.Create(context.Background(), s, sc)
}

// testSource is a reader with fixed answers.
type testSource struct {
	info     SourceInfo
	user     []Found
	patterns []FolderPattern
}

func (s testSource) Info() SourceInfo                               { return s.info }
func (s testSource) Detect(Env) bool                                { return len(s.user) > 0 }
func (s testSource) ScanUser(context.Context, Env) ([]Found, error) { return s.user, nil }
func (s testSource) FolderPatterns() []FolderPattern                { return s.patterns }

func itemNamed(items []SourceItem, kind Kind, name string) *SourceItem {
	for i := range items {
		if items[i].Kind == kind && items[i].Name == name {
			return &items[i]
		}
	}
	return nil
}

func ids(items []SourceItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func userSource() testSource {
	return testSource{info: SourceInfo{ID: "tool", Name: "Tool", User: true}, user: []Found{
		{Kind: KindInstructions, Name: "TOOL.md", Source: "~/.tool/TOOL.md", Text: "Use tabs."},
		{Kind: KindCommand, Name: "deploy", Source: "~/.tool/commands/deploy.md", Text: "Deploy it."},
		{Kind: KindMCP, Name: "fs", Source: "~/.tool/mcp.json", MCP: &MCPServer{Command: "npx", Args: []string{"-y", "fs", "--api-key", "abc123"}, Env: map[string]string{"GITHUB_TOKEN": "ghp_secretvalue", "MODE": "x", "HOME_DIR": "${env:HOME}"}}},
		{Kind: KindMCP, Name: "remote api", Source: "~/.tool/mcp.json", MCP: &MCPServer{URL: "https://mcp.example.com/mcp?api_key=k123&x=1", Headers: map[string]string{"Authorization": "Bearer sk-test"}}},
		{Kind: KindMCP, Name: "taken", Source: "~/.tool/mcp.json", MCP: &MCPServer{Command: "node"}},
		{Kind: KindMCP, Name: "socket", Source: "~/.tool/mcp.json", MCP: &MCPServer{Transport: "ws", URL: "wss://x.example.com"}},
		{Kind: KindSession, Name: "s1", Source: "~/.tool/s1.json"},
	}}
}

func TestEngineUserLevel(t *testing.T) {
	ctx := context.Background()
	fs := synctest.New()
	srv := fs.Start()
	defer srv.Close()
	client := syncclient.NewClient(srv.URL, nil, nil)
	target := &fakeMCP{have: []ExistingMCP{{ID: "mcp_1", Name: "taken", ManagedBy: "You", CanEdit: true}}}
	o := EngineOptions{Env: Env{Home: t.TempDir()}, Putter: client, MCP: target}
	src := userSource()

	scan, err := ScanSource(ctx, src, "", o)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Status{"TOOL.md": StatusPlanned, "deploy": StatusPlanned, "fs": StatusPlanned, "remote api": StatusPlanned, "taken": StatusExists, "socket": StatusNoted, "s1": StatusNoted}
	for _, it := range scan.Items {
		if it.Status != want[it.Name] {
			t.Errorf("%s: %s (%s)", it.Name, it.Status, it.Note)
		}
	}
	if it := itemNamed(scan.Items, KindMCP, "fs"); it == nil || !it.HasSecrets || !strings.Contains(it.Note, "GITHUB_TOKEN") {
		t.Errorf("fs: %+v", it)
	}
	if it := itemNamed(scan.Items, KindInstructions, "TOOL.md"); it.Target != "rule tool/TOOL.md" {
		t.Errorf("target: %q", it.Target)
	}
	if len(target.created) != 0 {
		t.Fatal("a scan must not create servers")
	}

	rep, err := RunSource(ctx, src, "", ids(scan.Items), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range rep.Items {
		if it.Status == StatusError {
			t.Errorf("%s: %s", it.Name, it.Note)
		}
	}
	doc := fs.Document(&syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_RULE, Key: "tool/TOOL.md"})
	if doc == nil || doc.GetContent() != "Use tabs." || doc.GetSource() != "import:tool" {
		t.Fatalf("rule: %v", doc)
	}
	if len(target.created) != 2 {
		t.Fatalf("created %d servers", len(target.created))
	}
	fsrv, remote := target.created[0], target.created[1]
	if fsrv.GetName() != "fs" || fsrv.GetStdio().GetEnv()["GITHUB_TOKEN"] != "${secret:GITHUB_TOKEN}" || fsrv.GetStdio().GetEnv()["MODE"] != "x" ||
		fsrv.GetStdio().GetEnv()["HOME_DIR"] != "${secret:HOME}" || fsrv.GetStdio().GetArgs()[3] != "${secret:ARG_4}" {
		t.Errorf("fs: %v", fsrv)
	}
	if target.secrets[0] != nil {
		t.Errorf("secrets must stay out without IncludeSecrets: %v", target.secrets[0])
	}
	if remote.GetName() != "remote-api" || remote.GetTransport() != mcpv1.Transport_TRANSPORT_STREAMABLE_HTTP ||
		remote.GetRemote().GetUrl() != "https://mcp.example.com/mcp?api_key=${secret:API_KEY}&x=1" || remote.GetRemote().GetHeaders()["Authorization"] != "${secret:AUTHORIZATION}" {
		t.Errorf("remote: %v", remote)
	}
	if it := itemNamed(rep.Items, KindMCP, "remote api"); it == nil || it.Status != StatusUploaded || strings.Contains(it.Note, "k123") || strings.Contains(it.Note, "sk-test") {
		t.Errorf("remote note: %+v", it)
	}

	// Again, with secrets and force: the servers now exist, and force replaces the editable one.
	o.IncludeSecrets, o.Force = true, true
	target.created, target.secrets = nil, nil
	if _, err := RunSource(ctx, src, "", []string{itemNamed(scan.Items, KindMCP, "fs").ID, itemNamed(scan.Items, KindMCP, "taken").ID}, o); err != nil {
		t.Fatal(err)
	}
	if len(target.replace) != 1 || target.replace[0] != "mcp_1" {
		t.Errorf("replace: %v", target.replace)
	}
	if got := target.secrets[0].GetSet(); got["GITHUB_TOKEN"] != "ghp_secretvalue" || got["ARG_4"] != "abc123" {
		t.Errorf("secrets: %v", got)
	}

	// The documents are there now: a scan says so.
	o.Exists = func(ctx context.Context, ref *syncv1.DocumentRef) (bool, error) {
		_, err := client.GetDocument(ctx, &syncv1.GetDocumentRequest{Selector: &syncv1.GetDocumentRequest_Ref{Ref: ref}})
		if syncclient.IsNotFound(err) {
			return false, nil
		}
		return err == nil, err
	}
	scan, err = ScanSource(ctx, src, "", o)
	if err != nil {
		t.Fatal(err)
	}
	if it := itemNamed(scan.Items, KindCommand, "deploy"); !scan.ServerChecked || it.Status != StatusExists {
		t.Errorf("deploy after import: %+v (checked %v)", it, scan.ServerChecked)
	}
}

func TestEngineUserLevelWithoutTarget(t *testing.T) {
	scan, err := ScanSource(context.Background(), userSource(), "", EngineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if it := itemNamed(scan.Items, KindMCP, "fs"); it.Status != StatusNoted || !strings.Contains(it.Note, "Settings > MCP") {
		t.Errorf("fs: %+v", it)
	}
	if _, err := ScanSource(context.Background(), userSource(), t.TempDir(), EngineOptions{}); err == nil {
		t.Error("a user-level source must refuse a folder")
	}
}

func folderSource() testSource {
	return testSource{info: SourceInfo{ID: "tool", Name: "Tool", Folder: true}, patterns: []FolderPattern{
		{Glob: ".toolrules", Read: func(f FolderFile) []Found {
			return []Found{{Kind: KindInstructions, Name: ".toolrules", Text: string(f.Data)}}
		}},
		{Glob: ".tool/rules/*.md", Read: func(f FolderFile) []Found {
			return []Found{{Kind: KindRule, Name: strings.TrimSuffix(filepath.Base(f.Rel), ".md"), Text: string(f.Data)}}
		}},
		{Glob: ".tool/mcp.json", Read: func(f FolderFile) []Found {
			servers, _ := ParseMCPServers(f.Data, "mcpServers")
			var out []Found
			for _, n := range sortedKeys(servers) {
				out = append(out, Found{Kind: KindMCP, Name: n, MCP: servers[n]})
			}
			return out
		}},
		{Glob: ".tool/bad.md", Read: func(FolderFile) []Found {
			return []Found{{Kind: KindRule, Name: "../../escape", Text: "x"}}
		}},
	}}
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		p = filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEngineFolder(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	root := filepath.Join(home, "proj")
	writeFiles(t, root, map[string]string{
		".git/HEAD":               "ref: refs/heads/main\n",
		".toolrules":              "Always run the tests.",
		".tool/rules/style.md":    "Short names.",
		".tool/bad.md":            "x",
		".tool/mcp.json":          `{"mcpServers":{"db":{"command":"db-mcp","env":{"DB_PASSWORD":"hunter2","DB_HOST":"localhost"}}}}`,
		"pkg/.toolrules":          "Package rules.",
		"node_modules/.toolrules": "never read",
		"AGENTS.md":               "# Project\n",
	})
	o := EngineOptions{Env: Env{Home: home}}
	scan, err := ScanSource(ctx, folderSource(), filepath.Join(root, "pkg"), o)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Root != root {
		t.Fatalf("root %s", scan.Root)
	}
	if len(scan.Items) != 5 {
		t.Fatalf("items: %+v", scan.Items)
	}
	byTarget := map[string]SourceItem{}
	for _, it := range scan.Items {
		byTarget[it.Source] = it
	}
	if it := byTarget[".toolrules"]; it.Target != "AGENTS.md" || it.Status != StatusPlanned {
		t.Errorf(".toolrules: %+v", it)
	}
	if it := byTarget["pkg/.toolrules"]; it.Target != "pkg/AGENTS.md" {
		t.Errorf("pkg: %+v", it)
	}
	if it := byTarget[".tool/rules/style.md"]; it.Target != ".harness/rules/style.md" {
		t.Errorf("rule: %+v", it)
	}
	if it := byTarget[".tool/bad.md"]; it.Status != StatusError {
		t.Errorf("escape: %+v", it)
	}
	if it := byTarget[".tool/mcp.json"]; it.Target != ".mcp.json" || !it.HasSecrets || !it.NeedsTrust || !strings.Contains(it.Note, "DB_PASSWORD") {
		t.Errorf("mcp: %+v", it)
	}
	if _, err := os.Stat(filepath.Join(root, ".harness")); err == nil {
		t.Fatal("a scan must not write")
	}

	rep, err := RunSource(ctx, folderSource(), root, ids(scan.Items), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 5 {
		t.Fatalf("report: %+v", rep.Items)
	}
	agents, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if !strings.HasPrefix(string(agents), "# Project\n") || !strings.Contains(string(agents), "<!-- harness:import .toolrules ") || !strings.Contains(string(agents), "Always run the tests.") {
		t.Errorf("AGENTS.md: %s", agents)
	}
	if b, _ := os.ReadFile(filepath.Join(root, ".harness", "rules", "style.md")); string(b) != "Short names." {
		t.Errorf("rule: %q", b)
	}
	var mcp struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	b, _ := os.ReadFile(filepath.Join(root, ".mcp.json"))
	if err := json.Unmarshal(b, &mcp); err != nil {
		t.Fatal(err)
	}
	if db := mcp.MCPServers["db"]; db.Command != "db-mcp" || db.Env["DB_PASSWORD"] != "" || db.Env["DB_HOST"] != "localhost" {
		t.Errorf(".mcp.json: %s", b)
	}
	if strings.Contains(string(b), "hunter2") {
		t.Error("secret written without IncludeSecrets")
	}

	// A second scan finds everything in place.
	scan, err = ScanSource(ctx, folderSource(), root, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range scan.Items {
		if it.Status != StatusExists && it.Status != StatusError {
			t.Errorf("after import %s: %s %s", it.Source, it.Status, it.Note)
		}
	}
	if _, err := ScanSource(ctx, folderSource(), home, o); err == nil {
		t.Error("the home folder must be refused")
	}
}

func TestPlanMCPRefs(t *testing.T) {
	p := planMCP("x", &MCPServer{Command: "${userHome}/bin/srv", Args: []string{"--token=tok123", "--dir", "${workspaceFolder}"}, Env: map[string]string{"KEY": "$API_KEY"}}, false, "/h", "Include secrets")
	st := p.server.GetStdio()
	if st.GetCommand() != "/h/bin/srv" || st.GetArgs()[0] != "--token=${secret:TOKEN}" || st.GetArgs()[2] != "${secret:WORKSPACEFOLDER}" || st.GetEnv()["KEY"] != "${secret:API_KEY}" {
		t.Errorf("stdio: %v", st)
	}
	if MCPName(" my server!") != "my-server-" || MCPName("___") != "server" {
		t.Errorf("names: %q %q", MCPName(" my server!"), MCPName("___"))
	}
}

func TestClaudeImportCreatesMCPServers(t *testing.T) {
	ch, cj, _ := fakeHome(t)
	fs := synctest.New()
	srv := fs.Start()
	defer srv.Close()
	target := &fakeMCP{have: []ExistingMCP{{ID: "mcl_1", Name: "github", CanEdit: true}}}
	o := Options{ClaudeHome: ch, ClaudeJSON: cj, Putter: syncclient.NewClient(srv.URL, nil, nil), MCP: target, Sections: map[Section]bool{SectionMCP: true}}
	rep, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if it := find(rep, "mcp", "github"); it == nil || it.Status != StatusExists {
		t.Errorf("github: %+v", it)
	}
	if it := find(rep, "mcp", "fs"); it == nil || it.Status != StatusUploaded {
		t.Errorf("fs: %+v", it)
	}
	if len(target.created) != 1 || target.created[0].GetStdio().GetEnv()["TOKEN"] != "${secret:TOKEN}" || target.secrets[0] != nil {
		t.Fatalf("created: %v %v", target.created, target.secrets)
	}
}
