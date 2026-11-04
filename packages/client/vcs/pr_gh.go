package vcs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
)

var _ PRProvider = (*GH)(nil)

func (g *GH) repoArg(repo Repo) string { return repo.Host + "/" + repo.Path }

// ViewPR runs `gh pr view <n> --repo host/owner/repo --json ...`.
func (g *GH) ViewPR(ctx context.Context, repo Repo, number int64) (*PRView, error) {
	res, err := g.runner().Run(ctx, g.bin(), "pr", "view", strconv.FormatInt(number, 10),
		"--repo", g.repoArg(repo), "--json", ghPRFields)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, cliError(g.bin(), res)
	}
	var p ghPR
	if err := json.Unmarshal(res.Stdout, &p); err != nil {
		return nil, fmt.Errorf("gh pr view: malformed JSON: %w", err)
	}
	if p.Number <= 0 {
		return nil, fmt.Errorf("gh pr view: no pull request %d in %s", number, repo.Key)
	}
	pr, checks := p.toProto(repo.Key)
	return &PRView{PR: pr, Checks: checks}, nil
}

// One GraphQL call has what no `gh pr` command shows: review threads and whether they are resolved. The newest
// entries are what a watcher is after, hence last: rather than pages.
const ghCommentsQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(last: 100) {
        nodes {
          isResolved
          comments(last: 20) {
            nodes { id body path line url createdAt author { login } }
          }
        }
      }
      comments(last: 30) {
        nodes { id body url createdAt author { login } }
      }
      reviews(last: 30) {
        nodes { id body state url submittedAt author { login } }
      }
    }
  }
}`

type ghComment struct {
	ID          string `json:"id"`
	Body        string `json:"body"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	URL         string `json:"url"`
	CreatedAt   string `json:"createdAt"`
	SubmittedAt string `json:"submittedAt"`
	State       string `json:"state"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
}

func (c ghComment) toComment(resolved bool) Comment {
	at := c.CreatedAt
	if at == "" {
		at = c.SubmittedAt
	}
	return Comment{ID: c.ID, Author: c.Author.Login, Body: c.Body, Path: c.Path, Line: c.Line, URL: c.URL, CreatedAt: utc(at), Resolved: resolved}
}

// PRComments runs `gh api graphql`.
func (g *GH) PRComments(ctx context.Context, repo Repo, number int64) ([]Comment, int, error) {
	owner, name, ok := strings.Cut(repo.Path, "/")
	if !ok {
		return nil, 0, fmt.Errorf("gh: %q is not owner/repo", repo.Path)
	}
	res, err := g.runner().Run(ctx, g.bin(), "api", "graphql", "--hostname", repo.Host,
		"-F", "owner="+owner, "-F", "name="+name, "-F", "number="+strconv.FormatInt(number, 10), "-f", "query="+ghCommentsQuery)
	if err != nil {
		return nil, 0, err
	}
	if res.ExitCode != 0 {
		return nil, 0, cliError(g.bin(), res)
	}
	var doc struct {
		Data struct {
			Repository struct {
				PullRequest *struct {
					ReviewThreads struct {
						Nodes []struct {
							IsResolved bool `json:"isResolved"`
							Comments   struct {
								Nodes []ghComment `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
					Comments struct {
						Nodes []ghComment `json:"nodes"`
					} `json:"comments"`
					Reviews struct {
						Nodes []ghComment `json:"nodes"`
					} `json:"reviews"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Stdout, &doc); err != nil {
		return nil, 0, fmt.Errorf("gh api graphql: malformed JSON: %w", err)
	}
	pr := doc.Data.Repository.PullRequest
	if pr == nil {
		return nil, 0, errors.New("gh api graphql: no such pull request")
	}
	var out []Comment
	unresolved := 0
	for _, t := range pr.ReviewThreads.Nodes {
		if !t.IsResolved {
			unresolved++
		}
		for _, c := range t.Comments.Nodes {
			out = append(out, c.toComment(t.IsResolved))
		}
	}
	for _, c := range pr.Comments.Nodes {
		out = append(out, c.toComment(false))
	}
	for _, c := range pr.Reviews.Nodes {
		// A review's inline comments are listed above; the review itself counts when it says something, or asks
		// for changes without a word.
		silent := strings.TrimSpace(c.Body) == ""
		if strings.EqualFold(c.State, "PENDING") || (silent && !strings.EqualFold(c.State, "CHANGES_REQUESTED")) {
			continue
		}
		if silent {
			c.Body = "(requested changes)"
		}
		out = append(out, c.toComment(false))
	}
	return out, unresolved, nil
}

var numericRe = regexp.MustCompile(`^\d+$`)

// CheckLog runs `gh run view --job <id> --log-failed`. Only jobs of GitHub Actions have logs there: other checks
// (commit statuses, external apps) have none and return "".
func (g *GH) CheckLog(ctx context.Context, repo Repo, check *vcsv1.Check) (string, error) {
	if !numericRe.MatchString(check.GetId()) {
		return "", nil
	}
	res, err := g.runner().Run(ctx, g.bin(), "run", "view", "--repo", g.repoArg(repo), "--job", check.GetId(), "--log-failed")
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", cliError(g.bin(), res)
	}
	return stripANSI(string(res.Stdout)), nil
}

// SetAutoMerge runs `gh pr merge <n> --auto --<method>` or `--disable-auto`. gh needs a method when it cannot ask;
// without one it takes what the repository allows, a merge commit first.
func (g *GH) SetAutoMerge(ctx context.Context, repo Repo, number int64, enabled bool, method string) error {
	args := []string{"pr", "merge", strconv.FormatInt(number, 10), "--repo", g.repoArg(repo)}
	if !enabled {
		args = append(args, "--disable-auto")
	} else {
		if method == "" {
			var err error
			if method, err = g.allowedMethod(ctx, repo); err != nil {
				return err
			}
		}
		args = append(args, "--auto", "--"+method)
	}
	res, err := g.runner().Run(ctx, g.bin(), args...)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return cliError(g.bin(), res)
	}
	return nil
}

func (g *GH) allowedMethod(ctx context.Context, repo Repo) (string, error) {
	res, err := g.runner().Run(ctx, g.bin(), "repo", "view", g.repoArg(repo), "--json", "mergeCommitAllowed,squashMergeAllowed,rebaseMergeAllowed")
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", cliError(g.bin(), res)
	}
	var r struct {
		Merge  bool `json:"mergeCommitAllowed"`
		Squash bool `json:"squashMergeAllowed"`
		Rebase bool `json:"rebaseMergeAllowed"`
	}
	if err := json.Unmarshal(res.Stdout, &r); err != nil {
		return "", fmt.Errorf("gh repo view: malformed JSON: %w", err)
	}
	switch {
	case r.Merge:
		return MergeMerge, nil
	case r.Squash:
		return MergeSquash, nil
	case r.Rebase:
		return MergeRebase, nil
	}
	return "", fmt.Errorf("%s allows no merge method", repo.Key)
}
