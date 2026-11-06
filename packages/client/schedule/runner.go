package schedule

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/google/uuid"
)

// Fire asks for one run. The run is on record as running when Start gets it; whoever starts it settles it with
// Store.FinishRun.
type Fire struct {
	Task   Task
	RunID  string
	Source Source
	// Payload says what happened, for an event: changed files, a webhook's body. It comes from outside.
	Payload string
}

// Runner decides what is due. All time goes through Tick, so a test hands it the clock.
type Runner struct {
	Store *Store
	// DeviceID and DeviceName are this machine's.
	DeviceID, DeviceName string
	// Start begins a run and returns soon.
	Start func(Fire)
	// Enabled says whether due tasks run at all (nil: they do).
	Enabled func() bool
	// Grace is how late a time may come and still count as on time (default 2 min): past it the client wasn't up.
	Grace time.Duration
	// MaxConcurrent caps running runs (default 4); what's over it stays due for a later tick.
	MaxConcurrent int
	// FilePoll is how often file triggers look at their files (default 2 s).
	FilePoll time.Duration
	// Every is how often Run ticks (default 5 s); Nudge ticks at once.
	Every time.Duration
	// OnTick runs after each tick of Run (server: settles runs, moves documents).
	OnTick func(time.Time)
	// Clock replaces time.Now in Run (tests).
	Clock  func() time.Time
	Logger *slog.Logger

	id    string
	once  sync.Once
	nudge chan struct{}
	// holding: this runner had the lease at its last tick.
	holding atomic.Bool

	mu    sync.Mutex
	files map[string]*fileWatch
}

func (r *Runner) init() {
	r.once.Do(func() {
		r.id = uuid.NewString()
		r.nudge = make(chan struct{}, 1)
		r.files = map[string]*fileWatch{}
	})
}

func (r *Runner) log() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// Nudge makes Run tick now.
func (r *Runner) Nudge() {
	r.init()
	select {
	case r.nudge <- struct{}{}:
	default:
	}
}

// Run ticks until ctx ends. A timer that would sleep through a laptop's suspension isn't relied on: it ticks often and
// reads the wall clock each time.
func (r *Runner) Run(ctx context.Context) {
	r.init()
	every := r.Every
	if every <= 0 {
		every = 5 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	clock := r.Clock
	if clock == nil {
		clock = time.Now
	}
	for {
		now := clock()
		r.Tick(now)
		if r.OnTick != nil {
			r.OnTick(now)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.nudge:
		}
	}
}

// Holding reports whether this runner had the lease at its last tick.
func (r *Runner) Holding() bool { return r.holding.Load() }

// Mine reports whether this machine runs the task.
func (r *Runner) Mine(t Task) bool {
	return t.DeviceLocal || (t.DeviceID != "" && t.DeviceID == r.DeviceID)
}

// Blocked says what keeps a task of this machine from running ("" when nothing does).
func Blocked(rec *Record) string {
	if rec.Trusted != rec.Task.TrustHash() {
		return "Changed on another machine; allow it here to run it."
	}
	if err := rec.Task.Validate(); err != nil {
		return err.Error()
	}
	return ""
}

func (r *Runner) newRun(rec *Record, status Status, source Source, why string, now time.Time) Run {
	run := Run{ID: "run_" + uuid.NewString(), ScheduleID: rec.Task.ID, Status: status, Source: source, StartedAt: now.UTC().Format(time.RFC3339), DeviceName: r.DeviceName, Error: why}
	if status != StatusRunning {
		run.FinishedAt = run.StartedAt
	}
	return run
}

func stamp(rec *Record, now time.Time) {
	rec.Task.UpdatedAt = now.UTC().Format(time.RFC3339)
	if !rec.Task.DeviceLocal {
		rec.Dirty = true
	}
}

func countActive(st *State) int {
	n := 0
	for _, rec := range st.Records {
		if _, ok := rec.Active(); ok {
			n++
		}
	}
	return n
}

// Tick runs what is due at now. It takes the lease first, so one client at a time runs this machine's tasks.
func (r *Runner) Tick(now time.Time) {
	r.init()
	grace := r.Grace
	if grace <= 0 {
		grace = 2 * time.Minute
	}
	limit := r.MaxConcurrent
	if limit <= 0 {
		limit = 4
	}
	enabled := r.Enabled == nil || r.Enabled()
	var fires []Fire
	var scan []Task
	// Nothing on record: no file to write, and no lease to hold yet.
	if st, err := r.Store.View(); err == nil && len(st.Records) == 0 {
		r.holding.Store(true)
		return
	}
	err := r.Store.Update(func(st *State) error {
		held := st.TakeLease(r.id, now)
		r.holding.Store(held)
		if !held {
			return nil
		}
		active := countActive(st)
		for _, rec := range st.Sorted() {
			t := rec.Task
			if !r.Mine(t) || t.Paused || t.Done || Blocked(rec) != "" || !enabled {
				continue
			}
			if t.ExpiresAt != "" {
				if end, err := time.Parse(time.RFC3339, t.ExpiresAt); err == nil && now.After(end) {
					rec.Task.Done = true
					stamp(rec, now)
					continue
				}
			}
			if t.Trigger.Kind == KindFile {
				scan = append(scan, t)
				continue
			}
			next, ok := rec.NextAt()
			if !ok || next.After(now) {
				continue
			}
			if _, busy := rec.Active(); busy {
				if t.Trigger.Kind != KindOnce {
					rec.AddRun(r.newRun(rec, StatusSkipped, SourceTime, "the previous run was still working", now))
					rec.LastFired = now.UTC().Format(time.RFC3339)
				}
				continue
			}
			source := SourceTime
			if now.Sub(next) > grace && !rec.RunFirst {
				if !t.CatchUp {
					rec.AddRun(r.newRun(rec, StatusMissed, SourceTime, "the client was not running at "+next.UTC().Format(time.RFC3339), now))
					r.advance(rec, now)
					continue
				}
				source = SourceCatchUp
			}
			if active >= limit {
				continue // still due next tick
			}
			active++
			run := r.newRun(rec, StatusRunning, source, "", now)
			rec.AddRun(run)
			rec.Runs++
			r.advance(rec, now)
			fires = append(fires, Fire{Task: rec.Task, RunID: run.ID, Source: source})
		}
		return nil
	})
	if err != nil {
		r.log().Warn("schedules: tick failed", "err", err)
		return
	}
	for _, f := range fires {
		r.Start(f)
	}
	r.scanFiles(scan, now)
}

// advance moves the task past its time, and ends it when that was the last.
func (r *Runner) advance(rec *Record, now time.Time) {
	rec.LastFired = now.UTC().Format(time.RFC3339)
	rec.RunFirst = false
	done := rec.Task.Trigger.Kind == KindOnce || (rec.Task.MaxRuns > 0 && rec.Runs >= rec.Task.MaxRuns)
	if done && !rec.Task.Done {
		rec.Task.Done = true
		stamp(rec, now)
	}
}

// ErrBusy says a run didn't start because one works.
var ErrBusy = errors.New("the previous run is still working")

// RunNow starts a run for a reason other than the clock: the user, a webhook, changed files. A paused schedule runs when
// the user asks; for an event it doesn't. Whatever asks, what this machine hasn't allowed doesn't run.
func (r *Runner) RunNow(id string, source Source, payload string, now time.Time) error {
	r.init()
	var fire *Fire
	err := r.Store.Update(func(st *State) error {
		rec := st.Records[id]
		if rec == nil {
			return fmt.Errorf("no schedule %s", id)
		}
		if !r.Mine(rec.Task) {
			return fmt.Errorf("schedule %s runs on %s", id, rec.Task.DeviceName)
		}
		if why := Blocked(rec); why != "" {
			rec.AddRun(r.newRun(rec, StatusSkipped, source, why, now))
			return nil
		}
		if source != SourceManual && (rec.Task.Paused || rec.Task.Done) {
			rec.AddRun(r.newRun(rec, StatusSkipped, source, "the schedule is paused or done", now))
			return nil
		}
		if _, busy := rec.Active(); busy {
			rec.AddRun(r.newRun(rec, StatusSkipped, source, ErrBusy.Error(), now))
			return nil
		}
		run := r.newRun(rec, StatusRunning, source, "", now)
		rec.AddRun(run)
		rec.Runs++
		if source != SourceManual {
			rec.LastFired = now.UTC().Format(time.RFC3339)
			if rec.Task.MaxRuns > 0 && rec.Runs >= rec.Task.MaxRuns {
				rec.Task.Done = true
				stamp(rec, now)
			}
		}
		fire = &Fire{Task: rec.Task, RunID: run.ID, Source: source, Payload: payload}
		return nil
	})
	if err != nil {
		return err
	}
	if fire != nil {
		r.Start(*fire)
	}
	return nil
}

// ---- file triggers ----

// fileWatch is what a file trigger last saw.
type fileWatch struct {
	hash     string
	baseline map[string]fileSign
	pending  string
	scanned  time.Time
}

type fileSign struct {
	size int64
	mod  time.Time
}

// Bounds of a scan: past maxScanEntries the trigger reports a problem rather than eat the disk.
const (
	maxScanEntries = 100_000
	maxPayloadList = 50
)

var skipDirs = map[string]bool{".git": true, "node_modules": true, ".hg": true, ".svn": true, "dist": true, "build": true, ".next": true, ".nuxt": true, ".output": true, "target": true, "vendor": true, "__pycache__": true, ".venv": true}

// scanOne lists the matching files of a task. A pattern that names a usually skipped folder keeps it in the scan.
func scanOne(t Task) (map[string]fileSign, error) {
	out := map[string]fileSign{}
	root := t.Cwd
	seen := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if seen++; seen > maxScanEntries {
			return fmt.Errorf("more than %d files under %s", maxScanEntries, root)
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if skipDirs[d.Name()] && !namesDir(t.Trigger.Paths, rel) {
				return filepath.SkipDir
			}
			return nil
		}
		for _, pat := range t.Trigger.Paths {
			if ok, _ := doublestar.Match(pat, rel); ok {
				if info, ierr := d.Info(); ierr == nil {
					out[rel] = fileSign{size: info.Size(), mod: info.ModTime()}
				}
				break
			}
		}
		return nil
	})
	return out, err
}

func namesDir(patterns []string, rel string) bool {
	for _, p := range patterns {
		if strings.HasPrefix(p, rel+"/") {
			return true
		}
	}
	return false
}

func changedFiles(old, cur map[string]fileSign) []string {
	var out []string
	for p, s := range cur {
		if o, ok := old[p]; !ok || !o.mod.Equal(s.mod) || o.size != s.size {
			out = append(out, p)
		}
	}
	for p := range old {
		if _, ok := cur[p]; !ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// scanFiles looks at the file triggers that are due for a look. Changes made during a run belong to the run and move the
// baseline; a change fires once two looks in a row saw the same thing (nothing is being written any more) and the
// cooldown has passed.
func (r *Runner) scanFiles(tasks []Task, now time.Time) {
	poll := r.FilePoll
	if poll <= 0 {
		poll = 2 * time.Second
	}
	r.mu.Lock()
	keep := map[string]bool{}
	for _, t := range tasks {
		keep[t.ID] = true
	}
	for id := range r.files {
		if !keep[id] {
			delete(r.files, id)
		}
	}
	r.mu.Unlock()
	for _, t := range tasks {
		r.mu.Lock()
		w := r.files[t.ID]
		hash := t.TrustHash()
		if w == nil || w.hash != hash {
			w = &fileWatch{hash: hash}
			r.files[t.ID] = w
		}
		due := w.scanned.IsZero() || now.Sub(w.scanned) >= poll
		if due {
			w.scanned = now
		}
		r.mu.Unlock()
		if !due {
			continue
		}
		cur, err := scanOne(t)
		if err != nil {
			if _, statErr := os.Stat(t.Cwd); statErr != nil {
				r.log().Debug("schedules: folder is gone", "schedule", t.ID, "err", statErr)
			} else {
				r.log().Warn("schedules: file scan failed", "schedule", t.ID, "err", err)
			}
			continue
		}
		r.mu.Lock()
		if w.baseline == nil {
			w.baseline = cur
			r.mu.Unlock()
			continue
		}
		r.mu.Unlock()
		var busy, cooling bool
		if st, err := r.Store.View(); err == nil {
			if rec := st.Records[t.ID]; rec != nil {
				_, busy = rec.Active()
				if last, err := time.Parse(time.RFC3339, rec.LastFired); err == nil {
					cooling = now.Sub(last) < t.Cooldown()
				}
			}
		}
		r.mu.Lock()
		if busy {
			w.baseline, w.pending = cur, ""
			r.mu.Unlock()
			continue
		}
		changed := changedFiles(w.baseline, cur)
		key := fmt.Sprint(changed, signOf(cur, changed))
		fire := len(changed) > 0 && key == w.pending && !cooling
		if len(changed) == 0 {
			w.pending = ""
		} else {
			w.pending = key
		}
		if fire {
			w.baseline, w.pending = cur, ""
		}
		r.mu.Unlock()
		if fire {
			if err := r.RunNow(t.ID, SourceFile, payloadOf(changed), now); err != nil {
				r.log().Warn("schedules: file trigger failed", "schedule", t.ID, "err", err)
			}
		}
	}
}

func signOf(cur map[string]fileSign, changed []string) []string {
	out := make([]string, 0, len(changed))
	for _, p := range changed {
		s := cur[p]
		out = append(out, fmt.Sprint(s.size, s.mod.UnixNano()))
	}
	return out
}

func payloadOf(changed []string) string {
	list := changed
	more := ""
	if len(list) > maxPayloadList {
		more = fmt.Sprintf("\n... and %d more", len(list)-maxPayloadList)
		list = list[:maxPayloadList]
	}
	return "Changed files:\n" + strings.Join(list, "\n") + more
}
