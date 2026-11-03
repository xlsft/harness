// Package usagelimit decides when the provider behind a session is about to run out (harness.agent.v1.UsageLimit):
// thresholds over a provider's limit snapshot (rate-limit windows, prepaid balance, monthly budget), REACHED from
// the provider's refusal, the state machine that publishes only changes, and the per-session Monitor that caches
// the server's snapshot.
//
// Everything here runs in the user's own client on what the server lets that user see: blocks hidden by the
// server's visibility policies are absent from the snapshot, so they never produce NEAR / WRAP_UP or numbers.
package usagelimit

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/xlsft/harness/packages/client/llm"
)

// State of a provider's usage.
type State string

const (
	// None: under the thresholds (or nothing known).
	None    State = ""
	Near    State = "near"
	WrapUp  State = "wrap_up"
	Reached State = "reached"
	// Cleared is published when the state goes back to None after a reported state.
	Cleared State = "cleared"
)

// Thresholds.
const (
	// NearPercent / WrapUpPercent: share of a rate-limit window or of the monthly budget used.
	NearPercent   = 90.0
	WrapUpPercent = 97.0
	// NearBalanceShare / WrapUpBalanceShare: share of the balance's total still left.
	NearBalanceShare   = 0.05
	WrapUpBalanceShare = 0.03
	// NearBalanceLeft / WrapUpBalanceLeft: absolute balance left (in the balance's currency).
	NearBalanceLeft   = 1.0
	WrapUpBalanceLeft = 0.25
)

// Status is one usage-limit state of a provider (the fields of harness.agent.v1.UsageLimit).
type Status struct {
	State        State   `json:"state,omitempty"`
	ProviderID   string  `json:"providerId,omitempty"`
	ProviderName string  `json:"providerName,omitempty"`
	WindowLabel  string  `json:"windowLabel,omitempty"`
	UsedPercent  float64 `json:"usedPercent,omitempty"`
	// ResetsAt is RFC 3339, UTC ("" when unknown).
	ResetsAt    string `json:"resetsAt,omitempty"`
	BalanceText string `json:"balanceText,omitempty"`
	WrappedUp   bool   `json:"wrappedUp,omitempty"`
	// Interrupted: the provider's refusal cut the last turn off and nothing has continued it yet.
	Interrupted bool `json:"interrupted,omitempty"`
}

// withoutNumbers keeps the provider and state only.
func (s Status) withoutNumbers() Status {
	return Status{State: s.State, ProviderID: s.ProviderID, ProviderName: s.ProviderName, WrappedUp: s.WrappedUp}
}

// Window is one rate-limit window.
type Window struct {
	Label       string
	UsedPercent float64
	// ResetsAt is RFC 3339 ("" when unknown).
	ResetsAt string
}

// Balance is prepaid credit or a key limit.
type Balance struct {
	Currency string
	Label    string
	// Left is what is left to spend; Total the cap it is part of (nil when unknown).
	Left, Total *float64
}

// Spend is this month's spend against an optional budget.
type Spend struct {
	Spent  float64
	Budget *float64
	// PeriodStart is RFC 3339 (the first of the month, UTC).
	PeriodStart string
}

// Snapshot is what the user may see of a provider's limits.
type Snapshot struct {
	ProviderID, ProviderName string
	Windows                  []Window
	Balance                  *Balance
	Spend                    *Spend
}

// Visible reports whether the snapshot has a block that can tell whether the provider is out (nil, or with every
// block hidden by the server's policies: it cannot).
func (s *Snapshot) Visible() bool {
	if s == nil {
		return false
	}
	return len(s.Windows) > 0 || (s.Balance != nil && s.Balance.Left != nil) || (s.Spend != nil && s.Spend.Budget != nil)
}

// Exhausted returns the Reached state of a snapshot with a block that is fully used: no balance left (no reset
// time), the monthly budget spent, or a rate-limit window at 100% that has not reset (the one that resets last, as
// the provider stays out until then). Windows whose reset time has passed are ignored, like in Evaluate.
func Exhausted(s *Snapshot, now time.Time) (Status, bool) {
	if s == nil {
		return Status{}, false
	}
	reached := Status{State: Reached, ProviderID: s.ProviderID, ProviderName: s.ProviderName}
	if b := s.Balance; b != nil && b.Left != nil && *b.Left <= 0 {
		label := b.Label
		if label == "" {
			label = "credits"
		}
		reached.WindowLabel, reached.BalanceText = label, Money(0, b.Currency)+" left"
		return reached, true
	}
	if sp := s.Spend; sp != nil && sp.Budget != nil && *sp.Budget > 0 && sp.Spent >= *sp.Budget {
		reached.WindowLabel, reached.UsedPercent, reached.ResetsAt, reached.BalanceText = "monthly budget", 100, nextMonth(sp.PeriodStart, now), Money(0, "USD")+" left"
		return reached, true
	}
	found := false
	for _, w := range s.Windows {
		if w.UsedPercent < 100 {
			continue
		}
		t, err := time.Parse(time.RFC3339, w.ResetsAt)
		if err == nil && !t.After(now) {
			continue
		}
		if !found || (err == nil && utc(w.ResetsAt) > reached.ResetsAt) {
			found = true
			reached.WindowLabel, reached.UsedPercent, reached.ResetsAt = w.Label, clampPct(w.UsedPercent), utc(w.ResetsAt)
		}
	}
	return reached, found
}

func rank(s State) int {
	switch s {
	case WrapUp:
		return 2
	case Near:
		return 1
	}
	return 0
}

func percentState(p float64) State {
	switch {
	case p >= WrapUpPercent:
		return WrapUp
	case p >= NearPercent:
		return Near
	}
	return None
}

// Evaluate returns the worst state of a snapshot (None with the provider when every block is under the
// thresholds or hidden). Windows whose reset time has passed are ignored: the snapshot predates the reset.
func Evaluate(s *Snapshot, now time.Time) Status {
	if s == nil {
		return Status{}
	}
	best := Status{ProviderID: s.ProviderID, ProviderName: s.ProviderName}
	consider := func(c Status) {
		if rank(c.State) > rank(best.State) || (rank(c.State) == rank(best.State) && c.State != None && c.UsedPercent > best.UsedPercent) {
			c.ProviderID, c.ProviderName = s.ProviderID, s.ProviderName
			best = c
		}
	}
	for _, w := range s.Windows {
		if t, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil && !t.After(now) {
			continue
		}
		consider(Status{State: percentState(w.UsedPercent), WindowLabel: w.Label, UsedPercent: clampPct(w.UsedPercent), ResetsAt: utc(w.ResetsAt)})
	}
	if b := s.Balance; b != nil && b.Left != nil {
		left := *b.Left
		st, used := None, 0.0
		if b.Total != nil && *b.Total > 0 {
			share := left / *b.Total
			used = clampPct((1 - share) * 100)
			switch {
			case share <= WrapUpBalanceShare:
				st = WrapUp
			case share <= NearBalanceShare:
				st = Near
			}
		}
		switch {
		case left <= WrapUpBalanceLeft:
			st = WrapUp
		case left <= NearBalanceLeft && st == None:
			st = Near
		}
		label := b.Label
		if label == "" {
			label = "credits"
		}
		consider(Status{State: st, WindowLabel: label, UsedPercent: used, BalanceText: Money(math.Max(left, 0), b.Currency) + " left"})
	}
	if sp := s.Spend; sp != nil && sp.Budget != nil && *sp.Budget > 0 {
		pct := clampPct(sp.Spent / *sp.Budget * 100)
		consider(Status{State: percentState(pct), WindowLabel: "monthly budget", UsedPercent: pct, ResetsAt: nextMonth(sp.PeriodStart, now),
			BalanceText: Money(math.Max(*sp.Budget-sp.Spent, 0), "USD") + " left"})
	}
	if best.State == None {
		return Status{ProviderID: s.ProviderID, ProviderName: s.ProviderName}
	}
	return best
}

func clampPct(p float64) float64 {
	if math.IsNaN(p) {
		return 0
	}
	return math.Max(0, math.Min(100, p))
}

func utc(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

// nextMonth is the start of the month after periodStart (the current month when it is not parseable).
func nextMonth(periodStart string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, periodStart)
	if err != nil {
		t = now
	}
	t = t.UTC()
	return time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

// Money formats an amount: "$0.40" for USD, "0.40 CNY" otherwise.
func Money(v float64, currency string) string {
	c := strings.ToUpper(strings.TrimSpace(currency))
	if c == "" || c == "USD" {
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("%.2f %s", v, c)
}

// reachedRe matches provider messages about an exhausted quota, credit or rate limit. "credits?" is a whole
// word so "credentials" does not match.
var reachedRe = regexp.MustCompile(`(?i)insufficient[_ ]quota|quota|\bcredits?\b|credit balance|rate[_ -]?limit|usage limit|too many requests|billing|spend(ing)? limit|budget exceeded|out of tokens|rejected credentials \(402\)`)

// IsReached reports whether a request failed because the provider refused it for limits: HTTP 429 or 402, the
// gateway's rate_limit / quota_exceeded classes, or a provider message about quota, credit or rate limits.
func IsReached(err error) bool {
	if err == nil {
		return false
	}
	var ae *llm.APIError
	if errors.As(err, &ae) {
		switch ae.Type {
		case "rate_limit", "quota_exceeded", "insufficient_quota", "billing_error", "rate_limit_error":
			return true
		case "invalid_request":
			// A request error never means the account ran out, whatever its message says.
			return false
		}
		if ae.Status == 429 || ae.Status == 402 {
			return true
		}
		return reachedRe.MatchString(ae.Message)
	}
	return reachedRe.MatchString(err.Error())
}

// Tracker remembers the last published state and reports changes.
type Tracker struct {
	last Status
}

// Seed restores the last published state (a resumed session).
func (t *Tracker) Seed(s Status) {
	s.WrappedUp = false
	t.last = s
}

// Last is the last published state (State None when nothing was published, or after Cleared it is Cleared).
func (t *Tracker) Last() Status { return t.last }

// Observe records a new state and returns the event to publish when it differs from the last one: another
// state, provider or window. Back under the thresholds after a reported state publishes Cleared.
func (t *Tracker) Observe(s Status) *Status {
	s.WrappedUp = false
	prev := t.last
	if s.State == None {
		if prev.State == None || prev.State == Cleared {
			return nil
		}
		ev := Status{State: Cleared, ProviderID: s.ProviderID, ProviderName: s.ProviderName, Interrupted: s.Interrupted}
		if ev.ProviderID == "" && ev.ProviderName == "" {
			ev.ProviderID, ev.ProviderName = prev.ProviderID, prev.ProviderName
		}
		t.last = ev
		return &ev
	}
	t.last = s
	if s.State == prev.State && s.ProviderID == prev.ProviderID && s.WindowLabel == prev.WindowLabel && s.Interrupted == prev.Interrupted {
		return nil
	}
	return &s
}

// Set records a state as published, whatever the last one was.
func (t *Tracker) Set(s Status) {
	s.WrappedUp = false
	t.last = s
}

// ReminderVars are the placeholders of the usage_limit_wrap_up reminder for a status.
func ReminderVars(s Status, loc *time.Location) map[string]string {
	provider := s.ProviderName
	if provider == "" {
		provider = s.ProviderID
	}
	if provider == "" {
		provider = "the model provider"
	}
	var parts []string
	if s.WindowLabel != "" {
		what := s.WindowLabel
		if s.UsedPercent > 0 {
			what = fmt.Sprintf("%s: %.0f%% used", s.WindowLabel, s.UsedPercent)
		}
		parts = append(parts, what)
	}
	if s.BalanceText != "" {
		parts = append(parts, s.BalanceText)
	}
	resets := "the limit resets"
	if t, err := time.Parse(time.RFC3339, s.ResetsAt); err == nil {
		if loc == nil {
			loc = time.Local
		}
		resets = "the limit resets at " + t.In(loc).Format("Mon 3:04 PM MST")
		parts = append(parts, "resets "+t.In(loc).Format("Mon 3:04 PM MST"))
	}
	detail := strings.Join(parts, ", ")
	if detail == "" {
		detail = "no further detail available"
	}
	return map[string]string{"provider": provider, "detail": detail, "resets": resets}
}

// WrapUpFallback is the usage_limit_wrap_up reminder used when the prompt bundle has none.
const WrapUpFallback = `<system-reminder>
The user's usage on {{provider}} is nearly used up ({{detail}}). Requests may start being refused soon, and the rest of this turn could be cut off. Bring the turn to an orderly end: finish the step you are on, but do not begin new large pieces of work or long explorations. Leave the code and any files in a consistent state, with no half-applied edits. Then stop and give the user a short summary of what is done and what remains, so they can continue after {{resets}}. You may say briefly that you are stopping because of the usage limit.
</system-reminder>`

// ReturnedVars are the placeholders of the usage_limit_returned reminder for the state that cut the turn off.
func ReturnedVars(s Status) map[string]string {
	provider := s.ProviderName
	if provider == "" {
		provider = s.ProviderID
	}
	if provider == "" {
		provider = "the model provider"
	}
	return map[string]string{"provider": provider}
}

// ReturnedFallback is the usage_limit_returned reminder used when the prompt bundle has none.
const ReturnedFallback = `<system-reminder>
The usage limit on {{provider}} that cut off the previous turn has lifted, and requests are going through again. Pick up the interrupted work from the point where it stopped: look at what was already done before repeating anything, and carry on from there. A user message next to this reminder was sent while the limit was in force, so take it into account. If nothing was left unfinished, say so in one sentence and stop.
</system-reminder>`
