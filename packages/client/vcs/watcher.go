package vcs

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Schedule is the polling schedule. Zero fields take the defaults of DefaultSchedule.
type Schedule struct {
	// Pending is the first interval while an open PR has queued or running checks; each further
	// pending poll multiplies it by 1.5 up to PendingMax.
	Pending    time.Duration
	PendingMax time.Duration
	// Open is the interval for an open PR whose checks have settled.
	Open time.Duration
	// NoPR is the interval when the branch has no PR yet.
	NoPR time.Duration
	// Closed is the interval when the branch's PRs are all merged or closed.
	Closed time.Duration
	// Idle is the interval when there is nothing to poll: no git remote, no logged-in CLI for the
	// remote's host, or a detached HEAD.
	Idle time.Duration
	// ErrorMin/ErrorMax bound the exponential backoff after CLI or upload errors.
	ErrorMin, ErrorMax time.Duration
	// Unsupported is the retry interval when the server does not implement the device RPCs.
	Unsupported time.Duration
	// BranchCheck is how often the local branch and remote are re-read while waiting; a change
	// triggers an immediate poll.
	BranchCheck time.Duration
	// Redetect is how long a CLI detection (version, logged-in hosts) is reused.
	Redetect time.Duration
	// Heartbeat re-uploads an unchanged snapshot after this long.
	Heartbeat time.Duration
	// Jitter is the random spread applied to every interval (0.1 = ±10%); negative disables it.
	Jitter float64
}

// DefaultSchedule: fast while CI runs, slow when idle.
var DefaultSchedule = Schedule{
	Pending:     15 * time.Second,
	PendingMax:  time.Minute,
	Open:        2 * time.Minute,
	NoPR:        5 * time.Minute,
	Closed:      15 * time.Minute,
	Idle:        15 * time.Minute,
	ErrorMin:    30 * time.Second,
	ErrorMax:    15 * time.Minute,
	Unsupported: time.Hour,
	BranchCheck: 30 * time.Second,
	Redetect:    30 * time.Minute,
	Heartbeat:   15 * time.Minute,
	Jitter:      0.1,
}

func (s Schedule) withDefaults() Schedule {
	d := DefaultSchedule
	set := func(v *time.Duration, def time.Duration) {
		if *v <= 0 {
			*v = def
		}
	}
	set(&s.Pending, d.Pending)
	set(&s.PendingMax, d.PendingMax)
	set(&s.Open, d.Open)
	set(&s.NoPR, d.NoPR)
	set(&s.Closed, d.Closed)
	set(&s.Idle, d.Idle)
	set(&s.ErrorMin, d.ErrorMin)
	set(&s.ErrorMax, d.ErrorMax)
	set(&s.Unsupported, d.Unsupported)
	set(&s.BranchCheck, d.BranchCheck)
	set(&s.Redetect, d.Redetect)
	set(&s.Heartbeat, d.Heartbeat)
	if s.Jitter == 0 {
		s.Jitter = d.Jitter
	}
	return s
}

// Options configure a Watcher.
type Options struct {
	Reporter Reporter
	// Dir is the session's working directory.
	Dir string
	// Providers default to gh and glab with a default Runner.
	Providers []Provider
	// Remote and Branch read the git remote URL and the current branch of Dir; defaults
	// workspace.GitRemote and workspace.GitBranch.
	Remote func(ctx context.Context, dir string) string
	Branch func(ctx context.Context, dir string) string
	// Detections caches CLI detection; default a process-wide cache shared by all sessions.
	Detections *DetectCache
	Schedule   Schedule
	Logger     *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// Watcher polls the PR/CI state of one session's branch through a device provider and uploads it.
// Run it in a goroutine; it stops when its context is cancelled.
type Watcher struct {
	o     Options
	sched Schedule
	log   *slog.Logger
	poke  chan struct{}

	// dir is the session's current directory (Options.Dir until SetDir).
	dirMu sync.Mutex
	dir   string

	// Poll state (owned by Run).
	reported      []byte // fingerprint of the last successful ReportDeviceProviders
	lastUpload    []byte
	lastUploadAt  time.Time
	lastBranch    string
	lastRemote    string
	pendingStreak int
	errorStreak   int
}

// NewWatcher creates a watcher.
func NewWatcher(o Options) *Watcher {
	if len(o.Providers) == 0 {
		o.Providers = defaultProviders
	}
	if o.Remote == nil {
		o.Remote = workspace.GitRemote
	}
	if o.Branch == nil {
		o.Branch = workspace.GitBranch
	}
	if o.Detections == nil {
		o.Detections = sharedDetections
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{o: o, sched: o.Schedule.withDefaults(), log: log.With("component", "vcs"), poke: make(chan struct{}, 1)}
}

// Dir returns the directory the watcher follows.
func (w *Watcher) Dir() string {
	w.dirMu.Lock()
	defer w.dirMu.Unlock()
	if w.dir == "" {
		return w.o.Dir
	}
	return w.dir
}

// SetDir follows the session to another directory, from the next poll on (which it triggers).
func (w *Watcher) SetDir(dir string) {
	w.dirMu.Lock()
	w.dir = dir
	w.dirMu.Unlock()
	w.Poke()
}

// Poke asks for a poll now (e.g. after a push or `gh pr create`). It never blocks.
func (w *Watcher) Poke() {
	select {
	case w.poke <- struct{}{}:
	default:
	}
}

// Run polls until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	for {
		delay := w.Tick(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.wait(ctx, w.jitter(delay)); err != nil {
			return err
		}
	}
}

// wait sleeps for d, waking early on Poke, cancellation, or a change of the branch or remote.
func (w *Watcher) wait(ctx context.Context, d time.Duration) error {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	check := time.NewTicker(w.sched.BranchCheck)
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return nil
		case <-w.poke:
			return nil
		case <-check.C:
			if w.o.Branch(ctx, w.Dir()) != w.lastBranch || w.o.Remote(ctx, w.Dir()) != w.lastRemote {
				return nil
			}
		}
	}
}

func (w *Watcher) jitter(d time.Duration) time.Duration {
	if w.sched.Jitter <= 0 {
		return d
	}
	f := 1 + w.sched.Jitter*(2*rand.Float64()-1)
	return time.Duration(float64(d) * f)
}

// Tick runs one poll and returns the delay until the next one.
func (w *Watcher) Tick(ctx context.Context) time.Duration {
	remote := w.o.Remote(ctx, w.Dir())
	branch := w.o.Branch(ctx, w.Dir())
	if remote != w.lastRemote || branch != w.lastBranch {
		w.pendingStreak = 0
		w.lastUpload = nil
	}
	w.lastRemote, w.lastBranch = remote, branch
	repo, ok := ParseRemote(remote)
	if !ok {
		// Outside a repository with a code-host remote the CLIs are not even started.
		return w.sched.Idle
	}

	detected, err := w.detectAndReport(ctx)
	if err != nil {
		return w.failed(err)
	}
	if branch == "" || branch == "HEAD" {
		return w.sched.Idle
	}
	var prov Provider
	for i, p := range w.o.Providers {
		if d := detected[i]; d != nil && covers(d, repo.Host) {
			prov = p
			break
		}
	}
	if prov == nil {
		w.log.Debug("no logged-in device provider for host", "host", repo.Host)
		return w.sched.Idle
	}
	// TODO(contract): VcsService has no signal that a server provider (GitHub App, GitLab OAuth)
	// already covers this repository, so the device provider always polls. Skip the repo here once
	// the server reports covered repositories.

	snap, err := prov.Snapshot(ctx, repo, branch)
	if err != nil {
		w.log.Debug("device provider poll failed", "provider", prov.Type(), "repo", repo.Key, "err", err)
		return w.failed(err)
	}
	if len(snap.PullRequests) == 0 {
		w.errorStreak, w.pendingStreak = 0, 0
		return w.sched.NoPR
	}
	if err := w.upload(ctx, prov.Type(), snap); err != nil {
		return w.failed(err)
	}
	w.errorStreak = 0
	switch {
	case snap.pending():
		d := float64(w.sched.Pending)
		for range w.pendingStreak {
			d *= 1.5
		}
		w.pendingStreak++
		return min(time.Duration(d), w.sched.PendingMax)
	case snap.hasOpen():
		w.pendingStreak = 0
		return w.sched.Open
	default:
		w.pendingStreak = 0
		return w.sched.Closed
	}
}

// failed records an error and returns the backoff delay.
func (w *Watcher) failed(err error) time.Duration {
	if errors.Is(err, ErrUnsupported) {
		w.log.Debug("server does not support device providers", "err", err)
		return w.sched.Unsupported
	}
	d := w.sched.ErrorMin
	for i := 0; i < w.errorStreak && d < w.sched.ErrorMax; i++ {
		d *= 2
	}
	w.errorStreak++
	return min(d, w.sched.ErrorMax)
}

// detectAndReport detects every provider (cached) and reports them when the set changed since the
// last successful report. The result is aligned with o.Providers; missing CLIs are nil.
func (w *Watcher) detectAndReport(ctx context.Context) ([]*vcsv1.DeviceProvider, error) {
	out := make([]*vcsv1.DeviceProvider, len(w.o.Providers))
	req := &vcsv1.ReportDeviceProvidersRequest{}
	for i, p := range w.o.Providers {
		d, err := w.o.Detections.Get(ctx, p, w.sched.Redetect, w.o.Now())
		if err != nil {
			if !errors.Is(err, ErrNotInstalled) {
				w.log.Debug("device provider detection failed", "provider", p.Type(), "err", err)
			}
			continue
		}
		out[i] = d
		req.Providers = append(req.Providers, d)
	}
	fp := fingerprint(req)
	if string(fp) == string(w.reported) {
		return out, nil
	}
	if _, err := w.o.Reporter.ReportDeviceProviders(ctx, req); err != nil {
		return out, err
	}
	w.reported = fp
	return out, nil
}

// upload sends the snapshot unless it is unchanged since the last upload and the heartbeat is not
// due.
func (w *Watcher) upload(ctx context.Context, typ vcsv1.ProviderType, snap *Snapshot) error {
	req := &vcsv1.ReportDeviceSnapshotRequest{Provider: typ, PullRequests: snap.PullRequests, Checks: snap.Checks}
	fp := fingerprint(req)
	now := w.o.Now()
	if string(fp) == string(w.lastUpload) && now.Sub(w.lastUploadAt) < w.sched.Heartbeat {
		return nil
	}
	req.ObservedAt = now.UTC().Format(time.RFC3339)
	resp, err := w.o.Reporter.ReportDeviceSnapshot(ctx, req)
	if err != nil {
		return err
	}
	w.lastUpload, w.lastUploadAt = fp, now
	if n := resp.GetEventsEmitted(); n > 0 {
		w.log.Debug("device snapshot produced events", "events", n)
	}
	return nil
}

func fingerprint(m proto.Message) []byte {
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	sum := sha256.Sum256(b)
	return sum[:]
}

// DetectCache reuses provider detections for a while, so several sessions in one process do not run
// `gh auth status` each.
type DetectCache struct {
	mu      sync.Mutex
	entries map[Provider]detectEntry
}

type detectEntry struct {
	p   *vcsv1.DeviceProvider
	err error
	at  time.Time
}

var (
	sharedDetections = &DetectCache{}
	// defaultProviders are shared so the process-wide cache recognizes them across sessions.
	defaultProviders = []Provider{&GH{}, &GLab{}}
)

// Get returns the cached detection of p when younger than ttl, else detects again. Timeouts and other
// transient failures are cached too, so a hanging CLI is not retried on every poll.
func (c *DetectCache) Get(ctx context.Context, p Provider, ttl time.Duration, now time.Time) (*vcsv1.DeviceProvider, error) {
	c.mu.Lock()
	e, ok := c.entries[p]
	c.mu.Unlock()
	if ok && e.err != nil && !errors.Is(e.err, ErrNotInstalled) {
		ttl /= 6 // transient failures (timeouts, crashes) are retried sooner
	}
	if ok && now.Sub(e.at) < ttl {
		return e.p, e.err
	}
	d, err := p.Detect(ctx)
	if ctx.Err() != nil { // a cancelled session must not poison the cache
		return d, err
	}
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[Provider]detectEntry{}
	}
	c.entries[p] = detectEntry{p: d, err: err, at: now}
	c.mu.Unlock()
	return d, err
}
