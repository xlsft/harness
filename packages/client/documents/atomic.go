package documents

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteAtomic replaces a file in one step: a temporary file beside it, synced, then renamed over it. A symlink keeps
// pointing where it did: the file it points to is what gets replaced. A file with hard links is refused, since a rename
// would silently split it from them.
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	real := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		real = r
	}
	if st, err := os.Stat(real); err == nil {
		if n := fileLinkCount(st); n > 1 {
			return fmt.Errorf("%s has %d hard links, and an atomic write would split it from them; copy it first", path, n)
		}
		if mode == 0 {
			mode = st.Mode().Perm()
		}
	}
	if mode == 0 {
		mode = 0o644
	}
	dir := filepath.Dir(real)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".harness-write-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // harmless once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	if err := os.Rename(name, real); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// ChangedAddresses lists what a change left with a new address or fingerprint, so a following edit needs no new read.
func ChangedAddresses(before, after Document, limit int) []string {
	old := map[string]bool{}
	for _, l := range addressLines(before) {
		old[l] = true
	}
	var out []string
	for _, l := range addressLines(after) {
		if !old[l] {
			if len(out) == limit {
				out = append(out, "…")
				break
			}
			out = append(out, l)
		}
	}
	return out
}

func addressLines(doc Document) []string {
	var out []string
	switch d := doc.(type) {
	case *docxDoc:
		for _, s := range d.stories {
			for _, p := range s.paras {
				out = append(out, p.line())
			}
		}
		for _, c := range d.changes {
			out = append(out, d.changeLine(c))
		}
	case *xlsxDoc:
		for _, s := range d.sheets {
			for _, c := range s.order {
				if c.hasContent() {
					out = append(out, s.name+"!"+s.cellLine(d, c))
				}
			}
		}
	case *pptxDoc:
		for _, s := range d.slides {
			for _, sh := range s.shapes {
				out = append(out, sh.line())
			}
		}
	}
	return out
}
