package hooks

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
)

// TrustStore remembers which content from the server may run as hooks on this machine. It is a file of this device
// (HARNESS_HOME/hook-trust.json) and is never synced: the point of it is that the server cannot grant it.
type TrustStore struct {
	Path string
	mu   sync.Mutex
}

// Trusted reports whether hash was trusted here. An unreadable file trusts nothing.
func (t *TrustStore) Trusted(hash string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Contains(t.read(), hash)
}

// Set trusts hash, or stops trusting it.
func (t *TrustStore) Set(hash string, trusted bool) error {
	if hash == "" {
		return errors.New("hash is required")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	list := slices.DeleteFunc(t.read(), func(h string) bool { return h == hash })
	if trusted {
		list = append(list, hash)
	}
	sort.Strings(list)
	data, err := json.MarshalIndent(map[string][]string{"trusted": list}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.Path), 0o700); err != nil {
		return err
	}
	tmp := t.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, t.Path)
}

func (t *TrustStore) read() []string {
	data, err := os.ReadFile(t.Path)
	if err != nil {
		return nil
	}
	var f struct {
		Trusted []string `json:"trusted"`
	}
	if json.Unmarshal(data, &f) != nil {
		return nil
	}
	return f.Trusted
}
