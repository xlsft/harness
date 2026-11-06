package documents

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Text fit needs the real widths of the fonts a slide uses. A font that is not on this machine cannot be measured: the
// check says "unverified" for it, never a pass.

const (
	defaultInsetX     = 91440
	defaultInsetY     = 45720
	defaultFontPt     = 18.0
	lineHeightFactor  = 1.2
	maxFontFileBytes  = 40 << 20
	maxFontFilesScan  = 3000
	fontMeasurePpem   = 1000
	overflowTolerance = 1.02
)

// FontDirs are searched for fonts; tests replace them.
var FontDirs = defaultFontDirs

func defaultFontDirs() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return []string{"/System/Library/Fonts", "/Library/Fonts", filepath.Join(home, "Library/Fonts"), "/Applications/Microsoft Word.app/Contents/Resources/DFonts", "/Applications/Microsoft PowerPoint.app/Contents/Resources/DFonts"}
	case "windows":
		return []string{filepath.Join(os.Getenv("WINDIR"), "Fonts")}
	}
	return []string{"/usr/share/fonts", "/usr/local/share/fonts", filepath.Join(home, ".fonts"), filepath.Join(home, ".local/share/fonts")}
}

type fontIndex struct {
	mu    sync.Mutex
	files []string
	seen  map[string]*sfnt.Font
	// tried holds what was looked for, found or not.
	tried map[string]*sfnt.Font
}

var fonts = &fontIndex{}

// ResetFontIndex forgets what was scanned; tests that replace FontDirs call it.
func ResetFontIndex() { fonts = &fontIndex{} }

func (f *fontIndex) scan() {
	if f.tried != nil {
		return
	}
	f.tried = map[string]*sfnt.Font{}
	for _, dir := range FontDirs() {
		_ = filepath.WalkDir(dir, func(p string, de os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if len(f.files) >= maxFontFilesScan {
				return filepath.SkipAll
			}
			if de.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(p)) {
			case ".ttf", ".otf", ".ttc", ".otc":
				f.files = append(f.files, p)
			}
			return nil
		})
	}
}

var fontNameNormRe = regexp.MustCompile(`[^a-z0-9]`)

func normFont(s string) string { return fontNameNormRe.ReplaceAllString(strings.ToLower(s), "") }

// lookup finds a typeface by family name. The file name is tried first, since reading every font is slow.
func (f *fontIndex) lookup(name string) *sfnt.Font {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scan()
	want := normFont(name)
	if want == "" {
		return nil
	}
	if ft, ok := f.tried[want]; ok {
		return ft
	}
	var found *sfnt.Font
	var rest []string
	for _, p := range f.files {
		base := normFont(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
		if strings.HasPrefix(base, want) && (base == want || strings.HasPrefix(strings.TrimPrefix(base, want), "regular") || strings.HasPrefix(strings.TrimPrefix(base, want), "mt")) {
			if ft := matchFont(p, want); ft != nil {
				found = ft
				break
			}
		} else {
			rest = append(rest, p)
		}
	}
	if found == nil {
		for _, p := range rest {
			if ft := matchFont(p, want); ft != nil {
				found = ft
				break
			}
		}
	}
	f.tried[want] = found
	return found
}

func matchFont(p, want string) *sfnt.Font {
	st, err := os.Stat(p)
	if err != nil || st.Size() > maxFontFileBytes {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var list []*sfnt.Font
	if c, err := sfnt.ParseCollection(data); err == nil {
		for i := 0; i < c.NumFonts(); i++ {
			if ft, err := c.Font(i); err == nil {
				list = append(list, ft)
			}
		}
	} else if ft, err := sfnt.Parse(data); err == nil {
		list = append(list, ft)
	}
	var buf sfnt.Buffer
	for _, ft := range list {
		if n, err := ft.Name(&buf, sfnt.NameIDFamily); err == nil && normFont(n) == want {
			return ft
		}
	}
	return nil
}

// measure returns the width of s in EMU at the given size.
func measure(ft *sfnt.Font, s string, pt float64) int64 {
	var buf sfnt.Buffer
	var total fixed.Int26_6
	ppem := fixed.I(fontMeasurePpem)
	for _, r := range s {
		g, err := ft.GlyphIndex(&buf, r)
		if err != nil || g == 0 {
			total += ppem / 2 // a character the font lacks: a typical width
			continue
		}
		a, err := ft.GlyphAdvance(&buf, g, ppem, font.HintingNone)
		if err != nil {
			continue
		}
		total += a
	}
	return int64(float64(total) / 64 / fontMeasurePpem * pt * emuPerPoint)
}

// wrappedLines counts the lines a paragraph takes in a given width, breaking at spaces.
func wrappedLines(ft *sfnt.Font, text string, pt float64, width int64) int {
	lines := 0
	for _, hard := range strings.Split(text, "\v") {
		cur := int64(0)
		n := 1
		space := measure(ft, " ", pt)
		for _, word := range strings.Fields(hard) {
			w := measure(ft, word, pt)
			if cur > 0 && cur+space+w > width {
				n++
				cur = 0
			}
			if cur > 0 {
				cur += space
			}
			cur += w
			// A word wider than the box breaks anywhere.
			for cur > width && width > 0 {
				n++
				cur -= width
			}
		}
		lines += n
	}
	return lines
}

// fontOf decides a run's typeface and size: its own, then the placeholder's list styles, the layout's and the master's.
func (d *pptxDoc) fontOf(s *pslide, sh *pshape, para, run *Node) (string, float64) {
	size, face := 0.0, ""
	take := func(pr *Node) {
		if pr == nil {
			return
		}
		if size == 0 && pr.Attr("sz") != "" {
			if v, err := strconv.ParseFloat(pr.Attr("sz"), 64); err == nil {
				size = v / 100
			}
		}
		if face == "" {
			if l := pr.Kid("latin"); l != nil {
				face = l.Attr("typeface")
			}
		}
	}
	take(run.kidOrNil("rPr"))
	lvl := 1
	if ppr := para.Kid("pPr"); ppr != nil && ppr.Attr("lvl") != "" {
		if v, err := strconv.Atoi(ppr.Attr("lvl")); err == nil {
			lvl = v + 1
		}
	}
	lvlName := fmt.Sprintf("lvl%dpPr", lvl)
	fromList := func(list *Node) {
		if list == nil {
			return
		}
		if l := list.Kid(lvlName); l != nil {
			take(l.Kid("defRPr"))
		}
	}
	fromList(sh.txBody.Kid("lstStyle"))
	master := d.masters[s.masterPart]
	if sh.isPH {
		for _, x := range []*XMLDoc{d.layouts[s.layoutPart], master} {
			if x == nil {
				continue
			}
			if m := matchPlaceholder(x, sh); m != nil {
				fromList(m.Kid("txBody").kidOrNil("lstStyle"))
			}
		}
	}
	isTitle := sh.phType == "title" || sh.phType == "ctrTitle"
	if master != nil {
		styles := master.Root.Kid("txStyles")
		switch {
		case isTitle:
			fromList(styles.kidOrNil("titleStyle"))
		case sh.isPH && (sh.phType == "body" || sh.phType == "subTitle" || sh.phType == "obj"):
			fromList(styles.kidOrNil("bodyStyle"))
		default:
			fromList(styles.kidOrNil("otherStyle"))
		}
	}
	if size == 0 {
		size = defaultFontPt
	}
	theme := d.themes[s.masterPart]
	switch face {
	case "", "+mn-lt":
		face = theme[1]
		if isTitle && face != "" && theme[0] != "" {
			face = theme[0]
		}
	case "+mj-lt":
		face = theme[0]
	}
	return face, size
}

// checkOverflow compares the height of a shape's text with its box.
func (d *pptxDoc) checkOverflow(s *pslide, sh *pshape) []Finding {
	body := sh.txBody
	bp := body.Kid("bodyPr")
	key := "s" + s.id + ":sp" + sh.id
	scale := 1.0
	if bp != nil {
		if bp.Kid("spAutoFit") != nil {
			return nil // the box grows with the text
		}
		if na := bp.Kid("normAutofit"); na != nil {
			if na.Attr("fontScale") == "" {
				return nil // PowerPoint shrinks the text when it next lays it out
			}
			if v, err := strconv.ParseFloat(na.Attr("fontScale"), 64); err == nil {
				scale = v / 100000
			}
		}
	}
	inset := func(a string, def int64) int64 {
		if bp != nil && bp.HasAttr(a) {
			if v, err := strconv.ParseInt(bp.Attr(a), 10, 64); err == nil {
				return v
			}
		}
		return def
	}
	width := sh.geom.cx - inset("lIns", defaultInsetX) - inset("rIns", defaultInsetX)
	height := sh.geom.cy - inset("tIns", defaultInsetY) - inset("bIns", defaultInsetY)
	noWrap := bp != nil && bp.Attr("wrap") == "none"
	if width <= 0 || height <= 0 {
		return nil
	}
	var used float64
	for _, para := range body.KidsNamed("p") {
		var firstRun *Node
		var b strings.Builder
		for _, k := range para.Kids {
			switch k.Local {
			case "r", "fld":
				if firstRun == nil {
					firstRun = k
				}
				if t := k.Kid("t"); t != nil {
					b.WriteString(t.Text)
				}
			case "br":
				b.WriteString("\v")
			}
		}
		face, pt := d.fontOf(s, sh, para, firstRun)
		pt *= scale
		if face == "" {
			return []Finding{{SeverityUnverified, "pptx:fit:" + key, fmt.Sprintf("%s: the text's font could not be determined, so its fit was not checked", sh.addr())}}
		}
		ft := fonts.lookup(face)
		if ft == nil {
			return []Finding{{SeverityUnverified, "pptx:fit-font:" + normFont(face), fmt.Sprintf("the font %q is not on this machine, so text fit in shapes using it was not checked", face)}}
		}
		lines := 1
		if !noWrap {
			lines = wrappedLines(ft, b.String(), pt, width)
		} else if measure(ft, b.String(), pt) > width && sh.geom.x+measure(ft, b.String(), pt) > d.width {
			return []Finding{{SeverityWarning, "pptx:overflow:" + key, fmt.Sprintf("%s %q: an unwrapped line runs past the slide", sh.addr(), sh.name)}}
		}
		used += float64(lines) * pt * lineHeightFactor * emuPerPoint
	}
	if used > float64(height)*overflowTolerance {
		return []Finding{{SeverityWarning, "pptx:overflow:" + key, fmt.Sprintf("%s %q: its text needs about %.1f in of height in a %.1f in box (measured with the real font; PowerPoint's line breaks may differ slightly)", sh.addr(), sh.name, used/914400, float64(height)/914400)}}
	}
	return nil
}
