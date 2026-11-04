package vcs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
)

// GLab is the glab (GitLab CLI) device provider: gitlab.com and self-managed GitLab hosts.
//
// PR state and CI come from the GitLab REST API through `glab api` (the same data `glab mr view -F json`
// and `glab ci status` show), because its JSON is stable across glab versions while the formatted
// commands are not.
type GLab struct {
	Runner *Runner
	// Binary defaults to "glab".
	Binary string
}

func (g *GLab) Type() vcsv1.ProviderType { return vcsv1.ProviderType_PROVIDER_TYPE_GLAB }

func (g *GLab) bin() string {
	if g.Binary != "" {
		return g.Binary
	}
	return "glab"
}

func (g *GLab) runner() *Runner {
	if g.Runner != nil {
		return g.Runner
	}
	return &Runner{}
}

// Detect runs `glab --version` and `glab auth status --all` (plain `glab auth status` on versions
// without --all). glab has no JSON auth status, so the text is parsed; only hosts with a
// "Logged in to <host> as <user>" line count.
func (g *GLab) Detect(ctx context.Context) (*vcsv1.DeviceProvider, error) {
	r := g.runner()
	res, err := r.Run(ctx, g.bin(), "--version")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, cliError(g.bin(), res)
	}
	p := &vcsv1.DeviceProvider{Type: g.Type(), Version: firstLine(res.Stdout)}

	res, err = r.Run(ctx, g.bin(), "auth", "status", "--all")
	if err != nil {
		return nil, err
	}
	out := string(res.Stdout) + "\n" + string(res.Stderr)
	if res.ExitCode != 0 && strings.Contains(out, "unknown flag") {
		if res, err = r.Run(ctx, g.bin(), "auth", "status"); err != nil {
			return nil, err
		}
		out = string(res.Stdout) + "\n" + string(res.Stderr)
	}
	// A non-zero exit means some host failed; the healthy ones are still listed.
	p.Hosts = parseGLabAuthText(out)
	return p, nil
}

var glabLoggedInRe = regexp.MustCompile(`Logged in to (\S+?) as (\S+)`)

func parseGLabAuthText(s string) []*vcsv1.DeviceProviderHost {
	s = stripANSI(s)
	seen := map[string]bool{}
	var out []*vcsv1.DeviceProviderHost
	for _, line := range strings.Split(s, "\n") {
		m := glabLoggedInRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		host := strings.ToLower(m[1])
		if seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, &vcsv1.DeviceProviderHost{Host: host, Login: m[2]})
	}
	sortHosts(out)
	return out
}

// Snapshot lists the MRs with the branch as source, then the head pipeline and its jobs of the most
// relevant MR (the open one, or the latest).
func (g *GLab) Snapshot(ctx context.Context, repo Repo, branch string) (*Snapshot, error) {
	if branch == "" {
		return nil, errors.New("no branch")
	}
	project := glabProject(repo)
	q := url.Values{}
	q.Set("source_branch", branch)
	q.Set("state", "all")
	q.Set("order_by", "updated_at")
	q.Set("per_page", "10")
	var mrs []glabMR
	if err := g.api(ctx, repo.Host, project+"/merge_requests?"+q.Encode(), &mrs); err != nil {
		return nil, err
	}
	snap := &Snapshot{Checks: map[string]*vcsv1.ChecksList{}}
	for _, m := range mrs {
		if m.IID <= 0 || (m.SourceBranch != "" && m.SourceBranch != branch) {
			continue
		}
		snap.PullRequests = append(snap.PullRequests, m.toProto(repo.Key))
	}
	sortPullRequests(snap.PullRequests)
	if len(snap.PullRequests) == 0 {
		return snap, nil
	}

	// Checks for the most relevant MR. The list endpoint has no pipeline; the single MR has it.
	top := snap.PullRequests[0]
	var detail glabMR
	if err := g.api(ctx, repo.Host, project+"/merge_requests/"+strconv.FormatInt(top.Number, 10), &detail); err != nil {
		return nil, err
	}
	checks, err := g.pipelineChecks(ctx, repo, &detail)
	if err != nil {
		return nil, err
	}
	top.Checks = summarize(checks)
	if detail.IID == top.Number { // the single MR is fresher than the list item
		fresh := detail.toProto(repo.Key)
		fresh.Checks = top.Checks
		snap.PullRequests[0] = fresh
		top = fresh
	}
	snap.Checks[CheckKey(repo.Key, top.Number)] = &vcsv1.ChecksList{Checks: checks}
	return snap, nil
}

// api runs `glab api --hostname <host> <endpoint>` and decodes the JSON response into v.
func (g *GLab) api(ctx context.Context, host, endpoint string, v any) error {
	res, err := g.runner().Run(ctx, g.bin(), "api", "--hostname", host, endpoint)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return cliError(g.bin(), res)
	}
	if err := json.Unmarshal(res.Stdout, v); err != nil {
		return fmt.Errorf("glab api %s: malformed JSON: %w", strings.SplitN(endpoint, "?", 2)[0], err)
	}
	return nil
}

type glabMR struct {
	IID                       int64  `json:"iid"`
	Title                     string `json:"title"`
	WebURL                    string `json:"web_url"`
	State                     string `json:"state"`
	Draft                     bool   `json:"draft"`
	WorkInProgress            bool   `json:"work_in_progress"`
	SourceBranch              string `json:"source_branch"`
	TargetBranch              string `json:"target_branch"`
	UpdatedAt                 string `json:"updated_at"`
	HasConflicts              bool   `json:"has_conflicts"`
	MergeStatus               string `json:"merge_status"`
	DetailedMergeStatus       string `json:"detailed_merge_status"`
	MergeWhenPipelineSucceeds bool   `json:"merge_when_pipeline_succeeds"`
	Author                    struct {
		Username string `json:"username"`
	} `json:"author"`
	HeadPipeline *struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
		WebURL string `json:"web_url"`
	} `json:"head_pipeline"`
}

func (m glabMR) toProto(repoKey string) *vcsv1.PullRequest {
	draft := m.Draft || m.WorkInProgress
	pr := &vcsv1.PullRequest{
		RepositoryKey: repoKey, Number: m.IID, Title: m.Title, Url: m.WebURL,
		HeadBranch: m.SourceBranch, BaseBranch: m.TargetBranch, Author: m.Author.Username,
		Draft:            draft,
		HasConflicts:     m.HasConflicts,
		Mergeable:        m.DetailedMergeStatus == "mergeable" || (m.DetailedMergeStatus == "" && m.MergeStatus == "can_be_merged"),
		AutoMergeEnabled: m.MergeWhenPipelineSucceeds,
		ReviewState:      glabReviewState(m.DetailedMergeStatus),
		UpdatedAt:        utc(m.UpdatedAt),
	}
	switch m.State {
	case "opened":
		pr.State = agentv1.PullRequest_STATE_OPEN
		if draft {
			pr.State = agentv1.PullRequest_STATE_DRAFT
		}
	case "merged":
		pr.State = agentv1.PullRequest_STATE_MERGED
	case "closed", "locked":
		pr.State = agentv1.PullRequest_STATE_CLOSED
	}
	return pr
}

func glabReviewState(detailed string) string {
	switch detailed {
	case "not_approved":
		return "review_required"
	case "requested_changes":
		return "changes_requested"
	}
	return "none"
}

type glabJob struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Stage        string   `json:"stage"`
	Status       string   `json:"status"`
	WebURL       string   `json:"web_url"`
	Duration     *float64 `json:"duration"`
	AllowFailure bool     `json:"allow_failure"`
}

func (j glabJob) toProto() *vcsv1.Check {
	c := &vcsv1.Check{
		Id: strconv.FormatInt(j.ID, 10), Name: j.Name, Url: j.WebURL, Workflow: j.Stage,
		State: glabJobState(j.Status, j.AllowFailure),
	}
	if j.Duration != nil && *j.Duration > 0 {
		c.DurationMs = int64(*j.Duration * 1000)
	}
	return c
}

func glabJobState(status string, allowFailure bool) vcsv1.CheckState {
	switch status {
	case "created", "waiting_for_resource", "preparing", "pending", "scheduled", "waiting_for_callback":
		return vcsv1.CheckState_CHECK_STATE_QUEUED
	case "running", "canceling":
		return vcsv1.CheckState_CHECK_STATE_RUNNING
	case "success":
		return vcsv1.CheckState_CHECK_STATE_SUCCESS
	case "failed":
		if allowFailure {
			return vcsv1.CheckState_CHECK_STATE_NEUTRAL
		}
		return vcsv1.CheckState_CHECK_STATE_FAILURE
	case "canceled":
		return vcsv1.CheckState_CHECK_STATE_CANCELLED
	case "skipped":
		return vcsv1.CheckState_CHECK_STATE_SKIPPED
	case "manual":
		// A manual job waits for a person, not for CI; treating it as pending would poll fast forever.
		return vcsv1.CheckState_CHECK_STATE_NEUTRAL
	}
	return vcsv1.CheckState_CHECK_STATE_UNSPECIFIED
}
