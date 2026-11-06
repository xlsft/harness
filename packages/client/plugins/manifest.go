// Package plugins installs and loads plugins: folders that bundle skills, custom commands, agents, output styles, hooks and
// MCP servers, fetched on their own or from a marketplace. See docs/design/plugins.md for the contract.
//
// A plugin follows the format other agents use (.claude-plugin/plugin.json and its folder layout); .harness-plugin/plugin.json
// is our own location for the same file and wins when both exist. Installing copies the plugin into HARNESS_HOME/plugins,
// pinned to the commit and content it had. Nothing of it runs until this device approved exactly that content and the session's
// folder is trusted; all of that state is a file of this device and is never synced.
//
// The package knows the formats, the store and the gates. What a session does with a plugin that may run is package bootstrap's.
package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/xlsft/harness/packages/client/memory"
	"github.com/xlsft/harness/packages/client/skills"
)

// The folders a manifest can be in, the first one found winning.
var manifestDirs = []string{".harness-plugin", ".claude-plugin"}

// Manifest and marketplace file names.
const (
	ManifestFile    = "plugin.json"
	MarketplaceFile = "marketplace.json"
)

// nameRe is what a plugin or marketplace name may hold: it is part of an id, of a folder's name and of what follows a slash.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidName reports whether a plugin or marketplace name can be used.
func ValidName(s string) bool { return nameRe.MatchString(s) && !strings.Contains(s, "..") }

// Manifest is a plugin.json. Component fields are kept raw: each takes several shapes.
type Manifest struct {
	Name           string          `json:"name,omitempty"`
	DisplayName    string          `json:"displayName,omitempty"`
	Version        string          `json:"version,omitempty"`
	Description    string          `json:"description,omitempty"`
	Author         json.RawMessage `json:"author,omitempty"`
	Homepage       string          `json:"homepage,omitempty"`
	Repository     json.RawMessage `json:"repository,omitempty"`
	License        string          `json:"license,omitempty"`
	Keywords       []string        `json:"keywords,omitempty"`
	DefaultEnabled *bool           `json:"defaultEnabled,omitempty"`

	Skills       json.RawMessage `json:"skills,omitempty"`
	Commands     json.RawMessage `json:"commands,omitempty"`
	Agents       json.RawMessage `json:"agents,omitempty"`
	Hooks        json.RawMessage `json:"hooks,omitempty"`
	MCPServers   json.RawMessage `json:"mcpServers,omitempty"`
	OutputStyles json.RawMessage `json:"outputStyles,omitempty"`

	// What the format has and this client does not act on; they are reported, not ignored in silence.
	LSPServers   json.RawMessage `json:"lspServers,omitempty"`
	Workflows    json.RawMessage `json:"workflows,omitempty"`
	UserConfig   json.RawMessage `json:"userConfig,omitempty"`
	Dependencies json.RawMessage `json:"dependencies,omitempty"`
	Channels     json.RawMessage `json:"channels,omitempty"`
	Experimental json.RawMessage `json:"experimental,omitempty"`
	Settings     json.RawMessage `json:"settings,omitempty"`
	Themes       json.RawMessage `json:"themes,omitempty"`
	Monitors     json.RawMessage `json:"monitors,omitempty"`
}

// ReadManifest reads the manifest of a plugin folder; found is false for a folder without one, which is allowed.
func ReadManifest(root string) (m Manifest, found bool, err error) {
	for _, d := range manifestDirs {
		data, err := os.ReadFile(filepath.Join(root, d, ManifestFile))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return m, false, err
		}
		if err := json.Unmarshal(data, &m); err != nil {
			return m, true, fmt.Errorf("%s/%s: %w", d, ManifestFile, err)
		}
		m.dropNulls()
		return m, true, nil
	}
	return m, false, nil
}

// HookInfo is one handler a plugin would run, as the user reads it before approving.
type HookInfo struct {
	Event, Matcher, Type, Command string
	// From says where it is defined when that isn't the plugin's hooks: a skill of it.
	From string
}

// MCPInfo is one MCP server a plugin would start or reach.
type MCPInfo struct {
	Name, Transport string
	// Command is the command line, or the URL.
	Command string
}

// Contents is what a plugin holds, by the names it would go by.
type Contents struct {
	Skills, Commands, Agents, OutputStyles []string
	Hooks                                  []HookInfo
	MCPServers                             []MCPInfo
	// AllowedTools are the permission rules its skills and commands grant while they run.
	AllowedTools []string
	// Skipped names what it holds and this client does not act on.
	Skipped []string
}

// AgentFile is an agent of a plugin, under the name it goes by.
type AgentFile struct{ Name, Path string }

// StyleFile is an output style of a plugin, under the id it goes by.
type StyleFile struct{ ID, Path string }

// Plugin is a plugin folder, read.
type Plugin struct {
	// ID is <entry name>@<marketplace>; Name the prefix of everything it holds (the manifest's name).
	ID, Name    string
	Marketplace string
	Manifest    Manifest
	// Root is the folder; Data the folder that outlives updates.
	Root, Data string

	SkillDirs []string
	Commands  []skills.PluginCommand
	Agents    []AgentFile
	Styles    []StyleFile
	// HookBlocks are event maps, in the shape of the settings key.
	HookBlocks []json.RawMessage
	// MCP holds the raw server entries by the name inside the plugin.
	MCP map[string]json.RawMessage

	Contents Contents
	// Problems say what could not be read; the rest of the plugin still loads.
	Problems []string
}

// SkillSource is the plugin as package skills reads it.
func (p *Plugin) SkillSource() skills.PluginSource {
	return skills.PluginSource{Prefix: p.Name, ID: p.ID, Root: p.Root, Data: p.Data, SkillDirs: p.SkillDirs, Commands: p.Commands}
}

// Env is what a process of the plugin gets, under the names users' scripts read and under ours.
func (p *Plugin) Env() []string {
	return []string{"CLAUDE_PLUGIN_ROOT=" + p.Root, "HARNESS_PLUGIN_ROOT=" + p.Root, "CLAUDE_PLUGIN_DATA=" + p.Data, "HARNESS_PLUGIN_DATA=" + p.Data}
}

// Vars are the placeholders a plugin's MCP configuration can name.
func (p *Plugin) Vars() map[string]string {
	return map[string]string{"CLAUDE_PLUGIN_ROOT": p.Root, "HARNESS_PLUGIN_ROOT": p.Root, "CLAUDE_PLUGIN_DATA": p.Data, "HARNESS_PLUGIN_DATA": p.Data}
}

// Empty reports a folder that holds nothing a plugin can.
func (c Contents) Empty() bool {
	return len(c.Skills)+len(c.Commands)+len(c.Agents)+len(c.OutputStyles)+len(c.Hooks)+len(c.MCPServers) == 0
}

// Overlay is what a marketplace entry adds to a manifest.
type Overlay struct {
	Strict   *bool
	Manifest Manifest
}

// Load reads a plugin folder. name and marketplace form its id. The overlay may be nil.
func Load(root, name, marketplace, dataDir string, over *Overlay) (*Plugin, error) {
	m, found, err := ReadManifest(root)
	if err != nil {
		return nil, err
	}
	if found && over != nil && over.Strict != nil && !*over.Strict && over.Manifest.hasComponents() {
		return nil, fmt.Errorf("plugin %s has conflicting manifests: both %s and the marketplace entry specify components", name, ManifestFile)
	}
	if !found && over != nil {
		m = over.Manifest
	}
	m.dropNulls()
	if m.Name == "" {
		m.Name = name
	}
	if !ValidName(m.Name) {
		return nil, fmt.Errorf("the plugin name %q may use only letters, digits, \".\", \"_\" and \"-\", and must start with a letter or digit", m.Name)
	}
	if name == "" {
		name = m.Name
	}
	p := &Plugin{ID: name + "@" + marketplace, Name: m.Name, Marketplace: marketplace, Manifest: m, Root: root, Data: dataDir, MCP: map[string]json.RawMessage{}}
	var extra *Manifest
	if found && over != nil {
		e := over.Manifest
		e.dropNulls()
		extra = &e
	}
	p.loadSkills(extra)
	p.loadCommands(extra)
	p.loadAgents(extra)
	p.loadStyles(extra)
	p.loadHooks(extra)
	p.loadMCP()
	p.describe()
	return p, nil
}

// dropNulls makes a field set to null the same as one left out.
func (m *Manifest) dropNulls() {
	for _, f := range []*json.RawMessage{&m.Skills, &m.Commands, &m.Agents, &m.Hooks, &m.MCPServers, &m.OutputStyles, &m.LSPServers, &m.Workflows, &m.UserConfig, &m.Dependencies, &m.Channels, &m.Experimental, &m.Settings, &m.Themes, &m.Monitors} {
		if string(bytes.TrimSpace(*f)) == "null" {
			*f = nil
		}
	}
}

func (m Manifest) hasComponents() bool {
	return len(m.Commands)+len(m.Agents)+len(m.Skills)+len(m.Hooks)+len(m.OutputStyles)+len(m.Themes) > 0
}

// resolve makes a path of the manifest absolute. It starts with ./, stays in the folder and exists.
func (p *Plugin) resolve(rel, what string) (string, bool) {
	if rel != "." && !strings.HasPrefix(rel, "./") {
		p.problem("%s path must start with \"./\": %s", what, rel)
		return "", false
	}
	if slices.Contains(strings.Split(filepath.ToSlash(rel), "/"), "..") {
		p.problem("%s path escapes the plugin folder: %s", what, rel)
		return "", false
	}
	abs := filepath.Join(p.Root, filepath.FromSlash(rel))
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		p.problem("%s path not found: %s", what, rel)
		return "", false
	}
	root, err := filepath.EvalSymlinks(p.Root)
	if err != nil || !inside(real, root) {
		p.problem("%s path escapes the plugin folder: %s", what, rel)
		return "", false
	}
	return abs, true
}

func inside(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (p *Plugin) problem(format string, args ...any) {
	p.Problems = append(p.Problems, fmt.Sprintf(format, args...))
}

// paths reads a path, or a list of them.
func paths(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, errors.New("expected a path or a list of paths")
	}
	return many, nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// loadSkills: the skills field adds to the default folder. A plugin with SKILL.md at its root and no skills is one skill.
func (p *Plugin) loadSkills(extra *Manifest) {
	dirs := []string{}
	if d := filepath.Join(p.Root, "skills"); isDir(d) {
		dirs = append(dirs, d)
	}
	add := func(raw json.RawMessage) {
		list, err := paths(raw)
		if err != nil {
			p.problem("skills: %v", err)
		}
		for _, rel := range list {
			if abs, ok := p.resolve(rel, "skills"); ok {
				if !slices.Contains(dirs, abs) {
					dirs = append(dirs, abs)
				}
			}
		}
	}
	add(p.Manifest.Skills)
	if extra != nil {
		add(extra.Skills)
	}
	if len(dirs) == 0 && isFile(filepath.Join(p.Root, skills.SkillFile)) {
		dirs = append(dirs, p.Root)
	}
	p.SkillDirs = dirs
}

// loadCommands: the commands field replaces the default folder, and takes paths or a map of named commands.
func (p *Plugin) loadCommands(extra *Manifest) {
	read := func(raw json.RawMessage) {
		var named map[string]struct {
			Source       string   `json:"source"`
			Content      string   `json:"content"`
			Description  string   `json:"description"`
			ArgumentHint string   `json:"argumentHint"`
			Model        string   `json:"model"`
			AllowedTools []string `json:"allowedTools"`
		}
		if len(raw) > 0 && raw[0] == '{' {
			if err := json.Unmarshal(raw, &named); err != nil {
				p.problem("commands: %v", err)
				return
			}
			keys := make([]string, 0, len(named))
			for k := range named {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				e := named[k]
				if (e.Source == "") == (e.Content == "") {
					p.problem("command %s must set exactly one of source and content", k)
					continue
				}
				c := skills.PluginCommand{Name: k, Content: e.Content, Description: e.Description, ArgumentHint: e.ArgumentHint, Model: e.Model, AllowedTools: e.AllowedTools}
				if e.Source != "" {
					abs, ok := p.resolve(e.Source, "commands")
					if !ok {
						continue
					}
					c.Path = abs
				}
				p.Commands = append(p.Commands, c)
			}
			return
		}
		list, err := paths(raw)
		if err != nil {
			p.problem("commands: %v", err)
		}
		for _, rel := range list {
			if abs, ok := p.resolve(rel, "commands"); ok {
				p.Commands = append(p.Commands, skills.PluginCommand{Path: abs})
			}
		}
	}
	if len(p.Manifest.Commands) > 0 {
		read(p.Manifest.Commands)
	} else if d := filepath.Join(p.Root, "commands"); isDir(d) {
		p.Commands = append(p.Commands, skills.PluginCommand{Path: d})
	}
	if extra != nil {
		read(extra.Commands)
	}
}

// loadAgents: the agents field replaces the default folder. Subfolders of a folder are part of an agent's name.
func (p *Plugin) loadAgents(extra *Manifest) {
	add := func(path, rel string) {
		stem := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
		name := strings.ReplaceAll(stem, "/", ":")
		data, err := os.ReadFile(path)
		if err != nil {
			p.problem("agent %s: %v", rel, err)
			return
		}
		if n := memory.FrontmatterString(string(data), "name"); n != "" {
			prefix := name[:strings.LastIndex(name, ":")+1]
			name = prefix + n
		}
		p.Agents = append(p.Agents, AgentFile{Name: p.Name + ":" + name, Path: path})
	}
	walk := func(dir string) {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".md") {
				return nil
			}
			rel, _ := filepath.Rel(dir, path)
			add(path, rel)
			return nil
		})
	}
	read := func(raw json.RawMessage) {
		list, err := paths(raw)
		if err != nil {
			p.problem("agents: %v", err)
		}
		for _, rel := range list {
			abs, ok := p.resolve(rel, "agents")
			switch {
			case !ok:
			case isDir(abs):
				walk(abs)
			default:
				add(abs, filepath.Base(abs))
			}
		}
	}
	if len(p.Manifest.Agents) > 0 {
		read(p.Manifest.Agents)
	} else if d := filepath.Join(p.Root, "agents"); isDir(d) {
		walk(d)
	}
	if extra != nil {
		read(extra.Agents)
	}
}

// loadStyles: the outputStyles field replaces the default folder.
func (p *Plugin) loadStyles(extra *Manifest) {
	add := func(path string) {
		stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		data, err := os.ReadFile(path)
		if err != nil {
			p.problem("output style %s: %v", path, err)
			return
		}
		if n := memory.FrontmatterString(string(data), "name"); n != "" {
			stem = n
		}
		p.Styles = append(p.Styles, StyleFile{ID: p.Name + ":" + stem, Path: path})
	}
	dir := func(d string) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".md") {
				add(filepath.Join(d, e.Name()))
			}
		}
	}
	read := func(raw json.RawMessage) {
		list, err := paths(raw)
		if err != nil {
			p.problem("outputStyles: %v", err)
		}
		for _, rel := range list {
			abs, ok := p.resolve(rel, "outputStyles")
			switch {
			case !ok:
			case isDir(abs):
				dir(abs)
			default:
				add(abs)
			}
		}
	}
	if len(p.Manifest.OutputStyles) > 0 {
		read(p.Manifest.OutputStyles)
	} else {
		dir(filepath.Join(p.Root, "output-styles"))
	}
	if extra != nil {
		read(extra.OutputStyles)
	}
}

// loadHooks merges hooks/hooks.json with what the manifest declares: files wrap the event map in a "hooks" key, inline objects
// are the event map itself.
func (p *Plugin) loadHooks(extra *Manifest) {
	file := func(path, rel string) {
		data, err := os.ReadFile(path)
		if err != nil {
			p.problem("hooks %s: %v", rel, err)
			return
		}
		var f struct {
			Hooks json.RawMessage `json:"hooks,omitempty"`
		}
		if err := json.Unmarshal(data, &f); err != nil {
			p.problem("hooks %s: %v", rel, err)
			return
		}
		if len(f.Hooks) == 0 {
			p.problem("hooks %s: the file must hold a top-level \"hooks\" key", rel)
			return
		}
		p.HookBlocks = append(p.HookBlocks, f.Hooks)
	}
	var read func(raw json.RawMessage)
	read = func(raw json.RawMessage) {
		if len(raw) == 0 {
			return
		}
		switch raw[0] {
		case '{':
			p.HookBlocks = append(p.HookBlocks, raw)
		case '[':
			var list []json.RawMessage
			if err := json.Unmarshal(raw, &list); err != nil {
				p.problem("hooks: %v", err)
				return
			}
			for _, r := range list {
				read(r)
			}
		case '"':
			var rel string
			if json.Unmarshal(raw, &rel) == nil {
				if abs, ok := p.resolve(rel, "hooks"); ok {
					file(abs, rel)
				}
			}
		default:
			p.problem("hooks: expected a path, an object or a list of them")
		}
	}
	if d := filepath.Join(p.Root, "hooks", "hooks.json"); isFile(d) {
		file(d, "hooks/hooks.json")
	}
	read(p.Manifest.Hooks)
	if extra != nil {
		read(extra.Hooks)
	}
}

// loadMCP merges .mcp.json with what the manifest declares; a name declared later replaces an earlier one. A file holds
// the servers under "mcpServers" or as its top level.
func (p *Plugin) loadMCP() {
	servers := func(data []byte, what string) {
		var wrapped struct {
			Servers map[string]json.RawMessage `json:"mcpServers,omitempty"`
		}
		if err := json.Unmarshal(data, &wrapped); err != nil {
			p.problem("%s: %v", what, err)
			return
		}
		list := wrapped.Servers
		if list == nil {
			if err := json.Unmarshal(data, &list); err != nil {
				p.problem("%s: %v", what, err)
				return
			}
		}
		for k, v := range list {
			p.MCP[k] = v
		}
	}
	var read func(raw json.RawMessage)
	read = func(raw json.RawMessage) {
		if len(raw) == 0 {
			return
		}
		switch raw[0] {
		case '{':
			servers(raw, "mcpServers")
		case '[':
			var list []json.RawMessage
			if err := json.Unmarshal(raw, &list); err != nil {
				p.problem("mcpServers: %v", err)
				return
			}
			for _, r := range list {
				read(r)
			}
		case '"':
			var rel string
			if json.Unmarshal(raw, &rel) != nil {
				return
			}
			if !strings.HasSuffix(rel, ".json") {
				p.Contents.Skipped = append(p.Contents.Skipped, "MCP bundle "+rel)
				return
			}
			if abs, ok := p.resolve(rel, "mcpServers"); ok {
				if data, err := os.ReadFile(abs); err == nil {
					servers(data, rel)
				} else {
					p.problem("%s: %v", rel, err)
				}
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(p.Root, ".mcp.json")); err == nil {
		servers(data, ".mcp.json")
	}
	read(p.Manifest.MCPServers)
}

// describe fills Contents.
func (p *Plugin) describe() {
	c := &p.Contents
	addHooks := func(raw json.RawMessage, from string) {
		var events map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				URL     string `json:"url"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(raw, &events); err != nil {
			p.problem("hooks: %v", err)
			return
		}
		names := make([]string, 0, len(events))
		for ev := range events {
			names = append(names, ev)
		}
		sort.Strings(names)
		for _, ev := range names {
			for _, g := range events[ev] {
				for _, h := range g.Hooks {
					cmd := h.Command
					if h.Type == "http" {
						cmd = h.URL
					}
					c.Hooks = append(c.Hooks, HookInfo{Event: ev, Matcher: g.Matcher, Type: h.Type, Command: cmd, From: from})
				}
			}
		}
	}
	for _, s := range skills.ScanPlugin(p.SkillSource()) {
		if s.Kind == skills.KindCommand {
			c.Commands = append(c.Commands, s.Name)
		} else {
			c.Skills = append(c.Skills, s.Name)
		}
		for _, r := range s.AllowedTools {
			if !slices.Contains(c.AllowedTools, r) {
				c.AllowedTools = append(c.AllowedTools, r)
			}
		}
		if len(s.Hooks) > 0 {
			addHooks(s.Hooks, s.Name)
		}
	}
	for _, a := range p.Agents {
		c.Agents = append(c.Agents, a.Name)
	}
	for _, s := range p.Styles {
		c.OutputStyles = append(c.OutputStyles, s.ID)
	}
	for _, b := range p.HookBlocks {
		addHooks(b, "")
	}
	names := make([]string, 0, len(p.MCP))
	for n := range p.MCP {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		var e struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
			URL     string   `json:"url"`
		}
		if err := json.Unmarshal(p.MCP[n], &e); err != nil {
			p.problem("MCP server %s: %v", n, err)
			continue
		}
		info := MCPInfo{Name: MCPServerName(p.Name, n), Transport: e.Type, Command: e.URL}
		if e.Command != "" {
			info.Command = strings.Join(append([]string{e.Command}, e.Args...), " ")
			if info.Transport == "" {
				info.Transport = "stdio"
			}
		}
		c.MCPServers = append(c.MCPServers, info)
	}
	m := p.Manifest
	skipped := func(raw json.RawMessage, what string) {
		if len(raw) > 0 && string(raw) != "null" {
			c.Skipped = append(c.Skipped, what)
		}
	}
	skipped(m.LSPServers, "language servers (lspServers)")
	skipped(m.Workflows, "workflows")
	skipped(m.UserConfig, "user configuration (userConfig)")
	skipped(m.Dependencies, "dependencies")
	skipped(m.Channels, "channels")
	skipped(m.Experimental, "experimental components")
	skipped(m.Settings, "default settings")
	skipped(m.Themes, "themes")
	skipped(m.Monitors, "monitors")
	for rel, what := range map[string]string{".lsp.json": "language servers (.lsp.json)", "bin": "executables (bin/)", "themes": "themes", "workflows": "workflows", "monitors/monitors.json": "monitors", "settings.json": "default settings"} {
		if _, err := os.Stat(filepath.Join(p.Root, filepath.FromSlash(rel))); err == nil && !slices.Contains(c.Skipped, what) {
			c.Skipped = append(c.Skipped, what)
		}
	}
	sort.Strings(c.Skipped)
}

// MCPServerName is what a plugin's server is called in a session.
func MCPServerName(plugin, server string) string { return "plugin:" + plugin + ":" + server }
