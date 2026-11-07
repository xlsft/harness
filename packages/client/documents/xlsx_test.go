package documents

import (
	"strings"
	"testing"
)

func cellFP(t *testing.T, d Document, addr string) string {
	t.Helper()
	sheet, cell, _ := strings.Cut(addr, "!")
	s := readAll(t, d, sheet)
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, cell+" #") {
			return strings.TrimPrefix(strings.Fields(l)[1], "#")
		}
	}
	t.Fatalf("no cell %s in:\n%s", addr, s)
	return ""
}

func TestXLSXStructureAndCoordinates(t *testing.T) {
	d := mustOpen(t, "a.xlsx", xlsxFixture(t))
	s := d.Structure()
	for _, want := range []string{`"Data"`, "used range A1:D6", "merged: A6:B6", `table "T"`, "HIDDEN", "PROTECTED", "hidden rows: [5]", "Name Rate", "External link"} {
		if !strings.Contains(s, want) {
			t.Errorf("structure lacks %q:\n%s", want, s)
		}
	}
	r := readAll(t, d, "Data!A2:C5")
	for _, want := range []string{"=A2+B2 → 30", "=A4+B4 → 11", "[shared formula master C3:C4]", "2025-", "[hidden row]"} {
		if !strings.Contains(r, want) {
			t.Errorf("read lacks %q:\n%s", want, r)
		}
	}
	if strings.Contains(r, "A1 #") {
		t.Errorf("a cell outside the range was listed:\n%s", r)
	}
	if !strings.Contains(readAll(t, d, "Data!A6"), `"Rich text"`) {
		t.Errorf("rich text: %s", readAll(t, d, "Data!A6"))
	}
	if !strings.Contains(d.Search("a4+b4", 0), "Data!C4") {
		t.Errorf("search over expanded formulas: %s", d.Search("a4+b4", 0))
	}
}

func TestXLSXDependentsBeforeAndAfter(t *testing.T) {
	data := xlsxFixture(t)
	d := mustOpen(t, "a.xlsx", data)
	res := apply(t, "a.xlsx", data, Edit{Op: "set_cell", Target: "Data!A2", Fingerprint: cellFP(t, d, "Data!A2"), Value: 100.0})
	notes := strings.Join(res.Notes, "\n")
	for _, want := range []string{"Data!C2 (=A2+B2): 30 → 120", "Data!D2 (=C2*2): 60 → 240", "unknown", "function VLOOKUP"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	x := partOf(t, res.Data, "xl/worksheets/sheet1.xml")
	if !strings.Contains(x, `<c r="C2"><f>A2+B2</f><v>120</v></c>`) || !strings.Contains(x, `<c r="D2"><f>C2*2</f><v>240</v></c>`) {
		t.Fatalf("computed values not stored:\n%s", x)
	}
	// Unaffected and uncomputable cells keep their bytes.
	for _, keep := range []string{`<c r="C3"><f t="shared" ref="C3:C4" si="0">A3+B3</f><v>3</v></c>`, `<f>VLOOKUP(A2,A2:B4,2,FALSE)</f><v>20</v>`} {
		if !strings.Contains(x, keep) {
			t.Errorf("%s changed:\n%s", keep, x)
		}
	}
	if !strings.Contains(partOf(t, res.Data, "xl/workbook.xml"), `fullCalcOnLoad="1"`) {
		t.Error("not marked to recalculate")
	}
	onlyChanged(t, data, res.Data, "xl/worksheets/sheet1.xml", "xl/workbook.xml")
	noErrors(t, "a.xlsx", res.Data)
}

func TestXLSXNamesAndSharedChildrenAreTraced(t *testing.T) {
	data := xlsxFixture(t)
	d := mustOpen(t, "a.xlsx", data)
	res := apply(t, "a.xlsx", data, Edit{Op: "set_cell", Target: "Data!B3", Fingerprint: cellFP(t, d, "Data!B3"), Value: 7.0})
	notes := strings.Join(res.Notes, "\n")
	if !strings.Contains(notes, "Data!C3 (=A3+B3): 3 → 8") || !strings.Contains(notes, "Data!C5 (=Rate*A2): 5 → 70") {
		t.Errorf("%s", notes)
	}
	res = apply(t, "a.xlsx", data, Edit{Op: "set_cell", Target: "Data!A4", Fingerprint: cellFP(t, d, "Data!A4"), Value: 50.0})
	x := partOf(t, res.Data, "xl/worksheets/sheet1.xml")
	if !strings.Contains(x, `<c r="C4"><f t="shared" si="0"/><v>56</v></c>`) {
		t.Errorf("shared child:\n%s", x)
	}
}

func TestXLSXRefusals(t *testing.T) {
	data := xlsxFixture(t)
	d := mustOpen(t, "a.xlsx", data)
	fp := func(a string) string { return cellFP(t, d, a) }
	refused(t, "a.xlsx", data, "shared formula", Edit{Op: "set_cell", Target: "Data!C3", Fingerprint: fp("Data!C3"), Value: 1.0})
	refused(t, "a.xlsx", data, "merged range", Edit{Op: "set_cell", Target: "Data!B6", Value: 1.0})
	refused(t, "a.xlsx", data, "protected", Edit{Op: "set_cell", Target: "Locked!B1", Value: 1.0})
	refused(t, "a.xlsx", data, "header of table", Edit{Op: "set_cell", Target: "Data!A1", Fingerprint: fp("Data!A1"), Value: "New"})
	refused(t, "a.xlsx", data, "needs its current fingerprint", Edit{Op: "set_cell", Target: "Data!A2", Value: 1.0})
	refused(t, "a.xlsx", data, "no longer has fingerprint", Edit{Op: "set_cell", Target: "Data!A2", Fingerprint: "deadbeef", Value: 1.0})
	refused(t, "a.xlsx", data, "date format", Edit{Op: "set_cell", Target: "Data!A5", Fingerprint: fp("Data!A5"), Value: "tomorrow"})
	refused(t, "a.xlsx", data, "looks like a number", Edit{Op: "set_cell", Target: "Data!F1", Value: "12"})
	refused(t, "a.xlsx", data, "starts with =", Edit{Op: "set_cell", Target: "Data!F1", Value: "=A1"})
	refused(t, "a.xlsx", data, "needs its sheet", Edit{Op: "set_cell", Target: "A1", Value: 1.0})
	refused(t, "a.xlsx", data, "no sheet", Edit{Op: "set_cell", Target: "Nope!A1", Value: 1.0})
	refused(t, "a.xlsx", data, "row and column insertion", Edit{Op: "insert_row", Target: "Data!1:1"})
}

func TestXLSXNewCellsAndDates(t *testing.T) {
	data := xlsxFixture(t)
	res := apply(t, "a.xlsx", data,
		Edit{Op: "set_cell", Target: "Data!F2", Value: "text"},
		Edit{Op: "set_cell", Target: "Data!E2", Value: true},
		Edit{Op: "set_cell", Target: "Data!E9", Value: "2026-11-07", Type: "date"},
		Edit{Op: "set_cell", Target: "Data!A9", Value: 1.5},
		Edit{Op: "set_cell", Target: "Data!E8", Formula: "=A2+1"},
	)
	x := partOf(t, res.Data, "xl/worksheets/sheet1.xml")
	if !strings.Contains(x, `<c r="E2" t="b"><v>1</v></c><c r="F2" t="inlineStr">`) {
		t.Errorf("new cells out of order:\n%s", x)
	}
	if !strings.Contains(x, `<row r="8"><c r="E8"><f>A2+1</f><v>11</v></c></row><row r="9"><c r="A9"><v>1.5</v></c><c r="E9" s="2"><v>46333</v></c></row>`) {
		t.Errorf("new rows:\n%s", x)
	}
	if !strings.Contains(x, `<dimension ref="A1:F9"/>`) {
		t.Errorf("dimension:\n%s", x)
	}
	if strings.Contains(x, `<row r="2" spans`) {
		t.Errorf("a stale spans attribute stayed:\n%s", x)
	}
	styles := partOf(t, res.Data, "xl/styles.xml")
	if !strings.Contains(styles, `count="3"`) || !strings.Contains(styles, `<xf numFmtId="14" applyNumberFormat="1"/>`) {
		t.Errorf("styles:\n%s", styles)
	}
	noErrors(t, "a.xlsx", res.Data)
	d := mustOpen(t, "a.xlsx", res.Data)
	if !strings.Contains(readAll(t, d, "Data!E9"), "2026-11-07") {
		t.Errorf("date: %s", readAll(t, d, "Data!E9"))
	}
}

func TestXLSXRemovingAFormulaRemovesTheCalcChain(t *testing.T) {
	data := xlsxFixture(t)
	d := mustOpen(t, "a.xlsx", data)
	res := apply(t, "a.xlsx", data, Edit{Op: "set_cell", Target: "Data!C2", Fingerprint: cellFP(t, d, "Data!C2"), Value: 5.0})
	if hasPart(t, res.Data, "xl/calcChain.xml") {
		t.Fatal("calcChain.xml stayed")
	}
	if strings.Contains(partOf(t, res.Data, contentTypesPart), "calcChain") || strings.Contains(partOf(t, res.Data, "xl/_rels/workbook.xml.rels"), "calcChain") {
		t.Fatal("calcChain references stayed")
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "calcChain") {
		t.Error("not reported")
	}
	noErrors(t, "a.xlsx", res.Data)
	if !hasPart(t, res.Data, "xl/externalLinks/externalLink1.xml") {
		t.Error("the external link went away")
	}
}

func TestXLSXCheckFindsCalcChainDefect(t *testing.T) {
	data := xlsxFixture(t)
	// A plain write without removing the chain would leave this defect: the check must see it.
	c := NewChanges()
	pkg, _ := OpenPackage(data)
	sheet := partOf(t, data, "xl/worksheets/sheet1.xml")
	c.Set["xl/worksheets/sheet1.xml"] = []byte(strings.Replace(sheet, `<f>A2+B2</f>`, ``, 1))
	bad, err := pkg.Write(c)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range mustOpen(t, "a.xlsx", bad).Check() {
		if strings.HasPrefix(f.Key, "xlsx:calc-chain:") && f.Severity == SeverityError {
			found = true
		}
	}
	if !found {
		t.Error("the calc chain defect went unseen")
	}
}

func TestXLSXCheckComparesWithOriginal(t *testing.T) {
	data := xlsxFixture(t)
	d := mustOpen(t, "a.xlsx", data)
	res := apply(t, "a.xlsx", data, Edit{Op: "set_cell", Target: "Data!A2", Fingerprint: cellFP(t, d, "Data!A2"), Value: 0.0}, Edit{Op: "set_cell", Target: "Data!E2", Formula: "=1/A2"})
	v := Verify("a.xlsx", data, res.Data, t.TempDir())
	var inherited, fresh []string
	for _, f := range v.Inherited {
		inherited = append(inherited, f.Key)
	}
	for _, f := range v.New {
		fresh = append(fresh, f.Key)
	}
	if !strings.Contains(strings.Join(inherited, " "), "error-value:Data!D6") {
		t.Errorf("inherited: %v", inherited)
	}
	if !strings.Contains(strings.Join(fresh, " "), "error-value:Data!E2") {
		t.Errorf("new: %v", fresh)
	}
	if v.HasNewErrors() {
		t.Errorf("a #DIV/0! is a warning about content, not a broken file: %v", v.New)
	}
	if !strings.Contains(v.Report(false), NotOfficeProof) {
		t.Error("the report must say how far a pass goes")
	}
}

func TestFormulaEvaluator(t *testing.T) {
	vals := map[string]fval{"A1": 2.0, "A2": 3.0, "A3": "x", "B1": 10.0}
	ctx := &evalCtx{sheet: "S",
		cell: func(_ string, c, r int) (fval, error) {
			return vals[cellName(c, r)], nil
		},
		bounds: func(string) (int, int) { return 2, 3 },
	}
	cases := map[string]fval{
		"=A1+A2*2":                8.0,
		"=(A1+A2)*2":              10.0,
		"=SUM(A1:A3)":             5.0,
		"=AVERAGE(A1:A2)":         2.5,
		"=COUNT(A:A)":             2.0,
		"=COUNTA(A1:A3)":          3.0,
		`=IF(A1>1,"big","small")`: "big",
		`=A3&"-"&A1`:              "x-2",
		"=1/0":                    ferr("#DIV/0!"),
		"=IFERROR(1/0,7)":         7.0,
		"=ROUND(3.14159,2)":       3.14,
		"=10%":                    0.1,
		"=2^3":                    8.0,
		"=MOD(7,3)":               1.0,
		`=LEFT("hello",2)`:        "he",
		"=AND(A1>0,A2>0)":         true,
		"=-A1":                    -2.0,
		`="a""b"`:                 `a"b`,
		"=MAX(A1,B1)":             10.0,
		`=LEN(UPPER("abc"))`:      3.0,
	}
	for f, want := range cases {
		n, err := parseFormula(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		got, err := ctx.eval(n)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if got != want {
			t.Errorf("%s = %v, want %v", f, got, want)
		}
	}
	n, _ := parseFormula("=VLOOKUP(1,A1:B2,2)")
	if _, err := ctx.eval(n); err == nil {
		t.Error("an unsupported function must be unknown, not a guess")
	}
}

func TestShiftFormula(t *testing.T) {
	cases := [][3]string{
		{"A1+B2", "A2+B3", ""},
		{`$A$1+A1&"A1"`, `$A$1+A2&"A1"`, ""},
		{"SUM(A1:A3)", "SUM(A2:A4)", ""},
		{"LOG10(A1)", "LOG10(A2)", ""},
	}
	for _, c := range cases {
		if got := shiftFormula(c[0], 0, 1); got != c[1] {
			t.Errorf("%s shifted: %s, want %s", c[0], got, c[1])
		}
	}
}
