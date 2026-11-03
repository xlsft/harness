package registry

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LimitWindow is one rate-limit window of an agent's subscription.
type LimitWindow struct {
	Label         string    `json:"label"`
	UsedPercent   float64   `json:"usedPercent"`
	ResetsAt      time.Time `json:"resetsAt,omitzero"`
	WindowMinutes int64     `json:"windowMinutes,omitempty"`
}

// Limits is the latest subscription-limit snapshot an agent reported.
type Limits struct {
	Windows   []LimitWindow `json:"windows"`
	PlanName  string        `json:"planName,omitempty"`
	UpdatedAt time.Time     `json:"updatedAt"`
}

// SetLimits stores an agent's latest limits snapshot (device-local).
func (m *Manager) SetLimits(id string, l *Limits) error {
	if !validID(id) || l == nil {
		return nil
	}
	return m.updateState(func(st *stateFile) error {
		s := st.Agents[id]
		if s == nil {
			s = &AgentState{}
			st.Agents[id] = s
		}
		s.Limits = l
		return nil
	})
}

// limitKeys name objects that hold rate-limit windows.
var limitKeys = map[string]bool{"rate_limits": true, "ratelimits": true, "rate_limit": true, "ratelimit": true,
	"usage_limits": true, "usagelimits": true, "limits": true, "subscription_limits": true, "subscriptionlimits": true}

func norm(k string) string { return strings.ToLower(strings.ReplaceAll(k, "-", "_")) }

// ParseLimits looks for a rate-limit snapshot in a decoded JSON value (an agent's `_meta`, or the params of an
// extension notification). It understands the common shapes: an object under rate_limits / rateLimits / limits
// whose members (primary, secondary, or named windows; or an array) carry used_percent / usedPercent and
// window_minutes, resets_at (unix seconds or RFC 3339) or resets_in_seconds. Returns nil when there is none.
func ParseLimits(v any, now time.Time) *Limits {
	return findLimits(v, now, 0)
}

func findLimits(v any, now time.Time, depth int) *Limits {
	if depth > 6 {
		return nil
	}
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if limitKeys[norm(k)] {
				if l := windowsOf(child, now); l != nil {
					if l.PlanName == "" {
						l.PlanName = planOf(x)
					}
					return l
				}
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if l := findLimits(x[k], now, depth+1); l != nil {
				return l
			}
		}
	case []any:
		for _, c := range x {
			if l := findLimits(c, now, depth+1); l != nil {
				return l
			}
		}
	}
	return nil
}

func planOf(m map[string]any) string {
	for _, k := range []string{"plan_type", "planType", "plan_name", "planName", "plan"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func windowsOf(v any, now time.Time) *Limits {
	l := &Limits{UpdatedAt: now.UTC()}
	add := func(name string, w any) {
		m, ok := w.(map[string]any)
		if !ok {
			return
		}
		pct, ok := number(m, "used_percent", "usedPercent", "percent_used", "percentUsed", "used_pct", "utilization")
		if !ok {
			return
		}
		if pct <= 1 && hasKey(m, "utilization") {
			pct *= 100
		}
		win := LimitWindow{UsedPercent: math.Max(0, math.Min(100, pct))}
		if mins, ok := number(m, "window_minutes", "windowMinutes", "window_duration_mins", "windowDurationMins"); ok {
			win.WindowMinutes = int64(mins)
		} else if secs, ok := number(m, "window_seconds", "windowSeconds"); ok {
			win.WindowMinutes = int64(secs / 60)
		}
		win.ResetsAt = resetTime(m, now)
		win.Label = labelOf(m, name, win.WindowMinutes)
		l.Windows = append(l.Windows, win)
	}
	switch x := v.(type) {
	case map[string]any:
		l.PlanName = planOf(x)
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		// primary before secondary, then the rest by name.
		sort.Slice(keys, func(i, j int) bool { return rank(keys[i]) < rank(keys[j]) || rank(keys[i]) == rank(keys[j]) && keys[i] < keys[j] })
		for _, k := range keys {
			add(k, x[k])
		}
	case []any:
		for i, w := range x {
			add(strconv.Itoa(i), w)
		}
	}
	if len(l.Windows) == 0 {
		return nil
	}
	return l
}

func rank(k string) int {
	switch norm(k) {
	case "primary":
		return 0
	case "secondary":
		return 1
	}
	return 2
}

func hasKey(m map[string]any, k string) bool { _, ok := m[k]; return ok }

func number(m map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		switch n := m[k].(type) {
		case float64:
			return n, true
		case int:
			return float64(n), true
		case int64:
			return float64(n), true
		case string:
			if f, err := strconv.ParseFloat(n, 64); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

func resetTime(m map[string]any, now time.Time) time.Time {
	for _, k := range []string{"resets_at", "resetsAt", "reset_at", "resetAt"} {
		switch v := m[k].(type) {
		case float64:
			if v > 1e12 {
				return time.UnixMilli(int64(v)).UTC()
			}
			return time.Unix(int64(v), 0).UTC()
		case string:
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				return t.UTC()
			}
		}
	}
	if secs, ok := number(m, "resets_in_seconds", "resetsInSeconds", "reset_after_seconds", "resetAfterSeconds"); ok {
		return now.Add(time.Duration(secs) * time.Second).UTC()
	}
	return time.Time{}
}

func labelOf(m map[string]any, name string, minutes int64) string {
	for _, k := range []string{"label", "name", "window"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	switch {
	case minutes == 10080:
		return "weekly"
	case minutes > 0 && minutes%1440 == 0:
		return fmt.Sprintf("%dd", minutes/1440)
	case minutes > 0 && minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60)
	case minutes > 0:
		return fmt.Sprintf("%dm", minutes)
	}
	if _, err := strconv.Atoi(name); err == nil {
		return "window " + name
	}
	return name
}
