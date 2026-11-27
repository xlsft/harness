package sources

import (
	"github.com/xlsft/harness/packages/client/importer"
)

// Gemini CLI. Folder level only so far (~/.gemini is still to come).
//
// Context: GEMINI.md in the workspace and its subfolders; the name is configurable with context.fileName
// (https://geminicli.com/docs/cli/gemini-md/). Only the default name is looked for here.
// MCP: .gemini/settings.json "mcpServers": command/args/env/cwd, url (SSE), httpUrl (Streamable HTTP), headers;
// $VAR and ${VAR} in values (https://geminicli.com/docs/tools/mcp-server/).

func geminiMCP(f importer.FolderFile) []importer.Found {
	out := mcpFile(f.Data, "mcpServers", f.Rel, false)
	for i := range out {
		// A bare url is SSE for Gemini; httpUrl was already read as Streamable HTTP (Transport "http").
		if m := out[i].MCP; m != nil && m.Transport == "" && m.Command == "" && m.URL != "" {
			m.Transport = "sse"
		}
	}
	return out
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "gemini", Name: "Gemini CLI", Logo: "gemini",
			Description: "A folder's GEMINI.md files and .gemini/settings.json MCP servers."},
		patterns: []importer.FolderPattern{
			{Glob: "GEMINI.md", Read: instructions},
			{Glob: ".gemini/settings.json", Read: geminiMCP},
		},
	})
}
