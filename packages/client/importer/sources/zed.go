package sources

import (
	"encoding/json"
	"path/filepath"

	"github.com/xlsft/harness/packages/client/importer"
)

// Zed (https://zed.dev/docs/ai/mcp, https://zed.dev/docs/configuring-zed, https://zed.dev/docs/ai/instructions).
//
// User level: ~/.config/zed (or $XDG_CONFIG_HOME/zed; %APPDATA%\Zed on Windows): settings.json (JSONC)
// "context_servers" → MCP servers, as {command, args, env}, {url, headers}, or the older {source: custom, command: {path,
// args, env}}; servers an extension provides (only "settings") are installed with the extension, so they are listed to
// set up by hand. AGENTS.md there → instructions. Skills and AGENTS.md replaced the Rules Library, so there is no
// database to read.
// Folder level: .rules at the root (first of Zed's list; the other names belong to other tools), .zed/settings.json.

type zedServer struct {
	Source   string            `json:"source"`
	Command  json.RawMessage   `json:"command"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	Settings json.RawMessage   `json:"settings"`
}

func zedServers(data []byte, source string) []importer.Found {
	var cfg struct {
		ContextServers map[string]zedServer `json:"context_servers"`
	}
	if err := json.Unmarshal(stripJSONC(data), &cfg); err != nil {
		return []importer.Found{{Kind: importer.KindMCP, Name: filepath.Base(source), Source: source, Status: importer.StatusError, Note: "invalid JSON: " + err.Error()}}
	}
	var out []importer.Found
	for _, name := range sortedNames(cfg.ContextServers) {
		s := cfg.ContextServers[name]
		m := &importer.MCPServer{Args: s.Args, Env: s.Env, URL: s.URL, Headers: s.Headers}
		// command is a string, or {path, args, env} in the older form.
		var cmd string
		if json.Unmarshal(s.Command, &cmd) == nil {
			m.Command = cmd
		} else {
			var old struct {
				Path string            `json:"path"`
				Args []string          `json:"args"`
				Env  map[string]string `json:"env"`
			}
			if json.Unmarshal(s.Command, &old) == nil && old.Path != "" {
				m.Command, m.Args, m.Env = old.Path, old.Args, old.Env
			}
		}
		if m.Command == "" && m.URL == "" {
			out = append(out, noted(importer.KindMCP, name, source, "a Zed extension provides it: add the server by hand"))
			continue
		}
		out = append(out, importer.Found{Kind: importer.KindMCP, Name: name, Source: source, MCP: m})
	}
	return out
}

func zedDir(env importer.Env) string {
	if env.GOOS == "windows" {
		return filepath.Join(env.UserConfigDir(), "Zed")
	}
	return filepath.Join(env.UserConfigDirXDG(), "zed")
}

func zedUser(env importer.Env) []importer.Found {
	d := zedDir(env)
	out := userInstructions(env, filepath.Join(d, "AGENTS.md"), "AGENTS.md")
	p := filepath.Join(d, "settings.json")
	data, ok, err := readUser(p)
	switch {
	case !ok:
	case err != nil:
		out = append(out, failed(env, importer.KindMCP, "settings.json", p, err))
	default:
		out = append(out, zedServers(data, env.Display(p))...)
	}
	return out
}

func init() {
	register(&reader{
		info: importer.SourceInfo{ID: "zed", Name: "Zed", Logo: "zed",
			Description: "AGENTS.md and context servers from Zed's settings; a folder's .rules and .zed/settings.json."},
		detect: func(env importer.Env) bool { return exists(zedDir(env)) },
		user:   zedUser,
		patterns: []importer.FolderPattern{
			{Glob: ".rules", Read: instructions},
			{Glob: ".zed/settings.json", Read: func(f importer.FolderFile) []importer.Found { return zedServers(f.Data, f.Rel) }},
		},
	})
}
