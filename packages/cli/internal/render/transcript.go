package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// UserPrompt renders a submitted user message: `❯ text` on a full-width band (256 colors and
// truecolor), continuation lines indented 2.
func (s *Styles) UserPrompt(text string, width int) string {
	lines := Wrap(expandTabs(strings.TrimRight(text, "\n")), width-2)
	banded := s.Env.Depth >= 2 // 256 colors and truecolor draw the band
	var out []string
	for i, l := range lines {
		switch {
		case banded && i == 0:
			out = append(out, s.Dim.Inherit(s.Band).Render(s.prompt())+s.Band.Render(" "+PadRight(l, width-2)))
		case banded:
			out = append(out, s.Band.Render(PadRight("  "+l, width)))
		case i == 0:
			out = append(out, s.Accent.Render(s.prompt())+" "+l)
		default:
			out = append(out, "  "+l)
		}
	}
	return strings.Join(out, "\n")
}

// Queued renders messages typed while a turn runs: dim, indented 2, with "(queued)".
func (s *Styles) Queued(texts []string, width int) string {
	var out []string
	for _, t := range texts {
		t = strings.ReplaceAll(strings.TrimSpace(t), "\n", " ")
		line := s.Truncate(s.prompt()+" "+t, width-2-len(" (queued)"))
		out = append(out, "  "+s.Dim.Render(line+" (queued)"))
	}
	return strings.Join(out, "\n")
}

// Assistant renders assistant markdown: `▰ ` on the first line, the rest indented 2. first is
// false for continuation chunks of a streamed message (no bullet, same indent).
func (s *Styles) Assistant(md string, width int, first bool) string {
	body := s.Markdown(md, width-2)
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		switch {
		case i == 0 && first:
			lines[i] = s.Env.Bullet(true) + " " + l
		case l == "":
		default:
			lines[i] = "  " + l
		}
	}
	return strings.Join(lines, "\n")
}

// Duration formats elapsed time like the reference: 0s, 13s, 2m 1s, 1h 2m.
func Duration(d time.Duration) string {
	sec := int(d.Round(time.Second).Seconds())
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm %ds", sec/60, sec%60)
	}
	return fmt.Sprintf("%dh %dm", sec/3600, sec%3600/60)
}

// Tokens formats a token count: 479, 1.1k, 12.3k, 1.2M.
func Tokens(n int) string {
	switch {
	case n < 1000:
		return itoa(n)
	case n < 1_000_000:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1000)) + "k"
	}
	return trimZero(fmt.Sprintf("%.1f", float64(n)/1e6)) + "M"
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

// Cost formats a USD amount.
func Cost(usd float64) string {
	switch {
	case usd == 0:
		return "$0"
	case usd < 0.01:
		return fmt.Sprintf("$%.4f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}

// TurnDone is the turn footer that stays in the transcript: `▰ Done in 15s` (success bullet,
// secondary text).
func (s *Styles) TurnDone(d time.Duration) string {
	return s.Success.Render(s.bulletGlyph()) + " " + s.Sec.Render("Done in "+Duration(d))
}

func (s *Styles) bulletGlyph() string {
	if s.Env.Unicode {
		return "▰"
	}
	return "*"
}

// Interrupted hangs under the interrupted message (reference §7b).
func (s *Styles) Interrupted() string {
	return s.result([]string{s.Sec.Render("Interrupted " + s.dot() + " What should harness do instead?")})
}

// ErrorMessage renders an error as a message with the error bullet (reference §9a).
func (s *Styles) ErrorMessage(text string, width int) string {
	return Hang(s.Env.Bullet(false)+" ", "  ", strings.TrimSpace(text), width)
}

// Notice renders a one-line notice: warning (`⚠`), error or info.
func (s *Styles) Notice(level int, text string, width int) string {
	glyph := "⚠"
	if !s.Env.Unicode {
		glyph = "!"
	}
	switch level {
	case 2:
		return Hang(s.Error.Render(glyph)+" ", "  ", s.Error.Render(text), width)
	case 1:
		return Hang(s.Warning.Render(glyph)+" ", "  ", s.Warning.Render(text), width)
	}
	return Hang("  ", "  ", s.Sec.Render(text), width)
}

// CommandOutput renders a local slash command: the command as a user message with its output
// hanging below (reference §3c, §9b).
func (s *Styles) CommandOutput(cmd, output string, width int) string {
	head := s.UserPrompt(cmd, width)
	if strings.TrimSpace(output) == "" {
		return head
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		lines = append(lines, l)
	}
	return head + "\n" + s.result(lines)
}

// Info renders "key: value" rows with the keys padded to one column (the /status layout).
func (s *Styles) Info(rows [][2]string) string {
	w := 0
	for _, r := range rows {
		w = max(w, ansi.StringWidth(r[0])+1)
	}
	var out []string
	for _, r := range rows {
		out = append(out, s.Bold.Render(PadRight(r[0]+":", w+1))+" "+r[1])
	}
	return strings.Join(out, "\n")
}
