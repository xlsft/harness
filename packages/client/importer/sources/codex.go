package sources

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/xlsft/harness/packages/client/importer"
)

// Codex CLI, user level ($CODEX_HOME, default ~/.codex). Formats from OpenAI's docs, checked against the open-source CLI:
//
//	AGENTS.override.md, else AGENTS.md → instructions; only the first non-empty one counts
//	  (https://learn.chatgpt.com/docs/agent-configuration/agents-md.md)
//	config.toml [mcp_servers.<id>]     → MCP servers on this device; model, effort, approvals and sandbox are listed to set
//	  by hand (https://learn.chatgpt.com/docs/config-file/config-reference.md)
//	<name>.config.toml                 → profiles, listed to set by hand
//	prompts/*.md (top level only)      → commands; $1..$9 and $ARGUMENTS are the same here, named $UPPER placeholders
//	  become an `arguments:` list (https://learn.chatgpt.com/docs/custom-prompts.md)
//	agents/*.toml                      → agents: name, description, developer_instructions
//	  (https://learn.chatgpt.com/docs/agent-configuration/subagents.md)
//	~/.agents/skills/<name>/SKILL.md   → skills; shared with Cursor and Gemini CLI, so read here only. Codex's bundled
//	  .system skills are not read (https://learn.chatgpt.com/docs/build-skills.md)
//	sessions/YYYY/MM/DD/rollout-*.jsonl → sessions, converted (codex_sessions.go)
//	auth.json                          → never read

// codexMCPConfig is one [mcp_servers.<id>] table.
type codexMCPConfig struct {
	Command           string            `toml:"command"`
	Args              []string          `toml:"args"`
	Env               map[string]string `toml:"env"`
	EnvVars           []any             `toml:"env_vars"`
	Cwd               string            `toml:"cwd"`
	URL               string            `toml:"url"`
	BearerTokenEnvVar string            `toml:"bearer_token_env_var"`
	HTTPHeaders       map[string]string `toml:"http_headers"`
	EnvHTTPHeaders    map[string]string `toml:"env_http_headers"`
	Enabled           *bool             `toml:"enabled"`
	Auth              string            `toml:"auth"`
	OAuth             map[string]any    `toml:"oauth"`
	EnabledTools      []string          `toml:"enabled_tools"`
	DisabledTools     []string          `toml:"disabled_tools"`
	StartupTimeout    any               `toml:"startup_timeout_sec"`
	ToolTimeout       any               `toml:"tool_timeout_sec"`
}

// codexSettings are the config.toml keys listed to set by hand; the rest are Codex's own.
var codexSettings = []string{"model", "model_reasoning_effort", "approval_policy", "sandbox_mode"}

func codexMCP(c codexMCPConfig) (*importer.MCPServer, string) {
	m := &importer.MCPServer{Command: c.Command, Args: c.Args, Cwd: c.Cwd, URL: c.URL, Env: map[string]string{}, Headers: map[string]string{}}
	for k, v := range c.Env {
		m.Env[k] = v
	}
	// env_vars: names passed through from Codex's own environment, as a string or {name, source}.
	for _, v := range c.EnvVars {
		name := ""
		switch x := v.(type) {
		case string:
			name = x
		case map[string]any:
			name, _ = x["name"].(string)
		}
		if name != "" {
			m.Env[name] = "${" + name + "}"
		}
	}
	for k, v := range c.HTTPHeaders {
		m.Headers[k] = v
	}
	for k, v := range c.EnvHTTPHeaders {
		m.Headers[k] = "${" + v + "}"
	}
	if c.BearerTokenEnvVar != "" {
		m.Headers["Authorization"] = "Bearer ${" + c.BearerTokenEnvVar + "}"
	}
	if c.Enabled != nil && !*c.Enabled {
		m.Disabled = true
	}
	var notes []string
	if strings.EqualFold(c.Auth, "oauth") || len(c.OAuth) > 0 {
		notes = append(notes, "Codex signed in to it with OAuth: sign in again here")
	}
	if len(c.EnabledTools) > 0 {
		notes = append(notes, "Codex allowed only "+strings.Join(c.EnabledTools, ", "))
	}
	if len(c.DisabledTools) > 0 {
		notes = append(notes, "Codex turned off "+strings.Join(c.DisabledTools, ", "))
	}
	if c.StartupTimeout != nil || c.ToolTimeout != nil {
		notes = append(notes, "Codex's timeouts are not carried over")
	}
	return m, strings.Join(notes, "; ")
}

func codexConfig(env importer.Env, p string, profile string) []importer.Found {
	data, ok, err := readUser(p)
	if !ok {
		return nil
	}
	name := filepath.Base(p)
	if err != nil {
		return []importer.Found{failed(env, importer.KindSettings, name, p, err)}
	}
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return []importer.Found{{Kind: importer.KindSettings, Name: name, Source: env.Display(p), Status: importer.StatusError, Note: "invalid TOML: " + err.Error()}}
	}
	var cfg struct {
		MCPServers map[string]codexMCPConfig `toml:"mcp_servers"`
	}
	var out []importer.Found
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		out = append(out, importer.Found{Kind: importer.KindMCP, Name: name, Source: env.Display(p), Status: importer.StatusError, Note: "mcp_servers: " + err.Error()})
	}
	if profile == "" {
		for _, id := range sortedNames(cfg.MCPServers) {
			m, note := codexMCP(cfg.MCPServers[id])
			out = append(out, importer.Found{Kind: importer.KindMCP, Name: id, Source: env.Display(p), MCP: m, Note: note})
		}
	}
	var set []string
	for _, k := range codexSettings {
		if v, ok := raw[k]; ok {
			set = append(set, fmt.Sprintf("%s = %v", k, v))
		}
	}
	label := "Codex settings"
	if profile != "" {
		label = "Codex profile " + profile
	}
	if len(set) > 0 || profile != "" {
		note := "models and permission modes are chosen in Settings; import never widens what runs without asking"
		if len(set) > 0 {
			note = strings.Join(set, ", ") + ": " + note
		}
		out = append(out, importer.Found{Kind: importer.KindSettings, Name: label, Source: env.Display(p), Status: importer.StatusNoted, Note: note})
	}
	return out
}

var (
	codexNamedArg = regexp.MustCompile(`\$([A-Z][A-Z0-9_]*)\b`)
	codexEscaped  = regexp.MustCompile(`\$\$`)
)

// codexPrompt turns a custom prompt into a command: $1..$9 and $ARGUMENTS stay, $$ becomes $, and named $UPPER
// placeholders (given as KEY=value in Codex) become an `arguments:` list, filled by position here.
func codexPrompt(md string) (string, string) {
	text := body(md)
	var notes []string
	if codexEscaped.MatchString(text) {
		text = codexEscaped.ReplaceAllString(text, "$")
		notes = append(notes, "$$ became $")
	}
	var named []string
	seen := map[string]bool{}
	for _, m := range codexNamedArg.FindAllStringSubmatch(text, -1) {
		if n := m[1]; n != "ARGUMENTS" && !seen[n] {
			seen[n] = true
			named = append(named, n)
		}
	}
	var lists map[string][]string
	if len(named) > 0 {
		lists = map[string][]string{"arguments": named}
		notes = append(notes, "Codex took "+strings.Join(named, ", ")+" as KEY=value; here they are given in order")
	}
	return frontmatter([][2]string{{"description", fm(md, "description")}, {"argument-hint", fm(md, "argument-hint")}}, lists, text), strings.Join(notes, "; ")
}

type codexAgent struct {
	Name                  string `toml:"name"`
	Description           string `toml:"description"`
	DeveloperInstructions string `toml:"developer_instructions"`
	Model                 string `toml:"model"`
	ReasoningEffort       string `toml:"model_reasoning_effort"`
}

func codexAgents(env importer.Env, dir string) []importer.Found {
	var out []importer.Found
	for _, rel := range userFiles(dir, ".toml", false) {
		p := filepath.Join(dir, rel)
		name := trimExt(rel, ".toml")
		data, _, err := readUser(p)
		if err != nil {
			out = append(out, failed(env, importer.KindAgent, name, p, err))
			continue
		}
		var a codexAgent
		if _, err := toml.Decode(string(data), &a); err != nil {
			out = append(out, importer.Found{Kind: importer.KindAgent, Name: name, Source: env.Display(p), Status: importer.StatusError, Note: "invalid TOML: " + err.Error()})
			continue
		}
		if a.Name != "" {
			name = a.Name
		}
		f := importer.Found{Kind: importer.KindAgent, Name: name, Source: env.Display(p),
			Text: frontmatter([][2]string{{"name", name}, {"description", a.Description}}, nil, a.DeveloperInstructions)}
		if a.Model != "" || a.ReasoningEffort != "" {
			f.Note = strings.TrimSpace("Codex ran it with " + strings.TrimSpace(a.Model+" "+a.ReasoningEffort) + "; it uses the session's model here")
		}
		out = append(out, f)
	}
	return out
}

func codexUser(env importer.Env) []importer.Found {
	d := env.Codex()
	var out []importer.Found
	override := userInstructions(env, filepath.Join(d, "AGENTS.override.md"), "AGENTS.md")
	if len(override) > 0 {
		out = append(out, override...)
	} else {
		out = append(out, userInstructions(env, filepath.Join(d, "AGENTS.md"), "AGENTS.md")...)
	}
	out = append(out, codexConfig(env, filepath.Join(d, "config.toml"), "")...)
	if entries, err := os.ReadDir(d); err == nil {
		var profiles []string
		for _, e := range entries {
			if n := e.Name(); strings.HasSuffix(n, ".config.toml") && !e.IsDir() {
				profiles = append(profiles, strings.TrimSuffix(n, ".config.toml"))
			}
		}
		sort.Strings(profiles)
		for _, p := range profiles {
			out = append(out, codexConfig(env, filepath.Join(d, p+".config.toml"), p)...)
		}
	}
	out = append(out, userMarkdown(env, filepath.Join(d, "prompts"), ".md", false, importer.KindCommand,
		func(rel string) string { return trimExt(rel, ".md") }, codexPrompt)...)
	out = append(out, codexAgents(env, filepath.Join(d, "agents"))...)
	out = append(out, userSkills(env, filepath.Join(env.Home, ".agents", "skills"))...)
	out = append(out, codexSessions(env, d)...)
	return out
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "codex", Name: "Codex CLI", Logo: "codex",
			Description: "AGENTS.md, MCP servers, custom prompts, agents and skills from ~/.codex; a folder's AGENTS.override.md."},
		detect: func(env importer.Env) bool { return exists(env.Codex()) },
		user:   codexUser,
		patterns: []importer.FolderPattern{
			{Glob: "AGENTS.override.md", Read: instructions},
		},
	})
}
