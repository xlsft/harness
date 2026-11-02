package brand

import "strings"

// The logo mark (chevron + two parallelogram bars) rasterized from docs/assets/logo.svg into
// half-block cells. Rows are padded to equal width.
var (
	Mark18x6 = []string{
		"████▄      ▄████▀ ",
		"▀█████▄  ▄████▀   ",
		"  ▀█████▄███▀     ",
		"  ▄█████▀ ▄███████",
		"▄█████▀ ▄████████▀",
		"████▀ ▄████████▀  ",
	}
	Mark12x4 = []string{
		"███▄   ▄██▀ ",
		" ▀███▄██▀   ",
		" ▄███▀▄█████",
		"███▀▄█████▀ ",
	}
	// MarkASCII is used when the terminal has no Unicode support.
	MarkASCII = []string{
		`\\  //`,
		` \\//_`,
		` //\\_`,
		`//  \\`,
	}
)

// Mark picks the mark for the available width (columns) and Unicode support.
func (e Env) Mark(width int) []string {
	switch {
	case !e.Unicode:
		return MarkASCII
	case width >= 60:
		return Mark18x6
	default:
		return Mark12x4
	}
}

// RenderMark renders the mark in the accent color, one string per row.
func (e Env) RenderMark(width int) []string {
	rows := e.Mark(width)
	st := e.Fg(Accent)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = st.Render(r)
	}
	return out
}

// Banner renders the welcome banner: the mark with info lines beside rows 2..5 (or 1..4 for the
// smaller marks). info[0] is the title line (e.g. "harness v0.1.0").
func (e Env) Banner(width int, info []string) string {
	rows := e.RenderMark(width)
	first := 1
	if len(rows) < 6 {
		first = 0
	}
	var b strings.Builder
	for i, r := range rows {
		b.WriteString(r)
		if j := i - first; j >= 0 && j < len(info) && info[j] != "" {
			b.WriteString("   ")
			b.WriteString(info[j])
		}
		b.WriteByte('\n')
	}
	return b.String()
}
