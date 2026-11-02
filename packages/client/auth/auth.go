// Package auth implements device-flow login, credential storage and access
// token management (docs/protocol/auth.md).
//
// The server API is behind the Service interface; HTTPService is the current
// HTTPS/JSON adapter. Manager implements TokenSource on top of any Service.
package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// User is the authenticated user.
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

// Device describes the current device.
type Device struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at,omitempty"`
	LastSeenAt string `json:"last_seen_at,omitempty"`
}

// DeviceCode is the response of the device code request.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// DeviceGrant is the successful token response of the device flow.
type DeviceGrant struct {
	DeviceID        string `json:"device_id"`
	DeviceToken     string `json:"device_token"`
	AccessToken     string `json:"access_token"`
	AccessExpiresIn int    `json:"access_expires_in"`
	User            User   `json:"user"`
}

// AccessGrant is the response of the refresh request.
type AccessGrant struct {
	AccessToken     string `json:"access_token"`
	AccessExpiresIn int    `json:"access_expires_in"`
}

// Me is the response of GET /api/auth/me.
type Me struct {
	User   User   `json:"user"`
	Device Device `json:"device"`
}

// DeviceInfo identifies the client when requesting a device code.
type DeviceInfo struct {
	ClientName string `json:"client_name"`
	DeviceName string `json:"device_name"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
}

// Device-flow polling errors (auth.md).
var (
	ErrAuthorizationPending = errors.New("authorization_pending")
	ErrSlowDown             = errors.New("slow_down")
	ErrExpiredToken         = errors.New("device code expired")
	ErrAccessDenied         = errors.New("access denied")
	// ErrDeviceRevoked means the device token is no longer valid; the user must log in again.
	ErrDeviceRevoked = errors.New("device revoked or logged out; run `harness login <url>`")
	// ErrNotLoggedIn means no credentials are stored.
	ErrNotLoggedIn = errors.New("not logged in; run `harness login <url>`")
)

// Service is the server-side auth API. HTTPService implements it over
// HTTPS/JSON; a gRPC/Connect adapter can implement it later.
type Service interface {
	RequestDeviceCode(ctx context.Context, info DeviceInfo) (*DeviceCode, error)
	// PollDeviceToken returns ErrAuthorizationPending / ErrSlowDown /
	// ErrExpiredToken / ErrAccessDenied while the flow is not complete.
	PollDeviceToken(ctx context.Context, deviceCode string) (*DeviceGrant, error)
	// Refresh exchanges a device token for an access token; ErrDeviceRevoked on 401.
	Refresh(ctx context.Context, deviceToken string) (*AccessGrant, error)
	Me(ctx context.Context, accessToken string) (*Me, error)
	Logout(ctx context.Context, accessToken string) error
}

// TokenSource supplies bearer tokens to API adapters.
type TokenSource interface {
	// Token returns a valid access token, refreshing proactively if needed.
	Token(ctx context.Context) (string, error)
	// Refresh forces a refresh (called once after a 401).
	Refresh(ctx context.Context) (string, error)
}

// StaticToken is a TokenSource with a fixed token (e.g. HARNESS_ACCESS_TOKEN
// for session-scoped tokens handed to SSH runners). It cannot refresh.
type StaticToken string

func (s StaticToken) Token(context.Context) (string, error) { return string(s), nil }
func (s StaticToken) Refresh(context.Context) (string, error) {
	return "", errors.New("static access token rejected by server")
}

// RefreshSkew is how long before expiry the access token is refreshed.
const RefreshSkew = 5 * time.Minute

// Manager keeps the access token in memory and refreshes it from the stored
// device token. It is safe for concurrent use.
type Manager struct {
	Service Service
	Store   Store
	Now     func() time.Time

	mu      sync.Mutex
	creds   *Credentials
	access  string
	expires time.Time
}

// NewManager loads credentials from store.
func NewManager(svc Service, store Store) (*Manager, error) {
	c, err := store.Load()
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, ErrNotLoggedIn
	}
	return &Manager{Service: svc, Store: store, creds: c}, nil
}

// Credentials returns the stored credentials.
func (m *Manager) Credentials() *Credentials { return m.creds }

// SetAccess seeds the access token (e.g. right after login).
func (m *Manager) SetAccess(token string, expiresIn int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.access = token
	m.expires = m.now().Add(time.Duration(expiresIn) * time.Second)
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Token implements TokenSource.
func (m *Manager) Token(ctx context.Context) (string, error) {
	m.mu.Lock()
	if m.access != "" && m.now().Add(RefreshSkew).Before(m.expires) {
		t := m.access
		m.mu.Unlock()
		return t, nil
	}
	m.mu.Unlock()
	return m.Refresh(ctx)
}

// Refresh implements TokenSource.
func (m *Manager) Refresh(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, err := m.Service.Refresh(ctx, m.creds.DeviceToken)
	if err != nil {
		if errors.Is(err, ErrDeviceRevoked) {
			_ = m.Store.Delete()
		}
		return "", err
	}
	m.access = g.AccessToken
	m.expires = m.now().Add(time.Duration(g.AccessExpiresIn) * time.Second)
	return m.access, nil
}

// Login runs the device flow against svc, prints instructions to out and
// stores the resulting credentials. It returns a ready Manager.
func Login(ctx context.Context, svc Service, store Store, server string, info DeviceInfo, out io.Writer) (*Manager, *DeviceGrant, error) {
	return LoginWith(ctx, svc, store, server, info, func(uri, userCode string) {
		fmt.Fprintf(out, "To sign in, open %s\nand confirm the code: %s\n", uri, userCode)
	})
}

// LoginWith is Login with the instructions handed to prompt (the URL to open
// and the code to confirm) instead of printed, for frontends that show them.
func LoginWith(ctx context.Context, svc Service, store Store, server string, info DeviceInfo, prompt func(uri, userCode string)) (*Manager, *DeviceGrant, error) {
	code, err := svc.RequestDeviceCode(ctx, info)
	if err != nil {
		return nil, nil, fmt.Errorf("request device code: %w", err)
	}
	uri := code.VerificationURIComplete
	if uri == "" {
		uri = code.VerificationURI
	}
	prompt(uri, code.UserCode)
	interval := time.Duration(code.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)
	if code.ExpiresIn <= 0 {
		deadline = time.Now().Add(10 * time.Minute)
	}
	for {
		if time.Now().After(deadline) {
			return nil, nil, ErrExpiredToken
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(interval):
		}
		g, err := svc.PollDeviceToken(ctx, code.DeviceCode)
		switch {
		case err == nil:
			creds := &Credentials{Server: server, DeviceID: g.DeviceID, DeviceToken: g.DeviceToken, User: g.User}
			if err := store.Save(creds); err != nil {
				return nil, nil, fmt.Errorf("save credentials: %w", err)
			}
			m := &Manager{Service: svc, Store: store, creds: creds}
			m.SetAccess(g.AccessToken, g.AccessExpiresIn)
			return m, g, nil
		case errors.Is(err, ErrAuthorizationPending):
		case errors.Is(err, ErrSlowDown):
			interval += 5 * time.Second
		default:
			return nil, nil, err
		}
	}
}

// Me fetches the current user, refreshing the access token once on an
// authentication error.
func (m *Manager) Me(ctx context.Context) (*Me, error) {
	t, err := m.Token(ctx)
	if err != nil {
		return nil, err
	}
	me, err := m.Service.Me(ctx, t)
	if err != nil && isAuthErr(err) {
		if t, err = m.Refresh(ctx); err != nil {
			return nil, err
		}
		me, err = m.Service.Me(ctx, t)
	}
	return me, err
}

// Logout revokes the device on the server (best effort) and deletes local credentials.
func (m *Manager) Logout(ctx context.Context) error {
	var serr error
	if t, err := m.Token(ctx); err == nil {
		serr = m.Service.Logout(ctx, t)
	}
	if err := m.Store.Delete(); err != nil {
		return err
	}
	return serr
}
