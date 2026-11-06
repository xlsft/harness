package plugins

import (
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
}

// UnmarshalJSON reads both the embedded manifest and the entry's own fields.
func (e *Entry) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.Manifest); err != nil {
		return err
	}
	var own struct {
		Source json.RawMessage `json:"source"`
		Strict *bool           `json:"strict"`
	}
	if err := json.Unmarshal(b, &own); err != nil {
		return err
	}
	e.Source, e.Strict = own.Source, own.Strict
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
		if s.Source == SourceSubdir && (s.Path == "" || slicesContainDotDot(s.Path)) {
			return "", s, fmt.Errorf("the source of %s needs a path inside the repository", e.Name)
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
