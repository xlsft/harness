package documents

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultAuthor signs tracked changes and comments when the call names nobody.
const DefaultAuthor = "xlsft`s harness"

// nowFunc gives the date of tracked changes; tests set it.
var nowFunc = func() time.Time { return time.Now().UTC() }

// rep is one change inside a paragraph, in the coordinates of its flattened text. keep leaves the text in place and
// adds before and after around it (a comment's range).
type rep struct {
	a, b          int
	text          string
	keep          bool
	before, after string
	track         bool
	author        string
	firstRPr      string
	ins, del      [2]string
}

type docxEdit struct {
	d       *docxDoc
	res     *EditResult
	reps    map[*docxPara][]*rep
	spans   map[*docxStory][]Span
	nextID  int
	removed map[*docxPara]bool
	comment []string
	// extra part edits, applied after the stories.
	cmtSpans []Span
	touched  map[*docxPara]bool
}

var docxEditOps = "replace_text, set_text, insert_after, insert_before, delete_paragraph, add_comment, delete_comment, accept_change, reject_change"

// Apply implements Document. Every target is resolved against the document as it is, then all changes apply together.
func (d *docxDoc) Apply(edits []Edit) (*EditResult, error) {
	e := &docxEdit{d: d, res: &EditResult{}, reps: map[*docxPara][]*rep{}, spans: map[*docxStory][]Span{}, removed: map[*docxPara]bool{}, touched: map[*docxPara]bool{}, nextID: d.maxAnnotationID() + 1}
	for i, ed := range edits {
		var err error
		switch ed.Op {
		case "replace_text":
			err = e.replaceText(ed)
		case "set_text":
			err = e.setText(ed)
		case "insert_after", "insert_before":
			err = e.insert(ed)
		case "delete_paragraph":
			err = e.deleteParagraph(ed)
		case "add_comment":
			err = e.addComment(ed)
		case "delete_comment":
			err = e.deleteComment(ed)
		case "accept_change", "reject_change":
			err = e.resolveChange(ed)
		default:
			err = refuse("unknown operation %q for a Word document; use one of: %s", ed.Op, docxEditOps)
		}
		if err != nil {
			return nil, fmt.Errorf("edit %d (%s): %w", i+1, ed.Op, err)
		}
	}
	return e.finish()
}

func (d *docxDoc) maxAnnotationID() int {
	maxID := 0
	see := func(n *Node) bool {
		if isW(n) && n.HasAttr("id") {
			if changeKinds[n.Local] != "" || n.Local == "commentRangeStart" || n.Local == "bookmarkStart" || n.Local == "comment" {
				if v, err := strconv.Atoi(n.Attr("id")); err == nil && v > maxID {
					maxID = v
				}
			}
		}
		return true
	}
	for _, s := range d.stories {
		s.xml.Root.Walk(see)
	}
	if d.cmtXML != nil {
		d.cmtXML.Root.Walk(see)
	}
	return maxID
}

func (e *docxEdit) id() string {
	e.nextID++
	return strconv.Itoa(e.nextID)
}

func (p *docxPara) current() string {
	return fmt.Sprintf("%s #%s %s", p.id(), p.fp, oneLine(p.display, 200))
}

// resolve finds a paragraph from its address and checks the fingerprint.
func (e *docxEdit) resolve(ed Edit) (*docxPara, error) {
	m := docxRangeRe.FindStringSubmatch(ed.Target)
	if m == nil || m[3] != "" {
		return nil, refuse("target %q is not a paragraph address such as document:p3", ed.Target)
	}
	s := e.d.byName[m[1]]
	if s == nil {
		return nil, refuse("no story %q; stories: %s", m[1], strings.Join(e.d.storyNames(), ", "))
	}
	idx, _ := strconv.Atoi(m[2])
	var cur *docxPara
	if idx < len(s.paras) {
		cur = s.paras[idx]
	}
	if ed.Fingerprint == "" {
		if cur == nil {
			return nil, refuse("%s has %d paragraphs; %s does not exist", s.name, len(s.paras), ed.Target)
		}
		return nil, &RefusedError{Message: "the target needs its fingerprint, from structure, read or search. Current state:", Current: []string{cur.current()}}
	}
	if cur != nil && cur.fp == ed.Fingerprint {
		return cur, nil
	}
	var same []*docxPara
	for _, p := range s.paras {
		if p.fp == ed.Fingerprint {
			same = append(same, p)
		}
	}
	switch len(same) {
	case 1:
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("%s moved: it is now %s (same fingerprint).", ed.Target, same[0].id()))
		return same[0], nil
	case 0:
		r := &RefusedError{Message: fmt.Sprintf("%s no longer has fingerprint %s: the content changed. Nothing was written. Current state:", ed.Target, ed.Fingerprint)}
		if cur != nil {
			r.Current = []string{cur.current()}
		}
		return nil, r
	}
	r := &RefusedError{Message: fmt.Sprintf("fingerprint %s fits %d paragraphs, and %s is not one of them. Pick one:", ed.Fingerprint, len(same), ed.Target)}
	for _, p := range same {
		r.Current = append(r.Current, p.current())
	}
	return nil, r
}

func allIndexes(text, find string) []int {
	var out []int
	for at := 0; ; {
		i := strings.Index(text[at:], find)
		if i < 0 {
			return out
		}
		out = append(out, at+i)
		at += i + len(find)
	}
}

// replaceText replaces `find` in the target paragraph, or anywhere when no target is given. More than one match needs `all`.
func (e *docxEdit) replaceText(ed Edit) error {
	if ed.Find == "" {
		return refuse("replace_text needs find")
	}
	type hit struct {
		p  *docxPara
		at int
	}
	var hits []hit
	if ed.Target != "" {
		p, err := e.resolve(ed)
		if err != nil {
			return err
		}
		for _, i := range allIndexes(p.text, ed.Find) {
			hits = append(hits, hit{p, i})
		}
		if len(hits) == 0 {
			return &RefusedError{Message: fmt.Sprintf("%q is not in %s. Current state:", ed.Find, p.id()), Current: []string{p.current()}}
		}
	} else {
		for _, s := range e.d.stories {
			for _, p := range s.paras {
				for _, i := range allIndexes(p.text, ed.Find) {
					hits = append(hits, hit{p, i})
				}
			}
		}
		if len(hits) == 0 {
			return refuse("%q is not in the document's text (deleted text cannot be replaced)", ed.Find)
		}
	}
	if len(hits) > 1 && !ed.All {
		r := &RefusedError{Message: fmt.Sprintf("%q matches %d places. Give a target with its fingerprint, a longer find, or all: true. Candidates:", ed.Find, len(hits))}
		for i, h := range hits {
			if i == 20 {
				r.Current = append(r.Current, fmt.Sprintf("… %d more", len(hits)-20))
				break
			}
			r.Current = append(r.Current, fmt.Sprintf("%s #%s …%s…", h.p.id(), h.p.fp, oneLine(contextOf(h.p.text, h.at, len(ed.Find)), 120)))
		}
		return r
	}
	track := e.tracked(ed)
	for _, h := range hits {
		if err := e.addRep(h.p, &rep{a: h.at, b: h.at + len(ed.Find), text: ed.Text, track: track, author: authorOf(ed)}); err != nil {
			return err
		}
	}
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Replaced %d occurrence(s) of %q%s.", len(hits), oneLine(ed.Find, 60), trackedWord(track)))
	return nil
}

func contextOf(text string, at, n int) string {
	from := max(at-30, 0)
	to := min(at+n+30, len(text))
	for from > 0 && text[from]&0xC0 == 0x80 {
		from--
	}
	for to < len(text) && text[to]&0xC0 == 0x80 {
		to++
	}
	return text[from:to]
}

func trackedWord(t bool) string {
	if t {
		return " as a tracked change"
	}
	return ""
}

func authorOf(ed Edit) string {
	if ed.Author != "" {
		return ed.Author
	}
	return DefaultAuthor
}

func (e *docxEdit) tracked(ed Edit) bool {
	if ed.Track != nil {
		if *ed.Track != e.d.tracking {
			if *ed.Track {
				e.res.Notes = append(e.res.Notes, "Tracked as asked, though the document's own track-changes setting is off.")
			} else {
				e.res.Notes = append(e.res.Notes, "Not tracked, as asked, though the document has track changes ON.")
			}
		}
		return *ed.Track
	}
	return e.d.tracking
}

// setText replaces the whole text of a paragraph.
func (e *docxEdit) setText(ed Edit) error {
	p, err := e.resolve(ed)
	if err != nil {
		return err
	}
	track := e.tracked(ed)
	if p.objects > 0 {
		return &RefusedError{Message: fmt.Sprintf("%s holds a picture, note reference or other object that set_text would remove; use replace_text on the text around it. Current state:", p.id()), Current: []string{p.current()}}
	}
	if p.text == "" {
		// Nothing to cut: a new run goes at the end of the paragraph.
		e.touched[p] = true
		e.noteFallback(p)
		run := e.newRun(p, ed.Text, "", track, authorOf(ed))
		e.spans[p.story] = append(e.spans[p.story], insertKidSpan(p.story.xml.Data, p.node, run))
		e.res.Changes = append(e.res.Changes, fmt.Sprintf("Set the text of %s%s.", p.id(), trackedWord(track)))
		return nil
	}
	if err := e.addRep(p, &rep{a: 0, b: len(p.text), text: ed.Text, track: track, author: authorOf(ed)}); err != nil {
		return err
	}
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Set the text of %s%s.", p.id(), trackedWord(track)))
	return nil
}

// addRep checks a change against the paragraph's segments. The rule for mixed formatting lives here: the new text
// takes the formatting of the first run the match touches, and the report says when the matched runs differ.
func (e *docxEdit) addRep(p *docxPara, r *rep) error {
	if e.removed[p] {
		return refuse("%s is being deleted in this batch", p.id())
	}
	for _, o := range e.reps[p] {
		if r.a < o.b && o.a < r.b {
			return refuse("two edits of this batch overlap in %s", p.id())
		}
	}
	data := p.story.xml.Data
	var first *seg
	var rprs = map[string]bool{}
	for _, s := range p.segs {
		if s.end <= r.a || s.start >= r.b || s.start == s.end {
			continue
		}
		if s.kind == segObject {
			return &RefusedError{Message: fmt.Sprintf("the match in %s covers a picture, note reference or other object, which would be lost. Match only the text around it. Current state:", p.id()), Current: []string{p.current()}}
		}
		if s.inField {
			return &RefusedError{Message: fmt.Sprintf("the match in %s lies in a field's result (a table of contents, a date, a cross-reference), which Word regenerates. Edit the source instead. Current state:", p.id()), Current: []string{p.current()}}
		}
		if first == nil {
			first = s
		} else if s.container != first.container {
			return &RefusedError{Message: fmt.Sprintf("the match in %s crosses a hyperlink or tracked-change boundary. Match each side separately. Current state:", p.id()), Current: []string{p.current()}}
		}
		rprs[rawRPr(data, s.run)] = true
	}
	if first == nil {
		return refuse("the match in %s has no text", p.id())
	}
	if r.track && first.container != nil && (first.container.Local == "ins" || first.container.Local == "moveTo") {
		return &RefusedError{Message: fmt.Sprintf("the match in %s lies inside a tracked insertion. Accept or reject it first, or pass track: false. Current state:", p.id()), Current: []string{p.current()}}
	}
	r.firstRPr = rawRPr(data, first.run)
	if len(rprs) > 1 && !r.keep {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("Mixed formatting in %s: the matched text had %d different run formats; the new text took the first one's.", p.id(), len(rprs)))
	}
	if r.track {
		r.del = [2]string{e.changeOpen(p.node, "del", r.author), e.closeTag(p.node, "del")}
		r.ins = [2]string{e.changeOpen(p.node, "ins", r.author), e.closeTag(p.node, "ins")}
	}
	e.reps[p] = append(e.reps[p], r)
	e.touched[p] = true
	e.noteFallback(p)
	return nil
}

// noteFallback says when a text box's copy for old readers keeps the old text.
func (e *docxEdit) noteFallback(p *docxPara) {
	if p.node.HasAncestor("Choice") {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("%s is in a text box: its fallback copy for old readers keeps the old text. Current Word and LibreOffice show the new one.", p.id()))
	}
}

func rawRPr(data []byte, run *Node) string {
	if k := run.Kid("rPr"); k != nil {
		return string(k.Raw(data))
	}
	return ""
}

func (e *docxEdit) changeOpen(p *Node, kind, author string) string {
	return fmt.Sprintf(`<%s %s="%s" %s="%s" %s="%s">`, p.Tag(kind), p.Tag("id"), e.id(), p.Tag("author"), escAttr(author), p.Tag("date"), nowFunc().Format("2006-01-02T15:04:05Z"))
}

func (e *docxEdit) closeTag(p *Node, kind string) string { return "</" + p.Tag(kind) + ">" }

// runContent renders text as the content of a run: tabs and line breaks become their elements.
func runContent(p *Node, text, tTag string) string {
	var b strings.Builder
	t := p.Tag(tTag)
	flush := func(s string) {
		if s != "" {
			fmt.Fprintf(&b, `<%s xml:space="preserve">%s</%s>`, t, escText(s), t)
		}
	}
	start := 0
	for i, c := range text {
		switch c {
		case '\t':
			flush(text[start:i])
			b.WriteString("<" + p.Tag("tab") + "/>")
			start = i + 1
		case '\n':
			flush(text[start:i])
			b.WriteString("<" + p.Tag("br") + "/>")
			start = i + 1
		}
	}
	flush(text[start:])
	return b.String()
}

// newRun renders a new run, wrapped in an insertion when tracked.
func (e *docxEdit) newRun(p *docxPara, text, rPr string, track bool, author string) string {
	n := p.node
	if text == "" {
		return ""
	}
	r := "<" + n.Tag("r") + ">" + rPr + runContent(n, text, "t") + "</" + n.Tag("r") + ">"
	if track {
		return e.changeOpen(n, "ins", author) + r + e.closeTag(n, "ins")
	}
	return r
}

// rewriteRuns turns the paragraph's changes into spans: each touched run is rebuilt, with its attributes and properties,
// around what changed. Elements between runs (bookmarks, comment ranges) are not touched.
func (e *docxEdit) rewriteRuns(p *docxPara) []Span {
	reps := e.reps[p]
	sort.Slice(reps, func(i, j int) bool { return reps[i].a < reps[j].a })
	data := p.story.xml.Data
	bySeg := map[*Node]*seg{}
	touchedRuns := map[*Node]bool{}
	var order []*Node
	for _, s := range p.segs {
		bySeg[s.child] = s
		for _, r := range reps {
			if s.end > r.a && s.start < r.b {
				if !touchedRuns[s.run] {
					touchedRuns[s.run] = true
					order = append(order, s.run)
				}
			}
		}
	}
	var spans []Span
	n := p.node
	for _, run := range order {
		var out, frag strings.Builder
		rPr := rawRPr(data, run)
		open := string(startTag(data, run))
		if run.SelfClosed {
			open = strings.TrimSuffix(open, "/>") + ">"
		}
		flush := func() {
			if frag.Len() > 0 {
				out.WriteString(open + rPr + frag.String() + "</" + run.Tag("r") + ">")
				frag.Reset()
			}
		}
		piece := func(s *seg, x, y int, tag string) string {
			if s.kind == segText {
				return runContentRaw(n, s.text[x-s.start:y-s.start], tag)
			}
			return string(s.child.Raw(data))
		}
		for _, c := range run.Kids {
			if c.Local == "rPr" {
				continue
			}
			s := bySeg[c]
			if s == nil || s.start == s.end {
				frag.Write(c.Raw(data))
				continue
			}
			pos := s.start
			for _, r := range reps {
				if r.b <= s.start || r.a >= s.end {
					continue
				}
				if r.a > pos {
					frag.WriteString(piece(s, pos, r.a, "t"))
					pos = r.a
				}
				if r.a >= s.start && r.a < s.end { // it starts here
					flush()
					switch {
					case r.keep:
						out.WriteString(r.before)
					case !r.track && r.text != "":
						out.WriteString("<" + n.Tag("r") + ">" + r.firstRPr + runContent(n, r.text, "t") + "</" + n.Tag("r") + ">")
					}
				}
				y := min(r.b, s.end)
				switch {
				case r.keep:
					frag.WriteString(piece(s, pos, y, "t"))
				case r.track:
					out.WriteString(r.del[0] + open + rPr + piece(s, pos, y, "delText") + "</" + run.Tag("r") + ">" + r.del[1])
				}
				pos = y
				if r.b > s.start && r.b <= s.end { // it ends here
					if r.keep {
						flush()
						out.WriteString(r.after)
					} else if r.track && r.text != "" {
						out.WriteString(r.ins[0] + "<" + n.Tag("r") + ">" + r.firstRPr + runContent(n, r.text, "t") + "</" + n.Tag("r") + ">" + r.ins[1])
					}
				}
			}
			if pos < s.end {
				frag.WriteString(piece(s, pos, s.end, "t"))
			}
		}
		flush()
		spans = append(spans, Span{run.Start, run.End, out.String()})
	}
	return spans
}

func runContentRaw(p *Node, text, tag string) string {
	t := p.Tag(tag)
	return fmt.Sprintf(`<%s xml:space="preserve">%s</%s>`, t, escText(text), t)
}

// paragraphProps gives what a new paragraph next to p takes: its properties, without a section break, and the formatting of its first run.
func paragraphProps(p *docxPara) (pPr, rPr string) {
	data := p.story.xml.Data
	if k := p.node.Kid("pPr"); k != nil {
		pPr = string(k.Raw(data))
		if sect := k.Kid("sectPr"); sect != nil {
			pPr = string(data[k.Start:sect.Start]) + string(data[sect.End:k.End])
		}
		if pc := k.Kid("pPrChange"); pc != nil {
			pPr = strings.Replace(pPr, string(pc.Raw(data)), "", 1)
		}
	}
	for _, s := range p.segs {
		if s.kind == segText {
			return pPr, rawRPr(data, s.run)
		}
	}
	return pPr, ""
}

func (e *docxEdit) styleID(name string) (string, error) {
	if _, ok := e.d.styles[name]; ok {
		return name, nil
	}
	var names []string
	for id, st := range e.d.styles {
		if strings.EqualFold(st.name, name) {
			return id, nil
		}
		names = append(names, st.name)
	}
	sort.Strings(names)
	if len(names) > 30 {
		names = names[:30]
	}
	return "", refuse("the document has no style %q. Styles: %s", name, strings.Join(names, ", "))
}

// insert adds paragraphs before or after the target. They take its properties and first run's formatting, or `style`.
func (e *docxEdit) insert(ed Edit) error {
	p, err := e.resolve(ed)
	if err != nil {
		return err
	}
	track := e.tracked(ed)
	pPr, rPr := paragraphProps(p)
	n := p.node
	if ed.Style != "" {
		id, err := e.styleID(ed.Style)
		if err != nil {
			return err
		}
		pPr = fmt.Sprintf(`<%s><%s %s="%s"/></%s>`, n.Tag("pPr"), n.Tag("pStyle"), n.Tag("val"), escAttr(id), n.Tag("pPr"))
		rPr = ""
	}
	var b strings.Builder
	for _, line := range strings.Split(ed.Text, "\n") {
		b.WriteString("<" + n.Tag("p") + ">")
		props := pPr
		if track {
			props = e.markParagraph(n, props, "ins", authorOf(ed))
		}
		b.WriteString(props)
		b.WriteString(e.newRun(p, line, rPr, track, authorOf(ed)))
		b.WriteString("</" + n.Tag("p") + ">")
	}
	at := n.End
	if ed.Op == "insert_before" {
		at = n.Start
	}
	e.spans[p.story] = append(e.spans[p.story], Span{at, at, b.String()})
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Inserted %d paragraph(s) %s %s%s.", strings.Count(ed.Text, "\n")+1, strings.TrimPrefix(ed.Op, "insert_"), p.id(), trackedWord(track)))
	return nil
}

// markParagraph records the paragraph mark as inserted or deleted in a paragraph's properties.
func (e *docxEdit) markParagraph(n *Node, pPr, kind, author string) string {
	mark := fmt.Sprintf(`<%s %s="%s" %s="%s" %s="%s"/>`, n.Tag(kind), n.Tag("id"), e.id(), n.Tag("author"), escAttr(author), n.Tag("date"), nowFunc().Format("2006-01-02T15:04:05Z"))
	rPrTag, pPrTag := n.Tag("rPr"), n.Tag("pPr")
	if pPr == "" {
		return "<" + pPrTag + "><" + rPrTag + ">" + mark + "</" + rPrTag + "></" + pPrTag + ">"
	}
	if i := strings.Index(pPr, "<"+rPrTag+">"); i >= 0 {
		at := i + len(rPrTag) + 2
		return pPr[:at] + mark + pPr[at:]
	}
	closing := "</" + pPrTag + ">"
	if strings.HasSuffix(strings.TrimSpace(pPr), "/>") {
		return strings.TrimSuffix(strings.TrimSpace(pPr), "/>") + "><" + rPrTag + ">" + mark + "</" + rPrTag + ">" + closing
	}
	return strings.TrimSuffix(pPr, closing) + "<" + rPrTag + ">" + mark + "</" + rPrTag + ">" + closing
}

func (e *docxEdit) deleteParagraph(ed Edit) error {
	p, err := e.resolve(ed)
	if err != nil {
		return err
	}
	if p.node.Kid("pPr").kidOrNil("sectPr") != nil {
		return refuse("%s ends a section: deleting it would merge the sections", p.id())
	}
	track := e.tracked(ed)
	if track {
		if p.objects > 0 {
			return refuse("%s holds an object; tracked deletion of objects is not supported, pass track: false or delete the text around it", p.id())
		}
		if p.text != "" {
			if err := e.addRep(p, &rep{a: 0, b: len(p.text), track: true, author: authorOf(ed)}); err != nil {
				return err
			}
		}
		data := p.story.xml.Data
		if k := p.node.Kid("pPr"); k != nil {
			e.spans[p.story] = append(e.spans[p.story], Span{k.Start, k.End, e.markParagraph(p.node, string(k.Raw(data)), "del", authorOf(ed))})
		} else {
			at := p.node.InnerStart
			if p.node.SelfClosed {
				return refuse("%s is an empty self-closed paragraph; pass track: false to delete it", p.id())
			}
			e.spans[p.story] = append(e.spans[p.story], Span{at, at, e.markParagraph(p.node, "", "del", authorOf(ed))})
		}
		e.res.Changes = append(e.res.Changes, fmt.Sprintf("Deleted %s as a tracked change.", p.id()))
		return nil
	}
	// A cell, a note, and the body need a paragraph left over.
	parent := p.node.Parent
	blocks := 0
	for _, k := range parent.Kids {
		if isW(k) && (k.Local == "p" || k.Local == "tbl" || k.Local == "sdt") {
			blocks++
		}
	}
	if blocks <= 1 {
		return refuse("%s is the only block of its %s, which must keep one; use set_text with empty text", p.id(), parent.Local)
	}
	if parent.Local == "tc" && parent.Kids[len(parent.Kids)-1] == p.node {
		last := parent.Kids[len(parent.Kids)-2]
		if last.Local != "p" {
			return refuse("%s is what ends its table cell after a nested table; a cell must end with a paragraph", p.id())
		}
	}
	if e.touched[p] {
		return refuse("another edit of this batch changes %s", p.id())
	}
	if p.objects > 0 {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("%s held an object (picture, note reference), which went with it.", p.id()))
	}
	e.removed[p] = true
	e.spans[p.story] = append(e.spans[p.story], Span{p.node.Start, p.node.End, ""})
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Deleted %s: %s", p.id(), oneLine(p.text, 80)))
	return nil
}

func (e *docxEdit) addComment(ed Edit) error {
	p, err := e.resolve(ed)
	if err != nil {
		return err
	}
	if ed.Text == "" {
		return refuse("add_comment needs text")
	}
	id := e.id()
	n := p.node
	start := fmt.Sprintf(`<%s %s="%s"/>`, n.Tag("commentRangeStart"), n.Tag("id"), id)
	end := fmt.Sprintf(`<%s %s="%s"/><%s><%s %s="%s"/></%s>`, n.Tag("commentRangeEnd"), n.Tag("id"), id, n.Tag("r"), n.Tag("commentReference"), n.Tag("id"), id, n.Tag("r"))
	if ed.Find != "" {
		hits := allIndexes(p.text, ed.Find)
		if len(hits) != 1 {
			return &RefusedError{Message: fmt.Sprintf("the anchor %q matches %d places in %s; it must match one. Current state:", ed.Find, len(hits), p.id()), Current: []string{p.current()}}
		}
		if err := e.addRep(p, &rep{a: hits[0], b: hits[0] + len(ed.Find), keep: true, before: start, after: end}); err != nil {
			return err
		}
	} else {
		data := p.story.xml.Data
		if n.SelfClosed {
			e.spans[p.story] = append(e.spans[p.story], insertKidSpan(data, n, start+end))
		} else {
			at := n.InnerStart
			if k := n.Kid("pPr"); k != nil {
				at = k.End
			}
			e.spans[p.story] = append(e.spans[p.story], Span{at, at, start}, Span{n.InnerEnd, n.InnerEnd, end})
		}
	}
	e.comment = append(e.comment, fmt.Sprintf(`<%s %s="%s" %s="%s" %s="%s" %s="%s">%s</%s>`, n.Tag("comment"), n.Tag("id"), id, n.Tag("author"), escAttr(authorOf(ed)), n.Tag("date"), nowFunc().Format("2006-01-02T15:04:05Z"), n.Tag("initials"), "AI",
		"<"+n.Tag("p")+"><"+n.Tag("r")+">"+runContent(n, ed.Text, "t")+"</"+n.Tag("r")+"></"+n.Tag("p")+">", n.Tag("comment")))
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Added comment:%s on %s.", id, p.id()))
	return nil
}

var commentTargetRe = regexp.MustCompile(`^comment:(\S+)$`)

func (e *docxEdit) deleteComment(ed Edit) error {
	m := commentTargetRe.FindStringSubmatch(ed.Target)
	if m == nil {
		return refuse("target %q is not a comment address such as comment:3", ed.Target)
	}
	var c *docxComment
	for _, x := range e.d.comments {
		if x.id == m[1] {
			c = x
		}
	}
	if c == nil {
		return refuse("no comment %s", m[1])
	}
	e.cmtSpans = append(e.cmtSpans, Span{c.node.Start, c.node.End, ""})
	for _, s := range e.d.stories {
		data := s.xml.Data
		s.xml.Root.Walk(func(n *Node) bool {
			if !isW(n) || n.Attr("id") != c.id {
				return true
			}
			switch n.Local {
			case "commentRangeStart", "commentRangeEnd":
				e.spans[s] = append(e.spans[s], Span{n.Start, n.End, ""})
			case "commentReference":
				run := n.Parent
				only := run != nil && run.Local == "r"
				if only {
					for _, k := range run.Kids {
						if k != n && k.Local != "rPr" {
							only = false
						}
					}
				}
				if only {
					e.spans[s] = append(e.spans[s], Span{run.Start, run.End, ""})
				} else {
					e.spans[s] = append(e.spans[s], Span{n.Start, n.End, ""})
				}
			}
			return true
		})
		_ = data
	}
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Deleted comment:%s by %s.", c.id, c.author))
	return nil
}

// resolveChange accepts or rejects one tracked change, or all of them. What cannot be done safely (merging paragraphs
// whose marks are tracked but whose content stays, restoring old formatting, moves) is refused and named.
func (e *docxEdit) resolveChange(ed Edit) error {
	accept := ed.Op == "accept_change"
	var targets []*docxChange
	if ed.All {
		targets = e.d.changes
		if len(targets) == 0 {
			return refuse("the document has no tracked changes")
		}
	} else {
		id := strings.TrimPrefix(ed.Target, "change:")
		for _, c := range e.d.changes {
			if c.id == id {
				targets = append(targets, c)
			}
		}
		switch len(targets) {
		case 0:
			return refuse("no tracked change %q; read range \"changes\" lists them", ed.Target)
		case 1:
		default:
			r := &RefusedError{Message: fmt.Sprintf("the id %s is used by %d tracked changes. Use all: true or fix the document in Word. Candidates:", id, len(targets))}
			for _, c := range targets {
				r.Current = append(r.Current, e.d.changeLine(c))
			}
			return r
		}
	}
	covered := func(n *Node, removed map[*Node]bool) bool {
		for c := n.Parent; c != nil; c = c.Parent {
			if removed[c] {
				return true
			}
		}
		return false
	}
	removed := map[*Node]bool{}
	type pending struct{ fn func() }
	var todo []pending
	for _, c := range targets {
		c := c
		n := c.node
		data := c.story.xml.Data
		removeWhole := func() {
			removed[n] = true
			todo = append(todo, pending{func() {
				if !covered(n, removed) {
					e.spans[c.story] = append(e.spans[c.story], Span{n.Start, n.End, ""})
				}
			}})
		}
		unwrap := func(convertDelText bool) {
			todo = append(todo, pending{func() {
				if covered(n, removed) {
					return
				}
				if n.SelfClosed {
					e.spans[c.story] = append(e.spans[c.story], Span{n.Start, n.End, ""})
					return
				}
				e.spans[c.story] = append(e.spans[c.story], Span{n.Start, n.InnerStart, ""}, Span{n.InnerEnd, n.End, ""})
				if !convertDelText {
					return
				}
				n.Walk(func(k *Node) bool {
					if isW(k) && (k.Local == "delText" || k.Local == "delInstrText") {
						newName := map[string]string{"delText": "t", "delInstrText": "instrText"}[k.Local]
						if k.SelfClosed {
							return true
						}
						open := string(startTag(data, k))
						open = strings.Replace(open, k.Tag(k.Local), k.Tag(newName), 1)
						e.spans[c.story] = append(e.spans[c.story], Span{k.Start, k.InnerStart, open}, Span{k.InnerEnd, k.End, "</" + k.Tag(newName) + ">"})
					}
					return true
				})
			}})
		}
		switch n.Local {
		case "moveFrom", "moveTo":
			return refuse("change:%s is a tracked move; resolve moves in Word", c.id)
		case "ins", "del":
			inRowProps := n.Parent != nil && n.Parent.Local == "trPr"
			if inRowProps {
				return refuse("change:%s tracks a table row; resolve it in Word", c.id)
			}
			isMark := n.Parent != nil && n.Parent.Local == "rPr" && n.Parent.Parent != nil && n.Parent.Parent.Local == "pPr"
			if isMark {
				gone := (accept && n.Local == "del") || (!accept && n.Local == "ins")
				if !gone {
					removeWhole()
					break
				}
				// The paragraph's mark goes. Fine when its whole content goes too; otherwise two paragraphs would merge.
				para := c.para
				if para == nil || !paragraphContentGoes(para, accept) {
					return refuse("change:%s removes the paragraph mark of %s, whose text stays, so paragraphs would merge; resolve it in Word", c.id, c.story.name)
				}
				pn := para.node
				removed[pn] = true
				todo = append(todo, pending{func() { e.spans[c.story] = append(e.spans[c.story], Span{pn.Start, pn.End, ""}) }})
				break
			}
			if (accept && n.Local == "ins") || (!accept && n.Local == "del") {
				unwrap(n.Local == "del")
			} else {
				removeWhole()
			}
		default:
			if !accept {
				return refuse("change:%s is a %s change; rejecting it means restoring old properties, so do it in Word", c.id, c.kind)
			}
			removeWhole()
		}
	}
	for _, t := range todo {
		t.fn()
	}
	verb := map[bool]string{true: "Accepted", false: "Rejected"}[accept]
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("%s %d tracked change(s).", verb, len(targets)))
	return nil
}

// paragraphContentGoes reports whether all of a paragraph's runs are inside tracked changes that resolving removes.
func paragraphContentGoes(p *docxPara, accept bool) bool {
	for _, k := range p.node.Kids {
		if !isW(k) {
			continue
		}
		switch k.Local {
		case "pPr", "bookmarkStart", "bookmarkEnd", "proofErr", "commentRangeStart", "commentRangeEnd":
		case "ins":
			if accept {
				return false
			}
		case "del":
			if !accept {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (e *docxEdit) finish() (*EditResult, error) {
	c := NewChanges()
	for p := range e.reps {
		e.spans[p.story] = append(e.spans[p.story], e.rewriteRuns(p)...)
	}
	for s, spans := range e.spans {
		if spansOverlap(spans) {
			return nil, refuse("the edits of this batch overlap in %s; send them in separate calls", s.name)
		}
		c.Set[s.part] = Splice(s.xml.Data, spans)
	}
	if len(e.comment) > 0 || len(e.cmtSpans) > 0 {
		if err := e.writeComments(c); err != nil {
			return nil, err
		}
	}
	data, err := e.d.pkg.Write(c)
	if err != nil {
		return nil, err
	}
	e.res.Data = data
	return e.res, nil
}

func (e *docxEdit) writeComments(c *Changes) error {
	d := e.d
	add := strings.Join(e.comment, "")
	if d.cmtXML != nil {
		spans := append([]Span(nil), e.cmtSpans...)
		if add != "" {
			spans = append(spans, insertKidSpan(d.cmtXML.Data, d.cmtXML.Root, add))
		}
		c.Set[d.cmtPart] = Splice(d.cmtXML.Data, spans)
		return nil
	}
	root := d.stories[0].xml.Root
	ns := root.resolve(root.Prefix)
	part := "word/comments.xml"
	prefix := root.Prefix
	if prefix != "" {
		prefix = ":" + prefix
	}
	c.Set[part] = []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" + fmt.Sprintf(`<%s xmlns%s="%s">%s</%s>`, root.Tag("comments"), prefix, ns, add, root.Tag("comments")))
	if err := c.addContentType(d.pkg, part, "application/vnd.openxmlformats-officedocument.wordprocessingml.comments+xml"); err != nil {
		return err
	}
	relType := "http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments"
	if ns == wordNSStrict {
		relType = "http://purl.oclc.org/ooxml/officeDocument/relationships/comments"
	}
	_, err := c.addRelationship(d.pkg, d.mainPart, relType, relTargetFrom(d.mainPart, part), false)
	return err
}

// relTargetFrom writes part relative to from's folder, or absolute when it is elsewhere.
func relTargetFrom(from, part string) string {
	dir := from[:strings.LastIndex(from, "/")+1]
	if strings.HasPrefix(part, dir) {
		return strings.TrimPrefix(part, dir)
	}
	return "/" + part
}
