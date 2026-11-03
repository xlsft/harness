package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/session"
)

// A working directory outside git has nothing to compare with, so the first measurement of a session records it:
// the size and mtime of each file and a copy of its content. Whatever changes after that, by any means (Bash
// included), then shows in the diff. Kept in bootstrap.FolderBaselineDir, so it survives a client restart.

const (
	// maxFolderEntries caps what a folder's walk visits; maxFolderBytes the content one snapshot copies. A file over
	// maxDiffFileBytes, or past that budget, is recorded without its content.
	maxFolderEntries = 20000
	maxFolderBytes   = 64 << 20
	// maxFolderBaselines caps the snapshots held in memory; the others load from disk when asked for.
	maxFolderBaselines = 32
)

// folderFile is a file as the snapshot found it. Blob names the copy of its content; "" when it was not copied.
type folderFile struct {
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
	Blob  string `json:"blob,omitempty"`
}

// folderBaseline is one directory's snapshot. Partial: the walk did not finish (too many files, or out of time), so
// a file it does not list may simply not have been reached: only the listed files are compared.
type folderBaseline struct {
	Partial bool                  `json:"partial,omitempty"`
	Files   map[string]folderFile `json:"files"`
	store   string
}

var folderBaselines = struct {
	sync.Mutex
	held map[string]*folderBaseline
}{held: map[string]*folderBaseline{}}

// folderBaselineOf returns the snapshot of the resolved directory root, taking it if the session has none yet. Held
// under the lock so that measurements at the same moment share one snapshot.
func folderBaselineOf(ctx context.Context, store, root string, skip map[string]bool) *folderBaseline {
	sum := sha256.Sum256([]byte(root))
	name := filepath.Join(store, hex.EncodeToString(sum[:12])+".json")
	folderBaselines.Lock()
	defer folderBaselines.Unlock()
	if fb := folderBaselines.held[name]; fb != nil {
		return fb
	}
	fb := loadFolderBaseline(name)
	if fb == nil {
		fb = takeFolderBaseline(ctx, store, root, skip)
		if raw, err := json.Marshal(fb); err == nil && os.MkdirAll(store, 0o700) == nil {
			_ = os.WriteFile(name, raw, 0o600)
		}
	}
	fb.store = store
	if len(folderBaselines.held) >= maxFolderBaselines {
		folderBaselines.held = map[string]*folderBaseline{}
	}
	folderBaselines.held[name] = fb
	return fb
}

func loadFolderBaseline(name string) *folderBaseline {
	raw, err := os.ReadFile(name)
	if err != nil {
		return nil
	}
	fb := &folderBaseline{}
	if json.Unmarshal(raw, fb) != nil || fb.Files == nil {
		return nil
	}
	return fb
}

func takeFolderBaseline(ctx context.Context, store, root string, skip map[string]bool) *folderBaseline {
	fb := &folderBaseline{Files: map[string]folderFile{}}
	budget := int64(maxFolderBytes)
	fb.Partial = !walkFolder(ctx, root, skip, func(p, rel string, st fs.FileInfo) bool {
		f := folderFile{Size: st.Size(), Mtime: st.ModTime().UnixNano()}
		if st.Size() <= maxDiffFileBytes && st.Size() <= budget {
			if blob := copyToStore(store, p, st); blob != "" {
				f.Blob = blob
				budget -= st.Size()
			}
		}
		fb.Files[rel] = f
		return true
	})
	return fb
}

// copyToStore stores the content of p under its hash; for a symlink, its target, as compareFiles reads it.
func copyToStore(store, p string, st fs.FileInfo) string {
	var data []byte
	if st.Mode()&fs.ModeSymlink != 0 {
		t, err := os.Readlink(p)
		if err != nil {
			return ""
		}
		data = []byte(t)
	} else {
		var err error
		if data, err = os.ReadFile(p); err != nil {
			return ""
		}
	}
	sum := sha256.Sum256(data)
	blob := hex.EncodeToString(sum[:])
	if os.MkdirAll(store, 0o700) != nil {
		return ""
	}
	target := filepath.Join(store, blob)
	if _, err := os.Stat(target); err == nil {
		return blob
	}
	if os.WriteFile(target, data, 0o600) != nil {
		return ""
	}
	return blob
}

// walkFolder calls fn for the regular files and symlinks under the resolved directory root, leaving out what is
// always ignored, the directories in skip, and nested repositories, which have their own history. False when it
// stopped early: too many entries, ctx done, or fn said so.
func walkFolder(ctx context.Context, root string, skip map[string]bool, fn func(p, rel string, st fs.FileInfo) bool) bool {
	done, entries := true, 0
	_ = filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			if de != nil && de.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		if entries++; entries > maxFolderEntries || ctx.Err() != nil {
			done = false
			return filepath.SkipAll
		}
		if de.IsDir() {
			if alwaysIgnored[de.Name()] || skip[p] {
				return filepath.SkipDir
			}
			if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if alwaysIgnored[de.Name()] {
			return nil
		}
		st, err := de.Info()
		if err != nil || (!st.Mode().IsRegular() && st.Mode()&fs.ModeSymlink == 0) {
			return nil
		}
		if !fn(p, relSlash(root, p), st) {
			done = false
			return filepath.SkipAll
		}
		return nil
	})
	return done
}

// sweepFolderBaselines drops the snapshots of sessions that left no transcript (opened and never used). Runs before
// the client serves, so no session is being opened.
func sweepFolderBaselines() {
	root := filepath.Dir(bootstrap.FolderBaselineDir("x"))
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	store := session.Store{Root: config.ProjectsDir()}
	for _, e := range entries {
		if _, err := store.Find("", e.Name()); err != nil && readAcpMeta(e.Name()) == nil {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// folderSkips are the directories that hold the session's own files, not the project's.
func (b *diffBuilder) folderSkips() map[string]bool {
	skip := map[string]bool{}
	for _, d := range []string{b.src.scratch, b.src.private, b.src.harnessHome} {
		if d != "" {
			skip[resolvedPath(d)] = true
		}
	}
	return skip
}

// folderTooBroad: the home directory and the file system root are far too big to mean a project.
func folderTooBroad(real string) bool {
	if real == filepath.Dir(real) {
		return true
	}
	home, err := os.UserHomeDir()
	return err == nil && resolvedPath(home) == real
}

// addFolder adds what changed in the directory (clean as the session knows it, real resolved) since its snapshot.
func (b *diffBuilder) addFolder(ctx context.Context, clean, real string) {
	skip := b.folderSkips()
	if skip[real] || folderTooBroad(real) {
		return
	}
	fb := folderBaselineOf(ctx, b.src.folderStore, real, skip)
	rels := make([]string, 0, len(fb.Files))
	for rel := range fb.Files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		p := filepath.Join(real, filepath.FromSlash(rel))
		if b.seen[p] {
			continue
		}
		b.seen[p] = true
		f := fb.Files[rel]
		st, err := os.Lstat(p)
		if err == nil && st.Size() == f.Size && st.ModTime().UnixNano() == f.Mtime {
			continue
		}
		if b.full() {
			continue
		}
		path := lexicalPath(clean, real, p)
		if f.Blob != "" {
			b.add(b.diffOnDisk(path, p, filepath.Join(fb.store, f.Blob), agentv1.FileDiffSource_FILE_DIFF_SOURCE_HISTORY))
			continue
		}
		// Not copied: known to have changed, not how.
		d := &agentv1.FileDiff{Path: sanitizeUTF8(path), Status: agentv1.FileDiffStatus_FILE_DIFF_STATUS_MODIFIED, TooLarge: true, Source: agentv1.FileDiffSource_FILE_DIFF_SOURCE_HISTORY}
		if err != nil {
			d.Status = agentv1.FileDiffStatus_FILE_DIFF_STATUS_DELETED
		}
		b.add(d)
	}
	if fb.Partial {
		return
	}
	finished := walkFolder(ctx, real, skip, func(p, rel string, _ fs.FileInfo) bool {
		if _, known := fb.Files[rel]; known || b.seen[p] {
			return true
		}
		b.seen[p] = true
		if b.full() {
			return false
		}
		b.add(b.diffOnDisk(lexicalPath(clean, real, p), p, "", agentv1.FileDiffSource_FILE_DIFF_SOURCE_NEW_FILE))
		return true
	})
	if !finished && ctx.Err() == nil {
		b.truncated = true
	}
}
