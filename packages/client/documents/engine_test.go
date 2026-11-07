package documents

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeSoffice writes a script that behaves like the converter's command line: it copies a canned result to --outdir.
func fakeSoffice(t *testing.T, result []byte, exitCode string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake is a shell script")
	}
	dir := t.TempDir()
	canned := filepath.Join(dir, "canned")
	if err := os.WriteFile(canned, result, 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nwhile [ $# -gt 0 ]; do case \"$1\" in --outdir) out=\"$2\"; shift;; --convert-to) fmt=\"$2\"; shift;; esac; shift; done\n" +
		"ext=$(echo \"$fmt\" | cut -d: -f1)\ncp " + canned + " \"$out/document.$ext\"\nexit " + exitCode + "\n"
	p := filepath.Join(dir, "soffice")
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEngineConvertWithFake(t *testing.T) {
	bin := fakeSoffice(t, []byte("%PDF-fake"), "0")
	confined := false
	e := &Engine{Soffice: bin, Dir: t.TempDir(), Confine: func(argv []string) ([]string, []string, error) {
		confined = true
		return argv, nil, nil
	}}
	out, err := e.ToPDF(context.Background(), "a.docx", []byte("data"))
	if err != nil || string(out) != "%PDF-fake" {
		t.Fatalf("%q %v", out, err)
	}
	if !confined {
		t.Error("the engine did not go through the sandbox")
	}
	entries, _ := os.ReadDir(e.Dir)
	for _, x := range entries {
		if strings.HasPrefix(x.Name(), "engine-") && x.Name() != "engine-profile" {
			t.Errorf("left behind: %s", x.Name())
		}
	}
	settings, err := os.ReadFile(filepath.Join(e.Dir, "engine-profile", "user", "registrymodifications.xcu"))
	if err != nil || !strings.Contains(string(settings), "DisableMacrosExecution") {
		t.Errorf("the profile must turn macros off: %v", err)
	}
}

func TestEngineRefusedBySandboxDoesNotRun(t *testing.T) {
	bin := fakeSoffice(t, []byte("x"), "0")
	e := &Engine{Soffice: bin, Dir: t.TempDir(), Confine: func([]string) ([]string, []string, error) {
		return nil, nil, os.ErrPermission
	}}
	if _, err := e.ToPDF(context.Background(), "a.docx", []byte("d")); err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("%v", err)
	}
}

func TestEngineFailureIsNotAPass(t *testing.T) {
	e := &Engine{Soffice: "/bin/false", Dir: t.TempDir()}
	if _, err := e.ToPDF(context.Background(), "a.docx", []byte("d")); err == nil {
		t.Fatal("a failed conversion must be an error")
	}
	blocked := &Engine{Soffice: "/bin/false", Dir: t.TempDir(), Blocked: "the sandbox"}
	if blocked.Available() {
		t.Fatal("a blocked engine is not available")
	}
	if _, err := blocked.ToPDF(context.Background(), "a.docx", []byte("d")); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("%v", err)
	}
}

func TestRecalculateFindsDifferencesWithFake(t *testing.T) {
	orig, err := Create("a.xlsx", CreateSpec{Sheets: []SheetSpec{{Name: "S", Rows: [][]any{{1.0, "=A1+1"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	same := fakeSoffice(t, orig, "0")
	fs, err := (&Engine{Soffice: same, Dir: t.TempDir()}).Recalculate(context.Background(), "a.xlsx", orig)
	if err != nil || len(fs) != 0 {
		t.Fatalf("%v %v", fs, err)
	}
	pkg, _ := OpenPackage(orig)
	c := NewChanges()
	c.Set["xl/worksheets/sheet1.xml"] = []byte(strings.Replace(partOf(t, orig, "xl/worksheets/sheet1.xml"), "<v>2</v>", "<v>99</v>", 1))
	other, err := pkg.Write(c)
	if err != nil {
		t.Fatal(err)
	}
	diff := fakeSoffice(t, other, "0")
	fs, err = (&Engine{Soffice: diff, Dir: t.TempDir()}).Recalculate(context.Background(), "a.xlsx", orig)
	if err != nil || len(fs) != 1 || !strings.Contains(fs[0].Message, "99") {
		t.Fatalf("%v %v", fs, err)
	}
}

// The tests below use the real programs when this machine has them.

// realEngine is for a person checking this machine's LibreOffice on purpose. A default test run must never start it: a
// failed launch leaves crash reports on the developer's machine.
func realEngine(t *testing.T) *Engine {
	t.Helper()
	if os.Getenv("HARNESS_TEST_LIBREOFFICE") == "" {
		t.Skip("set HARNESS_TEST_LIBREOFFICE=1 to run this against the real LibreOffice")
	}
	bin := findSoffice()
	if bin == "" {
		t.Skip("no LibreOffice here")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{Soffice: bin, Dir: dir}
}

func TestRealEngineOpensWhatWeWrite(t *testing.T) {
	e := realEngine(t)
	ctx := context.Background()
	cases := map[string]CreateSpec{
		"a.docx": {Title: "Report", Blocks: []Block{{Type: "heading", Text: "One"}, {Type: "bullet", Text: "b"}, {Type: "table", Rows: [][]string{{"x", "y"}}}}},
		"a.pptx": {Slides: []SlideSpec{{Layout: "title", Title: "Hello deck", Text: "Sub"}, {Title: "Plan", Bullets: []string{"a"}, Notes: "n"}}},
		"a.xlsx": {Sheets: []SheetSpec{{Name: "S", Rows: [][]any{{"Total", 2.0, 3.0, "=SUM(B1:C1)"}}}}},
	}
	for name, spec := range cases {
		data, err := Create(name, spec)
		if err != nil {
			t.Fatal(err)
		}
		pdf, err := e.ToPDF(ctx, name, data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if n, err := PDFPageCount(pdf); err != nil || n < 1 {
			t.Errorf("%s: %d pages, %v", name, n, err)
		}
	}
}

func TestRealRecalculation(t *testing.T) {
	e := realEngine(t)
	data, err := Create("a.xlsx", CreateSpec{Sheets: []SheetSpec{{Name: "S", Rows: [][]any{{2.0, 3.0, "=SUM(A1:B1)", "=VLOOKUP(2,A1:B1,2,FALSE)"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	// The stored value of C1 is right, D1's is absent because the evaluator cannot compute VLOOKUP.
	fs, err := e.Recalculate(context.Background(), "a.xlsx", data)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range fs {
		if strings.Contains(f.Key, "S!C1") {
			t.Errorf("C1 was right: %s", f.Message)
		}
		if strings.Contains(f.Key, "S!D1") && strings.Contains(f.Message, "3") {
			found = true
		}
	}
	if !found {
		t.Errorf("the engine's VLOOKUP result was not seen: %v", fs)
	}
}

func TestRealRasterizer(t *testing.T) {
	if os.Getenv("HARNESS_TEST_PDFIUM") == "" {
		t.Skip("set HARNESS_TEST_PDFIUM=1: the first compile of the module takes about 20 s, far more under -race")
	}
	RasterCacheDir = filepath.Join(t.TempDir(), "cache")
	defer func() { RasterCacheDir = ""; closeRaster() }()
	var blocks []Block
	for i := 0; i < 3; i++ {
		blocks = append(blocks, Block{Text: "A page of text"}, Block{Type: "page_break"})
	}
	data, err := Create("p.pdf", CreateSpec{Blocks: blocks})
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := RenderPages(context.Background(), data, []int{1, 2}, OverviewDPI)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 2 || imgs[0].Bounds().Dx() < 100 {
		t.Fatalf("%d images", len(imgs))
	}
	sheet := Overview(imgs, []int{1, 2})
	if sheet.Bounds().Dx() <= imgs[0].Bounds().Dx() {
		t.Errorf("overview %v", sheet.Bounds())
	}
	if b, err := EncodePNG(sheet); err != nil || len(b) == 0 {
		t.Errorf("png: %v", err)
	}
}
