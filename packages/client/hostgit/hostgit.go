// Package hostgit runs git with credentials the harness server issued for one repository (docs/design/hosts.md, decisions
// 10, 12 and 22): on an always-on host for its checkouts, fetches and pushes, and on a laptop for the push of a session that
// moves to a host. The token reaches one git process through its environment and an inline credential helper: never argv, a
// URL or a config file. An agent can write to the repository's configuration and hooks, so these commands take nothing from
// them that could run code or see the token.
package hostgit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Credentials are what the server issued for one repository.
type Credentials struct {
	CloneURL, Username, Password string
}

const (
	// QueryTimeout bounds a local command; NetTimeout one that reaches the code host.
	QueryTimeout = 30 * time.Second
	NetTimeout   = 20 * time.Minute
)

// credentialHelper answers `get` from the environment and ignores `store` and `erase`. Nothing here is secret.
const credentialHelper = `!f() { test "$1" = get && printf 'username=%s\npassword=%s\n' "$HARNESS_GIT_USERNAME" "$HARNESS_GIT_PASSWORD"; }; f`

// safeConfig overrides what a repository's own configuration could use to run a program or to hand the token over.
// Command-line values go last, and the empty helper clears the list of helpers a repository may have set.
var safeConfig = []string{
	"-c", "core.hooksPath=" + os.DevNull,
	"-c", "core.fsmonitor=false",
	"-c", "core.sshCommand=",
	"-c", "core.gitProxy=",
	"-c", "core.askPass=",
	"-c", "http.extraHeader=",
	"-c", "http.proxy=",
	"-c", "credential.helper=",
	"-c", "credential.helper=" + credentialHelper,
	"-c", "credential.useHttpPath=false",
	"-c", "commit.gpgSign=false",
	"-c", "tag.gpgSign=false",
	"-c", "submodule.recurse=false",
}

// env is the environment of a git command: no global or system configuration, no prompts, and only the protocols a code
// host is reached by (file only when a test allowed it). The credentials are added by Run.
func env(allowFile bool) []string {
	allowed := "https:http"
	if allowFile {
		allowed += ":file"
	}
	out := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_ALLOW_PROTOCOL=" + allowed, "LC_ALL=C", "GIT_ASKPASS=true"}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "PATH", "HOME", "TMPDIR", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy":
			out = append(out, kv)
		}
	}
	return out
}

// Git runs hardened git commands.
type Git struct {
	// AllowFile lets file:// remotes through (tests).
	AllowFile bool
}

// Run runs one git command in dir. A failure carries the first line of stderr, with the token taken out.
func (g Git) Run(ctx context.Context, dir string, timeout time.Duration, cred *Credentials, extraEnv []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	full := append(append([]string(nil), safeConfig...), args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(env(g.AllowFile), extraEnv...)
	if cred != nil {
		cmd.Env = append(cmd.Env, "HARNESS_GIT_USERNAME="+cred.Username, "HARNESS_GIT_PASSWORD="+cred.Password)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if cred != nil && cred.Password != "" {
			msg = strings.ReplaceAll(msg, cred.Password, "***")
		}
		if i := strings.IndexByte(msg, '\n'); i > 0 {
			msg = msg[:i]
		}
		if msg == "" {
			msg = err.Error()
		}
		return string(out), fmt.Errorf("git %s: %s", args[0], msg)
	}
	return string(out), nil
}

// CheckRefName refuses what would read as an option or a revision expression.
func CheckRefName(name string) error {
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, " \t\n~^:?*[\\") || strings.Contains(name, "..") {
		return fmt.Errorf("%q is not a branch name", name)
	}
	return nil
}

// RewritesURLs reports whether the repository's configuration rewrites remote URLs: a rewritten URL would take the token
// somewhere else.
func (g Git) RewritesURLs(ctx context.Context, dir string) bool {
	out, _ := g.Run(ctx, dir, QueryTimeout, nil, nil, "config", "--name-only", "--get-regexp", `^url\..*\.(insteadof|pushinsteadof)$`)
	return strings.TrimSpace(out) != ""
}

// ErrRewrites is the refusal of a push from a repository whose configuration rewrites remote URLs.
var ErrRewrites = errors.New("the repository's configuration rewrites remote URLs; refusing to push with credentials")

// PushCommit pushes a commit to a branch of the repository, never forcing and never to base. It returns false when the
// branch is at that commit already.
func (g Git) PushCommit(ctx context.Context, dir, commit, branch, base string, cred Credentials) (bool, error) {
	if err := CheckRefName(branch); err != nil {
		return false, err
	}
	if branch == base || !strings.HasPrefix(branch, "harness/") {
		return false, fmt.Errorf("refusing to push to %s: only harness/ branches, never the base", branch)
	}
	if g.RewritesURLs(ctx, dir) {
		return false, ErrRewrites
	}
	remote, err := g.Run(ctx, dir, NetTimeout, &cred, nil, "ls-remote", "--heads", cred.CloneURL, "refs/heads/"+branch)
	if err != nil {
		return false, err
	}
	if fields := strings.Fields(remote); len(fields) > 0 && fields[0] == commit {
		return false, nil
	}
	if _, err := g.Run(ctx, dir, NetTimeout, &cred, nil, "push", "--no-verify", cred.CloneURL, commit+":refs/heads/"+branch); err != nil {
		return false, err
	}
	return true, nil
}

// DeleteBranch deletes a harness/ branch on the code host. It is the one thing here that removes work, so it takes only
// a branch the caller made, and only after the user said so (decision 32).
func (g Git) DeleteBranch(ctx context.Context, dir, branch string, cred Credentials) error {
	if err := CheckRefName(branch); err != nil {
		return err
	}
	if !strings.HasPrefix(branch, "harness/") {
		return fmt.Errorf("refusing to delete %s: only harness/ branches", branch)
	}
	if g.RewritesURLs(ctx, dir) {
		return ErrRewrites
	}
	remote, err := g.Run(ctx, dir, NetTimeout, &cred, nil, "ls-remote", "--heads", cred.CloneURL, "refs/heads/"+branch)
	if err != nil {
		return err
	}
	if strings.TrimSpace(remote) == "" {
		return nil
	}
	_, err = g.Run(ctx, dir, NetTimeout, &cred, nil, "push", "--no-verify", cred.CloneURL, "--delete", "refs/heads/"+branch)
	return err
}
