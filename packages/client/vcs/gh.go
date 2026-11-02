package vcs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
)

// GH is the gh (GitHub CLI) device provider: github.com and GitHub Enterprise Server hosts.
type GH struct {
	Runner *Runner
	// Binary defaults to "gh".
	Binary string
}

func (g *GH) Type() vcsv1.ProviderType { return vcsv1.ProviderType_PROVIDER_TYPE_GH }

func (g *GH) bin() string {
	if g.Binary != "" {
		return g.Binary
	}
	return "gh"
}

func (g *GH) runner() *Runner {
	if g.Runner != nil {
		return g.Runner
	}
	return &Runner{}
}

// Detect runs `gh --version` and `gh auth status --json hosts` (falling back to the text output on
// gh versions without --json).
func (g *GH) Detect(ctx context.Context) (*vcsv1.DeviceProvider, error) {
	r := g.runner()
	res, err := r.Run(ctx, g.bin(), "--version")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, cliError(g.bin(), res)
	}
	p := &vcsv1.DeviceProvider{Type: g.Type(), Version: firstLine(res.Stdout)}

	res, err = r.Run(ctx, g.bin(), "auth", "status", "--json", "hosts")
	if err != nil {
		return nil, err
	}
	if hosts, ok := parseGHAuthJSON(res.Stdout); ok && res.ExitCode == 0 {
		p.Hosts = hosts
		return p, nil
	}
	// Older gh: no --json. The text status goes to stderr (stdout on some versions); exit 1 means
	// a host has problems, which still leaves the healthy hosts listed.
	res, err = r.Run(ctx, g.bin(), "auth", "status")
	if err != nil {
		return nil, err
	}
	p.Hosts = parseGHAuthText(string(res.Stdout) + "\n" + string(res.Stderr))
	return p, nil
}

type ghAuthAccount struct {
	State  string `json:"state"`
	Active bool   `json:"active"`
	Host   string `json:"host"`
	Login  string `json:"login"`
}

// parseGHAuthJSON parses `gh auth status --json hosts`: {"hosts": {"<host>": [account...]}}. Per host
// it keeps the active account with state "success" (or the first successful one).
func parseGHAuthJSON(b []byte) ([]*vcsv1.DeviceProviderHost, bool) {
	var doc struct {
		Hosts map[string][]ghAuthAccount `json:"hosts"`
	}
	if err := json.Unmarshal(b, &doc); err != nil || doc.Hosts == nil {
		return nil, false
	}
	var out []*vcsv1.DeviceProviderHost
	for host, accounts := range doc.Hosts {
		var pick *ghAuthAccount
		for i := range accounts {
			a := &accounts[i]
			if a.State != "success" {
				continue
			}
			if pick == nil || (a.Active && !pick.Active) {
				pick = a
			}
		}
		if pick == nil {
			continue
		}
		h := pick.Host
		if h == "" {
			h = host
		}
		out = append(out, &vcsv1.DeviceProviderHost{Host: strings.ToLower(h), Login: pick.Login})
	}
	sortHosts(out)
	return out, true
}

// "Logged in to github.com account octocat (keyring)" (gh >= 2.40) or
// "Logged in to github.com as octocat (/path/hosts.yml)" (older).
var ghLoggedInRe = regexp.MustCompile(`Logged in to (\S+?) (?:account|as) (\S+)`)

func parseGHAuthText(s string) []*vcsv1.DeviceProviderHost {
	s = stripANSI(s)
	seen := map[string]bool{}
	var out []*vcsv1.DeviceProviderHost
	for _, line := range strings.Split(s, "\n") {
		m := ghLoggedInRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		host := strings.ToLower(m[1])
		if seen[host] { // gh lists the active account of a host first
			continue
		}
		seen[host] = true
		out = append(out, &vcsv1.DeviceProviderHost{Host: host, Login: m[2]})
	}
	sortHosts(out)
	return out
}

const ghPRFields = "number,title,url,state,isDraft,headRefName,baseRefName,author,reviewDecision," +
	"mergeable,mergeStateStatus,autoMergeRequest,updatedAt,statusCheckRollup"

// Snapshot runs `gh pr list -R host/owner/repo --head <branch> --state all --json ...`. Checks come
// from statusCheckRollup in the same call (the data `gh pr checks` shows), so one invocation covers
// both PR state and CI.
func (g *GH) Snapshot(ctx context.Context, repo Repo, branch string) (*Snapshot, error) {
	if branch == "" {
		return nil, errors.New("no branch")
	}
	res, err := g.runner().Run(ctx, g.bin(), "pr", "list",
		"--repo", repo.Host+"/"+repo.Path,
		"--head", branch,
		"--state", "all",
		"--limit", "10",
		"--json", ghPRFields)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, cliError(g.bin(), res)
	}
	var prs []ghPR
	if err := json.Unmarshal(res.Stdout, &prs); err != nil {
		return nil, fmt.Errorf("gh pr list: malformed JSON: %w", err)
	}
	snap := &Snapshot{Checks: map[string]*vcsv1.ChecksList{}}
	for _, p := range prs {
		if p.Number <= 0 || (p.HeadRefName != "" && p.HeadRefName != branch) {
			continue
		}
		pr, checks := p.toProto(repo.Key)
		snap.PullRequests = append(snap.PullRequests, pr)
		snap.Checks[CheckKey(repo.Key, pr.Number)] = &vcsv1.ChecksList{Checks: checks}
	}
	sortPullRequests(snap.PullRequests)
	return snap, nil
}

type ghPR struct {
	Number           int64  `json:"number"`
	Title            string `json:"title"`
	URL              string `json:"url"`
	State            string `json:"state"`
	IsDraft          bool   `json:"isDraft"`
	HeadRefName      string `json:"headRefName"`
	BaseRefName      string `json:"baseRefName"`
	ReviewDecision   string `json:"reviewDecision"`
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	UpdatedAt        string `json:"updatedAt"`
	Author           struct {
		Login string `json:"login"`
	} `json:"author"`
	AutoMergeRequest  json.RawMessage `json:"autoMergeRequest"`
	StatusCheckRollup []ghCheck       `json:"statusCheckRollup"`
}

// ghCheck is a statusCheckRollup item: a CheckRun (GitHub Actions, apps) or a StatusContext
// (commit statuses).
type ghCheck struct {
	Typename string `json:"__typename"`
	// CheckRun
	Name         string `json:"name"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	DetailsURL   string `json:"detailsUrl"`
	WorkflowName string `json:"workflowName"`
	StartedAt    string `json:"startedAt"`
	CompletedAt  string `json:"completedAt"`
	// StatusContext
	Context     string `json:"context"`
	State       string `json:"state"`
	TargetURL   string `json:"targetUrl"`
	Description string `json:"description"`
}

var ghJobIDRe = regexp.MustCompile(`/job/(\d+)`)

func (p ghPR) toProto(repoKey string) (*vcsv1.PullRequest, []*vcsv1.Check) {
	pr := &vcsv1.PullRequest{
		RepositoryKey: repoKey, Number: p.Number, Title: p.Title, Url: p.URL,
		HeadBranch: p.HeadRefName, BaseBranch: p.BaseRefName, Author: p.Author.Login,
		Draft:            p.IsDraft,
		ReviewState:      ghReviewState(p.ReviewDecision),
		Mergeable:        strings.EqualFold(p.Mergeable, "MERGEABLE"),
		HasConflicts:     strings.EqualFold(p.Mergeable, "CONFLICTING") || strings.EqualFold(p.MergeStateStatus, "DIRTY"),
		AutoMergeEnabled: len(p.AutoMergeRequest) > 0 && string(p.AutoMergeRequest) != "null",
		UpdatedAt:        utc(p.UpdatedAt),
	}
	switch strings.ToUpper(p.State) {
	case "OPEN":
		pr.State = agentv1.PullRequest_STATE_OPEN
		if p.IsDraft {
			pr.State = agentv1.PullRequest_STATE_DRAFT
		}
	case "MERGED":
		pr.State = agentv1.PullRequest_STATE_MERGED
	case "CLOSED":
		pr.State = agentv1.PullRequest_STATE_CLOSED
	}
	checks := make([]*vcsv1.Check, 0, len(p.StatusCheckRollup))
	for _, c := range p.StatusCheckRollup {
		checks = append(checks, c.toProto())
	}
	pr.Checks = summarize(checks)
	return pr, checks
}

func (c ghCheck) toProto() *vcsv1.Check {
	if c.Typename == "StatusContext" || (c.Context != "" && c.Name == "") {
		return &vcsv1.Check{
			Id: c.Context, Name: c.Context, Url: c.TargetURL, Summary: c.Description,
			State: ghStatusState(c.State),
		}
	}
	id := c.Name
	if m := ghJobIDRe.FindStringSubmatch(c.DetailsURL); m != nil {
		id = m[1]
	}
	out := &vcsv1.Check{
		Id: id, Name: c.Name, Url: c.DetailsURL, Workflow: c.WorkflowName,
		State: ghCheckRunState(c.Status, c.Conclusion),
	}
	if out.State != vcsv1.CheckState_CHECK_STATE_QUEUED && out.State != vcsv1.CheckState_CHECK_STATE_RUNNING {
		out.DurationMs = durationMs(c.StartedAt, c.CompletedAt)
	}
	return out
}

func ghCheckRunState(status, conclusion string) vcsv1.CheckState {
	switch strings.ToUpper(status) {
	case "QUEUED", "WAITING", "PENDING", "REQUESTED":
		return vcsv1.CheckState_CHECK_STATE_QUEUED
	case "IN_PROGRESS":
		return vcsv1.CheckState_CHECK_STATE_RUNNING
	}
	switch strings.ToUpper(conclusion) {
	case "SUCCESS":
		return vcsv1.CheckState_CHECK_STATE_SUCCESS
	case "FAILURE", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED":
		return vcsv1.CheckState_CHECK_STATE_FAILURE
	case "CANCELLED":
		return vcsv1.CheckState_CHECK_STATE_CANCELLED
	case "SKIPPED":
		return vcsv1.CheckState_CHECK_STATE_SKIPPED
	case "NEUTRAL", "STALE":
		return vcsv1.CheckState_CHECK_STATE_NEUTRAL
	case "":
		if strings.EqualFold(status, "COMPLETED") {
			return vcsv1.CheckState_CHECK_STATE_NEUTRAL
		}
		return vcsv1.CheckState_CHECK_STATE_QUEUED
	}
	return vcsv1.CheckState_CHECK_STATE_UNSPECIFIED
}

func ghStatusState(state string) vcsv1.CheckState {
	switch strings.ToUpper(state) {
	case "SUCCESS":
		return vcsv1.CheckState_CHECK_STATE_SUCCESS
	case "FAILURE", "ERROR":
		return vcsv1.CheckState_CHECK_STATE_FAILURE
	case "PENDING", "EXPECTED":
		return vcsv1.CheckState_CHECK_STATE_RUNNING
	}
	return vcsv1.CheckState_CHECK_STATE_UNSPECIFIED
}

func ghReviewState(decision string) string {
	switch strings.ToUpper(decision) {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes_requested"
	case "REVIEW_REQUIRED":
		return "review_required"
	}
	return "none"
}
