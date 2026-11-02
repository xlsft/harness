package brand

import (
	"strings"
	"time"
)

// FrameInterval is the frame duration shared by all loaders.
const FrameInterval = 120 * time.Millisecond

// RailCells is the number of parallelogram cells after the chevron.
const RailCells = 4

// RailFrames is the length of one Rail cycle: the head crosses RailCells cells and the tail drains.
const RailFrames = RailCells + 3

var asciiRail = []string{">    ", ">/   ", ">//  ", "> // ", ">  //", ">   /"}

// Rail renders frame n of the main "working" indicator: a chevron followed by a run of
// parallelograms with a bright head and a fading tail (the logo mark in motion).
func (e Env) Rail(n int) string {
	if !e.Unicode {
		if e.ReducedMotion {
			return ">////"
		}
		return asciiRail[mod(n, len(asciiRail))]
	}
	var b strings.Builder
	b.WriteString(e.Fg(Accent).Render("❯"))
	b.WriteByte(' ')
	if e.ReducedMotion {
		b.WriteString(e.Fg(Accent).Render(strings.Repeat("▰", RailCells)))
		return b.String()
	}
	pos := mod(n, RailFrames)
	for k := 0; k < RailCells; k++ {
		switch pos - k {
		case 0:
			b.WriteString(e.Fg(AccentHigh).Render("▰"))
		case 1:
			b.WriteString(e.Fg(Accent).Render("▰"))
		case 2:
			b.WriteString(e.Fg(AccentLow).Render("▰"))
		default:
			b.WriteString(e.Fg(Line).Render("▱"))
		}
	}
	return b.String()
}

var pulseSteps = []Swatch{AccentLow, Accent, Swatch{Dark: "#51A2FF", Light: "#2B7FFF", C256: 75, Light256: 33, C16: 12, NoColor: AttrBold}, AccentHigh, Swatch{Dark: "#51A2FF", Light: "#2B7FFF", C256: 75, Light256: 33, C16: 12, NoColor: AttrBold}, Accent}

// Pulse renders frame n of the one-cell indicator for a running tool call.
func (e Env) Pulse(n int) string {
	if !e.Unicode {
		if mod(n, 2) == 0 || e.ReducedMotion {
			return ">"
		}
		return " "
	}
	if e.Depth <= Depth16 {
		glyph := "❯"
		if mod(n, 2) == 1 && !e.ReducedMotion {
			glyph = "›"
		}
		return e.Fg(Accent).Render(glyph)
	}
	if e.ReducedMotion {
		return e.Fg(Accent).Render("❯")
	}
	return e.Fg(pulseSteps[mod(n, len(pulseSteps))]).Render("❯")
}

// Bullet renders the settled bullet of a finished message or tool call.
func (e Env) Bullet(ok bool) string {
	g := "▰"
	if !e.Unicode {
		g = "*"
	}
	if ok {
		return e.Fg(Accent).Render(g)
	}
	return e.Fg(Error).Render(g)
}

func mod(n, m int) int {
	r := n % m
	if r < 0 {
		r += m
	}
	return r
}
