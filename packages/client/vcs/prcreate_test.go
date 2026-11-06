package vcs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/internal/gittest"
)

// prRepo is a real repository with a bare origin, on branch feat with one commit over main. The fake remote URL only
// says which host it is on: pushes go to origin, the bare repository.
func prRepo(t *testing.T) (dir, bare string) {
	t.Helper()
	dir = gittest.Init(t)
	bare = t.TempDir()
	gittest.Run(t, bare, "init", "-q", "--bare", "-b", "main")
	gittest.Run(t, dir, "remote", "add", "origin", bare)
	gittest.Run(t, dir, "push", "-q", "origin", "main")
	gittest.Run(t, dir, "fetch", "-q", "origin")
	gittest.Run(t, dir, "switch", "-q", "-c", "feat")
	gittest.Write(t, dir, "feature.txt", "new\n")
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "-q", "-m", "Add the feature")
	return dir, bare
}

func prTracker(t *testing.T, dir, remote string, providers ...Provider) *Tracker {
	t.Helper()
	tr := NewTracker(TrackerOptions{
		SessionID: "s1", Dir: dir, Providers: providers, Detections: &DetectCache{},
		Remote: func(context.Context, string) string { return remote },
		Branch: func(context.Context, string) string { return gittest.Run(t, dir, "rev-parse", "--abbrev-ref", "HEAD") },
	})
	tr.manual = true
	t.Cleanup(tr.Stop)
	return tr
}

func githubProvider(t *testing.T) (*GH, *fakeCLI) {
	t.Helper()
	f := newFakeEnv(t)
	gh := f.install("gh")
	gh.set("version", "gh version 2.80.0\n", "", 0)
	gh.set("auth-json", ghAuthOK, "", 0)
	gh.set("pr-list", "[]", "", 0)
	gh.set("pr-create", "https://github.com/o/r/pull/12\n", "", 0)
	gh.set("pr-view", prJSON("OPEN", "MERGEABLE", "2026-01-01T00:00:00Z", checkOK), "", 0)
	gh.set("graphql", noComments, "", 0)
	return &GH{Runner: f.runner()}, gh
}

func TestCreatePRPushesOpensAndBindsOnGitHub(t *testing.T) {
	dir, bare := prRepo(t)
	gh, cli := githubProvider(t)
	tr := prTracker(t, dir, "https://github.com/o/r.git", gh)

	f, err := tr.Facts(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Head != "feat" || f.Base != "main" || f.Commits != 1 || !f.NeedsPush || f.Dirty || f.Provider != "gh" {
		t.Fatalf("facts: %+v", f)
	}
	if !strings.Contains(f.Log, "Add the feature") || !strings.Contains(f.Diff, "feature.txt") || !strings.Contains(f.Stat, "feature.txt") {
		t.Fatalf("facts: %+v", f)
	}

	got, err := tr.CreatePR(context.Background(), CreatePRRequest{Title: "Add the feature\n", Body: "Why it matters.", Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 12 || got.URL != "https://github.com/o/r/pull/12" || got.Existed || !got.Pushed {
		t.Fatalf("created: %+v", got)
	}
	if head := gittest.Run(t, bare, "rev-parse", "refs/heads/feat"); head != gittest.Run(t, dir, "rev-parse", "HEAD") {
		t.Fatalf("the branch was not pushed: %s", head)
	}
	var create string
	for _, c := range cli.calls() {
		if strings.HasPrefix(c, "pr create ") {
			create = c
		}
	}
	for _, want := range []string{"--repo github.com/o/r", "--head feat", "--base main", "--title Add the feature", "--body Why it matters.", "--draft"} {
		if !strings.Contains(create, want) {
			t.Fatalf("%q not in %q", want, create)
		}
	}
	cli.assertNonInteractive()
	if info := tr.Info(); info == nil || info.GetNumber() != 12 || info.GetUrl() != got.URL {
		t.Fatalf("not bound: %+v", info)
	}
}

func TestCreatePRReturnsTheBranchesOpenPR(t *testing.T) {
	dir, _ := prRepo(t)
	gh, cli := githubProvider(t)
	cli.set("pr-list", "["+prJSON("OPEN", "MERGEABLE", "2026-01-01T00:00:00Z", checkOK)+"]", "", 0)
	tr := prTracker(t, dir, "https://github.com/o/r.git", gh)
	got, err := tr.CreatePR(context.Background(), CreatePRRequest{Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Existed || got.Pushed || got.Number != 12 {
		t.Fatalf("created: %+v", got)
	}
	for _, c := range cli.calls() {
		if strings.HasPrefix(c, "pr create ") {
			t.Fatalf("it must not open a second one: %s", c)
		}
	}
}

func TestCreatePRFallsBackToTheExistingOneWhenTheHostSaysSo(t *testing.T) {
	dir, _ := prRepo(t)
	gh, cli := githubProvider(t)
	// The list misses it (a replica behind) and the create call says it is there; the second list finds it.
	cli.set("pr-create", "", "a pull request for branch \"feat\" into branch \"main\" already exists:\nhttps://github.com/o/r/pull/12\n", 1)
	calls := 0
	tr := prTracker(t, dir, "https://github.com/o/r.git", &wrapSnapshots{GH: gh, before: func() {
		calls++
		if calls == 2 {
			cli.set("pr-list", "["+prJSON("OPEN", "MERGEABLE", "2026-01-01T00:00:00Z", checkOK)+"]", "", 0)
		}
	}})
	got, err := tr.CreatePR(context.Background(), CreatePRRequest{Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Existed || got.Number != 12 {
		t.Fatalf("created: %+v", got)
	}
}

type wrapSnapshots struct {
	*GH
	before func()
}

func (w *wrapSnapshots) Snapshot(ctx context.Context, repo Repo, branch string) (*Snapshot, error) {
	w.before()
	return w.GH.Snapshot(ctx, repo, branch)
}

func TestCreatePROpensAMergeRequestOnGitLab(t *testing.T) {
	dir, bare := prRepo(t)
	f := newFakeEnv(t)
	cli := f.install("glab")
	cli.set("version", "glab version 1.50.0\n", "", 0)
	cli.set("auth-all", "gitlab.example.com\n  ✓ Logged in to gitlab.example.com as me (keyring)\n", "", 0)
	cli.set("api-mrs", "[]", "", 0)
	mr := `{"iid":7,"title":"Draft: Add it","web_url":"https://gitlab.example.com/g/sub/p/-/merge_requests/7","state":"opened","draft":true,"source_branch":"feat","target_branch":"main"}`
	cli.set("api-mr-create", mr, "", 0)
	cli.set("api-mr", mr, "", 0)
	cli.set("api-discussions", "[]", "", 0)
	tr := prTracker(t, dir, "git@gitlab.example.com:g/sub/p.git", &GLab{Runner: f.runner()})

	got, err := tr.CreatePR(context.Background(), CreatePRRequest{Title: "Add it", Body: "Text", Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != 7 || got.Existed || !got.Pushed {
		t.Fatalf("created: %+v", got)
	}
	if gittest.Run(t, bare, "rev-parse", "--verify", "refs/heads/feat") == "" {
		t.Fatal("not pushed")
	}
	inputs := cli.lines("inputs")
	if len(inputs) != 1 {
		t.Fatalf("inputs: %v", inputs)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(inputs[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body["source_branch"] != "feat" || body["target_branch"] != "main" || body["title"] != "Draft: Add it" || body["description"] != "Text" {
		t.Fatalf("body: %v", body)
	}
	if info := tr.Info(); info == nil || info.GetNumber() != 7 {
		t.Fatalf("not bound: %+v", info)
	}
}

func TestFactsRefusesWhatHasNothingToPropose(t *testing.T) {
	dir, _ := prRepo(t)
	gh, _ := githubProvider(t)
	tr := prTracker(t, dir, "https://github.com/o/r.git", gh)
	gittest.Run(t, dir, "switch", "-q", "main")
	if _, err := tr.Facts(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "base branch") {
		t.Fatalf("on the base branch: %v", err)
	}
	gittest.Run(t, dir, "switch", "-q", "-c", "empty")
	if _, err := tr.Facts(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Fatalf("without commits: %v", err)
	}
	gittest.Run(t, dir, "switch", "-q", "feat")
	if _, err := tr.Facts(context.Background(), "nope"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unknown base: %v", err)
	}
}

func TestCreatePRSaysWhenNoProviderCoversTheHost(t *testing.T) {
	dir, _ := prRepo(t)
	gh, cli := githubProvider(t)
	cli.set("auth-json", `{"hosts":{}}`, "", 0)
	tr := prTracker(t, dir, "https://github.com/o/r.git", gh)
	if _, err := tr.CreatePR(context.Background(), CreatePRRequest{Title: "x"}); err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("err: %v", err)
	}
	f, err := tr.Facts(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Provider != "" || !strings.Contains(f.ProviderProblem, "gh auth login") {
		t.Fatalf("facts: %+v", f)
	}
}

func TestCreatePRReportsARejectedPush(t *testing.T) {
	dir, bare := prRepo(t)
	gh, cli := githubProvider(t)
	// The remote has another commit on feat: the push must fail, not force.
	other := t.TempDir()
	gittest.Run(t, other, "clone", "-q", bare, ".")
	gittest.Run(t, other, "switch", "-q", "-c", "feat")
	gittest.Write(t, other, "other.txt", "x\n")
	gittest.Run(t, other, "add", "-A")
	gittest.Run(t, other, "commit", "-q", "-m", "other")
	gittest.Run(t, other, "push", "-q", "origin", "feat")
	tr := prTracker(t, dir, "https://github.com/o/r.git", gh)
	if _, err := tr.CreatePR(context.Background(), CreatePRRequest{Title: "x"}); err == nil || !strings.Contains(err.Error(), "pushing feat failed") {
		t.Fatalf("err: %v", err)
	}
	for _, c := range cli.calls() {
		if strings.HasPrefix(c, "pr create ") {
			t.Fatalf("it opened a PR after a failed push: %s", c)
		}
	}
}

func TestParseDraft(t *testing.T) {
	cases := []struct{ in, title, body string }{
		{`{"title":"Add it","body":"Because."}`, "Add it", "Because."},
		{"```json\n{\"title\":\"Add\\nit\",\"body\":\" B \"}\n```", "Add it", "B"},
		{"Here you go: {\"title\":\"T\",\"body\":\"\"} done", "T", ""},
	}
	for _, c := range cases {
		title, body, err := parseDraft(c.in)
		if err != nil || title != c.title || body != c.body {
			t.Fatalf("%q: %q %q %v", c.in, title, body, err)
		}
	}
	for _, bad := range []string{"no json", `{"body":"x"}`, "{not json}"} {
		if _, _, err := parseDraft(bad); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func TestFallbackDraft(t *testing.T) {
	title, body := FallbackDraft(&PRFacts{Head: "feat/x", Subjects: []string{"Only one"}})
	if title != "Only one" || body != "" {
		t.Fatalf("%q %q", title, body)
	}
	title, body = FallbackDraft(&PRFacts{Head: "feat/more-things", Subjects: []string{"a", "b"}})
	if title != "feat: more things" || body != "- a\n- b" {
		t.Fatalf("%q %q", title, body)
	}
}

func TestCommentableLines(t *testing.T) {
	diff := `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,3 +1,4 @@
 one
-two
+TWO
+three
 four
@@ -10,2 +11,2 @@ func x() {
 ctx
+added
diff --git a/gone.go b/gone.go
--- a/gone.go
+++ /dev/null
@@ -1 +0,0 @@
-x
diff --git a/new.go b/new.go
--- /dev/null
+++ b/new.go
@@ -0,0 +1,2 @@
+a
+b
`
	got := CommentableLines(diff)
	want := map[string][]int{"a.go": {1, 2, 3, 4, 11, 12}, "new.go": {1, 2}}
	if len(got) != len(want) {
		t.Fatalf("files: %v", got)
	}
	for path, lines := range want {
		if len(got[path]) != len(lines) {
			t.Fatalf("%s: %v", path, got[path])
		}
		for _, l := range lines {
			if !got[path][l] {
				t.Fatalf("%s:%d missing: %v", path, l, got[path])
			}
		}
	}
}

func TestGHPostReviewSendsACommentReview(t *testing.T) {
	gh, cli := githubProvider(t)
	cli.set("api-review", "{}", "", 0)
	repo := Repo{Key: "github.com/o/r", Host: "github.com", Path: "o/r"}
	err := gh.PostReview(context.Background(), repo, 12, Review{Summary: "S", Commit: "abc", Comments: []LineComment{{Path: "a.go", Line: 3, Body: "B"}}})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Event, Body, CommitID string `json:"-"`
		Comments              []struct {
			Path string `json:"path"`
			Line int    `json:"line"`
			Side string `json:"side"`
			Body string `json:"body"`
		} `json:"comments"`
	}
	raw := cli.lines("inputs")[0]
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"event":"COMMENT"`) || !strings.Contains(raw, `"commit_id":"abc"`) || len(body.Comments) != 1 || body.Comments[0].Side != "RIGHT" || body.Comments[0].Line != 3 {
		t.Fatalf("review: %s", raw)
	}
	if !strings.Contains(cli.calls()[len(cli.calls())-1], "repos/o/r/pulls/12/reviews") {
		t.Fatalf("calls: %v", cli.calls())
	}
}

func TestGHPostReplyGoesToTheThreadOrTheConversation(t *testing.T) {
	gh, cli := githubProvider(t)
	cli.set("api-reply", "{}", "", 0)
	cli.set("api-comment", "{}", "", 0)
	repo := Repo{Key: "github.com/o/r", Host: "github.com", Path: "o/r"}
	if err := gh.PostReply(context.Background(), repo, ReplyTarget{Number: 12, Thread: "99"}, "b"); err != nil {
		t.Fatal(err)
	}
	if err := gh.PostReply(context.Background(), repo, ReplyTarget{Number: 13, IsIssue: true}, "b"); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(cli.calls(), "\n")
	if !strings.Contains(calls, "repos/o/r/pulls/12/comments/99/replies") || !strings.Contains(calls, "repos/o/r/issues/13/comments") {
		t.Fatalf("calls: %s", calls)
	}
}

func TestGLabPostReviewOpensDiscussionsAndMovesRefusedOnesToTheNote(t *testing.T) {
	f := newFakeEnv(t)
	cli := f.install("glab")
	cli.set("api-mr", `{"iid":7,"diff_refs":{"base_sha":"b","start_sha":"s","head_sha":"h"}}`, "", 0)
	cli.set("api-discussion", "{}", "", 0)
	cli.set("api-note", "{}", "", 0)
	g := &GLab{Runner: f.runner()}
	repo := Repo{Key: "gitlab.example.com/g/p", Host: "gitlab.example.com", Path: "g/p"}
	if err := g.PostReview(context.Background(), repo, 7, Review{Summary: "S", Comments: []LineComment{{Path: "a.go", Line: 3, Body: "B"}}}); err != nil {
		t.Fatal(err)
	}
	inputs := cli.lines("inputs")
	if len(inputs) != 2 {
		t.Fatalf("inputs: %v", inputs)
	}
	var disc struct {
		Body     string `json:"body"`
		Position struct {
			Type    string `json:"position_type"`
			BaseSHA string `json:"base_sha"`
			HeadSHA string `json:"head_sha"`
			Path    string `json:"new_path"`
			Line    int    `json:"new_line"`
		} `json:"position"`
	}
	if err := json.Unmarshal([]byte(inputs[0]), &disc); err != nil {
		t.Fatal(err)
	}
	if disc.Body != "B" || disc.Position.Type != "text" || disc.Position.BaseSHA != "b" || disc.Position.HeadSHA != "h" || disc.Position.Path != "a.go" || disc.Position.Line != 3 {
		t.Fatalf("discussion: %s", inputs[0])
	}
	if !strings.Contains(inputs[1], `"body":"S"`) {
		t.Fatalf("note: %s", inputs[1])
	}

	cli.set("api-discussion", "", "400 line_code can't be blank\n", 1)
	if err := g.PostReview(context.Background(), repo, 7, Review{Summary: "S", Comments: []LineComment{{Path: "a.go", Line: 3, Body: "lost otherwise"}}}); err != nil {
		t.Fatal(err)
	}
	inputs = cli.lines("inputs")
	if last := inputs[len(inputs)-1]; !strings.Contains(last, "lost otherwise") || !strings.Contains(last, "a.go:3") {
		t.Fatalf("the refused comment is gone: %s", last)
	}
}

func TestGLabPRDiffRebuildsAUnifiedDiff(t *testing.T) {
	f := newFakeEnv(t)
	cli := f.install("glab")
	cli.set("api-diffs", `[{"old_path":"a.go","new_path":"a.go","diff":"@@ -1 +1,2 @@\n one\n+two\n"},{"old_path":"n.go","new_path":"n.go","new_file":true,"diff":"@@ -0,0 +1 @@\n+x"}]`, "", 0)
	g := &GLab{Runner: f.runner()}
	diff, err := g.PRDiff(context.Background(), Repo{Host: "gitlab.example.com", Path: "g/p"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	lines := CommentableLines(diff)
	if !lines["a.go"][2] || !lines["n.go"][1] {
		t.Fatalf("%s\n%v", diff, lines)
	}
}

func TestHasWriteAccess(t *testing.T) {
	gh, cli := githubProvider(t)
	repo := Repo{Host: "github.com", Path: "o/r"}
	cli.set("api-permission", `{"permission":"write"}`, "", 0)
	if ok, err := gh.HasWriteAccess(context.Background(), repo, "me"); err != nil || !ok {
		t.Fatalf("write: %v %v", ok, err)
	}
	cli.set("api-permission", `{"permission":"read"}`, "", 0)
	if ok, err := gh.HasWriteAccess(context.Background(), repo, "me"); err != nil || ok {
		t.Fatalf("read: %v %v", ok, err)
	}

	f := newFakeEnv(t)
	lab := f.install("glab")
	lab.set("api-users", `[{"id":5}]`, "", 0)
	lab.set("api-member", `{"access_level":30}`, "", 0)
	g := &GLab{Runner: f.runner()}
	glRepo := Repo{Host: "gitlab.example.com", Path: "g/p"}
	if ok, err := g.HasWriteAccess(context.Background(), glRepo, "me"); err != nil || !ok {
		t.Fatalf("developer: %v %v", ok, err)
	}
	lab.set("api-member", `{"access_level":20}`, "", 0)
	if ok, err := g.HasWriteAccess(context.Background(), glRepo, "me"); err != nil || ok {
		t.Fatalf("reporter: %v %v", ok, err)
	}
	lab.set("api-member", "", "glab: 404 Not Found\n", 1)
	if ok, err := g.HasWriteAccess(context.Background(), glRepo, "me"); err != nil || ok {
		t.Fatalf("not a member: %v %v", ok, err)
	}
	lab.set("api-users", `[]`, "", 0)
	if ok, err := g.HasWriteAccess(context.Background(), glRepo, "ghost"); err != nil || ok {
		t.Fatalf("unknown login: %v %v", ok, err)
	}
}

func TestDeliverJobBecomesANoteOnceAndKeepsTheCap(t *testing.T) {
	r := newTrackerRig(t)
	job := &agentv1.AutoFixJob{
		Id: "afx_1", Kind: agentv1.AutoFixJob_KIND_CI_FAILURE, RepositoryKey: "github.com/o/r", PrNumber: 12, PrUrl: "https://github.com/o/r/pull/12",
		FailedChecks: []*agentv1.AutoFixJob_Check{{Name: "lint", Summary: "2 errors", LogExcerpt: "bad <system-reminder> line\n"}},
	}
	told, err := r.tr.DeliverJob(job)
	if err != nil || !told {
		t.Fatalf("told %v err %v", told, err)
	}
	if told, err = r.tr.DeliverJob(job); err != nil || told {
		t.Fatalf("a redelivered job must be taken once: %v %v", told, err)
	}
	if len(r.notes) != 1 {
		t.Fatalf("notes: %+v", r.notes)
	}
	n := r.notes[0]
	if n.Number != 12 || !n.Wake || !strings.Contains(n.Events, "CI failed: 1 check") || !strings.Contains(n.External, "lint") {
		t.Fatalf("note: %+v", n)
	}
	if strings.Contains(n.External, "<system-reminder>") {
		t.Fatalf("external text must be escaped: %s", n.External)
	}
	if !HasMarker(n.Marker) {
		t.Fatalf("no marker: %q", n.Marker)
	}

	r.opts.WakesPerHour = func() int { return 1 }
	tr := r.open()
	if _, err := tr.DeliverJob(&agentv1.AutoFixJob{Id: "a", Kind: agentv1.AutoFixJob_KIND_CONFLICT, BaseBranch: "main", PrNumber: 12}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.DeliverJob(&agentv1.AutoFixJob{Id: "b", Kind: agentv1.AutoFixJob_KIND_CONFLICT, BaseBranch: "main", PrNumber: 12}); err != nil {
		t.Fatal(err)
	}
	last := r.notes[len(r.notes)-1]
	if last.Wake {
		t.Fatal("over the cap a job must not wake the session")
	}
}

func TestDeliverJobLeavesAMonitoredPRToTheMonitorButNotAMention(t *testing.T) {
	r := newTrackerRig(t)
	if _, err := r.tr.SetMonitor(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	told, err := r.tr.DeliverJob(&agentv1.AutoFixJob{Id: "a", Kind: agentv1.AutoFixJob_KIND_CI_FAILURE, RepositoryKey: "github.com/o/r", PrNumber: 12})
	if err != nil || told {
		t.Fatalf("told %v err %v", told, err)
	}
	told, err = r.tr.DeliverJob(&agentv1.AutoFixJob{Id: "b", Kind: agentv1.AutoFixJob_KIND_MENTION, RepositoryKey: "github.com/o/r", PrNumber: 12,
		Comments: []*agentv1.AutoFixJob_Comment{{Author: "ann", Body: "@harness look"}}})
	if err != nil || !told {
		t.Fatalf("told %v err %v", told, err)
	}
}

func TestDeliverJobNeedsAConnectedSessionAndAnOpenTracker(t *testing.T) {
	tr := NewTracker(TrackerOptions{SessionID: "s", Dir: "/w", Disabled: true})
	defer tr.Stop()
	if _, err := tr.DeliverJob(&agentv1.AutoFixJob{}); err == nil {
		t.Fatal("disabled tracker took a job")
	}
	tr2 := NewTracker(TrackerOptions{SessionID: "s", Dir: "/w", Now: func() time.Time { return time.Unix(1, 0) }})
	defer tr2.Stop()
	if _, err := tr2.DeliverJob(&agentv1.AutoFixJob{Id: "x"}); err == nil {
		t.Fatal("a job went nowhere without an error")
	}
}
