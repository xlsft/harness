package keepawake

import (
	"errors"
	"testing"
)

type fakeInhibitor struct {
	starts, releases int
	err              error
}

func (f *fakeInhibitor) start() (func(), error) {
	if f.err != nil {
		return nil, f.err
	}
	f.starts++
	return func() { f.releases++ }, nil
}

func TestHoldsOnlyWhileAWantingSessionIsBusy(t *testing.T) {
	f := &fakeInhibitor{}
	h := New(f.start)
	if err := h.Set("a", true); err != nil {
		t.Fatal(err)
	}
	if h.Holding() || f.starts != 0 {
		t.Fatal("an idle session must not hold off sleep")
	}
	h.SetBusy("a", true)
	if !h.Holding() || f.starts != 1 {
		t.Fatal("a busy session with the switch on must hold off sleep")
	}
	h.SetBusy("b", true) // busy without the switch changes nothing
	h.SetBusy("a", false)
	if h.Holding() || f.releases != 1 {
		t.Fatal("released when the session stops working")
	}
	h.SetBusy("a", true)
	if f.starts != 2 {
		t.Fatal("held again on the next turn")
	}
	if err := h.Set("a", false); err != nil || h.Holding() || h.Wanted("a") {
		t.Fatalf("turning it off releases: err=%v holding=%v wanted=%v", err, h.Holding(), h.Wanted("a"))
	}
}

func TestSharedAcrossSessionsAndForget(t *testing.T) {
	f := &fakeInhibitor{}
	h := New(f.start)
	for _, id := range []string{"a", "b"} {
		h.SetBusy(id, true)
		if err := h.Set(id, true); err != nil {
			t.Fatal(err)
		}
	}
	if f.starts != 1 {
		t.Fatalf("one inhibitor for all sessions, started %d", f.starts)
	}
	h.Forget("a")
	if !h.Holding() {
		t.Fatal("the other session still holds")
	}
	h.Forget("b")
	if h.Holding() || f.releases != 1 || h.Wanted("b") {
		t.Fatalf("closing the last session releases: holding=%v releases=%d", h.Holding(), f.releases)
	}
}

func TestFailureLeavesTheSwitchOff(t *testing.T) {
	f := &fakeInhibitor{err: errors.New("no inhibitor")}
	h := New(f.start)
	h.SetBusy("a", true)
	if err := h.Set("a", true); err == nil || h.Wanted("a") {
		t.Fatalf("err=%v wanted=%v", err, h.Wanted("a"))
	}
	var seen error
	h.OnError = func(err error) { seen = err }
	f.err = nil
	if err := h.Set("b", true); err != nil { // idle: nothing to start yet
		t.Fatal(err)
	}
	f.err = errors.New("gone")
	h.SetBusy("b", true)
	if seen == nil {
		t.Fatal("a failure without a waiting caller goes to OnError")
	}
}
