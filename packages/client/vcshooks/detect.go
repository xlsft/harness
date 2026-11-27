package vcshooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// The version control systems Detect knows.
const (
	Mercurial = "mercurial"
	Jujutsu   = "jujutsu"
	Perforce  = "perforce"
)

// Detect finds the version control of dir that git does not cover: walking up, the first folder with .git ends the
// search (git covers it; a Jujutsu repository colocated with git included), the first with .jj, .hg or a Perforce config
// file (P4CONFIG's name, else .p4config) names it. kind is "" when there is none.
func Detect(dir string) (kind, root string) {
	p4config := os.Getenv("P4CONFIG")
	if p4config == "" || filepath.Base(p4config) != p4config {
		p4config = ".p4config"
	}
	isDir := func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && st.IsDir()
	}
	exists := func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}
	d, err := filepath.Abs(dir)
	if err != nil {
		return "", ""
	}
	for {
		switch {
		case exists(filepath.Join(d, ".git")):
			return "", ""
		case isDir(filepath.Join(d, ".jj")):
			return Jujutsu, d
		case isDir(filepath.Join(d, ".hg")):
			return Mercurial, d
		case exists(filepath.Join(d, p4config)):
			return Perforce, d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", ""
		}
		d = parent
	}
}

// Notices remembers the working copies whose missing hooks were pointed out on this device, so each gets the notice
// once. A file of this device (HARNESS_HOME/vcs-notices.json), never synced: it says which folders exist here.
type Notices struct {
	Path string
	mu   sync.Mutex
}

// First records root and reports whether it was not recorded before. An unreadable file counts as empty.
func (n *Notices) First(root string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	var f struct {
		Shown []string `json:"shown"`
	}
	if data, err := os.ReadFile(n.Path); err == nil {
		_ = json.Unmarshal(data, &f)
	}
	if slices.Contains(f.Shown, root) {
		return false
	}
	f.Shown = append(f.Shown, root)
	if data, err := json.MarshalIndent(f, "", "  "); err == nil {
		if err := os.MkdirAll(filepath.Dir(n.Path), 0o700); err == nil {
			tmp := n.Path + ".tmp"
			if os.WriteFile(tmp, data, 0o600) == nil {
				_ = os.Rename(tmp, n.Path)
			}
		}
	}
	return true
}
