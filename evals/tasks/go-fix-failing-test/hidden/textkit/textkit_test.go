package textkit

import "testing"

func TestReverse(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"a":        "a",
		"hello":    "olleh",
		"héllo":    "olléh",
		"日本語":      "語本日",
		"go 🚀 now": "won 🚀 og",
	}
	for in, want := range cases {
		if got := Reverse(in); got != want {
			t.Errorf("Reverse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInitials(t *testing.T) {
	if got := Initials("grace brewster hopper"); got != "GBH" {
		t.Errorf("Initials = %q, want GBH", got)
	}
	if got := Initials("élan vital"); got != "ÉV" {
		t.Errorf("Initials = %q, want ÉV", got)
	}
}
