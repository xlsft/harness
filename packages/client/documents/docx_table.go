package documents

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Table rows in Word documents (ECMA-376 Part 1, §17.4). A row is addressed as story:tN/rM: the table's ordinal in its
// story (nested tables count, in document order) and the row's in its table, with the row's fingerprint. Rows are
// inserted by copying the target row's shape (row and cell properties, the first paragraph's properties and the first
// run's format), so new rows look like their neighbours. Vertical merges are refused where a row would join or split
// one: that is a layout decision for Word.

type docxTable struct {
	story *docxStory
	index int
	node  *Node
	rows  []*docxRow
}

type docxRow struct {
	table *docxTable
	index int
	node  *Node
	fp    string
}

func (t *docxTable) id() string { return fmt.Sprintf("%s:t%d", t.story.name, t.index) }
func (r *docxRow) id() string   { return fmt.Sprintf("%s/r%d", r.table.id(), r.index) }

var (
	docxRowRe   = regexp.MustCompile(`^([^:]+):t(\d+)/r(\d+)$`)
	docxTableRe = regexp.MustCompile(`^([^:]+):t(\d+)$`)
)

// cells returns each cell's text, paragraphs joined by a line break.
func (d *docxDoc) cellTexts(r *docxRow) []string {
	var out []string
	for _, tc := range r.node.KidsNamed("tc") {
		var parts []string
		for _, p := range r.table.story.paras {
			for c := p.node.Parent; c != nil; c = c.Parent {
				if c == tc {
					parts = append(parts, p.display)
					break
				}
				if c.Local == "tbl" { // a nested table's paragraphs belong to its own rows
					break
				}
			}
		}
		out = append(out, strings.Join(parts, "⏎"))
	}
	return out
}

func (d *docxDoc) rowLine(r *docxRow) string {
	cells := d.cellTexts(r)
	for i, c := range cells {
		cells[i] = oneLine(c, 60)
	}
	flags := ""
	if r.node.Kid("trPr").kidOrNil("tblHeader") != nil {
		flags += " [header]"
	}
	if r.node.Kid("trPr").kidOrNil("ins") != nil {
		flags += " [inserted]"
	}
	if r.node.Kid("trPr").kidOrNil("del") != nil {
		flags += " [deleted]"
	}
	return fmt.Sprintf("%s #%s%s | %s |", r.id(), r.fp, flags, strings.Join(cells, " | "))
}

func (d *docxDoc) tableLines() []string {
	var lines []string
	for _, s := range d.stories {
		for _, t := range s.tables {
			cols := 0
			if len(t.rows) > 0 {
				cols = len(t.rows[0].node.KidsNamed("tc"))
			}
			first := ""
			if len(t.rows) > 0 {
				first = oneLine(strings.Join(d.cellTexts(t.rows[0]), " | "), 100)
			}
			lines = append(lines, fmt.Sprintf("%s: %d rows × %d cells, first row: %s", t.id(), len(t.rows), cols, first))
		}
	}
	return lines
}

func (d *docxDoc) table(story, idx string) (*docxTable, error) {
	s := d.byName[story]
	if s == nil {
		return nil, refuse("no story %q; stories: %s", story, strings.Join(d.storyNames(), ", "))
	}
	i, _ := strconv.Atoi(idx)
	if i >= len(s.tables) {
		return nil, refuse("%s has %d table(s), numbered from t0", s.name, len(s.tables))
	}
	return s.tables[i], nil
}

// resolveRow finds a row from its address and checks its fingerprint; a row that moved within its table is followed.
func (e *docxEdit) resolveRow(ed Edit) (*docxRow, error) {
	m := docxRowRe.FindStringSubmatch(ed.Target)
	if m == nil {
		return nil, refuse("target %q is not a row address such as document:t0/r2", ed.Target)
	}
	t, err := e.d.table(m[1], m[2])
	if err != nil {
		return nil, err
	}
	ri, _ := strconv.Atoi(m[3])
	var cur *docxRow
	if ri < len(t.rows) {
		cur = t.rows[ri]
	}
	if ed.Fingerprint == "" {
		if cur == nil {
			return nil, refuse("%s has %d rows; %s does not exist", t.id(), len(t.rows), ed.Target)
		}
		return nil, &RefusedError{Message: "the target needs its fingerprint, from read. Current state:", Current: []string{e.d.rowLine(cur)}}
	}
	if cur != nil && cur.fp == ed.Fingerprint {
		return cur, nil
	}
	var same []*docxRow
	for _, r := range t.rows {
		if r.fp == ed.Fingerprint {
			same = append(same, r)
		}
	}
	if len(same) == 1 {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("%s moved: it is now %s (same fingerprint).", ed.Target, same[0].id()))
		return same[0], nil
	}
	r := &RefusedError{Message: fmt.Sprintf("%s no longer has fingerprint %s: the row changed. Nothing was written. Current state:", ed.Target, ed.Fingerprint)}
	if cur != nil {
		r.Current = []string{e.d.rowLine(cur)}
	}
	return nil, r
}

// continuesMerge reports whether a row has a cell that continues a vertical merge from the row above.
func continuesMerge(r *Node) bool {
	for _, tc := range r.KidsNamed("tc") {
		if v := tc.Kid("tcPr").kidOrNil("vMerge"); v != nil && v.Attr("val") != "restart" {
			return true
		}
	}
	return false
}

func hasMerge(r *Node) bool {
	for _, tc := range r.KidsNamed("tc") {
		if tc.Kid("tcPr").kidOrNil("vMerge") != nil {
			return true
		}
	}
	return false
}

// trackMarkRe finds a paragraph mark's own insertion or deletion mark, which a copied paragraph must not take along.
var trackMarkRe = regexp.MustCompile(`<(\w+:)?(ins|del)\b[^>]*/>`)

// withoutKids returns an element's bytes with some children left out.
func withoutKids(data []byte, n *Node, drop ...string) string {
	if n == nil {
		return ""
	}
	skip := map[string]bool{}
	for _, d := range drop {
		skip[d] = true
	}
	var spans []Span
	for _, k := range n.Kids {
		if skip[k.Local] {
			spans = append(spans, Span{k.Start - n.Start, k.End - n.Start, ""})
		}
	}
	return string(Splice(n.Raw(data), spans))
}

// newRowsXML builds rows shaped like tmpl, one per line of text, cells separated by tabs.
func (e *docxEdit) newRowsXML(tmpl *docxRow, text string, track bool, author string) (string, int, error) {
	s := tmpl.table.story
	data := s.xml.Data
	n := tmpl.node
	cells := tmpl.node.KidsNamed("tc")
	if len(cells) == 0 {
		return "", 0, refuse("%s has no cells to copy", tmpl.id())
	}
	lines := strings.Split(text, "\n")
	var b strings.Builder
	for _, line := range lines {
		values := strings.Split(line, "\t")
		if len(values) > len(cells) {
			return "", 0, refuse("a new row has %d values but %s has %d cells", len(values), tmpl.id(), len(cells))
		}
		b.WriteString("<" + n.Tag("tr") + ">")
		if ex := n.Kid("tblPrEx"); ex != nil {
			b.Write(ex.Raw(data))
		}
		trPr := withoutKids(data, n.Kid("trPr"), "ins", "del", "trPrChange", "tblHeader", "cnfStyle")
		if track {
			mark := fmt.Sprintf(`<%s %s="%s" %s="%s" %s="%s"/>`, n.Tag("ins"), n.Tag("id"), e.id(), n.Tag("author"), escAttr(author), n.Tag("date"), nowFunc().Format("2006-01-02T15:04:05Z"))
			if trPr == "" {
				trPr = "<" + n.Tag("trPr") + ">" + mark + "</" + n.Tag("trPr") + ">"
			} else if strings.HasSuffix(trPr, "/>") {
				trPr = strings.TrimSuffix(trPr, "/>") + ">" + mark + "</" + n.Tag("trPr") + ">"
			} else {
				trPr = strings.TrimSuffix(trPr, "</"+n.Tag("trPr")+">") + mark + "</" + n.Tag("trPr") + ">"
			}
		}
		b.WriteString(trPr)
		for ci, tc := range cells {
			b.WriteString("<" + n.Tag("tc") + ">")
			b.WriteString(withoutKids(data, tc.Kid("tcPr"), "vMerge", "cellIns", "cellDel", "cellMerge", "tcPrChange", "cnfStyle"))
			pPr, rPr := "", ""
			for _, p := range s.paras {
				if p.node.Parent == tc {
					pPr, rPr = paragraphProps(p)
					break
				}
			}
			pPr = trackMarkRe.ReplaceAllString(pPr, "")
			if track {
				pPr = e.markParagraph(n, pPr, "ins", author)
			}
			v := ""
			if ci < len(values) {
				v = values[ci]
			}
			b.WriteString("<" + n.Tag("p") + ">" + pPr)
			if v != "" {
				r := "<" + n.Tag("r") + ">" + rPr + runContent(n, v, "t") + "</" + n.Tag("r") + ">"
				if track {
					r = e.changeOpen(n, "ins", author) + r + e.closeTag(n, "ins")
				}
				b.WriteString(r)
			}
			b.WriteString("</" + n.Tag("p") + "></" + n.Tag("tc") + ">")
		}
		b.WriteString("</" + n.Tag("tr") + ">")
	}
	return b.String(), len(lines), nil
}

// insertRows adds rows before or after the target row.
func (e *docxEdit) insertRows(ed Edit) error {
	r, err := e.resolveRow(ed)
	if err != nil {
		return err
	}
	t := r.table
	after := ed.Op == "insert_after"
	// The row that will follow the new ones must not continue a merge, or it would join them.
	var follower *docxRow
	if after {
		if r.index+1 < len(t.rows) {
			follower = t.rows[r.index+1]
		}
	} else {
		follower = r
	}
	if follower != nil && continuesMerge(follower.node) {
		return refuse("%s continues a vertically merged cell from the row above, so new rows there would split the merge; do it in Word", follower.id())
	}
	track := e.tracked(ed)
	xml, count, err := e.newRowsXML(r, ed.Text, track, authorOf(ed))
	if err != nil {
		return err
	}
	at := r.node.End
	if !after {
		at = r.node.Start
	}
	e.spans[t.story] = append(e.spans[t.story], Span{at, at, xml})
	where := "after"
	if !after {
		where = "before"
	}
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Inserted %d row(s) %s %s%s.", count, where, r.id(), trackedWord(track)))
	return nil
}

func (e *docxEdit) deleteRow(ed Edit) error {
	r, err := e.resolveRow(ed)
	if err != nil {
		return err
	}
	t := r.table
	if len(t.rows) <= 1 {
		return refuse("%s is the only row of %s; a table needs one", r.id(), t.id())
	}
	if next := r.index + 1; next < len(t.rows) && continuesMerge(t.rows[next].node) && hasMerge(r.node) {
		return refuse("%s holds the top of a vertically merged cell that continues below; do it in Word", r.id())
	}
	if e.removedRows[r] {
		return refuse("%s is already deleted in this batch", r.id())
	}
	e.removedRows[r] = true
	data := t.story.xml.Data
	if !e.tracked(ed) {
		e.spans[t.story] = append(e.spans[t.story], Span{r.node.Start, r.node.End, ""})
		e.res.Changes = append(e.res.Changes, fmt.Sprintf("Deleted %s: %s", r.id(), oneLine(strings.Join(e.d.cellTexts(r), " | "), 80)))
		return nil
	}
	n := r.node
	trPr := n.Kid("trPr")
	if trPr.kidOrNil("del") != nil {
		return refuse("%s is already marked deleted", r.id())
	}
	mark := fmt.Sprintf(`<%s %s="%s" %s="%s" %s="%s"/>`, n.Tag("del"), n.Tag("id"), e.id(), n.Tag("author"), escAttr(authorOf(ed)), n.Tag("date"), nowFunc().Format("2006-01-02T15:04:05Z"))
	switch {
	case trPr == nil:
		at := n.InnerStart
		if ex := n.Kid("tblPrEx"); ex != nil {
			at = ex.End
		}
		e.spans[t.story] = append(e.spans[t.story], Span{at, at, "<" + n.Tag("trPr") + ">" + mark + "</" + n.Tag("trPr") + ">"})
	case trPr.Kid("trPrChange") != nil:
		at := trPr.Kid("trPrChange").Start
		e.spans[t.story] = append(e.spans[t.story], Span{at, at, mark})
	default:
		e.spans[t.story] = append(e.spans[t.story], insertKidSpan(data, trPr, mark))
	}
	// The text of the row's cells goes as tracked deletions too.
	for _, p := range t.story.paras {
		inRow := false
		for c := p.node.Parent; c != nil; c = c.Parent {
			if c == n {
				inRow = true
				break
			}
		}
		if !inRow || p.text == "" {
			continue
		}
		if p.objects > 0 {
			return refuse("%s holds a picture or other object; a tracked row deletion cannot carry it. Pass track: false", r.id())
		}
		if err := e.addRep(p, &rep{a: 0, b: len(p.text), track: true, author: authorOf(ed)}); err != nil {
			return err
		}
	}
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Deleted %s as a tracked change.", r.id()))
	return nil
}

// moveRow moves a row to a 1-based position in its table. Moves are not tracked.
func (e *docxEdit) moveRow(ed Edit) error {
	r, err := e.resolveRow(ed)
	if err != nil {
		return err
	}
	t := r.table
	if ed.Track != nil && *ed.Track || ed.Track == nil && e.d.tracking {
		return refuse("moving a row cannot be recorded as a tracked change; pass track: false to move it directly")
	}
	if ed.Position < 1 || ed.Position > len(t.rows) {
		return refuse("position must be 1 to %d, the rows of %s", len(t.rows), t.id())
	}
	if ed.Position-1 == r.index {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("%s is already at position %d.", r.id(), ed.Position))
		return nil
	}
	if hasMerge(r.node) {
		return refuse("%s takes part in a vertically merged cell; move it in Word", r.id())
	}
	others := make([]*docxRow, 0, len(t.rows)-1)
	for _, x := range t.rows {
		if x != r {
			others = append(others, x)
		}
	}
	at := t.rows[len(t.rows)-1].node.End
	if ed.Position-1 < len(others) {
		dest := others[ed.Position-1]
		at = dest.node.Start
		if continuesMerge(dest.node) {
			return refuse("%s continues a vertically merged cell; a row cannot go between it and the row above", dest.id())
		}
	}
	if e.removedRows[r] {
		return refuse("%s is being deleted in this batch", r.id())
	}
	e.removedRows[r] = true
	data := t.story.xml.Data
	e.spans[t.story] = append(e.spans[t.story], Span{r.node.Start, r.node.End, ""}, Span{at, at, string(r.node.Raw(data))})
	e.res.Changes = append(e.res.Changes, fmt.Sprintf("Moved %s to position %d of %s.", r.id(), ed.Position, t.id()))
	return nil
}
