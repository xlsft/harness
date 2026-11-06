// Package daemon is `harness-client daemon`: an always-on host that takes work from the server's queue, makes a checkout
// for it, runs it as an ordinary session and lands the result as a branch or pull request. docs/design/hosts.md has the
// design, the decisions and the isolation model.
//
// Runner is the part that runs sessions: on a personal host the daemon itself, on a team host one worker per member.
// Supervisor (supervisor.go) is the other part of a team host, which starts those workers under their own Unix users.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/server"
)

// Sessions is the local agent, server.Server through ServerSessions.
type Sessions interface {
	Create(ctx context.Context, req *agentv1.CreateSessionRequest) (string, error)
	SetTitle(ctx context.Context, id, title string) error
	Send(ctx context.Context, id, text string) error
	DeliverJob(ctx context.Context, id string, job *agentv1.AutoFixJob) error
	Close(ctx context.Context, id string) error
	// Live lists the open sessions.
	Live() []string
	// Status says how an open session is doing, and when it last did anything.
	Status(id string) (agentv1.SessionStatus, time.Time, bool)
	// Events follows an open session from where this process opened it; the channel closes with the session, or when the
	// subscriber falls behind (then call again). cancel releases it.
	Events(id string, after int64) (events <-chan *agentv1.SessionEvent, start int64, cancel func(), ok bool)
	// Publish tells the session's viewers what became of its work.
	Publish(id string, update *agentv1.HostWorkUpdated)
}

// Options configure a Runner.
type Options struct {
	ServerURL string
	HTTP      *http.Client
	Tokens    rpc.Tokens
	Sessions  Sessions
	Logger    *slog.Logger

	// Name is what the host is called. Default: the machine's name.
	Name string
	// Root holds the checkouts: Root/repos and Root/work. It must exist or be creatable.
	Root string
	// StateDir holds what survives a restart. Default: HARNESS_HOME/daemon.
	StateDir string
	// MaxSessions is what the daemon offers; 0 leaves the server's limit as it is.
	MaxSessions int
	// Isolation is what this process can promise. Default: none.
	Isolation relayv1.AgentHostIsolation
	// SandboxDriver names the OS sandbox of this host, "" for none.
	SandboxDriver string
	// IdleClose closes a session that has done nothing for this long (default 24 h). KeepWorkspaces removes a closed
	// session's checkout after this long (default 7 days).
	IdleClose, KeepWorkspaces time.Duration
	// ReportEvery, Tick and PollSeconds set the pace (defaults: 30 s, 2 s, 25 s). Tests shorten them.
	ReportEvery, Tick time.Duration
	PollSeconds       int32
	// Git overrides the git settings (tests allow file:// remotes).
	Git Git
}

// Runner takes and runs work.
type Runner struct {
	o   Options
	api relayv1connect.HostAgentServiceClient
	log *slog.Logger

	mu         sync.Mutex
	tracked    map[string]*tracked
	watching   map[string]bool
	preparing  int
	hostID     string
	reportKick chan struct{}
}

// tracked is a session this daemon made a checkout for. It is on disk, because such a session can reopen weeks later for a
// job, and its work then lands like any.
type tracked struct {
	SessionID     string `json:"sessionId"`
	WorkID        string `json:"workId,omitempty"`
	RepositoryKey string `json:"repositoryKey"`
	Dir           string `json:"dir"`
	Branch        string `json:"branch"`
	Base          string `json:"base"`
	Landing       int32  `json:"landing"`
	Title         string `json:"title,omitempty"`
	PullRequest   string `json:"pullRequest,omitempty"`
	ClosedAt      string `json:"closedAt,omitempty"`
}

// New makes a Runner; Run starts it.
func New(o Options) *Runner {
	if o.Name == "" {
		o.Name, _ = os.Hostname()
	}
	if o.StateDir == "" {
		o.StateDir = filepath.Join(config.Home(), "daemon")
	}
	if o.IdleClose <= 0 {
		o.IdleClose = 24 * time.Hour
	}
	if o.KeepWorkspaces <= 0 {
		o.KeepWorkspaces = 7 * 24 * time.Hour
	}
	if o.ReportEvery <= 0 {
		o.ReportEvery = 30 * time.Second
	}
	if o.Tick <= 0 {
		o.Tick = 2 * time.Second
	}
	if o.PollSeconds <= 0 {
		o.PollSeconds = 25
	}
	if o.Isolation == relayv1.AgentHostIsolation_AGENT_HOST_ISOLATION_UNSPECIFIED {
		o.Isolation = relayv1.AgentHostIsolation_AGENT_HOST_ISOLATION_NONE
	}
	if o.HTTP == nil {
		o.HTTP = rpc.HTTPClient(o.ServerURL)
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		o: o, log: log.With("component", "daemon"),
		api:     relayv1connect.NewHostAgentServiceClient(o.HTTP, strings.TrimRight(o.ServerURL, "/"), rpc.ClientOptions()...),
		tracked: map[string]*tracked{}, watching: map[string]bool{}, reportKick: make(chan struct{}, 1),
	}
}

// callAPI runs one RPC with the device's token, refreshing it once.
func callAPI[Req, Res any](ctx context.Context, tokens rpc.Tokens, fn func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error), msg *Req) (*Res, error) {
	do := func(token string) (*Res, error) {
		req := connect.NewRequest(msg)
		rpc.SetBearer(req.Header(), token)
		res, err := fn(ctx, req)
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	if tokens == nil {
		return do("")
	}
	token, err := tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	out, err := do(token)
	if connect.CodeOf(err) == connect.CodeUnauthenticated {
		if token, rerr := tokens.Refresh(ctx); rerr == nil {
			out, err = do(token)
		}
	}
	return out, err
}

func backoff(n int) time.Duration {
	d := 500 * time.Millisecond << min(n, 6)
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// HostID is the host this daemon registered as ("" until it did).
func (r *Runner) HostID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hostID
}

// register tells the server what this daemon is. A refusal that retrying can't change stops the daemon, and says why.
func (r *Runner) register(ctx context.Context) (*relayv1.RegisterAgentHostResponse, error) {
	for attempt := 0; ; attempt++ {
		res, err := callAPI(ctx, r.o.Tokens, r.api.RegisterAgentHost, &relayv1.RegisterAgentHostRequest{
			Name: r.o.Name, Isolation: r.o.Isolation, SandboxDriver: r.o.SandboxDriver, Os: runtime.GOOS, Arch: runtime.GOARCH,
			ClientVersion: config.Version, CpuCount: int32(runtime.NumCPU()), MaxSessions: int32(r.o.MaxSessions),
		})
		if err == nil {
			return res, nil
		}
		switch connect.CodeOf(err) {
		case connect.CodeFailedPrecondition, connect.CodePermissionDenied, connect.CodeNotFound:
			return nil, err
		}
		r.log.Warn("registering with the server failed; retrying", "err", err)
		if !sleep(ctx, backoff(attempt)) {
			return nil, ctx.Err()
		}
	}
}

// Run registers and takes work until ctx ends.
func (r *Runner) Run(ctx context.Context) error {
	if err := os.MkdirAll(r.o.Root, 0o700); err != nil {
		return fmt.Errorf("the workspace root: %w", err)
	}
	reg, err := r.register(ctx)
	if err != nil {
		return err
	}
	if reg.GetSupervisor() {
		return errors.New("this token is a team host's: run the daemon with --shared")
	}
	r.mu.Lock()
	r.hostID = reg.GetHost().GetId()
	r.mu.Unlock()
	r.log.Info("registered", "host", reg.GetHost().GetId(), "name", reg.GetHost().GetName())
	r.loadTracked()
	r.reopenTracked(ctx)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); r.reportLoop(ctx) }()
	go func() { defer wg.Done(); r.tickLoop(ctx) }()
	go func() { defer wg.Done(); r.claimLoop(ctx) }()
	wg.Wait()
	return ctx.Err()
}

func (r *Runner) claimLoop(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		res, err := callAPI(ctx, r.o.Tokens, r.api.ClaimHostWork, &relayv1.ClaimHostWorkRequest{WaitSeconds: r.o.PollSeconds})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			r.log.Debug("claiming work failed", "err", err)
			failures++
			sleep(ctx, backoff(failures))
			continue
		}
		failures = 0
		if w := res.GetWork(); w != nil {
			r.addPreparing(1)
			go func() {
				defer r.addPreparing(-1)
				r.runWork(ctx, w, res.GetGitName(), res.GetGitEmail())
			}()
		}
	}
}

func (r *Runner) addPreparing(n int) {
	r.mu.Lock()
	r.preparing += n
	r.mu.Unlock()
}

func (r *Runner) update(ctx context.Context, req *relayv1.UpdateHostWorkRequest) bool {
	res, err := callAPI(ctx, r.o.Tokens, r.api.UpdateHostWork, req)
	if err != nil {
		r.log.Debug("updating work failed", "work", req.GetWorkId(), "err", err)
		return false
	}
	return res.GetCancelled()
}

// credentials asks the server for a repository's git credentials. The token stays in this struct.
func (r *Runner) credentials(ctx context.Context, repositoryKey string) (Credentials, error) {
	res, err := callAPI(ctx, r.o.Tokens, r.api.IssueGitCredentials, &relayv1.IssueGitCredentialsRequest{RepositoryKey: repositoryKey})
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{CloneURL: res.GetCloneUrl(), Username: res.GetUsername(), Password: res.GetPassword()}, nil
}

// connectMessage is the error's own words, without the code in front.
func connectMessage(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return err.Error()
}

// runWork takes one piece of claimed work to a running session, or to a failure.
func (r *Runner) runWork(ctx context.Context, w *relayv1.HostWork, gitName, gitEmail string) {
	fail := func(err error) {
		r.log.Warn("work failed", "work", w.GetId(), "err", err)
		r.update(ctx, &relayv1.UpdateHostWorkRequest{WorkId: w.GetId(), State: relayv1.HostWorkState_HOST_WORK_STATE_FAILED, Error: connectMessage(err)})
	}
	// The lease lasts minutes, a clone can take longer.
	renew, stopRenew := context.WithCancel(ctx)
	defer stopRenew()
	cancelled := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-renew.Done():
				return
			case <-t.C:
				if r.update(renew, &relayv1.UpdateHostWorkRequest{WorkId: w.GetId(), State: relayv1.HostWorkState_HOST_WORK_STATE_CLAIMED}) {
					close(cancelled)
					stopRenew()
					return
				}
			}
		}
	}()
	cred, err := r.credentials(renew, w.GetRepositoryKey())
	if err != nil {
		fail(err)
		return
	}
	branch := w.GetBranch()
	existing := branch != ""
	if !existing {
		branch = "harness/" + w.GetId()
	}
	base := w.GetBaseRef()
	dir := filepath.Join(r.o.Root, "work", w.GetId())
	p, err := r.o.Git.Prepare(renew, r.o.Root, Checkout{
		RepositoryKey: w.GetRepositoryKey(), Base: base, Branch: branch, Existing: existing, Fresh: w.GetFreshClone(), Dir: dir, Name: gitName, Email: gitEmail,
	}, cred)
	if err != nil {
		select {
		case <-cancelled:
		default:
			fail(err)
		}
		return
	}
	drop := func() { _ = r.o.Git.Remove(ctx, r.o.Root, w.GetRepositoryKey(), dir) }
	mode := w.GetPermissionMode()
	if mode == agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS {
		drop()
		fail(errors.New("bypassPermissions cannot be enabled remotely"))
		return
	}
	create := &agentv1.CreateSessionRequest{Cwd: p.Dir, Model: w.GetModel(), Effort: w.GetEffort(), PermissionMode: mode}
	id, err := r.createForWork(renew, w, create)
	if err != nil {
		drop()
		fail(err)
		return
	}
	stopRenew()
	if t := w.GetTitle(); t != "" {
		if err := r.o.Sessions.SetTitle(ctx, id, t); err != nil {
			r.log.Debug("naming the session failed", "session", id, "err", err)
		}
	}
	t := &tracked{SessionID: id, WorkID: w.GetId(), RepositoryKey: w.GetRepositoryKey(), Dir: p.Dir, Branch: p.Branch, Base: p.Base, Landing: int32(w.GetLanding()), Title: w.GetTitle()}
	r.track(t)
	cancelledNow := r.update(ctx, &relayv1.UpdateHostWorkRequest{WorkId: w.GetId(), State: relayv1.HostWorkState_HOST_WORK_STATE_RUNNING, SessionId: id, Branch: p.Branch})
	if cancelledNow {
		if err := r.o.Sessions.Close(ctx, id); err != nil {
			r.log.Debug("closing a cancelled session failed", "session", id, "err", err)
		}
		return
	}
	r.startWatching(ctx, id)
	r.kickReport()
	if job := w.GetJob(); job != nil {
		if err := r.o.Sessions.DeliverJob(ctx, id, job); err != nil {
			r.log.Warn("the session did not take its job", "session", id, "err", err)
			r.update(ctx, &relayv1.UpdateHostWorkRequest{WorkId: w.GetId(), Error: connectMessage(err)})
		}
		return
	}
	if err := r.o.Sessions.Send(ctx, id, w.GetPrompt()); err != nil {
		r.log.Warn("the session did not take its first message", "session", id, "err", err)
		r.update(ctx, &relayv1.UpdateHostWorkRequest{WorkId: w.GetId(), Error: connectMessage(err)})
	}
}

// createForWork starts the session. Work that continues another session forks its transcript if the server has it, and
// starts afresh if not: the job says what it needs.
func (r *Runner) createForWork(ctx context.Context, w *relayv1.HostWork, create *agentv1.CreateSessionRequest) (string, error) {
	if from := w.GetForkSessionId(); from != "" {
		fork := &agentv1.CreateSessionRequest{Cwd: create.GetCwd(), Model: create.GetModel(), Effort: create.GetEffort(), PermissionMode: create.GetPermissionMode(), ResumeSessionId: from, Fork: true}
		id, err := r.o.Sessions.Create(ctx, fork)
		if err == nil {
			return id, nil
		}
		r.log.Debug("forking the session's transcript failed; starting afresh", "session", from, "err", err)
	}
	return r.o.Sessions.Create(ctx, create)
}

// ---- tracked sessions ----

func (r *Runner) trackedPath(id string) string {
	return filepath.Join(r.o.StateDir, "tracked", safeKey.ReplaceAllString(id, "-")+".json")
}

func (r *Runner) track(t *tracked) {
	r.mu.Lock()
	r.tracked[t.SessionID] = t
	r.mu.Unlock()
	r.saveTracked(t)
}

func (r *Runner) saveTracked(t *tracked) {
	r.mu.Lock()
	b, err := json.Marshal(t)
	r.mu.Unlock()
	if err != nil {
		return
	}
	path := r.trackedPath(t.SessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, path)
		}
		if err != nil {
			r.log.Warn("saving a session's record failed", "session", t.SessionID, "err", err)
		}
	}
}

func (r *Runner) loadTracked() {
	entries, _ := os.ReadDir(filepath.Join(r.o.StateDir, "tracked"))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(r.o.StateDir, "tracked", e.Name()))
		if err != nil {
			continue
		}
		var t tracked
		if json.Unmarshal(b, &t) == nil && t.SessionID != "" {
			r.mu.Lock()
			r.tracked[t.SessionID] = &t
			r.mu.Unlock()
		}
	}
}

// reopenTracked brings back the sessions the last run of this daemon had open, so that they can be followed up from the
// relay. A turn it was in the middle of reads as interrupted, as on any machine.
func (r *Runner) reopenTracked(ctx context.Context) {
	r.mu.Lock()
	var open []string
	for id, t := range r.tracked {
		if t.ClosedAt == "" {
			open = append(open, id)
		}
	}
	r.mu.Unlock()
	for _, id := range open {
		if _, err := r.o.Sessions.Create(ctx, &agentv1.CreateSessionRequest{ResumeSessionId: id}); err != nil {
			r.log.Warn("reopening a session failed", "session", id, "err", err)
		}
	}
}

func (r *Runner) tickLoop(ctx context.Context) {
	t := time.NewTicker(r.o.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		live := map[string]bool{}
		for _, id := range r.o.Sessions.Live() {
			live[id] = true
			r.startWatching(ctx, id)
		}
		r.closeIdle(ctx, live, now)
		r.sweepWorkspaces(ctx, live, now)
	}
}

func (r *Runner) trackedSession(id string) *tracked {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tracked[id]
}

func (r *Runner) closeIdle(ctx context.Context, live map[string]bool, now time.Time) {
	r.mu.Lock()
	ids := make([]string, 0, len(r.tracked))
	for id := range r.tracked {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		t := r.trackedSession(id)
		if t == nil {
			continue
		}
		if !live[id] {
			r.mu.Lock()
			needClose := t.ClosedAt == ""
			if needClose {
				t.ClosedAt = now.UTC().Format(time.RFC3339)
			}
			workID := t.WorkID
			r.mu.Unlock()
			if needClose {
				r.saveTracked(t)
				if workID != "" {
					r.update(ctx, &relayv1.UpdateHostWorkRequest{WorkId: workID, State: relayv1.HostWorkState_HOST_WORK_STATE_DONE})
				}
			}
			continue
		}
		r.mu.Lock()
		reopened := t.ClosedAt != ""
		t.ClosedAt = ""
		r.mu.Unlock()
		if reopened {
			r.saveTracked(t)
		}
		status, updated, ok := r.o.Sessions.Status(id)
		if ok && status == agentv1.SessionStatus_SESSION_STATUS_IDLE && now.Sub(updated) > r.o.IdleClose {
			if err := r.o.Sessions.Close(ctx, id); err != nil {
				r.log.Debug("closing an idle session failed", "session", id, "err", err)
			}
		}
	}
}

func (r *Runner) sweepWorkspaces(ctx context.Context, live map[string]bool, now time.Time) {
	r.mu.Lock()
	var old []*tracked
	for id, t := range r.tracked {
		if t.ClosedAt == "" || live[id] {
			continue
		}
		if at, err := time.Parse(time.RFC3339, t.ClosedAt); err == nil && now.Sub(at) > r.o.KeepWorkspaces {
			old = append(old, t)
			delete(r.tracked, id)
		}
	}
	r.mu.Unlock()
	for _, t := range old {
		if err := r.o.Git.Remove(ctx, r.o.Root, t.RepositoryKey, t.Dir); err != nil {
			r.log.Warn("removing a workspace failed", "dir", t.Dir, "err", err)
		}
		_ = os.Remove(r.trackedPath(t.SessionID))
	}
}

// startWatching lands a tracked session's work at the end of each of its turns. No-op for other sessions (a schedule's
// tracks itself through Track), and for one already being watched.
func (r *Runner) startWatching(ctx context.Context, id string) {
	r.mu.Lock()
	if r.tracked[id] == nil || r.watching[id] {
		r.mu.Unlock()
		return
	}
	r.watching[id] = true
	r.mu.Unlock()
	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.watching, id)
			r.mu.Unlock()
		}()
		var after int64 = -1
		for ctx.Err() == nil {
			events, start, cancel, ok := r.o.Sessions.Events(id, after)
			if !ok {
				return
			}
			after = start
			for ev := range events {
				if ev.GetSeq() > after {
					after = ev.GetSeq()
				}
				if _, finished := ev.GetEvent().(*agentv1.SessionEvent_TurnFinished); finished {
					r.land(ctx, id)
				}
			}
			cancel()
			// Closed with the session, or this watcher fell behind: then it goes on from where it was.
			if _, stillOpen := r.statusOf(id); !stillOpen {
				return
			}
		}
	}()
}

func (r *Runner) statusOf(id string) (agentv1.SessionStatus, bool) {
	s, _, ok := r.o.Sessions.Status(id)
	return s, ok
}

// land commits, pushes and opens the pull request, and tells the session and the work row (decision 12).
func (r *Runner) land(ctx context.Context, id string) {
	t := r.trackedSession(id)
	if t == nil {
		return
	}
	r.mu.Lock()
	landing := relayv1.HostWorkLanding(t.Landing)
	repo, dir, branch, base, workID, title, known := t.RepositoryKey, t.Dir, t.Branch, t.Base, t.WorkID, t.Title, t.PullRequest
	r.mu.Unlock()
	if landing == relayv1.HostWorkLanding_HOST_WORK_LANDING_NONE {
		return
	}
	report := func(url string, err error) {
		update := &agentv1.HostWorkUpdated{WorkId: workID, Branch: branch, PullRequestUrl: url}
		if err != nil {
			update.Error = connectMessage(err)
			r.log.Warn("landing failed", "session", id, "err", err)
		}
		r.o.Sessions.Publish(id, update)
		if workID != "" {
			r.update(ctx, &relayv1.UpdateHostWorkRequest{WorkId: workID, Branch: branch, PullRequestUrl: url, Error: update.GetError()})
		}
	}
	cred, err := r.credentials(ctx, repo)
	if err != nil {
		report(known, err)
		return
	}
	message := "Work of a session on " + r.o.Name
	if title != "" {
		message = title
	}
	res, err := r.o.Git.Land(ctx, dir, branch, base, message, cred)
	if err != nil {
		report(known, err)
		return
	}
	if res.Ahead == 0 {
		return // nothing to show for it yet
	}
	url := known
	wantPR := landing == relayv1.HostWorkLanding_HOST_WORK_LANDING_PULL_REQUEST || landing == relayv1.HostWorkLanding_HOST_WORK_LANDING_UNSPECIFIED
	if wantPR && url == "" {
		prTitle := title
		if prTitle == "" {
			prTitle = res.Subject
		}
		if prTitle == "" {
			prTitle = branch
		}
		pr, err := callAPI(ctx, r.o.Tokens, r.api.OpenHostPullRequest, &relayv1.OpenHostPullRequestRequest{
			RepositoryKey: repo, HeadBranch: branch, BaseBranch: base, Title: prTitle, Draft: true, SessionId: id,
			Body: "Opened from a session on the always-on host " + r.o.Name + ".",
		})
		if err != nil {
			report("", fmt.Errorf("the branch is pushed, but opening a pull request failed: %w", err))
			return
		}
		url = pr.GetUrl()
		r.mu.Lock()
		t.PullRequest = url
		r.mu.Unlock()
		r.saveTracked(t)
	}
	if res.Pushed || url != known {
		report(url, nil)
	}
}

// Track starts following a session this daemon did not start from the queue (a schedule's run on a checkout from Prepare).
func (r *Runner) Track(ctx context.Context, sessionID, repositoryKey string, p *Prepared) {
	r.track(&tracked{SessionID: sessionID, RepositoryKey: repositoryKey, Dir: p.Dir, Branch: p.Branch, Base: p.Base, Landing: int32(relayv1.HostWorkLanding_HOST_WORK_LANDING_BRANCH)})
	r.startWatching(ctx, sessionID)
}

// ScheduleHooks is what the agent server needs to run schedules on this host (decision 19). Name and email sign the commits.
func (r *Runner) ScheduleHooks(name, email string) *server.HostMode {
	return &server.HostMode{
		HostID: r.HostID,
		Claim: func(ctx context.Context, scheduleID, dueAt string) (bool, error) {
			return ClaimSchedule(ctx, r.o.Tokens, r.o.HTTP, r.o.ServerURL, scheduleID, dueAt)
		},
		Prepare: func(ctx context.Context, repositoryKey string) (string, func(context.Context, string), error) {
			cred, err := r.credentials(ctx, repositoryKey)
			if err != nil {
				return "", nil, errors.New(connectMessage(err))
			}
			id := fmt.Sprintf("sch-%d-%d", time.Now().UnixNano(), rand.Int32N(1<<20))
			p, err := r.o.Git.Prepare(ctx, r.o.Root, Checkout{RepositoryKey: repositoryKey, Branch: "harness/" + id, Dir: filepath.Join(r.o.Root, "work", id), Name: name, Email: email}, cred)
			if err != nil {
				return "", nil, err
			}
			return p.Dir, func(sctx context.Context, sessionID string) { r.Track(sctx, sessionID, repositoryKey, p) }, nil
		},
	}
}

// ClaimSchedule asks the server who runs a schedule's due time. Any machine with that schedule uses this.
func ClaimSchedule(ctx context.Context, tokens rpc.Tokens, hc *http.Client, serverURL, scheduleID, dueAt string) (bool, error) {
	api := relayv1connect.NewHostsServiceClient(hc, strings.TrimRight(serverURL, "/"), rpc.ClientOptions()...)
	res, err := callAPI(ctx, tokens, api.ClaimScheduleRun, &relayv1.ClaimScheduleRunRequest{ScheduleId: scheduleID, DueAt: dueAt})
	if err != nil {
		return false, err
	}
	return res.GetGranted(), nil
}

// ---- reports ----

func (r *Runner) kickReport() {
	select {
	case r.reportKick <- struct{}{}:
	default:
	}
}

// working counts what uses a slot: sessions at work, or waiting for someone's answer. An idle session stays open, for
// follow-ups, and takes no slot (decision 13).
func (r *Runner) working() int {
	n := 0
	for _, id := range r.o.Sessions.Live() {
		if s, ok := r.statusOf(id); ok && (s == agentv1.SessionStatus_SESSION_STATUS_WORKING || s == agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT) {
			n++
		}
	}
	return n
}

func (r *Runner) reportLoop(ctx context.Context) {
	t := time.NewTicker(r.o.ReportEvery)
	defer t.Stop()
	for {
		req := &relayv1.ReportAgentHostRequest{WorkingSessions: int32(r.working()), LoadAverage: loadAverage(), DiskFreeBytes: diskFree(r.o.Root)}
		if _, err := callAPI(ctx, r.o.Tokens, r.api.ReportAgentHost, req); err != nil && ctx.Err() == nil {
			r.log.Debug("reporting failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.reportKick:
		}
	}
}
