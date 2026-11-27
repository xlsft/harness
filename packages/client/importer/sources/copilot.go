package sources

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xlsft/harness/packages/client/importer"
)

// GitHub Copilot (VS Code and the Copilot CLI).
//
// User level, two families:
//
//	$COPILOT_HOME (default ~/.copilot): copilot-instructions.md → instructions; instructions/**/*.instructions.md → rules
//	  (applyTo → paths); agents/*.agent.md → agents; skills/<name>/SKILL.md → skills; mcp-config.json "mcpServers"
//	  (https://code.visualstudio.com/docs/agents/reference/custom-instructions-reference.md,
//	  https://code.visualstudio.com/docs/agent-customization/migrate-customizations.md)
//	VS Code's user data, each profile (<config>/Code[ - Insiders]/User and its profiles/<id>):
//	  prompts/*.prompt.md → commands (${input:x} → $ARGUMENTS, or named arguments when there are several);
//	  prompts/*.instructions.md → rules; prompts/*.agent.md and legacy *.chatmode.md → agents;
//	  mcp.json "servers" (JSONC; ${input:id} become secret references)
//	  (https://code.visualstudio.com/docs/agents/reference/mcp-configuration.md)
//
// Folder level:
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

// copilotRule is an *.instructions.md file at user level: applyTo limits it.
func copilotRule(md string) (string, string) {
	var globs []string
	for _, g := range globList(md, "applyTo") {
		if g != "**" && g != "**/*" {
			globs = append(globs, g)
		}
	}
	return harnessRule(globs, body(md)), ""
}

var vscodeInput = regexp.MustCompile(`\$\{input:([A-Za-z0-9_-]+)(?::[^}]*)?\}`)
var vscodeVars = regexp.MustCompile(`\$\{(file|selection|selectedText|workspaceFolder|workspaceFolderBasename|fileBasename|fileDirname|fileBasenameNoExtension)\}`)

// copilotPrompt turns a prompt file into a command: with one input it is $ARGUMENTS, with several each is a named
// argument, given in order. VS Code's editor variables have no equivalent and stay as written.
func copilotPrompt(md string) (string, string) {
	text := body(md)
	var names []string
	seen := map[string]bool{}
	for _, m := range vscodeInput.FindAllStringSubmatch(text, -1) {
		if n := strings.ReplaceAll(m[1], "-", "_"); !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	var lists map[string][]string
	var notes []string
	switch len(names) {
	case 0:
	case 1:
		text = vscodeInput.ReplaceAllString(text, "$$ARGUMENTS")
	default:
		text = vscodeInput.ReplaceAllStringFunc(text, func(m string) string {
			return "$" + strings.ReplaceAll(vscodeInput.FindStringSubmatch(m)[1], "-", "_")
		})
		lists = map[string][]string{"arguments": names}
		notes = append(notes, "its inputs "+strings.Join(names, ", ")+" are given in order")
	}
	if vscodeVars.MatchString(text) {
		notes = append(notes, "VS Code's editor variables (${file}, ${selection}, …) stay as written")
	}
	for _, k := range []string{"agent", "mode", "model", "tools"} {
		if fm(md, k) != "" || len(globList(md, k)) > 0 {
			notes = append(notes, "its "+k+" setting is not carried over")
		}
	}
	return frontmatter([][2]string{{"description", fm(md, "description")}, {"argument-hint", fm(md, "argument-hint")}}, lists, text), strings.Join(notes, "; ")
}

// copilotAgent keeps an agent file's name and description; its tools and model are VS Code's.
func copilotAgent(md string) (string, string) {
	note := ""
	if len(globList(md, "tools")) > 0 || fm(md, "model") != "" {
		note = "its tools and model are VS Code's and were left out"
	}
	return frontmatter([][2]string{{"name", fm(md, "name")}, {"description", fm(md, "description")}}, nil, body(md)), note
}

// vscodeUserDirs are VS Code's user folders with their profiles.
func vscodeUserDirs(env importer.Env) []string {
	var out []string
	for _, app := range []string{"Code", "Code - Insiders"} {
		u := filepath.Join(env.UserConfigDir(), app, "User")
		if !exists(u) {
			continue
		}
		out = append(out, u)
		if entries, err := os.ReadDir(filepath.Join(u, "profiles")); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					out = append(out, filepath.Join(u, "profiles", e.Name()))
				}
			}
		}
	}
	return out
}

func copilotUser(env importer.Env) []importer.Found {
	d := env.Copilot()
	byName := func(ext string) func(string) string { return func(rel string) string { return trimExt(rel, ext) } }
	out := userInstructions(env, filepath.Join(d, "copilot-instructions.md"), "copilot-instructions.md")
	out = append(out, userMarkdown(env, filepath.Join(d, "instructions"), ".instructions.md", true, importer.KindRule, byName(".instructions.md"), copilotRule)...)
	out = append(out, userMarkdown(env, filepath.Join(d, "agents"), ".agent.md", false, importer.KindAgent, byName(".agent.md"), copilotAgent)...)
	out = append(out, userSkills(env, filepath.Join(d, "skills"))...)
	out = append(out, userMCP(env, filepath.Join(d, "mcp-config.json"), "mcpServers", true)...)
	for _, u := range vscodeUserDirs(env) {
		prompts := filepath.Join(u, "prompts")
		out = append(out, userMarkdown(env, prompts, ".prompt.md", false, importer.KindCommand, byName(".prompt.md"), copilotPrompt)...)
		out = append(out, userMarkdown(env, prompts, ".instructions.md", false, importer.KindRule, byName(".instructions.md"), copilotRule)...)
		out = append(out, userMarkdown(env, prompts, ".agent.md", false, importer.KindAgent, byName(".agent.md"), copilotAgent)...)
		modes := userMarkdown(env, prompts, ".chatmode.md", false, importer.KindAgent, byName(".chatmode.md"), copilotAgent)
		for i := range modes {
			modes[i].Note = joinNotes(modes[i].Note, "a legacy chat mode")
		}
		out = append(out, modes...)
		out = append(out, userMCP(env, filepath.Join(u, "mcp.json"), "servers", true)...)
	}
	return out
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "copilot", Name: "GitHub Copilot", Logo: "github",
			Description: "Instructions, prompts, agents, skills and MCP servers from ~/.copilot and VS Code's user data; a folder's Copilot instructions and .vscode/mcp.json."},
		detect: func(env importer.Env) bool { return exists(env.Copilot()) || len(vscodeUserDirs(env)) > 0 },
		user:   copilotUser,
		patterns: []importer.FolderPattern{
			{Glob: ".github/copilot-instructions.md", Read: instructions},
			{Glob: ".github/instructions/*.instructions.md", Read: one(copilotInstructionsFile)},
			{Glob: ".vscode/mcp.json", Read: func(f importer.FolderFile) []importer.Found {
				return mcpFile(f.Data, "servers", f.Rel, true)
			}},
		},
	})
}
