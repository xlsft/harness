package permissions

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// Rule is a parsed permission rule in Claude Code syntax:
//
//	Tool                 every call of Tool
//	Tool(specifier)      Bash command pattern, gitignore-style path, ...
//	Tool(param:value)    input parameter match, e.g. Bash(run_in_background:true)
//	mcp__server          every tool of an MCP server
//	mcp__server__*, *    tool name globs
type Rule struct {
	Tool string
	Spec string // empty: the whole tool
	// Source names where the rule came from ("cliArg", "localSettings", ...).
	Source string
	// Root is the base directory for "/path" specifiers.
	Root string
}

func (r Rule) String() string {
	if r.Spec == "" {
		return r.Tool
	}
	return r.Tool + "(" + r.Spec + ")"
}

// ParseRules parses rule lists like "Bash(git diff *) Read,Edit". Separators
// (comma or whitespace) inside parentheses are kept.
func ParseRules(items []string) []Rule {
	var out []Rule
	for _, item := range items {
		depth := 0
		start := 0
		flush := func(end int) {
			tok := strings.TrimSpace(item[start:end])
			if tok != "" {
				out = append(out, ParseRule(tok))
			}
		}
		for i, c := range item {
			switch {
			case c == '(':
				depth++
			case c == ')':
				if depth > 0 {
					depth--
				}
			case (c == ',' || c == ' ' || c == '\t' || c == '\n') && depth == 0:
				flush(i)
				start = i + 1
			}
		}
		flush(len(item))
	}
	return out
}

// ParseRule parses one rule.
func ParseRule(s string) Rule {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '('); i > 0 && strings.HasSuffix(s, ")") {
		spec := strings.TrimSpace(s[i+1 : len(s)-1])
		if spec == "*" {
			spec = ""
		}
		return Rule{Tool: s[:i], Spec: spec}
	}
	return Rule{Tool: s}
}

// Tool categories: Edit rules apply to every file-editing tool and Read
// rules to every file-reading tool.
var (
	editTools = map[string]bool{"Edit": true, "Write": true, "NotebookEdit": true, "MultiEdit": true, "DocumentEdit": true}
	readTools = map[string]bool{"Read": true, "Glob": true, "Grep": true, "LS": true, "NotebookRead": true, "DocumentRead": true}
	// pathObservers read a file only when their input names one (Wait on a
	// path, LSP) or show one to the user (OpenInEditor, RevealPath): that path
	// gets the read-tool directory check, and Read deny rules cover them.
	pathObservers = map[string]bool{"Wait": true, "LSP": true, "OpenInEditor": true, "RevealPath": true}
	// worktreeTools change only the session's own worktree and branch: accept-edits mode lets them through. Throwing
	// away uncommitted work is marked Sensitive by the tool and always asks.
	worktreeTools = map[string]bool{"EnterWorktree": true, "ExitWorktree": true, "SyncWithBase": true}
	// sessionTools start or reach other sessions. They change nothing here and those sessions keep their own permission
	// checks, so they run without a prompt, except in plan mode. SessionArchive is not one of them: it always asks.
	sessionTools = map[string]bool{"SessionStart": true, "SessionHandoff": true, "SessionSend": true}
)

// IsEditTool reports whether name modifies files.
func IsEditTool(name string) bool { return editTools[name] }

// IsReadTool reports whether name reads files.
func IsReadTool(name string) bool { return readTools[name] }

// toolMatches reports whether the rule's tool part covers name. allowImplied
// makes Edit rules cover read tools too (an allowed edit implies reading).
func (r Rule) toolMatches(name string, allowImplied bool) bool {
	if r.Tool == name || globName(r.Tool, name) || mcpServerMatches(r.Tool, name) {
		return true
	}
	switch {
	case r.Tool == "Edit" && editTools[name]:
		return true
	case r.Tool == "Read" && (readTools[name] || pathObservers[name]):
		return true
	case allowImplied && editTools[r.Tool] && readTools[name]:
		return true
	case r.Tool == BrowserRuleTool && browserSiteTools[name]:
		return true
	case r.Tool == ComputerRuleTool && isComputerTool(name):
		return true
	case r.Tool == "Bash" && name == "TerminalRun": // a shell command too: a rule that removes Bash removes it
		return true
	}
	return false
}

// mcpServerMatches reports whether rule tool "mcp__<server>" (no tool part)
// covers name "mcp__<server>__<tool>".
func mcpServerMatches(rule, name string) bool {
	server, ok := strings.CutPrefix(rule, "mcp__")
	if !ok || server == "" || strings.Contains(server, "__") || strings.Contains(server, "*") {
		return false
	}
	return strings.HasPrefix(name, rule+"__") && len(name) > len(rule)+2
}

func globName(pattern, name string) bool {
	if !strings.Contains(pattern, "*") {
		return false
	}
	return globMatch(pattern, name)
}

// globMatch matches s against a pattern where '*' matches any sequence of
// characters (including spaces and slashes) and "\*" is a literal star.
func globMatch(pattern, s string) bool {
	var re strings.Builder
	re.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '\\' && i+1 < len(pattern) && pattern[i+1] == '*':
			re.WriteString(`\*`)
			i++
		case c == '*':
			re.WriteString(`.*`)
		default:
			re.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	re.WriteString("$")
	ok, _ := regexp.MatchString("(?s)"+re.String(), s)
	return ok
}

var paramRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]+):(.*)$`)

// paramSpec returns (key, value) when the spec has the param:value form and
// the tool input actually has that key (or the key is WebFetch's domain).
func paramSpec(tool, spec string, input map[string]any) (string, string, bool) {
	m := paramRe.FindStringSubmatch(spec)
	if m == nil {
		return "", "", false
	}
	if (tool == "WebFetch" || isBrowserTool(tool)) && m[1] == "domain" {
		return m[1], m[2], true
	}
	if _, ok := input[m[1]]; ok {
		return m[1], m[2], true
	}
	return "", "", false
}

func decodeInput(raw json.RawMessage) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

func valueString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// matchBashSpec matches a command pattern against one (normalized) command.
// '*' matches anything including spaces; a trailing ":*" is the legacy form
// of " *"; a trailing " *" also matches the bare prefix.
func matchBashSpec(spec, cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if strings.HasSuffix(spec, ":*") {
		spec = strings.TrimSuffix(spec, ":*") + " *"
	}
	if strings.HasSuffix(spec, " *") && !strings.HasSuffix(spec, `\ *`) {
		prefix := strings.TrimSuffix(spec, " *")
		if !strings.Contains(prefix, "*") && cmd == prefix {
			return true
		}
		if globMatch(prefix, cmd) {
			return true
		}
	}
	if !strings.Contains(spec, "*") {
		return cmd == spec
	}
	return globMatch(spec, cmd)
}

// matchContext carries what rule matching needs besides the request.
type matchContext struct {
	cwd  string
	home string
}

// matchesCall reports whether the rule applies to a non-Bash call (Bash is
// matched per subcommand by the engine).
func (r Rule) matchesCall(req Request, input map[string]any, mc matchContext, allowImplied bool) bool {
	if !r.toolMatches(req.ToolName, allowImplied) {
		return false
	}
	if pathObservers[req.ToolName] && r.Tool != req.ToolName && req.FilePath == "" {
		return false // Read rules cover only the file such a tool reads
	}
	if r.Spec == "" {
		return true
	}
	if req.ToolName == "Skill" {
		return matchSkillSpec(r.Spec, valueString(input["skill"]), valueString(input["args"]))
	}
	if k, v, ok := paramSpec(req.ToolName, r.Spec, input); ok {
		if isBrowserTool(req.ToolName) && k == "domain" {
			host, _ := browserSite(req.URL)
			return host != "" && globMatch(strings.ToLower(v), host)
		}
		if req.ToolName == "WebFetch" && k == "domain" {
			u, err := url.Parse(valueString(input["url"]))
			if err != nil {
				return false
			}
			return globMatch(v, u.Hostname())
		}
		return globMatch(v, valueString(input[k]))
	}
	if editTools[req.ToolName] || readTools[req.ToolName] || pathObservers[req.ToolName] {
		p := req.FilePath
		if p == "" {
			p = mc.cwd
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(mc.cwd, p)
		}
		root := r.Root
		if root == "" {
			root = mc.cwd
		}
		return compilePath(r.Spec, root, mc.cwd, mc.home).match(filepath.Clean(p))
	}
	// Other tools: match the specifier against the first string parameter.
	return globMatch(r.Spec, firstString(input))
}

// matchSkillSpec matches Skill(name) against the skill alone, and Skill(name *) against the skill and its arguments, as
// Bash rules read a command line.
func matchSkillSpec(spec, name, args string) bool {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	if !strings.Contains(spec, "*") {
		return spec == name
	}
	return matchBashSpec(spec, strings.TrimSpace(name+" "+args))
}

func firstString(input map[string]any) string {
	for _, k := range []string{"command", "url", "query", "skill", "pattern", "prompt"} {
		if s, ok := input[k].(string); ok {
			return s
		}
	}
	return ""
}

// ValidateRule reports a syntax problem with a rule string.
func ValidateRule(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("empty rule")
	}
	if strings.Count(s, "(") != strings.Count(s, ")") {
		return fmt.Errorf("unbalanced parentheses in %q", s)
	}
	if i := strings.IndexByte(s, '('); i == 0 {
		return fmt.Errorf("missing tool name in %q", s)
	}
	return nil
}
