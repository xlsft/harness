package terminal

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// What the agent does with a terminal: read its screen, see which program holds it and how long it has been quiet,
// start a command in a new tab and notice when the shell gets its prompt back, stop what runs.
//
// The screen is the source of truth. What the PTY tells is who owns the terminal: the foreground process group is the
// shell's own while it waits at its prompt and a program's while that runs. No shell integration is needed, so it works
// in the user's shell as it is; the price is that the exit code stays unknown (the shell keeps it to itself), and that a
// quiet program may be working or waiting for input, and a shell builtin such as read looks like the prompt. Only the
// screen tells those apart. Windows cannot tell who owns the terminal.

var (
	// ErrNotBusy: nothing runs in the terminal to stop.
	ErrNotBusy = errors.New("nothing is running in this terminal")
)

const (
	runPoll = 150 * time.Millisecond
	// runPollSlow is the pace after runPollSlowAfter: a server may run for days.
	runPollSlow      = time.Second
	runPollSlowAfter = 30 * time.Second
	// runQuiet is how long the shell must have the terminal, without output, for a command to count as finished
	// (a list like "a; b" hands it back for a moment between its commands).
	runQuiet = 300 * time.Millisecond
	// runUnseenAfter is when a command that was never seen in the foreground counts as finished: it ran between
	// two polls, or it was a shell builtin.
	runUnseenAfter = 700 * time.Millisecond
	// promptQuiet and promptWait: a new shell is at its prompt once it printed something and went quiet.
	promptQuiet = 200 * time.Millisecond
	promptWait  = 8 * time.Second
	// outputQuiet ends WaitOutput once output has started and paused.
	outputQuiet = 200 * time.Millisecond
	// stopGrace is how long each signal gets before the next, stronger one.
	stopGrace = 2 * time.Second
)

// State says what a terminal is doing, as far as its PTY can tell.
type State struct {
	// Known is false where the platform cannot tell (Busy and Command mean nothing then).
	Known bool
	// Busy: a program holds the terminal; the shell is not at its prompt.
	Busy bool
	// Command is the program's command line, Program its name.
	Command, Program string
	// Quiet is how long the terminal has had no new output.
	Quiet time.Duration
	// Cwd is the shell's current directory, where it can be told; the directory it started in otherwise.
	Cwd string
}

func (t *Terminal) foreground() (pgid int, busy, known bool) {
	in, ok := t.p.(inspector)
	if !ok {
		return 0, false, false
	}
	pgid, err := in.foregroundGroup()
	if err != nil || pgid <= 0 {
		return 0, false, false
	}
	return pgid, pgid != t.pid, true
}

// State looks at what runs in the terminal now.
func (t *Terminal) State() State {
	st := State{Cwd: t.cwd, Quiet: t.quiet()}
	if !t.Info().Running {
		return st
	}
	in, ok := t.p.(inspector)
	if !ok {
		return st
	}
	if d := in.currentDir(); d != "" {
		st.Cwd = d
	}
	pgid, busy, known := t.foreground()
	st.Known, st.Busy = known, busy
	if busy {
		st.Command = in.groupCommand(pgid)
		st.Program = programName(st.Command)
	}
	return st
}

// programName is the name of the program in a command line.
func programName(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(strings.TrimPrefix(fields[0], "-"))
}

func (t *Terminal) quiet() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	last := t.lastOutput
	if last.IsZero() {
		last = t.created
	}
	return time.Since(last)
}

// OpenedByAgent reports whether the agent opened this terminal and the user has not typed in it since, so what runs
// in it is the agent's own. Any input from a viewer counts (it may be a terminal's automatic reply): the mistake
// then only costs a confirmation.
func (t *Terminal) OpenedByAgent() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.agent && t.lastInput.IsZero()
}

// activity is when the user last typed here, else when it last printed: which tab is "the one in use".
func (t *Terminal) activity() (input, output time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastInput, t.lastOutput
}

// Screen returns the terminal's text as a person sees it, with what the buffer still has of the scrollback. While a
// full-screen program has the alternate screen, that is what shows (alt), without a scrollback.
func (t *Terminal) Screen() (lines []string, alt bool) {
	t.mu.Lock()
	data, _, _ := t.ring.readFrom(0, len(t.ring.buf))
	cols, rows := int(t.cols), int(t.rows)
	t.mu.Unlock()
	s := newScreen(cols, rows)
	s.write(data)
	return s.text(), s.g == s.alt
}

func (t *Terminal) screenFrom(off int64) []string {
	t.mu.Lock()
	data, _, _ := t.ring.readFrom(off, len(t.ring.buf))
	cols, rows := int(t.cols), int(t.rows)
	t.mu.Unlock()
	return renderScreen(data, cols, rows)
}

// WaitOutput waits up to d for output that arrives after the call, and until it pauses; it reports whether any came.
func (t *Terminal) WaitOutput(ctx context.Context, d time.Duration) bool {
	t.mu.Lock()
	v := &Viewer{t: t, ch: make(chan struct{}, 1), off: t.ring.end, cols: t.cols, rows: t.rows}
	t.viewers[v] = struct{}{}
	t.mu.Unlock()
	defer v.Close()
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	got := false
	for {
		wait := ctx
		var c context.CancelFunc = func() {}
		if got {
			wait, c = context.WithTimeout(ctx, outputQuiet)
		}
		ev, err := v.Next(wait)
		c()
		if err != nil || ev.Exited {
			return got
		}
		got = got || len(ev.Data) > 0
	}
}

// waitPrompt gives a new shell the time to reach its prompt, so a command is not typed over its startup output.
func (t *Terminal) waitPrompt(ctx context.Context) {
	deadline := time.Now().Add(promptWait)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		_, last := t.activity()
		if !t.Info().Running || (!last.IsZero() && time.Since(last) >= promptQuiet) {
			return
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
		}
	}
}

// RunFinished says the shell got its prompt back after a command the agent typed, or exited.
type RunFinished struct {
	SessionID, TerminalID, Title, Command string
	// Status says how far that is known: the shell has its prompt back, or the shell itself exited.
	Status string
	// Tail is the end of what the terminal shows now.
	Tail []string
}

// Run is a command the agent typed into a terminal.
type Run struct {
	t       *Terminal
	Command string
	offset  int64
	done    chan struct{}
	// watched is false where the prompt coming back cannot be seen.
	watched bool

	mu       sync.Mutex
	finished bool
	status   string
	// waiting: a tool call is waiting for the end and reports it itself. stopped: stopped on request; nobody needs telling.
	waiting, stopped bool
}

// Watched reports whether the shell's prompt coming back will be noticed.
func (r *Run) Watched() bool { return r.watched }

// Wait blocks up to d for the shell to get its prompt back. When it reports true the caller tells the model; otherwise
// that is announced through Manager.OnRunFinished later.
func (r *Run) Wait(ctx context.Context, d time.Duration) (status string, finished bool) {
	if !r.watched {
		return "", false
	}
	r.mu.Lock()
	r.waiting = true
	r.mu.Unlock()
	select {
	case <-r.done:
	case <-time.After(d):
	case <-ctx.Done():
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.waiting = false
	return r.status, r.finished
}

// Output is what the terminal showed from the start of the command on.
func (r *Run) Output() []string { return r.t.screenFrom(r.offset) }

// Type types a command line for the shell of a tab just opened for it, and watches for the shell's prompt to come
// back. The command must not hold control characters (ValidateCommand).
func (t *Terminal) Type(command string) (*Run, error) {
	if err := ValidateCommand(command); err != nil {
		return nil, err
	}
	if !t.Info().Running {
		return nil, ErrExited
	}
	_, _, known := t.foreground()
	r := &Run{t: t, Command: command, done: make(chan struct{}), watched: known}
	t.mu.Lock()
	r.offset = t.ring.end
	t.lastRun = r
	t.mu.Unlock()
	// Enter submits each line.
	if _, err := t.p.Write([]byte(strings.ReplaceAll(command, "\n", "\r") + "\r")); err != nil {
		return nil, err
	}
	if known {
		go t.watch(r)
	}
	return r, nil
}

// ValidateCommand refuses what a line editor would not take as plain text: control characters would edit the line
// (or complete it) after the permission check saw it. Line breaks are fine.
func ValidateCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return errors.New("command is empty")
	}
	if len(command) > maxCommandBytes {
		return fmt.Errorf("command is too long to type (%d bytes, at most %d)", len(command), maxCommandBytes)
	}
	for _, c := range command {
		if (c < 0x20 && c != '\n') || (c >= 0x7f && c < 0xa0) {
			return fmt.Errorf("command holds the control character %q, which the shell's line editor would act on; use the Bash tool for it, or write it out in shell syntax such as $'\\t'", c)
		}
	}
	return nil
}

// maxCommandBytes is the longest command typed: terminals in line mode take about 1 KB per line, editors far more.
const maxCommandBytes = 8 << 10

func (r *Run) markStopped() {
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
}

func (t *Terminal) watch(r *Run) {
	start := time.Now()
	seen := false
	var idleSince time.Time
	status := ""
loop:
	for {
		poll := runPoll
		if time.Since(start) > runPollSlowAfter {
			poll = runPollSlow
		}
		select {
		case <-t.done:
			status = fmt.Sprintf("the shell itself exited with code %d", t.Info().ExitCode)
			break loop
		case <-time.After(poll):
		}
		_, busy, known := t.foreground()
		if busy || !known {
			seen = seen || busy
			idleSince = time.Time{}
			continue
		}
		if idleSince.IsZero() {
			idleSince = time.Now()
		}
		_, last := t.activity()
		if time.Since(idleSince) >= runQuiet && time.Since(last) >= runQuiet && (seen || time.Since(start) >= runUnseenAfter) {
			status = "the shell got its prompt back, which does not say the command succeeded: the exit code is not known, and a shell builtin such as read waiting for input looks the same"
			break loop
		}
	}
	r.mu.Lock()
	r.finished, r.status = true, status
	tell := !r.waiting && !r.stopped
	r.mu.Unlock()
	close(r.done)
	if f := t.mgr.OnRunFinished; tell && f != nil {
		f(RunFinished{
			SessionID: t.sessionID, TerminalID: t.id, Title: t.Info().Title, Command: r.Command, Status: status,
			Tail: r.Output(),
		})
	}
}

// Stop ends what runs in the terminal and leaves the shell: an interrupt, then a termination request, then a kill,
// each only if the program still holds the terminal after stopGrace. It returns the program's command line.
func (t *Terminal) Stop(ctx context.Context) (string, error) {
	if !t.Info().Running {
		return "", ErrExited
	}
	t.mu.Lock()
	if t.lastRun != nil {
		t.lastRun.markStopped()
	}
	t.mu.Unlock()
	in, ok := t.p.(inspector)
	if !ok {
		// Nothing to look at: the same ^C the user would press.
		_, err := t.p.Write([]byte{0x03})
		return "", err
	}
	pgid, busy, known := t.foreground()
	if !known {
		return "", errors.New("cannot tell what runs in this terminal")
	}
	if !busy {
		return "", ErrNotBusy
	}
	command := in.groupCommand(pgid)
	for stage := range 3 {
		if err := in.signalGroup(pgid, stage); err != nil {
			break // the group is gone
		}
		deadline := time.Now().Add(stopGrace)
		for time.Now().Before(deadline) {
			now, busy, known := t.foreground()
			if !known || !busy || now != pgid {
				return command, nil
			}
			select {
			case <-time.After(50 * time.Millisecond):
			case <-ctx.Done():
				return command, ctx.Err()
			}
		}
	}
	if now, busy, known := t.foreground(); known && busy && now == pgid {
		return command, errors.New("it still holds the terminal after being killed")
	}
	return command, nil
}
