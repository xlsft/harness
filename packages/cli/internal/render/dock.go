package render

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Working is the state shown on the working line while a turn runs.
type Working struct {
	Status  string // "Thinking", "Writing", "Running Bash", "Waiting for approval"
	Elapsed time.Duration
	Tokens  int
	// Retry, when set, replaces the status with the error and a countdown (reference §9a).
	Retry *Retry
}

// Retry describes a pending automatic retry.
type Retry struct {
	Error   string
	In      time.Duration
	Attempt int
	Max     int
}

// WorkingLine renders `❯ ▰▰▱▱  Thinking · 12s · ↓ 1.1k tokens · esc to interrupt`.
func (s *Styles) WorkingLine(w Working, frame, width int) string {
	d := s.dot()
	var b strings.Builder
	b.WriteString(s.Env.Rail(frame))
	b.WriteString("  ")
	if r := w.Retry; r != nil {
		b.WriteString(s.Error.Render(r.Error))
		b.WriteString(s.Dim.Render(" " + d + " Retrying in " + Duration(r.In) + " " + d + " attempt " + itoa(r.Attempt) + "/" + itoa(r.Max) + " " + d + " esc to cancel"))
		return s.Truncate(b.String(), width)
	}
	b.WriteString(w.Status)
	stats := " " + d + " " + Duration(w.Elapsed)
	if w.Tokens > 0 {
		arrow := "↓"
		if !s.Env.Unicode {
			arrow = "v"
		}
		stats += " " + d + " " + arrow + " " + Tokens(w.Tokens) + " tokens"
	}
	stats += " " + d + " esc to interrupt"
	b.WriteString(s.Dim.Render(stats))
	return s.Truncate(b.String(), width)
}

// InputLines renders the prompt text with a block cursor at rune offset cursor: `❯ ` on the first
// row, continuation rows (newlines and soft wraps) indented 2. placeholder is shown dim when the
// text is empty.
func (s *Styles) InputLines(text []rune, cursor, width int, placeholder string, glyph string) []string {
	if glyph == "" {
		glyph = s.Accent.Render(s.prompt())
	}
	cur := lipgloss.NewStyle().Reverse(true)
	avail := max(width-2, 1)
	if len(text) == 0 {
		row := glyph + " " + cur.Render(" ")
		if placeholder != "" {
			ph := []rune(placeholder)
			row = glyph + " " + cur.Render(string(ph[0])) + s.Dim.Render(s.Truncate(string(ph[1:]), avail-1))
		}
		return []string{row}
	}
	// Split into visual rows, tracking where the cursor lands.
	type cell struct {
		r   rune
		cur bool
	}
	var rows [][]cell
	var row []cell
	col := 0
	flush := func() { rows = append(rows, row); row = nil; col = 0 }
	for i := 0; i <= len(text); i++ {
		isCur := i == cursor
		if i == len(text) {
			if isCur {
				if col >= avail {
					flush()
				}
				row = append(row, cell{' ', true})
			}
			break
		}
		r := text[i]
		if r == '\n' {
			if isCur {
				row = append(row, cell{' ', true})
			}
			flush()
			continue
		}
		w := ansi.StringWidth(string(r))
		if col+w > avail {
			flush()
		}
		row = append(row, cell{r, isCur})
		col += w
	}
	flush()
	out := make([]string, len(rows))
	for i, cells := range rows {
		var b strings.Builder
		if i == 0 {
			b.WriteString(glyph + " ")
		} else {
			b.WriteString("  ")
		}
		var run strings.Builder
		for _, c := range cells {
			if c.cur {
				b.WriteString(run.String())
				run.Reset()
				b.WriteString(cur.Render(string(c.r)))
				continue
			}
			run.WriteRune(c.r)
		}
		b.WriteString(run.String())
		out[i] = b.String()
	}
	return out
}

// InputBox renders the input between two full-width rules (reference §0: not a box). hint, when
// set, is right-aligned in the row above the top rule.
func (s *Styles) InputBox(lines []string, width int, hint string) string {
	var b strings.Builder
	if hint != "" {
		b.WriteString(PadLeft(s.Dim.Render(hint), width))
		b.WriteByte('\n')
	}
	b.WriteString(s.Rule(width))
	for _, l := range lines {
		b.WriteByte('\n')
		b.WriteString(l)
	}
	b.WriteByte('\n')
	b.WriteString(s.Rule(width))
	return b.String()
}

// Mode is the permission mode shown in the footer.
type Mode int

const (
	ModeDefault Mode = iota
	ModeAcceptEdits
	ModePlan
	ModeBypass
	ModeDontAsk
)

// Footer is the status row under the input.
type Footer struct {
	Mode       Mode
	Hint       string // contextual hint replacing the default ("? for shortcuts")
	Model      string
	ContextPct int // -1 when unknown
	Cost       float64
	Tasks      int // running background tasks
	HasText    bool
}

// ModeLabel renders the mode indicator (reference §1c with brand glyphs). The default mode shows
// in secondary text; other modes color the glyph and the "X on" text.
func (s *Styles) ModeLabel(m Mode) string {
	pause, play := "⏸", "⏵⏵"
	if !s.Env.Unicode {
		pause, play = "||", ">>"
	}
	cycle := s.Sec.Render(" (shift+tab to cycle)")
	switch m {
	case ModeAcceptEdits:
		return s.Accent.Render(play+" accept edits on") + cycle
	case ModePlan:
		return s.Success.Render(pause+" plan mode on") + cycle
	case ModeBypass:
		return s.Error.Render(play+" bypass permissions on") + cycle
	case ModeDontAsk:
		return s.Warning.Render(pause+" don't ask mode on") + cycle
	}
	return s.Sec.Render(pause + " manual mode on")
}

// FooterLine renders the footer row: mode and hints on the left (column 2), model, context and
// cost right-aligned.
func (s *Styles) FooterLine(f Footer, width int) string {
	d := s.Sec.Render(" " + s.dot() + " ")
	left := s.ModeLabel(f.Mode)
	switch {
	case f.Hint != "":
		left += d + s.Sec.Render(f.Hint)
	case f.Tasks > 0:
		left += d + s.Accent.Render(plural(f.Tasks, "background task", "background tasks")) + s.Sec.Render(" (/tasks)")
	case !f.HasText:
		left += d + s.Sec.Render("? for shortcuts")
	}
	var right []string
	if f.Model != "" {
		right = append(right, f.Model)
	}
	if f.ContextPct >= 0 {
		right = append(right, itoa(f.ContextPct)+"% context")
	}
	if f.Cost > 0 {
		right = append(right, Cost(f.Cost))
	}
	r := s.Dim.Render(strings.Join(right, " "+s.dot()+" "))
	left = "  " + left
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(r) - 2
	if gap < 2 || len(right) == 0 {
		return s.Truncate(left, width)
	}
	return left + strings.Repeat(" ", gap) + r
}

// Shortcuts is the `?` help grid that replaces the footer (reference §2b).
func (s *Styles) Shortcuts(width int) string {
	cols := [][]string{
		{"/ for commands", "@ for file paths", "\\ + enter for newline"},
		{"shift + tab to cycle modes", "ctrl + o for verbose output", "ctrl + t to toggle tasks"},
		{"esc to interrupt", "esc esc to rewind", "ctrl + c twice to exit"},
	}
	colW := []int{24, 35}
	if width < 100 {
		colW = []int{22, 30}
	}
	var out []string
	for r := 0; r < 3; r++ {
		row := "  " + PadRight(cols[0][r], colW[0]) + PadRight(cols[1][r], colW[1])
		if width >= 90 {
			row += cols[2][r]
		}
		out = append(out, s.Sec.Render(TrimRight(row)))
	}
	return strings.Join(out, "\n")
}

// MenuItem is one row of the `/` or `@` menu.
type MenuItem struct {
	Name string
	Desc string
	// Match holds rune offsets of Name to highlight (fuzzy match).
	Match []int
}

// MenuRows is the number of visible menu rows.
const MenuRows = 5

// Menu renders the popup above the input: a sliding window of MenuRows rows, the command in a
// fixed column, the description after it (truncated at wide widths, wrapped at narrow ones).
// The selected row carries the `❯` cursor and the selection style, so it stays visible without
// color.
func (s *Styles) Menu(items []MenuItem, selected, width int) string {
	if len(items) == 0 {
		return "  " + s.Dim.Render("No matches")
	}
	start := 0
	if selected >= MenuRows {
		start = selected - MenuRows + 1
	}
	end := min(len(items), start+MenuRows)
	nameW := 0
	for _, it := range items {
		nameW = max(nameW, ansi.StringWidth(it.Name))
	}
	nameW = min(nameW+4, max(width*2/5, 20))
	descW := width - 2 - nameW
	var out []string
	for i := start; i < end; i++ {
		it := items[i]
		sel := i == selected
		base := s.Sec
		if sel {
			base = s.Selected
		}
		name := s.highlight(it.Name, it.Match, base, sel)
		lead := "  "
		if sel {
			lead = s.Accent.Render(s.prompt()) + " "
		}
		row := lead + name + base.Render(strings.Repeat(" ", max(nameW-ansi.StringWidth(it.Name), 1)))
		if it.Desc != "" && descW > 8 {
			if width >= 100 {
				row += base.Render(s.Truncate(it.Desc, descW))
				out = append(out, row)
				continue
			}
			lines := Wrap(it.Desc, descW)
			if len(lines) > 2 {
				lines = lines[:2]
				lines[1] = s.Truncate(lines[1]+" "+s.ellipsis(), descW)
			}
			row += base.Render(lines[0])
			out = append(out, row)
			for _, l := range lines[1:] {
				out = append(out, strings.Repeat(" ", 2+nameW)+base.Render(l))
			}
			continue
		}
		out = append(out, TrimRight(row))
	}
	return strings.Join(out, "\n")
}

// highlight renders name with matched runes in the accent (bold without color).
func (s *Styles) highlight(name string, match []int, base lipgloss.Style, sel bool) string {
	if len(match) == 0 {
		return base.Render(name)
	}
	hl := s.Accent.Bold(true)
	if sel {
		hl = base.Underline(true)
	}
	m := map[int]bool{}
	for _, i := range match {
		m[i] = true
	}
	var b strings.Builder
	var run []rune
	runIsHL := false
	flush := func() {
		if len(run) == 0 {
			return
		}
		if runIsHL {
			b.WriteString(hl.Render(string(run)))
		} else {
			b.WriteString(base.Render(string(run)))
		}
		run = run[:0]
	}
	for i, r := range []rune(name) {
		if m[i] != runIsHL {
			flush()
			runIsHL = m[i]
		}
		run = append(run, r)
	}
	flush()
	return b.String()
}

// Todo is one entry of the pinned task list.
type Todo struct {
	Content    string
	ActiveForm string
	Status     int // 0 pending, 1 in progress, 2 completed
}

// TodoList renders the task list pinned above the input (reference §8a): a header with counts,
// then one row per task.
func (s *Styles) TodoList(todos []Todo, width int) string {
	done, prog := 0, 0
	for _, t := range todos {
		switch t.Status {
		case 2:
			done++
		case 1:
			prog++
		}
	}
	open := len(todos) - done - prog
	b := func(n int) string { return s.Sec.Bold(true).Render(itoa(n)) }
	head := "  " + b(len(todos)) + s.Sec.Render(" "+pluralNoun(len(todos), "task", "tasks")+" (") + b(done) + s.Sec.Render(" done, ") +
		b(prog) + s.Sec.Render(" in progress, ") + b(open) + s.Sec.Render(" open)")
	lines := []string{head}
	glyphs := [3]string{"◻", "◼", "✔"}
	if !s.Env.Unicode {
		glyphs = [3]string{"[ ]", "[>]", "[x]"}
	}
	for _, t := range todos {
		text := s.Truncate(t.Content, width-4)
		switch t.Status {
		case 1:
			lines = append(lines, "  "+s.Accent.Render(glyphs[1])+" "+s.Bold.Render(text))
		case 2:
			lines = append(lines, "  "+s.Success.Render(glyphs[2])+" "+s.Sec.Render("\x1b[9m"+text+"\x1b[29m"))
		default:
			lines = append(lines, "  "+glyphs[0]+" "+text)
		}
	}
	return strings.Join(lines, "\n")
}
