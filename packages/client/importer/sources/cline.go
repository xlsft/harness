package sources

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/xlsft/harness/packages/client/importer"
)

// Cline, Roo Code and Kilo Code (the VS Code agents of one family).
//
// Cline (https://docs.cline.bot/features/cline-rules, https://docs.cline.bot/customization/workflows,
// https://docs.cline.bot/mcp/configuring-mcp-servers):
//
//	rules: ~/Documents/Cline/Rules, ~/.cline/rules, ~/Cline/Rules; a folder's .clinerules/ or .cline/rules/; .md with an
//	  optional `paths:` list (the same field harness rules use); a single .clinerules file is the older form
//	workflows (invoked /name.md): ~/Documents/Cline/Workflows, the CLI's ~/.cline/data/workflows, a folder's
//	  .clinerules/workflows → commands
//	MCP: cline_mcp_settings.json "mcpServers" in the CLI's ~/.cline/data/settings and in the extension's globalStorage
//	  (saoudrizwan.claude-dev/settings); a bare url without type is SSE in Cline
//
// Roo Code (https://roocodeinc.github.io/Roo-Code/features/custom-instructions, …/features/custom-modes,
// …/features/mcp/using-mcp-in-roo):
//
//	rules: ~/.roo/rules/ (all modes) and ~/.roo/rules-{slug}/ (one mode), recursive, .md/.txt; a folder's .roo/rules*,
//	  legacy .roorules and .roorules-{slug}
//	modes: settings/custom_modes.yaml (or .json) in globalStorage rooveterinaryinc.roo-cline; a folder's .roomodes
//	  (YAML or JSON) → agents
//	MCP: settings/mcp_settings.json in the same globalStorage; a folder's .roo/mcp.json
//
// Kilo Code's legacy extension (https://kilocode.ai/docs/advanced-usage/custom-rules): ~/.kilocode/rules and rules-{mode};
// globalStorage kilocode.kilo-code/settings/custom_modes.yaml and mcp_settings.json (Roo's layout); a folder's
// .kilocode/rules, .kilocodemodes. The current Kilo config (kilo.jsonc) is read by the OpenCode reader's parser
// (opencode.go).

func pathsRule(prefix string) func(importer.FolderFile) importer.Found {
	return func(f importer.FolderFile) importer.Found {
		md := string(f.Data)
		return rule(ruleName(f.Rel, prefix, ".md", ".txt"), md, globList(md, "paths"), "")
	}
}

// modeRule is a rule of one mode of Roo or Kilo: harness has no modes, so it always applies.
func modeRule(tool string) func(importer.FolderFile) importer.Found {
	return func(f importer.FolderFile) importer.Found {
		// ".roo/rules-code/x.md" → "code/x"
		rel := f.Rel
		if f.Dir != "" {
			rel = rel[len(f.Dir)+1:]
		}
		_, after, _ := strings.Cut(rel, "/rules-")
		r := rule(ruleName(after, "", ".md", ".txt"), string(f.Data), nil, "")
		r.Note = tool + " used it in one mode only; here it always applies"
		return r
	}
}

// workflow is a Cline workflow: a markdown prompt invoked by its file name.
func workflow(md string) (string, string) {
	return frontmatter([][2]string{{"description", fm(md, "description")}}, nil, body(md)), ""
}

func clineWorkflowFile(f importer.FolderFile) importer.Found {
	text, note := workflow(string(f.Data))
	return importer.Found{Kind: importer.KindCommand, Name: ruleName(f.Rel, ".clinerules/workflows/", ".md"), Text: text, Note: note}
}

// --- modes ---------------------------------------------------------------------------------------------------------

type customMode struct {
	Slug               string `yaml:"slug" json:"slug"`
	Name               string `yaml:"name" json:"name"`
	Description        string `yaml:"description" json:"description"`
	RoleDefinition     string `yaml:"roleDefinition" json:"roleDefinition"`
	WhenToUse          string `yaml:"whenToUse" json:"whenToUse"`
	CustomInstructions string `yaml:"customInstructions" json:"customInstructions"`
	Groups             []any  `yaml:"groups" json:"groups"`
}

// modeGroups names a mode's tool groups; a restricted edit group ([edit, {fileRegex}]) names its pattern.
func modeGroups(groups []any) (names []string, edits bool) {
	for _, g := range groups {
		switch x := g.(type) {
		case string:
			names = append(names, x)
			edits = edits || x == "edit" || x == "command"
		case []any:
			if len(x) == 0 {
				continue
			}
			n, _ := x[0].(string)
			edits = edits || n == "edit" || n == "command"
			if len(x) > 1 {
				if opts, ok := x[1].(map[string]any); ok {
					if re, _ := opts["fileRegex"].(string); re != "" {
						n += " (" + re + ")"
					}
				}
			}
			names = append(names, n)
		}
	}
	return names, edits
}

// parseModes reads a custom modes file (YAML or JSON, top key customModes) into agents.
func parseModes(data []byte, source, tool string) []importer.Found {
	var doc struct {
		CustomModes []customMode `yaml:"customModes" json:"customModes"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		if jerr := json.Unmarshal(data, &doc); jerr != nil {
			return []importer.Found{{Kind: importer.KindAgent, Name: filepath.Base(source), Source: source, Status: importer.StatusError, Note: "invalid YAML: " + err.Error()}}
		}
	}
	var out []importer.Found
	for _, m := range doc.CustomModes {
		if m.Slug == "" {
			continue
		}
		groups, edits := modeGroups(m.Groups)
		desc := m.WhenToUse
		if desc == "" {
			desc = m.Description
		}
		readOnly := ""
		if !edits {
			readOnly = "true"
		}
		body := strings.TrimSpace(m.RoleDefinition)
		if ci := strings.TrimSpace(m.CustomInstructions); ci != "" {
			body += "\n\n" + ci
		}
		f := importer.Found{Kind: importer.KindAgent, Name: m.Slug, Source: source,
			Text: frontmatter([][2]string{{"name", m.Slug}, {"description", desc}, {"read_only", readOnly}}, nil, body)}
		note := fmt.Sprintf("the %s mode %q", tool, m.Name)
		if len(groups) > 0 {
			note += "; its tool groups were " + strings.Join(groups, ", ")
		}
		f.Note = note
		out = append(out, f)
	}
	return out
}

func modesFile(tool string) func(importer.FolderFile) []importer.Found {
	return func(f importer.FolderFile) []importer.Found { return parseModes(f.Data, f.Rel, tool) }
}

// userModes reads custom_modes.yaml (or .json) in a settings folder.
func userModes(env importer.Env, settings, tool string) []importer.Found {
	for _, n := range []string{"custom_modes.yaml", "custom_modes.json"} {
		p := filepath.Join(settings, n)
		data, ok, err := readUser(p)
		if !ok {
			continue
		}
		if err != nil {
			return []importer.Found{failed(env, importer.KindAgent, n, p, err)}
		}
		return parseModes(data, env.Display(p), tool)
	}
	return nil
}

// --- user levels ---------------------------------------------------------------------------------------------------

func clineUser(env importer.Env) []importer.Found {
	h := env.Home
	var out []importer.Found
	for _, d := range []string{filepath.Join(h, "Documents", "Cline", "Rules"), filepath.Join(h, ".cline", "rules"), filepath.Join(h, "Cline", "Rules")} {
		out = append(out, userRules(env, d, "", "", ".md", ".txt")...)
	}
	for _, d := range []string{filepath.Join(h, "Documents", "Cline", "Workflows"), filepath.Join(h, ".cline", "data", "workflows")} {
		out = append(out, userMarkdown(env, d, ".md", false, importer.KindCommand, func(rel string) string { return trimExt(rel, ".md") }, workflow)...)
	}
	out = append(out, sseByDefault(userMCP(env, filepath.Join(h, ".cline", "data", "settings", "cline_mcp_settings.json"), "mcpServers", true))...)
	for _, g := range vscodeGlobalStorage(env, "saoudrizwan.claude-dev") {
		out = append(out, sseByDefault(userMCP(env, filepath.Join(g, "settings", "cline_mcp_settings.json"), "mcpServers", true))...)
	}
	return dedupe(out)
}

// rooFamilyUser reads one tool of Roo's layout: home rules folder (rules/ and rules-{slug}/), globalStorage modes and MCP.
func rooFamilyUser(env importer.Env, tool, home, extension string) []importer.Found {
	var out []importer.Found
	out = append(out, userRules(env, filepath.Join(home, "rules"), "", "", ".md", ".txt")...)
	if entries, err := readDirNames(home); err == nil {
		for _, n := range entries {
			if slug, ok := strings.CutPrefix(n, "rules-"); ok && slug != "" {
				out = append(out, userRules(env, filepath.Join(home, n), slug+"/", tool+" used it in one mode only; here it always applies", ".md", ".txt")...)
			}
		}
	}
	for _, g := range vscodeGlobalStorage(env, extension) {
		settings := filepath.Join(g, "settings")
		out = append(out, userModes(env, settings, tool)...)
		out = append(out, userMCP(env, filepath.Join(settings, "mcp_settings.json"), "mcpServers", true)...)
	}
	return dedupe(out)
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "cline", Name: "Cline", Logo: "cline",
			Description: "Global rules, workflows and MCP servers of Cline; a folder's Cline rules and workflows."},
		detect: func(env importer.Env) bool {
			return anyExists(filepath.Join(env.Home, "Documents", "Cline"), filepath.Join(env.Home, ".cline"), filepath.Join(env.Home, "Cline")) ||
				len(vscodeGlobalStorage(env, "saoudrizwan.claude-dev")) > 0
		},
		user: clineUser,
		patterns: []importer.FolderPattern{
			{Glob: ".clinerules/*.md", Read: one(pathsRule(".clinerules/"))},
			{Glob: ".clinerules/*.txt", Read: one(pathsRule(".clinerules/"))},
			{Glob: ".clinerules/workflows/*.md", Read: one(clineWorkflowFile)},
			{Glob: ".cline/rules/*.md", Read: one(pathsRule(".cline/rules/"))},
			{Glob: ".clinerules", Read: instructions},
		},
	})
	register(&reader{
		info: importer.SourceInfo{ID: "roo", Name: "Roo Code", Logo: "roo",
			Description: "Global rules, custom modes and MCP servers of Roo Code; a folder's rules, .roomodes and .roo/mcp.json."},
		detect: func(env importer.Env) bool {
			return exists(filepath.Join(env.Home, ".roo")) || len(vscodeGlobalStorage(env, "rooveterinaryinc.roo-cline")) > 0
		},
		user: func(env importer.Env) []importer.Found {
			return rooFamilyUser(env, "Roo Code", filepath.Join(env.Home, ".roo"), "rooveterinaryinc.roo-cline")
		},
		patterns: []importer.FolderPattern{
			{Glob: ".roo/rules/*", Read: one(pathsRule(".roo/rules/"))},
			{Glob: ".roo/rules-*/*", Read: one(modeRule("Roo Code"))},
			{Glob: ".roorules", Read: instructions},
			{Glob: ".roorules-*", Read: func(f importer.FolderFile) []importer.Found {
				out := instructions(f)
				out[0].Note = "Roo Code used it in one mode only; here it always applies"
				return out
			}},
			{Glob: ".roomodes", Read: modesFile("Roo Code")},
			{Glob: ".roo/mcp.json", Read: func(f importer.FolderFile) []importer.Found {
				return mcpFile(f.Data, "mcpServers", f.Rel, true)
			}},
		},
	})
}
