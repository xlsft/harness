package netguard

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
)

func TestIsPrivateLiteral(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "LOCALHOST.": true, "app.localhost": true, "127.0.0.1": true, "127.1.2.3": true,
		"10.1.2.3": true, "172.16.0.1": true, "192.168.1.10": true, "169.254.169.254": true, "100.64.0.1": true,
		"0.0.0.0": true, "[::1]": true, "::1": true, "fd00:ec2::254": true, "fe80::1": true, "::ffff:127.0.0.1": true,
		"metadata.google.internal": true, "example.com": false, "8.8.8.8": false, "2606:4700::1111": false, "172.32.0.1": false, "": false,
	} {
		if got := IsPrivateLiteral(host); got != want {
			t.Errorf("IsPrivateLiteral(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestIsPrivateResolvesAndMemoizes(t *testing.T) {
	var calls atomic.Int32
	answers := map[string][]netip.Addr{
		"intranet.corp": {netip.MustParseAddr("10.0.0.5")},
		"mixed.test":    {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")},
		"public.test":   {netip.MustParseAddr("93.184.216.34")},
	}
	g := &Guard{Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
		calls.Add(1)
		if a, ok := answers[host]; ok {
			return a, nil
		}
		return nil, errors.New("no such host")
	}}
	ctx := context.Background()
	if !g.IsPrivate(ctx, "intranet.corp") || !g.IsPrivate(ctx, "mixed.test") {
		t.Fatal("names resolving to private addresses must be private")
	}
	if g.IsPrivate(ctx, "public.test") || g.IsPrivate(ctx, "unknown.test") {
		t.Fatal("public and unresolvable names must not be private")
	}
	if !g.IsPrivate(ctx, "localhost") || g.IsPrivate(ctx, "1.1.1.1") {
		t.Fatal("literals are classified without lookups")
	}
	n := calls.Load()
	// The answer is reused even if DNS changes (rebinding): the fetcher's dial
	// guard then refuses the private address.
	answers["public.test"] = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	if g.IsPrivate(ctx, "public.test") || calls.Load() != n {
		t.Fatal("lookup results must be memoized")
	}
	g.Forget()
	if !g.IsPrivate(ctx, "public.test") {
		t.Fatal("Forget must drop the memo")
	}
}
