package worktree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SetupFile, at the root of a checkout, says what runs in a new worktree before anyone works in it
// (docs/design/worktree-setup.md). It is project configuration: it is read from the checkout the worktree is made from,
// so a setup that is not committed yet still applies, and accepting the suggestion writes it there.
const SetupFile = ".harness/worktree.json"

// Bounds of a setup.
const (
	// DefaultStepTimeout is a step's time limit when the file sets none.
	DefaultStepTimeout = 10 * time.Minute
	// MaxStepTimeout caps what the file may set.
	MaxStepTimeout = time.Hour
	// maxSetupSteps keeps a file that lists too much from running for hours.
	maxSetupSteps = 32
	// SetupOutputBytes is how much of a step's output is kept: its end, where the error is.
	SetupOutputBytes = 16 << 10
)

// SetupConfig is SetupFile.
type SetupConfig struct {
	// ShareDependencies installs the dependencies of every lockfile the worktree has from the package manager's own
	// cache or store (DependencySteps), before Setup runs. Nothing is linked to another checkout.
	ShareDependencies bool `json:"shareDependencies,omitempty"`
	// CloneDependencies (default true) lets ShareDependencies start a folder's node_modules as a copy-on-write clone of
	// the source checkout's (clone.go), where the file system can, and install only when the lockfiles differ. false
	// always installs.
	CloneDependencies *bool `json:"cloneDependencies,omitempty"`
	// Clone lists more folders (relative to the root) to clone copy-on-write from the source checkout when it has them:
	// build caches, generated code. They are cloned before the dependencies and Setup.
	Clone []string `json:"clone,omitempty"`
	// Setup runs in order after the dependencies.
	Setup []SetupCommand `json:"setup,omitempty"`
}

// SetupCommand is one command of SetupConfig.
type SetupCommand struct {
	// Run is the command line, run by the session's shell.
	Run string `json:"run"`
	// Dir is where it runs, relative to the worktree's root ("" or "." for the root). It must stay inside the worktree.
	Dir string `json:"dir,omitempty"`
	// TimeoutSeconds is its time limit (0: DefaultStepTimeout; at most MaxStepTimeout).
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

// SetupStep is one command a setup runs.
type SetupStep struct {
	Command string `json:"command"`
	// Dir is relative to the worktree's root, with forward slashes ("." for the root).
	Dir     string        `json:"dir"`
	Timeout time.Duration `json:"timeout"`
	// Manager names the package manager of a step ShareDependencies added ("" for a SetupCommand).
	Manager string `json:"manager,omitempty"`
	// Clone makes this a clone step: the folder Clone (relative to the root, forward slashes) is cloned copy-on-write
	// from the same folder of the checkout at Source. Command then only describes it; Fallback, when set, is the command
	// that runs instead when the file system cannot clone.
	Clone    string `json:"clone,omitempty"`
	Source   string `json:"source,omitempty"`
	Fallback string `json:"fallback,omitempty"`
}

// Status of a step.
const (
	StepPending  = "pending"
	StepRunning  = "running"
	StepOK       = "ok"
	StepFailed   = "failed"
	StepTimedOut = "timed_out"
	// StepCanceled: the setup stopped before or while it ran (the session closed).
	StepCanceled = "canceled"
)

// StepResult is a step and what became of it.
type StepResult struct {
	SetupStep
	Status   string        `json:"status"`
	ExitCode int           `json:"exitCode"`
	Output   string        `json:"output,omitempty"`
	Duration time.Duration `json:"duration"`
	// Error says why the step could not run or was stopped.
	Error string `json:"error,omitempty"`
}

// ErrSetupExists is returned by WriteSetup when the file is already there.
var ErrSetupExists = errors.New(SetupFile + " already exists")

// LoadSetup reads SetupFile under root. It returns nil and no error when there is none.
func LoadSetup(root string) (*SetupConfig, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(SetupFile)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c SetupConfig
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", SetupFile, err)
	}
	if len(c.Clone) > maxSetupSteps {
		return nil, fmt.Errorf("%s: at most %d folders to clone", SetupFile, maxSetupSteps)
	}
	for i, d := range c.Clone {
		if cd, err := cleanDir(d); err != nil || cd == "." {
			return nil, fmt.Errorf("%s: clone[%d]: %q is not a folder inside the checkout", SetupFile, i, d)
		}
	}
	if len(c.Setup) > maxSetupSteps {
		return nil, fmt.Errorf("%s: at most %d setup commands", SetupFile, maxSetupSteps)
	}
	for i, s := range c.Setup {
		if strings.TrimSpace(s.Run) == "" {
			return nil, fmt.Errorf("%s: setup[%d] has no run", SetupFile, i)
		}
		if _, err := cleanDir(s.Dir); err != nil {
			return nil, fmt.Errorf("%s: setup[%d]: %w", SetupFile, i, err)
		}
		if s.TimeoutSeconds < 0 {
			return nil, fmt.Errorf("%s: setup[%d]: timeoutSeconds is negative", SetupFile, i)
		}
	}
	return &c, nil
}

// WriteSetup writes c as SetupFile under root. It never replaces a file (ErrSetupExists) and returns the path written.
func WriteSetup(root string, c *SetupConfig) (string, error) {
	p := filepath.Join(root, filepath.FromSlash(SetupFile))
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return p, ErrSetupExists
	}
	if err != nil {
		return "", err
	}
	_, werr := f.Write(append(data, '\n'))
	if err := errors.Join(werr, f.Close()); err != nil {
		_ = os.Remove(p)
		return "", err
	}
	return p, nil
}

// cleanDir checks a step's folder: relative, inside the worktree. It returns it with forward slashes, "." for the root.
func cleanDir(dir string) (string, error) {
	d := strings.TrimSpace(strings.ReplaceAll(dir, `\`, "/"))
	if d == "" {
		return ".", nil
	}
	if path.IsAbs(d) || filepath.IsAbs(d) || filepath.VolumeName(d) != "" {
		return "", fmt.Errorf("dir %q must be relative to the worktree", dir)
	}
	d = path.Clean(d)
	if d == ".." || strings.HasPrefix(d, "../") {
		return "", fmt.Errorf("dir %q leaves the worktree", dir)
	}
	return d, nil
}

// lockfile is a lockfile ShareDependencies installs from, in order of preference within one folder.
type lockfile struct {
	name, manager string
	// command returns the install command for the folder dir (relative to root).
	command func(root, dir string) string
	// modules is the folder the install fills, next to the lockfile, when it can be cloned from another checkout ("":
	// it can't: a virtualenv holds absolute paths of the checkout it was made in).
	modules string
	// offline reconciles a cloned folder with a lockfile that differs from the source's: from the cache only, then, if
	// that lacks something, with the network.
	offline func(root, dir string) string
}

var lockfiles = []lockfile{
	{"pnpm-lock.yaml", "pnpm", pnpmCommand, "node_modules", func(root, dir string) string {
		on := pnpmCommand(root, dir)
		return strings.Replace(on, "--prefer-offline", "--offline", 1) + " || " + on
	}},
	{"bun.lock", "bun", bunCommand, "node_modules", bunCommand},
	{"bun.lockb", "bun", bunCommand, "node_modules", bunCommand},
	{"yarn.lock", "yarn", yarnCommand, "node_modules", func(root, dir string) string {
		on := yarnCommand(root, dir)
		if strings.Contains(on, "--prefer-offline") {
			return strings.Replace(on, "--prefer-offline", "--offline", 1) + " || " + on
		}
		return on // Yarn 2+ reads its cache first by itself
	}},
	{"package-lock.json", "npm", npmCommand, "node_modules", func(string, string) string {
		return "npm ci --offline --no-audit --no-fund || " + npmCommand("", "")
	}},
	{"uv.lock", "uv", func(string, string) string { return "uv sync --frozen" }, "", nil},
}

func bunCommand(string, string) string { return "bun install --frozen-lockfile" }
func npmCommand(string, string) string { return "npm ci --prefer-offline --no-audit --no-fund" }

// pnpmCommand installs from pnpm's content-addressed store: packages are hard links (or clones) of the store's files,
// so a worktree gets its own node_modules in seconds when the store has them. A folder that is no workspace of its own
// gets --ignore-workspace: pnpm looks for pnpm-workspace.yaml in the parent folders, and a worktree's parent folders
// are the main checkout's.
func pnpmCommand(root, dir string) string {
	cmd := "pnpm install --frozen-lockfile --prefer-offline"
	for d := dir; ; d = path.Dir(d) {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(d), "pnpm-workspace.yaml")); err == nil {
			return cmd
		}
		if d == "." || d == "/" {
			break
		}
	}
	return cmd + " --ignore-workspace"
}

// yarnCommand: Yarn 2 and later (a .yarnrc.yml next to the lockfile) installs from its global cache with --immutable;
// Yarn 1 from its offline mirror and cache.
func yarnCommand(root, dir string) string {
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), ".yarnrc.yml")); err == nil {
		return "yarn install --immutable"
	}
	return "yarn install --frozen-lockfile --prefer-offline"
}

// DependencySteps finds the tracked lockfiles of the checkout at root and returns the steps that give each folder its
// dependencies. Lockfiles of Go (go.sum) and Cargo need nothing: their caches serve every checkout.
//
// When source (the checkout the worktree came from) has the folder's node_modules and clone is set, the folder is
// cloned copy-on-write from it, with the install as the fallback for a file system that can't clone, and an install
// from the cache only when the two lockfiles differ. Otherwise it is one install from the package manager's shared
// cache or store.
func DependencySteps(ctx context.Context, root, source string, clone bool) ([]SetupStep, error) {
	args := []string{"ls-files", "-z", "--"}
	for _, l := range lockfiles {
		args = append(args, l.name, "**/"+l.name)
	}
	out, err := git(ctx, QueryTimeout, root, args...)
	if err != nil {
		return nil, err
	}
	byDir := map[string]lockfile{}
	rank := map[string]int{}
	for i, l := range lockfiles {
		rank[l.name] = i
	}
	for _, f := range strings.Split(out, "\x00") {
		if f == "" || strings.Contains("/"+f, "/node_modules/") || underWorktrees(f) {
			continue
		}
		dir, name := path.Dir(f), path.Base(f)
		i, ok := rank[name]
		if !ok {
			continue
		}
		if have, seen := byDir[dir]; !seen || i < rank[have.name] {
			byDir[dir] = lockfiles[i]
		}
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	steps := make([]SetupStep, 0, len(dirs))
	for _, d := range dirs {
		l := byDir[d]
		install := SetupStep{Command: l.command(root, d), Dir: d, Timeout: DefaultStepTimeout, Manager: l.manager}
		mods := path.Join(d, l.modules)
		if !clone || l.modules == "" || source == "" || !CloneSupported() || !isDir(filepath.Join(source, filepath.FromSlash(mods))) {
			steps = append(steps, install)
			continue
		}
		steps = append(steps, cloneStep(mods, source, l.manager, install.Command))
		if !sameFile(filepath.Join(root, filepath.FromSlash(d), l.name), filepath.Join(source, filepath.FromSlash(d), l.name)) {
			steps = append(steps, SetupStep{Command: l.offline(root, d), Dir: d, Timeout: DefaultStepTimeout, Manager: l.manager})
		}
	}
	return steps, nil
}

// cloneStep clones rel from source; fallback runs in rel's parent folder when the file system can't clone.
func cloneStep(rel, source, manager, fallback string) SetupStep {
	return SetupStep{Command: fmt.Sprintf("clone %s from %s", rel, source), Dir: path.Dir(rel), Timeout: DefaultStepTimeout,
		Manager: manager, Clone: rel, Source: source, Fallback: fallback}
}

func isDir(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.IsDir()
}

// sameFile reports whether two files have the same content (false when either can't be read).
func sameFile(a, b string) bool {
	x, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	y, err := os.ReadFile(b)
	return err == nil && bytes.Equal(x, y)
}

// PlanSetup returns the steps of c for the worktree at root, made from the checkout at source: the folders of Clone,
// the dependencies when ShareDependencies is set, then the commands of Setup.
func PlanSetup(ctx context.Context, root, source string, c *SetupConfig) ([]SetupStep, error) {
	if c == nil {
		return nil, nil
	}
	var steps []SetupStep
	if source != "" && CloneSupported() {
		for _, d := range c.Clone {
			rel, err := cleanDir(d)
			if err != nil {
				return nil, err
			}
			if isDir(filepath.Join(source, filepath.FromSlash(rel))) {
				steps = append(steps, cloneStep(rel, source, "", ""))
			}
		}
	}
	if c.ShareDependencies {
		deps, err := DependencySteps(ctx, root, source, c.CloneDependencies == nil || *c.CloneDependencies)
		if err != nil {
			return nil, fmt.Errorf("find lockfiles: %w", err)
		}
		steps = append(steps, deps...)
	}
	for _, s := range c.Setup {
		dir, err := cleanDir(s.Dir)
		if err != nil {
			return nil, err
		}
		timeout := DefaultStepTimeout
		if s.TimeoutSeconds > 0 {
			timeout = min(time.Duration(s.TimeoutSeconds)*time.Second, MaxStepTimeout)
		}
		steps = append(steps, SetupStep{Command: strings.TrimSpace(s.Run), Dir: dir, Timeout: timeout})
	}
	return steps, nil
}

// SuggestSetup proposes a setup for the worktree at root, made from the checkout at source, when the project has none:
// ShareDependencies, when it has lockfiles to install from (cloning what source has installed). It returns nil when
// there is nothing to propose.
func SuggestSetup(ctx context.Context, root, source string) (*SetupConfig, []SetupStep) {
	steps, err := DependencySteps(ctx, root, source, true)
	if err != nil || len(steps) == 0 {
		return nil, nil
	}
	return &SetupConfig{ShareDependencies: true}, steps
}

// CommandFunc builds the process of one step: command run by the session's shell in dir, under the session's sandbox
// with the worktree as its only working directory. The caller of RunSetup owns how; RunSetup sets the output and starts it.
type CommandFunc func(command, dir string) (*exec.Cmd, error)

// RunSetup runs steps in the worktree at root, in order, each with its time limit. A failed step does not stop the
// ones after it: a later step may not need what it did, and the report says what failed. progress, when set, hears
// about every change (with a copy), and about output at most every interval. It returns the results of all steps.
func RunSetup(ctx context.Context, root string, steps []SetupStep, build CommandFunc, progress func([]StepResult), interval time.Duration) []StepResult {
	results := make([]StepResult, len(steps))
	for i, s := range steps {
		results[i] = StepResult{SetupStep: s, Status: StepPending, ExitCode: -1}
	}
	var mu sync.Mutex
	report := func() {
		if progress == nil {
			return
		}
		mu.Lock()
		cp := append([]StepResult(nil), results...)
		mu.Unlock()
		progress(cp)
	}
	report()
	for i := range results {
		if ctx.Err() != nil {
			mu.Lock()
			results[i].Status, results[i].Error = StepCanceled, "the setup was stopped"
			mu.Unlock()
			continue
		}
		mu.Lock()
		results[i].Status = StepRunning
		mu.Unlock()
		report()
		runStep(ctx, root, &results[i], &mu, build, report, interval)
		report()
	}
	report()
	return results
}

func runStep(ctx context.Context, root string, r *StepResult, mu *sync.Mutex, build CommandFunc, report func(), interval time.Duration) {
	start := time.Now()
	set := func(f func()) {
		mu.Lock()
		f()
		r.Duration = time.Since(start)
		mu.Unlock()
	}
	dir := filepath.Join(root, filepath.FromSlash(r.Dir))
	if err := insideRoot(root, dir); err != nil {
		set(func() { r.Status, r.Error = StepFailed, err.Error() })
		return
	}
	command := r.Command
	if r.Clone != "" {
		note, err := CloneFolder(ctx, r.Source, root, r.Clone)
		if err == nil {
			set(func() { r.Status, r.ExitCode, r.Output = StepOK, 0, note })
			return
		}
		if r.Fallback == "" || ctx.Err() != nil {
			set(func() { r.Status, r.Error = StepFailed, err.Error() })
			return
		}
		// The file system can't clone: the install it stood for runs instead.
		command = r.Fallback
		set(func() { r.Command, r.Output = command, "could not clone ("+err.Error()+"); installing instead\n" })
	}
	cmd, err := build(command, dir)
	if err != nil {
		set(func() { r.Status, r.Error = StepFailed, err.Error() })
		return
	}
	out := &tailBuffer{max: SetupOutputBytes}
	_, _ = out.Write([]byte(r.Output))
	cmd.Stdout, cmd.Stderr, cmd.Stdin = out, out, nil
	if err := cmd.Start(); err != nil {
		set(func() { r.Status, r.Error = StepFailed, err.Error() })
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(r.Timeout)
	defer timer.Stop()
	var tick <-chan time.Time
	if interval > 0 {
		t := time.NewTicker(interval)
		defer t.Stop()
		tick = t.C
	}
	last := 0
	for {
		select {
		case err := <-done:
			set(func() {
				r.Output = out.String()
				r.ExitCode = cmd.ProcessState.ExitCode()
				if err == nil {
					r.Status = StepOK
				} else {
					r.Status = StepFailed
					if r.ExitCode < 0 {
						r.Error = err.Error()
					}
				}
			})
			return
		case <-tick:
			if n := out.Written(); n != last {
				last = n
				set(func() { r.Output = out.String() })
				report()
			}
		case <-timer.C:
			killGroup(cmd)
			<-done
			set(func() {
				r.Output, r.Status, r.ExitCode = out.String(), StepTimedOut, -1
				r.Error = fmt.Sprintf("killed after %s", r.Timeout)
			})
			return
		case <-ctx.Done():
			killGroup(cmd)
			<-done
			set(func() {
				r.Output, r.Status, r.ExitCode = out.String(), StepCanceled, -1
				r.Error = "the setup was stopped"
			})
			return
		}
	}
}

// insideRoot refuses a folder that leaves the worktree through a symbolic link. A folder that doesn't exist is the
// command's problem; it fails there.
func insideRoot(root, dir string) error {
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	d, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s does not exist in the worktree", dir)
		}
		return err
	}
	if rel, err := filepath.Rel(r, d); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s leaves the worktree", dir)
	}
	return nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu      sync.Mutex
	buf     []byte
	max     int
	written int
	cut     bool
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.written += len(p)
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.max:]...)
		b.cut = true
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := strings.ToValidUTF8(string(b.buf), "")
	if b.cut {
		if i := strings.IndexByte(s, '\n'); i >= 0 && i < len(s)-1 {
			s = s[i+1:]
		}
		s = "[…]\n" + s
	}
	return s
}

func (b *tailBuffer) Written() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.written
}

var _ io.Writer = (*tailBuffer)(nil)

// SetupSucceeded reports whether every step ended well.
func SetupSucceeded(results []StepResult) bool {
	for _, r := range results {
		if r.Status != StepOK {
			return false
		}
	}
	return true
}

// SetupSummary is what the agent is told about a setup that ran: one line per step, and the end of the output of
// those that failed.
func SetupSummary(results []StepResult, tailBytes int) string {
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "- `%s` in %s: %s", r.Command, r.Dir, describeStep(r))
		b.WriteString("\n")
		if r.Status != StepOK && r.Status != StepPending && strings.TrimSpace(r.Output) != "" {
			out := r.Output
			if len(out) > tailBytes {
				out = "[…]" + strings.ToValidUTF8(out[len(out)-tailBytes:], "")
			}
			b.WriteString("  output (end):\n")
			for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
				b.WriteString("    " + l + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func describeStep(r StepResult) string {
	secs := r.Duration.Round(100 * time.Millisecond)
	switch r.Status {
	case StepOK:
		return fmt.Sprintf("succeeded (%s)", secs)
	case StepFailed:
		if r.Error != "" && r.ExitCode < 0 {
			return "could not run: " + r.Error
		}
		return fmt.Sprintf("failed with exit code %d (%s)", r.ExitCode, secs)
	case StepTimedOut:
		return fmt.Sprintf("timed out after %s", r.Timeout)
	case StepCanceled:
		return "stopped before it finished"
	}
	return r.Status
}
