package sources

import (
	"strings"

	"github.com/xlsft/harness/packages/client/importer"
)

// Cursor. Folder level only so far (the user-level reader of ~/.cursor is still to come).
//
// Project rules: .cursor/rules/**/*.mdc, frontmatter description, globs (comma-separated), alwaysApply
// (https://cursor.com/docs/context/rules). Plain .md files there are ignored by Cursor, so they are not read here.
// Legacy .cursorrules at the root: whole file, always on (the same page's legacy section; also read by Zed,
// https://zed.dev/docs/ai/instructions).
// MCP: .cursor/mcp.json, "mcpServers" with command/args/env/envFile or url/headers; ${env:NAME}, ${userHome},
// ${workspaceFolder} are expanded by Cursor (https://cursor.com/docs/context/mcp).

func cursorRule(f importer.FolderFile) importer.Found {
	md := string(f.Data)
	name := ruleName(f.Rel, ".cursor/rules/", ".mdc")
	if strings.EqualFold(fm(md, "alwaysApply"), "true") {
		return rule(name, md, nil, "")
	}
	return rule(name, md, globList(md, "globs"), "Cursor attached it when relevant or when asked")
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "cursor", Name: "Cursor", Logo: "cursor",
			Description: "A folder's Cursor rules, .cursorrules and .cursor/mcp.json."},
		patterns: []importer.FolderPattern{
			{Glob: ".cursor/rules/*.mdc", Read: one(cursorRule)},
			{Glob: ".cursor/rules/*/*.mdc", Read: one(cursorRule)},
			{Glob: ".cursor/rules/*/*/*.mdc", Read: one(cursorRule)},
			{Glob: ".cursorrules", Read: instructions},
			{Glob: ".cursor/mcp.json", Read: func(f importer.FolderFile) []importer.Found {
				return mcpFile(f.Data, "mcpServers", f.Rel, true)
			}},
		},
	})
}
