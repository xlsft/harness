package agent

import "github.com/xlsft/harness/packages/client/llm"

// Context usage breakdown (AgentService.GetContextUsage).
//
// Token counts: the provider's usage of the last response (input + cache read + cache creation + output) is the
// real size of everything sent so far. Everything else uses one estimate, ~4 characters per token
// (EstimateTextTokens), the same heuristic auto-compaction uses: the system prompt, tool definitions (name,
// description and input schema) and the messages added since the last response. When the provider's total is
// known, Messages is that total minus the estimated system prompt and tools, so the categories add up to what
// the provider counted; before the first response (and right after a compaction) all of it is estimated.

// charsPerToken is the estimate used wherever the provider gives no count.
const charsPerToken = 4

// EstimateTextTokens estimates the tokens of chars characters of text.
func EstimateTextTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return (chars + charsPerToken - 1) / charsPerToken
}

// EstimateToolTokens estimates the tokens of a tool definition.
func EstimateToolTokens(d llm.ToolDef) int {
	return EstimateTextTokens(len(d.Name) + len(d.Description) + len(d.InputSchema))
}

// ContextUsage is the composition of the next request's context, in tokens.
type ContextUsage struct {
	Model string
	// Window is the model's context window (0 when unknown).
	Window int
	// SystemPrompt: the system blocks; SystemTools: definitions of the built-in (bundle) tools; MCPTools:
	// definitions of the extra tools offered to the model (ExtraTools: MCP tools that are not deferred or were
	// loaded with ToolSearch); Messages: the conversation.
	SystemPrompt, SystemTools, MCPTools, Messages int
	// Threshold is the context size that triggers auto-compaction; 0 when auto-compaction is off.
	Threshold int
	// Estimated is set when no provider count is known.
	Estimated bool
}

// Used is the size of the context: every category except the buffer and free space.
func (u ContextUsage) Used() int { return u.SystemPrompt + u.SystemTools + u.MCPTools + u.Messages }

// Buffer is the room auto-compaction keeps free (window minus threshold).
func (u ContextUsage) Buffer() int {
	if u.Threshold <= 0 || u.Window <= u.Threshold {
		return 0
	}
	return u.Window - u.Threshold
}

// Free is what remains of the window after the used tokens and the buffer.
func (u ContextUsage) Free() int { return max(0, u.Window-u.Used()-u.Buffer()) }

// UntilAutoCompact is how many tokens can still be added before auto-compaction (0 when off or reached).
func (u ContextUsage) UntilAutoCompact() int {
	if u.Threshold <= 0 {
		return 0
	}
	return max(0, u.Threshold-u.Used())
}

// Fraction is the share of the window in use (0 when the window is unknown).
func (u ContextUsage) Fraction() float64 {
	if u.Window <= 0 {
		return 0
	}
	return float64(u.Used()) / float64(u.Window)
}

// ContextUsage computes the breakdown of the context the next request would send. It is cheap enough to call on
// demand (no request is made) but renders the system prompt and lists the tools, so it is not meant for every
// event.
func (a *Agent) ContextUsage() ContextUsage {
	a.mu.Lock()
	msgs := append([]llm.Message(nil), a.messages...)
	last, count := a.lastUsage, a.lastMsgCount
	model, display := a.model, a.modelDisplay
	window, disabled := a.contextWindow, a.autoCompactDisabled
	bundle := a.bundle
	a.mu.Unlock()

	u := ContextUsage{Model: model, Window: window}
	chars := 0
	for _, s := range a.systemBlocks(model, display) {
		chars += len(s.Text)
	}
	u.SystemPrompt = EstimateTextTokens(chars)
	for _, d := range bundle.ToolDefs(a.toolNames()) {
		u.SystemTools += EstimateToolTokens(d)
	}
	if a.cfg.ExtraTools != nil {
		for _, d := range a.cfg.ExtraTools() {
			u.MCPTools += EstimateToolTokens(d)
		}
	}
	overhead := u.SystemPrompt + u.SystemTools + u.MCPTools
	if last > 0 && count <= len(msgs) {
		total := last + EstimateTextTokens(messagesChars(msgs[count:]))
		u.Messages = max(0, total-overhead)
	} else {
		u.Messages = EstimateTextTokens(messagesChars(msgs))
		u.Estimated = true
	}
	if window > 0 && !disabled {
		u.Threshold = a.contextLimit()
	}
	return u
}
