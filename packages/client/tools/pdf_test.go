package tools

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/llm"
)

// tinyPDF builds a valid PDF of n empty pages.
func tinyPDF(n int) []byte {
	var b bytes.Buffer
	offsets := map[int]int{}
	obj := func(id int, body string) {
		offsets[id] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", id, body)
	}
	b.WriteString("%PDF-1.4\n")
	kids := make([]string, n)
	for i := range n {
		kids[i] = fmt.Sprintf("%d 0 R", i+3)
	}
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n))
	for i := range n {
		obj(i+3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>")
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", n+3)
	for id := 1; id <= n+2; id++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[id])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", n+3, xref)
	return b.Bytes()
}

func writePDF(t *testing.T, env *Env, pages int) string {
	t.Helper()
	p := filepath.Join(env.Cwd, "doc.pdf")
	if err := os.WriteFile(p, tinyPDF(pages), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func documentPages(t *testing.T, r Result) int {
	t.Helper()
	for _, c := range r.Content {
		if c.Type == llm.BlockDocument {
			data, err := base64.StdEncoding.DecodeString(c.Source.Data)
			if err != nil {
				t.Fatal(err)
			}
			if c.Source.MediaType != "application/pdf" {
				t.Fatalf("media type %s", c.Source.MediaType)
			}
			n, err := pdfPageCount(data)
			if err != nil {
				t.Fatalf("the result is not a readable PDF: %v", err)
			}
			return n
		}
	}
	t.Fatalf("no document block: %s", text(r))
	return 0
}

func TestReadPDFPages(t *testing.T) {
	env := newEnv(t)
	p := writePDF(t, env, 30)

	// A range, a list, a mix, a single page; a range past the end stops at the end.
	for spec, want := range map[string]int{"3": 1, "2-5": 4, "1,3,7-9": 5, " 2 - 3 , 3 ": 2, "29-40": 2} {
		r := call(t, env, &Read{}, map[string]any{"file_path": p, "pages": spec})
		if r.IsError {
			t.Fatalf("%q: %s", spec, text(r))
		}
		if got := documentPages(t, r); got != want {
			t.Errorf("%q: %d pages, want %d", spec, got, want)
		}
		if !strings.Contains(text(r), "of 30") {
			t.Errorf("%q: the model should hear how long the PDF is: %s", spec, text(r))
		}
	}

	for spec, msg := range map[string]string{
		"31":         "does not exist",
		"0":          "page numbers",
		"abc":        "page numbers",
		"5-2":        "range",
		"1-30":       "more than 20",
		"1-15,20-30": "at most 20",
	} {
		r := call(t, env, &Read{}, map[string]any{"file_path": p, "pages": spec})
		if !r.IsError || !strings.Contains(text(r), msg) {
			t.Errorf("%q: %s", spec, text(r))
		}
	}
}

func TestReadPDFWhole(t *testing.T) {
	env := newEnv(t)
	p := writePDF(t, env, 3)
	r := call(t, env, &Read{}, map[string]any{"file_path": p})
	if r.IsError || documentPages(t, r) != 3 {
		t.Fatalf("%s", text(r))
	}

	// Too long to read at once: the error says what to do.
	long := writePDF(t, env, ReadPDFWholeMaxPages+1)
	r = call(t, env, &Read{}, map[string]any{"file_path": long})
	if !r.IsError || !strings.Contains(text(r), "pages") || !strings.Contains(text(r), "101") {
		t.Fatalf("%s", text(r))
	}
	r = call(t, env, &Read{}, map[string]any{"file_path": long, "pages": "90-101"})
	if r.IsError || documentPages(t, r) != 12 {
		t.Fatalf("%s", text(r))
	}

	// What the parser can't read still goes whole to a model that might, but pages can't be picked from it.
	bad := filepath.Join(env.Cwd, "bad.pdf")
	if err := os.WriteFile(bad, []byte("%PDF-1.4 not really"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = call(t, env, &Read{}, map[string]any{"file_path": bad})
	if r.IsError || r.Content[0].Type != llm.BlockDocument {
		t.Fatalf("%s", text(r))
	}
	if r := call(t, env, &Read{}, map[string]any{"file_path": bad, "pages": "1"}); !r.IsError {
		t.Fatal("pages of an unreadable PDF")
	}
}

func TestReadPagesOnOtherFiles(t *testing.T) {
	env := newEnv(t)
	p := filepath.Join(env.Cwd, "a.txt")
	if err := os.WriteFile(p, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := call(t, env, &Read{}, map[string]any{"file_path": p, "pages": "1"}); !r.IsError || !strings.Contains(text(r), "PDF") {
		t.Fatalf("%s", text(r))
	}
}
