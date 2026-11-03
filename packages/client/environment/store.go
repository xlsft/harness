package environment

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

// Environment is the stored environment of a primary folder.
type Environment struct {
	// Folder is the key: the primary folder's absolute path (see Key).
	Folder string
	Name   string
	// Worktree and BaseRef are the new-session defaults of the primary folder.
	Worktree bool
	BaseRef  string
	// Additional are extra working directories.
	Additional []Folder
	// Env is the env text as written (.env syntax).
	Env string
	// UpdatedAt is zero when the environment is not stored.
	UpdatedAt time.Time
}

// Folder is an additional folder of an environment.
type Folder struct {
	Path     string `json:"path"`
	Worktree bool   `json:"worktree,omitempty"`
	BaseRef  string `json:"base_ref,omitempty"`
}

// Key canonicalizes a folder path for lookups: absolute, cleaned, symlinks resolved when the folder exists.
func Key(folder string) string {
	if folder == "" {
		return ""
	}
	p, err := filepath.Abs(folder)
	if err != nil {
		p = filepath.Clean(folder)
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
}

// file is the on-disk form. The env text is sealed with AES-256-GCM (nonce || ciphertext, base64), with the
// folder as additional data so a sealed text cannot be moved to another folder's file.
type file struct {
	Version    int       `json:"version"`
	Folder     string    `json:"folder"`
	Name       string    `json:"name,omitempty"`
	Worktree   bool      `json:"worktree,omitempty"`
	BaseRef    string    `json:"base_ref,omitempty"`
	Additional []Folder  `json:"additional_folders,omitempty"`
	EnvSealed  string    `json:"env_sealed,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Store keeps environments as one 0600 JSON file per folder in Dir (0700), named after a hash of the key.
type Store struct {
	Dir string
	// Keys provides the encryption key of env texts (DefaultKeys when nil).
	Keys KeySource

	mu  sync.Mutex
	key []byte // cached key (mu)
}

// NewStore returns a store in dir with the default key source.
func NewStore(dir string) *Store { return &Store{Dir: dir} }

// cryptKey returns the key (cached after the first read, so the keychain is asked once per process).
// Called with mu held.
func (s *Store) cryptKey(create bool) ([]byte, error) {
	if s.key != nil {
		return s.key, nil
	}
	src := s.Keys
	if src == nil {
		src = DefaultKeys(filepath.Join(s.Dir, "key"))
	}
	k, err := src.Key(create)
	if err != nil {
		return nil, err
	}
	s.key = k
	return k, nil
}

func (s *Store) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.Dir, hex.EncodeToString(sum[:16])+".json")
}

// Get returns the environment of folder; nil, nil when none is stored.
func (s *Store) Get(folder string) (*Environment, error) {
	key := Key(folder)
	if key == "" {
		return nil, errors.New("folder is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("environment of %s: %w", key, err)
	}
	if f.Folder != key {
		return nil, nil // a hash collision, practically impossible
	}
	env := &Environment{Folder: key, Name: f.Name, Worktree: f.Worktree, BaseRef: f.BaseRef, Additional: f.Additional, UpdatedAt: f.UpdatedAt}
	if f.EnvSealed != "" {
		k, err := s.cryptKey(false)
		if err != nil {
			return nil, fmt.Errorf("environment variables of %s: %w", key, err)
		}
		if env.Env, err = open(k, f.EnvSealed, key); err != nil {
			return nil, fmt.Errorf("environment variables of %s: %w", key, err)
		}
	}
	return env, nil
}

// Put stores env under Key(env.Folder) and returns the stored form (UpdatedAt set). The env text must parse.
func (s *Store) Put(env Environment) (*Environment, error) {
	key := Key(env.Folder)
	if key == "" {
		return nil, errors.New("folder is required")
	}
	if _, err := Parse(env.Env); err != nil {
		return nil, err
	}
	env.Folder = key
	env.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	f := file{Version: 1, Folder: key, Name: env.Name, Worktree: env.Worktree, BaseRef: env.BaseRef, Additional: env.Additional, UpdatedAt: env.UpdatedAt}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(env.Env) != "" {
		k, err := s.cryptKey(true)
		if err != nil {
			return nil, fmt.Errorf("encryption key: %w", err)
		}
		if f.EnvSealed, err = seal(k, env.Env, key); err != nil {
			return nil, err
		}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writePrivate(s.path(key), data); err != nil {
		return nil, err
	}
	return &env, nil
}

// Delete removes the environment of folder (no error when there is none).
func (s *Store) Delete(folder string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(Key(folder)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// writePrivate writes data atomically with 0600 permissions in a 0700 directory.
func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func seal(key []byte, plain, aad string) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(plain), []byte(aad))
	return base64.StdEncoding.EncodeToString(out), nil
}

func open(key []byte, sealed, aad string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("sealed value is too short")
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], []byte(aad))
	if err != nil {
		return "", errors.New("cannot decrypt (the encryption key changed or the file was modified)")
	}
	return string(plain), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// KeySource provides the 32-byte key env texts are sealed with.
type KeySource interface {
	// Key returns the key; with create it makes and stores one when there is none.
	Key(create bool) ([]byte, error)
}

const (
	keyringService = "harness-client"
	keyringUser    = "environment-key"
)

// ErrNoKey is returned when no key is stored and none may be created.
var ErrNoKey = errors.New("no encryption key is stored")

// DefaultKeys keeps the key in the OS keychain (the same service as the client's credentials) and falls back
// to a 0600 file at path when the keychain is unavailable, or only uses the file when HARNESS_KEYRING=0.
func DefaultKeys(path string) KeySource {
	if os.Getenv("HARNESS_KEYRING") == "0" {
		return FileKey{Path: path}
	}
	return KeyringKey{Fallback: FileKey{Path: path}}
}

// FileKey keeps the key base64-encoded in a 0600 file.
type FileKey struct{ Path string }

func (f FileKey) Key(create bool) ([]byte, error) {
	data, err := os.ReadFile(f.Path)
	if err == nil {
		return decodeKey(strings.TrimSpace(string(data)))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if !create {
		return nil, ErrNoKey
	}
	k, err := newKey()
	if err != nil {
		return nil, err
	}
	if err := writePrivate(f.Path, []byte(base64.StdEncoding.EncodeToString(k)+"\n")); err != nil {
		return nil, err
	}
	return k, nil
}

// KeyringKey keeps the key in the OS keychain; a key already in the fallback file is used as is (it was
// created while the keychain was unavailable), and a new key goes to the file when the keychain refuses it.
type KeyringKey struct{ Fallback FileKey }

func (k KeyringKey) Key(create bool) ([]byte, error) {
	if s, err := keyring.Get(keyringService, keyringUser); err == nil {
		return decodeKey(s)
	}
	if key, err := k.Fallback.Key(false); !errors.Is(err, ErrNoKey) {
		return key, err
	}
	if !create {
		return nil, ErrNoKey
	}
	key, err := newKey()
	if err != nil {
		return nil, err
	}
	if err := keyring.Set(keyringService, keyringUser, base64.StdEncoding.EncodeToString(key)); err == nil {
		return key, nil
	}
	return k.Fallback.Key(true)
}

func newKey() ([]byte, error) {
	k := make([]byte, 32)
	_, err := rand.Read(k)
	return k, err
}

func decodeKey(s string) ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(k) != 32 {
		return nil, errors.New("the stored encryption key is malformed")
	}
	return k, nil
}
