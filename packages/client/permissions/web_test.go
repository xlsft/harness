package permissions

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/xlsft/harness/packages/client/netguard"
)

// testGuard resolves intranet.corp to a LAN address and nothing else.
func testGuard() *netguard.Guard {
	return &netguard.Guard{Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "intranet.corp":
			return []netip.Addr{netip.MustParseAddr("10.0.0.5")}, nil
		case "docs.example.com", "example.com", "evil.com", "go.dev":
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}
		return nil, errors.New("no such host")
	}}
}

func webReq(tool string, input map[string]any) Request {
	r := req(tool, input)
	r.ReadOnly = true // the web tools are read-only
	return r
}

func fetch(u string) Request {
	return webReq("WebFetch", map[string]any{"url": u, "prompt": "p"})
}

func search() Request { return webReq("WebSearch", map[string]any{"query": "golang release"}) }

func TestWebToolsNeedPermission(t *testing.T) {
	dir := t.TempDir()
	for _, m := range []Mode{ModeDefault, ModeAcceptEdits, ModePlan} {
		e := New(Config{Cwd: dir, Home: t.TempDir(), DefaultMode: m, NetGuard: testGuard()})
		if d := e.Evaluate(search()); d.Behavior != Ask || d.UserOnly {
			t.Errorf("%s: WebSearch without a rule = %+v, want a plain ask", m, d)
		}
		if d := e.Evaluate(fetch("https://go.dev/doc")); d.Behavior != Ask || d.UserOnly {
			t.Errorf("%s: WebFetch without a rule = %+v, want a plain ask", m, d)
		}
	}
	e := New(Config{Cwd: dir, Home: t.TempDir(), DefaultMode: ModeBypassPermissions, NetGuard: testGuard()})
	if e.Evaluate(search()).Behavior != Allow || e.Evaluate(fetch("https://go.dev")).Behavior != Allow {
		t.Error("bypassPermissions allows web tools")
	}
}

func TestWebRules(t *testing.T) {
	dir := t.TempDir()
	e := New(Config{
		Cwd: dir, Home: t.TempDir(), NetGuard: testGuard(),
		Allow: []string{"WebSearch", "WebFetch(domain:*.example.com)", "WebFetch(domain:go.dev)", "WebFetch(domain:localhost)"},
		Deny:  []string{"WebFetch(domain:evil.com)"},
		Ask:   []string{"WebFetch(domain:ask.example.com)"},
	})
	cases := []struct {
		name string
		r    Request
		want Behavior
		user bool
	}{
		{"search rule", search(), Allow, false},
		{"exact domain", fetch("https://go.dev/doc/install"), Allow, false},
		{"wildcard subdomain", fetch("https://docs.example.com/a?b=c"), Allow, false},
		{"wildcard does not match apex", fetch("https://example.com/"), Ask, false},
		{"other domain", fetch("https://golang.org/"), Ask, false},
		{"deny rule", fetch("https://evil.com/x"), Deny, false},
		{"ask rule", fetch("https://ask.example.com/x"), Ask, true},
		{"exact private allow rule", fetch("http://localhost:5173/"), Allow, false},
		{"private without exact rule", fetch("http://127.0.0.1:8080/"), Ask, true},
		{"metadata address", fetch("http://169.254.169.254/latest/meta-data/"), Ask, true},
		{"metadata v6", fetch("http://[fd00:ec2::254]/latest/"), Ask, true},
		{"name resolving to LAN", fetch("https://intranet.corp/wiki"), Ask, true},
	}
	for _, c := range cases {
		d := e.Evaluate(c.r)
		if d.Behavior != c.want || d.UserOnly != c.user {
			t.Errorf("%s: got %s userOnly=%v (%s), want %s userOnly=%v", c.name, d.Behavior, d.UserOnly, d.Message, c.want, c.user)
		}
	}
	if got := e.Suggest(fetch("https://Docs.Python.org/3/")); !reflect.DeepEqual(got, []string{"WebFetch(domain:docs.python.org)"}) {
		t.Errorf("suggestion = %v", got)
	}
	if got := e.Suggest(search()); !reflect.DeepEqual(got, []string{"WebSearch"}) {
		t.Errorf("suggestion = %v", got)
	}
}

func TestPrivateFetchNeverAutoAllowed(t *testing.T) {
	dir := t.TempDir()
	// A bare WebFetch rule and a domain wildcard allow public hosts, not private ones.
	e := New(Config{Cwd: dir, Home: t.TempDir(), NetGuard: testGuard(), Allow: []string{"WebFetch", "WebFetch(domain:*)"}})
	if d := e.Evaluate(fetch("https://go.dev/")); d.Behavior != Allow {
		t.Fatalf("bare rule allows public hosts: %+v", d)
	}
	for _, u := range []string{"http://localhost:3001/", "http://192.168.1.1/admin", "https://intranet.corp/"} {
		if d := e.Evaluate(fetch(u)); d.Behavior != Ask || !d.UserOnly {
			t.Errorf("%s: %+v, want a user-only ask", u, d)
		}
	}

	// auto mode: the Decider is not consulted, the user is asked.
	dec := &countingDecider{res: AutoResult{Behavior: Allow, Provider: "rules"}}
	a := &fixedAsker{d: Decision{Behavior: Allow}}
	e = New(Config{Cwd: dir, Home: t.TempDir(), NetGuard: testGuard(), DefaultMode: ModeAuto, Decider: dec})
	e.SetAsker(a)
	if d := e.Check(context.Background(), fetch("http://localhost:8080/")); d.Behavior != Allow || d.ResolvedBy != "user" {
		t.Fatalf("private fetch in auto mode: %+v", d)
	}
	if dec.n != 0 || a.last == nil || a.last.Reason == "" {
		t.Fatalf("decider calls %d, asker %+v", dec.n, a.last)
	}
	if !reflect.DeepEqual(a.last.Suggestions, []string{"WebFetch(domain:localhost)"}) {
		t.Errorf("suggestions = %v", a.last.Suggestions)
	}
	// Public targets go to the Decider as usual.
	if d := e.Check(context.Background(), fetch("https://go.dev/")); d.Behavior != Allow || d.ResolvedBy != "auto" || dec.n != 1 {
		t.Fatalf("public fetch in auto mode: %+v (decider calls %d)", d, dec.n)
	}

	// dontAsk denies, non-interactive denies.
	e = New(Config{Cwd: dir, Home: t.TempDir(), NetGuard: testGuard(), DefaultMode: ModeDontAsk, Allow: []string{"WebFetch"}})
	if d := e.Check(context.Background(), fetch("http://10.0.0.1/")); d.Behavior != Deny {
		t.Fatalf("dontAsk: %+v", d)
	}
}

type countingDecider struct {
	n   int
	res AutoResult
}

func (c *countingDecider) Decide(context.Context, AutoRequest) (AutoResult, error) {
	c.n++
	return c.res, nil
}
