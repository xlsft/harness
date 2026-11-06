package documents

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A workbook's sheets are read into one coordinate view: for every cell its value, its formula if any, and a
// fingerprint of its XML. A shared formula's children show the formula they evaluate to.

type xcell struct {
	sheet           *xsheet
	col, row        int
	node            *Node
	t               string
	style           int
	raw             string // the <v> text
	formula         string // with shared formulas expanded
	fType, fRef, si string
	master          bool
	fp              string
	metadata        bool // cm or vm: a dynamic array or a rich value
}

func (c *xcell) ref() string { return cellName(c.col, c.row) }

type xtable struct {
	name, part string
	ref        area
	header     bool
	columns    []string
}

type xsheet struct {
	name, id, rid, part, state string
	x                          *XMLDoc
	data                       *Node
	rows                       map[int]*Node
	rowNums                    []int
	cells                      map[[2]int]*xcell
	order                      []*xcell
	merges                     []area
	tables                     []xtable
	protected                  bool
	hiddenRows                 map[int]bool
	hiddenCols                 []area
	maxCol, maxRow             int
	minCol, minRow             int
	charts, validations, conds int
	drawings                   bool
	badOrder                   []string
}

type xlsxDoc struct {
	pkg      *Package
	wbPart   string
	wb       *XMLDoc
	sheets   []*xsheet
	byName   map[string]*xsheet
	strings  []string
	xfNumFmt []int
	numFmts  map[int]string
	names    []xname
	links    []string
	date1904 bool
	wbProt   bool
	stylesP  string
	stylesX  *XMLDoc
	calcP    string
	pivots   int
}

type xname struct {
	name, ref string
	hidden    bool
	localTo   string
}

func openXLSX(data []byte) (Document, error) {
	pkg, err := OpenPackage(data)
	if err != nil {
		return nil, err
	}
	part, err := pkg.officeDocumentPart()
	if err != nil {
		return nil, err
	}
	wb, err := pkg.XML(part)
	if err != nil {
		return nil, err
	}
	d := &xlsxDoc{pkg: pkg, wbPart: part, wb: wb, byName: map[string]*xsheet{}, numFmts: map[int]string{}}
	rels, err := pkg.Rels(part)
	if err != nil {
		return nil, err
	}
	for _, r := range rels {
		if r.External {
			continue
		}
		t := ResolveTarget(part, r.Target)
		switch {
		case strings.HasSuffix(r.Type, "/sharedStrings"):
			if err := d.loadStrings(t); err != nil {
				return nil, err
			}
		case strings.HasSuffix(r.Type, "/styles"):
			d.loadStyles(t)
		case strings.HasSuffix(r.Type, "/calcChain"):
			d.calcP = t
		case strings.HasSuffix(r.Type, "/externalLink"):
			d.links = append(d.links, t)
		case strings.HasSuffix(r.Type, "/pivotCacheDefinition"):
			d.pivots++
		}
	}
	if pr := wb.Root.Kid("workbookPr"); pr != nil {
		v := pr.Attr("date1904")
		d.date1904 = v == "1" || v == "true"
	}
	if pr := wb.Root.Kid("workbookProtection"); pr != nil {
		d.wbProt = pr.Attr("lockStructure") == "1" || pr.Attr("lockStructure") == "true"
	}
	for _, s := range wb.Root.Kid("sheets").KidsNamed("sheet") {
		sh := &xsheet{name: s.Attr("name"), id: s.Attr("sheetId"), rid: s.Attr("id"), state: s.Attr("state"), rows: map[int]*Node{}, cells: map[[2]int]*xcell{}, hiddenRows: map[int]bool{}}
		r, ok := relByID(rels, sh.rid)
		if !ok {
			return nil, fmt.Errorf("the sheet %q has no relationship %s", sh.name, sh.rid)
		}
		sh.part = ResolveTarget(part, r.Target)
		if !strings.HasSuffix(r.Type, "/worksheet") {
			continue // a chartsheet or a macro sheet has no cells to show
		}
		if err := d.loadSheet(sh); err != nil {
			return nil, err
		}
		d.sheets = append(d.sheets, sh)
		d.byName[sh.name] = sh
	}
	if len(d.sheets) == 0 {
		return nil, fmt.Errorf("the workbook has no worksheets")
	}
	for _, n := range wb.Root.Kid("definedNames").KidsNamed("definedName") {
		x := xname{name: n.Attr("name"), ref: n.Text, hidden: n.Attr("hidden") == "1"}
		if l := n.Attr("localSheetId"); l != "" {
			if i, err := strconv.Atoi(l); err == nil && i < len(d.sheets) {
				x.localTo = d.sheets[i].name
			}
		}
		d.names = append(d.names, x)
	}
	return d, nil
}

func (d *xlsxDoc) loadStrings(part string) error {
	x, err := d.pkg.XML(part)
	if err != nil {
		return err
	}
	for _, si := range x.Root.KidsNamed("si") {
		var b strings.Builder
		si.Walk(func(n *Node) bool {
			if n.Local == "rPh" {
				return false // phonetic hints are not the text
			}
			if n.Local == "t" {
				b.WriteString(n.Text)
			}
			return true
		})
		d.strings = append(d.strings, b.String())
	}
	return nil
}

func (d *xlsxDoc) loadStyles(part string) {
	x, err := d.pkg.XML(part)
	if err != nil {
		return
	}
	d.stylesP, d.stylesX = part, x
	for _, f := range x.Root.Kid("numFmts").KidsNamed("numFmt") {
		if id, err := strconv.Atoi(f.Attr("numFmtId")); err == nil {
			d.numFmts[id] = f.Attr("formatCode")
		}
	}
	for _, xf := range x.Root.Kid("cellXfs").KidsNamed("xf") {
		id, _ := strconv.Atoi(xf.Attr("numFmtId"))
		d.xfNumFmt = append(d.xfNumFmt, id)
	}
}

var dateCodeRe = regexp.MustCompile(`"[^"]*"|\\.|\[[^\]]*\]`)

func (d *xlsxDoc) isDateStyle(s int) bool {
	if s < 0 || s >= len(d.xfNumFmt) {
		return false
	}
	id := d.xfNumFmt[s]
	if (id >= 14 && id <= 22) || (id >= 27 && id <= 36) || (id >= 45 && id <= 47) || (id >= 50 && id <= 58) {
		return true
	}
	code := strings.ToLower(dateCodeRe.ReplaceAllString(d.numFmts[id], ""))
	return strings.ContainsAny(code, "ymdh") || strings.Contains(code, "ss")
}

// serialToDate turns a date serial into text.
func (d *xlsxDoc) serialToDate(v float64) string {
	if v < 0 || math.IsInf(v, 0) || math.IsNaN(v) || v > 2958465 {
		return ""
	}
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	if d.date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	t := base.Add(time.Duration(v * float64(24*time.Hour)))
	if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02 15:04:05")
}

func (d *xlsxDoc) dateToSerial(s string) (float64, bool) {
	for _, layout := range []string{"2006-01-02", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
			if d.date1904 {
				base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			return t.Sub(base).Hours() / 24, true
		}
	}
	return 0, false
}

func (d *xlsxDoc) loadSheet(sh *xsheet) error {
	x, err := d.pkg.XML(sh.part)
	if err != nil {
		return err
	}
	sh.x = x
	root := x.Root
	sh.data = root.Kid("sheetData")
	if sh.data == nil {
		return fmt.Errorf("the sheet %q has no sheetData", sh.name)
	}
	if p := root.Kid("sheetProtection"); p != nil {
		v := p.Attr("sheet")
		sh.protected = v == "1" || v == "true"
	}
	for _, m := range root.Kid("mergeCells").KidsNamed("mergeCell") {
		if a, ok := parseArea(m.Attr("ref")); ok {
			sh.merges = append(sh.merges, a)
		}
	}
	for _, c := range root.Kid("cols").KidsNamed("col") {
		if c.Attr("hidden") == "1" || c.Attr("hidden") == "true" {
			lo, _ := strconv.Atoi(c.Attr("min"))
			hi, _ := strconv.Atoi(c.Attr("max"))
			sh.hiddenCols = append(sh.hiddenCols, area{col1: lo, col2: hi})
		}
	}
	sh.validations = len(root.Kid("dataValidations").KidsNamed("dataValidation"))
	sh.conds = len(root.KidsNamed("conditionalFormatting"))
	sh.drawings = root.Kid("drawing") != nil
	if err := d.loadTables(sh); err != nil {
		return err
	}
	type master struct {
		col, row int
		f        string
	}
	masters := map[string]master{}
	lastRow := 0
	for _, r := range sh.data.KidsNamed("row") {
		rn, _ := strconv.Atoi(r.Attr("r"))
		if rn == 0 {
			rn = lastRow + 1 // the attribute is optional in the schema
		}
		if rn <= lastRow {
			sh.badOrder = append(sh.badOrder, fmt.Sprintf("row %d comes after row %d", rn, lastRow))
		}
		lastRow = rn
		sh.rows[rn] = r
		sh.rowNums = append(sh.rowNums, rn)
		if v := r.Attr("hidden"); v == "1" || v == "true" {
			sh.hiddenRows[rn] = true
		}
		lastCol := 0
		for _, c := range r.KidsNamed("c") {
			col, row, ok := parseCell(c.Attr("r"))
			if !ok {
				col, row = lastCol+1, rn
			}
			if row != rn {
				sh.badOrder = append(sh.badOrder, fmt.Sprintf("cell %s sits in row %d", c.Attr("r"), rn))
			}
			if col <= lastCol {
				sh.badOrder = append(sh.badOrder, fmt.Sprintf("cell %s is out of order in row %d", c.Attr("r"), rn))
			}
			lastCol = col
			cell := &xcell{sheet: sh, col: col, row: row, node: c, t: c.Attr("t"), fp: Fingerprint(c.Raw(x.Data)), metadata: c.HasAttr("cm") || c.HasAttr("vm")}
			cell.style, _ = strconv.Atoi(c.Attr("s"))
			if v := c.Kid("v"); v != nil {
				cell.raw = v.Text
			}
			if is := c.Kid("is"); is != nil && cell.t == "inlineStr" {
				var b strings.Builder
				for _, t := range is.Find("t") {
					b.WriteString(t.Text)
				}
				cell.raw = b.String()
			}
			if f := c.Kid("f"); f != nil {
				cell.formula, cell.fType, cell.fRef, cell.si = f.Text, f.Attr("t"), f.Attr("ref"), f.Attr("si")
				if cell.fType == "shared" {
					if f.Text != "" {
						cell.master = true
						masters[cell.si] = master{col, row, f.Text}
					} else if m, ok := masters[cell.si]; ok {
						cell.formula = shiftFormula(m.f, col-m.col, row-m.row)
					}
				}
			}
			sh.cells[[2]int{col, row}] = cell
			sh.order = append(sh.order, cell)
			if cell.hasContent() {
				if sh.maxCol == 0 || col > sh.maxCol {
					sh.maxCol = col
				}
				if sh.minCol == 0 || col < sh.minCol {
					sh.minCol = col
				}
				if row > sh.maxRow {
					sh.maxRow = row
				}
				if sh.minRow == 0 || row < sh.minRow {
					sh.minRow = row
				}
			}
		}
	}
	return nil
}

func (c *xcell) hasContent() bool { return c.raw != "" || c.formula != "" || c.fType != "" }

func (d *xlsxDoc) loadTables(sh *xsheet) error {
	rels, err := d.pkg.Rels(sh.part)
	if err != nil {
		return err
	}
	for _, r := range rels {
		if r.External {
			continue
		}
		t := ResolveTarget(sh.part, r.Target)
		switch {
		case strings.HasSuffix(r.Type, "/table"):
			x, err := d.pkg.XML(t)
			if err != nil {
				return err
			}
			tb := xtable{name: x.Root.Attr("displayName"), part: t, header: x.Root.Attr("headerRowCount") != "0"}
			tb.ref, _ = parseArea(x.Root.Attr("ref"))
			for _, c := range x.Root.Kid("tableColumns").KidsNamed("tableColumn") {
				tb.columns = append(tb.columns, c.Attr("name"))
			}
			sh.tables = append(sh.tables, tb)
		case strings.HasSuffix(r.Type, "/drawing"):
			if dr, err := d.pkg.Rels(t); err == nil {
				for _, x := range dr {
					if strings.HasSuffix(x.Type, "/chart") {
						sh.charts++
					}
				}
			}
		}
	}
	return nil
}

// value computes what the cell holds as a typed value.
func (d *xlsxDoc) value(c *xcell) fval {
	switch c.t {
	case "s":
		if i, err := strconv.Atoi(c.raw); err == nil && i >= 0 && i < len(d.strings) {
			return d.strings[i]
		}
		return nil
	case "str", "inlineStr":
		if c.raw == "" && c.formula == "" {
			return nil
		}
		return c.raw
	case "b":
		return c.raw == "1" || c.raw == "true"
	case "e":
		return ferr(c.raw)
	}
	if c.raw == "" {
		return nil
	}
	f, err := strconv.ParseFloat(c.raw, 64)
	if err != nil {
		return c.raw
	}
	return f
}

func (d *xlsxDoc) displayValue(c *xcell) string {
	v := d.value(c)
	switch x := v.(type) {
	case nil:
		return "(empty)"
	case string:
		return strconv.Quote(x)
	case float64:
		if d.isDateStyle(c.style) {
			if s := d.serialToDate(x); s != "" {
				return s + " (date, serial " + c.raw + ")"
			}
		}
		return c.raw
	case bool:
		return strings.ToUpper(strconv.FormatBool(x))
	case ferr:
		return string(x) + " (error)"
	}
	return c.raw
}

func (sh *xsheet) cellLine(d *xlsxDoc, c *xcell) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s #%s ", c.ref(), c.fp)
	if c.formula != "" {
		b.WriteString("=" + c.formula + " → ")
	}
	b.WriteString(d.displayValue(c))
	for _, m := range sh.merges {
		if m.contains(c.col, c.row) {
			if m.col1 == c.col && m.row1 == c.row {
				b.WriteString(" [merged " + m.String() + "]")
			} else {
				b.WriteString(" [inside merge " + m.String() + "]")
			}
		}
	}
	if c.fType == "array" {
		b.WriteString(" [array formula " + c.fRef + "]")
	}
	if c.master {
		b.WriteString(" [shared formula master " + c.fRef + "]")
	}
	if sh.hiddenRows[c.row] {
		b.WriteString(" [hidden row]")
	}
	for _, h := range sh.hiddenCols {
		if c.col >= h.col1 && c.col <= h.col2 {
			b.WriteString(" [hidden column]")
			break
		}
	}
	return b.String()
}

func (d *xlsxDoc) Format() Format { return FormatXLSX }

// Structure implements Document.
func (d *xlsxDoc) Structure() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Excel workbook: %d sheets, %d defined names, %d external links.\n", len(d.sheets), len(d.names), len(d.links))
	if d.date1904 {
		b.WriteString("Dates use the 1904 system.\n")
	}
	if d.wbProt {
		b.WriteString("The workbook's structure is protected.\n")
	}
	b.WriteString("Addresses are Sheet!Cell; a cell with content needs its #fingerprint when edited.\n")
	for _, s := range d.sheets {
		state := ""
		if s.state != "" && s.state != "visible" {
			state = " [" + strings.ToUpper(s.state) + "]"
		}
		if s.protected {
			state += " [PROTECTED]"
		}
		used := "empty"
		if s.maxCol > 0 {
			used = cellName(s.minCol, s.minRow) + ":" + cellName(s.maxCol, s.maxRow)
		}
		formulas := 0
		for _, c := range s.order {
			if c.formula != "" {
				formulas++
			}
		}
		fmt.Fprintf(&b, "Sheet %q%s: used range %s, %d cells, %d formulas", s.name, state, used, len(s.order), formulas)
		if s.charts > 0 {
			fmt.Fprintf(&b, ", %d charts", s.charts)
		}
		if s.validations > 0 {
			fmt.Fprintf(&b, ", %d data validations", s.validations)
		}
		if s.conds > 0 {
			fmt.Fprintf(&b, ", %d conditional formats", s.conds)
		}
		b.WriteString("\n")
		if len(s.merges) > 0 {
			var ms []string
			for i, m := range s.merges {
				if i == 30 {
					ms = append(ms, fmt.Sprintf("… %d more", len(s.merges)-30))
					break
				}
				ms = append(ms, m.String())
			}
			b.WriteString("  merged: " + strings.Join(ms, ", ") + "\n")
		}
		for _, t := range s.tables {
			fmt.Fprintf(&b, "  table %q %s, columns: %s\n", t.name, t.ref, strings.Join(t.columns, ", "))
		}
		if len(s.hiddenRows) > 0 {
			var rs []int
			for r := range s.hiddenRows {
				rs = append(rs, r)
			}
			sort.Ints(rs)
			if len(rs) > 30 {
				rs = rs[:30]
			}
			fmt.Fprintf(&b, "  hidden rows: %v (of %d)\n", rs, len(s.hiddenRows))
		}
		for _, h := range s.hiddenCols {
			fmt.Fprintf(&b, "  hidden columns: %s:%s\n", colName(h.col1), colName(h.col2))
		}
	}
	for _, n := range d.names {
		extra := ""
		if n.hidden {
			extra = " [hidden]"
		}
		if n.localTo != "" {
			extra += " [local to " + n.localTo + "]"
		}
		fmt.Fprintf(&b, "Name %s = %s%s\n", n.name, oneLine(n.ref, 100), extra)
	}
	for _, l := range d.links {
		fmt.Fprintf(&b, "External link: %s (stays as it is)\n", l)
	}
	if d.pivots > 0 {
		fmt.Fprintf(&b, "%d pivot caches: they do not refresh from edits until the user refreshes them.\n", d.pivots)
	}
	return clip(b.String())
}

func (d *xlsxDoc) sheetAndArea(s string) (*xsheet, area, error) {
	name, rest := s, ""
	if i := strings.LastIndex(s, "!"); i >= 0 {
		name, rest = s[:i], s[i+1:]
	}
	if strings.HasPrefix(name, "'") && strings.HasSuffix(name, "'") && len(name) > 1 {
		name = strings.ReplaceAll(name[1:len(name)-1], "''", "'")
	}
	sh := d.byName[name]
	if sh == nil {
		if rest == "" && !strings.Contains(s, "!") {
			if a, ok := parseArea(s); ok {
				return d.sheets[0], a, nil
			}
		}
		var names []string
		for _, x := range d.sheets {
			names = append(names, x.name)
		}
		return nil, area{}, refuse("no sheet %q; sheets: %s", name, strings.Join(names, ", "))
	}
	if rest == "" {
		return sh, area{col1: 1, row1: 1, col2: 16384, row2: 1048576}, nil
	}
	a, ok := parseArea(rest)
	if !ok {
		return nil, area{}, refuse("%q is not a cell or range", rest)
	}
	return sh, a, nil
}

// Read implements Document: cells of a sheet or range, values and formulas in one view.
func (d *xlsxDoc) Read(sel Selector) (string, error) {
	sh := d.sheets[0]
	a := area{col1: 1, row1: 1, col2: 16384, row2: 1048576}
	if strings.TrimSpace(sel.Range) != "" {
		var err error
		if sh, a, err = d.sheetAndArea(strings.TrimSpace(sel.Range)); err != nil {
			return "", err
		}
	}
	var lines []string
	for _, c := range sh.order {
		if a.contains(c.col, c.row) && c.hasContent() {
			lines = append(lines, sh.cellLine(d, c))
		}
	}
	head := fmt.Sprintf("Sheet %q.", sh.name)
	return pageLines(head, lines, sel), nil
}

// Search implements Document, over values and formulas of every sheet, hidden ones included.
func (d *xlsxDoc) Search(query string, limit int) string {
	if limit <= 0 {
		limit = DefaultLimit
	}
	q := strings.ToLower(query)
	var hits []string
	for _, s := range d.sheets {
		for _, c := range s.order {
			if !c.hasContent() {
				continue
			}
			if containsFold(toText(d.value(c)), q) || containsFold(c.formula, q) {
				hits = append(hits, s.name+"!"+s.cellLine(d, c))
			}
		}
	}
	return searchReport(query, hits, limit)
}

// Check implements Document.
func (d *xlsxDoc) Check() []Finding {
	out := d.pkg.checkPackage()
	bad := func(key, format string, args ...any) {
		out = append(out, Finding{SeverityError, "xlsx:" + key, fmt.Sprintf(format, args...)})
	}
	warn := func(key, format string, args ...any) {
		out = append(out, Finding{SeverityWarning, "xlsx:" + key, fmt.Sprintf(format, args...)})
	}
	seen := map[string]bool{}
	for _, s := range d.sheets {
		low := strings.ToLower(s.name)
		if seen[low] {
			bad("sheet-dup:"+low, "two sheets are named %q", s.name)
		}
		seen[low] = true
		if len([]rune(s.name)) > 31 || strings.ContainsAny(s.name, `[]:*?/\`) {
			bad("sheet-name:"+s.name, "the sheet name %q is not allowed (at most 31 characters, none of []:*?/\\)", s.name)
		}
		for _, o := range s.badOrder {
			bad("order:"+s.name+":"+o, "sheet %q: %s", s.name, o)
		}
		for i, a := range s.merges {
			for _, b2 := range s.merges[i+1:] {
				if a.col1 <= b2.col2 && b2.col1 <= a.col2 && a.row1 <= b2.row2 && b2.row1 <= a.row2 {
					bad("merge:"+s.name+":"+a.String(), "sheet %q: merged ranges %s and %s overlap", s.name, a, b2)
				}
			}
		}
		masters := map[string]bool{}
		for _, c := range s.order {
			if c.master {
				masters[c.si] = true
			}
		}
		for _, c := range s.order {
			addr := s.name + "!" + c.ref()
			if c.t == "s" {
				if i, err := strconv.Atoi(c.raw); err != nil || i < 0 || i >= len(d.strings) {
					bad("sst:"+addr, "%s points to shared string %q, which does not exist", addr, c.raw)
				}
			}
			if c.style >= len(d.xfNumFmt) && c.style > 0 && d.stylesX != nil {
				bad("style:"+addr, "%s uses style %d of %d", addr, c.style, len(d.xfNumFmt))
			}
			if c.fType == "shared" && !masters[c.si] {
				bad("shared:"+addr, "%s belongs to shared formula %s, which has no master", addr, c.si)
			}
			if c.t == "e" {
				warn("error-value:"+addr, "%s holds the error %s", addr, c.raw)
			}
			if c.formula != "" {
				if n, err := parseFormula(c.formula); err == nil {
					var refs []fref
					var names []string
					n.collectRefs(&refs, &names)
					for _, r := range refs {
						if r.sheet != "" && d.byName[r.sheet] == nil && !strings.HasPrefix(r.sheet, "[") {
							warn("bad-sheet-ref:"+addr, "%s refers to a sheet %q that does not exist", addr, r.sheet)
						}
					}
				}
				if strings.Contains(c.formula, "#REF!") {
					warn("ref-error:"+addr, "the formula of %s contains #REF!", addr)
				}
			}
		}
		for _, t := range s.tables {
			if !t.header {
				continue
			}
			for i, name := range t.columns {
				c := s.cells[[2]int{t.ref.col1 + i, t.ref.row1}]
				if c == nil || toText(d.value(c)) != name {
					have := ""
					if c != nil {
						have = toText(d.value(c))
					}
					bad("table-header:"+t.name+":"+name, "table %q: column %q does not match its header cell, which holds %q", t.name, name, have)
				}
			}
		}
		out = append(out, checkRelRefs(d.pkg, s.part, s.x)...)
	}
	for _, n := range d.names {
		if strings.Contains(n.ref, "#REF!") {
			warn("name-ref:"+n.name, "the defined name %s points to #REF!", n.name)
		}
	}
	// A calc chain entry for a cell without a formula makes Excel repair the file.
	if d.calcP != "" && d.pkg.Has(d.calcP) {
		if x, err := d.pkg.XML(d.calcP); err == nil {
			i := ""
			byID := map[string]*xsheet{}
			for _, s := range d.sheets {
				byID[s.id] = s
			}
			for _, c := range x.Root.KidsNamed("c") {
				if v := c.Attr("i"); v != "" {
					i = v
				}
				s := byID[i]
				if s == nil {
					continue
				}
				col, row, ok := parseCell(c.Attr("r"))
				if !ok {
					continue
				}
				if cell := s.cells[[2]int{col, row}]; cell == nil || cell.formula == "" {
					bad("calc-chain:"+s.name+"!"+c.Attr("r"), "calcChain lists %s!%s, which has no formula: Excel would repair the file", s.name, c.Attr("r"))
				}
			}
		}
	}
	return out
}

// Project implements Document.
func (d *xlsxDoc) Project() []string {
	var lines []string
	for _, s := range d.sheets {
		state := ""
		if s.state != "" && s.state != "visible" {
			state = " [" + s.state + "]"
		}
		lines = append(lines, "[sheet "+s.name+state+"]")
		for _, c := range s.order {
			if !c.hasContent() {
				continue
			}
			l := s.name + "!" + c.ref() + ": "
			if c.formula != "" {
				l += "=" + c.formula + " → "
			}
			lines = append(lines, l+d.displayValue(c))
		}
	}
	for _, n := range d.names {
		lines = append(lines, "[name "+n.name+"] "+n.ref)
	}
	return lines
}
