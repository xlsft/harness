package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxCheckpoints is how many checkpoints keep their file snapshots.
const MaxCheckpoints = 100

// FileHistory takes file checkpoints for one session: before every turn
// started by a user prompt, and the pre-edit state of every file first
// touched by a file tool during a turn. Backups live in
// ~/.harness/file-history/<session-id>/<path-hash>@v<n>; the transcript gets
// file-history-snapshot records pointing at them.
type FileHistory struct {
	mu        sync.Mutex
	dir       string
	w         *Writer
	snapshots []*FileSnapshot
	versions  map[string]int    // path -> latest version
	first     map[string]string // path -> backup name of its first snapshot
	max       int
}

// NewFileHistory opens the history in dir, restoring snapshots read from the
// transcript (resume). w may be nil (no records).
func NewFileHistory(dir string, w *Writer, restored []FileSnapshot) *FileHistory {
	h := &FileHistory{dir: dir, w: w, versions: map[string]int{}, first: map[string]string{}, max: MaxCheckpoints}
	for i := range restored {
		s := restored[i]
		h.snapshots = append(h.snapshots, &s)
		for p, b := range s.TrackedFileBackups {
			if b.Version > h.versions[p] {
				h.versions[p] = b.Version
			}
			if b.BackupFileName != nil {
				if cur, ok := h.first[p]; !ok || versionOf(*b.BackupFileName) < versionOf(cur) {
					h.first[p] = *b.BackupFileName
				}
			}
		}
	}
	return h
}

func versionOf(name string) int {
	i := strings.LastIndex(name, "@v")
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(name[i+2:])
	return n
}

func pathHash(p string) string {
	s := sha256.Sum256([]byte(p))
	return hex.EncodeToString(s[:8])
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// backup records the current state of path as a new version (or reuses the
// latest version when the content is unchanged).
func (h *FileHistory) backup(path string, latest *FileBackup) (FileBackup, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return FileBackup{BackupFileName: nil, Version: h.versions[path], BackupTime: now()}, nil
	}
	if err != nil {
		return FileBackup{}, err
	}
	if latest != nil && latest.BackupFileName != nil {
		if old, err := os.ReadFile(filepath.Join(h.dir, *latest.BackupFileName)); err == nil && bytes.Equal(old, data) {
			return *latest, nil
		}
	}
	v := h.versions[path] + 1
	name := fmt.Sprintf("%s@v%d", pathHash(path), v)
	if err := os.MkdirAll(h.dir, 0o700); err != nil {
		return FileBackup{}, err
	}
	if err := os.WriteFile(filepath.Join(h.dir, name), data, 0o600); err != nil {
		return FileBackup{}, err
	}
	h.versions[path] = v
	if _, ok := h.first[path]; !ok {
		h.first[path] = name
	}
	return FileBackup{BackupFileName: &name, Version: v, BackupTime: now()}, nil
}

func (h *FileHistory) record(s *FileSnapshot, update bool) {
	if h.w == nil {
		return
	}
	cp := *s
	cp.TrackedFileBackups = map[string]FileBackup{}
	for k, v := range s.TrackedFileBackups {
		cp.TrackedFileBackups[k] = v
	}
	_, _ = h.w.Append(Entry{Type: TypeFileHistorySnapshot, MessageID: s.MessageID, Snapshot: &cp, IsSnapshotUpdate: update})
}

// MakeCheckpoint snapshots every tracked file before the turn started by
// the user message messageID.
func (h *FileHistory) MakeCheckpoint(messageID string) FileSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := &FileSnapshot{MessageID: messageID, TrackedFileBackups: map[string]FileBackup{}, Timestamp: now()}
	if n := len(h.snapshots); n > 0 {
		for p, b := range h.snapshots[n-1].TrackedFileBackups {
			prev := b
			nb, err := h.backup(p, &prev)
			if err != nil {
				nb = b
			}
			s.TrackedFileBackups[p] = nb
		}
	}
	h.snapshots = append(h.snapshots, s)
	h.record(s, false)
	h.prune()
	return *s
}

// TrackEdit records the pre-edit state of path in the current checkpoint
// the first time a file tool touches it during the turn.
func (h *FileHistory) TrackEdit(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := len(h.snapshots)
	if n == 0 {
		return
	}
	cur := h.snapshots[n-1]
	if _, ok := cur.TrackedFileBackups[path]; ok {
		return
	}
	b, err := h.backup(path, nil)
	if err != nil {
		return
	}
	cur.TrackedFileBackups[path] = b
	h.record(cur, true)
}

func (h *FileHistory) prune() {
	if len(h.snapshots) <= h.max {
		return
	}
	h.snapshots = h.snapshots[len(h.snapshots)-h.max:]
	keep := map[string]bool{}
	for _, name := range h.first {
		keep[name] = true
	}
	for _, s := range h.snapshots {
		for _, b := range s.TrackedFileBackups {
			if b.BackupFileName != nil {
				keep[*b.BackupFileName] = true
			}
		}
	}
	entries, _ := os.ReadDir(h.dir)
	for _, e := range entries {
		if !keep[e.Name()] {
			os.Remove(filepath.Join(h.dir, e.Name()))
		}
	}
}

// Checkpoints returns the retained checkpoints, oldest first.
func (h *FileHistory) Checkpoints() []FileSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]FileSnapshot, len(h.snapshots))
	for i, s := range h.snapshots {
		out[i] = *s
	}
	return out
}

// targets returns the state each tracked file had at checkpoint messageID.
func (h *FileHistory) targets(messageID string) (map[string]FileBackup, error) {
	idx := -1
	for i, s := range h.snapshots {
		if s.MessageID == messageID {
			idx = i
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("no checkpoint for message %s", messageID)
	}
	out := map[string]FileBackup{}
	for p, b := range h.snapshots[idx].TrackedFileBackups {
		out[p] = b
	}
	// Files first touched after the checkpoint: their first pre-edit
	// snapshot is their state at the checkpoint.
	for _, s := range h.snapshots[idx+1:] {
		for p, b := range s.TrackedFileBackups {
			if _, ok := out[p]; !ok {
				out[p] = b
			}
		}
	}
	return out, nil
}

// HasChanges reports whether restoring checkpoint messageID would change
// any file.
func (h *FileHistory) HasChanges(messageID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, err := h.targets(messageID)
	if err != nil {
		return false
	}
	for p, b := range t {
		cur, err := os.ReadFile(p)
		exists := err == nil
		if b.BackupFileName == nil {
			if exists {
				return true
			}
			continue
		}
		want, err := os.ReadFile(filepath.Join(h.dir, *b.BackupFileName))
		if err != nil {
			continue
		}
		if !exists || !bytes.Equal(cur, want) {
			return true
		}
	}
	return false
}

// RestoreResult reports a code rewind.
type RestoreResult struct {
	Restored []string
	Skipped  []string
	// Reasons maps skipped paths to the reason (symlink, hardlink, missing snapshot).
	Reasons map[string]string
}

// Restore puts every tracked file back to its state at checkpoint
// messageID. Symlinks, hardlinks and files whose snapshot is gone are
// skipped.
func (h *FileHistory) Restore(messageID string) (RestoreResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	res := RestoreResult{Reasons: map[string]string{}}
	t, err := h.targets(messageID)
	if err != nil {
		return res, err
	}
	paths := make([]string, 0, len(t))
	for p := range t {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	skip := func(p, why string) {
		res.Skipped = append(res.Skipped, p)
		res.Reasons[p] = why
	}
	for _, p := range paths {
		b := t[p]
		st, lerr := os.Lstat(p)
		if lerr == nil {
			if st.Mode()&os.ModeSymlink != 0 {
				skip(p, "symlink")
				continue
			}
			if linkCount(st) > 1 {
				skip(p, "hardlink")
				continue
			}
		}
		if b.BackupFileName == nil {
			if lerr == nil {
				if err := os.Remove(p); err != nil {
					skip(p, err.Error())
					continue
				}
				res.Restored = append(res.Restored, p)
			}
			continue
		}
		want, err := os.ReadFile(filepath.Join(h.dir, *b.BackupFileName))
		if err != nil {
			skip(p, "missing snapshot")
			continue
		}
		if cur, err := os.ReadFile(p); err == nil && bytes.Equal(cur, want) {
			continue
		}
		mode := os.FileMode(0o644)
		if lerr == nil {
			mode = st.Mode().Perm()
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			skip(p, err.Error())
			continue
		}
		if err := os.WriteFile(p, want, mode); err != nil {
			skip(p, err.Error())
			continue
		}
		res.Restored = append(res.Restored, p)
	}
	return res, nil
}

// CopyDir copies a file-history directory (used when forking a session).
func CopyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}
