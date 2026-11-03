// Package agent runs the agentic loop: prompt → gateway stream → tool_use →
// permission check → local execution → tool_result → repeat until the model
// stops calling tools.
//
// Around the loop it keeps the session state that tools and frontends rely
// on: bundle reminders injected before model requests, the todo list, file
// checkpoints, compaction and rewind.
//
// Agent is single-turn-at-a-time and not safe for concurrent RunTurn calls;
// Driver adds the message queue and background execution used by frontends.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/harness/packages/client/gateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/tools"
)

// InterruptedFallback is used only when the bundle lacks the "interrupted" reminder.
const InterruptedFallback = "[Request interrupted by user]"

// Result subtypes (mirroring the Claude Code SDK result message).
const (
	SubtypeSuccess              = "success"
	SubtypeErrorMaxTurns        = "error_max_turns"
	SubtypeErrorDuringExecution = "error_during_execution"
)

// ErrBusy is returned by operations that need an idle session.
var ErrBusy = errors.New("a turn is in progress")

// EventKind enumerates agent events.
type EventKind int

const (
	// EventAssistant carries a complete assistant message.
	EventAssistant EventKind = iota + 1
	// EventUser carries a user message produced by the loop (tool results,
	// interrupt markers, injected queued messages).
	EventUser
	// EventStream carries a raw gateway stream event (partial messages).
	EventStream
	// EventRetry reports a gateway or client retry.
	EventRetry
	// EventToolStart is emitted right before a tool call executes.
	EventToolStart
	// EventToolEnd is emitted with the final result of every tool call
	// (including denials and synthetic interrupt results).
	EventToolEnd
	// EventQueueDrained is emitted when queued messages were consumed.
	EventQueueDrained
	// EventTurnStarted is emitted when a turn starts from a user prompt
	// (UUID is the prompt's uuid).
	EventTurnStarted
	// EventTodos carries the new todo list (Todos).
	EventTodos
	// EventCheckpoint carries a checkpoint taken before a turn.
	EventCheckpoint
	// EventCompacted reports a compaction (Compaction).
	EventCompacted
	// EventRewound reports a rewind (Rewind).
	EventRewound
	// EventTask carries a background task event (Task).
	EventTask
	// EventModeChanged reports a permission mode change (Mode).
	EventModeChanged
	// EventNotice is an informational message for frontends (Notice).
	EventNotice
)

// Notice is an informational event.
type Notice struct {
	Level string // info | warning | error
	Kind  string
	Text  string
	// Auto is set for Kind NoticeAutoDecision.
	Auto *AutoDecision
}

// NoticeAutoDecision is the Notice kind emitted for every tool call the
// auto permission mode decided or deferred to the user.
const NoticeAutoDecision = "auto_decision"

// AutoDecision is an auto-mode verdict on one tool call (also recorded on
// the tool_result transcript entry).
type AutoDecision = session.AutoDecision

// Checkpoint is a rewind point: the state before a turn started by a prompt.
type Checkpoint struct {
	ID              string
	UserMessageUUID string
	PromptPreview   string
	CreatedAt       time.Time
	HasCodeChanges  bool
}

// CompactInfo describes a finished compaction.
type CompactInfo struct {
	Trigger      string // manual | auto | rewind
	TokensBefore int
	TokensAfter  int
}

// Event is emitted while a turn runs.
type Event struct {
	Kind    EventKind
	UUID    string
	Message *llm.Message
	// Response is set on EventAssistant (id, model, stop reason, usage).
	Response *llm.Response
	// Raw is the raw stream event (EventStream) or the structured tool
	// result (EventUser with a tool_result).
	Raw   json.RawMessage
	Retry *llm.RetryInfo

	// Tool call fields (EventToolStart / EventToolEnd).
	ToolUseID   string
	ToolName    string
	Input       json.RawMessage
	IsError     bool
	Interrupted bool
	Content     []llm.ContentBlock
	Duration    time.Duration
	// Structured is the toolUseResult (EventToolEnd); Summary its short form.
	Structured any
	Summary    string

	Model      string
	Todos      []tools.Todo
	Checkpoint *Checkpoint
	Compaction *CompactInfo
	Rewind     *RewindResult
	Task       *tools.TaskEvent
	Mode       string
	Notice     *Notice
}

// ModelUsage aggregates usage per model.
type ModelUsage struct {
	InputTokens              int     `json:"inputTokens"`
	OutputTokens             int     `json:"outputTokens"`
	CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
}

// PermissionDenial records a denied tool call.
type PermissionDenial struct {
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id"`
	ToolInput json.RawMessage `json:"tool_input"`
	// Reason and DecidedBy are set for auto-mode denials (DecidedBy is
	// "auto:<provider>").
	Reason    string `json:"reason,omitempty"`
	DecidedBy string `json:"decided_by,omitempty"`
}

// TurnResult summarizes one turn.
type TurnResult struct {
	Subtype           string
	IsError           bool
	Result            string
	StopReason        string
	NumTurns          int
	Duration          time.Duration
	APIDuration       time.Duration
	Usage             llm.Usage
	CostUSD           float64
	ModelUsage        map[string]*ModelUsage
	PermissionDenials []PermissionDenial
	Errors            []string
	Interrupted       bool
}

// ModeController reads and switches the permission mode (permissions.Engine).
type ModeController interface {
	Mode() permissions.Mode
	SetMode(permissions.Mode) error
}

// Config configures an Agent.
type Config struct {
	Gateway     gateway.Gateway
	Bundle      *prompts.Bundle
	Tools       *tools.Registry
	ToolEnv     *tools.Env
	Permissions permissions.Checker
	// Modes is the permission mode controller (may be nil).
	Modes ModeController
	// Transcript may be nil (no persistence).
	Transcript *session.Writer
	// FileHistory takes checkpoints (may be nil).
	FileHistory *session.FileHistory

	Model            string
	ModelDisplayName string
	MaxTokens        int
	// MaxTurns limits tool-use round trips per turn; 0 = unlimited.
	MaxTurns int
	Effort   string
	// Thinking overrides the thinking request; nil means adaptive thinking when the model supports it
	// (ThinkingCapable). Models that think by default otherwise return their reasoning without text.
	Thinking        *llm.Thinking
	ThinkingCapable bool
	// ContextWindow of the model in tokens (0 disables auto-compaction).
	ContextWindow int
	// AutoCompactPct is the share of the context window that triggers
	// auto-compaction (default 0.92, env HARNESS_AUTOCOMPACT_PCT).
	AutoCompactPct float64
	// TodoNudgeRounds is how many tool rounds without TodoWrite trigger the
	// todo_nudge reminder (default 10).
	TodoNudgeRounds int
	// RefreshBundle re-fetches the prompt bundle after a compaction (the
	// only point where the frozen bundle may change). May be nil.
	RefreshBundle func(ctx context.Context) (*prompts.Bundle, error)

	SessionID string
	// Vars are the placeholder values for dynamic system sections.
	Vars map[string]string
	// ProjectContext is the {{content}} of the project_instructions reminder
	// injected into the first user message ("" = nothing to inject).
	ProjectContext string
	// History is the conversation restored from a transcript.
	History session.Conversation
	// Todos restored from the transcript.
	Todos []tools.Todo
	// DisabledTools are not offered to the model.
	DisabledTools map[string]bool
	// ExtraTools returns definitions of tools offered in addition to the
	// bundle's (MCP tools, which have no bundle entry). Called for every
	// request; the result must be stable to keep the prompt cache. May be nil.
	ExtraTools func() []llm.ToolDef
	// PermissionMode is recorded on user transcript entries.
	PermissionMode func() string
	// OutputStyle is the initial output style (nil = default). Its text is
	// sent with the first request unless History already carries it.
	OutputStyle *OutputStyle
	// DisableInterruptReply turns off the short model reply after an
	// interrupted turn (ReplyToInterrupt, run by Driver).
	DisableInterruptReply bool

	OnEvent func(Event)
	Retry   *gateway.RetryPolicy
	Logger  *slog.Logger
}

type pendingReminder struct {
	id   string
	vars map[string]string
}

// Agent holds the conversation and runs turns.
type Agent struct {
	cfg Config

	mu              sync.Mutex
	model           string
	modelDisplay    string
	bundle          *prompts.Bundle
	messages        []llm.Message
	contextInjected bool
	queue           []queuedMessage
	cancel          context.CancelFunc
	warned          map[string]bool
	todos           []tools.Todo
	reminders       []pendingReminder
	interactor      tools.Interactor
	contextWindow   int

	// token estimate: usage of the last response and the message count it covered
	lastUsage    int
	lastMsgCount int
	// auto-compaction anti-thrashing
	compactFailures     int
	autoCompactDisabled bool
	// todo nudge
	roundsSinceTodo int
	// prompts are the latest user prompts (newest last) for the auto-mode decider.
	prompts []string
	// output style (see OutputStyle)
	style          *OutputStyle
	omitCoding     bool
	styleDelivered bool
	// thinkingCapable follows the current model (Config.ThinkingCapable, then SetModelThinking).
	thinkingCapable bool
	// autoReminded: the auto_mode reminder was delivered since auto mode was entered (or since the last
	// compaction, which drops it from the conversation).
	autoReminded bool
}

// New creates an agent.
func New(cfg Config) *Agent {
	a := &Agent{cfg: cfg, model: cfg.Model, modelDisplay: cfg.ModelDisplayName, warned: map[string]bool{},
		bundle: cfg.Bundle, todos: cfg.Todos, contextWindow: cfg.ContextWindow, thinkingCapable: cfg.ThinkingCapable}
	a.messages = append(a.messages, cfg.History.Messages...)
	for _, m := range cfg.History.Messages {
		if m.Role == llm.RoleUser {
			a.notePrompt(m.Content)
		}
	}
	a.contextInjected = cfg.History.ContextInjected
	if cfg.MaxTokens <= 0 {
		a.cfg.MaxTokens = 32000
	}
	if a.cfg.TodoNudgeRounds <= 0 {
		a.cfg.TodoNudgeRounds = 10
	}
	if cfg.ToolEnv != nil {
		cfg.ToolEnv.SetHost(a)
	}
	a.initOutputStyle(cfg.OutputStyle, cfg.History.OutputStyle)
	return a
}

func (a *Agent) log() *slog.Logger {
	if a.cfg.Logger != nil {
		return a.cfg.Logger
	}
	return slog.Default()
}

// Bundle returns the current prompt bundle.
func (a *Agent) Bundle() *prompts.Bundle {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bundle
}

// Messages returns a copy of the conversation.
func (a *Agent) Messages() []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Message(nil), a.messages...)
}

// SetModel switches the model for subsequent requests (also mid-turn).
func (a *Agent) SetModel(id, display string, contextWindow int) {
	a.mu.Lock()
	a.model, a.modelDisplay = id, display
	if contextWindow > 0 {
		a.contextWindow = contextWindow
	}
	a.mu.Unlock()
}

// remindAutoMode delivers the auto_mode reminder once per stretch of auto mode: what auto mode means and
// that the shell is an acceptable route for reading, searching and mechanical edits.
func (a *Agent) remindAutoMode() {
	auto := a.permissionMode() == string(permissions.ModeAuto)
	a.mu.Lock()
	send := auto && !a.autoReminded
	a.autoReminded = auto
	a.mu.Unlock()
	if send {
		a.Remind("auto_mode", nil)
	}
}

// SetModelThinking records whether the current model supports thinking (after SetModel).
func (a *Agent) SetModelThinking(capable bool) {
	a.mu.Lock()
	a.thinkingCapable = capable
	a.mu.Unlock()
}

// thinking is the thinking request for a main-loop call.
func (a *Agent) thinking() *llm.Thinking {
	if a.cfg.Thinking != nil {
		return a.cfg.Thinking
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.thinkingCapable {
		return &llm.Thinking{Type: "adaptive"}
	}
	return nil
}

// Model returns the current model id.
func (a *Agent) Model() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

// SetMaxTurns changes the per-turn tool round limit (0 = unlimited).
func (a *Agent) SetMaxTurns(n int) {
	a.mu.Lock()
	a.cfg.MaxTurns = n
	a.mu.Unlock()
}

// queuedMessage is a user message waiting for the current tool calls to finish.
type queuedMessage struct {
	id     string
	blocks []llm.ContentBlock
}

// Enqueue adds a user message to be injected after the current tool calls. id (optional)
// lets RemoveQueued take it back.
func (a *Agent) Enqueue(id string, blocks []llm.ContentBlock) {
	a.mu.Lock()
	a.queue = append(a.queue, queuedMessage{id: id, blocks: blocks})
	a.mu.Unlock()
}

// RemoveQueued drops the queued message with id. It reports false when no such message is
// queued (it was never queued, or has already been injected).
func (a *Agent) RemoveQueued(id string) bool {
	if id == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, q := range a.queue {
		if q.id == id {
			a.queue = append(a.queue[:i], a.queue[i+1:]...)
			return true
		}
	}
	return false
}

// DrainQueue removes and returns all queued content.
func (a *Agent) DrainQueue() []llm.ContentBlock {
	a.mu.Lock()
	var out []llm.ContentBlock
	for _, q := range a.queue {
		out = append(out, q.blocks...)
	}
	a.queue = nil
	a.mu.Unlock()
	if len(out) > 0 {
		a.notePrompt(out)
		a.emit(Event{Kind: EventQueueDrained})
	}
	return out
}

// maxPrompts is how many recent user prompts are kept for the auto-mode decider.
const maxPrompts = 3

// notePrompt remembers the text of a user prompt. Tool results, injected
// reminders and context blocks (text starting with "<") and interrupt
// markers are not prompts.
func (a *Agent) notePrompt(blocks []llm.ContentBlock) {
	var parts []string
	for _, b := range blocks {
		if b.Type != llm.BlockText {
			continue
		}
		t := strings.TrimSpace(b.Text)
		if t == "" || strings.HasPrefix(t, "<") || strings.HasPrefix(t, "[Request interrupted") {
			continue
		}
		parts = append(parts, t)
	}
	if len(parts) == 0 {
		return
	}
	a.mu.Lock()
	a.prompts = append(a.prompts, strings.Join(parts, "\n"))
	if len(a.prompts) > maxPrompts {
		a.prompts = append([]string(nil), a.prompts[len(a.prompts)-maxPrompts:]...)
	}
	a.mu.Unlock()
}

// recentPrompts returns the latest user prompts, newest last.
func (a *Agent) recentPrompts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.prompts...)
}

// rationales maps each tool_use id to the assistant text that precedes it
// in the message (the text since the previous tool_use, or all text before
// the first one).
func rationales(content []llm.ContentBlock) map[string]string {
	out := map[string]string{}
	var text []string
	last := ""
	for _, b := range content {
		switch b.Type {
		case llm.BlockText:
			if t := strings.TrimSpace(b.Text); t != "" {
				text = append(text, t)
			}
		case llm.BlockToolUse:
			if len(text) > 0 {
				last = strings.Join(text, "\n")
				text = nil
			}
			out[b.ID] = last
		}
	}
	return out
}

// SetEffort changes the effort level for subsequent requests.
func (a *Agent) SetEffort(effort string) {
	a.mu.Lock()
	a.cfg.Effort = effort
	a.mu.Unlock()
}

// Effort returns the effort level set for the session ("" when unset).
func (a *Agent) Effort() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Effort
}

// QueueLen returns the number of queued messages.
func (a *Agent) QueueLen() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.queue)
}

// Busy reports whether a turn is running.
func (a *Agent) Busy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cancel != nil
}

// Interrupt cancels the running turn: in-flight tools are killed and the
// conversation records the interrupt marker.
func (a *Agent) Interrupt() {
	a.mu.Lock()
	c := a.cancel
	a.mu.Unlock()
	if c != nil {
		c()
	}
}

func (a *Agent) emit(e Event) {
	if a.cfg.OnEvent != nil {
		a.cfg.OnEvent(e)
	}
}

// Emit publishes an event through the agent's event callback (used by the
// session wiring for task and mode events).
func (a *Agent) Emit(e Event) { a.emit(e) }

func (a *Agent) interruptedText() string {
	return a.Bundle().Reminder("interrupted", InterruptedFallback)
}

func (a *Agent) interruptedToolText() string {
	b := a.Bundle()
	return b.Reminder("interrupted_tool_use", b.Reminder("interrupted", InterruptedFallback))
}

func (a *Agent) permissionMode() string {
	if a.cfg.PermissionMode != nil {
		return a.cfg.PermissionMode()
	}
	if a.cfg.Modes != nil {
		return string(a.cfg.Modes.Mode())
	}
	return ""
}

// record appends a transcript entry (no-op without transcript).
func (a *Agent) record(e session.Entry) string {
	if a.cfg.Transcript == nil {
		return e.UUID
	}
	id, err := a.cfg.Transcript.Append(e)
	if err != nil {
		a.log().Warn("transcript write failed", "err", err)
	}
	return id
}

// appendMessage adds a message, merging consecutive messages of one role.
func (a *Agent) appendMessage(role string, blocks []llm.ContentBlock) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := len(a.messages)
	if n > 0 && a.messages[n-1].Role == role {
		merged := append(append([]llm.ContentBlock(nil), a.messages[n-1].Content...), blocks...)
		a.messages[n-1] = llm.Message{Role: role, Content: merged}
		return
	}
	a.messages = append(a.messages, llm.Message{Role: role, Content: append([]llm.ContentBlock(nil), blocks...)})
}

func userEntryMessage(blocks []llm.ContentBlock) *session.Message {
	if len(blocks) == 1 && blocks[0].Type == llm.BlockText {
		return session.UserText(blocks[0].Text)
	}
	return session.UserBlocks(blocks)
}

func (a *Agent) projectContextBlock() llm.ContentBlock {
	tmpl := a.Bundle().Reminder("project_instructions", "{{content}}")
	return llm.TextBlock(prompts.Render(tmpl, map[string]string{"content": a.cfg.ProjectContext}, nil))
}

// RunTurn runs one turn for the given user input and blocks until the model
// stops calling tools, the turn is interrupted, max turns is reached or an
// error occurs. The tool_use/tool_result pairing invariant always holds when
// it returns.
func (a *Agent) RunTurn(parent context.Context, input []llm.ContentBlock) TurnResult {
	ctx, cancel := context.WithCancel(parent)
	a.mu.Lock()
	a.cancel = cancel
	maxTurns := a.cfg.MaxTurns
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.cancel = nil
		a.mu.Unlock()
		cancel()
	}()

	res := TurnResult{ModelUsage: map[string]*ModelUsage{}}
	start := time.Now()
	finish := func() TurnResult { res.Duration = time.Since(start); return res }

	promptID := session.NewID()
	var first []llm.ContentBlock
	a.mu.Lock()
	inject := !a.contextInjected && len(a.messages) == 0 && a.cfg.ProjectContext != ""
	if inject {
		a.contextInjected = true
	}
	a.mu.Unlock()
	if inject {
		block := a.projectContextBlock()
		first = append(first, block)
		a.record(session.Entry{Type: session.TypeUser, Message: session.UserBlocks([]llm.ContentBlock{block}), IsMeta: true, PromptID: promptID})
	}
	if len(input) > 0 {
		uid := session.NewID()
		if a.cfg.FileHistory != nil {
			snap := a.cfg.FileHistory.MakeCheckpoint(uid)
			created, _ := time.Parse("2006-01-02T15:04:05.000Z", snap.Timestamp)
			a.emit(Event{Kind: EventCheckpoint, Checkpoint: &Checkpoint{ID: uid, UserMessageUUID: uid, PromptPreview: preview(input), CreatedAt: created}})
		}
		a.record(session.Entry{Type: session.TypeUser, UUID: uid, Message: userEntryMessage(input), PromptID: promptID, PermissionMode: a.permissionMode()})
		a.notePrompt(input)
		a.emit(Event{Kind: EventUser, UUID: uid, Message: &llm.Message{Role: llm.RoleUser, Content: input}})
		a.emit(Event{Kind: EventTurnStarted, UUID: uid, Model: a.Model()})
		if a.permissionMode() == string(permissions.ModePlan) && a.cfg.ToolEnv != nil {
			a.Remind("plan_mode", map[string]string{"plan_file_path": tools.NextPlanPath(a.cfg.ToolEnv)})
		}
		a.remindAutoMode()
	}
	a.appendMessage(llm.RoleUser, append(first, input...))

	toolTurns := 0
	for {
		if ctx.Err() != nil {
			a.markInterrupted(nil)
			res.Subtype, res.IsError, res.Interrupted = SubtypeErrorDuringExecution, true, true
			return finish()
		}
		a.beforeRequest(ctx, promptID)
		if ctx.Err() != nil {
			continue
		}
		req := a.buildRequest()
		apiStart := time.Now()
		resp, acc, err := gateway.Send(ctx, a.cfg.Gateway, req, a.onStreamEvent, a.retryPolicy())
		res.APIDuration += time.Since(apiStart)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				a.markInterrupted(acc)
				res.Subtype, res.IsError, res.Interrupted = SubtypeErrorDuringExecution, true, true
				return finish()
			}
			a.record(session.Entry{Type: session.TypeSystem, Subtype: "api_error", Level: "error", Content: err.Error()})
			res.Subtype, res.IsError = SubtypeErrorDuringExecution, true
			res.Errors = append(res.Errors, err.Error())
			return finish()
		}
		a.accountUsage(&res, resp)
		res.StopReason = resp.StopReason

		content := sanitizeAssistant(resp.Message.Content)
		if len(content) == 0 {
			// Nothing usable: end the turn without polluting history.
			res.Subtype = SubtypeSuccess
			return finish()
		}
		resp.Message.Content = content
		a.appendMessage(llm.RoleAssistant, content)
		a.noteUsage(resp.Usage)
		asstUUID := a.record(session.Entry{Type: session.TypeAssistant, Message: session.AssistantMessage(resp), RequestID: resp.ID})
		msg := resp.Message
		a.emit(Event{Kind: EventAssistant, UUID: asstUUID, Message: &msg, Response: &resp})

		uses := msg.ToolUses()
		if len(uses) == 0 {
			res.Subtype = SubtypeSuccess
			res.Result = msg.Text()
			res.NumTurns = toolTurns + 1
			return finish()
		}

		outcomes, interrupted := a.executeTools(ctx, uses, rationales(msg.Content), &res)
		var blocks []llm.ContentBlock
		var comments []llm.ContentBlock
		for i, o := range outcomes {
			blocks = append(blocks, o.block)
			ub := []llm.ContentBlock{o.block}
			id := a.record(session.Entry{Type: session.TypeUser, Message: session.UserBlocks(ub), ToolUseResult: o.structured, SourceToolAssistantUUID: asstUUID, PromptID: promptID, AutoDecision: o.auto})
			a.emit(Event{Kind: EventUser, UUID: id, Message: &llm.Message{Role: llm.RoleUser, Content: ub}, Raw: rawStructured(outcomes[i].structured)})
			if o.comment != "" {
				comments = append(comments, llm.TextBlock(o.comment))
			}
		}
		a.noteToolRound(uses)
		var tail []llm.ContentBlock
		tail = append(tail, comments...)
		if interrupted {
			tail = append(tail, llm.TextBlock(a.interruptedText()))
		} else if q := a.DrainQueue(); len(q) > 0 {
			tail = append(tail, q...)
		}
		if len(tail) > 0 {
			blocks = append(blocks, tail...)
			id := a.record(session.Entry{Type: session.TypeUser, Message: userEntryMessage(tail), PromptID: promptID})
			a.emit(Event{Kind: EventUser, UUID: id, Message: &llm.Message{Role: llm.RoleUser, Content: tail}})
		}
		a.appendMessage(llm.RoleUser, blocks)
		toolTurns++
		res.NumTurns = toolTurns
		if interrupted {
			res.Subtype, res.IsError, res.Interrupted = SubtypeErrorDuringExecution, true, true
			return finish()
		}
		if maxTurns > 0 && toolTurns >= maxTurns {
			res.Subtype, res.IsError = SubtypeErrorMaxTurns, true
			res.Errors = append(res.Errors, fmt.Sprintf("Reached maximum number of turns (%d)", maxTurns))
			return finish()
		}
	}
}

func preview(blocks []llm.ContentBlock) string {
	for _, b := range blocks {
		if b.Type == llm.BlockText && strings.TrimSpace(b.Text) != "" {
			line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(b.Text), "\n", 2)[0])
			if len(line) > 120 {
				line = line[:120] + "…"
			}
			return line
		}
	}
	return ""
}

func rawStructured(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func (a *Agent) retryPolicy() *gateway.RetryPolicy {
	p := gateway.DefaultRetry
	if a.cfg.Retry != nil {
		p = *a.cfg.Retry
	}
	user := p.OnRetry
	p.OnRetry = func(attempt int, delay time.Duration, err error) {
		info := &llm.RetryInfo{Attempt: attempt, MaxRetries: p.MaxRetries, RetryDelayMs: int(delay.Milliseconds())}
		var ae *llm.APIError
		if errors.As(err, &ae) {
			info.Error = ae
		} else {
			info.Error = &llm.APIError{Type: "connection", Message: err.Error()}
		}
		a.emit(Event{Kind: EventRetry, Retry: info})
		if user != nil {
			user(attempt, delay, err)
		}
	}
	return &p
}

func (a *Agent) onStreamEvent(ev llm.StreamEvent) {
	switch ev.Type {
	case llm.EventPing:
		return
	case llm.EventHarnessRetry:
		var info llm.RetryInfo
		if json.Unmarshal(ev.Raw, &info) == nil {
			a.emit(Event{Kind: EventRetry, Retry: &info})
		}
		return
	}
	a.emit(Event{Kind: EventStream, Raw: ev.Raw})
}

func (a *Agent) accountUsage(res *TurnResult, resp llm.Response) {
	res.Usage.Add(resp.Usage)
	res.CostUSD += resp.Harness.CostUSD
	model := resp.Model
	if model == "" {
		model = a.Model()
	}
	mu := res.ModelUsage[model]
	if mu == nil {
		mu = &ModelUsage{}
		res.ModelUsage[model] = mu
	}
	mu.InputTokens += resp.Usage.InputTokens
	mu.OutputTokens += resp.Usage.OutputTokens
	mu.CacheReadInputTokens += resp.Usage.CacheReadInputTokens
	mu.CacheCreationInputTokens += resp.Usage.CacheCreationInputTokens
	mu.CostUSD += resp.Harness.CostUSD
}

// sanitizeAssistant drops blocks the API would reject in history.
func sanitizeAssistant(in []llm.ContentBlock) []llm.ContentBlock {
	var out []llm.ContentBlock
	for _, b := range in {
		switch b.Type {
		case llm.BlockText:
			if b.Text == "" {
				continue
			}
		case llm.BlockThinking:
			if b.Thinking == "" && b.Signature == "" {
				continue
			}
		}
		out = append(out, b)
	}
	return out
}

// markInterrupted records an interrupted stream: completed text/thinking
// blocks are kept, tool_use blocks are dropped (they were never executed),
// and the interrupt marker is appended as a user message.
func (a *Agent) markInterrupted(acc *llm.Accumulator) {
	if acc != nil {
		var keep []llm.ContentBlock
		for _, b := range acc.CompletedBlocks() {
			switch b.Type {
			case llm.BlockText:
				if b.Text != "" {
					keep = append(keep, b)
				}
			case llm.BlockThinking:
				if b.Signature != "" {
					keep = append(keep, b)
				}
			case llm.BlockRedactedThinking:
				keep = append(keep, b)
			}
		}
		if len(keep) > 0 {
			partial := acc.Response()
			partial.Message.Content = keep
			a.appendMessage(llm.RoleAssistant, keep)
			id := a.record(session.Entry{Type: session.TypeAssistant, Message: session.AssistantMessage(partial)})
			m := partial.Message
			a.emit(Event{Kind: EventAssistant, UUID: id, Message: &m, Response: &partial})
		}
	}
	marker := []llm.ContentBlock{llm.TextBlock(a.interruptedText())}
	a.appendMessage(llm.RoleUser, marker)
	id := a.record(session.Entry{Type: session.TypeUser, Message: userEntryMessage(marker)})
	a.emit(Event{Kind: EventUser, UUID: id, Message: &llm.Message{Role: llm.RoleUser, Content: marker}})
}

// toolNames returns the tools offered to the model.
func (a *Agent) toolNames() []string {
	var names []string
	for _, n := range a.cfg.Tools.Names() {
		if !a.cfg.DisabledTools[n] {
			names = append(names, n)
		}
	}
	return names
}

// toolDefs returns the bundle tool definitions followed by ExtraTools.
func (a *Agent) toolDefs(bundle *prompts.Bundle) []llm.ToolDef {
	defs := bundle.ToolDefs(a.toolNames())
	if a.cfg.ExtraTools != nil {
		defs = append(defs, a.cfg.ExtraTools()...)
	}
	return defs
}

func (a *Agent) systemBlocks(model, display string) []llm.SystemBlock {
	vars := map[string]string{}
	for k, v := range a.cfg.Vars {
		vars[k] = v
	}
	vars["model"], vars["model_id"] = model, model
	if display == "" {
		display = model
	}
	vars["model_display_name"] = display
	a.mu.Lock()
	var omit []string
	if a.omitCoding {
		omit = prompts.CodingSections
	}
	bundle := a.bundle
	a.mu.Unlock()
	return bundle.BuildSystemWithout(vars, func(name string) {
		a.mu.Lock()
		seen := a.warned[name]
		a.warned[name] = true
		a.mu.Unlock()
		if !seen {
			a.log().Warn("unknown prompt placeholder", "name", name)
		}
	}, omit...)
}

// buildRequest assembles the gateway request with cache markers: the last
// static system block (from the bundle), the project-context block and the
// last block of the last message — at most 3 of the 4 allowed points.
func (a *Agent) buildRequest() *llm.Request {
	a.mu.Lock()
	model, display := a.model, a.modelDisplay
	msgs := make([]llm.Message, len(a.messages))
	copy(msgs, a.messages)
	injected := a.contextInjected
	effort := a.cfg.Effort
	bundle := a.bundle
	a.mu.Unlock()

	system := a.systemBlocks(model, display)
	mark := func(mi, bi int) {
		if mi < 0 || mi >= len(msgs) || bi < 0 || bi >= len(msgs[mi].Content) {
			return
		}
		c := append([]llm.ContentBlock(nil), msgs[mi].Content...)
		c[bi].Cache = true
		msgs[mi] = llm.Message{Role: msgs[mi].Role, Content: c}
	}
	if injected && len(msgs) > 0 {
		mark(0, 0)
	}
	if n := len(msgs); n > 0 {
		mark(n-1, len(msgs[n-1].Content)-1)
	}

	return &llm.Request{
		Model:     model,
		MaxTokens: a.cfg.MaxTokens,
		System:    system,
		Messages:  msgs,
		Tools:     a.toolDefs(bundle),
		Thinking:  a.thinking(),
		Effort:    effort,
		Stream:    true,
		Metadata: &llm.Metadata{
			SessionID:           a.cfg.SessionID,
			RequestClass:        "main",
			PromptBundleVersion: bundle.Version,
		},
	}
}

type outcome struct {
	block      llm.ContentBlock
	structured any
	comment    string
	auto       *AutoDecision
}

// executeTools runs tool calls: consecutive read-only calls in parallel,
// mutating calls one at a time, in model order. Every tool_use gets exactly
// one tool_result; on interrupt unfinished calls get a synthetic error.
// rationale maps tool_use ids to the assistant text before them.
func (a *Agent) executeTools(ctx context.Context, uses []llm.ContentBlock, rationale map[string]string, res *TurnResult) ([]outcome, bool) {
	outcomes := make([]outcome, len(uses))
	autos := make([]*AutoDecision, len(uses))
	defer func() {
		for k := range outcomes {
			outcomes[k].auto = autos[k]
		}
	}()
	done := make([]bool, len(uses))
	interruptedText := a.interruptedToolText()
	started := make([]time.Time, len(uses))
	end := func(i int, interrupted bool, summary string) {
		var d time.Duration
		if !started[i].IsZero() {
			d = time.Since(started[i])
		}
		b := outcomes[i].block
		a.emit(Event{Kind: EventToolEnd, ToolUseID: uses[i].ID, ToolName: uses[i].Name, IsError: b.IsError, Interrupted: interrupted,
			Content: b.Content, Duration: d, Structured: outcomes[i].structured, Summary: summary})
	}
	synthetic := func(i int) {
		outcomes[i] = outcome{block: llm.ContentBlock{Type: llm.BlockToolResult, ToolUseID: uses[i].ID, IsError: true, Content: []llm.ContentBlock{llm.TextBlock(interruptedText)}}, structured: interruptedText}
		done[i] = true
		end(i, true, "Interrupted")
	}
	setResult := func(i int, r tools.Result, comment string) {
		outcomes[i] = outcome{block: llm.ContentBlock{Type: llm.BlockToolResult, ToolUseID: uses[i].ID, IsError: r.IsError, Content: r.Content}, structured: r.Structured, comment: comment}
		done[i] = true
		end(i, false, r.Summary)
	}
	start := func(i int, input json.RawMessage) {
		started[i] = time.Now()
		a.emit(Event{Kind: EventToolStart, ToolUseID: uses[i].ID, ToolName: uses[i].Name, Input: input})
	}

	type call struct {
		idx     int
		tool    tools.Tool
		input   json.RawMessage
		comment string
	}
	readOnly := func(u llm.ContentBlock) bool {
		t, ok := a.cfg.Tools.Get(u.Name)
		return ok && !a.cfg.DisabledTools[u.Name] && t.ReadOnly(u.Input)
	}

	for i := 0; i < len(uses); {
		j := i + 1
		if readOnly(uses[i]) {
			for j < len(uses) && readOnly(uses[j]) {
				j++
			}
		}
		batch := uses[i:j]
		base := i
		i = j

		if ctx.Err() != nil {
			for k := range batch {
				synthetic(base + k)
			}
			continue
		}
		var runnable []call
		for k, u := range batch {
			idx := base + k
			t, ok := a.cfg.Tools.Get(u.Name)
			if !ok || a.cfg.DisabledTools[u.Name] {
				setResult(idx, tools.ErrorResult("No such tool available: %s", u.Name), "")
				continue
			}
			req := permissions.Request{ToolName: u.Name, ToolUseID: u.ID, Input: u.Input, ReadOnly: t.ReadOnly(u.Input)}
			req.FilePath = tools.FilePath(u.Input)
			if aa, ok := t.(tools.AlwaysAsker); ok && aa.AlwaysAsk() {
				req.AlwaysAsk = true
			}
			if u.Name == "Bash" {
				req.Command = tools.BashCommand(u.Input)
				if a.cfg.ToolEnv != nil && a.cfg.ToolEnv.Shell != nil {
					req.Cwd = a.cfg.ToolEnv.Shell.Cwd()
				}
			}
			req.SessionID, req.RecentUserMessages, req.Rationale = a.cfg.SessionID, a.recentPrompts(), rationale[u.ID]
			d := a.cfg.Permissions.Check(ctx, req)
			if ctx.Err() != nil {
				synthetic(idx)
				continue
			}
			if d.Auto != nil {
				autos[idx] = a.noteAutoDecision(u, d)
			}
			if d.Behavior != permissions.Allow {
				denial := PermissionDenial{ToolName: u.Name, ToolUseID: u.ID, ToolInput: u.Input}
				if d.Auto != nil && d.ResolvedBy == "auto" {
					denial.Reason, denial.DecidedBy = d.Auto.Reason, "auto:"+d.Auto.Provider
				}
				res.PermissionDenials = append(res.PermissionDenials, denial)
				setResult(idx, tools.Result{Content: []llm.ContentBlock{llm.TextBlock(d.Message)}, IsError: true, Structured: "Error: " + d.Message, Summary: "Denied"}, "")
				if d.Interrupt {
					a.Interrupt()
				}
				continue
			}
			in := u.Input
			if len(d.UpdatedInput) > 0 {
				in = d.UpdatedInput
			}
			runnable = append(runnable, call{idx: idx, tool: t, input: in, comment: d.Comment})
		}
		if len(runnable) == 1 || (len(runnable) > 0 && !readOnly(uses[runnable[0].idx])) {
			for _, c := range runnable {
				if ctx.Err() != nil {
					synthetic(c.idx)
					continue
				}
				start(c.idx, c.input)
				r := a.runTool(ctx, c.tool, uses[c.idx].ID, c.input)
				if ctx.Err() != nil {
					synthetic(c.idx)
				} else {
					setResult(c.idx, r, c.comment)
				}
			}
			continue
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, c := range runnable {
			start(c.idx, c.input)
		}
		for _, c := range runnable {
			wg.Add(1)
			go func(c call) {
				defer wg.Done()
				r := a.runTool(ctx, c.tool, uses[c.idx].ID, c.input)
				mu.Lock()
				defer mu.Unlock()
				if ctx.Err() != nil {
					synthetic(c.idx)
				} else {
					setResult(c.idx, r, c.comment)
				}
			}(c)
		}
		wg.Wait()
	}
	for k := range uses {
		if !done[k] {
			synthetic(k)
		}
	}
	return outcomes, ctx.Err() != nil
}

func (a *Agent) runTool(ctx context.Context, t tools.Tool, id string, input json.RawMessage) (r tools.Result) {
	defer func() {
		if p := recover(); p != nil {
			r = tools.ErrorResult("tool %s panicked: %v", t.Name(), p)
		}
	}()
	return t.Run(ctx, a.cfg.ToolEnv, tools.Call{ID: id, Input: input})
}
