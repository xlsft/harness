package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// DefaultCORSOrigins are the browser origins allowed by default on the TCP
// listener: the UI dev server (packages/ui, `pnpm dev --port 3210`).
var DefaultCORSOrigins = []string{"http://localhost:3210", "http://127.0.0.1:3210"}

// MinTokenLength is the shortest bearer token accepted for the TCP listener.
const MinTokenLength = 16

// TCPOptions configures the TCP listener for browser frontends.
type TCPOptions struct {
	// Addr is the host:port to bind, e.g. "127.0.0.1:7801". Port 0 picks a
	// free port.
	Addr string
	// Token is the bearer token every request must carry
	// (Authorization: Bearer <token>). Required.
	Token string
	// AllowedOrigins are the exact browser origins (scheme://host[:port])
	// that may call the service. Requests with any other Origin header are
	// rejected. Requests without an Origin header (non-browser clients) are
	// allowed and still need the token.
	AllowedOrigins []string
	// AllowNonLoopback permits binding to a non-loopback address. Off by
	// default: the listener is meant for frontends on the same machine.
	AllowNonLoopback bool
	// OnListen, if set, is called with the bound address once listening.
	OnListen func(addr net.Addr)
}

// IsLoopbackAddr reports whether host:port binds only to loopback
// ("localhost", 127.0.0.0/8 or ::1). An empty host (all interfaces) is not
// loopback.
func IsLoopbackAddr(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, err
	}
	return isLoopbackHost(host), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && ip.Unmap().IsLoopback()
}

// GenerateToken returns a random 256-bit token, hex-encoded.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// WriteTokenFile writes token to path with mode 0600 (atomically, via a temp
// file in the same directory created with 0600 from the start).
func WriteTokenFile(path, token string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".agent-token-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ServeTCP serves AgentService on a TCP address for browser frontends
// (connect-web): the Connect protocol over HTTP/1.1 and h2c, with server
// streaming. Every request is checked by TCPGuard (Host, Origin/CORS and the
// bearer token).
func (s *Server) ServeTCP(ctx context.Context, opts TCPOptions) error {
	if len(opts.Token) < MinTokenLength {
		return fmt.Errorf("the TCP listener needs a bearer token of at least %d characters", MinTokenLength)
	}
	loopback, err := IsLoopbackAddr(opts.Addr)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", opts.Addr, err)
	}
	if !loopback && !opts.AllowNonLoopback {
		return fmt.Errorf("refusing to listen on non-loopback address %q (use 127.0.0.1:<port>, or pass the explicit non-loopback flag)", opts.Addr)
	}
	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return err
	}
	if opts.OnListen != nil {
		opts.OnListen(ln.Addr())
	}
	return s.serveHandler(ctx, ln, TCPGuard(s.mux(), opts))
}

// Headers browsers may send and read, for the Connect, gRPC-Web and gRPC
// protocols (the lists of connectrpc.com/cors plus Authorization).
var (
	corsAllowHeaders = strings.Join([]string{
		"Authorization", "Content-Type", "Content-Encoding", "Accept-Encoding",
		"Connect-Protocol-Version", "Connect-Timeout-Ms", "Connect-Accept-Encoding", "Connect-Content-Encoding",
		"Grpc-Timeout", "Grpc-Accept-Encoding", "Grpc-Encoding", "X-Grpc-Web", "X-User-Agent",
	}, ", ")
	corsExposeHeaders = strings.Join([]string{
		"Content-Encoding", "Connect-Content-Encoding", "Connect-Accept-Encoding",
		"Grpc-Status", "Grpc-Message", "Grpc-Status-Details-Bin", "Grpc-Encoding", "Grpc-Accept-Encoding",
	}, ", ")
)

// TCPGuard wraps next with the TCP listener's access checks, in order:
//
//  1. Host must be loopback ("localhost", *.localhost, a loopback IP) or the
//     bind host; with a non-loopback wildcard bind any IP literal is accepted.
//     This defeats DNS rebinding, where a page on an attacker's domain
//     resolves that name to 127.0.0.1: its requests carry the attacker's
//     Host.
//  2. A request with an Origin header must come from AllowedOrigins (403
//     otherwise). Allowed origins get CORS headers, and preflights (OPTIONS
//     with Access-Control-Request-Method) are answered here without a token,
//     since browsers never send credentials on a preflight.
//  3. Authorization: Bearer <token>, compared in constant time (401 with a
//     Connect "unauthenticated" error otherwise).
func TCPGuard(next http.Handler, opts TCPOptions) http.Handler {
	origins := map[string]bool{}
	for _, o := range opts.AllowedOrigins {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			origins[strings.ToLower(o)] = true
		}
	}
	bindHost, _, _ := net.SplitHostPort(opts.Addr)
	bindHost = strings.ToLower(strings.Trim(bindHost, "[]"))
	wildcard := bindHost == "" || bindHost == "0.0.0.0" || bindHost == "::"
	want := sha256.Sum256([]byte(opts.Token))

	hostAllowed := func(hostport string) bool {
		host := hostport
		if h, _, err := net.SplitHostPort(hostport); err == nil {
			host = h
		}
		host = strings.ToLower(strings.Trim(host, "[]"))
		if host == "" {
			return false
		}
		if isLoopbackHost(host) || strings.HasSuffix(host, ".localhost") {
			return true
		}
		if !wildcard && host == bindHost {
			return true
		}
		if wildcard && opts.AllowNonLoopback {
			_, err := netip.ParseAddr(host)
			return err == nil
		}
		return false
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host) {
			http.Error(w, "forbidden: Host is not allowed", http.StatusForbidden)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !origins[strings.ToLower(origin)] {
				http.Error(w, "forbidden: Origin is not allowed", http.StatusForbidden)
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
				h.Set("Access-Control-Allow-Methods", "GET, POST")
				h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
				h.Set("Access-Control-Max-Age", "7200")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if !tokenMatches(r.Header.Get("Authorization"), want) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="harness-client"`)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthenticated","message":"missing or invalid bearer token"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokenMatches compares the bearer token with the expected one in constant
// time. Both sides are hashed first so the comparison does not leak the
// token length either.
func tokenMatches(header string, want [32]byte) bool {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	got := sha256.Sum256([]byte(strings.TrimSpace(header[len(prefix):])))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}
