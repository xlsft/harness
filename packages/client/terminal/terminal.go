// Package terminal runs interactive shells in pseudo-terminals for the UI's terminal pane.
//
// A Manager owns the terminals of a client. Each terminal keeps its recent output in a ring buffer, so viewers
// (WebSocket connections, socket.go) can come and go while the shell keeps running: a new viewer gets the
// buffered output replayed, then live output. Viewers read the ring at their own pace; one that falls more than
// the buffer behind skips ahead instead of queueing, so a slow UI never grows memory or stalls the shell.
//
// The agent shares these terminals through the Terminal* tools (tools.go, run.go).
package terminal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMax caps the terminals of a client.
	DefaultMax = 16
	// DefaultBufferSize is the output kept for replay, per terminal.
	DefaultBufferSize = 256 << 10
	// DefaultTicketTTL is how long a socket ticket stays valid.
	DefaultTicketTTL = 30 * time.Second
	// DefaultCols and DefaultRows size a terminal created without a size.
	DefaultCols, DefaultRows = 80, 24

	readChunk = 32 << 10
	// drainGrace is how long output may still arrive after the shell exited (from its leftover jobs).
	drainGrace = 500 * time.Millisecond
	// hangupGrace is how long a hung-up shell gets before it is killed.
	hangupGrace = 2 * time.Second
)

var (
	ErrLimit         = errors.New("too many terminals")
	ErrNotFound      = errors.New("terminal not found")
	ErrExited        = errors.New("the terminal has exited")
	ErrClosed        = errors.New("terminals are shutting down")
	ErrTicketInvalid = errors.New("invalid or already used terminal ticket")
	ErrTicketExpired = errors.New("expired terminal ticket")
)

// Options describe a new terminal.
type Options struct {
	// SessionID owns the terminal; empty for a standalone one.
	SessionID string
	// Cwd must be an existing directory (validated by the caller).
	Cwd string
	// Title defaults to the base name of Cwd.
	Title      string
	Cols, Rows uint16
	// Env adds variables to the client's environment.
	Env map[string]string
	// Shell and Args override DefaultShell (tests).
	Shell string
	Args  []string
	// Agent says the agent opened it, not the user (TerminalOpen, TerminalRun).
	Agent bool
	// Wrap, when set, gets the shell's command line and returns the one to start and variables to add (TerminalRun, in the
	// OS sandbox). A nil command line leaves it as it is; an error means no tab.
	Wrap func(argv []string) (full, env []string, err error)
}

// Info is a snapshot of a terminal.
type Info struct {
	ID, SessionID, Title, Cwd, Shell string
	Pid                              int
	Running                          bool
	// ExitCode is set once Running is false.
	ExitCode   int
	CreatedAt  time.Time
	Cols, Rows uint16
}

// Terminal is a shell running in a PTY.
type Terminal struct {
	id, sessionID, title, cwd, shell string
	created                          time.Time
	p                                proc
	pid                              int
	done                             chan struct{}
	mgr                              *Manager
	agent                            bool

	mu         sync.Mutex
	ring       *ring
	cols, rows uint16
	running    bool
	exitCode   int
	viewers    map[*Viewer]struct{}
	// lastInput is the last time a viewer (the user) sent input; lastOutput the last output.
	lastInput, lastOutput time.Time
	// lastRun is the agent's latest command (run.go).
	lastRun *Run
}

func (t *Terminal) ID() string        { return t.id }
func (t *Terminal) SessionID() string { return t.sessionID }

// Done is closed once the shell has exited and its output is buffered.
func (t *Terminal) Done() <-chan struct{} { return t.done }

func (t *Terminal) Info() Info {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Info{
		ID: t.id, SessionID: t.sessionID, Title: t.title, Cwd: t.cwd, Shell: t.shell, Pid: t.pid,
		Running: t.running, ExitCode: t.exitCode, CreatedAt: t.created, Cols: t.cols, Rows: t.rows,
	}
}

// Write sends a viewer's input to the shell.
func (t *Terminal) Write(b []byte) (int, error) {
	t.mu.Lock()
	running := t.running
	t.lastInput = time.Now()
	t.mu.Unlock()
	if !running {
		return 0, ErrExited
	}
	return t.p.Write(b)
}

// Resize changes the PTY size; viewers are told about it.
func (t *Terminal) Resize(cols, rows uint16) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.resizeLocked(cols, rows)
}

func (t *Terminal) resizeLocked(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return errors.New("cols and rows must be positive")
	}
	if !t.running {
		return ErrExited
	}
	if cols == t.cols && rows == t.rows {
		return nil
	}
	if err := t.p.Resize(cols, rows); err != nil {
		return err
	}
	t.cols, t.rows = cols, rows
	t.notifyLocked()
	return nil
}

func (t *Terminal) output(b []byte) {
	t.mu.Lock()
	t.ring.write(b)
	t.lastOutput = time.Now()
	t.notifyLocked()
	t.mu.Unlock()
}

func (t *Terminal) notifyLocked() {
	for v := range t.viewers {
		select {
		case v.ch <- struct{}{}:
		default:
		}
	}
}

// run pumps output into the ring and records the exit.
func (t *Terminal) run() {
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, readChunk)
		for {
			n, err := t.p.Read(buf)
			if n > 0 {
				t.output(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	code, err := t.p.Wait()
	if err != nil {
		code = -1
	}
	select {
	case <-readDone:
	case <-time.After(drainGrace):
	}
	_ = t.p.Close()
	t.mu.Lock()
	t.running, t.exitCode = false, code
	t.notifyLocked()
	t.mu.Unlock()
	close(t.done)
}

// terminate hangs up the shell and kills it if it is still running after hangupGrace.
func (t *Terminal) terminate() {
	select {
	case <-t.done:
		return
	default:
	}
	_ = t.p.Hangup()
	go func() {
		select {
		case <-t.done:
		case <-time.After(hangupGrace):
			_ = t.p.Kill()
		}
	}()
}

// Viewer reads a terminal's output from the oldest buffered byte on. Not safe for concurrent use.
type Viewer struct {
	t          *Terminal
	ch         chan struct{}
	off        int64
	cols, rows uint16
	exitSent   bool
}

// Event is what Viewer.Next returns: output (after Skipped missed bytes), a size change, or the exit.
type Event struct {
	Data       []byte
	Skipped    int64
	Resized    bool
	Cols, Rows uint16
	Exited     bool
	ExitCode   int
}

// Attach adds a viewer that starts with the buffered output, from a safe boundary.
func (t *Terminal) Attach() *Viewer {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := &Viewer{t: t, ch: make(chan struct{}, 1), cols: t.cols, rows: t.rows}
	if s := t.ring.start(); s > 0 {
		v.off = t.ring.safeStart(s)
	}
	t.viewers[v] = struct{}{}
	return v
}

// Next blocks until there is output, a size change or the exit, and returns io.EOF after the exit was returned.
func (v *Viewer) Next(ctx context.Context) (Event, error) {
	t := v.t
	for {
		t.mu.Lock()
		data, next, skipped := t.ring.readFrom(v.off, readChunk)
		v.off = next
		var ev Event
		switch {
		case len(data) > 0 || skipped > 0:
			ev = Event{Data: data, Skipped: skipped}
		case t.cols != v.cols || t.rows != v.rows:
			v.cols, v.rows = t.cols, t.rows
			ev = Event{Resized: true, Cols: v.cols, Rows: v.rows}
		case !t.running && v.exitSent:
			t.mu.Unlock()
			return Event{}, io.EOF
		case !t.running:
			v.exitSent = true
			ev = Event{Exited: true, ExitCode: t.exitCode}
		default:
			t.mu.Unlock()
			select {
			case <-v.ch:
				continue
			case <-ctx.Done():
				return Event{}, ctx.Err()
			}
		}
		t.mu.Unlock()
		return ev, nil
	}
}

// Resize resizes the terminal for this viewer: the others are told, this one is not.
func (v *Viewer) Resize(cols, rows uint16) error {
	t := v.t
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.resizeLocked(cols, rows); err != nil {
		return err
	}
	v.cols, v.rows = cols, rows
	return nil
}

// Close detaches the viewer.
func (v *Viewer) Close() {
	v.t.mu.Lock()
	delete(v.t.viewers, v)
	v.t.mu.Unlock()
}

// Manager owns the terminals of a client and the one-time tickets of their sockets.
type Manager struct {
	// Max, BufferSize and TicketTTL default to DefaultMax, DefaultBufferSize and DefaultTicketTTL when zero.
	Max        int
	BufferSize int
	TicketTTL  time.Duration
	// Now is the clock for tickets (tests).
	Now func() time.Time
	// OnRunFinished is told when a command the agent started ends by itself and no tool result reported it (run.go).
	OnRunFinished func(RunFinished)
	// Refuse, when set, says why a session's agent cannot use terminals on this machine (tools.go).
	Refuse func(sessionID string) error

	mu      sync.Mutex
	terms   map[string]*Terminal
	order   []*Terminal
	tickets map[[32]byte]ticket
	closed  bool
}

type ticket struct {
	terminalID string
	expires    time.Time
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Create starts a shell. At the cap, the oldest exited terminal makes room; ErrLimit when none has exited.
func (m *Manager) Create(o Options) (*Terminal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if m.terms == nil {
		m.terms = map[string]*Terminal{}
	}
	limit := m.Max
	if limit <= 0 {
		limit = DefaultMax
	}
	if len(m.order) >= limit {
		for _, t := range m.order {
			if !t.Info().Running {
				m.removeLocked(t.id)
				break
			}
		}
		if len(m.order) >= limit {
			return nil, fmt.Errorf("%w: at most %d per client; close one first", ErrLimit, limit)
		}
	}
	shell, args := o.Shell, o.Args
	if shell == "" {
		shell, args = DefaultShell()
	}
	if shell == "" {
		return nil, ErrUnsupported
	}
	cols, rows := o.Cols, o.Rows
	if cols == 0 || rows == 0 {
		cols, rows = DefaultCols, DefaultRows
	}
	spec := startSpec{Shell: shell, Args: args, Dir: o.Cwd, Env: buildEnv(o.Env), Cols: cols, Rows: rows}
	if o.Wrap != nil {
		full, env, err := o.Wrap(append([]string{shell}, args...))
		if err != nil {
			return nil, err
		}
		if len(full) > 0 {
			spec.Shell, spec.Args = full[0], full[1:]
			spec.Env = append(spec.Env, env...)
		}
	}
	p, err := startProc(spec)
	if err != nil {
		if errors.Is(err, ErrUnsupported) {
			return nil, err
		}
		return nil, fmt.Errorf("start %s: %w", shell, err)
	}
	size := m.BufferSize
	if size <= 0 {
		size = DefaultBufferSize
	}
	title := o.Title
	if title == "" {
		title = filepath.Base(o.Cwd)
	}
	t := &Terminal{
		id: "term_" + randomHex(8), sessionID: o.SessionID, title: title, cwd: o.Cwd, shell: shell,
		created: time.Now(), p: p, pid: p.Pid(), done: make(chan struct{}), mgr: m, agent: o.Agent,
		ring: newRing(size), cols: cols, rows: rows, running: true, viewers: map[*Viewer]struct{}{},
	}
	go t.run()
	m.terms[t.id] = t
	m.order = append(m.order, t)
	return t, nil
}

func (m *Manager) Get(id string) *Terminal {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.terms[id]
}

// List returns the terminals of a session (all terminals when sessionID is empty), oldest first.
func (m *Manager) List(sessionID string) []*Terminal {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Terminal
	for _, t := range m.order {
		if sessionID == "" || t.sessionID == sessionID {
			out = append(out, t)
		}
	}
	return out
}

// Kill ends a terminal's shell and forgets the terminal; false when there is none.
func (m *Manager) Kill(id string) bool {
	m.mu.Lock()
	t := m.removeLocked(id)
	m.mu.Unlock()
	if t == nil {
		return false
	}
	t.terminate()
	return true
}

// KillSession kills the terminals of a session.
func (m *Manager) KillSession(sessionID string) {
	if sessionID == "" {
		return
	}
	for _, t := range m.List(sessionID) {
		m.Kill(t.id)
	}
}

func (m *Manager) removeLocked(id string) *Terminal {
	t := m.terms[id]
	if t == nil {
		return nil
	}
	delete(m.terms, id)
	for i, o := range m.order {
		if o == t {
			m.order = append(m.order[:i:i], m.order[i+1:]...)
			break
		}
	}
	return t
}

// Close ends all terminals and refuses new ones; it waits a little for the shells to exit.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	all := m.order
	m.terms, m.order, m.tickets = nil, nil, nil
	m.mu.Unlock()
	for _, t := range all {
		t.terminate()
	}
	deadline := time.After(hangupGrace + time.Second)
	for _, t := range all {
		select {
		case <-t.done:
		case <-deadline:
			return
		}
	}
}

// IssueTicket returns a one-time ticket for attaching to a terminal's socket.
func (m *Manager) IssueTicket(terminalID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.terms[terminalID] == nil {
		return "", ErrNotFound
	}
	now := m.now()
	for k, tk := range m.tickets {
		if now.After(tk.expires) {
			delete(m.tickets, k)
		}
	}
	if m.tickets == nil {
		m.tickets = map[[32]byte]ticket{}
	}
	ttl := m.TicketTTL
	if ttl <= 0 {
		ttl = DefaultTicketTTL
	}
	tok := randomHex(32)
	m.tickets[sha256.Sum256([]byte(tok))] = ticket{terminalID: terminalID, expires: now.Add(ttl)}
	return tok, nil
}

// Redeem consumes a ticket and returns its terminal. Tickets are keyed by their hash, so lookups do not leak
// ticket bytes through timing.
func (m *Manager) Redeem(tok string) (*Terminal, error) {
	key := sha256.Sum256([]byte(tok))
	m.mu.Lock()
	defer m.mu.Unlock()
	tk, ok := m.tickets[key]
	if !ok || tok == "" {
		return nil, ErrTicketInvalid
	}
	delete(m.tickets, key)
	if m.now().After(tk.expires) {
		return nil, ErrTicketExpired
	}
	t := m.terms[tk.terminalID]
	if t == nil {
		return nil, ErrNotFound
	}
	return t, nil
}

// Variables never passed to a shell: the agent token, and what describes the terminal the client itself
// may run in.
var droppedEnv = map[string]bool{
	"HARNESS_AGENT_TOKEN": true,
	"TERM_PROGRAM":        true, "TERM_PROGRAM_VERSION": true, "TERM_SESSION_ID": true,
	"COLUMNS": true, "LINES": true,
}

// buildEnv is the client's environment without droppedEnv, plus extra, with TERM and COLORTERM set.
func buildEnv(extra map[string]string) []string {
	norm := func(k string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(k)
		}
		return k
	}
	var keys []string
	vals := map[string]string{}
	names := map[string]string{}
	set := func(k, v string) {
		n := norm(k)
		if _, ok := vals[n]; !ok {
			keys = append(keys, n)
			names[n] = k
		}
		vals[n] = v
	}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || droppedEnv[norm(k)] {
			continue
		}
		set(k, v)
	}
	for k, v := range extra {
		set(k, v)
	}
	set("TERM", "xterm-256color")
	set("COLORTERM", "truecolor")
	out := make([]string, 0, len(keys))
	for _, n := range keys {
		out = append(out, names[n]+"="+vals[n])
	}
	return out
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
