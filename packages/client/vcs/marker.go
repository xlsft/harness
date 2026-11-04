package vcs

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The marker goes at the end of every comment the agent posts on a PR or MR. An HTML comment stays invisible on GitHub
// and GitLab, and tells the agent's comments from the user's when both post under one account:
//
//	<!-- harness v=1 session=<opaque> kind=<reply|review|status> ts=<UTC RFC 3339> -->
//
// It is public text on someone else's server, so it says nothing about the owner: only the format version, the kind,
// the time and an opaque session reference, never names, hosts, paths, repository details, models, costs or prompts.

// Marker kinds; the model picks one when it posts.
const (
	MarkerKindReply  = "reply"
	MarkerKindReview = "review"
	MarkerKindStatus = "status"
)

const (
	markerVersion   = 1
	sessionRefChars = 16
	saltBytes       = 32
)

// buildMarker is the marker for one session reference.
func buildMarker(ref, kind string, at time.Time) string {
	return fmt.Sprintf("<!-- harness v=%d session=%s kind=%s ts=%s -->", markerVersion, ref, kind, at.UTC().Format(time.RFC3339))
}

// sessionRef is a keyed hash of the session id: stable for a session, and without the salt, which stays on this client,
// neither reversible nor comparable to anything.
func sessionRef(salt []byte, sessionID string) string {
	h := hmac.New(sha256.New, salt)
	h.Write([]byte(sessionID))
	return hex.EncodeToString(h.Sum(nil))[:sessionRefChars]
}

// loadMarkerSalt reads the client's salt, creating it on first use. Without a usable file the salt lives as long as the
// process: references change, nothing leaks. Sessions may race here: the first link wins and everyone reads that one.
func loadMarkerSalt(path string) []byte {
	random := func() []byte {
		b := make([]byte, saltBytes)
		_, _ = rand.Read(b)
		return b
	}
	if path == "" {
		return random()
	}
	read := func() []byte {
		if b, err := os.ReadFile(path); err == nil && len(b) >= saltBytes {
			return b
		}
		return nil
	}
	if b := read(); b != nil {
		return b
	}
	salt := random()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return salt
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".salt-*")
	if err != nil {
		return salt
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(salt)
	if cerr := tmp.Close(); err != nil || cerr != nil {
		return salt
	}
	_ = os.Link(tmp.Name(), path)
	if b := read(); b != nil {
		return b
	}
	return salt
}

// Tolerant on purpose: other versions and fields of other client versions still count. Malformed is what does not have
// this shape: not one line, a word that is not key=value, or a version that is not a number.
var (
	markerRe        = regexp.MustCompile(`<!--[ \t]*harness((?:[ \t]+[A-Za-z0-9_.-]+=[^\s<>]+?)+)[ \t]*-->`)
	markerVersionRe = regexp.MustCompile(`^[0-9]+$`)
)

// HasMarker reports whether a comment body holds a well-formed harness marker, of any session or version. Anyone can
// type one: it only means something on a comment from the logged-in account (see the monitor).
func HasMarker(body string) bool {
	for _, m := range markerRe.FindAllStringSubmatch(body, -1) {
		for _, field := range strings.Fields(m[1]) {
			if v, ok := strings.CutPrefix(field, "v="); ok && markerVersionRe.MatchString(v) {
				return true
			}
		}
	}
	return false
}
