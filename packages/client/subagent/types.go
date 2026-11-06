package subagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/xlsft/harness/packages/client/memory"
	"github.com/xlsft/harness/packages/client/tools"
)

// Built-in agent types.
const (
	TypeGeneral = "general-purpose"
	TypeExplore = "Explore"
	TypePlan    = "Plan"
)

// Type is a kind of agent: built-in or loaded from a markdown definition.
type Type struct {
	tools.AgentType
	// Tools limits the agent to these tools (nil: every tool its depth allows).
	Tools []string
	// Model is the type's default model ("": the parent's).
	Model string
	// Instructions go into the agent's first message (custom types: the definition's body).
	Instructions string
	// Hooks is the definition's hooks frontmatter as JSON: they run for this agent's events while it works.
	Hooks json.RawMessage
	// DisallowedTools are taken away from what Tools leaves: tool names, or "mcp__server__*".
	DisallowedTools []string
	// Mode is the permission mode the definition asks for (permissions.AgentMode says what it gets).
	Mode string
	// MaxTurns limits the tool round trips of one turn (0: none).
	MaxTurns int
	// Effort is the agent's effort level ("": the session's).
	Effort string
	// Preload names skills whose text the agent gets with its first message.
	Preload []string
	// Isolation is tools.IsolationWorktree when every agent of this type gets a worktree.
	Isolation string
	// MCPServers names servers of the session this agent may use even with a Tools list.
	MCPServers []string
	// MCPInline are servers of its own, by name, in the shape of a .mcp.json entry. They run while the session does and
	// need a decision of the user, like a repository's.
	MCPInline map[string]json.RawMessage
	// reminder names the bundle text with the instructions of a built-in type.
	reminder string
}

// readOnlyTools are the tools of the read-only types. Bash stays: Check refuses every call the shell rules do not
// know to be read-only.
var readOnlyTools = []string{"Read", "Glob", "Grep", "LSP", "Bash", "WebFetch", "WebSearch"}

// BuiltinTypes returns the built-in types.
func BuiltinTypes() []Type {
	return []Type{
		{AgentType: tools.AgentType{Name: TypeGeneral, Description: "All tools; for work that changes things or takes many steps."}, reminder: "agent_type_general"},
		{AgentType: tools.AgentType{Name: TypeExplore, Description: "Read-only; for finding things across the code and reporting what is there.", ReadOnly: true}, Tools: readOnlyTools, reminder: "agent_type_explore"},
		{AgentType: tools.AgentType{Name: TypePlan, Description: "Read-only; for working out how to make a change and reporting a plan.", ReadOnly: true}, Tools: readOnlyTools, reminder: "agent_type_plan"},
	}
}

// leadOnlyTools talk to the user directly or change the session's own state, which belongs to its main agent. An
// agent asks through its report instead.
var leadOnlyTools = map[string]bool{"AskUserQuestion": true, "EnterPlanMode": true, "ExitPlanMode": true, "WrapUp": true, "Show": true, "Visual": true, "Workflow": true, "ScheduleWakeup": true, "ScheduleList": true, "ScheduleCancel": true}

// DefinitionDirs lists where custom types are looked for, later entries winning: the user's directory, then (for a
// trusted workspace) the project's.
func DefinitionDirs(harnessHome, cwd string, trusted bool) []string {
	var dirs []string
	if harnessHome != "" {
		dirs = append(dirs, filepath.Join(harnessHome, "agents"))
	}
	if trusted {
		dirs = append(dirs, filepath.Join(cwd, ".harness", "agents"))
	}
	return dirs
}

// LoadDefinitions reads custom types from markdown files with frontmatter: name, description, tools and disallowedTools
// (a list, or comma separated), model, read_only, permissionMode, maxTurns, effort, skills, isolation, mcpServers (names of
// the session's servers, or an entry with its own), hooks. The body is the type's instructions. A later directory replaces a type of the same name; the
// built-in names are kept.
func LoadDefinitions(dirs []string) []Type { return LoadDefinitionsOver(nil, dirs) }

// Document is a synced agent definition: its key and markdown.
type Document struct{ Key, Content string }

// LoadDefinitionsOver is LoadDefinitions with synced documents below the directories: a file on this device replaces the
// document of the same name.
func LoadDefinitionsOver(docs []Document, dirs []string) []Type {
	byName := map[string]Type{}
	for _, d := range docs {
		t := parseDefinition(d.Content, d.Key)
		if t.Name != TypeGeneral && t.Name != TypeExplore && t.Name != TypePlan {
			byName[t.Name] = t
		}
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			t := parseDefinition(string(data), strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
			if t.Name == TypeGeneral || t.Name == TypeExplore || t.Name == TypePlan {
				continue
			}
			byName[t.Name] = t
		}
	}
	out := make([]Type, 0, len(byName))
	for _, t := range byName {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// PluginDefinition is an agent file of a plugin, under the name it goes by.
type PluginDefinition struct {
	Name, Path string
	// Root and Data fill ${CLAUDE_PLUGIN_ROOT} and ${CLAUDE_PLUGIN_DATA} in the instructions.
	Root, Data string
}

// LoadPluginDefinitions reads a plugin's agents. A plugin's agent gets no hooks of its own: what a plugin runs is listed where
// the user approved it.
func LoadPluginDefinitions(defs []PluginDefinition) []Type {
	var out []Type
	for _, d := range defs {
		data, err := os.ReadFile(d.Path)
		if err != nil {
			continue
		}
		t := parseDefinition(string(data), d.Name)
		// What a plugin's agent may run or be allowed is what the plugin's approval listed.
		t.Name, t.Hooks, t.Mode, t.MCPInline = d.Name, nil, "", nil
		t.Instructions = strings.NewReplacer("${CLAUDE_PLUGIN_ROOT}", d.Root, "${HARNESS_PLUGIN_ROOT}", d.Root, "${CLAUDE_PLUGIN_DATA}", d.Data, "${HARNESS_PLUGIN_DATA}", d.Data).Replace(t.Instructions)
		out = append(out, t)
	}
	return out
}

func parseDefinition(markdown, fallback string) Type {
	t := Type{AgentType: tools.AgentType{Name: fallback}}
	_, body, ok := memory.SplitFrontmatter(markdown)
	if !ok {
		t.Instructions = strings.TrimSpace(markdown)
		return t
	}
	t.Instructions = strings.TrimSpace(body)
	if n := memory.FrontmatterString(markdown, "name"); n != "" {
		t.Name = n
	}
	t.Description = memory.FrontmatterString(markdown, "description")
	t.Model = memory.FrontmatterString(markdown, "model")
	switch strings.ToLower(memory.FrontmatterString(markdown, "read_only")) {
	case "true", "yes", "on", "1":
		t.ReadOnly = true
	}
	t.Hooks = frontmatterHooks(markdown)
	fm, _, _ := memory.SplitFrontmatter(markdown)
	var f definitionFields
	if yaml.Unmarshal([]byte(fm), &f) == nil {
		f.apply(&t)
	}
	return t
}

// definitionFields are the frontmatter fields beyond the plain strings. Both spellings of a two-word field are read.
type definitionFields struct {
	Tools              any    `yaml:"tools"`
	Disallowed         any    `yaml:"disallowedTools"`
	DisallowedSnake    any    `yaml:"disallowed_tools"`
	PermissionMode     string `yaml:"permissionMode"`
	PermissionModeSnak string `yaml:"permission_mode"`
	MaxTurns           int    `yaml:"maxTurns"`
	MaxTurnsSnake      int    `yaml:"max_turns"`
	Effort             string `yaml:"effort"`
	Skills             any    `yaml:"skills"`
	Isolation          string `yaml:"isolation"`
	MCPServers         any    `yaml:"mcpServers"`
	MCPServersSnake    any    `yaml:"mcp_servers"`
}

func (f definitionFields) apply(t *Type) {
	t.Tools = stringList(f.Tools)
	t.DisallowedTools = append(stringList(f.Disallowed), stringList(f.DisallowedSnake)...)
	t.Mode = firstNonEmpty(f.PermissionMode, f.PermissionModeSnak)
	t.MaxTurns = max(f.MaxTurns, f.MaxTurnsSnake, 0)
	t.Effort = strings.ToLower(strings.TrimSpace(f.Effort))
	t.Preload = stringList(f.Skills)
	if strings.TrimSpace(f.Isolation) == tools.IsolationWorktree {
		t.Isolation = tools.IsolationWorktree
	}
	servers := f.MCPServers
	if servers == nil {
		servers = f.MCPServersSnake
	}
	t.MCPServers, t.MCPInline = mcpServerField(servers)
	if strings.EqualFold(strings.TrimSpace(t.Model), "inherit") {
		t.Model = ""
	}
}

func firstNonEmpty(a, b string) string {
	if a = strings.TrimSpace(a); a != "" {
		return a
	}
	return strings.TrimSpace(b)
}

// stringList reads a list or a comma separated string.
func stringList(v any) []string {
	var parts []string
	switch x := v.(type) {
	case string:
		parts = strings.Split(x, ",")
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				parts = append(parts, s)
			}
		}
	}
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// mcpServerField reads mcpServers: a list whose items are a server's name or {name: entry}, or one map of entries.
func mcpServerField(v any) (names []string, inline map[string]json.RawMessage) {
	add := func(name string, entry any) {
		raw, err := json.Marshal(entry)
		if err != nil {
			return
		}
		if inline == nil {
			inline = map[string]json.RawMessage{}
		}
		inline[name] = raw
	}
	switch x := v.(type) {
	case string:
		names = stringList(x)
	case map[string]any:
		for name, entry := range x {
			add(name, entry)
		}
	case []any:
		for _, item := range x {
			switch i := item.(type) {
			case string:
				if i = strings.TrimSpace(i); i != "" {
					names = append(names, i)
				}
			case map[string]any:
				for name, entry := range i {
					add(name, entry)
				}
			}
		}
	}
	return names, inline
}

// allows says whether the type gives its agent a tool, leaving depth and the lead's own tools aside.
func (t Type) allows(name string) bool {
	if matchTool(t.DisallowedTools, name) {
		return false
	}
	if t.Tools == nil || matchTool(t.Tools, name) {
		return true
	}
	for _, s := range t.MCPServers {
		if strings.HasPrefix(name, "mcp__"+strings.ReplaceAll(s, "-", "_")+"__") || strings.HasPrefix(name, "mcp__"+s+"__") {
			return true
		}
	}
	return false
}

func matchTool(list []string, name string) bool {
	for _, p := range list {
		if p == name || (strings.HasSuffix(p, "*") && strings.HasPrefix(name, strings.TrimSuffix(p, "*"))) {
			return true
		}
	}
	return false
}

// InlineDefinitions reads types given on the command line, for this run only: a JSON object from name to description,
// prompt (the instructions) and the frontmatter fields. It reads as a definition would, so the rules are the same.
func InlineDefinitions(raw string) ([]Type, error) {
	var defs map[string]map[string]any
	if err := json.Unmarshal([]byte(raw), &defs); err != nil {
		return nil, fmt.Errorf("--agents is not a JSON object of agents: %w", err)
	}
	var out []Type
	for name, d := range defs {
		if name == TypeGeneral || name == TypeExplore || name == TypePlan {
			return nil, fmt.Errorf("--agents: %s is a built-in type", name)
		}
		prompt, _ := d["prompt"].(string)
		description, _ := d["description"].(string)
		if strings.TrimSpace(prompt) == "" || strings.TrimSpace(description) == "" {
			return nil, fmt.Errorf("--agents: %s needs a description and a prompt", name)
		}
		delete(d, "prompt")
		d["name"] = name
		fm, err := yaml.Marshal(d)
		if err != nil {
			return nil, err
		}
		out = append(out, parseDefinition("---\n"+string(fm)+"---\n"+prompt, name))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// frontmatterHooks reads a definition's hooks field as JSON (nil: none, or not readable).
func frontmatterHooks(markdown string) json.RawMessage {
	fm, _, ok := memory.SplitFrontmatter(markdown)
	if !ok {
		return nil
	}
	var fields struct {
		Hooks map[string]any `yaml:"hooks"`
	}
	if yaml.Unmarshal([]byte(fm), &fields) != nil || len(fields.Hooks) == 0 {
		return nil
	}
	raw, err := json.Marshal(fields.Hooks)
	if err != nil {
		return nil
	}
	return raw
}
