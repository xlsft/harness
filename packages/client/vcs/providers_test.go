package vcs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
)

const ghVersion = "gh version 2.95.0 (2026-06-17)\nhttps://github.com/cli/cli/releases/tag/v2.95.0\n"

const ghAuthMulti = `{"hosts":{
 "github.com":[
  {"state":"success","active":false,"host":"github.com","login":"old-account","tokenSource":"keyring"},
  {"state":"success","active":true,"host":"github.com","login":"octocat","tokenSource":"keyring"}],
 "GHE.Corp.Example":[{"state":"success","active":true,"host":"ghe.corp.example","login":"mona","tokenSource":"oauth_token"}],
 "broken.example":[{"state":"error","active":true,"host":"broken.example","login":"x","error":"token expired"}]}}`

const ghAuthTextNew = `github.com
  ✓ Logged in to github.com account octocat (keyring)
  - Active account: true
  - Git operations protocol: https
  ✓ Logged in to github.com account second (keyring)
  - Active account: false

ghe.corp.example
  X Failed to log in to ghe.corp.example account mona (GH_ENTERPRISE_TOKEN)
`

const ghAuthTextOld = "github.com\n  \x1b[32m✓\x1b[0m Logged in to github.com as octocat (/home/u/.config/gh/hosts.yml)\n"

func ghPRJSON(state, draft, rollup string) string {
	return `[{"number":42,"title":"Add feature","url":"https://github.com/o/r/pull/42","state":"` + state + `","isDraft":` + draft + `,
 "headRefName":"feat","baseRefName":"main","author":{"login":"octocat"},"reviewDecision":"APPROVED",
 "mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","autoMergeRequest":null,"updatedAt":"2026-11-02T10:00:00+02:00",
 "statusCheckRollup":` + rollup + `}]`
}

const ghRollupPending = `[
 {"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"https://github.com/o/r/actions/runs/1/job/111","workflowName":"CI","startedAt":"2026-11-02T08:00:00Z","completedAt":"2026-11-02T08:01:30Z"},
 {"__typename":"CheckRun","name":"test","status":"IN_PROGRESS","conclusion":"","detailsUrl":"https://github.com/o/r/actions/runs/1/job/112","workflowName":"CI","startedAt":"2026-11-02T08:00:00Z","completedAt":"0001-01-01T00:00:00Z"},
 {"__typename":"StatusContext","context":"ci/legacy","state":"PENDING","targetUrl":"https://ci.example/1","description":"waiting"}]`

const ghRollupFailed = `[
 {"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"https://github.com/o/r/actions/runs/1/job/111","workflowName":"CI"},
 {"__typename":"CheckRun","name":"test","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://github.com/o/r/actions/runs/1/job/112","workflowName":"CI"},
 {"__typename":"StatusContext","context":"ci/legacy","state":"SUCCESS","targetUrl":"https://ci.example/1"}]`

const ghRollupPassed = `[
 {"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"https://github.com/o/r/actions/runs/1/job/111","workflowName":"CI"},
 {"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"SKIPPED","detailsUrl":"https://github.com/o/r/actions/runs/1/job/113","workflowName":"CI"}]`

var ghRepo = Repo{Key: "github.com/o/r", Host: "github.com", Path: "o/r"}

func TestParseRemote(t *testing.T) {
	cases := []struct {
		in   string
		want Repo
		ok   bool
	}{
		{"https://github.com/Owner/Repo.git", Repo{"github.com/owner/repo", "github.com", "owner/repo"}, true},
		{"git@github.com:owner/repo.git", Repo{"github.com/owner/repo", "github.com", "owner/repo"}, true},
		{"ssh://git@ssh.github.com:443/owner/repo.git", Repo{"github.com/owner/repo", "github.com", "owner/repo"}, true},
		{"git@gitlab.example.com:group/sub/project.git", Repo{"gitlab.example.com/group/sub/project", "gitlab.example.com", "group/sub/project"}, true},
		{"/srv/git/repo.git", Repo{}, false},
		{"https://example.com/onlyone", Repo{}, false},
		{"", Repo{}, false},
	}
	for _, c := range cases {
		got, ok := ParseRemote(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseRemote(%q) = %+v, %v; want %+v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func hostsOf(p *vcsv1.DeviceProvider) string {
	var s []string
	for _, h := range p.GetHosts() {
		s = append(s, h.GetHost()+"="+h.GetLogin())
	}
	return strings.Join(s, ",")
}

func TestGHDetect(t *testing.T) {
	ctx := context.Background()
	t.Run("not installed", func(t *testing.T) {
		f := newFakeEnv(t)
		_, err := (&GH{Runner: f.runner()}).Detect(ctx)
		if !errors.Is(err, ErrNotInstalled) {
			t.Fatalf("err = %v, want ErrNotInstalled", err)
		}
	})
	t.Run("not logged in", func(t *testing.T) {
		f := newFakeEnv(t)
		gh := f.install("gh")
		gh.set("version", ghVersion, "", 0)
		gh.set("auth-json", `{"hosts":{}}`, "", 0)
		p, err := (&GH{Runner: f.runner()}).Detect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if p.GetVersion() != "gh version 2.95.0 (2026-06-17)" || len(p.GetHosts()) != 0 || p.GetType() != vcsv1.ProviderType_PROVIDER_TYPE_GH {
			t.Fatalf("provider = %v", p)
		}
		gh.assertNonInteractive()
	})
	t.Run("multiple hosts and accounts", func(t *testing.T) {
		f := newFakeEnv(t)
		gh := f.install("gh")
		gh.set("version", ghVersion, "", 0)
		gh.set("auth-json", ghAuthMulti, "", 0)
		p, err := (&GH{Runner: f.runner()}).Detect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := hostsOf(p); got != "ghe.corp.example=mona,github.com=octocat" {
			t.Fatalf("hosts = %s", got)
		}
	})
	t.Run("text fallback without --json", func(t *testing.T) {
		f := newFakeEnv(t)
		gh := f.install("gh")
		gh.set("version", ghVersion, "", 0)
		gh.set("auth-json", "", "unknown flag: --json\n\nUsage: gh auth status [flags]\n", 1)
		gh.set("auth", "", ghAuthTextNew, 1)
		p, err := (&GH{Runner: f.runner()}).Detect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := hostsOf(p); got != "github.com=octocat" {
			t.Fatalf("hosts = %s", got)
		}
	})
	t.Run("old text format with colors", func(t *testing.T) {
		f := newFakeEnv(t)
		gh := f.install("gh")
		gh.set("version", ghVersion, "", 0)
		gh.set("auth-json", "not json at all", "", 0)
		gh.set("auth", "", ghAuthTextOld, 0)
		p, err := (&GH{Runner: f.runner()}).Detect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := hostsOf(p); got != "github.com=octocat" {
			t.Fatalf("hosts = %s", got)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		f := newFakeEnv(t)
		gh := f.install("gh")
		gh.set("version", ghVersion, "", 0)
		gh.hang("auth-json", 30)
		r := f.runner()
		r.Timeout = 2 * time.Second // --version must still answer in time on a slow first exec
		start := time.Now()
		_, err := (&GH{Runner: r}).Detect(ctx)
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("err = %v, want ErrTimeout", err)
		}
		if time.Since(start) > 8*time.Second {
			t.Fatalf("timeout took %s", time.Since(start))
		}
	})
}

const glabAuthMulti = `gitlab.com
  ✓ Logged in to gitlab.com as alice (/home/u/.config/glab-cli/config.yml)
  ✓ Git operations for gitlab.com configured to use ssh protocol.
  ✓ API calls for gitlab.com are made over https protocol.
gitlab.corp.example
  ✓ Logged in to gitlab.corp.example as bob (GITLAB_TOKEN)
broken.example
  x broken.example: API call failed: GET https://broken.example/api/v4/user: 401 {message: 401 Unauthorized}
  ! No token found (checked config file, keyring, and environment variables).
`

const glabAuthNone = `gitlab.com
  x gitlab.com: API call failed: GET https://gitlab.com/api/v4/user: 401 {message: 401 Unauthorized}
  ! No token found (checked config file, keyring, and environment variables).
`

func TestGLabDetect(t *testing.T) {
	ctx := context.Background()
	t.Run("not installed", func(t *testing.T) {
		f := newFakeEnv(t)
		f.install("gh") // gh present, glab missing
		_, err := (&GLab{Runner: f.runner()}).Detect(ctx)
		if !errors.Is(err, ErrNotInstalled) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("multiple hosts, one failing", func(t *testing.T) {
		f := newFakeEnv(t)
		g := f.install("glab")
		g.set("version", "glab 1.119.0 (f5016eda2)\n", "", 0)
		g.set("auth-all", "", glabAuthMulti, 1)
		p, err := (&GLab{Runner: f.runner()}).Detect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if p.GetVersion() != "glab 1.119.0 (f5016eda2)" || hostsOf(p) != "gitlab.com=alice,gitlab.corp.example=bob" {
			t.Fatalf("provider = %v", p)
		}
		g.assertNonInteractive()
	})
	t.Run("not logged in", func(t *testing.T) {
		f := newFakeEnv(t)
		g := f.install("glab")
		g.set("version", "glab version 1.30.0 (2023-05-01)\n", "", 0)
		g.set("auth-all", "", glabAuthNone, 1)
		p, err := (&GLab{Runner: f.runner()}).Detect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.GetHosts()) != 0 {
			t.Fatalf("hosts = %s", hostsOf(p))
		}
	})
	t.Run("old glab without --all", func(t *testing.T) {
		f := newFakeEnv(t)
		g := f.install("glab")
		g.set("version", "glab version 1.20.0\n", "", 0)
		g.set("auth-all", "", "Error: unknown flag: --all\n", 1)
		g.set("auth", "", "gitlab.com\n  ✓ Logged in to gitlab.com as alice (/x/config.yml)\n", 0)
		p, err := (&GLab{Runner: f.runner()}).Detect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if hostsOf(p) != "gitlab.com=alice" {
			t.Fatalf("hosts = %s", hostsOf(p))
		}
	})
}

func TestGHSnapshot(t *testing.T) {
	ctx := context.Background()
	f := newFakeEnv(t)
	gh := f.install("gh")
	p := &GH{Runner: f.runner()}

	t.Run("absent", func(t *testing.T) {
		gh.set("pr-list", "[]\n", "", 0)
		s, err := p.Snapshot(ctx, ghRepo, "feat")
		if err != nil || len(s.PullRequests) != 0 {
			t.Fatalf("snapshot = %v, %v", s, err)
		}
		last := gh.calls()[len(gh.calls())-1]
		if !strings.Contains(last, "--repo github.com/o/r") || !strings.Contains(last, "--head feat") || !strings.Contains(last, "--state all") {
			t.Fatalf("call = %q", last)
		}
	})
	t.Run("pending", func(t *testing.T) {
		gh.set("pr-list", ghPRJSON("OPEN", "false", ghRollupPending), "", 0)
		s, err := p.Snapshot(ctx, ghRepo, "feat")
		if err != nil {
			t.Fatal(err)
		}
		pr := s.PullRequests[0]
		if pr.GetNumber() != 42 || pr.GetState() != agentv1.PullRequest_STATE_OPEN || pr.GetReviewState() != "approved" ||
			!pr.GetMergeable() || pr.GetUpdatedAt() != "2026-11-02T08:00:00Z" || pr.GetAuthor() != "octocat" {
			t.Fatalf("pr = %v", pr)
		}
		sum := pr.GetChecks()
		if sum.GetState() != vcsv1.CheckState_CHECK_STATE_RUNNING || sum.GetTotal() != 3 || sum.GetRunning() != 2 || sum.GetPassed() != 1 {
			t.Fatalf("summary = %v", sum)
		}
		checks := s.Checks["github.com/o/r#42"].GetChecks()
		if len(checks) != 3 || checks[0].GetId() != "111" || checks[0].GetDurationMs() != 90000 || checks[0].GetWorkflow() != "CI" ||
			checks[2].GetName() != "ci/legacy" || checks[2].GetSummary() != "waiting" {
			t.Fatalf("checks = %v", checks)
		}
		if !s.pending() {
			t.Fatal("pending() = false")
		}
	})
	t.Run("failed", func(t *testing.T) {
		gh.set("pr-list", ghPRJSON("OPEN", "true", ghRollupFailed), "", 0)
		s, err := p.Snapshot(ctx, ghRepo, "feat")
		if err != nil {
			t.Fatal(err)
		}
		pr := s.PullRequests[0]
		if pr.GetState() != agentv1.PullRequest_STATE_DRAFT || !pr.GetDraft() {
			t.Fatalf("pr = %v", pr)
		}
		if sum := pr.GetChecks(); sum.GetState() != vcsv1.CheckState_CHECK_STATE_FAILURE || sum.GetFailed() != 1 || sum.GetPassed() != 2 {
			t.Fatalf("summary = %v", sum)
		}
		if s.pending() || !s.hasOpen() {
			t.Fatal("wrong pending/open")
		}
	})
	t.Run("passed and merged", func(t *testing.T) {
		gh.set("pr-list", ghPRJSON("MERGED", "false", ghRollupPassed), "", 0)
		s, err := p.Snapshot(ctx, ghRepo, "feat")
		if err != nil {
			t.Fatal(err)
		}
		pr := s.PullRequests[0]
		if pr.GetState() != agentv1.PullRequest_STATE_MERGED || pr.GetChecks().GetState() != vcsv1.CheckState_CHECK_STATE_SUCCESS {
			t.Fatalf("pr = %v", pr)
		}
		if s.hasOpen() {
			t.Fatal("merged PR counted as open")
		}
	})
	t.Run("malformed", func(t *testing.T) {
		gh.set("pr-list", `[{"number":`, "", 0)
		if _, err := p.Snapshot(ctx, ghRepo, "feat"); err == nil || !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("cli error", func(t *testing.T) {
		gh.set("pr-list", "", "GraphQL: Could not resolve to a Repository with the name 'o/r'.\n", 1)
		if _, err := p.Snapshot(ctx, ghRepo, "feat"); err == nil || !strings.Contains(err.Error(), "Could not resolve") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		gh.hang("pr-list", 30)
		r := f.runner()
		r.Timeout = 300 * time.Millisecond
		if _, err := (&GH{Runner: r}).Snapshot(ctx, ghRepo, "feat"); !errors.Is(err, ErrTimeout) {
			t.Fatalf("err = %v", err)
		}
	})
}

var glRepo = Repo{Key: "gitlab.corp.example/group/sub/proj", Host: "gitlab.corp.example", Path: "group/sub/proj"}

const glabMRList = `[
 {"iid":3,"title":"Old attempt","web_url":"https://gitlab.corp.example/group/sub/proj/-/merge_requests/3","state":"closed","source_branch":"feat","target_branch":"main","updated_at":"2026-10-01T00:00:00.000Z","author":{"username":"alice"}},
 {"iid":7,"title":"Draft: Feature","web_url":"https://gitlab.corp.example/group/sub/proj/-/merge_requests/7","state":"opened","draft":true,"source_branch":"feat","target_branch":"main","updated_at":"2026-11-01T12:00:00.000Z","author":{"username":"alice"},"has_conflicts":false,"detailed_merge_status":"not_approved"}]`

func glabMRDetail(state string) string {
	return `{"iid":7,"title":"Feature","web_url":"https://gitlab.corp.example/group/sub/proj/-/merge_requests/7","state":"` + state + `","draft":false,
 "source_branch":"feat","target_branch":"main","updated_at":"2026-11-02T09:00:00.000Z","author":{"username":"alice"},
 "has_conflicts":true,"detailed_merge_status":"broken_status","merge_when_pipeline_succeeds":true,
 "head_pipeline":{"id":900,"status":"running","web_url":"https://gitlab.corp.example/group/sub/proj/-/pipelines/900"}}`
}

func glabJobs(test string) string {
	return `[
 {"id":1,"name":"build","stage":"build","status":"success","web_url":"https://gitlab.corp.example/j/1","duration":12.5,"allow_failure":false},
 {"id":2,"name":"test","stage":"test","status":"` + test + `","web_url":"https://gitlab.corp.example/j/2","duration":null,"allow_failure":false},
 {"id":3,"name":"flaky","stage":"test","status":"failed","web_url":"https://gitlab.corp.example/j/3","duration":3,"allow_failure":true},
 {"id":4,"name":"deploy","stage":"deploy","status":"manual","web_url":"https://gitlab.corp.example/j/4","allow_failure":true}]`
}

func TestGLabSnapshot(t *testing.T) {
	ctx := context.Background()
	f := newFakeEnv(t)
	g := f.install("glab")
	p := &GLab{Runner: f.runner()}

	t.Run("absent", func(t *testing.T) {
		g.set("api-mrs", "[]", "", 0)
		s, err := p.Snapshot(ctx, glRepo, "feat")
		if err != nil || len(s.PullRequests) != 0 {
			t.Fatalf("snapshot = %v, %v", s, err)
		}
		last := g.calls()[len(g.calls())-1]
		if !strings.HasPrefix(last, "api --hostname gitlab.corp.example projects/group%2Fsub%2Fproj/merge_requests?") || !strings.Contains(last, "source_branch=feat") {
			t.Fatalf("call = %q", last)
		}
	})
	for _, tc := range []struct {
		job  string
		want vcsv1.CheckState
	}{
		{"running", vcsv1.CheckState_CHECK_STATE_RUNNING},
		{"pending", vcsv1.CheckState_CHECK_STATE_RUNNING},
		{"failed", vcsv1.CheckState_CHECK_STATE_FAILURE},
		{"success", vcsv1.CheckState_CHECK_STATE_SUCCESS},
	} {
		t.Run("job "+tc.job, func(t *testing.T) {
			g.set("api-mrs", glabMRList, "", 0)
			g.set("api-mr", glabMRDetail("opened"), "", 0)
			g.set("api-jobs", glabJobs(tc.job), "", 0)
			s, err := p.Snapshot(ctx, glRepo, "feat")
			if err != nil {
				t.Fatal(err)
			}
			if len(s.PullRequests) != 2 {
				t.Fatalf("prs = %v", s.PullRequests)
			}
			top := s.PullRequests[0]
			if top.GetNumber() != 7 || top.GetState() != agentv1.PullRequest_STATE_OPEN || !top.GetHasConflicts() || !top.GetAutoMergeEnabled() ||
				top.GetTitle() != "Feature" || top.GetUpdatedAt() != "2026-11-02T09:00:00Z" {
				t.Fatalf("top = %v", top)
			}
			if s.PullRequests[1].GetState() != agentv1.PullRequest_STATE_CLOSED {
				t.Fatalf("second = %v", s.PullRequests[1])
			}
			if got := top.GetChecks().GetState(); got != tc.want {
				t.Fatalf("summary state = %v, want %v (%v)", got, tc.want, top.GetChecks())
			}
			checks := s.Checks["gitlab.corp.example/group/sub/proj#7"].GetChecks()
			if len(checks) != 4 || checks[0].GetDurationMs() != 12500 || checks[0].GetWorkflow() != "build" ||
				checks[2].GetState() != vcsv1.CheckState_CHECK_STATE_NEUTRAL || checks[3].GetState() != vcsv1.CheckState_CHECK_STATE_NEUTRAL {
				t.Fatalf("checks = %v", checks)
			}
			if len(s.Checks) != 1 {
				t.Fatalf("checks keys = %v", s.Checks)
			}
		})
	}
	t.Run("no pipeline", func(t *testing.T) {
		g.set("api-mrs", glabMRList, "", 0)
		g.set("api-mr", `{"iid":7,"state":"merged","source_branch":"feat","head_pipeline":null}`, "", 0)
		s, err := p.Snapshot(ctx, glRepo, "feat")
		if err != nil {
			t.Fatal(err)
		}
		if s.PullRequests[0].GetState() != agentv1.PullRequest_STATE_MERGED || s.PullRequests[0].GetChecks().GetTotal() != 0 {
			t.Fatalf("pr = %v", s.PullRequests[0])
		}
	})
	t.Run("malformed", func(t *testing.T) {
		g.set("api-mrs", "<html>502 Bad Gateway</html>", "", 0)
		if _, err := p.Snapshot(ctx, glRepo, "feat"); err == nil || !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("api error", func(t *testing.T) {
		g.set("api-mrs", "", "glab: 404 Project Not Found (HTTP 404)\n", 1)
		if _, err := p.Snapshot(ctx, glRepo, "feat"); err == nil || !strings.Contains(err.Error(), "404") {
			t.Fatalf("err = %v", err)
		}
	})
}
