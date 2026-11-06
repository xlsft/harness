package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/settings"
)

// Limits.
const (
	// defaultCommandTimeout and defaultHTTPTimeout apply to handlers without a timeout of their own.
	defaultCommandTimeout = 10 * time.Minute
	defaultHTTPTimeout    = 30 * time.Second
	// defaultEndTimeout replaces them for SessionEnd: something is closing and waits.
	defaultEndTimeout = 10 * time.Second
	// maxOutputBytes is how much of a handler's output is read; the rest is dropped.
	maxOutputBytes = 1 << 20
	// maxContextRunes is how much context one handler can add for the model.
	maxContextRunes = 10000
	// maxShownRunes is how much of a handler's output the transcript row keeps.
	maxShownRunes = 2000
	// killWait is how long a finished command's pipes may stay open (a child it left in the background holds them).
	killWait = 2 * time.Second
	// endBudget bounds all of SessionEnd.
	endBudget = 30 * time.Second
)

// Status values of a Run.
const (
	RunRunning  = "running"
	RunOK       = "ok"
	RunBlocked  = "blocked"
	RunError    = "error"
	RunTimedOut = "timed_out"
	RunCanceled = "canceled"
)

// Run is one handler at work, as frontends and the transcript see it.
type Run = session.HookRun

// Input is what a handler gets on stdin (or in the POST body), besides the common fields the engine fills in.
type Input struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	Cwd            string `json:"cwd"`
	PermissionMode string `json:"permission_mode,omitempty"`
	HookEventName  string `json:"hook_event_name"`

	ToolName              string          `json:"tool_name,omitempty"`
	ToolInput             json.RawMessage `json:"tool_input,omitempty"`
	ToolUseID             string          `json:"tool_use_id,omitempty"`
	ToolResponse          any             `json:"tool_response,omitempty"`
	ToolCalls             []ToolCall      `json:"tool_calls,omitempty"`
	Error                 string          `json:"error,omitempty"`
	Reason                string          `json:"reason,omitempty"`
	PermissionSuggestions []string        `json:"permission_suggestions,omitempty"`
	Prompt                string          `json:"prompt,omitempty"`
	StopHookActive        *bool           `json:"stop_hook_active,omitempty"`
	LastAssistantMessage  *string         `json:"last_assistant_message,omitempty"`
	Source                string          `json:"source,omitempty"`
	Model                 string          `json:"model,omitempty"`
	Trigger               string          `json:"trigger,omitempty"`
	CustomInstructions    *string         `json:"custom_instructions,omitempty"`
	CompactSummary        string          `json:"compact_summary,omitempty"`
	Message               string          `json:"message,omitempty"`
	Title                 string          `json:"title,omitempty"`
	NotificationType      string          `json:"notification_type,omitempty"`
	AgentID               string          `json:"agent_id,omitempty"`
	AgentType             string          `json:"agent_type,omitempty"`
	AgentTranscriptPath   string          `json:"agent_transcript_path,omitempty"`
	OldCwd                string          `json:"old_cwd,omitempty"`
	NewCwd                string          `json:"new_cwd,omitempty"`
}

// ToolCall is one call of a PostToolBatch.
type ToolCall struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
}

// Permission decisions of a PreToolUse hook.
const (
	Allow = "allow"
	Deny  = "deny"
	Ask   = "ask"
)

// Answer is a PermissionRequest hook answering for the user.
type Answer struct {
	Allow        bool
	Message      string
	UpdatedInput json.RawMessage
	Interrupt    bool
}

// Outcome is what the handlers of one event said together. Where they disagree the strictest wins, whichever finished
// first; what can only be one value (a rewritten input) comes from the last handler in configuration order, which is the
// highest settings level. Parallel runs therefore give the same outcome every time.
type Outcome struct {
	// Blocked: a handler refused what the event is about (exit code 2 or decision "block"); Reason says why, for the model.
	Blocked bool
	Reason  string
	// Permission is a PreToolUse decision (Allow, Deny, Ask; "" when none); PermissionReason says why.
	Permission       string
	PermissionReason string
	UpdatedInput     json.RawMessage
	// Answer is the PermissionRequest answer, when there is one.
	Answer *Answer
	// Context is text for the model.
	Context []string
	// Halt: a handler said continue false. The agent stops working after this point; HaltReason is for the user.
	Halt       bool
	HaltReason string
	// UpdatedToolOutput replaces what an MCP tool returned (PostToolUse).
	UpdatedToolOutput json.RawMessage
}

// Options configure an Engine. The functions read the session's state as it is now.
type Options struct {
	SessionID string
	// TranscriptPath, Cwd, ProjectDir and Mode fill the input; ExtraEnv is added to a command's environment (the folder's
	// environment). All may be nil.
	TranscriptPath func() string
	Cwd            func() string
	ProjectDir     func() string
	Mode           func() string
	ExtraEnv       func() []string
	// Switches reads disableAllHooks again: the user's switch reaches a running session. Nil: the set's own values.
	Switches func() (off, policyOff bool)
	// OnRun is told about every handler as it starts and as it ends. Called from handler goroutines.
	OnRun  func(Run)
	Logger *slog.Logger
	// HTTP overrides the client of http handlers (tests).
	HTTP *http.Client
}

// Engine runs the hooks of one session. A nil Engine runs nothing, so code without hooks has nothing to check.
type Engine struct {
	o      Options
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	set    *Set
	closed bool
	wg     sync.WaitGroup
}

// New returns an engine running set.
func New(set *Set, o Options) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{o: o, set: set, ctx: ctx, cancel: cancel}
}

// Replace changes the set (the session moved to another folder's settings).
func (e *Engine) Replace(set *Set) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.set = set
	e.mu.Unlock()
}

// Set returns the current set.
func (e *Engine) Set() *Set {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.set
}

// Close cancels what still runs (handlers on their own, notifications) and waits for it.
func (e *Engine) Close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.cancel()
	e.wg.Wait()
}

// Context ends when the engine closes.
func (e *Engine) Context() context.Context {
	if e == nil {
		return context.Background()
	}
	return e.ctx
}

// Fire runs an event's handlers without waiting for them: for events nothing can answer. It never blocks the caller.
func (e *Engine) Fire(ev Event, value string, in Input) {
	if e == nil || !e.Set().Has(ev) {
		return
	}
	e.Spawn(func() { e.Dispatch(e.ctx, ev, value, in) })
}

// Spawn runs f in the background, Close waiting for it, unless the engine closed already: then it reports false.
func (e *Engine) Spawn(f func()) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		f()
	}()
	return true
}

// End runs SessionEnd, with its own time budget: the session's own context has usually ended by now.
func (e *Engine) End(reason string) {
	if e == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), endBudget)
	defer cancel()
	e.Dispatch(ctx, SessionEnd, reason, Input{Reason: reason})
}

// active returns the hooks that run now for an event.
func (e *Engine) active(ev Event, value string) []*Hook {
	hooks := e.Set().Matching(ev, value)
	if len(hooks) == 0 {
		return nil
	}
	if os.Getenv("HARNESS_NO_HOOKS") == "1" {
		return nil
	}
	off, policyOff := e.Set().Off, e.Set().PolicyOff
	if e.o.Switches != nil {
		off, policyOff = e.o.Switches()
	}
	if policyOff {
		return nil
	}
	if off {
		hooks = slices.DeleteFunc(hooks, func(h *Hook) bool { return h.Source.Level != settings.LevelPolicy })
	}
	return hooks
}

// Dispatch runs the handlers of an event in parallel and returns what they said. value is what the event's matcher looks
// at. The handlers stop when ctx ends. With nothing configured it costs a look at a list.
func (e *Engine) Dispatch(ctx context.Context, ev Event, value string, in Input) Outcome {
	if e == nil {
		return Outcome{}
	}
	hooks := e.active(ev, value)
	if len(hooks) == 0 {
		return Outcome{}
	}
	in.HookEventName = string(ev)
	in.SessionID = e.o.SessionID
	if e.o.TranscriptPath != nil {
		in.TranscriptPath = e.o.TranscriptPath()
	}
	if e.o.Cwd != nil && in.Cwd == "" {
		in.Cwd = e.o.Cwd()
	}
	if e.o.Mode != nil {
		in.PermissionMode = e.o.Mode()
	}
	body, err := json.Marshal(in)
	if err != nil {
		e.log().Warn("hook input could not be encoded", "event", ev, "err", err)
		return Outcome{}
	}
	results := make([]*result, len(hooks))
	var wg sync.WaitGroup
	for i, h := range hooks {
		if h.Async {
			// Nothing waits for it, and it outlives the turn that started it.
			e.Spawn(func() { e.runOne(e.ctx, h, ev, in, body) })
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = e.runOne(ctx, h, ev, in, body)
		}()
	}
	wg.Wait()
	return combine(ev, results)
}

func (e *Engine) log() *slog.Logger {
	if e.o.Logger != nil {
		return e.o.Logger
	}
	return slog.Default()
}

// result is one handler's reply.
type result struct {
	// exit is the exit code, or the status code of an http handler (web says which); hasExit says there is one.
	exit    int
	hasExit bool
	web     bool
	stdout  string
	stderr  string
	// failure says the handler did not run to an answer (a timeout, an unreachable URL, invalid output).
	failure string
	status  string
	out     *output
}

// output is the JSON a handler can answer with.
type output struct {
	Continue       *bool  `json:"continue"`
	StopReason     string `json:"stopReason"`
	SuppressOutput bool   `json:"suppressOutput"`
	SystemMessage  string `json:"systemMessage"`
	Decision       string `json:"decision"`
	Reason         string `json:"reason"`
	Specific       struct {
		HookEventName            string          `json:"hookEventName"`
		PermissionDecision       string          `json:"permissionDecision"`
		PermissionDecisionReason string          `json:"permissionDecisionReason"`
		UpdatedInput             json.RawMessage `json:"updatedInput"`
		AdditionalContext        string          `json:"additionalContext"`
		UpdatedMCPToolOutput     json.RawMessage `json:"updatedMCPToolOutput"`
		Decision                 struct {
			Behavior     string          `json:"behavior"`
			Message      string          `json:"message"`
			UpdatedInput json.RawMessage `json:"updatedInput"`
			Interrupt    bool            `json:"interrupt"`
		} `json:"decision"`
	} `json:"hookSpecificOutput"`
}

// runOne runs a handler and reports it.
func (e *Engine) runOne(ctx context.Context, h *Hook, ev Event, in Input, body []byte) *result {
	run := Run{ID: "hook_" + session.NewID(), Event: string(ev), Type: h.Type, Command: valid(h.Command), Matcher: h.Matcher, Source: h.Source.Level.String(),
		Status: RunRunning, ToolName: in.ToolName, ToolUseID: in.ToolUseID, AgentID: in.AgentID, Async: h.Async}
	e.report(run)
	start := time.Now()
	timeout := h.Timeout
	if timeout <= 0 {
		switch {
		case ev == SessionEnd:
			timeout = defaultEndTimeout
		case h.Type == TypeHTTP:
			timeout = defaultHTTPTimeout
		default:
			timeout = defaultCommandTimeout
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var r *result
	if h.Type == TypeHTTP {
		r = e.runHTTP(runCtx, h, body)
	} else {
		r = e.runCommand(runCtx, h, in, body)
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		r.status, r.failure = RunTimedOut, fmt.Sprintf("Timed out after %s.", timeout)
	} else if ctx.Err() != nil {
		r.status, r.failure = RunCanceled, "Canceled."
	}
	interpret(ev, r)
	run.Status, run.Duration = r.status, time.Since(start).Milliseconds()
	if r.hasExit {
		exit := r.exit
		run.ExitCode = &exit
	}
	run.Decision, run.Output = shown(ev, r)
	e.report(run)
	return r
}

func (e *Engine) report(r Run) {
	if e.o.OnRun != nil {
		e.o.OnRun(r)
	}
}

// limitedBuffer keeps the start of what is written and drops the rest. It never fails, so a chatty program is not killed
// by a closed pipe.
type limitedBuffer struct {
	b bytes.Buffer
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxOutputBytes - l.b.Len(); room > 0 {
		l.b.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (e *Engine) runCommand(ctx context.Context, h *Hook, in Input, body []byte) *result {
	cmd, kill := shellCommand(h.Command)
	cmd.Dir = in.Cwd
	cmd.Env = e.environ()
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = killWait
	r := &result{}
	if err := cmd.Start(); err != nil {
		r.failure = "Could not start: " + err.Error()
		return r
	}
	stop := context.AfterFunc(ctx, kill)
	err := cmd.Wait()
	stop()
	r.stdout, r.stderr = valid(stdout.b.String()), valid(stderr.b.String())
	var exitErr *exec.ExitError
	switch {
	case err == nil, errors.Is(err, exec.ErrWaitDelay):
		r.hasExit = true
	case errors.As(err, &exitErr):
		r.exit, r.hasExit = exitErr.ExitCode(), exitErr.ExitCode() >= 0
		if !r.hasExit {
			r.failure = "Killed: " + err.Error()
		}
	default:
		r.failure = err.Error()
	}
	return r
}

// environ is what a command runs with: the client's environment, the folder's, and the project directory under the name
// users' scripts already read and under ours.
func (e *Engine) environ() []string {
	env := os.Environ()
	if e.o.ExtraEnv != nil {
		env = append(env, e.o.ExtraEnv()...)
	}
	if e.o.ProjectDir != nil {
		dir := e.o.ProjectDir()
		env = append(env, "CLAUDE_PROJECT_DIR="+dir, "HARNESS_PROJECT_DIR="+dir)
	}
	return env
}

func (e *Engine) lookupEnv(name string) string {
	val := ""
	for _, kv := range e.environ() {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			val = v
		}
	}
	return val
}

var headerVar = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)

func (e *Engine) runHTTP(ctx context.Context, h *Hook, body []byte) *result {
	r := &result{web: true}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Command, bytes.NewReader(body))
	if err != nil {
		r.failure = err.Error()
		return r
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range h.Headers {
		// A header reaches the variables the hook names, so a URL in a repository cannot collect the rest.
		req.Header.Set(k, headerVar.ReplaceAllStringFunc(v, func(m string) string {
			name := headerVar.FindStringSubmatch(m)[1]
			if slices.Contains(h.AllowedEnvVars, name) {
				return e.lookupEnv(name)
			}
			return ""
		}))
	}
	client := e.o.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		r.failure = "Request failed: " + err.Error()
		return r
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxOutputBytes))
	if err != nil {
		r.failure = "Reading the response failed: " + err.Error()
		return r
	}
	r.exit, r.hasExit = resp.StatusCode, true
	r.stdout = valid(string(data))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.failure = "The server answered " + resp.Status + "."
	}
	return r
}

// blockers are the events a handler can refuse by exit code or decision "block". Elsewhere exit code 2 is an ordinary
// failure, shown and otherwise ignored.
var blockers = map[Event]bool{PreToolUse: true, PermissionRequest: true, UserPromptSubmit: true, Stop: true, SubagentStop: true,
	PreCompact: true, PostToolUse: true, PostToolUseFailure: true, PostToolBatch: true}

// interpret reads the exit code and the output into a status and, on an answer, r.out.
func interpret(ev Event, r *result) {
	if r.status != "" {
		return
	}
	if r.failure != "" {
		r.status = RunError
		return
	}
	switch {
	case r.exit == 0 || r.web: // an http handler that failed has a failure by now
		r.status = RunOK
		text := strings.TrimSpace(r.stdout)
		if !strings.HasPrefix(text, "{") {
			return
		}
		var o output
		if err := json.Unmarshal([]byte(text), &o); err != nil {
			r.status, r.failure = RunError, "Its output starts like JSON but is not: "+err.Error()
			return
		}
		if n := o.Specific.HookEventName; n != "" && n != string(ev) {
			r.status, r.failure = RunError, "Its hookSpecificOutput is for "+n+"."
			return
		}
		r.out = &o
		if o.Continue != nil && !*o.Continue {
			r.status = RunBlocked
		}
		if blockers[ev] && blocking(ev, &o) {
			r.status = RunBlocked
		}
	case r.exit == 2 && blockers[ev]:
		r.status = RunBlocked
	default:
		r.status = RunError
	}
}

// blocking reports whether JSON output refuses what the event is about.
func blocking(ev Event, o *output) bool {
	switch ev {
	case PreToolUse:
		return o.Specific.PermissionDecision == Deny || o.Decision == "block"
	case PermissionRequest:
		return o.Specific.Decision.Behavior == Deny
	}
	return o.Decision == "block"
}

// shown is what the transcript row says about a finished handler.
func shown(ev Event, r *result) (decision, text string) {
	if o := r.out; o != nil {
		switch {
		case o.Continue != nil && !*o.Continue:
			decision = "stop"
		case ev == PreToolUse && o.Specific.PermissionDecision != "":
			decision = o.Specific.PermissionDecision
		case ev == PreToolUse && o.Decision == "approve":
			decision = Allow
		case ev == PreToolUse && o.Decision == "block":
			decision = Deny
		case ev == PermissionRequest && o.Specific.Decision.Behavior != "":
			decision = o.Specific.Decision.Behavior
		case o.Decision == "block":
			decision = "block"
		}
		for _, s := range []string{o.StopReason, o.Specific.PermissionDecisionReason, o.Specific.Decision.Message, o.Reason, o.SystemMessage} {
			if s != "" {
				return decision, clip(s)
			}
		}
		if o.SuppressOutput {
			return decision, ""
		}
		return decision, clip(o.Specific.AdditionalContext)
	}
	switch {
	case r.failure != "":
		text = r.failure
		if s := strings.TrimSpace(r.stderr); s != "" {
			text += " " + s
		}
	case r.status == RunBlocked:
		decision = "block"
		if ev == PreToolUse || ev == PermissionRequest {
			decision = Deny
		}
		text = r.stderr
	case r.status == RunError:
		text = firstNonEmpty(r.stderr, r.stdout)
		if r.hasExit {
			text = strings.TrimSpace(fmt.Sprintf("Exited with %d. %s", r.exit, text))
		}
	default:
		text = firstNonEmpty(r.stdout, r.stderr)
	}
	return decision, clip(text)
}

// valid makes program output safe to carry: invalid UTF-8 fails the proto marshal of the event that holds it.
func valid(s string) string { return strings.ToValidUTF8(s, "�") }

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// clip shortens text for a transcript row, keeping its end, where a program's message usually is.
func clip(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxShownRunes {
		return "…" + string(r[len(r)-maxShownRunes:])
	}
	return s
}

func limitContext(s string) string {
	if r := []rune(s); len(r) > maxContextRunes {
		return string(r[:maxContextRunes]) + "\n[Output truncated]"
	}
	return s
}

// plainContext are the events whose plain standard output is context for the model; elsewhere it only shows in the
// transcript.
var plainContext = map[Event]bool{UserPromptSubmit: true, SessionStart: true, PreCompact: true}

// combine merges the replies of an event's handlers (nil: a handler that runs on its own).
func combine(ev Event, results []*result) Outcome {
	var o Outcome
	rank := map[string]int{"": 0, Allow: 1, Ask: 2, Deny: 3}
	permission := func(decision, reason string) {
		if rank[decision] > rank[o.Permission] {
			o.Permission, o.PermissionReason = decision, reason
		} else if decision == o.Permission && reason != "" && decision != "" {
			o.PermissionReason = joinReason(o.PermissionReason, reason)
		}
	}
	for _, r := range results {
		if r == nil || r.failure != "" || r.status == RunError {
			continue
		}
		if !r.web && r.exit == 2 {
			reason := strings.TrimSpace(r.stderr)
			if reason == "" {
				reason = "A hook refused this without saying why."
			}
			switch ev {
			case PreToolUse:
				permission(Deny, reason)
			case PermissionRequest:
				o.Answer = &Answer{Message: reason}
			default:
				o.Blocked, o.Reason = true, joinReason(o.Reason, reason)
			}
			continue
		}
		if r.out == nil {
			if plainContext[ev] && strings.TrimSpace(r.stdout) != "" {
				o.Context = append(o.Context, limitContext(strings.TrimSpace(r.stdout)))
			}
			continue
		}
		out := r.out
		if out.Continue != nil && !*out.Continue {
			o.Halt, o.HaltReason = true, joinReason(o.HaltReason, out.StopReason)
		}
		if c := strings.TrimSpace(out.Specific.AdditionalContext); c != "" {
			o.Context = append(o.Context, limitContext(c))
		}
		if len(out.Specific.UpdatedMCPToolOutput) > 0 {
			o.UpdatedToolOutput = out.Specific.UpdatedMCPToolOutput
		}
		switch ev {
		case PreToolUse:
			d, reason := out.Specific.PermissionDecision, out.Specific.PermissionDecisionReason
			if d == "" {
				// The older form of the same answer.
				switch out.Decision {
				case "approve":
					d, reason = Allow, out.Reason
				case "block":
					d, reason = Deny, out.Reason
				}
			}
			if d == Allow || d == Deny || d == Ask {
				permission(d, reason)
			}
			if isObject(out.Specific.UpdatedInput) {
				o.UpdatedInput = out.Specific.UpdatedInput
			}
		case PermissionRequest:
			d := out.Specific.Decision
			switch d.Behavior {
			case Deny:
				if o.Answer == nil || o.Answer.Allow {
					o.Answer = &Answer{Message: d.Message, Interrupt: d.Interrupt}
				}
			case Allow:
				if o.Answer == nil {
					o.Answer = &Answer{Allow: true}
				}
				if o.Answer.Allow && isObject(d.UpdatedInput) {
					o.Answer.UpdatedInput = d.UpdatedInput
				}
			}
		default:
			if out.Decision == "block" && blockers[ev] {
				reason := out.Reason
				if reason == "" {
					reason = "A hook refused this without saying why."
				}
				o.Blocked, o.Reason = true, joinReason(o.Reason, reason)
			}
		}
	}
	return o
}

func joinReason(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "" || a == b:
		return a
	}
	return a + "\n" + b
}

func isObject(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	return len(raw) > 0 && json.Unmarshal(raw, &m) == nil && m != nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
