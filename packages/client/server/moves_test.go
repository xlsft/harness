package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/llm"
)

// call is a scripted model reply with one tool call; say one that ends the turn.
func call(id, name string, input map[string]any) func(*llm.Request) fg.Reply {
	return func(*llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m_"+id, fg.ToolUse(id, name, input))} }
}

func say(text string) func(*llm.Request) fg.Reply {
	return func(*llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m_say", fg.Text(text))} }
}

// results returns the text of the tool result each request carried, by tool use id.
func results(h *harness) map[string]string {
	out := map[string]string{}
	for _, r := range h.gw.Requests() {
		for _, m := range r.Messages {
			for _, b := range m.Content {
				if b.Type == llm.BlockToolResult {
					out[b.ToolUseID] = fg.ResultText(b)
				}
			}
		}
	}
	return out
}

func moveRequestText(r *llm.Request) string {
	var sb strings.Builder
	for _, s := range r.System {
		sb.WriteString(s.Text + "\n")
	}
	sb.WriteString(allText(r))
	return sb.String()
}

func TestEnterAndExitWorktreeMidTurn(t *testing.T) {
	repo := gittest.Init(t)
	wtPath := filepath.Join(repo, ".harness", "worktrees", "feat")
	h := startHarness(t, fg.Sequence(
		call("before", "Bash", map[string]any{"command": "pwd", "description": "working directory"}),
		call("enter", "EnterWorktree", map[string]any{"name": "feat"}),
		call("inside", "Bash", map[string]any{"command": "pwd", "description": "working directory"}),
		call("again", "EnterWorktree", map[string]any{"name": "other"}),
		call("write", "Write", map[string]any{"file_path": filepath.Join(wtPath, "new.txt"), "content": "work\n"}),
		call("refused", "ExitWorktree", map[string]any{"action": "remove"}),
		call("keep", "ExitWorktree", map[string]any{"action": "keep"}),
		call("back", "Bash", map[string]any{"command": "pwd", "description": "working directory"}),
		say("done"),
	), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: repo, PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	h.send(sid, "go")
	var notices []string
	var updates []*agentv1.SessionInfo
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if n := ev.GetNotice(); n != nil && n.Kind == "session_moved" {
			notices = append(notices, n.Text)
		}
		if u := ev.GetSessionUpdated(); u != nil {
			updates = append(updates, u)
		}
		if r := ev.GetPermissionRequest(); r != nil {
			t.Fatalf("unexpected permission request for %s", r.ToolName)
		}
		return isTurnFinished(ev)
	})
	res := results(h)
	if !strings.Contains(res["before"], repo) || !strings.Contains(res["inside"], wtPath) || !strings.Contains(res["back"], repo) || strings.Contains(res["back"], wtPath) {
		t.Fatalf("shell did not follow: before %q inside %q back %q", res["before"], res["inside"], res["back"])
	}
	if !strings.Contains(res["again"], "already runs in the worktree") {
		t.Fatalf("second enter: %q", res["again"])
	}
	if !strings.Contains(res["refused"], "uncommitted") || !strings.Contains(res["refused"], "new.txt") {
		t.Fatalf("remove with uncommitted work: %q", res["refused"])
	}
	if _, err := os.Stat(filepath.Join(wtPath, "new.txt")); err != nil {
		t.Fatalf("kept worktree lost its work: %v", err)
	}
	// Frontends saw it in the session's state, and notices in the transcript.
	sawWorktree := false
	for _, u := range updates {
		if u.Cwd == wtPath && u.GetWorktree().GetPath() == wtPath && u.GitBranch == "harness/feat" && u.GetWorktree().GetBaseRef() == "main" {
			sawWorktree = true
		}
	}
	if !sawWorktree || len(notices) != 2 {
		t.Fatalf("worktree update seen %v, notices %q", sawWorktree, notices)
	}
	last := updates[len(updates)-1]
	if last.Cwd != repo || last.GetWorktree() != nil || last.GitBranch != "main" {
		t.Fatalf("after leaving: %+v", last)
	}
	// The model was told: the reminder rides on the next request, and the environment section follows.
	reqs := h.gw.Requests()
	if txt := moveRequestText(reqs[2]); !strings.Contains(txt, "Moved to "+wtPath) || fg.Cwd(reqs[2]) != wtPath {
		t.Fatalf("request after entering: cwd %q\n%s", fg.Cwd(reqs[2]), txt)
	}
	// The replay after a restart has the notices.
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if cs.Msg.Session.Cwd != repo || cs.Msg.Session.GetWorktree() != nil {
		t.Fatalf("resumed where? %+v", cs.Msg.Session)
	}
	replayed := 0
	h.until(h.subscribe(sid), func(ev *agentv1.SessionEvent) bool {
		if n := ev.GetNotice(); n != nil && n.Kind == "session_moved" {
			replayed++
		}
		return replayed == 2 || isTurnFinished(ev) && replayed == 2
	})
}

func TestResumeFindsWorktree(t *testing.T) {
	repo := gittest.Init(t)
	wtPath := filepath.Join(repo, ".harness", "worktrees", "feat")
	h := startHarness(t, fg.Sequence(
		call("enter", "EnterWorktree", map[string]any{"name": "feat"}),
		say("in"),
		call("pwd", "Bash", map[string]any{"command": "pwd", "description": "working directory"}),
		call("remove", "ExitWorktree", map[string]any{"action": "remove"}),
		call("pwd2", "Bash", map[string]any{"command": "pwd", "description": "working directory"}),
		say("out"),
	), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: repo, PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	h.send(sid, "go in")
	h.until(ch, isTurnFinished)
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	// Closed, the lists know where it is.
	all, err := h.client.ListSessions(h.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{}))
	if err != nil || len(all.Msg.Sessions) != 1 || all.Msg.Sessions[0].Cwd != wtPath || all.Msg.Sessions[0].GetWorktree().GetBranch() != "harness/feat" {
		t.Fatalf("list closed: %v %+v", err, all)
	}
	// A frontend that still names the old folder gets the session where it went.
	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid, Cwd: repo,
		PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS}))
	if err != nil {
		t.Fatal(err)
	}
	if got := cs.Msg.Session; got.Cwd != wtPath || got.GetWorktree().GetPath() != wtPath || got.GitBranch != "harness/feat" {
		t.Fatalf("resumed %+v", got)
	}
	ch = h.subscribe(sid)
	h.send(sid, "come out")
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		return isTurnFinished(ev) && strings.Contains(results(h)["pwd2"], repo)
	})
	res := results(h)
	if !strings.Contains(res["pwd"], wtPath) {
		t.Fatalf("resumed shell: %q", res["pwd"])
	}
	if !strings.Contains(res["remove"], "is gone") || strings.Contains(res["remove"], "stays") {
		t.Fatalf("remove: %q", res["remove"])
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("worktree still there: %v", err)
	}
	if b := gittest.Run(t, repo, "branch", "--list", "harness/feat"); b != "" {
		t.Fatalf("branch without commits stayed: %q", b)
	}
}

func TestDiscardNeedsTheUser(t *testing.T) {
	repo := gittest.Init(t)
	wtPath := filepath.Join(repo, ".harness", "worktrees", "feat")
	h := startHarness(t, fg.Sequence(
		call("enter", "EnterWorktree", map[string]any{"name": "feat"}),
		call("write", "Write", map[string]any{"file_path": filepath.Join(wtPath, "new.txt"), "content": "work\n"}),
		call("discard", "ExitWorktree", map[string]any{"action": "remove", "discard_changes": true}),
		say("gone"),
	), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: repo, PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	h.send(sid, "go")
	asked := ""
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if r := ev.GetPermissionRequest(); r != nil {
			asked = r.ToolName
			if _, err := h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: r.RequestId,
				Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW})); err != nil {
				t.Fatal(err)
			}
		}
		return isTurnFinished(ev)
	})
	if asked != "ExitWorktree" {
		t.Fatalf("asked %q", asked)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("worktree still there: %v", err)
	}
}

func TestChangeAndRequestDirectory(t *testing.T) {
	other := t.TempDir()
	third := t.TempDir()
	var h *harness
	h = startHarness(t, fg.Sequence(
		call("outside", "ChangeDirectory", map[string]any{"path": other}),
		call("denied", "RequestDirectory", map[string]any{"path": other, "reason": "the docs live there"}),
		call("still", "ChangeDirectory", map[string]any{"path": other}),
		call("allowed", "RequestDirectory", map[string]any{"path": other, "reason": "the docs live there"}),
		call("move", "ChangeDirectory", map[string]any{"path": other}),
		call("pwd", "Bash", map[string]any{"command": "pwd", "description": "working directory"}),
		func(r *llm.Request) fg.Reply {
			return call("back", "ChangeDirectory", map[string]any{"path": h.proj})(r)
		},
		call("movedthere", "RequestDirectory", map[string]any{"path": third, "reason": "next", "move": true}),
		say("done"),
	), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	answers := []string{"Deny", "Allow", "Allow"}
	var cards []*agentv1.Question
	var last *agentv1.SessionInfo
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if q := ev.GetQuestion(); q != nil {
			cards = append(cards, q)
			if _, err := h.client.AnswerQuestion(h.ctx, connect.NewRequest(&agentv1.AnswerQuestionRequest{SessionId: sid, RequestId: q.RequestId,
				Answers: []*agentv1.QuestionAnswer{{Selected: []string{answers[0]}}}})); err != nil {
				t.Fatal(err)
			}
			answers = answers[1:]
		}
		if u := ev.GetSessionUpdated(); u != nil {
			last = u
		}
		if r := ev.GetPermissionRequest(); r != nil {
			t.Fatalf("unexpected permission request for %s", r.ToolName)
		}
		return isTurnFinished(ev)
	})
	res := results(h)
	if !strings.Contains(res["outside"], "RequestDirectory") || !strings.Contains(res["still"], "RequestDirectory") {
		t.Fatalf("moved without a grant: %q / %q", res["outside"], res["still"])
	}
	if !strings.Contains(res["denied"], "did not allow") {
		t.Fatalf("denied: %q", res["denied"])
	}
	if !strings.Contains(res["allowed"], "working directory") || !strings.Contains(res["move"], "now works in") {
		t.Fatalf("allowed %q move %q", res["allowed"], res["move"])
	}
	if !strings.Contains(res["pwd"], filepath.Base(other)) {
		t.Fatalf("pwd %q", res["pwd"])
	}
	if !strings.Contains(res["movedthere"], "now works in "+third) || len(cards) != 3 || cards[0].ToolUseId != "denied" {
		t.Fatalf("movedthere %q, cards %d", res["movedthere"], len(cards))
	}
	if last.Cwd != third {
		t.Fatalf("session in %q", last.Cwd)
	}
	// After a restart it is still where it went, and what it gained stays allowed.
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if cs.Msg.Session.Cwd != third {
		t.Fatalf("resumed in %q", cs.Msg.Session.Cwd)
	}

}

func TestProposeBranch(t *testing.T) {
	repo := gittest.Init(t)
	gittest.Run(t, repo, "branch", "elsewhere")
	h := startHarness(t, fg.Sequence(
		call("no", "ProposeBranch", map[string]any{"name": "fix/one"}),
		call("yes", "ProposeBranch", map[string]any{"name": "fix/two", "reason": "the default branch should stay clean"}),
		call("typed", "ProposeBranch", map[string]any{"name": "fix/three"}),
		call("exists", "ProposeBranch", map[string]any{"name": "elsewhere"}),
		call("bad", "ProposeBranch", map[string]any{"name": "-nope"}),
		say("done"),
	), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: repo})
	h.send(sid, "go")
	answers := []*agentv1.QuestionAnswer{{Selected: []string{"Not now"}}, {Selected: []string{"Accept"}}, {Text: "my/name"}, {Selected: []string{"Accept"}}}
	branches := map[string]bool{}
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if q := ev.GetQuestion(); q != nil {
			if _, err := h.client.AnswerQuestion(h.ctx, connect.NewRequest(&agentv1.AnswerQuestionRequest{SessionId: sid, RequestId: q.RequestId, Answers: answers[:1]})); err != nil {
				t.Fatal(err)
			}
			answers = answers[1:]
		}
		if u := ev.GetSessionUpdated(); u != nil {
			branches[u.GitBranch] = true
		}
		return isTurnFinished(ev)
	})
	if got := gittest.Run(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "elsewhere" {
		t.Fatalf("branch %q", got)
	}
	for _, b := range []string{"fix/two", "my/name", "elsewhere"} {
		if !branches[b] {
			t.Fatalf("no update for %s: %v", b, branches)
		}
		if gittest.Run(t, repo, "branch", "--list", b) == "" {
			t.Fatalf("no branch %s", b)
		}
	}
	res := results(h)
	if gittest.Run(t, repo, "branch", "--list", "fix/one") != "" || !strings.Contains(res["no"], "did not accept") {
		t.Fatalf("declined: %q", res["no"])
	}
	if !strings.Contains(res["bad"], "cannot be a branch") {
		t.Fatalf("bad name: %q", res["bad"])
	}
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
}

func TestSyncWithBase(t *testing.T) {
	repo := gittest.Init(t)
	wtPath := filepath.Join(repo, ".harness", "worktrees", "feat")
	var h *harness
	h = startHarness(t, fg.Sequence(
		call("nosync", "SyncWithBase", map[string]any{}),
		call("enter", "EnterWorktree", map[string]any{"name": "feat"}),
		say("in"),
		call("uptodate", "SyncWithBase", map[string]any{}),
		say("nothing"),
		call("merged", "SyncWithBase", map[string]any{}),
		say("merged"),
		call("conflict", "SyncWithBase", map[string]any{}),
		say("conflict"),
		call("again", "SyncWithBase", map[string]any{}),
		say("in progress"),
	), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: repo, PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	turn := func(msg string) {
		t.Helper()
		h.send(sid, msg)
		h.until(ch, func(ev *agentv1.SessionEvent) bool {
			if r := ev.GetPermissionRequest(); r != nil {
				t.Fatalf("unexpected permission request for %s", r.ToolName)
			}
			return isTurnFinished(ev)
		})
	}
	turn("in")
	turn("up to date?")
	gittest.Write(t, repo, "base.txt", "from base\n")
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "base moves")
	turn("merge")
	if _, err := os.Stat(filepath.Join(wtPath, "base.txt")); err != nil {
		t.Fatalf("not merged: %v", err)
	}
	gittest.Write(t, repo, "README.md", "base side\n")
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "base edits")
	gittest.Write(t, wtPath, "README.md", "branch side\n")
	gittest.Run(t, wtPath, "add", "-A")
	gittest.Run(t, wtPath, "commit", "-q", "-m", "branch edits")
	turn("conflict")
	turn("again")
	res := results(h)
	if !strings.Contains(res["nosync"], "does not run in a worktree") || !strings.Contains(res["uptodate"], "already has") {
		t.Fatalf("nosync %q uptodate %q", res["nosync"], res["uptodate"])
	}
	if !strings.Contains(res["merged"], "Merged 1 commit") {
		t.Fatalf("merged %q", res["merged"])
	}
	if !strings.Contains(res["conflict"], "README.md") || !strings.Contains(res["conflict"], "git merge --abort") {
		t.Fatalf("conflict %q", res["conflict"])
	}
	if !strings.Contains(res["again"], "in progress") {
		t.Fatalf("again %q", res["again"])
	}
	// Nothing was resolved for the model: the merge is still there.
	gittest.Run(t, wtPath, "rev-parse", "--verify", "MERGE_HEAD")
}

func TestChangesFollowTheWorktree(t *testing.T) {
	repo := gittest.Init(t)
	wtPath := filepath.Join(repo, ".harness", "worktrees", "feat")
	readme := filepath.Join(wtPath, "README.md")
	h := startHarness(t, fg.Sequence(
		call("enter", "EnterWorktree", map[string]any{"name": "feat"}),
		call("read", "Read", map[string]any{"file_path": readme}),
		call("write", "Write", map[string]any{"file_path": readme, "content": "changed\n"}),
		say("edited"),
		call("discard", "ExitWorktree", map[string]any{"action": "remove", "discard_changes": true}),
		say("gone"),
	), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: repo, PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	h.send(sid, "edit")
	h.until(ch, isTurnFinished)
	diff, err := h.client.GetSessionDiff(h.ctx, connect.NewRequest(&agentv1.GetSessionDiffRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Msg.Files) != 1 || diff.Msg.Files[0].Path != readme {
		t.Fatalf("in the worktree: %+v", diff.Msg.Files)
	}
	h.send(sid, "drop it")
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if r := ev.GetPermissionRequest(); r != nil {
			if _, err := h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: r.RequestId,
				Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW})); err != nil {
				t.Fatal(err)
			}
		}
		return isTurnFinished(ev)
	})
	// The file went with the worktree: that is not a deletion the session made in the checkout it is back in.
	diff, err = h.client.GetSessionDiff(h.ctx, connect.NewRequest(&agentv1.GetSessionDiffRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Msg.Files) != 0 {
		t.Fatalf("after removing: %+v", diff.Msg.Files)
	}
}

// A session created in a worktree leaves it for the same folder of the main checkout, and stays there.
func TestLeaveAWorktreeTheSessionStartedIn(t *testing.T) {
	repo := gittest.Init(t)
	sub := filepath.Join(repo, "src", "app")
	h := startHarness(t, fg.Sequence(
		call("leave", "ExitWorktree", map[string]any{"action": "keep"}),
		say("out"),
	), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: sub, Worktree: &agentv1.WorktreeOptions{Name: "made"}, PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	h.send(sid, "go")
	h.until(ch, isTurnFinished)
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	all, err := h.client.ListSessions(h.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{}))
	if err != nil || len(all.Msg.Sessions) != 1 || all.Msg.Sessions[0].Cwd != sub || all.Msg.Sessions[0].GetWorktree() != nil {
		t.Fatalf("listed: %v %+v", err, all)
	}
	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid, Cwd: filepath.Join(repo, ".harness", "worktrees", "made", "src", "app")}))
	if err != nil {
		t.Fatal(err)
	}
	if got := cs.Msg.Session; got.Cwd != sub || got.GetWorktree() != nil {
		t.Fatalf("resumed %+v", got)
	}
}
