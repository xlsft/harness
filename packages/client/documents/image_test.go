package documents

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

// pngOf draws a w×h picture, generated here rather than copied from anywhere.
func pngOf(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegOf(t *testing.T, w, h int) []byte {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range m.Pix {
		m.Pix[i] = byte(i)
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, m, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func ptr[T any](v T) *T { return &v }

func TestLoadPicture(t *testing.T) {
	p, err := loadPicture(pngOf(t, 96, 48, color.Black))
	if err != nil {
		t.Fatal(err)
	}
	if cx, cy := p.naturalEMU(); cx != emuPerInch || cy != emuPerInch/2 {
		t.Errorf("natural size %d×%d", cx, cy)
	}
	cx, cy, _ := p.sizeEMU(ptr[int64](emuPerInch*2), nil, 0, 0)
	if cx != 2*emuPerInch || cy != emuPerInch {
		t.Errorf("aspect not kept: %d×%d", cx, cy)
	}
	cx, cy, _ = p.sizeEMU(nil, nil, emuPerInch/2, 0)
	if cx != emuPerInch/2 || cy != emuPerInch/4 {
		t.Errorf("not fitted: %d×%d", cx, cy)
	}
	if _, err := loadPicture([]byte("not a picture")); err == nil {
		t.Error("garbage was taken for a picture")
	}
	if j, err := loadPicture(jpegOf(t, 10, 10)); err != nil || j.contentType != "image/jpeg" {
		t.Errorf("jpeg: %v", err)
	}
}

func TestPPTXAddAndReplaceImage(t *testing.T) {
	data := deckFixture(t)
	red := pngOf(t, 192, 96, color.RGBA{255, 0, 0, 255})
	res := apply(t, "d.pptx", data, Edit{Op: "add_image", Target: "s257", ImageData: red, Alt: ptr("A red bar")})
	onlyChanged(t, data, res.Data, "ppt/slides/slide2.xml", "ppt/slides/_rels/slide2.xml.rels", "ppt/media/image1.png", contentTypesPart)
	noErrors(t, "d.pptx", res.Data)
	if !strings.Contains(partOf(t, res.Data, contentTypesPart), `Extension="png"`) {
		t.Error("no content type for png")
	}
	d := mustOpen(t, "d.pptx", res.Data)
	listing := readAll(t, d, "s257")
	if !strings.Contains(listing, `picture ppt/media/image1.png alt="A red bar"`) {
		t.Fatalf("the picture is not listed:\n%s", listing)
	}
	addr, fp := addrOf(t, listing, "A red bar")

	// Two in one batch get different ids and parts; no alt text is reported.
	two := apply(t, "d.pptx", data, Edit{Op: "add_image", Target: "s257", ImageData: red}, Edit{Op: "add_image", Target: "s257", ImageData: red})
	if !hasPart(t, two.Data, "ppt/media/image2.png") || !strings.Contains(strings.Join(two.Notes, " "), "alternative text") {
		t.Errorf("second picture: %v", two.Notes)
	}
	noErrors(t, "d.pptx", two.Data)

	// Replacing fits the new picture into the old frame, keeps the old media for nobody, and removes it.
	blue := jpegOf(t, 50, 100)
	rep := apply(t, "d.pptx", res.Data, Edit{Op: "replace_image", Target: addr, Fingerprint: fp, ImageData: blue})
	noErrors(t, "d.pptx", rep.Data)
	if hasPart(t, rep.Data, "ppt/media/image1.png") {
		t.Error("the old picture stayed though nothing uses it")
	}
	if !hasPart(t, rep.Data, "ppt/media/image1.jpeg") {
		t.Error("the new picture is missing")
	}
	if !strings.Contains(strings.Join(rep.Notes, " "), "described the old picture") {
		t.Errorf("notes: %v", rep.Notes)
	}
	after := readAll(t, mustOpen(t, "d.pptx", rep.Data), "s257")
	if !strings.Contains(after, "image1.jpeg") {
		t.Errorf("after replace:\n%s", after)
	}
	// Only alt text.
	alt := apply(t, "d.pptx", res.Data, Edit{Op: "replace_image", Target: addr, Fingerprint: fp, Alt: ptr("")})
	if !strings.Contains(readAll(t, mustOpen(t, "d.pptx", alt.Data), "s257"), `alt=""`) {
		t.Error("alt text not cleared")
	}
	onlyChanged(t, res.Data, alt.Data, "ppt/slides/slide2.xml")

	textAddr, textFP := shapeLine(t, d, "One thing")
	refused(t, "d.pptx", res.Data, "not a picture", Edit{Op: "replace_image", Target: textAddr, Fingerprint: textFP, ImageData: blue})
	refused(t, "d.pptx", data, "PNG, JPEG or GIF", Edit{Op: "add_image", Target: "s257", ImageData: []byte("not a picture at all")})
}

func TestPPTXSharedPictureSurvivesReplace(t *testing.T) {
	data := deckFixture(t)
	red := pngOf(t, 20, 20, color.RGBA{255, 0, 0, 255})
	res := apply(t, "d.pptx", data, Edit{Op: "add_image", Target: "s257", ImageData: red, Alt: ptr("r")})
	// A clone shares the media part.
	cl := apply(t, "d.pptx", res.Data, Edit{Op: "add_slide", CloneOf: "s257"})
	d := mustOpen(t, "d.pptx", cl.Data)
	addr, fp := addrOf(t, readAll(t, d, "s257"), `alt="r"`)
	rep := apply(t, "d.pptx", cl.Data, Edit{Op: "replace_image", Target: addr, Fingerprint: fp, ImageData: pngOf(t, 20, 20, color.White)})
	if !hasPart(t, rep.Data, "ppt/media/image1.png") {
		t.Error("a picture another slide shows was removed")
	}
	noErrors(t, "d.pptx", rep.Data)
}
