package render

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// DiffLine is one row of a rendered diff: Kind is ' ' (context), '-' or '+'. No is the line
// number in the old file for removals and in the new file otherwise.
type DiffLine struct {
	Kind byte
	No   int
	Text string
}

// Hunk is a run of diff lines; hunks are separated by "..." when rendered.
type Hunk []DiffLine

// FileDiff is the change an Edit or Write makes to one file.
type FileDiff struct {
	Path    string
	Hunks   []Hunk
	Added   int
	Removed int
	// Created is set for a Write of a new file (rendered as a numbered preview, not a diff).
	Created bool
	Lines   []string // full content for Write previews
}

// PatchHunk is one hunk in Claude Code's structuredPatch shape (the client's tools.Hunk and the
// structured_result_json of Edit/Write): lines carry a ' ', '-' or '+' prefix.
type PatchHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// DiffFromPatch numbers the lines of a structured patch.
func DiffFromPatch(path string, patch []PatchHunk) *FileDiff {
	d := &FileDiff{Path: path}
	for _, ph := range patch {
		var h Hunk
		o, n := ph.OldStart, ph.NewStart
		for _, l := range ph.Lines {
			if l == "" {
				l = " "
			}
			k, text := l[0], l[1:]
			switch k {
			case '-':
				h = append(h, DiffLine{'-', o, text})
				o++
				d.Removed++
			case '+':
				h = append(h, DiffLine{'+', n, text})
				n++
				d.Added++
			case '\\':
				// "\ No newline at end of file"
			default:
				h = append(h, DiffLine{' ', n, text})
				o++
				n++
			}
		}
		d.Hunks = append(d.Hunks, h)
	}
	return d
}

// WriteDiff describes a Write of a new file: a numbered preview of content.
func WriteDiff(path, content string) *FileDiff {
	var lines []string
	if content != "" {
		lines = strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	}
	return &FileDiff{Path: path, Created: true, Lines: lines, Added: len(lines)}
}

// expandTabs replaces tabs with spaces up to the next multiple of 4 columns.
func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\n' {
			col = -1
		}
		if r == '\t' {
			n := 4 - col%4
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
}

// DiffBody renders hunks with line numbers and +/- markers, each row starting with indent.
// Added and removed rows are tinted across the full width (16 colors: text color only; no
// color: the markers alone, which is why they are always printed).
func (s *Styles) DiffBody(d *FileDiff, indent string, width int) []string {
	maxNo := 1
	for _, h := range d.Hunks {
		for _, l := range h {
			maxNo = max(maxNo, l.No)
		}
	}
	numW := len(itoa(maxNo)) + 1
	add, del := s.Env.DiffAdd(), s.Env.DiffDel()
	avail := width - ansi.StringWidth(indent)
	var out []string
	for hi, h := range d.Hunks {
		if hi > 0 {
			out = append(out, indent+s.Dim.Render(PadLeft("...", numW)))
		}
		for _, l := range h {
			num := PadLeft(itoa(l.No), numW)
			body := num + " " + string(l.Kind) + expandTabs(l.Text)
			rows := []string{body}
			if ansi.StringWidth(body) > avail && avail > numW+4 {
				rows = Wrap(body, avail)
				for k := 1; k < len(rows); k++ {
					rows[k] = strings.Repeat(" ", numW+2) + rows[k]
				}
			}
			for _, r := range rows {
				switch l.Kind {
				case '+':
					out = append(out, indent+add.Render(s.padTint(r, avail)))
				case '-':
					out = append(out, indent+del.Render(s.padTint(r, avail)))
				default:
					if strings.HasPrefix(r, num) {
						r = s.Dim.Render(num) + r[len(num):]
					}
					out = append(out, TrimRight(indent+r))
				}
			}
		}
	}
	return out
}

// padTint pads a tinted diff row to the full width when the depth draws a background.
func (s *Styles) padTint(r string, width int) string {
	if s.Env.Depth < 2 { // Depth256
		return r
	}
	return PadRight(r, width)
}

// WritePreview renders the first limit lines of a Write with line numbers, then "… +N lines".
func (s *Styles) WritePreview(d *FileDiff, indent string, width, limit int) []string {
	lines := d.Lines
	extra := 0
	if limit > 0 && len(lines) > limit {
		extra = len(lines) - limit
		lines = lines[:limit]
	}
	numW := len(itoa(len(d.Lines))) + 1
	var out []string
	for i, l := range lines {
		row := s.Dim.Render(PadLeft(itoa(i+1), numW)) + " " + expandTabs(l)
		out = append(out, TrimRight(indent+s.Truncate(row, width-ansi.StringWidth(indent))))
	}
	if extra > 0 {
		out = append(out, indent+s.Dim.Render(s.ellipsis()+" +"+plural(extra, "line", "lines")))
	}
	return out
}

// PadLeft right-aligns s in width cells.
func PadLeft(str string, width int) string {
	if w := ansi.StringWidth(str); w < width {
		return strings.Repeat(" ", width-w) + str
	}
	return str
}
