package browser

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

func TestCFTPlatforms(t *testing.T) {
	cases := map[string]string{"darwin/arm64": "mac-arm64", "darwin/amd64": "mac-x64", "linux/amd64": "linux64", "windows/amd64": "win64", "linux/arm64": ""}
	for k, want := range cases {
		parts := strings.Split(k, "/")
		if got := cftPlatform(parts[0], parts[1]); got != want {
			t.Errorf("cftPlatform(%s) = %q, want %q", k, got, want)
		}
	}
	if !strings.HasSuffix(cftExecutable("mac-arm64"), filepath.Join("Contents", "MacOS", "Google Chrome for Testing")) || !strings.HasSuffix(cftExecutable("win64"), "chrome.exe") {
		t.Error("executable paths")
	}
}

func fakeArchive(t *testing.T, platform string, extra func(*zip.Writer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: filepath.ToSlash(cftExecutable(platform)), Method: zip.Store}
	h.SetMode(0o755)
	f, err := w.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("#!/bin/sh\n"))
	if extra != nil {
		extra(w)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInstallDownloadsAndVerifies(t *testing.T) {
	platform := cftPlatform(runtime.GOOS, runtime.GOARCH)
	if platform == "" {
		t.Skip("no build for this platform")
	}
	archive := fakeArchive(t, platform, nil)
	sum := md5.Sum(archive)
	goodHash := "crc32c=AAAAAA==, md5=" + base64.StdEncoding.EncodeToString(sum[:])
	hash := goodHash
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ChromeForTestingVersion) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Goog-Hash", hash)
		_, _ = w.Write(archive)
	}))
	defer srv.Close()
	old := chromeForTestingBase
	chromeForTestingBase = srv.URL
	defer func() { chromeForTestingBase = old }()

	dir := t.TempDir()
	m := &Manager{Dir: dir}
	run := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		d, err := m.Install(ctx)
		if err != nil {
			return err
		}
		<-d.Done()
		return d.Err()
	}
	hash = "md5=" + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	if err := run(); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("a corrupt download: %v", err)
	}
	if _, ok := downloadedExecutable(dir); ok {
		t.Fatal("a corrupt download was installed")
	}
	hash = goodHash
	if err := run(); err != nil {
		t.Fatal(err)
	}
	e, ok := downloadedExecutable(dir)
	if !ok || e.Source != SourceDownloaded {
		t.Fatalf("not installed: %+v", e)
	}
	t.Setenv("HARNESS_BROWSER_PREFER_DOWNLOADED", "1")
	t.Setenv("HARNESS_BROWSER", "")
	if got, ok := Discover(dir); !ok || got.Path != e.Path {
		t.Fatalf("discover %+v", got)
	}
	a := m.Availability()
	if !a.Available || !a.Downloaded || a.Source != agentv1.BrowserAvailability_SOURCE_DOWNLOADED {
		t.Fatalf("availability %+v", a)
	}
}

func TestExtractZipRefusesEscapes(t *testing.T) {
	write := func(name string, mode os.FileMode, content string) string {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		h := &zip.FileHeader{Name: name}
		h.SetMode(mode)
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(content))
		_ = w.Close()
		p := filepath.Join(t.TempDir(), "a.zip")
		if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ctx := context.Background()
	if err := extractZip(ctx, write("../evil", 0o644, "x"), t.TempDir()); err == nil {
		t.Error("a path outside the target was extracted")
	}
	if err := extractZip(ctx, write("link", os.ModeSymlink|0o777, "../../etc/passwd"), t.TempDir()); err == nil {
		t.Error("a link outside the target was extracted")
	}
	dir := t.TempDir()
	if err := extractZip(ctx, write("a/link", os.ModeSymlink|0o777, "../b"), dir); err != nil {
		t.Errorf("a link inside the target: %v", err)
	}
	if got, err := os.Readlink(filepath.Join(dir, "a", "link")); err != nil || got != "../b" {
		t.Errorf("link %q %v", got, err)
	}
}

func TestDiscoverEnvironmentOverride(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "my-chromium")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_BROWSER", exe)
	got, ok := Discover(t.TempDir())
	if !ok || got.Path != exe || got.Source != SourceEnv || got.Product != "Chromium" {
		t.Fatalf("discover %+v %v", got, ok)
	}
}
