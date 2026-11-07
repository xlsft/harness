package documents

import (
	"context"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPDFAddText(t *testing.T) {
	data := pdfFixture(t, 3)
	res := apply(t, "p.pdf", data, Edit{Op: "add_text", Pages: "1,3", X: ptr[int64](72), Y: ptr[int64](36), Text: "DRAFT – café\nsecond line", Size: 10})
	noErrors(t, "p.pdf", res.Data)
	d := mustOpen(t, "p.pdf", res.Data)
	for _, page := range []string{"1", "3"} {
		txt := readAll(t, d, page)
		if !strings.Contains(txt, "café") || !strings.Contains(txt, "second line") || !strings.Contains(txt, "Page heading") {
			t.Errorf("page %s:\n%s", page, txt)
		}
	}
	if strings.Contains(readAll(t, d, "2"), "DRAFT") {
		t.Error("page 2 got the text")
	}
	if !strings.Contains(strings.Join(res.Changes, " "), "page 1 at (72, 36) pt") {
		t.Errorf("%v", res.Changes)
	}
	// Wrapped within a width.
	wrapped := apply(t, "p.pdf", data, Edit{Op: "add_text", Pages: "2", X: ptr[int64](72), Y: ptr[int64](600), CX: ptr[int64](60), Text: "one two three four five six seven"})
	if txt := readAll(t, mustOpen(t, "p.pdf", wrapped.Data), "2"); !strings.Contains(txt, "seven") {
		t.Errorf("wrapped:\n%s", txt)
	}
	refused(t, "p.pdf", data, "outside WinAnsi", Edit{Op: "add_text", Pages: "1", X: ptr[int64](1), Y: ptr[int64](1), Text: "Привет"})
	refused(t, "p.pdf", data, "needs x and y", Edit{Op: "add_text", Pages: "1", Text: "x"})
	refused(t, "p.pdf", data, "needs pages", Edit{Op: "add_text", X: ptr[int64](1), Y: ptr[int64](1), Text: "x"})
}

// A picture placed at the top-left corner lands there as the page is shown, rotated or not: PDFium draws it.
func TestRealPDFPicturePlacement(t *testing.T) {
	if os.Getenv("HARNESS_TEST_PDFIUM") == "" {
		t.Skip("set HARNESS_TEST_PDFIUM=1: the first compile of the module takes about 20 s, far more under -race")
	}
	RasterCacheDir = filepath.Join(t.TempDir(), "cache")
	defer func() { RasterCacheDir = ""; closeRaster() }()
	for _, deg := range []int{0, 90, 180, 270} {
		data := pdfFixture(t, 1)
		if deg != 0 {
			data = apply(t, "p.pdf", data, Edit{Op: "rotate", Degrees: deg}).Data
		}
		data = apply(t, "p.pdf", data, Edit{Op: "add_image", Pages: "1", X: ptr[int64](0), Y: ptr[int64](0), CX: ptr[int64](72), CY: ptr[int64](72), ImageData: pngOf(t, 8, 8, color.RGBA{0, 0, 255, 255})}).Data
		imgs, err := RenderPages(context.Background(), data, []int{1}, 72)
		if err != nil {
			t.Fatal(err)
		}
		m := imgs[0]
		b := m.Bounds()
		blue := func(x, y int) bool {
			r, g, bl, _ := m.At(b.Min.X+x, b.Min.Y+y).RGBA()
			return bl > 0xC000 && r < 0x4000 && g < 0x4000
		}
		if !blue(30, 30) || blue(b.Dx()-30, b.Dy()-30) || blue(100, 100) {
			t.Errorf("rotated %d: the picture is not at the top-left corner as shown (%v)", deg, b)
		}
	}
}

func TestPDFAddTextOnRotatedPage(t *testing.T) {
	data := apply(t, "p.pdf", pdfFixture(t, 1), Edit{Op: "rotate", Degrees: 90}).Data
	res := apply(t, "p.pdf", data, Edit{Op: "add_text", Pages: "1", X: ptr[int64](10), Y: ptr[int64](10), Text: "Corner"})
	noErrors(t, "p.pdf", res.Data)
	if !strings.Contains(readAll(t, mustOpen(t, "p.pdf", res.Data), "1"), "Corner") {
		t.Error("text missing on a rotated page")
	}
}

func TestPDFAddImage(t *testing.T) {
	data := pdfFixture(t, 2)
	res := apply(t, "p.pdf", data, Edit{Op: "add_image", Pages: "2", X: ptr[int64](72), Y: ptr[int64](72), ImageData: pngOf(t, 96, 48, color.RGBA{0, 0, 255, 255}), Alt: ptr("Blue")})
	noErrors(t, "p.pdf", res.Data)
	s := mustOpen(t, "p.pdf", res.Data).Structure()
	if !strings.Contains(s, "2: ") || !strings.Contains(s, "1 images") {
		t.Errorf("structure:\n%s", s)
	}
	if !strings.Contains(strings.Join(res.Changes, " "), "72×36 pt") {
		t.Errorf("natural size at 96 dpi: %v", res.Changes)
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "no alternative text") {
		t.Errorf("notes: %v", res.Notes)
	}
	j := apply(t, "p.pdf", data, Edit{Op: "add_image", Pages: "1-2", X: ptr[int64](0), Y: ptr[int64](0), CX: ptr[int64](100), ImageData: jpegOf(t, 40, 20)})
	noErrors(t, "p.pdf", j.Data)
	if !strings.Contains(strings.Join(j.Changes, " "), "100×50 pt") {
		t.Errorf("%v", j.Changes)
	}
	refused(t, "p.pdf", data, "PNG, JPEG or GIF", Edit{Op: "add_image", Pages: "1", X: ptr[int64](0), Y: ptr[int64](0), ImageData: []byte("nope, not one")})
}
