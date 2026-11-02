package server_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/server"
)

const (
	testToken  = "0123456789abcdef0123456789abcdef"
	testOrigin = "http://localhost:3210"
)

func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:7801": true,
		"127.1.2.3:0":    true,
		"localhost:80":   true,
		"[::1]:7801":     true,
		"0.0.0.0:7801":   false,
		":7801":          false,
		"[::]:7801":      false,
		"192.168.1.5:80": false,
		"example.com:80": false,
	}
	for addr, want := range cases {
		got, err := server.IsLoopbackAddr(addr)
		if err != nil || got != want {
			t.Errorf("IsLoopbackAddr(%q) = %v, %v; want %v", addr, got, err, want)
		}
	}
	if _, err := server.IsLoopbackAddr("127.0.0.1"); err == nil {
		t.Error("an address without a port should fail")
	}
}

func TestServeTCPRefusesNonLoopbackAndWeakTokens(t *testing.T) {
	srv := server.New(bootstrap.Options{})
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listened := false
	onListen := func(net.Addr) { listened = true }

	err := srv.ServeTCP(ctx, server.TCPOptions{Addr: "0.0.0.0:0", Token: testToken, OnListen: onListen})
	if err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("wildcard bind: %v", err)
	}
	err = srv.ServeTCP(ctx, server.TCPOptions{Addr: ":0", Token: testToken, OnListen: onListen})
	if err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("empty host: %v", err)
	}
	err = srv.ServeTCP(ctx, server.TCPOptions{Addr: "127.0.0.1:0", Token: "short", OnListen: onListen})
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("short token: %v", err)
	}
	err = srv.ServeTCP(ctx, server.TCPOptions{Addr: "127.0.0.1:0", OnListen: onListen})
	if err == nil {
		t.Fatal("empty token accepted")
	}
	if listened {
		t.Fatal("listened despite a refused configuration")
	}
}

func TestServeTCPNonLoopbackWhenAllowed(t *testing.T) {
	srv := server.New(bootstrap.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- srv.ServeTCP(ctx, server.TCPOptions{
			Addr: "0.0.0.0:0", Token: testToken, AllowNonLoopback: true,
			OnListen: func(a net.Addr) { addrCh <- a },
		})
	}()
	select {
	case <-addrCh:
	case err := <-done:
		t.Fatalf("serve: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("did not listen")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// guarded starts an httptest server with TCPGuard around a stub handler.
func guarded(t *testing.T, opts server.TCPOptions) (*httptest.Server, *int) {
	hits := new(int)
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		w.Header().Set("Grpc-Status", "0")
		_, _ = io.WriteString(w, "ok")
	})
	ts := httptest.NewUnstartedServer(nil)
	opts.Addr = ts.Listener.Addr().String()
	if opts.Token == "" {
		opts.Token = testToken
	}
	if opts.AllowedOrigins == nil {
		opts.AllowedOrigins = server.DefaultCORSOrigins
	}
	ts.Config.Handler = server.TCPGuard(stub, opts)
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, hits
}

func do(t *testing.T, ts *httptest.Server, method string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+"/harness.agent.v1.AgentService/ListSessions", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res
}

func TestTCPGuardToken(t *testing.T) {
	ts, hits := guarded(t, server.TCPOptions{})
	for name, auth := range map[string]string{
		"missing":      "",
		"wrong":        "Bearer " + strings.Repeat("x", len(testToken)),
		"prefix":       "Bearer " + testToken[:len(testToken)-1],
		"longer":       "Bearer " + testToken + "0",
		"wrong scheme": "Basic " + testToken,
		"bare token":   testToken,
	} {
		hdr := map[string]string{}
		if auth != "" {
			hdr["Authorization"] = auth
		}
		res := do(t, ts, http.MethodPost, hdr)
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s token: status %d", name, res.StatusCode)
		}
		if !strings.HasPrefix(res.Header.Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("%s token: no WWW-Authenticate", name)
		}
	}
	if *hits != 0 {
		t.Fatalf("handler reached %d times without a valid token", *hits)
	}
	if res := do(t, ts, http.MethodPost, map[string]string{"Authorization": "Bearer " + testToken}); res.StatusCode != http.StatusOK {
		t.Fatalf("valid token: status %d", res.StatusCode)
	}
	if res := do(t, ts, http.MethodPost, map[string]string{"Authorization": "bearer " + testToken}); res.StatusCode != http.StatusOK {
		t.Fatalf("lower-case scheme: status %d", res.StatusCode)
	}
}

func TestTCPGuardOrigin(t *testing.T) {
	ts, hits := guarded(t, server.TCPOptions{})
	authz := "Bearer " + testToken
	for _, origin := range []string{"https://evil.example", "http://localhost:3211", "null", "http://localhost"} {
		res := do(t, ts, http.MethodPost, map[string]string{"Authorization": authz, "Origin": origin})
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("origin %q: status %d", origin, res.StatusCode)
		}
		if res.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("origin %q got CORS headers", origin)
		}
		// Preflights from foreign origins are refused too.
		pre := do(t, ts, http.MethodOptions, map[string]string{"Origin": origin, "Access-Control-Request-Method": "POST"})
		if pre.StatusCode != http.StatusForbidden || pre.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("preflight from %q: status %d", origin, pre.StatusCode)
		}
	}
	if *hits != 0 {
		t.Fatalf("handler reached from a foreign origin")
	}
	for _, origin := range server.DefaultCORSOrigins {
		res := do(t, ts, http.MethodPost, map[string]string{"Authorization": authz, "Origin": origin})
		if res.StatusCode != http.StatusOK || res.Header.Get("Access-Control-Allow-Origin") != origin {
			t.Errorf("allowed origin %q: status %d, ACAO %q", origin, res.StatusCode, res.Header.Get("Access-Control-Allow-Origin"))
		}
		if !strings.Contains(res.Header.Get("Access-Control-Expose-Headers"), "Grpc-Status") {
			t.Errorf("expose headers: %q", res.Header.Get("Access-Control-Expose-Headers"))
		}
	}
	// A custom allowlist replaces the default.
	custom, _ := guarded(t, server.TCPOptions{AllowedOrigins: []string{"tauri://localhost/"}})
	if res := do(t, custom, http.MethodPost, map[string]string{"Authorization": authz, "Origin": testOrigin}); res.StatusCode != http.StatusForbidden {
		t.Errorf("default origin with a custom list: %d", res.StatusCode)
	}
	if res := do(t, custom, http.MethodPost, map[string]string{"Authorization": authz, "Origin": "tauri://localhost"}); res.StatusCode != http.StatusOK {
		t.Errorf("custom origin: %d", res.StatusCode)
	}
}

func TestTCPGuardPreflight(t *testing.T) {
	ts, hits := guarded(t, server.TCPOptions{})
	res := do(t, ts, http.MethodOptions, map[string]string{
		"Origin":                         testOrigin,
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "authorization,content-type,connect-protocol-version,connect-timeout-ms",
	})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status %d", res.StatusCode)
	}
	h := res.Header
	if h.Get("Access-Control-Allow-Origin") != testOrigin {
		t.Errorf("ACAO %q", h.Get("Access-Control-Allow-Origin"))
	}
	if !strings.Contains(h.Get("Access-Control-Allow-Methods"), "POST") {
		t.Errorf("methods %q", h.Get("Access-Control-Allow-Methods"))
	}
	allow := strings.ToLower(h.Get("Access-Control-Allow-Headers"))
	for _, want := range []string{"authorization", "content-type", "connect-protocol-version", "connect-timeout-ms", "x-grpc-web"} {
		if !strings.Contains(allow, want) {
			t.Errorf("allowed headers miss %s: %q", want, allow)
		}
	}
	if h.Get("Access-Control-Max-Age") == "" {
		t.Error("no max-age")
	}
	if *hits != 0 {
		t.Error("preflight reached the handler")
	}
	// A 401 still carries CORS headers so the browser can read the error.
	res = do(t, ts, http.MethodPost, map[string]string{"Origin": testOrigin})
	if res.StatusCode != http.StatusUnauthorized || res.Header.Get("Access-Control-Allow-Origin") != testOrigin {
		t.Errorf("unauthenticated CORS request: %d %q", res.StatusCode, res.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestTCPGuardHostRebinding(t *testing.T) {
	ts, hits := guarded(t, server.TCPOptions{})
	_, port, _ := net.SplitHostPort(ts.Listener.Addr().String())
	authz := "Bearer " + testToken
	for _, host := range []string{"evil.example:" + port, "evil.example", "127.0.0.1.nip.io:" + port, "localhost.evil.example", "192.168.1.5:" + port} {
		res := do(t, ts, http.MethodPost, map[string]string{"Authorization": authz, "Host": host})
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("host %q: status %d", host, res.StatusCode)
		}
	}
	if *hits != 0 {
		t.Fatal("handler reached with a foreign Host")
	}
	for _, host := range []string{"127.0.0.1:" + port, "localhost:" + port, "[::1]:" + port, "app.localhost:" + port} {
		res := do(t, ts, http.MethodPost, map[string]string{"Authorization": authz, "Host": host})
		if res.StatusCode != http.StatusOK {
			t.Errorf("host %q: status %d", host, res.StatusCode)
		}
	}
}

func TestTCPGuardNonLoopbackHosts(t *testing.T) {
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	check := func(opts server.TCPOptions, host string) int {
		opts.Token = testToken
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Host = host
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		server.TCPGuard(stub, opts).ServeHTTP(rec, req)
		return rec.Code
	}
	specific := server.TCPOptions{Addr: "192.168.1.5:7801", AllowNonLoopback: true}
	if c := check(specific, "192.168.1.5:7801"); c != http.StatusOK {
		t.Errorf("bind host: %d", c)
	}
	if c := check(specific, "10.0.0.1:7801"); c != http.StatusForbidden {
		t.Errorf("other IP with a specific bind: %d", c)
	}
	wildcard := server.TCPOptions{Addr: "0.0.0.0:7801", AllowNonLoopback: true}
	if c := check(wildcard, "10.0.0.1:7801"); c != http.StatusOK {
		t.Errorf("IP literal with a wildcard bind: %d", c)
	}
	if c := check(wildcard, "evil.example:7801"); c != http.StatusForbidden {
		t.Errorf("DNS name with a wildcard bind: %d", c)
	}
}

// browserClient mimics connect-web: the Connect protocol with JSON over
// HTTP/1.1, an Origin header and the bearer token.
func browserClient(base string, protocols func(*http.Protocols)) agentv1connect.AgentServiceClient {
	p := new(http.Protocols)
	protocols(p)
	hc := &http.Client{Transport: &http.Transport{Protocols: p}}
	return agentv1connect.NewAgentServiceClient(hc, base, connect.WithProtoJSON(), connect.WithInterceptors(browserHeaders{}))
}

// browserHeaders adds the headers connect-web sends from the UI.
type browserHeaders struct{}

func (browserHeaders) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", "Bearer "+testToken)
		req.Header().Set("Origin", testOrigin)
		return next(ctx, req)
	}
}
func (browserHeaders) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", "Bearer "+testToken)
		conn.RequestHeader().Set("Origin", testOrigin)
		return conn
	}
}
func (browserHeaders) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func TestAgentServiceOverTCPConnectHTTP1(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	_, url := fg.Start(t, fg.Sequence(
		func(r *llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("m1", fg.Text("Hello from the agent."))}
		},
	))
	tok := auth.StaticToken("hat_test")
	srv := server.New(bootstrap.Options{Gateway: connectgw.New(url, nil, tok), Prompts: prompts.NewConnectSource(url, nil, tok)})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addrCh := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- srv.ServeTCP(ctx, server.TCPOptions{
			Addr: "127.0.0.1:0", Token: testToken, AllowedOrigins: server.DefaultCORSOrigins,
			OnListen: func(a net.Addr) { addrCh <- a },
		})
	}()
	var base string
	select {
	case a := <-addrCh:
		base = "http://" + a.String()
	case err := <-done:
		t.Fatalf("serve: %v", err)
	}

	// Without the token the real service answers UNAUTHENTICATED.
	anon := agentv1connect.NewAgentServiceClient(http.DefaultClient, base, connect.WithProtoJSON())
	if _, err := anon.ListSessions(ctx, connect.NewRequest(&agentv1.ListSessionsRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous call: %v", err)
	}

	// h2c works for non-browser clients.
	h2 := browserClient(base, func(p *http.Protocols) { p.SetUnencryptedHTTP2(true) })
	if _, err := h2.Initialize(ctx, connect.NewRequest(&agentv1.InitializeRequest{Frontend: "test"})); err != nil {
		t.Fatalf("h2c initialize: %v", err)
	}

	client := browserClient(base, func(p *http.Protocols) { p.SetHTTP1(true) })
	init, err := client.Initialize(ctx, connect.NewRequest(&agentv1.InitializeRequest{Frontend: "web"}))
	if err != nil || len(init.Msg.ModelIds) != 1 {
		t.Fatalf("initialize: %v %+v", err, init)
	}
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	cs, err := client.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: proj}))
	if err != nil {
		t.Fatal(err)
	}
	sid := cs.Msg.Session.Id
	stream, err := client.Subscribe(ctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendUserMessage(ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "u1", Content: text("hi")})); err != nil {
		t.Fatal(err)
	}
	var finished *agentv1.TurnFinished
	sawAssistant := false
	for finished == nil && stream.Receive() {
		switch e := stream.Msg().Event.Event.(type) {
		case *agentv1.SessionEvent_AssistantMessage:
			sawAssistant = true
		case *agentv1.SessionEvent_TurnFinished:
			finished = e.TurnFinished
		}
	}
	if finished == nil || finished.Result != agentv1.TurnFinished_RESULT_SUCCESS || !sawAssistant {
		t.Fatalf("turn finished: %+v assistant=%v (err %v)", finished, sawAssistant, stream.Err())
	}
	if ct := stream.ResponseHeader().Get("Content-Type"); ct != "application/connect+json" {
		t.Errorf("stream content type %q", ct)
	}
	if got := stream.ResponseHeader().Get("Access-Control-Allow-Origin"); got != testOrigin {
		t.Errorf("stream ACAO %q", got)
	}
	_ = stream.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWriteTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "home", "agent-token")
	tok, err := server.GenerateToken()
	if err != nil || len(tok) != 64 {
		t.Fatalf("token %q %v", tok, err)
	}
	if other, _ := server.GenerateToken(); other == tok {
		t.Fatal("tokens repeat")
	}
	if err := server.WriteTokenFile(path, tok); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", st.Mode(), err)
	}
	data, _ := os.ReadFile(path)
	if strings.TrimSpace(string(data)) != tok {
		t.Fatalf("content %q", data)
	}
}
