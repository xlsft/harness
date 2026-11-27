package selfupdate

import "testing"

func TestBoundsCheck(t *testing.T) {
	b := Bounds{Min: "0.2.0", Max: "v0.4.1"}
	for current, want := range map[string]string{
		"0.1.9": Below, "0.2.0-rc.1": Below, "0.0.0-dev": Below, "garbage": Below,
		"0.2.0": Within, "0.3.5": Within, "v0.4.1": Within,
		"0.4.2": Above, "1.0.0": Above,
	} {
		if got := b.Check(current); got != want {
			t.Errorf("%s: %q, want %q", current, got, want)
		}
	}
	if got := (Bounds{}).Check("0.0.0-dev"); got != Within {
		t.Errorf("no bounds: %q", got)
	}
	if (Bounds{Min: "soon"}).Set() {
		t.Error("an unreadable bound counts as one")
	}
	if got := (Bounds{Max: "0.1.0"}).Check("0.0.0-dev"); got != Within {
		t.Errorf("a dev build under a maximum only: %q", got)
	}
}

func TestBoundsTarget(t *testing.T) {
	cases := []struct {
		max, latest, current, want string
	}{
		{"", "v0.5.0", "0.4.0", "v0.5.0"},
		{"0.4.1", "v0.5.0", "0.4.0", "v0.4.1"},
		{"0.4.1", "v0.4.0", "0.3.0", "v0.4.0"},
		{"0.4.1", "v0.5.0", "0.4.1", ""},
		{"0.4.1", "v0.5.0", "0.4.5", ""}, // above the maximum already: never "updates" backwards
		{"", "v0.4.0", "0.4.0", ""},
	}
	for _, c := range cases {
		if got := (Bounds{Max: c.max}).Target(c.latest, c.current); got != c.want {
			t.Errorf("max %s latest %s current %s: %q, want %q", c.max, c.latest, c.current, got, c.want)
		}
	}
}

func TestBoundsMessage(t *testing.T) {
	b := Bounds{Min: "0.2.0", Max: "0.3.0"}
	if b.Message("harness-client", "0.2.5") != "" {
		t.Error("within: want no message")
	}
	if m := b.Message("harness-client", "0.1.0"); m == "" {
		t.Error("below: want a message")
	}
	if m := b.Message("harness-client", "0.4.0"); m == "" {
		t.Error("above: want a message")
	}
}
