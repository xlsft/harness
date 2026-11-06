// Package skills finds the skills and custom commands of a session and turns them into text for the model.
//
// A skill is a folder holding SKILL.md (a Markdown file with YAML frontmatter) and whatever other files it needs; a
// custom command is a single Markdown file with the same frontmatter. Both are run the same way: the user types
// `/name args`, or the model calls the Skill tool. They follow the format other agents use, so the folders of
// ~/.claude and .claude work unchanged.
//
// Disclosure is progressive: discovery keeps the frontmatter only, the model's listing holds names and descriptions
// within a budget (Listing), the body is read from its source when the skill runs (Skill.LoadBody), and the other files
// of the folder are read by the model if the body points at them.
package skills

import (
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/xlsft/harness/packages/client/memory"
	"github.com/xlsft/harness/packages/client/permissions"
)

// Kind tells skills from custom commands.
type Kind string

// Kinds.
const (
	KindSkill   Kind = "skill"
	KindCommand Kind = "command"
)

// Sources, from most to least authoritative. On a name clash the earlier one wins.
const (
	SourceOrganization = "organization"
	SourcePersonal     = "personal"
	SourceImported     = "imported"
	SourceProject      = "project"
	SourcePlugin       = "plugin"
)

// Skill is one skill or custom command as discovery sees it: everything but the body.
type Skill struct {
	Name         string
	Description  string
	WhenToUse    string
	ArgumentHint string
	// Arguments names the positional arguments, for $name in the body.
	Arguments []string
	// AllowedTools are permission rules that apply without a prompt while the skill is active.
	AllowedTools []string
	Model        string
	Effort       string
	// DisableModelInvocation keeps it out of the listing and away from the Skill tool.
	DisableModelInvocation bool
	// UserInvocable is false for what only the model may run: no `/name`, not in the menu.
	UserInvocable bool
	// Fork runs it in a subagent; Agent is that subagent's type ("" for the general one).
	Fork  bool
	Agent string
	// Paths hold it back from the listing until a file matching one of these globs was touched.
	Paths []string
	// Hooks is the frontmatter's hooks block as JSON, in the shape of the settings key: they run while the skill is at work.
	Hooks json.RawMessage

	// Plugin is the id of the plugin it came from; PluginRoot and PluginData are that plugin's folders.
	Plugin, PluginRoot, PluginData string

	Kind   Kind
	Source string
	// Path is the Markdown file, or "sync:<document id>".
	Path string
	// Dir is the skill's folder on disk ("" for a command, and for a document until Materialize).
	Dir string

	// Skipped names the frontmatter fields read but not acted on; Problems says what's wrong, for the user.
	Skipped  []string
	Problems []string
	// ShadowedBy is the source of the entry of this name that wins ("" for the winner).
	ShadowedBy string

	// doc is set for a skill stored on the server: its text and files come from there.
	doc docBody
	// order is the position in the scan; earlier wins inside a source and kind.
	order int
}

// Fields of the format that describe a skill without changing what the client does.
var informational = map[string]bool{"license": true, "compatibility": true, "metadata": true, "version": true}

// nameRe is what can follow the slash: folder namespaces use ":".
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// ValidName reports whether name can be typed after a slash.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Parse reads a skill or command file. fallback is the name the location gives it (the folder, or the file's path in
// the commands folder); a command is always called that, as its folder is its namespace.
func Parse(markdown, fallback string, kind Kind) Skill {
	s := Skill{Name: fallback, Kind: kind, UserInvocable: true}
	fm, body, ok := memory.SplitFrontmatter(markdown)
	if !ok {
		s.Description = firstLine(body)
		return s
	}
	fields := map[string]any{}
	if err := yaml.Unmarshal([]byte(fm), &fields); err != nil {
		// An unquoted "description: Use when: ..." is not YAML, and is common: read such a file line by line.
		fields = looseFields(fm)
		s.Problems = append(s.Problems, "The frontmatter isn't valid YAML; it was read line by line.")
	}
	normal := make(map[string]any, len(fields))
	for k, v := range fields {
		normal[strings.ReplaceAll(strings.ToLower(k), "_", "-")] = v
	}
	if name := scalar(normal["name"]); name != "" && kind == KindSkill {
		if ValidName(name) {
			s.Name = name
		} else {
			s.Problems = append(s.Problems, "The name \""+name+"\" can't follow a slash; using \""+fallback+"\".")
		}
	}
	s.Description = strings.TrimSpace(scalar(normal["description"]))
	if s.Description == "" {
		s.Description = firstLine(body)
	}
	s.WhenToUse = strings.TrimSpace(scalar(normal["when-to-use"]))
	s.ArgumentHint = strings.TrimSpace(scalar(normal["argument-hint"]))
	s.Arguments = wordList(normal["arguments"])
	s.AllowedTools = ruleList(normal["allowed-tools"])
	s.Model = strings.TrimSpace(scalar(normal["model"]))
	if strings.EqualFold(s.Model, "inherit") {
		s.Model = ""
	}
	s.Effort = strings.ToLower(strings.TrimSpace(scalar(normal["effort"])))
	s.DisableModelInvocation = truth(normal["disable-model-invocation"], false)
	s.UserInvocable = truth(normal["user-invocable"], true)
	if v := strings.ToLower(strings.TrimSpace(scalar(normal["context"]))); v == "fork" {
		s.Fork = true
	} else if v != "" {
		s.Problems = append(s.Problems, "Unknown context \""+v+"\"; it runs in the conversation.")
	}
	s.Agent = strings.TrimSpace(scalar(normal["agent"]))
	s.Paths = commaList(normal["paths"])
	if h, ok := normal["hooks"]; ok && h != nil {
		if raw, err := json.Marshal(h); err == nil {
			s.Hooks = raw
		} else {
			s.Problems = append(s.Problems, "The hooks field can't be read.")
		}
	}
	for k := range normal {
		switch k {
		case "name", "description", "when-to-use", "argument-hint", "arguments", "allowed-tools", "model", "effort",
			"disable-model-invocation", "user-invocable", "context", "agent", "paths", "hooks":
		case "shell":
			// Commands in a body run in the session's shell, whatever this asks for, unless it asks for what that is.
			if !slices.Contains([]string{"", "bash", "sh", "zsh"}, strings.ToLower(strings.TrimSpace(scalar(normal[k])))) {
				s.Skipped = append(s.Skipped, k)
			}
		default:
			if !informational[k] {
				s.Skipped = append(s.Skipped, k)
			}
		}
	}
	sort.Strings(s.Skipped)
	return s
}

// firstLine is the description of a file without one: its first line of prose.
func firstLine(body string) string {
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "```") {
			continue
		}
		l = strings.TrimSpace(strings.TrimLeft(l, "#"))
		if l == "" {
			continue
		}
		if r := []rune(l); len(r) > 200 {
			return string(r[:200]) + "…"
		}
		return l
	}
	return ""
}

var looseKey = regexp.MustCompile(`^([A-Za-z_-]+):[ \t]*(.*)$`)

// looseFields reads top-level "key: value" lines and "- item" lists under them.
func looseFields(fm string) map[string]any {
	out := map[string]any{}
	key := ""
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := looseKey.FindStringSubmatch(line); m != nil {
			key = m[1]
			out[key] = strings.Trim(strings.TrimSpace(m[2]), `"'`)
			continue
		}
		t := strings.TrimSpace(line)
		if key != "" && strings.HasPrefix(t, "- ") {
			items, _ := out[key].([]any)
			out[key] = append(items, strings.Trim(strings.TrimSpace(t[2:]), `"'`))
		}
	}
	return out
}

func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int:
		return itoa(x)
	case float64:
		return strings.TrimRight(strings.TrimRight(ftoa(x), "0"), ".")
	}
	return ""
}

func truth(v any, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(scalar(v))) {
	case "true", "yes", "on", "1":
		return true
	case "false", "no", "off", "0":
		return false
	}
	return def
}

func items(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, it := range list {
		if s := strings.TrimSpace(scalar(it)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// wordList reads a YAML list or a space-separated string.
func wordList(v any) []string {
	if l := items(v); l != nil {
		return l
	}
	return strings.Fields(scalar(v))
}

// commaList reads a YAML list or a comma-separated string (globs may hold spaces and braces).
func commaList(v any) []string {
	if l := items(v); l != nil {
		return l
	}
	var out []string
	depth := 0
	cur := strings.Builder{}
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, r := range scalar(v) {
		switch {
		case r == '{':
			depth++
		case r == '}' && depth > 0:
			depth--
		case r == ',' && depth == 0:
			flush()
			continue
		}
		cur.WriteRune(r)
	}
	flush()
	return out
}

// ruleList reads permission rules: a YAML list, or a string separated by commas or spaces outside parentheses.
func ruleList(v any) []string {
	src := items(v)
	if src == nil {
		if s := scalar(v); s != "" {
			src = []string{s}
		}
	}
	rules := permissions.ParseRules(src)
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.String())
	}
	return out
}
