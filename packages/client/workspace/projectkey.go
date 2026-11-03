package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"path/filepath"
	"strings"
)

// NormalizeRemote converts a git remote URL into the repository part of a
// project_key (harness.common.v1.Scope): "host/owner/repo", lowercased,
// without scheme, user info, port and a trailing ".git". It accepts URL
// forms (https://, ssh://, git://, file://) and scp-like forms
// (git@host:owner/repo.git). It returns "" for inputs it cannot parse and
// for local paths.
func NormalizeRemote(remote string) string {
	r := strings.TrimSpace(remote)
	if r == "" {
		return ""
	}
	var host, path string
	if i := strings.Index(r, "://"); i >= 0 {
		u, err := url.Parse(r)
		if err != nil || u.Scheme == "file" {
			return ""
		}
		host, path = u.Hostname(), u.Path
	} else {
		// scp-like: [user@]host:path. A colon before any slash is required;
		// otherwise it is a local path.
		colon := strings.Index(r, ":")
		slash := strings.Index(r, "/")
		if colon <= 0 || (slash >= 0 && slash < colon) {
			return ""
		}
		host, path = r[:colon], r[colon+1:]
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		// Windows drive letters ("C:\repo") are local paths.
		if len(host) == 1 {
			return ""
		}
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	path = strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	if host == "" || path == "" {
		return ""
	}
	return strings.ToLower(host + "/" + path)
}

// PathKey returns the fallback project_key for a directory without a git
// remote: "path:" + lowercase hex SHA-256 of the absolute path.
func PathKey(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	sum := sha256.Sum256([]byte(abs))
	return "path:" + hex.EncodeToString(sum[:])
}

// GitRemote returns the URL of the "origin" remote, or of the first remote
// when there is no origin, or "".
func GitRemote(ctx context.Context, dir string) string {
	if out, err := run(ctx, dir, "git", "remote", "get-url", "origin"); err == nil {
		if u := strings.TrimSpace(out); u != "" {
			return u
		}
	}
	out, err := run(ctx, dir, "git", "remote")
	if err != nil {
		return ""
	}
	for _, name := range strings.Fields(out) {
		if u, err := run(ctx, dir, "git", "remote", "get-url", name); err == nil {
			return strings.TrimSpace(u)
		}
	}
	return ""
}

// Repo returns the normalized git remote of dir ("github.com/owner/repo"),
// or "" outside a repository or without a usable remote.
func Repo(ctx context.Context, dir string) string {
	return NormalizeRemote(GitRemote(ctx, dir))
}

// ProjectKey returns the project_key for dir following the rules of
// harness.common.v1.Scope: the normalized remote when there is one,
// otherwise "path:"+sha256 of the repository root (or of dir outside a
// repository).
func ProjectKey(ctx context.Context, dir string) string {
	if k := Repo(ctx, dir); k != "" {
		return k
	}
	if root := GitRoot(ctx, dir); root != "" {
		return PathKey(root)
	}
	return PathKey(dir)
}

// RemoteWebURL converts a git remote URL into the repository's browsable web URL: "https://host/owner/repo"
// for scp-like (git@host:owner/repo.git), ssh://, git:// and https:// remotes, keeping the case of the path,
// without user info and a trailing ".git". The port of an http(s) remote is kept (its web server listens
// there); ssh and git ports are dropped. An http:// remote stays http. It returns "" for local paths and
// inputs it cannot parse.
func RemoteWebURL(remote string) string {
	r := strings.TrimSpace(remote)
	if r == "" {
		return ""
	}
	scheme, host, path := "https", "", ""
	if i := strings.Index(r, "://"); i >= 0 {
		u, err := url.Parse(r)
		if err != nil {
			return ""
		}
		switch strings.ToLower(u.Scheme) {
		case "http":
			scheme, host = "http", u.Host
		case "https":
			host = u.Host
		case "ssh", "git", "git+ssh", "ssh+git":
			host = u.Hostname()
		default:
			return ""
		}
		path = u.Path
	} else {
		// scp-like: [user@]host:path (see NormalizeRemote).
		colon := strings.Index(r, ":")
		slash := strings.Index(r, "/")
		if colon <= 0 || (slash >= 0 && slash < colon) {
			return ""
		}
		host, path = r[:colon], r[colon+1:]
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		if len(host) == 1 {
			return ""
		}
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	path = strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	path = strings.Trim(strings.TrimSuffix(path, ".git"), "/")
	if host == "" || path == "" {
		return ""
	}
	return scheme + "://" + host + "/" + path
}
