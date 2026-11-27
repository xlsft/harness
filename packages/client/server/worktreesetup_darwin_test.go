//go:build darwin

package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/gittest"
)

// A worktree's setup runs in the sandbox with the worktree as its only working directory: it writes there, and not into
// the main checkout the worktree sits in.
func TestWorktreeSetupCannotWriteTheMainCheckout(t *testing.T) {
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("no sandbox-exec")
	}
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("HOME", t.TempDir())
	repo := gittest.Init(t)
	gittest.Write(t, repo, ".harness/worktree.json", `{"setup": [{"run": "echo in > inside.txt"}, {"run": "echo out > ../../../escaped.txt"}]}`)
	h := startHarness(t, fg.Sequence(say("ok")), nil)
	_, ch := h.create(&agentv1.CreateSessionRequest{Cwd: repo, Worktree: &agentv1.WorktreeOptions{Name: "boxed"}})
	got := untilSetup(h, ch, agentv1.WorktreeSetup_STATE_FAILED)
	if got.Steps[0].Status != agentv1.WorktreeSetupStep_STATUS_OK || got.Steps[1].Status != agentv1.WorktreeSetupStep_STATUS_FAILED {
		t.Fatalf("%+v", got.Steps)
	}
	if _, err := os.Stat(filepath.Join(repo, ".harness", "worktrees", "boxed", "inside.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "escaped.txt")); err == nil {
		t.Fatal("the setup wrote into the main checkout")
	}
}

// The default setup clones what the main checkout has installed into the worktree, copy-on-write.
func TestWorktreeSetupClonesNodeModules(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	repo := gittest.Init(t)
	gittest.Write(t, repo, ".gitignore", "node_modules/\n")
	gittest.Write(t, repo, "web/pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	gittest.Write(t, repo, ".harness/worktree.json", `{"shareDependencies": true}`)
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "web")
	gittest.Write(t, repo, "web/node_modules/dep/index.js", "module.exports = 42\n")
	h := startHarness(t, fg.Sequence(say("ok")), nil)
	_, ch := h.create(&agentv1.CreateSessionRequest{Cwd: repo, Worktree: &agentv1.WorktreeOptions{Name: "cow"}})
	got := untilSetup(h, ch, agentv1.WorktreeSetup_STATE_SUCCEEDED)
	if len(got.Steps) != 1 || !strings.HasPrefix(got.Steps[0].Command, "clone web/node_modules from ") {
		t.Fatalf("%+v", got.Steps)
	}
	data, err := os.ReadFile(filepath.Join(repo, ".harness", "worktrees", "cow", "web", "node_modules", "dep", "index.js"))
	if err != nil || string(data) != "module.exports = 42\n" {
		t.Fatalf("%q %v", data, err)
	}
}
