// Package netguard classifies network targets as private (loopback, RFC 1918,
// link-local, unique-local, carrier-grade NAT, cloud metadata) so that the
// permission engine and the WebFetch tool agree on which URLs need an explicit
// user approval.
//
// A host is private when it is a private IP literal, a well-known local name
// (localhost, *.localhost, cloud metadata names), or a name that resolves to
// at least one private address. Name lookups are memoized for a short time so
// the permission check and the fetch that follows it see the same answer: a
// name that turns private between the two (DNS rebinding) is then refused by
// the fetcher's dial guard instead of being fetched without approval.
package netguard

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// LookupFunc resolves a host name to addresses.
type LookupFunc func(ctx context.Context, host string) ([]netip.Addr, error)

// Guard classifies hosts. The zero value uses the system resolver.
type Guard struct {
	// Lookup overrides name resolution (tests).
	Lookup LookupFunc
	// Timeout bounds one lookup (default 2 s).
	Timeout time.Duration
	// TTL is how long a lookup result is reused (default 2 min).
	TTL time.Duration

	mu   sync.Mutex
	memo map[string]memoEntry
}

type memoEntry struct {
	private bool
	at      time.Time
}

// Default is the process-wide guard shared by the permission engine and the
// WebFetch tool.
var Default = &Guard{}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")
var thisNet = netip.MustParsePrefix("0.0.0.0/8")

// metadataNames are cloud instance-metadata host names.
var metadataNames = map[string]bool{
	"metadata.google.internal": true, "metadata.goog": true, "metadata": true,
	"instance-data": true, "instance-data.ec2.internal": true,
}

// IsPrivateAddr reports whether a is not a public unicast address.
func IsPrivateAddr(a netip.Addr) bool {
	a = a.Unmap()
	return !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsUnspecified() || a.IsMulticast() ||
		(a.Is4() && (cgnat.Contains(a) || thisNet.Contains(a)))
}

// IsPrivateIP is IsPrivateAddr for a net.IP.
func IsPrivateIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	return !ok || IsPrivateAddr(a)
}

// normalize lowercases host and strips brackets, a trailing dot and a zone.
func normalize(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")
	return strings.TrimSuffix(h, ".")
}

// IsPrivateLiteral reports whether host is private without resolving it: an
// IP literal in a private range, localhost (and *.localhost) or a cloud
// metadata name.
func IsPrivateLiteral(host string) bool {
	h := normalize(host)
	if h == "" {
		return false
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return IsPrivateAddr(a)
	}
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || metadataNames[h]
}

// IsPrivate reports whether host is private: a private literal, or a name
// resolving to at least one private address. A failed lookup counts as public
// (the fetcher's dial guard still refuses private addresses then).
func (g *Guard) IsPrivate(ctx context.Context, host string) bool {
	h := normalize(host)
	if IsPrivateLiteral(h) {
		return true
	}
	if _, err := netip.ParseAddr(h); err == nil || h == "" {
		return false
	}
	ttl := g.TTL
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	g.mu.Lock()
	if e, ok := g.memo[h]; ok && time.Since(e.at) < ttl {
		g.mu.Unlock()
		return e.private
	}
	g.mu.Unlock()

	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	lctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addrs, err := g.Resolve(lctx, h)
	if err != nil {
		return false
	}
	private := false
	for _, a := range addrs {
		if IsPrivateAddr(a) {
			private = true
			break
		}
	}
	g.mu.Lock()
	if g.memo == nil {
		g.memo = map[string]memoEntry{}
	}
	if len(g.memo) > 1024 {
		g.memo = map[string]memoEntry{}
	}
	g.memo[h] = memoEntry{private: private, at: time.Now()}
	g.mu.Unlock()
	return private
}

// Resolve looks host up with Lookup, or the system resolver (no memo).
func (g *Guard) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if g.Lookup != nil {
		return g.Lookup(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// Forget drops memoized lookups (tests).
func (g *Guard) Forget() {
	g.mu.Lock()
	g.memo = nil
	g.mu.Unlock()
}
