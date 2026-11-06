// Package mcp connects the agent to the user's MCP servers (docs/PLAN.md §1.5).
//
// Configs and authorization live on the server (harness.mcp.v1.McpService):
//   - remote servers (Streamable HTTP, SSE, WS) are called through the server's proxy, which holds the
//     provider tokens; the client never sees them;
//   - stdio servers run on this device. Each one needs a local approval of its config hash; the
//     resolved command and secrets are fetched with IssueStdioLaunch right before spawning and kept in
//     memory only.
//
// Servers can also be this device's own (LocalStore): stdio, Streamable HTTP or SSE, reached directly, kept in
// HARNESS_HOME and never mentioned to the xlsft`s harness server. Inventory is what Settings > MCP works on.
//
// Besides tools a session gets the servers' instructions (within a size budget), their resources (agent tools and
// @-mentions), their prompts (as /mcp__<server>__<prompt> commands), elicitations (through the question UI, not the
// conversation), progress, and list_changed notifications, which change the tool set while the session runs.
//
// Tools are exposed to the model as mcp__<server>__<tool>. When their definitions would take too much
// of the context window they are deferred: the model sees only their names and loads full schemas
// with the ToolSearch tool.
package mcp

import "strings"

// Prefix starts every MCP tool name.
const Prefix = "mcp__"

// NormalizeName replaces every character outside [A-Za-z0-9_-] with '_', the form used in tool names
// (model APIs accept only ^[a-zA-Z0-9_-]{1,64}$).
func NormalizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ToolName returns the model-facing name of tool on server: mcp__<server>__<tool>.
func ToolName(server, tool string) string {
	return ServerPrefix(server) + NormalizeName(tool)
}

// ServerPrefix returns "mcp__<server>__", the prefix of all tools of a server.
func ServerPrefix(server string) string { return Prefix + NormalizeName(server) + "__" }

// IsToolName reports whether name looks like an MCP tool name.
func IsToolName(name string) bool {
	rest, ok := strings.CutPrefix(name, Prefix)
	return ok && strings.Contains(rest, "__")
}
