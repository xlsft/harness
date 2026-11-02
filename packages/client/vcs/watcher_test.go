package vcs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/auth"
	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
	"github.com/xlsft/harness/packages/client/vcs/vcstest"
)

// testSchedule has distinct values so assertions can tell the branches apart; no jitter.
var testSchedule = Schedule{
	Pending: 10 * time.Second, PendingMax: 20 * time.Second, Open: 100 * time.Second, NoPR: 200 * time.Second,
	Closed: 300 * time.Second, Idle: 400 * time.Second, ErrorMin: 1 * time.Second, ErrorMax: 5 * time.Second,
	Unsupported: 999 * time.Second, BranchCheck: 20 * time.Millisecond, Redetect: time.Hour, Heartbeat: time.Hour,
	Jitter: -1,
}

type gitState struct {
	mu             sync.Mutex
	remote, branch string
}

func (g *gitState) set(remote, branch string) {
	g.mu.Lock()
	g.remote, g.branch = remote, branch
	g.mu.Unlock()
}

func (g *gitState) Remote(context.Context, string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.remote
}

func (g *gitState) Branch(context.Context, string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.branch
}

type harness struct {
	srv  *vcstest.Server
	env  *fakeEnv
	gh   *fakeCLI
	glab *fakeCLI
	git  *gitState
	w    *Watcher
	now  time.Time
}

// newHarness wires a watcher to the fake VcsService (with bearer auth) and fake CLIs.
func newHarness(t *testing.T, installGH, installGLab bool) *harness {
	t.Helper()
	srv := vcstest.New()
	srv.Token = "secret"
	t.Cleanup(srv.Close)
	h := &harness{srv: srv, env: newFakeEnv(t), git: &gitState{}, now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	r := h.env.runner()
	if installGH {
		h.gh = h.env.install("gh")
		h.gh.set("version", ghVersion, "", 0)
		h.gh.set("auth-json", `{"hosts":{"github.com":[{"state":"success","active":true,"host":"github.com","login":"octocat"}]}}`, "", 0)
	}
	if installGLab {
		h.glab = h.env.install("glab")
		h.glab.set("version", "glab 1.119.0 (f5016eda2)\n", "", 0)
		h.glab.set("auth-all", "", "gitlab.corp.example\n  ✓ Logged in to gitlab.corp.example as bob (x)\n", 0)
	}
	h.w = NewWatcher(Options{
		Reporter:   NewClient(srv.URL(), nil, auth.StaticToken("secret")),
		Dir:        "/work",
		Providers:  []Provider{&GH{Runner: r}, &GLab{Runner: r}},
		Remote:     h.git.Remote,
		Branch:     h.git.Branch,
		Detections: &DetectCache{},
		Schedule:   testSchedule,
		Now:        func() time.Time { return h.now },
	})
	return h
}

func (h *harness) tick(t *testing.T, want time.Duration) {
	t.Helper()
	if got := h.w.Tick(context.Background()); got != want {
		t.Fatalf("Tick delay = %s, want %s", got, want)
	}
}

func TestWatcherGHLifecycle(t *testing.T) {
	h := newHarness(t, true, true)
	h.git.set("git@github.com:o/r.git", "feat")

	// No PR yet: providers are reported, no snapshot.
	h.gh.set("pr-list", "[]", "", 0)
	h.tick(t, testSchedule.NoPR)
	provs := h.srv.Providers()
	if len(provs) != 1 || len(provs[0].GetProviders()) != 2 {
		t.Fatalf("providers = %v", provs)
	}
	if p := provs[0].GetProviders()[0]; p.GetType() != vcsv1.ProviderType_PROVIDER_TYPE_GH || hostsOf(p) != "github.com=octocat" {
		t.Fatalf("gh provider = %v", p)
	}
	if n := len(h.srv.Snapshots()); n != 0 {
		t.Fatalf("snapshots = %d", n)
	}

	// PR opened with pending checks: upload, fast poll that backs off up to PendingMax.
	h.gh.set("pr-list", ghPRJSON("OPEN", "false", ghRollupPending), "", 0)
	h.tick(t, 10*time.Second)
	h.tick(t, 15*time.Second)
	h.tick(t, 20*time.Second)
	h.tick(t, 20*time.Second)
	snaps := h.srv.Snapshots()
	if len(snaps) != 1 {
		t.Fatalf("unchanged snapshot re-uploaded: %d uploads", len(snaps))
	}
	s := snaps[0]
	if s.GetProvider() != vcsv1.ProviderType_PROVIDER_TYPE_GH || s.GetObservedAt() != "2026-11-02T12:00:00Z" ||
		len(s.GetPullRequests()) != 1 || s.GetPullRequests()[0].GetChecks().GetState() != vcsv1.CheckState_CHECK_STATE_RUNNING ||
		len(s.GetChecks()["github.com/o/r#42"].GetChecks()) != 3 {
		t.Fatalf("snapshot = %v", s)
	}
	if n := len(h.srv.Providers()); n != 1 {
		t.Fatalf("unchanged providers re-reported: %d", n)
	}

	// Checks fail: upload, slow open-PR interval.
	h.gh.set("pr-list", ghPRJSON("OPEN", "false", ghRollupFailed), "", 0)
	h.tick(t, testSchedule.Open)
	// Heartbeat: an unchanged snapshot is re-sent once it is old.
	h.now = h.now.Add(2 * time.Hour)
	h.tick(t, testSchedule.Open)
	// Merged with passing checks.
	h.gh.set("pr-list", ghPRJSON("MERGED", "false", ghRollupPassed), "", 0)
	h.tick(t, testSchedule.Closed)

	snaps = h.srv.Snapshots()
	if len(snaps) != 4 {
		t.Fatalf("uploads = %d, want 4", len(snaps))
	}
	if st := snaps[1].GetPullRequests()[0].GetChecks().GetState(); st != vcsv1.CheckState_CHECK_STATE_FAILURE {
		t.Fatalf("second upload state = %v", st)
	}
	if snaps[2].GetObservedAt() != "2026-11-02T14:00:00Z" {
		t.Fatalf("heartbeat observed_at = %s", snaps[2].GetObservedAt())
	}
	if st := snaps[3].GetPullRequests()[0].GetState().String(); st != "STATE_MERGED" {
		t.Fatalf("last upload state = %s", st)
	}
	h.gh.assertNonInteractive()
	if h.glab.calls() == nil {
		t.Fatal("glab was not detected")
	}
	for _, c := range h.glab.calls() {
		if c != "--version" && c != "auth status --all" {
			t.Fatalf("glab polled for a GitHub repo: %q", c)
		}
	}
}

func TestWatcherGLabSelfManaged(t *testing.T) {
	h := newHarness(t, true, true)
	h.git.set("https://gitlab.corp.example/group/sub/proj.git", "feat")
	h.glab.set("api-mrs", glabMRList, "", 0)
	h.glab.set("api-mr", glabMRDetail("opened"), "", 0)
	h.glab.set("api-jobs", glabJobs("running"), "", 0)
	h.tick(t, testSchedule.Pending)
	snaps := h.srv.Snapshots()
	if len(snaps) != 1 || snaps[0].GetProvider() != vcsv1.ProviderType_PROVIDER_TYPE_GLAB || len(snaps[0].GetPullRequests()) != 2 {
		t.Fatalf("snapshots = %v", snaps)
	}
	for _, c := range h.gh.calls() {
		if c != "--version" && c != "auth status --json hosts" {
			t.Fatalf("gh polled for a GitLab repo: %q", c)
		}
	}
}

func TestWatcherIdle(t *testing.T) {
	t.Run("no CLIs installed", func(t *testing.T) {
		h := newHarness(t, false, false)
		h.git.set("git@github.com:o/r.git", "feat")
		h.tick(t, testSchedule.Idle)
		provs := h.srv.Providers()
		if len(provs) != 1 || len(provs[0].GetProviders()) != 0 {
			t.Fatalf("providers = %v", provs)
		}
	})
	t.Run("not logged in to the host", func(t *testing.T) {
		h := newHarness(t, true, false)
		h.gh.set("auth-json", `{"hosts":{}}`, "", 0)
		h.git.set("git@github.com:o/r.git", "feat")
		h.tick(t, testSchedule.Idle)
	})
	t.Run("no remote", func(t *testing.T) {
		h := newHarness(t, true, false)
		h.git.set("", "main")
		h.tick(t, testSchedule.Idle)
	})
	t.Run("detached head", func(t *testing.T) {
		h := newHarness(t, true, false)
		h.git.set("git@github.com:o/r.git", "HEAD")
		h.tick(t, testSchedule.Idle)
	})
}

func TestWatcherErrors(t *testing.T) {
	t.Run("cli failures back off exponentially", func(t *testing.T) {
		h := newHarness(t, true, false)
		h.git.set("git@github.com:o/r.git", "feat")
		h.gh.set("pr-list", "{garbage", "", 0)
		for _, want := range []time.Duration{1, 2, 4, 5, 5} {
			h.tick(t, want*time.Second)
		}
		h.gh.set("pr-list", ghPRJSON("OPEN", "false", ghRollupPassed), "", 0)
		h.tick(t, testSchedule.Open)
		h.gh.set("pr-list", "", "HTTP 502", 1)
		h.tick(t, testSchedule.ErrorMin) // the streak was reset by the success
	})
	t.Run("cli timeout", func(t *testing.T) {
		h := newHarness(t, true, false)
		h.git.set("git@github.com:o/r.git", "feat")
		h.gh.set("pr-list", "[]", "", 0)
		h.tick(t, testSchedule.NoPR) // detection (cached from now on) with the normal timeout
		h.gh.hang("pr-list", 30)
		h.w.o.Providers[0].(*GH).Runner.Timeout = 300 * time.Millisecond
		start := time.Now()
		h.tick(t, testSchedule.ErrorMin)
		if d := time.Since(start); d > 4*time.Second {
			t.Fatalf("timed-out poll took %s", d)
		}
	})
	t.Run("upload failure is retried", func(t *testing.T) {
		h := newHarness(t, true, false)
		h.git.set("git@github.com:o/r.git", "feat")
		h.gh.set("pr-list", ghPRJSON("OPEN", "false", ghRollupPassed), "", 0)
		h.tick(t, testSchedule.Open) // providers + snapshot
		h.gh.set("pr-list", ghPRJSON("OPEN", "false", ghRollupFailed), "", 0)
		h.srv.FailNext(connect.NewError(connect.CodeUnavailable, errors.New("down")))
		h.tick(t, testSchedule.ErrorMin)
		h.tick(t, testSchedule.Open)
		if n := len(h.srv.Snapshots()); n != 2 {
			t.Fatalf("uploads = %d, want 2", n)
		}
	})
	t.Run("server without device RPCs", func(t *testing.T) {
		h := newHarness(t, true, false)
		h.srv.SetUnimplemented(true)
		h.git.set("git@github.com:o/r.git", "feat")
		h.tick(t, testSchedule.Unsupported)
	})
	t.Run("wrong token", func(t *testing.T) {
		h := newHarness(t, true, false)
		h.srv.Token = "other"
		h.git.set("git@github.com:o/r.git", "feat")
		h.tick(t, testSchedule.ErrorMin)
		if len(h.srv.Providers()) != 0 {
			t.Fatal("unauthenticated report recorded")
		}
	})
}

func TestWatcherRunWakesOnBranchChangeAndPoke(t *testing.T) {
	h := newHarness(t, true, false)
	h.git.set("git@github.com:o/r.git", "main")
	h.gh.set("pr-list", "[]", "", 0) // NoPR: 200 s wait, so only wake-ups can trigger polls
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.w.Run(ctx) }()

	waitCalls := func(n int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for countPRList(h.gh.calls()) < n {
			if time.Now().After(deadline) {
				t.Fatalf("pr list calls = %d, want %d", countPRList(h.gh.calls()), n)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitCalls(1)

	// A branch switch triggers a poll within BranchCheck.
	h.gh.set("pr-list", ghPRJSON("OPEN", "false", ghRollupPassed), "", 0)
	h.git.set("git@github.com:o/r.git", "feat")
	if got := h.srv.WaitSnapshots(1, 10*time.Second); len(got) != 1 {
		t.Fatalf("snapshots after branch change = %d", len(got))
	}
	// Poke polls immediately (Open interval is 100 s).
	before := countPRList(h.gh.calls())
	h.w.Poke()
	waitCalls(before + 1)

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

func countPRList(calls []string) int {
	n := 0
	for _, c := range calls {
		if len(c) > 7 && c[:7] == "pr list" {
			n++
		}
	}
	return n
}

func TestDetectCacheSharesAndExpires(t *testing.T) {
	f := newFakeEnv(t)
	gh := f.install("gh")
	gh.set("version", ghVersion, "", 0)
	gh.set("auth-json", `{"hosts":{}}`, "", 0)
	p := &GH{Runner: f.runner()}
	c := &DetectCache{}
	now := time.Now()
	for range 3 {
		if _, err := c.Get(context.Background(), p, time.Minute, now); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(gh.calls()); n != 2 {
		t.Fatalf("calls = %d, want 2 (one detection)", n)
	}
	if _, err := c.Get(context.Background(), p, time.Minute, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := len(gh.calls()); n != 4 {
		t.Fatalf("calls = %d, want 4 after expiry", n)
	}
}
