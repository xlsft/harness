package brand

import "charm.land/lipgloss/v2"

// Surface is the background band behind user messages (zinc-800 on dark terminals, zinc-100 on
// light ones). It has no 16-color or no-color value: those depths drop the band and rely on the
// `❯` glyph alone.
var Surface = Swatch{Dark: "#27272A", Light: "#F4F4F5", C256: 235, Light256: 255, C16: 0, NoColor: AttrNone}

// Bg returns a style with the swatch as background, or an empty style when the depth has no
// usable background for it (16 colors and no color).
func (e Env) Bg(s Swatch) lipgloss.Style {
	st := lipgloss.NewStyle()
	if e.Depth < Depth256 {
		return st
	}
	return st.Background(e.Color(s))
}

// Hex returns the swatch as a color string for libraries that take strings (glamour, chroma):
// "#rrggbb" in truecolor, the xterm index otherwise, "" without color.
func (e Env) Hex(s Swatch) string {
	switch e.Depth {
	case DepthTrue:
		if e.Theme == ThemeLight {
			return s.Light
		}
		return s.Dark
	case Depth256:
		if e.Theme == ThemeLight && s.Light256 != 0 {
			return itoa(int(s.Light256))
		}
		return itoa(int(s.C256))
	case Depth16:
		return itoa(int(s.C16))
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [4]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
