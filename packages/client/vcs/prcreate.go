package vcs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Opening a PR: the facts of the session's branch, the push, and the call to whichever provider covers the host.

// PRInput is a PR to open. Head is on the repository itself: forks are not handled here.
type PRInput struct {
	Head, Base, Title, Body string
	Draft                   bool
	SessionID               string
}

// ErrPRExists: the branch already has an open PR for that base.
var ErrPRExists = errors.New("a pull request for this branch already exists")

// PRCreator is a provider that can open PRs.
type PRCreator interface {
	CreatePR(ctx context.Context, repo Repo, in PRInput) (*vcsv1.PullRequest, error)
}

const (
	// maxPRBody stays under the 65536 characters GitHub takes.
	maxPRBody      = 60000
	gitTimeout     = 30 * time.Second
	gitPushTimeout = 5 * time.Minute
	// What the draft sees of the branch at most.
	maxFactLog  = 8000
	maxFactStat = 4000
	maxFactDiff = 60000
)

// runGit runs git in dir without a prompt and returns stdout; a failure carries the first line of stderr.
func runGit(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
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

// PRFacts is what the session's branch would propose.
type PRFacts struct {
	Repo Repo
	Head string
	Base string
	// Commits ahead of the base, the log and diff that come with them, cut to size.
	Commits int
	Log     string
	Stat    string
	Diff    string
	// Subjects are the commit subjects, oldest first.
	Subjects []string
	// Dirty: uncommitted changes the PR will not carry. NeedsPush: commits the remote does not have.
	Dirty, NeedsPush bool
	// Provider is "server", "gh" or "glab"; ProviderProblem says why there is none.
	Provider        string
	ProviderProblem string
}

// Facts reads the session's branch against base ("" = the repository's default branch).
func (t *Tracker) Facts(ctx context.Context, base string) (*PRFacts, error) {
	if err := t.enabled(); err != nil {
		return nil, err
	}
	repo, err := t.localRepo(ctx)
	if err != nil {
		return nil, err
	}
	head := t.o.Branch(ctx, t.o.Dir)
	if head == "" || head == "HEAD" {
		return nil, fmt.Errorf("%s is not on a branch: switch to one to open a pull request", t.o.Dir)
	}
	if base == "" {
		base = workspace.DefaultBranch(ctx, t.o.Dir)
	}
	if base == "" {
		base = "main"
	}
	if head == base {
		return nil, fmt.Errorf("%s is the base branch: create a branch for this work first", head)
	}
	f := &PRFacts{Repo: repo, Head: head, Base: base}
	ref := "origin/" + base
	if _, err := runGit(ctx, t.o.Dir, gitTimeout, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
		ref = base
		if _, err := runGit(ctx, t.o.Dir, gitTimeout, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
			return nil, fmt.Errorf("the base branch %s does not exist here", base)
		}
	}
	span := ref + "..HEAD"
	if out, err := runGit(ctx, t.o.Dir, gitTimeout, "rev-list", "--count", span); err == nil {
		f.Commits, _ = strconv.Atoi(strings.TrimSpace(out))
	}
	if f.Commits == 0 {
		return nil, fmt.Errorf("%s has no commits on top of %s: commit the work first", head, base)
	}
	if out, err := runGit(ctx, t.o.Dir, gitTimeout, "log", "--reverse", "--format=%s", span); err == nil {
		f.Subjects = strings.Split(strings.TrimSpace(out), "\n")
	}
	if out, err := runGit(ctx, t.o.Dir, gitTimeout, "log", "--reverse", "--format=%h %s%n%b", span); err == nil {
		f.Log = truncateBytes(strings.TrimSpace(out), maxFactLog)
	}
	if out, err := runGit(ctx, t.o.Dir, gitTimeout, "diff", "--stat", ref+"...HEAD"); err == nil {
		f.Stat = truncateBytes(strings.TrimSpace(out), maxFactStat)
	}
	if out, err := runGit(ctx, t.o.Dir, gitTimeout, "diff", ref+"...HEAD"); err == nil {
		f.Diff = truncateBytes(out, maxFactDiff)
	}
	if out, err := runGit(ctx, t.o.Dir, gitTimeout, "status", "--porcelain"); err == nil {
		f.Dirty = strings.TrimSpace(out) != ""
	}
	f.NeedsPush = true
	if remote, err := runGit(ctx, t.o.Dir, gitTimeout, "rev-parse", "--verify", "--quiet", "origin/"+head+"^{commit}"); err == nil {
		if local, err := runGit(ctx, t.o.Dir, gitTimeout, "rev-parse", "HEAD"); err == nil {
			f.NeedsPush = strings.TrimSpace(remote) != strings.TrimSpace(local)
		}
	}
	prov, _, err := t.provider(ctx, repo, true)
	switch {
	case err != nil:
		f.ProviderProblem = err.Error()
	default:
		f.Provider = providerLabel(prov)
	}
	return f, nil
}

func providerLabel(p Provider) string {
	switch p.Type() {
	case vcsv1.ProviderType_PROVIDER_TYPE_GH:
		return "gh"
	case vcsv1.ProviderType_PROVIDER_TYPE_GLAB:
		return "glab"
	}
	return "server"
}

// CreatePRRequest is what the user kept of a draft.
type CreatePRRequest struct {
	Title, Body, Base string
	Draft, Monitor    bool
}

// CreatedPR says what CreatePR did.
type CreatedPR struct {
	URL     string
	Number  int64
	Title   string
	Existed bool
	Pushed  bool
}

// CreatePR pushes the session's branch when the remote lacks commits, opens the PR, and binds it to the session. An open
// PR of the branch is bound and returned as it is. The push never forces.
func (t *Tracker) CreatePR(ctx context.Context, in CreatePRRequest) (*CreatedPR, error) {
	in.Title = strings.TrimSpace(strings.ReplaceAll(in.Title, "\n", " "))
	if in.Title == "" {
		return nil, errors.New("a pull request needs a title")
	}
	if r := []rune(in.Body); len(r) > maxPRBody {
		in.Body = string(r[:maxPRBody])
	}
	f, err := t.Facts(ctx, in.Base)
	if err != nil {
		return nil, err
	}
	prov, _, err := t.provider(ctx, f.Repo, true)
	if err != nil {
		return nil, err
	}
	creator, ok := prov.(PRCreator)
	if !ok {
		return nil, fmt.Errorf("the %s provider cannot open pull requests", providerLabel(prov))
	}
	out := &CreatedPR{}
	pr := t.openPROf(ctx, prov, f.Repo, f.Head)
	if pr != nil {
		out.Existed = true
	} else {
		if f.NeedsPush {
			if _, err := runGit(ctx, t.o.Dir, gitPushTimeout, "push", "--set-upstream", "origin", "HEAD:refs/heads/"+f.Head); err != nil {
				return nil, fmt.Errorf("pushing %s failed: %w", f.Head, err)
			}
			out.Pushed = true
		}
		pr, err = creator.CreatePR(ctx, f.Repo, PRInput{Head: f.Head, Base: f.Base, Title: in.Title, Body: in.Body, Draft: in.Draft, SessionID: t.o.SessionID})
		if errors.Is(err, ErrPRExists) {
			if pr = t.openPROf(ctx, prov, f.Repo, f.Head); pr != nil {
				out.Existed = true
				err = nil
			}
		}
		if err != nil {
			return nil, err
		}
	}
	out.URL, out.Number, out.Title = pr.GetUrl(), pr.GetNumber(), pr.GetTitle()
	t.adopt(ctx, f.Repo, pr)
	if in.Monitor {
		if _, err := t.SetMonitor(ctx, true); err != nil {
			t.log.Warn("the new pull request cannot be watched", "pr", pr.GetNumber(), "err", err)
		}
	}
	return out, nil
}

// openPROf is the branch's open PR, nil when it has none or the lookup fails (the create call then says what is wrong).
func (t *Tracker) openPROf(ctx context.Context, prov Provider, repo Repo, branch string) *vcsv1.PullRequest {
	snap, err := prov.Snapshot(ctx, repo, branch)
	if err != nil {
		t.log.Debug("looking for the branch's pull request failed", "err", err)
		return nil
	}
	for _, pr := range snap.PullRequests {
		if isOpen(pr) {
			return pr
		}
	}
	return nil
}

// adopt binds a PR the session just created, reading it back when it can.
func (t *Tracker) adopt(ctx context.Context, repo Repo, pr *vcsv1.PullRequest) {
	status, err := t.read(ctx, repo, pr.GetNumber())
	if err != nil {
		t.log.Debug("reading the new pull request failed", "pr", pr.GetNumber(), "err", err)
		status = &PRStatus{PRSummary: summaryOf(&PRView{PR: pr}, 0), Repo: repo.Key}
	}
	t.bind(repo, status)
}

// ---- gh ----

var ghPRURLRe = regexp.MustCompile(`https?://\S+/pull/\d+`)

// CreatePR runs `gh pr create`. The head is named, so gh never asks to push or to pick a fork.
func (g *GH) CreatePR(ctx context.Context, repo Repo, in PRInput) (*vcsv1.PullRequest, error) {
	args := []string{"pr", "create", "--repo", g.repoArg(repo), "--head", in.Head, "--base", in.Base, "--title", in.Title, "--body", in.Body}
	if in.Draft {
		args = append(args, "--draft")
	}
	res, err := g.runner().Run(ctx, g.bin(), args...)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		if strings.Contains(string(res.Stderr), "already exists") {
			return nil, ErrPRExists
		}
		return nil, cliError(g.bin(), res)
	}
	url := ghPRURLRe.FindString(string(res.Stdout))
	if url == "" {
		return nil, fmt.Errorf("gh pr create: no pull request URL in its output: %s", firstLine(res.Stdout))
	}
	_, number, err := parsePRRef(url)
	if err != nil {
		return nil, err
	}
	view, err := g.ViewPR(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	return view.PR, nil
}

// ---- glab ----

// glabPost sends JSON through `glab api --input`: nested fields do not survive -f.
func (g *GLab) glabPost(ctx context.Context, repo Repo, endpoint string, body any, v any) error {
	path, cleanup, err := jsonInputFile(body)
	if err != nil {
		return err
	}
	defer cleanup()
	res, err := g.runner().Run(ctx, g.bin(), "api", "--hostname", repo.Host, "-X", "POST", endpoint, "-H", "Content-Type: application/json", "--input", path)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return cliError(g.bin(), res)
	}
	if v == nil {
		return nil
	}
	if err := json.Unmarshal(res.Stdout, v); err != nil {
		return fmt.Errorf("glab api %s: malformed JSON: %w", endpoint, err)
	}
	return nil
}

// jsonInputFile writes body to a private temp file for `--input`; the text never goes on a command line for others to see.
func jsonInputFile(body any) (string, func(), error) {
	data, err := json.Marshal(body)
	if err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp("", "harness-vcs-*.json")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if _, err := f.Write(data); err != nil {
		f.Close()
		cleanup()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return f.Name(), cleanup, nil
}

// CreatePR posts to the merge request API. GitLab's draft state is a title prefix.
func (g *GLab) CreatePR(ctx context.Context, repo Repo, in PRInput) (*vcsv1.PullRequest, error) {
	title := in.Title
	if in.Draft && !strings.HasPrefix(strings.ToLower(title), "draft:") {
		title = "Draft: " + title
	}
	var mr glabMR
	err := g.glabPost(ctx, repo, glabProject(repo)+"/merge_requests", map[string]any{
		"source_branch": in.Head, "target_branch": in.Base, "title": title, "description": in.Body,
	}, &mr)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "409") {
			return nil, ErrPRExists
		}
		return nil, err
	}
	if mr.IID <= 0 {
		return nil, errors.New("glab api: the merge request came back without an iid")
	}
	return mr.toProto(repo.Key), nil
}
