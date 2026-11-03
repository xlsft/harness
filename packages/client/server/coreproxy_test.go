package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xlsft/harness/packages/client/bootstrap"
)

type proxyTokens struct {
	mu        sync.Mutex
	token     string
	refreshed int
}

func (t *proxyTokens) Token(context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.token, nil
}

func (t *proxyTokens) Refresh(context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshed++
	t.token = "hat_fresh"
	return t.token, nil
}

func TestCoreProxyForwardsProviderServiceWithServerCredentials(t *testing.T) {
	var seen []*http.Request
	var bodies []string
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Clone(context.Background()))
		bodies = append(bodies, string(b))
		if r.Header.Get("Authorization") != "Bearer hat_fresh" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":"unauthenticated"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "x=y")
		_, _ = io.WriteString(w, `{"canManageGlobal":true}`)
	}))
	defer core.Close()

	tokens := &proxyTokens{token: "hat_stale"}
	s := New(bootstrap.Options{})
	defer s.Close()
	s.Core = &CoreProxy{URL: core.URL, HTTP: core.Client(), Tokens: tokens}
	mux := s.mux()

	req := httptest.NewRequest(http.MethodPost, "/harness.llm.v1.ProviderService/ListProviders", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer local-agent-token")
	req.Header.Set("Cookie", "session=abc")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "canManageGlobal") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if tokens.refreshed != 1 || len(seen) != 2 {
		t.Fatalf("want one refresh and a retry, got refreshed=%d calls=%d", tokens.refreshed, len(seen))
	}
	for _, r := range seen {
		if strings.Contains(r.Header.Get("Authorization"), "local-agent-token") || r.Header.Get("Cookie") != "" {
			t.Fatalf("frontend credentials leaked to the server: %v", r.Header)
		}
		if r.URL.Path != "/harness.llm.v1.ProviderService/ListProviders" || r.Header.Get("Connect-Protocol-Version") != "1" {
			t.Fatalf("bad forward: %s %v", r.URL.Path, r.Header)
		}
	}
	if bodies[1] != `{}` {
		t.Fatalf("body %q", bodies[1])
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatal("server cookies must not reach the frontend")
	}
}

func TestCoreProxyRefusesOtherServicesAndStreams(t *testing.T) {
	calls := 0
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer core.Close()
	s := New(bootstrap.Options{})
	defer s.Close()
	s.Core = &CoreProxy{URL: core.URL, HTTP: core.Client(), Tokens: &proxyTokens{token: "hat_x"}}
	mux := s.mux()

	for _, c := range []struct{ path, ct string }{
		{"/harness.llm.v1.GatewayService/ListModels", "application/json"},
		{"/harness.auth.v1.DeviceAuthService/GetMe", "application/json"},
		{"/harness.llm.v1.ProviderService/ListProviders", "application/connect+json"},
	} {
		req := httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", c.ct)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("%s (%s) was forwarded", c.path, c.ct)
		}
	}
	if calls != 0 {
		t.Fatalf("server got %d calls", calls)
	}
}
