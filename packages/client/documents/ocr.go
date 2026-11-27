package documents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"strings"
)

// OCR: recognising text in scanned pages and pictures. The built-in engine is Tesseract compiled to WebAssembly
// (ocr_tesseract.go); this is the seam it plugs into, and tests or embedders may plug in another (see
// docs/design/documents.md, "OCR"). With no engine the operation says "not available", never an empty result that reads
// as "no text".

// OCREngine recognises printed text in an image.
type OCREngine interface {
	// Name and License identify the engine and its trained data in reports.
	Name() string
	License() string
	// Recognize returns the lines found, top to bottom. langs are ISO 639-2 codes ("eng", "deu"); empty means the
	// engine's default.
	Recognize(ctx context.Context, img image.Image, langs []string) ([]OCRLine, error)
}

// OCRLine is one recognised line, with its box in the image's pixels and a confidence from 0 to 1.
type OCRLine struct {
	Text       string
	Box        image.Rectangle
	Confidence float64
}

// OCR is the engine in use: Tesseract, set in ocr_tesseract.go; nil means none. Tests and embedders may replace it.
var OCR OCREngine

// ErrNoOCR says no engine is there. It is never reported as a page without text.
var ErrNoOCR = errors.New("not available: no OCR engine is in this build of xlsft`s harness. Render the pages instead and read them visually; scanned pages stay without a text layer")

// OCRDPI is the resolution pages are rendered at for recognition.
const OCRDPI = 300

// OCRLowConfidence is the confidence below which a line is counted as doubtful.
const OCRLowConfidence = 0.6

// RecognizePDF renders the pages of a PDF and recognises their text.
func RecognizePDF(ctx context.Context, pdf []byte, pages []int, langs []string) (string, error) {
	if OCR == nil {
		return "", ErrNoOCR
	}
	var b strings.Builder
	for _, n := range pages {
		imgs, err := RenderPages(ctx, pdf, []int{n}, OCRDPI)
		if err != nil {
			return "", fmt.Errorf("page %d could not be rendered for recognition: %v", n, err)
		}
		lines, err := OCR.Recognize(ctx, dpiImage{imgs[0], OCRDPI}, langs)
		if err != nil {
			return "", fmt.Errorf("page %d: %s: %v", n, OCR.Name(), err)
		}
		fmt.Fprintf(&b, "--- page %d ---\n%s", n, ocrText(lines))
	}
	return b.String(), nil
}

// RecognizeImage recognises the text of a picture file.
func RecognizeImage(ctx context.Context, data []byte, langs []string) (string, error) {
	if OCR == nil {
		return "", ErrNoOCR
	}
	if _, err := loadPicture(data); err != nil {
		return "", err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	lines, err := OCR.Recognize(ctx, img, langs)
	if err != nil {
		return "", fmt.Errorf("%s: %v", OCR.Name(), err)
	}
	return ocrText(lines), nil
}

func ocrText(lines []OCRLine) string {
	if len(lines) == 0 {
		return "(no text recognised)\n"
	}
	// Each line leads with its confidence, so a doubtful line can be checked against the page on its own.
	var b strings.Builder
	low := 0
	for _, l := range lines {
		fmt.Fprintf(&b, "%3d%% %s\n", int(l.Confidence*100+0.5), l.Text)
		if l.Confidence < OCRLowConfidence {
			low++
		}
	}
	if low > 0 {
		fmt.Fprintf(&b, "[%d of %d lines were recognised with low confidence: check them against the page]\n", low, len(lines))
	}
	return b.String()
}
