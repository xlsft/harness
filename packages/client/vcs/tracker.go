package vcs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Tracker is the session's bound pull request: what PrStatus, PrBind, PrUnbind, PrMonitor and PrAutoMerge act on, and
// the poll behind them. It shares the Watcher's CLI layer, Schedule and detection cache; the Watcher uploads the
// branch's PRs to the server, the Tracker follows the one PR the session is bound to, for the session.
//
// While a PR is bound and open it is polled on the Schedule (fast while CI runs, slow when settled, backing off on
// errors). The poll ends when the session closes or the PR is merged or closed. With monitoring on, a CI failure, a
// conflict or a new comment becomes a PRNote that wakes the session.
//
// The binding is a device-local file, one per session (StateDir/<session id>.json), so the poll and what was already
// reported survive a client restart. Nothing in it comes from or goes to a token: the CLIs keep their own auth.
type Tracker struct {
	o     TrackerOptions
	sched Schedule
	log   *slog.Logger
	poke  chan struct{}

	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	hooks  TrackerHooks
	st     *prState
	closed bool
	// gen counts bindings: a poll that started under another binding is dropped.
	gen     int
	looping bool
	// commentsAt and commentsFor are when comments were last read and the PR's updated_at then; failSince is when the
	// oldest unreported failure was first seen; notesAt the waking notes of the last hour.
	commentsAt  time.Time
	commentsFor string
	failSince   time.Time
	notesAt     []time.Time

	// refOnce computes ref, the session's opaque reference in the marker.
	refOnce sync.Once
	ref     string

	// manual: no loop, tests call tick.
	manual bool

	// Poll state (owned by the loop).
	pendingStreak, errorStreak int
}

// TrackerOptions configure a Tracker.
type TrackerOptions struct {
	SessionID string
	// Dir is the session's working directory.
	Dir string
	// StateDir holds the bindings; empty keeps the binding in memory only.
	StateDir string
	// Disabled refuses the tools and never polls (HARNESS_NO_VCS=1).
	Disabled bool
	// Providers default to gh and glab with a default Runner.
	Providers []Provider
	// SaltFile holds the client's salt for the marker's session reference; empty keeps a salt in memory.
	SaltFile string
	// Remote and Branch default to workspace.GitRemote and workspace.GitBranch.
	Remote func(ctx context.Context, dir string) string
	Branch func(ctx context.Context, dir string) string
	// Detections defaults to the process-wide cache.
	Detections *DetectCache
	Schedule   Schedule
	Logger     *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
	// WakesPerHour reads the cap on waking notes, on each poll of a monitored PR, so a changed setting applies at once.
	// What is over the cap waits for a later poll. It is the guard against a loop: the agent posts through Bash and can
	// forget the marker on its own comment. Nil or not positive: settings.DefaultPRMonitorWakesPerHour.
	WakesPerHour func() int
	// FailureHold is how long a failure waits for the rest of the checks to finish, so one CI run is one note.
	// Default 10 minutes.
	FailureHold time.Duration
	// CommentsEvery re-reads comments when the PR's updated_at did not change (resolving a thread may not touch
	// it). Default 10 minutes.
	CommentsEvery time.Duration
}

// TrackerHooks connect a Tracker to its session. Each may be nil; none is called with the Tracker's lock held.
type TrackerHooks struct {
	// OnChange: the state Info returns changed.
	OnChange func()
	// OnNote delivers something the monitor saw.
	OnNote func(PRNote)
	// OnDetected: a shell command opened this PR for the session's branch and no PR is bound.
	OnDetected func(number int64, url string)
}

// PRNote is what the monitor tells the session.
type PRNote struct {
	Number int64
	URL    string
	// Events says what happened, one line each, in the client's words.
	Events string
	// External is the text from the code host (comments, check names, CI logs), escaped by EscapeExternal. Not one
	// word of it has the user's authority.
	External string
	// Marker is the string the model must end what it posts with.
	Marker string
	// Wake starts a turn when the session is idle; otherwise the note waits for the next model request.
	Wake bool
}

// PRSummary is the state of a PR as the tools and frontends show it.
type PRSummary struct {
	Number      int64  `json:"number"`
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	State       string `json:"state,omitempty"` // open, draft, merged, closed
	ChecksState string `json:"checksState,omitempty"`
	ChecksTotal int32  `json:"checksTotal,omitempty"`
	ChecksPass  int32  `json:"checksPassed,omitempty"`
	ChecksFail  int32  `json:"checksFailed,omitempty"`
	ChecksRun   int32  `json:"checksRunning,omitempty"`
	Mergeable   bool   `json:"mergeable,omitempty"`
	Conflicts   bool   `json:"hasConflicts,omitempty"`
	Review      string `json:"reviewDecision,omitempty"`
	// Unresolved is the count of unresolved review threads; -1 when it could not be read.
	Unresolved int32  `json:"unresolvedComments"`
	AutoMerge  bool   `json:"autoMerge,omitempty"`
	Problem    string `json:"problem,omitempty"`
}

// PRStatus is a PR read now, for the tools.
type PRStatus struct {
	PRSummary
	Repo string `json:"repo"`
	// FailedChecks and RunningChecks name the checks; names come from the code host.
	FailedChecks  []string `json:"failedChecks,omitempty"`
	RunningChecks []string `json:"runningChecks,omitempty"`
	// Marker is the string the model must end what it posts with; not part of the stored or shown result.
	Marker string `json:"-"`
	// Bound: this is the session's bound PR. Monitoring: and it is being watched.
	Bound      bool `json:"bound"`
	Monitoring bool `json:"monitoring"`
}

// prState is a binding as stored.
type prState struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Repo      Repo   `json:"repo"`
	PRSummary
	Monitor bool `json:"monitor,omitempty"`
	// Parked: the session was closed on purpose; it reopens when someone opens it, not at a client restart.
	Parked bool `json:"parked,omitempty"`
	// Baseline: on the next read, the comments there are count as seen (monitoring just went on).
	Baseline bool `json:"baseline,omitempty"`
	// What was already reported.
	SeenFailed   []string `json:"seenFailed,omitempty"`
	SeenConflict bool     `json:"seenConflict,omitempty"`
	SeenComments []string `json:"seenComments,omitempty"`
}

func (s *prState) open() bool { return s.State != "merged" && s.State != "closed" }

const (
	maxSeenComments = 2000
	// What one note carries at most.
	noteMaxComments   = 10
	noteMaxLogs       = 3
	noteCommentBytes  = 2000
	noteLogBytes      = 3000
	noteLogLines      = 40
	noteExternalBytes = 16000
)

// NewTracker creates a Tracker; it polls nothing until Start or a bind.
func NewTracker(o TrackerOptions) *Tracker {
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
	if o.WakesPerHour == nil {
		o.WakesPerHour = func() int { return settings.DefaultPRMonitorWakesPerHour }
	}
	if o.FailureHold <= 0 {
		o.FailureHold = 10 * time.Minute
	}
	if o.CommentsEvery <= 0 {
		o.CommentsEvery = 10 * time.Minute
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &Tracker{o: o, sched: o.Schedule.withDefaults(), log: log.With("component", "vcs.pr"), poke: make(chan struct{}, 1), base: ctx, cancel: cancel}
	if !o.Disabled {
		t.st = loadPRState(o.StateDir, o.SessionID)
	}
	return t
}

// SetHooks connects the Tracker to its session; call it before Start.
func (t *Tracker) SetHooks(h TrackerHooks) {
	t.mu.Lock()
	t.hooks = h
	t.mu.Unlock()
}

// Start resumes the poll of a stored binding, as the session opens (again).
func (t *Tracker) Start() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.st == nil || t.closed {
		return
	}
	if t.st.Parked || t.st.Cwd != t.o.Dir {
		t.st.Parked, t.st.Cwd = false, t.o.Dir
		t.saveLocked()
	}
	t.ensureLoopLocked()
}

// Stop ends the poll. The binding stays for the next open.
func (t *Tracker) Stop() {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	t.cancel()
	t.wg.Wait()
}

// Park records that the session was closed on purpose: a client restart does not reopen it for its PR.
func (t *Tracker) Park() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.st != nil && !t.st.Parked {
		t.st.Parked = true
		t.saveLocked()
	}
}

// Poke asks for a poll now. It never blocks.
func (t *Tracker) Poke() {
	select {
	case t.poke <- struct{}{}:
	default:
	}
}

// Marker is the string for the model to end what it posts on a PR with (marker.go), kind reply; the model may change the
// kind only.
func (t *Tracker) Marker() string {
	t.refOnce.Do(func() { t.ref = sessionRef(loadMarkerSalt(t.o.SaltFile), t.o.SessionID) })
	return buildMarker(t.ref, MarkerKindReply, t.o.Now())
}

// Info is the bound PR for SessionInfo.pull_request (nil when none).
func (t *Tracker) Info() *agentv1.PullRequest {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st.info()
}

func (s *prState) info() *agentv1.PullRequest {
	if s == nil {
		return nil
	}
	pr := &agentv1.PullRequest{
		Url: s.URL, Number: int32(s.Number), Title: s.Title, ChecksState: s.ChecksState,
		ChecksTotal: s.ChecksTotal, ChecksPassed: s.ChecksPass, ChecksFailed: s.ChecksFail,
		Mergeable: s.Mergeable, HasConflicts: s.Conflicts, ReviewDecision: s.Review,
		UnresolvedComments: max(s.Unresolved, 0), Monitoring: s.Monitor, AutoMerge: s.AutoMerge, Problem: s.Problem,
	}
	switch s.State {
	case "draft":
		pr.State = agentv1.PullRequest_STATE_DRAFT
	case "merged":
		pr.State = agentv1.PullRequest_STATE_MERGED
	case "closed":
		pr.State = agentv1.PullRequest_STATE_CLOSED
	default:
		pr.State = agentv1.PullRequest_STATE_OPEN
	}
	return pr
}

// ---- Tool operations ----

// Status reads a PR now: ref, else the bound PR, else the open PR of the session's branch.
func (t *Tracker) Status(ctx context.Context, ref string) (*PRStatus, error) {
	if err := t.enabled(); err != nil {
		return nil, err
	}
	repo, number, err := t.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	return t.read(ctx, repo, number)
}

// Bind attaches a PR to the session. Binding another PR replaces the old one and keeps monitoring as it was.
func (t *Tracker) Bind(ctx context.Context, ref string) (*PRStatus, error) {
	if err := t.enabled(); err != nil {
		return nil, err
	}
	repo, number, err := t.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	status, err := t.read(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	t.bind(repo, status)
	return t.read(ctx, repo, number)
}

func (t *Tracker) bind(repo Repo, status *PRStatus) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	same := t.st != nil && t.st.Repo.Key == repo.Key && t.st.Number == status.Number
	if !same {
		monitor := t.st != nil && t.st.Monitor
		t.st = &prState{SessionID: t.o.SessionID, Cwd: t.o.Dir, Repo: repo, Monitor: monitor, Baseline: monitor}
		t.gen++
		t.commentsAt, t.commentsFor, t.failSince = time.Time{}, "", time.Time{}
	}
	t.st.PRSummary = status.PRSummary
	if !t.st.open() {
		t.st.Monitor = false
	}
	t.saveLocked()
	t.ensureLoopLocked()
	t.mu.Unlock()
	t.changed()
}

// Unbind detaches the bound PR and ends its poll; false when none was bound.
func (t *Tracker) Unbind() (bool, error) {
	if err := t.enabled(); err != nil {
		return false, err
	}
	t.mu.Lock()
	if t.st == nil {
		t.mu.Unlock()
		return false, nil
	}
	t.st = nil
	t.gen++
	RemovePRState(t.o.StateDir, t.o.SessionID)
	t.mu.Unlock()
	t.Poke() // ends the sleeping loop
	t.changed()
	return true, nil
}

// SetMonitor turns watching on or off. On, without a bound PR, binds the open PR of the session's branch. A CI failure
// that is already there is reported at the next poll; comments that are already there are not.
func (t *Tracker) SetMonitor(ctx context.Context, on bool) (*PRStatus, error) {
	if err := t.enabled(); err != nil {
		return nil, err
	}
	if !on {
		t.mu.Lock()
		if t.st == nil {
			t.mu.Unlock()
			return nil, nil
		}
		t.st.Monitor, t.st.Baseline = false, false
		t.saveLocked()
		repo, number := t.st.Repo, t.st.Number
		t.mu.Unlock()
		t.changed()
		return &PRStatus{PRSummary: PRSummary{Number: number}, Repo: repo.Key, Bound: true}, nil
	}
	repo, number, err := t.resolve(ctx, "")
	if err != nil {
		return nil, err
	}
	status, err := t.read(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	if status.State == "merged" || status.State == "closed" {
		return nil, fmt.Errorf("pull request #%d is %s: there is nothing to watch", number, status.State)
	}
	t.bind(repo, status)
	t.mu.Lock()
	if t.st != nil {
		t.st.Monitor, t.st.Baseline = true, true
		t.st.SeenFailed, t.st.SeenConflict, t.st.SeenComments = nil, false, nil
		t.failSince = time.Time{}
		t.saveLocked()
	}
	t.mu.Unlock()
	t.Poke()
	t.changed()
	status.Bound, status.Monitoring = true, true
	return status, nil
}

// SetAutoMerge sets auto-merge on the bound PR, else the open PR of the session's branch, and reads the PR back. The
// user's approval is the caller's business (PrAutoMerge always asks).
func (t *Tracker) SetAutoMerge(ctx context.Context, on bool, method string) (*PRStatus, error) {
	if err := t.enabled(); err != nil {
		return nil, err
	}
	switch method {
	case "", MergeMerge, MergeSquash, MergeRebase:
	default:
		return nil, fmt.Errorf("method must be merge, squash or rebase, not %q", method)
	}
	repo, number, err := t.resolve(ctx, "")
	if err != nil {
		return nil, err
	}
	prov, _, err := t.provider(ctx, repo, true)
	if err != nil {
		return nil, err
	}
	if err := prov.SetAutoMerge(ctx, repo, number, on, method); err != nil {
		return nil, err
	}
	t.Poke()
	return t.read(ctx, repo, number)
}

// ObserveCommand looks at a shell command that ended: one that opens a PR makes the Tracker look for the branch's PR,
// so the session can be offered to watch it. That takes one CLI call, and only after such a command.
func (t *Tracker) ObserveCommand(command string) {
	if t.o.Disabled || !prCreateRe.MatchString(command) {
		return
	}
	t.mu.Lock()
	if t.closed || t.st != nil {
		t.mu.Unlock()
		return
	}
	t.wg.Add(1)
	t.mu.Unlock()
	go func() {
		defer t.wg.Done()
		_, pr, err := t.branchPR(t.base, false)
		if err != nil {
			t.log.Debug("no pull request detected", "err", err)
			return
		}
		t.mu.Lock()
		bound := t.st != nil
		h := t.hooks.OnDetected
		t.mu.Unlock()
		if !bound && h != nil && isOpen(pr) {
			h(pr.GetNumber(), pr.GetUrl())
		}
	}()
}

var prCreateRe = regexp.MustCompile(`\b(?:gh\s+pr\s+create|glab\s+mr\s+create)\b`)

func (t *Tracker) enabled() error {
	if t.o.Disabled {
		return errors.New("pull request tools are off on this client (HARNESS_NO_VCS=1)")
	}
	return nil
}

// resolve finds what ref means: a URL names its repository, a number is in the bound PR's repository or the session's.
// Empty is the bound PR, else the open PR of the session's branch.
func (t *Tracker) resolve(ctx context.Context, ref string) (Repo, int64, error) {
	t.mu.Lock()
	var boundRepo Repo
	var boundNumber int64
	if t.st != nil {
		boundRepo, boundNumber = t.st.Repo, t.st.Number
	}
	t.mu.Unlock()
	if strings.TrimSpace(ref) == "" {
		if boundNumber > 0 {
			return boundRepo, boundNumber, nil
		}
		repo, pr, err := t.branchPR(ctx, true)
		if err != nil {
			return Repo{}, 0, err
		}
		return repo, pr.GetNumber(), nil
	}
	repo, number, err := parsePRRef(ref)
	if err != nil {
		return Repo{}, 0, err
	}
	if repo.Key != "" {
		return repo, number, nil
	}
	if local, err := t.localRepo(ctx); err == nil {
		return local, number, nil
	} else if boundNumber > 0 {
		return boundRepo, number, nil
	} else {
		return Repo{}, 0, err
	}
}

func (t *Tracker) localRepo(ctx context.Context) (Repo, error) {
	repo, ok := ParseRemote(t.o.Remote(ctx, t.o.Dir))
	if !ok {
		return Repo{}, fmt.Errorf("%s has no git remote on a code host, so a pull request cannot be found from it; pass the full URL", t.o.Dir)
	}
	return repo, nil
}

// branchPR finds the most relevant PR of the session's branch (Provider.Snapshot sorts open ones first).
func (t *Tracker) branchPR(ctx context.Context, fresh bool) (Repo, *vcsv1.PullRequest, error) {
	repo, err := t.localRepo(ctx)
	if err != nil {
		return Repo{}, nil, err
	}
	branch := t.o.Branch(ctx, t.o.Dir)
	if branch == "" || branch == "HEAD" {
		return Repo{}, nil, fmt.Errorf("%s is not on a branch, so it has no pull request of its own; pass its number or URL", t.o.Dir)
	}
	prov, _, err := t.provider(ctx, repo, fresh)
	if err != nil {
		return Repo{}, nil, err
	}
	snap, err := prov.Snapshot(ctx, repo, branch)
	if err != nil {
		return Repo{}, nil, err
	}
	if len(snap.PullRequests) == 0 {
		return Repo{}, nil, fmt.Errorf("branch %s has no pull request in %s", branch, repo.Key)
	}
	return repo, snap.PullRequests[0], nil
}

// provider picks the CLI logged in to the repo's host, from cached detections. fresh (a tool call, someone may have
// just logged in) detects again before giving up.
func (t *Tracker) provider(ctx context.Context, repo Repo, fresh bool) (PRProvider, string, error) {
	ttls := []time.Duration{t.sched.Redetect}
	if fresh {
		ttls = append(ttls, 0)
	}
	detected := make([]*vcsv1.DeviceProvider, len(t.o.Providers))
	fails := make([]error, len(t.o.Providers))
	for _, ttl := range ttls {
		for i, p := range t.o.Providers {
			d, err := t.o.Detections.Get(ctx, p, ttl, t.o.Now())
			detected[i], fails[i] = d, err
			if err != nil || !covers(d, repo.Host) {
				continue
			}
			pp, ok := p.(PRProvider)
			if !ok {
				return nil, "", fmt.Errorf("the %s provider does not support pull request tools", p.Type())
			}
			login := ""
			for _, h := range d.GetHosts() {
				if strings.EqualFold(h.GetHost(), repo.Host) {
					login = h.GetLogin()
				}
			}
			return pp, login, nil
		}
	}
	return nil, "", noProviderError(repo.Host, t.o.Providers, detected, fails)
}

// read gets a PR from the host; when it is the bound one, the binding takes the news.
func (t *Tracker) read(ctx context.Context, repo Repo, number int64) (*PRStatus, error) {
	prov, _, err := t.provider(ctx, repo, true)
	if err != nil {
		return nil, err
	}
	view, err := prov.ViewPR(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	unresolved := int32(-1)
	if _, n, err := prov.PRComments(ctx, repo, number); err == nil {
		unresolved = int32(n)
	} else {
		t.log.Debug("reading comments failed", "pr", number, "err", err)
	}
	status := &PRStatus{PRSummary: summaryOf(view, unresolved), Repo: repo.Key, Marker: t.Marker()}
	for _, c := range view.Checks {
		switch c.GetState() {
		case vcsv1.CheckState_CHECK_STATE_FAILURE:
			status.FailedChecks = append(status.FailedChecks, c.GetName())
		case vcsv1.CheckState_CHECK_STATE_QUEUED, vcsv1.CheckState_CHECK_STATE_RUNNING:
			status.RunningChecks = append(status.RunningChecks, c.GetName())
		}
	}
	t.mu.Lock()
	changed := false
	if st := t.st; st != nil && st.Repo.Key == repo.Key && st.Number == number {
		status.Bound, status.Monitoring = true, st.Monitor
		if unresolved < 0 {
			status.Unresolved = st.Unresolved
		}
		if changed = st.PRSummary != status.PRSummary; changed {
			st.PRSummary = status.PRSummary
			t.saveLocked()
		}
	}
	t.mu.Unlock()
	if changed {
		t.changed()
	}
	return status, nil
}

func summaryOf(view *PRView, unresolved int32) PRSummary {
	pr := view.PR
	s := PRSummary{
		Number: pr.GetNumber(), URL: pr.GetUrl(), Title: pr.GetTitle(), Mergeable: pr.GetMergeable(), Conflicts: pr.GetHasConflicts(),
		Review: pr.GetReviewState(), AutoMerge: pr.GetAutoMergeEnabled(), Unresolved: unresolved,
	}
	switch pr.GetState() {
	case agentv1.PullRequest_STATE_DRAFT:
		s.State = "draft"
	case agentv1.PullRequest_STATE_MERGED:
		s.State = "merged"
	case agentv1.PullRequest_STATE_CLOSED:
		s.State = "closed"
	default:
		s.State = "open"
	}
	c := pr.GetChecks()
	s.ChecksTotal, s.ChecksPass, s.ChecksFail, s.ChecksRun = c.GetTotal(), c.GetPassed(), c.GetFailed(), c.GetRunning()
	switch {
	case c.GetTotal() == 0:
		s.ChecksState = "none"
	case c.GetRunning() > 0:
		s.ChecksState = "running"
	case c.GetFailed() > 0:
		s.ChecksState = "failing"
	default:
		s.ChecksState = "passing"
	}
	return s
}

func (t *Tracker) changed() {
	t.mu.Lock()
	h := t.hooks.OnChange
	t.mu.Unlock()
	if h != nil {
		h()
	}
}

// ---- The poll ----

func (t *Tracker) ensureLoopLocked() {
	if t.looping || t.manual || t.closed || t.st == nil || !t.st.open() {
		return
	}
	t.looping = true
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		t.loop(t.base)
	}()
}

func (t *Tracker) loop(ctx context.Context) {
	for {
		delay, stop := t.tick(ctx)
		if ctx.Err() == nil && !stop {
			timer := time.NewTimer(t.jitter(delay))
			select {
			case <-ctx.Done():
			case <-timer.C:
			case <-t.poke:
			}
			timer.Stop()
		}
		t.mu.Lock()
		if ctx.Err() != nil || t.st == nil || !t.st.open() {
			t.looping = false
			t.mu.Unlock()
			return
		}
		t.mu.Unlock()
	}
}

func (t *Tracker) jitter(d time.Duration) time.Duration {
	if t.sched.Jitter <= 0 {
		return d
	}
	return time.Duration(float64(d) * (1 + t.sched.Jitter*(2*rand.Float64()-1)))
}

// tick runs one poll and returns the delay until the next one; stop means the PR ended (or nothing is bound).
func (t *Tracker) tick(ctx context.Context) (time.Duration, bool) {
	t.mu.Lock()
	if t.st == nil {
		t.mu.Unlock()
		return 0, true
	}
	gen, repo, number := t.gen, t.st.Repo, t.st.Number
	t.mu.Unlock()

	prov, login, err := t.provider(ctx, repo, false)
	if err != nil {
		t.setProblem(gen, err)
		return t.sched.Idle, false
	}
	view, err := prov.ViewPR(ctx, repo, number)
	if err != nil {
		if ctx.Err() != nil {
			return 0, true
		}
		t.log.Debug("poll failed", "pr", number, "err", err)
		t.setProblem(gen, err)
		d := t.sched.ErrorMin
		for i := 0; i < t.errorStreak && d < t.sched.ErrorMax; i++ {
			d *= 2
		}
		t.errorStreak++
		return min(d, t.sched.ErrorMax), false
	}
	t.errorStreak = 0

	var comments []Comment
	unresolved, fetched := int32(-1), false
	if isOpen(view.PR) && t.commentsDue(view.PR.GetUpdatedAt()) {
		cs, n, err := prov.PRComments(ctx, repo, number)
		if err == nil {
			comments, unresolved, fetched = cs, int32(n), true
		} else {
			t.log.Debug("reading comments failed", "pr", number, "err", err)
		}
	}
	limit := 0
	if t.monitoring() {
		limit = t.o.WakesPerHour() // reads settings: not under the lock
	}
	if limit <= 0 {
		limit = settings.DefaultPRMonitorWakesPerHour
	}
	res := t.apply(gen, view, comments, unresolved, fetched, login, limit)
	if res.stale {
		return t.sched.ErrorMin, false // a bind or unbind came in; the next poll reads what is bound now
	}
	if res.changed {
		t.changed()
	}
	if res.note != nil {
		t.deliver(ctx, prov, repo, res.note)
	}
	switch {
	case !isOpen(view.PR):
		return 0, true
	case view.PR.GetChecks().GetRunning() > 0:
		d := float64(t.sched.Pending)
		for range t.pendingStreak {
			d *= 1.5
		}
		t.pendingStreak++
		return min(time.Duration(d), t.sched.PendingMax), false
	default:
		t.pendingStreak = 0
		return t.sched.Open, false
	}
}

func (t *Tracker) setProblem(gen int, err error) {
	t.mu.Lock()
	changed := false
	if t.st != nil && t.gen == gen && t.st.Problem != err.Error() {
		t.st.Problem = err.Error()
		t.saveLocked()
		changed = true
	}
	t.mu.Unlock()
	if changed {
		t.changed()
	}
}

func (t *Tracker) monitoring() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st != nil && t.st.Monitor
}

func (t *Tracker) commentsDue(updatedAt string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st != nil && (t.st.Baseline || updatedAt != t.commentsFor || t.o.Now().Sub(t.commentsAt) >= t.o.CommentsEvery)
}

// pendingNote is what apply found to report; deliver adds the CI logs, which take CLI calls.
type pendingNote struct {
	number   int64
	url      string
	failed   []*vcsv1.Check
	conflict bool
	comments []Comment
	ended    string
}

type applied struct {
	stale, changed bool
	note           *pendingNote
}

// apply takes a poll's result into the binding and decides what to report.
func (t *Tracker) apply(gen int, view *PRView, comments []Comment, unresolved int32, fetched bool, login string, limit int) applied {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.st
	if st == nil || t.gen != gen {
		return applied{stale: true}
	}
	now := t.o.Now()
	if fetched {
		t.commentsAt, t.commentsFor = now, view.PR.GetUpdatedAt()
	} else {
		unresolved = st.Unresolved
	}
	sum := summaryOf(view, unresolved)
	res := applied{changed: sum != st.PRSummary}
	wasMonitoring := st.Monitor
	st.PRSummary = sum

	switch {
	case !st.open():
		st.Monitor, st.Baseline = false, false
		res.changed = res.changed || wasMonitoring
		if wasMonitoring {
			res.note = &pendingNote{number: st.Number, url: st.URL, ended: st.State}
		}
	case st.Monitor:
		res.note = t.monitorLocked(st, view, comments, fetched, login, limit, now)
	}
	t.saveLocked()
	return res
}

// monitorLocked compares a poll with what was reported. Over the hourly cap nothing counts as reported, so it all
// goes out with a later poll.
func (t *Tracker) monitorLocked(st *prState, view *PRView, comments []Comment, fetched bool, login string, limit int, now time.Time) *pendingNote {
	if st.Baseline && fetched {
		for _, c := range comments {
			st.SeenComments = append(st.SeenComments, c.ID)
		}
		st.Baseline = false
		comments = nil
	}
	note := &pendingNote{number: st.Number, url: st.URL}

	// Failures: a check that passes or runs again leaves the reported set, so failing again is news.
	var failing []string
	for _, c := range view.Checks {
		if c.GetState() == vcsv1.CheckState_CHECK_STATE_FAILURE {
			failing = append(failing, c.GetId())
			if !slices.Contains(st.SeenFailed, c.GetId()) {
				note.failed = append(note.failed, c)
			}
		}
	}
	seenFailed := slices.DeleteFunc(slices.Clone(st.SeenFailed), func(id string) bool { return !slices.Contains(failing, id) })
	if len(note.failed) == 0 {
		t.failSince = time.Time{}
	} else {
		if t.failSince.IsZero() {
			t.failSince = now
		}
		// The first failure of a run waits for the others, for a while.
		if view.PR.GetChecks().GetRunning() > 0 && now.Sub(t.failSince) < t.o.FailureHold {
			note.failed = nil
		} else {
			seenFailed = failing
		}
	}

	note.conflict = view.PR.GetHasConflicts() && !st.SeenConflict

	seenComments := st.SeenComments
	if fetched {
		for _, c := range comments {
			if slices.Contains(seenComments, c.ID) {
				continue
			}
			seenComments = append(seenComments, c.ID)
			// A marker keeps quiet only a comment from the logged-in account: that is the agent, in this session or
			// another (two sessions must not wake each other). Without one it is the user under the same account. From
			// another account it is news either way, since anyone can type an HTML comment.
			own := login != "" && strings.EqualFold(c.Author, login)
			if !c.Resolved && !(own && HasMarker(c.Body)) {
				note.comments = append(note.comments, c)
			}
		}
		if n := len(seenComments) - maxSeenComments; n > 0 {
			seenComments = seenComments[n:]
		}
	}

	hasNews := len(note.failed) > 0 || note.conflict || len(note.comments) > 0
	if hasNews {
		hour := now.Add(-time.Hour)
		t.notesAt = slices.DeleteFunc(t.notesAt, func(at time.Time) bool { return at.Before(hour) })
		if len(t.notesAt) >= limit {
			t.log.Debug("waking notes over the hourly cap; holding this one", "pr", st.Number)
			return nil
		}
		t.notesAt = append(t.notesAt, now)
	}
	st.SeenFailed, st.SeenConflict, st.SeenComments = seenFailed, view.PR.GetHasConflicts(), seenComments
	if !hasNews {
		return nil
	}
	if len(note.failed) > 0 {
		t.failSince = time.Time{}
	}
	return note
}

// deliver renders a note and hands it to the session.
func (t *Tracker) deliver(ctx context.Context, prov PRProvider, repo Repo, n *pendingNote) {
	note := PRNote{Number: n.number, URL: n.url, Marker: t.Marker(), Wake: n.ended == ""}
	var events []string
	var ext strings.Builder
	if n.ended != "" {
		events = append(events, fmt.Sprintf("The pull request was %s, so the client stopped watching it.", n.ended))
	}
	if k := len(n.failed); k > 0 {
		events = append(events, fmt.Sprintf("CI: %d %s failed.", k, plural(k, "check", "checks")))
		for i, c := range n.failed {
			fmt.Fprintf(&ext, "Check %q failed (%s).\n", c.GetName(), c.GetUrl())
			if c.GetSummary() != "" {
				ext.WriteString(c.GetSummary() + "\n")
			}
			if i < noteMaxLogs {
				log, err := prov.CheckLog(ctx, repo, c)
				if err != nil {
					t.log.Debug("reading a check log failed", "check", c.GetName(), "err", err)
				} else if tail := logTail(log); tail != "" {
					ext.WriteString("The end of its log:\n" + tail + "\n")
				}
			}
			ext.WriteString("\n")
		}
	}
	if n.conflict {
		events = append(events, "The branch now conflicts with its base branch.")
	}
	if k := len(n.comments); k > 0 {
		events = append(events, fmt.Sprintf("%d new %s.", k, plural(k, "comment", "comments")))
		shown := n.comments
		if len(shown) > noteMaxComments {
			shown = shown[len(shown)-noteMaxComments:]
			fmt.Fprintf(&ext, "(%d older new comments are not shown.)\n\n", k-noteMaxComments)
		}
		for _, c := range shown {
			fmt.Fprintf(&ext, "Comment by %s", c.Author)
			if c.Path != "" {
				fmt.Fprintf(&ext, " on %s", c.Path)
				if c.Line > 0 {
					fmt.Fprintf(&ext, ":%d", c.Line)
				}
			}
			if c.URL != "" {
				fmt.Fprintf(&ext, " (%s)", c.URL)
			}
			ext.WriteString(":\n" + truncateBytes(c.Body, noteCommentBytes) + "\n\n")
		}
	}
	note.Events = strings.Join(events, "\n")
	note.External = EscapeExternal(truncateBytes(strings.TrimSpace(ext.String()), noteExternalBytes))
	if note.External == "" {
		note.External = "(none)"
	}
	t.mu.Lock()
	h := t.hooks.OnNote
	t.mu.Unlock()
	if h != nil {
		h(note)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "… (cut)"
}

// logTail keeps the end of a log: where a failure usually says what it is.
func logTail(log string) string {
	lines := strings.Split(strings.TrimRight(log, "\n"), "\n")
	if len(lines) > noteLogLines {
		lines = lines[len(lines)-noteLogLines:]
	}
	s := strings.TrimSpace(strings.Join(lines, "\n"))
	if len(s) > noteLogBytes {
		s = "(cut) …" + strings.ToValidUTF8(s[len(s)-noteLogBytes:], "")
	}
	return s
}

var externalTagRe = regexp.MustCompile(`(?i)<(/?)(system-reminder|pr-external-data)`)

// EscapeExternal keeps code-host text from closing the <pr-external-data> tag around it or opening a system reminder
// of its own.
func EscapeExternal(s string) string {
	return externalTagRe.ReplaceAllString(s, "&lt;$1$2")
}

// ---- The stored binding ----

func prStatePath(dir, sessionID string) string { return filepath.Join(dir, sessionID+".json") }

func loadPRState(dir, sessionID string) *prState {
	if dir == "" || sessionID == "" {
		return nil
	}
	b, err := os.ReadFile(prStatePath(dir, sessionID))
	if err != nil {
		return nil
	}
	var st prState
	if json.Unmarshal(b, &st) != nil || st.Number <= 0 || st.Repo.Key == "" {
		return nil
	}
	st.SessionID = sessionID
	return &st
}

func (t *Tracker) saveLocked() {
	if t.o.StateDir == "" || t.st == nil {
		return
	}
	b, err := json.MarshalIndent(t.st, "", "  ")
	if err == nil {
		err = os.MkdirAll(t.o.StateDir, 0o700)
	}
	if err == nil {
		path := prStatePath(t.o.StateDir, t.o.SessionID)
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		t.log.Warn("saving the pull request binding failed", "err", err)
	}
}

// RemovePRState forgets a session's binding (the session was deleted, or its PR unbound).
func RemovePRState(dir, sessionID string) {
	if dir != "" && sessionID != "" {
		_ = os.Remove(prStatePath(dir, sessionID))
	}
}

// LoadPRInfo reads a session's bound PR without opening it, for session lists (nil when none).
func LoadPRInfo(dir, sessionID string) *agentv1.PullRequest {
	return loadPRState(dir, sessionID).info()
}

// PRWatch names a session that was watching its PR when the client stopped.
type PRWatch struct {
	SessionID string
	Cwd       string
}

// ListPRWatches finds the sessions to reopen at a client start: monitoring on, the PR open, not closed on purpose.
func ListPRWatches(dir string) []PRWatch {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var out []PRWatch
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".json")
		if st := loadPRState(dir, id); st != nil && st.Monitor && !st.Parked && st.open() && st.Cwd != "" {
			out = append(out, PRWatch{SessionID: id, Cwd: st.Cwd})
		}
	}
	return out
}
