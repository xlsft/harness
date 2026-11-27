package tracker

import (
	"context"
	"fmt"
	"sync"
)

// Knowledge providers: wikis, knowledge bases and a provider's documents, separate from issue trackers. A knowledge source
// connection signs in on its own or borrows the sign-in of a tracker connection whose provider is in BorrowFrom.
// docs/design/tracker.md, "Writing a provider".

// Article is a document of a knowledge source.
type Article struct {
	ID          string
	Title       string
	URL         string
	Content     string
	Project     string
	UpdatedAt   string
	ParentID    string
	HasChildren bool
}

// KnowledgeProvider is one knowledge source connection, opened.
type KnowledgeProvider interface {
	// Test signs in and says as whom.
	Test(ctx context.Context) (Identity, error)
	// SearchArticles searches with query, narrowed by base (the connection's Query, in the provider's syntax).
	SearchArticles(ctx context.Context, query, base string, limit int) ([]Article, error)
	// ListArticles lists the children of parentID, or the top level narrowed by base.
	ListArticles(ctx context.Context, parentID, base string, page Page) (articles []Article, hasMore bool, err error)
	// GetArticle reads one with its content. errors.Is(err, ErrNotFound) when it is not there.
	GetArticle(ctx context.Context, id string) (*Article, error)
}

// KnowledgeCapabilities says what a knowledge provider can do and what its form asks for.
type KnowledgeCapabilities struct {
	// Provider is its id, such as "youtrack-kb" or "confluence".
	Provider    string
	DisplayName string
	// Description is one line on what it gives, in English.
	Description string
	// AuthMethods are the ways it signs in on its own. Empty when it can only borrow.
	AuthMethods  []AuthMethod
	OptionFields []OptionField
	DocsURL      string
	Logo         string
	// BorrowFrom are the tracker providers whose connections it can borrow the sign-in of: same vendor and same site.
	BorrowFrom         []string
	QueryLanguage      string
	QueryExample       string
	BaseURLPlaceholder string
	DefaultBaseURL     string
	AuthHints          map[AuthMethod]AuthHint
	// Tree says documents have children.
	Tree bool
}

// KnowledgeOpenOptions is what opening a knowledge provider takes. When the connection borrows a tracker connection's sign-in,
// Auth, Login, OAuthClientID and Credentials are that connection's, resolved now and never copied, and OnTokens writes back to it.
type KnowledgeOpenOptions struct {
	// BaseURL is the connection's own address, or the borrowed connection's when it has none.
	BaseURL       string
	Auth          AuthMethod
	Login         string
	OAuthClientID string
	Credentials   Credentials
	// Options are the borrowed connection's options, overridden by the knowledge connection's own.
	Options  map[string]string
	OnTokens func(Credentials)
	VCSToken func(ctx context.Context, host string) (string, error)
	// BorrowedFrom is the tracker provider the sign-in is borrowed from; "" for its own.
	BorrowedFrom string
	// TrackerBaseURL is the borrowed connection's address; "" for its own sign-in.
	TrackerBaseURL string
}

// KnowledgeFactory makes the knowledge providers of one kind.
type KnowledgeFactory struct {
	Capabilities KnowledgeCapabilities
	Open         func(KnowledgeOpenOptions) (KnowledgeProvider, error)
	// OAuth for its own OAuth sign-in; nil when it has none.
	OAuth func(baseURL string) (OAuthExchange, error)
	// Validate checks what the generic checks can't. Optional.
	Validate func(baseURL string, options map[string]string) error
}

var (
	knowledgeMu    sync.RWMutex
	knowledgeKinds = map[string]KnowledgeFactory{}
	knowledgeOrder []string
)

// RegisterKnowledge makes a knowledge provider available. Providers call it from init.
func RegisterKnowledge(name string, f KnowledgeFactory) {
	knowledgeMu.Lock()
	defer knowledgeMu.Unlock()
	if _, ok := knowledgeKinds[name]; !ok {
		knowledgeOrder = append(knowledgeOrder, name)
	}
	knowledgeKinds[name] = f
}

// KnowledgeFactories lists the registered knowledge providers in registration order.
func KnowledgeFactories() []KnowledgeFactory {
	knowledgeMu.RLock()
	defer knowledgeMu.RUnlock()
	out := make([]KnowledgeFactory, 0, len(knowledgeOrder))
	for _, n := range knowledgeOrder {
		out = append(out, knowledgeKinds[n])
	}
	return out
}

func knowledgeFactoryOf(name string) (KnowledgeFactory, error) {
	knowledgeMu.RLock()
	defer knowledgeMu.RUnlock()
	f, ok := knowledgeKinds[name]
	if !ok {
		return KnowledgeFactory{}, fmt.Errorf("unknown knowledge provider %q", name)
	}
	return f, nil
}

// CanBorrow says whether a knowledge provider may borrow the sign-in of a tracker provider's connection.
func (c KnowledgeCapabilities) CanBorrow(trackerProvider string) bool {
	for _, p := range c.BorrowFrom {
		if p == trackerProvider {
			return true
		}
	}
	return false
}
