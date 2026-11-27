package sources

import (
	"path/filepath"

	"github.com/xlsft/harness/packages/client/importer"
)

// JetBrains Junie (https://junie.jetbrains.com/docs/guidelines-and-memory.html,
// https://junie.jetbrains.com/docs/junie-cli-mcp-configuration.html).
//
// User level: ~/.junie/AGENTS.md (global guidelines) → instructions; ~/.junie/mcp/mcp.json "mcpServers" → MCP servers.
// Folder level: .junie/guidelines.md (the IDE plugin's default) and .junie/AGENTS.md; .junie/mcp/mcp.json.

func junieUser(env importer.Env) []importer.Found {
	d := filepath.Join(env.Home, ".junie")
	out := userInstructions(env, filepath.Join(d, "AGENTS.md"), "AGENTS.md")
	return append(out, userMCP(env, filepath.Join(d, "mcp", "mcp.json"), "mcpServers", true)...)
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "junie", Name: "Junie", Logo: "junie",
			Description: "Global guidelines and MCP servers from ~/.junie; a folder's Junie guidelines and MCP servers."},
		detect: func(env importer.Env) bool { return exists(filepath.Join(env.Home, ".junie")) },
		user:   junieUser,
		patterns: []importer.FolderPattern{
			{Glob: ".junie/guidelines.md", Read: instructions},
			{Glob: ".junie/AGENTS.md", Read: instructions},
			{Glob: ".junie/mcp/mcp.json", Read: func(f importer.FolderFile) []importer.Found {
				return mcpFile(f.Data, "mcpServers", f.Rel, true)
			}},
		},
	})
}
