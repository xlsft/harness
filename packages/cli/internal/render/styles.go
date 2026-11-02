// Package render holds the pure rendering functions of the terminal UI: transcript items, tool
// groups, diffs, the permission box, menus, the working line and the footer. Every function takes
// the data and a width and returns a string, so they can be golden-tested without a terminal.
//
// Layout follows docs/design/cli-reference.md; colors, glyphs and loaders come from the brand
// package (docs/design/brand.md).
package render

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/xlsft/harness/packages/cli/brand"
)

// Styles resolves every role used by the UI for one environment.
type Styles struct {
	Env brand.Env

	Accent     lipgloss.Style // brand: prompt, bullets, titles
	AccentHigh lipgloss.Style // selection (bold + reverse without color)
	Sec        lipgloss.Style // secondary text
	Dim        lipgloss.Style // hints, metadata
	Line       lipgloss.Style // rules, borders, connectors
	Success    lipgloss.Style
	Warning    lipgloss.Style
	Error      lipgloss.Style
	Bold       lipgloss.Style
	Band       lipgloss.Style // user message band (background only)

	// Selected marks the selected row of a menu or option list. With color it is the accent
	// highlight; without color it is bold + reverse so the selection stays visible.
	Selected lipgloss.Style

	md *markdown
}

// New builds the styles for e.
func New(e brand.Env) *Styles {
	s := &Styles{
		Env:        e,
		Accent:     e.Fg(brand.Accent),
		AccentHigh: e.Fg(brand.AccentHigh),
		Sec:        e.Fg(brand.Secondary),
		Dim:        e.Fg(brand.Dim),
		Line:       e.Fg(brand.Line),
		Success:    e.Fg(brand.Success),
		Warning:    e.Fg(brand.Warning),
		Error:      e.Fg(brand.Error),
		Bold:       lipgloss.NewStyle().Bold(true),
		Band:       e.Bg(brand.Surface),
	}
	if e.Depth == brand.Depth16 {
		// brand.md: secondary text is the terminal default in 16 colors (the swatch's white
		// would vanish on light terminals).
		s.Sec = lipgloss.NewStyle()
	}
	s.Selected = s.AccentHigh
	if e.Depth == brand.DepthNone {
		s.Selected = lipgloss.NewStyle().Bold(true).Reverse(true)
	}
	return s
}

// Glyphs (brand.md, with ASCII fallbacks).
func (s *Styles) prompt() string {
	if s.Env.Unicode {
		return "❯"
	}
	return ">"
}

func (s *Styles) connector() string {
	if s.Env.Unicode {
		return "└"
	}
	return "`"
}

func (s *Styles) rule() string {
	if s.Env.Unicode {
		return "─"
	}
	return "-"
}

func (s *Styles) dot() string {
	if s.Env.Unicode {
		return "·"
	}
	return "-"
}

func (s *Styles) ellipsis() string {
	if s.Env.Unicode {
		return "…"
	}
	return "..."
}

// Rule renders a full-width horizontal rule in the line color.
func (s *Styles) Rule(width int) string {
	return s.Line.Render(strings.Repeat(s.rule(), max(width, 1)))
}

// ---- text helpers ----

// Wrap word-wraps text to width (hard-wrapping long tokens) and returns the lines.
func Wrap(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		w := ansi.Wrap(para, width, "")
		out = append(out, strings.Split(w, "\n")...)
	}
	return out
}

// Hang renders text with a first-line prefix and an indent for the following lines, wrapping to
// width. prefix and indent must have the same display width.
func Hang(prefix, indent, text string, width int) string {
	pw := ansi.StringWidth(indent)
	lines := Wrap(text, width-pw)
	for i, l := range lines {
		if i == 0 {
			lines[i] = prefix + l
		} else {
			lines[i] = indent + l
		}
	}
	return strings.Join(lines, "\n")
}

// Truncate cuts s to width display cells, ending with an ellipsis when cut.
func (s *Styles) Truncate(str string, width int) string {
	if ansi.StringWidth(str) <= width {
		return str
	}
	return ansi.Truncate(str, width, s.ellipsis())
}

// TrimRight removes trailing blanks from every line, keeping escape sequences intact.
func TrimRight(str string) string {
	lines := strings.Split(str, "\n")
	for i, l := range lines {
		plain := ansi.Strip(l)
		trimmed := strings.TrimRight(plain, " \t")
		if len(trimmed) == len(plain) {
			continue
		}
		cut := ansi.Truncate(l, ansi.StringWidth(trimmed), "")
		if strings.Contains(cut, "\x1b[") {
			cut += "\x1b[m"
		}
		lines[i] = cut
	}
	return strings.Join(lines, "\n")
}

// PadRight pads s with spaces to width display cells.
func PadRight(str string, width int) string {
	if w := ansi.StringWidth(str); w < width {
		return str + strings.Repeat(" ", width-w)
	}
	return str
}

// plural returns "1 file" / "2 files".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}

func itoa(n int) string { return strconv.Itoa(n) }
