package server_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/fakemcp"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/mcp"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/server"
)

func TestMain(m *testing.M) {
	fakemcp.MaybeServe()
	os.Exit(m.Run())
}

// mcpSession starts a harness with an extended stdio MCP server named x, already approved.
func mcpSession(t *testing.T, respond fg.Responder) (*harness, *fakemcp.Service, string, <-chan *agentv1.SessionEvent) {
	t.Helper()
	t.Setenv("HARNESS_MCP_APPROVE", "x")
	svc := fakemcp.NewService()
	exe, _ := os.Executable()
	svc.Servers = []*mcpv1.Server{{Id: "mcp_x", Name: "x", Scope: commonv1.Scope_SCOPE_USER, Transport: mcpv1.Transport_TRANSPORT_STDIO, Enabled: true, ConfigHash: "h", Stdio: &mcpv1.StdioConfig{Command: exe}}}
	svc.Launch["mcp_x"] = fakemcp.SelfLaunch(map[string]string{fakemcp.EnvExtras: "1"})
	url := svc.Start(t)
	h := startHarnessGW(t, respond, func(gw *fg.Server) {
		gw.Bundle.Tools[mcp.ListResourcesToolName] = prompts.ToolPrompt{Description: "List.", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}
		gw.Bundle.Reminders[mcp.ReminderInstructions] = "<system-reminder>SERVERS SAY\n{{servers}}\n</system-reminder>"
		gw.Bundle.Reminders[mcp.ReminderToolsChanged] = "<system-reminder>CHANGED {{server}} added {{added}} removed {{removed}}</system-reminder>"
	}, func(o *bootstrap.Options) { o.MCP = mcp.NewClient(url, nil, nil) })
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	return h, svc, sid, ch
}

func requestBody(r *llm.Request) string {
	b, _ := json.Marshal(r.Messages)
	return string(b)
}

func TestMcpCommandAndMentionInAMessage(t *testing.T) {
	var last *llm.Request
	h, _, sid, ch := mcpSession(t, func(n int, r *llm.Request) fg.Reply {
		last = r
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	})
	cmds, err := h.client.ListMcpCommands(h.ctx, connect.NewRequest(&agentv1.ListMcpCommandsRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds.Msg.GetCommands()) != 1 || cmds.Msg.GetCommands()[0].GetCommand() != "/mcp__x__greet" || len(cmds.Msg.GetCommands()[0].GetArguments()) != 2 {
		t.Fatalf("commands %v", cmds.Msg)
	}
	res, err := h.client.ListMcpResources(h.ctx, connect.NewRequest(&agentv1.ListMcpResourcesRequest{SessionId: sid, Query: "hello"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Msg.GetResources()) != 1 || res.Msg.GetResources()[0].GetMention() != "@x:mem://hello" {
		t.Fatalf("resources %v", res.Msg)
	}

	h.send(sid, "/mcp__x__greet Ann warmly")
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetTurnFinished() != nil })
	body := requestBody(last)
	if !strings.Contains(body, "Say hello to Ann (warmly).") || strings.Contains(body, "/mcp__x__greet") {
		t.Fatalf("the command was not expanded: %s", body)
	}
	if !strings.Contains(body, "SERVERS SAY") || !strings.Contains(body, fakemcp.Instructions) {
		t.Fatalf("no server instructions: %s", body)
	}

	h.send(sid, "summarize @x:mem://hello please")
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetTurnFinished() != nil })
	if body := requestBody(last); !strings.Contains(body, "hello from a resource") {
		t.Fatalf("the mention was not attached: %s", body)
	}

	// A command without its required argument is refused where the user sees it, and no turn starts.
	_, err = h.client.SendUserMessage(h.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Content: []*llmv1.ContentBlock{{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{Text: "/mcp__x__greet"}}}}}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "missing argument") {
		t.Fatalf("missing argument: %v", err)
	}

	// Sessions this client does not run have nothing here.
	other, err := h.client.ListMcpCommands(h.ctx, connect.NewRequest(&agentv1.ListMcpCommandsRequest{SessionId: "nope"}))
	if err != nil || len(other.Msg.GetCommands()) != 0 {
		t.Fatalf("unknown session: %v %v", other, err)
	}
}

func TestMcpProgressAndListChangedEvents(t *testing.T) {
	h, _, sid, ch := mcpSession(t, func(n int, r *llm.Request) fg.Reply {
		switch n {
		case 0:
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("t1", "mcp__x__slow", map[string]any{}))}
		case 1:
			return fg.Reply{Events: fg.Message("m2", fg.ToolUse("t2", "mcp__x__grow", map[string]any{}))}
		default:
			return fg.Reply{Events: fg.Message("m3", fg.Text("done"))}
		}
	})
	if _, err := h.client.SetPermissionMode(h.ctx, connect.NewRequest(&agentv1.SetPermissionModeRequest{SessionId: sid, Mode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})); err != nil {
		t.Fatal(err)
	}
	h.send(sid, "go")
	var progress *agentv1.McpToolProgress
	var changed *agentv1.McpListChanged
	finished := false
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if r := ev.GetPermissionRequest(); r != nil {
			_, err := h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: r.GetRequestId(), Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW}))
			if err != nil {
				t.Error(err)
			}
		}
		if p := ev.GetMcpToolProgress(); p != nil && p.GetProgress() == 2 {
			progress = p
		}
		if c := ev.GetMcpListChanged(); c != nil {
			changed = c
		}
		finished = finished || ev.GetTurnFinished() != nil
		return finished && progress != nil && changed != nil
	})
	if progress.GetToolUseId() != "t1" || progress.GetTotal() != 2 || progress.GetMessage() != "step 2" {
		t.Fatalf("progress %v", progress)
	}
	if changed.GetServer() != "x" || changed.GetKind() != agentv1.McpListChanged_KIND_TOOLS {
		t.Fatalf("list changed %v", changed)
	}
}

func TestMcpElicitationIsAQuestion(t *testing.T) {
	var results []string
	h, _, sid, ch := mcpSession(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("t1", "mcp__x__ask", map[string]any{}))}
		}
		results = append(results, requestBody(r))
		return fg.Reply{Events: fg.Message("m2", fg.Text("done"))}
	})
	h.send(sid, "go")
	answered := 0
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if r := ev.GetPermissionRequest(); r != nil {
			if _, err := h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: r.GetRequestId(), Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW})); err != nil {
				t.Error(err)
			}
		}
		if q := ev.GetQuestion(); q != nil {
			answered++
			if q.GetItems()[0].GetHeader() != "MCP form" || !strings.Contains(q.GetItems()[0].GetQuestion(), "Tell me more") {
				t.Errorf("question %v", q)
			}
			_, err := h.client.AnswerQuestion(h.ctx, connect.NewRequest(&agentv1.AnswerQuestionRequest{SessionId: sid, RequestId: q.GetRequestId(), Answers: []*agentv1.QuestionAnswer{
				{Text: "33"}, {Selected: []string{"red"}}, {Skipped: true}, {Skipped: true}}}))
			if err != nil {
				t.Error(err)
			}
		}
		return ev.GetTurnFinished() != nil
	})
	if answered != 1 || len(results) != 1 {
		t.Fatalf("answered %d, requests %d", answered, len(results))
	}
	if !strings.Contains(results[0], `accept:{\"age\":33,\"colour\":\"red\"}`) {
		t.Fatalf("tool result: %s", results[0])
	}
}

func TestSettingsDeviceServerStaysOnTheDevice(t *testing.T) {
	svc := fakemcp.NewService()
	url := svc.Start(t)
	h := startHarnessSrv(t, func(int, *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("ok"))} }, nil, nil,
		func(s *server.Server) { s.MCPClient = mcp.NewClient(url, nil, nil) })
	exe, _ := os.Executable()
	created, err := h.client.CreateMcpServer(h.ctx, connect.NewRequest(&agentv1.CreateMcpServerRequest{Source: agentv1.McpServerSource_MCP_SERVER_SOURCE_DEVICE,
		Server:  &mcpv1.Server{Name: "dev", Transport: mcpv1.Transport_TRANSPORT_STDIO, Enabled: true, Stdio: &mcpv1.StdioConfig{Command: exe, Env: map[string]string{fakemcp.EnvMarker: "1", "T": "${secret:T}"}}},
		Secrets: &mcpv1.SecretChanges{Set: map[string]string{"T": "keep-me-local"}}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(svc.Created) != 0 {
		t.Fatalf("sent to the harness server: %v", svc.Created)
	}
	entry := created.Msg.GetEntry()
	id := entry.GetServer().GetId()
	data, err := os.ReadFile(filepath.Join(os.Getenv("HARNESS_HOME"), "mcp-local.json"))
	if err != nil || !strings.Contains(string(data), "keep-me-local") {
		t.Fatalf("the device file: %v", err)
	}

	list, err := h.client.ListMcpServers(h.ctx, connect.NewRequest(&agentv1.ListMcpServersRequest{}))
	if err != nil || len(list.Msg.GetServers()) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	if b, _ := json.Marshal(list.Msg); strings.Contains(string(b), "keep-me-local") {
		t.Fatal("the list returned a secret value")
	}
	got, err := h.client.InspectMcpServer(h.ctx, connect.NewRequest(&agentv1.InspectMcpServerRequest{ServerId: id}))
	if err != nil || !got.Msg.GetConnected() || len(got.Msg.GetTools()) != 3 {
		t.Fatalf("inspect %v %v", got, err)
	}
	if _, err := h.client.DeleteMcpServer(h.ctx, connect.NewRequest(&agentv1.DeleteMcpServerRequest{ServerId: id})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.InspectMcpServer(h.ctx, connect.NewRequest(&agentv1.InspectMcpServerRequest{ServerId: id})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("inspect of a deleted server: %v", err)
	}
}

func TestDeviceServerToolsReachASession(t *testing.T) {
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("ok"))} }, func(o *bootstrap.Options) {
		svc := fakemcp.NewService()
		o.MCP = mcp.NewClient(svc.Start(t), nil, nil)
	})
	// The store of the same HARNESS_HOME the session reads.
	exe, _ := os.Executable()
	if _, err := h.client.CreateMcpServer(h.ctx, connect.NewRequest(&agentv1.CreateMcpServerRequest{Source: agentv1.McpServerSource_MCP_SERVER_SOURCE_DEVICE,
		Server: &mcpv1.Server{Name: "dev", Transport: mcpv1.Transport_TRANSPORT_STDIO, Enabled: true, Stdio: &mcpv1.StdioConfig{Command: exe, Env: map[string]string{fakemcp.EnvMarker: "1"}}}})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.SetMcpToolRule(h.ctx, connect.NewRequest(&agentv1.SetMcpToolRuleRequest{Server: "dev", Tool: "fail", Rule: "deny", DeviceLocal: true})); err != nil {
		t.Fatal(err)
	}
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "hi")
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetTurnFinished() != nil })
	reqs := h.gw.Requests()
	names := map[string]bool{}
	for _, d := range reqs[len(reqs)-1].Tools {
		names[d.Name] = true
	}
	if !names["mcp__dev__echo"] {
		t.Fatalf("device server tool not offered: %v", names)
	}
	if names["mcp__dev__fail"] {
		t.Fatal("a device-local deny rule did not apply")
	}
}
