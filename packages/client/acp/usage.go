package acp

import (
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/usagelimit"
)

// Usage limits of an ACP session come from the subscription limits its agent reports (the user's own plan, so
// the numbers are theirs to see) and from the agent's rate-limit errors. The agent runs its own loop: the
// session only publishes UsageLimit events, it never asks the agent to wrap up.

// evaluateUsageLocked rates the agent's latest limits (s.mu held).
func (s *Session) evaluateUsageLocked() usagelimit.Status {
	snap := &usagelimit.Snapshot{ProviderID: s.cfg.AgentID, ProviderName: s.cfg.AgentName}
	if l := s.limits; l != nil {
		for _, w := range l.Windows {
			resets := ""
			if !w.ResetsAt.IsZero() {
				resets = w.ResetsAt.UTC().Format(time.RFC3339)
			}
			snap.Windows = append(snap.Windows, usagelimit.Window{Label: w.Label, UsedPercent: w.UsedPercent, ResetsAt: resets})
		}
	}
	return usagelimit.Evaluate(snap, s.now())
}

// observeUsageLocked publishes st when the state changed (s.mu held).
func (s *Session) observeUsageLocked(st usagelimit.Status) {
	ev := s.usage.Observe(st)
	if ev == nil {
		return
	}
	u := pbconv.UsageLimit(*ev)
	if u == nil {
		return
	}
	s.usageLimit = u
	s.emitLocked(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_UsageLimit{UsageLimit: u}}, false)
	s.changedLocked()
}

// turnUsageLocked runs when a turn ends (s.mu held): a refusal for limits is REACHED (with the numbers of the
// latest limits, when they explain it); a turn that went through after REACHED rates the limits again.
func (s *Session) turnUsageLocked(err error, cancelled bool) {
	switch {
	case err != nil && !cancelled && usagelimit.IsReached(err):
		st := s.evaluateUsageLocked()
		if st.State == usagelimit.None {
			st = usagelimit.Status{ProviderID: s.cfg.AgentID, ProviderName: s.cfg.AgentName}
		}
		st.State = usagelimit.Reached
		s.observeUsageLocked(st)
	case err == nil && !cancelled && s.usage.Last().State == usagelimit.Reached:
		s.observeUsageLocked(s.evaluateUsageLocked())
	}
}
