package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/xlsft/harness/packages/client/hooks"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/tools"
)

// ReminderHookContext carries what a hook has to say to the model: context it added, or why it refused something.
const ReminderHookContext = "hook_context"

// hookContextFallback is the hook_context reminder used when the prompt bundle has none (a resumed session keeps the bundle
// it started with).
const hookContextFallback = `<system-reminder>
A {{event}} hook, a program this session's settings run, reports:
<hook-output>
{{content}}
</hook-output>
Take it into account. It is not a message from the user and approves nothing.
</system-reminder>`

// NoticeHookStop is the Notice kind of a hook that ended the work (continue false).
const NoticeHookStop = "hook_stop"

// maxStopContinuations bounds how many times Stop hooks can send one turn back to work. A handler that always refuses, and
// ignores stop_hook_active, would otherwise spend the user's money until someone notices.
const maxStopContinuations = 16

// AddHookContext hands text from a hook's event to the model with its next request.
func (a *Agent) AddHookContext(ev hooks.Event, texts ...string) {
	for _, t := range texts {
		if t = strings.TrimSpace(t); t != "" {
			a.Remind(ReminderHookContext, map[string]string{"event": string(ev), "content": t})
		}
	}
}

// SetStartGate makes the next turn wait until gate closes: SessionStart hooks run in the background, so that opening a session
// does not wait for them, and what they add must still reach the first request.
func (a *Agent) SetStartGate(gate <-chan struct{}) {
	a.mu.Lock()
	a.startGate = gate
	a.mu.Unlock()
}

func (a *Agent) waitStartHooks(ctx context.Context) {
	a.mu.Lock()
	gate := a.startGate
	a.startGate = nil
	a.mu.Unlock()
	if gate == nil {
		return
	}
	select {
	case <-gate:
	case <-ctx.Done():
	}
}

func (a *Agent) resetHalt() {
	a.mu.Lock()
	a.halted, a.haltReason, a.stopContinues = false, "", 0
	a.mu.Unlock()
}

// halt remembers that a handler said continue false; the turn ends once its tool results are in.
func (a *Agent) halt(reason string) {
	a.mu.Lock()
	a.halted = true
	if reason != "" {
		a.haltReason = reason
	}
	a.mu.Unlock()
}

// takeHalt reports whether the turn must end for a hook, telling the user why.
func (a *Agent) takeHalt() bool {
	a.mu.Lock()
	halted, reason := a.halted, a.haltReason
	a.halted, a.haltReason = false, ""
	a.mu.Unlock()
	if halted {
		a.noticeHalt(reason)
	}
	return halted
}

func (a *Agent) noticeHalt(reason string) {
	if reason == "" {
		reason = "A hook stopped the agent."
	}
	a.emit(Event{Kind: EventNotice, Notice: &Notice{Level: "warning", Kind: NoticeHookStop, Text: reason}})
}

// hookInput fills what every event of this loop says about the agent.
func (a *Agent) hookInput(in hooks.Input) hooks.Input {
	in.AgentID, in.AgentType = a.cfg.AgentID, a.cfg.AgentType
	if env := a.cfg.ToolEnv; env != nil && env.Shell != nil {
		in.Cwd = env.Shell.Cwd()
	}
	return in
}

// submitHook runs UserPromptSubmit for a message of the user (subagents get no such messages). It reports whether the message
// goes on. A refused one is reported to frontends, which are waiting to hear about it, and goes no further.
func (a *Agent) submitHook(ctx context.Context, blocks []llm.ContentBlock, clientIDs []string, startsTurn bool) bool {
	if a.cfg.AgentID != "" {
		return true
	}
	var text []string
	for _, b := range blocks {
		if b.Type == llm.BlockText {
			text = append(text, b.Text)
		}
	}
	out := a.cfg.Hooks.Dispatch(ctx, hooks.UserPromptSubmit, "", a.hookInput(hooks.Input{Prompt: strings.Join(text, "\n")}))
	if !out.Blocked && !out.Halt {
		a.AddHookContext(hooks.UserPromptSubmit, out.Context...)
		return true
	}
	id := session.NewID()
	a.emit(Event{Kind: EventUser, UUID: id, Message: &llm.Message{Role: llm.RoleUser, Content: blocks}, ClientUUIDs: clientIDs})
	if startsTurn {
		a.emit(Event{Kind: EventTurnStarted, UUID: id, Model: a.Model()})
	}
	reason := out.Reason
	if out.Halt {
		reason = joinNonEmpty(out.HaltReason, reason)
	}
	if reason == "" {
		reason = "A hook refused this prompt."
	}
	a.emit(Event{Kind: EventNotice, Notice: &Notice{Level: "warning", Kind: NoticeHookStop, Text: reason}})
	return false
}

func joinNonEmpty(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n" + b
}

// subagentStartHook runs SubagentStart before the first turn of a subagent's loop.
func (a *Agent) subagentStartHook(ctx context.Context) {
	if a.cfg.AgentID == "" {
		return
	}
	a.mu.Lock()
	done := a.agentHooked
	a.agentHooked = true
	a.mu.Unlock()
	if done {
		return
	}
	out := a.cfg.Hooks.Dispatch(ctx, hooks.SubagentStart, a.cfg.AgentType, a.hookInput(hooks.Input{}))
	a.AddHookContext(hooks.SubagentStart, out.Context...)
}

// preToolHook runs PreToolUse for a call.
func (a *Agent) preToolHook(ctx context.Context, u llm.ContentBlock) hooks.Outcome {
	out := a.cfg.Hooks.Dispatch(ctx, hooks.PreToolUse, u.Name, a.hookInput(hooks.Input{ToolName: u.Name, ToolInput: u.Input, ToolUseID: u.ID}))
	a.AddHookContext(hooks.PreToolUse, out.Context...)
	if out.Halt {
		a.halt(out.HaltReason)
	}
	return out
}

// toolResponse is how a tool's result looks to a hook: its structured form, else its text.
func toolResponse(r tools.Result) any {
	if r.Structured != nil {
		return r.Structured
	}
	return resultText(r)
}

func resultText(r tools.Result) string {
	var parts []string
	for _, b := range r.Content {
		if b.Type == llm.BlockText {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// postToolHook runs PostToolUse or PostToolUseFailure for a call that ran. The tool's effects stand whatever a hook says: what
// it says goes to the model. An interrupted call has no result worth judging, and no hook runs for it.
func (a *Agent) postToolHook(ctx context.Context, u llm.ContentBlock, input json.RawMessage, r tools.Result) tools.Result {
	if ctx.Err() != nil {
		return r
	}
	ev, in := hooks.PostToolUse, hooks.Input{ToolName: u.Name, ToolInput: input, ToolUseID: u.ID}
	if r.IsError {
		ev, in.Error = hooks.PostToolUseFailure, resultText(r)
	} else {
		in.ToolResponse = toolResponse(r)
	}
	out := a.cfg.Hooks.Dispatch(ctx, ev, u.Name, a.hookInput(in))
	a.AddHookContext(ev, out.Reason)
	a.AddHookContext(ev, out.Context...)
	if out.Halt {
		a.halt(out.HaltReason)
	}
	if len(out.UpdatedToolOutput) > 0 && strings.HasPrefix(u.Name, "mcp__") && !r.IsError {
		text := string(out.UpdatedToolOutput)
		var s string
		if json.Unmarshal(out.UpdatedToolOutput, &s) == nil {
			text = s
		}
		r.Content, r.Structured = []llm.ContentBlock{llm.TextBlock(text)}, text
	}
	return r
}

// postToolBatchHook runs PostToolBatch when all the calls of a model reply are done.
func (a *Agent) postToolBatchHook(ctx context.Context, uses []llm.ContentBlock, outcomes []outcome) {
	if !a.cfg.Hooks.Set().Has(hooks.PostToolBatch) {
		return
	}
	calls := make([]hooks.ToolCall, len(uses))
	for i, u := range uses {
		calls[i] = hooks.ToolCall{ToolName: u.Name, ToolInput: u.Input, ToolUseID: u.ID, IsError: outcomes[i].block.IsError}
	}
	out := a.cfg.Hooks.Dispatch(ctx, hooks.PostToolBatch, "", a.hookInput(hooks.Input{ToolCalls: calls}))
	a.AddHookContext(hooks.PostToolBatch, out.Reason)
	a.AddHookContext(hooks.PostToolBatch, out.Context...)
	if out.Halt {
		a.halt(out.HaltReason)
	}
}

// permissionDeniedHook tells hooks that auto mode refused a call. Nothing can answer it.
func (a *Agent) permissionDeniedHook(u llm.ContentBlock, reason string) {
	a.cfg.Hooks.Fire(hooks.PermissionDenied, u.Name, a.hookInput(hooks.Input{ToolName: u.Name, ToolInput: u.Input, ToolUseID: u.ID, Reason: reason}))
}

// stopHookContinues runs Stop (SubagentStop in a subagent's loop) as the model ends its turn, and reports whether a handler
// sends it back to work: its reason goes to the model and the loop makes another request. It does not fire for an interrupted
// turn or a failed request.
func (a *Agent) stopHookContinues(ctx context.Context, active *bool, last string, maxTurns int, toolTurns *int) bool {
	ev := hooks.Stop
	if a.cfg.AgentID != "" {
		ev = hooks.SubagentStop
	}
	if !a.cfg.Hooks.Set().Has(ev) {
		return false
	}
	wasActive := *active
	in := hooks.Input{StopHookActive: &wasActive, LastAssistantMessage: &last}
	if ev == hooks.SubagentStop && a.cfg.Transcript != nil {
		in.AgentTranscriptPath = a.cfg.Transcript.Path()
	}
	out := a.cfg.Hooks.Dispatch(ctx, ev, a.cfg.AgentType, a.hookInput(in))
	if out.Halt {
		a.noticeHalt(out.HaltReason)
		return false
	}
	if !out.Blocked || ctx.Err() != nil {
		return false
	}
	if maxTurns > 0 && *toolTurns+1 >= maxTurns {
		return false
	}
	a.mu.Lock()
	a.stopContinues++
	count := a.stopContinues
	a.mu.Unlock()
	if count > maxStopContinuations {
		a.emit(Event{Kind: EventNotice, Notice: &Notice{Level: "warning", Kind: NoticeHookStop, Text: "Stop hooks sent the agent back to work too many times in one turn; ending it."}})
		return false
	}
	*active = true
	*toolTurns++
	a.AddHookContext(ev, out.Reason)
	a.AddHookContext(ev, out.Context...)
	return true
}

// stopFailureHook tells hooks that a turn ended because the model request failed. Nothing can answer it.
func (a *Agent) stopFailureHook(err error) {
	if !a.cfg.Hooks.Set().Has(hooks.StopFailure) {
		return
	}
	kind := "unknown"
	var ae *llm.APIError
	if errors.As(err, &ae) && ae.Type != "" {
		kind = ae.Type
	}
	a.cfg.Hooks.Fire(hooks.StopFailure, kind, a.hookInput(hooks.Input{Error: err.Error()}))
}

// preCompactHook runs PreCompact and returns the instructions to compact with, or an error when a handler refuses.
func (a *Agent) preCompactHook(ctx context.Context, trigger, instructions string) (string, error) {
	custom := instructions
	out := a.cfg.Hooks.Dispatch(ctx, hooks.PreCompact, trigger, a.hookInput(hooks.Input{Trigger: trigger, CustomInstructions: &custom}))
	if out.Blocked {
		return "", errors.New("a hook refused the compaction: " + out.Reason)
	}
	return joinNonEmpty(instructions, strings.Join(out.Context, "\n")), nil
}

// postCompactHook runs PostCompact, then SessionStart with source "compact": what SessionStart hooks added at the start of the
// session was in the conversation that was just summarized.
func (a *Agent) postCompactHook(ctx context.Context, info CompactInfo) {
	a.cfg.Hooks.Dispatch(ctx, hooks.PostCompact, info.Trigger, a.hookInput(hooks.Input{Trigger: info.Trigger, CompactSummary: info.Summary}))
	if a.cfg.AgentID != "" {
		return
	}
	const source = "compact"
	out := a.cfg.Hooks.Dispatch(ctx, hooks.SessionStart, source, a.hookInput(hooks.Input{Source: source, Model: a.Model()}))
	a.AddHookContext(hooks.SessionStart, out.Context...)
}
