// Package vcs implements the device providers of harness.vcs.v1.VcsService: gh (GitHub CLI) and glab
// (GitLab CLI). For a session it detects which CLIs are installed and logged in, maps the session's
// git remote to a repository on one of their hosts, polls the pull/merge request and CI state of the
// current branch through the CLI, and uploads it with ReportDeviceProviders and ReportDeviceSnapshot.
//
// The CLIs run without a shell, with a timeout, empty stdin and prompts, pagers and colors disabled.
// Tokens never leave the CLIs: only the parsed state is uploaded.
package vcs

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Repo is a repository on a code host, derived from a git remote.
type Repo struct {
	// Key is host/path, lowercased (the repository_key / project_key normalization).
	Key string
	// Host is the code host ("github.com", a GHES or self-managed GitLab host).
	Host string
	// Path is owner/repo, or group/subgroup/project on GitLab.
	Path string
}

// hostAliases maps SSH endpoints to the host the CLIs are logged in to.
var hostAliases = map[string]string{
	"ssh.github.com":    "github.com",
	"altssh.gitlab.com": "gitlab.com",
}

// ParseRemote maps a git remote URL (https, ssh, scp-like) to a Repo. ok is false for local paths
// and remotes without an owner/repo path.
func ParseRemote(remote string) (Repo, bool) {
	key := workspace.NormalizeRemote(remote)
	host, path, found := strings.Cut(key, "/")
	if !found || !strings.Contains(path, "/") {
		return Repo{}, false
	}
	if a, ok := hostAliases[host]; ok {
		host = a
		key = host + "/" + path
	}
	return Repo{Key: key, Host: host, Path: path}, true
}

// Snapshot is the polled state of one branch.
type Snapshot struct {
	// PullRequests with the branch as head, most relevant first (open before closed/merged).
	PullRequests []*vcsv1.PullRequest
	// Checks per PR, keyed by CheckKey.
	Checks map[string]*vcsv1.ChecksList
}

// CheckKey is the key of ReportDeviceSnapshotRequest.checks: "repository_key#number".
func CheckKey(repoKey string, number int64) string {
	return repoKey + "#" + strconv.FormatInt(number, 10)
}

// Provider is one device provider (a local CLI).
type Provider interface {
	Type() vcsv1.ProviderType
	// Detect reports the CLI version and logged-in hosts. It returns ErrNotInstalled when the CLI
	// is missing; a CLI that is installed but logged in nowhere returns a provider with no hosts.
	Detect(ctx context.Context) (*vcsv1.DeviceProvider, error)
	// Snapshot polls the PRs/MRs whose head is branch, with their checks.
	Snapshot(ctx context.Context, repo Repo, branch string) (*Snapshot, error)
}

// covers reports whether a detected provider is logged in to host.
func covers(p *vcsv1.DeviceProvider, host string) bool {
	for _, h := range p.GetHosts() {
		if strings.EqualFold(h.GetHost(), host) {
			return true
		}
	}
	return false
}

// summarize derives a ChecksSummary from individual checks.
func summarize(checks []*vcsv1.Check) *vcsv1.ChecksSummary {
	s := &vcsv1.ChecksSummary{Total: int32(len(checks))}
	if len(checks) == 0 {
		return s
	}
	cancelled := 0
	for _, c := range checks {
		switch c.GetState() {
		case vcsv1.CheckState_CHECK_STATE_SUCCESS, vcsv1.CheckState_CHECK_STATE_SKIPPED, vcsv1.CheckState_CHECK_STATE_NEUTRAL:
			s.Passed++
		case vcsv1.CheckState_CHECK_STATE_FAILURE:
			s.Failed++
		case vcsv1.CheckState_CHECK_STATE_CANCELLED:
			cancelled++
		case vcsv1.CheckState_CHECK_STATE_QUEUED, vcsv1.CheckState_CHECK_STATE_RUNNING:
			s.Running++
		}
	}
	switch {
	case s.Running > 0:
		s.State = vcsv1.CheckState_CHECK_STATE_RUNNING
	case s.Failed > 0:
		s.State = vcsv1.CheckState_CHECK_STATE_FAILURE
	case cancelled > 0:
		s.State = vcsv1.CheckState_CHECK_STATE_CANCELLED
	default:
		s.State = vcsv1.CheckState_CHECK_STATE_SUCCESS
	}
	return s
}

// pending reports whether the snapshot has an open PR with checks still queued or running.
func (s *Snapshot) pending() bool {
	if s == nil {
		return false
	}
	for _, pr := range s.PullRequests {
		if isOpen(pr) && pr.GetChecks().GetRunning() > 0 {
			return true
		}
	}
	return false
}

// hasOpen reports whether the snapshot has an open (or draft) PR.
func (s *Snapshot) hasOpen() bool {
	if s == nil {
		return false
	}
	for _, pr := range s.PullRequests {
		if isOpen(pr) {
			return true
		}
	}
	return false
}

func isOpen(pr *vcsv1.PullRequest) bool {
	st := pr.GetState()
	return st == agentv1.PullRequest_STATE_OPEN || st == agentv1.PullRequest_STATE_DRAFT
}

// sortPullRequests orders open PRs first, then by updated_at, newest first.
func sortPullRequests(prs []*vcsv1.PullRequest) {
	sort.SliceStable(prs, func(i, j int) bool {
		if oi, oj := isOpen(prs[i]), isOpen(prs[j]); oi != oj {
			return oi
		}
		return prs[i].GetUpdatedAt() > prs[j].GetUpdatedAt()
	})
}

// utc normalizes a timestamp to RFC 3339 UTC; unparsable input is returned unchanged.
func utc(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return t.UTC().Format(time.RFC3339)
}

// durationMs returns completed-started in milliseconds, or 0 when either is missing.
func durationMs(started, completed string) int64 {
	s, err1 := time.Parse(time.RFC3339Nano, started)
	c, err2 := time.Parse(time.RFC3339Nano, completed)
	if err1 != nil || err2 != nil || s.IsZero() || c.Before(s) {
		return 0
	}
	return c.Sub(s).Milliseconds()
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// stripANSI removes terminal escape sequences some CLI versions print even with NO_COLOR.
func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func sortHosts(h []*vcsv1.DeviceProviderHost) {
	sort.Slice(h, func(i, j int) bool { return h[i].GetHost() < h[j].GetHost() })
}
