package terminal

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The text of a terminal as a person sees it. Stripping escape sequences is not enough: shells redraw the line
// they edit, progress bars overwrite themselves with \r, full-screen programs position every character. So the
// output is replayed on a small screen model that knows the cursor, erasing, scroll regions and the alternate
// screen, and keeps the scrollback. Colors, modes and titles are dropped; soft-wrapped lines are joined again.

// wideRest fills the second column of a double-width character.
const wideRest = "\x00"

type screenLine struct {
	cells []string
	// wrapped: this line continues the previous one (the text ran past the last column).
	wrapped bool
}

type screenGrid struct {
	lines    []screenLine
	row, col int
	// top is the first line of the visible screen; what is above it is scrollback.
	top int
	// scrollTop and scrollBot are the scroll region, in screen rows; scrollBot < 0 means the whole screen.
	scrollTop, scrollBot int
	savedRow, savedCol   int
}

type screen struct {
	cols, rows int
	main, alt  *screenGrid
	g          *screenGrid
}

func newScreen(cols, rows int) *screen {
	if cols <= 0 || rows <= 0 {
		cols, rows = DefaultCols, DefaultRows
	}
	m := &screenGrid{scrollBot: -1}
	return &screen{cols: cols, rows: rows, main: m, g: m}
}

// renderScreen replays terminal output and returns the resulting lines, oldest first, without trailing blanks.
func renderScreen(data []byte, cols, rows int) []string {
	s := newScreen(cols, rows)
	s.write(data)
	return s.text()
}

func (s *screen) write(data []byte) {
	for i := 0; i < len(data); {
		b := data[i]
		switch {
		case b == 0x1b:
			i = s.escape(data, i+1)
		case b < 0x20 || b == 0x7f:
			s.control(b)
			i++
		case b < utf8.RuneSelf:
			s.put(string(rune(b)), 1)
			i++
		default:
			r, n := utf8.DecodeRune(data[i:])
			i += n
			s.putRune(r, n)
		}
	}
}

func (s *screen) control(b byte) {
	g := s.g
	switch b {
	case '\n', '\v', '\f':
		s.lineFeed()
	case '\r':
		g.col = 0
	case '\b':
		g.col = max(0, min(g.col, s.cols-1)-1)
	case '\t':
		g.col = min((min(g.col, s.cols-1)/8+1)*8, s.cols-1)
	}
}

func (s *screen) putRune(r rune, n int) {
	switch {
	case r == utf8.RuneError && n == 1, r < 0xa0, unicode.Is(unicode.Cf, r):
	case unicode.In(r, unicode.Mn, unicode.Me):
		s.combine(string(r))
	case wideRune(r):
		s.put(string(r), 2)
	default:
		s.put(string(r), 1)
	}
}

// combine adds a combining mark to the character before the cursor.
func (s *screen) combine(mark string) {
	g := s.g
	l := s.line(g.row)
	for c := min(g.col, len(l.cells)) - 1; c >= 0; c-- {
		if l.cells[c] != wideRest {
			l.cells[c] += mark
			return
		}
	}
}

func wideRune(r rune) bool {
	return r >= 0x1100 && (r <= 0x115f || // Hangul Jamo
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) || // CJK
		(r >= 0xac00 && r <= 0xd7a3) || // Hangul syllables
		(r >= 0xf900 && r <= 0xfaff) || // CJK compatibility
		(r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) || (r >= 0xffe0 && r <= 0xffe6) || // fullwidth forms
		(r >= 0x1f300 && r <= 0x1f64f) || (r >= 0x1f680 && r <= 0x1f6ff) || (r >= 0x1f900 && r <= 0x1f9ff) || // emoji
		(r >= 0x20000 && r <= 0x3fffd))
}

func (s *screen) line(row int) *screenLine {
	g := s.g
	for len(g.lines) <= row {
		g.lines = append(g.lines, screenLine{})
	}
	return &g.lines[row]
}

// put writes a character at the cursor. A cursor past the last column wraps first, so a line that exactly fills
// the width does not wrap until more text comes.
func (s *screen) put(cell string, width int) {
	g := s.g
	if g.col+width > s.cols {
		s.lineFeed()
		g.col = 0
		s.line(g.row).wrapped = true
	}
	l := s.line(g.row)
	for len(l.cells) < g.col+width {
		l.cells = append(l.cells, " ")
	}
	l.cells[g.col] = cell
	if width == 2 {
		l.cells[g.col+1] = wideRest
	}
	g.col += width
}

func (s *screen) regionBounds() (top, bot int) {
	g := s.g
	top, bot = g.top+g.scrollTop, g.top+s.rows-1
	if g.scrollBot >= 0 {
		bot = g.top + g.scrollBot
	}
	return top, bot
}

func (s *screen) lineFeed() {
	g := s.g
	top, bot := s.regionBounds()
	switch {
	case g.row == bot && (g.scrollBot >= 0 || g == s.alt):
		s.scrollUp(top, bot, 1)
	default:
		g.row++
		if g.row-g.top >= s.rows {
			g.top = g.row - s.rows + 1
		}
	}
}

func (s *screen) reverseIndex() {
	g := s.g
	top, bot := s.regionBounds()
	if g.row == top {
		s.scrollDown(top, bot, 1)
	} else if g.row > g.top {
		g.row--
	}
}

// scrollUp removes n lines at top inside [top, bot] and adds blank ones at bot.
func (s *screen) scrollUp(top, bot, n int) {
	s.line(bot)
	g := s.g
	n = min(n, bot-top+1)
	copy(g.lines[top:bot+1], g.lines[top+n:bot+1])
	for i := bot - n + 1; i <= bot; i++ {
		g.lines[i] = screenLine{}
	}
}

// scrollDown adds n blank lines at top inside [top, bot].
func (s *screen) scrollDown(top, bot, n int) {
	s.line(bot)
	g := s.g
	n = min(n, bot-top+1)
	copy(g.lines[top+n:bot+1], g.lines[top:bot+1-n])
	for i := top; i < top+n; i++ {
		g.lines[i] = screenLine{}
	}
}

// escape handles the sequence after an ESC at data[i-1] and returns where to go on.
func (s *screen) escape(data []byte, i int) int {
	if i >= len(data) {
		return i
	}
	g := s.g
	b := data[i]
	i++
	switch b {
	case '[':
		return s.csi(data, i)
	case ']', 'P', 'X', '^', '_':
		return skipString(data, i)
	case '(', ')', '*', '+', '-', '.', '/', '#', '%', ' ':
		return i + 1
	case '7':
		g.savedRow, g.savedCol = g.row-g.top, g.col
	case '8':
		g.row, g.col = g.top+g.savedRow, g.savedCol
	case 'M':
		s.reverseIndex()
	case 'D':
		s.lineFeed()
	case 'E':
		s.lineFeed()
		g.col = 0
	case 'c':
		*s = *newScreen(s.cols, s.rows)
	}
	return i
}

// skipString skips an OSC, DCS, SOS, PM or APC string: up to BEL or ST.
func skipString(data []byte, i int) int {
	for ; i < len(data); i++ {
		switch data[i] {
		case 0x07:
			return i + 1
		case 0x1b:
			if i+1 < len(data) && data[i+1] == '\\' {
				return i + 2
			}
			return i // another sequence starts: this one was cut
		}
	}
	return i
}

func (s *screen) csi(data []byte, i int) int {
	start := i
	for i < len(data) && (data[i] < 0x40 || data[i] > 0x7e) {
		if data[i] == 0x1b { // cut off by another sequence
			return i
		}
		i++
	}
	if i >= len(data) {
		return i
	}
	raw, final := string(data[start:i]), data[i]
	i++
	private := raw != "" && raw[0] >= 0x3c && raw[0] <= 0x3f
	if private {
		raw = raw[1:]
	}
	var params []int
	for _, p := range strings.Split(raw, ";") {
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				break
			}
			if n < 1<<20 {
				n = n*10 + int(c-'0')
			}
		}
		params = append(params, n)
	}
	arg := func(k, def int) int {
		if k < len(params) && params[k] > 0 {
			return params[k]
		}
		return def
	}
	if private {
		if final == 'h' || final == 'l' {
			for _, p := range params {
				if p == 1049 || p == 1047 || p == 47 {
					s.altScreen(final == 'h')
				}
			}
		}
		return i
	}
	g := s.g
	screenRow := func(r int) int { return g.top + min(max(r, 0), s.rows-1) }
	col := min(g.col, s.cols-1)
	switch final {
	case 'A':
		g.row = max(g.top, g.row-arg(0, 1))
		g.col = col
	case 'B', 'e':
		g.row = min(g.top+s.rows-1, g.row+arg(0, 1))
		g.col = col
	case 'C', 'a':
		g.col = min(s.cols-1, col+arg(0, 1))
	case 'D':
		g.col = max(0, col-arg(0, 1))
	case 'E':
		g.row, g.col = min(g.top+s.rows-1, g.row+arg(0, 1)), 0
	case 'F':
		g.row, g.col = max(g.top, g.row-arg(0, 1)), 0
	case 'G', '`':
		g.col = min(arg(0, 1)-1, s.cols-1)
	case 'd':
		g.row = screenRow(arg(0, 1) - 1)
	case 'H', 'f':
		g.row, g.col = screenRow(arg(0, 1)-1), min(arg(1, 1)-1, s.cols-1)
	case 'J':
		s.eraseDisplay(arg(0, 0))
	case 'K':
		s.eraseLine(arg(0, 0))
	case 'L', 'M':
		if top, bot := s.regionBounds(); g.row >= top && g.row <= bot {
			if final == 'L' {
				s.scrollDown(g.row, bot, arg(0, 1))
			} else {
				s.scrollUp(g.row, bot, arg(0, 1))
			}
		}
	case 'S':
		top, bot := s.regionBounds()
		s.scrollUp(top, bot, arg(0, 1))
	case 'T':
		top, bot := s.regionBounds()
		s.scrollDown(top, bot, arg(0, 1))
	case 'P':
		l := s.line(g.row)
		if col < len(l.cells) {
			l.cells = append(l.cells[:col], l.cells[min(col+arg(0, 1), len(l.cells)):]...)
		}
	case '@':
		l := s.line(g.row)
		if col < len(l.cells) {
			blanks := strings.Split(strings.Repeat(" ", min(arg(0, 1), s.cols)), "")
			l.cells = append(l.cells[:col:col], append(blanks, l.cells[col:]...)...)
			l.cells = l.cells[:min(len(l.cells), s.cols)]
		}
	case 'X':
		l := s.line(g.row)
		for c := col; c < min(col+arg(0, 1), len(l.cells)); c++ {
			l.cells[c] = " "
		}
	case 'r':
		g.scrollTop, g.scrollBot = arg(0, 1)-1, min(arg(1, s.rows)-1, s.rows-1)
		if g.scrollTop == 0 && g.scrollBot == s.rows-1 || g.scrollTop >= g.scrollBot {
			g.scrollTop, g.scrollBot = 0, -1
		}
		g.row, g.col = g.top, 0
	case 's':
		g.savedRow, g.savedCol = g.row-g.top, g.col
	case 'u':
		g.row, g.col = g.top+g.savedRow, g.savedCol
	}
	return i
}

func (s *screen) altScreen(on bool) {
	switch {
	case on && s.g != s.alt:
		s.alt = &screenGrid{scrollBot: -1}
		s.g = s.alt
	case !on:
		s.g = s.main
	}
}

func (s *screen) eraseLine(mode int) {
	g := s.g
	l := s.line(g.row)
	col := min(g.col, s.cols)
	switch mode {
	case 0:
		if col < len(l.cells) {
			l.cells = l.cells[:col]
		}
	case 1:
		for c := 0; c <= col && c < len(l.cells); c++ {
			l.cells[c] = " "
		}
	case 2:
		l.cells = nil
	}
}

func (s *screen) eraseDisplay(mode int) {
	g := s.g
	switch mode {
	case 0:
		s.eraseLine(0)
		for r := g.row + 1; r < len(g.lines); r++ {
			g.lines[r] = screenLine{}
		}
	case 1:
		s.eraseLine(1)
		for r := g.top; r < g.row && r < len(g.lines); r++ {
			g.lines[r] = screenLine{}
		}
	case 2:
		for r := g.top; r < len(g.lines); r++ {
			g.lines[r] = screenLine{}
		}
	case 3: // the scrollback goes; the visible screen stays
		if g.top > 0 {
			g.lines = g.lines[min(g.top, len(g.lines)):]
			g.row -= g.top
			g.top = 0
		}
	}
}

// text returns the lines of the current screen with their scrollback.
func (s *screen) text() []string {
	var out []string
	for _, l := range s.g.lines {
		var b strings.Builder
		for _, c := range l.cells {
			if c != wideRest {
				b.WriteString(c)
			}
		}
		text := strings.TrimRight(b.String(), " ")
		if l.wrapped && len(out) > 0 {
			out[len(out)-1] += text
		} else {
			out = append(out, text)
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}
