package browser

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// ChromeForTestingVersion is the pinned Chrome for Testing build Install downloads.
const ChromeForTestingVersion = "156.0.8078.80"

// chromeForTestingBase is where the builds are published (overridable in tests).
var chromeForTestingBase = "https://storage.googleapis.com/chrome-for-testing-public"

// cftPlatform maps GOOS/GOARCH to the Chrome for Testing platform name ("" when none is published).
func cftPlatform(goos, goarch string) string {
	switch goos + "/" + goarch {
	case "darwin/arm64":
		return "mac-arm64"
	case "darwin/amd64":
		return "mac-x64"
	case "linux/amd64":
		return "linux64"
	case "windows/amd64":
		return "win64"
	case "windows/386":
		return "win32"
	}
	return ""
}

// cftExecutable is the executable inside the extracted archive, relative to its root.
func cftExecutable(platform string) string {
	switch platform {
	case "mac-arm64", "mac-x64":
		return filepath.Join("chrome-"+platform, "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing")
	case "win64", "win32":
		return filepath.Join("chrome-"+platform, "chrome.exe")
	}
	return filepath.Join("chrome-"+platform, "chrome")
}

// Downloadable reports whether a build is published for this platform.
func Downloadable() bool { return cftPlatform(runtime.GOOS, runtime.GOARCH) != "" }

func installDir(dir string) string { return filepath.Join(dir, "chrome-"+ChromeForTestingVersion) }

// downloadedExecutable returns the downloaded build when it is complete.
func downloadedExecutable(dir string) (Executable, bool) {
	platform := cftPlatform(runtime.GOOS, runtime.GOARCH)
	if platform == "" || dir == "" {
		return Executable{}, false
	}
	p := filepath.Join(installDir(dir), cftExecutable(platform))
	if !isExecutable(p) {
		return Executable{}, false
	}
	return Executable{Path: p, Product: "Chrome for Testing", Source: SourceDownloaded}, true
}

// Download is a running or finished browser download.
type Download struct {
	downloaded atomic.Int64
	total      atomic.Int64
	done       chan struct{}
	err        error
}

// Progress is the bytes downloaded so far and the expected total (0 when unknown).
func (d *Download) Progress() (int64, int64) { return d.downloaded.Load(), d.total.Load() }

// Done is closed when the download ends.
func (d *Download) Done() <-chan struct{} { return d.done }

// Err is the outcome once Done is closed.
func (d *Download) Err() error { return d.err }

// installer runs at most one download at a time.
type installer struct {
	mu      sync.Mutex
	current *Download
	client  *http.Client
}

// start begins (or joins) the download into dir. ctx bounds the download itself.
func (in *installer) start(ctx context.Context, dir string) (*Download, error) {
	platform := cftPlatform(runtime.GOOS, runtime.GOARCH)
	if platform == "" {
		return nil, fmt.Errorf("no Chrome for Testing build is published for %s/%s; install Chrome or Chromium, or set HARNESS_BROWSER", runtime.GOOS, runtime.GOARCH)
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.current != nil {
		select {
		case <-in.current.done:
		default:
			return in.current, nil
		}
	}
	d := &Download{done: make(chan struct{})}
	if _, ok := downloadedExecutable(dir); ok {
		close(d.done)
		return d, nil
	}
	in.current = d
	go func() {
		d.err = in.fetch(ctx, d, dir, platform)
		close(d.done)
	}()
	return d, nil
}

func (in *installer) downloading() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.current == nil {
		return false
	}
	select {
	case <-in.current.done:
		return false
	default:
		return true
	}
}

type countingWriter struct {
	d *Download
	h hash.Hash
}

func (c countingWriter) Write(p []byte) (int, error) {
	c.d.downloaded.Add(int64(len(p)))
	c.h.Write(p)
	return len(p), nil
}

func (in *installer) fetch(ctx context.Context, d *Download, dir, platform string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	url := fmt.Sprintf("%s/%s/%s/chrome-%s.zip", chromeForTestingBase, ChromeForTestingVersion, platform, platform)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	hc := in.client
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("downloading Chrome for Testing: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading Chrome for Testing: %s returned %s", url, resp.Status)
	}
	d.total.Store(max(resp.ContentLength, 0))
	zipFile, err := os.CreateTemp(dir, ".chrome-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(zipFile.Name())
	defer zipFile.Close()
	h := md5.New()
	if _, err := io.Copy(zipFile, io.TeeReader(resp.Body, countingWriter{d: d, h: h})); err != nil {
		return fmt.Errorf("downloading Chrome for Testing: %w", err)
	}
	// Google Cloud Storage sends the object's MD5; a mismatch means a broken transfer.
	if want := googMD5(resp.Header); want != nil && string(want) != string(h.Sum(nil)) {
		return errors.New("downloading Chrome for Testing: the archive's checksum does not match")
	}
	tmp, err := os.MkdirTemp(dir, ".chrome-extract-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := extractZip(ctx, zipFile.Name(), tmp); err != nil {
		return fmt.Errorf("extracting Chrome for Testing: %w", err)
	}
	if !isExecutable(filepath.Join(tmp, cftExecutable(platform))) {
		return errors.New("extracting Chrome for Testing: the archive has no browser executable")
	}
	final := installDir(dir)
	_ = os.RemoveAll(final)
	return os.Rename(tmp, final)
}

// googMD5 reads the md5 entry of x-goog-hash headers.
func googMD5(h http.Header) []byte {
	for _, v := range h.Values("X-Goog-Hash") {
		for _, part := range strings.Split(v, ",") {
			if b64, ok := strings.CutPrefix(strings.TrimSpace(part), "md5="); ok {
				if b, err := base64.StdEncoding.DecodeString(b64); err == nil {
					return b
				}
			}
		}
	}
	return nil
}

// extractZip extracts archive into dir, keeping file modes and symbolic links (macOS app bundles use them).
// Entries and link targets must stay inside dir.
func extractZip(ctx context.Context, archive, dir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	inside := func(p string) bool {
		rel, err := filepath.Rel(root, p)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	for _, f := range zr.File {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		target := filepath.Join(root, filepath.FromSlash(f.Name))
		if !inside(target) {
			return fmt.Errorf("archive entry %q escapes the target directory", f.Name)
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case mode&os.ModeSymlink != 0:
			rc, err := f.Open()
			if err != nil {
				return err
			}
			link, err := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err != nil {
				return err
			}
			dest := filepath.Join(filepath.Dir(target), filepath.FromSlash(string(link)))
			if filepath.IsAbs(string(link)) || !inside(dest) {
				return fmt.Errorf("archive link %q points outside the target directory", f.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(string(link), target); err != nil {
				return err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			perm := mode.Perm() | 0o600
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
			if err != nil {
				rc.Close()
				return err
			}
			_, err = io.Copy(out, rc)
			rc.Close()
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}
