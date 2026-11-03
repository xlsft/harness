package usagelimit

import (
	"context"
	"sync"
	"time"
)

// Fetcher returns the limits of the provider behind model, as the user may see them (nil when the server does
// not know the model or hides it).
type Fetcher func(ctx context.Context, model string) (*Snapshot, error)

// Default Monitor timings.
const (
	DefaultTTL     = 30 * time.Second
	DefaultTimeout = 3 * time.Second
)

// Monitor follows the usage limits of one harness session's provider. Check is called after each model response
// (and when a request failed for good); the snapshot is fetched at most once per TTL (a failure is cached too), so
// a turn of many requests costs one lookup per TTL.
type Monitor struct {
	Fetch Fetcher
	// TTL of the cached snapshot (default DefaultTTL); Timeout of one fetch (default DefaultTimeout).
	TTL, Timeout time.Duration
	// Now overrides time.Now (tests).
	Now func() time.Time

	mu        sync.Mutex
	snap      *Snapshot
	snapModel string
	fetchedAt time.Time
	// lastEval is the latest evaluation of a snapshot (numbers only when they were visible).
	lastEval Status
	tracker  Tracker
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Seed restores the last published state of a resumed session, so the first check publishes only a change.
func (m *Monitor) Seed(s Status) {
	m.mu.Lock()
	m.tracker.Seed(s)
	m.mu.Unlock()
}

// Current is the last published state.
func (m *Monitor) Current() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tracker.Last()
}

// snapshot returns the cached snapshot for model, fetching it when stale.
func (m *Monitor) snapshot(ctx context.Context, model string) *Snapshot {
	ttl := m.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	m.mu.Lock()
	if m.snapModel == model && !m.fetchedAt.IsZero() && m.now().Sub(m.fetchedAt) < ttl {
		s := m.snap
		m.mu.Unlock()
		return s
	}
	m.mu.Unlock()
	if m.Fetch == nil {
		return nil
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	s, err := m.Fetch(fctx, model)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		// Keep what was known for the same model; retry after the TTL.
		if m.snapModel != model {
			m.snap = nil
		}
	} else {
		m.snap = s
	}
	m.snapModel, m.fetchedAt = model, m.now()
	return m.snap
}

// Check evaluates the provider of model after a model response (err nil) or a failed request (err set). It
// returns the state to publish when it changed (nil otherwise) and the current state.
func (m *Monitor) Check(ctx context.Context, model string, err error) (*Status, Status) {
	if err != nil {
		if !IsReached(err) {
			return nil, m.Current()
		}
		snap := m.snapshot(ctx, model)
		m.mu.Lock()
		defer m.mu.Unlock()
		st := Status{State: Reached, Interrupted: true}
		if snap != nil {
			st.ProviderID, st.ProviderName = snap.ProviderID, snap.ProviderName
		}
		// Numbers only from a visible evaluation of the same provider; none when the limits are hidden. The
		// snapshot at hand explains the refusal best (a first request may have no earlier evaluation).
		e := m.lastEval
		if x, ok := Exhausted(snap, m.now()); ok {
			e = x
		} else if x := Evaluate(snap, m.now()); x.State != None {
			e = x
		}
		if e.State != None && e.ProviderID == st.ProviderID {
			st.WindowLabel, st.UsedPercent, st.ResetsAt, st.BalanceText = e.WindowLabel, e.UsedPercent, e.ResetsAt, e.BalanceText
		}
		return m.tracker.Observe(st), m.tracker.Last()
	}
	snap := m.snapshot(ctx, model)
	m.mu.Lock()
	defer m.mu.Unlock()
	if snap == nil {
		// Nothing known (no server, unknown or hidden model, failed fetch): keep the state, except that a
		// successful response ends REACHED.
		if m.tracker.Last().State != Reached {
			return nil, m.tracker.Last()
		}
		m.lastEval = Status{}
		return m.tracker.Observe(Status{}), m.tracker.Last()
	}
	st := Evaluate(snap, m.now())
	m.lastEval = st
	return m.tracker.Observe(st), m.tracker.Last()
}

// Reading is a fresh look at a provider's limits (Recheck).
type Reading struct {
	// Known: the snapshot had a visible block, so Status says how the provider stands: Reached when a block is
	// fully used, else what Evaluate says. Otherwise only the provider's name is known, at best (limits hidden,
	// unknown model, failed fetch).
	Known  bool
	Status Status
}

// Recheck fetches the limits of the provider behind model, past the cache, and rates them. It changes no published
// state (see Observe, Set); a failed fetch reads as not Known.
func (m *Monitor) Recheck(ctx context.Context, model string) Reading {
	if m.Fetch == nil {
		return Reading{}
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	fctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	snap, err := m.Fetch(fctx, model)
	if err != nil || snap == nil {
		return Reading{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snap, m.snapModel, m.fetchedAt = snap, model, m.now()
	r := Reading{Known: snap.Visible(), Status: Status{ProviderID: snap.ProviderID, ProviderName: snap.ProviderName}}
	if !r.Known {
		return r
	}
	if st, ok := Exhausted(snap, m.now()); ok {
		r.Status = st
		return r
	}
	r.Status = Evaluate(snap, m.now())
	m.lastEval = r.Status
	return r
}

// Observe records a state decided outside Check (a reset, a model switch) and returns the state to publish when it
// changed (nil otherwise).
func (m *Monitor) Observe(s Status) *Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.State == None {
		m.lastEval = Status{}
	}
	return m.tracker.Observe(s)
}

// Set records a state to publish even when it reads as unchanged (the reset time or numbers of REACHED moved).
func (m *Monitor) Set(s Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tracker.Set(s)
}

// TurnContinues forgets that the last turn was cut off: a new turn is starting.
func (m *Monitor) TurnContinues() {
	m.mu.Lock()
	defer m.mu.Unlock()
	last := m.tracker.Last()
	last.Interrupted = false
	m.tracker.Set(last)
}
