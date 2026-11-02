package render

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Post-processing for glamour output, to match the reference rendering (cli-reference §3):
// tables as compact box-drawing grids in the default foreground, links as "text (url)" without OSC 8 sequences, and list
// items with a hanging indent.

// osc8 matches OSC 8 hyperlink open/close sequences (terminated by ST or BEL).
var osc8 = regexp.MustCompile("\x1b\\]8;[^\x07\x1b]*(\x1b\\\\|\x07)")

func stripOSC8(s string) string { return osc8.ReplaceAllString(s, "") }

// tableRow matches a GFM table row; tableSep matches its header separator row.
var (
	tableRow = regexp.MustCompile(`^\s*\|.*\|\s*$`)
	tableSep = regexp.MustCompile(`^\s*\|?\s*:?-{1,}:?\s*(\|\s*:?-{1,}:?\s*)*\|?\s*$`)
	// inlineMarks strips the inline markdown glamour would style inside a cell.
	inlineMarks = regexp.MustCompile("(\\*\\*|__|`)")
	mdLink      = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
)

// block is a piece of the source rendered apart from glamour's main pass: a GFM table, or a
// top-level fenced code block (kept out so list post-processing never touches code).
type block struct {
	rows [][]string // table cells; nil for code
	code string     // the fenced block's source
}

const placeholder = "HARNESSBLOCK"

func placeholderFor(i int) string { return placeholder + strconv.Itoa(i) + "Z" }

// extractBlocks replaces tables and top-level fenced code blocks with placeholder paragraphs.
func extractBlocks(src string) (string, []block) {
	lines := strings.Split(src, "\n")
	var out []string
	var blocks []block
	put := func(b block) {
		out = append(out, "", placeholderFor(len(blocks)), "")
		blocks = append(blocks, b)
	}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if fence := fenceOf(l); fence != "" && !strings.HasPrefix(l, " ") {
			j := i + 1
			for j < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[j]), fence) {
				j++
			}
			end := min(j, len(lines)-1)
			put(block{code: strings.Join(lines[i:end+1], "\n")})
			i = end
			continue
		}
		if tableRow.MatchString(l) && i+1 < len(lines) && tableSep.MatchString(lines[i+1]) {
			rows := [][]string{splitRow(l)}
			i += 2
			for ; i < len(lines) && tableRow.MatchString(lines[i]); i++ {
				rows = append(rows, splitRow(lines[i]))
			}
			i--
			put(block{rows: rows})
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n"), blocks
}

// fenceOf returns the fence marker a line opens ("```" or "~~~", possibly longer), or "".
func fenceOf(l string) string {
	t := strings.TrimLeft(l, " ")
	for _, c := range []string{"`", "~"} {
		n := len(t) - len(strings.TrimLeft(t, c))
		if n >= 3 {
			return strings.Repeat(c, n)
		}
	}
	return ""
}

// restoreBlocks swaps placeholder lines in rendered output for the rendered blocks.
func restoreBlocks(out string, blocks []block, render func(block) string) string {
	if len(blocks) == 0 {
		return out
	}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(ansi.Strip(l))
		if !strings.HasPrefix(t, placeholder) || !strings.HasSuffix(t, "Z") {
			continue
		}
		n, err := strconv.Atoi(t[len(placeholder) : len(t)-1])
		if err != nil || n < 0 || n >= len(blocks) {
			continue
		}
		lines[i] = render(blocks[n])
	}
	return strings.Join(lines, "\n")
}

func splitRow(l string) []string {
	l = strings.TrimSpace(l)
	l = strings.TrimPrefix(l, "|")
	l = strings.TrimSuffix(l, "|")
	var cells []string
	var cur strings.Builder
	escaped := false
	for _, r := range l {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '|':
			cells = append(cells, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	cells = append(cells, cur.String())
	for i, c := range cells {
		c = mdLink.ReplaceAllString(strings.TrimSpace(c), "$1 ($2)")
		cells[i] = inlineMarks.ReplaceAllString(c, "")
	}
	return cells
}

// renderTable draws rows as a box-drawing grid at most width columns wide: one space of cell
// padding, a separator between every row, header not bold. Too-wide tables wrap inside cells.
func renderTable(rows [][]string, width int, unicode bool) string {
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return ""
	}
	w := make([]int, cols)
	for _, r := range rows {
		for i, c := range r {
			w[i] = max(w[i], ansi.StringWidth(c))
		}
	}
	for i := range w {
		w[i] = max(w[i], 1)
	}
	// Total = sum(w) + 3*cols + 1 (borders and padding). Shrink the widest columns to fit.
	budget := width - 3*cols - 1
	for sum(w) > budget && budget >= cols {
		widest := 0
		for i := range w {
			if w[i] > w[widest] {
				widest = i
			}
		}
		if w[widest] <= 1 {
			break
		}
		w[widest]--
	}
	h, v := "─", "│"
	tl, tm, tr := "┌", "┬", "┐"
	ml, mm, mr := "├", "┼", "┤"
	bl, bm, br := "└", "┴", "┘"
	if !unicode {
		h, v = "-", "|"
		tl, tm, tr, ml, mm, mr, bl, bm, br = "+", "+", "+", "+", "+", "+", "+", "+", "+"
	}
	rule := func(l, m, r string) string {
		parts := make([]string, cols)
		for i := range w {
			parts[i] = strings.Repeat(h, w[i]+2)
		}
		return l + strings.Join(parts, m) + r
	}
	var b strings.Builder
	b.WriteString(rule(tl, tm, tr))
	for ri, r := range rows {
		cells := make([][]string, cols)
		height := 1
		for i := 0; i < cols; i++ {
			text := ""
			if i < len(r) {
				text = r[i]
			}
			wrapped := strings.Split(ansi.Wrap(text, w[i], ""), "\n")
			cells[i] = wrapped
			height = max(height, len(wrapped))
		}
		for li := 0; li < height; li++ {
			b.WriteString("\n" + v)
			for i := 0; i < cols; i++ {
				text := ""
				if li < len(cells[i]) {
					text = cells[i][li]
				}
				b.WriteString(" " + text + strings.Repeat(" ", max(0, w[i]-ansi.StringWidth(text))) + " " + v)
			}
		}
		if ri < len(rows)-1 {
			b.WriteString("\n" + rule(ml, mm, mr))
		}
	}
	b.WriteString("\n" + rule(bl, bm, br))
	return b.String()
}

func sum(xs []int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}

// listItem matches a rendered list item: indent, then "- ", "N. " or a task box.
var listItem = regexp.MustCompile(`^(\s*)(- |\d+\. |\[[ x]\] )`)

// hangLists re-wraps list items so continuation lines align with the item text instead of the
// bullet. It works on glamour's output, whose continuation lines start at the bullet's column.
func hangLists(out string, width int) string {
	lines := strings.Split(out, "\n")
	var res []string
	for i := 0; i < len(lines); i++ {
		plain := ansi.Strip(lines[i])
		m := listItem.FindStringSubmatch(plain)
		if m == nil {
			res = append(res, lines[i])
			continue
		}
		indent := len(m[1])
		marker := len(m[2])
		// Collect continuation lines: same indent, not a new item, not blank.
		text := []string{strings.TrimLeft(lines[i], " ")}
		j := i + 1
		for ; j < len(lines); j++ {
			p := ansi.Strip(lines[j])
			if strings.TrimSpace(p) == "" || listItem.MatchString(p) || leadingSpaces(p) != indent {
				break
			}
			text = append(text, strings.TrimLeft(lines[j], " "))
		}
		if len(text) == 1 {
			res = append(res, lines[i])
			continue
		}
		joined := strings.Join(text, " ")
		// Peel the marker off (it may be wrapped in SGR codes) by plain-text length.
		head := ansi.Truncate(joined, marker, "")
		body := strings.TrimLeft(ansi.TruncateLeft(joined, marker, ""), " ")
		avail := max(width-indent-marker, 10)
		wrapped := strings.Split(ansi.Wordwrap(body, avail, ""), "\n")
		pad := strings.Repeat(" ", indent)
		res = append(res, pad+head+wrapped[0])
		for _, wl := range wrapped[1:] {
			res = append(res, pad+strings.Repeat(" ", marker)+strings.TrimLeft(wl, " "))
		}
		i = j - 1
	}
	return strings.Join(res, "\n")
}

func leadingSpaces(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
