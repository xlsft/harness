package vcs

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// What the CI bot (package cibot) needs of a code host: the PR's diff, posting findings as review comments, replying,
// and asking whether someone has write access.

// LineComment is one finding on a line of the PR's new version.
type LineComment struct {
	Path string
	Line int
	Body string
}

// Review is what a review posts. Commit pins the comments to what was reviewed.
type Review struct {
	Summary  string
	Comments []LineComment
	Commit   string
}

// ReplyTarget says where a reply goes.
type ReplyTarget struct {
	Number int64
	// IsIssue: an issue, not a PR/MR.
	IsIssue bool
	// Thread is the review comment id (GitHub) or discussion id (GitLab) to reply in; empty is a plain comment.
	Thread string
}

// BotHost is a CLI provider as the CI bot uses it.
type BotHost interface {
	// PRDiff is the PR's unified diff.
	PRDiff(ctx context.Context, repo Repo, number int64) (string, error)
	// PostReview posts the summary and the line comments.
	PostReview(ctx context.Context, repo Repo, number int64, r Review) error
	// PostReply comments on a PR, MR or issue.
	PostReply(ctx context.Context, repo Repo, to ReplyTarget, body string) error
	// HasWriteAccess reports whether a login can write to the repository.
	HasWriteAccess(ctx context.Context, repo Repo, login string) (bool, error)
	// PRComments lists what is there, to skip findings already posted.
	PRComments(ctx context.Context, repo Repo, number int64) ([]Comment, int, error)
}

var (
	_ BotHost = (*GH)(nil)
	_ BotHost = (*GLab)(nil)
)

// NewMarker is the marker for what a process without a session posts (the CI bot): the id is hashed with a salt that
// lives as long as the process, so it can be compared to nothing.
func NewMarker(id, kind string) string {
	return buildMarker(sessionRef(loadMarkerSalt(""), id), kind, time.Now())
}

var hunkRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// CommentableLines reads a unified diff: per file, the lines of the new version a review comment can sit on (added and
// context lines inside a hunk).
func CommentableLines(diff string) map[string]map[int]bool {
	out := map[string]map[int]bool{}
	var cur map[int]bool
	line := 0
	inHunk := false
	sc := bufio.NewScanner(strings.NewReader(diff))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		s := sc.Text()
		switch {
		case strings.HasPrefix(s, "diff --git "):
			cur, inHunk = nil, false
		case !inHunk && strings.HasPrefix(s, "+++ "):
			path := strings.TrimSpace(strings.TrimPrefix(s, "+++ "))
			if path == "/dev/null" {
				cur = nil
				continue
			}
			path = strings.TrimPrefix(path, "b/")
			cur = map[int]bool{}
			out[path] = cur
		case strings.HasPrefix(s, "@@ "):
			m := hunkRe.FindStringSubmatch(s)
			if m == nil {
				inHunk = false
				continue
			}
			line, _ = strconv.Atoi(m[1])
			inHunk = true
		case !inHunk || cur == nil:
		case strings.HasPrefix(s, "+"), strings.HasPrefix(s, " "):
			cur[line] = true
			line++
		case strings.HasPrefix(s, "-"), strings.HasPrefix(s, `\`):
		}
	}
	return out
}

// ---- gh ----

func ghOwnerRepo(repo Repo) (string, error) {
	if !strings.Contains(repo.Path, "/") {
		return "", fmt.Errorf("gh: %q is not owner/repo", repo.Path)
	}
	return "repos/" + repo.Path, nil
}

// ghPost sends JSON through `gh api --input`.
func (g *GH) ghPost(ctx context.Context, repo Repo, endpoint string, body any) error {
	path, cleanup, err := jsonInputFile(body)
	if err != nil {
		return err
	}
	defer cleanup()
	res, err := g.runner().Run(ctx, g.bin(), "api", "--hostname", repo.Host, "-X", "POST", endpoint, "--input", path)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return cliError(g.bin(), res)
	}
	return nil
}

// PRDiff runs `gh pr diff`.
func (g *GH) PRDiff(ctx context.Context, repo Repo, number int64) (string, error) {
	res, err := g.runner().Run(ctx, g.bin(), "pr", "diff", strconv.FormatInt(number, 10), "--repo", g.repoArg(repo))
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", cliError(g.bin(), res)
	}
	return string(res.Stdout), nil
}

// PostReview posts one review with its comments: a COMMENT review, which neither approves nor blocks.
func (g *GH) PostReview(ctx context.Context, repo Repo, number int64, r Review) error {
	base, err := ghOwnerRepo(repo)
	if err != nil {
		return err
	}
	comments := make([]map[string]any, 0, len(r.Comments))
	for _, c := range r.Comments {
		comments = append(comments, map[string]any{"path": c.Path, "line": c.Line, "side": "RIGHT", "body": c.Body})
	}
	body := map[string]any{"event": "COMMENT", "body": r.Summary, "comments": comments}
	if r.Commit != "" {
		body["commit_id"] = r.Commit
	}
	return g.ghPost(ctx, repo, fmt.Sprintf("%s/pulls/%d/reviews", base, number), body)
}

// PostReply posts an issue comment, or a reply in a review comment's thread.
func (g *GH) PostReply(ctx context.Context, repo Repo, to ReplyTarget, body string) error {
	base, err := ghOwnerRepo(repo)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/issues/%d/comments", base, to.Number)
	if to.Thread != "" && !to.IsIssue {
		endpoint = fmt.Sprintf("%s/pulls/%d/comments/%s/replies", base, to.Number, url.PathEscape(to.Thread))
	}
	return g.ghPost(ctx, repo, endpoint, map[string]any{"body": body})
}

// HasWriteAccess asks for the login's permission on the repository.
func (g *GH) HasWriteAccess(ctx context.Context, repo Repo, login string) (bool, error) {
	base, err := ghOwnerRepo(repo)
	if err != nil {
		return false, err
	}
	res, err := g.runner().Run(ctx, g.bin(), "api", "--hostname", repo.Host, base+"/collaborators/"+url.PathEscape(login)+"/permission")
	if err != nil {
		return false, err
	}
	if res.ExitCode != 0 {
		return false, cliError(g.bin(), res)
	}
	var p struct {
		Permission string `json:"permission"`
	}
	if err := json.Unmarshal(res.Stdout, &p); err != nil {
		return false, fmt.Errorf("gh api: malformed JSON: %w", err)
	}
	return p.Permission == "admin" || p.Permission == "write" || p.Permission == "maintain", nil
}

// ---- glab ----

// PRDiff rebuilds a unified diff from the merge request's diffs.
func (g *GLab) PRDiff(ctx context.Context, repo Repo, number int64) (string, error) {
	var files []struct {
		OldPath string `json:"old_path"`
		NewPath string `json:"new_path"`
		Diff    string `json:"diff"`
		New     bool   `json:"new_file"`
		Deleted bool   `json:"deleted_file"`
	}
	if err := g.api(ctx, repo.Host, fmt.Sprintf("%s/merge_requests/%d/diffs?per_page=100", glabProject(repo), number), &files); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, f := range files {
		oldName, newName := "a/"+f.OldPath, "b/"+f.NewPath
		if f.New {
			oldName = "/dev/null"
		}
		if f.Deleted {
			newName = "/dev/null"
		}
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- %s\n+++ %s\n%s", f.OldPath, f.NewPath, oldName, newName, f.Diff)
		if !strings.HasSuffix(f.Diff, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String(), nil
}

// PostReview opens a discussion per line comment; one GitLab refuses (the line moved) goes into the summary note, so no
// finding is lost.
func (g *GLab) PostReview(ctx context.Context, repo Repo, number int64, r Review) error {
	mr := fmt.Sprintf("%s/merge_requests/%d", glabProject(repo), number)
	var refs struct {
		DiffRefs *struct {
			BaseSHA  string `json:"base_sha"`
			StartSHA string `json:"start_sha"`
			HeadSHA  string `json:"head_sha"`
		} `json:"diff_refs"`
	}
	if err := g.api(ctx, repo.Host, mr, &refs); err != nil {
		return err
	}
	summary := r.Summary
	for _, c := range r.Comments {
		posted := false
		if refs.DiffRefs != nil {
			err := g.glabPost(ctx, repo, mr+"/discussions", map[string]any{
				"body": c.Body,
				"position": map[string]any{
					"position_type": "text", "base_sha": refs.DiffRefs.BaseSHA, "start_sha": refs.DiffRefs.StartSHA, "head_sha": refs.DiffRefs.HeadSHA,
					"old_path": c.Path, "new_path": c.Path, "new_line": c.Line,
				},
			}, nil)
			posted = err == nil
		}
		if !posted {
			summary += fmt.Sprintf("\n\n`%s:%d`\n\n%s", c.Path, c.Line, c.Body)
		}
	}
	if strings.TrimSpace(summary) == "" {
		return nil
	}
	return g.glabPost(ctx, repo, mr+"/notes", map[string]any{"body": summary}, nil)
}

// PostReply posts a note on a merge request or an issue, or a reply in a discussion.
func (g *GLab) PostReply(ctx context.Context, repo Repo, to ReplyTarget, body string) error {
	kind := "merge_requests"
	if to.IsIssue {
		kind = "issues"
	}
	endpoint := fmt.Sprintf("%s/%s/%d/notes", glabProject(repo), kind, to.Number)
	if to.Thread != "" {
		endpoint = fmt.Sprintf("%s/%s/%d/discussions/%s/notes", glabProject(repo), kind, to.Number, url.PathEscape(to.Thread))
	}
	return g.glabPost(ctx, repo, endpoint, map[string]any{"body": body}, nil)
}

// glabDeveloperAccess is GitLab's access level that can push.
const glabDeveloperAccess = 30

// HasWriteAccess looks the login up and asks for its access level on the project, inherited membership included. Someone
// who is not a member answers 404, which counts as no access.
func (g *GLab) HasWriteAccess(ctx context.Context, repo Repo, login string) (bool, error) {
	var users []struct {
		ID int64 `json:"id"`
	}
	if err := g.api(ctx, repo.Host, "users?username="+url.QueryEscape(login), &users); err != nil {
		return false, err
	}
	if len(users) == 0 {
		return false, nil
	}
	res, err := g.runner().Run(ctx, g.bin(), "api", "--hostname", repo.Host, fmt.Sprintf("%s/members/all/%d", glabProject(repo), users[0].ID))
	if err != nil {
		return false, err
	}
	if res.ExitCode != 0 {
		if strings.Contains(string(res.Stderr), "404") || strings.Contains(string(res.Stdout), "404") {
			return false, nil
		}
		return false, cliError(g.bin(), res)
	}
	var m struct {
		AccessLevel int `json:"access_level"`
	}
	if err := json.Unmarshal(res.Stdout, &m); err != nil {
		return false, fmt.Errorf("glab api: malformed JSON: %w", err)
	}
	return m.AccessLevel >= glabDeveloperAccess, nil
}
