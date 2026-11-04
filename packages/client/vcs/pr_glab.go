package vcs

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
)

var _ PRProvider = (*GLab)(nil)

func glabProject(repo Repo) string { return "projects/" + url.PathEscape(repo.Path) }

// pipelineChecks reads the jobs of the MR's head pipeline.
func (g *GLab) pipelineChecks(ctx context.Context, repo Repo, detail *glabMR) ([]*vcsv1.Check, error) {
	checks := []*vcsv1.Check{}
	pl := detail.HeadPipeline
	if pl == nil || pl.ID <= 0 {
		return checks, nil
	}
	var jobs []glabJob
	if err := g.api(ctx, repo.Host, glabProject(repo)+"/pipelines/"+strconv.FormatInt(pl.ID, 10)+"/jobs?per_page=100", &jobs); err != nil {
		return nil, err
	}
	for _, j := range jobs {
		checks = append(checks, j.toProto())
	}
	if len(checks) == 0 { // a pipeline without visible jobs: report the pipeline itself
		checks = append(checks, &vcsv1.Check{
			Id: strconv.FormatInt(pl.ID, 10), Name: "pipeline", Url: pl.WebURL,
			State: glabJobState(pl.Status, false),
		})
	}
	return checks, nil
}

// ViewPR reads the MR and the jobs of its head pipeline through `glab api`.
func (g *GLab) ViewPR(ctx context.Context, repo Repo, number int64) (*PRView, error) {
	var detail glabMR
	if err := g.api(ctx, repo.Host, glabProject(repo)+"/merge_requests/"+strconv.FormatInt(number, 10), &detail); err != nil {
		return nil, err
	}
	if detail.IID <= 0 {
		return nil, fmt.Errorf("glab api: no merge request %d in %s", number, repo.Key)
	}
	checks, err := g.pipelineChecks(ctx, repo, &detail)
	if err != nil {
		return nil, err
	}
	pr := detail.toProto(repo.Key)
	pr.Checks = summarize(checks)
	return &PRView{PR: pr, Checks: checks}, nil
}

type glabDiscussion struct {
	Notes []struct {
		ID         int64  `json:"id"`
		Body       string `json:"body"`
		System     bool   `json:"system"`
		Resolvable bool   `json:"resolvable"`
		Resolved   bool   `json:"resolved"`
		CreatedAt  string `json:"created_at"`
		Author     struct {
			Username string `json:"username"`
		} `json:"author"`
		Position *struct {
			NewPath string `json:"new_path"`
			NewLine int    `json:"new_line"`
		} `json:"position"`
	} `json:"notes"`
}

// PRComments reads the MR's discussions; a discussion with an unresolved resolvable note counts as unresolved.
// System notes ("added 1 commit") are not comments.
func (g *GLab) PRComments(ctx context.Context, repo Repo, number int64) ([]Comment, int, error) {
	var ds []glabDiscussion
	if err := g.api(ctx, repo.Host, glabProject(repo)+"/merge_requests/"+strconv.FormatInt(number, 10)+"/discussions?per_page=100", &ds); err != nil {
		return nil, 0, err
	}
	var out []Comment
	unresolved := 0
	for _, d := range ds {
		open := false
		for _, n := range d.Notes {
			if n.Resolvable && !n.Resolved {
				open = true
			}
		}
		if open {
			unresolved++
		}
		for _, n := range d.Notes {
			if n.System {
				continue
			}
			c := Comment{ID: strconv.FormatInt(n.ID, 10), Author: n.Author.Username, Body: n.Body, CreatedAt: utc(n.CreatedAt), Resolved: n.Resolvable && n.Resolved}
			if p := n.Position; p != nil {
				c.Path, c.Line = p.NewPath, p.NewLine
			}
			out = append(out, c)
		}
	}
	return out, unresolved, nil
}

// CheckLog reads a job's trace. A check that is not a job (a pipeline without visible jobs) may answer 404, which
// the caller treats like no log.
func (g *GLab) CheckLog(ctx context.Context, repo Repo, check *vcsv1.Check) (string, error) {
	if !numericRe.MatchString(check.GetId()) {
		return "", nil
	}
	res, err := g.runner().Run(ctx, g.bin(), "api", "--hostname", repo.Host, glabProject(repo)+"/jobs/"+check.GetId()+"/trace")
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", cliError(g.bin(), res)
	}
	return stripANSI(string(res.Stdout)), nil
}

// SetAutoMerge asks GitLab to merge when the pipeline succeeds (through the REST API, as everywhere in this provider,
// because its form is stable across glab versions) or cancels that. That call has no rebase method.
func (g *GLab) SetAutoMerge(ctx context.Context, repo Repo, number int64, enabled bool, method string) error {
	mr := glabProject(repo) + "/merge_requests/" + strconv.FormatInt(number, 10)
	var args []string
	if enabled {
		if method == MergeRebase {
			return fmt.Errorf("GitLab's auto-merge has no rebase method: use merge or squash")
		}
		args = []string{"api", "--hostname", repo.Host, "-X", "PUT", mr + "/merge", "-f", "merge_when_pipeline_succeeds=true"}
		if method == MergeSquash {
			args = append(args, "-f", "squash=true")
		}
	} else {
		args = []string{"api", "--hostname", repo.Host, "-X", "POST", mr + "/cancel_merge_when_pipeline_succeeds"}
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
