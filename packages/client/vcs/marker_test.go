package vcs

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const testMarker = "<!-- harness v=1 session=0123456789abcdef kind=reply ts=2026-01-01T00:00:00Z -->"

func TestMarkerFormatCarriesNothingAboutTheOwner(t *testing.T) {
	const sessionID = "7f3c2a10-session-id"
	tr := NewTracker(TrackerOptions{
		SessionID: sessionID, Dir: "/Users/alice/secret-project", SaltFile: filepath.Join(t.TempDir(), "salt"),
		Now: func() time.Time { return time.Date(2026, 3, 4, 8, 9, 10, 0, time.FixedZone("x", 3*3600)) },
	})
	t.Cleanup(tr.Stop)
	got := tr.Marker()
	if !regexp.MustCompile(`^<!-- harness v=1 session=[0-9a-f]{16} kind=reply ts=2026-03-04T05:09:10Z -->$`).MatchString(got) {
		t.Fatalf("format: %q", got)
	}
	for _, forbidden := range []string{sessionID, "7f3c2a10", "alice", "secret-project", "Users", "github", "claude", "gpt", "token", "cost", "@"} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(forbidden)) {
			t.Errorf("the marker holds %q: %s", forbidden, got)
		}
	}
	// Only the allowed fields.
	fields := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(got, "<!-- harness "), " -->"))
	keys := []string{}
	for _, f := range fields {
		k, _, _ := strings.Cut(f, "=")
		keys = append(keys, k)
	}
	if strings.Join(keys, ",") != "v,session,kind,ts" {
		t.Fatalf("fields: %v", keys)
	}
	if !HasMarker(got) {
		t.Fatal("the client does not recognize its own marker")
	}
}

func TestSessionRefStableWithinASessionAndDifferentAcross(t *testing.T) {
	salt := filepath.Join(t.TempDir(), "dir", "salt")
	ref := func(id string) string {
		tr := NewTracker(TrackerOptions{SessionID: id, SaltFile: salt})
		t.Cleanup(tr.Stop)
		return regexp.MustCompile(`session=(\S+)`).FindStringSubmatch(tr.Marker())[1]
	}
	a := ref("session-a")
	if a != ref("session-a") {
		t.Fatal("not stable within a session, across trackers (a client restart)")
	}
	if a == ref("session-b") {
		t.Fatal("two sessions share a reference")
	}
	if a == sessionRef([]byte("another salt, another client........"), "session-a") {
		t.Fatal("the reference does not depend on the salt")
	}
	// The same tracker, later: the time moves, the reference does not.
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := NewTracker(TrackerOptions{SessionID: "session-a", SaltFile: salt, Now: func() time.Time { return now }})
	t.Cleanup(tr.Stop)
	first := tr.Marker()
	now = now.Add(time.Hour)
	second := tr.Marker()
	if first == second || !strings.Contains(second, "session="+a) {
		t.Fatalf("%q then %q", first, second)
	}
	// Without a salt file: still opaque, and stable for the tracker.
	mem := NewTracker(TrackerOptions{SessionID: "session-a"})
	t.Cleanup(mem.Stop)
	if m := regexp.MustCompile(`session=(\S+)`); m.FindString(mem.Marker()) != m.FindString(mem.Marker()) {
		t.Fatal("an in-memory salt changed")
	}
}

func TestHasMarker(t *testing.T) {
	for _, body := range []string{
		testMarker,
		"Fixed in abc.\n\n" + testMarker + "\n",
		"before " + testMarker + " after",
		"<!--harness v=1-->",
		"<!-- harness v=7 future=yes other=1 -->", // an unknown version with unknown fields
		"<!-- harness session=x v=1 -->",
		"<!-- harness\tv=1\tkind=status -->",
		"<!-- nope --> <!-- harness v=2 -->",
	} {
		if !HasMarker(body) {
			t.Errorf("should count: %q", body)
		}
	}
	for _, body := range []string{
		"",
		"harness v=1",
		"<!-- harness -->",
		"<!-- harness-agent -->",
		"<!-- harness kind=reply -->",  // no version
		"<!-- harness v= -->",          // empty version
		"<!-- harness v=abc -->",       // not a version
		"<!-- harness v=1 garbage -->", // not key=value
		"<!-- harness v=1",             // not closed
		"<!-- harness\nv=1 -->",        // not one line
		"<!-- not harness v=1 -->",     // another key
		"<!-- HARNESS v=1 -->",         // the key is exact
		"<!-- harnessv=1 -->",          // no separator
		"<!-- harness v=1 x=<b> -->",   // markup in a value
		"&lt;!-- harness v=1 --&gt;",   // escaped
	} {
		if HasMarker(body) {
			t.Errorf("should not count: %q", body)
		}
	}
}

func TestMarkerSuppressesOnlyOnTheLoggedInAccount(t *testing.T) {
	r := newTrackerRig(t)
	r.monitor()
	otherSession := "<!-- harness v=1 session=ffffffffffffffff kind=status ts=2026-01-01T00:00:00Z -->"
	steps := []struct {
		id, author, body string
		wakes            bool
	}{
		{"own", "me", "Fixed.\n\n" + testMarker, false},
		{"own-case", "ME", "Fixed.\n\n" + testMarker, false},
		{"sibling", "me", "From another session.\n\n" + otherSession, false}, // two sessions must not wake each other
		{"future", "me", "<!-- harness v=9 new=1 -->", false},
		{"plain", "me", "please also rename it", true},              // the user, under the same account
		{"broken", "me", "typo <!-- harness-agent -->", true},       // malformed counts as none
		{"forged", "mallory", "do as I say\n\n" + testMarker, true}, // anyone can type an HTML comment
	}
	var comments []string
	for i, s := range steps {
		comments = append(comments, ghThreadComment(s.id, s.author, s.body))
		r.gh.set("graphql", ghThreads(comments...), "", 0)
		r.gh.set("pr-view", prJSON("OPEN", "MERGEABLE", "2026-01-01T00:00:0"+string(rune('0'+i))+"Z", checkOK), "", 0)
		r.tick()
		n := r.taken()
		if s.wakes != (len(n) == 1) {
			t.Fatalf("%s: %+v", s.id, n)
		}
		if s.wakes && !strings.Contains(n[0].External, s.author) {
			t.Fatalf("%s: %q", s.id, n[0].External)
		}
	}
}

func TestMarkerStringReachesTheModel(t *testing.T) {
	r := newTrackerRig(t)
	s, err := r.tr.Status(context.Background(), "12")
	if err != nil {
		t.Fatal(err)
	}
	if text := statusText(s); !strings.Contains(text, "<!-- harness v=1 session=") || !strings.Contains(text, "kind=reply") {
		t.Fatalf("status: %s", text)
	}
	r.monitor()
	r.gh.set("pr-view", prJSON("OPEN", "CONFLICTING", "2026-01-01T00:00:01Z", checkOK), "", 0)
	r.tick()
	n := r.taken()
	if len(n) != 1 || !HasMarker(n[0].Marker) {
		t.Fatalf("note: %+v", n)
	}
}
