package agent

import (
	"context"

	"github.com/xlsft/harness/packages/client/gateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/usagelimit"
)

// ReminderUsageWrapUp is the reminder sent once per turn when the session's provider is nearly out of usage.
const ReminderUsageWrapUp = "usage_limit_wrap_up"

// ReminderUsageReturned is the reminder that continues a turn the usage limit cut off, once the limit is back.
const ReminderUsageReturned = "usage_limit_returned"

// UsageMonitor follows the usage limits of the session's provider (usagelimit.Monitor).
type UsageMonitor interface {
	// Check is called after each model response (err nil) and when a request failed for good (err set). It
	// returns the state to publish when it changed (nil otherwise) and the current state.
	Check(ctx context.Context, model string, err error) (*usagelimit.Status, usagelimit.Status)
}

// usageTurn is the usage-limit state of the running turn.
type usageTurn struct {
	// reminded: the wrap-up reminder was queued in this turn; delivered: it reached a model request.
	reminded, delivered bool
	current             usagelimit.Status
}

// startUsageTurn resets the per-turn state and drops a wrap-up reminder left over from an earlier turn (one
// queued right before an interruption): the new turn decides again.
func (a *Agent) startUsageTurn() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.usage.reminded, a.usage.delivered = false, false
	kept := a.reminders[:0]
	for _, r := range a.reminders {
		if r.id != ReminderUsageWrapUp {
			kept = append(kept, r)
		}
	}
	a.reminders = kept
}

// checkUsage asks the monitor about the provider after a model response (err nil) or a failed request.
// continuing says the turn goes on with another request: only then is the wrap-up reminder queued (once per
// turn), since a turn that ends now has nothing left to wrap up.
func (a *Agent) checkUsage(ctx context.Context, err error, continuing bool) {
	m := a.cfg.UsageMonitor
	if m == nil {
		return
	}
	changed, cur := m.Check(ctx, a.Model(), err)
	if changed != nil {
		a.publishUsage(*changed)
	}
	a.mu.Lock()
	a.usage.current = cur
	remind := continuing && cur.State == usagelimit.WrapUp && !a.usage.reminded
	if remind {
		a.usage.reminded = true
	}
	a.mu.Unlock()
	if remind {
		a.Remind(ReminderUsageWrapUp, usagelimit.ReminderVars(cur, nil))
	}
}

// finishUsageTurn publishes wrapped_up when the turn ended after the wrap-up reminder reached the model (not
// when the provider refused the turn in the end: REACHED says that).
func (a *Agent) finishUsageTurn() {
	a.mu.Lock()
	delivered, cur := a.usage.delivered, a.usage.current
	a.usage.reminded, a.usage.delivered = false, false
	a.mu.Unlock()
	if !delivered || cur.State == usagelimit.Reached {
		return
	}
	cur.WrappedUp = true
	a.publishUsage(cur)
}

// PublishUsage publishes a usage-limit state decided outside a turn (a reset, a model switch).
func (a *Agent) PublishUsage(s usagelimit.Status) { a.publishUsage(s) }

// probeMaxTokens caps the reply to ProbeModel; some providers refuse much smaller limits.
const probeMaxTokens = 16

// ProbeModel asks the session's model for a tiny reply, outside the conversation (no tools, nothing recorded, no
// retries), to learn whether the provider takes requests: nil when it does. It lets a held message go out only when
// it will be read, so a refusal leaves the message held instead of in the conversation.
func (a *Agent) ProbeModel(ctx context.Context) error {
	req := &llm.Request{
		Model:     a.Model(),
		MaxTokens: probeMaxTokens,
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("ping")}}},
		Stream:    true,
		Metadata:  &llm.Metadata{SessionID: a.cfg.SessionID, RequestClass: "auxiliary", PromptBundleVersion: a.Bundle().Version},
	}
	_, _, err := gateway.Send(ctx, a.cfg.Gateway, req, nil, &gateway.RetryPolicy{})
	return err
}

// publishUsage records a usage-limit state in the transcript (replayed on resume) and emits it.
func (a *Agent) publishUsage(s usagelimit.Status) {
	level := "warning"
	if s.State == usagelimit.Cleared {
		level = "info"
	}
	st := s
	a.record(session.Entry{Type: session.TypeSystem, Subtype: session.SubtypeUsageLimit, Level: level, UsageLimit: &st})
	a.emit(Event{Kind: EventUsageLimit, UsageLimit: &st})
}
