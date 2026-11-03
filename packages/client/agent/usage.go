package agent

import (
	"context"

	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/usagelimit"
)

// ReminderUsageWrapUp is the reminder sent once per turn when the session's provider is nearly out of usage.
const ReminderUsageWrapUp = "usage_limit_wrap_up"

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
