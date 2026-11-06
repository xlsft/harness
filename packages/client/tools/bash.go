package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/sandbox"
)

// Bash limits (overridable with BASH_DEFAULT_TIMEOUT_MS / BASH_MAX_TIMEOUT_MS).
const (
	BashDefaultTimeout   = 2 * time.Minute
	BashMaxTimeout       = 10 * time.Minute
	BashMaxInlineChars   = 30000
	BashPreviewChars     = 2000
	BashMaxErrorChars    = 10000
	bashMaxCaptureBytes  = 64 << 20
	bashSnapshotDeadline = 10 * time.Second
)

// Shell keeps state that survives between Bash calls: the working directory
// and the login-shell snapshot of aliases/functions/PATH. Environment
// variables do not persist: every call runs in a fresh shell process.
type Shell struct {
	env  *Env
	mu   sync.Mutex
	cwd  string
	path string
	kind string // bash | zsh | sh
	// moves counts the session's moves (relocate): a command that started before one does not set cwd when it ends.
	moves int

	snapOnce sync.Once
	snapPath string

	// running foreground commands that may be moved to the background
	running map[*fgExec]struct{}
}

// NewShell creates shell state rooted at env.Cwd.
func NewShell(env *Env) *Shell {
	s := &Shell{env: env, cwd: env.Cwd}
	s.path, s.kind = detectShell()
	return s
}

// relocate moves the shell with the session, also while a command runs: that one finishes where it is, and the next
// starts in dir.
func (s *Shell) relocate(dir string) {
	s.mu.Lock()
	s.cwd = dir
	s.moves++
	s.mu.Unlock()
}

// Cwd returns the current shell directory.
func (s *Shell) Cwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

func detectShell() (string, string) {
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("bash"); err == nil {
			return p, "bash"
		}
		return "sh", "sh"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		switch filepath.Base(sh) {
		case "bash", "zsh":
			if _, err := os.Stat(sh); err == nil {
				return sh, filepath.Base(sh)
			}
		}
	}
	if p, err := exec.LookPath("bash"); err == nil {
		return p, "bash"
	}
	return "/bin/sh", "sh"
}

// snapshotScript dumps aliases, non-underscore functions and PATH.
func snapshotScript(kind, out string) string {
	q := shellQuote(out)
	switch kind {
	case "zsh":
		return `{ for __f in ${(k)functions}; do [[ $__f == _* ]] || typeset -f -- $__f; done; alias -L; print -r -- "setopt aliases"; print -r -- "export PATH=${(q)PATH}"; } >| ` + q + ` 2>/dev/null`
	case "bash":
		return `{ declare -F | awk '{print $3}' | grep -v '^_' | while read -r __f; do declare -f "$__f"; done; alias -p; echo "shopt -s expand_aliases"; printf 'export PATH=%q\n' "$PATH"; } > ` + q + ` 2>/dev/null`
	}
	return ""
}

func (s *Shell) snapshot() string {
	s.snapOnce.Do(func() {
		if s.kind == "sh" || s.env.ScratchDir == "" || os.Getenv("HARNESS_NO_SHELL_SNAPSHOT") == "1" {
			return
		}
		if err := os.MkdirAll(s.env.ScratchDir, 0o700); err != nil {
			return
		}
		path := filepath.Join(s.env.ScratchDir, "shell-snapshot."+s.kind)
		ctx, cancel := context.WithTimeout(context.Background(), bashSnapshotDeadline)
		defer cancel()
		cmd := exec.CommandContext(ctx, s.path, "-l", "-i", "-c", snapshotScript(s.kind, path))
		cmd.Dir = s.env.CurrentDir()
		setNewSession(cmd)
		cmd.WaitDelay = time.Second
		if err := cmd.Run(); err != nil {
			if _, statErr := os.Stat(path); statErr != nil {
				return
			}
		}
		s.snapPath = path
	})
	return s.snapPath
}

// ExecResult is the raw result of one shell command.
type ExecResult struct {
	Stdout, Stderr string
	ExitCode       int
	TimedOut       bool
	Interrupted    bool
	Truncated      bool
	// CwdReset is where the shell went back to when the command left the working directories, or when the
	// directory it should have started in no longer exists.
	CwdReset string
	// StartCwd is the directory the command started in.
	StartCwd string
	// EndCwd is the directory the command ended in, when that is not StartCwd (with CwdReset: where it was left).
	EndCwd string
	// CwdGone is set when the shell's directory had disappeared before the command (EndCwd names it).
	CwdGone bool
	// Denial says what the OS sandbox refused the command, when it ran in it and that can be told.
	Denial *sandbox.Denial
	// Background is set when the command was moved to the background.
	Background *TaskInfo
	// BackgroundReason is BackgroundAuto (timeout), BackgroundUser or
	// BackgroundQueuedMessage.
	BackgroundReason string
}

// ExecOptions configure Shell.Run.
type ExecOptions struct {
	Timeout time.Duration
	// Tasks enables moving the command to the background (on timeout when
	// AutoBackground is set, or when the user asks via BackgroundRunning).
	Tasks          *TaskManager
	AutoBackground bool
	Description    string
	ToolUseID      string
	// OnOutput, when set, receives every chunk the command writes (stream 0 is stdout, 1 stderr) as it is
	// captured, until the command moves to the background. It is called with the capture lock held: it must
	// not block.
	OnOutput func(stream int, p []byte)
	// Confine runs the command in the session's OS sandbox: what the model chose. What the user typed does not.
	Confine bool
}

// switchWriter captures stdout/stderr in memory until the command is moved
// to the background, then redirects both into the task output file.
type switchWriter struct {
	mu       sync.Mutex
	bufs     [2]bytes.Buffer
	cut      bool
	file     *os.File
	onOutput func(stream int, p []byte)
}

type swStream struct {
	w *switchWriter
	i int
}

func (s swStream) Write(p []byte) (int, error) {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if s.w.file != nil {
		return s.w.file.Write(p)
	}
	if s.w.onOutput != nil {
		s.w.onOutput(s.i, p)
	}
	b := &s.w.bufs[s.i]
	if room := bashMaxCaptureBytes - b.Len(); room < len(p) {
		if room > 0 {
			b.Write(p[:room])
		}
		s.w.cut = true
		return len(p), nil
	}
	return b.Write(p)
}

func (w *switchWriter) snapshot() (string, string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bufs[0].String(), w.bufs[1].String(), w.cut
}

func (w *switchWriter) switchTo(f *os.File) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f.Write(w.bufs[0].Bytes())
	f.Write(w.bufs[1].Bytes())
	w.file = f
}

// Why a Bash command runs in the background (ExecResult.BackgroundReason and
// the "backgroundReason" field of the Bash structured result).
const (
	// BackgroundUser: the user moved the running command to the background.
	BackgroundUser = "user"
	// BackgroundQueuedMessage: moved so a queued user message could be read.
	BackgroundQueuedMessage = "queued_message"
	// BackgroundAgent: started with run_in_background.
	BackgroundAgent = "agent"
	// BackgroundAuto: moved when it hit its timeout.
	BackgroundAuto = "auto"
)

type fgExec struct {
	once      sync.Once
	bg        chan struct{}
	toolUseID string
	reason    string
}

func (fg *fgExec) background(reason string) {
	fg.once.Do(func() {
		fg.reason = reason
		close(fg.bg)
	})
}

// BackgroundRunning asks every running foreground command that can be
// backgrounded to move to the background, giving reason (BackgroundUser or
// BackgroundQueuedMessage). It reports whether there was one.
func (s *Shell) BackgroundRunning(reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for fg := range s.running {
		fg.background(reason)
	}
	return len(s.running) > 0
}

// BackgroundToolCall moves the running foreground command of one tool call to
// the background. It reports whether such a command was running.
func (s *Shell) BackgroundToolCall(toolUseID, reason string) bool {
	if toolUseID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for fg := range s.running {
		if fg.toolUseID == toolUseID {
			fg.background(reason)
			found = true
		}
	}
	return found
}

// Command builds a dev server's process for a command line (not started): it runs in cwd the way the Bash tool runs
// it (same shell, PATH and environment), as the leader of a new process group, under the sandbox's policy for servers. The
// caller owns the process.
func (s *Shell) Command(command, cwd string) *exec.Cmd {
	cmd, _ := s.command(command, "", cwd, confineServer)
	return cmd
}

// confinement says how a command relates to the session's OS sandbox.
type confinement int

const (
	unconfined confinement = iota
	confineCommand
	confineServer
)

// command builds the shell process for a command line (not started). With a sandbox that applies, it also returns what
// explains the command's failures. A sandbox that should apply and cannot means the process does nothing but say so: the
// command must not run without it, and every caller already shows what a failed command printed.
func (s *Shell) command(command, cwdFile, cwd string, how confinement) (*exec.Cmd, *sandbox.Wrapped) {
	snap := s.snapshot()
	var script strings.Builder
	if snap != "" {
		script.WriteString(". " + shellQuote(snap) + " >/dev/null 2>&1\n")
	}
	if s.kind == "bash" {
		script.WriteString("shopt -s expand_aliases 2>/dev/null\n")
	}
	filter, memoryMB := s.env.ShellLimits()
	script.WriteString(memoryLimitPrefix(runtime.GOOS, memoryMB))
	script.WriteString(`__harness_cmd=$__HARNESS_CMD; __harness_cwd=$__HARNESS_CWD_FILE; unset __HARNESS_CMD __HARNESS_CWD_FILE` + "\n")
	script.WriteString(`if [ -n "$__harness_cwd" ]; then trap 'pwd -P > "$__harness_cwd" 2>/dev/null' EXIT; fi` + "\n")
	script.WriteString(`eval "$__harness_cmd"` + "\n")
	argv := []string{s.path, "-c", script.String()}
	var wrapped *sandbox.Wrapped
	if how != unconfined {
		kind := sandbox.KindCommand
		if how == confineServer {
			kind = sandbox.KindServer
		}
		w, err := s.env.Sandbox.Wrap(sandbox.Request{Kind: kind, WorkDirs: s.env.WorkingDirs(), ScratchDir: s.env.ScratchDir}, argv)
		switch {
		case err != nil:
			argv = []string{s.path, "-c", `printf 'harness: %s\n' "$1" >&2; exit 126`, "sh", err.Error()}
		case w != nil:
			argv, wrapped = w.Argv, w
		}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	// The session environment's variables override the client's; the harness entries come last so they cannot
	// be overridden (exec keeps the last value of a name).
	cmd.Env = append(os.Environ(), s.env.ExtraEnv()...)
	if filter != nil {
		cmd.Env = filter(cmd.Env)
	}
	if wrapped != nil {
		cmd.Env = append(cmd.Env, wrapped.Env...)
	}
	cmd.Env = append(cmd.Env,
		"__HARNESS_CMD="+command,
		"__HARNESS_CWD_FILE="+cwdFile,
		"HARNESS=1",
		"GIT_EDITOR=true",
	)
	setProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	return cmd, wrapped
}

// Exec runs command in the persistent cwd and kills it on timeout.
func (s *Shell) Exec(ctx context.Context, command string, timeout time.Duration) (ExecResult, error) {
	return s.Run(ctx, command, ExecOptions{Timeout: timeout})
}

// Run runs command in the persistent cwd. With o.Tasks set the command can
// move to the background instead of being killed.
func (s *Shell) Run(ctx context.Context, command string, o ExecOptions) (ExecResult, error) {
	s.mu.Lock()
	cwd, moves := s.cwd, s.moves
	s.mu.Unlock()
	gone := ""
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		gone, cwd = cwd, s.env.CurrentDir()
		s.mu.Lock()
		s.cwd = cwd
		s.mu.Unlock()
	}
	cwdFile := ""
	if s.env.ScratchDir != "" {
		_ = os.MkdirAll(s.env.ScratchDir, 0o700)
		f, err := os.CreateTemp(s.env.ScratchDir, "cwd-*")
		if err == nil {
			cwdFile = f.Name()
			f.Close()
		}
	}
	handedOff := false
	defer func() {
		if cwdFile != "" && !handedOff {
			os.Remove(cwdFile)
		}
	}()

	how := unconfined
	if o.Confine {
		how = confineCommand
	}
	cmd, wrapped := s.command(command, cwdFile, cwd, how)
	sw := &switchWriter{onOutput: o.OnOutput}
	cmd.Stdout, cmd.Stderr = swStream{sw, 0}, swStream{sw, 1}
	if err := cmd.Start(); err != nil {
		return ExecResult{StartCwd: cwd}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var bgReq chan struct{}
	var bgFg *fgExec
	if o.Tasks != nil {
		fg := &fgExec{bg: make(chan struct{}), toolUseID: o.ToolUseID}
		bgReq, bgFg = fg.bg, fg
		s.mu.Lock()
		if s.running == nil {
			s.running = map[*fgExec]struct{}{}
		}
		s.running[fg] = struct{}{}
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.running, fg)
			s.mu.Unlock()
		}()
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = BashDefaultTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	res := ExecResult{StartCwd: cwd}
	if gone != "" {
		res.CwdReset, res.EndCwd, res.CwdGone = cwd, gone, true
	}
	handoff := func(reason string) (ExecResult, error) {
		id := newTaskID()
		f, path, err := o.Tasks.outputFile(id)
		if err != nil {
			_ = killProcessGroup(cmd)
			<-done
			res.TimedOut = true
			res.ExitCode = -1
			return res, nil
		}
		res.Stdout, res.Stderr, res.Truncated = sw.snapshot()
		sw.switchTo(f)
		handedOff = true
		cleanup := func() {
			if cwdFile != "" {
				os.Remove(cwdFile)
			}
			noteDenialInOutput(wrapped, path, cwd)
		}
		info := o.Tasks.adopt(id, path, f, cmd, done, command, o.Description, o.ToolUseID, 0, cleanup)
		res.Background = &info
		res.BackgroundReason = reason
		return res, nil
	}

	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = killProcessGroup(cmd)
		err = <-done
		res.Interrupted = true
	case <-timer.C:
		if o.Tasks != nil && o.AutoBackground && !startsWithSleep(command) {
			return handoff(BackgroundAuto)
		}
		_ = killProcessGroup(cmd)
		err = <-done
		res.TimedOut = true
	case <-bgReq:
		reason := BackgroundUser
		s.mu.Lock()
		if bgFg != nil && bgFg.reason != "" {
			reason = bgFg.reason
		}
		s.mu.Unlock()
		return handoff(reason)
	}
	res.Stdout, res.Stderr, res.Truncated = sw.snapshot()
	var exitErr *exec.ExitError
	switch {
	case res.Interrupted, res.TimedOut:
		res.ExitCode = -1
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	case err != nil && !errors.Is(err, exec.ErrWaitDelay):
		return res, err
	}
	if !res.Interrupted && !res.TimedOut {
		res.Denial = wrapped.Diagnose(res.Stdout+"\n"+res.Stderr, cwd, res.ExitCode != 0)
	}

	// A stopped command leaves the directory as it was: the shell's EXIT trap may not have run. A failed one
	// moves it like any other, its `cd` did happen.
	if cwdFile != "" && !res.Interrupted && !res.TimedOut {
		if data, err := os.ReadFile(cwdFile); err == nil {
			if nd := strings.TrimSpace(string(data)); nd != "" {
				s.mu.Lock()
				moved := s.moves != moves
				s.mu.Unlock()
				// If the session moved while the command ran, the shell stays where the move put it.
				if !moved {
					s.settleCwd(&res, nd)
				}
			}
		}
	}
	return res, nil
}

// settleCwd records the directory a command ended in: the shell stays there inside the working directories and
// goes back to the primary one outside them. pwd -P prints the resolved path while the start may go through a
// symlink, so "the same directory" compares resolved paths.
func (s *Shell) settleCwd(res *ExecResult, end string) {
	inside := within(end, s.env.WorkingDirs())
	s.mu.Lock()
	if inside {
		s.cwd = end
	} else {
		s.cwd = s.env.CurrentDir()
	}
	s.mu.Unlock()
	if evalPath(end) == evalPath(res.StartCwd) && inside {
		return
	}
	res.EndCwd, res.CwdGone = end, false
	res.CwdReset = ""
	if !inside {
		res.CwdReset = s.env.CurrentDir()
	}
}

// CwdNote is the line that closes a Bash result when the command ended in another directory ("" when it did not).
func (r ExecResult) CwdNote() string {
	switch {
	case r.CwdGone:
		return fmt.Sprintf("Shell cwd was reset to %s: %s no longer exists.", r.CwdReset, r.EndCwd)
	case r.CwdReset != "":
		return fmt.Sprintf("Shell cwd was reset to %s: %s is outside the working directories.", r.CwdReset, r.EndCwd)
	case r.EndCwd != "":
		return "Shell now stays in " + r.EndCwd + "."
	}
	return ""
}

// startsWithSleep reports whether the command line starts with sleep (such
// commands are not auto-backgrounded on timeout).
func startsWithSleep(command string) bool {
	cmds := permissions.SplitCommand(command)
	return len(cmds) > 0 && cmds[0].Normalize().Name() == "sleep"
}

// shellQuote single-quotes s for POSIX shells.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// noteDenialInOutput adds the sandbox's explanation to the output file of a background command that ended: nobody reads
// its result, only that file.
func noteDenialInOutput(w *sandbox.Wrapped, path, cwd string) {
	if w == nil {
		return
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return
	}
	defer f.Close()
	var tail string
	if st, err := f.Stat(); err == nil {
		buf := make([]byte, min(st.Size(), BashMaxErrorChars))
		if n, err := f.ReadAt(buf, st.Size()-int64(len(buf))); err == nil || errors.Is(err, io.EOF) {
			tail = string(buf[:n])
		}
	}
	if d := w.Diagnose(tail, cwd, true); d != nil {
		_, _ = f.WriteString("\n" + d.Note(OutsideSandboxParam) + "\n")
	}
}

// Bash runs shell commands.
type Bash struct{}

// OutsideSandboxParam is the input field with which the model asks to run a command outside the OS sandbox. The permission
// check turns it into a user-only prompt (permissions/sandbox.go).
const OutsideSandboxParam = "run_outside_sandbox"

type bashInput struct {
	Command           string   `json:"command"`
	Description       string   `json:"description"`
	Timeout           *float64 `json:"timeout"`
	RunInBackground   bool     `json:"run_in_background"`
	RunOutsideSandbox bool     `json:"run_outside_sandbox"`
	Stdin             bool     `json:"stdin"`
}

func (*Bash) Name() string { return "Bash" }

// ReadOnly reports read-only command lines (built-in allowlist) that do not
// change directory, so they may run in parallel.
func (*Bash) ReadOnly(input json.RawMessage) bool {
	var in bashInput
	if json.Unmarshal(input, &in) != nil || in.RunInBackground || in.RunOutsideSandbox || !permissions.IsReadOnlyCommandLine(in.Command) {
		return false
	}
	for _, c := range permissions.SplitCommand(in.Command) {
		if n := c.Normalize().Name(); n == "cd" || n == "pushd" || n == "popd" {
			return false
		}
	}
	return true
}

// BashCommand extracts the command from a Bash input (used by permissions).
func BashCommand(input json.RawMessage) string {
	var in bashInput
	_ = json.Unmarshal(input, &in)
	return in.Command
}

// WantsOutsideSandbox reports whether a call's input asks to run outside the OS sandbox (used by permissions).
func WantsOutsideSandbox(input json.RawMessage) bool {
	var in struct {
		RunOutsideSandbox bool `json:"run_outside_sandbox"`
	}
	_ = json.Unmarshal(input, &in)
	return in.RunOutsideSandbox
}

func envDuration(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return def
}

func backgroundCwdNote(cwd string) string {
	return fmt.Sprintf("Session cwd remains %s; directory changes made by the backgrounded command do not apply to subsequent commands.", cwd)
}

func changesDir(command string) bool {
	for _, c := range permissions.SplitCommand(command) {
		if n := c.Normalize().Name(); n == "cd" || n == "pushd" || n == "popd" || n == "chdir" {
			return true
		}
	}
	return false
}

func (*Bash) Run(ctx context.Context, env *Env, call Call) Result {
	var in bashInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return ErrorResult("InputValidationError: command is required")
	}
	if strings.TrimSpace(in.Description) == "" {
		return ErrorResult("InputValidationError: description is required: a noun phrase of 2 to 6 words naming what the command works on or produces")
	}
	maxT := envDuration("BASH_MAX_TIMEOUT_MS", BashMaxTimeout)
	defT := envDuration("BASH_DEFAULT_TIMEOUT_MS", BashDefaultTimeout)
	if defT > maxT {
		maxT = defT
	}
	timeout := defT
	var requested time.Duration
	if in.Timeout != nil && *in.Timeout > 0 {
		requested = time.Duration(*in.Timeout) * time.Millisecond
		timeout = requested
	}
	if timeout > maxT {
		timeout = maxT
	}
	if in.Stdin && !in.RunInBackground {
		return ErrorResult("InputValidationError: stdin needs run_in_background: TaskInput writes to a background command")
	}
	if in.RunInBackground {
		if env.Tasks == nil {
			return ErrorResult("Background commands are not available in this session. Run the command in the foreground.")
		}
		start := env.Tasks.Start
		if in.RunOutsideSandbox {
			start = env.Tasks.StartOutside
		}
		if in.Stdin {
			start = func(sh *Shell, command, description, toolUseID string, timeout time.Duration) (TaskInfo, error) {
				return env.Tasks.StartWithStdin(sh, command, description, toolUseID, timeout, in.RunOutsideSandbox)
			}
		}
		info, err := start(env.Shell, in.Command, in.Description, call.ID, requested)
		if err != nil {
			return ErrorResult("failed to start background command: %v", err)
		}
		msg := BackgroundNote(env, BackgroundAgent, info.ID, info.OutputPath, 0)
		if changesDir(in.Command) {
			msg += "\n" + backgroundCwdNote(env.Shell.Cwd())
		}
		structured := map[string]any{"stdout": "", "stderr": "", "interrupted": false, "backgroundTaskId": info.ID, "outputFile": info.OutputPath, "backgroundReason": BackgroundAgent}
		markOutsideSandbox(env, in.RunOutsideSandbox, structured)
		r := TextResult(msg, structured)
		r.Summary = "Started background task " + info.ID
		return r
	}
	res, err := env.Shell.Run(ctx, in.Command, ExecOptions{Timeout: timeout, Tasks: env.Tasks, AutoBackground: true, Description: in.Description, ToolUseID: call.ID, Confine: !in.RunOutsideSandbox})
	if err != nil {
		return ErrorResult("failed to run command: %v", err)
	}
	if bg := res.Background; bg != nil {
		msg := BackgroundNote(env, res.BackgroundReason, bg.ID, bg.OutputPath, timeout)
		if changesDir(in.Command) {
			msg += "\n" + backgroundCwdNote(res.StartCwd)
		}
		structured := map[string]any{"stdout": res.Stdout, "stderr": res.Stderr, "interrupted": false, "backgroundTaskId": bg.ID, "outputFile": bg.OutputPath, "backgroundReason": res.BackgroundReason}
		markOutsideSandbox(env, in.RunOutsideSandbox, structured)
		r := TextResult(msg, structured)
		r.Summary = "Moved to background task " + bg.ID
		return r
	}
	structured := map[string]any{
		"stdout": res.Stdout, "stderr": res.Stderr, "interrupted": res.Interrupted,
		"exitCode": res.ExitCode,
	}
	markOutsideSandbox(env, in.RunOutsideSandbox, structured)

	out := strings.TrimRight(res.Stdout, "\n")
	if e := strings.TrimRight(res.Stderr, "\n"); e != "" {
		if out != "" {
			out += "\n"
		}
		out += e
	}
	isErr := false
	var notes []string
	switch {
	case res.Interrupted:
		isErr = true
		notes = append(notes, "[Command interrupted]")
	case res.TimedOut:
		isErr = true
		notes = append(notes, fmt.Sprintf("Command timed out after %s", timeout))
	case res.ExitCode != 0:
		if !(res.ExitCode == 1 && exitOneIsOK(in.Command)) {
			isErr = true
			notes = append(notes, fmt.Sprintf("Exit code %d", res.ExitCode))
		}
	}
	if !isErr && res.ExitCode == 0 {
		trackBashReads(env, res.StartCwd, in.Command)
	}
	if res.Truncated {
		notes = append(notes, "[output exceeded capture limit and was cut]")
	}
	if note := res.CwdNote(); note != "" {
		notes = append(notes, note)
		structured["cwdNote"] = note
	}
	// Last, so that a frontend can take it off the end of the text and show its short form instead.
	if res.Denial != nil {
		note := res.Denial.Note(OutsideSandboxParam)
		notes = append(notes, note)
		structured["sandboxNote"] = note
		structured["sandboxDenied"] = res.Denial.Summary()
	}

	if isErr {
		out = headTail(out, BashMaxErrorChars)
	} else if len(out) > BashMaxInlineChars {
		saved, err := saveOutput(env, call.ID, out)
		if err == nil {
			structured["outputFile"] = saved
			out = fmt.Sprintf("Output too large (%.1fKB). Full output saved to: %s\n\nPreview (first 2KB):\n%s\n…", float64(len(out))/1024, saved, truncateUTF8(out, BashPreviewChars))
		} else {
			out = headTail(out, BashMaxInlineChars)
		}
	}
	if len(notes) > 0 {
		if out != "" {
			out += "\n"
		}
		out += strings.Join(notes, "\n")
	}
	if out == "" {
		out = "(no output)"
	}
	r := TextResult(out, structured)
	r.IsError = isErr
	switch {
	case res.Interrupted:
		r.Summary = "Interrupted"
	case res.TimedOut:
		r.Summary = "Timed out"
	default:
		r.Summary = fmt.Sprintf("Exit code %d", res.ExitCode)
	}
	return r
}

// markOutsideSandbox records that a command the sandbox would have confined ran outside it, for the mark on its row. A
// session with the sandbox off has nothing to mark: every row would carry it.
func markOutsideSandbox(env *Env, outside bool, structured map[string]any) {
	if st := env.Sandbox.State(); outside && (st.Active || st.Refusing) {
		structured["sandbox"] = "outside"
	}
}

// Reminder ids of the Bash tool_result notes about a background command
// (packages/core/prompts/reminders/<id>.md), by reason.
var backgroundNoteKeys = map[string]string{
	BackgroundUser:          "bash_background_user",
	BackgroundQueuedMessage: "bash_background_queued_message",
	BackgroundAgent:         "bash_background_agent",
	BackgroundAuto:          "bash_background_timeout",
}

// Fallbacks for a bundle without the note templates.
var backgroundNoteFallbacks = map[string]string{
	BackgroundUser:          "The user moved this command to the background while it was running. It keeps running as background task {{task_id}}; its output so far and from now on goes to {{output_path}}. Read new output with TaskOutput or stop it with TaskStop, using that task id. You will be notified when it finishes.",
	BackgroundQueuedMessage: "This command was moved to the background so that a message the user sent while it was running could be delivered now. It keeps running as background task {{task_id}}; its output so far and from now on goes to {{output_path}}. Read new output with TaskOutput or stop it with TaskStop, using that task id. You will be notified when it finishes.",
	BackgroundAgent:         "You started this command in the background (run_in_background). It runs as background task {{task_id}}; its output goes to {{output_path}}. Read new output with TaskOutput or stop it with TaskStop, using that task id. You will be notified when it finishes.",
	BackgroundAuto:          "This command did not finish within its {{timeout}} timeout, so it was moved to the background instead of being stopped. It keeps running as background task {{task_id}}; its output so far and from now on goes to {{output_path}}. Read new output with TaskOutput or stop it with TaskStop, using that task id. You will be notified when it finishes.",
}

// BackgroundNote is the tool_result text of a Bash command that runs in the
// background for reason (BackgroundUser, BackgroundQueuedMessage,
// BackgroundAgent or BackgroundAuto). timeout is used by BackgroundAuto.
func BackgroundNote(env *Env, reason, taskID, outputPath string, timeout time.Duration) string {
	if _, ok := backgroundNoteKeys[reason]; !ok {
		reason = BackgroundUser
	}
	tmpl := ""
	if env != nil {
		tmpl = env.reminder(backgroundNoteKeys[reason])
	}
	if tmpl == "" {
		tmpl = backgroundNoteFallbacks[reason]
	}
	return prompts.Render(tmpl, map[string]string{"task_id": taskID, "output_path": outputPath, "timeout": formatDuration(timeout)}, nil)
}

func formatDuration(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return d.String()
}

var sedPrintRe = regexp.MustCompile(`^[0-9]+(,[0-9]+)?p$`)

// trackBashReads marks files read by cat/head/tail/nl/bat, sed -n 'x,yp' and
// single-file grep/rg as read, satisfying read-before-edit.
func trackBashReads(env *Env, cwd, command string) {
	resolve := func(p string) string {
		if strings.ContainsAny(p, "$`*?[") {
			return ""
		}
		p = resolvePath(cwd, p)
		if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
			return ""
		}
		return p
	}
	for _, c := range permissions.SplitCommand(command) {
		c = c.Normalize()
		if c.Unsafe {
			continue
		}
		args := c.Args()
		var files []string
		switch c.Name() {
		case "cat", "nl", "bat", "batcat":
			for _, a := range args {
				if !strings.HasPrefix(a, "-") {
					files = append(files, a)
				}
			}
		case "head", "tail":
			for i := 0; i < len(args); i++ {
				a := args[i]
				if a == "-n" || a == "-c" || a == "--lines" || a == "--bytes" {
					i++
					continue
				}
				if !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+") {
					files = append(files, a)
				}
			}
		case "sed":
			quiet := false
			var ops []string
			for _, a := range args {
				switch {
				case a == "-n" || a == "--quiet" || a == "--silent":
					quiet = true
				case strings.HasPrefix(a, "-"):
					quiet = quiet || strings.Contains(strings.TrimPrefix(a, "-"), "n") && !strings.HasPrefix(a, "--")
					if strings.Contains(a, "i") && !strings.HasPrefix(a, "--") {
						ops = nil
						quiet = false
					}
				default:
					ops = append(ops, a)
				}
			}
			if quiet && len(ops) == 2 && sedPrintRe.MatchString(ops[0]) {
				files = append(files, ops[1])
			}
		case "grep", "egrep", "fgrep", "rg":
			var ops []string
			patternGiven, recursive := false, false
			for i := 0; i < len(args); i++ {
				a := args[i]
				switch {
				case a == "-e" || a == "--regexp" || a == "-f":
					patternGiven = true
					i++
				case a == "-A" || a == "-B" || a == "-C" || a == "-m" || a == "--max-count" || a == "-g" || a == "--glob" || a == "-t" || a == "--type":
					i++
				case a == "--recursive" || a == "-r" || a == "-R" || (c.Name() != "rg" && strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, "rR")):
					recursive = true
				case strings.HasPrefix(a, "-") && a != "-":
				default:
					ops = append(ops, a)
				}
			}
			if !patternGiven && len(ops) > 0 {
				ops = ops[1:]
			}
			if len(ops) == 1 && !recursive {
				files = append(files, ops[0])
			}
		}
		for _, f := range files {
			if p := resolve(f); p != "" {
				env.Files.MarkRead(p)
			}
		}
	}
}

func saveOutput(env *Env, id, out string) (string, error) {
	if env.ScratchDir == "" {
		return "", errors.New("no scratch dir")
	}
	dir := filepath.Join(env.ScratchDir, "tool-results")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if id == "" {
		id = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	p := filepath.Join(dir, safeName(id)+".txt")
	return p, os.WriteFile(p, []byte(out), 0o600)
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == '.' {
			return '_'
		}
		return r
	}, s)
}

// headTail keeps the first and last half of s when it exceeds n chars.
func headTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	half := n / 2
	head := truncateUTF8(s, half)
	tailStart := len(s) - half
	for tailStart < len(s) && (s[tailStart]&0xC0) == 0x80 {
		tailStart++
	}
	return fmt.Sprintf("%s\n\n… [%d characters truncated] …\n\n%s", head, len(s)-len(head)-(len(s)-tailStart), s[tailStart:])
}

// exitOneIsOK reports whether exit code 1 means "no match / differences"
// rather than failure for the last command of the pipeline.
func exitOneIsOK(command string) bool {
	seg := command
	for _, sep := range []string{"&&", "||", ";", "|", "\n"} {
		if i := strings.LastIndex(seg, sep); i >= 0 {
			seg = seg[i+len(sep):]
		}
	}
	fields := strings.Fields(seg)
	for len(fields) > 0 && strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "=") {
		fields = fields[1:] // env assignments
	}
	if len(fields) == 0 {
		return false
	}
	name := filepath.Base(fields[0])
	switch name {
	case "grep", "rg", "egrep", "fgrep", "find", "diff", "test", "[":
		return true
	case "git":
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "-") {
				continue
			}
			return f == "diff" || f == "grep"
		}
	}
	return false
}

// memoryLimitPrefix is the line that caps the address space of a command and all it starts. Linux enforces it; macOS takes
// the limit and ignores it, so nothing is claimed there.
func memoryLimitPrefix(goos string, mb int) string {
	if goos != "linux" || mb <= 0 {
		return ""
	}
	return fmt.Sprintf("ulimit -v %d 2>/dev/null\n", mb*1024)
}
