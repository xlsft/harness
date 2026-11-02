package permissions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xlsft/harness/packages/client/config"
)

// RepoRoot returns the nearest ancestor of dir containing .git, or dir.
func RepoRoot(dir string) string {
	d := filepath.Clean(dir)
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return filepath.Clean(dir)
		}
		d = parent
	}
}

var persistMu sync.Mutex

// PersistAllowRules merges rules into permissions.allow of the JSON settings
// file at path, keeping every other key. A file that is not valid JSON is
// left untouched and an error is returned.
func PersistAllowRules(path string, rules []string) error {
	persistMu.Lock()
	defer persistMu.Unlock()
	doc := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil && len(data) > 0:
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("%s is not valid JSON; rule not saved: %w", path, err)
		}
		if doc == nil {
			doc = map[string]any{}
		}
	case err != nil && !os.IsNotExist(err):
		return err
	}
	perms, _ := doc["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	allow, _ := perms["allow"].([]any)
	have := map[string]bool{}
	for _, a := range allow {
		if s, ok := a.(string); ok {
			have[s] = true
		}
	}
	for _, r := range rules {
		if !have[r] {
			allow = append(allow, r)
			have[r] = true
		}
	}
	perms["allow"] = allow
	doc["permissions"] = perms
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, append(out, '\n'), 0o644)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := fmt.Sprintf("%s.tmp-%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// TrustStore records trusted workspace directories in a JSON file
// (~/.harness/trust.json). Project hooks, .mcp.json servers and allow rules
// from committed project settings apply only in trusted workspaces.
type TrustStore struct {
	Path string
}

type trustFile struct {
	Trusted map[string]trustEntry `json:"trusted"`
}

type trustEntry struct {
	TrustedAt string `json:"trustedAt"`
}

func (s TrustStore) load() trustFile {
	var f trustFile
	data, err := os.ReadFile(s.Path)
	if err == nil {
		_ = json.Unmarshal(data, &f)
	}
	if f.Trusted == nil {
		f.Trusted = map[string]trustEntry{}
	}
	return f
}

// IsTrusted reports whether dir or one of its ancestors was trusted.
func (s TrustStore) IsTrusted(dir string) bool {
	f := s.load()
	d := evalPath(dir)
	for {
		if _, ok := f.Trusted[d]; ok {
			return true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return false
		}
		d = parent
	}
}

// Trust marks dir as trusted.
func (s TrustStore) Trust(dir string) error {
	persistMu.Lock()
	defer persistMu.Unlock()
	f := s.load()
	f.Trusted[evalPath(dir)] = trustEntry{TrustedAt: time.Now().UTC().Format(time.RFC3339)}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(s.Path, append(data, '\n'), 0o600)
}

// DefaultTrustStore is ~/.harness/trust.json (honoring HARNESS_HOME).
func DefaultTrustStore() TrustStore {
	return TrustStore{Path: filepath.Join(config.Home(), "trust.json")}
}

// IsTrusted reports whether the workspace dir is trusted on this machine.
func IsTrusted(dir string) bool { return DefaultTrustStore().IsTrusted(dir) }

// Trust records dir as trusted on this machine.
func Trust(dir string) error { return DefaultTrustStore().Trust(dir) }
