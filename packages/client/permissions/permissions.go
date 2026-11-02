// Package permissions decides whether a tool call may run. M0 provides a basic
// engine (modes + simple allow/deny rules + ask); the full rule engine (M1)
// will implement the same Checker interface.
package permissions

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
)

// Mode is a permission mode.
type Mode string

// Supported modes.
const (
	ModeDefault           Mode = "default"
	ModeAcceptEdits       Mode = "acceptEdits"
	ModeBypassPermissions Mode = "bypassPermissions"
	ModePlan              Mode = "plan"
	// ModeDontAsk denies everything that would otherwise prompt.
	ModeDontAsk Mode = "dontAsk"
)

// ParseMode validates a mode name.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case "":
		return ModeDefault, nil
	case ModeDefault, ModeAcceptEdits, ModeBypassPermissions, ModePlan, ModeDontAsk:
		return Mode(s), nil
	}
	return "", fmt.Errorf("unknown permission mode %q (want default, acceptEdits, plan, dontAsk or bypassPermissions)", s)
}

// Behavior is the outcome of a check.
type Behavior string

// Behaviors.
const (
	Allow Behavior = "allow"
	Deny  Behavior = "deny"
	Ask   Behavior = "ask"
)

// Request describes a pending tool call.
type Request struct {
	ToolName  string
	ToolUseID string
	Input     json.RawMessage
	ReadOnly  bool
	// FilePath / Command are pre-extracted for matching (may be empty).
	FilePath string
	Command  string
}

// Decision is the result of a permission check.
type Decision struct {
	Behavior Behavior
	// Message explains a denial (sent to the model).
	Message string
	// UpdatedInput replaces the tool input when set (from an interactive approver).
	UpdatedInput json.RawMessage
	// Interrupt asks the loop to stop the turn after denying.
	Interrupt bool
}

// Checker decides on tool calls. It must be safe for concurrent use.
type Checker interface {
	Check(ctx context.Context, req Request) Decision
}

// Asker resolves Ask decisions interactively (TUI prompt, SDK can_use_tool).
type Asker interface {
	Ask(ctx context.Context, req Request) (Decision, error)
}

// Rule is a parsed permission rule: Tool or Tool(spec).
type Rule struct {
	Tool string
	Spec string // empty: whole tool
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
				out = append(out, parseRule(tok))
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

func parseRule(s string) Rule {
	if i := strings.IndexByte(s, '('); i > 0 && strings.HasSuffix(s, ")") {
		return Rule{Tool: s[:i], Spec: strings.TrimSpace(s[i+1 : len(s)-1])}
	}
	return Rule{Tool: s}
}

// Matches reports whether the rule applies to req.
func (r Rule) Matches(req Request) bool {
	if r.Tool != req.ToolName {
		return false
	}
	if r.Spec == "" || r.Spec == "*" {
		return true
	}
	switch req.ToolName {
	case "Bash":
		return matchCommand(r.Spec, req.Command)
	default:
		if req.FilePath == "" {
			return false
		}
		return matchPath(r.Spec, req.FilePath)
	}
}

// matchCommand supports "prefix *", "prefix:*" and exact matches.
func matchCommand(spec, cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	switch {
	case strings.HasSuffix(spec, ":*"):
		p := strings.TrimSpace(strings.TrimSuffix(spec, ":*"))
		return cmd == p || strings.HasPrefix(cmd, p+" ")
	case strings.HasSuffix(spec, " *"):
		p := strings.TrimSuffix(spec, " *")
		return cmd == p || strings.HasPrefix(cmd, p+" ")
	case strings.HasSuffix(spec, "*"):
		return strings.HasPrefix(cmd, strings.TrimSuffix(spec, "*"))
	}
	return cmd == spec
}

func matchPath(spec, path string) bool {
	path = filepath.ToSlash(filepath.Clean(path))
	spec = filepath.ToSlash(spec)
	if m, _ := doublestar.Match(spec, path); m {
		return true
	}
	// A directory spec covers everything beneath it.
	return strings.HasPrefix(path, strings.TrimSuffix(spec, "/")+"/")
}

var editTools = map[string]bool{"Write": true, "Edit": true, "NotebookEdit": true}

// Engine is the M0 Checker.
type Engine struct {
	mu       sync.RWMutex
	mode     Mode
	allow    []Rule
	deny     []Rule
	asker    Asker
	workDirs []string
}

// NewEngine builds an engine.
func NewEngine(mode Mode, allow, deny []Rule, workDirs []string) *Engine {
	return &Engine{mode: mode, allow: allow, deny: deny, workDirs: workDirs}
}

// SetAsker installs an interactive approver; without it Ask becomes Deny.
func (e *Engine) SetAsker(a Asker) {
	e.mu.Lock()
	e.asker = a
	e.mu.Unlock()
}

// SetMode changes the mode at runtime.
func (e *Engine) SetMode(m Mode) {
	e.mu.Lock()
	e.mode = m
	e.mu.Unlock()
}

// Mode returns the current mode.
func (e *Engine) Mode() Mode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode
}

// AddAllow appends allow rules (e.g. "don't ask again").
func (e *Engine) AddAllow(r ...Rule) {
	e.mu.Lock()
	e.allow = append(e.allow, r...)
	e.mu.Unlock()
}

// Evaluate returns the static decision without consulting the asker.
func (e *Engine) Evaluate(req Request) Decision {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, r := range e.deny {
		if r.Matches(req) {
			return Decision{Behavior: Deny, Message: fmt.Sprintf("Permission to use %s has been denied by rule %s.", req.ToolName, r)}
		}
	}
	if req.ReadOnly {
		return Decision{Behavior: Allow}
	}
	if e.mode == ModePlan {
		return Decision{Behavior: Deny, Message: fmt.Sprintf("Plan mode is active: %s is not allowed. Only read-only tools may be used.", req.ToolName)}
	}
	if e.mode == ModeBypassPermissions {
		return Decision{Behavior: Allow}
	}
	for _, r := range e.allow {
		if r.Matches(req) {
			return Decision{Behavior: Allow}
		}
	}
	if e.mode == ModeAcceptEdits && editTools[req.ToolName] && req.FilePath != "" && inDirs(req.FilePath, e.workDirs) {
		return Decision{Behavior: Allow}
	}
	return Decision{Behavior: Ask}
}

// Check implements Checker.
func (e *Engine) Check(ctx context.Context, req Request) Decision {
	d := e.Evaluate(req)
	if d.Behavior != Ask {
		return d
	}
	e.mu.RLock()
	asker, mode := e.asker, e.mode
	e.mu.RUnlock()
	if mode == ModeDontAsk {
		return Decision{Behavior: Deny, Message: fmt.Sprintf("Permission to use %s was not granted (dontAsk mode).", req.ToolName)}
	}
	if asker == nil {
		return Decision{Behavior: Deny, Message: fmt.Sprintf("Permission to use %s was not granted (non-interactive mode). Allow it with --allowedTools or --permission-mode.", req.ToolName)}
	}
	ad, err := asker.Ask(ctx, req)
	if err != nil {
		return Decision{Behavior: Deny, Message: fmt.Sprintf("Permission request for %s failed: %v", req.ToolName, err)}
	}
	if ad.Behavior != Allow {
		ad.Behavior = Deny
		if ad.Message == "" {
			ad.Message = fmt.Sprintf("The user denied permission to use %s.", req.ToolName)
		}
	}
	return ad
}

func inDirs(p string, dirs []string) bool {
	p = filepath.Clean(p)
	for _, d := range dirs {
		rel, err := filepath.Rel(filepath.Clean(d), p)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
