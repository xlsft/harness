package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCronNext(t *testing.T) {
	cases := []struct{ expr, after, want string }{
		{"*/15 * * * *", "2026-03-10T10:07:30Z", "2026-03-10T10:15:00Z"},
		{"0 9 * * 1-5", "2026-03-13T09:00:00Z", "2026-03-16T09:00:00Z"}, // Friday 9:00 -> Monday
		{"30 2 1 * *", "2026-01-15T00:00:00Z", "2026-02-01T02:30:00Z"},
		{"0 0 29 2 *", "2026-03-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"@daily", "2026-03-10T10:00:00Z", "2026-03-11T00:00:00Z"},
		{"0 12 * JAN,JUL sun", "2026-01-01T00:00:00Z", "2026-01-04T12:00:00Z"},
		{"0 0 1 * 7", "2026-03-02T00:00:00Z", "2026-03-08T00:00:00Z"}, // day of month or Sunday (7), whichever is first
	}
	for _, c := range cases {
		cr, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		got, ok := cr.Next(mustTime(t, c.after), time.UTC)
		if !ok || !got.Equal(mustTime(t, c.want)) {
			t.Errorf("%s after %s = %v, want %s", c.expr, c.after, got, c.want)
		}
	}
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "*/0 * * * *", "5-1 * * * *", "a * * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
	if cr, _ := ParseCron("0 0 30 2 *"); cr != nil {
		if _, ok := cr.Next(time.Unix(0, 0), time.UTC); ok {
			t.Error("February 30th should never come")
		}
	}
}

func TestCronAcrossClockChange(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no time zone data")
	}
	cr, _ := ParseCron("30 2 * * *")
	// 2:30 doesn't exist on 2026-03-08: the next one is on the 9th.
	got, ok := cr.Next(time.Date(2026, 3, 8, 0, 0, 0, 0, ny), ny)
	if !ok || got.Day() != 9 || got.Hour() != 2 || got.Minute() != 30 {
		t.Fatalf("got %v", got)
	}
}

func TestValidate(t *testing.T) {
	ok := Task{Prompt: "x", Trigger: Trigger{Kind: KindInterval, EverySeconds: 60}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]Task{
		"no prompt":        {Trigger: Trigger{Kind: KindInterval, EverySeconds: 60}},
		"too fast":         {Prompt: "x", Trigger: Trigger{Kind: KindInterval, EverySeconds: 10}},
		"no time":          {Prompt: "x", Trigger: Trigger{Kind: KindOnce}},
		"bad cron":         {Prompt: "x", Trigger: Trigger{Kind: KindCron, Cron: "nope"}},
		"bad zone":         {Prompt: "x", Trigger: Trigger{Kind: KindCron, Cron: "* * * * *", Timezone: "Mars/Olympus"}},
		"no folder":        {Prompt: "x", Trigger: Trigger{Kind: KindFile, Paths: []string{"*.go"}}},
		"escaping":         {Prompt: "x", Cwd: "/x", Trigger: Trigger{Kind: KindFile, Paths: []string{"../*.go"}}},
		"no trigger":       {Prompt: "x"},
		"file with bypass": {Prompt: "x", Cwd: "/x", Mode: "bypassPermissions", Trigger: Trigger{Kind: KindFile, Paths: []string{"*.go"}}},
		"hook with bypass": {Prompt: "x", Mode: "bypassPermissions", Trigger: Trigger{Kind: KindWebhook}},
	}
	for name, task := range bad {
		if task.Validate() == nil {
			t.Errorf("%s should be refused", name)
		}
	}
}

func TestParseEvery(t *testing.T) {
	for in, want := range map[string]time.Duration{"90s": 90 * time.Second, "5m": 5 * time.Minute, "2h": 2 * time.Hour, "1d": 24 * time.Hour, "10 min": 10 * time.Minute} {
		if got, ok := ParseEvery(in); !ok || got != want {
			t.Errorf("%s = %v, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "soon", "0m", "-5m", "5"} {
		if _, ok := ParseEvery(in); ok {
			t.Errorf("%q should not parse", in)
		}
	}
}

// fixture is a runner whose fires are collected and a clock the test sets.
type fixture struct {
	t  *testing.T
	r  *Runner
	mu sync.Mutex
	f  []Fire
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t}
	f.r = &Runner{Store: Open(filepath.Join(t.TempDir(), "s")), DeviceID: "dev_a", DeviceName: "a", Start: func(fr Fire) {
		f.mu.Lock()
		f.f = append(f.f, fr)
		f.mu.Unlock()
	}, FilePoll: time.Millisecond}
	return f
}

func (f *fixture) add(task Task, created time.Time) string {
	f.t.Helper()
	if task.ID == "" {
		task.ID = "sch_1"
	}
	if task.Prompt == "" {
		task.Prompt = "go"
	}
	task.CreatedAt, task.DeviceID = created.UTC().Format(time.RFC3339), "dev_a"
	if err := f.r.Store.Update(func(st *State) error {
		st.Records[task.ID] = &Record{Task: task, Trusted: task.TrustHash()}
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	return task.ID
}

func (f *fixture) take() []Fire {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.f
	f.f = nil
	return out
}

func (f *fixture) rec(id string) *Record {
	f.t.Helper()
	st, err := f.r.Store.View()
	if err != nil {
		f.t.Fatal(err)
	}
	return st.Records[id]
}

func TestOneOffRunsOnceAndEnds(t *testing.T) {
	f := newFixture(t)
	t0 := mustTime(t, "2026-03-10T10:00:00Z")
	id := f.add(Task{Trigger: Trigger{Kind: KindOnce, At: "2026-03-10T10:05:00Z"}}, t0)
	f.r.Tick(t0.Add(4 * time.Minute))
	if len(f.take()) != 0 {
		t.Fatal("fired early")
	}
	f.r.Tick(t0.Add(5 * time.Minute))
	got := f.take()
	if len(got) != 1 || got[0].Source != SourceTime {
		t.Fatalf("fires: %+v", got)
	}
	rec := f.rec(id)
	if !rec.Task.Done || rec.Runs != 1 {
		t.Fatalf("record: %+v", rec)
	}
	if err := f.r.Store.FinishRun(id, got[0].RunID, StatusSucceeded, "done", "", t0.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.r.Tick(t0.Add(time.Hour))
	if len(f.take()) != 0 {
		t.Fatal("a one-off fired twice")
	}
}

func TestIntervalSkipsWhileARunWorks(t *testing.T) {
	f := newFixture(t)
	t0 := mustTime(t, "2026-03-10T10:00:00Z")
	id := f.add(Task{Trigger: Trigger{Kind: KindInterval, EverySeconds: 60}}, t0)
	f.r.Tick(t0.Add(61 * time.Second))
	fires := f.take()
	if len(fires) != 1 {
		t.Fatalf("fires: %d", len(fires))
	}
	f.r.Tick(t0.Add(122 * time.Second))
	if len(f.take()) != 0 {
		t.Fatal("started over a working run")
	}
	rec := f.rec(id)
	if rec.History[0].Status != StatusSkipped {
		t.Fatalf("history: %+v", rec.History)
	}
	if err := f.r.Store.FinishRun(id, fires[0].RunID, StatusSucceeded, "", "", t0.Add(123*time.Second)); err != nil {
		t.Fatal(err)
	}
	f.r.Tick(t0.Add(183 * time.Second))
	if len(f.take()) != 1 {
		t.Fatal("did not go on after the run ended")
	}
}

func TestDownTime(t *testing.T) {
	t0 := mustTime(t, "2026-03-10T10:00:00Z")
	t.Run("catches up once", func(t *testing.T) {
		f := newFixture(t)
		f.add(Task{CatchUp: true, Trigger: Trigger{Kind: KindInterval, EverySeconds: 60}}, t0)
		f.r.Tick(t0.Add(3 * time.Hour))
		got := f.take()
		if len(got) != 1 || got[0].Source != SourceCatchUp {
			t.Fatalf("fires: %+v", got)
		}
	})
	t.Run("records a missed run without catching up", func(t *testing.T) {
		f := newFixture(t)
		id := f.add(Task{Trigger: Trigger{Kind: KindInterval, EverySeconds: 60}}, t0)
		f.r.Tick(t0.Add(3 * time.Hour))
		if len(f.take()) != 0 {
			t.Fatal("ran what it should have missed")
		}
		rec := f.rec(id)
		if len(rec.History) != 1 || rec.History[0].Status != StatusMissed {
			t.Fatalf("history: %+v", rec.History)
		}
		// From now on it's on time again.
		f.r.Tick(t0.Add(3*time.Hour + 61*time.Second))
		if len(f.take()) != 1 {
			t.Fatal("did not resume")
		}
	})
}

func TestPausedOtherMachineAndUntrustedDoNotRun(t *testing.T) {
	f := newFixture(t)
	t0 := mustTime(t, "2026-03-10T10:00:00Z")
	every := Trigger{Kind: KindInterval, EverySeconds: 60}
	f.add(Task{ID: "paused", Paused: true, Trigger: every}, t0)
	if err := f.r.Store.Update(func(st *State) error {
		other := Task{ID: "other", Prompt: "go", DeviceID: "dev_b", CreatedAt: t0.Format(time.RFC3339), Trigger: every}
		st.Records["other"] = &Record{Task: other, Trusted: other.TrustHash()}
		// Edited elsewhere: what's trusted here isn't what it says now.
		edited := Task{ID: "edited", Prompt: "something else", DeviceID: "dev_a", CreatedAt: t0.Format(time.RFC3339), Trigger: every}
		st.Records["edited"] = &Record{Task: edited, Trusted: "older"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.r.Tick(t0.Add(61 * time.Second))
	if got := f.take(); len(got) != 0 {
		t.Fatalf("ran %+v", got)
	}
	// Asking doesn't get past the machine's consent either.
	if err := f.r.RunNow("edited", SourceManual, "", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(f.take()) != 0 {
		t.Fatal("ran what this machine hasn't allowed")
	}
	if rec := f.rec("edited"); rec.History[0].Status != StatusSkipped || !strings.Contains(rec.History[0].Error, "allow") {
		t.Fatalf("history: %+v", rec.History)
	}
	if err := f.r.RunNow("other", SourceManual, "", t0); err == nil {
		t.Fatal("ran another machine's schedule")
	}
}

func TestMaxRunsAndExpiry(t *testing.T) {
	f := newFixture(t)
	t0 := mustTime(t, "2026-03-10T10:00:00Z")
	id := f.add(Task{MaxRuns: 2, Trigger: Trigger{Kind: KindInterval, EverySeconds: 60}}, t0)
	for i := 1; i <= 4; i++ {
		f.r.Tick(t0.Add(time.Duration(i) * 61 * time.Second))
		for _, fr := range f.take() {
			_ = f.r.Store.FinishRun(id, fr.RunID, StatusSucceeded, "", "", t0)
		}
	}
	if rec := f.rec(id); rec.Runs != 2 || !rec.Task.Done {
		t.Fatalf("record: %+v", rec)
	}
	g := newFixture(t)
	id2 := g.add(Task{ExpiresAt: t0.Add(time.Minute).Format(time.RFC3339), Trigger: Trigger{Kind: KindInterval, EverySeconds: 60}}, t0)
	g.r.Tick(t0.Add(2 * time.Minute))
	if len(g.take()) != 0 || !g.rec(id2).Task.Done {
		t.Fatal("ran past its end")
	}
}

func TestConcurrencyCapKeepsWhatIsDue(t *testing.T) {
	f := newFixture(t)
	f.r.MaxConcurrent = 1
	t0 := mustTime(t, "2026-03-10T10:00:00Z")
	f.add(Task{ID: "a", Trigger: Trigger{Kind: KindOnce, At: t0.Format(time.RFC3339)}}, t0)
	f.add(Task{ID: "b", Trigger: Trigger{Kind: KindOnce, At: t0.Format(time.RFC3339)}}, t0)
	f.r.Tick(t0)
	first := f.take()
	if len(first) != 1 {
		t.Fatalf("fires: %d", len(first))
	}
	f.r.Tick(t0.Add(time.Second))
	if len(f.take()) != 0 {
		t.Fatal("went over the cap")
	}
	_ = f.r.Store.FinishRun(first[0].Task.ID, first[0].RunID, StatusSucceeded, "", "", t0)
	f.r.Tick(t0.Add(2 * time.Second))
	if len(f.take()) != 1 {
		t.Fatal("the second did not run")
	}
}

func TestLeaseKeepsTwoClientsFromRunningTheSameTask(t *testing.T) {
	f := newFixture(t)
	t0 := mustTime(t, "2026-03-10T10:00:00Z")
	f.add(Task{Trigger: Trigger{Kind: KindInterval, EverySeconds: 60}}, t0)
	f.r.Tick(t0)
	var second []Fire
	other := &Runner{Store: f.r.Store, DeviceID: "dev_a", Start: func(fr Fire) { second = append(second, fr) }}
	other.Tick(t0.Add(70 * time.Second))
	if len(second) != 0 || other.Holding() {
		t.Fatal("a second client ran it")
	}
	// The holder stopped renewing: the other takes over.
	other.Tick(t0.Add(70*time.Second + leaseFor))
	if len(second) != 1 || !other.Holding() {
		t.Fatalf("no takeover: %d", len(second))
	}
}

func TestFileTrigger(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/a.go", "one")
	write("notes.txt", "one")
	f := newFixture(t)
	t0 := mustTime(t, "2026-03-10T10:00:00Z")
	id := f.add(Task{Cwd: dir, Trigger: Trigger{Kind: KindFile, Paths: []string{"src/**/*.go"}, CooldownSeconds: 60}}, t0)
	now := t0
	tick := func() []Fire {
		now = now.Add(time.Second)
		f.r.Tick(now)
		return f.take()
	}
	if len(tick()) != 0 { // the first look only sets the baseline
		t.Fatal("fired on existing files")
	}
	write("notes.txt", "two")
	if len(tick()) != 0 || len(tick()) != 0 {
		t.Fatal("fired for a file it doesn't watch")
	}
	write("src/a.go", "two")
	if len(tick()) != 0 {
		t.Fatal("fired while the file may still be changing")
	}
	got := tick()
	if len(got) != 1 || got[0].Source != SourceFile || !strings.Contains(got[0].Payload, "src/a.go") {
		t.Fatalf("fires: %+v", got)
	}
	// What the run itself writes doesn't start another.
	write("src/a.go", "written by the run")
	if len(tick()) != 0 || len(tick()) != 0 {
		t.Fatal("re-triggered on the run's own change")
	}
	if err := f.r.Store.FinishRun(id, got[0].RunID, StatusSucceeded, "", "", now); err != nil {
		t.Fatal(err)
	}
	tick()
	// A real change, within the cooldown, waits for it to pass.
	write("src/a.go", "three")
	tick()
	if len(tick()) != 0 {
		t.Fatal("fired within the cooldown")
	}
	now = now.Add(time.Minute)
	if len(tick()) != 1 {
		t.Fatal("did not fire after the cooldown")
	}
}

func TestStoreInterruptsWhatAClientLeftBehind(t *testing.T) {
	f := newFixture(t)
	t0 := mustTime(t, "2026-03-10T10:00:00Z")
	id := f.add(Task{Trigger: Trigger{Kind: KindOnce, At: t0.Format(time.RFC3339)}}, t0)
	f.r.Tick(t0)
	f.take()
	if err := f.r.Store.InterruptStale(func(Run) bool { return false }, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rec := f.rec(id); rec.History[0].Status != StatusInterrupted {
		t.Fatalf("history: %+v", rec.History)
	}
}

func TestHistoryIsBounded(t *testing.T) {
	r := &Record{}
	for range MaxHistory + 10 {
		r.AddRun(Run{ID: "x"})
	}
	if len(r.History) != MaxHistory {
		t.Fatalf("%d", len(r.History))
	}
}

func TestStoreSurvivesConcurrentChanges(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "s"))
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A separate Store is what another process has.
			other := Open(s.dir)
			if err := other.Update(func(st *State) error {
				st.Records[string(rune('a'+i))] = &Record{}
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	st, err := s.View()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Records) != 20 {
		t.Fatalf("%d records", len(st.Records))
	}
}

func TestAwayHostAdoptionAndClaims(t *testing.T) {
	store := Open(t.TempDir())
	now := mustTime(t, "2026-03-10T10:00:00Z")
	task := Task{ID: "t1", Prompt: "p", DeviceID: "dev_laptop", AwayHostID: "ahs", RepositoryKey: "h/o/r", Trigger: Trigger{Kind: KindInterval, EverySeconds: 3600}, CreatedAt: now.Add(-2 * time.Hour).Format(time.RFC3339)}
	if err := store.Update(func(st *State) error {
		rec := &Record{Task: task}
		rec.Trusted = task.TrustHash()
		st.Records["t1"] = rec
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var fires []Fire
	adopt, grant := false, false
	var asked []time.Time
	r := &Runner{Store: store, DeviceID: "dev_host", Start: func(f Fire) { fires = append(fires, f) }, Grace: 10 * time.Hour,
		Adopt: func(Task) bool { return adopt },
		Claim: func(_ Task, due time.Time) bool {
			asked = append(asked, due)
			return grant
		}}
	r.Tick(now)
	if len(fires) != 0 || len(asked) != 0 {
		t.Fatalf("not adopted, yet %d fires, %d claims", len(fires), len(asked))
	}
	adopt = true
	r.Tick(now)
	if len(fires) != 0 || len(asked) != 1 {
		t.Fatalf("a refused claim: %d fires, %d claims", len(fires), len(asked))
	}
	st, _ := store.View()
	if got := len(st.Records["t1"].History); got != 0 {
		t.Fatalf("a refused claim is no run: %d in the history", got)
	}
	grant = true
	r.Tick(now.Add(2 * time.Hour))
	if len(fires) != 1 {
		t.Fatalf("a granted claim: %d fires", len(fires))
	}
}

func TestMergeHistoryAddsWhatTheAwayHostRan(t *testing.T) {
	r := &Record{History: []Run{{ID: "a", StartedAt: "2026-03-10T10:00:00Z"}}}
	r.MergeHistory([]Run{{ID: "a", StartedAt: "2026-03-10T10:00:00Z"}, {ID: "b", StartedAt: "2026-03-11T10:00:00Z"}})
	if len(r.History) != 2 || r.History[0].ID != "b" {
		t.Fatalf("history %+v", r.History)
	}
}
