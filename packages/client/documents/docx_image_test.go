package documents

import (
	"image/color"
	"strings"
	"testing"
)

func TestDOCXInsertAndReplaceImage(t *testing.T) {
	data := docxFixture(t, false, "")
	d := mustOpen(t, "a.docx", data)
	addr, fp := addrOf(t, readAll(t, d, ""), "The end.")
	red := pngOf(t, 192, 96, color.RGBA{255, 0, 0, 255})
	res := apply(t, "a.docx", data, Edit{Op: "insert_image", Target: addr, Fingerprint: fp, ImageData: red, Alt: ptr("Red bar")})
	onlyChanged(t, data, res.Data, "word/document.xml", "word/_rels/document.xml.rels", "word/media/image1.png", contentTypesPart)
	noErrors(t, "a.docx", res.Data)
	d2 := mustOpen(t, "a.docx", res.Data)
	if s := d2.Structure(); !strings.Contains(s, `picture 1: 2.00×1.00 in, inline, alt="Red bar", word/media/image1.png`) {
		t.Fatalf("structure:\n%s", s)
	}
	pics := readAll(t, d2, "images")
	paddr, pfp := addrOf(t, pics, "Red bar")

	// Replace: the new picture fits the old frame, and the old media goes.
	tall := jpegOf(t, 48, 96)
	rep := apply(t, "a.docx", res.Data, Edit{Op: "replace_image", Target: paddr, Fingerprint: pfp, ImageData: tall, Alt: ptr("Tall")})
	noErrors(t, "a.docx", rep.Data)
	if hasPart(t, rep.Data, "word/media/image1.png") || !hasPart(t, rep.Data, "word/media/image1.jpeg") {
		t.Error("media parts after replace")
	}
	if s := readAll(t, mustOpen(t, "a.docx", rep.Data), "images"); !strings.Contains(s, `0.50×1.00 in, inline, alt="Tall"`) {
		t.Errorf("after replace:\n%s", s)
	}
	// Only the size.
	sz := apply(t, "a.docx", res.Data, Edit{Op: "replace_image", Target: paddr, Fingerprint: pfp, CX: ptr[int64](emuPerInch)})
	if s := readAll(t, mustOpen(t, "a.docx", sz.Data), "images"); !strings.Contains(s, "1.00×1.00 in") {
		t.Errorf("resize:\n%s", s)
	}
	onlyChanged(t, res.Data, sz.Data, "word/document.xml")

	// In a paragraph after some text, and floating.
	baddr, bfp := addrOf(t, readAll(t, d, ""), "Bold start")
	in := apply(t, "a.docx", data, Edit{Op: "insert_image", Target: baddr, Fingerprint: bfp, Find: "plain", ImageData: red, Alt: ptr("")},
		Edit{Op: "insert_image", Target: addr, Fingerprint: fp, ImageData: red, Alt: ptr("f"), X: ptr[int64](emuPerInch), Y: ptr[int64](emuPerInch)})
	noErrors(t, "a.docx", in.Data)
	x := partOf(t, in.Data, "word/document.xml")
	if !strings.Contains(x, "<wp:anchor ") || !strings.Contains(x, `plain</w:t></w:r><w:r><w:drawing>`) {
		t.Errorf("placement:\n%s", x)
	}
	if strings.Count(x, `<wp:docPr id="1"`) != 1 || strings.Count(x, `<wp:docPr id="2"`) != 1 {
		t.Errorf("drawing ids are not unique")
	}
	two := mustOpen(t, "a.docx", apply(t, "a.docx", res.Data, Edit{Op: "insert_image", Target: paddr, Fingerprint: pfp, Find: "", ImageData: red, Alt: ptr("x")}).Data)
	_ = two
	// A paragraph with two pictures needs a position.
	both := apply(t, "a.docx", data, Edit{Op: "insert_image", Target: baddr, Fingerprint: bfp, Find: "plain", ImageData: red, Alt: ptr("a")}).Data
	b2 := mustOpen(t, "a.docx", both)
	a2, f2 := addrOf(t, readAll(t, b2, ""), "Bold start")
	both = apply(t, "a.docx", both, Edit{Op: "insert_image", Target: a2, Fingerprint: f2, Find: "Bold", ImageData: red, Alt: ptr("b")}).Data
	a3, f3 := addrOf(t, readAll(t, mustOpen(t, "a.docx", both), ""), "Bold")
	refused(t, "a.docx", both, "say which with position", Edit{Op: "replace_image", Target: a3, Fingerprint: f3, Alt: ptr("c")})
	ok := apply(t, "a.docx", both, Edit{Op: "replace_image", Target: a3, Fingerprint: f3, Position: 2, Alt: ptr("c")})
	if s := readAll(t, mustOpen(t, "a.docx", ok.Data), "images"); !strings.Contains(s, `picture 2: 2.00×1.00 in, inline, alt="c"`) {
		t.Errorf("position:\n%s", s)
	}
	refused(t, "a.docx", data, "holds no picture", Edit{Op: "replace_image", Target: addr, Fingerprint: fp, Alt: ptr("c")})
}

func TestDOCXImageInHeaderGetsItsOwnRelationships(t *testing.T) {
	data := docxFixture(t, false, "")
	addr, fp := addrOf(t, readAll(t, mustOpen(t, "a.docx", data), ""), "Header words")
	res := apply(t, "a.docx", data, Edit{Op: "insert_image", Target: addr, Fingerprint: fp, ImageData: pngOf(t, 10, 10, color.Black), Alt: ptr("logo")})
	if !hasPart(t, res.Data, "word/_rels/header1.xml.rels") {
		t.Fatal("no relationships part for the header")
	}
	noErrors(t, "a.docx", res.Data)
}

func TestDOCXTrackedImageReplace(t *testing.T) {
	plain := docxFixture(t, false, "")
	addr, fp := addrOf(t, readAll(t, mustOpen(t, "a.docx", plain), ""), "The end.")
	with := apply(t, "a.docx", plain, Edit{Op: "insert_image", Target: addr, Fingerprint: fp, ImageData: pngOf(t, 10, 10, color.Black), Alt: ptr("old")}).Data
	// Switch tracking on by asking for it.
	on := true
	paddr, pfp := addrOf(t, readAll(t, mustOpen(t, "a.docx", with), "images"), "old")
	res := apply(t, "a.docx", with, Edit{Op: "replace_image", Target: paddr, Fingerprint: pfp, ImageData: pngOf(t, 10, 10, color.White), Track: &on, Author: "Eve"})
	noErrors(t, "a.docx", res.Data)
	x := partOf(t, res.Data, "word/document.xml")
	if !strings.Contains(x, `<w:del w:id=`) || !strings.Contains(x, `<w:ins w:id=`) || strings.Count(x, "<wp:inline") != 2 {
		t.Fatalf("not tracked:\n%s", x)
	}
	if !hasPart(t, res.Data, "word/media/image1.png") || !hasPart(t, res.Data, "word/media/image2.png") {
		t.Error("both pictures must stay while the change is pending")
	}
	d := mustOpen(t, "a.docx", res.Data)
	if !strings.Contains(d.Structure(), "deletion by Eve") {
		t.Errorf("%s", d.Structure())
	}
	acc := apply(t, "a.docx", res.Data, Edit{Op: "accept_change", All: true})
	noErrors(t, "a.docx", acc.Data)
	if n := strings.Count(partOf(t, acc.Data, "word/document.xml"), "<wp:inline"); n != 1 {
		t.Errorf("after accepting: %d drawings", n)
	}
}
