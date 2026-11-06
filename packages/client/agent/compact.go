package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/xlsft/harness/packages/client/gateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
)

// Compaction tuning.
const (
	// DefaultAutoCompactPct is the share of the context window that triggers
	// auto-compaction.
	DefaultAutoCompactPct = 0.92
	// MaxCompactFailures consecutive failed or ineffective auto-compactions
	// disable auto-compaction for the session.
	MaxCompactFailures = 3
	// keepToolResults recent tool results are never trimmed.
	keepToolResults = 3
	// trimmedToolResult replaces old tool output during trimming.
	trimmedToolResult = "[Old tool result content cleared to save context]"
	// minTrimChars: shorter tool results are not worth trimming.
	minTrimChars = 200
	// imageTokens approximates an image or document block.
	imageTokens = 1600
	// summaryMaxTokens caps the summary response.
	summaryMaxTokens = 20000
)

// ErrNothingToCompact is returned when the conversation is empty.
var ErrNothingToCompact = errors.New("nothing to compact")

// autoCompactPct resolves the trigger threshold: config, then
// HARNESS_AUTOCOMPACT_PCT ("92" or "0.92"), then the default.
func (a *Agent) autoCompactPct() float64 {
	if p := a.cfg.AutoCompactPct; p > 0 {
		return p
	}
	if v := os.Getenv("HARNESS_AUTOCOMPACT_PCT"); v != "" {
		if f, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64); err == nil && f > 0 {
			if f > 1 {
				f /= 100
			}
			return f
		}
	}
	return DefaultAutoCompactPct
}

func blockChars(b llm.ContentBlock) int {
	switch b.Type {
	case llm.BlockImage, llm.BlockDocument:
		return imageTokens * 4
	case llm.BlockToolResult:
		n := 0
		for _, c := range b.Content {
			n += blockChars(c)
		}
		return n + 20
	case llm.BlockToolUse:
		return len(b.Input) + len(b.Name) + 20
	case llm.BlockThinking:
		return len(b.Thinking) + len(b.Signature)/4
	}
	return len(b.Text)
}

func messagesChars(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			n += blockChars(b)
		}
	}
	return n
}

// noteUsage remembers the real context size reported for the last request.
func (a *Agent) noteUsage(u llm.Usage) {
	a.mu.Lock()
	a.lastUsage = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens + u.OutputTokens
	a.lastMsgCount = len(a.messages)
	a.mu.Unlock()
}

// EstimateTokens estimates the context size of the next request: the last
// reported usage plus ~4 characters per token for messages added since, or a
// full character-based estimate when no usage is known.
func (a *Agent) EstimateTokens() int {
	a.mu.Lock()
	msgs := append([]llm.Message(nil), a.messages...)
	last, count := a.lastUsage, a.lastMsgCount
	model, display := a.model, a.modelDisplay
	a.mu.Unlock()
	if last > 0 && count <= len(msgs) {
		return last + messagesChars(msgs[count:])/4
	}
	n := messagesChars(msgs)
	for _, s := range a.systemBlocks(model, display) {
		n += len(s.Text)
	}
	for _, t := range a.toolDefs(a.Bundle()) {
		n += len(t.Description) + len(t.InputSchema) + len(t.Name)
	}
	return n / 4
}

func (a *Agent) contextLimit() int {
	a.mu.Lock()
	w := a.contextWindow
	a.mu.Unlock()
	if w <= 0 {
		return 0
	}
	return int(float64(w) * a.autoCompactPct())
}

// trimToolResults replaces the content of old tool results (all but the
// last few) with a placeholder. It reports whether anything changed.
func (a *Agent) trimToolResults() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	type pos struct{ m, b int }
	var all []pos
	for mi, m := range a.messages {
		for bi, b := range m.Content {
			if b.Type == llm.BlockToolResult {
				all = append(all, pos{mi, bi})
			}
		}
	}
	if len(all) <= keepToolResults {
		return false
	}
	changed := false
	for _, p := range all[:len(all)-keepToolResults] {
		b := a.messages[p.m].Content[p.b]
		if blockChars(b) < minTrimChars {
			continue
		}
		if len(b.Content) == 1 && b.Content[0].Text == trimmedToolResult {
			continue
		}
		c := append([]llm.ContentBlock(nil), a.messages[p.m].Content...)
		c[p.b].Content = []llm.ContentBlock{llm.TextBlock(trimmedToolResult)}
		a.messages[p.m] = llm.Message{Role: a.messages[p.m].Role, Content: c}
		changed = true
	}
	if changed {
		a.lastUsage = 0 // the reported usage no longer matches the history
	}
	return changed
}

// maybeAutoCompact compacts when the estimated context exceeds the
// threshold: first by trimming old tool outputs, then by summarizing.
func (a *Agent) maybeAutoCompact(ctx context.Context) {
	limit := a.contextLimit()
	a.mu.Lock()
	disabled := a.autoCompactDisabled
	a.mu.Unlock()
	if limit <= 0 || disabled {
		return
	}
	before := a.EstimateTokens()
	if before < limit {
		return
	}
	if a.trimToolResults() {
		if after := a.EstimateTokens(); after < limit {
			a.emit(Event{Kind: EventNotice, Notice: &Notice{Level: "info", Kind: "compacted", Text: fmt.Sprintf("Cleared old tool results (%d → %d tokens)", before, after)}})
			a.mu.Lock()
			a.compactFailures = 0
			a.mu.Unlock()
			return
		}
	}
	info, err := a.compact(ctx, "", "auto")
	a.mu.Lock()
	if err != nil || info.TokensAfter >= limit {
		a.compactFailures++
	} else {
		a.compactFailures = 0
	}
	failures := a.compactFailures
	if failures >= MaxCompactFailures {
		a.autoCompactDisabled = true
	}
	a.mu.Unlock()
	switch {
	case failures >= MaxCompactFailures:
		a.emit(Event{Kind: EventNotice, Notice: &Notice{Level: "warning", Kind: "compaction_disabled", Text: fmt.Sprintf("Auto-compaction stopped after %d failed or ineffective attempts; use /compact manually.", failures)}})
	case err != nil && ctx.Err() == nil:
		a.emit(Event{Kind: EventNotice, Notice: &Notice{Level: "warning", Kind: "compaction_failed", Text: "Auto-compaction failed: " + err.Error()}})
	}
}

// Compact summarizes the conversation now (manual /compact). The session
// must be idle.
func (a *Agent) Compact(ctx context.Context, instructions string) (CompactInfo, error) {
	if a.Busy() {
		return CompactInfo{}, ErrBusy
	}
	info, err := a.compact(ctx, instructions, "manual")
	if err == nil {
		a.mu.Lock()
		a.compactFailures, a.autoCompactDisabled = 0, false
		a.mu.Unlock()
	}
	return info, err
}

// CompactCommand recognizes "/compact [focus instructions]" and returns the instructions.
func CompactCommand(prompt string) (string, bool) {
	p := strings.TrimSpace(prompt)
	if p == "/compact" {
		return "", true
	}
	if strings.HasPrefix(p, "/compact ") || strings.HasPrefix(p, "/compact\n") {
		return strings.TrimSpace(p[len("/compact"):]), true
	}
	return "", false
}

// CompactIdle summarizes the conversation of a session nobody has used for a while (trigger "idle"). The
// session must be idle; failures do not count against auto-compaction.
func (a *Agent) CompactIdle(ctx context.Context) (CompactInfo, error) {
	if a.Busy() {
		return CompactInfo{}, ErrBusy
	}
	return a.compact(ctx, "", "idle")
}

// summarize asks the gateway (request class compaction) for a summary of
// msgs using the bundle's compaction instruction.
func (a *Agent) summarize(ctx context.Context, msgs []llm.Message, instructions string) (string, error) {
	if len(msgs) == 0 {
		return "", ErrNothingToCompact
	}
	bundle := a.Bundle()
	instr := "Summarize the conversation so far so that the work can continue from the summary alone."
	if bundle.Compaction != nil && strings.TrimSpace(bundle.Compaction.Instruction) != "" {
		instr = bundle.Compaction.Instruction
	}
	if s := strings.TrimSpace(instructions); s != "" {
		instr += "\n\nAdditional instructions: " + s
	}
	conv := append([]llm.Message(nil), msgs...)
	// Strip cache markers and make sure the request ends with the instruction.
	last := &conv[len(conv)-1]
	if last.Role == llm.RoleUser {
		last.Content = append(append([]llm.ContentBlock(nil), last.Content...), llm.TextBlock(instr))
	} else {
		conv = append(conv, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock(instr)}})
	}
	a.mu.Lock()
	model, display := a.model, a.modelDisplay
	a.mu.Unlock()
	maxTokens := min(a.cfg.MaxTokens, summaryMaxTokens)
	req := &llm.Request{
		Model:      model,
		MaxTokens:  maxTokens,
		System:     a.systemBlocks(model, display),
		Messages:   conv,
		Tools:      a.toolDefs(bundle),
		ToolChoice: &llm.ToolChoice{Type: "none"},
		Stream:     true,
		Metadata:   &llm.Metadata{SessionID: a.cfg.SessionID, RequestClass: "compaction", AgentID: a.cfg.AgentID, PromptBundleVersion: bundle.Version},
	}
	resp, _, err := gateway.Send(ctx, a.cfg.Gateway, req, nil, a.retryPolicy())
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(resp.Message.Text())
	if text == "" {
		return "", errors.New("the model returned an empty summary")
	}
	return text, nil
}

// summaryBlocks builds the blocks that replace the summarized history.
func (a *Agent) summaryBlocks(summary string) []llm.ContentBlock {
	blocks := []llm.ContentBlock{llm.TextBlock(summary)}
	if r := a.Bundle().Reminder("compacted", ""); r != "" {
		blocks = append(blocks, llm.TextBlock(r))
	}
	return blocks
}

func (a *Agent) compact(ctx context.Context, instructions, trigger string) (CompactInfo, error) {
	// The lost turn's end goes before the boundary, where its tool calls are.
	a.recordLostTurn()
	instructions, err := a.preCompactHook(ctx, trigger, instructions)
	if err != nil {
		return CompactInfo{}, err
	}
	msgs := a.Messages()
	before := a.EstimateTokens()
	summary, err := a.summarize(ctx, msgs, instructions)
	if err != nil {
		return CompactInfo{}, err
	}
	// Transcript: boundary (no parent, logical link to the old leaf), the
	// project context again, then the summary.
	var msgBlocks []llm.ContentBlock
	if w := a.cfg.Transcript; w != nil {
		leaf := w.LastUUID()
		_, _ = w.AppendBranch(session.Entry{Type: session.TypeSystem, Subtype: session.SubtypeCompactBoundary, Content: "Conversation compacted",
			Level: "info", LogicalParentUUID: leaf, CompactMetadata: &session.CompactMetadata{Trigger: trigger, PreTokens: before}}, nil)
	}
	injected := false
	for _, block := range a.contextBlocks() {
		msgBlocks = append(msgBlocks, block)
		a.record(session.Entry{Type: session.TypeUser, Message: session.UserBlocks([]llm.ContentBlock{block}), IsMeta: true})
		injected = true
	}
	sb := a.summaryBlocks(summary)
	msgBlocks = append(msgBlocks, sb...)
	a.record(session.Entry{Type: session.TypeUser, Message: session.UserBlocks(sb), IsCompactSummary: true, IsVisibleInTranscriptOnly: true})
	a.mu.Lock()
	a.messages = []llm.Message{{Role: llm.RoleUser, Content: msgBlocks}}
	a.contextInjected = injected
	a.lastUsage = 0
	a.afterCompactStyle()
	a.autoReminded = false
	a.mu.Unlock()
	info := CompactInfo{Trigger: trigger, TokensBefore: before, TokensAfter: a.EstimateTokens(), Summary: summary}
	a.refreshBundle(ctx)
	a.emit(Event{Kind: EventCompacted, Compaction: &info})
	a.postCompactHook(ctx, info)
	return info, nil
}

// refreshBundle re-fetches the prompt bundle after a compaction; a new
// version is recorded in the transcript.
func (a *Agent) refreshBundle(ctx context.Context) {
	if a.cfg.RefreshBundle == nil {
		return
	}
	b, err := a.cfg.RefreshBundle(ctx)
	if err != nil || b == nil {
		return
	}
	a.mu.Lock()
	old := a.bundle
	changed := old == nil || old.Version != b.Version
	if changed {
		a.bundle = b
	}
	model := a.model
	a.mu.Unlock()
	if changed {
		a.record(session.Entry{Type: session.TypeMeta, PromptBundleVersion: b.Version, Model: model})
	}
}
