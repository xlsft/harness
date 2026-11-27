package sources

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/xlsft/harness/packages/client/importer"
)

// Kiro.
//
// Steering (https://kiro.dev/docs/steering/): ~/.kiro/steering/*.md and a folder's .kiro/steering/*.md, frontmatter
// inclusion always (default) / fileMatch with fileMatchPattern / manual / auto.
// MCP (https://kiro.dev/docs/mcp/configuration/): ~/.kiro/settings/mcp.json and .kiro/settings/mcp.json, "mcpServers",
// ${VAR} in values, a bare url is remote HTTP.
// Hooks (https://kiro.dev/docs/hooks/): ~/.kiro/hooks and .kiro/hooks, JSON {version, hooks: [{name, trigger, matcher,
// action}]} or older *.kiro.hook files. Their events and tool names do not map one to one: each is listed to set up by
// hand in Settings → Hooks.
// Specs: .kiro/specs/<feature>/*.md stay in the repository; listed, with a note to reference them from AGENTS.md.

func kiroSteering(f importer.FolderFile) importer.Found {
	md := string(f.Data)
	name := ruleName(f.Rel, ".kiro/steering/", ".md")
	switch fm(md, "inclusion") {
	case "", "always":
		return rule(name, md, nil, "")
	case "fileMatch":
		return rule(name, md, globList(md, "fileMatchPattern"), "")
	case "manual":
		return rule(name, md, nil, "Kiro used it only when referenced with #")
	}
	return rule(name, md, nil, "Kiro used it when the request matched its description")
}

// kiroHooks lists each hook of a hooks file.
func kiroHooks(data []byte, source string) []importer.Found {
	var doc struct {
		Name  string `json:"name"`
		Hooks []struct {
			Name    string `json:"name"`
			Trigger string `json:"trigger"`
		} `json:"hooks"`
		When struct {
			Type string `json:"type"`
		} `json:"when"`
	}
	if err := json.Unmarshal(stripJSONC(data), &doc); err != nil {
		return []importer.Found{{Kind: importer.KindSettings, Name: filepath.Base(source), Source: source, Status: importer.StatusError, Note: "invalid JSON: " + err.Error()}}
	}
	const hint = "Kiro's hook events and tool names differ: set it up by hand in Settings → Hooks"
	if len(doc.Hooks) == 0 {
		name := doc.Name
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(source), ".kiro.hook")
		}
		return []importer.Found{noted(importer.KindSettings, "hook "+name, source, joinNotes(doc.When.Type, hint))}
	}
	var out []importer.Found
	for _, h := range doc.Hooks {
		out = append(out, noted(importer.KindSettings, "hook "+h.Name, source, joinNotes(h.Trigger, hint)))
	}
	return out
}

func kiroHookFile(f importer.FolderFile) []importer.Found { return kiroHooks(f.Data, f.Rel) }

func kiroSpec(f importer.FolderFile) []importer.Found {
	return []importer.Found{noted(importer.KindInstructions, f.Rel, f.Rel, "a Kiro spec: it stays in the repository; reference it from AGENTS.md with @"+f.Rel+" if the agent should read it")}
}

func kiroUser(env importer.Env) []importer.Found {
	d := filepath.Join(env.Home, ".kiro")
	var out []importer.Found
	steering := filepath.Join(d, "steering")
	for _, rel := range userFiles(steering, ".md", false) {
		p := filepath.Join(steering, rel)
		data, _, err := readUser(p)
		if err != nil {
			out = append(out, failed(env, importer.KindRule, trimExt(rel, ".md"), p, err))
			continue
		}
		f := kiroSteering(importer.FolderFile{Rel: ".kiro/steering/" + rel, Data: data})
		f.Source = env.Display(p)
		out = append(out, f)
	}
	out = append(out, userMCP(env, filepath.Join(d, "settings", "mcp.json"), "mcpServers", true)...)
	hooks := filepath.Join(d, "hooks")
	for _, ext := range []string{".json", ".kiro.hook"} {
		for _, rel := range userFiles(hooks, ext, false) {
			p := filepath.Join(hooks, rel)
			data, _, err := readUser(p)
			if err != nil {
				out = append(out, failed(env, importer.KindSettings, rel, p, err))
				continue
			}
			out = append(out, kiroHooks(data, env.Display(p))...)
		}
	}
	return out
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "kiro", Name: "Kiro", Logo: "kiro",
			Description: "Steering, MCP servers and hooks from ~/.kiro; a folder's steering files, MCP servers, hooks and specs."},
		detect: func(env importer.Env) bool { return exists(filepath.Join(env.Home, ".kiro")) },
		user:   kiroUser,
		patterns: []importer.FolderPattern{
			{Glob: ".kiro/steering/*.md", Read: one(kiroSteering)},
			{Glob: ".kiro/settings/mcp.json", Read: func(f importer.FolderFile) []importer.Found {
				return mcpFile(f.Data, "mcpServers", f.Rel, true)
			}},
			{Glob: ".kiro/hooks/*.json", Read: kiroHookFile},
			{Glob: ".kiro/hooks/*.kiro.hook", Read: kiroHookFile},
			{Glob: ".kiro/specs/*/*.md", Read: kiroSpec},
		},
	})
}
