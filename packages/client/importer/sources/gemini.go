package sources

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/xlsft/harness/packages/client/importer"
)

// Gemini CLI.
//
// User level (~/.gemini):
//
//	GEMINI.md, and the other names of settings.json context.fileName → instructions
//	  (https://geminicli.com/docs/cli/gemini-md/)
//	settings.json mcpServers           → MCP servers on this device
//	commands/**/*.toml                 → commands: git/commit.toml is git:commit; {{args}} → $ARGUMENTS, !{cmd} → !`cmd`,
//	  @{path} → an @mention (https://geminicli.com/docs/cli/custom-commands/)
//	agents/*.md                        → agents (https://geminicli.com/docs/core/subagents/)
//	skills/<name>/SKILL.md             → skills (https://geminicli.com/docs/cli/skills/)
//	extensions/<dir>/gemini-extension.json → the extension's MCP servers (${extensionPath} expanded), context file and
//	  commands (https://geminicli.com/docs/extensions/reference/)
//	.env, oauth_creds.json             → never read
//
// Folder level:
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

// geminiContextNames are the context file names of settings.json: context.fileName (or the older contextFileName), a
// string or a list; GEMINI.md when unset.
func geminiContextNames(settings map[string]any) []string {
	var v any
	if ctx, ok := settings["context"].(map[string]any); ok {
		v = ctx["fileName"]
	}
	if v == nil {
		v = settings["contextFileName"]
	}
	var out []string
	switch x := v.(type) {
	case string:
		out = append(out, x)
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	}
	if len(out) == 0 {
		out = []string{"GEMINI.md"}
	}
	return out
}

// convertGeminiCommand turns a Gemini CLI command (TOML with prompt and description) into a command.
func convertGeminiCommand(src string) (string, string, error) {
	var c struct {
		Prompt      string `toml:"prompt"`
		Description string `toml:"description"`
	}
	if _, err := toml.Decode(src, &c); err != nil {
		return "", "", fmt.Errorf("invalid TOML: %w", err)
	}
	if strings.TrimSpace(c.Prompt) == "" {
		return "", "", fmt.Errorf("no prompt")
	}
	text, notes := convertGeminiPrompt(c.Prompt)
	return frontmatter([][2]string{{"description", strings.TrimSpace(c.Description)}}, nil, text), strings.Join(notes, "; "), nil
}

// convertGeminiPrompt rewrites {{args}} and the injections. Braces inside an injection must balance, as in Gemini CLI.
// Without {{args}}, Gemini adds what follows the command at the end, and so does harness: the prompt stays as it is.
func convertGeminiPrompt(p string) (string, []string) {
	var b strings.Builder
	var notes []string
	files, kept := 0, 0
	for i := 0; i < len(p); {
		if (p[i] == '!' || p[i] == '@') && i+1 < len(p) && p[i+1] == '{' {
			end := balanced(p, i+1)
			if end < 0 {
				b.WriteString(p[i:])
				break
			}
			inner := p[i+2 : end]
			if p[i] == '@' {
				files++
				b.WriteString(p[i : end+1])
			} else if cmd := strings.ReplaceAll(inner, "{{args}}", "$ARGUMENTS"); !strings.ContainsAny(cmd, "`\n") {
				b.WriteString("!`" + strings.TrimSpace(cmd) + "`")
			} else {
				kept++
				b.WriteString(p[i : end+1])
			}
			i = end + 1
			continue
		}
		if strings.HasPrefix(p[i:], "{{args}}") {
			b.WriteString("$ARGUMENTS")
			i += len("{{args}}")
			continue
		}
		b.WriteByte(p[i])
		i++
	}
	if files > 0 {
		notes = append(notes, "@{…} file injections have no equivalent here and were left as written")
	}
	if kept > 0 {
		notes = append(notes, fmt.Sprintf("%d shell injection(s) span lines or hold a backtick and were left as written", kept))
	}
	return b.String(), notes
}

// balanced returns the index of the brace closing the one at open, or -1.
func balanced(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// geminiCommands reads commands/**/*.toml below dir; prefix goes before each name ("" or "<extension>.").
func geminiCommands(env importer.Env, dir string) []importer.Found {
	var out []importer.Found
	for _, rel := range userFiles(dir, ".toml", true) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		name := strings.ReplaceAll(trimExt(rel, ".toml"), "/", ":")
		data, _, err := readUser(p)
		if err != nil {
			out = append(out, failed(env, importer.KindCommand, name, p, err))
			continue
		}
		text, note, err := convertGeminiCommand(string(data))
		if err != nil {
			out = append(out, importer.Found{Kind: importer.KindCommand, Name: name, Source: env.Display(p), Status: importer.StatusError, Note: err.Error()})
			continue
		}
		out = append(out, importer.Found{Kind: importer.KindCommand, Name: name, Source: env.Display(p), Text: text, Note: note})
	}
	return out
}

// geminiExtension reads one extension folder: its MCP servers, context file and commands.
func geminiExtension(env importer.Env, dir string) []importer.Found {
	p := filepath.Join(dir, "gemini-extension.json")
	data, ok, err := readUser(p)
	if !ok {
		return nil
	}
	ext := filepath.Base(dir)
	if err != nil {
		return []importer.Found{failed(env, importer.KindSettings, "extension "+ext, p, err)}
	}
	var manifest struct {
		Name            string          `json:"name"`
		ContextFileName any             `json:"contextFileName"`
		MCPServers      json.RawMessage `json:"mcpServers"`
		Settings        []struct {
			Name   string `json:"name"`
			EnvVar string `json:"envVar"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return []importer.Found{{Kind: importer.KindSettings, Name: "extension " + ext, Source: env.Display(p), Status: importer.StatusError, Note: "invalid JSON: " + err.Error()}}
	}
	if manifest.Name != "" {
		ext = manifest.Name
	}
	var out []importer.Found
	if len(manifest.MCPServers) > 0 {
		// ${extensionPath} and ${/} are Gemini's; expanded here so the server runs from the same place.
		raw := strings.NewReplacer("${extensionPath}", filepath.ToSlash(dir), "${/}", "/").Replace(string(data))
		for _, f := range mcpFile([]byte(raw), "mcpServers", env.Display(p), false) {
			if m := f.MCP; m != nil && m.Transport == "" && m.Command == "" && m.URL != "" {
				m.Transport = "sse"
			}
			if strings.Contains(raw, "${workspacePath}") {
				f.Note = joinNotes(f.Note, "${workspacePath} stays as written: set the folder by hand")
			}
			f.Note = joinNotes(f.Note, "from the Gemini CLI extension "+ext)
			out = append(out, f)
		}
	}
	names := geminiContextNames(map[string]any{"contextFileName": manifest.ContextFileName})
	for _, n := range names {
		for _, f := range userInstructions(env, filepath.Join(dir, n), ext+"/"+n) {
			out = append(out, f)
		}
	}
	for _, f := range geminiCommands(env, filepath.Join(dir, "commands")) {
		f.Note = joinNotes(f.Note, "from the Gemini CLI extension "+ext)
		out = append(out, f)
	}
	for _, s := range manifest.Settings {
		if s.EnvVar != "" {
			out = append(out, importer.Found{Kind: importer.KindSettings, Name: ext + " " + s.EnvVar, Source: env.Display(p), Status: importer.StatusNoted,
				Note: "the extension reads " + s.EnvVar + ": set it in the MCP server's environment"})
		}
	}
	return out
}

func joinNotes(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

func geminiUser(env importer.Env) []importer.Found {
	d := filepath.Join(env.Home, ".gemini")
	var out []importer.Found
	settingsPath := filepath.Join(d, "settings.json")
	settings := map[string]any{}
	if data, ok, err := readUser(settingsPath); ok {
		if err != nil {
			out = append(out, failed(env, importer.KindMCP, "settings.json", settingsPath, err))
		} else if err := json.Unmarshal(stripJSONC(data), &settings); err != nil {
			out = append(out, importer.Found{Kind: importer.KindMCP, Name: "settings.json", Source: env.Display(settingsPath), Status: importer.StatusError, Note: "invalid JSON: " + err.Error()})
		} else {
			out = append(out, geminiMCP(importer.FolderFile{Rel: env.Display(settingsPath), Data: stripJSONC(data)})...)
		}
	}
	for _, n := range geminiContextNames(settings) {
		out = append(out, userInstructions(env, filepath.Join(d, n), n)...)
	}
	commands := geminiCommands(env, filepath.Join(d, "commands"))
	out = append(out, commands...)
	out = append(out, userMarkdown(env, filepath.Join(d, "agents"), ".md", false, importer.KindAgent,
		func(rel string) string { return trimExt(rel, ".md") }, nil)...)
	out = append(out, userSkills(env, filepath.Join(d, "skills"))...)
	if entries, err := os.ReadDir(filepath.Join(d, "extensions")); err == nil {
		taken := map[string]bool{}
		for _, c := range commands {
			taken[c.Name] = true
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			for _, f := range geminiExtension(env, filepath.Join(d, "extensions", e.Name())) {
				// Gemini prefixes an extension's command with its name only when the user has one of the same name.
				if f.Kind == importer.KindCommand {
					if taken[f.Name] {
						f.Name = e.Name() + "." + f.Name
					}
					taken[f.Name] = true
				}
				out = append(out, f)
			}
		}
	}
	return out
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "gemini", Name: "Gemini CLI", Logo: "gemini",
			Description: "GEMINI.md, MCP servers, commands, agents, skills and extensions from ~/.gemini; a folder's GEMINI.md files and .gemini/settings.json."},
		detect: func(env importer.Env) bool { return exists(filepath.Join(env.Home, ".gemini")) },
		user:   geminiUser,
		patterns: []importer.FolderPattern{
			{Glob: "GEMINI.md", Read: instructions},
			{Glob: ".gemini/settings.json", Read: geminiMCP},
		},
	})
}
