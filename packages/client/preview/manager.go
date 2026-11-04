// Package preview runs the project's dev servers for the agent (the Preview* tools) and the Browser pane: one
// process group per server, known by the client until the server ends or the session is deleted, and stopped when
// the client exits. It only ever signals processes it started. See proto/harness/agent/v1/preview.proto.
package preview

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// Errors.
var (
	ErrClosed   = errors.New("the client is shutting down")
	ErrNotFound = errors.New("no preview server with that id in this session")
)

// Defaults.
const (
	// DefaultReadyTimeout is how long Start waits for the port.
	DefaultReadyTimeout = 90 * time.Second
	// DefaultStopGrace is how long a server has between SIGTERM and SIGKILL.
	DefaultStopGrace = 5 * time.Second

	portPollInterval = 150 * time.Millisecond
	portDialTimeout  = 500 * time.Millisecond
)

// PortInUseError says the port answers and it is not this session's server of that name. Nothing is killed.
type PortInUseError struct {
	Port int
	// Owner names the preview server holding it, when it is one of this client's.
	Owner string
}

func (e *PortInUseError) Error() string {
	who := "another process, which harness did not start and will not stop"
	if e.Owner != "" {
		who = e.Owner
	}
	return fmt.Sprintf("port %d is already taken by %s. Free the port, or use a different port in the launch file", e.Port, who)
}

// ChangedError says the server of that name runs with another configuration than the launch file now has.
type ChangedError struct {
	Server Info
}

func (e *ChangedError) Error() string {
	return fmt.Sprintf("server %q (%s) is running with a different configuration (%s on port %d). Stop it with PreviewStop, then start it again", e.Server.Name, e.Server.ID, e.Server.CommandLine, e.Server.Port)
}

// Info is a snapshot of a server.
type Info struct {
	ID          string
	Name        string
	CommandLine string
	Port        int
	URL         string
	Status      agentv1.PreviewServer_Status
	ExitCode    int
	StartedAt   time.Time
}

// Proto renders the snapshot for the RPCs.
func (i Info) Proto() *agentv1.PreviewServer {
	return &agentv1.PreviewServer{ServerId: i.ID, Name: i.Name, CommandLine: i.CommandLine, Port: int32(i.Port), Url: i.URL,
		Status: i.Status, ExitCode: int32(i.ExitCode), StartedAtMs: i.StartedAt.UnixMilli()}
}

func (i Info) alive() bool {
	return i.Status == agentv1.PreviewServer_STATUS_STARTING || i.Status == agentv1.PreviewServer_STATUS_RUNNING
}

// server is one started process.
type server struct {
	m   *Manager
	cmd *exec.Cmd
	// logs is written by the pipe reader until the process group is gone.
	logs *logRing
	// done closes when the leader ended and the rest of its group is gone.
	done chan struct{}
	// guard is the client's end of the exit guard's pipe; it must stay open (and referenced) while the server runs.
	guard *os.File

	mu       sync.Mutex
	info     Info
	stopping bool
	// reaped: the leader is gone, so its pid may not name the group any more; only the waiter signals from then on.
	reaped bool
}

func (s *server) snapshot() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// Manager owns the preview servers of a client's sessions. It outlives the sessions' open and close: a resumed
// session finds its servers again.
type Manager struct {
	// ReadyTimeout overrides DefaultReadyTimeout.
	ReadyTimeout time.Duration
	// StopGrace overrides DefaultStopGrace.
	StopGrace time.Duration
	// Pages opens a page in the session's browser (nil: there is no browser).
	Pages  Pages
	Logger *slog.Logger

	mu       sync.Mutex
	sessions map[string][]*server
	seq      int
	closed   bool
}

// Pages is the session's browser, as far as previews need it (browser.Manager implements it).
type Pages interface {
	OpenPage(ctx context.Context, sessionID, url string) error
}

func (m *Manager) log() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

func (m *Manager) readyTimeout() time.Duration {
	if m.ReadyTimeout > 0 {
		return m.ReadyTimeout
	}
	return DefaultReadyTimeout
}

func (m *Manager) stopGrace() time.Duration {
	if m.StopGrace > 0 {
		return m.StopGrace
	}
	return DefaultStopGrace
}

// portAnswers reports whether something accepts connections on the port of this machine.
func portAnswers(ctx context.Context, port int) bool {
	d := net.Dialer{Timeout: portDialTimeout}
	for _, host := range []string{"127.0.0.1", "::1"} {
		c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err == nil {
			_ = c.Close()
			return true
		}
	}
	return false
}

// lookupName returns the newest server of that name in the session.
func (m *Manager) lookupName(sessionID, name string) *server {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.sessions[sessionID]
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].info.Name == name {
			return list[i]
		}
	}
	return nil
}

// Running returns the live server of that name in the session.
func (m *Manager) Running(sessionID, name string) (Info, bool) {
	s := m.lookupName(sessionID, name)
	if s == nil {
		return Info{}, false
	}
	i := s.snapshot()
	return i, i.alive()
}

// portOwner names the live server of this client on a port.
func (m *Manager) portOwner(sessionID string, port int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for sid, list := range m.sessions {
		for _, s := range list {
			i := s.snapshot()
			if !i.alive() || i.Port != port {
				continue
			}
			if sid == sessionID {
				return fmt.Sprintf("preview server %q (%s) of this session", i.Name, i.ID)
			}
			return fmt.Sprintf("preview server %q of another session", i.Name)
		}
	}
	return ""
}

// StartResult is the outcome of Start.
type StartResult struct {
	Info Info
	// Reused: the server already ran; nothing was started.
	Reused bool
}

// TimeoutError says the port did not answer in time. The server keeps running: a slow first build may still
// finish, and starting it again waits further.
type TimeoutError struct {
	Server Info
	Waited time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("server %q (%s) is running, but port %d did not answer within %s", e.Server.Name, e.Server.ID, e.Server.Port, e.Waited.Round(time.Second))
}

// ExitedError says the server ended before its port answered.
type ExitedError struct {
	Server Info
}

func (e *ExitedError) Error() string {
	return fmt.Sprintf("server %q (%s) exited with code %d before port %d answered", e.Server.Name, e.Server.ID, e.Server.ExitCode, e.Server.Port)
}

// Start starts the configured server, or finds it running, and waits until its port answers. newCmd builds the
// process (not started) for the command line, in its own process group; nil means the caller may only reuse a
// running server (nothing else was approved).
func (m *Manager) Start(ctx context.Context, sessionID string, c LaunchConfig, newCmd func(commandLine string) *exec.Cmd) (StartResult, error) {
	line, page := c.CommandLine(), c.PageURL()
	s := m.lookupName(sessionID, c.Name)
	if s != nil {
		i := s.snapshot()
		if i.alive() {
			if i.CommandLine != line || i.Port != c.Port {
				return StartResult{}, &ChangedError{Server: i}
			}
			s.mu.Lock()
			s.info.URL = page
			s.mu.Unlock()
			info, err := m.waitReady(ctx, s)
			return StartResult{Info: info, Reused: true}, err
		}
	}
	if newCmd == nil {
		return StartResult{}, fmt.Errorf("server %q stopped in the meantime; call PreviewStart again to start it", c.Name)
	}
	if portAnswers(ctx, c.Port) {
		return StartResult{}, &PortInUseError{Port: c.Port, Owner: m.portOwner(sessionID, c.Port)}
	}
	s, started, err := m.spawn(sessionID, c, newCmd(exitGuard+line))
	if err != nil {
		return StartResult{}, err
	}
	info, err := m.waitReady(ctx, s)
	if !started {
		return StartResult{Info: info, Reused: true}, err
	}
	if ctx.Err() != nil {
		// Interrupted while it came up: nobody is waiting for this server.
		m.stop(s)
		info = s.snapshot()
	}
	return StartResult{Info: info}, err
}

// spawn starts the process and registers it, replacing an ended server of that name. A concurrent call may have
// started that name meanwhile: then it returns that server, started false, and runs nothing.
func (m *Manager) spawn(sessionID string, c LaunchConfig, cmd *exec.Cmd) (*server, bool, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, false, err
	}
	// One pipe for both streams keeps their order, and with a plain file cmd.Wait returns when the leader ends,
	// whoever else still holds the pipe.
	cmd.Stdout, cmd.Stderr = w, w
	cmd.Stdin = nil
	guard, err := attachExitGuard(cmd)
	if err != nil {
		_ = r.Close()
		_ = w.Close()
		return nil, false, err
	}
	closeChildEnds := func() {
		_ = w.Close()
		for _, f := range cmd.ExtraFiles {
			_ = f.Close()
		}
	}
	giveUp := func() {
		closeChildEnds()
		_ = r.Close()
		if guard != nil {
			_ = guard.Close()
		}
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		giveUp()
		return nil, false, ErrClosed
	}
	for _, old := range m.sessions[sessionID] {
		if old.info.Name == c.Name && old.snapshot().alive() {
			m.mu.Unlock()
			giveUp()
			return old, false, nil
		}
	}
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		giveUp()
		return nil, false, fmt.Errorf("starting %q: %w", c.Name, err)
	}
	closeChildEnds()
	m.seq++
	s := &server{m: m, cmd: cmd, logs: &logRing{}, done: make(chan struct{}), guard: guard, info: Info{
		ID: fmt.Sprintf("%s-%d", c.Name, m.seq), Name: c.Name, CommandLine: c.CommandLine(), Port: c.Port, URL: c.PageURL(),
		Status: agentv1.PreviewServer_STATUS_STARTING, StartedAt: time.Now(),
	}}
	if m.sessions == nil {
		m.sessions = map[string][]*server{}
	}
	kept := m.sessions[sessionID][:0]
	for _, old := range m.sessions[sessionID] {
		if old.info.Name != c.Name {
			kept = append(kept, old)
		}
	}
	m.sessions[sessionID] = append(kept, s)
	m.mu.Unlock()

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buf := make([]byte, 32<<10)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				_, _ = s.logs.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go s.wait(r, readerDone)
	return s, true, nil
}

// wait follows the leader. When it ends, the rest of its group goes too right away: the pid names the group only
// while a member lives, so waiting longer could later hit a stranger that got the number.
func (s *server) wait(r *os.File, readerDone <-chan struct{}) {
	err := s.cmd.Wait()
	s.mu.Lock()
	s.reaped = true
	s.mu.Unlock()
	s.cleanGroup()
	// A process that left the group may still hold the pipe; the buffer does not wait for it.
	select {
	case <-readerDone:
	case <-time.After(200 * time.Millisecond):
	}
	_ = r.Close()
	if s.guard != nil {
		_ = s.guard.Close()
	}

	s.mu.Lock()
	if s.stopping {
		s.info.Status = agentv1.PreviewServer_STATUS_STOPPED
	} else {
		s.info.Status = agentv1.PreviewServer_STATUS_EXITED
		s.info.ExitCode = exitCode(err)
	}
	s.mu.Unlock()
	close(s.done)
}

// cleanGroup ends what is left of the group after the leader ended.
func (s *server) cleanGroup() {
	if !groupAlive(s.cmd) {
		return
	}
	termGroup(s.cmd)
	deadline := time.Now().Add(s.m.stopGrace())
	for time.Now().Before(deadline) {
		if !groupAlive(s.cmd) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	killGroup(s.cmd)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// waitReady waits until the port answers, the server ends, ctx ends or the time is up. Only a live server's port
// counts: the port was free when it started.
func (m *Manager) waitReady(ctx context.Context, s *server) (Info, error) {
	timeout := m.readyTimeout()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(portPollInterval)
	defer tick.Stop()
	for {
		i := s.snapshot()
		select {
		case <-s.done:
			i = s.snapshot()
			if i.Status == agentv1.PreviewServer_STATUS_STOPPED {
				return i, fmt.Errorf("server %q (%s) was stopped", i.Name, i.ID)
			}
			return i, &ExitedError{Server: i}
		default:
		}
		if portAnswers(ctx, i.Port) {
			s.mu.Lock()
			if s.info.Status == agentv1.PreviewServer_STATUS_STARTING {
				s.info.Status = agentv1.PreviewServer_STATUS_RUNNING
			}
			i = s.info
			s.mu.Unlock()
			return i, nil
		}
		select {
		case <-ctx.Done():
			return i, ctx.Err()
		case <-deadline.C:
			return i, &TimeoutError{Server: i, Waited: timeout}
		case <-s.done:
		case <-tick.C:
		}
	}
}

// stop ends the server's process group and waits until it is gone.
func (m *Manager) stop(s *server) {
	s.mu.Lock()
	s.stopping = s.stopping || (s.info.Status != agentv1.PreviewServer_STATUS_EXITED && s.info.Status != agentv1.PreviewServer_STATUS_STOPPED)
	s.mu.Unlock()
	s.signal(termGroup)
	grace := time.NewTimer(m.stopGrace())
	defer grace.Stop()
	select {
	case <-s.done:
		return
	case <-grace.C:
	}
	s.signal(killGroup)
	<-s.done
}

// signal signals the group while the leader's pid surely names it.
func (s *server) signal(send func(*exec.Cmd)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.reaped {
		send(s.cmd)
	}
}

func (m *Manager) find(sessionID, serverID string) (*server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions[sessionID] {
		if s.info.ID == serverID {
			return s, nil
		}
	}
	return nil, ErrNotFound
}

// Stop stops a server of the session; stopping one that ended is not an error.
func (m *Manager) Stop(sessionID, serverID string) (Info, error) {
	s, err := m.find(sessionID, serverID)
	if err != nil {
		return Info{}, err
	}
	m.stop(s)
	return s.snapshot(), nil
}

// List returns the session's servers, oldest first.
func (m *Manager) List(sessionID string) []Info {
	m.mu.Lock()
	list := append([]*server(nil), m.sessions[sessionID]...)
	m.mu.Unlock()
	out := make([]Info, 0, len(list))
	for _, s := range list {
		out = append(out, s.snapshot())
	}
	return out
}

// Logs returns a server's recent output and whether older output is gone.
func (m *Manager) Logs(sessionID, serverID string, f LogFilter) ([]string, bool, error) {
	s, err := m.find(sessionID, serverID)
	if err != nil {
		return nil, false, err
	}
	lines, dropped := s.logs.tail(f)
	return lines, dropped, nil
}

// tail returns the last lines of a server by id, for error reports.
func (m *Manager) tail(sessionID, serverID string, n int) []string {
	lines, _, err := m.Logs(sessionID, serverID, LogFilter{Lines: n})
	if err != nil {
		return nil
	}
	return lines
}

// CloseSession stops the servers of a deleted session and forgets them.
func (m *Manager) CloseSession(sessionID string) {
	m.mu.Lock()
	list := m.sessions[sessionID]
	delete(m.sessions, sessionID)
	m.mu.Unlock()
	m.stopAll(list)
}

// Close stops every server; the client is exiting.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	var list []*server
	for _, l := range m.sessions {
		list = append(list, l...)
	}
	m.sessions = nil
	m.mu.Unlock()
	m.stopAll(list)
}

func (m *Manager) stopAll(list []*server) {
	var wg sync.WaitGroup
	for _, s := range list {
		wg.Add(1)
		go func(s *server) {
			defer wg.Done()
			m.stop(s)
		}(s)
	}
	wg.Wait()
}
