package sources

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/xlsft/harness/packages/client/importer"
)

// Continue (https://docs.continue.dev/customize/deep-dives/configuration, https://docs.continue.dev/reference,
// https://docs.continue.dev/customize/deep-dives/rules, https://docs.continue.dev/customize/deep-dives/mcp,
// https://docs.continue.dev/reference/json-reference).
//
// User level: ~/.continue/config.yaml; when it exists, the legacy config.json is not loaded.
//
//	rules: strings, or {name, rule, globs, regex, description, alwaysApply} → rules; {uses: hub block} → set up by hand
//	prompts: {name, description, prompt} → commands
//	mcpServers: [{name, type, command, args, env, cwd, url, requestOptions.headers}] → MCP servers
//	${{ secrets.NAME }} → ${NAME}; models, context, docs and data are set up by hand
//
// Legacy config.json: systemMessage → instructions; customCommands → commands;
// experimental.modelContextProtocolServers ({transport: {type, command, args, env}}) → MCP servers.
// The docs name no global block folders in ~/.continue, so none are read.
//
// Folder level: .continue/rules/*.md; .continue/prompts/*.md (frontmatter name, description, invokable) → commands;
// .continue/mcpServers/*.yaml (a block with mcpServers) and *.json (another tool's mcpServers map) → MCP servers.

var continueSecret = regexp.MustCompile(`\$\{\{\s*(?:secrets|inputs)\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

func continueRule(f importer.FolderFile) importer.Found {
	md := string(f.Data)
	name := ruleName(f.Rel, ".continue/rules/", ".md")
	if fm(md, "alwaysApply") == "true" {
		return rule(name, md, nil, "")
	}
	onDemand := ""
	if fm(md, "alwaysApply") == "false" {
		onDemand = "Continue used it when the agent judged it relevant"
	}
	return rule(name, md, globList(md, "globs"), onDemand)
}

func continuePrompt(f importer.FolderFile) []importer.Found {
	md := string(f.Data)
	name := fm(md, "name")
	if name == "" {
		name = ruleName(f.Rel, ".continue/prompts/", ".md")
	}
	return []importer.Found{{Kind: importer.KindCommand, Name: slug(name), Text: frontmatter([][2]string{{"description", fm(md, "description")}}, nil, body(md))}}
}

// slug makes a command name of a display name: "Write tests" → "write-tests".
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == ':' || r == '_' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

type continueMCP struct {
	Name           string            `yaml:"name" json:"name"`
	Type           string            `yaml:"type" json:"type"`
	Command        string            `yaml:"command" json:"command"`
	Args           []string          `yaml:"args" json:"args"`
	Env            map[string]string `yaml:"env" json:"env"`
	Cwd            string            `yaml:"cwd" json:"cwd"`
	URL            string            `yaml:"url" json:"url"`
	Uses           string            `yaml:"uses" json:"uses"`
	RequestOptions struct {
		Headers map[string]string `yaml:"headers" json:"headers"`
	} `yaml:"requestOptions" json:"requestOptions"`
}

func continueServers(list []continueMCP, source string) []importer.Found {
	var out []importer.Found
	for i, s := range list {
		if s.Uses != "" {
			out = append(out, noted(importer.KindMCP, s.Uses, source, "a Continue Hub block: add the server by hand"))
			continue
		}
		name := s.Name
		if name == "" {
			name = fmt.Sprintf("server-%d", i+1)
		}
		m := &importer.MCPServer{Transport: s.Type, Command: s.Command, Args: s.Args, Env: s.Env, Cwd: s.Cwd, URL: s.URL, Headers: s.RequestOptions.Headers}
		out = append(out, importer.Found{Kind: importer.KindMCP, Name: name, Source: source, MCP: m})
	}
	return out
}

type continueYAML struct {
	Rules      []any         `yaml:"rules"`
	Prompts    []any         `yaml:"prompts"`
	MCPServers []continueMCP `yaml:"mcpServers"`
	Models     []any         `yaml:"models"`
	Context    []any         `yaml:"context"`
	Docs       []any         `yaml:"docs"`
	Data       []any         `yaml:"data"`
}

func parseContinueYAML(data []byte, source string) []importer.Found {
	text := continueSecret.ReplaceAllString(string(data), "$${$1}")
	var cfg continueYAML
	if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
		return []importer.Found{{Kind: importer.KindSettings, Name: filepath.Base(source), Source: source, Status: importer.StatusError, Note: "invalid YAML: " + err.Error()}}
	}
	var out []importer.Found
	for i, r := range cfg.Rules {
		switch x := r.(type) {
		case string:
			out = append(out, importer.Found{Kind: importer.KindRule, Name: fmt.Sprintf("rule-%d", i+1), Source: source, Text: harnessRule(nil, x)})
		case map[string]any:
			if uses, _ := x["uses"].(string); uses != "" {
				out = append(out, noted(importer.KindRule, uses, source, "a Continue Hub block: copy it by hand"))
				continue
			}
			name, _ := x["name"].(string)
			if name == "" {
				name = fmt.Sprintf("rule-%d", i+1)
			}
			text, _ := x["rule"].(string)
			var globs []string
			switch g := x["globs"].(type) {
			case string:
				globs = []string{g}
			case []any:
				for _, v := range g {
					if s, ok := v.(string); ok {
						globs = append(globs, s)
					}
				}
			}
			f := importer.Found{Kind: importer.KindRule, Name: slug(name), Source: source, Text: harnessRule(globs, text)}
			if re, _ := x["regex"].(string); re != "" {
				f.Note = "Continue also matched files by content (" + re + "); here only the globs limit it"
			} else if a, ok := x["alwaysApply"].(bool); ok && !a && len(globs) == 0 {
				f.Note = "Continue used it when the agent judged it relevant; here it always applies"
			}
			out = append(out, f)
		}
	}
	for i, p := range cfg.Prompts {
		x, _ := p.(map[string]any)
		if uses, _ := x["uses"].(string); uses != "" {
			out = append(out, noted(importer.KindCommand, uses, source, "a Continue Hub block: copy it by hand"))
			continue
		}
		name, _ := x["name"].(string)
		if name == "" {
			name = fmt.Sprintf("prompt-%d", i+1)
		}
		text, _ := x["prompt"].(string)
		desc, _ := x["description"].(string)
		out = append(out, importer.Found{Kind: importer.KindCommand, Name: slug(name), Source: source, Text: frontmatter([][2]string{{"description", desc}}, nil, text)})
	}
	out = append(out, continueServers(cfg.MCPServers, source)...)
	var other []string
	for k, n := range map[string]int{"models": len(cfg.Models), "context": len(cfg.Context), "docs": len(cfg.Docs), "data": len(cfg.Data)} {
		if n > 0 {
			other = append(other, k)
		}
	}
	if len(other) > 0 {
		out = append(out, noted(importer.KindSettings, "Continue settings", source, strings.Join(sortedStrings(other), ", ")+": models and context providers are set up in Settings"))
	}
	return out
}

func sortedStrings(s []string) []string {
	m := map[string]bool{}
	for _, v := range s {
		m[v] = true
	}
	return sortedNames(m)
}

func parseContinueJSON(data []byte, source string) []importer.Found {
	var cfg struct {
		SystemMessage  string `json:"systemMessage"`
		CustomCommands []struct {
			Name        string `json:"name"`
			Prompt      string `json:"prompt"`
			Description string `json:"description"`
		} `json:"customCommands"`
		Experimental struct {
			Servers []struct {
				Transport struct {
					Type    string            `json:"type"`
					Command string            `json:"command"`
					Args    []string          `json:"args"`
					Env     map[string]string `json:"env"`
					URL     string            `json:"url"`
				} `json:"transport"`
			} `json:"modelContextProtocolServers"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(stripJSONC(data), &cfg); err != nil {
		return []importer.Found{{Kind: importer.KindSettings, Name: filepath.Base(source), Source: source, Status: importer.StatusError, Note: "invalid JSON: " + err.Error()}}
	}
	var out []importer.Found
	if strings.TrimSpace(cfg.SystemMessage) != "" {
		out = append(out, importer.Found{Kind: importer.KindInstructions, Name: "systemMessage", Source: source, Text: strings.TrimSpace(cfg.SystemMessage)})
	}
	for _, c := range cfg.CustomCommands {
		out = append(out, importer.Found{Kind: importer.KindCommand, Name: slug(c.Name), Source: source, Text: frontmatter([][2]string{{"description", c.Description}}, nil, c.Prompt)})
	}
	for i, s := range cfg.Experimental.Servers {
		t := s.Transport
		name := filepath.Base(t.Command)
		if name == "" || name == "." {
			name = fmt.Sprintf("server-%d", i+1)
		}
		out = append(out, importer.Found{Kind: importer.KindMCP, Name: name, Source: source,
			MCP: &importer.MCPServer{Transport: t.Type, Command: t.Command, Args: t.Args, Env: t.Env, URL: t.URL}})
	}
	return dedupe(out)
}

func continueUser(env importer.Env) []importer.Found {
	d := filepath.Join(env.Home, ".continue")
	for _, n := range []string{"config.yaml", "config.json"} {
		p := filepath.Join(d, n)
		data, ok, err := readUser(p)
		if !ok {
			continue
		}
		if err != nil {
			return []importer.Found{failed(env, importer.KindSettings, n, p, err)}
		}
		if n == "config.yaml" {
			return dedupe(parseContinueYAML(data, env.Display(p)))
		}
		return parseContinueJSON(data, env.Display(p))
	}
	return nil
}

func continueMCPBlock(f importer.FolderFile) []importer.Found {
	if strings.HasSuffix(f.Rel, ".json") {
		return mcpFile(f.Data, "mcpServers", f.Rel, true)
	}
	var block struct {
		MCPServers []continueMCP `yaml:"mcpServers"`
	}
	text := continueSecret.ReplaceAllString(string(f.Data), "$${$1}")
	if err := yaml.Unmarshal([]byte(text), &block); err != nil {
		return []importer.Found{{Kind: importer.KindMCP, Name: path0(f.Rel), Status: importer.StatusError, Note: "invalid YAML: " + err.Error()}}
	}
	return continueServers(block.MCPServers, f.Rel)
}

func path0(rel string) string { return filepath.Base(filepath.FromSlash(rel)) }

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "continue", Name: "Continue", Logo: "continue",
			Description: "Rules, prompts and MCP servers from ~/.continue/config.yaml (or config.json); a folder's Continue rules, prompts and MCP blocks."},
		detect: func(env importer.Env) bool { return exists(filepath.Join(env.Home, ".continue")) },
		user:   continueUser,
		patterns: []importer.FolderPattern{
			{Glob: ".continue/rules/*.md", Read: one(continueRule)},
			{Glob: ".continue/prompts/*.md", Read: continuePrompt},
			{Glob: ".continue/mcpServers/*.yaml", Read: continueMCPBlock},
			{Glob: ".continue/mcpServers/*.yml", Read: continueMCPBlock},
			{Glob: ".continue/mcpServers/*.json", Read: continueMCPBlock},
		},
	})
}
