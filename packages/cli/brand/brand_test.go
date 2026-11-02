package brand

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func envs() map[string]Env {
	return map[string]Env{
		"true-dark":  {Depth: DepthTrue, Theme: ThemeDark, Unicode: true},
		"true-light": {Depth: DepthTrue, Theme: ThemeLight, Unicode: true},
		"256":        {Depth: Depth256, Unicode: true},
		"16":         {Depth: Depth16, Unicode: true},
		"none":       {Depth: DepthNone, Unicode: true},
		"ascii":      {Depth: Depth16, Unicode: false},
	}
}

func TestRailFramesHaveStableWidth(t *testing.T) {
	for name, e := range envs() {
		for n := 0; n < 2*RailFrames; n++ {
			if w := ansi.StringWidth(e.Rail(n)); w != 2+RailCells && w != 5 {
				t.Fatalf("%s frame %d: width %d", name, n, w)
			}
		}
	}
}

func TestRailHeadMoves(t *testing.T) {
	e := Env{Depth: DepthNone, Unicode: true}
	var seen []string
	for n := 0; n < RailFrames; n++ {
		seen = append(seen, ansi.Strip(e.Rail(n)))
	}
	want := []string{"❯ ▰▱▱▱", "❯ ▰▰▱▱", "❯ ▰▰▰▱", "❯ ▱▰▰▰", "❯ ▱▱▰▰", "❯ ▱▱▱▰", "❯ ▱▱▱▱"}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("frames:\n got %q\nwant %q", seen, want)
	}
}

func TestReducedMotionIsStatic(t *testing.T) {
	e := Env{Depth: DepthTrue, Unicode: true, ReducedMotion: true}
	if e.Rail(0) != e.Rail(3) || e.Pulse(0) != e.Pulse(3) {
		t.Fatal("reduced motion must not animate")
	}
}

func TestColorDepths(t *testing.T) {
	cases := map[string]string{
		"true-dark":  "38;2;43;127;255",
		"true-light": "38;2;21;93;252",
		"256":        "38;5;33",
		"16":         "34",
	}
	for name, sgr := range cases {
		out := envs()[name].Fg(Accent).Render("x")
		if !strings.Contains(out, sgr) {
			t.Errorf("%s: %q does not contain %q", name, out, sgr)
		}
	}
	if out := envs()["none"].Fg(Accent).Render("x"); !strings.Contains(out, "\x1b[1m") || strings.Contains(out, "38;") {
		t.Errorf("no-color accent should be bold only: %q", out)
	}
}

func TestDiff16UsesForegroundOnly(t *testing.T) {
	out := envs()["16"].DiffAdd().Render("+x")
	if strings.Contains(out, "4") && strings.Contains(out, "[4") {
		t.Fatalf("unexpected background in 16 colors: %q", out)
	}
	if !strings.Contains(out, "32") {
		t.Fatalf("expected green foreground: %q", out)
	}
}

func TestMarkSelection(t *testing.T) {
	e := Env{Unicode: true}
	if len(e.Mark(120)) != 6 || len(e.Mark(40)) != 4 {
		t.Fatal("wrong mark for width")
	}
	if got := (Env{Unicode: false}).Mark(120); got[0] != MarkASCII[0] {
		t.Fatal("ascii mark expected without unicode")
	}
	for _, rows := range [][]string{Mark18x6, Mark12x4} {
		for _, r := range rows {
			if ansi.StringWidth(r) != ansi.StringWidth(rows[0]) {
				t.Fatalf("ragged mark row %q", r)
			}
		}
	}
}
