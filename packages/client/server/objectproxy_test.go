package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type objectTokens struct{ refreshed int }

func (t *objectTokens) Token(context.Context) (string, error) {
	if t.refreshed > 0 {
		return "fresh", nil
	}
	return "stale", nil
}

func (t *objectTokens) Refresh(context.Context) (string, error) {
	t.refreshed++
	return "fresh", nil
}

var objectSHA = strings.Repeat("ab", 32)

func TestObjectProxy(t *testing.T) {
	var seen *http.Request
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		seen = r
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Range", "bytes 1-2/10")
		w.Header().Set("Content-Disposition", "inline")
		w.Header().Set("Set-Cookie", "session=core")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "xy")
	}))
	defer core.Close()

	p := NewObjectProxy(core.URL, core.Client(), &objectTokens{})
	ticket, at := p.Ticket()
	if time.Until(at) < time.Hour {
		t.Fatalf("expires %v", at)
	}
	get := func(path string, h http.Header) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range h {
			req.Header[k] = v
		}
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/objects/"+objectSHA+"?ticket="+ticket, http.Header{"Range": {"bytes=1-2"}, "Authorization": {"Bearer agent-token"}, "Cookie": {"a=b"}})
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "xy" {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if seen.Header.Get("Range") != "bytes=1-2" || seen.URL.Path != "/api/objects/"+objectSHA || seen.Header.Get("Cookie") != "" || strings.Contains(seen.URL.RawQuery, "ticket") {
		t.Fatalf("core saw %v %v", seen.URL, seen.Header)
	}
	h := rec.Header()
	if h.Get("Content-Range") != "bytes 1-2/10" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") == "" || h.Get("Set-Cookie") != "" {
		t.Fatalf("%v", h)
	}

	for _, bad := range []string{"", "?ticket=", "?ticket=123.abc", "?ticket=" + ticket + "x"} {
		if rec := get("/objects/"+objectSHA+bad, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%q: %d", bad, rec.Code)
		}
	}
	for _, bad := range []string{"/objects/abc", "/objects/" + strings.Repeat("AB", 32), "/objects/" + objectSHA + "/.."} {
		if rec := get(bad+"?ticket="+ticket, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("%q: %d", bad, rec.Code)
		}
	}

	p.now = func() time.Time { return time.Now().Add(13 * time.Hour) }
	if rec := get("/objects/"+objectSHA+"?ticket="+ticket, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired: %d", rec.Code)
	}
}

func TestTCPGuardLetsTheObjectRouteThroughOnly(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	g := TCPGuard(next, TCPOptions{Addr: "127.0.0.1:1234", Token: strings.Repeat("t", MinTokenLength)})
	for path, want := range map[string]int{"/objects/" + objectSHA: http.StatusTeapot, "/harness.agent.v1.AgentService/Subscribe": http.StatusUnauthorized, "/objectsx": http.StatusUnauthorized} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1234"+path, nil)
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
}
