package documents

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// Text and pictures added to PDF pages (ISO 32000-1 §7.8 content streams, §9 text, §8.9 images). pdfcpu parses and
// writes the file and builds the image XObject; the page gets two new content streams around its own: "q" before it
// and, after it, "Q" and our drawing. So whatever state the page's own content leaves behind cannot shift what we add,
// and the page's content is not rewritten. Coordinates are points from the top-left corner of the page as it is shown,
// so a rotated page takes them the way a reader sees it. Text uses the standard Helvetica with WinAnsiEncoding, which
// covers Latin-1, as created PDFs do; other characters are refused.

const (
	pdfDefaultTextSize = 12.0
	helveticaAscent    = 0.718
	pdfLeading         = 1.2
)

type pdfAddition struct {
	pages []int
	text  []string
	size  float64
	pic   *picture
	x, y  float64
	w, h  float64 // a picture's size; for text, w > 0 wraps
}

// pdfAddContent applies add_text or add_image to the selected pages.
func pdfAddContent(data []byte, ed Edit, total int) ([]byte, string, []string, error) {
	if ed.Pages == "" {
		return nil, "", nil, refuse("%s needs pages, such as \"1\", \"2-4\" or \"1-%d\"", ed.Op, total)
	}
	pages, err := parsePages(ed.Pages, total)
	if err != nil {
		return nil, "", nil, err
	}
	if ed.X == nil || ed.Y == nil {
		return nil, "", nil, refuse("%s needs x and y: points from the top-left corner of the page as shown", ed.Op)
	}
	a := &pdfAddition{pages: pages, x: float64(*ed.X), y: float64(*ed.Y)}
	var notes []string
	switch ed.Op {
	case "add_text":
		if strings.TrimSpace(ed.Text) == "" {
			return nil, "", nil, refuse("add_text needs text")
		}
		for _, r := range ed.Text {
			if _, ok := winAnsiByte(r); !ok && r != '\n' && r != '\t' {
				return nil, "", nil, refuse("the character %q is outside WinAnsi (Latin-1 and a few typographic marks), which the standard PDF fonts used here hold; nothing was changed", string(r))
			}
		}
		a.size = ed.Size
		if a.size == 0 {
			a.size = pdfDefaultTextSize
		}
		if a.size < 1 || a.size > 500 {
			return nil, "", nil, refuse("size must be between 1 and 500 points")
		}
		if ed.CX != nil {
			if *ed.CX <= 0 {
				return nil, "", nil, refuse("cx, the width to wrap the text in, must be positive")
			}
			a.w = float64(*ed.CX)
		}
		for _, hard := range strings.Split(strings.ReplaceAll(ed.Text, "\t", "    "), "\n") {
			if a.w > 0 {
				a.text = append(a.text, wrapHelvetica(hard, a.size, a.w)...)
			} else {
				a.text = append(a.text, hard)
			}
		}
	case "add_image":
		if a.pic, err = loadPicture(ed.ImageData); err != nil {
			return nil, "", nil, err
		}
		if ed.Alt != nil && *ed.Alt != "" {
			notes = append(notes, "PDF pictures added here carry no alternative text: the file has no structure tree to hold it.")
		}
	}
	var out bytes.Buffer
	var placed []string
	err = guard(ed.Op, func() error {
		ctx, err := api.ReadValidateAndOptimize(bytes.NewReader(data), pdfConf())
		if err != nil {
			return err
		}
		x := ctx.XRefTable
		var font, img *types.IndirectRef
		for _, n := range a.pages {
			pageDict, _, inh, err := x.PageDict(n, false)
			if err != nil {
				return err
			}
			if inh == nil || inh.MediaBox == nil {
				return fmt.Errorf("page %d has no media box", n)
			}
			box := inh.MediaBox
			if inh.CropBox != nil {
				box = inh.CropBox
			}
			rot := ((inh.Rotate % 360) + 360) % 360
			bw, bh := box.Width(), box.Height()
			dw, dh := bw, bh
			if rot == 90 || rot == 270 {
				dw, dh = bh, bw
			}
			res, err := pageResources(x, pageDict, inh)
			if err != nil {
				return err
			}
			var draw strings.Builder
			draw.WriteString("q " + displayMatrix(rot, box.LL.X, box.LL.Y, bw, bh) + " cm\n")
			if a.pic != nil {
				if img == nil {
					irs, err := model.CreateImageResources(x, bytes.NewReader(a.pic.data), false, false)
					if err != nil {
						return err
					}
					if len(irs) == 0 || irs[0].Res.IndRef == nil {
						return fmt.Errorf("the picture could not be stored")
					}
					img = irs[0].Res.IndRef
					w, h := float64(a.pic.w)*72/a.pic.dpiX, float64(a.pic.h)*72/a.pic.dpiY
					cx, cy := ed.CX, ed.CY
					switch {
					case cx != nil && cy != nil:
						w, h = float64(*cx), float64(*cy)
					case cx != nil:
						h, w = float64(*cx)*h/w, float64(*cx)
					case cy != nil:
						w, h = float64(*cy)*w/h, float64(*cy)
					default:
						if room := dw - a.x; room > 0 && w > room {
							h, w = h*room/w, room
						}
						if room := dh - a.y; room > 0 && h > room {
							w, h = w*room/h, room
						}
					}
					if w <= 0 || h <= 0 {
						return refuse("the picture's size must be positive")
					}
					a.w, a.h = w, h
				}
				name, err := addResource(x, res, "XObject", "HxIm", *img)
				if err != nil {
					return err
				}
				fmt.Fprintf(&draw, "q %s 0 0 %s %s %s cm /%s Do Q\n", num(a.w), num(a.h), num(a.x), num(dh-a.y-a.h), name)
				placed = append(placed, fmt.Sprintf("page %d at (%s, %s) pt, %s×%s pt", n, num(a.x), num(a.y), num(a.w), num(a.h)))
				if a.x+a.w > dw || a.y+a.h > dh || a.x < 0 || a.y < 0 {
					notes = append(notes, fmt.Sprintf("On page %d the picture reaches past the page's edge (the page is %s×%s pt).", n, num(dw), num(dh)))
				}
			} else {
				if font == nil {
					ref, err := x.IndRefForNewObject(types.Dict{"Type": types.Name("Font"), "Subtype": types.Name("Type1"), "BaseFont": types.Name("Helvetica"), "Encoding": types.Name("WinAnsiEncoding")})
					if err != nil {
						return err
					}
					font = ref
				}
				name, err := addResource(x, res, "Font", "HxF", *font)
				if err != nil {
					return err
				}
				base := dh - a.y - a.size*helveticaAscent
				fmt.Fprintf(&draw, "BT /%s %s Tf 0 g %s TL %s %s Td\n", name, num(a.size), num(a.size*pdfLeading), num(a.x), num(base))
				for i, l := range a.text {
					if i > 0 {
						draw.WriteString("T* ")
					}
					draw.WriteString(winAnsiLiteral(l) + " Tj\n")
				}
				draw.WriteString("ET\n")
				placed = append(placed, fmt.Sprintf("page %d at (%s, %s) pt", n, num(a.x), num(a.y)))
				height := a.size * pdfLeading * float64(len(a.text))
				if a.y+height > dh || a.x > dw {
					notes = append(notes, fmt.Sprintf("On page %d the text runs past the page's edge (the page is %s×%s pt).", n, num(dw), num(dh)))
				}
			}
			draw.WriteString("Q\n")
			if err := wrapContents(x, pageDict, draw.String()); err != nil {
				return err
			}
		}
		return api.WriteContext(ctx, &out)
	})
	if err != nil {
		return nil, "", nil, err
	}
	what := "text"
	if a.pic != nil {
		what = fmt.Sprintf("a picture (%s, %d×%d px)", strings.ToUpper(a.pic.format), a.pic.w, a.pic.h)
	}
	return out.Bytes(), fmt.Sprintf("Added %s on %s.", what, strings.Join(placed, "; ")), notes, nil
}

// displayMatrix maps coordinates on the page as shown (origin bottom-left, after /Rotate) to the page's own space.
func displayMatrix(rot int, llx, lly, w, h float64) string {
	var m [6]float64
	switch rot {
	case 90:
		m = [6]float64{0, 1, -1, 0, llx + w, lly}
	case 180:
		m = [6]float64{-1, 0, 0, -1, llx + w, lly + h}
	case 270:
		m = [6]float64{0, -1, 1, 0, llx, lly + h}
	default:
		m = [6]float64{1, 0, 0, 1, llx, lly}
	}
	parts := make([]string, 6)
	for i, v := range m {
		parts[i] = num(v)
	}
	return strings.Join(parts, " ")
}

func num(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 3, 64)
}

// pageResources returns the page's own resource dictionary, giving it one copied from what it inherits when it has
// none, so additions reach this page alone.
func pageResources(x *model.XRefTable, page types.Dict, inh *model.InheritedPageAttrs) (types.Dict, error) {
	if obj, ok := page.Find("Resources"); ok && obj != nil {
		d, err := x.DereferenceDict(obj)
		if err != nil {
			return nil, err
		}
		if d != nil {
			return d, nil
		}
	}
	d := types.Dict{}
	if inh != nil && inh.Resources != nil {
		d = inh.Resources.Clone().(types.Dict)
	}
	page["Resources"] = d
	return d, nil
}

// addResource adds an entry under a fresh name to a category of the resources (Font, XObject) and returns the name.
func addResource(x *model.XRefTable, res types.Dict, category, prefix string, ref types.IndirectRef) (string, error) {
	sub := types.Dict{}
	if obj, ok := res.Find(category); ok && obj != nil {
		d, err := x.DereferenceDict(obj)
		if err != nil {
			return "", err
		}
		if _, direct := obj.(types.Dict); direct && d != nil {
			sub = d
		} else if d != nil {
			// An indirect category may be shared with other pages; this page gets its own copy.
			sub = d.Clone().(types.Dict)
		}
	}
	res[category] = sub
	for name, v := range sub {
		if r, ok := v.(types.IndirectRef); ok && r.ObjectNumber == ref.ObjectNumber && r.GenerationNumber == ref.GenerationNumber {
			return name, nil
		}
	}
	for i := 1; ; i++ {
		name := prefix + strconv.Itoa(i)
		if _, used := sub[name]; !used {
			sub[name] = ref
			return name, nil
		}
	}
}

// wrapContents puts "q" before the page's content and our drawing, after a "Q", behind it.
func wrapContents(x *model.XRefTable, page types.Dict, drawing string) error {
	obj, ok := page.Find("Contents")
	var existing types.Array
	if ok && obj != nil {
		switch v := obj.(type) {
		case types.IndirectRef:
			d, err := x.Dereference(v)
			if err != nil {
				return err
			}
			if arr, ok := d.(types.Array); ok {
				existing = arr
			} else {
				existing = types.Array{v}
			}
		case types.Array:
			existing = v
		default:
			return fmt.Errorf("the page's content is not a stream")
		}
	}
	if len(existing) == 0 {
		ref, err := x.StreamDictIndRef([]byte(drawing))
		if err != nil {
			return err
		}
		page["Contents"] = *ref
		return nil
	}
	open, err := x.StreamDictIndRef([]byte("q\n"))
	if err != nil {
		return err
	}
	closeAndDraw, err := x.StreamDictIndRef([]byte("\nQ\n" + drawing))
	if err != nil {
		return err
	}
	arr := types.Array{*open}
	arr = append(arr, existing...)
	arr = append(arr, *closeAndDraw)
	page["Contents"] = arr
	return nil
}

// winAnsiExtra are the WinAnsiEncoding codes 0x80-0x9F (ISO 32000-1, Annex D), which Latin-1 leaves to control
// characters.
var winAnsiExtra = map[rune]byte{
	0x20AC: 0x80, 0x201A: 0x82, 0x0192: 0x83, 0x201E: 0x84, 0x2026: 0x85, 0x2020: 0x86, 0x2021: 0x87, 0x02C6: 0x88,
	0x2030: 0x89, 0x0160: 0x8A, 0x2039: 0x8B, 0x0152: 0x8C, 0x017D: 0x8E, 0x2018: 0x91, 0x2019: 0x92, 0x201C: 0x93,
	0x201D: 0x94, 0x2022: 0x95, 0x2013: 0x96, 0x2014: 0x97, 0x02DC: 0x98, 0x2122: 0x99, 0x0161: 0x9A, 0x203A: 0x9B,
	0x0153: 0x9C, 0x017E: 0x9E, 0x0178: 0x9F,
}

var winAnsiRunes = func() map[byte]rune {
	m := map[byte]rune{}
	for r, b := range winAnsiExtra {
		m[b] = r
	}
	return m
}()

func winAnsiByte(r rune) (byte, bool) {
	switch {
	case r >= 0x20 && r <= 0x7E, r >= 0xA0 && r <= 0xFF:
		return byte(r), true
	}
	b, ok := winAnsiExtra[r]
	return b, ok
}

// winAnsiLiteral writes a string literal for a WinAnsiEncoding font.
func winAnsiLiteral(s string) string {
	var b strings.Builder
	b.WriteByte('(')
	for _, r := range s {
		c, ok := winAnsiByte(r)
		switch {
		case !ok:
			b.WriteByte('?')
		case c == '(' || c == ')' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 0x20 || c > 0x7E:
			fmt.Fprintf(&b, "\\%03o", c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte(')')
	return b.String()
}
