package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"

	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	"github.com/xlsft/harness/packages/client/mcp"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/subagent"
)

// agentMCP starts the MCP servers a subagent's definition brings. A definition is a file of a repository or a document off
// the server, so its servers get what a repository's .mcp.json gets: the user decides on this device, bound to what the
// server runs. Asking blocks the Agent call; with nobody to ask a server stays off unless HARNESS_MCP_APPROVE names it.
func (s *Session) agentMCP(o Options, log *slog.Logger) func(ctx context.Context, agentName string, servers map[string]json.RawMessage) (subagent.AgentServers, []string) {
	if o.NoMCP || os.Getenv("HARNESS_NO_MCP") == "1" {
		return nil
	}
	return func(ctx context.Context, agentName string, servers map[string]json.RawMessage) (subagent.AgentServers, []string) {
		var (
			approved []*mcpv1.Server
			notes    []string
		)
		store := &mcp.Approvals{Path: MCPApprovalsFile()}
		auto := strings.Split(os.Getenv("HARNESS_MCP_APPROVE"), ",")
		names := make([]string, 0, len(servers))
		for n := range servers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			cfg, err := mcp.PluginServer("agent", name, servers[name], nil)
			if err != nil {
				notes = append(notes, fmt.Sprintf("Your MCP server %s could not be read: %v.", name, err))
				continue
			}
			if !o.ModePolicy.Managed().McpServerAllowed(name) {
				notes = append(notes, fmt.Sprintf("Your MCP server %s is not allowed by the organization's policy.", name))
				continue
			}
			switch store.State(cfg.GetId(), cfg.GetConfigHash()) {
			case mcpv1.ApprovalState_APPROVAL_STATE_APPROVED:
			case mcpv1.ApprovalState_APPROVAL_STATE_DENIED:
				notes = append(notes, fmt.Sprintf("Your MCP server %s was refused on this device.", name))
				continue
			default:
				if !slices.Contains(auto, "*") && !slices.Contains(auto, name) {
					if why := s.askAgentServer(ctx, agentName, name, servers[name]); why != "" {
						notes = append(notes, fmt.Sprintf("Your MCP server %s did not start: %s", name, why))
						continue
					}
					if err := store.Set(cfg.GetId(), name, cfg.GetConfigHash(), true); err != nil {
						log.Warn("remember an agent's MCP server", "server", name, "err", err)
					}
				}
			}
			approved = append(approved, cfg)
		}
		if len(approved) == 0 {
			return nil, notes
		}
		m := mcp.OpenServers(ctx, mcp.Options{Cwd: s.Dir(), Disabled: s.Permissions.IsDisabled, SessionID: s.ID, Logger: log}, approved)
		notes = append(notes, m.Warnings()...)
		return m, notes
	}
}

// askAgentServer asks the user whether a server may run: only the user can answer. It returns why not ("": it may).
func (s *Session) askAgentServer(ctx context.Context, agentName, name string, entry json.RawMessage) string {
	d := s.Permissions.Check(ctx, permissions.Request{
		ToolName: "Agent", Label: "Start MCP server " + name, Input: entry, AlwaysAsk: true, AgentName: agentName,
	})
	if d.Behavior != permissions.Allow {
		return strings.TrimSpace(d.Message)
	}
	return ""
}

// agentSkillText reads a skill for a definition's skills field. A skill that is turned off, or that runs as an agent of its
// own, isn't handed over.
func (s *Session) agentSkillText(ctx context.Context, name string) (string, bool) {
	if s.Skills == nil {
		return "", false
	}
	sk, ok := s.Skills.Catalog(true).Lookup(name)
	if !ok || sk.Fork || !s.SkillEnabled(sk.Name) {
		return "", false
	}
	text, _ := s.skillText(ctx, sk, "", s.ToolEnv)
	return text, true
}

// promptCacheTTL is the promptCacheTtl setting ("": the provider's default).
func promptCacheTTL(st *settings.Settings) string {
	if st == nil {
		return ""
	}
	return st.Extras.PromptCacheTTL
}
