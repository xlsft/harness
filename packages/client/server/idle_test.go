package server

import (
	"context"
	"testing"
	"time"
)

// fakeIdle is an idle compaction candidate with a scripted state.
type fakeIdle struct {
	since     time.Time
	busy      bool // a turn, a compaction or pending input
	fraction  float64
	disabled  bool
	handled   bool
	compacted int
}

func (f *fakeIdle) idleSince() (time.Time, bool)      { return f.since, !f.busy && !f.handled }
func (f *fakeIdle) contextFraction() float64          { return f.fraction }
func (f *fakeIdle) idleCompactionEnabled() bool       { return !f.disabled }
func (f *fakeIdle) markIdleHandled()                  { f.handled = true }
func (f *fakeIdle) compactIdle(context.Context) error { f.compacted++; return nil }

func TestIdleTick(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s := &fakeIdle{since: t0, fraction: 0.7}
	cands := []idleCandidate{s}

	// The clock moves: nothing happens before the idle period is over.
	for _, d := range []time.Duration{0, time.Minute, IdleCompactAfter - time.Second} {
		if got := idleTick(ctx, t0.Add(d), cands); got != nil || s.compacted != 0 {
			t.Fatalf("compacted after %v", d)
		}
	}
	if got := idleTick(ctx, t0.Add(IdleCompactAfter), cands); got != s || s.compacted != 1 || !s.handled {
		t.Fatalf("not compacted at the idle period: %+v", s)
	}
	// Handled: not again until its next turn (which clears handled and moves since).
	if idleTick(ctx, t0.Add(3*IdleCompactAfter), cands) != nil || s.compacted != 1 {
		t.Fatal("compacted twice without a turn")
	}
	s.handled, s.since = false, t0.Add(3*IdleCompactAfter)
	if idleTick(ctx, t0.Add(3*IdleCompactAfter+IdleCompactAfter/2), cands) != nil {
		t.Fatal("compacted before the new idle period was over")
	}
	if idleTick(ctx, t0.Add(4*IdleCompactAfter), cands) != s || s.compacted != 2 {
		t.Fatal("not compacted after the second idle period")
	}
}

func TestIdleTickSkips(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	now := t0.Add(2 * IdleCompactAfter)

	busy := &fakeIdle{since: t0, fraction: 0.9, busy: true}
	small := &fakeIdle{since: t0, fraction: IdleCompactMinFraction}
	off := &fakeIdle{since: t0, fraction: 0.9, disabled: true}
	if idleTick(ctx, now, []idleCandidate{busy, small, off}) != nil {
		t.Fatal("compacted a session that should be skipped")
	}
	if busy.compacted+small.compacted+off.compacted != 0 {
		t.Fatal("compaction ran")
	}
	// Below the threshold: handled until the next turn. Turned off: looked at again, so turning it on applies.
	if busy.handled || !small.handled || off.handled {
		t.Fatalf("handled busy=%v small=%v off=%v", busy.handled, small.handled, off.handled)
	}
	off.disabled = false
	if idleTick(ctx, now, []idleCandidate{busy, small, off}) != off || off.compacted != 1 {
		t.Fatal("turning idle compaction on did not apply")
	}
}

// After a restart every session that is opened again counts as active from that moment, and a check compacts at
// most one session: there is no burst of compactions.
func TestIdleTickOnePerCheck(t *testing.T) {
	ctx := context.Background()
	opened := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	var cands []idleCandidate
	var all []*fakeIdle
	for range 5 {
		f := &fakeIdle{since: opened, fraction: 0.8}
		cands, all = append(cands, f), append(all, f)
	}
	if idleTick(ctx, opened.Add(time.Minute), cands) != nil {
		t.Fatal("compacted right after opening")
	}
	for i := range 5 {
		idleTick(ctx, opened.Add(IdleCompactAfter+time.Duration(i)*idleCheckInterval), cands)
		n := 0
		for _, f := range all {
			n += f.compacted
		}
		if n != i+1 {
			t.Fatalf("check %d: %d compactions", i, n)
		}
	}
}
