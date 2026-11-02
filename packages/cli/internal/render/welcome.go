package render

import (
	"os"
	"path/filepath"
	"strings"
)

// Welcome is the information shown next to the logo mark at startup.
type Welcome struct {
	Version string
	Model   string
	User    string
	Cwd     string
	Branch  string
	Notices []string // startup warnings, one per line below the banner
}

// ShortPath abbreviates the home directory to ~ and long paths to /…/last/two.
func ShortPath(p string, width int) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		p = "~" + strings.TrimPrefix(p, home)
	}
	if len([]rune(p)) <= width {
		return p
	}
	parts := strings.Split(filepath.ToSlash(p), "/")
	if len(parts) > 3 {
		short := "/…/" + strings.Join(parts[len(parts)-2:], "/")
		if len([]rune(short)) <= width {
			return short
		}
	}
	r := []rune(p)
	return "…" + string(r[len(r)-width+1:])
}

// WelcomeBanner renders the brand mark (18×6, or 12×4 below 60 columns) with the info lines to
// its right, then the notices.
func (s *Styles) WelcomeBanner(w Welcome, width int) string {
	markW := 18
	if width < 60 || !s.Env.Unicode {
		markW = 12
	}
	infoW := max(width-markW-3, 10)
	d := " " + s.dot() + " "
	title := s.Accent.Bold(true).Render("harness") + s.Dim.Render(" "+w.Version)
	var who []string
	if w.Model != "" {
		who = append(who, w.Model)
	}
	if w.User != "" {
		who = append(who, w.User)
	}
	where := ShortPath(w.Cwd, infoW)
	if w.Branch != "" {
		where += "  (" + w.Branch + ")"
	}
	info := []string{
		title,
		s.Sec.Render(s.Truncate(strings.Join(who, d), infoW)),
		s.Sec.Render(s.Truncate(where, infoW)),
		s.Dim.Render(s.Truncate("/help for commands"+d+"shift+tab to switch modes", infoW)),
	}
	out := strings.TrimRight(s.Env.Banner(width, info), "\n")
	out = TrimRight(out)
	for _, n := range w.Notices {
		out += "\n\n" + s.Notice(1, n, width)
	}
	return out
}
