package agent

import (
	"fmt"
	"strings"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
)

// noteAutoDecision turns an auto-mode verdict into an AutoDecision and emits
// the auto_decision Notice for frontends.
func (a *Agent) noteAutoDecision(u llm.ContentBlock, d permissions.Decision) *AutoDecision {
	r := d.Auto
	ad := &AutoDecision{
		ToolUseID: u.ID, ToolName: u.Name, Decision: string(r.Behavior), Provider: r.Provider, Reason: r.Reason,
		Categories: append([]string(nil), r.Categories...), Cached: r.Cached, Confidence: r.Confidence,
		LatencyMs: r.LatencyMs, CostUSD: r.CostUSD,
	}
	reason := strings.TrimSpace(r.Reason)
	level, text := "info", ""
	switch {
	case r.Behavior == permissions.Allow:
		text = fmt.Sprintf("Auto-approved by %s: %s", r.Provider, reason)
	case r.Behavior == permissions.Deny:
		level = "warning"
		text = fmt.Sprintf("Auto mode denied %s (%s): %s", u.Name, r.Provider, reason)
	case d.ResolvedBy == "auto":
		level = "warning"
		text = fmt.Sprintf("Auto mode could not approve %s and nobody can be asked (%s): %s", u.Name, r.Provider, reason)
	default:
		text = fmt.Sprintf("Auto mode deferred %s to you (%s): %s", u.Name, r.Provider, reason)
	}
	a.emit(Event{Kind: EventNotice, ToolUseID: u.ID, ToolName: u.Name, Notice: &Notice{Level: level, Kind: NoticeAutoDecision, Text: text, Auto: ad}})
	return ad
}
