package documents

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const xlsxEditOps = "set_cell, clear_cell"

// maxDependencyFormulas bounds the dependency walk: past it, dependents go unreported and the report says so.
const maxDependencyFormulas = 200_000

type cellWrite struct {
	sheet    *xsheet
	col, row int
	old      *xcell
	// xml is the whole new <c> element.
	value   fval
	formula string
	typed   string // "n", "str", "b", "date", ""
	style   int
	clear   bool
}

type xlsxEdit struct {
	d         *xlsxDoc
	res       *EditResult
	writes    map[string]*cellWrite
	order     []*cellWrite
	newStyles map[int]int
	stylesXML []string
	// untraced counts formulas the parser could not read: their dependence on an edit is unknown.
	untraced int
}

// Apply implements Document.
func (d *xlsxDoc) Apply(edits []Edit) (*EditResult, error) {
	e := &xlsxEdit{d: d, res: &EditResult{}, writes: map[string]*cellWrite{}, newStyles: map[int]int{}}
	for i, ed := range edits {
		var err error
		switch ed.Op {
		case "set_cell":
			err = e.setCell(ed)
		case "clear_cell":
			ed.Value = nil
			err = e.clearCell(ed)
		default:
			err = refuse("unknown operation %q for a workbook; use one of: %s (row and column insertion are not available in this version)", ed.Op, xlsxEditOps)
		}
		if err != nil {
			return nil, fmt.Errorf("edit %d (%s): %w", i+1, ed.Op, err)
		}
	}
	return e.finish()
}

func (e *xlsxEdit) target(ed Edit) (*xsheet, int, int, *xcell, error) {
	if !strings.Contains(ed.Target, "!") {
		return nil, 0, 0, nil, refuse("target %q needs its sheet, as in Sheet1!B2", ed.Target)
	}
	sh, a, err := e.d.sheetAndArea(ed.Target)
	if err != nil {
		return nil, 0, 0, nil, err
	}
	if a.col1 != a.col2 || a.row1 != a.row2 || a.col1 == 0 {
		return nil, 0, 0, nil, refuse("target %q must be one cell; send one edit per cell", ed.Target)
	}
	col, row := a.col1, a.row1
	cell := sh.cells[[2]int{col, row}]
	state := func() []string {
		if cell == nil {
			return []string{sh.name + "!" + cellName(col, row) + " (empty)"}
		}
		return []string{sh.name + "!" + sh.cellLine(e.d, cell)}
	}
	// The address is checked: anything already there must be named by its fingerprint.
	if cell != nil && cell.hasContent() && ed.Fingerprint != cell.fp {
		msg := fmt.Sprintf("%s needs its current fingerprint. Nothing was written. Current state:", ed.Target)
		if ed.Fingerprint != "" {
			msg = fmt.Sprintf("%s no longer has fingerprint %s: the cell changed. Nothing was written. Current state:", ed.Target, ed.Fingerprint)
		}
		return nil, 0, 0, nil, &RefusedError{Message: msg, Current: state()}
	}
	if (cell == nil || !cell.hasContent()) && ed.Fingerprint != "" && (cell == nil || ed.Fingerprint != cell.fp) {
		return nil, 0, 0, nil, &RefusedError{Message: fmt.Sprintf("%s no longer has fingerprint %s: it is empty now. Nothing was written.", ed.Target, ed.Fingerprint), Current: state()}
	}
	if sh.protected {
		return nil, 0, 0, nil, refuse("sheet %q is protected; unprotect it in Excel first", sh.name)
	}
	for _, m := range sh.merges {
		if m.contains(col, row) && (m.col1 != col || m.row1 != row) {
			return nil, 0, 0, nil, refuse("%s lies inside the merged range %s; write to its anchor %s!%s", ed.Target, m, sh.name, cellName(m.col1, m.row1))
		}
	}
	for _, t := range sh.tables {
		if t.header && row == t.ref.row1 && col >= t.ref.col1 && col <= t.ref.col2 {
			return nil, 0, 0, nil, refuse("%s is a header of table %q, whose definition names its columns; rename the column in Excel", ed.Target, t.name)
		}
	}
	if cell != nil {
		switch {
		case cell.fType == "array":
			return nil, 0, 0, nil, refuse("%s holds an array formula over %s; change it in Excel", ed.Target, cell.fRef)
		case cell.fType == "dataTable":
			return nil, 0, 0, nil, refuse("%s belongs to a data table; change it in Excel", ed.Target)
		case cell.master:
			return nil, 0, 0, nil, refuse("%s is the master of shared formula %s, over %s: the other cells derive from it. Write to the other cells, or rewrite the formula in Excel", ed.Target, cell.si, cell.fRef)
		case cell.metadata:
			return nil, 0, 0, nil, refuse("%s holds a dynamic array or rich value; change it in Excel", ed.Target)
		}
	}
	for _, a := range sh.arrayRanges() {
		if a.contains(col, row) && (cell == nil || cell.fType != "array") {
			return nil, 0, 0, nil, refuse("%s lies inside the array formula over %s; change it in Excel", ed.Target, a)
		}
	}
	return sh, col, row, cell, nil
}

func (sh *xsheet) arrayRanges() []area {
	var out []area
	for _, c := range sh.order {
		if c.fType == "array" && c.fRef != "" {
			if a, ok := parseArea(c.fRef); ok {
				out = append(out, a)
			}
		}
	}
	return out
}

func (e *xlsxEdit) add(ed Edit, w *cellWrite) error {
	key := ed.Target
	if _, dup := e.writes[key]; dup {
		return refuse("two edits of this batch write %s", key)
	}
	e.writes[key] = w
	e.order = append(e.order, w)
	return nil
}

func (e *xlsxEdit) setCell(ed Edit) error {
	sh, col, row, cell, err := e.target(ed)
	if err != nil {
		return err
	}
	w := &cellWrite{sheet: sh, col: col, row: row, old: cell}
	if cell != nil {
		w.style = cell.style
	}
	addr := sh.name + "!" + cellName(col, row)
	if ed.Formula != "" {
		if ed.Value != nil {
			return refuse("%s: give a value or a formula, not both", addr)
		}
		f := strings.TrimPrefix(strings.TrimSpace(ed.Formula), "=")
		if f == "" {
			return refuse("%s: the formula is empty", addr)
		}
		if _, err := lex(f); err != nil {
			return refuse("%s: the formula does not read: %v", addr, err)
		}
		w.formula = f
		w.typed = "f"
		return e.add(ed, w)
	}
	isDateCell := e.d.isDateStyle(w.style)
	switch v := ed.Value.(type) {
	case nil:
		return refuse("%s: set_cell needs value or formula; clear_cell empties a cell", addr)
	case float64:
		switch ed.Type {
		case "date":
			return refuse("%s: a date is written as an ISO string, such as \"2026-11-06\"", addr)
		case "string":
			w.value, w.typed = formatNum(v), "str"
		default:
			w.value, w.typed = v, "n"
			if isDateCell {
				e.res.Notes = append(e.res.Notes, fmt.Sprintf("%s has a date format: the number %s shows as %s.", addr, formatNum(v), e.d.serialToDate(v)))
			}
		}
	case bool:
		w.value, w.typed = v, "b"
	case string:
		switch ed.Type {
		case "date":
			serial, ok := e.d.dateToSerial(v)
			if !ok {
				return refuse("%s: %q is not an ISO date such as 2026-11-06 or 2026-11-06T09:30:00", addr, v)
			}
			w.value, w.typed = serial, "n"
			if !isDateCell {
				s, err := e.dateStyle(w.style)
				if err != nil {
					return err
				}
				w.style = s
				e.res.Notes = append(e.res.Notes, fmt.Sprintf("%s got a date format, based on its old style.", addr))
			}
		case "number":
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return refuse("%s: %q is not a number", addr, v)
			}
			w.value, w.typed = f, "n"
		default:
			if strings.HasPrefix(v, "=") {
				return refuse("%s: the value starts with =. To set a formula use formula; to store this text pass type: \"string\"", addr)
			}
			if isDateCell && ed.Type == "" {
				return refuse("%s has a date format. Pass type: \"date\" with an ISO date, or type: \"string\" to store text", addr)
			}
			if _, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && ed.Type == "" {
				return refuse("%s: %q looks like a number. Pass it as a JSON number, or type: \"string\" to store it as text", addr, v)
			}
			w.value, w.typed = v, "str"
		}
	default:
		return refuse("%s: a value is a number, string or boolean", addr)
	}
	return e.add(ed, w)
}

func (e *xlsxEdit) clearCell(ed Edit) error {
	sh, col, row, cell, err := e.target(ed)
	if err != nil {
		return err
	}
	if cell == nil || !cell.hasContent() {
		return refuse("%s is already empty", ed.Target)
	}
	return e.add(ed, &cellWrite{sheet: sh, col: col, row: row, old: cell, clear: true, style: cell.style})
}

// dateStyle returns a cell format like base but with a date number format, adding it to the style table.
func (e *xlsxEdit) dateStyle(base int) (int, error) {
	if s, ok := e.newStyles[base]; ok {
		return s, nil
	}
	x := e.d.stylesX
	if x == nil {
		return 0, refuse("the workbook has no style table to add a date format to; set the cell's format in Excel first")
	}
	xfs := x.Root.Kid("cellXfs")
	kids := xfs.KidsNamed("xf")
	if base >= len(kids) {
		return 0, refuse("the cell uses style %d, which does not exist", base)
	}
	// The clone keeps everything the format has, alignment and protection included.
	raw := kids[base].Raw(x.Data)
	tmp, err := ParseXML(raw)
	if err != nil {
		return 0, err
	}
	raw = Splice(raw, []Span{setAttrSpan(raw, tmp.Root, "numFmtId", "14")})
	if tmp, err = ParseXML(raw); err != nil {
		return 0, err
	}
	raw = Splice(raw, []Span{setAttrSpan(raw, tmp.Root, "applyNumberFormat", "1")})
	e.stylesXML = append(e.stylesXML, string(raw))
	id := len(kids) + len(e.stylesXML) - 1
	e.newStyles[base] = id
	return id, nil
}

func fmtV(f float64) string { return strconv.FormatFloat(f, 'g', 17, 64) }

// cellXML renders a cell in the sheet's prefix, keeping its address and style.
func cellXML(sample *Node, col, row, style int, t, inner string) string {
	var b strings.Builder
	b.WriteString("<" + sample.Tag("c") + ` r="` + cellName(col, row) + `"`)
	if style > 0 {
		fmt.Fprintf(&b, ` s="%d"`, style)
	}
	if t != "" {
		b.WriteString(` t="` + t + `"`)
	}
	if inner == "" {
		b.WriteString("/>")
		return b.String()
	}
	b.WriteString(">" + inner + "</" + sample.Tag("c") + ">")
	return b.String()
}

func valueXML(n *Node, v fval) (t, inner string) {
	vt := n.Tag("v")
	switch x := v.(type) {
	case float64:
		return "", "<" + vt + ">" + fmtV(x) + "</" + vt + ">"
	case bool:
		s := "0"
		if x {
			s = "1"
		}
		return "b", "<" + vt + ">" + s + "</" + vt + ">"
	case string:
		return "str", "<" + vt + ">" + escText(x) + "</" + vt + ">"
	case ferr:
		return "e", "<" + vt + ">" + string(x) + "</" + vt + ">"
	}
	return "", ""
}

type cellKey struct {
	sheet    string
	col, row int
}

// dependents finds the formula cells affected by the written cells, in no particular order.
func (e *xlsxEdit) dependents() (map[cellKey]bool, bool) {
	d := e.d
	type frm struct {
		c     *xcell
		refs  []fref
		names []string
	}
	var formulas []frm
	for _, s := range d.sheets {
		for _, c := range s.order {
			if c.formula == "" {
				continue
			}
			if w := e.writeAt(s, c.col, c.row); w != nil {
				continue // being replaced
			}
			f := frm{c: c}
			if n, err := parseFormula(c.formula); err == nil {
				n.collectRefs(&f.refs, &f.names)
			} else {
				e.untraced++
			}
			formulas = append(formulas, f)
			if len(formulas) > maxDependencyFormulas {
				return nil, false
			}
		}
	}
	nameRefs := func(name string) []fref {
		for _, n := range d.names {
			if strings.EqualFold(n.name, name) {
				if r, err := parseRefToken(strings.TrimSpace(n.ref)); err == nil {
					return []fref{r}
				}
			}
		}
		return nil
	}
	changed := map[cellKey]bool{}
	var queue []cellKey
	for _, w := range e.order {
		k := cellKey{w.sheet.name, w.col, w.row}
		changed[k] = true
		queue = append(queue, k)
	}
	out := map[cellKey]bool{}
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		for _, f := range formulas {
			fk := cellKey{f.c.sheet.name, f.c.col, f.c.row}
			if out[fk] || changed[fk] {
				continue
			}
			hit := false
			for _, r := range f.refs {
				s := r.sheet
				if s == "" {
					s = f.c.sheet.name
				}
				if s == k.sheet && r.area.contains(k.col, k.row) {
					hit = true
				}
			}
			for _, nm := range f.names {
				for _, r := range nameRefs(nm) {
					s := r.sheet
					if s == "" {
						s = f.c.sheet.name
					}
					if s == k.sheet && r.area.contains(k.col, k.row) {
						hit = true
					}
				}
			}
			if hit {
				out[fk] = true
				queue = append(queue, fk)
			}
		}
	}
	return out, true
}

func (e *xlsxEdit) writeAt(s *xsheet, col, row int) *cellWrite {
	for _, w := range e.order {
		if w.sheet == s && w.col == col && w.row == row {
			return w
		}
	}
	return nil
}

// evalAll computes the new value of every affected formula. A cell it cannot compute stays absent from values.
func (e *xlsxEdit) evalAll(affected map[cellKey]bool) (map[cellKey]fval, map[cellKey]string) {
	d := e.d
	values := map[cellKey]fval{}
	unknown := map[cellKey]string{}
	visiting := map[cellKey]bool{}
	writes := map[cellKey]*cellWrite{}
	for _, w := range e.order {
		writes[cellKey{w.sheet.name, w.col, w.row}] = w
	}
	var compute func(k cellKey) (fval, error)
	computeFormula := func(k cellKey, f string) (fval, error) {
		n, err := parseFormula(f)
		if err != nil {
			return nil, errUnknown{"a formula it cannot parse"}
		}
		c := &evalCtx{sheet: k.sheet}
		c.cell = func(sheet string, col, row int) (fval, error) {
			if d.byName[sheet] == nil {
				return nil, errUnknown{"another workbook or sheet"}
			}
			return compute(cellKey{sheet, col, row})
		}
		c.bounds = func(sheet string) (int, int) {
			s := d.byName[sheet]
			if s == nil {
				return 0, 0
			}
			mc, mr := s.maxCol, s.maxRow
			for _, w := range e.order {
				if w.sheet == s {
					mc, mr = max(mc, w.col), max(mr, w.row)
				}
			}
			return mc, mr
		}
		c.name = func(name string) (fref, bool) {
			for _, x := range d.names {
				if strings.EqualFold(x.name, name) {
					if r, err := parseRefToken(strings.TrimSpace(x.ref)); err == nil {
						return r, true
					}
				}
			}
			return fref{}, false
		}
		v, err := c.eval(n)
		if err != nil {
			return nil, err
		}
		if _, isRange := v.(frange); isRange {
			return nil, errUnknown{"a formula that returns a range"}
		}
		return v, nil
	}
	compute = func(k cellKey) (fval, error) {
		if w, ok := writes[k]; ok {
			if w.clear {
				return nil, nil
			}
			if w.typed != "f" {
				return w.value, nil
			}
		}
		if v, ok := values[k]; ok {
			return v, nil
		}
		if why, ok := unknown[k]; ok {
			return nil, errUnknown{why}
		}
		var formula string
		if w, ok := writes[k]; ok && w.typed == "f" {
			formula = w.formula
		} else if affected[k] {
			formula = d.byName[k.sheet].cells[[2]int{k.col, k.row}].formula
		} else {
			c := d.byName[k.sheet].cells[[2]int{k.col, k.row}]
			if c == nil {
				return nil, nil
			}
			return d.value(c), nil
		}
		if visiting[k] {
			return nil, errUnknown{"a circular reference"}
		}
		visiting[k] = true
		v, err := computeFormula(k, formula)
		delete(visiting, k)
		if err != nil {
			unknown[k] = err.Error()
			return nil, err
		}
		values[k] = v
		return v, nil
	}
	for k := range affected {
		_, _ = compute(k)
	}
	for k, w := range writes {
		if w.typed == "f" {
			_, _ = compute(k)
		}
	}
	return values, unknown
}

func (e *xlsxEdit) finish() (*EditResult, error) {
	d := e.d
	c := NewChanges()
	affected, known := e.dependents()
	if !known {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("The workbook has over %d formulas: dependent cells were not traced. It recalculates when opened.", maxDependencyFormulas))
		affected = map[cellKey]bool{}
	}
	if e.untraced > 0 {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("%d formula(s) use syntax this tool cannot trace (structured references, other workbooks): they may depend on the edit too.", e.untraced))
	}
	values, unknown := e.evalAll(affected)

	spans := map[*xsheet][]Span{}
	pendingNew := map[*xsheet]map[int]map[int]string{}
	addNew := func(s *xsheet, col, row int, xml string) {
		if pendingNew[s] == nil {
			pendingNew[s] = map[int]map[int]string{}
		}
		if pendingNew[s][row] == nil {
			pendingNew[s][row] = map[int]string{}
		}
		pendingNew[s][row][col] = xml
	}
	removedFormula := false
	writesFormula := false
	for _, w := range e.order {
		sample := w.sheet.data
		addr := w.sheet.name + "!" + cellName(w.col, w.row)
		before := "(empty)"
		if w.old != nil {
			before = d.displayValue(w.old)
			if w.old.formula != "" {
				before = "=" + w.old.formula + " → " + before
			}
			if w.old.formula != "" && (w.typed != "f" || w.clear) {
				removedFormula = true
			}
		}
		var xml, after string
		switch {
		case w.clear:
			xml = cellXML(sample, w.col, w.row, w.style, "", "")
			after = "(empty)"
		case w.typed == "f":
			writesFormula = true
			inner := "<" + sample.Tag("f") + ">" + escText(w.formula) + "</" + sample.Tag("f") + ">"
			t := ""
			after = "=" + w.formula
			if v, ok := values[cellKey{w.sheet.name, w.col, w.row}]; ok {
				vt, vx := valueXML(sample, v)
				t, inner = vt, inner+vx
				after += " → " + toText(v)
			} else {
				after += " → computed when opened"
			}
			xml = cellXML(sample, w.col, w.row, w.style, t, inner)
		case w.typed == "str":
			s := w.value.(string)
			xml = cellXML(sample, w.col, w.row, w.style, "inlineStr", "<"+sample.Tag("is")+`><`+sample.Tag("t")+` xml:space="preserve">`+escText(s)+"</"+sample.Tag("t")+"></"+sample.Tag("is")+">")
			after = strconv.Quote(s)
		default:
			t, inner := valueXML(sample, w.value)
			xml = cellXML(sample, w.col, w.row, w.style, t, inner)
			after = toText(w.value)
		}
		if w.old != nil {
			spans[w.sheet] = append(spans[w.sheet], Span{w.old.node.Start, w.old.node.End, xml})
		} else {
			addNew(w.sheet, w.col, w.row, xml)
		}
		e.res.Changes = append(e.res.Changes, fmt.Sprintf("%s: %s → %s", addr, before, after))
	}

	// Dependent cells: the report gives before and after; values it can compute are written, into those cells only.
	var keys []cellKey
	for k := range affected {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.sheet != b.sheet {
			return a.sheet < b.sheet
		}
		if a.row != b.row {
			return a.row < b.row
		}
		return a.col < b.col
	})
	stale := 0
	reported := 0
	for _, k := range keys {
		s := d.byName[k.sheet]
		cell := s.cells[[2]int{k.col, k.row}]
		addr := k.sheet + "!" + cellName(k.col, k.row)
		line := ""
		if v, ok := values[k]; ok {
			line = fmt.Sprintf("%s (=%s): %s → %s", addr, cell.formula, toText(d.value(cell)), toText(v))
			if span, ok := dependentSpan(s, cell, v); ok {
				spans[s] = append(spans[s], span)
			}
		} else {
			stale++
			line = fmt.Sprintf("%s (=%s): %s → unknown, until recalculated (it uses %s)", addr, cell.formula, toText(d.value(cell)), unknown[k])
		}
		if reported < 50 {
			e.res.Notes = append(e.res.Notes, "Dependent "+line)
			reported++
		}
	}
	if len(keys) > 50 {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("… and %d more dependent cells.", len(keys)-50))
	}
	if stale > 0 {
		e.res.Notes = append(e.res.Notes, fmt.Sprintf("%d dependent cell(s) keep their old stored value until the workbook recalculates; it is marked to do so on open.", stale))
	}

	// New cells and rows go in sorted, as the schema requires.
	for s, rows := range pendingNew {
		spans[s] = append(spans[s], e.newCellSpans(s, rows)...)
	}
	for s := range spans {
		if span, ok := e.dimensionSpan(s); ok {
			spans[s] = append(spans[s], span)
		}
	}
	for s, sp := range spans {
		if spansOverlap(sp) {
			return nil, refuse("the edits of this batch overlap in sheet %q", s.name)
		}
		c.Set[s.part] = Splice(s.x.Data, sp)
	}

	if len(e.stylesXML) > 0 {
		x := d.stylesX
		xfs := x.Root.Kid("cellXfs")
		total := len(xfs.KidsNamed("xf")) + len(e.stylesXML)
		c.Set[d.stylesP] = Splice(x.Data, []Span{setAttrSpan(x.Data, xfs, "count", strconv.Itoa(total)), {xfs.InnerEnd, xfs.InnerEnd, strings.Join(e.stylesXML, "")}})
	}
	// A formula that went away must leave the calc chain, or Excel repairs the file. Excel rebuilds a missing chain.
	if removedFormula && d.calcP != "" && d.pkg.Has(d.calcP) {
		c.Remove[d.calcP] = true
		if err := c.removeContentType(d.pkg, d.calcP); err != nil {
			return nil, err
		}
		rels, _ := d.pkg.Rels(d.wbPart)
		for _, r := range rels {
			if strings.HasSuffix(r.Type, "/calcChain") {
				if err := c.removeRelationship(d.pkg, d.wbPart, r.ID); err != nil {
					return nil, err
				}
			}
		}
		e.res.Notes = append(e.res.Notes, "calcChain.xml was removed because a formula went away; Excel rebuilds it.")
	}
	if len(keys) > 0 || writesFormula {
		wb, err := e.markRecalc()
		if err != nil {
			return nil, err
		}
		c.Set[d.wbPart] = wb
	}
	if len(keys) == 0 && !writesFormula && known {
		e.res.Notes = append(e.res.Notes, "No formula depends on the changed cells.")
	}
	data, err := d.pkg.Write(c)
	if err != nil {
		return nil, err
	}
	e.res.Data = data
	return e.res, nil
}

// dependentSpan rewrites a dependent cell's stored value, leaving its formula element as it is.
func dependentSpan(s *xsheet, cell *xcell, v fval) (Span, bool) {
	n := cell.node
	f := n.Kid("f")
	if f == nil || n.SelfClosed {
		return Span{}, false
	}
	t, inner := valueXML(n, v)
	var b strings.Builder
	b.WriteString("<" + n.Tag("c"))
	for _, a := range n.Attrs {
		if a.Name.Local == "t" {
			continue
		}
		name := a.Name.Local
		if a.Name.Space != "" {
			name = a.Name.Space + ":" + name
		}
		fmt.Fprintf(&b, ` %s="%s"`, name, escAttr(a.Value))
	}
	if t != "" {
		b.WriteString(` t="` + t + `"`)
	}
	b.WriteString(">")
	b.Write(f.Raw(s.x.Data))
	b.WriteString(inner)
	b.WriteString("</" + n.Tag("c") + ">")
	return Span{n.Start, n.End, b.String()}, true
}

func (e *xlsxEdit) newCellSpans(s *xsheet, rows map[int]map[int]string) []Span {
	var out []Span
	data := s.x.Data
	var nums []int
	for r := range rows {
		nums = append(nums, r)
	}
	sort.Ints(nums)
	for _, rn := range nums {
		cols := rows[rn]
		var cs []int
		for c := range cols {
			cs = append(cs, c)
		}
		sort.Ints(cs)
		if row := s.rows[rn]; row != nil {
			if row.SelfClosed {
				var b strings.Builder
				for _, c := range cs {
					b.WriteString(cols[c])
				}
				open := rowSpansRe.ReplaceAllString(string(startTag(data, row)), "")
				out = append(out, Span{row.Start, row.End, strings.TrimSuffix(strings.TrimRight(strings.TrimSuffix(open, "/>"), " "), "/") + ">" + b.String() + "</" + row.Tag("row") + ">"})
				continue
			}
			if sp, ok := removeAttrSpan(data, row, "spans"); ok {
				out = append(out, sp)
			}
			existing := row.KidsNamed("c")
			for _, c := range cs {
				at := row.InnerEnd
				for _, x := range existing {
					col, _, ok := parseCell(x.Attr("r"))
					if ok && col > c {
						at = x.Start
						break
					}
				}
				out = append(out, Span{at, at, cols[c]})
			}
			continue
		}
		at := -1
		for _, other := range s.rowNums {
			if other > rn {
				at = s.rows[other].Start
				break
			}
		}
		var b strings.Builder
		b.WriteString("<" + s.data.Tag("row") + ` r="` + strconv.Itoa(rn) + `">`)
		for _, c := range cs {
			b.WriteString(cols[c])
		}
		b.WriteString("</" + s.data.Tag("row") + ">")
		if at >= 0 {
			out = append(out, Span{at, at, b.String()})
		} else if s.data.SelfClosed {
			// Collected below: a self-closed sheetData is rewritten once.
			out = appendToSelfClosed(out, data, s.data, b.String())
		} else {
			out = append(out, Span{s.data.InnerEnd, s.data.InnerEnd, b.String()})
		}
	}
	return out
}

var rowSpansRe = regexp.MustCompile(`\sspans="[^"]*"`)

func appendToSelfClosed(out []Span, data []byte, n *Node, text string) []Span {
	for i := range out {
		if out[i].Start == n.Start && out[i].End == n.End {
			closing := "</" + n.Tag(n.Local) + ">"
			out[i].Text = strings.TrimSuffix(out[i].Text, closing) + text + closing
			return out
		}
	}
	return append(out, insertKidSpan(data, n, text))
}

// dimensionSpan widens the sheet's dimension element when new cells fall outside it.
func (e *xlsxEdit) dimensionSpan(s *xsheet) (Span, bool) {
	dim := s.x.Root.Kid("dimension")
	if dim == nil {
		return Span{}, false
	}
	minC, minR, maxC, maxR := s.minCol, s.minRow, s.maxCol, s.maxRow
	for _, w := range e.order {
		if w.sheet != s || w.clear {
			continue
		}
		if minC == 0 || w.col < minC {
			minC = w.col
		}
		if minR == 0 || w.row < minR {
			minR = w.row
		}
		maxC, maxR = max(maxC, w.col), max(maxR, w.row)
	}
	if maxC == 0 {
		return Span{}, false
	}
	ref := cellName(minC, minR) + ":" + cellName(maxC, maxR)
	if ref == dim.Attr("ref") {
		return Span{}, false
	}
	return setAttrSpan(s.x.Data, dim, "ref", ref), true
}

// markRecalc sets fullCalcOnLoad so a reader that calculates brings every value up to date.
func (e *xlsxEdit) markRecalc() ([]byte, error) {
	d := e.d
	x := d.wb
	if cp := x.Root.Kid("calcPr"); cp != nil {
		return Splice(x.Data, []Span{setAttrSpan(x.Data, cp, "fullCalcOnLoad", "1")}), nil
	}
	// calcPr follows definedNames, or sheets when there are none.
	after := x.Root.Kid("definedNames")
	if after == nil {
		after = x.Root.Kid("sheets")
	}
	if after == nil {
		return nil, refuse("the workbook part has no sheets element")
	}
	el := "<" + x.Root.Tag("calcPr") + ` fullCalcOnLoad="1"/>`
	return Splice(x.Data, []Span{{after.End, after.End, el}}), nil
}
