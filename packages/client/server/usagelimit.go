package server

import (
	"context"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/usagelimit"
)

// newUsageMonitor creates the usage-limit monitor of a harness session: the limits of the session model's
// provider come from the server's ProviderLimitsService with the user's credentials, so the server's
// visibility policies apply (hidden blocks never reach the monitor). Without a harness server only REACHED
// (from the provider's errors) is reported.
func (s *Server) newUsageMonitor() *usagelimit.Monitor {
	return &usagelimit.Monitor{Fetch: func(ctx context.Context, model string) (*usagelimit.Snapshot, error) {
		res, err := s.providerLimits(ctx, nil, model)
		if err != nil {
			return nil, err
		}
		return pbconv.LimitsSnapshot(res.GetModel()), nil
	}}
}

// lastUsageLimit is the last usage-limit state recorded in a transcript (nil when none).
func lastUsageLimit(entries []session.Entry) *agentv1.UsageLimit {
	for i := len(entries) - 1; i >= 0; i-- {
		if e := entries[i]; e.Type == session.TypeSystem && e.Subtype == session.SubtypeUsageLimit && e.UsageLimit != nil {
			return pbconv.UsageLimit(*e.UsageLimit)
		}
	}
	return nil
}

// onUsageLimit publishes a usage-limit state and keeps it for SessionInfo.usage_limit.
func (ls *liveSession) onUsageLimit(s usagelimit.Status) {
	u := pbconv.UsageLimit(s)
	if u == nil {
		return
	}
	ls.mu.Lock()
	ls.usageLimit = u
	ls.mu.Unlock()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_UsageLimit{UsageLimit: u}})
	ls.publishInfo()
}
