package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// A fake GitHub: a release of signed assets over real HTTP, each of which a test can spoil.
type fakeRelease struct {
	tag        string
	archive    []byte
	sums       []byte
	sig        string
	cutArchive bool // the connection drops halfway through the archive
	srv        *httptest.Server
}

type signer struct {
	id   [8]byte
	priv ed25519.PrivateKey
	pub  string
}

func newSigner(t *testing.T) signer {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := signer{priv: priv}
	if _, err := rand.Read(s.id[:]); err != nil {
		t.Fatal(err)
	}
	raw := append(append([]byte("Ed"), s.id[:]...), pub...)
	file := "untrusted comment: minisign public key\n" + base64.StdEncoding.EncodeToString(raw) + "\n"
	s.pub = base64.StdEncoding.EncodeToString([]byte(file))
	return s
}

// sign writes what `tauri signer sign` writes.
func (s signer) sign(data []byte) string {
	sum := blake2b.Sum512(data)
	sig := ed25519.Sign(s.priv, sum[:])
	comment := "timestamp:1\tfile:checksums.txt"
	global := ed25519.Sign(s.priv, append(append([]byte{}, sig...), comment...))
	raw := append(append([]byte("ED"), s.id[:]...), sig...)
	file := "untrusted comment: signature\n" + base64.StdEncoding.EncodeToString(raw) + "\ntrusted comment: " + comment + "\n" +
		base64.StdEncoding.EncodeToString(global) + "\n"
	return base64.StdEncoding.EncodeToString([]byte(file))
}

func tarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		data []byte
	}{{"LICENSE", []byte("MIT")}, {name, content}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// A runnable "new binary": Install runs it before it swaps it in.
const newBinary = "#!/bin/sh\necho harness-client/1.2.3\n"

func newRelease(t *testing.T, s signer, binary string) (*fakeRelease, Updater) {
	t.Helper()
	const tag = "v1.2.3"
	name := ArchiveName(tag, "testos", "testarch")
	archive := tarGz(t, binaryName, []byte(binary))
	sum := sha256.Sum256(archive)
	sums := fmt.Appendf(nil, "deadbeef  %s\n%s  %s\n", ArchiveName(tag, "other", "arch"), hex.EncodeToString(sum[:]), name)
	r := &fakeRelease{tag: tag, archive: archive, sums: sums, sig: s.sign(sums)}
	mux := http.NewServeMux()
	mux.HandleFunc("/xlsft/harness/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", r.srv.URL+"/xlsft/harness/releases/tag/"+r.tag)
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/xlsft/harness/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) { w.Write(r.sums) })
	mux.HandleFunc("/xlsft/harness/releases/download/"+tag+"/checksums.txt.sig", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, r.sig) })
	mux.HandleFunc("/xlsft/harness/releases/download/"+tag+"/"+name, func(w http.ResponseWriter, _ *http.Request) {
		if r.cutArchive {
			w.Header().Set("Content-Length", fmt.Sprint(len(r.archive)))
			w.Write(r.archive[:len(r.archive)/2])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler) // drops the connection mid-body
		}
		w.Write(r.archive)
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r, Updater{Web: r.srv.URL, Repo: "xlsft/harness", PublicKey: s.pub, HTTP: r.srv.Client(), OS: "testos", Arch: "testarch"}
}

// installed is a directory holding the "old binary".
func installed(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary is a shell script")
	}
	exe := filepath.Join(t.TempDir(), binaryName)
	if err := os.WriteFile(exe, []byte("old binary"), 0o751); err != nil {
		t.Fatal(err)
	}
	return exe
}

// assertUntouched: the old binary is there, and nothing was left next to it.
func assertUntouched(t *testing.T, exe string) {
	t.Helper()
	b, err := os.ReadFile(exe)
	if err != nil || string(b) != "old binary" {
		t.Fatalf("binary changed: %q, %v", b, err)
	}
	entries, err := os.ReadDir(filepath.Dir(exe))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestInstallReplacesBinary(t *testing.T) {
	s := newSigner(t)
	r, u := newRelease(t, s, newBinary)
	exe := installed(t)
	tag, err := u.LatestTag(context.Background())
	if err != nil || tag != r.tag {
		t.Fatalf("LatestTag = %q, %v", tag, err)
	}
	if err := u.Install(context.Background(), tag, exe); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(exe)
	if string(b) != newBinary {
		t.Fatalf("binary = %q", b)
	}
	info, _ := os.Stat(exe)
	if info.Mode().Perm() != 0o751 {
		t.Fatalf("mode = %v, want the old binary's", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(exe))
	if len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestInstallRefusesSignatureFailures(t *testing.T) {
	s := newSigner(t)
	cases := map[string]func(*fakeRelease){
		"tampered checksums": func(r *fakeRelease) { r.sums = append(r.sums, "00  extra\n"...) },
		"another key":        func(r *fakeRelease) { r.sig = newSigner(t).sign(r.sums) },
		"garbage":            func(r *fakeRelease) { r.sig = "not a signature" },
		"empty":              func(r *fakeRelease) { r.sig = "" },
		"same key id, forged": func(r *fakeRelease) {
			forged := newSigner(t)
			forged.id = s.id
			r.sig = forged.sign(r.sums)
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			r, u := newRelease(t, s, newBinary)
			spoil(r)
			exe := installed(t)
			err := u.Install(context.Background(), r.tag, exe)
			if err == nil || !strings.Contains(err.Error(), ErrBadSignature.Error()) {
				t.Fatalf("err = %v", err)
			}
			assertUntouched(t, exe)
		})
	}
}

func TestInstallRefusesChecksumMismatch(t *testing.T) {
	s := newSigner(t)
	r, u := newRelease(t, s, newBinary)
	r.archive = tarGz(t, binaryName, []byte("#!/bin/sh\necho evil\n")) // validly signed checksums, another archive
	exe := installed(t)
	err := u.Install(context.Background(), r.tag, exe)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, exe)
}

func TestInterruptedDownloadLeavesOldBinary(t *testing.T) {
	s := newSigner(t)
	r, u := newRelease(t, s, newBinary)
	r.cutArchive = true
	exe := installed(t)
	if err := u.Install(context.Background(), r.tag, exe); err == nil {
		t.Fatal("no error")
	}
	assertUntouched(t, exe)
}

func TestInstallRefusesABinaryThatDoesNotRun(t *testing.T) {
	s := newSigner(t)
	r, u := newRelease(t, s, "#!/bin/sh\nexit 3\n")
	exe := installed(t)
	err := u.Install(context.Background(), r.tag, exe)
	if err == nil || !strings.Contains(err.Error(), "does not run") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, exe)
}

func TestInstallRefusesAReplayedOlderRelease(t *testing.T) {
	// The checksums of an older release, correctly signed, offered as the new tag: they list no archive of this version.
	s := newSigner(t)
	r, u := newRelease(t, s, newBinary)
	r.sums = fmt.Appendf(nil, "abcd  %s\n", ArchiveName("v1.0.0", "testos", "testarch"))
	r.sig = s.sign(r.sums)
	exe := installed(t)
	err := u.Install(context.Background(), r.tag, exe)
	if err == nil || !strings.Contains(err.Error(), "lists no") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, exe)
}

func TestInstallRefusesWithoutAKey(t *testing.T) {
	s := newSigner(t)
	r, u := newRelease(t, s, newBinary)
	u.PublicKey = ""
	exe := installed(t)
	err := u.Install(context.Background(), r.tag, exe)
	if err == nil || !strings.Contains(err.Error(), "no release key") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, exe)
}

func TestLatestTagWithoutARelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://example.test/xlsft/harness/releases")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	u := Updater{Web: srv.URL, Repo: "xlsft/harness", HTTP: srv.Client()}
	if _, err := u.LatestTag(context.Background()); err == nil || !strings.Contains(err.Error(), "no release") {
		t.Fatalf("err = %v", err)
	}
}

// A real pair from `tauri signer` (the private key was thrown away): this code and the release workflow's tool agree.
func TestVerifiesTauriSignerOutput(t *testing.T) {
	const (
		pub = "dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWduIHB1YmxpYyBrZXk6IDFENUYwNjlCOTcwMTM1QkQKUldTOU5RR1htd1pmSFVrQlB4L1U4VVNVQlJoRmgrTnptS3FJaCt6RlhSVjY3U2RuL3RSQ2E2cm0K"
		sig = "dW50cnVzdGVkIGNvbW1lbnQ6IHNpZ25hdHVyZSBmcm9tIHRhdXJpIHNlY3JldCBrZXkKUlVTOU5RR1htd1pmSFRmWmpMMnFTa0dRelQyNmM0dmNXVjZ3R0dLQ2VFZXN5L2t0K3p3ZFNiS3ptR0FLd0k3ck9YUUo3M0loSU9GbGwzeEhqVEdNVU5xalZXUFZ4YWlrR2c0PQp0cnVzdGVkIGNvbW1lbnQ6IHRpbWVzdGFtcDoxNzkzOTYyMDYxCWZpbGU6Y2hlY2tzdW1zLnR4dApIb0ZMYkp5MVF5OEUzamVwWTNlUkVmVk80K0RpK2lqd0lBNUUxTThDRXQ4U29jZHh4MU9Qd090Zkt2QVVEcE42OXF1RTBnNXdaT3I4aENtZmdHZU5Bdz09Cg=="
	)
	data := []byte("abc123  harness-client_1.2.3_darwin_arm64.tar.gz\n")
	if err := verifySignature(pub, sig, data); err != nil {
		t.Fatal(err)
	}
	if err := verifySignature(pub, sig, append(data, ' ')); err == nil {
		t.Fatal("verified changed data")
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		tag, current string
		want         bool
	}{
		{"v0.2.0", "0.1.0", true},
		{"v0.1.0", "0.1.0", false},
		{"v0.1.0", "0.2.0", false},
		{"v0.2.0", "0.2.0-rc.1", true},
		{"v0.2.0-rc.2", "0.2.0-rc.1", true},
		{"v0.2.0-rc.10", "0.2.0-rc.9", true},
		{"v0.2.0-rc.1", "0.2.0", false},
		{"v0.1.0", "0.0.0-dev", true},
		{"nonsense", "0.1.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.tag, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.tag, c.current, got)
		}
	}
}

func TestManagedExecutable(t *testing.T) {
	cases := []struct {
		exe, dist, want string
	}{
		{"/Applications/x.app/Contents/MacOS/harness-client", "", "desktop"},
		{"/somewhere/target/debug/harness-client", "desktop", "desktop"},
		{"/usr/local/bin/harness-client", "", ""},
	}
	for _, c := range cases {
		if got := ManagedExecutable(c.exe, c.dist); got != c.want {
			t.Errorf("ManagedExecutable(%q, %q) = %q", c.exe, c.dist, got)
		}
	}
}

// buildClient builds the real binary, pointed at a fake GitHub.
func buildClient(t *testing.T, out, version, web, key, distribution string) {
	t.Helper()
	const mod = "github.com/xlsft/harness/packages/client"
	ldflags := fmt.Sprintf("-X main.releasesWeb=%s -X %s/config.Version=%s -X %s/selfupdate.PublicKey=%s -X %s/config.Distribution=%s",
		web, mod, version, mod, key, mod, distribution)
	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", out, "../cmd/harness-client")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
}

// The whole command, end to end: the real v0.1.0 binary finds v0.2.0 on a fake GitHub and becomes it.
func TestCommandUpdatesItself(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the client")
	}
	if runtime.GOOS == "windows" {
		t.Skip("no Windows release")
	}
	s := newSigner(t)
	const tag = "v0.2.0"
	name := ArchiveName(tag, runtime.GOOS, runtime.GOARCH)

	dir := t.TempDir()
	newBin := filepath.Join(dir, "new")
	ln, err := net.Listen("tcp", "127.0.0.1:0") // the address first: the binaries being served are built with it
	if err != nil {
		t.Fatal(err)
	}
	web := "http://" + ln.Addr().String()
	buildClient(t, newBin, "0.2.0", web, s.pub, "")
	content, err := os.ReadFile(newBin)
	if err != nil {
		t.Fatal(err)
	}
	archive := tarGz(t, binaryName, content)
	sum := sha256.Sum256(archive)
	sums := fmt.Appendf(nil, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	sig := s.sign(sums)

	mux := http.NewServeMux()
	mux.HandleFunc("/xlsft/harness/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://"+r.Host+"/xlsft/harness/releases/tag/"+tag)
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/xlsft/harness/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) { w.Write(sums) })
	mux.HandleFunc("/xlsft/harness/releases/download/"+tag+"/checksums.txt.sig", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, sig) })
	mux.HandleFunc("/xlsft/harness/releases/download/"+tag+"/"+name, func(w http.ResponseWriter, _ *http.Request) { w.Write(archive) })
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	run := func(exe string, args ...string) (string, error) {
		b, err := exec.Command(exe, args...).CombinedOutput()
		return string(b), err
	}

	exe := filepath.Join(dir, "bin", binaryName)
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	buildClient(t, exe, "0.1.0", srv.URL, s.pub, "")
	if out, err := run(exe, "update", "--check"); err != nil || !strings.Contains(out, "v0.2.0 is available") {
		t.Fatalf("--check: %v: %s", err, out)
	}
	if out, _ := run(exe, "version"); !strings.Contains(out, "harness-client/0.1.0") {
		t.Fatalf("--check changed the binary: %s", out)
	}
	if out, err := run(exe, "update"); err != nil || !strings.Contains(out, "Updated") {
		t.Fatalf("update: %v: %s", err, out)
	}
	if out, _ := run(exe, "version"); !strings.Contains(out, "harness-client/0.2.0") {
		t.Fatalf("after update: %s", out)
	}
	if out, err := run(exe, "update"); err != nil || !strings.Contains(out, "latest version") {
		t.Fatalf("second update: %v: %s", err, out)
	}

	// The same binary as the desktop's sidecar refuses, by path and by stamp, and stays as it was.
	sidecar := filepath.Join(dir, "x.app", "Contents", "MacOS", binaryName)
	if err := os.MkdirAll(filepath.Dir(sidecar), 0o755); err != nil {
		t.Fatal(err)
	}
	buildClient(t, sidecar, "0.1.0", srv.URL, s.pub, "")
	stamped := filepath.Join(dir, "stamped")
	buildClient(t, stamped, "0.1.0", srv.URL, s.pub, "desktop")
	for _, b := range []string{sidecar, stamped} {
		if out, err := run(b, "update"); err == nil || !strings.Contains(out, "updated with the app") {
			t.Fatalf("%s update: %v: %s", b, err, out)
		}
		if out, _ := run(b, "version"); !strings.Contains(out, "harness-client/0.1.0") {
			t.Fatalf("%s: %s", b, out)
		}
	}

	// A development build asks for --force before it goes.
	dev := filepath.Join(dir, "dev", binaryName)
	if err := os.MkdirAll(filepath.Dir(dev), 0o755); err != nil {
		t.Fatal(err)
	}
	buildClient(t, dev, "0.0.0-dev", srv.URL, s.pub, "")
	if out, err := run(dev, "update"); err == nil || !strings.Contains(out, "development build") {
		t.Fatalf("dev update: %v: %s", err, out)
	}
}
