package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func parseTCP(t *testing.T, args ...string) *tcpFlags {
	t.Helper()
	fs := pflag.NewFlagSet("serve", pflag.ContinueOnError)
	tcp := addTCPFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return tcp
}

func TestServeTCPFlags(t *testing.T) {
	t.Setenv("HARNESS_AGENT_LISTEN", "")
	t.Setenv("HARNESS_AGENT_TOKEN", "")
	t.Setenv("HARNESS_AGENT_CORS_ORIGINS", "")
	cases := []struct {
		socket string
		args   []string
		err    string
	}{
		{"", nil, "nothing to serve on"},
		{"/tmp/a.sock", nil, ""},
		{"", []string{"--listen", "127.0.0.1:0"}, ""},
		{"", []string{"--listen", "[::1]:7801"}, ""},
		{"", []string{"--listen", "0.0.0.0:7801"}, "--allow-non-loopback"},
		{"", []string{"--listen", ":7801"}, "--allow-non-loopback"},
		{"", []string{"--listen", "0.0.0.0:7801", "--allow-non-loopback"}, ""},
		{"", []string{"--listen", "127.0.0.1"}, "missing port"},
		{"", []string{"--listen", "127.0.0.1:0", "--token", "short"}, "at least"},
		{"", []string{"--listen", "127.0.0.1:0", "--cors-origin", "vscode-webview://*"}, ""},
		{"", []string{"--listen", "127.0.0.1:0", "--cors-origin", "https://*"}, "any web page"},
		{"", []string{"--listen", "127.0.0.1:0", "--cors-origin", "http://*"}, "any web page"},
	}
	for _, c := range cases {
		err := parseTCP(t, c.args...).validate(c.socket)
		if c.err == "" && err != nil || c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%q %v: got %v, want %q", c.socket, c.args, err, c.err)
		}
	}
}

func TestServeTCPEnv(t *testing.T) {
	t.Setenv("HARNESS_AGENT_LISTEN", "127.0.0.1:7801")
	t.Setenv("HARNESS_AGENT_TOKEN", "env-token-0123456789")
	t.Setenv("HARNESS_AGENT_CORS_ORIGINS", "http://a.localhost:1, tauri://localhost")
	tcp := parseTCP(t)
	if *tcp.listen != "127.0.0.1:7801" || tcp.resolvedToken() != "env-token-0123456789" {
		t.Fatalf("env: %q %q", *tcp.listen, tcp.resolvedToken())
	}
	if o := tcp.origins(); len(o) != 2 || o[1] != "tauri://localhost" {
		t.Fatalf("origins %v", o)
	}
	tcp = parseTCP(t, "--token", "flag-token-0123456789", "--cors-origin", "http://x.localhost:2")
	if tcp.resolvedToken() != "flag-token-0123456789" || len(tcp.origins()) != 1 {
		t.Fatalf("flags override env: %q %v", tcp.resolvedToken(), tcp.origins())
	}
	t.Setenv("HARNESS_AGENT_CORS_ORIGINS", "")
	if o := parseTCP(t).origins(); len(o) != 3 || o[0] != "http://localhost:3210" {
		t.Fatalf("default origins %v", o)
	}
}

// A client with a generated token says where it listens, so that an editor can find it; one given a token belongs to
// whoever started it and stays quiet. The address goes when the client does.
func TestServeTCPAnnouncesItsEndpoint(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_AGENT_LISTEN", "")
	t.Setenv("HARNESS_AGENT_TOKEN", "")
	t.Setenv("HARNESS_AGENT_CORS_ORIGINS", "")
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7801}

	opts, cleanup, err := parseTCP(t, "--listen", "127.0.0.1:0").options()
	if err != nil {
		t.Fatal(err)
	}
	opts.OnListen(addr)
	data, err := os.ReadFile(endpointFile())
	if err != nil || strings.TrimSpace(string(data)) != "http://127.0.0.1:7801" {
		t.Fatalf("announced %q %v", data, err)
	}
	if st, err := os.Stat(endpointFile()); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode: %v %v", st, err)
	}
	if strings.Contains(string(data), opts.Token) {
		t.Error("the token is in the endpoint file")
	}
	cleanup()
	if _, err := os.Stat(endpointFile()); !os.IsNotExist(err) {
		t.Errorf("still announced: %v", err)
	}
	if _, err := os.Stat(tokenFile()); !os.IsNotExist(err) {
		t.Errorf("the token stayed: %v", err)
	}

	opts, cleanup, err = parseTCP(t, "--listen", "127.0.0.1:0", "--token", "given-token-0123456789").options()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	opts.OnListen(addr)
	if _, err := os.Stat(filepath.Join(os.Getenv("HARNESS_HOME"), "agent-endpoint")); !os.IsNotExist(err) {
		t.Errorf("a client given its token announced itself: %v", err)
	}
}
