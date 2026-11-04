package registry

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
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
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxArchiveBytes bounds a download; maxExtractBytes bounds what an archive may unpack to.
const (
	maxArchiveBytes = 1 << 30
	maxExtractBytes = 4 << 30
)

// Phase of an install.
type Phase int

const (
	PhaseResolving Phase = iota + 1
	PhaseDownloading
	PhaseVerifying
	PhaseExtracting
	PhaseDone
)

// Progress of an install.
type Progress struct {
	Phase      Phase
	BytesDone  int64
	BytesTotal int64
	Message    string
}

// ErrUnavailable wraps the reason an agent cannot be installed here.
var ErrUnavailable = errors.New("the agent cannot run on this machine")

func (m *Manager) agentsDir() string { return filepath.Join(m.Dir, "agents") }

func (m *Manager) lockInstall(id string) func() {
	v, _ := m.installs.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Install installs the given registry entry (the registry's current version). A binary agent is downloaded,
// verified against its sha256 when the registry has one, and extracted into agents/<id>/<version>/; an npx or
// uvx agent is recorded (npx/uvx fetch the package on first launch). A previously installed version is removed
// once the new one is in place.
func (m *Manager) Install(ctx context.Context, a Agent, progress func(Progress)) (Installed, error) {
	if progress == nil {
		progress = func(Progress) {}
	}
	if !validID(a.ID) || !validVersion(a.Version) {
		return Installed{}, fmt.Errorf("invalid agent %q version %q", a.ID, a.Version)
	}
	defer m.lockInstall(a.ID)()
	progress(Progress{Phase: PhaseResolving, Message: "Resolving " + a.Name})
	res := m.Resolve(a)
	if !res.Available {
		return Installed{}, fmt.Errorf("%w: %s", ErrUnavailable, res.Reason)
	}
	in := Installed{ID: a.ID, Version: a.Version, Kind: res.Kind, InstalledAt: m.now().UTC()}
	switch res.Kind {
	case KindNpx, KindUvx:
		in.Package, in.Args, in.Env = res.Package.Package, res.Package.Args, res.Package.Env
	case KindBinary:
		dir, err := m.installBinary(ctx, a, res.Binary, progress)
		if err != nil {
			return Installed{}, err
		}
		in.Dir, in.Cmd, in.Args, in.Env = dir, res.Binary.Cmd, res.Binary.Args, res.Binary.Env
	}
	var old *Installed
	err := m.updateState(func(st *stateFile) error {
		s := st.Agents[a.ID]
		if s == nil {
			s = &AgentState{}
			st.Agents[a.ID] = s
		}
		old = s.Installed
		s.Installed = &in
		return nil
	})
	if err != nil {
		return Installed{}, err
	}
	if old != nil && old.Dir != "" && old.Dir != in.Dir && within(m.agentsDir(), old.Dir) {
		_ = os.RemoveAll(old.Dir)
	}
	progress(Progress{Phase: PhaseDone, Message: a.Name + " " + a.Version + " installed"})
	return in, nil
}

// Uninstall removes an installed agent (its files, for a binary) and forgets it. Sign-in state is kept.
func (m *Manager) Uninstall(id string) error {
	if !validID(id) {
		return fmt.Errorf("invalid agent id %q", id)
	}
	defer m.lockInstall(id)()
	var old *Installed
	if err := m.updateState(func(st *stateFile) error {
		s := st.Agents[id]
		if s == nil || s.Installed == nil {
			return ErrNotInstalled
		}
		old = s.Installed
		s.Installed, s.Catalog = nil, nil
		return nil
	}); err != nil {
		return err
	}
	if old.Dir != "" && within(m.agentsDir(), old.Dir) {
		_ = os.RemoveAll(old.Dir)
		_ = os.Remove(filepath.Dir(old.Dir)) // the agent's directory, when empty
	}
	return nil
}

func (m *Manager) installBinary(ctx context.Context, a Agent, t *BinaryTarget, progress func(Progress)) (string, error) {
	u, err := url.Parse(t.Archive)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("agent %s: unsupported archive URL %q", a.ID, t.Archive)
	}
	base := filepath.Join(m.agentsDir(), a.ID)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(base, ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	archive := filepath.Join(tmp, "download")
	sum, err := m.download(ctx, t.Archive, archive, progress)
	if err != nil {
		return "", fmt.Errorf("agent %s: %w", a.ID, err)
	}
	progress(Progress{Phase: PhaseVerifying, Message: "Verifying"})
	if want := strings.ToLower(strings.TrimSpace(t.Sha256)); want != "" && want != sum {
		return "", fmt.Errorf("agent %s: checksum mismatch (got %s, want %s)", a.ID, sum, want)
	}
	progress(Progress{Phase: PhaseExtracting, Message: "Extracting"})
	out := filepath.Join(tmp, "out")
	if err := os.MkdirAll(out, 0o700); err != nil {
		return "", err
	}
	if err := extract(archive, archiveName(u), t.Cmd, out); err != nil {
		return "", fmt.Errorf("agent %s: %w", a.ID, err)
	}
	cmd := filepath.Join(out, filepath.FromSlash(strings.ReplaceAll(t.Cmd, `\`, "/")))
	if !within(out, cmd) {
		return "", fmt.Errorf("agent %s: command %s is outside the archive", a.ID, t.Cmd)
	}
	st, err := os.Stat(cmd)
	if err != nil {
		return "", fmt.Errorf("agent %s: the archive has no %s", a.ID, t.Cmd)
	}
	_ = os.Chmod(cmd, st.Mode()|0o700)
	dest := filepath.Join(base, a.Version)
	_ = os.RemoveAll(dest)
	if err := os.Rename(out, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// archiveName is the file name of the archive URL (its format is told by the extension).
func archiveName(u *url.URL) string { return path.Base(u.Path) }

func (m *Manager) download(ctx context.Context, src, dst string, progress func(Progress)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return "", err
	}
	resp, err := m.http().Do(req)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", src, resp.Status)
	}
	if resp.ContentLength > maxArchiveBytes {
		return "", errors.New("download: archive too large")
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	var done int64
	last := time.Time{}
	buf := make([]byte, 64<<10)
	progress(Progress{Phase: PhaseDownloading, BytesTotal: total, Message: "Downloading"})
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			done += int64(n)
			if done > maxArchiveBytes {
				return "", errors.New("download: archive too large")
			}
			if _, err := f.Write(buf[:n]); err != nil {
				return "", err
			}
			h.Write(buf[:n])
			if time.Since(last) > 200*time.Millisecond {
				last = time.Now()
				progress(Progress{Phase: PhaseDownloading, BytesDone: done, BytesTotal: total, Message: "Downloading"})
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", fmt.Errorf("download: %w", rerr)
		}
	}
	progress(Progress{Phase: PhaseDownloading, BytesDone: done, BytesTotal: total, Message: "Downloaded"})
	return hex.EncodeToString(h.Sum(nil)), f.Close()
}

// extract unpacks archive (named name, for its format) into dir. Every entry must stay inside dir; symlinks
// must point inside it too. A file that is no archive is the command itself.
func extract(archive, name, cmd, dir string) error {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(archive, dir)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		zr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		return extractTar(zr, dir)
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"):
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		return extractTar(bzip2.NewReader(f), dir)
	case strings.HasSuffix(lower, ".tar"):
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		return extractTar(f, dir)
	case strings.HasSuffix(lower, ".tar.xz"), strings.HasSuffix(lower, ".txz"), strings.HasSuffix(lower, ".7z"), strings.HasSuffix(lower, ".rar"):
		return fmt.Errorf("unsupported archive format %s", name)
	}
	// A bare executable.
	target, err := safeJoin(dir, strings.ReplaceAll(cmd, `\`, "/"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	return copyFile(archive, target, 0o700)
}

// safeJoin joins an archive entry name to dir, refusing absolute names and names that leave dir.
func safeJoin(dir, name string) (string, error) {
	name = strings.TrimPrefix(strings.ReplaceAll(name, `\`, "/"), "./")
	if name == "" || strings.HasPrefix(name, "/") || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", fmt.Errorf("archive entry %q has an absolute path", name)
	}
	p := filepath.Join(dir, filepath.FromSlash(name))
	if !within(dir, p) {
		return "", fmt.Errorf("archive entry %q escapes the install directory", name)
	}
	return p, nil
}

func extractTar(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Name == "." || h.Name == "./" {
			continue
		}
		p, err := safeJoin(dir, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if total > maxExtractBytes {
				return errors.New("archive too large")
			}
			if err := writeEntry(p, tr, h.FileInfo().Mode()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := safeSymlink(dir, p, h.Linkname); err != nil {
				return err
			}
		case tar.TypeLink:
			src, err := safeJoin(dir, h.Linkname)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				return err
			}
			if err := os.Link(src, p); err != nil {
				return err
			}
		default:
			// Devices, fifos and the like are skipped.
		}
	}
}

func extractZip(archive, dir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	var total int64
	for _, f := range zr.File {
		p, err := safeJoin(dir, f.Name)
		if err != nil {
			return err
		}
		mode := f.Mode()
		switch {
		case f.FileInfo().IsDir():
			if err := os.MkdirAll(p, 0o700); err != nil {
				return err
			}
			continue
		case mode&os.ModeSymlink != 0:
			rc, err := f.Open()
			if err != nil {
				return err
			}
			target, err := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err != nil {
				return err
			}
			if err := safeSymlink(dir, p, string(target)); err != nil {
				return err
			}
			continue
		}
		total += int64(f.UncompressedSize64)
		if total > maxExtractBytes {
			return errors.New("archive too large")
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeEntry(p, rc, mode)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func safeSymlink(dir, p, target string) error {
	if filepath.IsAbs(target) {
		return fmt.Errorf("archive symlink %s points to an absolute path", p)
	}
	if !within(dir, filepath.Join(filepath.Dir(p), target)) {
		return fmt.Errorf("archive symlink %s escapes the install directory", p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.Symlink(target, p)
}

func writeEntry(p string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	perm := mode.Perm() & 0o755
	if perm == 0 {
		perm = 0o644
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(r, maxExtractBytes)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeEntry(dst, in, mode)
}
