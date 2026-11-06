package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/rpc"
)

// ObjectProxyPath is where frontends read the object store: GET /objects/<sha256>?ticket=<ticket>.
const ObjectProxyPath = "/objects/"

// objectTicketTTL is how long a read ticket lasts; a frontend asks for a new one well before that.
const objectTicketTTL = 12 * time.Hour

var objectPathRe = regexp.MustCompile(`^/objects/[0-9a-f]{64}$`)

// objectResponseHeaders are the only headers of core's answer that reach the frontend.
var objectResponseHeaders = []string{
	"Content-Type", "Content-Length", "Content-Range", "Content-Disposition", "Accept-Ranges", "Etag", "Cache-Control",
	"X-Content-Type-Options", "Content-Security-Policy", "Referrer-Policy", "Cross-Origin-Resource-Policy",
}

// ObjectProxy serves the object store's bytes to frontends: core's read route, with this client's credentials. It is
// how an <img> or a <video> reads an object on any device, since a tag cannot send the agent token and frontends never
// hold server credentials. Tickets (AgentService.CreateObjectTicket) are signed with a key that lives as long as this
// process, last a few hours, and allow this one route, for the user this client is signed in as.
type ObjectProxy struct {
	url    string
	http   *http.Client
	tokens rpc.Tokens
	key    [32]byte
	now    func() time.Time
}

// NewObjectProxy creates the proxy for a server.
func NewObjectProxy(url string, hc *http.Client, tokens rpc.Tokens) *ObjectProxy {
	p := &ObjectProxy{url: strings.TrimRight(url, "/"), http: hc, tokens: tokens, now: time.Now}
	if _, err := rand.Read(p.key[:]); err != nil {
		panic(err)
	}
	return p
}

func (p *ObjectProxy) sign(exp string) string {
	m := hmac.New(sha256.New, p.key[:])
	m.Write([]byte("object-read:" + exp))
	return hex.EncodeToString(m.Sum(nil))
}

func (p *ObjectProxy) signArtifact(id, exp string) string {
	m := hmac.New(sha256.New, p.key[:])
	m.Write([]byte("artifact-read:" + id + ":" + exp))
	return hex.EncodeToString(m.Sum(nil))
}

func hmacEqual(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }

// Ticket issues a read ticket.
func (p *ObjectProxy) Ticket() (string, time.Time) {
	at := p.now().Add(objectTicketTTL)
	exp := strconv.FormatInt(at.Unix(), 10)
	return exp + "." + p.sign(exp), at
}

func (p *ObjectProxy) validTicket(t string) bool {
	exp, sig, ok := strings.Cut(t, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(p.sign(exp))) {
		return false
	}
	at, err := strconv.ParseInt(exp, 10, 64)
	return err == nil && p.now().Unix() < at
}

func (p *ObjectProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !p.validTicket(r.URL.Query().Get("ticket")) {
		http.Error(w, "missing or expired ticket", http.StatusUnauthorized)
		return
	}
	if !objectPathRe.MatchString(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	send := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, r.Method, p.url+"/api/objects/"+strings.TrimPrefix(r.URL.Path, ObjectProxyPath), nil)
		if err != nil {
			return nil, err
		}
		for _, h := range []string{"Range", "If-None-Match"} {
			if v := r.Header.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
		req.Header.Set("User-Agent", config.UserAgent())
		rpc.SetBearer(req.Header, token)
		return p.http.Do(req)
	}
	token, err := p.tokens.Token(ctx)
	if err != nil {
		http.Error(w, "not signed in to the harness server", http.StatusUnauthorized)
		return
	}
	res, err := send(token)
	if err == nil && res.StatusCode == http.StatusUnauthorized {
		res.Body.Close()
		if token, err = p.tokens.Refresh(ctx); err == nil {
			res, err = send(token)
		}
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			http.Error(w, "harness server is unreachable", http.StatusBadGateway)
		}
		return
	}
	defer res.Body.Close()
	for _, h := range objectResponseHeaders {
		if v := res.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	// Whatever answered, nothing from here renders on this origin as anything but what it says it is.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if w.Header().Get("Content-Security-Policy") == "" {
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}

// CreateObjectTicket implements AgentService.CreateObjectTicket.
func (s *Server) CreateObjectTicket(_ context.Context, _ *connect.Request[agentv1.CreateObjectTicketRequest]) (*connect.Response[agentv1.CreateObjectTicketResponse], error) {
	if s.Objects == nil {
		return connect.NewResponse(&agentv1.CreateObjectTicketResponse{}), nil
	}
	t, at := s.Objects.Ticket()
	return connect.NewResponse(&agentv1.CreateObjectTicketResponse{Available: true, Ticket: t, ExpiresAt: at.UTC().Format(time.RFC3339)}), nil
}
