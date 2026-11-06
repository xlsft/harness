package subagent

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

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
var leadOnlyTools = map[string]bool{"AskUserQuestion": true, "EnterPlanMode": true, "ExitPlanMode": true, "WrapUp": true, "Show": true, "Visual": true, "Workflow": true}

// DefinitionDirs lists where custom types are looked for, later entries winning: the user's directories, then (for
// a trusted workspace) the project's.
func DefinitionDirs(harnessHome, home, cwd string, trusted bool) []string {
	var dirs []string
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".claude", "agents"))
	}
	if harnessHome != "" {
		dirs = append(dirs, filepath.Join(harnessHome, "agents"))
	}
	if trusted {
		dirs = append(dirs, filepath.Join(cwd, ".claude", "agents"), filepath.Join(cwd, ".harness", "agents"))
	}
	return dirs
}

// LoadDefinitions reads custom types from markdown files with frontmatter: name, description, tools (comma separated),
// model, read_only. The body is the type's instructions. A later directory replaces a type of the same name; the
// built-in names are kept.
func LoadDefinitions(dirs []string) []Type {
	byName := map[string]Type{}
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
	for _, n := range strings.Split(memory.FrontmatterString(markdown, "tools"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			t.Tools = append(t.Tools, n)
		}
	}
	return t
}
