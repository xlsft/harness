package worktree

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var prPathRe = regexp.MustCompile(`^/(.+?)(?:/-)?/(?:pull|pulls|merge_requests)/([0-9]+)(?:/.*)?$`)

// PullRequestRef is a pull request as a user or the model names it.
type PullRequestRef struct {
	Number int
	// Repo is the "owner/name" path of a URL, empty for a bare number.
	Repo string
}

// ParsePullRequest reads "123", "#123" or the URL of a pull or merge request.
func ParsePullRequest(s string) (PullRequestRef, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(strings.TrimPrefix(s, "#")); err == nil {
		if n <= 0 {
			return PullRequestRef{}, fmt.Errorf("%w: pull request %q", ErrInvalidRef, s)
		}
		return PullRequestRef{Number: n}, nil
	}
	u, err := url.Parse(s)
	if err == nil && u.Host != "" {
		if m := prPathRe.FindStringSubmatch(u.Path); m != nil {
			if n, err := strconv.Atoi(m[2]); err == nil && n > 0 {
				return PullRequestRef{Number: n, Repo: m[1]}, nil
			}
		}
	}
	return PullRequestRef{}, fmt.Errorf("%w: %q is neither a pull request number nor its URL", ErrInvalidRef, s)
}

// fetchPullRequest fetches the head of a pull request from the repository's remote and returns its commit. GitHub and
// Forgejo keep it under refs/pull, GitLab under refs/merge-requests; a bare remote tells nothing about which one it is, so
// both are tried.
func fetchPullRequest(ctx context.Context, dir string, pr PullRequestRef) (string, error) {
	remote := "origin"
	if configValue(ctx, dir, "remote.origin.url") == "" {
		out, err := lines(ctx, dir, "remote")
		if err != nil || len(out) == 0 {
			return "", fmt.Errorf("%w: the repository has no remote to fetch pull request %d from", ErrInvalidRef, pr.Number)
		}
		remote = out[0]
	}
	if pr.Repo != "" && !sameRepo(configValue(ctx, dir, "remote."+remote+".url"), pr.Repo) {
		return "", fmt.Errorf("%w: that pull request is of %s, and this repository's %s is elsewhere", ErrInvalidRef, pr.Repo, remote)
	}
	var last error
	for _, ref := range []string{fmt.Sprintf("refs/pull/%d/head", pr.Number), fmt.Sprintf("refs/merge-requests/%d/head", pr.Number)} {
		if _, err := git(ctx, AddTimeout, dir, "fetch", "--quiet", "--no-tags", remote, ref); err != nil {
			last = err
			continue
		}
		sha, err := git(ctx, QueryTimeout, dir, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(sha), nil
	}
	return "", fmt.Errorf("could not fetch pull request %d from %s: %w", pr.Number, remote, last)
}

// sameRepo compares a remote's URL with an "owner/name" path, whatever the URL's form (https, ssh, scp-like).
func sameRepo(remoteURL, repo string) bool {
	path := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(remoteURL), "/"), ".git"))
	want := strings.ToLower(strings.Trim(repo, "/"))
	return strings.HasSuffix(path, "/"+want) || strings.HasSuffix(path, ":"+want)
}
