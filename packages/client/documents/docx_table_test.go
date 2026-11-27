package documents

import (
	"strings"
	"testing"
)

// rowsTable is the second table of the fixture: a header row, two data rows with formatted text, and two rows that
// share a vertically merged cell.
const rowsTable = `<w:tbl><w:tblPr/><w:tblGrid><w:gridCol w:w="100"/><w:gridCol w:w="100"/></w:tblGrid>` +
	`<w:tr><w:trPr><w:tblHeader/></w:trPr><w:tc><w:p><w:r><w:t>Name</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Qty</w:t></w:r></w:p></w:tc></w:tr>` +
	`<w:tr><w:tc><w:tcPr><w:tcW w:w="100" w:type="dxa"/></w:tcPr><w:p><w:pPr><w:jc w:val="left"/></w:pPr><w:r><w:rPr><w:b/></w:rPr><w:t>Apples</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>3</w:t></w:r></w:p></w:tc></w:tr>` +
	`<w:tr><w:tc><w:p><w:r><w:t>Pears</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>5</w:t></w:r></w:p></w:tc></w:tr>` +
	`<w:tr><w:tc><w:tcPr><w:vMerge w:val="restart"/></w:tcPr><w:p><w:r><w:t>Merged</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>7</w:t></w:r></w:p></w:tc></w:tr>` +
	`<w:tr><w:tc><w:tcPr><w:vMerge/></w:tcPr><w:p/></w:tc><w:tc><w:p><w:r><w:t>8</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`

func rowAddr(t *testing.T, d Document, text string) (string, string) {
	t.Helper()
	return addrOf(t, readAll(t, d, "document:t1"), text)
}

func TestDOCXTableRowsAreListed(t *testing.T) {
	d := mustOpen(t, "a.docx", docxFixture(t, false, rowsTable))
	if s := d.Structure(); !strings.Contains(s, "document:t1: 5 rows × 2 cells, first row: Name | Qty") {
		t.Fatalf("structure:\n%s", s)
	}
	rows := readAll(t, d, "document:t1")
	for _, want := range []string{"document:t1/r0 #", "[header] | Name | Qty |", "document:t1/r2 #", "| Pears | 5 |"} {
		if !strings.Contains(rows, want) {
			t.Errorf("rows lack %q:\n%s", want, rows)
		}
	}
}

func TestDOCXInsertDeleteMoveRows(t *testing.T) {
	data := docxFixture(t, false, rowsTable)
	d := mustOpen(t, "a.docx", data)
	addr, fp := rowAddr(t, d, "Apples")
	res := apply(t, "a.docx", data, Edit{Op: "insert_after", Target: addr, Fingerprint: fp, Text: "Plums\t9\nKiwis"})
	onlyChanged(t, data, res.Data, "word/document.xml")
	noErrors(t, "a.docx", res.Data)
	rows := readAll(t, mustOpen(t, "a.docx", res.Data), "document:t1")
	if !strings.Contains(rows, "document:t1/r2 #") || !strings.Contains(rows, "| Plums | 9 |") || !strings.Contains(rows, "| Kiwis |  |") {
		t.Fatalf("rows:\n%s", rows)
	}
	x := partOf(t, res.Data, "word/document.xml")
	// The copy takes the cell width, the alignment and the bold of the template, not its header flag.
	if !strings.Contains(x, `<w:tc><w:tcPr><w:tcW w:w="100" w:type="dxa"/></w:tcPr><w:p><w:pPr><w:jc w:val="left"/></w:pPr><w:r><w:rPr><w:b/></w:rPr><w:t xml:space="preserve">Plums</w:t>`) {
		t.Errorf("the new row does not look like its template:\n%s", x)
	}
	hAddr, hFP := rowAddr(t, d, "Name")
	res = apply(t, "a.docx", data, Edit{Op: "insert_after", Target: hAddr, Fingerprint: hFP, Text: "First\t1"})
	if strings.Count(partOf(t, res.Data, "word/document.xml"), "tblHeader") != 1 {
		t.Error("a row copied from the header became a header too")
	}
	refused(t, "a.docx", data, "has 2 cells", Edit{Op: "insert_before", Target: addr, Fingerprint: fp, Text: "a\tb\tc"})

	// Delete and move.
	pAddr, pFP := rowAddr(t, d, "Pears")
	res = apply(t, "a.docx", data, Edit{Op: "delete_row", Target: pAddr, Fingerprint: pFP})
	if strings.Contains(partOf(t, res.Data, "word/document.xml"), "Pears") {
		t.Error("the row is still there")
	}
	noErrors(t, "a.docx", res.Data)
	res = apply(t, "a.docx", data, Edit{Op: "move_row", Target: pAddr, Fingerprint: pFP, Position: 2})
	rows = readAll(t, mustOpen(t, "a.docx", res.Data), "document:t1")
	if strings.Index(rows, "Pears") > strings.Index(rows, "Apples") {
		t.Errorf("not moved:\n%s", rows)
	}
	noErrors(t, "a.docx", res.Data)
	res = apply(t, "a.docx", data, Edit{Op: "move_row", Target: hAddr, Fingerprint: hFP, Position: 3})
	if rows = readAll(t, mustOpen(t, "a.docx", res.Data), "document:t1"); !strings.Contains(rows, "document:t1/r2 #") || strings.Index(rows, "Name") < strings.Index(rows, "Pears") {
		t.Errorf("move down:\n%s", rows)
	}

	// Vertical merges and the last row are refused.
	mAddr, mFP := rowAddr(t, d, "| 8 |")
	refused(t, "a.docx", data, "vertically merged", Edit{Op: "insert_before", Target: mAddr, Fingerprint: mFP, Text: "x"})
	topAddr, topFP := rowAddr(t, d, "Merged")
	refused(t, "a.docx", data, "vertically merged", Edit{Op: "delete_row", Target: topAddr, Fingerprint: topFP})
	refused(t, "a.docx", data, "vertically merged", Edit{Op: "move_row", Target: topAddr, Fingerprint: topFP, Position: 1})
	only := readAll(t, d, "document:t0")
	oAddr, oFP := addrOf(t, only, "Cell one")
	refused(t, "a.docx", data, "only row", Edit{Op: "delete_row", Target: oAddr, Fingerprint: oFP})
	refused(t, "a.docx", data, "no longer has fingerprint", Edit{Op: "delete_row", Target: pAddr, Fingerprint: "00000000"})
}

func TestDOCXTrackedRows(t *testing.T) {
	data := docxFixture(t, true, rowsTable)
	d := mustOpen(t, "a.docx", data)
	addr, fp := rowAddr(t, d, "Apples")
	pAddr, pFP := rowAddr(t, d, "Pears")
	res := apply(t, "a.docx", data, Edit{Op: "insert_after", Target: addr, Fingerprint: fp, Text: "Plums\t9", Author: "Eve"}, Edit{Op: "delete_row", Target: pAddr, Fingerprint: pFP, Author: "Eve"})
	noErrors(t, "a.docx", res.Data)
	x := partOf(t, res.Data, "word/document.xml")
	if !strings.Contains(x, `<w:trPr><w:ins w:id=`) || !strings.Contains(x, `<w:trPr><w:del w:id=`) || !strings.Contains(x, "<w:delText xml:space=\"preserve\">Pears") {
		t.Fatalf("rows not tracked:\n%s", x)
	}
	refused(t, "a.docx", data, "track: false", Edit{Op: "move_row", Target: pAddr, Fingerprint: pFP, Position: 2})

	acc := apply(t, "a.docx", res.Data, Edit{Op: "accept_change", All: true})
	noErrors(t, "a.docx", acc.Data)
	ax := partOf(t, acc.Data, "word/document.xml")
	if strings.Contains(ax, "Pears") || !strings.Contains(ax, "Plums") || strings.Contains(ax, "<w:trPr><w:ins") {
		t.Errorf("accept:\n%s", ax)
	}
	rej := apply(t, "a.docx", res.Data, Edit{Op: "reject_change", All: true})
	noErrors(t, "a.docx", rej.Data)
	rx := partOf(t, rej.Data, "word/document.xml")
	if !strings.Contains(rx, "Pears") || strings.Contains(rx, "Plums") {
		t.Errorf("reject:\n%s", rx)
	}
}
