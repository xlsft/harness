package documents

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// A Word document is a set of story parts: the body, headers, footers, footnotes and endnotes. Each holds paragraphs,
// inside tables and text boxes too. Every paragraph is flattened to the text a reader sees: insertions shown, deletions
// hidden. That view is what addresses, search and edits refer to, and the structure step says so.

const (
	wordNSTransitional = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	wordNSStrict       = "http://purl.oclc.org/ooxml/wordprocessingml/main"
	objectChar         = "￼"
)

func isW(n *Node) bool { return n.NS == wordNSTransitional || n.NS == wordNSStrict }

type segKind int

const (
	segText segKind = iota
	segChar
	segObject
)

// seg is a stretch of a paragraph's flattened text and the run content it came from.
type seg struct {
	run, child *Node
	// container is the element around the run when it is not the paragraph: a hyperlink, an insertion, a field.
	container  *Node
	start, end int
	kind       segKind
	inField    bool
	text       string
}

type docxPara struct {
	story *docxStory
	index int
	node  *Node
	fp    string
	// text is the view addresses refer to; display also marks tracked changes and hidden text.
	text, display string
	segs          []*seg
	runs          []*Node
	styleID       string
	level         int
	ctx           string
	hidden        bool
	deleted       string
	objects       int
}

func (p *docxPara) id() string { return fmt.Sprintf("%s:p%d", p.story.name, p.index) }

type docxStory struct {
	name, part string
	xml        *XMLDoc
	paras      []*docxPara
	state      *fieldState
}

type docxChange struct {
	id     string
	kind   string
	author string
	date   string
	node   *Node
	story  *docxStory
	para   *docxPara
	text   string
	mark   bool // it marks a paragraph mark or a property, not text
}

type docxComment struct {
	id, author, text string
	node             *Node
	anchored         bool
	resolved         bool
}

type docxField struct {
	instr string
	para  *docxPara
	// result is the field's cached text.
	result string
}

type docxDoc struct {
	pkg      *Package
	mainPart string
	stories  []*docxStory
	byName   map[string]*docxStory
	tracking bool
	updateF  bool
	styles   map[string]docxStyle
	changes  []*docxChange
	comments []*docxComment
	fields   []*docxField
	cmtPart  string
	cmtXML   *XMLDoc
	sections int
	tables   int
	images   int
}

type docxStyle struct {
	name  string
	level int
}

func openDOCX(data []byte) (Document, error) {
	pkg, err := OpenPackage(data)
	if err != nil {
		return nil, err
	}
	main, err := pkg.officeDocumentPart()
	if err != nil {
		return nil, err
	}
	d := &docxDoc{pkg: pkg, mainPart: main, byName: map[string]*docxStory{}, styles: map[string]docxStyle{}}
	if err := d.loadStory("document", main); err != nil {
		return nil, err
	}
	rels, err := pkg.Rels(main)
	if err != nil {
		return nil, err
	}
	for _, r := range rels {
		t := ResolveTarget(main, r.Target)
		if r.External {
			continue
		}
		switch {
		case strings.HasSuffix(r.Type, "/styles"):
			d.loadStyles(t)
		case strings.HasSuffix(r.Type, "/settings"):
			d.loadSettings(t)
		case strings.HasSuffix(r.Type, "/comments"):
			d.cmtPart = t
		}
	}
	sort.Slice(rels, func(i, j int) bool { return rels[i].Target < rels[j].Target })
	for _, r := range rels {
		if r.External {
			continue
		}
		for _, kind := range []string{"header", "footer", "footnotes", "endnotes"} {
			if strings.HasSuffix(r.Type, "/"+kind) {
				part := ResolveTarget(main, r.Target)
				name := strings.TrimSuffix(path.Base(part), path.Ext(part))
				if err := d.loadStory(name, part); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, s := range d.stories {
		d.scanParas(s)
	}
	if err := d.loadComments(); err != nil {
		return nil, err
	}
	d.scanChanges()
	d.sections = len(d.stories[0].xml.Root.Find("sectPr"))
	d.tables = len(d.stories[0].xml.Root.Find("tbl"))
	d.images = len(d.stories[0].xml.Root.Find("drawing")) + len(d.stories[0].xml.Root.Find("pict"))
	return d, nil
}

func (d *docxDoc) loadStory(name, part string) error {
	x, err := d.pkg.XML(part)
	if err != nil {
		return err
	}
	s := &docxStory{name: name, part: part, xml: x}
	d.stories = append(d.stories, s)
	d.byName[name] = s
	return nil
}

func (d *docxDoc) loadStyles(part string) {
	x, err := d.pkg.XML(part)
	if err != nil {
		return
	}
	for _, s := range x.Root.KidsNamed("style") {
		st := docxStyle{name: s.Kid("name").attrOrEmpty("val")}
		low := strings.ToLower(st.name)
		if strings.HasPrefix(low, "heading ") {
			st.level, _ = strconv.Atoi(strings.TrimPrefix(low, "heading "))
		}
		if o := s.Kid("pPr").kidOrNil("outlineLvl"); o != nil && st.level == 0 {
			if v, err := strconv.Atoi(o.Attr("val")); err == nil && v < 9 {
				st.level = v + 1
			}
		}
		d.styles[s.Attr("styleId")] = st
	}
}

func (n *Node) attrOrEmpty(local string) string {
	if n == nil {
		return ""
	}
	return n.Attr(local)
}

func (n *Node) kidOrNil(local string) *Node {
	if n == nil {
		return nil
	}
	return n.Kid(local)
}

func (d *docxDoc) loadSettings(part string) {
	x, err := d.pkg.XML(part)
	if err != nil {
		return
	}
	if t := x.Root.Kid("trackRevisions"); t != nil && onOff(t) {
		d.tracking = true
	}
	if t := x.Root.Kid("updateFields"); t != nil && onOff(t) {
		d.updateF = true
	}
}

// onOff reads a CT_OnOff element: present means on unless val says otherwise.
func onOff(n *Node) bool {
	switch n.Attr("val") {
	case "0", "false", "off":
		return false
	}
	return true
}

// scanParas finds every paragraph of a story, tables and text boxes included. mc:Fallback holds a second copy of the
// same content for old readers, so it gets no addresses.
func (d *docxDoc) scanParas(s *docxStory) {
	var walk func(n *Node, ctx string)
	var tableCount int
	walk = func(n *Node, ctx string) {
		for _, k := range n.Kids {
			switch {
			case k.Local == "Fallback":
				continue
			case isW(k) && k.Local == "p":
				p := &docxPara{story: s, index: len(s.paras), node: k, ctx: ctx, fp: Fingerprint(k.Raw(s.xml.Data))}
				s.paras = append(s.paras, p)
				d.flatten(p)
				// Text boxes inside the paragraph have their own paragraphs.
				k.Walk(func(c *Node) bool {
					if c == k {
						return true
					}
					if c.Local == "Fallback" {
						return false
					}
					if isW(c) && c.Local == "txbxContent" {
						walk(c, "textbox")
						return false
					}
					return true
				})
			case isW(k) && k.Local == "tbl":
				t := tableCount
				tableCount++
				for ri, tr := range k.KidsNamed("tr") {
					for ci, tc := range tr.KidsNamed("tc") {
						cellCtx := fmt.Sprintf("t%d r%d c%d", t, ri, ci)
						if ctx != "" {
							cellCtx = ctx + " " + cellCtx
						}
						walk(tc, cellCtx)
					}
				}
			case isW(k) && (k.Local == "footnote" || k.Local == "endnote"):
				if tp := k.Attr("type"); tp != "" && tp != "normal" {
					continue
				}
				walk(k, k.Local+" "+k.Attr("id"))
			default:
				walk(k, ctx)
			}
		}
	}
	walk(s.xml.Root, "")
}

type fieldState struct {
	depth int
	cur   *docxField
	sep   bool
}

func (d *docxDoc) flatten(p *docxPara) {
	var text, display strings.Builder
	if ppr := p.node.Kid("pPr"); ppr != nil {
		if st := ppr.Kid("pStyle"); st != nil {
			p.styleID = st.Attr("val")
		}
	}
	p.level = d.styles[p.styleID].level
	if p.node.Kid("pPr").kidOrNil("outlineLvl") != nil {
		if v, err := strconv.Atoi(p.node.Kid("pPr").Kid("outlineLvl").Attr("val")); err == nil && v < 9 {
			p.level = v + 1
		}
	}
	st := d.fieldStateFor(p.story)
	var deleted strings.Builder

	var runFn func(run, container *Node, inSimple bool, mark string)
	runFn = func(run, container *Node, inSimple bool, mark string) {
		p.runs = append(p.runs, run)
		vanish := run.Kid("rPr").kidOrNil("vanish") != nil
		for _, c := range run.Kids {
			if !isW(c) {
				p.addSeg(&text, &display, &seg{run: run, child: c, container: container, kind: segObject, text: objectChar, inField: inSimple || st.sep})
				p.objects++
				continue
			}
			inField := inSimple || st.depth > 0
			switch c.Local {
			case "rPr":
			case "t":
				if st.depth > 0 && st.sep && st.cur != nil {
					st.cur.result += c.Text
				}
				s := &seg{run: run, child: c, container: container, kind: segText, text: c.Text, inField: inField}
				if vanish {
					p.hidden = true
					display.WriteString("[hidden:")
				}
				if mark != "" {
					display.WriteString("[" + mark)
				}
				p.addSeg(&text, &display, s)
				if mark != "" {
					display.WriteString(mark + "]")
				}
				if vanish {
					display.WriteString("]")
				}
			case "tab", "ptab":
				p.addSeg(&text, &display, &seg{run: run, child: c, container: container, kind: segChar, text: "\t", inField: inField})
			case "br", "cr":
				p.addSeg(&text, &display, &seg{run: run, child: c, container: container, kind: segChar, text: "\n", inField: inField})
			case "noBreakHyphen":
				p.addSeg(&text, &display, &seg{run: run, child: c, container: container, kind: segChar, text: "‑", inField: inField})
			case "fldChar":
				switch c.Attr("fldCharType") {
				case "begin":
					st.depth++
					if st.depth == 1 {
						st.cur = &docxField{para: p}
						st.sep = false
						d.fields = append(d.fields, st.cur)
					}
				case "separate":
					if st.depth == 1 {
						st.sep = true
					}
				case "end":
					st.depth--
					if st.depth <= 0 {
						st.depth, st.cur, st.sep = 0, nil, false
					}
				}
			case "instrText":
				if st.cur != nil {
					st.cur.instr += c.Text
				}
			case "softHyphen", "lastRenderedPageBreak", "commentReference", "annotationRef", "footnoteRef", "endnoteRef", "separator", "continuationSeparator":
			default: // a drawing, a picture, a symbol, a note reference: something that is not text
				p.addSeg(&text, &display, &seg{run: run, child: c, container: container, kind: segObject, text: objectChar, inField: inField})
				p.objects++
			}
		}
	}

	var walk func(n, container *Node, inSimple bool, mark string)
	walk = func(n, container *Node, inSimple bool, mark string) {
		for _, k := range n.Kids {
			if !isW(k) {
				continue // an mc:AlternateContent at paragraph level has no text of its own here
			}
			switch k.Local {
			case "r":
				runFn(k, container, inSimple, mark)
			case "ins", "moveTo":
				walk(k, k, inSimple, "+")
			case "del", "moveFrom":
				t := delTextOf(k)
				deleted.WriteString(t)
				if t != "" {
					display.WriteString("[-" + t + "-]")
				}
			case "fldSimple":
				f := &docxField{instr: k.Attr("instr"), para: p}
				before := text.Len()
				walk(k, k, true, mark)
				f.result = text.String()[before:]
				d.fields = append(d.fields, f)
			case "hyperlink", "smartTag", "customXml", "sdtContent", "dir", "bdo":
				walk(k, k, inSimple, mark)
			case "sdt":
				walk(k, k, inSimple, mark)
			}
		}
	}
	walk(p.node, nil, false, "")
	p.text, p.display, p.deleted = text.String(), display.String(), deleted.String()
}

func delTextOf(n *Node) string {
	var b strings.Builder
	n.Walk(func(c *Node) bool {
		if c.Local == "delText" || c.Local == "t" {
			b.WriteString(c.Text)
		}
		return true
	})
	return b.String()
}

func (p *docxPara) addSeg(text, display *strings.Builder, s *seg) {
	s.start = text.Len()
	text.WriteString(s.text)
	s.end = text.Len()
	display.WriteString(s.text)
	p.segs = append(p.segs, s)
}

// fieldStateFor keeps the state of an open complex field across paragraphs of a story (a table of contents spans many).
func (d *docxDoc) fieldStateFor(s *docxStory) *fieldState {
	if s.state == nil {
		s.state = &fieldState{}
	}
	return s.state
}

func (d *docxDoc) loadComments() error {
	if d.cmtPart == "" || !d.pkg.Has(d.cmtPart) {
		return nil
	}
	x, err := d.pkg.XML(d.cmtPart)
	if err != nil {
		return err
	}
	d.cmtXML = x
	anchored := map[string]bool{}
	for _, s := range d.stories {
		for _, n := range s.xml.Root.Find("commentReference") {
			anchored[n.Attr("id")] = true
		}
	}
	for _, c := range x.Root.KidsNamed("comment") {
		var b strings.Builder
		for _, t := range c.Find("t") {
			b.WriteString(t.Text)
		}
		d.comments = append(d.comments, &docxComment{id: c.Attr("id"), author: c.Attr("author"), text: b.String(), node: c, anchored: anchored[c.Attr("id")]})
	}
	return nil
}

var changeKinds = map[string]string{
	"ins": "insertion", "del": "deletion", "moveFrom": "move from", "moveTo": "move to",
	"rPrChange": "formatting", "pPrChange": "paragraph formatting", "sectPrChange": "section formatting", "tblPrChange": "table formatting",
	"tcPrChange": "cell formatting", "trPrChange": "row formatting", "tblGridChange": "table grid change", "numberingChange": "numbering", "cellIns": "cell insertion", "cellDel": "cell deletion", "cellMerge": "cell merge",
}

func (d *docxDoc) scanChanges() {
	for _, s := range d.stories {
		paraOf := func(n *Node) *docxPara {
			for c := n; c != nil; c = c.Parent {
				if isW(c) && c.Local == "p" {
					for _, p := range s.paras {
						if p.node == c {
							return p
						}
					}
				}
			}
			return nil
		}
		s.xml.Root.Walk(func(n *Node) bool {
			if !isW(n) {
				return n.Local != "Fallback"
			}
			kind, ok := changeKinds[n.Local]
			if !ok || !n.HasAttr("author") {
				return true
			}
			ch := &docxChange{id: n.Attr("id"), kind: kind, author: n.Attr("author"), date: n.Attr("date"), node: n, story: s, para: paraOf(n)}
			ch.mark = len(n.Kids) == 0 || strings.HasSuffix(n.Local, "Change")
			if !ch.mark {
				ch.text = delTextOf(n)
			}
			d.changes = append(d.changes, ch)
			return true
		})
	}
}

func (d *docxDoc) Format() Format { return FormatDOCX }

func (d *docxDoc) viewNote() string {
	return "View: addresses and text show insertions and hide deletions; [+x+] marks inserted, [-x-] deleted (deleted text cannot be addressed)."
}

// Structure implements Document.
func (d *docxDoc) Structure() string {
	var b strings.Builder
	total := 0
	for _, s := range d.stories {
		total += len(s.paras)
	}
	fmt.Fprintf(&b, "Word document: %d paragraphs, %d tables, %d images, %d sections, %d tracked changes, %d comments.\n", total, d.tables, d.images, d.sections, len(d.changes), len(d.comments))
	if d.tracking {
		b.WriteString("Track changes is ON in this document: edits are recorded as tracked changes unless the call says track: false.\n")
	} else {
		b.WriteString("Track changes is off in this document: edits are direct unless the call says track: true.\n")
	}
	b.WriteString(d.viewNote() + "\n")
	b.WriteString("Stories:\n")
	for _, s := range d.stories {
		fmt.Fprintf(&b, "  %s (%s): %d paragraphs\n", s.name, s.part, len(s.paras))
	}
	b.WriteString("Headings:\n")
	any := false
	for _, s := range d.stories {
		for _, p := range s.paras {
			if p.level > 0 && p.ctx == "" {
				any = true
				fmt.Fprintf(&b, "  %s%s #%s %s\n", strings.Repeat("  ", p.level-1), p.id(), p.fp, oneLine(p.text, 120))
			}
		}
	}
	if !any {
		b.WriteString("  (none)\n")
	}
	hidden := 0
	for _, s := range d.stories {
		for _, p := range s.paras {
			if p.hidden {
				if hidden == 0 {
					b.WriteString("Hidden text:\n")
				}
				hidden++
				if hidden <= 20 {
					fmt.Fprintf(&b, "  %s #%s %s\n", p.id(), p.fp, oneLine(p.display, 120))
				}
			}
		}
	}
	if len(d.changes) > 0 {
		b.WriteString("Tracked changes:\n")
		for i, c := range d.changes {
			if i == 50 {
				fmt.Fprintf(&b, "  … %d more; read range \"changes\"\n", len(d.changes)-50)
				break
			}
			b.WriteString("  " + d.changeLine(c) + "\n")
		}
	}
	if len(d.comments) > 0 {
		b.WriteString("Comments:\n")
		for i, c := range d.comments {
			if i == 50 {
				fmt.Fprintf(&b, "  … %d more; read range \"comments\"\n", len(d.comments)-50)
				break
			}
			fmt.Fprintf(&b, "  comment:%s by %s: %s\n", c.id, c.author, oneLine(c.text, 120))
		}
	}
	for _, f := range d.staleFields() {
		b.WriteString("Stale: " + f.Message + "\n")
	}
	return clip(b.String())
}

func (d *docxDoc) changeLine(c *docxChange) string {
	where := c.story.name
	if c.para != nil {
		where = c.para.id()
	}
	t := ""
	if c.text != "" {
		t = ": " + oneLine(c.text, 80)
	}
	return fmt.Sprintf("change:%s %s by %s at %s%s", c.id, c.kind, c.author, where, t)
}

func (p *docxPara) line() string {
	label := ""
	if p.styleID != "" {
		label = " [" + p.styleID + "]"
	}
	if p.ctx != "" {
		label += " [" + p.ctx + "]"
	}
	t := p.display
	if t == "" {
		t = "(empty)"
	}
	return fmt.Sprintf("%s #%s%s %s", p.id(), p.fp, label, strings.ReplaceAll(t, "\n", "⏎"))
}

var docxRangeRe = regexp.MustCompile(`^([^:]+):p(\d+)(?:-p?(\d+))?$`)

// Read implements Document. The range is a story, "story:pN", "story:pN-pM", "comments" or "changes".
func (d *docxDoc) Read(sel Selector) (string, error) {
	var lines []string
	rng := strings.TrimSpace(sel.Range)
	switch {
	case rng == "comments":
		for _, c := range d.comments {
			lines = append(lines, fmt.Sprintf("comment:%s by %s%s: %s", c.id, c.author, map[bool]string{true: "", false: " (no anchor)"}[c.anchored], c.text))
		}
	case rng == "changes":
		for _, c := range d.changes {
			lines = append(lines, d.changeLine(c))
		}
	case rng == "":
		for _, s := range d.stories {
			for _, p := range s.paras {
				lines = append(lines, p.line())
			}
		}
	default:
		if m := docxRangeRe.FindStringSubmatch(rng); m != nil {
			s := d.byName[m[1]]
			if s == nil {
				return "", fmt.Errorf("no story %q; stories: %s", m[1], strings.Join(d.storyNames(), ", "))
			}
			from, _ := strconv.Atoi(m[2])
			to := from
			if m[3] != "" {
				to, _ = strconv.Atoi(m[3])
			}
			if from >= len(s.paras) {
				return "", fmt.Errorf("%s has %d paragraphs, numbered from p0", s.name, len(s.paras))
			}
			for i := from; i <= to && i < len(s.paras); i++ {
				lines = append(lines, s.paras[i].line())
			}
		} else if s := d.byName[rng]; s != nil {
			for _, p := range s.paras {
				lines = append(lines, p.line())
			}
		} else {
			return "", fmt.Errorf("range %q: use a story (%s), story:pN, story:pN-pM, comments or changes", rng, strings.Join(d.storyNames(), ", "))
		}
	}
	return pageLines(d.viewNote(), lines, sel), nil
}

func (d *docxDoc) storyNames() []string {
	out := make([]string, len(d.stories))
	for i, s := range d.stories {
		out[i] = s.name
	}
	return out
}

// pageLines applies offset and limit to lines and says how much there is.
func pageLines(head string, lines []string, sel Selector) string {
	limit := sel.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	first := max(sel.Offset, 0)
	if first > len(lines) {
		first = len(lines)
	}
	end := min(first+limit, len(lines))
	var b strings.Builder
	if head != "" {
		b.WriteString(head + "\n")
	}
	fmt.Fprintf(&b, "Items %d-%d of %d.\n", min(first+1, end), end, len(lines))
	for _, l := range lines[first:end] {
		b.WriteString(l + "\n")
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "[%d more: pass offset %d]\n", len(lines)-end, end)
	}
	return clip(b.String())
}

// Search implements Document: case-insensitive, over the view, deleted text and comments.
func (d *docxDoc) Search(query string, limit int) string {
	if limit <= 0 {
		limit = DefaultLimit
	}
	q := strings.ToLower(query)
	var hits []string
	for _, s := range d.stories {
		for _, p := range s.paras {
			if containsFold(p.text, q) {
				hits = append(hits, fmt.Sprintf("%s #%s %s", p.id(), p.fp, snippetAround(p.text, q)))
			}
			if containsFold(p.deleted, q) {
				hits = append(hits, fmt.Sprintf("%s #%s (deleted text) %s", p.id(), p.fp, snippetAround(p.deleted, q)))
			}
		}
	}
	for _, c := range d.comments {
		if containsFold(c.text, q) {
			hits = append(hits, fmt.Sprintf("comment:%s by %s: %s", c.id, c.author, snippetAround(c.text, q)))
		}
	}
	return searchReport(query, hits, limit)
}

func searchReport(query string, hits []string, limit int) string {
	if len(hits) == 0 {
		return fmt.Sprintf("No match for %q.", query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d matches for %q.\n", len(hits), query)
	for i, h := range hits {
		if i == limit {
			fmt.Fprintf(&b, "[%d more]\n", len(hits)-limit)
			break
		}
		b.WriteString(h + "\n")
	}
	return clip(b.String())
}

func snippetAround(text, lowerQuery string) string {
	i := strings.Index(strings.ToLower(text), lowerQuery)
	if i < 0 || len(text) < 160 {
		return oneLine(text, 160)
	}
	from := max(i-60, 0)
	to := min(i+len(lowerQuery)+60, len(text))
	for from > 0 && text[from]&0xC0 == 0x80 {
		from--
	}
	for to < len(text) && text[to]&0xC0 == 0x80 {
		to++
	}
	return "…" + oneLine(text[from:to], 200) + "…"
}

// staleFields compares each table of contents with the headings it should list.
func (d *docxDoc) staleFields() []Finding {
	var out []Finding
	for _, f := range d.fields {
		instr := strings.TrimSpace(f.instr)
		if !strings.HasPrefix(strings.ToUpper(instr), "TOC") {
			continue
		}
		var missing []string
		toc := strings.ToLower(f.result)
		for _, p := range d.stories[0].paras {
			if p.level == 0 || p.level > 3 || p.ctx != "" || strings.TrimSpace(p.text) == "" {
				continue
			}
			if !strings.Contains(toc, strings.ToLower(strings.TrimSpace(p.text))) {
				missing = append(missing, oneLine(p.text, 60))
			}
		}
		if len(missing) > 0 {
			out = append(out, Finding{SeverityWarning, "docx:toc-stale", fmt.Sprintf("the table of contents does not list %d heading(s), such as %q: it updates when the user refreshes fields in Word", len(missing), missing[0])})
		}
	}
	return out
}

// Check implements Document.
func (d *docxDoc) Check() []Finding {
	out := d.pkg.checkPackage()
	bad := func(key, format string, args ...any) {
		out = append(out, Finding{SeverityError, "docx:" + key, fmt.Sprintf(format, args...)})
	}
	ids := map[string]bool{}
	for _, c := range d.comments {
		ids[c.id] = true
	}
	for _, s := range d.stories {
		root := s.xml.Root
		for _, tc := range root.Find("tc") {
			if len(tc.Kids) == 0 || tc.Kids[len(tc.Kids)-1].Local != "p" {
				bad("cell-end:"+s.name, "a table cell in %s does not end with a paragraph", s.name)
			}
		}
		for _, tr := range root.Find("tr") {
			if tr.Kid("tc") == nil && tr.Kid("sdt") == nil {
				bad("row-empty:"+s.name, "a table row in %s has no cells", s.name)
			}
		}
		starts := map[string]int{}
		ends := map[string]int{}
		root.Walk(func(n *Node) bool {
			if !isW(n) {
				return true
			}
			switch n.Local {
			case "commentRangeStart":
				starts["c"+n.Attr("id")]++
			case "commentRangeEnd":
				ends["c"+n.Attr("id")]++
			case "bookmarkStart":
				starts["b"+n.Attr("id")]++
			case "bookmarkEnd":
				ends["b"+n.Attr("id")]++
			case "commentReference":
				if !ids[n.Attr("id")] {
					bad("comment-ref:"+n.Attr("id"), "%s refers to comment %s, which does not exist", s.name, n.Attr("id"))
				}
			}
			return true
		})
		for k, n := range starts {
			if ends[k] != n {
				bad("range:"+s.name+":"+k, "%s has an unpaired comment or bookmark range (%s)", s.name, k)
			}
		}
		out = append(out, checkRelRefs(d.pkg, s.part, s.xml)...)
	}
	dupes := map[string]int{}
	for _, c := range d.changes {
		dupes[c.id]++
	}
	for id, n := range dupes {
		if n > 1 {
			out = append(out, Finding{SeverityWarning, "docx:change-id:" + id, fmt.Sprintf("the tracked change id %s is used %d times", id, n)})
		}
	}
	return append(out, d.staleFields()...)
}

// checkRelRefs reports r:id style attributes that name a relationship the part does not have.
func checkRelRefs(pkg *Package, part string, x *XMLDoc) []Finding {
	rels, err := pkg.Rels(part)
	if err != nil {
		return nil
	}
	have := map[string]bool{}
	for _, r := range rels {
		have[r.ID] = true
	}
	var out []Finding
	x.Root.Walk(func(n *Node) bool {
		for _, a := range n.Attrs {
			if a.Name.Space == "" || a.Name.Space == "xmlns" || a.Name.Space == "xml" {
				continue
			}
			if !strings.HasSuffix(n.resolve(a.Name.Space), "/officeDocument/2006/relationships") && !strings.HasSuffix(n.resolve(a.Name.Space), "/officeDocument/relationships") {
				continue
			}
			if a.Value != "" && !have[a.Value] {
				out = append(out, Finding{SeverityError, "package:rel-ref:" + part + ":" + a.Value, fmt.Sprintf("%s refers to relationship %s, which it does not have", part, a.Value)})
			}
		}
		return true
	})
	return out
}

// Project implements Document.
func (d *docxDoc) Project() []string {
	var lines []string
	for _, s := range d.stories {
		for _, p := range s.paras {
			label := s.name
			if p.styleID != "" {
				label += " " + p.styleID
			}
			if p.ctx != "" {
				label += " " + p.ctx
			}
			if p.display == "" && p.ctx == "" {
				continue
			}
			lines = append(lines, "["+label+"] "+strings.ReplaceAll(p.display, "\n", "⏎"))
		}
	}
	for _, c := range d.comments {
		lines = append(lines, fmt.Sprintf("[comment by %s] %s", c.author, oneLine(c.text, 2000)))
	}
	return lines
}
