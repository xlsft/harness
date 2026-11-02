package relay

import "testing"

type historyFakeFeed struct {
	*fakeFeed
	n int64
}

func (f historyFakeFeed) HistoryLen() int64 { return f.n }

func TestRebaseSkipsReplayedHistoryTheServerHas(t *testing.T) {
	// The server already stores 40 events: the 5 replayed history events are
	// not published again; local seq 6 (the first live event) becomes 41.
	hs := newHostSession(nil, "s1", historyFakeFeed{newFakeFeed("s1"), 5})
	if after := hs.rebase(1, 40); after != 5 || hs.base+6 != 41 {
		t.Fatalf("after %d base %d", after, hs.base)
	}
	// A session the server never saw publishes its history too.
	hs = newHostSession(nil, "s2", historyFakeFeed{newFakeFeed("s2"), 5})
	if after := hs.rebase(1, 0); after != 0 || hs.base != 0 {
		t.Fatalf("new: after %d base %d", after, hs.base)
	}
	// Feeds without history keep the old mapping.
	hs = newHostSession(nil, "s3", newFakeFeed("s3"))
	if after := hs.rebase(1, 40); after != 0 || hs.base != 40 {
		t.Fatalf("plain: after %d base %d", after, hs.base)
	}
}
