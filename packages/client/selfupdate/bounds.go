package selfupdate

import (
	"fmt"
	"strings"
)

// Valid reports whether s is a release version: X.Y.Z or vX.Y.Z, with an optional -prerelease.
func Valid(s string) bool {
	_, ok := parseVersion(s)
	return ok
}

// Compare orders two versions like Newer does: -1, 0 or 1. ok is false when either isn't a version.
func Compare(a, b string) (c int, ok bool) {
	if _, ok := parseVersion(a); !ok {
		return 0, false
	}
	if _, ok := parseVersion(b); !ok {
		return 0, false
	}
	switch {
	case Newer(a, b):
		return 1, true
	case Newer(b, a):
		return -1, true
	}
	return 0, true
}

// Bounds are the client versions an organization's managed policy allows (minimumClientVersion, maximumClientVersion).
// An empty or unreadable bound is no bound.
type Bounds struct {
	Min, Max string
}

// Set reports whether there is any bound.
func (b Bounds) Set() bool { return Valid(b.Min) || Valid(b.Max) }

// Standing of a version against Bounds.
const (
	Within = ""
	Below  = "below"
	Above  = "above"
)

// Check says where a version stands. A development build (0.0.0-dev) is below any minimum, as the server sees it.
func (b Bounds) Check(current string) string {
	if Valid(b.Min) {
		if c, ok := Compare(current, b.Min); !ok || c < 0 {
			return Below
		}
	}
	if Valid(b.Max) {
		if c, ok := Compare(current, b.Max); ok && c > 0 {
			return Above
		}
	}
	return Within
}

// Message says it in a sentence, or "" when the version is within the bounds.
func (b Bounds) Message(program, current string) string {
	switch b.Check(current) {
	case Below:
		return fmt.Sprintf("%s %s is older than %s, the oldest version your administrator allows: update it (harness-client update, or Settings > About in the app)", program, current, norm(b.Min))
	case Above:
		return fmt.Sprintf("%s %s is newer than %s, the newest version your administrator allows; it keeps working, and updates stop at %s", program, current, norm(b.Max), norm(b.Max))
	}
	return ""
}

// Target is the release an update should install, given the newest stable tag: that tag when the maximum allows it, else
// the maximum itself. "" when the update would go nowhere newer than current.
func (b Bounds) Target(latest, current string) string {
	target := latest
	if Valid(b.Max) {
		if c, ok := Compare(latest, b.Max); ok && c > 0 {
			target = "v" + norm(b.Max)
		}
	}
	if !Newer(target, current) {
		return ""
	}
	return target
}

func norm(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }
