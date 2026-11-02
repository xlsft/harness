package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

// Credentials are persisted between runs. DeviceToken is the only secret.
type Credentials struct {
	Server      string `json:"server"`
	DeviceID    string `json:"device_id"`
	DeviceToken string `json:"device_token"`
	User        User   `json:"user"`
}

// Store persists credentials.
type Store interface {
	Load() (*Credentials, error) // nil, nil when absent
	Save(*Credentials) error
	Delete() error
}

// FileStore keeps credentials in a JSON file with 0600 permissions.
type FileStore struct{ Path string }

func (f FileStore) Load() (*Credentials, error) {
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (f FileStore) Save(c *Credentials) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

func (f FileStore) Delete() error {
	err := os.Remove(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

const keyringService = "harness-client"
const keyringUser = "credentials"

// KeyringStore keeps credentials in the OS keychain and falls back to a
// FileStore when the keychain is unavailable (headless Linux, CI, …).
type KeyringStore struct {
	Fallback FileStore
}

func (k KeyringStore) Load() (*Credentials, error) {
	s, err := keyring.Get(keyringService, keyringUser)
	if err == nil {
		var c Credentials
		if jerr := json.Unmarshal([]byte(s), &c); jerr == nil {
			return &c, nil
		}
	}
	return k.Fallback.Load()
}

func (k KeyringStore) Save(c *Credentials) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := keyring.Set(keyringService, keyringUser, string(data)); err == nil {
		_ = k.Fallback.Delete()
		return nil
	}
	return k.Fallback.Save(c)
}

func (k KeyringStore) Delete() error {
	kerr := keyring.Delete(keyringService, keyringUser)
	ferr := k.Fallback.Delete()
	if kerr != nil && !errors.Is(kerr, keyring.ErrNotFound) && ferr != nil {
		return ferr
	}
	return nil
}

// DefaultStore returns the keychain store with file fallback at path, or
// only the file store when HARNESS_KEYRING=0.
func DefaultStore(path string) Store {
	if os.Getenv("HARNESS_KEYRING") == "0" {
		return FileStore{Path: path}
	}
	return KeyringStore{Fallback: FileStore{Path: path}}
}
