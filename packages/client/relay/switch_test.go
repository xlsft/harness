package relay

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
)

// The per-session switch (SetSessionMirrored): off unregisters the session and stops its events and commands;
// on registers it again and publishes what it missed, continuing the server's seq without duplicates.
func TestSessionSwitchOffAndOn(t *testing.T) {
	r := newRig(t, nil)
	f := r.addSession("s1")
	waitSubscribed(t, f)
	for i := 1; i <= 3; i++ {
		f.emit(true, fmt.Sprint("e", i))
	}
	r.fake.waitFor(t, "seq 3", func() bool { return r.fake.acked["s1"] == 3 })

	// Off.
	f.relayOff.Store(true)
	r.host.SetSessionMirrored(r.ctx, "s1", false)
	r.fake.waitFor(t, "unregister", func() bool { return len(r.fake.unregs) == 1 && r.fake.unregs[0] == "s1" })
	if r.host.session("s1") != nil {
		t.Fatal("session still mirrored after the switch went off")
	}
	r.fake.mu.Lock()
	batches := len(r.fake.batches)
	r.fake.mu.Unlock()
	f.emit(true, "e4")
	f.emit(false, "delta while off")
	f.emit(true, "e5")
	// A viewer's command is refused instead of waiting for an owner.
	r.send(&relayv1.Command{CommandId: "cmd_off", SessionId: "s1", Epoch: 1, ExpiresAt: future(),
		Payload: &relayv1.Command_SendUserMessage{SendUserMessage: &agentv1.SendUserMessageRequest{Uuid: "u"}}})
	r.fake.waitFor(t, "rejection", func() bool { return r.fake.ackFor("cmd_off") != nil })
	if a := r.fake.ackFor("cmd_off"); a.GetOutcome() != relayv1.CommandAck_OUTCOME_REJECTED {
		t.Fatalf("ack = %v", a)
	}
	time.Sleep(150 * time.Millisecond)
	r.fake.mu.Lock()
	if len(r.fake.batches) != batches || r.fake.acked["s1"] != 3 || r.fake.epochs["s1"] != 1 {
		r.fake.mu.Unlock()
		t.Fatalf("published while off: batches %d -> %d, acked %d, epochs %d", batches, len(r.fake.batches), r.fake.acked["s1"], r.fake.epochs["s1"])
	}
	r.fake.mu.Unlock()
	if n := r.backend.messageCount(); n != 0 {
		t.Fatalf("command ran while off (%d)", n)
	}

	// On: a fresh registration catches up with what happened while off.
	f.relayOff.Store(false)
	r.host.SetSessionMirrored(r.ctx, "s1", true)
	r.fake.waitFor(t, "caught up", func() bool { return r.fake.epochs["s1"] == 2 && r.fake.acked["s1"] == 5 })
	waitSubscribed(t, f)
	f.emit(true, "e6")
	r.fake.waitFor(t, "live again", func() bool { return r.fake.acked["s1"] == 6 })
	r.fake.mu.Lock()
	defer r.fake.mu.Unlock()
	if got := r.fake.storedSeqs("s1"); !reflect.DeepEqual(got, []int64{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("stored seqs %v", got)
	}
	if got := r.fake.stored["s1"][4].GetEvent().GetNotice().GetText(); got != "e4" {
		t.Fatalf("seq 4 = %q", got)
	}
}

// Turning the switch off and on again quickly never leaves the session unmirrored or double-registered.
func TestSessionSwitchFlapping(t *testing.T) {
	r := newRig(t, nil)
	f := r.addSession("s1")
	f.emit(true, "e1")
	r.fake.waitFor(t, "seq 1", func() bool { return r.fake.acked["s1"] == 1 })
	for i := 0; i < 5; i++ {
		r.host.SetSessionMirrored(r.ctx, "s1", false)
		r.host.SetSessionMirrored(r.ctx, "s1", true)
	}
	f.emit(true, "e2")
	r.fake.waitFor(t, "seq 2", func() bool { return r.fake.acked["s1"] == 2 })
	if r.host.session("s1") == nil {
		t.Fatal("session not mirrored after flapping")
	}
	r.fake.mu.Lock()
	defer r.fake.mu.Unlock()
	if got := r.fake.storedSeqs("s1"); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Fatalf("stored seqs %v", got)
	}
}

// A registration whose acked_seq covers events this host sent without getting the answer continues after them
// instead of shifting the seq (which would store them twice under new seqs).
func TestRebaseRecognisesOwnUnansweredEvents(t *testing.T) {
	hs := newHostSession(nil, "s1", newFakeFeed("s1"))
	hs.base, hs.baseSet, hs.lastLocal, hs.lastSent = 10, true, 3, 5
	if after := hs.rebase(2, 14); after != 4 || hs.base != 10 || hs.lastLocal != 4 {
		t.Fatalf("own events: after=%d base=%d last=%d", after, hs.base, hs.lastLocal)
	}
	// Beyond what was sent: another host published; continue after its events.
	if after := hs.rebase(3, 20); after != 4 || hs.base != 16 {
		t.Fatalf("other host: after=%d base=%d", after, hs.base)
	}
}

func TestSessionSwitchOffBeforeRegistered(t *testing.T) {
	r := newRig(t, nil)
	f := newFakeFeed("s3")
	f.relayOff.Store(true)
	r.backend.mu.Lock()
	r.backend.feeds["s3"] = f
	r.backend.mu.Unlock()
	r.host.AddSession(r.ctx, "s3")
	r.host.SetSessionMirrored(r.ctx, "s3", false) // no-op
	if r.host.session("s3") != nil {
		t.Fatal("a session switched off was mirrored")
	}
	f.relayOff.Store(false)
	r.host.SetSessionMirrored(r.ctx, "s3", true)
	r.fake.waitFor(t, "registration", func() bool { return r.fake.epochs["s3"] == 1 })
}
