package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
)

func TestACloudContainerTakesOneSessionAndAlwaysPushesItsBranch(t *testing.T) {
	cloneURL, bare := bareRemote(t)
	api, url := newFakeAPI(t)
	api.cloneURL, api.cloud = cloneURL, true
	s := newFakeSessions()
	s.onTurn = func(fs *fakeSession) {
		if err := os.WriteFile(filepath.Join(fs.req.GetCwd(), "new.txt"), []byte("work\n"), 0o644); err != nil {
			t.Error(err)
		}
	}
	var setups []string
	var mu sync.Mutex
	startRunner(t, api, url, s, func(o *Options) {
		o.Cloud = true
		o.Setup = func(_ context.Context, dir string) error {
			mu.Lock()
			defer mu.Unlock()
			setups = append(setups, dir)
			return nil
		}
	})
	api.add(&relayv1.HostWork{Id: "hwk_c", RepositoryKey: "code.test/a/b", Prompt: "go", Landing: relayv1.HostWorkLanding_HOST_WORK_LANDING_NONE})
	waitFor(t, "the branch", func() bool {
		return strings.Contains(testGit(t, bare, "branch", "--list", "harness/hwk_c"), "harness/hwk_c")
	})
	if got := testGit(t, bare, "show", "harness/hwk_c:new.txt"); got != "work" {
		t.Fatalf("pushed %q", got)
	}
	api.mu.Lock()
	prs := len(api.prs)
	api.mu.Unlock()
	if prs != 0 {
		t.Error("landing NONE pushes the branch and opens no pull request")
	}
	// One session per container: more work is not taken.
	api.add(&relayv1.HostWork{Id: "hwk_other", RepositoryKey: "code.test/a/b", Prompt: "no"})
	time.Sleep(300 * time.Millisecond)
	api.mu.Lock()
	left := len(api.queue)
	api.mu.Unlock()
	if left != 1 {
		t.Errorf("a cloud daemon claimed a second piece of work (%d left)", left)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(setups) != 1 {
		t.Errorf("setup ran %d times", len(setups))
	}
}

func TestACloudContainerResumesItsSession(t *testing.T) {
	cloneURL, bare := bareRemote(t)
	api, url := newFakeAPI(t)
	api.cloneURL, api.cloud = cloneURL, true
	s := newFakeSessions()
	s.onTurn = func(fs *fakeSession) {
		if err := os.WriteFile(filepath.Join(fs.req.GetCwd(), "more.txt"), []byte("more\n"), 0o644); err != nil {
			t.Error(err)
		}
	}
	type restore struct{ id, cwd string }
	var restored []restore
	var mu sync.Mutex
	r, _ := startRunner(t, api, url, s, func(o *Options) {
		o.Cloud = true
		o.Restore = func(_ context.Context, id, cwd string) error {
			mu.Lock()
			defer mu.Unlock()
			restored = append(restored, restore{id, cwd})
			return nil
		}
	})
	// The first container pushed nothing: the branch starts again from the base.
	api.add(&relayv1.HostWork{Id: "hwk_r", RepositoryKey: "code.test/a/b", SessionId: "sess-1", Branch: "harness/hwk_r", State: relayv1.HostWorkState_HOST_WORK_STATE_RUNNING})
	var running *relayv1.UpdateHostWorkRequest
	waitFor(t, "the resumed session", func() bool {
		running = api.updateWith(func(u *relayv1.UpdateHostWorkRequest) bool {
			return u.GetState() == relayv1.HostWorkState_HOST_WORK_STATE_RUNNING
		})
		return running != nil
	})
	if running.GetSessionId() != "sess-1" || running.GetBranch() != "harness/hwk_r" {
		t.Fatalf("running %v", running)
	}
	dir := filepath.Join(r.o.Root, "work", "hwk_r")
	mu.Lock()
	if len(restored) != 1 || restored[0] != (restore{"sess-1", dir}) {
		t.Fatalf("restored %v", restored)
	}
	mu.Unlock()
	fs := s.session("sess-1")
	if fs == nil || fs.req.GetResumeSessionId() != "sess-1" || fs.req.GetCwd() != dir {
		t.Fatalf("resumed with %v", fs)
	}
	if got := testGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "harness/hwk_r" {
		t.Fatalf("on %s", got)
	}
	// The message that woke it is a turn like any: it lands on the branch.
	if err := s.Send(context.Background(), "sess-1", "and more"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the push", func() bool {
		return strings.Contains(testGit(t, bare, "branch", "--list", "harness/hwk_r"), "harness/hwk_r")
	})
	if s.session("sess-1").status == agentv1.SessionStatus_SESSION_STATUS_UNSPECIFIED {
		t.Error("the session is open")
	}
}

func TestACloudDaemonRefusesTheWrongToken(t *testing.T) {
	api, url := newFakeAPI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base := t.TempDir()
	err := New(Options{ServerURL: url, Sessions: newFakeSessions(), Root: base, StateDir: base, Cloud: true}).Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "not a cloud session's") {
		t.Fatalf("a host's token in a container: %v", err)
	}
	api.cloud = true
	err = New(Options{ServerURL: url, Sessions: newFakeSessions(), Root: base, StateDir: base}).Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "--cloud") {
		t.Fatalf("a cloud session's token outside: %v", err)
	}
}
