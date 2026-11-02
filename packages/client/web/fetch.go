package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"golang.org/x/net/html/charset"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/netguard"
	"github.com/xlsft/harness/packages/client/tools"
)

// Fetch limits.
const (
	DefaultMaxBytes        = 10 << 20
	DefaultTimeout         = 60 * time.Second
	DefaultMaxContentChars = 100_000
	MaxURLLength           = 2000
	maxRedirects           = 10
)

// WebFetch is the WebFetch tool: fetch a URL locally, convert it to Markdown
// and answer the caller's prompt about it with the small-slot model.
type WebFetch struct {
	// Summarizer answers the prompt about the page; nil makes calls fail.
	Summarizer Summarizer
	// Cache keeps converted pages per URL (nil disables caching).
	Cache *Cache
	// Guard classifies private-network hosts (default netguard.Default).
	Guard *netguard.Guard
	// MaxBytes bounds the response body (default 10 MB).
	MaxBytes int64
	// Timeout bounds the whole fetch (default 60 s).
	Timeout time.Duration
	// MaxContentChars bounds the Markdown handed to the model (default 100 000).
	MaxContentChars int
	// UserAgent is sent with every request.
	UserAgent string
	// Proxy picks an HTTP proxy per request (default http.ProxyFromEnvironment).
	Proxy func(*http.Request) (*url.URL, error)
}

var _ tools.Tool = (*WebFetch)(nil)

type fetchInput struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
}

// FetchOutput is persisted as the toolUseResult of a WebFetch call.
type FetchOutput struct {
	URL        string `json:"url"`
	Bytes      int    `json:"bytes"`
	Code       int    `json:"code"`
	CodeText   string `json:"codeText"`
	Result     string `json:"result"`
	DurationMs int64  `json:"durationMs"`
	// FinalURL is set when same-host redirects were followed.
	FinalURL string `json:"finalUrl,omitempty"`
	// RedirectURL is set when a redirect to another host was not followed.
	RedirectURL string `json:"redirectUrl,omitempty"`
	Cached      bool   `json:"cached,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// page is a fetched, converted document.
type page struct {
	finalURL    string
	code        int
	codeText    string
	bytes       int
	contentType string
	content     string
	truncated   bool
}

// redirectNotice is a redirect to another host that was not followed.
type redirectNotice struct {
	from, to string
	code     int
	codeText string
}

// fetchError is a failure reported to the model as is.
type fetchError struct{ msg string }

func (e *fetchError) Error() string { return e.msg }

func failf(format string, args ...any) error { return &fetchError{fmt.Sprintf(format, args...)} }

// Name implements tools.Tool.
func (*WebFetch) Name() string { return "WebFetch" }

// ReadOnly implements tools.Tool.
func (*WebFetch) ReadOnly(json.RawMessage) bool { return true }

func (t *WebFetch) guard() *netguard.Guard {
	if t.Guard != nil {
		return t.Guard
	}
	return netguard.Default
}

// Run implements tools.Tool.
func (t *WebFetch) Run(ctx context.Context, env *tools.Env, call tools.Call) tools.Result {
	var in fetchInput
	if err := json.Unmarshal(orEmpty(call.Input), &in); err != nil {
		return tools.ErrorResult("InputValidationError: %v", err)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return tools.ErrorResult("InputValidationError: prompt is required")
	}
	u, err := t.NormalizeURL(ctx, in.URL)
	if err != nil {
		return tools.ErrorResult("%v", err)
	}
	start := time.Now()
	key := u.String()
	pg, cached := t.Cache.get(key)
	if !cached {
		var notice *redirectNotice
		pg, notice, err = t.fetch(ctx, u)
		if err != nil {
			if ctx.Err() != nil {
				return tools.ErrorResult("The fetch of %s was cancelled.", key)
			}
			var fe *fetchError
			if errors.As(err, &fe) {
				return tools.ErrorResult("%s", fe.msg)
			}
			return tools.ErrorResult("Failed to fetch %s: %v", key, err)
		}
		if notice != nil {
			text := noticeText(notice)
			return tools.Result{
				Content: []llm.ContentBlock{llm.TextBlock(text)},
				Structured: FetchOutput{URL: key, Code: notice.code, CodeText: notice.codeText, Result: text,
					RedirectURL: notice.to, DurationMs: time.Since(start).Milliseconds()},
				Summary: "Redirected to another host",
			}
		}
		t.Cache.put(key, pg)
	}
	if t.Summarizer == nil {
		return tools.ErrorResult("WebFetch cannot process pages in this session (no gateway connection).")
	}
	answer, err := t.Summarizer.Summarize(ctx, env, SummaryRequest{URL: pg.finalURL, Prompt: in.Prompt, Content: pg.content, Truncated: pg.truncated})
	if err != nil {
		if ctx.Err() != nil {
			return tools.ErrorResult("The fetch of %s was cancelled.", key)
		}
		return tools.ErrorResult("Fetched %s but the model could not process it: %v", key, err)
	}
	out := FetchOutput{URL: key, Bytes: pg.bytes, Code: pg.code, CodeText: pg.codeText, Result: answer,
		DurationMs: time.Since(start).Milliseconds(), Cached: cached, Truncated: pg.truncated}
	if pg.finalURL != key {
		out.FinalURL = pg.finalURL
	}
	return tools.Result{
		Content:    []llm.ContentBlock{llm.TextBlock(answer)},
		Structured: out,
		Summary:    fmt.Sprintf("Received %s (%d %s)", formatBytes(pg.bytes), pg.code, pg.codeText),
	}
}

// NormalizeURL validates a WebFetch URL and upgrades http to https, except
// for private-network hosts (a local dev server rarely speaks TLS).
func (t *WebFetch) NormalizeURL(ctx context.Context, raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("InputValidationError: url is required")
	}
	if len(raw) > MaxURLLength {
		return nil, fmt.Errorf("InputValidationError: url is longer than %d characters", MaxURLLength)
	}
	u, err := url.Parse(raw)
	if err == nil && u.Scheme != "" {
		u.Scheme = strings.ToLower(u.Scheme)
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("InputValidationError: unsupported URL scheme %q; only http and https are supported", u.Scheme)
		}
	}
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("InputValidationError: invalid URL %q; pass a fully-formed http(s) URL", raw)
	}
	if u.User != nil {
		return nil, errors.New("InputValidationError: URLs with credentials (user:password@host) are not supported")
	}
	u.Host = strings.ToLower(u.Host)
	u.Fragment, u.RawFragment = "", ""
	if u.Scheme == "http" && !t.guard().IsPrivate(ctx, u.Hostname()) {
		u.Scheme = "https"
		if u.Port() == "80" {
			u.Host = u.Hostname()
			if strings.Contains(u.Host, ":") {
				u.Host = "[" + u.Host + "]"
			}
		}
	}
	return u, nil
}

// fetch downloads u, following same-host redirects.
func (t *WebFetch) fetch(ctx context.Context, u *url.URL) (page, *redirectNotice, error) {
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	maxBytes := t.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return page{}, nil, err
	}
	ua := t.UserAgent
	if ua == "" {
		ua = "harness-webfetch/1.0"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/markdown, text/html;q=0.9, application/xhtml+xml;q=0.9, text/plain;q=0.8, application/json;q=0.8, */*;q=0.5")

	client := t.client(ctx, u)
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) && ue.Timeout() {
			return page{}, nil, failf("Fetching %s timed out after %s.", u, timeout)
		}
		var be *blockedError
		if errors.As(err, &be) {
			return page{}, nil, failf("Refused to fetch %s: %v", u, be)
		}
		return page{}, nil, err
	}
	defer resp.Body.Close()
	codeText := strings.TrimPrefix(resp.Status, fmt.Sprint(resp.StatusCode))
	codeText = strings.TrimSpace(codeText)
	if codeText == "" {
		codeText = http.StatusText(resp.StatusCode)
	}

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		if loc == "" {
			return page{}, nil, failf("%s answered %d %s without a Location header.", u, resp.StatusCode, codeText)
		}
		to, err := resp.Request.URL.Parse(loc)
		if err != nil {
			return page{}, nil, failf("%s redirected to an invalid URL %q.", u, loc)
		}
		return page{}, &redirectNotice{from: resp.Request.URL.String(), to: to.String(), code: resp.StatusCode, codeText: codeText}, nil
	}
	if resp.StatusCode >= 400 {
		return page{}, nil, failf("%s answered HTTP %d %s.", resp.Request.URL, resp.StatusCode, codeText)
	}
	if resp.ContentLength > maxBytes {
		return page{}, nil, failf("%s is too large (%s); WebFetch reads at most %s.", u, formatBytes(int(resp.ContentLength)), formatBytes(int(maxBytes)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return page{}, nil, failf("Fetching %s timed out after %s.", u, timeout)
		}
		return page{}, nil, err
	}
	if int64(len(body)) > maxBytes {
		return page{}, nil, failf("%s is larger than %s, the most WebFetch reads.", u, formatBytes(int(maxBytes)))
	}

	final := resp.Request.URL.String()
	ctype := resp.Header.Get("Content-Type")
	content, kind, err := convert(body, ctype, final)
	if err != nil {
		return page{}, nil, err
	}
	pg := page{finalURL: final, code: resp.StatusCode, codeText: codeText, bytes: len(body), contentType: kind, content: content}
	limit := t.MaxContentChars
	if limit <= 0 {
		limit = DefaultMaxContentChars
	}
	if utf8.RuneCountInString(pg.content) > limit {
		pg.content = truncateRunes(pg.content, limit)
		pg.truncated = true
	}
	return pg, nil, nil
}

// client builds the HTTP client of one fetch: same-host redirects are
// followed, others stop the fetch, and connections to private addresses are
// refused unless the target itself is a private host (approved explicitly by
// the permission engine).
func (t *WebFetch) client(ctx context.Context, u *url.URL) *http.Client {
	g := t.guard()
	allowPrivate := g.IsPrivate(ctx, u.Hostname())
	proxy := t.Proxy
	if proxy == nil {
		proxy = http.ProxyFromEnvironment
	}
	proxyHost := ""
	if probe, err := http.NewRequest(http.MethodGet, u.String(), nil); err == nil {
		if pu, err := proxy(probe); err == nil && pu != nil {
			proxyHost = pu.Host
		}
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if allowPrivate || (proxyHost != "" && strings.EqualFold(addr, hostPort(proxyHost))) {
			return dialer.DialContext(ctx, network, addr)
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		var addrs []netip.Addr
		if a, err := netip.ParseAddr(host); err == nil {
			addrs = []netip.Addr{a}
		} else if addrs, err = g.Resolve(ctx, host); err != nil {
			return nil, err
		}
		var lastErr error = &blockedError{host: host}
		for _, a := range addrs {
			if netguard.IsPrivateAddr(a) {
				continue
			}
			c, err := dialer.DialContext(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
			if err == nil {
				return c, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	tr := &http.Transport{
		Proxy:                 proxy,
		DialContext:           dial,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
	origin := u
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return failf("%s redirected more than %d times.", origin, maxRedirects)
			}
			if !sameHost(via[len(via)-1].URL, req.URL) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// blockedError is a connection refused by the private-address guard.
type blockedError struct{ host string }

func (e *blockedError) Error() string {
	return fmt.Sprintf("%s resolves to a private-network address, which WebFetch only connects to when that host was requested directly", e.host)
}

func hostPort(h string) string {
	if _, _, err := net.SplitHostPort(h); err == nil {
		return h
	}
	return net.JoinHostPort(h, "80")
}

// sameHost reports whether a redirect from a to b stays on the same site:
// the same host (ignoring a leading "www."), the same port, no credentials,
// and either the same scheme or an upgrade from http to https.
func sameHost(a, b *url.URL) bool {
	if b.User != nil {
		return false
	}
	if a.Scheme != b.Scheme && !(a.Scheme == "http" && b.Scheme == "https") {
		return false
	}
	strip := func(h string) string { return strings.TrimPrefix(strings.ToLower(h), "www.") }
	if strip(a.Hostname()) != strip(b.Hostname()) {
		return false
	}
	return effectivePort(a) == effectivePort(b) || (a.Scheme != b.Scheme && a.Port() == "" && b.Port() == "")
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// noticeText tells the model about a redirect to another host.
func noticeText(n *redirectNotice) string {
	return fmt.Sprintf("The page redirected to a different host, and WebFetch does not follow such redirects on its own.\n\n"+
		"Original URL: %s\nRedirect URL: %s\nStatus: %d %s\n\n"+
		"To read the redirect target, call WebFetch again with url set to the redirect URL and the same prompt.",
		n.from, n.to, n.code, n.codeText)
}

// Content kinds.
const (
	kindHTML = "html"
	kindText = "text"
)

// convert decodes a response body to text: HTML becomes Markdown, text and
// structured formats stay as they are, binary content is refused.
func convert(body []byte, contentType, pageURL string) (string, string, error) {
	media := ""
	if contentType != "" {
		if mt, _, err := mime.ParseMediaType(contentType); err == nil {
			media = strings.ToLower(mt)
		}
	}
	if media == "" || media == "application/octet-stream" {
		media, _, _ = mime.ParseMediaType(http.DetectContentType(body))
		contentType = media
	}
	kind := ""
	switch {
	case media == "text/html" || media == "application/xhtml+xml":
		kind = kindHTML
	case isTextual(media):
		kind = kindText
	default:
		return "", "", failf("%s is binary content (%s); WebFetch only reads HTML and text. Use a different source, or ask the user to download it.", pageURL, media)
	}
	text, err := decodeText(body, contentType)
	if err != nil {
		return "", "", err
	}
	if kind == kindText && looksBinary(text) {
		return "", "", failf("%s is declared as %s but contains binary data; WebFetch only reads HTML and text.", pageURL, media)
	}
	if kind == kindHTML {
		md, err := htmltomarkdown.ConvertString(text, converter.WithDomain(pageURL))
		if err != nil {
			return "", "", failf("Could not convert the HTML of %s: %v", pageURL, err)
		}
		return strings.TrimSpace(md), kindHTML, nil
	}
	return text, kindText, nil
}

func isTextual(media string) bool {
	if strings.HasPrefix(media, "text/") {
		return true
	}
	switch media {
	case "application/json", "application/xml", "application/javascript", "application/x-javascript",
		"application/ecmascript", "application/x-yaml", "application/yaml", "application/toml",
		"application/x-sh", "application/sql", "application/graphql", "application/x-ndjson", "application/ld+json":
		return true
	}
	return strings.HasSuffix(media, "+json") || strings.HasSuffix(media, "+xml")
}

// decodeText converts body to UTF-8 using the declared or detected charset.
func decodeText(body []byte, contentType string) (string, error) {
	if utf8.Valid(body) {
		return string(body), nil
	}
	r, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return strings.ToValidUTF8(string(body), "�"), nil
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return strings.ToValidUTF8(string(body), "�"), nil
	}
	return string(out), nil
}

func looksBinary(s string) bool {
	head := s[:min(len(s), 8192)]
	return strings.IndexByte(head, 0) >= 0
}

func truncateRunes(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

func formatBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
