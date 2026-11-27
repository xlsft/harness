// Package tracker is the client's side of external trackers: the provider interface, the connections kept on this device,
// the worker that does a personal board's work from here, the folder links and the merged view of Knowledge.
// docs/design/tracker.md. The server has its own mirror of the provider (packages/core/src/tracker), since admin connections make
// their requests from there.
package tracker

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Errors a provider classifies its failures into, for errors.Is. Whatever else it returns counts as permanent.
var (
	ErrNotFound  = errors.New("not found")
	ErrAuth      = errors.New("authentication failed")
	ErrForbidden = errors.New("forbidden")
	ErrTransient = errors.New("temporary failure")
)

// IsTransient reports a failure worth trying again by itself: network, 429, 5xx.
func IsTransient(err error) bool { return errors.Is(err, ErrTransient) }

// AuthMethod is how a connection signs in.
type AuthMethod string

const (
	AuthToken    AuthMethod = "token"
	AuthPassword AuthMethod = "password"
	AuthOAuth    AuthMethod = "oauth"
	// AuthEmailToken is an account email in Login and an API token as the secret (Atlassian Cloud basic auth).
	AuthEmailToken AuthMethod = "email_token"
	// AuthVCS signs in through the code host: on a device the gh or glab command line signed in for the host of the base URL,
	// asked through OpenOptions.VCSToken. Nothing is stored.
	AuthVCS AuthMethod = "vcs"
)

// OptionField describes a provider-specific setting kept in a connection's Options. Never a secret: options are stored and
// shown in the clear. The form renders these generically (harness.library.v1.ProviderOptionField).
type OptionField struct {
	// Key is lower case letters, digits and underscores.
	Key         string
	Label       string
	Placeholder string
	Required    bool
	// AllowedValues, when set, are the only values taken; the form shows a choice.
	AllowedValues []string
	Help          string
	// Default is used when the form leaves the field empty.
	Default string
}

// AuthHint is what the form says for one sign-in method of one provider, in English.
type AuthHint struct {
	Help        string
	SecretLabel string
	LoginLabel  string
}

// Capabilities says what a provider can do, so forms and tools need no knowledge of it.
type Capabilities struct {
	Provider      string
	DisplayName   string
	AuthMethods   []AuthMethod
	QueryLanguage string
	QueryExample  string
	CustomFields  bool
	Comments      bool
	// OptionFields are the provider-specific settings of a connection.
	OptionFields []OptionField
	// DocsURL is where the vendor documents the credentials the form asks for.
	DocsURL string
	// Logo is the key in packages/ui/app/utils/providerLogos.ts; empty: a monogram.
	Logo               string
	BaseURLPlaceholder string
	// DefaultBaseURL is used when the form leaves the address empty (a hosted-only service). Empty: the address is required.
	DefaultBaseURL string
	AuthHints      map[AuthMethod]AuthHint
}

// Identity is who the credentials sign in as.
type Identity struct {
	Login       string
	DisplayName string
}

// Page bounds a listing.
type Page struct {
	Limit  int
	Offset int
}

// FieldValue is a field's name and value as text.
type FieldValue struct {
	Name  string
	Value string
}

// IssueComment is a comment on an issue.
type IssueComment struct {
	Author    string
	Text      string
	CreatedAt string
}

// Issue is an external issue. Search leaves Description, Fields and Comments empty.
type Issue struct {
	ID          string
	Title       string
	URL         string
	Description string
	Project     string
	Fields      []FieldValue
	UpdatedAt   string
	Comments    []IssueComment
}

// IssueDraft is a new issue.
type IssueDraft struct {
	Project     string
	Title       string
	Description string
	Fields      []FieldValue
}

// IssueUpdate changes an issue; nil leaves a value as it is.
type IssueUpdate struct {
	Title       *string
	Description *string
	Fields      []FieldValue
}

// Field is a field an output can set.
type Field struct {
	Name   string
	Kind   string
	Multi  bool
	Values []string
}

// Project is a provider's project.
type Project struct {
	Key         string
	Name        string
	Description string
	Logo        string
}

// Provider is one connection to an external tracker.
type Provider interface {
	Capabilities() Capabilities
	Test(ctx context.Context) (Identity, error)
	Search(ctx context.Context, query string, page Page) (issues []Issue, hasMore bool, err error)
	// Get reads one issue with its description and recent comments. errors.Is(err, ErrNotFound) when it is not there.
	Get(ctx context.Context, id string) (*Issue, error)
	Create(ctx context.Context, draft IssueDraft) (*Issue, error)
	Update(ctx context.Context, id string, update IssueUpdate) error
	Comment(ctx context.Context, id, text string) error
	Fields(ctx context.Context, project string) ([]Field, error)
	Projects(ctx context.Context) ([]Project, error)
}

// Credentials are what a provider signs in with. Never logged, never sent to the server or to a model.
type Credentials struct {
	Secret       string `json:"secret,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	// ExpiresAt is Unix seconds; 0 when unknown.
	ExpiresAt int64 `json:"expires_at,omitempty"`
}

// OpenOptions is what opening a provider takes.
type OpenOptions struct {
	BaseURL       string
	Auth          AuthMethod
	Login         string
	OAuthClientID string
	Credentials   Credentials
	// Options are the connection's provider-specific settings, validated against Capabilities.OptionFields with defaults filled.
	Options map[string]string
	// OnTokens stores refreshed OAuth tokens.
	OnTokens func(Credentials)
	// VCSToken gives a token of the code host signed in on this device for host (gh or glab), for AuthVCS. Never log it.
	VCSToken func(ctx context.Context, host string) (string, error)
}

// OAuthExchange is what the provider-specific part of Authorization Code offers.
type OAuthExchange interface {
	// AuthorizeURL is where the user's browser goes.
	AuthorizeURL(ctx context.Context, clientID, redirectURI, state, challenge string) (string, error)
	// Exchange turns a code into tokens.
	Exchange(ctx context.Context, clientID, clientSecret, code, redirectURI, verifier string) (Credentials, error)
}

// Factory makes the providers of one kind.
type Factory struct {
	Capabilities Capabilities
	Open         func(OpenOptions) (Provider, error)
	// OAuth starts over a base URL; nil when the provider has no OAuth.
	OAuth func(baseURL string) (OAuthExchange, error)
	// Validate checks what the generic checks can't (a base URL of the right shape, options that go together). Optional.
	Validate func(baseURL string, options map[string]string) error
}

var (
	factoriesMu sync.RWMutex
	factories   = map[string]Factory{}
	order       []string
)

// Register makes a provider available. Providers call it from init.
func Register(name string, f Factory) {
	factoriesMu.Lock()
	defer factoriesMu.Unlock()
	if _, ok := factories[name]; !ok {
		order = append(order, name)
	}
	factories[name] = f
}

// Factories lists the registered providers in registration order.
func Factories() []Factory {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	out := make([]Factory, 0, len(order))
	for _, n := range order {
		out = append(out, factories[n])
	}
	return out
}

func factoryOf(name string) (Factory, error) {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	f, ok := factories[name]
	if !ok {
		return Factory{}, fmt.Errorf("unknown provider %q", name)
	}
	return f, nil
}
