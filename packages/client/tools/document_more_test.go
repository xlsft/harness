package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/documents"
)

func writePNG(t *testing.T, p string) {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for i := range m.Pix {
		m.Pix[i] = 200
	}
	m.Set(0, 0, color.Black)
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentEditPictureFromWorkingFolder(t *testing.T) {
	env, dir := docEnv(t)
	p := filepath.Join(dir, "a.docx")
	writeDoc(t, p, documents.CreateSpec{Blocks: []documents.Block{{Text: "Logo below"}}})
	pic := filepath.Join(dir, "logo.png")
	writePNG(t, pic)
	addr, fp := docAddr(t, env, p, "Logo below")
	r := docCall(t, env, &DocumentEdit{}, map[string]any{"file_path": p, "edits": []map[string]any{{"op": "insert_image", "target": addr, "fingerprint": fp, "image": pic, "alt": "Logo"}}})
	if r.IsError {
		t.Fatal(resultText(r))
	}
	if !strings.Contains(resultText(r), "Inserted a picture (inline") {
		t.Errorf("%s", resultText(r))
	}
	read := docCall(t, env, &DocumentRead{}, map[string]any{"file_path": p, "operation": "read", "range": "images"})
	if !strings.Contains(resultText(read), `alt="Logo"`) {
		t.Fatalf("%s", resultText(read))
	}

	// Outside the working folders, or denied by a rule: refused, and the file stays.
	before := docBytes(t, p)
	other := filepath.Join(t.TempDir(), "x.png")
	writePNG(t, other)
	addr, fp = docAddr(t, env, p, "Logo below")
	r = docCall(t, env, &DocumentEdit{}, map[string]any{"file_path": p, "edits": []map[string]any{{"op": "insert_image", "target": addr, "fingerprint": fp, "image": other}}})
	if !r.IsError || !strings.Contains(resultText(r), "outside the session's working folders") {
		t.Fatalf("%s", resultText(r))
	}
	env.ReadDenied = func(x string) bool { return x == pic }
	r = docCall(t, env, &DocumentEdit{}, map[string]any{"file_path": p, "edits": []map[string]any{{"op": "insert_image", "target": addr, "fingerprint": fp, "image": pic}}})
	if !r.IsError || !strings.Contains(resultText(r), "denies") {
		t.Fatalf("%s", resultText(r))
	}
	if !bytes.Equal(before, docBytes(t, p)) {
		t.Error("a refused edit changed the file")
	}
}

// fakeSofficeCopying stands in for LibreOffice: it copies a prepared file to where the real one writes its output.
func fakeSofficeCopying(t *testing.T, outputs map[string]string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake engine is a shell script")
	}
	dir := t.TempDir()
	mark := filepath.Join(dir, "started")
	var cases strings.Builder
	for ext, src := range outputs {
		cases.WriteString(ext + "*) cp " + shellQuote(src) + " \"$out/document." + ext + "\";;\n")
	}
	script := "#!/bin/sh\ntouch " + shellQuote(mark) + "\nwhile [ $# -gt 0 ]; do case \"$1\" in --outdir) out=\"$2\"; shift;; --convert-to) to=\"$2\"; shift;; esac; shift; done\ncase \"$to\" in\n" + cases.String() + "esac\n"
	p := filepath.Join(dir, "soffice")
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	old := documents.FindSoffice
	documents.FindSoffice = func() string { return p }
	t.Cleanup(func() { documents.FindSoffice = old })
	return mark
}

func odtBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	w.Write([]byte("application/vnd.oasis.opendocument.text"))
	w, _ = z.Create("content.xml")
	w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"><office:body/></office:document-content>`))
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDocumentConvert(t *testing.T) {
	env, dir := docEnv(t)
	prepared := t.TempDir()
	pdf, err := documents.Create("x.pdf", documents.CreateSpec{Blocks: []documents.Block{{Text: "Converted"}}})
	if err != nil {
		t.Fatal(err)
	}
	pdfPath, odtPath := filepath.Join(prepared, "out.pdf"), filepath.Join(prepared, "out.odt")
	os.WriteFile(pdfPath, pdf, 0o600)
	os.WriteFile(odtPath, odtBytes(t), 0o600)
	mark := fakeSofficeCopying(t, map[string]string{"pdf": pdfPath, "odt": odtPath})

	src := filepath.Join(dir, "a.docx")
	writeDoc(t, src, documents.CreateSpec{Title: "T"})
	out := filepath.Join(dir, "a.pdf")
	r := docCall(t, env, &DocumentEdit{}, map[string]any{"file_path": out, "operation": "convert", "source": src})
	if !r.IsError || !strings.Contains(resultText(r), "has not been read") {
		t.Fatalf("an unread source: %s", resultText(r))
	}
	docCall(t, env, &DocumentRead{}, map[string]any{"file_path": src})
	r = docCall(t, env, &DocumentEdit{}, map[string]any{"file_path": out, "operation": "convert", "source": src})
	if r.IsError || !strings.Contains(resultText(r), "Converted") {
		t.Fatalf("%s", resultText(r))
	}
	if _, err := os.Stat(mark); err != nil {
		t.Error("the engine did not run")
	}
	if n, _ := documents.PDFPageCount(docBytes(t, out)); n != 1 {
		t.Errorf("%d pages", n)
	}
	odt := filepath.Join(dir, "a.odt")
	r = docCall(t, env, &DocumentEdit{}, map[string]any{"file_path": odt, "operation": "convert", "source": src})
	if r.IsError || !strings.Contains(resultText(r), "checked only as a package") {
		t.Fatalf("%s", resultText(r))
	}
	// Back from OpenDocument: it cannot be read with DocumentRead, so the read rules alone apply to it.
	docx := filepath.Join(dir, "b.docx")
	env.ReadDenied = func(p string) bool { return p == odt }
	r = docCall(t, env, &DocumentEdit{}, map[string]any{"file_path": docx, "operation": "convert", "source": odt})
	if !r.IsError || !strings.Contains(resultText(r), "denies") {
		t.Fatalf("%s", resultText(r))
	}
	env.ReadDenied = nil
	r = docCall(t, env, &DocumentEdit{}, map[string]any{"file_path": filepath.Join(dir, "c.xlsx"), "operation": "convert", "source": src})
	if !r.IsError || !strings.Contains(resultText(r), "convert does") {
		t.Fatalf("%s", resultText(r))
	}
	noSoffice(t)
	r = docCall(t, env, &DocumentEdit{}, map[string]any{"file_path": filepath.Join(dir, "d.pdf"), "operation": "convert", "source": src})
	if !r.IsError || !strings.Contains(resultText(r), "not available") {
		t.Fatalf("%s", resultText(r))
	}
}

type fakeOCR struct{}

func (fakeOCR) Name() string    { return "fake OCR" }
func (fakeOCR) License() string { return "test only" }
func (fakeOCR) Recognize(_ context.Context, img image.Image, _ []string) ([]documents.OCRLine, error) {
	return []documents.OCRLine{{Text: "Recognised words", Box: img.Bounds(), Confidence: 0.9}, {Text: "maybe", Confidence: 0.3}}, nil
}

func TestDocumentOCR(t *testing.T) {
	env, dir := docEnv(t)
	pic := filepath.Join(dir, "scan.png")
	writePNG(t, pic)
	r := docCall(t, env, &DocumentRead{}, map[string]any{"file_path": pic, "operation": "ocr"})
	if !r.IsError || !strings.Contains(resultText(r), "not available") {
		t.Fatalf("no engine must say so: %s", resultText(r))
	}
	documents.OCR = fakeOCR{}
	t.Cleanup(func() { documents.OCR = nil })
	r = docCall(t, env, &DocumentRead{}, map[string]any{"file_path": pic, "operation": "ocr"})
	if r.IsError || !strings.Contains(resultText(r), "Recognised words") || !strings.Contains(resultText(r), "low confidence") || !strings.Contains(resultText(r), "trust=\"untrusted\"") {
		t.Fatalf("%s", resultText(r))
	}
	p := filepath.Join(dir, "text.pdf")
	writeDoc(t, p, documents.CreateSpec{Blocks: []documents.Block{{Text: "Plenty of real text on this page"}}})
	r = docCall(t, env, &DocumentRead{}, map[string]any{"file_path": p, "operation": "ocr"})
	if !r.IsError || !strings.Contains(resultText(r), "No page") {
		t.Fatalf("%s", resultText(r))
	}
}
