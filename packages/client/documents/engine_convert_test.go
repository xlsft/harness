package documents

import (
	"context"
	"image/color"
	"strings"
	"testing"
)

// Round trip through OpenDocument with a picture and a row we added, read back by a second program. Runs only with
// HARNESS_TEST_LIBREOFFICE=1.
func TestRealEngineConvertsWhatWeAdd(t *testing.T) {
	e := realEngine(t)
	ctx := context.Background()
	data, err := Create("a.docx", CreateSpec{Title: "Round trip", Blocks: []Block{{Text: "Body text"}, {Type: "table", Rows: [][]string{{"a", "b"}, {"c", "d"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	d := mustOpen(t, "a.docx", data)
	addr, fp := addrOf(t, readAll(t, d, ""), "Body text")
	row, rfp := addrOf(t, readAll(t, d, "document:t0"), "| a |")
	res := apply(t, "a.docx", data, Edit{Op: "insert_image", Target: addr, Fingerprint: fp, ImageData: pngOf(t, 64, 32, color.Black), Alt: ptr("bar")}, Edit{Op: "insert_after", Target: row, Fingerprint: rfp, Text: "e\tf"})
	odt, err := e.Convert(ctx, "a.docx", res.Data, "odt", ".odt")
	if err != nil {
		t.Fatal(err)
	}
	back, err := e.Convert(ctx, "a.odt", odt, "docx:MS Word 2007 XML", ".docx")
	if err != nil {
		t.Fatal(err)
	}
	bd := mustOpen(t, "b.docx", back)
	all, pics := readAll(t, bd, ""), readAll(t, bd, "images")
	if !strings.Contains(all, "Body text") || !strings.Contains(all, "e") || !strings.Contains(pics, "picture 1") {
		t.Errorf("after the round trip:\n%s\n%s", all, pics)
	}
	if _, err := e.ToPDF(ctx, "a.docx", res.Data); err != nil {
		t.Fatal(err)
	}
}
