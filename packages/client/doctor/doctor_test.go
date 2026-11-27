package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/auth"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/sandbox"
)

func fakeServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": version})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// healthy is a machine where everything works.
func healthy(server string) Options {
	return Options{
		ClientVersion: "0.3.1",
		Server:        server,
		Credentials:   func() (string, error) { return auth.InKeychain, nil },
		Me:            func(context.Context) (string, error) { return "ada@example.com", nil },
		Policy:        func(context.Context) (permissions.ManagedPolicy, bool) { return permissions.ManagedPolicy{}, true },
		Sandbox:       func() sandbox.Info { return sandbox.Info{Platform: "macos", Supported: true} },
		MCP: func(context.Context) ([]MCPResult, error) {
			return []MCPResult{{Name: "github", OK: true}}, nil
		},
		DataDir:  "/",
		DiskFree: func(string) (uint64, error) { return 50 << 30, nil },
		LookPath: func(string) (string, error) { return "/opt/homebrew/bin/git", nil },
		Run:      func(context.Context, string, ...string) (string, error) { return "git version 2.50.1", nil },
		Getenv:   func(string) string { return "" },
		GOOS:     "darwin",
	}
}

func byID(r Report, id string) Check {
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	return Check{}
}

func TestHealthy(t *testing.T) {
	srv := fakeServer(t, "0.3.0")
	r := Run(context.Background(), healthy(srv.URL))
	if r.Status != Pass {
		var b bytes.Buffer
		r.Text(&b)
		t.Fatalf("status %s:\n%s", r.Status, b.String())
	}
	if len(r.Checks) != len(Order) {
		t.Fatalf("%d checks, want %d", len(r.Checks), len(Order))
	}
	for i, c := range r.Checks {
		if c.ID != Order[i] {
			t.Fatalf("check %d is %s, want %s", i, c.ID, Order[i])
		}
	}
	if !strings.Contains(byID(r, "server").Detail, "version 0.3.0") || !strings.Contains(byID(r, "git").Detail, "2.50.1") {
		t.Fatalf("details: %+v", r.Checks)
	}
	var b bytes.Buffer
	r.Text(&b)
	if !strings.Contains(b.String(), "pass  Sign-in") || !strings.Contains(b.String(), "Everything checked works.") {
		t.Fatal(b.String())
	}
	p := r.Proto()
	if p.GetStatus() != agentv1.DoctorStatus_DOCTOR_STATUS_PASS || len(p.GetChecks()) != len(Order) {
		t.Fatalf("proto: %v", p)
	}
}

func TestNotSignedIn(t *testing.T) {
	o := healthy("")
	o.Credentials = func() (string, error) { return auth.Nowhere, nil }
	r := Run(context.Background(), o)
	if byID(r, "signin").Status != Fail || byID(r, "signin").Hint == "" || r.Status != Fail {
		t.Fatalf("%+v", byID(r, "signin"))
	}
	for _, id := range []string{"server", "version", "keychain"} {
		if byID(r, id).Status != Skip {
			t.Errorf("%s: %+v", id, byID(r, id))
		}
	}
}

func TestRevokedAndUnreachable(t *testing.T) {
	o := healthy("http://127.0.0.1:1")
	o.Me = func(context.Context) (string, error) { return "", auth.ErrDeviceRevoked }
	o.HTTP = &http.Client{Timeout: 2 * time.Second}
	r := Run(context.Background(), o)
	if byID(r, "signin").Status != Fail {
		t.Errorf("revoked: %+v", byID(r, "signin"))
	}
	if byID(r, "server").Status != Fail || !strings.Contains(byID(r, "server").Detail, "can't be reached") {
		t.Errorf("unreachable: %+v", byID(r, "server"))
	}
	o.Me = func(context.Context) (string, error) { return "", errors.New("connection refused") }
	if c := byID(Run(context.Background(), o), "signin"); c.Status != Warn {
		t.Errorf("a network error isn't a bad sign-in: %+v", c)
	}
}

func TestVersionSkew(t *testing.T) {
	cases := []struct {
		client, server string
		want           Status
	}{
		{"0.3.1", "0.3.0", Pass},
		{"0.2.9", "0.3.0", Pass}, // one minor behind keeps working
		{"0.1.0", "0.3.0", Warn},
		{"0.4.0", "0.3.0", Warn},
		{"1.0.0", "0.9.0", Warn},
		{"0.0.0-dev", "0.3.0", Pass},
		{"0.3.0", "", Skip},
	}
	for _, c := range cases {
		o := healthy("x")
		o.ClientVersion = c.client
		if got := checkVersion(o, c.server); got.Status != c.want {
			t.Errorf("client %s server %s: %s (%s)", c.client, c.server, got.Status, got.Detail)
		}
	}
}

func TestVersionPolicy(t *testing.T) {
	o := healthy("x")
	cases := []struct {
		min, max, current string
		want              Status
	}{
		{"", "", "0.3.0", Pass},
		{"0.2.0", "0.4.0", "0.3.0", Pass},
		{"0.4.0", "", "0.3.0", Fail},
		{"", "0.2.0", "0.3.0", Warn},
	}
	for _, c := range cases {
		o.ClientVersion = c.current
		got := checkVersionPolicy(o, permissions.ManagedPolicy{MinClientVersion: c.min, MaxClientVersion: c.max}, true)
		if got.Status != c.want {
			t.Errorf("%+v: %s %s", c, got.Status, got.Detail)
		}
	}
	if got := checkVersionPolicy(o, permissions.ManagedPolicy{MinClientVersion: "0.1.0"}, false); !strings.Contains(got.Detail, "last known") {
		t.Errorf("a cached policy says so: %s", got.Detail)
	}
}

func TestSandboxRequiredButMissing(t *testing.T) {
	o := healthy("x")
	o.Sandbox = func() sandbox.Info { return sandbox.Info{Platform: "windows", Reason: "not yet"} }
	if c := checkSandbox(o, permissions.ManagedPolicy{}); c.Status != Warn {
		t.Errorf("missing: %+v", c)
	}
	if c := checkSandbox(o, permissions.ManagedPolicy{SandboxRequired: true}); c.Status != Fail {
		t.Errorf("missing and required: %+v", c)
	}
}

func TestKeychain(t *testing.T) {
	o := healthy("x")
	o.Credentials = func() (string, error) { return auth.InFile, errors.New("locked") }
	if c := checkKeychain(o); c.Status != Warn || !strings.Contains(c.Detail, "locked") {
		t.Errorf("%+v", c)
	}
	o.Credentials = func() (string, error) { return auth.InFile, nil }
	if c := checkKeychain(o); c.Status != Pass {
		t.Errorf("%+v", c)
	}
}

func TestDisk(t *testing.T) {
	o := healthy("x")
	o.DataDir = t.TempDir() + "/not/yet/created"
	for free, want := range map[uint64]Status{100 << 20: Fail, 1 << 30: Warn, 10 << 30: Pass} {
		o.DiskFree = func(string) (uint64, error) { return free, nil }
		if c := checkDisk(o); c.Status != want {
			t.Errorf("%d free: %+v", free, c)
		}
	}
	if free, err := diskFree(t.TempDir()); err != nil || free == 0 {
		t.Errorf("the real disk: %d, %v", free, err)
	}
}

func TestGit(t *testing.T) {
	o := healthy("x")
	o.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	if c := checkGit(context.Background(), o); c.Status != Warn || c.Hint != "xcode-select --install" {
		t.Errorf("missing: %+v", c)
	}
	o.LookPath = func(string) (string, error) { return "/usr/bin/git", nil }
	var ran []string
	o.Run = func(_ context.Context, name string, _ ...string) (string, error) {
		ran = append(ran, name)
		return "", errors.New("no developer tools")
	}
	if c := checkGit(context.Background(), o); c.Status != Warn || len(ran) != 1 || ran[0] != "/usr/bin/xcode-select" {
		t.Errorf("the stub git must not run: %+v %v", c, ran)
	}
}

func TestProxy(t *testing.T) {
	o := healthy("https://harness.example.com")
	env := map[string]string{}
	o.Getenv = func(k string) string { return env[k] }
	if c := checkProxy(o); c.Status != Pass || !strings.Contains(c.Detail, "direct") {
		t.Errorf("none: %+v", c)
	}
	env["https_proxy"] = "http://user:secret@proxy.corp:3128"
	c := checkProxy(o)
	if c.Status != Pass || strings.Contains(c.Detail, "secret") || !strings.Contains(c.Detail, "through http://user:xxxxx@proxy.corp:3128") {
		t.Errorf("set: %+v", c)
	}
	env["NO_PROXY"] = "example.com"
	if c := checkProxy(o); !strings.Contains(c.Detail, "reached directly") {
		t.Errorf("no_proxy: %+v", c)
	}
	env = map[string]string{"ALL_PROXY": "socks5://proxy:1080"}
	if c := checkProxy(o); c.Status != Warn {
		t.Errorf("ALL_PROXY only: %+v", c)
	}
}

type fakeInventory struct {
	entries []*agentv1.McpServerEntry
	errs    map[string]string
}

func (f fakeInventory) List(context.Context, string) *agentv1.ListMcpServersResponse {
	return &agentv1.ListMcpServersResponse{Servers: f.entries}
}

func (f fakeInventory) Inspect(_ context.Context, id, _ string) (*agentv1.InspectMcpServerResponse, error) {
	if e, ok := f.errs[id]; ok {
		return &agentv1.InspectMcpServerResponse{Error: e}, nil
	}
	return &agentv1.InspectMcpServerResponse{Connected: true}, nil
}

func entry(id string, st agentv1.McpServerState) *agentv1.McpServerEntry {
	return &agentv1.McpServerEntry{Server: &mcpv1.Server{Id: id, Name: id}, State: st}
}

func TestMCP(t *testing.T) {
	inv := fakeInventory{
		entries: []*agentv1.McpServerEntry{
			entry("ok", agentv1.McpServerState_MCP_SERVER_STATE_READY),
			entry("broken", agentv1.McpServerState_MCP_SERVER_STATE_READY),
			entry("pending", agentv1.McpServerState_MCP_SERVER_STATE_NEEDS_APPROVAL),
			entry("off", agentv1.McpServerState_MCP_SERVER_STATE_DISABLED),
		},
		errs: map[string]string{"broken": "exec: npx: not found"},
	}
	results, err := ProbeMCP(context.Background(), inv, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	o := healthy("x")
	o.MCP = func(context.Context) ([]MCPResult, error) { return results, nil }
	c := checkMCP(context.Background(), o)
	if c.Status != Warn || !strings.Contains(c.Detail, "broken: exec: npx: not found") || strings.Contains(c.Detail, "pending") {
		t.Fatalf("%+v", c)
	}
	o.MCP = nil
	if c := checkMCP(context.Background(), o); c.Status != Skip {
		t.Fatalf("not asked: %+v", c)
	}
}
