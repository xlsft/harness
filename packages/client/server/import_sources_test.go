package server

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/mcp"
)

// The generic import RPCs, against a temporary home (HOME is replaced, so the real one is never read).

func fakeImportHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	var dir string
	switch runtime.GOOS {
	case "darwin":
		dir = filepath.Join(home, "Library", "Application Support", "Claude")
	case "windows":
		dir = filepath.Join(home, "AppData", "Roaming", "Claude")
	default:
		dir = filepath.Join(home, ".config", "Claude")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"mcpServers":{"files":{"command":"files-mcp","args":["--root","/tmp"],"env":{"FILES_TOKEN":"test-token-1"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "claude_desktop_config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func runSource(t *testing.T, e *importEnv, req *agentv1.RunImportSourceRequest) *agentv1.ImportSourceReport {
	t.Helper()
	stream, err := e.client.RunImportSource(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	var report *agentv1.ImportSourceReport
	for stream.Receive() {
		if r := stream.Msg().GetReport(); r != nil {
			report = r
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestImportSourcesClaudeDesktop(t *testing.T) {
	fakeImportHome(t)
	e := newImportEnv(t, true)
	ctx := context.Background()
	list, err := e.client.ListImportSources(ctx, connect.NewRequest(&agentv1.ListImportSourcesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*agentv1.ImportSourceInfo{}
	for _, s := range list.Msg.GetSources() {
		byID[s.GetId()] = s
	}
	if cd := byID["claude-desktop"]; cd == nil || !cd.GetDetected() || !cd.GetUserLevel() {
		t.Fatalf("claude-desktop: %v", cd)
	}
	if byID["claude-code"].GetScreen() != "claude-code" || byID["folder"] == nil || !byID["folder"].GetFolderLevel() {
		t.Fatalf("sources: %v", list.Msg.GetSources())
	}

	scan, err := e.client.ScanImportSource(ctx, connect.NewRequest(&agentv1.ScanImportSourceRequest{SourceId: "claude-desktop"}))
	if err != nil {
		t.Fatal(err)
	}
	items := scan.Msg.GetItems()
	if len(items) != 1 || items[0].GetStatus() != agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_NEW || !items[0].GetHasSecrets() {
		t.Fatalf("scan: %v", items)
	}
	rep := runSource(t, e, &agentv1.RunImportSourceRequest{SourceId: "claude-desktop", ItemIds: []string{items[0].GetId()}})
	if rep.GetImported() != 1 || rep.GetError() != "" {
		t.Fatalf("report: %v", rep)
	}
	local := (&mcp.LocalStore{Path: bootstrap.MCPLocalFile()}).List()
	if len(local) != 1 || local[0].GetName() != "files" || local[0].GetStdio().GetEnv()["FILES_TOKEN"] != "${secret:FILES_TOKEN}" {
		t.Fatalf("device servers: %v", local)
	}
	if len(local[0].GetSecrets()) != 1 || local[0].GetSecrets()[0].GetHasValue() {
		t.Fatalf("the secret must be named without a value: %v", local[0].GetSecrets())
	}
	// Now it exists; without force it stays.
	scan, _ = e.client.ScanImportSource(ctx, connect.NewRequest(&agentv1.ScanImportSourceRequest{SourceId: "claude-desktop"}))
	if st := scan.Msg.GetItems()[0].GetStatus(); st != agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_EXISTS {
		t.Fatalf("second scan: %v", st)
	}
	// With force and secrets, the value is stored on this device.
	rep = runSource(t, e, &agentv1.RunImportSourceRequest{SourceId: "claude-desktop", ItemIds: []string{items[0].GetId()}, Force: true, IncludeSecrets: true})
	if rep.GetImported() != 1 {
		t.Fatalf("forced: %v", rep)
	}
	local = (&mcp.LocalStore{Path: bootstrap.MCPLocalFile()}).List()
	if len(local) != 1 || !local[0].GetSecrets()[0].GetHasValue() {
		t.Fatalf("after force: %v", local)
	}
	if _, err := e.client.ScanImportSource(ctx, connect.NewRequest(&agentv1.ScanImportSourceRequest{SourceId: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown source: %v", err)
	}
}

func TestImportSourcesAnyFolder(t *testing.T) {
	home := fakeImportHome(t)
	e := newImportEnv(t, false)
	root := filepath.Join(home, "proj")
	for p, c := range map[string]string{".git/HEAD": "x", ".cursorrules": "Prefer early returns.", "GEMINI.md": "Answer briefly."} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(c), 0o644)
	}
	scan, err := e.client.ScanImportSource(context.Background(), connect.NewRequest(&agentv1.ScanImportSourceRequest{SourceId: "folder", Folder: root}))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range scan.Msg.GetItems() {
		ids = append(ids, it.GetId())
	}
	if len(ids) != 2 || scan.Msg.GetRoot() != root {
		t.Fatalf("scan: %v", scan.Msg)
	}
	rep := runSource(t, e, &agentv1.RunImportSourceRequest{SourceId: "folder", Folder: root, ItemIds: ids})
	if rep.GetImported() != 2 {
		t.Fatalf("report: %v", rep)
	}
	b, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if !strings.Contains(string(b), "Prefer early returns.") || !strings.Contains(string(b), "Answer briefly.") {
		t.Fatalf("AGENTS.md: %s", b)
	}
	if _, err := e.client.ScanImportSource(context.Background(), connect.NewRequest(&agentv1.ScanImportSourceRequest{SourceId: "folder"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("folder source without a folder: %v", err)
	}
}

func TestRunClaudeImportCreatesMCPServers(t *testing.T) {
	e := newImportEnv(t, true)
	_, report := e.run(t, &agentv1.RunClaudeImportRequest{Sections: []agentv1.ClaudeImportSection{agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_MCP_SERVERS}})
	if report == nil || report.Imported != 1 || report.Errors != 0 {
		t.Fatalf("report: %v", report)
	}
	local := (&mcp.LocalStore{Path: bootstrap.MCPLocalFile()}).List()
	if len(local) != 1 || local[0].GetName() != "gh" || local[0].GetRemote().GetUrl() != "https://x" {
		t.Fatalf("device servers: %v", local)
	}
}
