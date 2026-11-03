package permissions

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func browserReq(tool, pageURL string, input map[string]any) Request {
	r := req(tool, input)
	r.URL = pageURL
	return r
}

func TestBrowserSitePermission(t *testing.T) {
	ctx := context.Background()
	e := New(Config{Cwd: t.TempDir(), Home: t.TempDir(), NetGuard: testGuard()})

	// Read-only browser tools never ask.
	snap := browserReq("BrowserSnapshot", "https://example.com/", map[string]any{})
	snap.ReadOnly = true
	if d := e.Evaluate(snap); d.Behavior != Allow {
		t.Fatalf("snapshot = %+v", d)
	}

	// Local pages are allowed by default.
	for _, u := range []string{"http://localhost:3000/", "http://127.0.0.1:8080/x", "http://app.localhost/", "http://[::1]:5173/", "file://" + filepath.ToSlash(e.cwd) + "/index.html", "about:blank", ""} {
		if d := e.Evaluate(browserReq("BrowserNavigate", u, map[string]any{"url": u})); d.Behavior != Allow {
			t.Errorf("navigate %q = %+v, want allow", u, d)
		}
		if d := e.Evaluate(browserReq("BrowserClick", u, map[string]any{"ref": "ref_1"})); d.Behavior != Allow {
			t.Errorf("click on %q = %+v, want allow", u, d)
		}
	}

	// Files outside the working directories ask, as Read would.
	if d := e.Evaluate(browserReq("BrowserNavigate", "file:///etc/passwd", map[string]any{"url": "file:///etc/passwd"})); d.Behavior != Ask {
		t.Fatalf("file outside the working directories = %+v", d)
	}

	// A new site asks, with the site rule as the "always" suggestion.
	nav := browserReq("BrowserNavigate", "https://example.com/a", map[string]any{"url": "https://example.com/a"})
	if d := e.Evaluate(nav); d.Behavior != Ask || d.UserOnly {
		t.Fatalf("new site = %+v, want a plain ask", d)
	}
	if got := e.Suggest(nav); !reflect.DeepEqual(got, []string{"Browser(domain:example.com)"}) {
		t.Fatalf("suggestions %v", got)
	}

	// The user allows once: the site is approved for the rest of the session, for every site action.
	e.SetAsker(&fixedAsker{d: Decision{Behavior: Allow}})
	if d := e.Check(ctx, nav); d.Behavior != Allow {
		t.Fatalf("check = %+v", d)
	}
	if !reflect.DeepEqual(e.AllowedSites(), []string{"example.com"}) {
		t.Fatalf("allowed sites %v", e.AllowedSites())
	}
	e.SetAsker(nil)
	for _, tool := range []string{"BrowserClick", "BrowserType", "BrowserKey", "BrowserScroll", "BrowserNavigate"} {
		if d := e.Evaluate(browserReq(tool, "https://example.com/b", map[string]any{})); d.Behavior != Allow || d.ResolvedBy != "site" {
			t.Errorf("%s on the approved site = %+v", tool, d)
		}
	}
	// Another site still asks; without an asker that is a denial.
	if d := e.Check(ctx, browserReq("BrowserClick", "https://go.dev/", map[string]any{})); d.Behavior != Deny {
		t.Fatalf("unapproved site without asker = %+v", d)
	}

	// A denied answer approves nothing.
	e.SetAsker(&fixedAsker{d: Decision{Behavior: Deny}})
	_ = e.Check(ctx, browserReq("BrowserNavigate", "https://go.dev/", map[string]any{"url": "https://go.dev/"}))
	if len(e.AllowedSites()) != 1 {
		t.Fatalf("a denial approved a site: %v", e.AllowedSites())
	}

	// LAN hosts are for the user only.
	if d := e.Evaluate(browserReq("BrowserNavigate", "http://intranet.corp/", map[string]any{})); d.Behavior != Ask || !d.UserOnly {
		t.Fatalf("LAN host = %+v, want a user-only ask", d)
	}
	// Odd schemes ask the user.
	if d := e.Evaluate(browserReq("BrowserNavigate", "chrome://settings", map[string]any{})); d.Behavior != Ask || !d.UserOnly {
		t.Fatalf("chrome:// = %+v", d)
	}
}

func TestBrowserRules(t *testing.T) {
	e := New(Config{Cwd: t.TempDir(), Home: t.TempDir(), NetGuard: testGuard(),
		Allow: []string{"Browser(domain:*.example.com)", "BrowserClick(domain:go.dev)"},
		Deny:  []string{"Browser(domain:evil.com)"},
	})
	check := func(tool, u string) Behavior { return e.Evaluate(browserReq(tool, u, map[string]any{})).Behavior }
	if check("BrowserNavigate", "https://docs.example.com/") != Allow || check("BrowserType", "https://docs.example.com/") != Allow {
		t.Error("Browser(domain:*.example.com) covers the site actions")
	}
	if check("BrowserClick", "https://go.dev/") != Allow || check("BrowserType", "https://go.dev/") != Ask {
		t.Error("a per-tool rule covers only that tool")
	}
	if check("BrowserNavigate", "https://evil.com/") != Deny {
		t.Error("deny rules apply to browser sites")
	}
	if check("BrowserEval", "https://docs.example.com/") != Ask {
		t.Error("Browser(domain:...) must not cover BrowserEval")
	}
}

func TestBrowserEvalAlwaysAsks(t *testing.T) {
	e := New(Config{Cwd: t.TempDir(), Home: t.TempDir(), NetGuard: testGuard()})
	ev := browserReq("BrowserEval", "http://localhost:3000/", map[string]any{"expression": "1"})
	if d := e.Evaluate(ev); d.Behavior != Ask {
		t.Fatalf("eval on localhost = %+v, want ask", d)
	}
	if got := e.Suggest(ev); !reflect.DeepEqual(got, []string{"BrowserEval"}) {
		t.Fatalf("eval suggestions %v", got)
	}
	e.AllowSite("example.com")
	if d := e.Evaluate(browserReq("BrowserEval", "https://example.com/", map[string]any{})); d.Behavior != Ask {
		t.Fatalf("eval on an approved site = %+v, want ask", d)
	}
	e2 := New(Config{Cwd: t.TempDir(), Home: t.TempDir(), Allow: []string{"BrowserEval(domain:localhost)"}})
	if d := e2.Evaluate(ev); d.Behavior != Allow {
		t.Fatalf("eval with a rule = %+v", d)
	}
	plan := New(Config{Cwd: t.TempDir(), Home: t.TempDir(), DefaultMode: ModePlan})
	if d := plan.Evaluate(ev); d.Behavior != Deny {
		t.Fatalf("eval in plan mode = %+v", d)
	}
	if d := plan.Evaluate(browserReq("BrowserNavigate", "http://localhost/", map[string]any{})); d.Behavior != Deny {
		t.Fatalf("navigate in plan mode = %+v", d)
	}
}

func TestBrowserSensitiveFieldAlwaysAsksTheUser(t *testing.T) {
	ctx := context.Background()
	for _, m := range []Mode{ModeDefault, ModeAcceptEdits, ModeBypassPermissions, ModeAuto} {
		e := New(Config{Cwd: t.TempDir(), Home: t.TempDir(), DefaultMode: m, Allow: []string{"Browser", "BrowserType"}})
		r := browserReq("BrowserType", "http://localhost:3000/login", map[string]any{"ref": "ref_3", "text": "x"})
		r.Sensitive = "The agent wants to type into a password field."
		d := e.Evaluate(r)
		if d.Behavior != Ask || !d.UserOnly || d.Message != r.Sensitive {
			t.Errorf("%s: sensitive = %+v, want a user-only ask", m, d)
		}
		if got := e.Suggest(r); len(got) != 0 {
			t.Errorf("%s: a sensitive call offers rules %v", m, got)
		}
		// Without anyone to ask it is refused.
		if d := e.Check(ctx, r); d.Behavior != Deny {
			t.Errorf("%s: sensitive without asker = %+v", m, d)
		}
		a := &fixedAsker{d: Decision{Behavior: Allow}}
		e.SetAsker(a)
		if d := e.Check(ctx, r); d.Behavior != Allow || a.last == nil {
			t.Errorf("%s: the user approves = %+v", m, d)
		}
	}
	e := New(Config{Cwd: t.TempDir(), Home: t.TempDir(), DefaultMode: ModeDontAsk})
	r := browserReq("BrowserType", "http://localhost/", map[string]any{})
	r.Sensitive = "password"
	if d := e.Check(ctx, r); d.Behavior != Deny {
		t.Fatalf("dontAsk = %+v", d)
	}
	// A deny rule still wins.
	e = New(Config{Cwd: t.TempDir(), Home: t.TempDir(), Deny: []string{"BrowserType"}})
	if d := e.Evaluate(r); d.Behavior != Deny {
		t.Fatalf("deny rule = %+v", d)
	}
}
