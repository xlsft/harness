package render

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Option is one numbered choice of a prompt.
type Option struct {
	Label string
	// Key is a bold suffix after the label, e.g. "(shift+tab)".
	Key string
	// Desc is a secondary line under the label (questions, rewind entries).
	Desc string
	// Checked is non-nil for multi-select options.
	Checked *bool
	// Placeholder is the dim text shown when the option is being amended with a comment.
	Placeholder string
}

// Prompt is a modal that replaces the input: permission requests, the plan approval, questions,
// the rewind menu. It renders as a rounded box (brand.md) with the reference's inner structure:
// title, subtitle, body between dashed rules, question, numbered options, hint.
type Prompt struct {
	Title    string
	Subtitle string
	Chips    []string // question headers shown as tabs above the title
	Body     []string // pre-rendered rows
	Dashed   bool     // frame Body with dashed rules
	Question string
	Options  []Option
	Selected int
	// Comment is the inline text field on the selected option (Tab to amend).
	Commenting bool
	Comment    []rune
	CommentAt  int
	Hint       string
	// More marks that Body was cut (shows "↓" at the right edge of the last body row).
	More bool
}

// PromptBox renders p at the full width.
func (s *Styles) PromptBox(p Prompt, width int) string {
	inner := max(width-4, 10)
	var rows []string
	add := func(r string) { rows = append(rows, r) }
	dash := "╌"
	if !s.Env.Unicode {
		dash = "-"
	}
	if len(p.Chips) > 0 {
		var chips []string
		for i, c := range p.Chips {
			box := "☐ "
			if !s.Env.Unicode {
				box = "[ ] "
			}
			if i == p.Selected && false {
				box = "☒ "
			}
			chips = append(chips, s.Selected.Render(" "+box+c+" "))
		}
		add(strings.Join(chips, " "))
		add("")
	}
	if p.Title != "" {
		add(s.Accent.Bold(true).Render(p.Title))
	}
	if p.Subtitle != "" {
		for _, l := range Wrap(p.Subtitle, inner) {
			add(s.Sec.Render(l))
		}
	}
	if len(p.Body) > 0 {
		if p.Dashed {
			add(s.Line.Render(strings.Repeat(dash, inner)))
		} else {
			add("")
		}
		for i, b := range p.Body {
			if p.More && i == len(p.Body)-1 {
				b = PadRight(s.Truncate(b, inner-2), inner-1) + s.Dim.Render("↓")
			}
			add(b)
		}
		if p.Dashed {
			add(s.Line.Render(strings.Repeat(dash, inner)))
		} else {
			add("")
		}
	}
	if p.Question != "" {
		for _, l := range Wrap(p.Question, inner) {
			add(l)
		}
	}
	for i, o := range p.Options {
		sel := i == p.Selected
		lead := "  "
		if sel {
			lead = s.Accent.Render(s.prompt()) + " "
		}
		num := s.Sec.Render(itoa(i+1) + ". ")
		check := ""
		if o.Checked != nil {
			check = "[ ] "
			if *o.Checked {
				check = "[x] "
			}
		}
		label := o.Label
		var text string
		if sel && p.Commenting {
			typed := s.InputLines(p.Comment, p.CommentAt, inner, o.Placeholder, " ")
			field := strings.TrimPrefix(typed[0], "  ")
			text = s.Selected.Render(check+label+",") + " " + field
		} else if sel {
			text = s.Selected.Render(check + label)
		} else {
			text = check + label
		}
		if o.Key != "" {
			text += " " + s.Bold.Render(o.Key)
		}
		rowIndent := strings.Repeat(" ", 2+ansi.StringWidth(itoa(i+1)+". "))
		add(Hang(lead+num, rowIndent, text, inner))
		if o.Desc != "" {
			for _, l := range Wrap(o.Desc, inner-len(rowIndent)) {
				add(rowIndent + s.Sec.Render(l))
			}
		}
	}
	if p.Hint != "" {
		add("")
		for _, l := range Wrap(p.Hint, inner) {
			add(s.Sec.Render(l))
		}
	}
	return s.box(rows, width)
}

// box draws rows inside a rounded border in the line color.
func (s *Styles) box(rows []string, width int) string {
	inner := max(width-4, 10)
	tl, tr, bl, br, h, v := "╭", "╮", "╰", "╯", "─", "│"
	if !s.Env.Unicode {
		tl, tr, bl, br, h, v = "+", "+", "+", "+", "-", "|"
	}
	var out []string
	out = append(out, s.Line.Render(tl+strings.Repeat(h, inner+2)+tr))
	for _, r := range rows {
		for _, l := range strings.Split(r, "\n") {
			if ansi.StringWidth(l) > inner {
				l = s.Truncate(l, inner)
			}
			out = append(out, s.Line.Render(v)+" "+PadRight(l, inner)+" "+s.Line.Render(v))
		}
	}
	out = append(out, s.Line.Render(bl+strings.Repeat(h, inner+2)+br))
	return strings.Join(out, "\n")
}

// CommandPreview renders a Bash command and its description for the permission body (indented
// 2 inside the box; long commands hard-wrap).
func (s *Styles) CommandPreview(cmd, desc string, width int) []string {
	inner := max(width-4, 10)
	var out []string
	for _, l := range Wrap(cmd, inner-2) {
		out = append(out, "  "+l)
	}
	if desc != "" {
		for _, l := range Wrap(desc, inner-2) {
			out = append(out, "  "+s.Sec.Render(l))
		}
	}
	return out
}
