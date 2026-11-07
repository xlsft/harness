package sandbox

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xlsft/harness/packages/client/netguard"
)

// A sandboxed command has one way out: this proxy, on the loopback interface. It speaks HTTP: CONNECT for TLS, plain
// forwarding for http://. It sees a host name and a port and nothing else, so an allowed host is allowed for anything its
// TLS endpoint will do, domain fronting included. It answers a request for another host with a 403 at once and remembers
// the host for the command's result (Wrapped.Diagnose). A prompt instead would have to reach the user while the command
// waits on a socket, under its own timeouts and retries; a clear error is what works every time.

// DefaultHosts are always allowed, so package managers and fetching public code work out of the box: public registries and
// code hosts. Anything that can publish can carry data out for a command holding a token for it, and the sandbox keeps the
// user's tokens from it, but not a token the command brought along. Settings adds to this list.
var DefaultHosts = []string{
	"registry.npmjs.org", "registry.yarnpkg.com",
	"pypi.org", "files.pythonhosted.org",
	"proxy.golang.org", "sum.golang.org",
	"index.crates.io", "static.crates.io", "crates.io",
	"github.com", "codeload.github.com", "objects.githubusercontent.com", "raw.githubusercontent.com",
}

// Hosts is an allowlist: "example.com", "*.example.com" (any subdomain, not the name itself), either with ":port". Without
// a port an entry covers 80 and 443.
type Hosts struct{ entries []hostEntry }

type hostEntry struct {
	host string
	wild bool
	port int
}

// NewHosts parses entries, leaving out those ValidHost refuses.
func NewHosts(entries []string) Hosts {
	var h Hosts
	for _, e := range entries {
		if entry, ok := parseHost(e); ok {
			h.entries = append(h.entries, entry)
		}
	}
	return h
}

// ValidHost reports whether s can be an entry of the allowed hosts.
func ValidHost(s string) bool {
	_, ok := parseHost(s)
	return ok
}

func parseHost(s string) (hostEntry, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	var e hostEntry
	if rest, ok := strings.CutPrefix(s, "*."); ok {
		e.wild, s = true, rest
	}
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return e, false
		}
		s, e.port = h, n
	}
	s = strings.Trim(s, "[]")
	if s == "" || strings.ContainsAny(s, "/*@?# ") {
		return e, false
	}
	e.host = s
	return e, true
}

// allows reports whether the list allows host:port. private says an entry names this private address itself, which is the
// only way to reach one: a public name that resolves to a private address, or a wildcard, is refused when connecting.
func (h Hosts) allows(host string, port int) (ok, private bool) {
	host = strings.ToLower(strings.Trim(host, "[]"))
	for _, e := range h.entries {
		if e.port == 0 && port != 80 && port != 443 || e.port != 0 && e.port != port {
			continue
		}
		switch {
		case e.wild && strings.HasSuffix(host, "."+e.host):
			return true, false
		case !e.wild && host == e.host:
			return true, netguard.IsPrivateLiteral(host)
		}
	}
	return false, false
}

// blockedHost is a request the proxy refused.
type blockedHost struct {
	seq  uint64
	host string
}

// proxyRemember bounds the refusals kept per session.
const proxyRemember = 64

type proxy struct {
	ln    net.Listener
	srv   *http.Server
	hosts func() Hosts

	mu      sync.Mutex
	seq     uint64
	blocked []blockedHost
	// sockDir holds the unix socket, once a driver asked for one.
	sockDir string
}

func startProxy(hosts func() Hosts) (*proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &proxy{ln: ln, hosts: hosts}
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

func (p *proxy) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *proxy) close() {
	_ = p.srv.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sockDir != "" {
		_ = os.RemoveAll(p.sockDir)
	}
}

// unixSocket serves the proxy on a unix socket as well and returns its path: the way in for a command whose network namespace
// has nothing else. The folder is the user's alone. It is in the system's temp folder, since a socket path has a short limit.
func (p *proxy) unixSocket() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sockDir != "" {
		return filepath.Join(p.sockDir, "proxy.sock"), nil
	}
	dir, err := os.MkdirTemp("", "hsb")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "proxy.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	p.sockDir = dir
	go func() { _ = p.srv.Serve(ln) }()
	return path, nil
}

// mark returns the position to read refusals from.
func (p *proxy) mark() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seq
}

// blockedSince returns the hosts refused after mark, without repeats. The proxy serves all of the session's commands, so a
// command running at the same moment may show up in another's list.
func (p *proxy) blockedSince(mark uint64) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, b := range p.blocked {
		if b.seq > mark && !slices.Contains(out, b.host) {
			out = append(out, b.host)
		}
	}
	return out
}

func (p *proxy) note(host string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	p.blocked = append(p.blocked, blockedHost{p.seq, host})
	if len(p.blocked) > proxyRemember {
		p.blocked = p.blocked[len(p.blocked)-proxyRemember:]
	}
}

// dialer refuses a private address at the moment of connecting, when a name that passed the list can no longer change what
// it resolves to, unless the list names that address.
func dialer(allowPrivate bool) *net.Dialer {
	d := &net.Dialer{Timeout: 30 * time.Second}
	if !allowPrivate {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			h, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if a, err := netip.ParseAddr(h); err == nil && netguard.IsPrivateAddr(a) {
				return fmt.Errorf("%s is a private address", h)
			}
			return nil
		}
	}
	return d
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	port := 80
	if r.Method == http.MethodConnect {
		port = 443
	}
	host := r.Host
	if h, ps, err := net.SplitHostPort(host); err == nil {
		host = h
		if n, err := strconv.Atoi(ps); err == nil {
			port = n
		}
	}
	host = strings.Trim(host, "[]")
	ok, private := p.hosts().allows(host, port)
	if !ok {
		label := host
		if port != 80 && port != 443 {
			label = net.JoinHostPort(host, strconv.Itoa(port))
		}
		p.note(label)
		http.Error(w, fmt.Sprintf("harness sandbox: %s is not on the allowed hosts", label), http.StatusForbidden)
		return
	}
	if r.Method == http.MethodConnect {
		p.tunnel(w, r, net.JoinHostPort(host, strconv.Itoa(port)), private)
		return
	}
	p.forward(w, r, private)
}

func (p *proxy) tunnel(w http.ResponseWriter, r *http.Request, addr string, private bool) {
	up, err := dialer(private).DialContext(r.Context(), "tcp", addr)
	if err != nil {
		http.Error(w, "harness sandbox: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer up.Close()
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "harness sandbox: cannot tunnel", http.StatusInternalServerError)
		return
	}
	client, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	copyBoth(client, rw.Reader, up)
}

// copyBoth pumps both directions until both have ended.
func copyBoth(client net.Conn, buffered *bufio.Reader, up net.Conn) {
	done := make(chan struct{}, 2)
	half := func(c net.Conn) {
		if t, ok := c.(interface{ CloseWrite() error }); ok {
			_ = t.CloseWrite()
		}
	}
	go func() {
		_, _ = io.Copy(up, buffered)
		half(up)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, up)
		half(client)
		done <- struct{}{}
	}()
	<-done
	<-done
}

// hopHeaders are for this connection only.
var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func (p *proxy) forward(w http.ResponseWriter, r *http.Request, private bool) {
	if !r.URL.IsAbs() {
		http.Error(w, "harness sandbox: this is a proxy; send an absolute URL", http.StatusBadRequest)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range hopHeaders {
		out.Header.Del(h)
	}
	t := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer(private).DialContext(ctx, network, addr)
	}}
	defer t.CloseIdleConnections()
	res, err := t.RoundTrip(out)
	if err != nil {
		http.Error(w, "harness sandbox: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	for k, vs := range res.Header {
		if !slices.Contains(hopHeaders, k) {
			w.Header()[k] = vs
		}
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}
