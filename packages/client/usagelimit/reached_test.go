package usagelimit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/llm"
)

func TestExhausted(t *testing.T) {
	fut, past := "2026-11-03T23:00:00Z", "2026-11-03T19:00:00Z"
	if _, ok := Exhausted(&Snapshot{Windows: []Window{{Label: "5h", UsedPercent: 99, ResetsAt: fut}}}, now); ok {
		t.Error("99% is not out")
	}
	if _, ok := Exhausted(&Snapshot{Windows: []Window{{Label: "5h", UsedPercent: 100, ResetsAt: past}}}, now); ok {
		t.Error("a window that has reset is not out")
	}
	later := "2026-11-05T00:00:00Z"
	st, ok := Exhausted(&Snapshot{ProviderID: "a", Windows: []Window{{Label: "5h", UsedPercent: 100, ResetsAt: fut}, {Label: "weekly", UsedPercent: 100, ResetsAt: later}}}, now)
	if !ok || st.State != Reached || st.WindowLabel != "weekly" || st.ResetsAt != later || st.ProviderID != "a" {
		t.Errorf("the window that resets last: %+v %v", st, ok)
	}
	st, ok = Exhausted(&Snapshot{Balance: &Balance{Left: f(0), Currency: "USD"}}, now)
	if !ok || st.ResetsAt != "" || st.BalanceText != "$0.00 left" {
		t.Errorf("no credit: %+v %v", st, ok)
	}
	st, ok = Exhausted(&Snapshot{Spend: &Spend{Spent: 10, Budget: f(10), PeriodStart: "2026-11-01T00:00:00Z"}}, now)
	if !ok || st.ResetsAt != "2026-12-01T00:00:00Z" {
		t.Errorf("budget spent: %+v %v", st, ok)
	}
}

func TestMonitorRecheck(t *testing.T) {
	var snap *Snapshot
	var fail error
	m := &Monitor{Now: func() time.Time { return now }, Fetch: func(context.Context, string) (*Snapshot, error) { return snap, fail }}
	ctx := context.Background()
	if r := m.Recheck(ctx, "m"); r.Known {
		t.Errorf("no snapshot: %+v", r)
	}
	snap = &Snapshot{ProviderID: "team", ProviderName: "Team"}
	if r := m.Recheck(ctx, "m"); r.Known || r.Status.ProviderID != "team" || r.Status.State != None {
		t.Errorf("hidden limits: %+v", r)
	}
	snap = &Snapshot{ProviderID: "a", Windows: []Window{{Label: "5h", UsedPercent: 100, ResetsAt: "2026-11-03T23:00:00Z"}}}
	if r := m.Recheck(ctx, "m"); !r.Known || r.Status.State != Reached {
		t.Errorf("out: %+v", r)
	}
	snap = &Snapshot{ProviderID: "a", Windows: []Window{{Label: "5h", UsedPercent: 10, ResetsAt: "2026-11-03T23:00:00Z"}}}
	if r := m.Recheck(ctx, "m"); !r.Known || r.Status.State != None {
		t.Errorf("fine: %+v", r)
	}
	fail = errors.New("offline")
	if r := m.Recheck(ctx, "m"); r.Known {
		t.Errorf("failed fetch: %+v", r)
	}
}

func TestMonitorReachedFromSnapshotOnFirstRefusal(t *testing.T) {
	// The very first request is refused: there is no earlier evaluation, the snapshot still gives the reset time.
	m := &Monitor{Now: func() time.Time { return now }, Fetch: func(context.Context, string) (*Snapshot, error) {
		return &Snapshot{ProviderID: "a", Windows: []Window{{Label: "5h", UsedPercent: 100, ResetsAt: "2026-11-03T23:00:00Z"}}}, nil
	}}
	ev, cur := m.Check(context.Background(), "m", &llm.APIError{Type: "rate_limit", Status: 429})
	if ev == nil || cur.State != Reached || !cur.Interrupted || cur.ResetsAt != "2026-11-03T23:00:00Z" || cur.WindowLabel != "5h" {
		t.Fatalf("%+v %+v", ev, cur)
	}
	m.TurnContinues()
	if m.Current().Interrupted {
		t.Fatal("still interrupted")
	}
	// Cleared keeps saying the turn was cut off when it is told to.
	if ev := m.Observe(Status{ProviderID: "a", Interrupted: true}); ev == nil || ev.State != Cleared || !ev.Interrupted {
		t.Fatalf("cleared: %+v", ev)
	}
}
