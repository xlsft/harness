package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/documents"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

func diffDoc(t *testing.T, name string, spec documents.CreateSpec) []byte {
	t.Helper()
	data, err := documents.Create(name, spec)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func editDoc(t *testing.T, name string, data []byte, edits ...documents.Edit) []byte {
	t.Helper()
	doc, err := documents.Open(name, data)
	if err != nil {
		t.Fatal(err)
	}
	res, err := doc.Apply(edits)
	if err != nil {
		t.Fatal(err)
	}
	return res.Data
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestSemanticDiffOnTheGitPath(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "-q")
	p := filepath.Join(dir, "report.docx")
	orig := diffDoc(t, p, documents.CreateSpec{Blocks: []documents.Block{{Text: "Keep this"}, {Text: "Old sentence"}}})
	if err := os.WriteFile(p, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "first")
	if err := os.WriteFile(p, editDoc(t, p, orig, documents.Edit{Op: "replace_text", Find: "Old", Text: "New"}), 0o644); err != nil {
		t.Fatal(err)
	}
	r := diffOf(t, &diffSources{cwd: dir, dirs: []string{dir}})
	f := wantDiff(t, r, "report.docx")
	if f.Binary || f.Additions != 1 || f.Deletions != 1 {
		t.Fatalf("%+v", f)
	}
	text := hunkText(f)
	if !strings.Contains(text, "-[document] Old sentence") || !strings.Contains(text, "+[document] New sentence") {
		t.Errorf("%s", text)
	}
	if !strings.Contains(f.Hunks[0].Header, "content") {
		t.Errorf("header %q", f.Hunks[0].Header)
	}
	one, err := computeSessionFileDiff(context.Background(), &diffSources{cwd: dir, dirs: []string{dir}}, nil, p)
	if err != nil || len(one.Hunks) == 0 {
		t.Errorf("one file: %v %+v", err, one)
	}
}

func TestSemanticDiffOnTheDiskPath(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "book.xlsx")
	base := filepath.Join(dir, "backup.bin")
	orig := diffDoc(t, p, documents.CreateSpec{Sheets: []documents.SheetSpec{{Name: "S", Rows: [][]any{{1.0, "=A1+1"}}}}})
	if err := os.WriteFile(base, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, editDoc(t, p, orig, documents.Edit{Op: "set_cell", Target: "S!C1", Value: "note"}), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &diffBuilder{budget: maxDiffTotalBytes}
	f := b.diffOnDisk(p, p, base, agentv1.FileDiffSource_FILE_DIFF_SOURCE_HISTORY)
	if f == nil || f.Binary || f.Additions != 1 || !strings.Contains(hunkText(f), `S!C1: "note"`) {
		t.Fatalf("%+v", f)
	}
}

func TestBrokenDocumentStaysBinaryAndLargeOnesAreBounded(t *testing.T) {
	if r := documentDiff("a.docx", []byte("not a zip"), []byte("nor this")); !r.binary {
		t.Errorf("%+v", r)
	}
	if diffFileLimit("a.docx") <= diffFileLimit("a.txt") {
		t.Error("documents have their own limit, being compressed")
	}
	if r := documentDiff("a.xlsx", nil, nil); r.binary {
		t.Error("no sides is nothing to show, not binary")
	}
}
