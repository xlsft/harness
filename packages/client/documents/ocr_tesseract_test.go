package documents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/draw"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Recognition tests render text with our own code (the Go fonts cover Latin and Cyrillic), recognise it and compare.
// They need the trained data, fetched once into the cache, so they run with HARNESS_TEST_OCR=1, as PDFium's do with
// HARNESS_TEST_PDFIUM.

func needOCR(t *testing.T) {
	t.Helper()
	if os.Getenv("HARNESS_TEST_OCR") == "" {
		t.Skip("set HARNESS_TEST_OCR=1: fetches tessdata_fast for English and Russian once (8 MB) and compiles the module")
	}
}

// renderLines draws lines of text in black on white at the given pixel size.
func renderLines(t testing.TB, lines []string, px float64) *image.RGBA {
	t.Helper()
	f, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		t.Fatal(err)
	}
	defer face.Close()
	lineH := int(px * 1.6)
	width := 0
	for _, l := range lines {
		width = max(width, font.MeasureString(face, l).Ceil())
	}
	img := image.NewRGBA(image.Rect(0, 0, width+2*int(px), lineH*len(lines)+2*int(px)))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	d := &font.Drawer{Dst: img, Src: image.NewUniform(color.Black), Face: face}
	for i, l := range lines {
		d.Dot = fixed.P(int(px), int(px)+lineH*i+int(px))
		d.DrawString(l)
	}
	return img
}

// words keeps letters and digits, lower-cased, so punctuation and spacing slips do not fail a test.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// wordRecall is the share of want's words found, in any order, in got.
func wordRecall(want, got string) float64 {
	have := map[string]int{}
	for _, w := range words(got) {
		have[w]++
	}
	ws := words(want)
	hit := 0
	for _, w := range ws {
		if have[w] > 0 {
			have[w]--
			hit++
		}
	}
	return float64(hit) / float64(len(ws))
}

func TestOCRModuleChecksum(t *testing.T) {
	wasm, err := tesseractModule()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(wasm)
	if hex.EncodeToString(sum[:]) != strings.TrimSpace(tesseractModuleSHA256) {
		t.Fatal("checksum")
	}
}

func TestOCRLanguages(t *testing.T) {
	got, err := NormalizeOCRLanguages([]string{"RUS+eng", "eng"})
	if err != nil || strings.Join(got, "+") != "rus+eng" {
		t.Fatalf("%v %v", got, err)
	}
	if got, _ := NormalizeOCRLanguages(nil); strings.Join(got, "+") != "eng" {
		t.Fatal(got)
	}
	if _, err := NormalizeOCRLanguages([]string{"klingon"}); err == nil || !strings.Contains(err.Error(), "eng, rus") {
		t.Fatal(err)
	}
}

func TestOCRParseResult(t *testing.T) {
	lines := parseOCRResult("96\t10\t20\t300\t50\tHello world\nbad\n41\t1\t2\t3\t4\t  \n55\t1\t2\t3\t4\tmaybe\n")
	if len(lines) != 2 || lines[0].Text != "Hello world" || lines[0].Box != image.Rect(10, 20, 300, 50) || lines[0].Confidence != 0.96 || lines[1].Confidence != 0.55 {
		t.Fatalf("%+v", lines)
	}
	text := ocrText(lines)
	if !strings.Contains(text, " 96% Hello world\n") || !strings.Contains(text, "1 of 2 lines") {
		t.Fatal(text)
	}
}

func TestOCRGreyPixels(t *testing.T) {
	m := image.NewRGBA(image.Rect(5, 5, 7, 6))
	m.Set(5, 5, color.RGBA{0, 0, 0, 255})
	// (6,5) stays fully transparent: white.
	g := greyPixels(m)
	if len(g) != 2 || g[0] != 0 || g[1] != 255 {
		t.Fatal(g)
	}
	n := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	n.Set(0, 0, color.NRGBA{255, 0, 0, 255})
	if g := greyPixels(n); g[0] != 76 {
		t.Fatal(g)
	}
}

var englishSample = []string{
	"The quick brown fox jumps over the lazy dog.",
	"Invoice 2026-118, total due 4,250.00 EUR",
	"Please return the signed contract by Friday.",
}

var russianSample = []string{
	"Съешь же ещё этих мягких французских булок,",
	"да выпей чаю. Договор номер 118 от 7 ноября.",
	"Оплата в течение десяти рабочих дней.",
}

func recognise(t *testing.T, img image.Image, langs []string) ([]OCRLine, time.Duration) {
	t.Helper()
	start := time.Now()
	lines, err := Tesseract{}.Recognize(context.Background(), img, langs)
	if err != nil {
		t.Fatal(err)
	}
	return lines, time.Since(start)
}

func joined(lines []OCRLine) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

func TestOCRRealEnglish(t *testing.T) {
	needOCR(t)
	img := renderLines(t, englishSample, 40)
	lines, took := recognise(t, img, []string{"eng"})
	got := joined(lines)
	t.Logf("%v\n%s", took, got)
	if r := wordRecall(strings.Join(englishSample, "\n"), got); r < 0.9 {
		t.Fatalf("recall %.2f:\n%s", r, got)
	}
	if len(lines) != len(englishSample) {
		t.Fatalf("%d lines", len(lines))
	}
	for _, l := range lines {
		if l.Confidence < 0.8 || l.Box.Empty() || !l.Box.In(img.Bounds()) {
			t.Fatalf("%+v", l)
		}
	}
	// The same pixels give the same answer.
	again, _ := recognise(t, img, []string{"eng"})
	if joined(again) != got {
		t.Fatalf("not deterministic:\n%s\n%s", got, joined(again))
	}
}

func TestOCRRealRussian(t *testing.T) {
	needOCR(t)
	img := renderLines(t, russianSample, 40)
	lines, took := recognise(t, img, []string{"rus"})
	got := joined(lines)
	t.Logf("%v\n%s", took, got)
	if r := wordRecall(strings.Join(russianSample, "\n"), got); r < 0.9 {
		t.Fatalf("recall %.2f:\n%s", r, got)
	}
	// Both languages at once, on a mixed page.
	mixed := append(append([]string{}, englishSample[:2]...), russianSample[:2]...)
	lines, _ = recognise(t, renderLines(t, mixed, 40), []string{"eng", "rus"})
	if r := wordRecall(strings.Join(mixed, "\n"), joined(lines)); r < 0.85 {
		t.Fatalf("mixed recall %.2f:\n%s", r, joined(lines))
	}
}

func TestOCRRealPDF(t *testing.T) {
	needOCR(t)
	if os.Getenv("HARNESS_TEST_PDFIUM") == "" {
		t.Skip("set HARNESS_TEST_PDFIUM=1 as well: the page is rendered through PDFium")
	}
	// A scanned page: a picture of text and no text layer.
	pic, err := EncodePNG(renderLines(t, englishSample, 60))
	if err != nil {
		t.Fatal(err)
	}
	pdf := scannedPDF(t, pic)
	text, err := RecognizePDF(context.Background(), pdf, []int{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "--- page 1 ---\n") || wordRecall(strings.Join(englishSample, "\n"), text) < 0.9 {
		t.Fatal(text)
	}
}

// scannedPDF places a picture across a page of the usual fixture, as a scanner would leave it.
func scannedPDF(t *testing.T, pic []byte) []byte {
	t.Helper()
	return apply(t, "p.pdf", pdfFixture(t, 1), Edit{Op: "add_image", Pages: "1", X: ptr[int64](36), Y: ptr[int64](200), CX: ptr[int64](520), ImageData: pic}).Data
}

func TestOCRRealTimeout(t *testing.T) {
	needOCR(t)
	recognise(t, renderLines(t, englishSample[:1], 40), nil) // warm: data loaded, module compiled
	// A full page takes seconds; the deadline stops it at the next word.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (Tesseract{}).Recognize(ctx, a4Page(t, englishSample), nil)
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("after the deadline: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("stopped only after %v", took)
	}
	// The next page gets a fresh instance.
	lines, _ := recognise(t, renderLines(t, englishSample[:1], 40), nil)
	if wordRecall(englishSample[0], joined(lines)) < 0.9 {
		t.Fatal(joined(lines))
	}
}

// a4Page is an A4 page at 300 dpi with 40 lines of 10-point text (about 42 px) from sample.
func a4Page(t testing.TB, sample []string) image.Image {
	var lines []string
	for len(lines) < 40 {
		lines = append(lines, sample...)
	}
	text := renderLines(t, lines[:40], 42)
	page := image.NewRGBA(image.Rect(0, 0, 2480, 3508))
	draw.Draw(page, page.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(page, text.Bounds().Add(image.Pt(150, 150)), text, image.Point{}, draw.Src)
	return dpiImage{page, 300}
}

// BenchmarkOCRPage measures one A4 page at 300 dpi, 40 lines of 10-point text. Run with HARNESS_TEST_OCR=1
// go test -run - -bench OCRPage ./documents. The peak memory is the module's linear memory after the page.
func BenchmarkOCRPage(b *testing.B) {
	if os.Getenv("HARNESS_TEST_OCR") == "" {
		b.Skip("set HARNESS_TEST_OCR=1")
	}
	for _, lang := range []string{"eng", "rus"} {
		b.Run(lang, func(b *testing.B) {
			sample := englishSample
			if lang == "rus" {
				sample = russianSample
			}
			page := a4Page(b, sample)
			if _, err := (Tesseract{}).Recognize(context.Background(), page, []string{lang}); err != nil {
				b.Fatal(err) // warm: data loaded, module compiled and started
			}
			cpu := processCPU()
			n := 0
			b.ResetTimer()
			for b.Loop() {
				lines, err := (Tesseract{}).Recognize(context.Background(), page, []string{lang})
				if err != nil {
					b.Fatal(err)
				}
				if len(lines) < 35 {
					b.Fatalf("%d lines", len(lines))
				}
				n++
			}
			b.StopTimer()
			if c := processCPU() - cpu; c > 0 {
				b.ReportMetric(c.Seconds()/float64(n), "cpu-s/page")
			}
			tessTurn <- struct{}{}
			if tess.mod != nil {
				b.ReportMetric(float64(tess.mod.Memory().Size())/(1<<20), "wasm-MiB")
			}
			<-tessTurn
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			b.ReportMetric(float64(ms.Sys)/(1<<20), "go-sys-MiB")
		})
	}
}
