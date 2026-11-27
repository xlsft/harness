package sources

import (
	"path/filepath"
	"strings"

	"github.com/xlsft/harness/packages/client/importer"
)

// Windsurf (Cascade, now Devin Desktop).
//
// User level (https://docs.devin.ai/desktop/devin-local/customizations):
//
//	~/.devin/global_rules.md, else the legacy ~/.windsurf/global_rules.md, else Windsurf's older
//	  ~/.codeium/windsurf/memories/global_rules.md → instructions (only the first that exists loads)
//	~/.devin/rules/*.md and ~/.windsurf/rules/*.md → rules (trigger frontmatter, as in a folder)
//	~/.config/devin/skills/<name>/SKILL.md, legacy ~/.codeium/windsurf/skills → skills (~/.agents/skills is Codex's reader)
//	~/.config/devin/mcp_config.json, legacy ~/.codeium/windsurf/mcp_config.json → MCP servers (serverUrl is HTTP)
//	~/.codeium/windsurf/global_workflows/*.md → commands (Windsurf's /workflow-name)
//
// Folder level:
//
// Workspace rules: .devin/rules/*.md (native) and .windsurf/rules/*.md, one rule per file, frontmatter trigger
// (always_on, glob, model_decision, manual) and globs; legacy .windsurfrules at the root
// (https://docs.devin.ai/desktop/cascade/memories, formerly https://docs.windsurf.com/windsurf/cascade/memories).

func windsurfRule(prefix string) func(importer.FolderFile) importer.Found {
	return func(f importer.FolderFile) importer.Found {
		md := string(f.Data)
		name := ruleName(f.Rel, prefix, ".md")
		switch fm(md, "trigger") {
		case "", "always_on":
			return rule(name, md, nil, "")
		case "glob":
			return rule(name, md, globList(md, "globs"), "")
		case "manual":
			return rule(name, md, nil, "Windsurf used it only when mentioned")
		}
		return rule(name, md, nil, "Windsurf used it when it judged it relevant")
	}
}

func windsurfUser(env importer.Env) []importer.Found {
	h := env.Home
	codeium := filepath.Join(h, ".codeium", "windsurf")
	var out []importer.Found
	for _, p := range []string{filepath.Join(h, ".devin", "global_rules.md"), filepath.Join(h, ".windsurf", "global_rules.md"), filepath.Join(codeium, "memories", "global_rules.md")} {
		if exists(p) {
			out = append(out, userInstructions(env, p, "global_rules.md")...)
			break
		}
	}
	for _, dir := range []string{filepath.Join(h, ".devin", "rules"), filepath.Join(h, ".windsurf", "rules")} {
		for _, rel := range userFiles(dir, ".md", false) {
			p := filepath.Join(dir, rel)
			data, _, err := readUser(p)
			if err != nil {
				out = append(out, failed(env, importer.KindRule, trimExt(rel, ".md"), p, err))
				continue
			}
			f := windsurfRule("")(importer.FolderFile{Rel: rel, Data: data})
			f.Source = env.Display(p)
			out = append(out, f)
		}
	}
	out = append(out, userSkills(env, filepath.Join(env.UserConfigDirXDG(), "devin", "skills"))...)
	out = append(out, userSkills(env, filepath.Join(codeium, "skills"))...)
	if p := filepath.Join(env.UserConfigDirXDG(), "devin", "mcp_config.json"); exists(p) {
		out = append(out, userMCP(env, p, "mcpServers", true)...)
	} else {
		out = append(out, userMCP(env, filepath.Join(codeium, "mcp_config.json"), "mcpServers", true)...)
	}
	out = append(out, userMarkdown(env, filepath.Join(codeium, "global_workflows"), ".md", false, importer.KindCommand,
		func(rel string) string { return strings.ToLower(trimExt(rel, ".md")) },
		func(md string) (string, string) {
			return frontmatter([][2]string{{"description", fm(md, "description")}}, nil, body(md)), ""
		})...)
	return out
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "windsurf", Name: "Windsurf", Logo: "windsurf",
			Description: "Global rules, rules, skills, MCP servers and workflows of Windsurf and Devin Desktop; a folder's Windsurf rules and .windsurfrules."},
		detect: func(env importer.Env) bool {
			return anyExists(filepath.Join(env.Home, ".codeium", "windsurf"), filepath.Join(env.Home, ".devin"), filepath.Join(env.Home, ".windsurf"),
				filepath.Join(env.UserConfigDirXDG(), "devin"))
		},
		user: windsurfUser,
		patterns: []importer.FolderPattern{
			{Glob: ".windsurf/rules/*.md", Read: one(windsurfRule(".windsurf/rules/"))},
			{Glob: ".devin/rules/*.md", Read: one(windsurfRule(".devin/rules/"))},
			{Glob: ".windsurfrules", Read: instructions},
		},
	})
}
