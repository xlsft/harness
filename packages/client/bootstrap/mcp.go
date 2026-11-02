package bootstrap

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/mcp"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/workspace"
)

// MCPApprovalsFile stores this device's stdio MCP server approvals.
func MCPApprovalsFile() string { return filepath.Join(config.Home(), "mcp-approvals.json") }

// openMCP loads the user's MCP servers and registers their tools (and ToolSearch when tools are
// deferred). Called after the permission engine exists, before the disabled-tool set is computed.
func (s *Session) openMCP(ctx context.Context, o Options, contextWindow int, conv session.Conversation, log *slog.Logger) {
	if o.MCP == nil || o.NoMCP || os.Getenv("HARNESS_NO_MCP") == "1" {
		return
	}
	projectConfig := ""
	if s.Trusted {
		root := workspace.GitRoot(ctx, s.Cwd)
		if root == "" {
			root = s.Cwd
		}
		projectConfig = filepath.Join(root, ".mcp.json")
	}
	var autoApprove []string
	if v := os.Getenv("HARNESS_MCP_APPROVE"); v != "" {
		autoApprove = strings.Split(v, ",")
	}
	catalog := ""
	if s.Resumed {
		catalog = conv.McpCatalogVersion
	}
	m, err := mcp.Open(ctx, mcp.Options{
		Client: o.MCP, Cwd: s.Cwd, ProjectKey: workspace.ProjectKey(ctx, s.Cwd), ProjectConfig: projectConfig,
		Approvals: &mcp.Approvals{Path: MCPApprovalsFile()}, AutoApprove: autoApprove, CatalogVersion: catalog,
		ContextWindow: contextWindow, ToolSearch: os.Getenv("HARNESS_TOOL_SEARCH"), Bundle: s.Bundle,
		Disabled: s.Permissions.IsDisabled, SessionID: s.ID, Logger: log,
	})
	if err != nil {
		log.Warn("MCP disabled", "err", err)
		return
	}
	s.MCP = m
	for _, t := range m.Tools() {
		s.Tools.Register(t)
	}
	s.Warnings = append(s.Warnings, m.Warnings()...)
}

// mcpExtraTools is the agent's ExtraTools hook (nil without MCP).
func (s *Session) mcpExtraTools() func() []llm.ToolDef {
	if s.MCP == nil {
		return nil
	}
	return s.MCP.ToolDefs
}

// startMCP runs once the agent exists: it restores deferred tools a resumed conversation already
// loaded, records the catalog snapshot and lists the deferred tools for the model.
func (s *Session) startMCP(conv session.Conversation) {
	if s.MCP == nil {
		return
	}
	s.MCP.RestoreLoaded(conv.Messages)
	v := s.MCP.CatalogVersion()
	changed := v != conv.McpCatalogVersion
	if v != "" && changed {
		_, _ = s.Transcript.Append(session.Entry{Type: session.TypeMeta, McpCatalogVersion: v})
	}
	if vars := s.MCP.DeferredReminder(); vars != nil && (!s.Resumed || changed) {
		s.Agent.Remind(mcp.ReminderDeferredTools, vars)
	}
}
