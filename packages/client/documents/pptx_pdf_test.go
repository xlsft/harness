package documents

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func deckFixture(t *testing.T) []byte {
	t.Helper()
	data, err := Create("d.pptx", CreateSpec{Title: "Deck", Slides: []SlideSpec{
		{Layout: "title", Title: "Opening", Text: "Sub"},
		{Layout: "content", Title: "Plan", Bullets: []string{"One thing", "Two things"}, Notes: "Say hello"},
		{Layout: "section", Title: "Part two", Text: "More"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func shapeLine(t *testing.T, d Document, text string) (string, string) {
	t.Helper()
	return addrOf(t, readAll(t, d, ""), text)
}

func TestPPTXStructure(t *testing.T) {
	d := mustOpen(t, "d.pptx", deckFixture(t))
	s := d.Structure()
	for _, want := range []string{"3 slides", "Title and Content", `title "Plan"`, `notes "Say hello"`, "s257"} {
		if !strings.Contains(s, want) {
			t.Errorf("structure lacks %q:\n%s", want, s)
		}
	}
	r := readAll(t, d, "s257")
	for _, want := range []string{"s257/sp2", "placeholder=title", "x=838200", "(from layout)", "One thing ¶ Two things", "notes (not shown"} {
		if !strings.Contains(r, want) {
			t.Errorf("read lacks %q:\n%s", want, r)
		}
	}
	if !strings.Contains(d.Search("hello", 0), "notes") {
		t.Error("notes are not searched")
	}
	noErrors(t, "d.pptx", deckFixture(t))
}

func TestPPTXTextEdits(t *testing.T) {
	data := deckFixture(t)
	d := mustOpen(t, "d.pptx", data)
	addr, fp := shapeLine(t, d, "One thing")
	res := apply(t, "d.pptx", data, Edit{Op: "set_text", Target: addr, Fingerprint: fp, Text: "A\nB\nC"})
	onlyChanged(t, data, res.Data, "ppt/slides/slide2.xml")
	if !strings.Contains(readAll(t, mustOpen(t, "d.pptx", res.Data), "s257"), "A ¶ B ¶ C") {
		t.Error("set_text")
	}
	res = apply(t, "d.pptx", data, Edit{Op: "replace_text", Find: "Two things", Text: "2 things"})
	if !strings.Contains(partOf(t, res.Data, "ppt/slides/slide2.xml"), "2 things") {
		t.Error("replace_text")
	}
	refused(t, "d.pptx", data, "fingerprint", Edit{Op: "set_text", Target: addr, Text: "x"})
	refused(t, "d.pptx", data, "no longer has fingerprint", Edit{Op: "set_text", Target: addr, Fingerprint: "00000000", Text: "x"})
	two := apply(t, "d.pptx", data, Edit{Op: "replace_text", Find: "One thing", Text: "Part"}).Data
	refused(t, "d.pptx", two, "matches", Edit{Op: "replace_text", Find: "Part", Text: "x"})
}

func TestPPTXReplaceAcrossRunsKeepsRuns(t *testing.T) {
	data := deckFixture(t)
	c := NewChanges()
	pkg, _ := OpenPackage(data)
	x := partOf(t, data, "ppt/slides/slide2.xml")
	c.Set["ppt/slides/slide2.xml"] = []byte(strings.Replace(x, `<a:r><a:rPr lang="en-US"/><a:t>One thing</a:t></a:r>`, `<a:r><a:rPr b="1"/><a:t>One </a:t></a:r><a:r><a:rPr i="1"/><a:t>thing here</a:t></a:r>`, 1))
	mixed, err := pkg.Write(c)
	if err != nil {
		t.Fatal(err)
	}
	res := apply(t, "d.pptx", mixed, Edit{Op: "replace_text", Find: "One thing", Text: "Z"})
	got := partOf(t, res.Data, "ppt/slides/slide2.xml")
	if !strings.Contains(got, `<a:rPr b="1"/><a:t>Z</a:t>`) || !strings.Contains(got, `<a:rPr i="1"/><a:t> here</a:t>`) {
		t.Fatalf("%s", got)
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "Mixed formatting") {
		t.Error("mixed formatting not reported")
	}
}

func TestPPTXGeometry(t *testing.T) {
	data := deckFixture(t)
	d := mustOpen(t, "d.pptx", data)
	addr, fp := shapeLine(t, d, "One thing")
	x, y := int64(100), int64(200)
	cx, cy := int64(3000000), int64(1000000)
	res := apply(t, "d.pptx", data, Edit{Op: "set_geometry", Target: addr, Fingerprint: fp, X: &x, Y: &y, CX: &cx, CY: &cy})
	if !strings.Contains(partOf(t, res.Data, "ppt/slides/slide2.xml"), `<a:off x="100" y="200"/><a:ext cx="3000000" cy="1000000"/>`) {
		t.Fatal(partOf(t, res.Data, "ppt/slides/slide2.xml"))
	}
	noErrors(t, "d.pptx", res.Data)
	// Now it has its own geometry: a second edit changes it in place.
	d2 := mustOpen(t, "d.pptx", res.Data)
	addr2, fp2 := shapeLine(t, d2, "One thing")
	x2 := int64(555)
	res2 := apply(t, "d.pptx", res.Data, Edit{Op: "set_geometry", Target: addr2, Fingerprint: fp2, X: &x2})
	if !strings.Contains(partOf(t, res2.Data, "ppt/slides/slide2.xml"), `<a:off x="555" y="200"/>`) {
		t.Fatal(partOf(t, res2.Data, "ppt/slides/slide2.xml"))
	}
}

func keysOf(fs []Finding, prefix string) []string {
	var out []string
	for _, f := range fs {
		if strings.HasPrefix(f.Key, prefix) {
			out = append(out, f.Key)
		}
	}
	return out
}

func TestPPTXLayoutChecks(t *testing.T) {
	data := deckFixture(t)
	big := int64(99_000_000)
	small := int64(1000)
	cx := int64(4000000)
	cy := int64(1000000)
	res := apply(t, "d.pptx", data,
		Edit{Op: "add_textbox", Target: "s256", X: &big, Y: &small, CX: &cx, CY: &cy, Text: "Far away"},
		Edit{Op: "add_textbox", Target: "s258", X: &small, Y: &small, CX: &cx, CY: &cy, Text: "Click to add title"},
		Edit{Op: "add_textbox", Target: "s258", X: &small, Y: &small, CX: &cx, CY: &cy, Text: "On top"},
	)
	fs := mustOpen(t, "d.pptx", res.Data).Check()
	if len(keysOf(fs, "pptx:off-slide:s256")) != 1 {
		t.Errorf("off-slide: %v", fs)
	}
	if len(keysOf(fs, "pptx:leftover:s258")) != 1 {
		t.Errorf("leftover: %v", fs)
	}
	if len(keysOf(fs, "pptx:overlap:s258")) != 1 {
		t.Errorf("overlap: %v", fs)
	}
}

func TestPPTXOverflowNeedsFontMetrics(t *testing.T) {
	old := FontDirs
	defer func() { FontDirs = old; ResetFontIndex() }()
	FontDirs = func() []string { return []string{t.TempDir()} }
	ResetFontIndex()
	data := deckFixture(t)
	cx := int64(500000)
	cy := int64(200000)
	x := int64(1000)
	res := apply(t, "d.pptx", data, Edit{Op: "add_textbox", Target: "s256", X: &x, Y: &x, CX: &cx, CY: &cy, Text: strings.Repeat("Too much text. ", 40)})
	fs := mustOpen(t, "d.pptx", res.Data).Check()
	if u := keysOf(fs, "pptx:fit-font:"); len(u) == 0 {
		t.Fatalf("a missing font must report unverified: %v", fs)
	}
	for _, f := range fs {
		if strings.HasPrefix(f.Key, "pptx:fit-font:") && f.Severity != SeverityUnverified {
			t.Errorf("severity %s", f.Severity)
		}
	}
}

func TestPPTXOverflowMeasured(t *testing.T) {
	ResetFontIndex()
	defer ResetFontIndex()
	data := deckFixture(t)
	cx := int64(500000)
	cy := int64(200000)
	x := int64(1000)
	res := apply(t, "d.pptx", data, Edit{Op: "add_textbox", Target: "s256", X: &x, Y: &x, CX: &cx, CY: &cy, Text: strings.Repeat("Too much text. ", 40)})
	fs := mustOpen(t, "d.pptx", res.Data).Check()
	if fonts.lookup("Arial") == nil {
		t.Skip("no Arial on this machine; the unverified path has its own test")
	}
	if len(keysOf(fs, "pptx:overflow:")) == 0 {
		t.Errorf("overflow not seen: %v", fs)
	}
}

func TestPPTXSlideOperations(t *testing.T) {
	data := deckFixture(t)
	// Add from a layout.
	res := apply(t, "d.pptx", data, Edit{Op: "add_slide", Layout: "Title and Content", Title: "New", Text: "Body", Position: 2})
	noErrors(t, "d.pptx", res.Data)
	d := mustOpen(t, "d.pptx", res.Data)
	s := d.Structure()
	if !strings.Contains(s, `2. s259`) || !strings.Contains(s, `title "New"`) {
		t.Errorf("add: %s", s)
	}
	refused(t, "d.pptx", data, "no layout", Edit{Op: "add_slide", Layout: "Nope"})

	// Move keeps ids.
	res = apply(t, "d.pptx", data, Edit{Op: "move_slide", Target: "s258", Position: 1})
	if s := mustOpen(t, "d.pptx", res.Data).Structure(); !strings.Contains(s, "1. s258") || !strings.Contains(s, "3. s257") {
		t.Errorf("move: %s", s)
	}
	onlyChanged(t, data, res.Data, "ppt/presentation.xml")

	// Delete takes the notes along and leaves nothing dangling.
	res = apply(t, "d.pptx", data, Edit{Op: "delete_slide", Target: "s257"})
	noErrors(t, "d.pptx", res.Data)
	if hasPart(t, res.Data, "ppt/slides/slide2.xml") || hasPart(t, res.Data, "ppt/notesSlides/notesSlide1.xml") || hasPart(t, res.Data, "ppt/slides/_rels/slide2.xml.rels") {
		t.Error("parts of the deleted slide stayed")
	}
	if strings.Contains(partOf(t, res.Data, contentTypesPart), "slide2.xml") || strings.Contains(partOf(t, res.Data, "ppt/_rels/presentation.xml.rels"), "slide2.xml") {
		t.Error("references to the deleted slide stayed")
	}
	if !hasPart(t, res.Data, "ppt/slideLayouts/slideLayout2.xml") {
		t.Error("a shared layout went away")
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "Say hello") {
		t.Error("the lost notes were not reported")
	}

	one, _ := Create("o.pptx", CreateSpec{Slides: []SlideSpec{{Layout: "blank"}}})
	refused(t, "o.pptx", one, "only slide", Edit{Op: "delete_slide", Target: "s256"})
}

func TestPPTXCloneSharesAndCopies(t *testing.T) {
	data := deckFixture(t)
	// Give slide 2 a chart with an embedded workbook.
	c := NewChanges()
	pkg, _ := OpenPackage(data)
	c.Set["ppt/charts/chart1.xml"] = []byte(`<c:chartSpace xmlns:c="urn:c"/>`)
	c.Set["ppt/embeddings/wb1.xlsx"] = []byte("workbook")
	c.Set["ppt/charts/_rels/chart1.xml.rels"] = []byte(xmlHeader + `<Relationships xmlns="` + pkgRelNS + `"><Relationship Id="rId1" Type="` + relNS + `/package" Target="../embeddings/wb1.xlsx"/></Relationships>`)
	c.Set["ppt/media/image1.png"] = []byte("png")
	if err := c.addContentType(pkg, "ppt/charts/chart1.xml", "application/vnd.openxmlformats-officedocument.drawingml.chart+xml"); err != nil {
		t.Fatal(err)
	}
	if err := c.addContentType(pkg, "ppt/embeddings/wb1.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"); err != nil {
		t.Fatal(err)
	}
	if err := c.addContentType(pkg, "ppt/media/image1.png", "image/png"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.addRelationship(pkg, "ppt/slides/slide2.xml", relNS+"/chart", "../charts/chart1.xml", false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.addRelationship(pkg, "ppt/slides/slide2.xml", relNS+"/image", "../media/image1.png", false); err != nil {
		t.Fatal(err)
	}
	withChart, err := pkg.Write(c)
	if err != nil {
		t.Fatal(err)
	}
	res := apply(t, "d.pptx", withChart, Edit{Op: "add_slide", CloneOf: "s257"})
	noErrors(t, "d.pptx", res.Data)
	notes := strings.Join(res.Notes, " ")
	if !strings.Contains(notes, "chart1.xml") || !strings.Contains(notes, "notesSlide1.xml") {
		t.Errorf("clone not reported: %s", notes)
	}
	rels := partOf(t, res.Data, "ppt/slides/_rels/slide4.xml.rels")
	if !strings.Contains(rels, "/ppt/charts/chart1000.xml") || !strings.Contains(rels, "/ppt/media/image1.png") || strings.Contains(rels, "notesSlide") {
		t.Errorf("clone rels: %s", rels)
	}
	if !hasPart(t, res.Data, "ppt/embeddings/wb1000.xlsx") {
		t.Error("the chart's workbook was not copied")
	}
	onlyNew := rawEntries(t, withChart)
	if rawEntries(t, res.Data)["ppt/charts/chart1.xml"] != onlyNew["ppt/charts/chart1.xml"] {
		t.Error("the source's chart changed")
	}
}

func TestPPTXSectionsStayInStep(t *testing.T) {
	data := deckFixture(t)
	pres := partOf(t, data, "ppt/presentation.xml")
	sect := `<p:extLst><p:ext uri="{521415D9-36F7-43E2-AB2F-B90AF26B5E84}"><p14:sectionLst xmlns:p14="urn:p14"><p14:section name="A" id="{1}"><p14:sldIdLst><p14:sldId id="256"/><p14:sldId id="257"/></p14:sldIdLst></p14:section><p14:section name="B" id="{2}"><p14:sldIdLst><p14:sldId id="258"/></p14:sldIdLst></p14:section></p14:sectionLst></p:ext></p:extLst>`
	pkg, _ := OpenPackage(data)
	c := NewChanges()
	c.Set["ppt/presentation.xml"] = []byte(strings.Replace(pres, "</p:presentation>", sect+"</p:presentation>", 1))
	with, err := pkg.Write(c)
	if err != nil {
		t.Fatal(err)
	}
	res := apply(t, "d.pptx", with, Edit{Op: "delete_slide", Target: "s257"})
	if strings.Contains(partOf(t, res.Data, "ppt/presentation.xml"), `p14:sldId id="257"`) {
		t.Error("the section list kept a deleted slide")
	}
	res = apply(t, "d.pptx", with, Edit{Op: "add_slide", Layout: "Blank", Position: 2})
	if !strings.Contains(partOf(t, res.Data, "ppt/presentation.xml"), `<p14:sldId id="256"/><p14:sldId id="259"/>`) {
		t.Errorf("section: %s", partOf(t, res.Data, "ppt/presentation.xml"))
	}
}

// --- PDF ---

func pdfFixture(t *testing.T, pages int) []byte {
	t.Helper()
	var blocks []Block
	for i := 1; i <= pages; i++ {
		blocks = append(blocks, Block{Type: "heading", Text: fmt.Sprintf("Page heading %d", i)}, Block{Type: "paragraph", Text: fmt.Sprintf("Body text number %d (with parens).", i)})
		if i < pages {
			blocks = append(blocks, Block{Type: "page_break"})
		}
	}
	data, err := Create("p.pdf", CreateSpec{Blocks: blocks})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPDFStructureAndText(t *testing.T) {
	d := mustOpen(t, "p.pdf", pdfFixture(t, 3))
	s := d.Structure()
	if !strings.Contains(s, "3 pages") || !strings.Contains(s, "chars") {
		t.Errorf("%s", s)
	}
	r := readAll(t, d, "2")
	if !strings.Contains(r, "Body text number 2 (with parens).") || strings.Contains(r, "number 3") {
		t.Errorf("%s", r)
	}
	if !strings.Contains(d.Search("number 3", 0), "page 3") {
		t.Errorf("%s", d.Search("number 3", 0))
	}
	if _, err := d.Read(Selector{Range: "9"}); err == nil {
		t.Error("page 9 does not exist")
	}
	for _, f := range d.Check() {
		if f.Severity == SeverityError {
			t.Errorf("%s", f.Message)
		}
	}
}

func TestPDFPageOperations(t *testing.T) {
	data := pdfFixture(t, 4)
	res := apply(t, "p.pdf", data, Edit{Op: "rotate", Pages: "2", Degrees: 90})
	d := mustOpen(t, "p.pdf", res.Data).(*pdfDoc)
	d.measure()
	if d.pages[1].rotate != 90 || d.pages[0].rotate != 0 {
		t.Errorf("rotate: %d %d", d.pages[1].rotate, d.pages[0].rotate)
	}
	refused(t, "p.pdf", data, "multiple of 90", Edit{Op: "rotate", Degrees: 45})
	refused(t, "p.pdf", data, "does not exist", Edit{Op: "rotate", Pages: "9", Degrees: 90})

	res = apply(t, "p.pdf", data, Edit{Op: "delete_pages", Pages: "1,3"})
	if n, _ := PDFPageCount(res.Data); n != 2 {
		t.Errorf("delete: %d pages", n)
	}
	refused(t, "p.pdf", data, "every page", Edit{Op: "delete_pages", Pages: "1-4"})

	part, n, err := ExtractPDFPages(data, "2-3")
	if err != nil || n != 2 {
		t.Fatalf("split: %v %d", err, n)
	}
	if r := readAll(t, mustOpen(t, "p.pdf", part), ""); !strings.Contains(r, "number 2") || strings.Contains(r, "number 4") {
		t.Errorf("split: %s", r)
	}
	merged, err := MergePDFs([][]byte{pdfFixture(t, 2), pdfFixture(t, 3)})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := PDFPageCount(merged); n != 5 {
		t.Errorf("merge: %d pages", n)
	}
}

// formPDF writes a PDF with an AcroForm, by hand from ISO 32000 section 12.7.
func formPDF(t *testing.T, extraAcro string) []byte {
	t.Helper()
	var out bytes.Buffer
	out.WriteString("%PDF-1.5\n")
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	obj(`<</Type/Catalog/Pages 2 0 R/AcroForm<</Fields[4 0 R 5 0 R]/DA(/Helv 0 Tf 0 g)/DR<</Font<</Helv 6 0 R>>>>` + extraAcro + `>>>>`)
	obj(`<</Type/Pages/Count 1/Kids[3 0 R]>>`)
	obj(`<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Resources<</Font<</Helv 6 0 R>>>>/Annots[4 0 R 5 0 R]>>`)
	obj(`<</Type/Annot/Subtype/Widget/FT/Tx/T(name)/V(old)/Rect[100 700 300 720]/DA(/Helv 12 Tf 0 g)/F 4>>`)
	obj(`<</Type/Annot/Subtype/Widget/FT/Tx/T(city)/V()/Rect[100 660 300 680]/DA(/Helv 12 Tf 0 g)/F 4>>`)
	obj(`<</Type/Font/Subtype/Type1/BaseFont/Helvetica/Encoding/WinAnsiEncoding>>`)
	obj("<</Length 5>>\nstream\n<xdp>\nendstream")
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&out, "trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return out.Bytes()
}

func TestPDFForms(t *testing.T) {
	data := formPDF(t, "")
	d := mustOpen(t, "f.pdf", data)
	list, ok := Fields(d)
	if !ok || !strings.Contains(list, `"name"`) || !strings.Contains(list, `"old"`) {
		t.Fatalf("%q", list)
	}
	res := apply(t, "f.pdf", data, Edit{Op: "fill_form", Fields: map[string]any{"name": "Ann", "city": "Riga"}})
	list, _ = Fields(mustOpen(t, "f.pdf", res.Data))
	if !strings.Contains(list, `"Ann"`) || !strings.Contains(list, `"Riga"`) {
		t.Fatalf("%q", list)
	}
	msg := refused(t, "f.pdf", data, "no field", Edit{Op: "fill_form", Fields: map[string]any{"nope": "x"}})
	if !strings.Contains(msg, `"city"`) {
		t.Errorf("no candidates: %s", msg)
	}
}

func TestPDFXFAAndSignedAreReportedNotFilled(t *testing.T) {
	xfa := formPDF(t, "/XFA[(preamble) 7 0 R]")
	d := mustOpen(t, "f.pdf", xfa)
	if !strings.Contains(d.Structure(), "XFA") {
		t.Errorf("%s", d.Structure())
	}
	refused(t, "f.pdf", xfa, "XFA", Edit{Op: "fill_form", Fields: map[string]any{"name": "x"}})
	signed := formPDF(t, "/SigFlags 3")
	d = mustOpen(t, "f.pdf", signed)
	if !strings.Contains(d.Structure(), "SIGNED") {
		t.Errorf("%s", d.Structure())
	}
	refused(t, "f.pdf", signed, "signed", Edit{Op: "fill_form", Fields: map[string]any{"name": "x"}})
	refused(t, "f.pdf", signed, "signed", Edit{Op: "rotate", Degrees: 90})
}

func TestPDFScannedPageDetection(t *testing.T) {
	d := &pdfDoc{pages: []*pdfPage{{number: 1, chars: 500}, {number: 2, chars: 0, images: 1}, {number: 3, chars: 0}}, measured: true}
	if s := d.scanned(); len(s) != 1 || s[0] != 2 {
		t.Errorf("%v", s)
	}
}

func TestPDFTextDecoding(t *testing.T) {
	cmap := parseToUnicode([]byte(`1 begincodespacerange <0000> <FFFF> endcodespacerange 2 beginbfchar <0001> <0041> <0002> <0042> endbfchar 1 beginbfrange <0010> <0012> <0061> endbfrange beginbfrange <0020> <0021> [<0078> <0079>] endbfrange`))
	if cmap[1] != "A" || cmap[0x11] != "b" || cmap[0x21] != "y" {
		t.Errorf("%v", cmap)
	}
	bad := false
	text, _ := extractText([]byte(`BT /F1 12 Tf (Hi \(there\)) Tj 0 -14 Td [(a) -300 (b)] TJ /F2 12 Tf <00010002> Tj ET`), map[string]*pdfFont{"F2": {twoByte: true, cmap: cmap}}, 0, &bad)
	if !strings.Contains(text, "Hi (there)") || !strings.Contains(text, "a b") || !strings.Contains(text, "AB") {
		t.Errorf("%q", text)
	}
}

// --- Limits and package rules ---

func TestLimitsAndHostileInput(t *testing.T) {
	if _, err := ParseXML([]byte(`<!DOCTYPE a [<!ENTITY x "y">]><a>&x;</a>`)); err == nil {
		t.Error("a DOCTYPE must be refused")
	}
	if _, err := Open("a.docx", []byte("plain text")); err == nil {
		t.Error("not a package")
	}
	if _, err := Open("a.pdf", []byte("plain text")); err == nil {
		t.Error("not a pdf")
	}
	if _, err := Open("a.docx", []byte{0xD0, 0xCF, 0x11, 0xE0, 0, 0}); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Errorf("legacy: %v", err)
	}
	if _, err := Open("a.pdf", []byte("%PDF-1.4\ngarbage")); err == nil {
		t.Error("a broken pdf must be an error, not a panic")
	}

	var unsafe bytes.Buffer
	w := zip.NewWriter(&unsafe)
	fw, _ := w.Create("../evil.xml")
	_, _ = fw.Write([]byte("x"))
	_ = w.Close()
	if _, err := OpenPackage(unsafe.Bytes()); err == nil {
		t.Error("a path traversal name must be refused")
	}

	// A bomb: far past the part limit once inflated, compressing very well.
	var bomb bytes.Buffer
	w = zip.NewWriter(&bomb)
	fw, _ = w.Create("word/document.xml")
	chunk := make([]byte, 1<<20)
	for i := 0; i < MinRatioBytes>>20+8; i++ {
		_, _ = fw.Write(chunk)
	}
	_ = w.Close()
	if _, err := OpenPackage(bomb.Bytes()); err == nil || !strings.Contains(err.Error(), "bomb") {
		t.Errorf("bomb: %v", err)
	}
}

func TestPackageCheckFindsDefects(t *testing.T) {
	data := docxFixture(t, false, "")
	pkg, _ := OpenPackage(data)
	c := NewChanges()
	c.Set["word/document.xml"] = []byte(`<w:document xmlns:w="` + wordNSTransitional + `"><w:body><w:p></w:body>`)
	broken, _ := pkg.Write(c)
	v := Verify("a.docx", data, broken, t.TempDir())
	if !v.HasNewErrors() {
		t.Errorf("broken XML was not a new error: %+v", v)
	}

	c = NewChanges()
	c.Set["word/_rels/document.xml.rels"] = []byte(strings.Replace(partOf(t, data, "word/_rels/document.xml.rels"), `</Relationships>`, `<Relationship Id="rId9" Type="`+relNS+`/image" Target="media/gone.png"/></Relationships>`, 1))
	dangling, _ := pkg.Write(c)
	found := false
	for _, f := range mustOpen(t, "a.docx", dangling).Check() {
		found = found || strings.HasPrefix(f.Key, "package:rel-target:")
	}
	if !found {
		t.Error("a dangling relationship went unseen")
	}
}

func TestSpliceKeepsOtherEntriesByteForByte(t *testing.T) {
	data := docxFixture(t, false, "")
	addr, fp := addrOf(t, readAll(t, mustOpen(t, "a.docx", data), ""), "The end.")
	res := apply(t, "a.docx", data, Edit{Op: "set_text", Target: addr, Fingerprint: fp, Text: "Done."})
	onlyChanged(t, data, res.Data, "word/document.xml")
	// The order of the entries stays.
	names := func(b []byte) string {
		p, _ := OpenPackage(b)
		return strings.Join(p.Names(), ",")
	}
	if names(data) != names(res.Data) {
		t.Errorf("entry order changed:\n%s\n%s", names(data), names(res.Data))
	}
}

func TestCreateAllFormats(t *testing.T) {
	cases := []struct {
		name string
		spec CreateSpec
		want []string
	}{
		{"n.docx", CreateSpec{Title: "T", Blocks: []Block{{Type: "heading", Level: 2, Text: "H"}, {Type: "bullet", Text: "b"}, {Type: "numbered", Text: "n"}, {Type: "table", Rows: [][]string{{"a", "b"}, {"c"}}}, {Type: "page_break"}, {Type: "paragraph", Text: "p & q"}}}, []string{"Heading2", "t0 r1 c0", "p & q"}},
		{"n.xlsx", CreateSpec{Sheets: []SheetSpec{{Name: "S", HeaderRow: true, Rows: [][]any{{"h", "i"}, {2.0, "=A2*3"}, {map[string]any{"date": "2026-11-07"}, nil, true}}}}}, []string{"=A2*3 → 6", "2026-11-07", "TRUE"}},
		{"n.pptx", CreateSpec{Slides: []SlideSpec{{Title: "S", Bullets: []string{"x"}}}}, []string{"placeholder=title", "x"}},
		{"n.pdf", CreateSpec{Title: "T", Blocks: []Block{{Type: "heading", Text: "H"}, {Type: "bullet", Text: "b"}, {Type: "table", Rows: [][]string{{"a", "b"}}}}}, []string{"H", "a  |  b"}},
	}
	for _, c := range cases {
		data, err := Create(c.name, c.spec)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		noErrors(t, c.name, data)
		r := readAll(t, mustOpen(t, c.name, data), "")
		for _, w := range c.want {
			if !strings.Contains(r, w) {
				t.Errorf("%s lacks %q:\n%s", c.name, w, r)
			}
		}
	}
	if _, err := Create("n.pdf", CreateSpec{Title: "日本語"}); err == nil {
		t.Error("the simple PDF writer must refuse what it cannot encode")
	}
	if _, err := Create("n.dotx", CreateSpec{}); err == nil {
		t.Error("a template extension needs a template")
	}
}

func TestRetargetTemplate(t *testing.T) {
	data, _ := Create("n.docx", CreateSpec{})
	pkg, _ := OpenPackage(data)
	c := NewChanges()
	c.Set[contentTypesPart] = []byte(strings.Replace(partOf(t, data, contentTypesPart), "document.main+xml", "template.main+xml", 1))
	tmpl, _ := pkg.Write(c)
	out, err := RetargetTemplate("new.docx", tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(partOf(t, out, contentTypesPart), "wordprocessingml.document.main+xml") {
		t.Error("the content type still says template")
	}
}

func TestVerifyLeavesNoFilesBehind(t *testing.T) {
	dir := t.TempDir()
	data := docxFixture(t, false, "")
	Verify("a.docx", nil, data, dir)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
	v := Verify("a.docx", nil, data, filepath.Join(dir, "missing"))
	if len(v.Unverified) == 0 {
		t.Error("a failed copy must be could-not-verify, not a pass")
	}
}
