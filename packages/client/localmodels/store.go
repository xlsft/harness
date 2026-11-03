package localmodels

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"
)

// storeFile is HARNESS_HOME/local-providers.json: user-added providers plus the settings changed on built-in
// ones (enabled, context length). It never leaves the machine.
type storeFile struct {
	Providers []ProviderConfig `json:"providers"`
}

func loadStore(path string) ([]ProviderConfig, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f storeFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return f.Providers, nil
}

func saveStore(path string, providers []ProviderConfig) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(storeFile{Providers: providers}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Secrets stores provider API keys on this machine.
type Secrets interface {
	// Get returns "" when no key is stored.
	Get(providerID string) (string, error)
	Set(providerID, key string) error
	Delete(providerID string) error
}

const keyringService = "harness-client"

func keyringUser(providerID string) string { return "local-provider:" + providerID }

// KeyringSecrets keeps keys in the OS keychain, falling back to a 0600 JSON file (headless Linux, CI, or
// HARNESS_KEYRING=0).
type KeyringSecrets struct {
	// FallbackPath is the JSON file used when the keychain is unavailable.
	FallbackPath string
	// NoKeyring uses the file only.
	NoKeyring bool
	mu        sync.Mutex
}

func (k *KeyringSecrets) Get(id string) (string, error) {
	if !k.NoKeyring {
		if s, err := keyring.Get(keyringService, keyringUser(id)); err == nil {
			return s, nil
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.readFile()
	if err != nil {
		return "", err
	}
	return m[id], nil
}

func (k *KeyringSecrets) Set(id, key string) error {
	if !k.NoKeyring {
		if err := keyring.Set(keyringService, keyringUser(id), key); err == nil {
			k.mu.Lock()
			defer k.mu.Unlock()
			return k.deleteFromFile(id)
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.readFile()
	if err != nil {
		return err
	}
	m[id] = key
	return k.writeFile(m)
}

func (k *KeyringSecrets) Delete(id string) error {
	if !k.NoKeyring {
		_ = keyring.Delete(keyringService, keyringUser(id))
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.deleteFromFile(id)
}

func (k *KeyringSecrets) deleteFromFile(id string) error {
	m, err := k.readFile()
	if err != nil {
		return err
	}
	if _, ok := m[id]; !ok {
		return nil
	}
	delete(m, id)
	return k.writeFile(m)
}

func (k *KeyringSecrets) readFile() (map[string]string, error) {
	m := map[string]string{}
	if k.FallbackPath == "" {
		return m, nil
	}
	data, err := os.ReadFile(k.FallbackPath)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (k *KeyringSecrets) writeFile(m map[string]string) error {
	if k.FallbackPath == "" {
		return errors.New("no keychain and no fallback file for local provider keys")
	}
	if len(m) == 0 {
		err := os.Remove(k.FallbackPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(k.FallbackPath), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := k.FallbackPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, k.FallbackPath)
}

// MemorySecrets keeps keys in memory (tests).
type MemorySecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *MemorySecrets) Get(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id], nil
}

func (s *MemorySecrets) Set(id, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[id] = key
	return nil
}

func (s *MemorySecrets) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}
