package bootstrap_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	searchv1 "github.com/xlsft/harness/packages/client/gen/harness/search/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/web"
)

// TestWebToolsEndToEnd runs a session whose model searches, then fetches a
// local page: WebSearch goes to the server's SearchService, WebFetch fetches
// locally and asks the small-slot model with the bundle's summarization
// prompt. The private page is allowed by an exact domain rule.
func TestWebToolsEndToEnd(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Changelog</h1><p>v2 removes the legacy API.</p>")
	}))
	defer page.Close()

	srv, url := fg.Start(t, func(n int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.RequestClass == "auxiliary" {
			return fg.Reply{Events: fg.Message("s1", fg.Text("v2 removes the legacy API."))}
		}
		switch n {
		case 0:
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("tu1", "WebSearch", map[string]any{"query": "widget changelog", "allowed_domains": []string{"example.com"}}))}
		case 1:
			return fg.Reply{Events: fg.Message("m2", fg.ToolUse("tu2", "WebFetch", map[string]any{"url": page.URL + "/changelog", "prompt": "What does v2 remove?"}))}
		default:
			return fg.Reply{Events: fg.Message("m3", fg.Text("done"))}
		}
	})
	srv.Models = []string{"main-model", "small-model"}
	schema := json.RawMessage(`{"type":"object"}`)
	srv.Bundle.Tools["WebSearch"] = prompts.ToolPrompt{Description: "Search the web.", InputSchema: schema}
	srv.Bundle.Tools["WebFetch"] = prompts.ToolPrompt{Description: "Fetch a page.", InputSchema: schema}
	srv.Bundle.Reminders[web.SummaryTemplateKey] = "Answer {{prompt}} about {{url}}:\n{{content}}"
	srv.SetSearch(func(r *searchv1.SearchRequest) (*searchv1.SearchResponse, error) {
		return &searchv1.SearchResponse{Provider: "fake", Results: []*searchv1.SearchResult{
			{Title: "Widget changelog", Url: "https://example.com/widget/changelog", Snippet: "All releases."},
		}}, nil
	})

	proj, _ := filepath.EvalSymlinks(t.TempDir())
	tok := auth.StaticToken("hat_test")
	s, err := bootstrap.Open(context.Background(), bootstrap.Options{
		Cwd: proj, Gateway: connectgw.New(url, nil, tok), Prompts: prompts.NewConnectSource(url, nil, tok),
		Search:       web.NewSearchClient(url, nil, tok),
		AllowedTools: []string{"WebSearch", "WebFetch(domain:127.0.0.1)"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if names := s.ToolNames(); !slices.Contains(names, "WebSearch") || !slices.Contains(names, "WebFetch") {
		t.Fatalf("ToolNames = %v", names)
	}

	res := s.Agent.RunTurn(context.Background(), []llm.ContentBlock{llm.TextBlock("what changed in widget v2?")})
	if res.IsError || res.Result != "done" {
		t.Fatalf("turn = %+v", res)
	}
	if reqs := srv.SearchRequests(); len(reqs) != 1 || reqs[0].GetQuery() != "widget changelog" || reqs[0].GetSessionId() != s.ID {
		t.Fatalf("search requests = %v", reqs)
	}
	var main []*llm.Request
	var aux *llm.Request
	for _, r := range srv.Requests() {
		if r.Metadata != nil && r.Metadata.RequestClass == "auxiliary" {
			aux = r
		} else {
			main = append(main, r)
		}
	}
	if aux == nil || aux.Model != "small-model" {
		t.Fatalf("summarization request = %+v", aux)
	}
	if got := aux.Messages[0].Content[0].Text; got != "Answer What does v2 remove? about "+page.URL+"/changelog:\n# Changelog\n\nv2 removes the legacy API." {
		t.Fatalf("summarization prompt = %q", got)
	}
	if len(main) != 3 {
		t.Fatalf("main requests = %d", len(main))
	}
	if txt := requestText(main[1]); !strings.Contains(txt, "1. Widget changelog\n   URL: https://example.com/widget/changelog") {
		t.Fatalf("search result missing: %q", txt)
	}
	if txt := requestText(main[2]); !strings.Contains(txt, "v2 removes the legacy API.") {
		t.Fatalf("fetch result missing: %q", txt)
	}
}
