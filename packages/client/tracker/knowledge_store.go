package tracker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// KnowledgeConnection is a knowledge source connection on this device. It lives in HARNESS_HOME/knowledge-connections.json and is
// never synced; its own secret is in the keychain. With Borrow set it has none of its own: it signs in as that tracker connection
// of this device, resolved at every open.
type KnowledgeConnection struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Name     string `json:"name"`
	// BaseURL is its own address; empty when borrowing and the tracker connection's is used.
	BaseURL       string            `json:"base_url,omitempty"`
	Auth          AuthMethod        `json:"auth,omitempty"`
	Login         string            `json:"login,omitempty"`
	OAuthClientID string            `json:"oauth_client_id,omitempty"`
	Enabled       bool              `json:"enabled"`
	Query         string            `json:"query,omitempty"`
	Options       map[string]string `json:"options,omitempty"`
	// Borrow is the id of a tracker connection of this device whose sign-in it uses.
	Borrow string `json:"borrow,omitempty"`
	// MigratedFrom is the tracker connection it was made from, so document ids of the form conn:<that id>:<article> still read.
	MigratedFrom  string `json:"migrated_from,omitempty"`
	Healthy       bool   `json:"healthy,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	LastCheckedAt string `json:"last_checked_at,omitempty"`
}

// KnowledgeInput is a knowledge connection as a form sends it. Nil secrets keep what is stored.
type KnowledgeInput struct {
	KnowledgeConnection
	Secret            *string
	OAuthClientSecret *string
}

// KnowledgeStore keeps this device's knowledge source connections. Trackers is the store borrowed sign-ins come from.
type KnowledgeStore struct {
	Path     string
	Secrets  Secrets
	Trackers *Store

	mu sync.Mutex
}

// NewKnowledgeStore is the device's knowledge store under home, borrowing from trackers.
func NewKnowledgeStore(home string, trackers *Store) *KnowledgeStore {
	return &KnowledgeStore{
		Path:     filepath.Join(home, "knowledge-connections.json"),
		Secrets:  &KeyringSecrets{FallbackPath: filepath.Join(home, "knowledge-connection-keys.json"), NoKeyring: os.Getenv("HARNESS_KEYRING") == "0", Prefix: "knowledge-connection:"},
		Trackers: trackers,
	}
}

type knowledgeFile struct {
	Connections []KnowledgeConnection `json:"connections"`
}

func (s *KnowledgeStore) load() ([]KnowledgeConnection, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f knowledgeFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return f.Connections, nil
}

// List returns the connections, by name.
func (s *KnowledgeStore) List() ([]KnowledgeConnection, error) {
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
func (s *KnowledgeStore) Get(id string) (KnowledgeConnection, error) {
	list, err := s.List()
	if err != nil {
		return KnowledgeConnection{}, err
	}
	for _, c := range list {
		if c.ID == id {
			return c, nil
		}
	}
	return KnowledgeConnection{}, fmt.Errorf("%w: knowledge source %s", ErrNotFound, id)
}

// borrowed is the tracker connection a knowledge connection borrows, checked against what its provider may borrow from.
func (s *KnowledgeStore) borrowed(caps KnowledgeCapabilities, id string) (Connection, error) {
	if s.Trackers == nil {
		return Connection{}, errors.New("there are no tracker connections to borrow a sign-in from")
	}
	t, err := s.Trackers.Get(id)
	if err != nil {
		return Connection{}, fmt.Errorf("the tracker connection whose sign-in this uses is gone: %w", err)
	}
	if !caps.CanBorrow(t.Provider) {
		return Connection{}, fmt.Errorf("%s can't use the sign-in of a %s connection", caps.DisplayName, t.Provider)
	}
	return t, nil
}

func validateKnowledge(c *KnowledgeConnection, trackers func(KnowledgeCapabilities, string) (Connection, error)) error {
	f, err := knowledgeFactoryOf(c.Provider)
	if err != nil {
		return err
	}
	caps := f.Capabilities
	c.Query = strings.TrimSpace(c.Query)
	host := ""
	if c.Borrow != "" {
		t, err := trackers(caps, c.Borrow)
		if err != nil {
			return err
		}
		c.Auth, c.Login, c.OAuthClientID = "", "", ""
		opts, err := CheckOptions(caps.OptionFields, c.Options)
		if err != nil {
			return err
		}
		c.Options = opts
		if strings.TrimSpace(c.BaseURL) != "" {
			base, u, err := checkAddress(c.BaseURL, "")
			if err != nil {
				return err
			}
			c.BaseURL, host = base, u.Host
		}
		if f.Validate != nil {
			base := c.BaseURL
			if base == "" {
				base = t.BaseURL
			}
			if err := f.Validate(base, mergeOptions(t.Options, c.Options)); err != nil {
				return err
			}
		}
		if strings.TrimSpace(c.Name) == "" {
			c.Name = caps.DisplayName + " · " + t.Name
		}
	} else {
		spec := signInSpec{authMethods: caps.AuthMethods, optionFields: caps.OptionFields, defaultBaseURL: caps.DefaultBaseURL, validate: f.Validate}
		u, err := checkSignIn(spec, &c.BaseURL, c.Auth, c.Login, c.OAuthClientID, &c.Options)
		if err != nil {
			return err
		}
		host = u.Host
	}
	if len(c.Options) == 0 {
		c.Options = nil
	}
	if strings.TrimSpace(c.Name) == "" {
		c.Name = host
	}
	if len(c.Name) > 200 {
		return errors.New("names have at most 200 characters")
	}
	return nil
}

func mergeOptions(base, own map[string]string) map[string]string {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, own)
	return out
}

func newKnowledgeID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "kc_" + hex.EncodeToString(b)
}

// Save adds a connection, or changes the one with this id.
func (s *KnowledgeStore) Save(in KnowledgeInput) (KnowledgeConnection, error) {
	c := in.KnowledgeConnection
	if err := validateKnowledge(&c, s.borrowed); err != nil {
		return KnowledgeConnection{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load()
	if err != nil {
		return KnowledgeConnection{}, err
	}
	at := -1
	if c.ID != "" {
		for i := range list {
			if list[i].ID == c.ID {
				at = i
			}
		}
		if at < 0 {
			return KnowledgeConnection{}, fmt.Errorf("%w: knowledge source %s", ErrNotFound, c.ID)
		}
	} else {
		c.ID = newKnowledgeID()
	}
	creds, err := readCredentials(s.Secrets, c.ID)
	if err != nil {
		return KnowledgeConnection{}, err
	}
	if at >= 0 {
		old := list[at]
		c.Healthy, c.LastError, c.LastCheckedAt, c.MigratedFrom = old.Healthy, old.LastError, old.LastCheckedAt, old.MigratedFrom
		if old.BaseURL != c.BaseURL || old.OAuthClientID != c.OAuthClientID {
			creds.AccessToken, creds.RefreshToken, creds.ExpiresAt = "", "", 0
		}
	}
	if c.Borrow != "" {
		// Borrowing: nothing of its own to keep.
		creds = Credentials{}
	} else {
		if in.Secret != nil {
			creds.Secret = *in.Secret
		}
		if in.OAuthClientSecret != nil {
			creds.ClientSecret = *in.OAuthClientSecret
		}
	}
	if err := writeCredentials(s.Secrets, c.ID, creds); err != nil {
		return KnowledgeConnection{}, err
	}
	if at >= 0 {
		list[at] = c
	} else {
		list = append(list, c)
	}
	if err := writeJSON(s.Path, knowledgeFile{list}); err != nil {
		return KnowledgeConnection{}, err
	}
	return c, nil
}

// Delete removes a connection and its own credentials. A borrowed tracker connection is not touched.
func (s *KnowledgeStore) Delete(id string) error {
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
	if err := writeJSON(s.Path, knowledgeFile{out}); err != nil {
		return err
	}
	return s.Secrets.Delete(id)
}

// Record stores the outcome of a test.
func (s *KnowledgeStore) Record(id string, err error, now time.Time) {
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
	_ = writeJSON(s.Path, knowledgeFile{list})
}

// HasSecret says whether the connection has what it signs in with: its own, or the borrowed connection's.
func (s *KnowledgeStore) HasSecret(c KnowledgeConnection) bool {
	if c.Borrow != "" {
		t, err := s.Trackers.Get(c.Borrow)
		return err == nil && s.Trackers.HasSecret(t)
	}
	creds, err := readCredentials(s.Secrets, c.ID)
	if err != nil {
		return false
	}
	switch c.Auth {
	case AuthOAuth:
		return creds.AccessToken != "" || creds.RefreshToken != ""
	case AuthVCS:
		return true
	}
	return creds.Secret != ""
}

// SetTokens stores what OAuth gave a connection with its own sign-in, keeping the client's secret.
func (s *KnowledgeStore) SetTokens(id string, t Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := readCredentials(s.Secrets, id)
	if err != nil {
		return err
	}
	cur.AccessToken, cur.RefreshToken, cur.ExpiresAt = t.AccessToken, t.RefreshToken, t.ExpiresAt
	return writeCredentials(s.Secrets, id, cur)
}

// Open makes a provider for a stored connection, resolving a borrowed sign-in now.
func (s *KnowledgeStore) Open(c KnowledgeConnection) (KnowledgeProvider, error) {
	f, err := knowledgeFactoryOf(c.Provider)
	if err != nil {
		return nil, err
	}
	if c.Borrow != "" {
		t, err := s.borrowed(f.Capabilities, c.Borrow)
		if err != nil {
			return nil, err
		}
		if !t.Enabled {
			return nil, fmt.Errorf("%s, whose sign-in this uses, is disabled", t.Name)
		}
		creds, err := s.Trackers.credentials(t.ID)
		if err != nil {
			return nil, err
		}
		base := c.BaseURL
		if base == "" {
			base = t.BaseURL
		}
		return f.Open(KnowledgeOpenOptions{
			BaseURL: base, Auth: t.Auth, Login: t.Login, OAuthClientID: t.OAuthClientID, Credentials: creds, Options: mergeOptions(t.Options, c.Options),
			OnTokens: func(x Credentials) { _ = s.Trackers.SetTokens(t.ID, x) }, VCSToken: DeviceVCSToken, BorrowedFrom: t.Provider, TrackerBaseURL: t.BaseURL,
		})
	}
	creds, err := readCredentials(s.Secrets, c.ID)
	if err != nil {
		return nil, err
	}
	return f.Open(KnowledgeOpenOptions{
		BaseURL: c.BaseURL, Auth: c.Auth, Login: c.Login, OAuthClientID: c.OAuthClientID, Credentials: creds, Options: mergeOptions(nil, c.Options),
		OnTokens: func(x Credentials) { _ = s.SetTokens(c.ID, x) }, VCSToken: DeviceVCSToken,
	})
}

// TestDraft signs in with a connection not saved yet.
func (s *KnowledgeStore) TestDraft(ctx context.Context, in KnowledgeInput) (Identity, error) {
	c := in.KnowledgeConnection
	c.ID = ""
	if err := validateKnowledge(&c, s.borrowed); err != nil {
		return Identity{}, err
	}
	if c.Borrow == "" {
		if c.Auth == AuthOAuth {
			return Identity{}, errors.New("save the connection and authorize it first")
		}
		if in.Secret != nil {
			// Only the draft's secret: Open would read a stored one by id, and there is none.
			f, err := knowledgeFactoryOf(c.Provider)
			if err != nil {
				return Identity{}, err
			}
			p, err := f.Open(KnowledgeOpenOptions{BaseURL: c.BaseURL, Auth: c.Auth, Login: c.Login, Credentials: Credentials{Secret: *in.Secret}, Options: c.Options, VCSToken: DeviceVCSToken})
			if err != nil {
				return Identity{}, err
			}
			return p.Test(ctx)
		}
	}
	p, err := s.Open(c)
	if err != nil {
		return Identity{}, err
	}
	return p.Test(ctx)
}

// readCredentials and writeCredentials keep a connection's credentials as one JSON value under its id.
func readCredentials(sec Secrets, id string) (Credentials, error) {
	raw, err := sec.Get(id)
	if err != nil || raw == "" {
		return Credentials{}, err
	}
	var c Credentials
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Credentials{}, err
	}
	return c, nil
}

func writeCredentials(sec Secrets, id string, c Credentials) error {
	if c == (Credentials{}) {
		return sec.Delete(id)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return sec.Set(id, string(raw))
}

// KnowledgeOf finds the connection that reads a document id's connection part: a knowledge connection's id, or, for ids made
// before knowledge sources were split from trackers, the tracker connection a connection was migrated from.
func (s *KnowledgeStore) KnowledgeOf(id string) (KnowledgeConnection, bool) {
	list, err := s.List()
	if err != nil {
		return KnowledgeConnection{}, false
	}
	for _, c := range list {
		if c.ID == id {
			return c, true
		}
	}
	for _, c := range list {
		if c.MigratedFrom != "" && c.MigratedFrom == id {
			return c, true
		}
	}
	return KnowledgeConnection{}, false
}

// MigrateKnowledge turns each tracker connection that still serves Knowledge (knowledge_enabled, from before knowledge sources had
// connections of their own) into a knowledge source connection borrowing it, keeping its query, and clears the flag. It runs at
// startup and does nothing once done. The provider is the first knowledge provider that borrows from the tracker's.
func (s *KnowledgeStore) MigrateKnowledge() (int, error) {
	if s.Trackers == nil {
		return 0, nil
	}
	list, err := s.Trackers.List()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range list {
		if !t.KnowledgeEnabled {
			continue
		}
		provider := ""
		for _, f := range KnowledgeFactories() {
			if f.Capabilities.CanBorrow(t.Provider) {
				provider = f.Capabilities.Provider
				break
			}
		}
		if provider == "" {
			continue
		}
		f, _ := knowledgeFactoryOf(provider)
		if _, err := s.Save(KnowledgeInput{KnowledgeConnection: KnowledgeConnection{Provider: provider, Name: f.Capabilities.DisplayName + " · " + t.Name, Enabled: t.Enabled, Query: t.KnowledgeQuery, Borrow: t.ID, MigratedFrom: t.ID}}); err != nil {
			return n, err
		}
		if err := s.Trackers.clearKnowledge(t.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// clearKnowledge drops the legacy Knowledge flags of a tracker connection once it is migrated.
func (s *Store) clearKnowledge(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load()
	if err != nil {
		return err
	}
	for i := range list {
		if list[i].ID == id {
			list[i].KnowledgeEnabled, list[i].KnowledgeQuery = false, ""
		}
	}
	return writeJSON(s.Path, storeFile{list})
}
