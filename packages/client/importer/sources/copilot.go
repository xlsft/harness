package sources

import (
	"github.com/xlsft/harness/packages/client/importer"
)

// GitHub Copilot (VS Code). Folder level only so far.
//
// Instructions: .github/copilot-instructions.md (always on) and .github/instructions/*.instructions.md with an
// applyTo glob relative to the workspace root
// (https://code.visualstudio.com/docs/copilot/customization/custom-instructions).
// MCP: .vscode/mcp.json, JSON with comments, top-level "servers" with type stdio/http/sse, command/args/env/envFile or
// url/headers; ${input:id} and ${env:NAME} variables (https://code.visualstudio.com/docs/agents/reference/mcp-configuration).

func copilotInstructionsFile(f importer.FolderFile) importer.Found {
	md := string(f.Data)
	name := ruleName(f.Rel, ".github/instructions/", ".instructions.md")
	var globs []string
	for _, g := range globList(md, "applyTo") {
		if g != "**" && g != "**/*" {
			globs = append(globs, g)
		}
	}
	return rule(name, md, globs, "")
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "copilot", Name: "GitHub Copilot", Logo: "github",
			Description: "A folder's Copilot instructions and .vscode/mcp.json."},
		patterns: []importer.FolderPattern{
			{Glob: ".github/copilot-instructions.md", Read: instructions},
			{Glob: ".github/instructions/*.instructions.md", Read: one(copilotInstructionsFile)},
			{Glob: ".vscode/mcp.json", Read: func(f importer.FolderFile) []importer.Found {
				return mcpFile(f.Data, "servers", f.Rel, true)
			}},
		},
	})
}
