// Package browser runs a headless Chromium per session, shared by the user (the UI's Browser pane, over a
// WebSocket with a JPEG screencast and input forwarding) and the agent (the Browser* tools). See
// proto/harness/agent/v1/browser.proto for the protocol.
package browser

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// Errors.
var (
	ErrNoBrowser     = errors.New("no Chromium-based browser was found; install Chrome or Chromium, set HARNESS_BROWSER, or download Chrome for Testing (InstallBrowser)")
	ErrClosed        = errors.New("the browser manager is closed")
	ErrTicketInvalid = errors.New("invalid browser ticket")
	ErrTicketExpired = errors.New("the browser ticket expired")
	ErrNotFound      = errors.New("no browser for this session")
)

// Defaults.
const (
	DefaultIdleTimeout = 15 * time.Minute
	DefaultTicketTTL   = 30 * time.Second
)

// Manager owns the browsers of a client's sessions.
type Manager struct {
	// Dir is HARNESS_HOME/browser: the downloaded build and the per-session profiles.
	Dir string
	// IdleTimeout stops a browser without viewers and tool calls for that long (0: DefaultIdleTimeout; <0: never).
	IdleTimeout time.Duration
	// OnAction is told about the agent's browser actions (the server publishes them as SessionEvents).
	OnAction func(sessionID string, a *agentv1.BrowserAction)
	// HTTP downloads Chrome for Testing (nil: http.DefaultClient).
	HTTP   *http.Client
	Logger *slog.Logger
	// Executable overrides discovery (tests).
	Executable *Executable
	// OnSiteRequest asks the user to allow a site in their real Chrome (attach.go). Nil: the answer is no.
	OnSiteRequest func(sessionID string, r SiteRequest)
	// ChromeDirs overrides the profile folders searched for a Chrome to attach to (tests).
	ChromeDirs []string

	mu       sync.Mutex
	browsers map[string]*Browser
	tickets  map[[32]byte]ticket
	inst     installer
	closed   bool
	grants   *SiteGrants
}

type ticket struct {
	session string
	expires time.Time
}

func (m *Manager) log() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

func (m *Manager) idle() time.Duration {
	if m.IdleTimeout == 0 {
		return DefaultIdleTimeout
	}
	return m.IdleTimeout
}

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Session returns the browser of a session, creating it (not started) on first use.
func (m *Manager) Session(sessionID string) (*Browser, error) {
	if !sessionIDPattern.MatchString(sessionID) {
		return nil, fmt.Errorf("invalid session id %q", sessionID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if m.browsers == nil {
		m.browsers = map[string]*Browser{}
	}
	b := m.browsers[sessionID]
	if b == nil {
		b = newBrowser(m, sessionID)
		m.browsers[sessionID] = b
	}
	return b, nil
}

// Lookup returns the browser of a session, or nil.
func (m *Manager) Lookup(sessionID string) *Browser {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.browsers[sessionID]
}

// OpenPage opens a page in the session's browser, starting it if needed, for tools that are not Browser* tools but
// lead there (preview). Its permission check is the caller's. The user's take-over holds as for any agent action.
func (m *Manager) OpenPage(ctx context.Context, sessionID, u string) error {
	b := m.Lookup(sessionID)
	if b == nil {
		return ErrNotFound
	}
	if err := b.checkControl(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := b.Ensure(ctx); err != nil {
		return err
	}
	return b.showPage(ctx, u)
}

// CloseSession stops the session's browser and forgets it (the profile stays on disk).
func (m *Manager) CloseSession(sessionID string) {
	m.mu.Lock()
	b := m.browsers[sessionID]
	delete(m.browsers, sessionID)
	m.mu.Unlock()
	if b != nil {
		b.shutdown("session_closed")
	}
}

// DeleteProfile stops the session's browser and removes its profile.
func (m *Manager) DeleteProfile(sessionID string) error {
	m.CloseSession(sessionID)
	if !sessionIDPattern.MatchString(sessionID) {
		return nil
	}
	return os.RemoveAll(m.profileDir(sessionID))
}

// Close stops every browser.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	all := m.browsers
	m.browsers = nil
	m.mu.Unlock()
	for _, b := range all {
		b.shutdown("host_shutdown")
	}
}

func (m *Manager) profileDir(sessionID string) string {
	return filepath.Join(m.Dir, "profiles", sessionID)
}

// executable finds the browser to run.
func (m *Manager) executable() (Executable, error) {
	if m.Executable != nil {
		return *m.Executable, nil
	}
	if e, ok := Discover(m.Dir); ok {
		return e, nil
	}
	return Executable{}, ErrNoBrowser
}

// Availability says which browser would run.
func (m *Manager) Availability() *agentv1.BrowserAvailability {
	a := &agentv1.BrowserAvailability{Downloadable: Downloadable(), DownloadVersion: ChromeForTestingVersion, Downloading: m.inst.downloading()}
	_, a.Downloaded = downloadedExecutable(m.Dir)
	if e, err := m.executable(); err == nil {
		a.Available, a.Executable, a.Product = true, e.Path, e.Product
		switch e.Source {
		case SourceEnv:
			a.Source = agentv1.BrowserAvailability_SOURCE_ENV
		case SourceSystem:
			a.Source = agentv1.BrowserAvailability_SOURCE_SYSTEM
		case SourceDownloaded:
			a.Source = agentv1.BrowserAvailability_SOURCE_DOWNLOADED
		}
	}
	return a
}

// Install downloads the pinned Chrome for Testing build into Dir (joining a running download). The download
// runs under ctx, which should outlive the request that started it.
func (m *Manager) Install(ctx context.Context) (*Download, error) {
	m.inst.client = m.HTTP
	return m.inst.start(ctx, m.Dir)
}

// IssueTicket returns a one-time ticket for the session's browser socket.
func (m *Manager) IssueTicket(sessionID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.browsers[sessionID] == nil {
		return "", ErrNotFound
	}
	now := time.Now()
	for k, t := range m.tickets {
		if now.After(t.expires) {
			delete(m.tickets, k)
		}
	}
	if m.tickets == nil {
		m.tickets = map[[32]byte]ticket{}
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	m.tickets[sha256.Sum256([]byte(tok))] = ticket{session: sessionID, expires: now.Add(DefaultTicketTTL)}
	return tok, nil
}

// redeem consumes a ticket.
func (m *Manager) redeem(tok string) (*Browser, error) {
	key := sha256.Sum256([]byte(tok))
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[key]
	if !ok || tok == "" {
		return nil, ErrTicketInvalid
	}
	delete(m.tickets, key)
	if time.Now().After(t.expires) {
		return nil, ErrTicketExpired
	}
	b := m.browsers[t.session]
	if b == nil {
		return nil, ErrNotFound
	}
	return b, nil
}

func (m *Manager) reportAction(sessionID string, a *agentv1.BrowserAction) {
	if m.OnAction != nil {
		m.OnAction(sessionID, a)
	}
}

// removeProfileData deletes a stopped browser's profile (cookies, cache, storage); it is created again on the
// next start.
func removeProfileData(dir string) error { return os.RemoveAll(dir) }
