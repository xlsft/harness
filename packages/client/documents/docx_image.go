package documents

import (
	"fmt"
	"strconv"
	"strings"
)

// Pictures in Word documents (ECMA-376 Part 1, §20.4: DrawingML in WordprocessingML). A picture is a w:drawing in a
// run, holding a wp:inline (in the line of text) or a wp:anchor (floating), and in it a pic:pic whose a:blip names the
// media part through the story part's relationships. Pictures are addressed by their paragraph and a 1-based position
// in it, so the paragraph's fingerprint guards them like any other edit.

const (
	wpNS        = "http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"
	wpNSStrict  = "http://purl.oclc.org/ooxml/drawingml/wordprocessingDrawing"
	picNS       = "http://schemas.openxmlformats.org/drawingml/2006/picture"
	picNSStrict = "http://purl.oclc.org/ooxml/drawingml/picture"
	// twip is a twentieth of a point; 635 EMU.
	emuPerTwip = 635
)

type docxImage struct {
	para                        *docxPara
	n                           int
	run, drawing, frame         *Node
	blip, extent, docPr, picExt *Node
	srcRect                     *Node
	media                       string
	cx, cy                      int64
}

func (im *docxImage) inline() bool { return im.frame.Local == "inline" }

// imagesOf lists the pictures of a paragraph in order. Text boxes and charts are drawings too; only drawings with a
// picture fill count.
func (d *docxDoc) imagesOf(p *docxPara) []*docxImage {
	var out []*docxImage
	var rels []Relationship
	for _, s := range p.segs {
		if s.kind != segObject || !isW(s.child) || s.child.Local != "drawing" || len(s.child.Kids) == 0 {
			continue
		}
		frame := s.child.Kids[0]
		var pic *Node
		frame.Walk(func(n *Node) bool {
			if pic == nil && n.Local == "pic" && (n.NS == picNS || n.NS == picNSStrict) {
				pic = n
			}
			return pic == nil
		})
		if pic == nil {
			continue
		}
		blip := pic.Kid("blipFill").kidOrNil("blip")
		if blip == nil {
			continue
		}
		im := &docxImage{para: p, n: len(out) + 1, run: s.run, drawing: s.child, frame: frame, blip: blip, extent: frame.Kid("extent"), docPr: frame.Kid("docPr"), srcRect: pic.Kid("blipFill").Kid("srcRect")}
		if x := pic.Kid("spPr").kidOrNil("xfrm"); x != nil {
			im.picExt = x.Kid("ext")
		}
		if im.extent != nil {
			im.cx, _ = strconv.ParseInt(im.extent.Attr("cx"), 10, 64)
			im.cy, _ = strconv.ParseInt(im.extent.Attr("cy"), 10, 64)
		}
		if rels == nil {
			rels, _ = d.pkg.Rels(p.story.part)
		}
		if r, ok := relByID(rels, relAttr(blip, "embed")); ok {
			if r.External {
				im.media = "linked " + r.Target
			} else {
				im.media = ResolveTarget(p.story.part, r.Target)
			}
		} else if l := relAttr(blip, "link"); l != "" {
			im.media = "linked"
		}
		out = append(out, im)
	}
	return out
}

func (im *docxImage) line() string {
	kind := "inline"
	if !im.inline() {
		kind = "floating"
	}
	alt := ""
	if im.docPr != nil {
		alt = im.docPr.Attr("descr")
	}
	return fmt.Sprintf("%s #%s picture %d: %s, %s, alt=%q, %s", im.para.id(), im.para.fp, im.n, emuText(im.cx, im.cy), kind, oneLine(alt, 120), im.media)
}

func (d *docxDoc) imageLines() []string {
	var lines []string
	for _, s := range d.stories {
		for _, p := range s.paras {
			if p.objects == 0 {
				continue
			}
			for _, im := range d.imagesOf(p) {
				lines = append(lines, im.line())
			}
		}
	}
	return lines
}

// textArea is the room between the margins of the document's last section, in EMU; 6×9 inches when it says nothing.
func (d *docxDoc) textArea() (int64, int64) {
	w, h := int64(6*emuPerInch), int64(9*emuPerInch)
	sects := d.stories[0].xml.Root.Find("sectPr")
	if len(sects) == 0 {
		return w, h
	}
	s := sects[len(sects)-1]
	num := func(n *Node, a string) int64 {
		v, _ := strconv.ParseInt(n.attrOrEmpty(a), 10, 64)
		return v
	}
	sz, mar := s.Kid("pgSz"), s.Kid("pgMar")
	if sz == nil {
		return w, h
	}
	pw, ph := num(sz, "w"), num(sz, "h")
	tw, th := pw-num(mar, "left")-num(mar, "right"), ph-num(mar, "top")-num(mar, "bottom")
	if tw > 0 {
		w = tw * emuPerTwip
	}
	if th > 0 {
		h = th * emuPerTwip
	}
	return w, h
}

// newDocPrID gives a drawing id no other drawing of the document has.
func (e *docxEdit) newDocPrID() int {
	if e.docPrID == 0 {
		for _, s := range e.d.stories {
			for _, n := range s.xml.Root.Find("docPr") {
				if v, err := strconv.Atoi(n.Attr("id")); err == nil && v > e.docPrID {
					e.docPrID = v
				}
			}
		}
	}
	e.docPrID++
	return e.docPrID
}

func (d *docxDoc) strict() bool { return d.stories[0].xml.Root.NS == wordNSStrict }

// drawingXML is a new picture: inline, or floating at x, y from the page's corner when both are given.
func drawingXML(w *Node, strict bool, id int, alt, relID string, cx, cy int64, x, y *int64) string {
	wp, a, pic, r := wpNS, drawingNS, picNS, relNSTransitional
	if strict {
		wp, a, pic, r = wpNSStrict, drawingNSStrict, picNSStrict, relNSStrict
	}
	ns := fmt.Sprintf(`xmlns:wp="%s" xmlns:a="%s" xmlns:pic="%s" xmlns:r="%s"`, wp, a, pic, r)
	common := fmt.Sprintf(`<wp:extent cx="%[1]d" cy="%[2]d"/><wp:effectExtent l="0" t="0" r="0" b="0"/>`, cx, cy)
	tail := fmt.Sprintf(`<wp:docPr id="%[1]d" name="Picture %[1]d" descr="%[2]s"/><wp:cNvGraphicFramePr><a:graphicFrameLocks noChangeAspect="1"/></wp:cNvGraphicFramePr><a:graphic><a:graphicData uri="%[3]s"><pic:pic><pic:nvPicPr><pic:cNvPr id="0" name="Picture %[1]d" descr="%[2]s"/><pic:cNvPicPr/></pic:nvPicPr><pic:blipFill><a:blip r:embed="%[4]s"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill><pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%[5]d" cy="%[6]d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr></pic:pic></a:graphicData></a:graphic>`,
		id, escAttr(alt), pic, relID, cx, cy)
	var frame string
	if x != nil && y != nil {
		frame = fmt.Sprintf(`<wp:anchor %s distT="0" distB="0" distL="114300" distR="114300" simplePos="0" relativeHeight="%d" behindDoc="0" locked="0" layoutInCell="1" allowOverlap="1"><wp:simplePos x="0" y="0"/><wp:positionH relativeFrom="page"><wp:posOffset>%d</wp:posOffset></wp:positionH><wp:positionV relativeFrom="page"><wp:posOffset>%d</wp:posOffset></wp:positionV>%s<wp:wrapSquare wrapText="bothSides"/>%s</wp:anchor>`,
			ns, 251658240+id, *x, *y, common, tail)
	} else {
		frame = fmt.Sprintf(`<wp:inline %s distT="0" distB="0" distL="0" distR="0">%s%s</wp:inline>`, ns, common, tail)
	}
	return "<" + w.Tag("drawing") + ">" + frame + "</" + w.Tag("drawing") + ">"
}

// insertImage puts a picture in a new paragraph after the target, or into the target right after `find`.
func (e *docxEdit) insertImage(ed Edit) error {
	p, err := e.resolve(ed)
	if err != nil {
		return err
	}
	pic, err := loadPicture(ed.ImageData)
	if err != nil {
		return err
	}
	if (ed.X == nil) != (ed.Y == nil) {
		return refuse("a floating picture needs both x and y (EMU from the page's top-left corner); leave both out for one in the line of text")
	}
	maxW, maxH := e.d.textArea()
	cx, cy, err := pic.sizeEMU(ed.CX, ed.CY, maxW, maxH)
	if err != nil {
		return err
	}
	strict := e.d.strict()
	relID, part, err := e.c.addMediaPart(e.d.pkg, p.story.part, mediaDir(p.story.part), pic, strict)
	if err != nil {
		return err
	}
	id := e.newDocPrID()
	alt := ""
	if ed.Alt != nil {
		alt = *ed.Alt
	} else {
		e.res.Notes = append(e.res.Notes, "The new picture has no alternative text; pass alt to describe it, or alt \"\" if it is decorative.")
	}
	n := p.node
	track := e.tracked(ed)
	run := "<" + n.Tag("r") + ">" + drawingXML(n, strict, id, alt, relID, cx, cy, ed.X, ed.Y) + "</" + n.Tag("r") + ">"
	if track {
		run = e.changeOpen(n, "ins", authorOf(ed)) + run + e.closeTag(n, "ins")
	}
	where := "in a new paragraph after " + p.id()
	if ed.Find != "" {
		hits := allIndexes(p.text, ed.Find)
		if len(hits) != 1 {
			return &RefusedError{Message: fmt.Sprintf("%q matches %d places in %s; it must match one. Current state:", ed.Find, len(hits), p.id()), Current: []string{p.current()}}
		}
		if err := e.addRep(p, &rep{a: hits[0], b: hits[0] + len(ed.Find), keep: true, after: run}); err != nil {
			return err
		}
		where = fmt.Sprintf("in %s after %q", p.id(), oneLine(ed.Find, 40))
	} else {
		pPr := ""
		if ed.Style != "" {
			sid, err := e.styleID(ed.Style)
			if err != nil {
				return err
			}
			pPr = fmt.Sprintf(`<%s><%s %s="%s"/></%s>`, n.Tag("pPr"), n.Tag("pStyle"), n.Tag("val"), escAttr(sid), n.Tag("pPr"))
		}
		if track {
			pPr = e.markParagraph(n, pPr, "ins", authorOf(ed))
		}
		e.spans[p.story] = append(e.spans[p.story], Span{n.End, n.End, "<" + n.Tag("p") + ">" + pPr + run + "</" + n.Tag("p") + ">"})
	}
	kind := "inline"
	if ed.X != nil {
		kind = "floating"
	}
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Inserted a %s picture (%s, %d×%d px, %s) %s, stored as %s%s.", kind, strings.ToUpper(pic.format), pic.w, pic.h, emuText(cx, cy), where, part, trackedWord(track)))
	return nil
}

// replaceImage swaps the picture of a drawing, fitted into its old frame unless cx or cy say otherwise, or only sets
// its size or alternative text. Tracked, the old run is deleted and a copy with the new picture inserted.
func (e *docxEdit) replaceImage(ed Edit) error {
	p, err := e.resolve(ed)
	if err != nil {
		return err
	}
	imgs := e.d.imagesOf(p)
	if len(imgs) == 0 {
		return &RefusedError{Message: fmt.Sprintf("%s holds no picture. Current state:", p.id()), Current: []string{p.current()}}
	}
	n := ed.Position
	if n == 0 {
		if len(imgs) > 1 {
			r := &RefusedError{Message: fmt.Sprintf("%s holds %d pictures; say which with position. Candidates:", p.id(), len(imgs))}
			for _, im := range imgs {
				r.Current = append(r.Current, im.line())
			}
			return r
		}
		n = 1
	}
	if n < 1 || n > len(imgs) {
		return refuse("%s holds %d picture(s); position %d does not exist", p.id(), len(imgs), n)
	}
	im := imgs[n-1]
	if ed.ImageData == nil && ed.Alt == nil && ed.CX == nil && ed.CY == nil {
		return refuse("replace_image needs image, alt, cx or cy")
	}
	data := p.story.xml.Data
	var spans []Span
	var said []string
	cx, cy := im.cx, im.cy
	track := false
	if ed.ImageData != nil {
		pic, err := loadPicture(ed.ImageData)
		if err != nil {
			return err
		}
		track = e.tracked(ed)
		relID, part, err := e.c.addMediaPart(e.d.pkg, p.story.part, mediaDir(p.story.part), pic, e.d.strict())
		if err != nil {
			return err
		}
		spans = append(spans, Span{im.blip.Start, im.blip.End, newBlip(data, im.blip, relID, e.d.strict())})
		if im.srcRect != nil {
			spans = append(spans, Span{im.srcRect.Start, im.srcRect.End, ""})
		}
		if ed.CX == nil && ed.CY == nil && cx > 0 && cy > 0 {
			g := pic.fitBox(rect{cx: cx, cy: cy})
			cx, cy = g.cx, g.cy
		} else if cx, cy, err = pic.sizeEMU(ed.CX, ed.CY, 0, 0); err != nil {
			return err
		}
		if !track {
			e.maybeDrop = append(e.maybeDrop, docxDrop{p.story, relIDsIn(im.blip)})
		}
		said = append(said, fmt.Sprintf("picture → %s (%s, %d×%d px)", part, strings.ToUpper(pic.format), pic.w, pic.h))
		if ed.Alt == nil && im.docPr != nil && im.docPr.Attr("descr") != "" {
			e.res.Notes = append(e.res.Notes, fmt.Sprintf("Picture %d of %s keeps its alternative text, which described the old picture: %q.", n, p.id(), oneLine(im.docPr.Attr("descr"), 120)))
		}
	} else {
		if ed.CX != nil {
			cx = *ed.CX
		}
		if ed.CY != nil {
			cy = *ed.CY
		}
		if cx <= 0 || cy <= 0 {
			return refuse("cx and cy must be positive")
		}
	}
	if cx != im.cx || cy != im.cy {
		if im.extent == nil {
			return refuse("picture %d of %s has no extent to resize", n, p.id())
		}
		spans = append(spans, setAttrSpan(data, im.extent, "cx", strconv.FormatInt(cx, 10)), setAttrSpan(data, im.extent, "cy", strconv.FormatInt(cy, 10)))
		if im.picExt != nil {
			spans = append(spans, setAttrSpan(data, im.picExt, "cx", strconv.FormatInt(cx, 10)), setAttrSpan(data, im.picExt, "cy", strconv.FormatInt(cy, 10)))
		}
		said = append(said, fmt.Sprintf("size %s → %s", emuText(im.cx, im.cy), emuText(cx, cy)))
	}
	if ed.Alt != nil {
		if im.docPr == nil {
			return refuse("picture %d of %s has no properties to hold alternative text", n, p.id())
		}
		spans = append(spans, setAttrSpan(data, im.docPr, "descr", *ed.Alt))
		said = append(said, fmt.Sprintf("alt text %q", oneLine(*ed.Alt, 80)))
	}
	e.touched[p] = true
	if !track {
		e.spans[p.story] = append(e.spans[p.story], spans...)
		e.res.Changes = append(e.res.Changes, fmt.Sprintf("Changed picture %d of %s: %s.", n, p.id(), strings.Join(said, "; ")))
		return nil
	}
	// Tracked: the run must hold the picture alone, so deleting it deletes nothing else.
	for _, k := range im.run.Kids {
		if k != im.drawing && k.Local != "rPr" {
			return refuse("picture %d of %s shares its run with text; a tracked replacement would delete that too. Pass track: false", n, p.id())
		}
	}
	for c := im.run.Parent; c != nil && c != p.node; c = c.Parent {
		if isW(c) && (c.Local == "ins" || c.Local == "del" || c.Local == "moveTo" || c.Local == "moveFrom") {
			return refuse("picture %d of %s lies inside a tracked change; resolve it first or pass track: false", n, p.id())
		}
	}
	if im.docPr != nil {
		spans = append(spans, setAttrSpan(data, im.docPr, "id", strconv.Itoa(e.newDocPrID())))
	}
	shifted := make([]Span, len(spans))
	for i, s := range spans {
		shifted[i] = Span{s.Start - im.run.Start, s.End - im.run.Start, s.Text}
	}
	old := im.run.Raw(data)
	nn := p.node
	out := e.changeOpen(nn, "del", authorOf(ed)) + string(old) + e.closeTag(nn, "del") + e.changeOpen(nn, "ins", authorOf(ed)) + string(Splice(old, shifted)) + e.closeTag(nn, "ins")
	e.spans[p.story] = append(e.spans[p.story], Span{im.run.Start, im.run.End, out})
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Replaced picture %d of %s as a tracked change: %s.", n, p.id(), strings.Join(said, "; ")))
	return nil
}

type docxDrop struct {
	story *docxStory
	ids   []string
}

func (e *docxEdit) dropRels() error {
	for _, m := range e.maybeDrop {
		cur, err := e.c.Current(e.d.pkg, m.story.part)
		if err != nil {
			return err
		}
		for _, id := range m.ids {
			if err := e.c.dropUnusedRel(e.d.pkg, m.story.part, cur, id); err != nil {
				return err
			}
		}
	}
	return nil
}
