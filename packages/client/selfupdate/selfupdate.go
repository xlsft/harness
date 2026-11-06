// Package selfupdate replaces the running harness-client with the newest stable GitHub release.
//
// What is trusted: checksums.txt, signed with the release key (`tauri signer`, the key the desktop updater checks; its
// public half is stamped in at build time). The archive's name carries the version, and only a checksums.txt that lists
// that exact name counts, so an older signed release can't be replayed as a new one. Nothing touches the installed
// binary until all of that passed: the new one is written next to it and renamed over it.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PublicKey is the base64 public key checksums.txt must be signed with; release builds stamp it with
// -ldflags "-X github.com/xlsft/harness/packages/client/selfupdate.PublicKey=…" from tauri.conf.json.
var PublicKey = ""

const (
	binaryName     = "harness-client"
	maxArchiveSize = 500 << 20
	maxBinarySize  = 1 << 30
	maxSumsSize    = 1 << 20
)

// Updater talks to the releases of one GitHub repository.
type Updater struct {
	Web       string // e.g. https://github.com
	Repo      string // owner/name
	PublicKey string
	HTTP      *http.Client
	OS        string // runtime.GOOS
	Arch      string // runtime.GOARCH
}

// ArchiveName is the release asset for a version and platform, as .goreleaser.yaml names it.
func ArchiveName(version, goos, goarch string) string {
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", binaryName, strings.TrimPrefix(version, "v"), goos, goarch)
}

// LatestTag returns the tag of the newest stable release: the web site's releases/latest redirect, which never points at
// a draft or a pre-release and, unlike the API, has no hourly limit.
func (u Updater) LatestTag(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.Web+"/"+u.Repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	client := *u.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	i := strings.LastIndex(loc, "/releases/tag/")
	if i < 0 {
		// GitHub redirects to the plain releases page while no release is published.
		if resp.StatusCode/100 == 3 || resp.StatusCode == http.StatusNotFound {
			return "", errors.New("no release published yet")
		}
		return "", fmt.Errorf("GitHub: %s", resp.Status)
	}
	tag, err := url.PathUnescape(loc[i+len("/releases/tag/"):])
	if err != nil {
		return "", fmt.Errorf("GitHub: bad release redirect %q", loc)
	}
	if _, ok := parseVersion(tag); !ok {
		return "", fmt.Errorf("GitHub: release tag %q is not a version", tag)
	}
	return tag, nil
}

// ReleaseURL is the page of a release, for people.
func (u Updater) ReleaseURL(tag string) string {
	return u.Web + "/" + u.Repo + "/releases/tag/" + tag
}

func (u Updater) assetURL(tag, name string) string {
	return u.Web + "/" + u.Repo + "/releases/download/" + url.PathEscape(tag) + "/" + url.PathEscape(name)
}

// Install downloads the release for this platform, verifies it, and replaces the binary at exe. On any error the
// binary at exe is as it was.
func (u Updater) Install(ctx context.Context, tag, exe string) error {
	if strings.TrimSpace(u.PublicKey) == "" {
		return errors.New("this build has no release key, so it can't verify an update: only release builds update themselves")
	}
	dir := filepath.Dir(exe)
	work, err := os.MkdirTemp(dir, ".harness-client-update-*")
	if err != nil {
		return fmt.Errorf("can't write next to %s: %w (is it installed system-wide? try with sudo)", exe, err)
	}
	defer os.RemoveAll(work)

	sums, err := u.fetch(ctx, u.assetURL(tag, "checksums.txt"), maxSumsSize)
	if err != nil {
		return err
	}
	sig, err := u.fetch(ctx, u.assetURL(tag, "checksums.txt.sig"), maxSumsSize)
	if err != nil {
		return err
	}
	if err := verifySignature(u.PublicKey, string(sig), sums); err != nil {
		return fmt.Errorf("checksums.txt of %s: %w", tag, err)
	}
	name := ArchiveName(tag, u.OS, u.Arch)
	want, err := checksumOf(sums, name)
	if err != nil {
		return fmt.Errorf("release %s: %w (is there a build for %s/%s?)", tag, err, u.OS, u.Arch)
	}
	archive := filepath.Join(work, name)
	got, err := u.download(ctx, u.assetURL(tag, name), archive)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("checksum mismatch for %s", name)
	}
	bin := filepath.Join(work, binaryName)
	if err := extractBinary(archive, bin); err != nil {
		return err
	}
	mode := os.FileMode(0o755)
	if info, err := os.Stat(exe); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.Chmod(bin, mode); err != nil {
		return err
	}
	// A verified file that can't run here (wrong architecture, a killed-on-sight binary) must not replace a working one.
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(runCtx, bin, "version").CombinedOutput(); err != nil {
		return fmt.Errorf("the downloaded binary does not run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(bin, exe); err != nil {
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	return nil
}

func (u Updater) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	return resp, nil
}

func (u Updater) fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	resp, err := u.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("download %s: larger than expected", url)
	}
	return b, nil
}

// download writes url to path and returns its SHA-256. A connection cut short is an error (net/http reports a body
// shorter than its Content-Length), and the caller's work directory goes away with the partial file.
func (u Updater) download(ctx context.Context, url, path string) (string, error) {
	resp, err := u.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxArchiveSize+1))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	if n > maxArchiveSize {
		return "", fmt.Errorf("download %s: larger than expected", url)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func checksumOf(sums []byte, name string) (string, error) {
	for line := range strings.SplitSeq(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksums.txt lists no %s", name)
}

func extractBinary(archive, out string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(archive), err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("no %s in %s", binaryName, filepath.Base(archive))
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", filepath.Base(archive), err)
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != binaryName {
			continue
		}
		w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
		if err != nil {
			return err
		}
		n, err := io.Copy(w, io.LimitReader(tr, maxBinarySize+1))
		if err == nil && n > maxBinarySize {
			err = fmt.Errorf("%s in %s is larger than expected", binaryName, filepath.Base(archive))
		}
		if err == nil {
			err = w.Sync()
		}
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		return err
	}
}

// ManagedExecutable says who updates the binary at exe when it is not this command: the desktop app's sidecar comes with
// the app and is replaced with it. Empty when the binary is on its own.
func ManagedExecutable(exe, distribution string) string {
	if distribution == "desktop" || strings.Contains(filepath.ToSlash(exe), ".app/Contents/MacOS/") {
		return "desktop"
	}
	return ""
}
