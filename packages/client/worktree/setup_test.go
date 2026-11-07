package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/internal/gittest"
)

// shBuilder runs steps with sh, unconfined: the session's shell and sandbox are the server's business.
func shBuilder(command, dir string) (*exec.Cmd, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	return cmd, nil
}

func TestLoadSetup(t *testing.T) {
	root := t.TempDir()
	if c, err := LoadSetup(root); c != nil || err != nil {
		t.Fatalf("no file: %+v %v", c, err)
	}
	gittest.Write(t, root, SetupFile, `{"shareDependencies": true, "setup": [{"run": "make gen", "dir": "tools/", "timeoutSeconds": 30}]}`)
	c, err := LoadSetup(root)
	if err != nil || !c.ShareDependencies || len(c.Setup) != 1 || c.Setup[0].Dir != "tools/" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range []string{
		`{"setup": [{"run": "x", "dir": "../other"}]}`,
		`{"setup": [{"run": "x", "dir": "/abs"}]}`,
		`{"setup": [{"run": " "}]}`,
		`{"setup": [{"run": "x", "timeoutSeconds": -1}]}`,
		`{"unknown": 1}`,
		`not json`,
	} {
		gittest.Write(t, root, SetupFile, bad)
		if _, err := LoadSetup(root); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestWriteSetupNeverReplaces(t *testing.T) {
	root := t.TempDir()
	p, err := WriteSetup(root, &SetupConfig{ShareDependencies: true})
	if err != nil || p != filepath.Join(root, ".harness", "worktree.json") {
		t.Fatal(p, err)
	}
	if c, err := LoadSetup(root); err != nil || !c.ShareDependencies {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := WriteSetup(root, &SetupConfig{}); !errors.Is(err, ErrSetupExists) {
		t.Fatal(err)
	}
	if c, _ := LoadSetup(root); !c.ShareDependencies {
		t.Fatal("replaced")
	}
}

func TestDependencySteps(t *testing.T) {
	repo := gittest.Init(t)
	gittest.Write(t, repo, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	gittest.Write(t, repo, "pnpm-workspace.yaml", "packages: [web]\n")
	gittest.Write(t, repo, "web/package.json", "{}\n")
	gittest.Write(t, repo, "tools/pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	gittest.Write(t, repo, "legacy/package-lock.json", "{}\n")
	gittest.Write(t, repo, "legacy/yarn.lock", "\n") // yarn wins over npm in one folder
	gittest.Write(t, repo, "berry/yarn.lock", "\n")
	gittest.Write(t, repo, "berry/.yarnrc.yml", "nodeLinker: node-modules\n")
	gittest.Write(t, repo, "py/uv.lock", "\n")
	gittest.Write(t, repo, "go.sum", "\n")
	gittest.Write(t, repo, "vendor/node_modules/x/package-lock.json", "{}\n")
	gittest.Write(t, repo, "untracked/pnpm-lock.yaml", "\n")
	gittest.Run(t, repo, "add", "-A", "--", ".", ":!untracked")
	gittest.Run(t, repo, "commit", "-q", "-m", "locks")

	steps, err := DependencySteps(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range steps {
		got[s.Dir] = s.Manager + ": " + s.Command
	}
	want := map[string]string{
		".":      "pnpm: pnpm install --frozen-lockfile --prefer-offline",
		"tools":  "pnpm: pnpm install --frozen-lockfile --prefer-offline",
		"legacy": "yarn: yarn install --frozen-lockfile --prefer-offline",
		"berry":  "yarn: yarn install --immutable",
		"py":     "uv: uv sync --frozen",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
	if steps[0].Dir != "." {
		t.Fatalf("not sorted: %+v", steps)
	}

	plain := gittest.Init(t)
	if c, s := SuggestSetup(context.Background(), plain); c != nil || s != nil {
		t.Fatalf("suggested for a repository without lockfiles: %+v", c)
	}
	if c, s := SuggestSetup(context.Background(), repo); c == nil || !c.ShareDependencies || len(s) != 5 {
		t.Fatalf("%+v %v", c, s)
	}
}

func TestPnpmOutsideAWorkspace(t *testing.T) {
	root := t.TempDir()
	gittest.Write(t, root, "a/pnpm-workspace.yaml", "packages: [b]\n")
	if c := pnpmCommand(root, "a/b"); strings.Contains(c, "--ignore-workspace") {
		t.Error(c)
	}
	// Nothing in the checkout makes it a workspace member: pnpm must not look above the worktree (the main checkout).
	if c := pnpmCommand(root, "c"); !strings.HasSuffix(c, " --ignore-workspace") {
		t.Error(c)
	}
}

func TestPlanSetup(t *testing.T) {
	repo := gittest.Init(t)
	gittest.Write(t, repo, "pnpm-lock.yaml", "\n")
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "lock")
	steps, err := PlanSetup(context.Background(), repo, &SetupConfig{ShareDependencies: true, Setup: []SetupCommand{
		{Run: "make gen", Dir: "./src/"}, {Run: "slow", TimeoutSeconds: 99999},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 || steps[0].Manager != "pnpm" || steps[1].Dir != "src" || steps[1].Timeout != DefaultStepTimeout || steps[2].Dir != "." || steps[2].Timeout != MaxStepTimeout {
		t.Fatalf("%+v", steps)
	}
	if s, err := PlanSetup(context.Background(), repo, nil); s != nil || err != nil {
		t.Fatal(s, err)
	}
}

func TestRunSetup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	steps := []SetupStep{
		{Command: "pwd; echo made > made.txt", Dir: "sub", Timeout: time.Minute},
		{Command: "echo boom >&2; exit 3", Dir: ".", Timeout: time.Minute},
		{Command: "sleep 30", Dir: ".", Timeout: 200 * time.Millisecond},
		{Command: "touch owned", Dir: "escape", Timeout: time.Minute},
		{Command: "echo after", Dir: ".", Timeout: time.Minute},
	}
	var mu sync.Mutex
	var updates [][]StepResult
	res := RunSetup(context.Background(), root, steps, shBuilder, func(r []StepResult) {
		mu.Lock()
		updates = append(updates, r)
		mu.Unlock()
	}, 10*time.Millisecond)

	if res[0].Status != StepOK || !strings.Contains(res[0].Output, "sub") {
		t.Errorf("step 0: %+v", res[0])
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "made.txt")); err != nil {
		t.Error(err)
	}
	if res[1].Status != StepFailed || res[1].ExitCode != 3 || !strings.Contains(res[1].Output, "boom") {
		t.Errorf("step 1: %+v", res[1])
	}
	if res[2].Status != StepTimedOut || res[2].Duration > 10*time.Second {
		t.Errorf("step 2: %+v", res[2])
	}
	if res[3].Status != StepFailed || !strings.Contains(res[3].Error, "leaves the worktree") {
		t.Errorf("step 3: %+v", res[3])
	}
	if _, err := os.Stat(filepath.Join(outside, "owned")); err == nil {
		t.Error("a step ran outside the worktree")
	}
	if res[4].Status != StepOK {
		t.Errorf("a failure stopped the steps after it: %+v", res[4])
	}
	if SetupSucceeded(res) || !SetupSucceeded(res[:1]) {
		t.Error("SetupSucceeded")
	}
	mu.Lock()
	if len(updates) < len(steps)*2 || updates[0][0].Status != StepPending {
		t.Errorf("progress: %d updates", len(updates))
	}
	mu.Unlock()

	sum := SetupSummary(res, 100)
	for _, want := range []string{"`pwd; echo made > made.txt` in sub: succeeded", "failed with exit code 3", "boom", "timed out after 200ms", "could not run:"} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary lacks %q:\n%s", want, sum)
		}
	}
}

func TestRunSetupCanceled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	ctx, cancel := context.WithCancel(context.Background())
	root := t.TempDir()
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	res := RunSetup(ctx, root, []SetupStep{{Command: "sleep 30", Dir: ".", Timeout: time.Minute}, {Command: "true", Dir: ".", Timeout: time.Minute}}, shBuilder, nil, 0)
	if res[0].Status != StepCanceled || res[1].Status != StepCanceled {
		t.Fatalf("%+v", res)
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{max: 10}
	_, _ = b.Write([]byte("first line\nsecond\nend"))
	if s := b.String(); s != "[…]\nend" || b.Written() != 21 {
		t.Fatalf("%q", s)
	}
}

// TestShareDependenciesPnpm installs a worktree's dependencies with pnpm from a local fixture, offline, with a store of
// the test's own: the worktree gets a node_modules of its own, and the main checkout is not written to.
func TestShareDependenciesPnpm(t *testing.T) {
	pnpm, err := exec.LookPath("pnpm")
	if err != nil || testing.Short() {
		t.Skip("pnpm is not installed")
	}
	if out, err := exec.Command(pnpm, "--version").CombinedOutput(); err != nil {
		t.Skipf("pnpm does not run here: %s", out)
	}
	ctx := context.Background()
	repo := gittest.Init(t)
	store, cache := t.TempDir(), t.TempDir()
	env := []string{"npm_config_store_dir=" + store, "npm_config_cache_dir=" + cache, "npm_config_offline=true",
		"npm_config_update_notifier=false", "COREPACK_ENABLE_STRICT=0"}
	builder := func(command, dir string) (*exec.Cmd, error) {
		cmd := exec.Command("sh", "-c", command)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		return cmd, nil
	}
	gittest.Write(t, repo, "web/vendor/dep/package.json", `{"name": "dep", "version": "1.0.0", "main": "index.js"}`)
	gittest.Write(t, repo, "web/vendor/dep/index.js", "module.exports = 42\n")
	gittest.Write(t, repo, "web/package.json", `{"name": "web", "private": true, "dependencies": {"dep": "file:./vendor/dep"}}`)
	gittest.Write(t, repo, ".gitignore", "node_modules/\n")
	// The main checkout installs once, which writes the lockfile.
	lock, _ := builder("pnpm install --offline --ignore-workspace", filepath.Join(repo, "web"))
	if out, err := lock.CombinedOutput(); err != nil {
		t.Skipf("pnpm cannot install the fixture offline here: %v\n%s", err, out)
	}
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-q", "-m", "web")
	mainModules := filepath.Join(repo, "web", "node_modules")
	before := listTree(t, mainModules)

	wt, err := Create(ctx, repo, Options{Name: "deps"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := SuggestSetup(ctx, wt.SourcePath)
	if cfg == nil {
		t.Fatal("no suggestion")
	}
	steps, err := PlanSetup(ctx, wt.Path, cfg)
	if err != nil || len(steps) != 1 || steps[0].Dir != "web" {
		t.Fatalf("%+v %v", steps, err)
	}
	res := RunSetup(ctx, wt.Path, steps, builder, nil, 0)
	if !SetupSucceeded(res) {
		t.Fatalf("%+v", res)
	}
	mods := filepath.Join(wt.Path, "web", "node_modules")
	if st, err := os.Lstat(mods); err != nil || st.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("node_modules of the worktree: %v %v", st, err)
	}
	data, err := os.ReadFile(filepath.Join(mods, "dep", "index.js"))
	if err != nil || !strings.Contains(string(data), "42") {
		t.Fatalf("%q %v", data, err)
	}
	if real, _ := filepath.EvalSymlinks(filepath.Join(mods, "dep")); strings.HasPrefix(real, mainModules) {
		t.Fatalf("the worktree's dependency is the main checkout's: %s", real)
	}
	if after := listTree(t, mainModules); after != before {
		t.Fatalf("the main checkout's node_modules changed:\n%s\n---\n%s", before, after)
	}
}

func listTree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			b.WriteString(p + " " + info.ModTime().String() + "\n")
		}
		return nil
	})
	return b.String()
}
