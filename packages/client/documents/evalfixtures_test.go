package documents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteEvalFixtures writes the binary files of the document tasks in evals/tasks, from our own code. It only runs when
// asked to:
//
//	HARNESS_WRITE_EVAL_FIXTURES=$PWD/../../evals/tasks go test ./documents -run TestWriteEvalFixtures
func TestWriteEvalFixtures(t *testing.T) {
	root := os.Getenv("HARNESS_WRITE_EVAL_FIXTURES")
	if root == "" {
		t.Skip("set HARNESS_WRITE_EVAL_FIXTURES to the evals/tasks folder to regenerate them")
	}
	write := func(rel string, data []byte) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A contract whose party name is split across differently formatted runs, in the body, a table and a header.
	contract := zipOf(t,
		[2]string{contentTypesPart, contentTypes(
			[2]string{"/word/document.xml", wordMain + "document.main+xml"}, [2]string{"/word/styles.xml", wordMain + "styles+xml"},
			[2]string{"/word/header1.xml", wordMain + "header+xml"}, [2]string{"/word/media/logo.bin", "application/octet-stream"})},
		[2]string{"_rels/.rels", pkgRels("officeDocument", "word/document.xml")},
		[2]string{"docProps/core.xml", xmlHeader + fmt.Sprintf(coreProps, "Contract")},
		[2]string{"word/document.xml", xmlHeader + `<w:document ` + wNS + `><w:body>` +
			`<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Services Agreement</w:t></w:r></w:p>` +
			`<w:p><w:r><w:t xml:space="preserve">This agreement is between </w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>ACME</w:t></w:r><w:r><w:rPr><w:b/><w:i/></w:rPr><w:t xml:space="preserve"> Corp</w:t></w:r><w:r><w:t xml:space="preserve"> and the Client.</w:t></w:r></w:p>` +
			`<w:p><w:r><w:t>The Client pays within 30 days.</w:t></w:r></w:p>` +
			`<w:tbl><w:tblPr/><w:tblGrid><w:gridCol w:w="4000"/><w:gridCol w:w="4000"/></w:tblGrid><w:tr><w:tc><w:tcPr><w:tcW w:w="4000" w:type="dxa"/></w:tcPr><w:p><w:r><w:t>Supplier</w:t></w:r></w:p></w:tc><w:tc><w:tcPr><w:tcW w:w="4000" w:type="dxa"/></w:tcPr><w:p><w:r><w:t>ACME Corp</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` +
			`<w:p><w:r><w:t>Signed for ACME Corp by its director.</w:t></w:r></w:p><w:sectPr/></w:body></w:document>`},
		[2]string{"word/_rels/document.xml.rels", xmlHeader + `<Relationships xmlns="` + pkgRelNS + `"><Relationship Id="rId1" Type="` + relNS + `/styles" Target="styles.xml"/><Relationship Id="rId2" Type="` + relNS + `/header" Target="header1.xml"/></Relationships>`},
		[2]string{"word/styles.xml", xmlHeader + `<w:styles ` + wNS + `><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/></w:style></w:styles>`},
		[2]string{"word/header1.xml", xmlHeader + `<w:hdr ` + wNS + `><w:p><w:r><w:t>Confidential: ACME Corp</w:t></w:r></w:p></w:hdr>`},
		[2]string{"word/media/logo.bin", "logo-bytes-that-must-survive"},
	)
	write("doc-docx-rename-party/repo/contract.docx", contract)
	write("doc-docx-rename-party/hidden/contract.docx", contract)
	write("doc-docx-rename-party/solution/contract.docx", apply(t, "c.docx", contract, Edit{Op: "replace_text", Find: "ACME Corp", Text: "Globex Ltd", All: true}).Data)

	// A budget: one input, a formula on another sheet, a hidden sheet that must stay.
	budget, err := Create("b.xlsx", CreateSpec{Sheets: []SheetSpec{
		{Name: "Inputs", HeaderRow: true, Rows: [][]any{{"Item", "Value"}, {"Unit price", 100.0}, {"Quantity", 10.0}}},
		{Name: "Summary", Rows: [][]any{{"Total", "=Inputs!B2*Inputs!B3"}, {"With tax", "=B1*1.2"}}},
		{Name: "Archive", Rows: [][]any{{"Do not touch", 7.0}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	budget = hideSheet(t, budget, "Archive")
	write("doc-xlsx-update-input/repo/budget.xlsx", budget)
	d := mustOpen(t, "b.xlsx", budget)
	write("doc-xlsx-update-input/solution/budget.xlsx", apply(t, "b.xlsx", budget, Edit{Op: "set_cell", Target: "Inputs!B2", Fingerprint: cellFP(t, d, "Inputs!B2"), Value: 125.0}).Data)

	// The read-only question: the answer sits on a hidden sheet.
	reserves, err := Create("r.xlsx", CreateSpec{Sheets: []SheetSpec{
		{Name: "Overview", Rows: [][]any{{"Revenue", 900.0}, {"Costs", 400.0}}},
		{Name: "Internal", Rows: [][]any{{"Note", "keep private"}, {"Reserve", 4217.0}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	write("doc-xlsx-hidden-question/repo/accounts.xlsx", hideSheet(t, reserves, "Internal"))

	// A deck to fix.
	deck, err := Create("d.pptx", CreateSpec{Slides: []SlideSpec{
		{Layout: "title", Title: "Opening", Text: "Quarterly review"},
		{Layout: "content", Title: "Plan", Bullets: []string{"Hire two people", "Ship the beta"}, Notes: "Mention the budget"},
		{Layout: "content", Title: "Old appendix", Bullets: []string{"Out of date"}, Notes: "Remove me"},
		{Layout: "content", Title: "Results", Bullets: []string{"Revenue up"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	write("doc-pptx-fix-deck/repo/deck.pptx", deck)
	fixed := apply(t, "d.pptx", deck,
		Edit{Op: "replace_text", Find: "Plan", Text: "Roadmap"},
		Edit{Op: "delete_slide", Target: "s258"},
		Edit{Op: "move_slide", Target: "s259", Position: 2},
	)
	write("doc-pptx-fix-deck/solution/deck.pptx", fixed.Data)

	// A report to split, with a code that only reading page 3 finds.
	var blocks []Block
	for i := 1; i <= 5; i++ {
		text := fmt.Sprintf("Section %d of the report. Nothing of note here.", i)
		if i == 3 {
			text = "Section 3 of the report. The access code is ORCHID-7."
		}
		blocks = append(blocks, Block{Type: "heading", Text: fmt.Sprintf("Part %d", i)}, Block{Type: "paragraph", Text: text})
		if i < 5 {
			blocks = append(blocks, Block{Type: "page_break"})
		}
	}
	report, err := Create("p.pdf", CreateSpec{Blocks: blocks})
	if err != nil {
		t.Fatal(err)
	}
	write("doc-pdf-split/repo/report.pdf", report)
	excerpt, _, err := ExtractPDFPages(report, "2-3")
	if err != nil {
		t.Fatal(err)
	}
	write("doc-pdf-split/solution/excerpt.pdf", excerpt)
}

func hideSheet(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	pkg, err := OpenPackage(data)
	if err != nil {
		t.Fatal(err)
	}
	c := NewChanges()
	wb := partOf(t, data, "xl/workbook.xml")
	c.Set["xl/workbook.xml"] = []byte(strings.Replace(wb, `<sheet name="`+name+`"`, `<sheet state="hidden" name="`+name+`"`, 1))
	out, err := pkg.Write(c)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
