package agent

import (
	"context"
	"time"

	"github.com/xlsft/harness/packages/client/gateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
)

// Interrupt reply tuning.
const (
	// InterruptReplyTimeout bounds the interrupt reply request.
	InterruptReplyTimeout = 10 * time.Second
	// interruptReplyMaxTokens caps the reply: one or two short sentences.
	interruptReplyMaxTokens = 150
)

// ReplyToInterrupt asks the model, after an interrupted turn, for a short
// message in the conversation's language that says where it stopped and asks
// what the user wants to change. The request is the current conversation plus
// the bundle's interrupt_reply reminder, on the session model, without
// thinking and with tool calls disabled. The reply is streamed (EventStream),
// appended to the history and the transcript as an assistant message and
// emitted as EventAssistant.
//
// It does nothing when Config.DisableInterruptReply is set, the bundle has no
// interrupt_reply reminder, the conversation does not end with a user message
// or a turn is running. A failed or cancelled request adds nothing (failures
// are logged). It reports whether a reply was added.
func (a *Agent) ReplyToInterrupt(ctx context.Context) bool {
	if a.cfg.DisableInterruptReply {
		return false
	}
	instruction := a.Bundle().Reminder("interrupt_reply", "")
	if instruction == "" {
		return false
	}
	a.mu.Lock()
	n := len(a.messages)
	ok := n > 0 && a.messages[n-1].Role == llm.RoleUser && a.cancel == nil
	a.mu.Unlock()
	if !ok {
		return false
	}

	req := a.buildRequest()
	last := &req.Messages[len(req.Messages)-1]
	last.Content = append(append([]llm.ContentBlock(nil), last.Content...), llm.TextBlock(instruction))
	req.MaxTokens = interruptReplyMaxTokens
	req.Thinking, req.Effort = nil, ""
	req.ToolChoice = &llm.ToolChoice{Type: "none"}
	req.Metadata.RequestClass = "auxiliary"

	resp, _, err := gateway.Send(ctx, a.cfg.Gateway, req, a.onStreamEvent, &gateway.RetryPolicy{})
	if err != nil {
		if ctx.Err() == nil {
			a.log().Warn("interrupt reply failed", "err", err)
		}
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	var text []llm.ContentBlock
	for _, b := range resp.Message.Content {
		if b.Type == llm.BlockText && b.Text != "" {
			text = append(text, b)
		}
	}
	if len(text) == 0 {
		a.log().Warn("interrupt reply was empty", "stop_reason", resp.StopReason)
		return false
	}

	a.mu.Lock()
	unchanged := len(a.messages) == n && a.cancel == nil
	a.mu.Unlock()
	if !unchanged {
		return false
	}
	resp.Message.Content = text
	a.appendMessage(llm.RoleAssistant, text)
	a.noteUsage(resp.Usage)
	id := a.record(session.Entry{Type: session.TypeAssistant, Message: session.AssistantMessage(resp), RequestID: resp.ID})
	msg := resp.Message
	a.emit(Event{Kind: EventAssistant, UUID: id, Message: &msg, Response: &resp})
	return true
}
