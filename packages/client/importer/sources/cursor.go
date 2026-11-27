package sources

import (
	"path/filepath"
	"strings"

	"github.com/xlsft/harness/packages/client/importer"
)

// Cursor.
//
// User level: ~/.cursor/mcp.json "mcpServers" (https://cursor.com/docs/context/mcp); ~/.cursor/skills/<name>/SKILL.md
// (https://cursor.com/docs/context/skills; ~/.agents/skills is Codex's reader). User Rules live in Cursor's settings, not
// in a file ("User rules ... are not stored on the file system", https://cursor.com/docs/context/rules): listed to copy
// by hand.
//
// Folder level:
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

func cursorUser(env importer.Env) []importer.Found {
	d := filepath.Join(env.Home, ".cursor")
	out := userMCP(env, filepath.Join(d, "mcp.json"), "mcpServers", true)
	out = append(out, userSkills(env, filepath.Join(d, "skills"))...)
	out = append(out, importer.Found{Kind: importer.KindInstructions, Name: "User Rules", Source: "Cursor Settings → Rules", Status: importer.StatusNoted,
		Note: "Cursor keeps User Rules in its settings, not in a file: copy them into a rule by hand"})
	return out
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "cursor", Name: "Cursor", Logo: "cursor",
			Description: "MCP servers and skills from ~/.cursor; a folder's Cursor rules, .cursorrules and .cursor/mcp.json."},
		detect: func(env importer.Env) bool { return exists(filepath.Join(env.Home, ".cursor")) },
		user:   cursorUser,
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
