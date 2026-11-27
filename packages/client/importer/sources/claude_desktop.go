package sources

import (
	"os"
	"path/filepath"

	"github.com/xlsft/harness/packages/client/importer"
)

// Claude Desktop: the MCP servers of claude_desktop_config.json (its "mcpServers": command, args, env). Nothing else in
// the file is imported.
//
// Paths, from https://modelcontextprotocol.io/quickstart/user ("Installing the Filesystem Server"):
//
//	macOS:   ~/Library/Application Support/Claude/claude_desktop_config.json
//	Windows: %APPDATA%\Claude\claude_desktop_config.json
//	Linux:   ~/.config/Claude/claude_desktop_config.json

func claudeDesktopConfig(env importer.Env) string {
	return filepath.Join(env.UserConfigDir(), "Claude", "claude_desktop_config.json")
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "claude-desktop", Name: "Claude Desktop", Logo: "claude",
			Description: "MCP servers from claude_desktop_config.json, added on this device."},
		detect: func(env importer.Env) bool { return exists(claudeDesktopConfig(env)) },
		user: func(env importer.Env) []importer.Found {
			p := claudeDesktopConfig(env)
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			return mcpFile(data, "mcpServers", env.Display(p), false)
		},
	})
}
