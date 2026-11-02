// Package brand holds harness's terminal identity: the color palette with explicit fallbacks for
// every color depth, the logo mark and the loaders. The spec is docs/design/brand.md; keep the two
// in sync.
package brand

import (
	"image/color"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// Depth is the color capability the output is rendered for.
type Depth int

const (
	DepthNone Depth = iota // NO_COLOR or not a terminal: attributes only (bold, dim, reverse)
	Depth16                // ANSI 16 colors
	Depth256               // xterm 256 colors
	DepthTrue              // 24-bit color
)

// Theme is the terminal background the palette is tuned for.
type Theme int

const (
	ThemeDark Theme = iota
	ThemeLight
)

// Attr is the fallback used when there is no color at all.
type Attr int

const (
	AttrNone Attr = iota
	AttrBold
	AttrDim
	AttrBoldReverse
)

// Swatch is one palette role with its value at every depth and theme.
type Swatch struct {
	Dark, Light string // truecolor hex
	C256        uint8  // xterm-256 index (dark theme; light terminals use Light256 when set)
	Light256    uint8
	C16         uint8 // 0–15 (8–15 are the bright variants)
	NoColor     Attr
}

// Palette roles. Tailwind v4: zinc (monochrome), blue (accent), emerald / amber / rose (status).
var (
	Accent     = Swatch{Dark: "#2B7FFF", Light: "#155DFC", C256: 33, Light256: 26, C16: 4, NoColor: AttrBold}
	AccentHigh = Swatch{Dark: "#8EC5FF", Light: "#1447E6", C256: 117, Light256: 26, C16: 12, NoColor: AttrBoldReverse}
	AccentLow  = Swatch{Dark: "#155DFC", Light: "#51A2FF", C256: 26, Light256: 75, C16: 4, NoColor: AttrDim}
	Secondary  = Swatch{Dark: "#A1A1AA", Light: "#52525B", C256: 248, Light256: 240, C16: 7, NoColor: AttrNone}
	Dim        = Swatch{Dark: "#71717A", Light: "#71717A", C256: 243, Light256: 243, C16: 8, NoColor: AttrDim}
	Line       = Swatch{Dark: "#3F3F46", Light: "#D4D4D8", C256: 238, Light256: 252, C16: 8, NoColor: AttrDim}
	Success    = Swatch{Dark: "#00D492", Light: "#009966", C256: 42, Light256: 29, C16: 2, NoColor: AttrBold}
	Warning    = Swatch{Dark: "#FFB900", Light: "#E17100", C256: 214, Light256: 166, C16: 3, NoColor: AttrBold}
	Error      = Swatch{Dark: "#FF637E", Light: "#EC003F", C256: 204, Light256: 161, C16: 1, NoColor: AttrBold}
	DiffAddBg  = Swatch{Dark: "#002C22", Light: "#D0FAE5", C256: 22, Light256: 194, C16: 0, NoColor: AttrNone}
	DiffAddFg  = Swatch{Dark: "#5EE9B5", Light: "#006045", C256: 79, Light256: 22, C16: 2, NoColor: AttrNone}
	DiffDelBg  = Swatch{Dark: "#4D0218", Light: "#FFE4E6", C256: 52, Light256: 224, C16: 0, NoColor: AttrNone}
	DiffDelFg  = Swatch{Dark: "#FFA1AD", Light: "#A50036", C256: 217, Light256: 88, C16: 1, NoColor: AttrNone}
)

// Env describes how output is rendered. Detect fills it from the terminal and environment.
type Env struct {
	Depth         Depth
	Theme         Theme
	Unicode       bool
	ReducedMotion bool
}

// Detect inspects stdout, the environment and (for the theme) the terminal.
//
// Overrides: NO_COLOR, HARNESS_COLOR=truecolor|256|16|none, HARNESS_THEME=dark|light,
// HARNESS_ASCII=1, HARNESS_REDUCED_MOTION=1.
func Detect() Env {
	env := os.Environ()
	e := Env{Depth: depthFromProfile(colorprofile.Detect(os.Stdout, env)), Theme: ThemeDark, Unicode: unicodeLocale()}
	switch strings.ToLower(os.Getenv("HARNESS_COLOR")) {
	case "truecolor", "24bit":
		e.Depth = DepthTrue
	case "256":
		e.Depth = Depth256
	case "16":
		e.Depth = Depth16
	case "none":
		e.Depth = DepthNone
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		e.Depth = DepthNone
	}
	switch strings.ToLower(os.Getenv("HARNESS_THEME")) {
	case "light":
		e.Theme = ThemeLight
	case "dark":
		e.Theme = ThemeDark
	default:
		if e.Depth != DepthNone && !lipgloss.HasDarkBackground(os.Stdin, os.Stdout) {
			e.Theme = ThemeLight
		}
	}
	if os.Getenv("HARNESS_ASCII") == "1" {
		e.Unicode = false
	}
	e.ReducedMotion = os.Getenv("HARNESS_REDUCED_MOTION") == "1"
	return e
}

func depthFromProfile(p colorprofile.Profile) Depth {
	switch p {
	case colorprofile.TrueColor:
		return DepthTrue
	case colorprofile.ANSI256:
		return Depth256
	case colorprofile.ANSI:
		return Depth16
	default:
		return DepthNone
	}
}

func unicodeLocale() bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := os.Getenv(k); v != "" {
			v = strings.ToUpper(v)
			return strings.Contains(v, "UTF-8") || strings.Contains(v, "UTF8")
		}
	}
	return true // most modern terminals default to UTF-8 even without a locale
}

// Color resolves a swatch for this environment; nil means "no color" (use Attr instead).
func (e Env) Color(s Swatch) color.Color {
	switch e.Depth {
	case DepthTrue:
		if e.Theme == ThemeLight {
			return lipgloss.Color(s.Light)
		}
		return lipgloss.Color(s.Dark)
	case Depth256:
		if e.Theme == ThemeLight && s.Light256 != 0 {
			return lipgloss.ANSIColor(s.Light256)
		}
		return lipgloss.ANSIColor(s.C256)
	case Depth16:
		// Basic SGR (30–37 / 90–97) so 16-color themes remap it, unlike the 38;5;n form.
		return ansi.BasicColor(s.C16)
	}
	return nil
}

// Fg returns a style with the swatch as foreground, or its attribute fallback without color.
func (e Env) Fg(s Swatch) lipgloss.Style {
	st := lipgloss.NewStyle()
	if c := e.Color(s); c != nil {
		return st.Foreground(c)
	}
	switch s.NoColor {
	case AttrBold:
		return st.Bold(true)
	case AttrDim:
		return st.Faint(true)
	case AttrBoldReverse:
		return st.Bold(true).Reverse(true)
	}
	return st
}

// DiffAdd and DiffDel style added / removed diff lines. In 16 colors only the foreground is used
// (backgrounds are unreadable on many 16-color themes); without color the caller relies on +/- prefixes.
func (e Env) DiffAdd() lipgloss.Style { return e.diff(DiffAddBg, DiffAddFg) }
func (e Env) DiffDel() lipgloss.Style { return e.diff(DiffDelBg, DiffDelFg) }

func (e Env) diff(bg, fg Swatch) lipgloss.Style {
	st := lipgloss.NewStyle()
	switch e.Depth {
	case DepthTrue, Depth256:
		return st.Background(e.Color(bg)).Foreground(e.Color(fg))
	case Depth16:
		return st.Foreground(e.Color(fg))
	}
	return st
}
