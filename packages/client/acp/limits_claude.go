package acp

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xlsft/harness/packages/client/acp/registry"
)

// Claude's limits (claude-agent-acp). Its rate-limit events (`_claude/rateLimit`) carry one window each and only the
// one Claude picks, so a window may never arrive that way. The whole picture is the answer to a prompt that is
// exactly `/usage`, in Markdown (usage-markdown.js in the adapter).
//
// Asking must spend no tokens. `/usage` itself does not: it is a local command of Claude Code, and the adapter only
// replaces its output with the structured one; when that query fails Claude Code's own text comes back, which is not
// the expected format and counts as no data. What can reach a model is what Claude Code and the adapter do around a
// turn in a new session, which the session is set up to rule out:
//   - the adapter asks for a generated title when a turn ends (session-titles.js), and the reply is long enough to
//     title from: the session has a title of its own (`title`), and the turn is cancelled as soon as the reply is in.
//   - Claude Code predicts the next prompt after a turn: `promptSuggestions` is off.
//   - a skill of the user's named `usage` would be a real prompt: the turn's usage shows that (ErrLimitsCostTokens).
//
// `persistSession` off leaves no transcript of these sessions in the user's Claude Code history.
type claudeLimits struct{}

// claudeHiddenSession is the _meta of session/new (options of the Claude Agent SDK).
var claudeHiddenSession = map[string]any{"claudeCode": map[string]any{"options": map[string]any{
	"title": "Harness limits check", "promptSuggestions": false, "persistSession": false,
}}}

func (claudeLimits) Fetch(ctx context.Context, cfg Config, mayAsk func() bool) (*registry.Limits, error) {
	reply, err := askHidden(ctx, cfg, hiddenPrompt{command: "usage", text: "/usage", meta: claudeHiddenSession, mayAsk: mayAsk, cancelOnReply: true})
	if err != nil || !reply.asked {
		return nil, err
	}
	if reply.tokens > 0 {
		return nil, fmt.Errorf("%w: %d", ErrLimitsCostTokens, reply.tokens)
	}
	now := time.Now()
	if cfg.Now != nil {
		now = cfg.Now()
	}
	return arrangeClaudeWindows(parseClaudeUsage(reply.text, now)), nil
}

// claudeEventLabels name the windows of the events as `/usage` names them, so that both land on the same rows.
var claudeEventLabels = map[string]string{
	"five_hour": "5-hour limit", "seven_day": "Weekly · all models", "seven_day_opus": "Weekly · Opus", "seven_day_sonnet": "Weekly · Sonnet",
}

// oldClaudeLabels are what these windows were called before; a stored report with them is out of date.
var oldClaudeLabels = map[string]bool{"5h": true, "7d": true, "7d Opus": true, "7d Sonnet": true}

func (claudeLimits) Event(v any, old *registry.Limits, now time.Time) *registry.Limits {
	m, _ := v.(map[string]any)
	if event, ok := m["_claude/rateLimit"].(map[string]any); ok {
		m = event
	}
	kind, _ := m["rateLimitType"].(string)
	if kind == "" {
		kind, _ = m["rate_limit_type"].(string)
	}
	pct, ok := registry.Number(m, "utilization")
	if kind == "" || !ok {
		return nil
	}
	if pct <= 1 {
		pct *= 100
	}
	label := claudeEventLabels[kind]
	if label == "" {
		label = strings.ReplaceAll(kind, "_", " ")
	}
	l := &registry.Limits{Partial: true, UpdatedAt: now.UTC(), Windows: []registry.LimitWindow{{
		Label: label, UsedPercent: math.Max(0, math.Min(100, pct)), ResetsAt: registry.ResetTime(m, now),
	}}}
	if old != nil {
		current := *old
		current.Windows = slices.DeleteFunc(slices.Clone(old.Windows), func(w registry.LimitWindow) bool { return oldClaudeLabels[w.Label] })
		old = &current
	}
	merged := l.Merge(old)
	merged.Partial = false
	return arrangeClaudeWindows(merged)
}

// arrangeClaudeWindows puts the shortest window first, as they read best; other windows keep their order after those.
func arrangeClaudeWindows(l *registry.Limits) *registry.Limits {
	if l == nil {
		return nil
	}
	rank := func(w registry.LimitWindow) int {
		i := slices.Index([]string{"5-hour limit", "Weekly · all models", "Weekly · Opus", "Weekly · Sonnet"}, w.Label)
		if i < 0 {
			return 4
		}
		return i
	}
	slices.SortStableFunc(l.Windows, func(a, b registry.LimitWindow) int { return rank(a) - rank(b) })
	return l
}

var (
	claudePlanLine   = regexp.MustCompile(`^>\s*Claude\s+(.+?)\s+subscription usage\s*$`)
	claudeWindowLine = regexp.MustCompile(`^\*\*(.+?)\*\*\s*[—–-]+\s*\*\*(\d+(?:\.\d+)?)%\*\*(.*)$`)
	claudeEscape     = regexp.MustCompile(`\\([\\` + "`" + `*_\[\]<>|])`)
)

// parseClaudeUsage reads the answer to `/usage` as a full report: it replaces the windows known so far. nil when it
// has no limits or does not read like one, so that a change of format keeps the old data. A window whose reset cannot
// be read is still shown.
func parseClaudeUsage(text string, now time.Time) *registry.Limits {
	l := &registry.Limits{UpdatedAt: now.UTC()}
	inLimits := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "---" {
			inLimits = line == "### Limits"
			continue
		}
		if m := claudePlanLine.FindStringSubmatch(line); m != nil {
			l.PlanName = claudeEscape.ReplaceAllString(m[1], "$1")
			continue
		}
		if !inLimits {
			continue
		}
		m := claudeWindowLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pct, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		l.Windows = append(l.Windows, registry.LimitWindow{Label: claudeEscape.ReplaceAllString(m[1], "$1"),
			UsedPercent: math.Min(100, pct), ResetsAt: claudeReset(m[3], now)})
	}
	if len(l.Windows) == 0 {
		return nil
	}
	return l
}

// claudeUSZones are the abbreviations Intl writes for en, by hours from UTC; elsewhere it writes GMT+3 or GMT+5:30.
var claudeUSZones = map[string]int{"EST": -5, "EDT": -4, "CST": -6, "CDT": -5, "MST": -7, "MDT": -6, "PST": -8, "PDT": -7,
	"AKST": -9, "AKDT": -8, "HST": -10, "HDT": -9}

var claudeGMTOffset = regexp.MustCompile(`^(?:GMT|UTC)([+-])(\d{1,2})(?::(\d{2}))?$`)

// claudeReset reads what follows a window's percent: ` · Resets Nov 4, 12:30 PM GMT+3` (formatReset: month, day and
// time without a year, in the zone of the agent, which runs on this machine), or a date formatReset could not read
// itself, as it came. The zero time when it cannot be read.
func claudeReset(rest string, now time.Time) time.Time {
	_, value, ok := strings.Cut(rest, "Resets ")
	if !ok {
		return time.Time{}
	}
	value = strings.NewReplacer(" ", " ", " ", " ").Replace(claudeEscape.ReplaceAllString(value, "$1"))
	value = strings.TrimSpace(value)
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC()
	}
	cut := strings.LastIndexByte(value, ' ')
	if cut < 0 {
		return time.Time{}
	}
	clock, zone := value[:cut], value[cut+1:]
	var t time.Time
	var err error
	for _, layout := range []string{"Jan 2, 3:04 PM", "Jan 2, 15:04"} {
		if t, err = time.Parse(layout, clock); err == nil {
			break
		}
	}
	if err != nil {
		return time.Time{}
	}
	at := func(year int, loc *time.Location) time.Time {
		return time.Date(year, t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
	}
	loc := claudeZone(zone)
	if loc == nil {
		// Some other abbreviation: this machine's own, if it is that one.
		if name, _ := at(now.Year(), now.Location()).Zone(); name != zone {
			return time.Time{}
		}
		loc = now.Location()
	}
	// No year: the next time this date comes. A reset that just passed stays in the past, and rates as passed.
	r := at(now.Year(), loc)
	if r.Before(now.AddDate(0, 0, -30)) {
		r = at(now.Year()+1, loc)
	}
	return r.UTC()
}

func claudeZone(zone string) *time.Location {
	if zone == "UTC" || zone == "GMT" {
		return time.UTC
	}
	if h, ok := claudeUSZones[zone]; ok {
		return time.FixedZone(zone, h*3600)
	}
	m := claudeGMTOffset.FindStringSubmatch(zone)
	if m == nil {
		return nil
	}
	h, _ := strconv.Atoi(m[2])
	mins, _ := strconv.Atoi(m[3])
	offset := h*3600 + mins*60
	if m[1] == "-" {
		offset = -offset
	}
	return time.FixedZone(zone, offset)
}
