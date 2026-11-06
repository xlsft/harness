// Package origins matches browser origins against the allowlist of the client's TCP listener. An entry is an exact
// origin (scheme://host[:port]) or a whole scheme that no web page can have, written "scheme://*": an editor's webview
// gets a random host per panel, so it can't be named in advance.
package origins

import (
	"fmt"
	"strings"
)

// webSchemes can be served by anyone on the web, so they must never be allowed as a whole.
var webSchemes = map[string]bool{"http": true, "https": true, "ws": true, "wss": true, "file": true, "data": true, "blob": true, "about": true, "null": true}

const wildcard = "://*"

// Validate refuses a wildcard entry that would admit web pages.
func Validate(list []string) error {
	for _, o := range list {
		o = normalize(o)
		if scheme, ok := strings.CutSuffix(o, wildcard); ok && (scheme == "" || webSchemes[scheme]) {
			return fmt.Errorf("origin %q would allow any web page; name the origin exactly", o)
		}
		if strings.Contains(strings.TrimSuffix(o, wildcard), "*") {
			return fmt.Errorf("origin %q: the only wildcard is a whole scheme, scheme://*", o)
		}
	}
	return nil
}

// Matcher answers whether an origin is allowed.
type Matcher struct {
	exact   map[string]bool
	schemes map[string]bool
}

// New builds a Matcher. An entry Validate refuses never matches, so a caller that skipped it stays closed.
func New(list []string) *Matcher {
	m := &Matcher{exact: map[string]bool{}, schemes: map[string]bool{}}
	for _, o := range list {
		o = normalize(o)
		if o == "" || Validate([]string{o}) != nil {
			continue
		}
		if scheme, ok := strings.CutSuffix(o, wildcard); ok {
			m.schemes[scheme] = true
		} else {
			m.exact[o] = true
		}
	}
	return m
}

// Allowed reports whether origin is on the list.
func (m *Matcher) Allowed(origin string) bool {
	origin = strings.ToLower(origin)
	if m.exact[origin] {
		return true
	}
	scheme, rest, ok := strings.Cut(origin, "://")
	return ok && rest != "" && m.schemes[scheme]
}

func normalize(o string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(o), "/"))
}
