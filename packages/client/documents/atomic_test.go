package documents

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteAtomicKeepsModeAndLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.docx")
	if err := os.WriteFile(p, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(p, []byte("new"), 0); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	st, _ := os.Stat(p)
	if string(got) != "new" || st.Mode().Perm() != 0o640 {
		t.Errorf("%q %o", got, st.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}
}

func TestWriteAtomicFollowsSymlinksAndRefusesHardLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links need privileges there")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real.docx")
	if err := os.WriteFile(real, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.docx")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(link, []byte("new"), 0); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Lstat(link); st.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a file")
	}
	if got, _ := os.ReadFile(real); string(got) != "new" {
		t.Errorf("the target holds %q", got)
	}
	hard := filepath.Join(dir, "hard.docx")
	if err := os.Link(real, hard); err != nil {
		t.Fatal(err)
	}
	err := WriteAtomic(real, []byte("newer"), 0)
	if err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("%v", err)
	}
	if got, _ := os.ReadFile(hard); string(got) != "new" {
		t.Errorf("refused, yet the file changed: %q", got)
	}
}

func TestChangedAddressesListsNewFingerprints(t *testing.T) {
	data := docxFixture(t, false, "")
	before := mustOpen(t, "a.docx", data)
	addr, fp := addrOf(t, readAll(t, before, ""), "The end.")
	res := apply(t, "a.docx", data, Edit{Op: "set_text", Target: addr, Fingerprint: fp, Text: "Done."})
	lines := ChangedAddresses(before, mustOpen(t, "a.docx", res.Data), 10)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], addr+" #") || !strings.Contains(lines[0], "Done.") {
		t.Errorf("%v", lines)
	}
}
