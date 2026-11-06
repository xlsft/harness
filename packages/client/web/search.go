// Package web implements the WebSearch and WebFetch tools. WebSearch runs the
// query on the harness server (harness.search.v1.SearchService); WebFetch
// fetches the page locally, converts it to Markdown and has the server's
// small-slot model answer the caller's prompt about it through the gateway.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	searchv1 "github.com/xlsft/harness/packages/client/gen/harness/search/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/search/v1/searchv1connect"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/tools"
)

// SearchQuery is one search request.
type SearchQuery struct {
	Query          string
	AllowedDomains []string
	BlockedDomains []string
	SessionID      string
}

// SearchResult is one hit.
type SearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
}

// SearchResponse is the answer of a Searcher.
type SearchResponse struct {
	Provider string
	Results  []SearchResult
}

// Searcher runs web searches (the server's SearchService).
type Searcher interface {
	Search(ctx context.Context, q SearchQuery) (SearchResponse, error)
}

// ErrSearchUnavailable means the server has no working search service: it
// does not implement SearchService, or it could not be reached.
var ErrSearchUnavailable = errors.New("web search is not available")

// SearchClient is the Connect client of harness.search.v1.SearchService.
type SearchClient struct {
	rpc    searchv1connect.SearchServiceClient
	tokens rpc.Tokens
	// Timeout bounds one search (default 30 s).
	Timeout time.Duration
}

// NewSearchClient creates the client. httpClient may be nil.
func NewSearchClient(baseURL string, httpClient *http.Client, tokens rpc.Tokens) *SearchClient {
	if httpClient == nil {
		httpClient = rpc.HTTPClient(baseURL)
	}
	return &SearchClient{
		rpc:    searchv1connect.NewSearchServiceClient(httpClient, strings.TrimRight(baseURL, "/"), rpc.ClientOptions()...),
		tokens: tokens, Timeout: 30 * time.Second,
	}
}

// Search implements Searcher.
func (c *SearchClient) Search(ctx context.Context, q SearchQuery) (SearchResponse, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	var code connect.Code
	hasInfo := false
	resp, err := rpc.WithAuth(ctx, c.tokens, func(token string) (*connect.Response[searchv1.SearchResponse], error) {
		r := connect.NewRequest(&searchv1.SearchRequest{
			Query: q.Query, AllowedDomains: q.AllowedDomains, BlockedDomains: q.BlockedDomains, SessionId: q.SessionID,
		})
		rpc.SetBearer(r.Header(), token)
		res, err := c.rpc.Search(ctx, r)
		code, hasInfo = connect.CodeOf(err), rpc.ErrorInfo(err) != nil
		return res, err
	})
	if err != nil {
		// Canceled with our context still live means the server went away mid-call.
		if ctx.Err() == nil && (code == connect.CodeUnimplemented || code == connect.CodeNotFound || code == connect.CodeCanceled ||
			(code == connect.CodeUnavailable && !hasInfo)) {
			return SearchResponse{}, fmt.Errorf("%w: %v", ErrSearchUnavailable, err)
		}
		return SearchResponse{}, err
	}
	out := SearchResponse{Provider: resp.Msg.GetProvider()}
	for _, r := range resp.Msg.GetResults() {
		out.Results = append(out.Results, SearchResult{Title: r.GetTitle(), URL: r.GetUrl(), Snippet: r.GetSnippet(), PublishedAt: r.GetPublishedAt()})
	}
	return out, nil
}

// WebSearch is the WebSearch tool.
type WebSearch struct {
	// Searcher runs the query; nil makes every call fail with a clear error.
	Searcher Searcher
}

var _ tools.Tool = (*WebSearch)(nil)

type searchInput struct {
	Query          string   `json:"query"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
}

// SearchOutput is persisted as the toolUseResult of a WebSearch call.
type SearchOutput struct {
	Query           string         `json:"query"`
	Provider        string         `json:"provider,omitempty"`
	Results         []SearchResult `json:"results"`
	DurationSeconds float64        `json:"durationSeconds"`
}

// Name implements tools.Tool.
func (*WebSearch) Name() string { return "WebSearch" }

// ReadOnly implements tools.Tool.
func (*WebSearch) ReadOnly(json.RawMessage) bool { return true }

// Run implements tools.Tool.
func (t *WebSearch) Run(ctx context.Context, env *tools.Env, call tools.Call) tools.Result {
	var in searchInput
	if err := json.Unmarshal(orEmpty(call.Input), &in); err != nil {
		return tools.ErrorResult("InputValidationError: %v", err)
	}
	in.Query = strings.TrimSpace(in.Query)
	if len([]rune(in.Query)) < 2 {
		return tools.ErrorResult("InputValidationError: query must be at least 2 characters")
	}
	allowed, err := normalizeDomains(in.AllowedDomains)
	if err != nil {
		return tools.ErrorResult("InputValidationError: allowed_domains: %v", err)
	}
	blocked, err := normalizeDomains(in.BlockedDomains)
	if err != nil {
		return tools.ErrorResult("InputValidationError: blocked_domains: %v", err)
	}
	if len(allowed) > 0 && len(blocked) > 0 {
		return tools.ErrorResult("InputValidationError: allowed_domains and blocked_domains cannot be used together")
	}
	if t.Searcher == nil {
		return tools.ErrorResult("Web search is not configured in this session (no harness server connection).")
	}
	start := time.Now()
	sid := ""
	if env != nil {
		sid = env.SessionID
	}
	resp, err := t.Searcher.Search(ctx, SearchQuery{Query: in.Query, AllowedDomains: allowed, BlockedDomains: blocked, SessionID: sid})
	if err != nil {
		if ctx.Err() != nil {
			return tools.ErrorResult("Web search was cancelled.")
		}
		return tools.ErrorResult("%s", searchErrorText(err))
	}
	results := filterResults(resp.Results, allowed, blocked)
	out := SearchOutput{Query: in.Query, Provider: resp.Provider, Results: results, DurationSeconds: time.Since(start).Seconds()}
	if out.Results == nil {
		out.Results = []SearchResult{}
	}
	return tools.Result{
		Content:    []llm.ContentBlock{llm.TextBlock(FormatResults(in.Query, results))},
		Structured: out,
		Summary:    fmt.Sprintf("Found %d result(s)", len(results)),
	}
}

// searchErrorText turns a search failure into a message for the model.
func searchErrorText(err error) string {
	if errors.Is(err, ErrSearchUnavailable) {
		return "Web search is not available on this harness server (the search service is missing or unreachable). Continue without web search, or ask the user for the information."
	}
	var ae *llm.APIError
	if errors.As(err, &ae) {
		switch ae.Type {
		case "upstream", "overloaded":
			return "The web search provider failed: " + ae.Message + ". Try again later or continue without web search."
		case "rate_limit":
			return "The web search provider is rate limiting requests. Wait a little before searching again."
		case "invalid_request":
			return "The search request was rejected: " + ae.Message
		}
		return "Web search failed: " + ae.Message
	}
	return "Web search failed: " + err.Error()
}

// FormatResults renders search results for the model.
func FormatResults(query string, results []SearchResult) string {
	var b strings.Builder
	if len(results) == 0 {
		fmt.Fprintf(&b, "No web results found for %q. Try a different or broader query.", query)
		return b.String()
	}
	fmt.Fprintf(&b, "Web search results for %q:\n", query)
	for i, r := range results {
		title := strings.TrimSpace(r.Title)
		if title == "" {
			title = r.URL
		}
		fmt.Fprintf(&b, "\n%d. %s\n   URL: %s\n", i+1, oneLine(title), r.URL)
		if d := formatDate(r.PublishedAt); d != "" {
			fmt.Fprintf(&b, "   Published: %s\n", d)
		}
		if s := oneLine(r.Snippet); s != "" {
			fmt.Fprintf(&b, "   %s\n", s)
		}
	}
	b.WriteString("\nWhen you use these results in your answer, cite the pages you relied on as markdown links, e.g. [Title](URL). Snippets are short and may be outdated; fetch a page with WebFetch when you need its details.")
	return b.String()
}

func formatDate(s string) string {
	if s == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// normalizeDomains lowercases domains and strips schemes, paths and a
// leading "*." (as the server does).
func normalizeDomains(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, raw := range in {
		d := strings.ToLower(strings.TrimSpace(raw))
		if d == "" {
			continue
		}
		if i := strings.Index(d, "://"); i >= 0 {
			d = d[i+3:]
		}
		if i := strings.IndexByte(d, '/'); i >= 0 {
			d = d[:i]
		}
		d = strings.Trim(strings.TrimPrefix(d, "*."), ".")
		if d == "" || strings.ContainsFunc(d, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-')
		}) {
			return nil, fmt.Errorf("invalid domain %q", raw)
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out, nil
}

// domainMatches reports whether host is domain or one of its subdomains.
func domainMatches(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// filterResults applies the domain filters again on the client (the server
// already does) and drops results without an http(s) URL.
func filterResults(results []SearchResult, allowed, blocked []string) []SearchResult {
	var out []SearchResult
	for _, r := range results {
		u, err := url.Parse(r.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			continue
		}
		host := strings.ToLower(u.Hostname())
		ok := len(allowed) == 0
		for _, d := range allowed {
			if domainMatches(host, d) {
				ok = true
				break
			}
		}
		for _, d := range blocked {
			if domainMatches(host, d) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, r)
		}
	}
	return out
}

func orEmpty(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return b
}

// SearchCacheTTL is how long the cached mode of WebSearch (webSearchMode) keeps an answer.
const SearchCacheTTL = time.Hour

// searchCacheEntries bounds what one client keeps.
const searchCacheEntries = 256

// CachingSearcher answers a query it has seen from memory for ttl: repeated searches cost nothing and read the same, at the
// price of staleness. It keeps what succeeded only. It belongs to one client process and goes with it.
type CachingSearcher struct {
	inner Searcher
	ttl   time.Duration
	now   func() time.Time

	mu      sync.Mutex
	entries map[string]cachedSearch
}

type cachedSearch struct {
	resp SearchResponse
	at   time.Time
}

// NewCachingSearcher wraps inner.
func NewCachingSearcher(inner Searcher, ttl time.Duration) *CachingSearcher {
	return &CachingSearcher{inner: inner, ttl: ttl, now: time.Now, entries: map[string]cachedSearch{}}
}

// Search implements Searcher.
func (c *CachingSearcher) Search(ctx context.Context, q SearchQuery) (SearchResponse, error) {
	key := strings.ToLower(q.Query) + "\x00" + strings.Join(q.AllowedDomains, ",") + "\x00" + strings.Join(q.BlockedDomains, ",")
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && c.now().Sub(e.at) < c.ttl {
		c.mu.Unlock()
		return e.resp, nil
	}
	c.mu.Unlock()
	resp, err := c.inner.Search(ctx, q)
	if err != nil {
		return resp, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= searchCacheEntries {
		now := c.now()
		for k, e := range c.entries {
			if now.Sub(e.at) >= c.ttl {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= searchCacheEntries {
			c.entries = map[string]cachedSearch{}
		}
	}
	c.entries[key] = cachedSearch{resp: resp, at: c.now()}
	return resp, nil
}
