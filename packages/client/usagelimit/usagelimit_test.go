package usagelimit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/llm"
)

var now = time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC)

func f(v float64) *float64 { return &v }

func TestEvaluateWindows(t *testing.T) {
	cases := []struct {
		used float64
		want State
	}{{50, None}, {89.9, None}, {90, Near}, {96.9, Near}, {97, WrapUp}, {100, WrapUp}}
	for _, c := range cases {
		st := Evaluate(&Snapshot{ProviderID: "anthropic", ProviderName: "Anthropic", Windows: []Window{{Label: "5h", UsedPercent: c.used, ResetsAt: "2026-11-03T23:00:00Z"}}}, now)
		if st.State != c.want {
			t.Errorf("%v%%: got %q want %q", c.used, st.State, c.want)
		}
		if st.ProviderName != "Anthropic" {
			t.Errorf("provider lost: %+v", st)
		}
		if c.want != None && (st.WindowLabel != "5h" || st.ResetsAt != "2026-11-03T23:00:00Z" || st.UsedPercent != c.used) {
			t.Errorf("details: %+v", st)
		}
	}
	// The worst window wins; a window whose reset already passed is stale.
	st := Evaluate(&Snapshot{Windows: []Window{
		{Label: "5h", UsedPercent: 99, ResetsAt: "2026-11-03T19:00:00Z"},
		{Label: "weekly", UsedPercent: 92, ResetsAt: "2026-11-08T00:00:00Z"},
		{Label: "requests/min", UsedPercent: 91},
	}}, now)
	if st.State != Near || st.WindowLabel != "weekly" {
		t.Fatalf("got %+v", st)
	}
}

func TestEvaluateBalanceAndBudget(t *testing.T) {
	cases := []struct {
		name        string
		left, total *float64
		want        State
		text        string
	}{
		{"plenty", f(40), f(100), None, ""},
		{"5% of total", f(5), f(100), Near, "$5.00 left"},
		{"3% of total", f(3), f(100), WrapUp, "$3.00 left"},
		{"small amount, no total", f(0.8), nil, Near, "$0.80 left"},
		{"almost nothing", f(0.2), nil, WrapUp, "$0.20 left"},
		{"negative", f(-1), f(10), WrapUp, "$0.00 left"},
	}
	for _, c := range cases {
		st := Evaluate(&Snapshot{Balance: &Balance{Currency: "USD", Label: "credits", Left: c.left, Total: c.total}}, now)
		if st.State != c.want || st.BalanceText != c.text {
			t.Errorf("%s: got %+v", c.name, st)
		}
	}
	if st := Evaluate(&Snapshot{Balance: &Balance{Currency: "CNY", Left: f(0.5)}}, now); st.BalanceText != "0.50 CNY left" || st.WindowLabel != "credits" {
		t.Errorf("CNY: %+v", st)
	}
	st := Evaluate(&Snapshot{Spend: &Spend{Spent: 91, Budget: f(100), PeriodStart: "2026-11-01T00:00:00Z"}}, now)
	if st.State != Near || st.WindowLabel != "monthly budget" || st.ResetsAt != "2026-12-01T00:00:00Z" || st.BalanceText != "$9.00 left" {
		t.Fatalf("budget: %+v", st)
	}
	if st := Evaluate(&Snapshot{Spend: &Spend{Spent: 98, Budget: f(100)}}, now); st.State != WrapUp {
		t.Fatalf("budget wrap-up: %+v", st)
	}
	if st := Evaluate(&Snapshot{Spend: &Spend{Spent: 500}}, now); st.State != None {
		t.Fatalf("no budget: %+v", st)
	}
}

func TestTracker(t *testing.T) {
	var tr Tracker
	near := Status{State: Near, ProviderID: "p", WindowLabel: "5h", UsedPercent: 91}
	steps := []struct {
		in   Status
		want State // "" = nothing published
	}{
		{Status{ProviderID: "p"}, ""},
		{near, Near},
		{Status{State: Near, ProviderID: "p", WindowLabel: "5h", UsedPercent: 93}, ""},
		{Status{State: Near, ProviderID: "p", WindowLabel: "weekly", UsedPercent: 90}, Near},
		{Status{State: WrapUp, ProviderID: "p", WindowLabel: "weekly", UsedPercent: 98}, WrapUp},
		{Status{State: Reached, ProviderID: "p"}, Reached},
		{Status{ProviderID: "p"}, Cleared},
		{Status{ProviderID: "p"}, ""},
		{near, Near},
	}
	for i, s := range steps {
		ev := tr.Observe(s.in)
		got := State("")
		if ev != nil {
			got = ev.State
		}
		if got != s.want {
			t.Fatalf("step %d: got %q want %q", i, got, s.want)
		}
	}
	// Seeding from a resumed transcript: the same state is not published again.
	var tr2 Tracker
	tr2.Seed(Status{State: Near, ProviderID: "p", WindowLabel: "5h", WrappedUp: true})
	if ev := tr2.Observe(near); ev != nil {
		t.Fatalf("seeded state republished: %+v", ev)
	}
	if ev := tr2.Observe(Status{ProviderID: "p"}); ev == nil || ev.State != Cleared || ev.ProviderID != "p" {
		t.Fatalf("clear after seed: %+v", ev)
	}
}

func TestIsReached(t *testing.T) {
	yes := []error{
		&llm.APIError{Type: "rate_limit", Status: 429, Message: "slow down"},
		&llm.APIError{Type: "quota_exceeded", Status: 429, Message: "monthly quota"},
		&llm.APIError{Type: "upstream", Status: 402, Message: "payment required"},
		&llm.APIError{Type: "upstream", Message: "Your credit balance is too low to access the API"},
		&llm.APIError{Type: "upstream", Message: "Upstream rejected credentials (402): nope"},
		fmt.Errorf("wrapped: %w", &llm.APIError{Type: "internal", Message: "insufficient_quota"}),
		errors.New("You've hit your usage limit. Try again at 11:00 PM"),
	}
	for _, err := range yes {
		if !IsReached(err) {
			t.Errorf("want reached: %v", err)
		}
	}
	no := []error{
		nil,
		&llm.APIError{Type: "upstream", Status: 401, Message: "Upstream rejected credentials (401): invalid x-api-key"},
		&llm.APIError{Type: "invalid_request", Status: 400, Message: "prompt mentions a quota"},
		&llm.APIError{Type: "overloaded", Status: 529, Message: "Overloaded"},
		errors.New("connection reset"),
	}
	for _, err := range no {
		if IsReached(err) {
			t.Errorf("want not reached: %v", err)
		}
	}
}

func TestMonitorCachesAndReportsChanges(t *testing.T) {
	clock := now
	calls := 0
	used := 91.0
	m := &Monitor{Now: func() time.Time { return clock }, Fetch: func(_ context.Context, model string) (*Snapshot, error) {
		calls++
		if model != "claude-x" {
			t.Errorf("model %q", model)
		}
		return &Snapshot{ProviderID: "anthropic", ProviderName: "Anthropic", Windows: []Window{{Label: "5h", UsedPercent: used, ResetsAt: "2026-11-03T23:00:00Z"}}}, nil
	}}
	ctx := context.Background()
	ev, cur := m.Check(ctx, "claude-x", nil)
	if ev == nil || ev.State != Near || cur.State != Near {
		t.Fatalf("first check: %+v %+v", ev, cur)
	}
	used = 99
	if ev, cur = m.Check(ctx, "claude-x", nil); ev != nil || cur.State != Near || calls != 1 {
		t.Fatalf("within the TTL the cached snapshot is used: %+v %+v calls=%d", ev, cur, calls)
	}
	clock = clock.Add(31 * time.Second)
	if ev, cur = m.Check(ctx, "claude-x", nil); ev == nil || ev.State != WrapUp || calls != 2 {
		t.Fatalf("after the TTL: %+v calls=%d", ev, calls)
	}
	// The provider refuses: REACHED, with the numbers the user could see.
	ev, _ = m.Check(ctx, "claude-x", &llm.APIError{Type: "rate_limit", Status: 429})
	if ev == nil || ev.State != Reached || ev.WindowLabel != "5h" || ev.UsedPercent != 99 || ev.ProviderName != "Anthropic" {
		t.Fatalf("reached: %+v", ev)
	}
	// Other errors change nothing.
	if ev, _ = m.Check(ctx, "claude-x", errors.New("connection reset")); ev != nil {
		t.Fatalf("unrelated error published %+v", ev)
	}
	// The window reset: past its reset time the snapshot no longer counts, so the state clears.
	clock = time.Date(2026, 11, 3, 23, 1, 0, 0, time.UTC)
	if ev, _ = m.Check(ctx, "claude-x", nil); ev == nil || ev.State != Cleared {
		t.Fatalf("after the reset: %+v", ev)
	}
}

func TestMonitorHiddenLimitsGiveNoNumbers(t *testing.T) {
	// The server hid the shared provider's limits from this user: the snapshot has the provider but no blocks.
	m := &Monitor{Fetch: func(context.Context, string) (*Snapshot, error) {
		return &Snapshot{ProviderID: "shared", ProviderName: "Team OpenAI"}, nil
	}}
	ctx := context.Background()
	if ev, cur := m.Check(ctx, "gpt", nil); ev != nil || cur.State != None {
		t.Fatalf("hidden limits produced %+v / %+v", ev, cur)
	}
	ev, _ := m.Check(ctx, "gpt", &llm.APIError{Type: "quota_exceeded", Status: 429, Message: "quota"})
	if ev == nil || ev.State != Reached || ev.ProviderName != "Team OpenAI" {
		t.Fatalf("reached: %+v", ev)
	}
	if ev.WindowLabel != "" || ev.UsedPercent != 0 || ev.ResetsAt != "" || ev.BalanceText != "" {
		t.Fatalf("numbers leaked: %+v", ev)
	}
	// A successful response ends REACHED.
	if ev, _ := m.Check(ctx, "gpt", nil); ev == nil || ev.State != Cleared {
		t.Fatalf("after success: %+v", ev)
	}
}

func TestMonitorUnknownKeepsState(t *testing.T) {
	fail := false
	m := &Monitor{TTL: time.Nanosecond, Fetch: func(context.Context, string) (*Snapshot, error) {
		if fail {
			return nil, errors.New("server down")
		}
		return &Snapshot{ProviderID: "p", Windows: []Window{{Label: "5h", UsedPercent: 95}}}, nil
	}}
	ctx := context.Background()
	m.Check(ctx, "m", nil)
	fail = true
	if ev, cur := m.Check(ctx, "m", nil); ev != nil || cur.State != Near {
		t.Fatalf("a failed fetch keeps the last known snapshot: %+v %+v", ev, cur)
	}
	// Another model with nothing known: no change published.
	if ev, _ := m.Check(ctx, "other", nil); ev != nil {
		t.Fatalf("unknown snapshot published %+v", ev)
	}
}

func TestReminderVars(t *testing.T) {
	v := ReminderVars(Status{State: WrapUp, ProviderName: "Anthropic", WindowLabel: "5h", UsedPercent: 98.4, ResetsAt: "2026-11-03T23:00:00Z"}, time.UTC)
	if v["provider"] != "Anthropic" || v["detail"] != "5h: 98% used, resets Tue 11:00 PM UTC" || v["resets"] != "the limit resets at Tue 11:00 PM UTC" {
		t.Fatalf("vars: %v", v)
	}
	v = ReminderVars(Status{State: WrapUp, BalanceText: "$0.20 left", WindowLabel: "credits"}, time.UTC)
	if v["provider"] != "the model provider" || v["detail"] != "credits, $0.20 left" || v["resets"] != "the limit resets" {
		t.Fatalf("vars: %v", v)
	}
	for _, ph := range []string{"{{provider}}", "{{detail}}", "{{resets}}"} {
		if !strings.Contains(WrapUpFallback, ph) {
			t.Fatalf("fallback lacks %s", ph)
		}
	}
}
