package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Everything git does on a host with credentials goes through here (docs/design/hosts.md, decision 10). The token reaches
// one git process through its environment and an inline credential helper: never argv, a URL or a config file. The agent
// can write to the repository's configuration and hooks, so these commands take nothing from them that could run code or
// see the token.

// Credentials are what the server issued for one repository.
type Credentials struct {
	CloneURL, Username, Password string
}

const (
	gitQueryTimeout = 30 * time.Second
	gitNetTimeout   = 20 * time.Minute
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

// gitEnv is the environment of a git command of the daemon: no global or system configuration, no prompts, and only the
// protocols a code host is reached by (file only when a test allowed it). The credentials are added by run.
func gitEnv(allowFile bool) []string {
	allowed := "https:http"
	if allowFile {
		allowed += ":file"
	}
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_ALLOW_PROTOCOL=" + allowed, "LC_ALL=C", "GIT_ASKPASS=true"}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "PATH", "HOME", "TMPDIR", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy":
			env = append(env, kv)
		}
	}
	return env
}

// Git runs the daemon's git commands.
type Git struct {
	// AllowFile lets file:// remotes through (tests).
	AllowFile bool
}

func (g Git) run(ctx context.Context, dir string, timeout time.Duration, cred *Credentials, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	full := append(append([]string(nil), safeConfig...), args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(gitEnv(g.AllowFile), env...)
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

var safeKey = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// cacheDir is the host's clone of a repository, which sessions' worktrees come from. One per repository under the root,
// and so one per worker on a team host.
func cacheDir(root, repositoryKey string) string {
	sum := sha256.Sum256([]byte(repositoryKey))
	name := strings.Trim(safeKey.ReplaceAllString(repositoryKey, "-"), "-.")
	if len(name) > 80 {
		name = name[len(name)-80:]
	}
	return filepath.Join(root, "repos", name+"-"+hex.EncodeToString(sum[:4]))
}

// Checkout says what a session's working tree should be.
type Checkout struct {
	RepositoryKey string
	// Base is what a new branch starts from ("": the default branch). With Existing, the branch's pull request base.
	Base string
	// Branch is the session's branch; with Existing it is on the remote already.
	Branch   string
	Existing bool
	// Fresh makes a clone of its own instead of a worktree of the host's cache.
	Fresh bool
	// Dir is where it goes; it must not exist.
	Dir string
	// Name and Email sign the commits made there.
	Name, Email string
}

// Prepared is the working tree that came out.
type Prepared struct {
	Dir, Branch, Base string
}

// Prepare makes the working tree. What is in Dir is removed again on failure.
func (g Git) Prepare(ctx context.Context, root string, c Checkout, cred Credentials) (*Prepared, error) {
	if err := os.MkdirAll(filepath.Dir(c.Dir), 0o700); err != nil {
		return nil, err
	}
	out := &Prepared{Dir: c.Dir, Branch: c.Branch, Base: c.Base}
	fail := func(err error) (*Prepared, error) {
		_ = os.RemoveAll(c.Dir)
		return nil, err
	}
	if c.Fresh {
		if _, err := g.run(ctx, filepath.Dir(c.Dir), gitNetTimeout, &cred, nil, "clone", "--no-recurse-submodules", "--no-checkout", "--", cred.CloneURL, c.Dir); err != nil {
			return fail(err)
		}
		base, err := g.resolveBase(ctx, c.Dir, c, &cred)
		if err != nil {
			return fail(err)
		}
		out.Base = base
		start := "origin/" + base
		if c.Existing {
			start = "origin/" + c.Branch
		}
		if _, err := g.run(ctx, c.Dir, gitNetTimeout, nil, nil, "checkout", "-B", c.Branch, "--no-track", start); err != nil {
			return fail(err)
		}
	} else {
		cache := cacheDir(root, c.RepositoryKey)
		if err := g.ensureCache(ctx, cache, &cred); err != nil {
			return fail(err)
		}
		base, err := g.resolveBase(ctx, cache, c, &cred)
		if err != nil {
			return fail(err)
		}
		out.Base = base
		start := "origin/" + base
		if c.Existing {
			start = "origin/" + c.Branch
		}
		// -B and --force: the branch of a previous session's worktree that was removed by hand must not stand in the way.
		if _, err := g.run(ctx, cache, gitQueryTimeout, nil, nil, "worktree", "prune"); err != nil {
			return fail(err)
		}
		if _, err := g.run(ctx, cache, gitNetTimeout, nil, nil, "worktree", "add", "--force", "--no-track", "-B", c.Branch, c.Dir, start); err != nil {
			return fail(err)
		}
	}
	for k, v := range map[string]string{"user.name": c.Name, "user.email": c.Email} {
		if v == "" {
			continue
		}
		if _, err := g.run(ctx, c.Dir, gitQueryTimeout, nil, nil, "config", k, v); err != nil {
			return fail(err)
		}
	}
	return out, nil
}

func (g Git) ensureCache(ctx context.Context, cache string, cred *Credentials) error {
	if _, err := os.Stat(filepath.Join(cache, ".git")); err != nil {
		if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
			return err
		}
		_ = os.RemoveAll(cache)
		if _, err := g.run(ctx, filepath.Dir(cache), gitNetTimeout, cred, nil, "clone", "--no-recurse-submodules", "--no-checkout", "--", cred.CloneURL, cache); err != nil {
			return err
		}
		return nil
	}
	if _, err := g.run(ctx, cache, gitNetTimeout, cred, nil, "fetch", "--prune", "--no-recurse-submodules", cred.CloneURL, "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return err
	}
	return nil
}

// resolveBase names the branch work starts from: the one asked for, or the remote's default.
func (g Git) resolveBase(ctx context.Context, dir string, c Checkout, cred *Credentials) (string, error) {
	base := c.Base
	if base == "" {
		out, err := g.run(ctx, dir, gitNetTimeout, cred, nil, "ls-remote", "--symref", cred.CloneURL, "HEAD")
		if err != nil {
			return "", err
		}
		for _, line := range strings.Split(out, "\n") {
			if rest, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
				base, _, _ = strings.Cut(rest, "\t")
				break
			}
		}
		if base == "" {
			return "", errors.New("the repository has no default branch")
		}
	}
	if err := checkRefName(base); err != nil {
		return "", err
	}
	if c.Existing {
		if err := checkRefName(c.Branch); err != nil {
			return "", err
		}
	}
	return base, nil
}

// checkRefName refuses what would read as an option or a revision expression.
func checkRefName(name string) error {
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, " \t\n~^:?*[\\") || strings.Contains(name, "..") {
		return fmt.Errorf("%q is not a branch name", name)
	}
	return nil
}

// LandResult says what became of a session's work.
type LandResult struct {
	// Committed: there were uncommitted changes, and they were committed.
	Committed bool
	// Pushed: the remote branch moved.
	Pushed bool
	// Head is the commit that is on the remote now, when Pushed.
	Head string
	// Subject is the first commit's subject of the branch, for a pull request's title.
	Subject string
	// Ahead counts the commits on top of the base.
	Ahead int
}

// Land commits what the session left in the working tree and pushes the branch, never forcing and never to the base.
func (g Git) Land(ctx context.Context, dir, branch, base, message string, cred Credentials) (*LandResult, error) {
	if err := checkRefName(branch); err != nil {
		return nil, err
	}
	if branch == base {
		return nil, fmt.Errorf("refusing to push to the base branch %s", base)
	}
	cur, err := g.run(ctx, dir, gitQueryTimeout, nil, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cur) != branch {
		// The agent moved to another branch: what goes up is still only ever the session's branch, from HEAD, and only if
		// HEAD isn't the base.
		if strings.TrimSpace(cur) == base {
			return nil, fmt.Errorf("the working tree is on the base branch %s; nothing to push", base)
		}
	}
	// A rewritten URL would take the token somewhere else, and the agent can write this repository's configuration.
	if out, _ := g.run(ctx, dir, gitQueryTimeout, nil, nil, "config", "--name-only", "--get-regexp", `^url\..*\.(insteadof|pushinsteadof)$`); strings.TrimSpace(out) != "" {
		return nil, errors.New("the repository's configuration rewrites remote URLs; refusing to push with credentials")
	}
	res := &LandResult{}
	status, err := g.run(ctx, dir, gitQueryTimeout, nil, nil, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(status) != "" {
		if _, err := g.run(ctx, dir, gitQueryTimeout, nil, nil, "add", "-A"); err != nil {
			return nil, err
		}
		if _, err := g.run(ctx, dir, gitQueryTimeout, nil, nil, "commit", "--no-verify", "-m", message); err != nil {
			return nil, err
		}
		res.Committed = true
	}
	head, err := g.run(ctx, dir, gitQueryTimeout, nil, nil, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	head = strings.TrimSpace(head)
	if n, err := g.run(ctx, dir, gitQueryTimeout, nil, nil, "rev-list", "--count", "origin/"+base+"..HEAD"); err == nil {
		res.Ahead, _ = strconv.Atoi(strings.TrimSpace(n))
	}
	if res.Ahead == 0 {
		return res, nil
	}
	if out, err := g.run(ctx, dir, gitQueryTimeout, nil, nil, "log", "--reverse", "--format=%s", "origin/"+base+"..HEAD"); err == nil {
		res.Subject, _, _ = strings.Cut(out, "\n")
	}
	remote, err := g.run(ctx, dir, gitNetTimeout, &cred, nil, "ls-remote", "--heads", cred.CloneURL, "refs/heads/"+branch)
	if err != nil {
		return nil, err
	}
	if fields := strings.Fields(remote); len(fields) > 0 && fields[0] == head {
		return res, nil
	}
	if _, err := g.run(ctx, dir, gitNetTimeout, &cred, nil, "push", cred.CloneURL, "HEAD:refs/heads/"+branch); err != nil {
		return nil, err
	}
	res.Pushed, res.Head = true, head
	return res, nil
}

// Fetch brings the remote's branches into a working tree's refs without touching the tree, so that the agent, which holds no
// credentials, sees what the code host has.
func (g Git) Fetch(ctx context.Context, dir string, cred Credentials) error {
	_, err := g.run(ctx, dir, gitNetTimeout, &cred, nil, "fetch", "--prune", "--no-recurse-submodules", cred.CloneURL, "+refs/heads/*:refs/remotes/origin/*")
	return err
}

// Remove takes a session's working tree away, and its worktree registration.
func (g Git) Remove(ctx context.Context, root, repositoryKey, dir string) error {
	cache := cacheDir(root, repositoryKey)
	if _, err := os.Stat(cache); err == nil {
		_, _ = g.run(ctx, cache, gitQueryTimeout, nil, nil, "worktree", "remove", "--force", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if _, err := os.Stat(cache); err == nil {
		_, _ = g.run(ctx, cache, gitQueryTimeout, nil, nil, "worktree", "prune")
	}
	return nil
}
