package web

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/auth"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	searchv1 "github.com/xlsft/harness/packages/client/gen/harness/search/v1"
	"github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/tools"
)

type fakeSearcher struct {
	got  SearchQuery
	resp SearchResponse
	err  error
}

func (f *fakeSearcher) Search(_ context.Context, q SearchQuery) (SearchResponse, error) {
	f.got = q
	return f.resp, f.err
}

func runSearch(t *testing.T, s Searcher, input map[string]any) tools.Result {
	t.Helper()
	b, _ := json.Marshal(input)
	env := tools.NewEnv(t.TempDir(), t.TempDir())
	env.SessionID = "sess-1"
	return (&WebSearch{Searcher: s}).Run(context.Background(), env, tools.Call{ID: "t1", Input: b})
}

func text(r tools.Result) string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

func TestFormatResults(t *testing.T) {
	out := FormatResults("go 1.26 release", []SearchResult{
		{Title: "Go 1.26 Release Notes", URL: "https://go.dev/doc/go1.26", Snippet: "The latest Go release,\n  version 1.26, arrives", PublishedAt: "2026-02-10T12:00:00Z"},
		{Title: "", URL: "https://example.com/x"},
	})
	for _, want := range []string{
		`Web search results for "go 1.26 release":`,
		"1. Go 1.26 Release Notes\n   URL: https://go.dev/doc/go1.26\n   Published: 2026-02-10\n   The latest Go release, version 1.26, arrives\n",
		"2. https://example.com/x\n   URL: https://example.com/x\n",
		"cite the pages you relied on as markdown links",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if out := FormatResults("q", nil); !strings.Contains(out, `No web results found for "q"`) {
		t.Errorf("empty results: %s", out)
	}
}

func TestWebSearchDomainFilters(t *testing.T) {
	f := &fakeSearcher{resp: SearchResponse{Provider: "searxng", Results: []SearchResult{
		{Title: "A", URL: "https://docs.python.org/3/a"},
		{Title: "B", URL: "https://python.org/b"},
		{Title: "C", URL: "https://evil-python.org/c"},
		{Title: "D", URL: "ftp://python.org/d"},
		{Title: "E", URL: "https://pypi.org/e"},
	}}}
	r := runSearch(t, f, map[string]any{"query": "asyncio", "allowed_domains": []string{"https://Python.org/path", "*.python.org", ""}})
	if r.IsError {
		t.Fatal(text(r))
	}
	if !reflect.DeepEqual(f.got.AllowedDomains, []string{"python.org"}) || f.got.SessionID != "sess-1" || f.got.Query != "asyncio" {
		t.Fatalf("query sent: %+v", f.got)
	}
	out := r.Structured.(SearchOutput)
	var titles []string
	for _, x := range out.Results {
		titles = append(titles, x.Title)
	}
	if !reflect.DeepEqual(titles, []string{"A", "B"}) || out.Provider != "searxng" {
		t.Fatalf("filtered results: %v (%+v)", titles, out)
	}

	r = runSearch(t, f, map[string]any{"query": "asyncio", "blocked_domains": []string{"python.org"}})
	titles = nil
	for _, x := range r.Structured.(SearchOutput).Results {
		titles = append(titles, x.Title)
	}
	if !reflect.DeepEqual(titles, []string{"C", "E"}) || !reflect.DeepEqual(f.got.BlockedDomains, []string{"python.org"}) {
		t.Fatalf("blocked results: %v, sent %+v", titles, f.got)
	}
}

func TestWebSearchInputAndErrors(t *testing.T) {
	cases := []struct {
		name  string
		s     Searcher
		input map[string]any
		want  string
	}{
		{"short query", &fakeSearcher{}, map[string]any{"query": " a "}, "at least 2 characters"},
		{"both lists", &fakeSearcher{}, map[string]any{"query": "abc", "allowed_domains": []string{"a.com"}, "blocked_domains": []string{"b.com"}}, "cannot be used together"},
		{"bad domain", &fakeSearcher{}, map[string]any{"query": "abc", "allowed_domains": []string{"a b.com"}}, "invalid domain"},
		{"no searcher", nil, map[string]any{"query": "abc"}, "not configured"},
		{"unavailable", &fakeSearcher{err: ErrSearchUnavailable}, map[string]any{"query": "abc"}, "not available on this harness server"},
		{"provider down", &fakeSearcher{err: &llm.APIError{Type: "upstream", Message: "SearXNG is unreachable"}}, map[string]any{"query": "abc"}, "provider failed: SearXNG is unreachable"},
		{"rate limited", &fakeSearcher{err: &llm.APIError{Type: "rate_limit", Message: "slow down"}}, map[string]any{"query": "abc"}, "rate limiting"},
		{"other", &fakeSearcher{err: errors.New("boom")}, map[string]any{"query": "abc"}, "Web search failed: boom"},
	}
	for _, c := range cases {
		r := runSearch(t, c.s, c.input)
		if !r.IsError || !strings.Contains(text(r), c.want) {
			t.Errorf("%s: got error=%v %q, want %q", c.name, r.IsError, text(r), c.want)
		}
	}
}

func TestSearchClientAgainstServer(t *testing.T) {
	srv := fakegateway.New(nil)
	url, stop, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	c := NewSearchClient(url, nil, auth.StaticToken("hat_test"))
	ctx := context.Background()

	// No search service on the server.
	if _, err := c.Search(ctx, SearchQuery{Query: "x"}); !errors.Is(err, ErrSearchUnavailable) {
		t.Fatalf("unimplemented: %v", err)
	}

	srv.SetSearch(func(r *searchv1.SearchRequest) (*searchv1.SearchResponse, error) {
		return &searchv1.SearchResponse{Provider: "searxng", Results: []*searchv1.SearchResult{
			{Title: "T", Url: "https://example.com/" + r.GetQuery(), Snippet: "s", PublishedAt: "2026-01-02T00:00:00Z"},
		}}, nil
	})
	resp, err := c.Search(ctx, SearchQuery{Query: "q1", AllowedDomains: []string{"example.com"}, SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	want := SearchResponse{Provider: "searxng", Results: []SearchResult{{Title: "T", URL: "https://example.com/q1", Snippet: "s", PublishedAt: "2026-01-02T00:00:00Z"}}}
	if !reflect.DeepEqual(resp, want) {
		t.Fatalf("resp = %+v", resp)
	}
	reqs := srv.SearchRequests()
	if last := reqs[len(reqs)-1]; last.GetSessionId() != "s1" || !reflect.DeepEqual(last.GetAllowedDomains(), []string{"example.com"}) {
		t.Fatalf("request = %+v", last)
	}

	// Provider failure: an upstream error with ErrorInfo.
	srv.SetSearch(func(*searchv1.SearchRequest) (*searchv1.SearchResponse, error) {
		return nil, rpc.NewError(connect.CodeUnavailable, commonv1.ErrorType_ERROR_TYPE_UPSTREAM, "SearXNG is unreachable", 0)
	})
	r := runSearch(t, c, map[string]any{"query": "abc"})
	if !r.IsError || !strings.Contains(text(r), "provider failed: SearXNG is unreachable") {
		t.Fatalf("upstream error: %q", text(r))
	}

	// Server down.
	stop()
	r = runSearch(t, c, map[string]any{"query": "abc"})
	if !r.IsError || !strings.Contains(text(r), "not available") {
		t.Fatalf("server down: %q", text(r))
	}
}
