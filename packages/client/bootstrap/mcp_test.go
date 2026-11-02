package bootstrap_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/fakemcp"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/mcp"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
)

func TestMain(m *testing.M) {
	fakemcp.MaybeServe()
	os.Exit(m.Run())
}

func toolDefNames(r *llm.Request) []string {
	var out []string
	for _, t := range r.Tools {
		out = append(out, t.Name)
	}
	return out
}

func requestText(r *llm.Request) string {
	var sb strings.Builder
	for _, m := range r.Messages {
		for _, b := range m.Content {
			sb.WriteString(b.Text)
			for _, c := range b.Content {
				sb.WriteString(c.Text)
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// TestMCPDeferredToolsEndToEnd runs a session against the fake gateway and a fake McpService with a
// stdio server: the model sees only the deferred tool names, loads one with ToolSearch and calls it.
func TestMCPDeferredToolsEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HARNESS_HOME", home)
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	t.Setenv("HARNESS_TOOL_SEARCH", "always")
	t.Setenv("HARNESS_MCP_APPROVE", "local")

	srv, url := fg.Start(t, func(n int, _ *llm.Request) fg.Reply {
		switch n {
		case 0:
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("tu1", mcp.ToolSearchName, map[string]any{"query": "select:mcp__local__echo"}))}
		case 1:
			return fg.Reply{Events: fg.Message("m2", fg.ToolUse("tu2", "mcp__local__echo", map[string]any{"text": "hi"}))}
		default:
			return fg.Reply{Events: fg.Message("m3", fg.Text("done"))}
		}
	})
	srv.Bundle.Tools[mcp.ToolSearchName] = prompts.ToolPrompt{Description: "Load deferred tools.", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)}
	srv.Bundle.Reminders[mcp.ReminderDeferredTools] = "<system-reminder>Deferred tools:\n{{tools}}</system-reminder>"

	svc := fakemcp.NewService()
	exe, _ := os.Executable()
	svc.Servers = []*mcpv1.Server{{Id: "mcp_s", Name: "local", Scope: commonv1.Scope_SCOPE_USER, Transport: mcpv1.Transport_TRANSPORT_STDIO,
		Enabled: true, ConfigHash: "h1", Stdio: &mcpv1.StdioConfig{Command: exe}}}
	svc.Launch["mcp_s"] = fakemcp.SelfLaunch(nil)
	svcURL := svc.Start(t)

	proj, _ := filepath.EvalSymlinks(t.TempDir())
	tok := auth.StaticToken("hat_test")
	s, err := bootstrap.Open(context.Background(), bootstrap.Options{
		Cwd: proj, Gateway: connectgw.New(url, nil, tok), Prompts: prompts.NewConnectSource(url, nil, tok),
		MCP: mcp.NewClient(svcURL, nil, tok), PermissionMode: permissions.ModeBypassPermissions,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.MCP == nil {
		t.Fatal("MCP manager not opened")
	}
	if names := s.ToolNames(); !slices.Contains(names, mcp.ToolSearchName) || slices.Contains(names, "mcp__local__echo") {
		t.Fatalf("ToolNames before loading = %v", names)
	}

	res := s.Agent.RunTurn(context.Background(), []llm.ContentBlock{llm.TextBlock("echo hi")})
	if res.IsError || res.Result != "done" {
		t.Fatalf("turn = %+v", res)
	}
	reqs := srv.Requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d", len(reqs))
	}
	first := toolDefNames(reqs[0])
	if !slices.Contains(first, mcp.ToolSearchName) || slices.Contains(first, "mcp__local__echo") {
		t.Fatalf("first request tools = %v", first)
	}
	if txt := requestText(reqs[0]); !strings.Contains(txt, "Deferred tools:\nmcp__local__add\nmcp__local__echo\nmcp__local__fail") {
		t.Fatalf("deferred reminder missing from the first request: %q", txt)
	}
	if second := toolDefNames(reqs[1]); !slices.Contains(second, "mcp__local__echo") || slices.Contains(second, "mcp__local__add") {
		t.Fatalf("second request tools = %v", second)
	}
	if txt := requestText(reqs[2]); !strings.Contains(txt, "echo: hi") {
		t.Fatalf("tool result missing: %q", txt)
	}
	if !slices.Contains(s.ToolNames(), "mcp__local__echo") {
		t.Fatalf("ToolNames after loading = %v", s.ToolNames())
	}

	// Resuming restores the loaded tool and keeps the catalog snapshot.
	id := s.ID
	s.Close()
	s2, err := bootstrap.Open(context.Background(), bootstrap.Options{
		Cwd: proj, Gateway: connectgw.New(url, nil, tok), Prompts: prompts.NewConnectSource(url, nil, tok),
		MCP: mcp.NewClient(svcURL, nil, tok), PermissionMode: permissions.ModeBypassPermissions, Resume: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if !slices.Contains(s2.ToolNames(), "mcp__local__echo") {
		t.Fatalf("resumed ToolNames = %v", s2.ToolNames())
	}
	if last := svc.Catalogs[len(svc.Catalogs)-1]; last.GetVersion() != "cat_1" {
		t.Fatalf("resume did not request the frozen catalog: %v", last)
	}
}
