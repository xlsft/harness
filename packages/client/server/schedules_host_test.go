package server_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/schedule"
	"github.com/xlsft/harness/packages/client/server"
	"github.com/xlsft/harness/packages/client/syncclient/synctest"
)

// fakeHostMode is what `harness-client daemon` gives the scheduler.
type fakeHostMode struct {
	mu       sync.Mutex
	online   bool
	claims   []string
	refuse   bool
	repos    []string
	sessions []string
	dir      string
}

func (f *fakeHostMode) mode() *server.HostMode {
	return &server.HostMode{
		HostID: func() string { return "ahs_1" },
		Grace:  time.Millisecond,
		ListMachines: func(context.Context) (map[string]bool, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return map[string]bool{"dev_laptop": f.online}, nil
		},
		Claim: func(_ context.Context, id, due string) (bool, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.claims = append(f.claims, id+"@"+due)
			return !f.refuse, nil
		},
		Prepare: func(_ context.Context, repo string) (string, func(context.Context, string), error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.repos = append(f.repos, repo)
			return f.dir, func(_ context.Context, id string) {
				f.mu.Lock()
				defer f.mu.Unlock()
				f.sessions = append(f.sessions, id)
			}, nil
		},
	}
}

func (f *fakeHostMode) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func TestAHostRunsASchedulesTimeWhileItsMachineIsAway(t *testing.T) {
	fake := synctest.New()
	httpSrv := fake.Start()
	t.Cleanup(httpSrv.Close)
	mode := &fakeHostMode{online: true, dir: t.TempDir()}
	var ran atomic.Int32
	h, clock, _ := startSyncedMachine(t, fake, httpSrv.URL, "dev_host", func(int, *llm.Request) fg.Reply {
		ran.Add(1)
		return answer("ran on the host")
	}, func(s *server.Server) { s.HostMode = mode.mode() })

	task := schedule.Task{
		ID: "sch_1", Name: "Nightly", Prompt: "check things", Cwd: "/Users/me/on-the-laptop", DeviceID: "dev_laptop", DeviceName: "laptop",
		AwayHostID: "ahs_1", RepositoryKey: "github.com/acme/widgets", CatchUp: true, Trigger: schedule.Trigger{Kind: schedule.KindInterval, EverySeconds: 3600},
		CreatedAt: clock.now().UTC().Format(time.RFC3339), UpdatedAt: clock.now().UTC().Format(time.RFC3339),
	}
	content, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	fake.Seed(&syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SCHEDULE, Key: "sch_1"}, string(content))
	waitFor(t, "the document", func() bool { return h.scheduleByID("sch_1") != nil })

	// The machine is up: its schedule is its own.
	clock.advance(2 * time.Hour)
	time.Sleep(50 * time.Millisecond)
	if ran.Load() != 0 {
		t.Fatal("ran while its machine was up")
	}

	// It goes away. A due time the other side claimed is not a run here.
	mode.set(func() { mode.online, mode.refuse = false, true })
	clock.advance(35 * time.Second) // past the host's next look at the machines
	waitFor(t, "a claim", func() bool {
		mode.mu.Lock()
		defer mode.mu.Unlock()
		return len(mode.claims) > 0
	})
	if ran.Load() != 0 {
		t.Fatal("ran what the other side has")
	}

	// The next due time is the host's: on a checkout of its own, without "Allow on this machine".
	mode.set(func() { mode.refuse = false })
	clock.advance(2 * time.Hour)
	waitFor(t, "the run", func() bool {
		for _, r := range h.runs("sch_1") {
			if r.GetStatus() == agentv1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_SUCCEEDED {
				return true
			}
		}
		return false
	})
	mode.mu.Lock()
	defer mode.mu.Unlock()
	if len(mode.repos) != 1 || mode.repos[0] != "github.com/acme/widgets" {
		t.Fatalf("checkouts: %v", mode.repos)
	}
	if len(mode.sessions) != 1 {
		t.Fatalf("the run's session is not followed for landing: %v", mode.sessions)
	}
}

func TestAHostRefusesABypassSchedule(t *testing.T) {
	fake := synctest.New()
	httpSrv := fake.Start()
	t.Cleanup(httpSrv.Close)
	mode := &fakeHostMode{dir: t.TempDir()}
	h, clock, _ := startSyncedMachine(t, fake, httpSrv.URL, "dev_host", func(int, *llm.Request) fg.Reply { return answer("no") }, func(s *server.Server) { s.HostMode = mode.mode() })
	task := schedule.Task{
		ID: "sch_2", Name: "Careless", Prompt: "x", DeviceID: "dev_laptop", AwayHostID: "ahs_1", RepositoryKey: "github.com/acme/widgets", Mode: "bypassPermissions", CatchUp: true,
		Trigger:   schedule.Trigger{Kind: schedule.KindInterval, EverySeconds: 3600},
		CreatedAt: clock.now().UTC().Format(time.RFC3339), UpdatedAt: clock.now().UTC().Format(time.RFC3339),
	}
	content, _ := json.Marshal(task)
	fake.Seed(&syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SCHEDULE, Key: "sch_2"}, string(content))
	waitFor(t, "the document", func() bool { return h.scheduleByID("sch_2") != nil })
	clock.advance(35 * time.Second)
	time.Sleep(20 * time.Millisecond)
	clock.advance(time.Hour)
	waitFor(t, "the refusal", func() bool {
		for _, r := range h.runs("sch_2") {
			if r.GetStatus() == agentv1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_FAILED {
				return true
			}
		}
		return false
	})
	mode.mu.Lock()
	defer mode.mu.Unlock()
	if len(mode.repos) != 0 {
		t.Fatal("made a checkout for it")
	}
}
