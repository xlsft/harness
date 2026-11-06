package bootstrap_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

func openWithExtended(t *testing.T, respond fg.Responder, trusted bool, mod func(*fakemcp.Service)) (*bootstrap.Session, *fg.Server, *fakemcp.Service) {
	t.Helper()
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	t.Setenv("HARNESS_MCP_APPROVE", "local")
	srv, url := fg.Start(t, respond)
	srv.Bundle.Reminders[mcp.ReminderInstructions] = "<system-reminder>SERVERS SAY\n{{servers}}\n</system-reminder>"
	srv.Bundle.Reminders[mcp.ReminderToolsChanged] = "<system-reminder>CHANGED {{server}} added {{added}} removed {{removed}}</system-reminder>"
	svc := fakemcp.NewService()
	exe, _ := os.Executable()
	svc.Servers = []*mcpv1.Server{{Id: "mcp_s", Name: "local", Scope: commonv1.Scope_SCOPE_USER, Transport: mcpv1.Transport_TRANSPORT_STDIO,
		Enabled: true, ConfigHash: "h1", Stdio: &mcpv1.StdioConfig{Command: exe}}}
	svc.Launch["mcp_s"] = fakemcp.SelfLaunch(map[string]string{fakemcp.EnvExtras: "1"})
	if mod != nil {
		mod(svc)
	}
	svcURL := svc.Start(t)
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	tok := auth.StaticToken("hat_test")
	s, err := bootstrap.Open(context.Background(), bootstrap.Options{
		Cwd: proj, Gateway: connectgw.New(url, nil, tok), Prompts: prompts.NewConnectSource(url, nil, tok),
		MCP: mcp.NewClient(svcURL, nil, tok), PermissionMode: permissions.ModeBypassPermissions, Trusted: &trusted,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, srv, svc
}

func TestServerInstructionsTravelWithTheConversation(t *testing.T) {
	s, srv, _ := openWithExtended(t, func(n int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.RequestClass == "compaction" {
			return fg.Reply{Events: fg.Message("s", fg.Text("SUMMARY"))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	}, true, nil)
	ctx := context.Background()
	if res := s.Agent.RunTurn(ctx, []llm.ContentBlock{llm.TextBlock("hello")}); res.IsError {
		t.Fatalf("turn: %+v", res)
	}
	reqs := srv.Requests()
	if txt := requestText(reqs[0]); !strings.Contains(txt, "SERVERS SAY") || !strings.Contains(txt, fakemcp.Instructions) {
		t.Fatalf("no instructions in the first request: %q", txt)
	}
	u := s.Agent.ContextUsage()
	if u.MCPInstructions == 0 {
		t.Fatalf("context usage %+v", u)
	}
	// A second turn does not repeat them.
	if res := s.Agent.RunTurn(ctx, []llm.ContentBlock{llm.TextBlock("again")}); res.IsError {
		t.Fatalf("turn: %+v", res)
	}
	reqs = srv.Requests()
	if n := strings.Count(requestText(reqs[len(reqs)-1]), "SERVERS SAY"); n != 1 {
		t.Fatalf("instructions %d times", n)
	}
	// Compaction drops the conversation; they come back.
	if _, err := s.Agent.Compact(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if res := s.Agent.RunTurn(ctx, []llm.ContentBlock{llm.TextBlock("after")}); res.IsError {
		t.Fatalf("turn: %+v", res)
	}
	reqs = srv.Requests()
	if n := strings.Count(requestText(reqs[len(reqs)-1]), "SERVERS SAY"); n != 1 {
		t.Fatalf("after compaction: %d times", n)
	}
}

func TestToolsChangedReachTheModel(t *testing.T) {
	s, srv, _ := openWithExtended(t, func(n int, r *llm.Request) fg.Reply {
		switch n {
		case 0:
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("t1", "mcp__local__grow", map[string]any{}))}
		default:
			return fg.Reply{Events: fg.Message("m2", fg.Text("done"))}
		}
	}, true, nil)
	ctx := context.Background()
	if res := s.Agent.RunTurn(ctx, []llm.ContentBlock{llm.TextBlock("grow")}); res.IsError {
		t.Fatalf("turn: %+v", res)
	}
	// The new tool arrives on the connection's own time; the next request after that carries it.
	deadline := time.Now().Add(20 * time.Second)
	for !slices.Contains(s.ToolNames(), "mcp__local__grown") {
		if time.Now().After(deadline) {
			t.Fatal("the tool did not arrive")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if res := s.Agent.RunTurn(ctx, []llm.ContentBlock{llm.TextBlock("next")}); res.IsError {
		t.Fatalf("turn: %+v", res)
	}
	reqs := srv.Requests()
	last := reqs[len(reqs)-1]
	if !slices.Contains(toolDefNames(last), "mcp__local__grown") {
		t.Fatalf("tools %v", toolDefNames(last))
	}
	all := ""
	for _, r := range reqs {
		all += requestText(r)
	}
	if !strings.Contains(all, "CHANGED local added mcp__local__grown removed (none)") {
		t.Fatalf("no reminder: %q", all)
	}
}

func TestUntrustedFolderGetsNoProjectServers(t *testing.T) {
	project := func(svc *fakemcp.Service) {
		svc.Servers = append(svc.Servers, &mcpv1.Server{Id: "mcp_p", Name: "fromrepo", Scope: commonv1.Scope_SCOPE_PROJECT, ProjectKey: "k", Transport: mcpv1.Transport_TRANSPORT_STREAMABLE_HTTP,
			Enabled: true, ConfigHash: "h2", Remote: &mcpv1.RemoteConfig{Url: "https://x/mcp"}})
		svc.Catalog["mcp_p"] = []*mcpv1.Tool{{Name: "go", Description: "Go"}}
	}
	_, _, svc := openWithExtended(t, func(int, *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("ok"))} }, false, project)
	for _, l := range svc.Lists {
		if l.GetProjectKey() != "" || slices.Contains(l.GetScopes(), commonv1.Scope_SCOPE_PROJECT) || len(l.GetScopes()) == 0 {
			t.Fatalf("an untrusted folder asked for project servers: %v", l)
		}
	}
	for _, c := range svc.Catalogs {
		if c.GetProjectKey() != "" {
			t.Fatalf("an untrusted folder asked for the project's catalog: %v", c)
		}
	}
}
