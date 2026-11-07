package documents

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const pptxEditOps = "set_text, replace_text, set_geometry, delete_shape, add_textbox, add_slide, delete_slide, move_slide"

type pptxEdit struct {
	d       *pptxDoc
	res     *EditResult
	c       *Changes
	spans   map[*pslide][]Span
	deleted map[*pslide]bool
	edited  map[*pslide]bool
	nextNum int
}

// Apply implements Document. Shape edits go to the slides as they were; slide operations run in order on the slide list.
func (d *pptxDoc) Apply(edits []Edit) (*EditResult, error) {
	e := &pptxEdit{d: d, res: &EditResult{}, c: NewChanges(), spans: map[*pslide][]Span{}, deleted: map[*pslide]bool{}, edited: map[*pslide]bool{}}
	for _, n := range d.pkg.Names() {
		if m := slideNumRe.FindStringSubmatch(n); m != nil {
			if v, _ := strconv.Atoi(m[1]); v > e.nextNum {
				e.nextNum = v
			}
		}
	}
	for i, ed := range edits {
		var err error
		switch ed.Op {
		case "set_text":
			err = e.setText(ed)
		case "replace_text":
			err = e.replaceText(ed)
		case "set_geometry":
			err = e.setGeometry(ed)
		case "delete_shape":
			err = e.deleteShape(ed)
		case "add_textbox":
			err = e.addTextbox(ed)
		case "add_slide":
			err = e.addSlide(ed)
		case "delete_slide":
			err = e.deleteSlide(ed)
		case "move_slide":
			err = e.moveSlide(ed)
		default:
			err = refuse("unknown operation %q for a presentation; use one of: %s", ed.Op, pptxEditOps)
		}
		if err != nil {
			return nil, fmt.Errorf("edit %d (%s): %w", i+1, ed.Op, err)
		}
	}
	return e.finish()
}

var slideNumRe = regexp.MustCompile(`slides/slide(\d+)\.xml$`)
var shapeAddrRe = regexp.MustCompile(`^s(\d+)/sp(\d+)$`)

func (e *pptxEdit) shape(ed Edit) (*pshape, error) {
	m := shapeAddrRe.FindStringSubmatch(ed.Target)
	if m == nil {
		return nil, refuse("target %q is not a shape address such as s256/sp4", ed.Target)
	}
	s := e.d.byID[m[1]]
	if s == nil {
		return nil, refuse("no slide s%s", m[1])
	}
	if e.deleted[s] {
		return nil, refuse("slide s%s is being deleted in this batch", m[1])
	}
	var sh *pshape
	for _, x := range s.shapes {
		if x.id == m[2] {
			if sh != nil {
				r := &RefusedError{Message: fmt.Sprintf("slide s%s uses the shape id %s more than once, so the address is ambiguous. Candidates:", m[1], m[2])}
				for _, y := range s.shapes {
					if y.id == m[2] {
						r.Current = append(r.Current, y.line())
					}
				}
				return nil, r
			}
			sh = x
		}
	}
	if ed.Fingerprint == "" {
		if sh == nil {
			return nil, refuse("no shape %s on slide s%s", m[2], m[1])
		}
		return nil, &RefusedError{Message: "the target needs its fingerprint, from read or search. Current state:", Current: []string{sh.line()}}
	}
	if sh != nil && sh.fp == ed.Fingerprint {
		return sh, nil
	}
	var same []*pshape
	for _, x := range s.shapes {
		if x.fp == ed.Fingerprint {
			same = append(same, x)
		}
	}
	if len(same) == 1 {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("%s moved: it is now %s (same fingerprint).", ed.Target, same[0].addr()))
		return same[0], nil
	}
	r := &RefusedError{Message: fmt.Sprintf("%s no longer has fingerprint %s: the shape changed. Nothing was written. Current state:", ed.Target, ed.Fingerprint)}
	if sh != nil {
		r.Current = []string{sh.line()}
	}
	return nil, r
}

func (e *pptxEdit) addSpan(s *pslide, sp ...Span) {
	e.spans[s] = append(e.spans[s], sp...)
	e.edited[s] = true
}

// setText replaces a shape's text: one paragraph per line, each taking the properties and first run format of the
// paragraph that stood in its place (or the last one).
func (e *pptxEdit) setText(ed Edit) error {
	sh, err := e.shape(ed)
	if err != nil {
		return err
	}
	if sh.txBody == nil {
		return refuse("%s (%s) has no text body", sh.addr(), sh.kind)
	}
	data := sh.slide.x.Data
	body := sh.txBody
	paras := body.KidsNamed("p")
	if len(paras) == 0 {
		return refuse("%s has no paragraphs", sh.addr())
	}
	var mixed bool
	rPrOf := func(p *Node) string {
		rs := p.KidsNamed("r")
		if len(rs) == 0 {
			if epr := p.Kid("endParaRPr"); epr != nil {
				raw := string(epr.Raw(data))
				return strings.Replace(strings.Replace(raw, epr.Tag("endParaRPr"), epr.Tag("rPr"), 1), "</"+epr.Tag("endParaRPr"), "</"+epr.Tag("rPr"), 1)
			}
			return ""
		}
		first := ""
		for i, r := range rs {
			v := ""
			if k := r.Kid("rPr"); k != nil {
				v = string(k.Raw(data))
			}
			if i == 0 {
				first = v
			} else if v != first {
				mixed = true
			}
		}
		return first
	}
	sample := paras[0]
	var b strings.Builder
	for i, line := range strings.Split(ed.Text, "\n") {
		src := paras[min(i, len(paras)-1)]
		if k := src.Kid("fld"); k != nil {
			return refuse("%s holds a field (%s), which set_text would remove; use replace_text", sh.addr(), k.Attr("type"))
		}
		b.WriteString("<" + sample.Tag("p") + ">")
		if k := src.Kid("pPr"); k != nil {
			b.Write(k.Raw(data))
		}
		rPr := rPrOf(src)
		if line != "" {
			pieces := strings.Split(line, "\v")
			for j, piece := range pieces {
				if j > 0 {
					b.WriteString("<" + sample.Tag("br") + "/>")
				}
				b.WriteString("<" + sample.Tag("r") + ">" + rPr + "<" + sample.Tag("t") + ">" + escText(piece) + "</" + sample.Tag("t") + "></" + sample.Tag("r") + ">")
			}
		}
		if k := src.Kid("endParaRPr"); k != nil {
			b.Write(k.Raw(data))
		}
		b.WriteString("</" + sample.Tag("p") + ">")
	}
	if mixed {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("Mixed formatting in %s: paragraphs took the format of their first run.", sh.addr()))
	}
	e.addSpan(sh.slide, Span{paras[0].Start, paras[len(paras)-1].End, b.String()})
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Set the text of %s: %s", sh.addr(), oneLine(ed.Text, 80)))
	return nil
}

// replaceText replaces inside the runs of a shape, or any shape when no target is given. Runs stay as they are; the
// first touched run gets the new text, so it takes that run's format.
func (e *pptxEdit) replaceText(ed Edit) error {
	if ed.Find == "" {
		return refuse("replace_text needs find")
	}
	var candidates []*pshape
	if ed.Target != "" {
		sh, err := e.shape(ed)
		if err != nil {
			return err
		}
		candidates = []*pshape{sh}
	} else {
		for _, s := range e.d.slides {
			if !e.deleted[s] {
				candidates = append(candidates, s.shapes...)
			}
		}
	}
	type hit struct {
		sh   *pshape
		para *Node
		at   int
	}
	var hits []hit
	for _, sh := range candidates {
		if sh.txBody == nil {
			continue
		}
		for _, p := range sh.txBody.KidsNamed("p") {
			text := ""
			for _, r := range p.KidsNamed("r") {
				if t := r.Kid("t"); t != nil {
					text += t.Text
				}
			}
			for _, at := range allIndexes(text, ed.Find) {
				hits = append(hits, hit{sh, p, at})
			}
		}
	}
	if len(hits) == 0 {
		return refuse("%q is not in that text (a match cannot cross a line break or a field)", ed.Find)
	}
	if len(hits) > 1 && !ed.All {
		r := &RefusedError{Message: fmt.Sprintf("%q matches %d places. Give a target with its fingerprint, a longer find, or all: true. Candidates:", ed.Find, len(hits))}
		for i, h := range hits {
			if i == 20 {
				break
			}
			r.Current = append(r.Current, h.sh.line())
		}
		return r
	}
	// Group by paragraph, then rewrite each touched run's text.
	byPara := map[*Node][]hit{}
	for _, h := range hits {
		byPara[h.para] = append(byPara[h.para], h)
	}
	for p, hs := range byPara {
		sh := hs[0].sh
		data := sh.slide.x.Data
		pos := 0
		type runSeg struct {
			t        *Node
			run      *Node
			from, to int
		}
		var segs []runSeg
		for _, r := range p.KidsNamed("r") {
			if t := r.Kid("t"); t != nil {
				segs = append(segs, runSeg{t, r, pos, pos + len(t.Text)})
				pos += len(t.Text)
			}
		}
		mixedNote := false
		newText := make([]strings.Builder, len(segs))
		sort.Slice(hs, func(i, j int) bool { return hs[i].at < hs[j].at })
		for i, s := range segs {
			cur := s.from
			for _, h := range hs {
				a, b := h.at, h.at+len(ed.Find)
				if b <= s.from || a >= s.to {
					continue
				}
				if a > cur {
					newText[i].WriteString(segsText(s.t, s.from, cur, a))
				}
				if a >= s.from {
					newText[i].WriteString(ed.Text)
				} else if rawRPrOf(data, s.run) != rawRPrOf(data, segs[0].run) {
					mixedNote = true
				}
				cur = min(b, s.to)
			}
			if cur < s.to {
				newText[i].WriteString(segsText(s.t, s.from, cur, s.to))
			}
		}
		for i, s := range segs {
			nt := newText[i].String()
			if nt == s.t.Text {
				continue
			}
			if s.t.SelfClosed {
				e.addSpan(sh.slide, Span{s.t.Start, s.t.End, "<" + s.t.Tag("t") + ">" + escText(nt) + "</" + s.t.Tag("t") + ">"})
			} else {
				e.addSpan(sh.slide, Span{s.t.InnerStart, s.t.InnerEnd, escText(nt)})
			}
		}
		if mixedNote {
			e.res.Notes = append(e.res.Notes, fmt.Sprintf("Mixed formatting in %s: the matched text crossed differently formatted runs; the new text took the first one's.", sh.addr()))
		}
	}
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Replaced %d occurrence(s) of %q.", len(hits), oneLine(ed.Find, 60)))
	return nil
}

func segsText(t *Node, from, a, b int) string { return t.Text[a-from : b-from] }

func rawRPrOf(data []byte, r *Node) string {
	if k := r.Kid("rPr"); k != nil {
		return string(k.Raw(data))
	}
	return ""
}

// setGeometry sets a shape's position and size; values left out stay. A placeholder with inherited geometry gets its own.
func (e *pptxEdit) setGeometry(ed Edit) error {
	sh, err := e.shape(ed)
	if err != nil {
		return err
	}
	if sh.inGroup {
		return refuse("%s lies inside a group, whose coordinates are its own; move the group instead", sh.addr())
	}
	g := sh.geom
	if !g.ok && (ed.X == nil || ed.Y == nil || ed.CX == nil || ed.CY == nil) {
		return refuse("%s has no position of its own or from its layout; give x, y, cx and cy", sh.addr())
	}
	if ed.X != nil {
		g.x = *ed.X
	}
	if ed.Y != nil {
		g.y = *ed.Y
	}
	if ed.CX != nil {
		g.cx = *ed.CX
	}
	if ed.CY != nil {
		g.cy = *ed.CY
	}
	if g.cx < 0 || g.cy < 0 {
		return refuse("a size cannot be negative")
	}
	data := sh.slide.x.Data
	if pr := sh.node.Kid("spPr"); pr != nil && pr.Kid("xfrm") != nil || sh.node.Kid("xfrm") != nil || sh.node.Kid("grpSpPr").kidOrNil("xfrm") != nil {
		var x *Node
		switch {
		case sh.node.Kid("spPr") != nil:
			x = sh.node.Kid("spPr").Kid("xfrm")
		case sh.node.Kid("xfrm") != nil:
			x = sh.node.Kid("xfrm")
		default:
			x = sh.node.Kid("grpSpPr").Kid("xfrm")
		}
		off, ext := x.Kid("off"), x.Kid("ext")
		if off == nil || ext == nil {
			return refuse("%s has an incomplete position", sh.addr())
		}
		e.addSpan(sh.slide, setAttrSpan(data, off, "x", strconv.FormatInt(g.x, 10)), setAttrSpan(data, off, "y", strconv.FormatInt(g.y, 10)), setAttrSpan(data, ext, "cx", strconv.FormatInt(g.cx, 10)), setAttrSpan(data, ext, "cy", strconv.FormatInt(g.cy, 10)))
	} else if pr := sh.node.Kid("spPr"); pr != nil {
		a := "a"
		if len(pr.Kids) > 0 {
			a = pr.Kids[0].Prefix
		}
		xml := fmt.Sprintf(`<%[1]s:xfrm><%[1]s:off x="%[2]d" y="%[3]d"/><%[1]s:ext cx="%[4]d" cy="%[5]d"/></%[1]s:xfrm>`, a, g.x, g.y, g.cx, g.cy)
		if pr.SelfClosed {
			e.addSpan(sh.slide, insertKidSpan(data, pr, xml))
		} else {
			e.addSpan(sh.slide, Span{pr.InnerStart, pr.InnerStart, xml})
		}
	} else {
		return refuse("%s has no shape properties to position", sh.addr())
	}
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Moved %s: %s → %s", sh.addr(), sh.geom, g))
	return nil
}

func (e *pptxEdit) deleteShape(ed Edit) error {
	sh, err := e.shape(ed)
	if err != nil {
		return err
	}
	if sh.isPH {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("%s was a placeholder: the layout's prompt no longer appears on this slide in edit view.", sh.addr()))
	}
	e.addSpan(sh.slide, Span{sh.node.Start, sh.node.End, ""})
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Deleted %s %q.", sh.addr(), sh.name))
	return nil
}

func drawingPrefix(root *Node) (string, bool) {
	for _, a := range root.Attrs {
		if a.Name.Space == "xmlns" && (a.Value == drawingNS || a.Value == drawingNSStrict) {
			return a.Name.Local, true
		}
	}
	return "", false
}

func textBoxXML(p string, a string, id int, name string, g rect, text string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<%[1]s:sp><%[1]s:nvSpPr><%[1]s:cNvPr id="%[3]d" name="%[4]s"/><%[1]s:cNvSpPr txBox="1"/><%[1]s:nvPr/></%[1]s:nvSpPr><%[1]s:spPr><%[2]s:xfrm><%[2]s:off x="%[5]d" y="%[6]d"/><%[2]s:ext cx="%[7]d" cy="%[8]d"/></%[2]s:xfrm><%[2]s:prstGeom prst="rect"><%[2]s:avLst/></%[2]s:prstGeom><%[2]s:noFill/></%[1]s:spPr><%[1]s:txBody><%[2]s:bodyPr wrap="square" rtlCol="0"><%[2]s:noAutofit/></%[2]s:bodyPr><%[2]s:lstStyle/>`, p, a, id, escAttr(name), g.x, g.y, g.cx, g.cy)
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(&b, `<%[1]s:p><%[1]s:r><%[1]s:rPr lang="en-US"/><%[1]s:t>%[2]s</%[1]s:t></%[1]s:r></%[1]s:p>`, a, escText(line))
	}
	fmt.Fprintf(&b, `</%s:txBody></%s:sp>`, p, p)
	return b.String()
}

// addTextbox adds a text box to a slide; target is the slide.
func (e *pptxEdit) addTextbox(ed Edit) error {
	if !pptxSlideRe.MatchString(ed.Target) {
		return refuse("target %q must be a slide, such as s256", ed.Target)
	}
	s := e.d.byID[strings.TrimPrefix(ed.Target, "s")]
	if s == nil {
		return refuse("no slide %s", ed.Target)
	}
	if ed.X == nil || ed.Y == nil || ed.CX == nil || ed.CY == nil {
		return refuse("add_textbox needs x, y, cx and cy, in EMU")
	}
	a, ok := drawingPrefix(s.x.Root)
	if !ok {
		return refuse("slide %s does not declare the DrawingML namespace at its root", ed.Target)
	}
	tree := s.x.Root.Kid("cSld").Kid("spTree")
	maxID := 1
	for _, sh := range s.shapes {
		if v, err := strconv.Atoi(sh.id); err == nil && v > maxID {
			maxID = v
		}
	}
	// New shapes of this batch number after each other.
	for _, sp := range e.spans[s] {
		maxID += strings.Count(sp.Text, "txBox=")
	}
	id := maxID + 1
	xml := textBoxXML(s.x.Root.Prefix, a, id, fmt.Sprintf("TextBox %d", id), rect{x: *ed.X, y: *ed.Y, cx: *ed.CX, cy: *ed.CY}, ed.Text)
	// extLst must stay last in the tree.
	at := tree.InnerEnd
	if ext := tree.Kid("extLst"); ext != nil {
		at = ext.Start
	}
	e.addSpan(s, Span{at, at, xml})
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Added text box s%s/sp%d.", s.id, id))
	return nil
}

// presentationIDs reads the slide list as it stands in this batch.
func (e *pptxEdit) presentation() ([]byte, *XMLDoc, *Node, error) {
	data, err := e.c.Current(e.d.pkg, e.d.part)
	if err != nil {
		return nil, nil, nil, err
	}
	x, err := ParseXML(data)
	if err != nil {
		return nil, nil, nil, err
	}
	lst := x.Root.Kid("sldIdLst")
	if lst == nil {
		return nil, nil, nil, refuse("the presentation has no slide list")
	}
	return data, x, lst, nil
}

func sldIDOf(n *Node) string {
	for _, a := range n.Attrs {
		if a.Name.Local == "id" && a.Name.Space == "" {
			return a.Value
		}
	}
	return ""
}

func (e *pptxEdit) newSlidePart() string {
	e.nextNum++
	dir := "ppt/slides"
	if len(e.d.slides) > 0 {
		dir = path.Dir(e.d.slides[0].part)
	}
	return fmt.Sprintf("%s/slide%d.xml", dir, e.nextNum)
}

const slideContentType = "application/vnd.openxmlformats-officedocument.presentationml.slide+xml"

// insertSlideID adds the slide to the list at a 1-based position (0: the end) and to the section of its neighbour.
func (e *pptxEdit) insertSlideID(rid string, position int) (string, int, error) {
	data, x, lst, err := e.presentation()
	if err != nil {
		return "", 0, err
	}
	kids := lst.KidsNamed("sldId")
	maxID := int64(255)
	for _, k := range kids {
		if v, err := strconv.ParseInt(sldIDOf(k), 10, 64); err == nil && v > maxID {
			maxID = v
		}
	}
	id := strconv.FormatInt(maxID+1, 10)
	if position <= 0 || position > len(kids)+1 {
		position = len(kids) + 1
	}
	rPrefix := "r"
	for _, a := range x.Root.Attrs {
		if a.Name.Space == "xmlns" && strings.HasSuffix(a.Value, "/relationships") {
			rPrefix = a.Name.Local
		}
	}
	el := fmt.Sprintf(`<%s id="%s" %s:id="%s"/>`, lst.Tag("sldId"), id, rPrefix, rid)
	var sp Span
	if position <= len(kids) {
		at := kids[position-1].Start
		sp = Span{at, at, el}
	} else {
		sp = insertKidSpan(data, lst, el)
	}
	updated := Splice(data, []Span{sp})
	neighbour := ""
	if position > 1 {
		neighbour = sldIDOf(kids[position-2])
	} else if len(kids) > 0 {
		neighbour = "first:" + sldIDOf(kids[0])
	}
	updated = addToSection(updated, id, neighbour)
	e.c.Set[e.d.part] = updated
	return id, position, nil
}

// addToSection keeps the optional section list (an extension of presentation.xml) in step with the slide list.
func addToSection(data []byte, id, neighbour string) []byte {
	x, err := ParseXML(data)
	if err != nil {
		return data
	}
	lists := x.Root.Find("sectionLst")
	if len(lists) == 0 {
		return data
	}
	first := strings.HasPrefix(neighbour, "first:")
	want := strings.TrimPrefix(neighbour, "first:")
	for _, n := range lists[0].Find("sldId") {
		if n.Attr("id") == want {
			el := "<" + n.Tag("sldId") + ` id="` + id + `"/>`
			at := n.End
			if first {
				at = n.Start
			}
			return Splice(data, []Span{{at, at, el}})
		}
	}
	secs := lists[0].Find("sldIdLst")
	if len(secs) > 0 {
		last := secs[len(secs)-1]
		el := "<" + last.Tag("sldId") + ` id="` + id + `"/>`
		return InsertBeforeClose(data, last, el)
	}
	return data
}

func removeFromSection(data []byte, id string) []byte {
	x, err := ParseXML(data)
	if err != nil {
		return data
	}
	lists := x.Root.Find("sectionLst")
	if len(lists) == 0 {
		return data
	}
	var spans []Span
	for _, n := range lists[0].Find("sldId") {
		if n.Attr("id") == id {
			spans = append(spans, Span{n.Start, n.End, ""})
		}
	}
	return Splice(data, spans)
}

// addSlide adds a slide from a layout, or as a copy of another slide.
func (e *pptxEdit) addSlide(ed Edit) error {
	part := e.newSlidePart()
	relT := "http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide"
	if pr, _ := e.d.pkg.Rels(e.d.part); len(pr) > 0 {
		if r, ok := relByType(pr, "/slide"); ok {
			relT = r.Type
		}
	}
	if ed.CloneOf != "" {
		if err := e.cloneSlide(ed, part); err != nil {
			return err
		}
	} else if err := e.slideFromLayout(ed, part); err != nil {
		return err
	}
	if err := e.c.addContentType(e.d.pkg, part, slideContentType); err != nil {
		return err
	}
	rid, err := e.c.addRelationship(e.d.pkg, e.d.part, relT, relTargetFrom(e.d.part, part), false)
	if err != nil {
		return err
	}
	id, pos, err := e.insertSlideID(rid, ed.Position)
	if err != nil {
		return err
	}
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Added slide s%s at position %d.", id, pos))
	return nil
}

func (e *pptxEdit) layoutByName(name string) (*playout, error) {
	var hits []*playout
	for i := range e.d.allLayout {
		l := &e.d.allLayout[i]
		if strings.EqualFold(l.name, name) {
			hits = append(hits, l)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	var names []string
	for _, l := range e.d.allLayout {
		names = append(names, fmt.Sprintf("%q (%s)", l.name, l.part))
	}
	if len(hits) > 1 {
		return nil, &RefusedError{Message: fmt.Sprintf("the layout name %q fits %d layouts (several masters). Clone a slide that uses the right one. Layouts:", name, len(hits)), Current: names}
	}
	return nil, &RefusedError{Message: fmt.Sprintf("no layout %q. Layouts:", name), Current: names}
}

// slideFromLayout writes a new slide holding the layout's placeholders, filled from title and text.
func (e *pptxEdit) slideFromLayout(ed Edit, part string) error {
	if ed.Layout == "" {
		return refuse("add_slide needs layout, or clone_of")
	}
	l, err := e.layoutByName(ed.Layout)
	if err != nil {
		return err
	}
	lx := e.d.layouts[l.part]
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	b.WriteString(`<p:sld xmlns:a="` + drawingNS + `" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/>`)
	id := 2
	usedTitle, usedBody := false, false
	if tree := lx.Root.Kid("cSld").kidOrNil("spTree"); tree != nil {
		for _, k := range tree.KidsNamed("sp") {
			if len(k.Kids) == 0 {
				continue
			}
			ph := k.Kids[0].Kid("nvPr").kidOrNil("ph")
			if ph == nil {
				continue
			}
			t := ph.Attr("type")
			if t == "dt" || t == "ftr" || t == "sldNum" {
				continue
			}
			attrs := ""
			if t != "" {
				attrs += ` type="` + escAttr(t) + `"`
			}
			if v := ph.Attr("idx"); v != "" {
				attrs += ` idx="` + escAttr(v) + `"`
			}
			text := ""
			if (t == "title" || t == "ctrTitle") && !usedTitle {
				text, usedTitle = ed.Title, true
			} else if !usedBody && (t == "" || t == "body" || t == "subTitle" || t == "obj") {
				text, usedBody = ed.Text, true
			}
			name := k.Kids[0].Kid("cNvPr").attrOrEmpty("name")
			fmt.Fprintf(&b, `<p:sp><p:nvSpPr><p:cNvPr id="%d" name="%s"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph%s/></p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:lstStyle/>`, id, escAttr(name), attrs)
			for _, line := range strings.Split(text, "\n") {
				if line == "" {
					b.WriteString(`<a:p><a:endParaRPr lang="en-US"/></a:p>`)
				} else {
					fmt.Fprintf(&b, `<a:p><a:r><a:rPr lang="en-US"/><a:t>%s</a:t></a:r></a:p>`, escText(line))
				}
			}
			b.WriteString(`</p:txBody></p:sp>`)
			id++
		}
	}
	if ed.Title != "" && !usedTitle {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("The layout %q has no title placeholder: the title was not placed.", ed.Layout))
	}
	if ed.Text != "" && !usedBody {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("The layout %q has no body placeholder: the text was not placed.", ed.Layout))
	}
	b.WriteString(`</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`)
	e.c.Set[part] = []byte(b.String())
	relT := "http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout"
	_, err = e.c.addRelationship(e.d.pkg, part, relT, "../slideLayouts/"+path.Base(l.part), false)
	if path.Dir(l.part) != "ppt/slideLayouts" {
		// Unusual package layout: an absolute target always resolves.
		e.c.Set[RelsPart(part)] = nil
		delete(e.c.Set, RelsPart(part))
		_, err = e.c.addRelationship(e.d.pkg, part, relT, "/"+l.part, false)
	}
	return err
}

// Parts a clone shares with its source; a notes slide belongs to one slide, so it is dropped, as are legacy comments.
func sharedWithClone(relType string) bool {
	for _, s := range []string{"/slideLayout", "/image", "/video", "/audio", "/media", "/hyperlink", "/slide", "/theme", "/slideMaster"} {
		if strings.HasSuffix(relType, s) {
			return true
		}
	}
	return false
}

func droppedFromClone(relType string) bool {
	return strings.HasSuffix(relType, "/notesSlide") || strings.HasSuffix(relType, "/comments")
}

func (e *pptxEdit) cloneSlide(ed Edit, part string) error {
	id := strings.TrimPrefix(ed.CloneOf, "s")
	src := e.d.byID[id]
	if src == nil {
		return refuse("no slide %s to clone", ed.CloneOf)
	}
	if e.deleted[src] {
		return refuse("slide %s is being deleted in this batch", ed.CloneOf)
	}
	if e.edited[src] {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("The clone of %s has its state before this batch's other edits.", ed.CloneOf))
	}
	e.c.Set[part] = src.x.Data
	copies, drops, err := e.cloneRels(src.part, part, map[string]string{})
	if err != nil {
		return err
	}
	if len(copies) > 0 {
		sort.Strings(copies)
		e.res.Notes = append(e.res.Notes, "The clone got its own copies of: "+strings.Join(copies, ", ")+". Editing them later does not touch the original; check them in PowerPoint.")
	}
	if len(drops) > 0 {
		e.res.Notes = append(e.res.Notes, "The clone does not carry over: "+strings.Join(drops, ", ")+".")
	}
	return nil
}

// cloneRels gives a copied part its relationships: same ids, so the part's XML stays valid. Shared targets are pointed
// at; owned ones are copied, recursively, under fresh names.
func (e *pptxEdit) cloneRels(srcPart, dstPart string, done map[string]string) (copies, drops []string, err error) {
	rels, err := e.d.pkg.Rels(srcPart)
	if err != nil {
		return nil, nil, err
	}
	if len(rels) == 0 {
		return nil, nil, nil
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for _, r := range rels {
		target := r.Target
		mode := ""
		if r.External {
			mode = ` TargetMode="External"`
		} else {
			t := ResolveTarget(srcPart, r.Target)
			switch {
			case droppedFromClone(r.Type):
				drops = append(drops, path.Base(t))
				continue
			case sharedWithClone(r.Type):
				target = "/" + t
			default:
				nt, ok := done[t]
				if !ok {
					nt = e.freshCopyName(t)
					done[t] = nt
					data, err := e.d.pkg.Read(t)
					if err != nil {
						return nil, nil, err
					}
					e.c.Set[nt] = data
					if ct, err := e.d.pkg.XML(contentTypesPart); err == nil {
						if v := contentTypeOf(ct, t); v != "" {
							if err := e.c.addContentType(e.d.pkg, nt, v); err != nil {
								return nil, nil, err
							}
						}
					}
					c2, d2, err := e.cloneRels(t, nt, done)
					if err != nil {
						return nil, nil, err
					}
					copies = append(append(copies, path.Base(t)), c2...)
					drops = append(drops, d2...)
				}
				target = "/" + nt
			}
		}
		fmt.Fprintf(&b, `<Relationship Id="%s" Type="%s" Target="%s"%s/>`, escAttr(r.ID), escAttr(r.Type), escAttr(target), mode)
	}
	b.WriteString(`</Relationships>`)
	e.c.Set[RelsPart(dstPart)] = []byte(b.String())
	return copies, drops, nil
}

var trailingNumRe = regexp.MustCompile(`\d*$`)

func (e *pptxEdit) freshCopyName(t string) string {
	dir, base := path.Split(t)
	ext := path.Ext(base)
	stem := trailingNumRe.ReplaceAllString(strings.TrimSuffix(base, ext), "")
	for i := 1000; ; i++ {
		n := fmt.Sprintf("%s%s%d%s", dir, stem, i, ext)
		if _, used := e.c.Set[n]; !used && !e.d.pkg.Has(n) {
			return n
		}
	}
}

func (e *pptxEdit) slideArg(target string) (*pslide, error) {
	if !pptxSlideRe.MatchString(target) {
		return nil, refuse("target %q must be a slide, such as s256", target)
	}
	s := e.d.byID[strings.TrimPrefix(target, "s")]
	if s == nil {
		return nil, refuse("no slide %s", target)
	}
	if e.deleted[s] {
		return nil, refuse("slide %s is already deleted in this batch", target)
	}
	return s, nil
}

func (e *pptxEdit) deleteSlide(ed Edit) error {
	s, err := e.slideArg(ed.Target)
	if err != nil {
		return err
	}
	if e.edited[s] {
		return refuse("another edit of this batch changes %s", ed.Target)
	}
	data, x, lst, err := e.presentation()
	if err != nil {
		return err
	}
	if len(lst.KidsNamed("sldId")) <= 1 {
		return refuse("%s is the only slide", ed.Target)
	}
	var spans []Span
	var rid string
	for _, k := range lst.KidsNamed("sldId") {
		if sldIDOf(k) == s.id {
			spans = append(spans, Span{k.Start, k.End, ""})
			for _, a := range k.Attrs {
				if a.Name.Local == "id" && a.Name.Space != "" {
					rid = a.Value
				}
			}
		}
	}
	// A custom show may list the slide by its relationship.
	for _, n := range x.Root.Find("sld") {
		for _, a := range n.Attrs {
			if a.Name.Local == "id" && a.Value == rid {
				spans = append(spans, Span{n.Start, n.End, ""})
			}
		}
	}
	e.c.Set[e.d.part] = removeFromSection(Splice(data, spans), s.id)
	if err := e.c.removeRelationship(e.d.pkg, e.d.part, rid); err != nil {
		return err
	}
	e.deleted[s] = true
	if s.notesPart != "" {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("The speaker notes of %s went with it: %s", ed.Target, oneLine(s.notes, 100)))
	}
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Deleted slide %s (%q).", ed.Target, oneLine(s.title, 60)))
	return nil
}

func (e *pptxEdit) moveSlide(ed Edit) error {
	s, err := e.slideArg(ed.Target)
	if err != nil {
		return err
	}
	data, _, lst, err := e.presentation()
	if err != nil {
		return err
	}
	kids := lst.KidsNamed("sldId")
	if ed.Position < 1 || ed.Position > len(kids) {
		return refuse("position must be from 1 to %d", len(kids))
	}
	var cur *Node
	var rest []*Node
	for _, k := range kids {
		if sldIDOf(k) == s.id {
			cur = k
		} else {
			rest = append(rest, k)
		}
	}
	if cur == nil {
		return refuse("slide %s is not in the slide list", ed.Target)
	}
	raw := string(cur.Raw(data))
	var b strings.Builder
	for i := 0; i <= len(rest); i++ {
		if i == ed.Position-1 {
			b.WriteString(raw)
		}
		if i < len(rest) {
			b.Write(rest[i].Raw(data))
		}
	}
	updated := Splice(data, []Span{{lst.InnerStart, lst.InnerEnd, b.String()}})
	neighbour := ""
	if ed.Position > 1 {
		neighbour = sldIDOf(rest[ed.Position-2])
	} else if len(rest) > 0 {
		neighbour = "first:" + sldIDOf(rest[0])
	}
	updated = addToSection(removeFromSection(updated, s.id), s.id, neighbour)
	e.c.Set[e.d.part] = updated
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Moved slide %s to position %d.", ed.Target, ed.Position))
	return nil
}

func (e *pptxEdit) finish() (*EditResult, error) {
	for s, sp := range e.spans {
		if spansOverlap(sp) {
			return nil, refuse("the edits of this batch overlap on slide s%s; send them in separate calls", s.id)
		}
		e.c.Set[s.part] = Splice(s.x.Data, sp)
	}
	if err := e.sweep(); err != nil {
		return nil, err
	}
	e.updateAppCount()
	data, err := e.d.pkg.Write(e.c)
	if err != nil {
		return nil, err
	}
	e.res.Data = data
	return e.res, nil
}

// sweep removes the parts a deleted slide alone led to: its notes, its charts, media nothing else uses. Whatever was
// reachable before and is not any more goes, with its relationships and content type.
func (e *pptxEdit) sweep() error {
	if len(e.deleted) == 0 {
		return nil
	}
	reach := func(current bool) map[string]bool {
		seen := map[string]bool{}
		var visit func(part string)
		visit = func(part string) {
			var rels []Relationship
			name := RelsPart(part)
			switch {
			case current && e.c.Remove[name]:
				return
			case current && e.c.Set[name] != nil:
				if x, err := ParseXML(e.c.Set[name]); err == nil {
					rels = relsOf(x)
				}
			default:
				rels, _ = e.d.pkg.Rels(part)
			}
			for _, r := range rels {
				if r.External || strings.HasPrefix(r.Target, "#") {
					continue
				}
				t := ResolveTarget(part, r.Target)
				if !seen[t] {
					seen[t] = true
					visit(t)
				}
			}
		}
		visit("")
		return seen
	}
	// Back-links from a notes slide to its slide must not keep the slide alive: the walk starts at the package root,
	// and a deleted slide is reachable only through itself.
	before, after := reach(false), reach(true)
	for part := range before {
		if after[part] || !e.d.pkg.Has(part) {
			continue
		}
		e.c.Remove[part] = true
		delete(e.c.Set, part)
		if err := e.c.removeContentType(e.d.pkg, part); err != nil {
			return err
		}
		if rn := RelsPart(part); e.d.pkg.Has(rn) {
			e.c.Remove[rn] = true
			delete(e.c.Set, rn)
		}
	}
	return nil
}

var appSlidesRe = regexp.MustCompile(`(<Slides>)\d+(</Slides>)`)

// updateAppCount keeps the slide count in the document properties right when slides were added or removed.
func (e *pptxEdit) updateAppCount() {
	data, err := e.c.Current(e.d.pkg, e.d.part)
	if err != nil {
		return
	}
	x, err := ParseXML(data)
	if err != nil {
		return
	}
	n := len(x.Root.Kid("sldIdLst").KidsNamed("sldId"))
	if n == len(e.d.slides) || !e.d.pkg.Has("docProps/app.xml") {
		return
	}
	app, err := e.d.pkg.Read("docProps/app.xml")
	if err != nil {
		return
	}
	e.c.Set["docProps/app.xml"] = appSlidesRe.ReplaceAll(app, []byte(fmt.Sprintf("${1}%d${2}", n)))
}
