package subagent

import (
	"encoding/json"
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

// LoadDefinitions reads custom types from markdown files with frontmatter: name, description, tools (comma separated),
// model, read_only. The body is the type's instructions. A later directory replaces a type of the same name; the
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
		t.Name, t.Hooks = d.Name, nil
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
	for _, n := range strings.Split(memory.FrontmatterString(markdown, "tools"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			t.Tools = append(t.Tools, n)
		}
	}
	return t
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
