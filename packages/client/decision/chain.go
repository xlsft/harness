package decision

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"github.com/xlsft/harness/packages/client/permissions"
)

// Chain runs the local rules first and asks the remote decider only for
// what they leave open. A local DENY or fast ALLOW is final and skips the
// network. When the remote decider is missing or fails, the local verdict
// (ASK) stands, with the failure in the reason.
type Chain struct {
	Local  *Rules
	Remote permissions.Decider
	// Sync, when set, keeps Local's options in line with the server's (see
	// RulesSync): it runs before every local decision and sees the
	// rules_version of every remote verdict.
	Sync *RulesSync
	// Git supplies the git facts (default DefaultGitInfo).
	Git    GitInfo
	Logger *slog.Logger
}

// versionedDecider is a remote decider that reports the rules_version of its
// verdicts (Client).
type versionedDecider interface {
	DecideWithRulesVersion(ctx context.Context, req permissions.AutoRequest) (permissions.AutoResult, string, error)
}

// Decide implements permissions.Decider.
func (c *Chain) Decide(ctx context.Context, req permissions.AutoRequest) (permissions.AutoResult, error) {
	git := c.Git
	if git == nil {
		git = DefaultGitInfo
	}
	f := ComputeFacts(ctx, req, git)
	ctx = withFacts(ctx, f)
	local := c.Local
	if local == nil {
		local = LocalRules()
	}
	if c.Sync != nil {
		// No local fast ALLOW before the admin's extra denies are known.
		c.Sync.Ensure(ctx)
	}
	res := local.Evaluate(req, f)
	if res.Behavior != permissions.Ask {
		return res, nil
	}
	if c.Remote == nil {
		res.Reason = "No decision service is configured and no local rule covers this action."
		return res, nil
	}
	var rr permissions.AutoResult
	var err error
	if vd, ok := c.Remote.(versionedDecider); ok && c.Sync != nil {
		var version string
		rr, version, err = vd.DecideWithRulesVersion(ctx, req)
		if err == nil {
			c.Sync.Observe(ctx, version)
		}
	} else {
		rr, err = c.Remote.Decide(ctx, req)
	}
	if err != nil {
		if ctx.Err() != nil {
			return permissions.AutoResult{}, ctx.Err()
		}
		if c.Logger != nil {
			c.Logger.Warn("decision service failed, using local rules", "tool", req.ToolName, "err", err)
		}
		res.Reason = "The decision service is unavailable (" + shortErr(err) + ") and no local rule covers this action."
		return res, nil
	}
	return rr, nil
}

func shortErr(err error) string {
	s := err.Error()
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

// Cache is a session-level cache of ALLOW and DENY verdicts keyed by tool,
// working directory and normalized input. ASK and errors are not cached.
// It implements permissions.Resetter: the engine clears it when the
// permission mode changes.
type Cache struct {
	Inner permissions.Decider

	mu sync.Mutex
	m  map[string]permissions.AutoResult
}

// NewCache wraps inner.
func NewCache(inner permissions.Decider) *Cache { return &Cache{Inner: inner} }

// Decide implements permissions.Decider.
func (c *Cache) Decide(ctx context.Context, req permissions.AutoRequest) (permissions.AutoResult, error) {
	key := CacheKey(req)
	c.mu.Lock()
	if r, ok := c.m[key]; ok {
		c.mu.Unlock()
		r.Cached = true
		r.LatencyMs, r.CostUSD = 0, 0
		return r, nil
	}
	c.mu.Unlock()
	r, err := c.Inner.Decide(ctx, req)
	if err == nil && (r.Behavior == permissions.Allow || r.Behavior == permissions.Deny) {
		c.mu.Lock()
		if c.m == nil {
			c.m = map[string]permissions.AutoResult{}
		}
		c.m[key] = r
		c.mu.Unlock()
	}
	return r, err
}

// Reset implements permissions.Resetter.
func (c *Cache) Reset() {
	c.mu.Lock()
	c.m = nil
	c.mu.Unlock()
}

// Len returns the number of cached verdicts.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// CacheKey normalizes a call: tool name, working directory and the input
// re-encoded with sorted keys; a Bash command is trimmed and its
// description (free text for the user) ignored.
func CacheKey(req permissions.AutoRequest) string {
	var in any
	if err := json.Unmarshal(req.Input, &in); err != nil {
		in = string(req.Input)
	}
	if m, ok := in.(map[string]any); ok && req.ToolName == "Bash" {
		if cmd, ok := m["command"].(string); ok {
			m["command"] = strings.TrimSpace(cmd)
		}
		delete(m, "description")
	}
	b, _ := json.Marshal(in)
	return req.ToolName + "\x00" + req.Cwd + "\x00" + string(b)
}

// New returns the auto-mode decider of a session: a Cache over a Chain of
// the local rules and remote (which may be nil).
func New(remote permissions.Decider, log *slog.Logger) *Cache {
	return NewWithRulesSync(remote, nil, log)
}

// NewWithRulesSync is New with the local rules kept in line with the
// server's rule options by sync (nil: the default options). New options
// clear the session cache. The caller starts sync (RulesSync.Start).
func NewWithRulesSync(remote permissions.Decider, sync *RulesSync, log *slog.Logger) *Cache {
	chain := &Chain{Local: LocalRules(), Remote: remote, Logger: log}
	cache := NewCache(chain)
	if sync != nil {
		if sync.Rules == nil {
			sync.Rules = NewRules(RulesOptions{})
		}
		chain.Local, chain.Sync = sync.Rules, sync
		prev := sync.OnChange
		sync.OnChange = func() {
			cache.Reset()
			if prev != nil {
				prev()
			}
		}
	}
	return cache
}
