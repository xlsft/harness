package documents

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// All fixtures here are written from the file format specifications.

func init() {
	nowFunc = func() time.Time { return time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC) }
}

func zipOf(t *testing.T, parts ...[2]string) []byte {
	t.Helper()
	data, err := buildZip(parts)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

const wordMain = "application/vnd.openxmlformats-officedocument.wordprocessingml."

// docxFixture is a document with every story: a body with mixed-format runs, a table, a text box with a fallback copy, a
// hidden run, tracked changes, a comment, a table of contents, a header, a footer and a footnote.
func docxFixture(t *testing.T, tracking bool, extraBody string) []byte {
	t.Helper()
	settings := ""
	if tracking {
		settings = `<w:trackRevisions/>`
	}
	body := `<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:bookmarkStart w:id="1" w:name="_a"/><w:r><w:t>Introduction</w:t></w:r><w:bookmarkEnd w:id="1"/></w:p>` +
		`<w:p><w:r><w:rPr><w:b/></w:rPr><w:t xml:space="preserve">Bold start </w:t></w:r><w:r><w:rPr><w:i/></w:rPr><w:t>italic end</w:t></w:r><w:r><w:t xml:space="preserve"> and plain text.</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t xml:space="preserve">Repeat word, repeat word.</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t xml:space="preserve">Visible </w:t></w:r><w:r><w:rPr><w:vanish/></w:rPr><w:t>secret hidden</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t xml:space="preserve">Kept </w:t></w:r><w:ins w:id="10" w:author="Ann" w:date="2026-01-01T00:00:00Z"><w:r><w:t>added</w:t></w:r></w:ins><w:del w:id="11" w:author="Bob" w:date="2026-01-01T00:00:00Z"><w:r><w:delText>removed</w:delText></w:r></w:del></w:p>` +
		`<w:p><w:commentRangeStart w:id="5"/><w:r><w:t>Commented text</w:t></w:r><w:commentRangeEnd w:id="5"/><w:r><w:commentReference w:id="5"/></w:r></w:p>` +
		`<w:tbl><w:tblPr/><w:tblGrid><w:gridCol w:w="100"/></w:tblGrid><w:tr><w:tc><w:tcPr/><w:p><w:r><w:t>Cell one</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Cell two</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` +
		`<w:p><w:r><mc:AlternateContent><mc:Choice Requires="wps"><w:drawing><wps:txbx><w:txbxContent><w:p><w:r><w:t>Box text</w:t></w:r></w:p></w:txbxContent></wps:txbx></w:drawing></mc:Choice><mc:Fallback><w:pict><v:textbox><w:txbxContent><w:p><w:r><w:t>Box text</w:t></w:r></w:p></w:txbxContent></v:textbox></w:pict></mc:Fallback></mc:AlternateContent></w:r></w:p>` +
		`<w:p><w:r><w:t xml:space="preserve">See note</w:t></w:r><w:r><w:footnoteReference w:id="2"/></w:r></w:p>` +
		extraBody +
		`<w:p><w:r><w:t>The end.</w:t></w:r></w:p><w:sectPr/>`
	ns := `xmlns:w="` + wordNSTransitional + `" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" xmlns:wps="urn:wps" xmlns:v="urn:v"`
	return zipOf(t,
		[2]string{contentTypesPart, contentTypes(
			[2]string{"/word/document.xml", wordMain + "document.main+xml"}, [2]string{"/word/styles.xml", wordMain + "styles+xml"},
			[2]string{"/word/settings.xml", wordMain + "settings+xml"}, [2]string{"/word/comments.xml", wordMain + "comments+xml"},
			[2]string{"/word/header1.xml", wordMain + "header+xml"}, [2]string{"/word/footer1.xml", wordMain + "footer+xml"},
			[2]string{"/word/footnotes.xml", wordMain + "footnotes+xml"}, [2]string{"/word/media/untouched.bin", "application/octet-stream"})},
		[2]string{"_rels/.rels", pkgRels("officeDocument", "word/document.xml")},
		[2]string{"docProps/core.xml", xmlHeader + fmt.Sprintf(coreProps, "t")},
		[2]string{"word/document.xml", xmlHeader + `<w:document ` + ns + `><w:body>` + body + `</w:body></w:document>`},
		[2]string{"word/_rels/document.xml.rels", xmlHeader + `<Relationships xmlns="` + pkgRelNS + `"><Relationship Id="rId1" Type="` + relNS + `/styles" Target="styles.xml"/><Relationship Id="rId2" Type="` + relNS + `/settings" Target="settings.xml"/><Relationship Id="rId3" Type="` + relNS + `/comments" Target="comments.xml"/><Relationship Id="rId4" Type="` + relNS + `/header" Target="header1.xml"/><Relationship Id="rId5" Type="` + relNS + `/footer" Target="footer1.xml"/><Relationship Id="rId6" Type="` + relNS + `/footnotes" Target="footnotes.xml"/></Relationships>`},
		[2]string{"word/styles.xml", xmlHeader + `<w:styles ` + wNS + `><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/></w:style><w:style w:type="paragraph" w:styleId="Quote"><w:name w:val="Quote"/></w:style></w:styles>`},
		[2]string{"word/settings.xml", xmlHeader + `<w:settings ` + wNS + `>` + settings + `</w:settings>`},
		[2]string{"word/comments.xml", xmlHeader + `<w:comments ` + wNS + `><w:comment w:id="5" w:author="Ann"><w:p><w:r><w:t>Check this</w:t></w:r></w:p></w:comment></w:comments>`},
		[2]string{"word/header1.xml", xmlHeader + `<w:hdr ` + wNS + `><w:p><w:r><w:t>Header words</w:t></w:r></w:p></w:hdr>`},
		[2]string{"word/footer1.xml", xmlHeader + `<w:ftr ` + wNS + `><w:p><w:r><w:t>Footer words</w:t></w:r></w:p></w:ftr>`},
		[2]string{"word/footnotes.xml", xmlHeader + `<w:footnotes ` + wNS + `><w:footnote w:type="separator" w:id="0"><w:p><w:r><w:separator/></w:r></w:p></w:footnote><w:footnote w:id="2"><w:p><w:r><w:t>Note words</w:t></w:r></w:p></w:footnote></w:footnotes>`},
		[2]string{"word/media/untouched.bin", "binary-bytes-that-must-survive"},
	)
}

const tocField = `<w:p><w:r><w:fldChar w:fldCharType="begin"/></w:r><w:r><w:instrText xml:space="preserve"> TOC \o "1-3" </w:instrText></w:r><w:r><w:fldChar w:fldCharType="separate"/></w:r><w:r><w:t>Old heading .... 1</w:t></w:r><w:r><w:fldChar w:fldCharType="end"/></w:r></w:p>`

// xlsxFixture: two sheets, one hidden; formulas, a shared formula, merges, a table, a date, a protected sheet, a defined
// name, a calc chain and an external link.
func xlsxFixture(t *testing.T) []byte {
	t.Helper()
	const sml = "application/vnd.openxmlformats-officedocument.spreadsheetml."
	sheet1 := `<worksheet xmlns="` + ssNS + `" xmlns:r="` + relNS + `"><dimension ref="A1:D6"/><sheetData>` +
		`<row r="1" spans="1:4"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>` +
		`<row r="2" spans="1:4"><c r="A2"><v>10</v></c><c r="B2"><v>20</v></c><c r="C2"><f>A2+B2</f><v>30</v></c><c r="D2"><f>C2*2</f><v>60</v></c></row>` +
		`<row r="3"><c r="A3"><v>1</v></c><c r="B3"><v>2</v></c><c r="C3"><f t="shared" ref="C3:C4" si="0">A3+B3</f><v>3</v></c></row>` +
		`<row r="4"><c r="A4"><v>5</v></c><c r="B4"><v>6</v></c><c r="C4"><f t="shared" si="0"/><v>11</v></c></row>` +
		`<row r="5" hidden="1"><c r="A5" s="1"><v>46000</v></c><c r="B5"><f>VLOOKUP(A2,A2:B4,2,FALSE)</f><v>20</v></c><c r="C5"><f>Rate*A2</f><v>5</v></c></row>` +
		`<row r="6"><c r="A6" t="s"><v>2</v></c><c r="B6"/><c r="D6" t="e"><v>#DIV/0!</v></c></row>` +
		`</sheetData><mergeCells count="1"><mergeCell ref="A6:B6"/></mergeCells><tableParts count="1"><tablePart r:id="rId1"/></tableParts></worksheet>`
	sheet2 := `<worksheet xmlns="` + ssNS + `"><sheetProtection sheet="1"/><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData></worksheet>`
	wb := `<workbook xmlns="` + ssNS + `" xmlns:r="` + relNS + `"><sheets><sheet name="Data" sheetId="1" r:id="rId1"/><sheet name="Locked" sheetId="2" r:id="rId2" state="hidden"/></sheets><definedNames><definedName name="Rate">Data!$B$3</definedName></definedNames><calcPr calcId="1"/></workbook>`
	styles := `<styleSheet xmlns="` + ssNS + `"><cellXfs count="2"><xf numFmtId="0"/><xf numFmtId="14"><alignment horizontal="left"/></xf></cellXfs></styleSheet>`
	return zipOf(t,
		[2]string{contentTypesPart, contentTypes(
			[2]string{"/xl/workbook.xml", sml + "sheet.main+xml"}, [2]string{"/xl/worksheets/sheet1.xml", sml + "worksheet+xml"}, [2]string{"/xl/worksheets/sheet2.xml", sml + "worksheet+xml"},
			[2]string{"/xl/sharedStrings.xml", sml + "sharedStrings+xml"}, [2]string{"/xl/styles.xml", sml + "styles+xml"}, [2]string{"/xl/calcChain.xml", sml + "calcChain+xml"},
			[2]string{"/xl/tables/table1.xml", sml + "table+xml"}, [2]string{"/xl/externalLinks/externalLink1.xml", sml + "externalLink+xml"})},
		[2]string{"_rels/.rels", pkgRels("officeDocument", "xl/workbook.xml")},
		[2]string{"docProps/core.xml", xmlHeader + fmt.Sprintf(coreProps, "t")},
		[2]string{"xl/workbook.xml", xmlHeader + wb},
		[2]string{"xl/_rels/workbook.xml.rels", xmlHeader + `<Relationships xmlns="` + pkgRelNS + `"><Relationship Id="rId1" Type="` + relNS + `/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="` + relNS + `/worksheet" Target="worksheets/sheet2.xml"/><Relationship Id="rId3" Type="` + relNS + `/sharedStrings" Target="sharedStrings.xml"/><Relationship Id="rId4" Type="` + relNS + `/styles" Target="styles.xml"/><Relationship Id="rId5" Type="` + relNS + `/calcChain" Target="calcChain.xml"/><Relationship Id="rId6" Type="` + relNS + `/externalLink" Target="externalLinks/externalLink1.xml"/></Relationships>`},
		[2]string{"xl/worksheets/sheet1.xml", xmlHeader + sheet1},
		[2]string{"xl/worksheets/_rels/sheet1.xml.rels", xmlHeader + `<Relationships xmlns="` + pkgRelNS + `"><Relationship Id="rId1" Type="` + relNS + `/table" Target="../tables/table1.xml"/></Relationships>`},
		[2]string{"xl/worksheets/sheet2.xml", xmlHeader + sheet2},
		[2]string{"xl/sharedStrings.xml", xmlHeader + `<sst xmlns="` + ssNS + `" count="3"><si><t>Name</t></si><si><t>Size</t></si><si><r><t>Rich </t></r><r><t>text</t></r></si></sst>`},
		[2]string{"xl/styles.xml", xmlHeader + styles},
		[2]string{"xl/calcChain.xml", xmlHeader + `<calcChain xmlns="` + ssNS + `"><c r="C2" i="1"/><c r="D2"/><c r="C3"/><c r="C4"/><c r="B5"/><c r="C5"/></calcChain>`},
		[2]string{"xl/tables/table1.xml", xmlHeader + `<table xmlns="` + ssNS + `" id="1" name="T" displayName="T" ref="A1:B2"><tableColumns count="2"><tableColumn id="1" name="Name"/><tableColumn id="2" name="Size"/></tableColumns></table>`},
		[2]string{"xl/externalLinks/externalLink1.xml", xmlHeader + `<externalLink xmlns="` + ssNS + `"/>`},
	)
}

func mustOpen(t *testing.T, name string, data []byte) Document {
	t.Helper()
	d, err := Open(name, data)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	return d
}

func apply(t *testing.T, name string, data []byte, edits ...Edit) *EditResult {
	t.Helper()
	res, err := mustOpen(t, name, data).Apply(edits)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return res
}

func refused(t *testing.T, name string, data []byte, wantText string, edits ...Edit) string {
	t.Helper()
	res, err := mustOpen(t, name, data).Apply(edits)
	if err == nil {
		t.Fatalf("the edit should be refused (want %q), got %v", wantText, res.Changes)
	}
	if !strings.Contains(err.Error(), wantText) {
		t.Fatalf("refusal %q does not mention %q", err, wantText)
	}
	return err.Error()
}

func partOf(t *testing.T, data []byte, name string) string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			b, err := io.ReadAll(rc)
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
	}
	t.Fatalf("no part %s", name)
	return ""
}

func hasPart(t *testing.T, data []byte, name string) bool {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.File {
		if f.Name == name {
			return true
		}
	}
	return false
}

// rawEntries returns each entry's compressed bytes and header facts, to prove entries were copied untouched.
func rawEntries(t *testing.T, data []byte) map[string]string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range r.File {
		rc, err := f.OpenRaw()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = fmt.Sprintf("%d/%d/%x/%s", f.Method, f.CRC32, b, f.Modified)
	}
	return out
}

// onlyChanged fails unless exactly the named parts differ between two packages.
func onlyChanged(t *testing.T, before, after []byte, names ...string) {
	t.Helper()
	a, b := rawEntries(t, before), rawEntries(t, after)
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	for n, v := range a {
		if want[n] {
			continue
		}
		if b[n] != v {
			t.Errorf("part %s changed or went away", n)
		}
	}
	for n := range b {
		if _, ok := a[n]; !ok && !want[n] {
			t.Errorf("unexpected new part %s", n)
		}
	}
}

// addrOf finds the first listing line holding text and returns its address and fingerprint.
func addrOf(t *testing.T, listing, text string) (string, string) {
	t.Helper()
	for _, l := range strings.Split(listing, "\n") {
		if strings.Contains(l, text) {
			f := strings.Fields(l)
			for i, x := range f {
				if strings.HasPrefix(x, "#") && i > 0 {
					return f[i-1], strings.TrimPrefix(x, "#")
				}
			}
		}
	}
	t.Fatalf("no line with %q in:\n%s", text, listing)
	return "", ""
}

func readAll(t *testing.T, d Document, rng string) string {
	t.Helper()
	s, err := d.Read(Selector{Range: rng, Limit: 100000})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func noErrors(t *testing.T, name string, data []byte) {
	t.Helper()
	for _, f := range mustOpen(t, name, data).Check() {
		if f.Severity == SeverityError {
			t.Errorf("check: %s", f.Message)
		}
	}
}
