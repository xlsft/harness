package documents

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A presentation is addressed by the sldId of a slide, which survives reordering, and the cNvPr id of a shape on it.

const (
	drawingNS       = "http://schemas.openxmlformats.org/drawingml/2006/main"
	drawingNSStrict = "http://purl.oclc.org/ooxml/drawingml/main"
	emuPerPoint     = 12700
)

type rect struct {
	x, y, cx, cy int64
	ok           bool
}

type pshape struct {
	slide      *pslide
	id, name   string
	node       *Node
	kind       string
	phType     string
	phIdx      string
	isPH       bool
	hidden     bool
	geom       rect
	inherited  bool
	inGroup    bool
	txBody     *Node
	paragraphs []string
	fp         string
}

func (s *pshape) addr() string { return "s" + s.slide.id + "/sp" + s.id }

func (s *pshape) text() string { return strings.Join(s.paragraphs, "\n") }

type pslide struct {
	id, rid, part string
	index         int
	x             *XMLDoc
	rels          []Relationship
	hidden        bool
	layoutPart    string
	layoutName    string
	masterPart    string
	notesPart     string
	notes         string
	comments      []string
	shapes        []*pshape
	title         string
}

type pptxDoc struct {
	pkg       *Package
	part      string
	pres      *XMLDoc
	slides    []*pslide
	byID      map[string]*pslide
	width     int64
	height    int64
	layouts   map[string]*XMLDoc
	masters   map[string]*XMLDoc
	themes    map[string][2]string // master part -> major, minor typefaces
	allLayout []playout
}

type playout struct {
	part, name, master string
}

func openPPTX(data []byte) (Document, error) {
	pkg, err := OpenPackage(data)
	if err != nil {
		return nil, err
	}
	part, err := pkg.officeDocumentPart()
	if err != nil {
		return nil, err
	}
	x, err := pkg.XML(part)
	if err != nil {
		return nil, err
	}
	d := &pptxDoc{pkg: pkg, part: part, pres: x, byID: map[string]*pslide{}, layouts: map[string]*XMLDoc{}, masters: map[string]*XMLDoc{}, themes: map[string][2]string{}}
	if sz := x.Root.Kid("sldSz"); sz != nil {
		d.width, _ = strconv.ParseInt(sz.Attr("cx"), 10, 64)
		d.height, _ = strconv.ParseInt(sz.Attr("cy"), 10, 64)
	}
	rels, err := pkg.Rels(part)
	if err != nil {
		return nil, err
	}
	for _, r := range rels {
		if !r.External && strings.HasSuffix(r.Type, "/slideMaster") {
			d.loadMaster(ResolveTarget(part, r.Target))
		}
	}
	for i, n := range x.Root.Kid("sldIdLst").KidsNamed("sldId") {
		s := &pslide{id: n.Attr("id"), rid: n.Attr("id"), index: i + 1}
		for _, a := range n.Attrs {
			if a.Name.Local == "id" && a.Name.Space != "" {
				s.rid = a.Value
			} else if a.Name.Local == "id" {
				s.id = a.Value
			}
		}
		r, ok := relByID(rels, s.rid)
		if !ok {
			return nil, fmt.Errorf("slide %s has no relationship %s", s.id, s.rid)
		}
		s.part = ResolveTarget(part, r.Target)
		if err := d.loadSlide(s); err != nil {
			return nil, err
		}
		d.slides = append(d.slides, s)
		d.byID[s.id] = s
	}
	return d, nil
}

func (d *pptxDoc) loadMaster(part string) {
	x, err := d.pkg.XML(part)
	if err != nil {
		return
	}
	d.masters[part] = x
	rels, _ := d.pkg.Rels(part)
	for _, r := range rels {
		t := ResolveTarget(part, r.Target)
		switch {
		case strings.HasSuffix(r.Type, "/slideLayout"):
			if lx, err := d.pkg.XML(t); err == nil {
				d.layouts[t] = lx
				d.allLayout = append(d.allLayout, playout{t, lx.Root.Kid("cSld").attrOrEmpty("name"), part})
			}
		case strings.HasSuffix(r.Type, "/theme"):
			if tx, err := d.pkg.XML(t); err == nil {
				var v [2]string
				if f := tx.Root.Find("majorFont"); len(f) > 0 {
					v[0] = f[0].Kid("latin").attrOrEmpty("typeface")
				}
				if f := tx.Root.Find("minorFont"); len(f) > 0 {
					v[1] = f[0].Kid("latin").attrOrEmpty("typeface")
				}
				d.themes[part] = v
			}
		}
	}
}

func (d *pptxDoc) loadSlide(s *pslide) error {
	x, err := d.pkg.XML(s.part)
	if err != nil {
		return err
	}
	s.x = x
	s.hidden = x.Root.Attr("show") == "0" || x.Root.Attr("show") == "false"
	if s.rels, err = d.pkg.Rels(s.part); err != nil {
		return err
	}
	for _, r := range s.rels {
		if r.External {
			continue
		}
		t := ResolveTarget(s.part, r.Target)
		switch {
		case strings.HasSuffix(r.Type, "/slideLayout"):
			s.layoutPart = t
			if l := d.layouts[t]; l != nil {
				s.layoutName = l.Root.Kid("cSld").attrOrEmpty("name")
				for _, pl := range d.allLayout {
					if pl.part == t {
						s.masterPart = pl.master
					}
				}
			}
		case strings.HasSuffix(r.Type, "/notesSlide"):
			s.notesPart = t
			if nx, err := d.pkg.XML(t); err == nil {
				var parts []string
				for _, sp := range nx.Root.Find("sp") {
					ph := sp.Find("ph")
					if len(ph) > 0 && ph[0].Attr("type") == "body" {
						parts = append(parts, strings.Join(textOfParagraphs(sp), "\n"))
					}
				}
				s.notes = strings.TrimSpace(strings.Join(parts, "\n"))
			}
		case strings.HasSuffix(r.Type, "/comments"):
			if cx, err := d.pkg.XML(t); err == nil {
				for _, c := range cx.Root.KidsNamed("cm") {
					if tx := c.Kid("text"); tx != nil {
						s.comments = append(s.comments, tx.Text)
					}
				}
			}
		}
	}
	tree := x.Root.Kid("cSld").kidOrNil("spTree")
	if tree == nil {
		return fmt.Errorf("slide %s has no shape tree", s.id)
	}
	d.scanShapes(s, tree, false, identityTransform)
	for _, sh := range s.shapes {
		if sh.isPH && (sh.phType == "title" || sh.phType == "ctrTitle") && s.title == "" {
			s.title = sh.text()
		}
	}
	return nil
}

// groupTransform maps a group's child coordinates to the slide's.
type groupTransform func(rect) rect

func identityTransform(r rect) rect { return r }

func (d *pptxDoc) scanShapes(s *pslide, tree *Node, inGroup bool, tf groupTransform) {
	for _, k := range tree.Kids {
		switch k.Local {
		case "sp", "pic", "cxnSp", "graphicFrame", "grpSp":
		case "AlternateContent":
			// The Choice part holds the real shape.
			if ch := k.Kid("Choice"); ch != nil {
				d.scanShapes(s, ch, inGroup, tf)
			}
			continue
		default:
			continue
		}
		sh := &pshape{slide: s, node: k, kind: k.Local, inGroup: inGroup, fp: Fingerprint(k.Raw(s.x.Data))}
		// nvSpPr, nvPicPr, nvCxnSpPr, nvGraphicFramePr, nvGrpSpPr: first child holds the ids.
		if len(k.Kids) > 0 {
			nv := k.Kids[0]
			if c := nv.Kid("cNvPr"); c != nil {
				sh.id, sh.name = c.Attr("id"), c.Attr("name")
				sh.hidden = c.Attr("hidden") == "1" || c.Attr("hidden") == "true"
			}
			if ph := nv.Kid("nvPr").kidOrNil("ph"); ph != nil {
				sh.isPH = true
				sh.phType, sh.phIdx = ph.Attr("type"), ph.Attr("idx")
				if sh.phType == "" {
					sh.phType = "body"
				}
			}
		}
		sh.geom = readXfrm(k)
		if !sh.geom.ok && sh.isPH {
			sh.geom = d.inheritedGeom(s, sh)
			sh.inherited = sh.geom.ok
		}
		if sh.geom.ok {
			sh.geom = tf(sh.geom)
		}
		if b := k.Kid("txBody"); b != nil {
			sh.txBody = b
			sh.paragraphs = textOfParagraphs(k)
		}
		s.shapes = append(s.shapes, sh)
		if k.Local == "grpSp" {
			inner := tf
			if x := k.Kid("grpSpPr").kidOrNil("xfrm"); x != nil {
				inner = composeGroup(x, tf)
			}
			d.scanShapes(s, k, true, inner)
		}
	}
}

func composeGroup(x *Node, outer groupTransform) groupTransform {
	num := func(n *Node, a string) int64 {
		v, _ := strconv.ParseInt(n.attrOrEmpty(a), 10, 64)
		return v
	}
	off, ext, chOff, chExt := x.Kid("off"), x.Kid("ext"), x.Kid("chOff"), x.Kid("chExt")
	sx, sy := 1.0, 1.0
	if c := num(chExt, "cx"); c != 0 {
		sx = float64(num(ext, "cx")) / float64(c)
	}
	if c := num(chExt, "cy"); c != 0 {
		sy = float64(num(ext, "cy")) / float64(c)
	}
	return func(r rect) rect {
		return outer(rect{
			x:  num(off, "x") + int64(float64(r.x-num(chOff, "x"))*sx),
			y:  num(off, "y") + int64(float64(r.y-num(chOff, "y"))*sy),
			cx: int64(float64(r.cx) * sx), cy: int64(float64(r.cy) * sy), ok: r.ok,
		})
	}
}

func readXfrm(n *Node) rect {
	var x *Node
	if pr := n.Kid("spPr"); pr != nil {
		x = pr.Kid("xfrm")
	} else if pr := n.Kid("grpSpPr"); pr != nil {
		x = pr.Kid("xfrm")
	} else if n.Kid("xfrm") != nil {
		x = n.Kid("xfrm")
	}
	if x == nil || x.Kid("off") == nil || x.Kid("ext") == nil {
		return rect{}
	}
	var r rect
	r.x, _ = strconv.ParseInt(x.Kid("off").Attr("x"), 10, 64)
	r.y, _ = strconv.ParseInt(x.Kid("off").Attr("y"), 10, 64)
	r.cx, _ = strconv.ParseInt(x.Kid("ext").Attr("cx"), 10, 64)
	r.cy, _ = strconv.ParseInt(x.Kid("ext").Attr("cy"), 10, 64)
	r.ok = true
	return r
}

// inheritedGeom takes a placeholder's position from its layout, then its master.
func (d *pptxDoc) inheritedGeom(s *pslide, sh *pshape) rect {
	for _, x := range []*XMLDoc{d.layouts[s.layoutPart], d.masters[s.masterPart]} {
		if x == nil {
			continue
		}
		if m := matchPlaceholder(x, sh); m != nil {
			if r := readXfrm(m); r.ok {
				return r
			}
		}
	}
	return rect{}
}

// matchPlaceholder finds the shape of a layout or master that a slide's placeholder inherits from: by index, then by type.
func matchPlaceholder(x *XMLDoc, sh *pshape) *Node {
	tree := x.Root.Kid("cSld").kidOrNil("spTree")
	if tree == nil {
		return nil
	}
	var byType *Node
	for _, k := range tree.KidsNamed("sp") {
		if len(k.Kids) == 0 {
			continue
		}
		ph := k.Kids[0].Kid("nvPr").kidOrNil("ph")
		if ph == nil {
			continue
		}
		if sh.phIdx != "" && ph.Attr("idx") == sh.phIdx {
			return k
		}
		t := ph.Attr("type")
		if t == "" {
			t = "body"
		}
		same := t == sh.phType || (t == "title" && sh.phType == "ctrTitle") || (t == "ctrTitle" && sh.phType == "title") || (t == "body" && sh.phType == "subTitle")
		if same && byType == nil {
			byType = k
		}
	}
	return byType
}

// textOfParagraphs returns the text of a shape's paragraphs, in a shape's own txBody only.
func textOfParagraphs(n *Node) []string {
	body := n.Kid("txBody")
	if body == nil {
		return nil
	}
	var out []string
	for _, p := range body.KidsNamed("p") {
		var b strings.Builder
		for _, k := range p.Kids {
			switch k.Local {
			case "r", "fld":
				if t := k.Kid("t"); t != nil {
					b.WriteString(t.Text)
				}
			case "br":
				b.WriteString("\v")
			}
		}
		out = append(out, b.String())
	}
	return out
}

func (d *pptxDoc) Format() Format { return FormatPPTX }

func inches(emu int64) string { return strconv.FormatFloat(float64(emu)/914400, 'f', 2, 64) }

func (r rect) String() string {
	if !r.ok {
		return "no position"
	}
	return fmt.Sprintf("x=%d y=%d cx=%d cy=%d", r.x, r.y, r.cx, r.cy)
}

// Structure implements Document.
func (d *pptxDoc) Structure() string {
	var b strings.Builder
	fmt.Fprintf(&b, "PowerPoint presentation: %d slides, %d×%d EMU (%s×%s in). Positions are in EMU (914400 per inch).\n", len(d.slides), d.width, d.height, inches(d.width), inches(d.height))
	var names []string
	for _, l := range d.allLayout {
		names = append(names, l.name)
	}
	if len(names) > 0 {
		b.WriteString("Layouts: " + strings.Join(names, ", ") + "\n")
	}
	for _, s := range d.slides {
		extra := ""
		if s.hidden {
			extra = " [HIDDEN]"
		}
		fmt.Fprintf(&b, "%d. s%s layout %q%s, %d shapes", s.index, s.id, s.layoutName, extra, len(s.shapes))
		if s.title != "" {
			fmt.Fprintf(&b, ", title %q", oneLine(s.title, 80))
		}
		if s.notes != "" {
			fmt.Fprintf(&b, ", notes %q", oneLine(s.notes, 80))
		}
		if len(s.comments) > 0 {
			fmt.Fprintf(&b, ", %d comments", len(s.comments))
		}
		b.WriteString("\n")
		for _, sh := range s.shapes {
			if sh.hidden {
				fmt.Fprintf(&b, "   hidden shape %s %q\n", sh.addr(), sh.name)
			}
		}
	}
	return clip(b.String())
}

func (sh *pshape) line() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s #%s %s %q", sh.addr(), sh.fp, sh.kind, sh.name)
	if sh.isPH {
		fmt.Fprintf(&b, " placeholder=%s", sh.phType)
		if sh.phIdx != "" {
			fmt.Fprintf(&b, "/%s", sh.phIdx)
		}
	}
	if sh.hidden {
		b.WriteString(" [hidden]")
	}
	if sh.inGroup {
		b.WriteString(" [in group]")
	}
	b.WriteString(" " + sh.geom.String())
	if sh.inherited {
		b.WriteString(" (from layout)")
	}
	if sh.txBody != nil {
		b.WriteString(": " + strings.NewReplacer("\n", " ¶ ", "\v", "⏎").Replace(sh.text()))
	}
	return b.String()
}

var pptxSlideRe = regexp.MustCompile(`^s(\d+)$`)

// Read implements Document. The range is empty, "s256" or a slide position such as "3" or "2-4".
func (d *pptxDoc) Read(sel Selector) (string, error) {
	rng := strings.TrimSpace(sel.Range)
	var slides []*pslide
	switch {
	case rng == "":
		slides = d.slides
	case pptxSlideRe.MatchString(rng):
		s := d.byID[strings.TrimPrefix(rng, "s")]
		if s == nil {
			return "", fmt.Errorf("no slide %s; the structure lists them", rng)
		}
		slides = []*pslide{s}
	default:
		from, to, isRange := strings.Cut(rng, "-")
		a, err := strconv.Atoi(from)
		if err != nil {
			return "", fmt.Errorf("range %q: use s<id>, a slide number or a range of them", rng)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(to); err != nil {
				return "", fmt.Errorf("range %q: use s<id>, a slide number or a range of them", rng)
			}
		}
		for _, s := range d.slides {
			if s.index >= a && s.index <= b {
				slides = append(slides, s)
			}
		}
		if len(slides) == 0 {
			return "", fmt.Errorf("the presentation has %d slides", len(d.slides))
		}
	}
	var lines []string
	for _, s := range slides {
		lines = append(lines, fmt.Sprintf("Slide %d (s%s, layout %q)", s.index, s.id, s.layoutName))
		for _, sh := range s.shapes {
			lines = append(lines, sh.line())
		}
		if s.notes != "" {
			lines = append(lines, fmt.Sprintf("s%s notes (not shown on the slide): %s", s.id, s.notes))
		}
		for _, c := range s.comments {
			lines = append(lines, fmt.Sprintf("s%s comment: %s", s.id, c))
		}
	}
	return pageLines("", lines, sel), nil
}

// Search implements Document, over shape text, notes and comments.
func (d *pptxDoc) Search(query string, limit int) string {
	if limit <= 0 {
		limit = DefaultLimit
	}
	q := strings.ToLower(query)
	var hits []string
	for _, s := range d.slides {
		for _, sh := range s.shapes {
			if containsFold(sh.text(), q) || containsFold(sh.name, q) {
				hits = append(hits, sh.line())
			}
		}
		if containsFold(s.notes, q) {
			hits = append(hits, fmt.Sprintf("s%s notes: %s", s.id, snippetAround(s.notes, q)))
		}
		for _, c := range s.comments {
			if containsFold(c, q) {
				hits = append(hits, fmt.Sprintf("s%s comment: %s", s.id, snippetAround(c, q)))
			}
		}
	}
	return searchReport(query, hits, limit)
}

// Project implements Document.
func (d *pptxDoc) Project() []string {
	var order []string
	for _, s := range d.slides {
		order = append(order, "s"+s.id)
	}
	lines := []string{"[slide order] " + strings.Join(order, ", ")}
	for _, s := range d.slides {
		lines = append(lines, fmt.Sprintf("[slide s%s] layout %q", s.id, s.layoutName))
		for _, sh := range s.shapes {
			base := fmt.Sprintf("s%s %q #%s @%s", s.id, sh.name, sh.id, sh.geom)
			if sh.txBody == nil || len(sh.paragraphs) == 0 {
				lines = append(lines, base)
				continue
			}
			for _, p := range sh.paragraphs {
				lines = append(lines, base+": "+strings.ReplaceAll(p, "\v", "⏎"))
			}
		}
		if s.notes != "" {
			lines = append(lines, fmt.Sprintf("s%s notes: %s", s.id, oneLine(s.notes, 4000)))
		}
	}
	return lines
}

var leftoverTextRe = regexp.MustCompile(`(?i)^\s*(click to (add|edit)|lorem ipsum|xxx+|\[?(title|subtitle|your text|text|content|name|date|company)( here)?\]?|add (a )?(title|text))\b`)

// Check implements Document: the package, the slide list, and a look at each slide's layout.
func (d *pptxDoc) Check() []Finding {
	out := d.pkg.checkPackage()
	bad := func(key, format string, args ...any) {
		out = append(out, Finding{SeverityError, "pptx:" + key, fmt.Sprintf(format, args...)})
	}
	warn := func(key, format string, args ...any) {
		out = append(out, Finding{SeverityWarning, "pptx:" + key, fmt.Sprintf(format, args...)})
	}
	if d.width <= 0 || d.height <= 0 {
		bad("size", "the presentation has no slide size")
	}
	seen := map[string]bool{}
	for _, s := range d.slides {
		if seen[s.id] {
			bad("dup-id:"+s.id, "the slide id %s is used twice", s.id)
		}
		seen[s.id] = true
		if v, err := strconv.ParseInt(s.id, 10, 64); err != nil || v < 256 || v >= 2147483648 {
			bad("id-range:"+s.id, "the slide id %s is outside 256 to 2147483647", s.id)
		}
		if s.layoutPart == "" {
			bad("no-layout:"+s.id, "slide s%s has no layout", s.id)
		}
		ids := map[string]bool{}
		for _, sh := range s.shapes {
			if sh.id != "" && ids[sh.id] {
				warn("dup-shape:"+s.id+":"+sh.id, "slide s%s uses shape id %s twice", s.id, sh.id)
			}
			ids[sh.id] = true
		}
		out = append(out, checkRelRefs(d.pkg, s.part, s.x)...)
		out = append(out, d.checkLayout(s)...)
	}
	return out
}

func (d *pptxDoc) checkLayout(s *pslide) []Finding {
	var out []Finding
	warn := func(key, format string, args ...any) {
		out = append(out, Finding{SeverityWarning, "pptx:" + key, fmt.Sprintf(format, args...)})
	}
	type box struct {
		sh *pshape
		r  rect
	}
	var boxes []box
	for _, sh := range s.shapes {
		if sh.hidden || sh.kind == "grpSp" && len(sh.node.Kids) == 0 {
			continue
		}
		key := "s" + s.id + ":sp" + sh.id
		g := sh.geom
		if g.ok && sh.kind != "cxnSp" && sh.kind != "grpSp" {
			if d.width > 0 && (g.x+g.cx <= 0 || g.y+g.cy <= 0 || g.x >= d.width || g.y >= d.height) {
				warn("off-slide:"+key, "%s %q is entirely off the slide (%s)", sh.addr(), sh.name, g)
			} else if d.width > 0 && (g.x < 0 || g.y < 0 || g.x+g.cx > d.width || g.y+g.cy > d.height) {
				warn("edge:"+key, "%s %q runs past the slide's edge (%s)", sh.addr(), sh.name, g)
			}
		}
		if sh.isPH && sh.txBody != nil && sh.phType != "dt" && sh.phType != "ftr" && sh.phType != "sldNum" {
			if strings.TrimSpace(sh.text()) == "" {
				warn("empty-ph:"+key, "%s %q is an empty placeholder: fill it or delete it", sh.addr(), sh.name)
			}
		}
		if t := sh.text(); leftoverTextRe.MatchString(t) {
			warn("leftover:"+key, "%s %q still holds template text: %q", sh.addr(), sh.name, oneLine(t, 60))
		}
		if sh.txBody != nil && g.ok && strings.TrimSpace(sh.text()) != "" {
			out = append(out, d.checkOverflow(s, sh)...)
		}
		if g.ok && sh.kind != "grpSp" && sh.kind != "cxnSp" && !sh.inGroup {
			boxes = append(boxes, box{sh, g})
		}
	}
	// A shape over most of the slide is a background.
	slideArea := float64(d.width) * float64(d.height)
	for i, a := range boxes {
		for _, b := range boxes[i+1:] {
			if slideArea > 0 && (float64(a.r.cx)*float64(a.r.cy) > 0.8*slideArea || float64(b.r.cx)*float64(b.r.cy) > 0.8*slideArea) {
				continue
			}
			if !hasContent(a.sh) || !hasContent(b.sh) {
				continue
			}
			ix := min(a.r.x+a.r.cx, b.r.x+b.r.cx) - max(a.r.x, b.r.x)
			iy := min(a.r.y+a.r.cy, b.r.y+b.r.cy) - max(a.r.y, b.r.y)
			if ix <= 0 || iy <= 0 {
				continue
			}
			area := float64(ix) * float64(iy)
			smaller := min(float64(a.r.cx)*float64(a.r.cy), float64(b.r.cx)*float64(b.r.cy))
			if smaller > 0 && area/smaller > 0.1 {
				warn("overlap:s"+s.id+":"+a.sh.id+":"+b.sh.id, "%s %q and %s %q overlap by %.0f%% of the smaller", a.sh.addr(), a.sh.name, b.sh.addr(), b.sh.name, 100*area/smaller)
			}
		}
	}
	return out
}

// hasContent: a filled rectangle with no text is taken as decoration, not as something that collides.
func hasContent(sh *pshape) bool {
	if sh.kind == "pic" || sh.kind == "graphicFrame" {
		return true
	}
	return strings.TrimSpace(sh.text()) != ""
}
