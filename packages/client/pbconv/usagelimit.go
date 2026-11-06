package pbconv

import (
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	"github.com/xlsft/harness/packages/client/usagelimit"
)

var usageStates = map[usagelimit.State]agentv1.UsageLimit_State{
	usagelimit.Near: agentv1.UsageLimit_STATE_NEAR, usagelimit.WrapUp: agentv1.UsageLimit_STATE_WRAP_UP,
	usagelimit.Reached: agentv1.UsageLimit_STATE_REACHED, usagelimit.Cleared: agentv1.UsageLimit_STATE_CLEARED,
}

// UsageLimit converts a usage-limit state (nil for State None).
func UsageLimit(s usagelimit.Status) *agentv1.UsageLimit {
	st, ok := usageStates[s.State]
	if !ok {
		return nil
	}
	return &agentv1.UsageLimit{State: st, ProviderId: s.ProviderID, ProviderName: s.ProviderName, WindowLabel: s.WindowLabel,
		UsedPercent: s.UsedPercent, ResetsAt: s.ResetsAt, BalanceText: s.BalanceText, WrappedUp: s.WrappedUp, Interrupted: s.Interrupted}
}

// FromUsageLimit converts a usage-limit event back (State None for nil).
func FromUsageLimit(u *agentv1.UsageLimit) usagelimit.Status {
	if u == nil {
		return usagelimit.Status{}
	}
	var st usagelimit.State
	for k, v := range usageStates {
		if v == u.GetState() {
			st = k
		}
	}
	return usagelimit.Status{State: st, ProviderID: u.GetProviderId(), ProviderName: u.GetProviderName(), WindowLabel: u.GetWindowLabel(),
		UsedPercent: u.GetUsedPercent(), ResetsAt: u.GetResetsAt(), BalanceText: u.GetBalanceText(), WrappedUp: u.GetWrappedUp(), Interrupted: u.GetInterrupted()}
}

// LimitsSnapshot converts what the server returned about a provider's limits (nil stays nil). Blocks the server
// hid from the user are absent, so they stay absent here. Rate limits count like a plan's windows, though no screen
// shows them. An estimated balance does not count: it is someone's number minus our own records, so it can say nothing
// about whether the provider will refuse.
func LimitsSnapshot(p *llmv1.ProviderLimits) *usagelimit.Snapshot {
	if p == nil {
		return nil
	}
	s := &usagelimit.Snapshot{ProviderID: p.GetProviderId(), ProviderName: p.GetProviderName()}
	for _, windows := range [][]*llmv1.LimitWindow{p.GetWindows(), p.GetRateWindows()} {
		for _, w := range windows {
			s.Windows = append(s.Windows, usagelimit.Window{Label: w.GetLabel(), UsedPercent: w.GetUsedPercent(), ResetsAt: w.GetResetsAt()})
		}
	}
	if b := p.GetBalance(); b != nil && b.GetError() == "" && b.Balance != nil && !b.GetEstimated() {
		bal := &usagelimit.Balance{Currency: b.GetCurrency(), Label: b.GetLabel()}
		left := b.GetBalance()
		bal.Left = &left
		if b.Total != nil {
			total := b.GetTotal()
			bal.Total = &total
		}
		s.Balance = bal
	}
	if sp := p.GetSpend(); sp != nil {
		spend := &usagelimit.Spend{Spent: sp.GetSpent(), PeriodStart: sp.GetPeriodStart()}
		if sp.Budget != nil {
			b := sp.GetBudget()
			spend.Budget = &b
		}
		s.Spend = spend
	}
	return s
}
