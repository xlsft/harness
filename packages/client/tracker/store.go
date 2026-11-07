package tracker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

// Connection is a personal connection on this device. It lives in HARNESS_HOME and is never synced; the secret is in the keychain.
type Connection struct {
	ID               string     `json:"id"`
	Provider         string     `json:"provider"`
	Name             string     `json:"name"`
	BaseURL          string     `json:"base_url"`
	Auth             AuthMethod `json:"auth"`
	Login            string     `json:"login,omitempty"`
	OAuthClientID    string     `json:"oauth_client_id,omitempty"`
	Enabled          bool       `json:"enabled"`
	KnowledgeEnabled bool       `json:"knowledge_enabled,omitempty"`
	KnowledgeQuery   string     `json:"knowledge_query,omitempty"`
	Healthy          bool       `json:"healthy,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	LastCheckedAt    string     `json:"last_checked_at,omitempty"`
}

// Secrets stores credentials on this machine.
type Secrets interface {
	// Get returns "" when nothing is stored.
	Get(id string) (string, error)
	Set(id, value string) error
	Delete(id string) error
}

const keyringService = "harness-client"

func keyringUser(id string) string { return "tracker-connection:" + id }

// KeyringSecrets uses the system keychain, and a 0600 file only where there is none (headless Linux, HARNESS_KEYRING=0).
type KeyringSecrets struct {
	FallbackPath string
	NoKeyring    bool
	mu           sync.Mutex
}

// Get implements Secrets.
func (k *KeyringSecrets) Get(id string) (string, error) {
	if !k.NoKeyring {
		if s, err := keyring.Get(keyringService, keyringUser(id)); err == nil {
			return s, nil
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.read()
	if err != nil {
		return "", err
	}
	return m[id], nil
}

// Set implements Secrets.
func (k *KeyringSecrets) Set(id, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.read()
	if err != nil {
		return err
	}
	if !k.NoKeyring {
		if err := keyring.Set(keyringService, keyringUser(id), value); err == nil {
			if _, stale := m[id]; stale {
				delete(m, id)
				return k.write(m)
			}
			return nil
		}
	}
	m[id] = value
	return k.write(m)
}

// Delete implements Secrets.
func (k *KeyringSecrets) Delete(id string) error {
	if !k.NoKeyring {
		_ = keyring.Delete(keyringService, keyringUser(id))
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.read()
	if err != nil {
		return err
	}
	if _, ok := m[id]; !ok {
		return nil
	}
	delete(m, id)
	return k.write(m)
}

func (k *KeyringSecrets) read() (map[string]string, error) {
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

func (k *KeyringSecrets) write(m map[string]string) error {
	if k.FallbackPath == "" {
		return errors.New("no keychain and no file to keep credentials in")
	}
	return writeJSON(k.FallbackPath, m)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Store keeps this device's connections.
type Store struct {
	Path    string
	Secrets Secrets

	mu sync.Mutex
}

// NewStore is the device's store under home.
func NewStore(home string) *Store {
	return &Store{
		Path:    filepath.Join(home, "tracker-connections.json"),
		Secrets: &KeyringSecrets{FallbackPath: filepath.Join(home, "tracker-connection-keys.json"), NoKeyring: os.Getenv("HARNESS_KEYRING") == "0"},
	}
}

type storeFile struct {
	Connections []Connection `json:"connections"`
}

func (s *Store) load() ([]Connection, error) {
	data, err := os.ReadFile(s.Path)
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
	return f.Connections, nil
}

// List returns the connections, by name.
func (s *Store) List() ([]Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

// Get returns one connection.
func (s *Store) Get(id string) (Connection, error) {
	list, err := s.List()
	if err != nil {
		return Connection{}, err
	}
	for _, c := range list {
		if c.ID == id {
			return c, nil
		}
	}
	return Connection{}, fmt.Errorf("%w: connection %s", ErrNotFound, id)
}

// Input is a connection as a form sends it. Nil secrets keep what is stored.
type Input struct {
	Connection
	Secret            *string
	OAuthClientSecret *string
}

// NormalizeURL trims an instance URL and adds https:// when only a host was given.
func NormalizeURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	return strings.TrimSuffix(raw, "/api")
}

func validate(c *Connection) error {
	if _, err := factoryOf(c.Provider); err != nil {
		return err
	}
	c.BaseURL = NormalizeURL(c.BaseURL)
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("the address must be an http(s) URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("the address must not carry credentials, a query or a fragment")
	}
	switch c.Auth {
	case AuthToken:
	case AuthPassword:
		if strings.TrimSpace(c.Login) == "" {
			return errors.New("the login is required for login and password")
		}
	case AuthOAuth:
		if strings.TrimSpace(c.OAuthClientID) == "" {
			return errors.New("the OAuth client id is required: an admin of the instance creates the client")
		}
	default:
		return errors.New("choose how this connection signs in")
	}
	if strings.TrimSpace(c.Name) == "" {
		c.Name = u.Host
	}
	return nil
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "tc_" + hex.EncodeToString(b)
}

// Save adds a connection, or changes the one with this id.
func (s *Store) Save(in Input) (Connection, error) {
	c := in.Connection
	if err := validate(&c); err != nil {
		return Connection{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load()
	if err != nil {
		return Connection{}, err
	}
	at := -1
	if c.ID != "" {
		for i := range list {
			if list[i].ID == c.ID {
				at = i
			}
		}
		if at < 0 {
			return Connection{}, fmt.Errorf("%w: connection %s", ErrNotFound, c.ID)
		}
	} else {
		c.ID = newID()
	}
	creds, err := s.credentials(c.ID)
	if err != nil {
		return Connection{}, err
	}
	if at >= 0 {
		old := list[at]
		c.Healthy, c.LastError, c.LastCheckedAt = old.Healthy, old.LastError, old.LastCheckedAt
		// A changed instance or client: what was authorized there means nothing here.
		if old.BaseURL != c.BaseURL || old.OAuthClientID != c.OAuthClientID {
			creds.AccessToken, creds.RefreshToken, creds.ExpiresAt = "", "", 0
		}
	}
	if in.Secret != nil {
		creds.Secret = *in.Secret
	}
	if in.OAuthClientSecret != nil {
		creds.ClientSecret = *in.OAuthClientSecret
	}
	if err := s.setCredentials(c.ID, creds); err != nil {
		return Connection{}, err
	}
	if at >= 0 {
		list[at] = c
	} else {
		list = append(list, c)
	}
	if err := writeJSON(s.Path, storeFile{list}); err != nil {
		return Connection{}, err
	}
	return c, nil
}

// Delete removes a connection and its credentials.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load()
	if err != nil {
		return err
	}
	out := list[:0]
	for _, c := range list {
		if c.ID != id {
			out = append(out, c)
		}
	}
	if err := writeJSON(s.Path, storeFile{out}); err != nil {
		return err
	}
	return s.Secrets.Delete(id)
}

// Record stores the outcome of a test.
func (s *Store) Record(id string, err error, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, lerr := s.load()
	if lerr != nil {
		return
	}
	for i := range list {
		if list[i].ID == id {
			list[i].Healthy, list[i].LastError, list[i].LastCheckedAt = err == nil, "", now.UTC().Format(time.RFC3339)
			if err != nil {
				list[i].LastError = err.Error()
			}
		}
	}
	_ = writeJSON(s.Path, storeFile{list})
}

func (s *Store) credentials(id string) (Credentials, error) {
	raw, err := s.Secrets.Get(id)
	if err != nil || raw == "" {
		return Credentials{}, err
	}
	var c Credentials
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Credentials{}, err
	}
	return c, nil
}

func (s *Store) setCredentials(id string, c Credentials) error {
	if c == (Credentials{}) {
		return s.Secrets.Delete(id)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.Secrets.Set(id, string(raw))
}

// HasSecret says whether the connection has what it signs in with.
func (s *Store) HasSecret(c Connection) bool {
	creds, err := s.credentials(c.ID)
	if err != nil {
		return false
	}
	if c.Auth == AuthOAuth {
		return creds.AccessToken != "" || creds.RefreshToken != ""
	}
	return creds.Secret != ""
}

// SetTokens stores what OAuth gave, keeping the client's secret.
func (s *Store) SetTokens(id string, t Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.credentials(id)
	if err != nil {
		return err
	}
	cur.AccessToken, cur.RefreshToken, cur.ExpiresAt = t.AccessToken, t.RefreshToken, t.ExpiresAt
	return s.setCredentials(id, cur)
}

// Open makes a provider for a stored connection.
func (s *Store) Open(c Connection) (Provider, error) {
	f, err := factoryOf(c.Provider)
	if err != nil {
		return nil, err
	}
	creds, err := s.credentials(c.ID)
	if err != nil {
		return nil, err
	}
	return f.Open(OpenOptions{
		BaseURL: c.BaseURL, Auth: c.Auth, Login: c.Login, OAuthClientID: c.OAuthClientID, Credentials: creds,
		OnTokens: func(t Credentials) { _ = s.SetTokens(c.ID, t) },
	})
}

// OpenByID makes a provider for a connection that is enabled.
func (s *Store) OpenByID(id string) (Connection, Provider, error) {
	c, err := s.Get(id)
	if err != nil {
		return c, nil, err
	}
	if !c.Enabled {
		return c, nil, fmt.Errorf("%s is disabled", c.Name)
	}
	p, err := s.Open(c)
	return c, p, err
}

// TestDraft signs in with a connection not saved yet.
func TestDraft(ctx context.Context, in Input) (Identity, error) {
	c := in.Connection
	if err := validate(&c); err != nil {
		return Identity{}, err
	}
	if c.Auth == AuthOAuth {
		return Identity{}, errors.New("save the connection and authorize it first")
	}
	f, err := factoryOf(c.Provider)
	if err != nil {
		return Identity{}, err
	}
	creds := Credentials{}
	if in.Secret != nil {
		creds.Secret = *in.Secret
	}
	p, err := f.Open(OpenOptions{BaseURL: c.BaseURL, Auth: c.Auth, Login: c.Login, Credentials: creds})
	if err != nil {
		return Identity{}, err
	}
	return p.Test(ctx)
}

// FreeTextTerms gives the plain words of a query in a provider's syntax: filters such as `project: HELP` and #shortcuts are not.
func FreeTextTerms(query string) []string {
	var terms []string
	rest := query
	for {
		start := strings.IndexByte(rest, '"')
		if start < 0 {
			break
		}
		end := strings.IndexByte(rest[start+1:], '"')
		if end < 0 {
			break
		}
		if phrase := strings.TrimSpace(rest[start+1 : start+1+end]); phrase != "" {
			terms = append(terms, strings.ToLower(phrase))
		}
		rest = rest[:start] + " " + rest[start+end+2:]
	}
	words := strings.Fields(rest)
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case strings.HasPrefix(w, "{"):
			for i < len(words) && !strings.HasSuffix(words[i], "}") {
				i++
			}
		case strings.HasSuffix(w, ":"):
			i++
			if i < len(words) && strings.HasPrefix(words[i], "{") {
				for i < len(words) && !strings.HasSuffix(words[i], "}") {
					i++
				}
			}
		case strings.Contains(w, ":"), strings.HasPrefix(w, "#"), strings.HasPrefix(w, "-"):
		default:
			terms = append(terms, strings.ToLower(w))
		}
	}
	return terms
}

// Snippet is up to three lines holding a term, or the start of the text when none does.
func Snippet(content string, terms []string) string {
	lines := strings.Split(content, "\n")
	var hit []string
	for _, l := range lines {
		low := strings.ToLower(l)
		for _, t := range terms {
			if strings.Contains(low, t) {
				hit = append(hit, l)
				break
			}
		}
		if len(hit) == 3 {
			break
		}
	}
	if len(hit) == 0 {
		hit = lines[:min(3, len(lines))]
	}
	text := strings.TrimSpace(strings.Join(hit, "\n"))
	if r := []rune(text); len(r) > 1200 {
		text = string(r[:1200]) + "…"
	}
	return text
}
