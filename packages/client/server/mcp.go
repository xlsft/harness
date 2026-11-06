package server

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/mcp"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Settings > MCP and the composer's MCP commands and mentions. The logic is in mcp.Inventory and mcp.Manager; this file
// connects them to AgentService. OAuth and the audit log are forwarded to the server (coreproxy.go).

var (
	mcpInventoryMu sync.Mutex
)

// mcpInventory returns the inventory, built on first use.
func (s *Server) mcpInventory() *mcp.Inventory {
	mcpInventoryMu.Lock()
	defer mcpInventoryMu.Unlock()
	if s.MCPInventory == nil {
		s.MCPInventory = &mcp.Inventory{Client: s.MCPClient, PolicyAllows: func(name string) bool { return s.Base.ModePolicy.Managed().McpServerAllowed(name) }}
	}
	inv := s.MCPInventory
	if inv.Local == nil {
		inv.Local = &mcp.LocalStore{Path: bootstrap.MCPLocalFile()}
	}
	if inv.Approvals == nil {
		inv.Approvals = &mcp.Approvals{Path: bootstrap.MCPApprovalsFile()}
	}
	if inv.Project == nil {
		inv.Project = func(ctx context.Context, folder string) mcp.ProjectInfo {
			abs, err := filepath.Abs(folder)
			if err != nil {
				return mcp.ProjectInfo{}
			}
			return mcp.ProjectInfo{Root: permissions.RepoRoot(abs), Key: workspace.ProjectKey(ctx, abs), Trusted: permissions.DefaultTrustStore().TrustedRoot(abs) != ""}
		}
	}
	if inv.Rules == nil {
		inv.Rules = s.mcpSyncedRules
	}
	return inv
}

// mcpSyncedRules reads the permission rules of the user's synced settings.
func (s *Server) mcpSyncedRules(ctx context.Context) (allow, ask, deny []string) {
	if s.Preferences == nil {
		return nil, nil, nil
	}
	doc, err := s.userSettings(ctx)
	if err != nil {
		return nil, nil, nil
	}
	p := readPermissions(doc)
	return stringList(p["allow"]), stringList(p["ask"]), stringList(p["deny"])
}

func readPermissions(doc *syncv1.Document) map[string]any {
	var raw struct {
		Permissions map[string]any `json:"permissions"`
	}
	_ = json.Unmarshal([]byte(doc.GetContent()), &raw)
	if raw.Permissions == nil {
		return map[string]any{}
	}
	return raw.Permissions
}

func stringList(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (s *Server) ListMcpServers(ctx context.Context, req *connect.Request[agentv1.ListMcpServersRequest]) (*connect.Response[agentv1.ListMcpServersResponse], error) {
	return connect.NewResponse(s.mcpInventory().List(ctx, req.Msg.GetFolder())), nil
}

func (s *Server) InspectMcpServer(ctx context.Context, req *connect.Request[agentv1.InspectMcpServerRequest]) (*connect.Response[agentv1.InspectMcpServerResponse], error) {
	out, err := s.mcpInventory().Inspect(ctx, req.Msg.GetServerId(), req.Msg.GetFolder())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(out), nil
}

func (s *Server) CreateMcpServer(ctx context.Context, req *connect.Request[agentv1.CreateMcpServerRequest]) (*connect.Response[agentv1.CreateMcpServerResponse], error) {
	e, err := s.mcpInventory().Create(ctx, req.Msg.GetSource(), req.Msg.GetServer(), req.Msg.GetSecrets())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.CreateMcpServerResponse{Entry: e}), nil
}

func (s *Server) UpdateMcpServer(ctx context.Context, req *connect.Request[agentv1.UpdateMcpServerRequest]) (*connect.Response[agentv1.UpdateMcpServerResponse], error) {
	e, conflict, err := s.mcpInventory().Update(ctx, req.Msg.GetServer(), req.Msg.GetSecrets(), req.Msg.GetBaseVersion())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.UpdateMcpServerResponse{Entry: e, Conflict: conflict}), nil
}

func (s *Server) DeleteMcpServer(ctx context.Context, req *connect.Request[agentv1.DeleteMcpServerRequest]) (*connect.Response[agentv1.DeleteMcpServerResponse], error) {
	if err := s.mcpInventory().Delete(ctx, req.Msg.GetServerId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.DeleteMcpServerResponse{}), nil
}

func (s *Server) SetMcpServerApproval(ctx context.Context, req *connect.Request[agentv1.SetMcpServerApprovalRequest]) (*connect.Response[agentv1.SetMcpServerApprovalResponse], error) {
	e, err := s.mcpInventory().SetApproval(ctx, req.Msg.GetServerId(), req.Msg.GetFolder(), req.Msg.GetApproved())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.SetMcpServerApprovalResponse{Entry: e}), nil
}

// SetMcpToolRule writes a permission rule. Rules about a device-local server stay on this device; the others go into
// the user's synced settings, next to the rules from "allow always".
func (s *Server) SetMcpToolRule(ctx context.Context, req *connect.Request[agentv1.SetMcpToolRuleRequest]) (*connect.Response[agentv1.SetMcpToolRuleResponse], error) {
	m := req.Msg
	if m.GetServer() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("server is required"))
	}
	decision := m.GetRule()
	if decision != "" && decision != "allow" && decision != "ask" && decision != "deny" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(`rule must be "allow", "ask", "deny" or empty`))
	}
	rule := mcp.RuleString(m.GetServer(), m.GetTool())
	if m.GetDeviceLocal() {
		if err := s.mcpInventory().Local.SetRule(rule, decision); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return connect.NewResponse(&agentv1.SetMcpToolRuleResponse{}), nil
	}
	if s.Preferences == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("preferences are stored on the xlsft`s harness server; this client is not connected to one"))
	}
	ctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
	defer cancel()
	doc, err := s.userSettings(ctx)
	if err != nil {
		return nil, syncError(err)
	}
	for range preferenceAttempts {
		perms := readPermissions(doc)
		for _, key := range []string{"allow", "ask", "deny"} {
			list := slices.DeleteFunc(stringList(perms[key]), func(x string) bool { return x == rule })
			if key == decision {
				list = append(list, rule)
			}
			if len(list) == 0 {
				delete(perms, key)
			} else {
				perms[key] = list
			}
		}
		content, err := mergeSettingsJSON(doc.GetContent(), map[string]any{"permissions": perms})
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		res, err := s.Preferences.PutDocument(ctx, &syncv1.PutDocumentRequest{Ref: userSettingsRef, Content: content, BaseVersion: proto.Int64(doc.GetVersion()), Source: "client:mcp-rule"})
		if err != nil {
			return nil, syncError(err)
		}
		if res.GetDocument() != nil {
			return connect.NewResponse(&agentv1.SetMcpToolRuleResponse{}), nil
		}
		doc = res.GetConflict().GetDocument()
	}
	return nil, connect.NewError(connect.CodeAborted, errors.New("the user settings document kept changing; try again"))
}

// ListMcpCommands lists the session's prompts as commands. A session this client does not run (on another machine, on
// an ACP agent) has none here.
func (s *Server) ListMcpCommands(ctx context.Context, req *connect.Request[agentv1.ListMcpCommandsRequest]) (*connect.Response[agentv1.ListMcpCommandsResponse], error) {
	out := &agentv1.ListMcpCommandsResponse{}
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return connect.NewResponse(out), nil
	}
	for _, c := range ls.sess.MCP.Commands(ctx) {
		cmd := &agentv1.McpCommand{Command: c.Command, Server: c.Server, Prompt: c.Prompt, Title: c.Title, Description: c.Description}
		for _, a := range c.Arguments {
			cmd.Arguments = append(cmd.Arguments, &agentv1.McpPromptArgument{Name: a.Name, Description: a.Description, Required: a.Required})
		}
		out.Commands = append(out.Commands, cmd)
	}
	return connect.NewResponse(out), nil
}

// ListMcpResources lists the session's resources for mentions; see ListMcpCommands about other sessions.
func (s *Server) ListMcpResources(ctx context.Context, req *connect.Request[agentv1.ListMcpResourcesRequest]) (*connect.Response[agentv1.ListMcpResourcesResponse], error) {
	out := &agentv1.ListMcpResourcesResponse{}
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return connect.NewResponse(out), nil
	}
	for _, r := range ls.sess.MCP.Mentions(ctx, req.Msg.GetQuery(), int(req.Msg.GetLimit())) {
		out.Resources = append(out.Resources, &agentv1.McpResourceMention{Mention: r.Mention, Server: r.Server, Uri: r.URI, Name: r.Name, Description: r.Description, MimeType: r.MimeType})
	}
	return connect.NewResponse(out), nil
}

// expandMcpMessage turns a "/mcp__server__prompt args" message into the prompt's content, and adds the content of the
// resources the message mentions. Everything else stays as it is.
func (ls *liveSession) expandMcpMessage(ctx context.Context, content []llm.ContentBlock) ([]llm.ContentBlock, error) {
	if ls.sess.MCP == nil {
		return content, nil
	}
	text := ""
	for _, b := range content {
		if b.Type == llm.BlockText {
			text = b.Text
			break
		}
	}
	if name, args, ok := mcp.ParseCommand(text); ok {
		blocks, handled, err := ls.sess.MCP.ExpandCommand(ctx, name, args)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		if handled {
			rest := slices.DeleteFunc(slices.Clone(content), func(b llm.ContentBlock) bool {
				return b.Type == llm.BlockText && strings.TrimSpace(b.Text) == strings.TrimSpace(text)
			})
			return append(blocks, rest...), nil
		}
	}
	if extra := ls.sess.MCP.ExpandMentions(ctx, text); len(extra) > 0 {
		return append(slices.Clone(content), extra...), nil
	}
	return content, nil
}

// watchMcp sends a running session's MCP progress and list changes to its frontends.
func (ls *liveSession) watchMcp() {
	ls.sess.MCP.UpdateListeners(func(l *mcp.Listeners) {
		l.Progress = func(toolUseID string, progress, total float64, message string) {
			ls.hub.publishEphemeral(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_McpToolProgress{McpToolProgress: &agentv1.McpToolProgress{ToolUseId: toolUseID, Progress: progress, Total: total, Message: message}}})
		}
		l.ListChanged = func(server, kind string) {
			k := agentv1.McpListChanged_KIND_TOOLS
			switch kind {
			case "prompts":
				k = agentv1.McpListChanged_KIND_PROMPTS
			case "resources":
				k = agentv1.McpListChanged_KIND_RESOURCES
			}
			ls.hub.publishEphemeral(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_McpListChanged{McpListChanged: &agentv1.McpListChanged{Server: server, Kind: k}}})
		}
	})
}
