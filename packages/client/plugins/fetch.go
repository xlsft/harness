package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The types of a source. Marketplaces use github, git, url (a link to the file), file and directory; plugins use github,
// url (a git repository), git-subdir and a relative path.
const (
	SourceGitHub    = "github"
	SourceGit       = "git"
	SourceURL       = "url"
	SourceFile      = "file"
	SourceDirectory = "directory"
	SourceSubdir    = "git-subdir"
)

// Limits on what is fetched. Past them it is not a plugin.
const (
	maxTreeBytes     = 512 << 20
	maxTreeFiles     = 20000
	maxManifestBytes = 16 << 20
	fetchTimeout     = 5 * time.Minute
)

// Source says where something comes from.
type Source struct {
	Source string `json:"source"`
	Repo   string `json:"repo,omitempty"`
	URL    string `json:"url,omitempty"`
	// Path is a local path (file, directory), the folder inside the repository (git-subdir), or the marketplace file's path
	// inside the repository (github, git).
	Path string `json:"path,omitempty"`
	Ref  string `json:"ref,omitempty"`
	Sha  string `json:"sha,omitempty"`
	// Headers go with a url source, as a policy document gives them.
	Headers map[string]string `json:"headers,omitempty"`
}

func (s Source) cloneURL() string {
	if s.Source == SourceGitHub {
		return "https://github.com/" + s.Repo + ".git"
	}
	if s.Source == SourceSubdir && ownerRepoRe.MatchString(s.URL) {
		return "https://github.com/" + s.URL + ".git"
	}
	return s.URL
}

// Host is the host of a remote source.
func (s Source) Host() string {
	switch s.Source {
	case SourceGitHub:
		return "github.com"
	case SourceGit, SourceURL, SourceSubdir:
		c := s.cloneURL()
		if m := scpRe.FindStringSubmatch(c); m != nil {
			return strings.ToLower(m[1])
		}
		if u, err := url.Parse(c); err == nil {
			return strings.ToLower(u.Hostname())
		}
	}
	return ""
}

func (s Source) String() string {
	out := ""
	switch s.Source {
	case SourceGitHub:
		out = s.Repo
	case SourceGit, SourceURL, SourceSubdir:
		out = s.URL
	case SourceFile, SourceDirectory:
		out = s.Path
	default:
		out = s.Source
	}
	if s.Ref != "" {
		out += "#" + s.Ref
	}
	return out
}

var (
	ownerRepoRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	scpRe       = regexp.MustCompile(`^[A-Za-z0-9._-]+@([A-Za-z0-9.-]+):(.+)$`)
	shaRe       = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// ParseSource reads what a person types for a marketplace or a plugin: owner/repo, a git URL, a URL of a manifest, or a local path.
func ParseSource(text string) (Source, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Source{}, errors.New("a source is required")
	}
	if strings.HasPrefix(text, "-") {
		return Source{}, fmt.Errorf("%q can't be a source", text)
	}
	base, ref := text, ""
	if i := strings.LastIndex(text, "#"); i > 0 {
		base, ref = text[:i], text[i+1:]
	}
	switch {
	case strings.HasPrefix(text, "/"), strings.HasPrefix(text, "./"), strings.HasPrefix(text, "../"), text == ".", strings.HasPrefix(text, "~/"):
		p := text
		if strings.HasPrefix(p, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return Source{}, err
			}
			p = filepath.Join(home, p[2:])
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return Source{}, err
		}
		if strings.HasSuffix(abs, ".json") {
			return Source{Source: SourceFile, Path: abs}, nil
		}
		return Source{Source: SourceDirectory, Path: abs}, nil
	case ownerRepoRe.MatchString(base):
		return Source{Source: SourceGitHub, Repo: base, Ref: ref}, nil
	case scpRe.MatchString(base):
		return Source{Source: SourceGit, URL: base, Ref: ref}, nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" {
		return Source{}, fmt.Errorf("%q is not a repository, a URL or a path", text)
	}
	switch u.Scheme {
	case "http", "https":
		if strings.HasSuffix(u.Path, ".json") {
			return Source{Source: SourceURL, URL: text}, nil
		}
		return Source{Source: SourceGit, URL: base, Ref: ref}, nil
	case "ssh", "git", "file":
		return Source{Source: SourceGit, URL: base, Ref: ref}, nil
	}
	return Source{}, fmt.Errorf("%s: this client fetches over https, ssh, git and file", u.Scheme)
}

// checkRemote refuses what a repository or a file must not name: a git transport helper runs a program of its choice, and a
// plain http fetch can be rewritten on its way to a machine that will run what it brings.
func checkRemote(raw string) error {
	if strings.HasPrefix(raw, "-") {
		return fmt.Errorf("%q can't be a URL", raw)
	}
	if scpRe.MatchString(raw) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q can't be read as a URL", raw)
	}
	switch u.Scheme {
	case "https", "ssh", "git", "file":
		return nil
	case "http":
		if ip := net.ParseIP(u.Hostname()); (ip != nil && ip.IsLoopback()) || u.Hostname() == "localhost" {
			return nil
		}
		return fmt.Errorf("%s: plain http is only for this machine; use https", raw)
	}
	return fmt.Errorf("%s: this client fetches over https, ssh, git and file", raw)
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	// Protect the clone: no helper transports whoever asks, and no hook runs out of it.
	full := append([]string{"-c", "protocol.ext.allow=never", "-c", "protocol.fd.allow=never", "-c", "core.hooksPath=" + os.DevNull}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "GCM_INTERACTIVE=never")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return "", fmt.Errorf("git %s: %s", args[0], text)
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return text, nil
}

// clone fetches a repository into dest and returns the commit it holds. A sha is checked out exactly; otherwise the tip of ref.
func clone(ctx context.Context, s Source, dest string) (string, error) {
	remote := s.cloneURL()
	if err := checkRemote(remote); err != nil {
		return "", err
	}
	if s.Sha != "" && !shaRe.MatchString(s.Sha) {
		return "", fmt.Errorf("sha %q is not a full commit id", s.Sha)
	}
	if s.Ref != "" && strings.HasPrefix(s.Ref, "-") {
		return "", fmt.Errorf("%q can't be a ref", s.Ref)
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	args := []string{"clone", "--quiet", "--no-recurse-submodules"}
	if s.Sha != "" {
		args = append(args, "--no-checkout")
	} else {
		args = append(args, "--depth", "1")
		if s.Ref != "" {
			args = append(args, "--branch", s.Ref)
		}
	}
	args = append(args, "--", remote, dest)
	if _, err := git(ctx, filepath.Dir(dest), args...); err != nil {
		return "", err
	}
	if s.Sha != "" {
		if _, err := git(ctx, dest, "checkout", "--quiet", "--detach", s.Sha); err != nil {
			return "", err
		}
	}
	head, err := git(ctx, dest, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if s.Sha != "" && head != s.Sha {
		return "", fmt.Errorf("the repository is at %s, not at the pinned %s", head, s.Sha)
	}
	return head, nil
}

// repoPathRe holds what a path in a repository may not, because it would mean more than itself as a sparse-checkout pattern.
var repoPathRe = regexp.MustCompile(`[*?\[\]\\!#\x00-\x1f]`)

// cleanRepoPath is a path inside a repository in its plain form, or an error when it isn't one: "" is the root, which no caller
// wants, and a pattern would select more than the path it spells.
func cleanRepoPath(raw string) (string, error) {
	if raw == "" || strings.HasPrefix(raw, "/") || slicesContainDotDot(raw) || repoPathRe.MatchString(raw) {
		return "", fmt.Errorf("%q is not a plain path inside the repository", raw)
	}
	clean := path.Clean(raw)
	if clean == "." {
		return "", fmt.Errorf("%q is the whole repository, not a path inside it", raw)
	}
	for _, part := range strings.Split(clean, "/") {
		if part != strings.TrimSpace(part) || strings.EqualFold(part, ".git") {
			return "", fmt.Errorf("%q is not a plain path inside the repository", raw)
		}
	}
	return clean, nil
}

// serverFilters asks a server what it can do, without taking any object from it. A server that doesn't filter ignores the request
// with a warning and sends everything, so this has to be known before fetching: afterwards it is too late to not have it.
func serverFilters(ctx context.Context, dir, remote string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-c", "protocol.ext.allow=never", "-c", "protocol.fd.allow=never", "-c", "protocol.version=2", "ls-remote", "--", remote, "HEAD")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "GCM_INTERACTIVE=never", "GIT_TRACE_PACKET=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git ls-remote: can't reach %s: %w", remote, err)
	}
	return filterAdvertised.Match(out), nil
}

// filterAdvertised finds the capability in a packet trace: protocol v2 lists it under fetch, older ones after the first ref.
var filterAdvertised = regexp.MustCompile(`(?m)(fetch=|\\0)[^\n]*\bfilter\b`)

// localBlobs lists the blobs a repository holds here, without asking its server for any.
func localBlobs(ctx context.Context, dir string) ([]string, error) {
	out, err := git(ctx, dir, "cat-file", "--batch-all-objects", "--batch-check=%(objecttype) %(objectname)")
	if err != nil {
		return nil, err
	}
	var blobs []string
	for _, line := range strings.Split(out, "\n") {
		if oid, ok := strings.CutPrefix(line, "blob "); ok {
			blobs = append(blobs, oid)
		}
	}
	return blobs, nil
}

// treeEntries reads `git ls-tree -z`: mode, type and object of each path.
func treeEntries(ctx context.Context, dir string, args ...string) ([][3]string, error) {
	out, err := git(ctx, dir, append([]string{"ls-tree", "-z"}, args...)...)
	if err != nil {
		return nil, err
	}
	var entries [][3]string
	for _, record := range strings.Split(out, "\x00") {
		meta, _, ok := strings.Cut(record, "\t")
		fields := strings.Fields(meta)
		if ok && len(fields) == 3 {
			entries = append(entries, [3]string{fields[0], fields[1], fields[2]})
		}
	}
	return entries, nil
}

// fetchSubdir brings one folder of a repository to dest, and the files named besides it, at a commit, and returns the commit. What
// else the repository holds stays on its server: of the rest only the commit and the listing of its folders are read, never a
// file. A server that can't do this is refused rather than downloaded from in full.
func fetchSubdir(ctx context.Context, s Source, dest string, files []string) (string, error) {
	remote := s.cloneURL()
	if err := checkRemote(remote); err != nil {
		return "", err
	}
	if s.Sha != "" && !shaRe.MatchString(s.Sha) {
		return "", fmt.Errorf("sha %q is not a full commit id", s.Sha)
	}
	if strings.HasPrefix(s.Ref, "-") {
		return "", fmt.Errorf("%q can't be a ref", s.Ref)
	}
	folder, err := cleanRepoPath(s.Path)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	parent := filepath.Dir(dest)
	filters, err := serverFilters(ctx, parent, remote)
	if err != nil {
		return "", err
	}
	if !filters {
		return "", fmt.Errorf("%s can't send part of a repository, and all of it won't be fetched to get %s", remote, folder)
	}
	if _, err := git(ctx, parent, "init", "--quiet", "--", dest); err != nil {
		return "", err
	}
	if _, err := git(ctx, dest, "remote", "add", "origin", remote); err != nil {
		return "", err
	}
	want := "HEAD"
	if s.Sha != "" {
		want = s.Sha
	} else if s.Ref != "" {
		want = s.Ref
	}
	if _, err := git(ctx, dest, "-c", "protocol.version=2", "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--depth=1", "--filter=blob:none", "origin", want); err != nil {
		return "", err
	}
	commit, err := git(ctx, dest, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return "", err
	}
	if s.Sha != "" && commit != s.Sha {
		return "", fmt.Errorf("the repository is at %s, not at the pinned %s", commit, s.Sha)
	}
	// A server that said it filters and sent files anyway shows here, before anything is put in place.
	if blobs, err := localBlobs(ctx, dest); err != nil {
		return "", err
	} else if len(blobs) > 0 {
		return "", fmt.Errorf("%s sent files it wasn't asked for", remote)
	}
	// Listed in the commit rather than found on disk: a link named like a folder would point anywhere.
	found, err := treeEntries(ctx, dest, commit, "--", folder)
	if err != nil {
		return "", err
	}
	if len(found) != 1 || found[0][1] != "tree" {
		return "", fmt.Errorf("%s is not a folder of the repository", folder)
	}
	contents, err := treeEntries(ctx, dest, "-r", commit, "--", folder)
	if err != nil {
		return "", err
	}
	allowed := map[string]bool{}
	for _, e := range contents {
		if e[1] == "blob" {
			allowed[e[2]] = true
		}
	}
	patterns := "/" + folder + "/\n"
	wanted := map[string]bool{}
	for _, f := range files {
		clean, err := cleanRepoPath(f)
		if err != nil {
			return "", err
		}
		found, err := treeEntries(ctx, dest, commit, "--", clean)
		if err != nil {
			return "", err
		}
		// Not a link: it would be copied as what it points at.
		if len(found) != 1 || found[0][1] != "blob" || !strings.HasPrefix(found[0][0], "100") {
			return "", fmt.Errorf("%s is not a file of the repository at %s", clean, commit)
		}
		allowed[found[0][2]] = true
		wanted[clean] = true
		patterns += "/" + clean + "\n"
	}
	gitDir := filepath.Join(dest, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "info"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(gitDir, "info", "sparse-checkout"), []byte(patterns), 0o644); err != nil {
		return "", err
	}
	if _, err := git(ctx, dest, "-c", "core.sparseCheckout=true", "-c", "core.sparseCheckoutCone=false", "checkout", "--quiet", "--detach", commit); err != nil {
		return "", err
	}
	blobs, err := localBlobs(ctx, dest)
	if err != nil {
		return "", err
	}
	for _, oid := range blobs {
		if !allowed[oid] {
			return "", fmt.Errorf("the fetch of %s brought %s, which is outside it", folder, oid)
		}
	}
	err = filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == gitDir {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dest, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, folder+"/") && !wanted[rel] {
			return fmt.Errorf("the checkout of %s holds %s, which is outside it", folder, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return commit, nil
}

// licenseNameRe is what a declared license file may be called. It lands in the plugin's root, where a name such as .mcp.json or
// SKILL.md would be a component of the plugin instead.
var licenseNameRe = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice)([._-][a-z0-9._-]*)?$`)

// cleanLicenseFile checks the licenseFile of an entry, a path from the root of its repository or marketplace.
func cleanLicenseFile(raw string) (string, error) {
	clean, err := cleanRepoPath(raw)
	if err != nil {
		return "", fmt.Errorf("licenseFile: %w", err)
	}
	if !licenseNameRe.MatchString(path.Base(clean)) {
		return "", fmt.Errorf("licenseFile: %q isn't named like a license", raw)
	}
	return clean, nil
}

// addLicenseFile makes sure the license an entry declares is in the plugin's folder. root is what the path starts from, folder the
// plugin's place in it ("" for all of it), dest the plugin's copy. A license already inside comes with the folder; one outside is
// copied to the plugin's root under its own name. Finding a different file there refuses the install.
func addLicenseFile(root, folder, file, dest string) error {
	clean, err := cleanLicenseFile(file)
	if err != nil {
		return err
	}
	if rel, inside := strings.CutPrefix(clean, folder+"/"); folder == "" || inside {
		if folder == "" {
			rel = clean
		}
		if st, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(rel))); err != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("the license file %s is not in the plugin", clean)
		}
		return nil
	}
	src := filepath.Join(root, filepath.FromSlash(clean))
	st, err := os.Lstat(src)
	if err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("the license file %s is not there", clean)
	}
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil || !inside(real, base) {
		return fmt.Errorf("the license file %s is outside what it belongs to", clean)
	}
	if st.Size() > maxManifestBytes {
		return fmt.Errorf("the license file %s is too large", clean)
	}
	target := filepath.Join(dest, path.Base(clean))
	if _, err := os.Lstat(target); err == nil {
		have, errHave := os.ReadFile(target)
		want, errWant := os.ReadFile(src)
		if errHave == nil && errWant == nil && bytes.Equal(have, want) {
			return nil
		}
		return fmt.Errorf("the plugin already has a different %s; the license file %s can't replace it", path.Base(clean), clean)
	}
	return copyFile(src, target, 0o644)
}

// fetchFile reads a marketplace file from a URL.
func fetchFile(ctx context.Context, hc *http.Client, s Source) ([]byte, error) {
	if err := checkRemote(s.URL); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", s.URL, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, maxManifestBytes))
}

// copyTree copies a plugin's folder without its .git. A link stays a link as long as it stays inside; any other refuses the copy,
// since it would hand the plugin a file of this device.
func copyTree(src, dst string) error {
	root, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	files, total := 0, int64(0)
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == ".git" {
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			resolved, err := filepath.EvalSymlinks(p)
			if err != nil || !inside(resolved, root) || filepath.IsAbs(link) {
				return fmt.Errorf("%s links outside the plugin", rel)
			}
			return os.Symlink(link, target)
		case !info.Mode().IsRegular():
			return nil
		}
		files++
		total += info.Size()
		if files > maxTreeFiles || total > maxTreeBytes {
			return errors.New("the plugin is too large")
		}
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		return copyFile(p, target, mode)
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// treeEntry is one path of a folder, as stat sees it.
type treeEntry struct {
	rel  string
	mode fs.FileMode
	size int64
	mod  int64
	link string
}

func listTree(root string) ([]treeEntry, error) {
	var out []treeEntry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		e := treeEntry{rel: filepath.ToSlash(rel), mode: info.Mode(), size: info.Size(), mod: info.ModTime().UnixNano()}
		if info.Mode()&os.ModeSymlink != 0 {
			if e.link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		out = append(out, e)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, err
}

// HashTree identifies a folder's content: paths, whether a file is executable, the bytes of files and the targets of links.
// Approval is bound to this value.
func HashTree(root string) (string, error) {
	list, err := listTree(root)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, e := range list {
		switch {
		case e.link != "":
			fmt.Fprintf(h, "l\x00%s\x00%s\x00", e.rel, e.link)
		case e.mode.IsRegular():
			kind := "f"
			if e.mode&0o111 != 0 {
				kind = "x"
			}
			fmt.Fprintf(h, "%s\x00%s\x00%d\x00", kind, e.rel, e.size)
			f, err := os.Open(filepath.Join(root, filepath.FromSlash(e.rel)))
			if err != nil {
				return "", err
			}
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return "", err
			}
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// treeStamp changes when a folder might have: a cheap look before reading all of it again.
func treeStamp(root string) (string, error) {
	list, err := listTree(root)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, e := range list {
		fmt.Fprintf(h, "%s\x00%o\x00%d\x00%d\x00%s\x00", e.rel, e.mode, e.size, e.mod, e.link)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// MarketplaceManifest is a marketplace.json.
type MarketplaceManifest struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Version     string          `json:"version"`
	Owner       json.RawMessage `json:"owner"`
	Metadata    struct {
		Description string `json:"description"`
		Version     string `json:"version"`
		PluginRoot  string `json:"pluginRoot"`
	} `json:"metadata"`
	Plugins []Entry `json:"plugins"`
}

// Entry is one plugin of a marketplace: its own fields over those of a manifest, which it may also be.
type Entry struct {
	Manifest
	Source json.RawMessage `json:"source"`
	Strict *bool           `json:"strict"`
	// LicenseFile is the entry's license, as a path from the root of its repository or marketplace. It is apart from license, the
	// manifest's name of the license, which is a string in the format every tool reads. An install carries the file in the plugin.
	LicenseFile string `json:"licenseFile,omitempty"`
}

// UnmarshalJSON reads both the embedded manifest and the entry's own fields.
func (e *Entry) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.Manifest); err != nil {
		return err
	}
	var own struct {
		Source      json.RawMessage `json:"source"`
		Strict      *bool           `json:"strict"`
		LicenseFile string          `json:"licenseFile"`
	}
	if err := json.Unmarshal(b, &own); err != nil {
		return err
	}
	e.Source, e.Strict, e.LicenseFile = own.Source, own.Strict, own.LicenseFile
	return nil
}

// parseMarketplace checks a marketplace file. Problems with one entry stay with that entry.
func parseMarketplace(data []byte) (*MarketplaceManifest, error) {
	var m MarketplaceManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", MarketplaceFile, err)
	}
	if !ValidName(m.Name) {
		return nil, fmt.Errorf("the marketplace name %q may use only letters, digits, \".\", \"_\" and \"-\", and must start with a letter or digit", m.Name)
	}
	if m.Description == "" {
		m.Description = m.Metadata.Description
	}
	return &m, nil
}

// reservedNames are ids this client gives its own meaning, or that other tools reserve for what they ship.
var reservedNames = map[string]bool{"local": true, "inline": true, "builtin": true, "skills-dir": true, "synced": true, "npm": true, "pip": true, "uv": true, "cargo": true, "github": true, "gh": true,
	"claude-code-marketplace": true, "claude-code-plugins": true, "claude-plugins-official": true, "anthropic-marketplace": true, "anthropic-plugins": true, "agent-skills": true}

func reservedName(name string, s Source) bool {
	n := strings.ToLower(name)
	if !reservedNames[n] {
		return false
	}
	official := strings.HasPrefix(canonicalURL(s.cloneURL()), "https://github.com/anthropics/")
	return n == "local" || n == "inline" || n == "builtin" || !official
}

// findManifest looks for a marketplace file under a root.
func findManifest(root, custom string) (string, error) {
	if custom != "" {
		if !filepath.IsLocal(filepath.FromSlash(custom)) {
			return "", fmt.Errorf("%s is outside the repository", custom)
		}
		return filepath.Join(root, filepath.FromSlash(custom)), nil
	}
	for _, d := range manifestDirs {
		p := filepath.Join(root, d, MarketplaceFile)
		if isFile(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("no %s/%s here", manifestDirs[1], MarketplaceFile)
}

// entrySource reads an entry's source: a path relative to the marketplace's root, or an object.
func entrySource(e Entry, m *MarketplaceManifest) (rel string, s Source, err error) {
	if len(e.Source) == 0 {
		return "", s, fmt.Errorf("the entry %s has no source", e.Name)
	}
	if e.LicenseFile != "" {
		if _, err := cleanLicenseFile(e.LicenseFile); err != nil {
			return "", s, fmt.Errorf("the entry %s: %w", e.Name, err)
		}
	}
	if e.Source[0] == '"' {
		var str string
		if err := json.Unmarshal(e.Source, &str); err != nil {
			return "", s, err
		}
		if str != "." && !strings.HasPrefix(str, "./") {
			if strings.Contains(str, "/") || m.Metadata.PluginRoot == "" {
				return "", s, fmt.Errorf("the source %q must start with \"./\"", str)
			}
			str = strings.TrimRight(m.Metadata.PluginRoot, "/") + "/" + str
		}
		if slicesContainDotDot(str) {
			return "", s, fmt.Errorf("the source %q leaves the marketplace", str)
		}
		return str, s, nil
	}
	if err := json.Unmarshal(e.Source, &s); err != nil {
		return "", s, err
	}
	switch s.Source {
	case SourceGitHub:
		if !ownerRepoRe.MatchString(s.Repo) {
			return "", s, fmt.Errorf("the repository %q is not owner/repo", s.Repo)
		}
	case SourceURL, SourceSubdir:
		if s.URL == "" {
			return "", s, fmt.Errorf("the source of %s needs a url", e.Name)
		}
		if s.Source == SourceSubdir {
			if _, err := cleanRepoPath(s.Path); err != nil {
				return "", s, fmt.Errorf("the source of %s needs a path inside the repository: %w", e.Name, err)
			}
		}
	default:
		return "", s, fmt.Errorf("this client does not fetch plugins from a source of type %q", s.Source)
	}
	return "", s, nil
}

func slicesContainDotDot(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}
