package sources

import (
	"encoding/json"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xlsft/harness/packages/client/importer"
)

// OpenCode, and Kilo Code, whose current extension runs on the Kilo CLI with an OpenCode-style config.
//
// OpenCode (https://opencode.ai/docs/config/, /docs/mcp-servers/, /docs/agents/, /docs/commands/, /docs/rules/):
//
//	~/.config/opencode/opencode.json(c) ($XDG_CONFIG_HOME on every OS); a folder's opencode.json(c) or
//	  .opencode/opencode.json(c). JSONC; {env:VAR} becomes ${VAR}, {file:path} is set up by hand.
//	  mcp: V1 {name: server}, V2 {servers: {name: server}}; local: command array, environment, cwd, enabled (V1) /
//	  disabled (V2); remote: url (Streamable HTTP), headers, oauth.
//	  agent (V1) / agents (V2): prompt (V1) / system (V2) is the body; entries without one override a built-in agent.
//	  command (V1) / commands (V2): template is the body.
//	  instructions: ~/ and absolute paths and globs are read at user level; relative ones belong to a project; URLs are
//	  set up by hand.
//	~/.config/opencode/AGENTS.md → instructions
//	agents/ (V1 agent/) *.md, nested paths joined with "-"; commands/ (V1 command/) *.md, nested folders give a:b.
//
// Kilo Code (https://kilo.ai/docs/automate/mcp/using-in-kilo-code, https://kilo.ai/docs/customize/custom-rules,
// https://kilo.ai/docs/customize/custom-modes): ~/.config/kilo/kilo.json(c), agent/ (and agents/) markdown; a folder's
// kilo.json(c), .kilo/kilo.json(c), .kilo/agents, .kilo/agent and the legacy .kilocode/agents. The legacy extension's
// files (rules, custom modes, mcp_settings.json) are read as in cline.go.

var (
	envSubst  = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
	fileSubst = regexp.MustCompile(`\{file:[^}]*\}`)
)

type ocServer struct {
	Type        string            `json:"type"`
	Command     any               `json:"command"`
	Environment map[string]string `json:"environment"`
	Cwd         string            `json:"cwd"`
	Enabled     *bool             `json:"enabled"`
	Disabled    bool              `json:"disabled"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	OAuth       any               `json:"oauth"`
}

type ocAgent struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	System      string `json:"system"`
	Mode        string `json:"mode"`
	Model       string `json:"model"`
}

type ocCommand struct {
	Template    string `json:"template"`
	Description string `json:"description"`
	Agent       string `json:"agent"`
	Model       string `json:"model"`
}

type ocConfig struct {
	MCP          json.RawMessage      `json:"mcp"`
	Agent        map[string]ocAgent   `json:"agent"`
	Agents       map[string]ocAgent   `json:"agents"`
	Command      map[string]ocCommand `json:"command"`
	Commands     map[string]ocCommand `json:"commands"`
	Instructions []string             `json:"instructions"`
}

func (s ocServer) server() (*importer.MCPServer, string) {
	m := &importer.MCPServer{Env: s.Environment, Cwd: s.Cwd, URL: s.URL, Headers: s.Headers, Disabled: s.Disabled || (s.Enabled != nil && !*s.Enabled)}
	switch c := s.Command.(type) {
	case string:
		m.Command = c
	case []any:
		for i, a := range c {
			if v, ok := a.(string); ok {
				if i == 0 {
					m.Command = v
				} else {
					m.Args = append(m.Args, v)
				}
			}
		}
	}
	if s.Type == "remote" || (m.Command == "" && m.URL != "") {
		m.Transport = "http"
	}
	note := ""
	if s.OAuth != nil {
		note = "it signed in with OAuth: sign in again here"
	}
	return m, note
}

// parseOpenCode reads an OpenCode or Kilo config. user says relative instructions belong to projects.
func parseOpenCode(env importer.Env, data []byte, source string, user bool) []importer.Found {
	text := string(stripJSONC(data))
	var notes []string
	if fileSubst.MatchString(text) {
		notes = append(notes, "{file:…} values were left out; set them by hand")
		text = fileSubst.ReplaceAllString(text, "")
	}
	text = envSubst.ReplaceAllString(text, "$${$1}")
	var cfg ocConfig
	if err := json.Unmarshal([]byte(text), &cfg); err != nil {
		return []importer.Found{{Kind: importer.KindMCP, Name: path.Base(source), Source: source, Status: importer.StatusError, Note: "invalid JSON: " + err.Error()}}
	}
	var out []importer.Found
	// mcp: V2 has {servers: {...}}, V1 the servers themselves.
	servers := map[string]ocServer{}
	if len(cfg.MCP) > 0 {
		var v2 struct {
			Servers map[string]ocServer `json:"servers"`
		}
		if err := json.Unmarshal(cfg.MCP, &v2); err == nil && v2.Servers != nil {
			servers = v2.Servers
		} else if err := json.Unmarshal(cfg.MCP, &servers); err != nil {
			out = append(out, importer.Found{Kind: importer.KindMCP, Name: "mcp", Source: source, Status: importer.StatusError, Note: "mcp: " + err.Error()})
		}
	}
	for _, name := range sortedNames(servers) {
		m, note := servers[name].server()
		out = append(out, importer.Found{Kind: importer.KindMCP, Name: name, Source: source, MCP: m, Note: joinNotes(note, strings.Join(notes, "; "))})
	}
	agents := cfg.Agents
	if agents == nil {
		agents = cfg.Agent
	}
	for _, name := range sortedNames(agents) {
		a := agents[name]
		prompt := a.System
		if prompt == "" {
			prompt = a.Prompt
		}
		if strings.TrimSpace(prompt) == "" {
			out = append(out, importer.Found{Kind: importer.KindAgent, Name: name, Source: source, Status: importer.StatusSkipped, Note: "it only changes a built-in agent"})
			continue
		}
		out = append(out, importer.Found{Kind: importer.KindAgent, Name: name, Source: source,
			Text: frontmatter([][2]string{{"name", name}, {"description", a.Description}}, nil, prompt), Note: droppedNote(a.Mode != "", a.Model != "")})
	}
	commands := cfg.Commands
	if commands == nil {
		commands = cfg.Command
	}
	for _, name := range sortedNames(commands) {
		c := commands[name]
		if strings.TrimSpace(c.Template) == "" {
			out = append(out, importer.Found{Kind: importer.KindCommand, Name: name, Source: source, Status: importer.StatusError, Note: "no template"})
			continue
		}
		out = append(out, importer.Found{Kind: importer.KindCommand, Name: name, Source: source,
			Text: frontmatter([][2]string{{"description", c.Description}}, nil, c.Template), Note: droppedNote(c.Agent != "", c.Model != "")})
	}
	for _, ins := range cfg.Instructions {
		switch {
		case strings.HasPrefix(ins, "http://") || strings.HasPrefix(ins, "https://"):
			out = append(out, noted(importer.KindInstructions, ins, source, "instructions from a URL: add them by hand"))
		case user && (strings.HasPrefix(ins, "~/") || filepath.IsAbs(ins)):
			p := ins
			if strings.HasPrefix(p, "~/") {
				p = filepath.Join(env.Home, p[2:])
			}
			matches, _ := filepath.Glob(p)
			for _, m := range matches {
				out = append(out, userInstructions(env, m, filepath.Base(m))...)
			}
		case user:
			out = append(out, noted(importer.KindInstructions, ins, source, "a path in each project: import the project's folder for it"))
		default:
			out = append(out, noted(importer.KindInstructions, ins, source, "listed as an instructions file of the project: add it to AGENTS.md with @"+ins))
		}
	}
	return out
}

func droppedNote(mode, model bool) string {
	var parts []string
	if mode {
		parts = append(parts, "mode")
	}
	if model {
		parts = append(parts, "model")
	}
	if len(parts) == 0 {
		return ""
	}
	return "its " + strings.Join(parts, " and ") + " were left out"
}

// ocAgentFile turns an agent markdown file into an agent; its tool settings are left out.
func ocAgentFile(name string) func(string) (string, string) {
	return func(md string) (string, string) {
		var dropped []string
		for _, k := range []string{"mode", "model", "permission", "tools", "temperature"} {
			if fm(md, k) != "" || len(globList(md, k)) > 0 {
				dropped = append(dropped, k)
			}
		}
		note := ""
		if len(dropped) > 0 {
			note = "its " + strings.Join(dropped, ", ") + " were left out"
		}
		return frontmatter([][2]string{{"name", name}, {"description", fm(md, "description")}}, nil, body(md)), note
	}
}

func ocCommandFile(md string) (string, string) {
	var dropped []string
	for _, k := range []string{"agent", "model", "subtask"} {
		if fm(md, k) != "" {
			dropped = append(dropped, k)
		}
	}
	note := ""
	if len(dropped) > 0 {
		note = "its " + strings.Join(dropped, ", ") + " were left out"
	}
	return frontmatter([][2]string{{"description", fm(md, "description")}}, nil, body(md)), note
}

// userAgentsDir reads agent markdown below dir: nested ids are joined with "-".
func userAgentsDir(env importer.Env, dir string) []importer.Found {
	var out []importer.Found
	for _, rel := range userFiles(dir, ".md", true) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		name := strings.ReplaceAll(trimExt(rel, ".md"), "/", "-")
		data, _, err := readUser(p)
		if err != nil {
			out = append(out, failed(env, importer.KindAgent, name, p, err))
			continue
		}
		text, note := ocAgentFile(name)(string(data))
		out = append(out, importer.Found{Kind: importer.KindAgent, Name: name, Source: env.Display(p), Text: text, Note: note})
	}
	return out
}

// userCommandsDir reads command markdown below dir: nested folders give a:b.
func userCommandsDir(env importer.Env, dir string) []importer.Found {
	return userMarkdown(env, dir, ".md", true, importer.KindCommand, func(rel string) string {
		return strings.ReplaceAll(trimExt(rel, ".md"), "/", ":")
	}, ocCommandFile)
}

// userConfig reads the first of the config files that exists.
func userConfig(env importer.Env, dir string, names ...string) []importer.Found {
	for _, n := range names {
		p := filepath.Join(dir, n)
		data, ok, err := readUser(p)
		if !ok {
			continue
		}
		if err != nil {
			return []importer.Found{failed(env, importer.KindMCP, n, p, err)}
		}
		return parseOpenCode(env, data, env.Display(p), true)
	}
	return nil
}

func opencodeUser(env importer.Env) []importer.Found {
	d := filepath.Join(env.UserConfigDirXDG(), "opencode")
	out := userConfig(env, d, "opencode.jsonc", "opencode.json")
	out = append(out, userInstructions(env, filepath.Join(d, "AGENTS.md"), "AGENTS.md")...)
	for _, sub := range []string{"agents", "agent"} {
		out = append(out, userAgentsDir(env, filepath.Join(d, sub))...)
	}
	for _, sub := range []string{"commands", "command"} {
		out = append(out, userCommandsDir(env, filepath.Join(d, sub))...)
	}
	return dedupe(out)
}

func kiloUser(env importer.Env) []importer.Found {
	d := filepath.Join(env.UserConfigDirXDG(), "kilo")
	out := userConfig(env, d, "kilo.jsonc", "kilo.json")
	for _, sub := range []string{"agent", "agents"} {
		out = append(out, userAgentsDir(env, filepath.Join(d, sub))...)
	}
	out = append(out, rooFamilyUser(env, "Kilo Code", filepath.Join(env.Home, ".kilocode"), "kilocode.kilo-code")...)
	return dedupe(out)
}

// configFile is a folder pattern for an OpenCode-style config.
func configFile(f importer.FolderFile) []importer.Found {
	return parseOpenCode(importer.Env{}, f.Data, f.Rel, false)
}

func agentFile(prefix string) func(importer.FolderFile) []importer.Found {
	return func(f importer.FolderFile) []importer.Found {
		name := strings.ReplaceAll(ruleName(f.Rel, prefix, ".md"), "/", "-")
		text, note := ocAgentFile(name)(string(f.Data))
		return []importer.Found{{Kind: importer.KindAgent, Name: name, Text: text, Note: note}}
	}
}

func commandFile(prefix string) func(importer.FolderFile) []importer.Found {
	return func(f importer.FolderFile) []importer.Found {
		text, note := ocCommandFile(string(f.Data))
		return []importer.Found{{Kind: importer.KindCommand, Name: strings.ReplaceAll(ruleName(f.Rel, prefix, ".md"), "/", ":"), Text: text, Note: note}}
	}
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "opencode", Name: "OpenCode", Logo: "opencode",
			Description: "MCP servers, agents, commands and instructions from ~/.config/opencode; a folder's opencode.json and .opencode."},
		detect: func(env importer.Env) bool { return exists(filepath.Join(env.UserConfigDirXDG(), "opencode")) },
		user:   opencodeUser,
		patterns: []importer.FolderPattern{
			{Glob: "opencode.json", Read: configFile},
			{Glob: "opencode.jsonc", Read: configFile},
			{Glob: ".opencode/opencode.json", Read: configFile},
			{Glob: ".opencode/opencode.jsonc", Read: configFile},
			{Glob: ".opencode/agents/*.md", Read: agentFile(".opencode/agents/")},
			{Glob: ".opencode/agent/*.md", Read: agentFile(".opencode/agent/")},
			{Glob: ".opencode/commands/*.md", Read: commandFile(".opencode/commands/")},
			{Glob: ".opencode/command/*.md", Read: commandFile(".opencode/command/")},
		},
	})
	register(&reader{
		info: importer.SourceInfo{ID: "kilo", Name: "Kilo Code", Logo: "kilo",
			Description: "MCP servers, agents and rules of Kilo Code, current and legacy; a folder's kilo.jsonc, .kilo and .kilocode files."},
		detect: func(env importer.Env) bool {
			return anyExists(filepath.Join(env.UserConfigDirXDG(), "kilo"), filepath.Join(env.Home, ".kilocode")) ||
				len(vscodeGlobalStorage(env, "kilocode.kilo-code")) > 0
		},
		user: kiloUser,
		patterns: []importer.FolderPattern{
			{Glob: "kilo.json", Read: configFile},
			{Glob: "kilo.jsonc", Read: configFile},
			{Glob: ".kilo/kilo.json", Read: configFile},
			{Glob: ".kilo/kilo.jsonc", Read: configFile},
			{Glob: ".kilo/agents/*.md", Read: agentFile(".kilo/agents/")},
			{Glob: ".kilo/agent/*.md", Read: agentFile(".kilo/agent/")},
			{Glob: ".kilocode/agents/*.md", Read: agentFile(".kilocode/agents/")},
			{Glob: ".kilocode/rules/*", Read: one(pathsRule(".kilocode/rules/"))},
			{Glob: ".kilocode/rules-*/*", Read: one(modeRule("Kilo Code"))},
			{Glob: ".kilo/rules/*", Read: one(pathsRule(".kilo/rules/"))},
			{Glob: ".kilocodemodes", Read: modesFile("Kilo Code")},
		},
	})
}
