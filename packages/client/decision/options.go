package decision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Defaults of RulesSync.
const (
	DefaultOptionsTimeout    = 5 * time.Second
	DefaultOptionsRetryAfter = 30 * time.Second
)

// RulesOptionsFetcher fetches the administrator's rule options
// (DecisionService.GetRulesOptions). Client implements it.
type RulesOptionsFetcher interface {
	GetRulesOptions(ctx context.Context) (version string, opts RulesOptions, err error)
}

// RulesSync keeps the options of a session's local rules in line with the
// server's: the admin's extra denies and allows, protected branches and the
// force-push policy. A local fast ALLOW must not bypass an extra deny the
// client does not know about, so:
//
//   - Start applies the on-disk cache (keyed by server and user) and
//     refreshes it from the server in the background;
//   - Ensure, called before every local decision, fetches synchronously when
//     nothing is known yet (no cache, no successful fetch); when that fails
//     the defaults apply and a warning is logged, and the next attempt waits
//     RetryAfter;
//   - Observe, called with DecideResponse.rules_version, refetches when the
//     server reports a different version.
//
// Offline the cache applies; with no cache, the defaults.
type RulesSync struct {
	// Rules is the provider whose options are kept in sync.
	Rules   *Rules
	Fetcher RulesOptionsFetcher
	// CachePath is the on-disk cache file ("" = no cache); see RulesCachePath.
	CachePath string
	// Server and User identify the cache entry (a cache written for another
	// server or user is ignored).
	Server, User string
	// Timeout bounds one fetch (default DefaultOptionsTimeout).
	Timeout time.Duration
	// RetryAfter is how long Ensure waits after a failed fetch before trying
	// again while nothing is known (default DefaultOptionsRetryAfter).
	RetryAfter time.Duration
	// OnChange runs after new options were applied (the session cache of
	// verdicts made with the old options is cleared here).
	OnChange func()
	Logger   *slog.Logger

	mu       sync.Mutex
	version  string
	known    bool          // options came from the server or the cache
	inflight chan struct{} // closed when the running fetch ends
	lastFail time.Time
	warned   bool
}

// NewRulesSync returns a RulesSync for fresh local rules fed by fetcher.
func NewRulesSync(fetcher RulesOptionsFetcher, cachePath, server, user string, log *slog.Logger) *RulesSync {
	return &RulesSync{Rules: NewRules(RulesOptions{}), Fetcher: fetcher, CachePath: cachePath, Server: server, User: user, Logger: log}
}

// RulesCachePath is the cache file of the rule options of one server and
// user under dir.
func RulesCachePath(dir, server, user string) string {
	sum := sha256.Sum256([]byte(server + "\n" + user))
	return filepath.Join(dir, "rules-options-"+hex.EncodeToString(sum[:12])+".json")
}

// Version returns the rules_version of the applied options ("" = defaults).
func (s *RulesSync) Version() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// Known reports whether options from the server (fetched or cached) apply.
func (s *RulesSync) Known() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.known
}

// Start applies the cached options and refreshes them in the background.
func (s *RulesSync) Start(ctx context.Context) {
	if s.loadCache() {
		go func() { _ = s.fetch(context.WithoutCancel(ctx)) }()
		return
	}
	// Nothing cached: start fetching now; the first Ensure waits for it.
	done := s.begin()
	if done == nil {
		return
	}
	go s.run(context.WithoutCancel(ctx), done)
}

// Ensure makes sure the server's options apply before a local decision when
// nothing is known yet (see RulesSync).
func (s *RulesSync) Ensure(ctx context.Context) {
	s.mu.Lock()
	if s.known {
		s.mu.Unlock()
		return
	}
	if ch := s.inflight; ch != nil {
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
		}
		return
	}
	if !s.lastFail.IsZero() && time.Since(s.lastFail) < s.retryAfter() {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	_ = s.fetch(ctx)
}

// Observe handles the rules_version of a DecideResponse: a version other
// than the applied one triggers a refetch.
func (s *RulesSync) Observe(ctx context.Context, version string) {
	if version == "" {
		return
	}
	s.mu.Lock()
	same := s.known && version == s.version
	s.mu.Unlock()
	if same {
		return
	}
	_ = s.fetch(context.WithoutCancel(ctx))
}

// Refresh fetches the options now.
func (s *RulesSync) Refresh(ctx context.Context) error { return s.fetch(ctx) }

// fetch runs one fetch, or waits for the one already running.
func (s *RulesSync) fetch(ctx context.Context) error {
	done := s.begin()
	if done == nil {
		s.mu.Lock()
		ch := s.inflight
		s.mu.Unlock()
		if ch != nil {
			select {
			case <-ch:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	return s.run(ctx, done)
}

// begin marks a fetch as running; nil when one already is.
func (s *RulesSync) begin() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight != nil {
		return nil
	}
	s.inflight = make(chan struct{})
	return s.inflight
}

func (s *RulesSync) run(ctx context.Context, done chan struct{}) error {
	defer func() {
		s.mu.Lock()
		s.inflight = nil
		s.mu.Unlock()
		close(done)
	}()
	if s.Fetcher == nil {
		return errors.New("no rules options source")
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultOptionsTimeout
	}
	fctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	version, opts, err := s.Fetcher.GetRulesOptions(fctx)
	if err != nil {
		s.mu.Lock()
		s.lastFail = time.Now()
		known, warned := s.known, s.warned
		s.warned = true
		s.mu.Unlock()
		if s.Logger != nil {
			if known {
				s.Logger.Debug("decision rules options refresh failed; keeping the cached options", "err", err)
			} else if !warned {
				s.Logger.Warn("decision rules options unavailable; local rules use the defaults and may allow what an administrator rule denies", "err", err)
			}
		}
		return err
	}
	s.apply(version, opts, true)
	s.saveCache(version, opts)
	return nil
}

func (s *RulesSync) apply(version string, opts RulesOptions, fetched bool) {
	s.mu.Lock()
	changed := !s.known || version != s.version
	s.version, s.known = version, true
	if fetched {
		s.warned, s.lastFail = false, time.Time{}
	}
	s.mu.Unlock()
	if s.Rules != nil {
		s.Rules.SetOptions(opts)
	}
	if changed && s.OnChange != nil {
		s.OnChange()
	}
}

func (s *RulesSync) retryAfter() time.Duration {
	if s.RetryAfter > 0 {
		return s.RetryAfter
	}
	return DefaultOptionsRetryAfter
}

// --- on-disk cache ---------------------------------------------------------------------------

type cachedPattern struct {
	Pattern    string   `json:"pattern"`
	Reason     string   `json:"reason,omitempty"`
	Categories []string `json:"categories,omitempty"`
}

type cachedOptions struct {
	Server            string          `json:"server"`
	User              string          `json:"user"`
	RulesVersion      string          `json:"rules_version"`
	FetchedAt         time.Time       `json:"fetched_at"`
	ExtraDeny         []cachedPattern `json:"extra_deny"`
	ExtraAllow        []string        `json:"extra_allow"`
	ProtectedBranches []string        `json:"protected_branches"`
	DenyAllForcePush  bool            `json:"deny_all_force_push"`
}

func (s *RulesSync) loadCache() bool {
	if s.CachePath == "" {
		return false
	}
	data, err := os.ReadFile(s.CachePath)
	if err != nil {
		return false
	}
	var c cachedOptions
	if json.Unmarshal(data, &c) != nil || c.Server != s.Server || c.User != s.User || c.RulesVersion == "" {
		return false
	}
	opts := RulesOptions{ExtraAllow: c.ExtraAllow, ProtectedBranches: c.ProtectedBranches, DenyAllForcePush: c.DenyAllForcePush}
	for _, d := range c.ExtraDeny {
		opts.ExtraDeny = append(opts.ExtraDeny, ExtraDenyRule{Pattern: d.Pattern, Reason: d.Reason, Categories: d.Categories})
	}
	s.apply(c.RulesVersion, opts, false)
	return true
}

func (s *RulesSync) saveCache(version string, opts RulesOptions) {
	if s.CachePath == "" {
		return
	}
	c := cachedOptions{
		Server: s.Server, User: s.User, RulesVersion: version, FetchedAt: time.Now().UTC(),
		ExtraDeny: []cachedPattern{}, ExtraAllow: append([]string{}, opts.ExtraAllow...),
		ProtectedBranches: append([]string{}, opts.ProtectedBranches...), DenyAllForcePush: opts.DenyAllForcePush,
	}
	for _, d := range opts.ExtraDeny {
		c.ExtraDeny = append(c.ExtraDeny, cachedPattern{Pattern: d.Pattern, Reason: d.Reason, Categories: d.Categories})
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err == nil {
		err = writeFileAtomic(s.CachePath, data)
	}
	if err != nil && s.Logger != nil {
		s.Logger.Warn("could not cache the decision rules options", "path", s.CachePath, "err", err)
	}
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".rules-options-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, 0o600)
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		_ = os.Remove(tmp)
	}
	return werr
}
