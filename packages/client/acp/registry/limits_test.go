package registry

import (
	"encoding/json"
	"testing"
	"time"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseLimitsCodexShape(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	v := decode(t, `{"codex":{"token_count":{"rate_limits":{
	  "primary":{"used_percent":42,"window_minutes":300,"resets_in_seconds":3600},
	  "secondary":{"used_percent":18.5,"window_minutes":10080,"resets_at":1793793600}},
	  "plan_type":"plus"}}}`)
	l := ParseLimits(v, now)
	if l == nil || len(l.Windows) != 2 {
		t.Fatalf("limits: %+v", l)
	}
	if w := l.Windows[0]; w.Label != "5h" || w.UsedPercent != 42 || !w.ResetsAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("primary: %+v", w)
	}
	if w := l.Windows[1]; w.Label != "weekly" || w.UsedPercent != 18.5 || w.ResetsAt.Unix() != 1793793600 {
		t.Fatalf("secondary: %+v", w)
	}
	if l.PlanName != "plus" {
		t.Fatalf("plan: %q", l.PlanName)
	}
}

func TestParseLimitsCamelCaseArrayAndNone(t *testing.T) {
	now := time.Now()
	l := ParseLimits(decode(t, `{"rateLimits":[{"label":"session","usedPercent":"7","resetsAt":"2026-11-04T00:00:00Z"}]}`), now)
	if l == nil || l.Windows[0].Label != "session" || l.Windows[0].UsedPercent != 7 || l.Windows[0].ResetsAt.IsZero() {
		t.Fatalf("limits: %+v", l)
	}
	if ParseLimits(decode(t, `{"usage":{"inputTokens":3},"limits":{"foo":1}}`), now) != nil {
		t.Fatal("found limits where there are none")
	}
}

func TestClaudeRateLimitEvent(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := ParseLimits(map[string]any{"_claude/rateLimit": map[string]any{
		"status": "allowed_warning", "rateLimitType": "five_hour", "utilization": 0.82, "resetsAt": float64(1_800_003_600),
	}}, now)
	if l == nil || !l.Partial || len(l.Windows) != 1 || l.Windows[0].Label != "5-hour limit" || int(l.Windows[0].UsedPercent) != 82 {
		t.Fatalf("event: %+v", l)
	}
	if !l.Windows[0].ResetsAt.Equal(time.Unix(1_800_003_600, 0)) {
		t.Fatalf("resets: %v", l.Windows[0].ResetsAt)
	}
	// Without a number it says nothing about how much is used.
	if got := ParseLimits(map[string]any{"_claude/rateLimit": map[string]any{"status": "allowed", "rateLimitType": "five_hour"}}, now); got != nil {
		t.Fatalf("no utilization: %+v", got)
	}
	// One window at a time: the others keep their numbers.
	week := ParseLimits(map[string]any{"_claude/rateLimit": map[string]any{"rateLimitType": "seven_day", "utilization": 0.4}}, now)
	merged := week.Merge(l)
	if len(merged.Windows) != 2 || merged.Windows[0].Label != "5-hour limit" || merged.Windows[1].Label != "Weekly · all models" || merged.Partial {
		t.Fatalf("merged: %+v", merged)
	}
	again := ParseLimits(map[string]any{"_claude/rateLimit": map[string]any{"rateLimitType": "five_hour", "utilization": 0.9}}, now).Merge(merged)
	if len(again.Windows) != 2 || int(again.Windows[0].UsedPercent) != 90 {
		t.Fatalf("replaced: %+v", again)
	}
}
