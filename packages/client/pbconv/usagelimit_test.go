package pbconv

import (
	"context"
	"testing"
	"time"

	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/usagelimit"
)

var (
	limitsNow = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	inAMinute = limitsNow.Add(time.Minute).Format(time.RFC3339)
)

// The server shows plan windows only and sends rate limits apart: the watch sees both, as it did when they were one list.
func TestLimitsSnapshotKeepsRateLimitsForTheWatch(t *testing.T) {
	s := LimitsSnapshot(&llmv1.ProviderLimits{
		ProviderId:  "a",
		Windows:     []*llmv1.LimitWindow{{Label: "5h", UsedPercent: 10, ResetsAt: inAMinute}},
		RateWindows: []*llmv1.LimitWindow{{Label: "tokens/min", UsedPercent: 98, ResetsAt: inAMinute}},
	})
	if len(s.Windows) != 2 {
		t.Fatalf("windows: %+v", s.Windows)
	}
	st := usagelimit.Evaluate(s, limitsNow)
	if st.State != usagelimit.WrapUp || st.WindowLabel != "tokens/min" {
		t.Errorf("a rate limit near its end still warns: %+v", st)
	}
}

// A provider refusing with 429 on a per-minute limit reads exactly as before: REACHED, with that window and its reset.
func TestRefusalIsExplainedByARateLimit(t *testing.T) {
	m := &usagelimit.Monitor{Now: func() time.Time { return limitsNow }, Fetch: func(context.Context, string) (*usagelimit.Snapshot, error) {
		return LimitsSnapshot(&llmv1.ProviderLimits{ProviderId: "a", RateWindows: []*llmv1.LimitWindow{{Label: "requests/min", UsedPercent: 100, ResetsAt: inAMinute}}}), nil
	}}
	_, cur := m.Check(context.Background(), "m", &llm.APIError{Type: "rate_limit", Status: 429})
	if cur.State != usagelimit.Reached || !cur.Interrupted || cur.WindowLabel != "requests/min" || cur.ResetsAt != inAMinute {
		t.Errorf("%+v", cur)
	}
	if r := m.Recheck(context.Background(), "m"); !r.Known || r.Status.State != usagelimit.Reached {
		t.Errorf("recheck: %+v", r)
	}
}

// An estimate at or below zero, or nearly there, neither warns nor holds anything: only the provider can say it is out.
func TestEstimatedBalanceTellsTheWatchNothing(t *testing.T) {
	left, total := -1.5, 100.0
	p := &llmv1.ProviderLimits{ProviderId: "a", Balance: &llmv1.ProviderBalance{Currency: "USD", Balance: &left, Total: &total, Label: "estimate", Estimated: true}}
	s := LimitsSnapshot(p)
	if s.Balance != nil || s.Visible() {
		t.Fatalf("%+v", s)
	}
	if st := usagelimit.Evaluate(s, limitsNow); st.State != usagelimit.None {
		t.Errorf("evaluate: %+v", st)
	}
	if st, out := usagelimit.Exhausted(s, limitsNow); out {
		t.Errorf("exhausted: %+v", st)
	}
	p.Balance.Estimated = false
	if st, out := usagelimit.Exhausted(LimitsSnapshot(p), limitsNow); !out || st.State != usagelimit.Reached {
		t.Errorf("a balance the provider reported still counts: %+v", st)
	}
}
