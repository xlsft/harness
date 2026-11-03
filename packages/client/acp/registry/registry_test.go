package registry

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testRegistry = `{
  "version": "1.0.0",
  "agents": [
    {"id": "claude-acp", "name": "Claude Agent", "version": "0.100.0", "description": "ACP wrapper",
     "icon": "%[1]s/icons/claude-acp.svg",
     "distribution": {"npx": {"package": "@agentclientprotocol/claude-agent-acp@0.100.0"}}},
    {"id": "goose", "name": "goose", "version": "1.57.0",
     "distribution": {"binary": {
       "darwin-aarch64": {"archive": "%[1]s/goose.tar.gz", "cmd": "./goose", "args": ["acp"], "sha256": "%[2]s"},
       "linux-x86_64": {"archive": "%[1]s/goose.zip", "cmd": "./bin/goose", "args": ["acp"]}
     }}},
    {"id": "fast-agent", "name": "fast-agent", "version": "0.10.1",
     "distribution": {"uvx": {"package": "fast-agent-acp==0.10.1", "args": ["-x"], "env": {"FAST_AGENT_MODEL": "x"}}}},
    {"id": "../evil", "name": "evil", "version": "1", "distribution": {}}
  ]
}`

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

type fixture struct {
	srv      *httptest.Server
	fetches  atomic.Int32
	notMod   atomic.Int32
	down     atomic.Bool
	archives map[string][]byte
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{archives: map[string][]byte{}}
	f.archives["/goose.tar.gz"] = tarGz(t, map[string]string{"goose": "#!/bin/sh\necho goose\n", "lib/readme.txt": "hi"})
	f.archives["/goose.zip"] = zipBytes(t, map[string]string{"bin/goose": "#!/bin/sh\n"})
	sum := sha256.Sum256(f.archives["/goose.tar.gz"])
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		switch {
		case r.URL.Path == "/registry.json":
			f.fetches.Add(1)
			if r.Header.Get("If-None-Match") == `"v1"` {
				f.notMod.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			fmt.Fprintf(w, testRegistry, f.srv.URL, hex.EncodeToString(sum[:]))
		case strings.HasPrefix(r.URL.Path, "/icons/"):
			w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))
		default:
			if b, ok := f.archives[r.URL.Path]; ok {
				w.Write(b)
				return
			}
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newManager(t *testing.T, f *fixture, platform string, path ...string) *Manager {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	m := &Manager{Dir: t.TempDir(), URL: f.srv.URL + "/registry.json", Platform: platform, Now: func() time.Time { return now }}
	m.LookPath = func(name string) (string, error) {
		for _, p := range path {
			if p == name {
				return "/usr/bin/" + name, nil
			}
		}
		return "", errors.New("not found")
	}
	return m
}

func TestLoadCachesWithETagAndWorksOffline(t *testing.T) {
	f := newFixture(t)
	m := newManager(t, f, "darwin-aarch64", "npx")
	snap := m.Load(context.Background(), false)
	if snap.Err != nil || snap.Registry == nil {
		t.Fatalf("load: %+v", snap)
	}
	if len(snap.Registry.Agents) != 3 {
		t.Fatalf("invalid ids must be dropped, got %d agents", len(snap.Registry.Agents))
	}
	if m.IconSVG("claude-acp") == "" {
		t.Fatal("icon not cached")
	}
	// Fresh cache: no fetch.
	m.Load(context.Background(), false)
	if f.fetches.Load() != 1 {
		t.Fatalf("fetches = %d, want 1", f.fetches.Load())
	}
	// Refresh: conditional request answered with 304.
	if snap := m.Load(context.Background(), true); snap.Err != nil || len(snap.Registry.Agents) != 3 {
		t.Fatalf("refresh: %+v", snap)
	}
	if f.notMod.Load() != 1 {
		t.Fatalf("expected a 304, got %d", f.notMod.Load())
	}
	// Offline: the cache is served with the error.
	f.down.Store(true)
	snap = m.Load(context.Background(), true)
	if snap.Err == nil || snap.Registry == nil || len(snap.Registry.Agents) != 3 {
		t.Fatalf("offline: %+v", snap)
	}
}

func TestResolve(t *testing.T) {
	f := newFixture(t)
	m := newManager(t, f, "linux-aarch64")
	reg := m.Load(context.Background(), false).Registry
	claude, _ := reg.Find("claude-acp")
	if r := m.Resolve(claude); r.Available || r.Kind != KindNpx || !strings.Contains(r.Reason, "npx") {
		t.Fatalf("claude without node: %+v", r)
	}
	goose, _ := reg.Find("goose")
	if r := m.Resolve(goose); r.Available || !strings.Contains(r.Reason, "linux-aarch64") {
		t.Fatalf("goose on linux-aarch64: %+v", r)
	}
	m.LookPath = func(string) (string, error) { return "/usr/bin/x", nil }
	if r := m.Resolve(claude); !r.Available || r.Runtime != "npx" {
		t.Fatalf("claude with node: %+v", r)
	}
}

func TestInstallTarGzVerifiesAndLaunches(t *testing.T) {
	f := newFixture(t)
	m := newManager(t, f, "darwin-aarch64")
	reg := m.Load(context.Background(), false).Registry
	goose, _ := reg.Find("goose")
	var phases []Phase
	in, err := m.Install(context.Background(), goose, func(p Progress) { phases = append(phases, p.Phase) })
	if err != nil {
		t.Fatal(err)
	}
	if in.Version != "1.57.0" || in.Kind != KindBinary {
		t.Fatalf("installed: %+v", in)
	}
	if phases[len(phases)-1] != PhaseDone {
		t.Fatalf("phases: %v", phases)
	}
	l, err := m.Launch("goose")
	if err != nil {
		t.Fatal(err)
	}
	if l.Command != filepath.Join(m.Dir, "agents", "goose", "1.57.0", "goose") || len(l.Args) != 1 || l.Args[0] != "acp" {
		t.Fatalf("launch: %+v", l)
	}
	if st, err := os.Stat(l.Command); err != nil || st.Mode()&0o100 == 0 {
		t.Fatalf("command not executable: %v %v", st, err)
	}
	// Update to a newer version removes the old directory.
	goose.Version = "1.58.0"
	if _, err := m.Install(context.Background(), goose, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "agents", "goose", "1.57.0")); !os.IsNotExist(err) {
		t.Fatalf("old version kept: %v", err)
	}
	if err := m.Uninstall("goose"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Launch("goose"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("launch after uninstall: %v", err)
	}
}

func TestInstallChecksumMismatch(t *testing.T) {
	f := newFixture(t)
	m := newManager(t, f, "darwin-aarch64")
	goose, _ := m.Load(context.Background(), false).Registry.Find("goose")
	goose.Distribution.Binary["darwin-aarch64"] = BinaryTarget{Archive: f.srv.URL + "/goose.tar.gz", Cmd: "./goose", Sha256: strings.Repeat("0", 64)}
	if _, err := m.Install(context.Background(), goose, nil); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want checksum error, got %v", err)
	}
	if st := m.State("goose"); st.Installed != nil {
		t.Fatal("recorded despite failure")
	}
}

func TestInstallZip(t *testing.T) {
	f := newFixture(t)
	m := newManager(t, f, "linux-x86_64")
	goose, _ := m.Load(context.Background(), false).Registry.Find("goose")
	if _, err := m.Install(context.Background(), goose, nil); err != nil {
		t.Fatal(err)
	}
	l, err := m.Launch("goose")
	if err != nil || !strings.HasSuffix(l.Command, filepath.Join("bin", "goose")) {
		t.Fatalf("launch: %+v %v", l, err)
	}
}

func TestInstallRejectsTraversal(t *testing.T) {
	f := newFixture(t)
	f.archives["/evil.tar.gz"] = tarGz(t, map[string]string{"../../escape": "x", "goose": "x"})
	f.archives["/evil.zip"] = zipBytes(t, map[string]string{"../escape": "x"})
	for _, name := range []string{"evil.tar.gz", "evil.zip"} {
		m := newManager(t, f, "darwin-aarch64")
		a := Agent{ID: "evil", Name: "evil", Version: "1", Distribution: Distribution{Binary: map[string]BinaryTarget{
			"darwin-aarch64": {Archive: f.srv.URL + "/" + name, Cmd: "./goose"}}}}
		if _, err := m.Install(context.Background(), a, nil); err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Fatalf("%s: want traversal error, got %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(m.Dir, "escape")); !os.IsNotExist(err) {
			t.Fatalf("%s: file escaped", name)
		}
	}
}

func TestInstallNpxAndUvxRecordLaunch(t *testing.T) {
	f := newFixture(t)
	m := newManager(t, f, "darwin-aarch64", "npx", "uvx")
	reg := m.Load(context.Background(), false).Registry
	claude, _ := reg.Find("claude-acp")
	if _, err := m.Install(context.Background(), claude, nil); err != nil {
		t.Fatal(err)
	}
	l, err := m.Launch("claude-acp")
	if err != nil || l.Command != "/usr/bin/npx" || strings.Join(l.Args, " ") != "--yes @agentclientprotocol/claude-agent-acp@0.100.0" {
		t.Fatalf("npx launch: %+v %v", l, err)
	}
	fa, _ := reg.Find("fast-agent")
	if _, err := m.Install(context.Background(), fa, nil); err != nil {
		t.Fatal(err)
	}
	l, err = m.Launch("fast-agent")
	if err != nil || l.Command != "/usr/bin/uvx" || strings.Join(l.Args, " ") != "fast-agent-acp==0.10.1 -x" || l.Env["FAST_AGENT_MODEL"] != "x" {
		t.Fatalf("uvx launch: %+v %v", l, err)
	}
	// Without node the npx agent cannot be installed.
	m2 := newManager(t, f, "darwin-aarch64")
	if _, err := m2.Install(context.Background(), claude, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestSetAuthKeepsAuthenticated(t *testing.T) {
	m := &Manager{Dir: t.TempDir()}
	methods := []AuthMethod{{ID: "login", Name: "Log in", Kind: "terminal", Args: []string{"--login"}}}
	if err := m.SetAuth("claude-acp", AuthRequired, methods); err != nil {
		t.Fatal(err)
	}
	m.SetAuth("claude-acp", AuthAuthenticated, nil)
	m.SetAuth("claude-acp", AuthNotRequired, nil)
	st := m.State("claude-acp")
	if st.Auth != AuthAuthenticated || len(st.AuthMethods) != 1 || st.AuthMethods[0].Args[0] != "--login" {
		t.Fatalf("state: %+v", st)
	}
}

func TestPlatformKey(t *testing.T) {
	for in, want := range map[[2]string]string{{"darwin", "arm64"}: "darwin-aarch64", {"linux", "amd64"}: "linux-x86_64", {"windows", "amd64"}: "windows-x86_64"} {
		if got := platformKey(in[0], in[1]); got != want {
			t.Errorf("%v: %s, want %s", in, got, want)
		}
	}
}
