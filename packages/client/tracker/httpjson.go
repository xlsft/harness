package tracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The JSON over HTTP the Jira, Linear and GitHub providers share. YouTrack keeps its own client, adapted from ymcp.

// HTTPError is an answer outside 2xx. Body is cut short and never holds a credential the request sent.
type HTTPError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s %s: %d", e.Method, e.Path, e.Status)
	}
	return fmt.Sprintf("%s %s: %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// Classify turns transport and status failures into this package's kinds, keeping the message.
func Classify(err error) error {
	if err == nil {
		return nil
	}
	for _, k := range []error{ErrNotFound, ErrAuth, ErrForbidden, ErrTransient} {
		if errors.Is(err, k) {
			return err
		}
	}
	var e *HTTPError
	if errors.As(err, &e) {
		switch {
		case e.Status == http.StatusNotFound || e.Status == http.StatusGone:
			return fmt.Errorf("%w: %v", ErrNotFound, err)
		case e.Status == http.StatusUnauthorized:
			return fmt.Errorf("%w: %v", ErrAuth, err)
		case e.Status == http.StatusForbidden:
			return fmt.Errorf("%w: %v", ErrForbidden, err)
		case e.Status == http.StatusTooManyRequests || e.Status >= 500:
			return fmt.Errorf("%w: %v", ErrTransient, err)
		}
		return err
	}
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: %v", ErrTransient, err)
	}
	return err
}

// JSONClient makes JSON requests to one API root.
type JSONClient struct {
	// Base is the API root; paths are joined to it.
	Base string
	// Authorize sets the request's credentials. It may refresh them.
	Authorize func(ctx context.Context, h http.Header) error
	// Header is added to every request.
	Header http.Header
	HTTP   *http.Client
	// Retries are the waits before trying again after a transient failure; nil: 500 ms, 1 s.
	Retries []time.Duration
	// Name says whose API it is in errors, such as "Jira".
	Name string
}

var defaultRetries = []time.Duration{500 * time.Millisecond, time.Second}

// NewHTTPClient is the transport providers use: the environment's proxy, a 30 s timeout.
func NewHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{Transport: t, Timeout: 30 * time.Second}
}

var htmlTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// BodySummary makes an error body readable: an HTML page (a proxy, a VPN portal) becomes its title.
func BodySummary(b []byte) string {
	s := strings.TrimSpace(string(b))
	low := strings.ToLower(s)
	if strings.HasPrefix(low, "<!doctype html") || strings.HasPrefix(low, "<html") {
		t := s
		if m := htmlTitle.FindStringSubmatch(s); m != nil {
			t = m[1]
		}
		s = "(HTML page) " + strings.Join(strings.Fields(t), " ")
	}
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return s
}

// Do sends body as JSON (nil: none) and decodes a JSON answer into out (nil: ignored). query may be nil.
func (c *JSONClient) Do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	_, err := c.DoHeaders(ctx, method, path, query, body, out)
	return err
}

// DoHeaders is Do that also gives the answer's headers, for paging by Link.
func (c *JSONClient) DoHeaders(ctx context.Context, method, path string, query url.Values, body, out any) (http.Header, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	retries := c.Retries
	if retries == nil {
		retries = defaultRetries
	}
	for attempt := 0; ; attempt++ {
		h, err := c.once(ctx, method, path, query, payload, out)
		if err == nil || attempt >= len(retries) || !retryable(method, err) {
			return h, Classify(err)
		}
		select {
		case <-ctx.Done():
			return nil, Classify(ctx.Err())
		case <-time.After(retryWait(err, retries[attempt])):
		}
	}
}

type retryAfterError struct {
	*HTTPError
	after time.Duration
}

func (e *retryAfterError) Unwrap() error { return e.HTTPError }

func retryWait(err error, d time.Duration) time.Duration {
	var ra *retryAfterError
	if errors.As(err, &ra) && ra.after > 0 {
		return min(ra.after, 10*time.Second)
	}
	return d
}

// retryable: reads on a network failure, 429 and 5xx; writes only on 429 and 503, which say nothing was done.
func retryable(method string, err error) bool {
	var e *HTTPError
	if errors.As(err, &e) {
		if e.Status == http.StatusTooManyRequests || e.Status == http.StatusServiceUnavailable {
			return true
		}
		return method == http.MethodGet && e.Status >= 500
	}
	var netErr net.Error
	return method == http.MethodGet && errors.As(err, &netErr)
}

func (c *JSONClient) once(ctx context.Context, method, path string, query url.Values, payload []byte, out any) (http.Header, error) {
	u := strings.TrimRight(c.Base, "/") + "/" + strings.TrimLeft(path, "/")
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		u = path
	}
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	for k, v := range c.Header {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Authorize != nil {
		if err := c.Authorize(ctx, req.Header); err != nil {
			return nil, err
		}
	}
	hc := c.HTTP
	if hc == nil {
		hc = NewHTTPClient()
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		he := &HTTPError{Method: method, Path: "/" + strings.TrimLeft(strings.SplitN(path, "?", 2)[0], "/"), Status: res.StatusCode, Body: BodySummary(data)}
		if s, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && s > 0 {
			return nil, &retryAfterError{he, time.Duration(s) * time.Second}
		}
		return nil, he
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return res.Header, nil
	}
	if ct := res.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "json") {
		name := c.Name
		if name == "" {
			name = "the provider"
		}
		return nil, fmt.Errorf("unexpected %s answer from %s at %s (check the address, VPN and proxy): %s", ct, name, req.URL.Host, BodySummary(data))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, fmt.Errorf("%s %s: invalid JSON: %w", method, req.URL.Path, err)
	}
	return res.Header, nil
}

// OAuthTokens posts a token request (form-encoded unless asJSON) and reads the usual answer.
func OAuthTokens(ctx context.Context, hc *http.Client, tokenURL string, form url.Values, asJSON bool) (Credentials, error) {
	var body io.Reader
	ct := "application/x-www-form-urlencoded"
	if asJSON {
		m := map[string]string{}
		for k := range form {
			m[k] = form.Get(k)
		}
		b, _ := json.Marshal(m)
		body, ct = bytes.NewReader(b), "application/json"
	} else {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, body)
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Accept", "application/json")
	if hc == nil {
		hc = NewHTTPClient()
	}
	res, err := hc.Do(req)
	if err != nil {
		return Credentials{}, Classify(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var t struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	_ = json.Unmarshal(data, &t)
	if res.StatusCode != http.StatusOK || t.AccessToken == "" {
		msg := t.Error
		if t.Description != "" {
			msg += ": " + t.Description
		}
		if msg == "" {
			msg = BodySummary(data)
		}
		err := &HTTPError{Method: http.MethodPost, Path: req.URL.Path, Status: res.StatusCode, Body: msg}
		if res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnauthorized {
			return Credentials{}, fmt.Errorf("%w: %v", ErrAuth, err)
		}
		return Credentials{}, Classify(err)
	}
	out := Credentials{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken}
	if t.ExpiresIn > 0 {
		out.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second).Unix()
	}
	return out, nil
}

// TokenSource keeps an OAuth access token fresh, refreshing it shortly before it runs out.
type TokenSource struct {
	Creds   Credentials
	Refresh func(ctx context.Context, refreshToken string) (Credentials, error)
	// OnTokens stores refreshed tokens.
	OnTokens func(Credentials)

	mu chan struct{}
}

// Token gives a usable access token.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	if t.mu == nil {
		t.mu = make(chan struct{}, 1)
	}
	t.mu <- struct{}{}
	defer func() { <-t.mu }()
	fresh := t.Creds.ExpiresAt == 0 || time.Now().Add(time.Minute).Unix() < t.Creds.ExpiresAt
	if t.Creds.AccessToken != "" && fresh {
		return t.Creds.AccessToken, nil
	}
	if t.Creds.RefreshToken == "" || t.Refresh == nil {
		if t.Creds.AccessToken == "" {
			return "", fmt.Errorf("%w: not authorized yet: use Authorize", ErrAuth)
		}
		return "", fmt.Errorf("%w: the authorization expired: use Authorize again", ErrAuth)
	}
	n, err := t.Refresh(ctx, t.Creds.RefreshToken)
	if err != nil {
		return "", err
	}
	if n.RefreshToken == "" {
		n.RefreshToken = t.Creds.RefreshToken
	}
	n.ClientSecret, n.Secret = t.Creds.ClientSecret, t.Creds.Secret
	t.Creds = n
	if t.OnTokens != nil {
		t.OnTokens(n)
	}
	return n.AccessToken, nil
}

// HTMLText makes stored HTML (Confluence's storage format) readable as text: blocks become lines, tags go, entities are read.
func HTMLText(s string) string {
	r := strings.NewReplacer("</p>", "\n\n", "<br>", "\n", "<br/>", "\n", "<br />", "\n", "</li>", "\n", "</tr>", "\n", "</h1>", "\n\n", "</h2>", "\n\n", "</h3>", "\n\n", "</h4>", "\n\n", "</h5>", "\n\n", "</h6>", "\n\n", "</div>", "\n", "</pre>", "\n", "<li>", "- ")
	s = r.Replace(s)
	var b strings.Builder
	in := false
	for _, ch := range s {
		switch {
		case ch == '<':
			in = true
		case ch == '>' && in:
			in = false
		case !in:
			b.WriteRune(ch)
		}
	}
	out := strings.NewReplacer("&nbsp;", " ", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&apos;", "'", "&amp;", "&").Replace(b.String())
	lines := strings.Split(out, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	out = strings.Join(lines, "\n")
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(out)
}
