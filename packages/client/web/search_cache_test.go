package web

import (
	"context"
	"errors"
	"testing"
	"time"
)

type countingSearcher struct {
	calls int
	err   error
}

func (c *countingSearcher) Search(context.Context, SearchQuery) (SearchResponse, error) {
	c.calls++
	return SearchResponse{Provider: "fake"}, c.err
}

func TestCachingSearcher(t *testing.T) {
	inner := &countingSearcher{}
	c := NewCachingSearcher(inner, time.Hour)
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	ctx := context.Background()
	search := func(q SearchQuery) {
		t.Helper()
		if _, err := c.Search(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	search(SearchQuery{Query: "Go"})
	search(SearchQuery{Query: "go"})
	if inner.calls != 1 {
		t.Fatalf("calls %d: the same query went out again", inner.calls)
	}
	search(SearchQuery{Query: "go", AllowedDomains: []string{"example.com"}})
	if inner.calls != 2 {
		t.Fatalf("calls %d: other filters are another query", inner.calls)
	}
	now = now.Add(2 * time.Hour)
	search(SearchQuery{Query: "go"})
	if inner.calls != 3 {
		t.Fatalf("calls %d: a stale answer was kept", inner.calls)
	}
	// A failure isn't kept.
	inner.err = errors.New("down")
	if _, err := c.Search(ctx, SearchQuery{Query: "other"}); err == nil {
		t.Fatal("no error")
	}
	inner.err = nil
	search(SearchQuery{Query: "other"})
	if inner.calls != 5 {
		t.Fatalf("calls %d", inner.calls)
	}
}
