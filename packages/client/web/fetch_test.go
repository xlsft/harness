package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	"github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/netguard"
	"github.com/xlsft/harness/packages/client/tools"
)

// recordingSummarizer returns a fixed answer and records what it was given.
type recordingSummarizer struct {
	mu   sync.Mutex
	reqs []SummaryRequest
}

func (r *recordingSummarizer) Summarize(_ context.Context, _ *tools.Env, req SummaryRequest) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	return "ANSWER", nil
}

func (r *recordingSummarizer) last(t *testing.T) SummaryRequest {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) == 0 {
		t.Fatal("summarizer was not called")
	}
	return r.reqs[len(r.reqs)-1]
}

// publicGuard treats every name except localhost as a public host.
func publicGuard() *netguard.Guard {
	return &netguard.Guard{Lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}}
}

func newFetch(s Summarizer) *WebFetch {
	return &WebFetch{Summarizer: s, Cache: NewCache(), Guard: publicGuard(), Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }}
}

func runFetch(t *testing.T, f *WebFetch, u, prompt string) tools.Result {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"url": u, "prompt": prompt})
	env := tools.NewEnv(t.TempDir(), t.TempDir())
	env.SessionID = "sess-1"
	return f.Run(context.Background(), env, tools.Call{ID: "f1", Input: b})
}

func TestFetchHTMLToMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>T</title><style>body{color:red}</style><script>alert("x")</script></head>
<body><h1>Release notes</h1><p>Version <strong>2.0</strong> drops Python 3.8.</p>
<ul><li>one</li><li>two</li></ul><a href="/docs/install">Install guide</a><pre><code>pip install x</code></pre></body></html>`)
	}))
	defer srv.Close()
	s := &recordingSummarizer{}
	r := runFetch(t, newFetch(s), srv.URL+"/notes#section", "What changed?")
	if r.IsError || text(r) != "ANSWER" {
		t.Fatalf("result: %v %q", r.IsError, text(r))
	}
	got := s.last(t)
	for _, want := range []string{"# Release notes", "Version **2.0** drops Python 3.8.", "- one", "[Install guide](" + srv.URL + "/docs/install)", "pip install x"} {
		if !strings.Contains(got.Content, want) {
			t.Errorf("markdown lacks %q:\n%s", want, got.Content)
		}
	}
	if strings.Contains(got.Content, "alert") || strings.Contains(got.Content, "color:red") {
		t.Errorf("scripts and styles must be dropped:\n%s", got.Content)
	}
	if got.Prompt != "What changed?" || got.URL != srv.URL+"/notes" {
		t.Errorf("summary request: %+v", got)
	}
	out := r.Structured.(FetchOutput)
	if out.Code != 200 || out.CodeText != "OK" || out.Bytes == 0 || out.Result != "ANSWER" || out.URL != srv.URL+"/notes" {
		t.Errorf("structured: %+v", out)
	}
}

func TestFetchTextJSONAndCharset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/data.json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"version":"1.2.3","<b>":"kept"}`)
		case "/latin1":
			w.Header().Set("Content-Type", "text/plain; charset=iso-8859-1")
			w.Write([]byte("caf\xe9"))
		case "/notype":
			w.Header()["Content-Type"] = nil
			fmt.Fprint(w, "plain words without a type")
		}
	}))
	defer srv.Close()
	s := &recordingSummarizer{}
	f := newFetch(s)
	for path, want := range map[string]string{
		"/data.json": `{"version":"1.2.3","<b>":"kept"}`,
		"/latin1":    "café",
		"/notype":    "plain words without a type",
	} {
		if r := runFetch(t, f, srv.URL+path, "p"); r.IsError {
			t.Fatalf("%s: %s", path, text(r))
		}
		if got := s.last(t).Content; got != want {
			t.Errorf("%s: content %q, want %q", path, got, want)
		}
	}
}

func TestFetchRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old":
			http.Redirect(w, r, "/new", http.StatusFound)
		case "/new":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "new page")
		case "/elsewhere":
			// 127.0.0.1 -> localhost is a different host name.
			http.Redirect(w, r, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)+"/new", http.StatusMovedPermanently)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	defer srv.Close()
	s := &recordingSummarizer{}
	f := newFetch(s)

	r := runFetch(t, f, srv.URL+"/old", "p")
	if r.IsError || s.last(t).Content != "new page" {
		t.Fatalf("same-host redirect: %q", text(r))
	}
	if out := r.Structured.(FetchOutput); out.FinalURL != srv.URL+"/new" {
		t.Errorf("final url: %+v", out)
	}

	n := len(s.reqs)
	r = runFetch(t, f, srv.URL+"/elsewhere", "p")
	body := text(r)
	target := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/new"
	if r.IsError || !strings.Contains(body, "Redirect URL: "+target) || !strings.Contains(body, "Status: 301 Moved Permanently") ||
		!strings.Contains(body, "call WebFetch again") {
		t.Fatalf("cross-host redirect notice: %v %q", r.IsError, body)
	}
	if out := r.Structured.(FetchOutput); out.RedirectURL != target || out.Code != 301 {
		t.Errorf("structured notice: %+v", out)
	}
	if len(s.reqs) != n {
		t.Error("a redirect notice must not reach the model")
	}

	if r := runFetch(t, f, srv.URL+"/loop", "p"); !r.IsError || !strings.Contains(text(r), "redirected more than 10 times") {
		t.Errorf("redirect loop: %q", text(r))
	}
}

func TestSameHost(t *testing.T) {
	u := func(s string) *url.URL { x, _ := url.Parse(s); return x }
	cases := []struct {
		a, b string
		want bool
	}{
		{"https://example.com/a", "https://example.com/b", true},
		{"https://example.com/a", "https://www.example.com/b", true},
		{"https://www.example.com/a", "https://example.com/b", true},
		{"http://example.com/a", "https://example.com/b", true},
		{"https://example.com/a", "http://example.com/b", false},
		{"https://example.com/a", "https://docs.example.com/b", false},
		{"https://example.com/a", "https://example.com:8443/b", false},
		{"https://example.com/a", "https://user:pw@example.com/b", false},
		{"https://example.com/a", "https://evil.com/b", false},
	}
	for _, c := range cases {
		if got := sameHost(u(c.a), u(c.b)); got != c.want {
			t.Errorf("sameHost(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestFetchLimitsAndRefusals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", "3000")
			fmt.Fprint(w, strings.Repeat("x", 3000))
		case "/big-chunked":
			w.Header().Set("Content-Type", "text/plain")
			for range 10 {
				fmt.Fprint(w, strings.Repeat("y", 500))
				w.(http.Flusher).Flush()
			}
		case "/image":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("\x89PNG\r\n\x1a\nbinary"))
		case "/sniffed":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte("%PDF-1.7 \x00\x01\x02"))
		case "/fake-text":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("abc\x00\x00def"))
		case "/missing":
			http.NotFound(w, r)
		case "/slow":
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		case "/long":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, strings.Repeat("é", 300))
		}
	}))
	defer srv.Close()
	s := &recordingSummarizer{}
	f := newFetch(s)
	f.MaxBytes = 1000
	f.Timeout = 300 * time.Millisecond
	f.MaxContentChars = 100
	for path, want := range map[string]string{
		"/big":         "too large",
		"/big-chunked": "larger than 1000 bytes",
		"/image":       "binary content (image/png)",
		"/sniffed":     "binary content (application/pdf)",
		"/fake-text":   "contains binary data",
		"/missing":     "HTTP 404 Not Found",
		"/slow":        "timed out",
	} {
		r := runFetch(t, f, srv.URL+path, "p")
		if !r.IsError || !strings.Contains(text(r), want) {
			t.Errorf("%s: got error=%v %q, want %q", path, r.IsError, text(r), want)
		}
	}
	if len(s.reqs) != 0 {
		t.Fatal("refused pages must not reach the model")
	}
	r := runFetch(t, f, srv.URL+"/long", "p")
	if r.IsError {
		t.Fatal(text(r))
	}
	if got := s.last(t); !got.Truncated || got.Content != strings.Repeat("é", 100) || !r.Structured.(FetchOutput).Truncated {
		t.Errorf("truncation: %+v", got)
	}
}

func TestFetchCache(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "hit %d", hits.Load())
	}))
	defer srv.Close()
	s := &recordingSummarizer{}
	f := newFetch(s)
	now := time.Now()
	f.Cache.now = func() time.Time { return now }

	runFetch(t, f, srv.URL+"/page", "first question")
	r := runFetch(t, f, srv.URL+"/page#other-anchor", "second question")
	if hits.Load() != 1 || !r.Structured.(FetchOutput).Cached {
		t.Fatalf("second fetch within 15 minutes must hit the cache (server hits %d)", hits.Load())
	}
	if got := s.last(t); got.Prompt != "second question" || got.Content != "hit 1" {
		t.Fatalf("cached content is summarized with the new prompt: %+v", got)
	}
	now = now.Add(16 * time.Minute)
	r = runFetch(t, f, srv.URL+"/page", "third")
	if hits.Load() != 2 || r.Structured.(FetchOutput).Cached || s.last(t).Content != "hit 2" {
		t.Fatalf("expired entry must be refetched (hits %d)", hits.Load())
	}
}

func TestCacheEvictsBySize(t *testing.T) {
	c := NewCache()
	c.MaxBytes = 10
	c.put("a", page{content: "12345"})
	c.put("b", page{content: "12345"})
	c.put("c", page{content: "123"})
	if _, ok := c.get("a"); ok {
		t.Error("oldest entry must be evicted")
	}
	if _, ok := c.get("c"); !ok {
		t.Error("newest entry must be kept")
	}
	c.put("huge", page{content: strings.Repeat("x", 11)})
	if _, ok := c.get("huge"); ok {
		t.Error("entries larger than the cache are not stored")
	}
}

func TestNormalizeURL(t *testing.T) {
	f := newFetch(nil)
	ctx := context.Background()
	for in, want := range map[string]string{
		"http://example.com/a?b=1#frag":   "https://example.com/a?b=1",
		"HTTP://Example.COM:80/x":         "https://example.com/x",
		"http://example.com:8080/x":       "https://example.com:8080/x",
		"https://docs.example.com/x":      "https://docs.example.com/x",
		"http://localhost:5173/":          "http://localhost:5173/",
		"http://127.0.0.1:8080/api":       "http://127.0.0.1:8080/api",
		"http://192.168.1.20/status":      "http://192.168.1.20/status",
		"  https://example.com/trimmed  ": "https://example.com/trimmed",
	} {
		u, err := f.NormalizeURL(ctx, in)
		if err != nil || u.String() != want {
			t.Errorf("NormalizeURL(%q) = %v, %v; want %s", in, u, err, want)
		}
	}
	for in, want := range map[string]string{
		"":                            "url is required",
		"ftp://example.com/f":         "unsupported URL scheme",
		"file:///etc/passwd":          "unsupported URL scheme",
		"example.com/no-scheme":       "invalid URL",
		"https://user:pw@example.com": "credentials",
		"https://example.com/" + strings.Repeat("a", 2001): "longer than 2000",
	} {
		if _, err := f.NormalizeURL(ctx, in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("NormalizeURL(%q) error = %v, want %q", in, err, want)
		}
	}
}

func TestDialGuardRefusesPrivateAddressOfPublicName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "internal secret")
	}))
	defer srv.Close()
	_, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	// The permission check saw a public address (memoized); DNS now answers
	// with loopback (rebinding). The fetcher must not connect.
	var answer atomic.Value
	answer.Store(netip.MustParseAddr("93.184.216.34"))
	g := &netguard.Guard{Lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{answer.Load().(netip.Addr)}, nil
	}}
	if g.IsPrivate(context.Background(), "rebind.test") {
		t.Fatal("setup: rebind.test must start public")
	}
	answer.Store(netip.MustParseAddr("127.0.0.1"))
	s := &recordingSummarizer{}
	f := &WebFetch{Summarizer: s, Guard: g, Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }}
	u, _ := url.Parse("http://rebind.test:" + port + "/")
	_, _, err := f.fetch(context.Background(), u)
	var fe *fetchError
	if !errors.As(err, &fe) || !strings.Contains(fe.msg, "private-network address") {
		t.Fatalf("err = %v", err)
	}
}

func TestGatewaySummarizer(t *testing.T) {
	gw := fakegateway.New(func(int, *llm.Request) fakegateway.Reply {
		return fakegateway.Reply{Events: fakegateway.Message("m1", fakegateway.Text("  The page says 42.  "))}
	})
	gwURL, stop, err := gw.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h2>Answer</h2><p>It is {{prompt}} 42.</p>")
	}))
	defer page.Close()

	sum := &GatewaySummarizer{
		Gateway:       connectgw.New(gwURL, nil, auth.StaticToken("hat_test")),
		Model:         func() string { return "small-model" },
		BundleVersion: func() string { return "v7" },
		Template: func(*tools.Env) string {
			return "URL: {{url}}\nREQUEST: {{prompt}}\nCONTENT:\n{{content}}"
		},
	}
	f := newFetch(sum)
	r := runFetch(t, f, page.URL+"/q", "What is the answer?")
	if r.IsError || text(r) != "The page says 42." {
		t.Fatalf("result: %v %q", r.IsError, text(r))
	}
	reqs := gw.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d gateway requests", len(reqs))
	}
	req := reqs[0]
	if req.Model != "small-model" || req.Metadata == nil || req.Metadata.RequestClass != "auxiliary" || req.Metadata.SessionID != "sess-1" {
		t.Fatalf("request: model %q metadata %+v", req.Model, req.Metadata)
	}
	msg := req.Messages[0].Content[0].Text
	want := "URL: " + page.URL + "/q\nREQUEST: What is the answer?\nCONTENT:\n## Answer\n\nIt is {{prompt}} 42."
	if msg != want {
		t.Fatalf("prompt:\n%s\nwant:\n%s", msg, want)
	}

	// Without a template (an old bundle) the tool reports the problem.
	sum.Template = func(*tools.Env) string { return "" }
	f.Cache = NewCache()
	if r := runFetch(t, f, page.URL+"/q", "p"); !r.IsError || !strings.Contains(text(r), SummaryTemplateKey) {
		t.Fatalf("missing template: %q", text(r))
	}
}
