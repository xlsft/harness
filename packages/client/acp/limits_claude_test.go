package acp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/acp/registry"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// claudeUsageFixture is the output of the adapter's formatUsageResponse (usage-markdown.js, claude-agent-acp 0.100.0)
// run by hand on sample values with TZ=Europe/Moscow; the model-scoped window's name has an underscore to escape.
const claudeUsageFixture = "## Usage\n\n> Claude max subscription usage\n\n### Limits\n\n" +
	"**5-hour limit** — **89%** · Resets Nov 4, 12:30 PM GMT+3\n\n`██████████████████░░`\n\n" +
	"**Weekly · all models** — **87%** · Resets Nov 8, 12:00 AM GMT+3\n\n`█████████████████░░░`\n\n" +
	"**Weekly · Claude\\_Fable** — **40%** · Resets Jan 1, 2:00 AM GMT+3\n\n`████████░░░░░░░░░░░░`\n\n" +
	"---\n\n### This session\n\n| Cost | API time | Active |\n|:--|:--|:--|\n| $0.00 | 0s | 1s |\n\n" +
	"| Breakdown | Tokens |\n|:--|--:|\n| Input | 0 |\n| Output | 0 |\n| Cache read | 0 |\n| Cache write | 0 |\n\n" +
	"---\n\n### What’s using your limits?\n\n> Approximate, overlapping measures · this machine only · excludes claude.ai\n\n" +
	"**Last 24h** · 12 requests · 2 sessions\n\n- **44%** Trim · less\n\n| MCP server | Usage |\n|:--|--:|\n| ctx | `██████░░░░░░░░░░░░░░` 30% |\n\n" +
	"**Last 7d** · 80 requests · 9 sessions"

var claudeTestNow = time.Date(2026, 11, 4, 6, 0, 0, 0, time.UTC)

func TestParseClaudeUsage(t *testing.T) {
	l := parseClaudeUsage(claudeUsageFixture, claudeTestNow)
	if l == nil || l.PlanName != "max" || l.Partial || !l.UpdatedAt.Equal(claudeTestNow) || len(l.Windows) != 3 {
		t.Fatalf("limits: %+v", l)
	}
	want := []registry.LimitWindow{
		{Label: "5-hour limit", UsedPercent: 89, ResetsAt: time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC)},
		{Label: "Weekly · all models", UsedPercent: 87, ResetsAt: time.Date(2026, 11, 7, 21, 0, 0, 0, time.UTC)},
		// January, seen from November: next year.
		{Label: "Weekly · Claude_Fable", UsedPercent: 40, ResetsAt: time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC)},
	}
	for i, w := range l.Windows {
		if w.Label != want[i].Label || w.UsedPercent != want[i].UsedPercent || !w.ResetsAt.Equal(want[i].ResetsAt) {
			t.Fatalf("window %d: %+v, want %+v", i, w, want[i])
		}
	}
}

func TestClaudeResetTimeZones(t *testing.T) {
	// As formatReset wrote the same moments with TZ=America/Los_Angeles, UTC and Asia/Kolkata.
	moment := time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC)
	for _, s := range []string{" · Resets Nov 4, 1:30 AM PST", " · Resets Nov 4, 9:30 AM UTC", " · Resets Nov 4, 3:00 PM GMT+5:30",
		" · Resets Nov 4, 1:30 AM PST", " · Resets 2026-11-04T09:30:00Z"} {
		if got := claudeReset(s, claudeTestNow); !got.Equal(moment) {
			t.Errorf("%q: %v", s, got)
		}
	}
	if got := claudeReset(" · Resets Nov 4, 3:00 PM GMT-3", claudeTestNow); !got.Equal(time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)) {
		t.Errorf("negative offset: %v", got)
	}
	// An abbreviation Intl has for this machine's zone only.
	cet := time.FixedZone("CET", 3600)
	if got := claudeReset(" · Resets Nov 4, 10:30 AM CET", claudeTestNow.In(cet)); !got.Equal(moment) {
		t.Errorf("own zone: %v", got)
	}
	for _, s := range []string{"", " · Resets someday", " · Resets Nov 4, 10:30 AM XYZ", " · Resets Smarch 4, 1:30 AM PST"} {
		if got := claudeReset(s, claudeTestNow); !got.IsZero() {
			t.Errorf("%q: %v", s, got)
		}
	}
}

func TestParseClaudeUsageTolerance(t *testing.T) {
	// Unknown lines around known ones; a window without a readable reset, or without any, still shows.
	l := parseClaudeUsage("## Usage\n\nsomething new\n\n### Limits\n\nA new paragraph.\n**5-hour limit** — **12.5%** · Resets at some point\n"+
		"**Weekly · Sonnet** — **150%**\n**Weekly · Opus** — unknown\n\n---\n\n**Outside** — **9%**\n", claudeTestNow)
	if l == nil || l.PlanName != "" || len(l.Windows) != 2 {
		t.Fatalf("limits: %+v", l)
	}
	if w := l.Windows[0]; w.Label != "5-hour limit" || w.UsedPercent != 12.5 || !w.ResetsAt.IsZero() {
		t.Fatalf("window: %+v", w)
	}
	if w := l.Windows[1]; w.Label != "Weekly · Sonnet" || w.UsedPercent != 100 {
		t.Fatalf("window: %+v", w)
	}
	// A change of format, Claude Code's own text, a model's prose and limits that are not available: no data.
	for _, s := range []string{
		"",
		"## Usage\n\n### Limits\n\n5-hour limit: 89%\n",
		"**5-hour limit** — **89%**\n",
		"Session cost: $0.00\nUsage: 89% of your 5-hour limit\n",
		"## Usage\n\n> Claude max subscription usage\n\n---\n\n### This session\n",
	} {
		if l := parseClaudeUsage(s, claudeTestNow); l != nil {
			t.Fatalf("%q: %+v", s, l)
		}
	}
}

func claudeEvent(kind string, utilization float64) map[string]any {
	return map[string]any{"_claude/rateLimit": map[string]any{"rateLimitType": kind, "utilization": utilization}}
}

func TestClaudeRateLimitEvent(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	src := LimitsSourceFor("claude-acp")
	l := src.Event(map[string]any{"_claude/rateLimit": map[string]any{
		"status": "allowed_warning", "rateLimitType": "five_hour", "utilization": 0.82, "resetsAt": float64(1_800_003_600),
	}}, nil, now)
	if l == nil || l.Partial || len(l.Windows) != 1 || l.Windows[0].Label != "5-hour limit" || int(l.Windows[0].UsedPercent) != 82 {
		t.Fatalf("event: %+v", l)
	}
	if !l.Windows[0].ResetsAt.Equal(time.Unix(1_800_003_600, 0)) {
		t.Fatalf("resets: %v", l.Windows[0].ResetsAt)
	}
	// Without a number it says nothing about how much is used.
	if got := src.Event(map[string]any{"_claude/rateLimit": map[string]any{"status": "allowed", "rateLimitType": "five_hour"}}, l, now); got != nil {
		t.Fatalf("no utilization: %+v", got)
	}
	if got := src.Event(map[string]any{"usage": map[string]any{"inputTokens": 3}}, l, now); got != nil {
		t.Fatalf("not an event: %+v", got)
	}
	// One window at a time: the others keep their numbers, shortest first.
	merged := src.Event(claudeEvent("seven_day", 0.4), l, now)
	if len(merged.Windows) != 2 || merged.Windows[0].Label != "5-hour limit" || merged.Windows[1].Label != "Weekly · all models" || merged.Partial {
		t.Fatalf("merged: %+v", merged)
	}
	again := src.Event(claudeEvent("five_hour", 0.9), merged, now)
	if len(again.Windows) != 2 || int(again.Windows[0].UsedPercent) != 90 {
		t.Fatalf("replaced: %+v", again)
	}
	// A stored report under the old names is out of date.
	old := &registry.Limits{Windows: []registry.LimitWindow{{Label: "5h", UsedPercent: 10}, {Label: "7d", UsedPercent: 20}}}
	if got := src.Event(claudeEvent("seven_day", 0.3), old, now); len(got.Windows) != 1 || got.Windows[0].Label != "Weekly · all models" {
		t.Fatalf("old names: %+v", got)
	}
}

// Events land on the rows of the full report, and the next full report replaces everything.
func TestClaudeEventsMergeOntoTheFullReport(t *testing.T) {
	src := LimitsSourceFor("claude-acp")
	full := arrangeClaudeWindows(parseClaudeUsage(claudeUsageFixture, claudeTestNow))
	l := src.Event(claudeEvent("five_hour", 0.95), full, claudeTestNow)
	if l.PlanName != "max" || len(l.Windows) != 3 || l.Windows[0].UsedPercent != 95 || l.Windows[1].UsedPercent != 87 {
		t.Fatalf("merged: %+v", l)
	}
	if got := full.Merge(l); got != full {
		t.Fatalf("a full report merged: %+v", got)
	}
}

func TestAgentWithoutSourceHasOnlyGenericParsing(t *testing.T) {
	if LimitsSourceFor("fake") != nil || LimitsSourceFor("codex-acp") != nil {
		t.Fatal("a source for an agent without one")
	}
	s, rec, _ := newTestSession(t)
	s.mu.Lock()
	ok := s.limitsLocked(claudeEvent("five_hour", 0.5))
	s.unlock()
	if ok || s.State().Limits != nil || rec.limits != nil {
		t.Fatalf("claimed a Claude event: %v %+v", ok, s.State().Limits)
	}
}

func TestSessionWithSourceReadsItsEvents(t *testing.T) {
	cwd := t.TempDir()
	s := New(Config{AgentID: "claude-acp", AgentName: "Claude", Launch: registry.Launch{Command: fakeAgentBin}, Cwd: cwd, LimitsSource: LimitsSourceFor("claude-acp")})
	t.Cleanup(s.Close)
	s.mu.Lock()
	ok := s.limitsLocked(claudeEvent("five_hour", 0.5))
	s.unlock()
	if st := s.State(); !ok || st.Limits == nil || st.Limits.Windows[0].Label != "5-hour limit" {
		t.Fatalf("limits: %v %+v", ok, st.Limits)
	}
}

// hiddenAgent is the fake agent's environment for one hidden session: its usage reply and what it was asked.
type hiddenAgent struct {
	env []string
	log string
}

func newHiddenAgent(t *testing.T, reply *string, extra ...string) *hiddenAgent {
	t.Helper()
	dir := t.TempDir()
	h := &hiddenAgent{log: filepath.Join(dir, "log")}
	h.env = append(extra, "FAKE_LOG="+h.log)
	if reply != nil {
		f := filepath.Join(dir, "usage")
		if err := os.WriteFile(f, []byte(*reply), 0o600); err != nil {
			t.Fatal(err)
		}
		h.env = append(h.env, "FAKE_USAGE_FILE="+f)
	}
	return h
}

func (h *hiddenAgent) logged() []string {
	b, _ := os.ReadFile(h.log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (h *hiddenAgent) fetch(t *testing.T, mayAsk func() bool) (*registry.Limits, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return LimitsSourceFor("claude-acp").Fetch(ctx, Config{AgentID: "claude-acp", AgentName: "Claude", Launch: registry.Launch{Command: fakeAgentBin},
		Cwd: t.TempDir(), Env: h.env, Now: func() time.Time { return claudeTestNow }}, mayAsk)
}

func TestClaudeFetchAsksInAHiddenSession(t *testing.T) {
	reply := claudeUsageFixture
	h := newHiddenAgent(t, &reply)
	l, err := h.fetch(t, nil)
	if err != nil || l == nil || l.PlanName != "max" || len(l.Windows) != 3 || l.Windows[0].Label != "5-hour limit" || l.Windows[0].UsedPercent != 89 {
		t.Fatalf("limits: %+v %v", l, err)
	}
	logged := h.logged()
	if !slices.Contains(logged, "usage prompt") {
		t.Fatalf("agent log: %v", logged)
	}
	// One prompt, nothing else: the session is set up against the model calls around a turn, and closed.
	n := 0
	for _, line := range logged {
		if strings.HasPrefix(line, "prompt") {
			n++
		}
	}
	if n != 1 || !slices.Contains(logged, "session/close") {
		t.Fatalf("agent log: %v", logged)
	}
	var meta string
	for _, line := range logged {
		if strings.HasPrefix(line, "session/meta ") {
			meta = line
		}
	}
	for _, want := range []string{`"title":"Harness limits check"`, `"promptSuggestions":false`, `"persistSession":false`} {
		if !strings.Contains(meta, want) {
			t.Fatalf("session/new _meta %q has no %s", meta, want)
		}
	}
}

func TestClaudeFetchNeverAsksAnAgentWithoutTheCommand(t *testing.T) {
	h := newHiddenAgent(t, nil)
	l, err := h.fetch(t, nil)
	if err != nil || l != nil {
		t.Fatalf("limits: %+v %v", l, err)
	}
	for _, line := range h.logged() {
		if strings.HasPrefix(line, "prompt") || line == "usage prompt" {
			t.Fatalf("a prompt was sent: %v", h.logged())
		}
	}
}

func TestClaudeFetchAsksOnlyWhenItMay(t *testing.T) {
	reply := claudeUsageFixture
	h := newHiddenAgent(t, &reply)
	l, err := h.fetch(t, func() bool { return false })
	if err != nil || l != nil || slices.Contains(h.logged(), "usage prompt") {
		t.Fatalf("limits: %+v %v, agent log: %v", l, err, h.logged())
	}
}

func TestClaudeFetchReplyThatIsNotAReportIsNoData(t *testing.T) {
	// What the adapter forwards when its structured query fails.
	reply := "Session cost: $0.00\nPlan usage is available at claude.ai/settings/usage"
	l, err := newHiddenAgent(t, &reply).fetch(t, nil)
	if err != nil || l != nil {
		t.Fatalf("limits: %+v %v", l, err)
	}
}

func TestClaudeFetchThatSpentTokensSaysSo(t *testing.T) {
	reply := claudeUsageFixture
	l, err := newHiddenAgent(t, &reply, "FAKE_USAGE_TOKENS=42").fetch(t, nil)
	if !errors.Is(err, ErrLimitsCostTokens) || l != nil {
		t.Fatalf("limits: %+v %v", l, err)
	}
}

func TestClaudeFetchFailureIsAnError(t *testing.T) {
	dir := t.TempDir()
	fail := filepath.Join(dir, "fail")
	if err := os.WriteFile(fail, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	reply := claudeUsageFixture
	if l, err := newHiddenAgent(t, &reply, "FAKE_FAIL_FILE="+fail).fetch(t, nil); err == nil || l != nil {
		t.Fatalf("limits: %+v %v", l, err)
	}
}

func usageLimits(evs []*agentv1.SessionEvent) []*agentv1.UsageLimit {
	var out []*agentv1.UsageLimit
	for _, e := range evs {
		if u := e.GetUsageLimit(); u != nil {
			out = append(out, u)
		}
	}
	return out
}

// A full report from outside rates the limits like any report, windows it no longer lists included.
func TestApplyLimitsUpdatesTheSessionAndItsBanner(t *testing.T) {
	s, rec, _ := newTestSession(t)
	future := time.Now().Add(time.Hour)
	s.ApplyLimits(&registry.Limits{PlanName: "max", Windows: []registry.LimitWindow{
		{Label: "5-hour limit", UsedPercent: 98, ResetsAt: future}, {Label: "Weekly · all models", UsedPercent: 87}}})
	if st := s.State(); st.Limits == nil || st.Limits.PlanName != "max" || len(st.Limits.Windows) != 2 {
		t.Fatalf("limits: %+v", st.Limits)
	}
	got := usageLimits(rec.snapshot())
	if len(got) != 1 || got[0].GetState() != agentv1.UsageLimit_STATE_WRAP_UP || got[0].GetWindowLabel() != "5-hour limit" || got[0].GetUsedPercent() != 98 {
		t.Fatalf("usage events: %v", got)
	}
	rec.mu.Lock()
	changed := rec.changed
	rec.mu.Unlock()
	if changed == 0 {
		t.Fatal("the session did not say it changed")
	}
	// The 5-hour window went down: the next report clears the banner.
	s.ApplyLimits(&registry.Limits{Windows: []registry.LimitWindow{{Label: "5-hour limit", UsedPercent: 10, ResetsAt: future}}})
	if got := usageLimits(rec.snapshot()); len(got) != 2 || got[1].GetState() != agentv1.UsageLimit_STATE_CLEARED {
		t.Fatalf("usage events: %v", got)
	}
	// nil: no report, nothing changes.
	s.ApplyLimits(nil)
	if st := s.State(); len(st.Limits.Windows) != 1 {
		t.Fatalf("limits: %+v", st.Limits)
	}
}

func TestApplyLimitsLeavesARefusalStanding(t *testing.T) {
	s, rec, _ := newTestSession(t)
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Submit("u1", text("ratelimited"), false)
	rec.waitFor(t, "turn", turnsFinished(1))
	s.ApplyLimits(&registry.Limits{Windows: []registry.LimitWindow{{Label: "5-hour limit", UsedPercent: 12}}})
	if st := s.State(); st.UsageLimit.GetState() != agentv1.UsageLimit_STATE_REACHED || st.Limits.Windows[0].UsedPercent != 12 {
		t.Fatalf("state: %v, limits: %+v", st.UsageLimit, st.Limits)
	}
}
