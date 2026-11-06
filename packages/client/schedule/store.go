package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// MaxHistory is how many runs a task keeps.
const MaxHistory = 50

// Record is a task with what this machine knows about it.
type Record struct {
	Task Task `json:"task"`
	// The synced document: its id and version; zero until it reached the server.
	DocID      string `json:"docId,omitempty"`
	DocVersion int64  `json:"docVersion,omitempty"`
	// Dirty: changed here, not on the server yet. PendingDelete: deleted here, the server doesn't know.
	Dirty         bool `json:"dirty,omitempty"`
	PendingDelete bool `json:"pendingDelete,omitempty"`
	// Trusted is the TrustHash allowed on this machine.
	Trusted string `json:"trusted,omitempty"`
	// LastFired is when its time last came (RFC 3339); zero: created at Task.CreatedAt.
	LastFired string `json:"lastFired,omitempty"`
	// RunFirst: its first run is due at once (`/loop`).
	RunFirst bool `json:"runFirst,omitempty"`
	Runs     int  `json:"runs,omitempty"`
	// WebhookSecret stays here: the document only has its hash.
	WebhookSecret string `json:"webhookSecret,omitempty"`
	History       []Run  `json:"history,omitempty"`
	// HistoryDirty: runs the server doesn't have yet; HistoryVersion: its document's version.
	HistoryDirty   bool   `json:"historyDirty,omitempty"`
	HistoryDocID   string `json:"historyDocId,omitempty"`
	HistoryVersion int64  `json:"historyVersion,omitempty"`
}

// Active returns the run that still works, if any.
func (r *Record) Active() (Run, bool) {
	for _, h := range r.History {
		if h.Active() {
			return h, true
		}
	}
	return Run{}, false
}

// AddRun puts a run first and drops what's over the limit.
func (r *Record) AddRun(run Run) {
	r.History = append([]Run{run}, r.History...)
	if len(r.History) > MaxHistory {
		r.History = r.History[:MaxHistory]
	}
	if !r.Task.DeviceLocal {
		r.HistoryDirty = true
	}
}

// LastRun returns the newest run.
func (r *Record) LastRun() (Run, bool) {
	if len(r.History) == 0 {
		return Run{}, false
	}
	return r.History[0], true
}

// NextAt returns when the task's time comes next, as seen from now. False: not known, paused or done.
func (r *Record) NextAt() (time.Time, bool) {
	t := r.Task
	if t.Paused || t.Done {
		return time.Time{}, false
	}
	if r.RunFirst {
		base, _ := time.Parse(time.RFC3339, t.CreatedAt)
		return base, true
	}
	base, err := time.Parse(time.RFC3339, r.LastFired)
	if err != nil {
		base, _ = time.Parse(time.RFC3339, t.CreatedAt)
	}
	return t.Next(base)
}

// Lease says which client on this machine runs the tasks: two processes (the app and a terminal) would run each twice.
type Lease struct {
	Owner string `json:"owner"`
	At    string `json:"at"`
}

// State is everything in the file.
type State struct {
	Records map[string]*Record `json:"records"`
	Lease   Lease              `json:"lease,omitempty"`
	// Handled are the trigger documents this machine has run but couldn't delete yet.
	Handled map[string]bool `json:"handled,omitempty"`
}

// Sorted returns the records, oldest first.
func (st *State) Sorted() []*Record {
	out := make([]*Record, 0, len(st.Records))
	for _, r := range st.Records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Task.CreatedAt != out[j].Task.CreatedAt {
			return out[i].Task.CreatedAt < out[j].Task.CreatedAt
		}
		return out[i].Task.ID < out[j].Task.ID
	})
	return out
}

// Store is the state file, HARNESS_HOME/schedules/state.json. Each change reads it, applies and writes it back under a
// lock, as another client process on this machine may be changing it too.
type Store struct {
	dir string
	mu  sync.Mutex
}

// Open returns the store in dir; the file appears with the first change.
func Open(dir string) *Store { return &Store{dir: dir} }

func (s *Store) file() string { return filepath.Join(s.dir, "state.json") }

// lockStale is how old a lock file is before it counts as left behind by a crash; a change takes far less.
const lockStale = 10 * time.Second

func (s *Store) lock() (func(), error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, "state.lock")
	deadline := time.Now().Add(2 * lockStale)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > lockStale {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the schedule store is locked: %s", path)
		}
		time.Sleep(5 * time.Millisecond) // another process holds it for the few milliseconds of a change
	}
}

func (s *Store) read() (*State, error) {
	st := &State{Records: map[string]*Record{}}
	data, err := os.ReadFile(s.file())
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("%s: %w", s.file(), err)
	}
	if st.Records == nil {
		st.Records = map[string]*Record{}
	}
	return st, nil
}

func (s *Store) write(st *State) error {
	data, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "state-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.file())
}

// View returns the state as it is on disk. The caller owns what it gets.
func (s *Store) View() (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

// Update applies fn to the state and writes the result; an error from fn writes nothing.
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	st, err := s.read()
	if err != nil {
		return err
	}
	if err := fn(st); err != nil {
		return err
	}
	return s.write(st)
}

// leaseFor is how long a lease lasts without being renewed.
const leaseFor = 90 * time.Second

// TakeLease reports whether owner runs the tasks now: it holds the lease, or the holder stopped renewing it.
func (st *State) TakeLease(owner string, now time.Time) bool {
	at, err := time.Parse(time.RFC3339, st.Lease.At)
	if st.Lease.Owner != "" && st.Lease.Owner != owner && err == nil && now.Sub(at) < leaseFor {
		return false
	}
	st.Lease = Lease{Owner: owner, At: now.UTC().Format(time.RFC3339)}
	return true
}

// FinishRun settles a run, unless it already was.
func (s *Store) FinishRun(scheduleID, runID string, status Status, summary, why string, now time.Time) error {
	return s.Update(func(st *State) error {
		r := st.Records[scheduleID]
		if r == nil {
			return nil
		}
		for i := range r.History {
			h := &r.History[i]
			if h.ID == runID && h.Active() {
				h.Status, h.Summary, h.Error = status, summary, why
				h.FinishedAt = now.UTC().Format(time.RFC3339)
				if !r.Task.DeviceLocal {
					r.HistoryDirty = true
				}
			}
		}
		return nil
	})
}

// SetRunSession records the session a run works in.
func (s *Store) SetRunSession(scheduleID, runID, sessionID string) error {
	return s.Update(func(st *State) error {
		if r := st.Records[scheduleID]; r != nil {
			for i := range r.History {
				if r.History[i].ID == runID {
					r.History[i].SessionID = sessionID
				}
			}
		}
		return nil
	})
}

// InterruptStale settles the runs of a client that went away: nothing works on them now. Runs nobody holds a session for
// come out of `running` as interrupted when the client that had them is gone.
func (s *Store) InterruptStale(keep func(Run) bool, now time.Time) error {
	return s.Update(func(st *State) error {
		for _, r := range st.Records {
			for i := range r.History {
				h := &r.History[i]
				if h.Active() && !keep(*h) {
					h.Status, h.Error = StatusInterrupted, "the client stopped while it ran"
					h.FinishedAt = now.UTC().Format(time.RFC3339)
					if !r.Task.DeviceLocal {
						r.HistoryDirty = true
					}
				}
			}
		}
		return nil
	})
}
