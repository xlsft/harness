package documents

import (
	"fmt"
	"strconv"
	"strings"
)

// Pictures on slides: add_image puts a p:pic on a slide; replace_image swaps the picture of an existing p:pic, or
// changes its size and alternative text. A new picture always gets a new media part, so a picture other slides share
// never changes with it.

// newShapeID gives the next free cNvPr id on a slide, counting the shapes this batch added.
func (e *pptxEdit) newShapeID(s *pslide) int {
	if e.shapeIDs == nil {
		e.shapeIDs = map[*pslide]int{}
	}
	n, ok := e.shapeIDs[s]
	if !ok {
		n = 1
		for _, sh := range s.shapes {
			if v, err := strconv.Atoi(sh.id); err == nil && v > n {
				n = v
			}
		}
		// cNvPr ids also sit in places that are not shapes (graphic frames' content, for one): count them all.
		for _, c := range s.x.Root.Find("cNvPr") {
			if v, err := strconv.Atoi(c.Attr("id")); err == nil && v > n {
				n = v
			}
		}
	}
	n++
	e.shapeIDs[s] = n
	return n
}

func (d *pptxDoc) strict() bool {
	return d.pres.Root.NS == "http://purl.oclc.org/ooxml/presentationml/main"
}

func picXML(p, a string, id int, name, alt, relID string, g rect, strict bool) string {
	r, decl := relDecl(strict)
	return fmt.Sprintf(`<%[1]s:pic><%[1]s:nvPicPr><%[1]s:cNvPr id="%[3]d" name="%[4]s" descr="%[5]s"/><%[1]s:cNvPicPr><%[2]s:picLocks noChangeAspect="1"/></%[1]s:cNvPicPr><%[1]s:nvPr/></%[1]s:nvPicPr><%[1]s:blipFill><%[2]s:blip%[6]s %[7]s:embed="%[8]s"/><%[2]s:stretch><%[2]s:fillRect/></%[2]s:stretch></%[1]s:blipFill><%[1]s:spPr><%[2]s:xfrm><%[2]s:off x="%[9]d" y="%[10]d"/><%[2]s:ext cx="%[11]d" cy="%[12]d"/></%[2]s:xfrm><%[2]s:prstGeom prst="rect"><%[2]s:avLst/></%[2]s:prstGeom></%[1]s:spPr></%[1]s:pic>`,
		p, a, id, escAttr(name), escAttr(alt), decl, r, relID, g.x, g.y, g.cx, g.cy)
}

// addImage puts a picture on a slide: target the slide; x, y default to centred, cx, cy to the picture's own size
// shrunk to fit the slide.
func (e *pptxEdit) addImage(ed Edit) error {
	if !pptxSlideRe.MatchString(ed.Target) {
		return refuse("target %q must be a slide, such as s256", ed.Target)
	}
	s := e.d.byID[strings.TrimPrefix(ed.Target, "s")]
	if s == nil {
		return refuse("no slide %s", ed.Target)
	}
	if e.deleted[s] {
		return refuse("slide %s is being deleted in this batch", ed.Target)
	}
	pic, err := loadPicture(ed.ImageData)
	if err != nil {
		return err
	}
	a, ok := drawingPrefix(s.x.Root)
	if !ok {
		return refuse("slide %s does not declare the DrawingML namespace at its root", ed.Target)
	}
	maxW, maxH := e.d.width, e.d.height
	if ed.X != nil {
		maxW -= *ed.X
	}
	if ed.Y != nil {
		maxH -= *ed.Y
	}
	if maxW <= 0 || maxH <= 0 {
		maxW, maxH = e.d.width, e.d.height
	}
	cx, cy, err := pic.sizeEMU(ed.CX, ed.CY, maxW, maxH)
	if err != nil {
		return err
	}
	g := rect{x: (e.d.width - cx) / 2, y: (e.d.height - cy) / 2, cx: cx, cy: cy}
	if ed.X != nil {
		g.x = *ed.X
	}
	if ed.Y != nil {
		g.y = *ed.Y
	}
	relID, part, err := e.c.addMediaPart(e.d.pkg, s.part, mediaDir(s.part), pic, e.d.strict())
	if err != nil {
		return err
	}
	id := e.newShapeID(s)
	alt := ""
	if ed.Alt != nil {
		alt = *ed.Alt
	} else {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("s%s/sp%d has no alternative text; pass alt to describe it, or alt \"\" if it is decorative.", s.id, id))
	}
	tree := s.x.Root.Kid("cSld").Kid("spTree")
	at := tree.InnerEnd
	if ext := tree.Kid("extLst"); ext != nil {
		at = ext.Start
	}
	e.addSpan(s, Span{at, at, picXML(s.x.Root.Prefix, a, id, fmt.Sprintf("Picture %d", id), alt, relID, g, e.d.strict())})
	if g.x < 0 || g.y < 0 || g.x+g.cx > e.d.width || g.y+g.cy > e.d.height {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("s%s/sp%d reaches past the slide's edge.", s.id, id))
	}
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Added picture s%s/sp%d (%s, %d×%d px) at %s, stored as %s.", s.id, id, strings.ToUpper(pic.format), pic.w, pic.h, g, part))
	return nil
}

// replaceImage swaps a picture shape's picture (fitted, centred, into the old frame unless cx or cy say otherwise),
// and sets its size or alternative text.
func (e *pptxEdit) replaceImage(ed Edit) error {
	sh, err := e.shape(ed)
	if err != nil {
		return err
	}
	if sh.kind != "pic" {
		return refuse("%s is a %s, not a picture", sh.addr(), sh.kind)
	}
	blip := sh.node.Kid("blipFill").kidOrNil("blip")
	if blip == nil {
		return refuse("%s has no picture fill to replace", sh.addr())
	}
	if ed.ImageData == nil && ed.Alt == nil && ed.CX == nil && ed.CY == nil {
		return refuse("replace_image needs image, alt, cx or cy")
	}
	data := sh.slide.x.Data
	var spans []Span
	var said []string
	g := sh.geom
	if ed.ImageData != nil {
		pic, err := loadPicture(ed.ImageData)
		if err != nil {
			return err
		}
		relID, part, err := e.c.addMediaPart(e.d.pkg, sh.slide.part, mediaDir(sh.slide.part), pic, e.d.strict())
		if err != nil {
			return err
		}
		spans = append(spans, Span{blip.Start, blip.End, newBlip(data, blip, relID, e.d.strict())})
		if src := sh.node.Kid("blipFill").Kid("srcRect"); src != nil {
			spans = append(spans, Span{src.Start, src.End, ""})
		}
		e.maybeDrop = append(e.maybeDrop, droppedRels{sh.slide, relIDsIn(blip)})
		if g.ok && ed.CX == nil && ed.CY == nil {
			g = pic.fitBox(g)
		} else if g.ok {
			cx, cy, err := pic.sizeEMU(ed.CX, ed.CY, 0, 0)
			if err != nil {
				return err
			}
			g.cx, g.cy = cx, cy
		}
		said = append(said, fmt.Sprintf("picture → %s (%s, %d×%d px)", part, strings.ToUpper(pic.format), pic.w, pic.h))
		if ed.Alt == nil && sh.alt != "" {
			e.res.Notes = append(e.res.Notes, fmt.Sprintf("%s keeps its alternative text, which described the old picture: %q.", sh.addr(), oneLine(sh.alt, 120)))
		}
	} else if g.ok && (ed.CX != nil || ed.CY != nil) {
		// Without a new picture, the frame's own shape gives the ratio.
		if ed.CX != nil {
			g.cx = *ed.CX
		}
		if ed.CY != nil {
			g.cy = *ed.CY
		}
		if g.cx <= 0 || g.cy <= 0 {
			return refuse("cx and cy must be positive")
		}
	}
	if g != sh.geom {
		x := sh.node.Kid("spPr").kidOrNil("xfrm")
		if x == nil || x.Kid("off") == nil || x.Kid("ext") == nil {
			e.res.Notes = append(e.res.Notes, fmt.Sprintf("%s takes its position from its layout, so its frame was left as it is.", sh.addr()))
		} else {
			spans = append(spans, setAttrSpan(data, x.Kid("off"), "x", strconv.FormatInt(g.x, 10)), setAttrSpan(data, x.Kid("off"), "y", strconv.FormatInt(g.y, 10)),
				setAttrSpan(data, x.Kid("ext"), "cx", strconv.FormatInt(g.cx, 10)), setAttrSpan(data, x.Kid("ext"), "cy", strconv.FormatInt(g.cy, 10)))
			said = append(said, fmt.Sprintf("frame %s → %s", sh.geom, g))
		}
	}
	if ed.Alt != nil {
		c := sh.node.Kids[0].Kid("cNvPr")
		if c == nil {
			return refuse("%s has no properties to hold alternative text", sh.addr())
		}
		spans = append(spans, setAttrSpan(data, c, "descr", *ed.Alt))
		said = append(said, fmt.Sprintf("alt text %q", oneLine(*ed.Alt, 80)))
	}
	e.addSpan(sh.slide, spans...)
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Changed picture %s: %s.", sh.addr(), strings.Join(said, "; ")))
	return nil
}

type droppedRels struct {
	slide *pslide
	ids   []string
}

// dropRels removes the relationships a replaced picture no longer uses, and its media when nothing else does.
func (e *pptxEdit) dropRels() error {
	for _, m := range e.maybeDrop {
		cur, err := e.c.Current(e.d.pkg, m.slide.part)
		if err != nil {
			return err
		}
		for _, id := range m.ids {
			if err := e.c.dropUnusedRel(e.d.pkg, m.slide.part, cur, id); err != nil {
				return err
			}
		}
	}
	return nil
}
