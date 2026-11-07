package server_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
)

func setupOf(ev *agentv1.SessionEvent) *agentv1.WorktreeSetup {
	return ev.GetWorktreeSetupUpdated().GetSetup()
}

func untilSetup(h *harness, ch <-chan *agentv1.SessionEvent, state agentv1.WorktreeSetup_State) *agentv1.WorktreeSetup {
	h.t.Helper()
	var got *agentv1.WorktreeSetup
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if s := setupOf(ev); s != nil && s.State == state {
			got = s
			return true
		}
		return false
	})
	return got
}

// The setup of .harness/worktree.json runs in a new session worktree before the first turn, a failed step stops nothing,
// and the first request tells the model what ran.
func TestWorktreeSetupRunsBeforeTheFirstTurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	repo := gittest.Init(t)
	// Not committed: the setup is read from the checkout the worktree comes from.
	gittest.Write(t, repo, ".harness/worktree.json", `{"setup": [
		{"run": "echo installed > ready.txt && echo step one done", "dir": "src"},
		{"run": "echo broken >&2; exit 4"},
		{"run": "pwd > where.txt"}
	]}`)
	h := startHarness(t, fakegateway.Sequence(say("ok")), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: repo, Worktree: &agentv1.WorktreeOptions{Name: "setup"}})
	h.send(sid, "go")
	var setups []*agentv1.WorktreeSetup
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if s := setupOf(ev); s != nil {
			setups = append(setups, s)
		}
		return isTurnFinished(ev)
	})
	if len(setups) == 0 {
		t.Fatal("no setup events")
	}
	last := setups[len(setups)-1]
	wt := filepath.Join(repo, ".harness", "worktrees", "setup")
	if last.State != agentv1.WorktreeSetup_STATE_FAILED || last.WorktreePath != wt || len(last.Steps) != 3 ||
		last.ConfigPath != filepath.Join(repo, ".harness", "worktree.json") {
		t.Fatalf("%+v", last)
	}
	st := last.Steps
	if st[0].Status != agentv1.WorktreeSetupStep_STATUS_OK || st[0].Dir != "src" || !strings.Contains(st[0].Output, "step one done") ||
		st[1].Status != agentv1.WorktreeSetupStep_STATUS_FAILED || st[1].ExitCode != 4 || !strings.Contains(st[1].Output, "broken") ||
		st[2].Status != agentv1.WorktreeSetupStep_STATUS_OK {
		t.Fatalf("steps %+v", st)
	}
	if setups[0].State != agentv1.WorktreeSetup_STATE_RUNNING {
		t.Fatalf("first event %+v", setups[0])
	}
	if _, err := os.Stat(filepath.Join(wt, "src", "ready.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "src", "ready.txt")); err == nil {
		t.Fatal("the setup wrote into the main checkout")
	}
	where, _ := os.ReadFile(filepath.Join(wt, "where.txt"))
	if real, _ := filepath.EvalSymlinks(wt); strings.TrimSpace(string(where)) != real && strings.TrimSpace(string(where)) != wt {
		t.Fatalf("ran in %q", where)
	}
	// The first request carries the result: the turn waited for the setup.
	reqs := h.gw.Requests()
	first := allText(reqs[0])
	for _, want := range []string{"The setup of the worktree " + wt, "succeeded", "failed with exit code 4", "broken", "repeat only what failed"} {
		if !strings.Contains(first, want) {
			t.Errorf("the first request lacks %q:\n%s", want, first)
		}
	}
}

// A project with lockfiles and no setup gets a suggestion; accepting it writes .harness/worktree.json into the checkout
// the worktree came from and runs it there, and the model hears about it. The next worktree runs it by itself.
func TestWorktreeSetupSuggestionAccepted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	repo := gittest.Init(t)
	gittest.Write(t, repo, "web/pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	gittest.Write(t, repo, "web/package.json", "{}\n")
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "web")
	// A pnpm of the test's own: it records where and how it ran, and installs nothing.
	bin := t.TempDir()
	gittest.Write(t, bin, "pnpm", "#!/bin/sh\necho \"pnpm $*\" > installed.txt\necho fake install\n")
	if err := os.Chmod(filepath.Join(bin, "pnpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	h := startHarness(t, fakegateway.Sequence(say("first"), say("second"), say("third")), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: repo, Worktree: &agentv1.WorktreeOptions{Name: "one"}})
	sug := untilSetup(h, ch, agentv1.WorktreeSetup_STATE_SUGGESTED)
	if !strings.Contains(sug.SuggestedConfig, `"shareDependencies": true`) || len(sug.Steps) != 1 || sug.Steps[0].Dir != "web" ||
		sug.Steps[0].Command != "pnpm install --frozen-lockfile --prefer-offline --ignore-workspace" || sug.Steps[0].Manager != "pnpm" {
		t.Fatalf("%+v", sug)
	}
	h.send(sid, "go")
	h.until(ch, isTurnFinished)
	if first := allText(h.gw.Requests()[0]); !strings.Contains(first, "The user was offered a setup") || !strings.Contains(first, "pnpm install") {
		t.Fatalf("the first request:\n%s", first)
	}

	if _, err := h.client.AnswerWorktreeSetup(h.ctx, connect.NewRequest(&agentv1.AnswerWorktreeSetupRequest{SessionId: sid, SetupId: "nope", Accept: true})); connectCode(err) != connect.CodeNotFound {
		t.Fatal(err)
	}
	if _, err := h.client.AnswerWorktreeSetup(h.ctx, connect.NewRequest(&agentv1.AnswerWorktreeSetupRequest{SessionId: sid, SetupId: sug.Id, Accept: true})); err != nil {
		t.Fatal(err)
	}
	done := untilSetup(h, ch, agentv1.WorktreeSetup_STATE_SUCCEEDED)
	if done.Id != sug.Id || !strings.Contains(done.Steps[0].Output, "fake install") {
		t.Fatalf("%+v", done)
	}
	data, err := os.ReadFile(filepath.Join(repo, ".harness", "worktree.json"))
	if err != nil || !strings.Contains(string(data), `"shareDependencies": true`) {
		t.Fatalf("%s %v", data, err)
	}
	wt := filepath.Join(repo, ".harness", "worktrees", "one")
	if got, _ := os.ReadFile(filepath.Join(wt, "web", "installed.txt")); !strings.Contains(string(got), "--frozen-lockfile") {
		t.Fatalf("installed %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "web", "installed.txt")); err == nil {
		t.Fatal("the install ran in the main checkout")
	}
	// A second answer finds nothing waiting.
	if _, err := h.client.AnswerWorktreeSetup(h.ctx, connect.NewRequest(&agentv1.AnswerWorktreeSetupRequest{SessionId: sid, SetupId: sug.Id})); connectCode(err) != connect.CodeNotFound {
		t.Fatal(err)
	}
	h.send(sid, "again")
	h.until(ch, isTurnFinished)
	h.waitRequest("The user accepted the suggested setup", "every step succeeded")

	// The next worktree has a setup: it runs without asking.
	sid2, ch2 := h.create(&agentv1.CreateSessionRequest{Cwd: repo, Worktree: &agentv1.WorktreeOptions{Name: "two"}})
	untilSetup(h, ch2, agentv1.WorktreeSetup_STATE_SUCCEEDED)
	if _, err := os.Stat(filepath.Join(repo, ".harness", "worktrees", "two", "web", "installed.txt")); err != nil {
		t.Fatal(err)
	}
	_ = sid2
}

// Dismissing the suggestion is remembered for the repository on this device, and writes nothing into the project.
func TestWorktreeSetupSuggestionDismissed(t *testing.T) {
	repo := gittest.Init(t)
	gittest.Write(t, repo, "package-lock.json", "{}\n")
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "lock")
	h := startHarness(t, fakegateway.Sequence(say("ok")), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: repo, Worktree: &agentv1.WorktreeOptions{Name: "one"}})
	sug := untilSetup(h, ch, agentv1.WorktreeSetup_STATE_SUGGESTED)
	if sug.Steps[0].Command != "npm ci --prefer-offline --no-audit --no-fund" {
		t.Fatalf("%+v", sug.Steps)
	}
	if _, err := h.client.AnswerWorktreeSetup(h.ctx, connect.NewRequest(&agentv1.AnswerWorktreeSetupRequest{SessionId: sid, SetupId: sug.Id, Accept: false})); err != nil {
		t.Fatal(err)
	}
	untilSetup(h, ch, agentv1.WorktreeSetup_STATE_DISMISSED)
	if _, err := os.Stat(filepath.Join(repo, ".harness", "worktree.json")); err == nil {
		t.Fatal("dismissing wrote the project's setup")
	}
	if data, err := os.ReadFile(filepath.Join(config.Home(), "worktree-setup-dismissed.json")); err != nil || !strings.Contains(string(data), repo) {
		t.Fatalf("%s %v", data, err)
	}
	sid2, ch2 := h.create(&agentv1.CreateSessionRequest{Cwd: repo, Worktree: &agentv1.WorktreeOptions{Name: "two"}})
	h.send(sid2, "go")
	h.until(ch2, func(ev *agentv1.SessionEvent) bool {
		if s := setupOf(ev); s != nil {
			t.Fatalf("suggested again: %+v", s)
		}
		return isTurnFinished(ev)
	})
}

// EnterWorktree runs the setup before it answers, and its result tells the model.
func TestEnterWorktreeRunsTheSetup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	repo := gittest.Init(t)
	gittest.Write(t, repo, ".harness/worktree.json", `{"setup": [{"run": "echo ok > ready.txt", "timeoutSeconds": 60}]}`)
	h := startHarness(t, fakegateway.Sequence(call("enter", "EnterWorktree", map[string]any{"name": "feat"}), say("done")), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: repo, PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	h.send(sid, "go")
	var states []agentv1.WorktreeSetup_State
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if s := setupOf(ev); s != nil {
			states = append(states, s.State)
		}
		return isTurnFinished(ev)
	})
	if len(states) == 0 || states[len(states)-1] != agentv1.WorktreeSetup_STATE_SUCCEEDED {
		t.Fatalf("%v", states)
	}
	res := results(h)["enter"]
	if !strings.Contains(res, "every step succeeded") || !strings.Contains(res, "`echo ok > ready.txt` in .: succeeded") {
		t.Fatalf("result: %s", res)
	}
	if _, err := os.Stat(filepath.Join(repo, ".harness", "worktrees", "feat", "ready.txt")); err != nil {
		t.Fatal(err)
	}
}

// A step that runs past its limit is stopped, and the session goes on.
func TestWorktreeSetupTimeout(t *testing.T) {
	if runtime.GOOS == "windows" || testing.Short() {
		t.Skip("sh")
	}
	repo := gittest.Init(t)
	gittest.Write(t, repo, ".harness/worktree.json", `{"setup": [{"run": "sleep 30", "timeoutSeconds": 1}]}`)
	h := startHarness(t, fakegateway.Sequence(say("ok")), nil)
	start := time.Now()
	_, ch := h.create(&agentv1.CreateSessionRequest{Cwd: repo, Worktree: &agentv1.WorktreeOptions{}})
	got := untilSetup(h, ch, agentv1.WorktreeSetup_STATE_FAILED)
	if got.Steps[0].Status != agentv1.WorktreeSetupStep_STATUS_TIMED_OUT || time.Since(start) > 15*time.Second {
		t.Fatalf("%+v", got.Steps[0])
	}
}

// An isolated agent's worktree is set up before its first turn, and that turn's request says what ran.
func TestSubagentWorktreeSetup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	gittest.Isolate(t)
	var first atomic.Value
	h := startHarness(t, func(n int, r *llm.Request) fakegateway.Reply {
		if isAgentRequest(r) {
			if first.Load() == nil {
				first.Store(requestText(r))
			}
			return fakegateway.Reply{Events: fakegateway.Message("c1", fakegateway.Text("done"))}
		}
		if hasToolResult(r) || strings.Contains(lastUserText(r), "<agent_report>") {
			return fakegateway.Reply{Events: fakegateway.Message("p", fakegateway.Text("ok"))}
		}
		return fakegateway.Reply{Events: spawnCall("a1", "in a worktree", map[string]any{"isolation": "worktree"})}
	}, func(o *bootstrap.Options) { o.PermissionMode = permissions.ModeBypassPermissions })
	gittest.Run(t, h.proj, "init", "-q", "-b", "main")
	gittest.Write(t, h.proj, ".gitignore", "deps/\n")
	gittest.Write(t, h.proj, ".harness/worktree.json", `{"setup": [{"run": "mkdir -p deps && echo x > deps/installed"}]}`)
	gittest.Run(t, h.proj, "add", "-A")
	gittest.Run(t, h.proj, "commit", "-q", "-m", "init")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	var setup *agentv1.WorktreeSetup
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if s := setupOf(ev); s != nil {
			setup = s
		}
		a := ev.GetSubagentUpdated().GetAgent()
		return a != nil && a.State == agentv1.SubagentState_SUBAGENT_STATE_FINISHED
	})
	if setup == nil || setup.State != agentv1.WorktreeSetup_STATE_SUCCEEDED || setup.AgentId == "" {
		t.Fatalf("%+v", setup)
	}
	if got, _ := first.Load().(string); !strings.Contains(got, "The setup of the worktree "+setup.WorktreePath) || !strings.Contains(got, "every step succeeded") {
		t.Fatalf("the agent's first request:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(setup.WorktreePath, "deps", "installed")); err != nil {
		t.Fatal(err)
	}
	h.closeSession(sid)
}
