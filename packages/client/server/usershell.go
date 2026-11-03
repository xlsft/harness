package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/agent"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/tools"
)

// User shell commands (shell.proto): the composer's shell mode runs a command in the session's shell without the
// model. The command runs at once, concurrently with a running turn and with other user shell commands, through
// tools.Shell (the Bash tool's machinery: shell cwd, session environment, Bash's time and capture limits). Its
// events stream to subscribers; when it ends a user_shell_command system entry is written to the transcript (for
// replays) and the result is queued for the model as the user_shell_command reminder (next model request).

// Streaming of a running command's output.
const (
	userShellFlushEvery  = 100 * time.Millisecond
	userShellStreamBytes = 256 << 10
)

type userShell struct {
	id, command string
	cancel      context.CancelFunc
	stopped     atomic.Bool
}

// userShells tracks a session's user shell commands: the running ones (for
// StopShellCommand) and the ids seen (deduplication).
type userShells struct {
	mu      sync.Mutex
	running map[string]*userShell
	seen    map[string]bool
}

func (u *userShells) get(id string) *userShell {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.running[id]
}

func (s *Server) RunShellCommand(ctx context.Context, req *connect.Request[agentv1.RunShellCommandRequest]) (*connect.Response[agentv1.RunShellCommandResponse], error) {
	id := req.Msg.GetSessionId()
	ls, err := s.get(id)
	if err != nil {
		if info := s.remoteInfo(id); info != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("session %s runs on %s: shell commands run only on the session's own machine", id, deviceLabel(info)))
		}
		return nil, err
	}
	command := strings.TrimSpace(req.Msg.GetCommand())
	if command == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("command is empty"))
	}
	cid, err := ls.runUserShell(req.Msg.GetUuid(), command)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.RunShellCommandResponse{CommandId: cid}), nil
}

func (s *Server) StopShellCommand(ctx context.Context, req *connect.Request[agentv1.StopShellCommandRequest]) (*connect.Response[agentv1.StopShellCommandResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	sh := ls.shells.get(req.Msg.GetCommandId())
	if sh == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("shell command %s is not running", req.Msg.GetCommandId()))
	}
	sh.stopped.Store(true)
	sh.cancel()
	return connect.NewResponse(&agentv1.StopShellCommandResponse{}), nil
}

// legacyShellCommand recognizes the shell mode of older UIs: a message whose only content is text starting
// with "!" ("!git status").
func legacyShellCommand(m *agentv1.SendUserMessageRequest) (string, bool) {
	blocks := pbconv.FromBlocks(m.GetContent())
	if len(blocks) != 1 || blocks[0].Type != llm.BlockText {
		return "", false
	}
	text := strings.TrimSpace(blocks[0].Text)
	if !strings.HasPrefix(text, "!") {
		return "", false
	}
	cmd := strings.TrimSpace(text[1:])
	return cmd, cmd != ""
}

// legacyShell runs a "!command" message of an older UI as a user shell command when the session is open here
// (ok = true). Messages for sessions of other machines are forwarded unchanged: their machine treats them as
// text.
func (r routed) legacyShell(ctx context.Context, req *connect.Request[agentv1.SendUserMessageRequest]) (*connect.Response[agentv1.SendUserMessageResponse], bool, error) {
	cmd, ok := legacyShellCommand(req.Msg)
	if !ok {
		return nil, false, nil
	}
	if _, err := r.get(req.Msg.GetSessionId()); err != nil {
		return nil, false, nil
	}
	_, err := r.RunShellCommand(ctx, connect.NewRequest(&agentv1.RunShellCommandRequest{
		SessionId: req.Msg.GetSessionId(), Command: cmd, Uuid: req.Msg.GetUuid()}))
	if err != nil {
		return nil, true, err
	}
	return connect.NewResponse(&agentv1.SendUserMessageResponse{}), true, nil
}

// runUserShell starts a user shell command and returns its id; a repeated id returns at once.
func (ls *liveSession) runUserShell(id, command string) (string, error) {
	if id == "" {
		id = newUUID("sh_")
	}
	ls.mu.Lock()
	if ls.closed {
		ls.mu.Unlock()
		return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s is closed", ls.sess.ID))
	}
	ls.shells.mu.Lock()
	if ls.shells.seen[id] {
		ls.shells.mu.Unlock()
		ls.mu.Unlock()
		return id, nil
	}
	if ls.shells.seen == nil {
		ls.shells.seen, ls.shells.running = map[string]bool{}, map[string]*userShell{}
	}
	ctx, cancel := context.WithCancel(ls.ctx)
	sh := &userShell{id: id, command: command, cancel: cancel}
	ls.shells.seen[id] = true
	ls.shells.running[id] = sh
	ls.shells.mu.Unlock()
	ls.bg.Add(1)
	ls.mu.Unlock()

	cwd := ls.sess.ToolEnv.Shell.Cwd()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_UserShellStarted{UserShellStarted: &agentv1.UserShellStarted{
		CommandId: id, Command: command, Cwd: cwd}}})
	go func() {
		defer ls.bg.Done()
		defer cancel()
		ls.execUserShell(ctx, sh)
	}()
	return id, nil
}

func (ls *liveSession) execUserShell(ctx context.Context, sh *userShell) {
	st := newShellStream(func(text string) {
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_UserShellOutput{UserShellOutput: &agentv1.UserShellOutput{
			CommandId: sh.id, Text: text}}})
	})
	flushDone := make(chan struct{})
	stopFlush := make(chan struct{})
	go func() {
		defer close(flushDone)
		t := time.NewTicker(userShellFlushEvery)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				st.flush(false)
			case <-stopFlush:
				st.flush(true)
				return
			}
		}
	}()
	res := ls.sess.ToolEnv.Shell.RunUser(ctx, sh.command, st.write)
	close(stopFlush)
	<-flushDone

	ls.shells.mu.Lock()
	delete(ls.shells.running, sh.id)
	ls.shells.mu.Unlock()

	reason, reasonName := agentv1.UserShellFinished_REASON_EXITED, "exited"
	errText := ""
	switch {
	case res.Err != nil:
		reason, reasonName, errText = agentv1.UserShellFinished_REASON_FAILED, "failed", res.Err.Error()
	case sh.stopped.Load():
		reason, reasonName = agentv1.UserShellFinished_REASON_STOPPED, "stopped"
	case ls.ctx.Err() != nil:
		reason, reasonName = agentv1.UserShellFinished_REASON_SESSION_CLOSED, "session_closed"
	case res.TimedOut:
		reason, reasonName = agentv1.UserShellFinished_REASON_TIME_LIMIT, "time_limit"
	}
	stdout, stderr := boundedText(res.Stdout, tools.UserShellDisplayChars), boundedText(res.Stderr, tools.UserShellDisplayChars)
	truncated := res.Truncated || len(res.Stdout) > tools.UserShellDisplayChars || len(res.Stderr) > tools.UserShellDisplayChars
	cwd := res.Cwd
	if cwd == "" {
		cwd = ls.sess.ToolEnv.Shell.Cwd()
	}
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_UserShellFinished{UserShellFinished: &agentv1.UserShellFinished{
		CommandId: sh.id, Reason: reason, ExitCode: int32(res.ExitCode), DurationMs: res.Duration.Milliseconds(),
		Stdout: stdout, Stderr: stderr, Truncated: truncated, Error: errText, Cwd: cwd, Command: sh.command,
	}}})
	if w := ls.sess.Transcript; w != nil {
		if _, err := w.Append(session.Entry{Type: session.TypeSystem, Subtype: session.SubtypeUserShell, Level: "info", Content: sh.command,
			UserShell: &session.UserShell{ID: sh.id, Command: sh.command, Cwd: cwd, ExitCode: res.ExitCode, Reason: reasonName,
				DurationMs: res.Duration.Milliseconds(), Stdout: stdout, Stderr: stderr, Truncated: truncated, Error: errText}}); err != nil {
			ls.srv.log().Warn("transcript write failed", "err", err)
		}
	}
	if reason == agentv1.UserShellFinished_REASON_SESSION_CLOSED {
		return
	}
	ls.sess.Agent.NoteUserShell(agent.UserShellNote{Command: sh.command, Cwd: cwd, ExitCode: res.ExitCode, Reason: reasonName,
		Output: boundedText(res.CombinedOutput(), agent.UserShellOutputChars), Error: errText})
}

// boundedText keeps head and tail of s within n bytes, as valid UTF-8.
func boundedText(s string, n int) string {
	return strings.ToValidUTF8(tools.HeadTail(s, n), "�")
}

// shellStream collects a running command's output and sends it in throttled chunks, never splitting a UTF-8
// sequence, up to userShellStreamBytes in all.
type shellStream struct {
	mu   sync.Mutex
	buf  []byte
	sent int
	cut  bool
	send func(string)
}

func newShellStream(send func(string)) *shellStream { return &shellStream{send: send} }

func (s *shellStream) write(_ int, p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cut {
		return
	}
	if room := userShellStreamBytes - s.sent - len(s.buf); len(p) > room {
		p = p[:max(room, 0)]
		s.cut = true
	}
	s.buf = append(s.buf, p...)
}

// flush sends the pending output; a trailing incomplete UTF-8 sequence waits for its rest unless final.
func (s *shellStream) flush(final bool) {
	s.mu.Lock()
	n := len(s.buf)
	if !final {
		n = completeUTF8(s.buf)
	}
	chunk := string(s.buf[:n])
	s.buf = append(s.buf[:0], s.buf[n:]...)
	s.sent += n
	cut := s.cut && final
	s.mu.Unlock()
	if cut {
		chunk += "\n… [live output limit reached; the full output follows when the command ends]\n"
	}
	if chunk != "" {
		s.send(strings.ToValidUTF8(chunk, "�"))
	}
}

var userShellReasons = map[string]agentv1.UserShellFinished_Reason{
	"exited": agentv1.UserShellFinished_REASON_EXITED, "stopped": agentv1.UserShellFinished_REASON_STOPPED,
	"time_limit": agentv1.UserShellFinished_REASON_TIME_LIMIT, "session_closed": agentv1.UserShellFinished_REASON_SESSION_CLOSED,
	"failed": agentv1.UserShellFinished_REASON_FAILED,
}

// userShell replays a user_shell_command entry as UserShellStarted and UserShellFinished.
func (r *replayer) userShell(e session.Entry, at time.Time) {
	u := e.UserShell
	if u == nil {
		return
	}
	start := at
	if !at.IsZero() {
		start = at.Add(-time.Duration(u.DurationMs) * time.Millisecond)
	}
	r.emit(start, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_UserShellStarted{UserShellStarted: &agentv1.UserShellStarted{
		CommandId: u.ID, Command: u.Command, Cwd: u.Cwd}}})
	r.emit(at, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_UserShellFinished{UserShellFinished: &agentv1.UserShellFinished{
		CommandId: u.ID, Reason: userShellReasons[u.Reason], ExitCode: int32(u.ExitCode), DurationMs: u.DurationMs,
		Stdout: u.Stdout, Stderr: u.Stderr, Truncated: u.Truncated, Error: u.Error, Cwd: u.Cwd, Command: u.Command}}})
}

// completeUTF8 returns the length of b without a trailing incomplete UTF-8 sequence.
func completeUTF8(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return i
			}
			break
		}
	}
	return len(b)
}
