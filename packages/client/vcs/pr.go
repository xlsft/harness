package vcs

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
)

// Comment is one comment on a PR/MR. All its text comes from the code host: it is external data.
type Comment struct {
	ID        string
	Author    string
	Body      string
	Path      string
	Line      int
	URL       string
	CreatedAt string
	// Resolved: its review thread or discussion is resolved.
	Resolved bool
}

// PRView is the state of one PR/MR with its checks.
type PRView struct {
	PR     *vcsv1.PullRequest
	Checks []*vcsv1.Check
}

// Merge methods of SetAutoMerge; empty leaves the choice to the provider.
const (
	MergeMerge  = "merge"
	MergeSquash = "squash"
	MergeRebase = "rebase"
)

// PRProvider is a Provider that can act on one PR/MR: what the session's PR tools and their monitor need.
type PRProvider interface {
	Provider
	// ViewPR reads the PR/MR with its checks.
	ViewPR(ctx context.Context, repo Repo, number int64) (*PRView, error)
	// PRComments lists review and conversation comments, and counts the unresolved review threads.
	PRComments(ctx context.Context, repo Repo, number int64) (comments []Comment, unresolved int, err error)
	// CheckLog returns the end of a failed check's log; "" when the host has none for that check.
	CheckLog(ctx context.Context, repo Repo, check *vcsv1.Check) (string, error)
	// SetAutoMerge turns merging by itself on (when the host's requirements pass) or off.
	SetAutoMerge(ctx context.Context, repo Repo, number int64, enabled bool, method string) error
}

var prURLRe = regexp.MustCompile(`^(https?://[^/\s]+/\S+?)/(?:-/)?(?:pull|pulls|merge_requests)/(\d+)(?:[/?#]\S*)?$`)

// parsePRRef reads a PR reference: a number ("12", "#12", "!12") or a PR/MR web URL. A number has no repository:
// repo is empty then.
func parsePRRef(ref string) (repo Repo, number int64, err error) {
	ref = strings.TrimSpace(ref)
	if m := prURLRe.FindStringSubmatch(ref); m != nil {
		r, ok := ParseRemote(m[1])
		if !ok {
			return Repo{}, 0, fmt.Errorf("cannot read the repository of %s", ref)
		}
		n, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil || n <= 0 {
			return Repo{}, 0, fmt.Errorf("invalid pull request number in %s", ref)
		}
		return r, n, nil
	}
	n, err := strconv.ParseInt(strings.TrimLeft(ref, "#!"), 10, 64)
	if err != nil || n <= 0 {
		return Repo{}, 0, fmt.Errorf("%q is not a pull request number or URL", ref)
	}
	return Repo{}, n, nil
}

// noProviderError says why no CLI serves host, and what to do about it. detected and fails are what detection found
// per provider, aligned with providers.
func noProviderError(host string, providers []Provider, detected []*vcsv1.DeviceProvider, fails []error) error {
	var msgs []string
	for i, p := range providers {
		var name, hint, other string
		switch p.Type() {
		case vcsv1.ProviderType_PROVIDER_TYPE_GH:
			name, hint, other = "gh", "https://cli.github.com", "gitlab"
		case vcsv1.ProviderType_PROVIDER_TYPE_GLAB:
			name, hint, other = "glab", "https://gitlab.com/gitlab-org/cli", "github"
		default:
			continue
		}
		// A host that names the other code host is not this CLI's business.
		if strings.Contains(host, other) {
			continue
		}
		switch {
		case errors.Is(fails[i], ErrNotInstalled):
			msgs = append(msgs, fmt.Sprintf("%s is not installed (%s); install it, then run `%s auth login --hostname %s`", name, hint, name, host))
		case fails[i] != nil:
			msgs = append(msgs, fmt.Sprintf("%s could not be checked: %v", name, fails[i]))
		case detected[i] != nil:
			msgs = append(msgs, fmt.Sprintf("%s is not logged in to %s; run `%s auth login --hostname %s`", name, host, name, host))
		}
	}
	if len(msgs) == 0 {
		return fmt.Errorf("no pull request provider for %s", host)
	}
	return errors.New(strings.Join(msgs, "; "))
}
