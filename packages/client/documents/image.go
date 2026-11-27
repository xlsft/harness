package documents

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/gif" // registers the GIF decoder for DecodeConfig
	_ "image/jpeg"
	_ "image/png"
	"math"
	"strconv"
	"strings"
)

// Pictures that go into documents: PNG, JPEG and GIF, the raster formats Word, PowerPoint and PDF viewers all show. The
// caller (the tool) reads the file from the working folders and hands over the bytes; this package never opens paths.

const (
	// MaxImageBytes bounds a picture file.
	MaxImageBytes = 50 << 20
	// maxImagePixels bounds the decoded size, which a PDF needs in memory.
	maxImagePixels = 60_000_000
	// defaultDPI is what a picture without a stated resolution is taken to have.
	defaultDPI = 96
	emuPerInch = 914400
)

// picture is a checked picture: its format, pixel size and resolution.
type picture struct {
	data        []byte
	format      string // png, jpeg, gif
	ext         string
	contentType string
	w, h        int
	dpiX, dpiY  float64
}

func loadPicture(data []byte) (*picture, error) {
	if len(data) == 0 {
		return nil, refuse("the picture is empty")
	}
	if len(data) > MaxImageBytes {
		return nil, refuse("the picture is %d MB, over the limit of %d MB", len(data)>>20, MaxImageBytes>>20)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, refuse("the picture is not a PNG, JPEG or GIF file that can be read: %v", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, refuse("the picture has no size")
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return nil, refuse("the picture is %d×%d pixels, over the limit of %d megapixels", cfg.Width, cfg.Height, maxImagePixels/1_000_000)
	}
	p := &picture{data: data, format: format, w: cfg.Width, h: cfg.Height, dpiX: defaultDPI, dpiY: defaultDPI}
	switch format {
	case "png":
		p.ext, p.contentType = "png", "image/png"
		if x, y, ok := pngDPI(data); ok {
			p.dpiX, p.dpiY = x, y
		}
	case "jpeg":
		p.ext, p.contentType = "jpeg", "image/jpeg"
		if x, y, ok := jpegDPI(data); ok {
			p.dpiX, p.dpiY = x, y
		}
	case "gif":
		p.ext, p.contentType = "gif", "image/gif"
	default:
		return nil, refuse("pictures must be PNG, JPEG or GIF, not %s", format)
	}
	return p, nil
}

// naturalEMU is the picture's size at its own resolution.
func (p *picture) naturalEMU() (int64, int64) {
	return int64(math.Round(float64(p.w) / p.dpiX * emuPerInch)), int64(math.Round(float64(p.h) / p.dpiY * emuPerInch))
}

// sizeEMU picks the size: both given, one given (the other keeps the aspect ratio), or the natural size shrunk to fit
// within maxCX × maxCY (0: no bound).
func (p *picture) sizeEMU(cx, cy *int64, maxCX, maxCY int64) (int64, int64, error) {
	aspect := float64(p.h) / p.dpiY / (float64(p.w) / p.dpiX)
	switch {
	case cx != nil && cy != nil:
		if *cx <= 0 || *cy <= 0 {
			return 0, 0, refuse("cx and cy must be positive")
		}
		return *cx, *cy, nil
	case cx != nil:
		if *cx <= 0 {
			return 0, 0, refuse("cx must be positive")
		}
		return *cx, int64(math.Round(float64(*cx) * aspect)), nil
	case cy != nil:
		if *cy <= 0 {
			return 0, 0, refuse("cy must be positive")
		}
		return int64(math.Round(float64(*cy) / aspect)), *cy, nil
	}
	w, h := p.naturalEMU()
	return fitWithin(w, h, maxCX, maxCY)
}

// fitWithin shrinks w × h, keeping its shape, until it fits; it never grows it.
func fitWithin(w, h, maxW, maxH int64) (int64, int64, error) {
	scale := 1.0
	if maxW > 0 && w > maxW {
		scale = math.Min(scale, float64(maxW)/float64(w))
	}
	if maxH > 0 && h > maxH {
		scale = math.Min(scale, float64(maxH)/float64(h))
	}
	w, h = int64(float64(w)*scale), int64(float64(h)*scale)
	if w <= 0 || h <= 0 {
		return 0, 0, refuse("the picture would be too small to show")
	}
	return w, h, nil
}

// fitBox places a picture of this shape inside a box, centred, as large as fits.
func (p *picture) fitBox(box rect) rect {
	aspect := float64(p.h) / p.dpiY / (float64(p.w) / p.dpiX)
	w, h := float64(box.cx), float64(box.cx)*aspect
	if h > float64(box.cy) {
		h = float64(box.cy)
		w = h / aspect
	}
	return rect{x: box.x + (box.cx-int64(w))/2, y: box.y + (box.cy-int64(h))/2, cx: int64(w), cy: int64(h), ok: true}
}

// pngDPI reads the pHYs chunk.
func pngDPI(b []byte) (float64, float64, bool) {
	if len(b) < 8 {
		return 0, 0, false
	}
	for at := 8; at+12 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[at:]))
		typ := string(b[at+4 : at+8])
		if n < 0 || at+12+n > len(b) {
			return 0, 0, false
		}
		if typ == "pHYs" && n == 9 {
			x := binary.BigEndian.Uint32(b[at+8:])
			y := binary.BigEndian.Uint32(b[at+12:])
			if b[at+16] == 1 && x > 0 && y > 0 { // per metre
				return float64(x) * 0.0254, float64(y) * 0.0254, sane(float64(x)*0.0254, float64(y)*0.0254)
			}
			return 0, 0, false
		}
		if typ == "IDAT" || typ == "IEND" {
			return 0, 0, false
		}
		at += 12 + n
	}
	return 0, 0, false
}

// jpegDPI reads the JFIF APP0 density.
func jpegDPI(b []byte) (float64, float64, bool) {
	if len(b) < 18 || b[0] != 0xFF || b[1] != 0xD8 || b[2] != 0xFF || b[3] != 0xE0 || string(b[6:11]) != "JFIF\x00" {
		return 0, 0, false
	}
	unit := b[13]
	x, y := float64(binary.BigEndian.Uint16(b[14:])), float64(binary.BigEndian.Uint16(b[16:]))
	switch unit {
	case 1:
		return x, y, sane(x, y)
	case 2:
		return x * 2.54, y * 2.54, sane(x*2.54, y*2.54)
	}
	return 0, 0, false
}

// sane rejects resolutions that would make a picture absurdly large or small.
func sane(x, y float64) bool { return x >= 10 && y >= 10 && x <= 10000 && y <= 10000 }

// Image parts.

const (
	imageRelType       = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/image"
	imageRelTypeStrict = "http://purl.oclc.org/ooxml/officeDocument/relationships/image"
	relNSTransitional  = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	relNSStrict        = "http://purl.oclc.org/ooxml/officeDocument/relationships"
)

// addMediaPart stores a picture as a new part in dir ("word/media"), declares its type and relates it to from. It
// returns the relationship id and the part name. Parts are never shared with an existing picture, so replacing one
// never changes another.
func (c *Changes) addMediaPart(p *Package, from, dir string, pic *picture, strict bool) (string, string, error) {
	name := ""
	for i := 1; ; i++ {
		name = fmt.Sprintf("%s/image%d.%s", dir, i, pic.ext)
		if (!p.Has(name) || c.Remove[name]) && c.Set[name] == nil {
			break
		}
	}
	c.Set[name] = pic.data
	if err := c.ensureDefaultType(p, pic.ext, pic.contentType); err != nil {
		return "", "", err
	}
	typ := imageRelType
	if strict {
		typ = imageRelTypeStrict
	}
	id, err := c.addRelationship(p, from, typ, relTargetFrom(from, name), false)
	if err != nil {
		return "", "", err
	}
	return id, name, nil
}

// ensureDefaultType declares a content type for an extension, unless one is there.
func (c *Changes) ensureDefaultType(p *Package, ext, contentType string) error {
	ct, err := c.Current(p, contentTypesPart)
	if err != nil {
		return err
	}
	d, err := ParseXML(ct)
	if err != nil {
		return err
	}
	for _, n := range d.Root.Kids {
		if n.Local == "Default" && strings.EqualFold(n.Attr("Extension"), ext) {
			if n.Attr("ContentType") != contentType {
				return refuse("the package declares .%s files as %s, not %s", ext, n.Attr("ContentType"), contentType)
			}
			return nil
		}
	}
	s := fmt.Sprintf(`<Default Extension="%s" ContentType="%s"/>`, escAttr(ext), escAttr(contentType))
	// Defaults come before Overrides by convention; the schema allows any order.
	at := d.Root.InnerEnd
	if d.Root.SelfClosed {
		c.Set[contentTypesPart] = InsertBeforeClose(ct, d.Root, s)
		return nil
	}
	for _, n := range d.Root.Kids {
		if n.Local == "Override" {
			at = n.Start
			break
		}
	}
	c.Set[contentTypesPart] = Splice(ct, []Span{{at, at, s}})
	return nil
}

// dropUnusedRel removes a relationship of part when nothing in the part's new XML refers to it any more, and the
// target part when no relationship anywhere in the package leads to it.
func (c *Changes) dropUnusedRel(p *Package, part string, newXML []byte, id string) error {
	if bytes.Contains(newXML, []byte(`"`+id+`"`)) {
		return nil
	}
	rels, err := c.currentRels(p, part)
	if err != nil {
		return err
	}
	r, ok := relByID(rels, id)
	if !ok {
		return nil
	}
	if err := c.removeRelationship(p, part, id); err != nil {
		return err
	}
	if r.External {
		return nil
	}
	target := ResolveTarget(part, r.Target)
	for _, n := range p.Names() {
		if !strings.HasSuffix(n, ".rels") || c.Remove[n] {
			continue
		}
		src := relsSource(n)
		others, err := c.currentRels(p, src)
		if err != nil {
			return err
		}
		for _, o := range others {
			if !o.External && ResolveTarget(src, o.Target) == target {
				return nil
			}
		}
	}
	for n, b := range c.Set { // a .rels part this batch created
		if strings.HasSuffix(n, ".rels") && !p.Has(n) {
			if x, err := ParseXML(b); err == nil {
				for _, o := range relsOf(x) {
					if !o.External && ResolveTarget(relsSource(n), o.Target) == target {
						return nil
					}
				}
			}
		}
	}
	if p.Has(target) {
		c.Remove[target] = true
		if ct, err := c.Current(p, contentTypesPart); err == nil && bytes.Contains(ct, []byte(`"/`+target+`"`)) {
			return c.removeContentType(p, target)
		}
	}
	return nil
}

func (c *Changes) currentRels(p *Package, part string) ([]Relationship, error) {
	name := RelsPart(part)
	if c.Remove[name] {
		return nil, nil
	}
	if b, ok := c.Set[name]; ok {
		x, err := ParseXML(b)
		if err != nil {
			return nil, err
		}
		return relsOf(x), nil
	}
	return p.Rels(part)
}

// mediaDir is the folder pictures of a part go into: word/media for word/document.xml.
func mediaDir(part string) string {
	if i := strings.Index(part, "/"); i > 0 {
		return part[:i] + "/media"
	}
	return "media"
}

// emuText formats a length for a report.
func emuText(cx, cy int64) string {
	return strconv.FormatFloat(float64(cx)/emuPerInch, 'f', 2, 64) + "×" + strconv.FormatFloat(float64(cy)/emuPerInch, 'f', 2, 64) + " in"
}

func isRelNS(ns string) bool {
	return ns == relNSTransitional || ns == relNSStrict
}

// relAttrName finds an attribute in the relationships namespace, such as r:embed, and returns its name as written.
func relAttrName(n *Node, local string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == local && a.Name.Space != "" && a.Name.Space != "xmlns" && isRelNS(n.resolve(a.Name.Space)) {
			return a.Name.Space + ":" + a.Name.Local
		}
	}
	return ""
}

// relAttr returns the value of a relationships-namespace attribute such as r:embed.
func relAttr(n *Node, local string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == local && a.Name.Space != "" && a.Name.Space != "xmlns" && isRelNS(n.resolve(a.Name.Space)) {
			return a.Value
		}
	}
	return ""
}

// relIDsIn lists the relationship ids an element and everything in it refer to.
func relIDsIn(n *Node) []string {
	var out []string
	n.Walk(func(c *Node) bool {
		for _, a := range c.Attrs {
			if a.Name.Space != "" && a.Name.Space != "xmlns" && isRelNS(c.resolve(a.Name.Space)) && a.Value != "" {
				out = append(out, a.Value)
			}
		}
		return true
	})
	return out
}

// newBlip rebuilds an a:blip for a new picture: the same element and effects, the new relationship, no extension list
// (which may hold a second, SVG copy of the old picture) and no link to an external file.
func newBlip(data []byte, blip *Node, relID string, strict bool) string {
	tag := blip.Tag(blip.Local)
	var b strings.Builder
	b.WriteString("<" + tag)
	for _, a := range blip.Attrs {
		if a.Name.Space != "" && a.Name.Space != "xmlns" && isRelNS(blip.resolve(a.Name.Space)) {
			continue // embed and link: written below
		}
		name := a.Name.Local
		if a.Name.Space != "" {
			name = a.Name.Space + ":" + name
		}
		fmt.Fprintf(&b, ` %s="%s"`, name, escAttr(a.Value))
	}
	ns := relNSTransitional
	if strict {
		ns = relNSStrict
	}
	fmt.Fprintf(&b, ` xmlns:hxr="%s" hxr:embed="%s">`, ns, escAttr(relID))
	for _, k := range blip.Kids {
		if k.Local != "extLst" {
			b.Write(k.Raw(data))
		}
	}
	b.WriteString("</" + tag + ">")
	return b.String()
}

// relDecl gives the prefix for r:embed in new XML, and the declaration to put on the element when none is in scope.
func relDecl(strict bool) (string, string) {
	ns := relNSTransitional
	if strict {
		ns = relNSStrict
	}
	return "r", ` xmlns:r="` + ns + `"`
}
