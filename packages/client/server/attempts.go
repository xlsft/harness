package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Best-of-N (docs/design/hosts.md, decisions 28 to 30): a session that is one attempt of a group runs the group's test
// command in its own checkout when a turn ends, and reports the result to the group and to its viewers. The same code runs on
// a laptop and on a host.

const (
	// attemptCheckTimeout bounds one run of a test command; attemptOutputCap is what of its output is kept (the end).
	attemptCheckTimeout = 10 * time.Minute
	attemptOutputCap    = 8 * 1024
	attemptReportWait   = 30 * time.Second
)

// AttemptSpec makes the session that CreateSession opens one attempt of a group.
type AttemptSpec struct {
	GroupID     string
	Index       int32
	Count       int32
	TestCommand string
	// BudgetUSD stops the session once it spent this much (0: no limit).
	BudgetUSD float64
}

type attemptKey struct{}

// WithAttempt carries an attempt to CreateSession: the daemon and the local attempts use it, never a frontend.
func WithAttempt(ctx context.Context, spec AttemptSpec) context.Context {
	return context.WithValue(ctx, attemptKey{}, &spec)
}

func attemptFrom(ctx context.Context) *AttemptSpec {
	s, _ := ctx.Value(attemptKey{}).(*AttemptSpec)
	return s
}

func attemptProto(a *session.Attempt) *agentv1.SessionAttempt {
	if a == nil {
		return nil
	}
	return &agentv1.SessionAttempt{GroupId: a.GroupID, Index: a.Index, Count: a.Count}
}

func attemptCheckProto(a *session.Attempt, c *session.AttemptCheck) *agentv1.AttemptCheck {
	if a == nil || c == nil {
		return nil
	}
	return &agentv1.AttemptCheck{GroupId: a.GroupID, Index: a.Index, Command: c.Command, ExitCode: c.ExitCode, Output: c.Output,
		Refused: c.Refused, TimedOut: c.TimedOut, Head: c.Head, CheckedAt: c.CheckedAt}
}

// setAttempt makes a new session an attempt, on record in its transcript.
func (ls *liveSession) setAttempt(spec *AttemptSpec) {
	a := &session.Attempt{Session: ls.sess.ID, GroupID: spec.GroupID, Index: spec.Index, Count: spec.Count, TestCommand: strings.TrimSpace(spec.TestCommand), BudgetUSD: spec.BudgetUSD}
	ls.mu.Lock()
	ls.attempt = a
	ls.mu.Unlock()
	if _, err := ls.sess.Transcript.Append(session.Entry{Type: session.TypeMeta, SessionID: ls.sess.ID, Attempt: a, Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		ls.srv.log().Warn("recording the attempt in the transcript failed", "session", ls.sess.ID, "err", err)
	}
}

// restoreAttempt reads back what the transcript says about the attempt, and shows its last check.
func (ls *liveSession) restoreAttempt(entries []session.Entry) {
	a, c := session.ReadAttempt(entries, ls.sess.ID)
	if a == nil {
		return
	}
	ls.mu.Lock()
	ls.attempt = a
	ls.mu.Unlock()
	if pc := attemptCheckProto(a, c); pc != nil {
		ls.hub.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_AttemptChecked{AttemptChecked: pc}})
		ls.history++
	}
}

func (ls *liveSession) attemptOf() *session.Attempt {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.attempt
}

// afterAttemptTurn runs the test command and reports the attempt when one of its turns ended.
func (ls *liveSession) afterAttemptTurn() {
	a := ls.attemptOf()
	if a == nil || ls.ctx.Err() != nil {
		return
	}
	ls.bg.Add(1)
	go func() {
		defer ls.bg.Done()
		var check *agentv1.AttemptCheck
		if a.TestCommand != "" {
			c := ls.runAttemptCheck(ls.ctx, a)
			if ls.ctx.Err() != nil {
				return
			}
			if _, err := ls.sess.Transcript.Append(session.Entry{Type: session.TypeMeta, SessionID: ls.sess.ID, AttemptCheck: c, Timestamp: c.CheckedAt}); err != nil {
				ls.srv.log().Warn("recording the attempt's check failed", "session", ls.sess.ID, "err", err)
			}
			check = attemptCheckProto(a, c)
			ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_AttemptChecked{AttemptChecked: check}})
		}
		ls.srv.reportAttempt(ls.ctx, &relayv1.ReportAttemptRequest{GroupId: a.GroupID, Index: a.Index, State: relayv1.AttemptState_ATTEMPT_STATE_IDLE,
			SessionId: ls.sess.ID, Branch: workspace.GitBranch(ls.ctx, ls.sess.Dir()), Check: check})
	}()
}

// runAttemptCheck runs the test command at the top of the session's folder, in the OS sandbox where there is a driver.
// A deny rule on the command refuses it.
func (ls *liveSession) runAttemptCheck(ctx context.Context, a *session.Attempt) *session.AttemptCheck {
	c := &session.AttemptCheck{Command: a.TestCommand}
	dir := ls.sess.Dir()
	if head, err := moveGit(ctx, dir, nil, "rev-parse", "HEAD"); err == nil {
		c.Head = strings.TrimSpace(head)
	}
	defer func() { c.CheckedAt = time.Now().UTC().Format(time.RFC3339) }()
	if rule := ls.sess.Permissions.DeniesCommand(a.TestCommand); rule != "" {
		c.Refused, c.ExitCode, c.Output = true, -1, fmt.Sprintf("Not run: %s denies it", rule)
		return c
	}
	sh := ls.sess.ToolEnv.Shell
	if sh == nil {
		c.ExitCode, c.Output = -1, "Not run: the session has no shell"
		return c
	}
	// A subshell, so that the session's own shell stays where it is.
	command := fmt.Sprintf("( cd %s && %s )", shellQuote(dir), a.TestCommand)
	res, err := sh.Run(ctx, command, tools.ExecOptions{Timeout: attemptCheckTimeout, Confine: true})
	out := tools.UserShellResult{Stdout: res.Stdout, Stderr: res.Stderr}.CombinedOutput()
	if len(out) > attemptOutputCap {
		out = out[len(out)-attemptOutputCap:]
	}
	c.Output, c.ExitCode, c.TimedOut = out, int32(res.ExitCode), res.TimedOut
	if err != nil {
		c.ExitCode = -1
		if c.Output == "" {
			c.Output = err.Error()
		}
	}
	return c
}

// shellQuote quotes a path for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// reportAttempt tells the group what became of an attempt. Without a server, or when it fails, the group shows the attempt as
// it was; the session has its result either way.
func (s *Server) reportAttempt(ctx context.Context, req *relayv1.ReportAttemptRequest) {
	hosts, _, tokens, err := s.hostsAPI()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), attemptReportWait)
	defer cancel()
	if _, err := coreCall(ctx, tokens, hosts.ReportAttempt, req); err != nil {
		s.log().Warn("reporting an attempt failed", "group", req.GetGroupId(), "index", req.GetIndex(), "err", err)
	}
}
