package relay

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/server"
)

// epochWait bounds how long a command routed to a newer epoch waits for this
// host's RegisterSession response (the server pushes queued commands to the
// new owner before the response arrives).
const epochWait = 10 * time.Second

func outcome(id string, o relayv1.CommandAck_Outcome, msg string) *relayv1.CommandAck {
	return &relayv1.CommandAck{CommandId: id, Outcome: o, ErrorMessage: msg}
}

// dispatch routes a command from the host stream: duplicates are re-acked or
// ignored, expired and bypass commands are rejected, commands for an epoch
// this host does not own are ignored (the owner gets them), and the rest run
// in order on the session's worker.
func (h *Host) dispatch(ctx context.Context, c *relayv1.Command) {
	id := c.GetCommandId()
	if id == "" {
		return
	}
	h.mu.Lock()
	if a, ok := h.done[id]; ok {
		// Redelivered because the ack was lost: send the same outcome again.
		h.acks = append(h.acks, a)
		h.mu.Unlock()
		h.kickAcks()
		return
	}
	if h.running[id] {
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()

	if exp, err := time.Parse(time.RFC3339Nano, c.GetExpiresAt()); err == nil && time.Now().After(exp) {
		h.ack(outcome(id, relayv1.CommandAck_OUTCOME_REJECTED, "command expired before it reached this machine"))
		return
	}
	if msg := rejectRemote(c); msg != "" {
		h.ack(outcome(id, relayv1.CommandAck_OUTCOME_REJECTED, msg))
		return
	}
	if c.GetSessionId() == "" {
		h.markRunning(id, true)
		go func() { h.ack(h.execHost(ctx, c)) }()
		return
	}
	// A session this host does not mirror (or lost to another host) is not
	// ours: leave the command to its owner. An older epoch on a session we
	// still own is a redelivery from before our last registration and runs.
	hs := h.session(c.GetSessionId())
	if hs == nil {
		// Open here with remote control turned off: refuse rather than leave the viewer waiting.
		if f, ok := h.o.Backend.SessionFeed(c.GetSessionId()); ok && !f.Closed() && !f.RemoteControl() {
			h.ack(outcome(id, relayv1.CommandAck_OUTCOME_REJECTED, "remote control is turned off for this session"))
		}
		return
	}
	if hs.isSuperseded() {
		return
	}
	h.markRunning(id, true)
	select {
	case hs.cmds <- c:
	default:
		h.markRunning(id, false) // full: the server redelivers it
	}
}

func (h *Host) markRunning(id string, on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if on {
		h.running[id] = true
	} else {
		delete(h.running, id)
	}
}

// rejectRemote returns why a command may never run remotely ("" if it may).
// Permissions stay on the host: bypass mode cannot be enabled through the relay.
func rejectRemote(c *relayv1.Command) string {
	const bypass = "bypassPermissions cannot be enabled remotely"
	switch p := c.GetPayload().(type) {
	case *relayv1.Command_SetPermissionMode:
		if p.SetPermissionMode.GetMode() == agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS {
			return bypass
		}
	case *relayv1.Command_SpawnSession:
		if p.SpawnSession.GetCreate().GetPermissionMode() == agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS {
			return bypass
		}
	case *relayv1.Command_OpenTunnel:
		return "tunnels are not supported by this client yet"
	}
	return ""
}

// waitEpoch waits until this host registered the epoch a command was routed
// to (or a newer one); false when the session was taken over.
func (hs *hostSession) waitEpoch(ctx context.Context, epoch int64) bool {
	deadline := time.Now().Add(epochWait)
	for {
		cur := hs.currentEpoch()
		switch {
		case hs.isSuperseded():
			return false
		case cur >= epoch:
			return true
		case time.Now().After(deadline):
			return false
		}
		if !sleep(ctx, 20*time.Millisecond) {
			return false
		}
	}
}

// worker runs the session's commands in arrival order. Compact and Rewind run
// for a long time and do not block later commands (interrupts, answers).
func (hs *hostSession) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-hs.quit:
			// Remote control turned off: commands not started yet are refused (dispatch refuses new ones).
			for {
				select {
				case c := <-hs.cmds:
					hs.h.ack(outcome(c.GetCommandId(), relayv1.CommandAck_OUTCOME_REJECTED, "remote control is turned off for this session"))
				default:
					return
				}
			}
		case c := <-hs.cmds:
			if !hs.waitEpoch(ctx, c.GetEpoch()) {
				hs.h.markRunning(c.GetCommandId(), false)
				continue
			}
			switch c.GetPayload().(type) {
			case *relayv1.Command_Compact, *relayv1.Command_Rewind:
				go func() { hs.h.ack(hs.h.exec(ctx, c)) }()
			default:
				hs.h.ack(hs.h.exec(ctx, c))
			}
		}
	}
}

// errAck converts an AgentService error into an ack. For answers to prompts
// (permission, question, plan), NOT_FOUND means another client answered first.
func errAck(id string, err error, answer bool) *relayv1.CommandAck {
	if err == nil {
		return outcome(id, relayv1.CommandAck_OUTCOME_OK, "")
	}
	code := connect.CodeOf(err)
	if answer && code == connect.CodeNotFound {
		return outcome(id, relayv1.CommandAck_OUTCOME_ALREADY_RESOLVED, "")
	}
	msg := err.Error()
	var ce *connect.Error
	if errors.As(err, &ce) {
		msg = ce.Message()
	}
	return outcome(id, relayv1.CommandAck_OUTCOME_FAILED, msg)
}

// exec runs a session command through the backend's AgentService methods, the
// same code paths local frontends call.
func (h *Host) exec(ctx context.Context, c *relayv1.Command) *relayv1.CommandAck {
	ctx = server.WithRemoteOrigin(ctx)
	b, id, sid := h.o.Backend, c.GetCommandId(), c.GetSessionId()
	// The command's session is authoritative; the wrapped request cannot point elsewhere.
	clone := func(m proto.Message) proto.Message { return proto.Clone(m) }
	switch p := c.GetPayload().(type) {
	case *relayv1.Command_SendUserMessage:
		m := clone(p.SendUserMessage).(*agentv1.SendUserMessageRequest)
		m.SessionId = sid
		_, err := b.SendUserMessage(server.WithRelayOrigin(ctx), connect.NewRequest(m))
		return errAck(id, err, false)
	case *relayv1.Command_Interrupt:
		_, err := b.Interrupt(ctx, connect.NewRequest(&agentv1.InterruptRequest{SessionId: sid}))
		return errAck(id, err, false)
	case *relayv1.Command_RespondToPermission:
		m := clone(p.RespondToPermission).(*agentv1.RespondToPermissionRequest)
		m.SessionId = sid
		_, err := b.RespondToPermission(ctx, connect.NewRequest(m))
		return errAck(id, err, true)
	case *relayv1.Command_AnswerQuestion:
		m := clone(p.AnswerQuestion).(*agentv1.AnswerQuestionRequest)
		m.SessionId = sid
		_, err := b.AnswerQuestion(ctx, connect.NewRequest(m))
		return errAck(id, err, true)
	case *relayv1.Command_RespondToPlan:
		m := clone(p.RespondToPlan).(*agentv1.RespondToPlanRequest)
		m.SessionId = sid
		_, err := b.RespondToPlan(ctx, connect.NewRequest(m))
		return errAck(id, err, true)
	case *relayv1.Command_SetModel:
		_, err := b.SetModel(ctx, connect.NewRequest(&agentv1.SetModelRequest{SessionId: sid, Model: p.SetModel.GetModel()}))
		return errAck(id, err, false)
	case *relayv1.Command_SetEffort:
		_, err := b.SetEffort(ctx, connect.NewRequest(&agentv1.SetEffortRequest{SessionId: sid, Effort: p.SetEffort.GetEffort()}))
		return errAck(id, err, false)
	case *relayv1.Command_SetPermissionMode:
		_, err := b.SetPermissionMode(ctx, connect.NewRequest(&agentv1.SetPermissionModeRequest{SessionId: sid, Mode: p.SetPermissionMode.GetMode()}))
		return errAck(id, err, false)
	case *relayv1.Command_SetOutputStyle:
		_, err := b.SetOutputStyle(ctx, connect.NewRequest(&agentv1.SetOutputStyleRequest{SessionId: sid, Style: p.SetOutputStyle.GetStyle()}))
		return errAck(id, err, false)
	case *relayv1.Command_SetAutoContinue:
		_, err := b.SetAutoContinue(ctx, connect.NewRequest(&agentv1.SetAutoContinueRequest{SessionId: sid, Enabled: p.SetAutoContinue.GetEnabled()}))
		return errAck(id, err, false)
	case *relayv1.Command_SetKeepAwake:
		_, err := b.SetKeepAwake(ctx, connect.NewRequest(&agentv1.SetKeepAwakeRequest{SessionId: sid, Enabled: p.SetKeepAwake.GetEnabled()}))
		return errAck(id, err, false)
	case *relayv1.Command_RemoveQueuedMessage:
		res, err := b.RemoveQueuedMessage(ctx, connect.NewRequest(&agentv1.RemoveQueuedMessageRequest{SessionId: sid, Uuid: p.RemoveQueuedMessage.GetUuid()}))
		a := errAck(id, err, false)
		if err == nil {
			a.Result = &relayv1.CommandAck_RemoveQueuedMessage{RemoveQueuedMessage: res.Msg}
		}
		return a
	case *relayv1.Command_StopTask:
		_, err := b.StopTask(ctx, connect.NewRequest(&agentv1.StopTaskRequest{SessionId: sid, TaskId: p.StopTask.GetTaskId()}))
		return errAck(id, err, false)
	case *relayv1.Command_CloseSession:
		_, err := b.CloseSession(ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid}))
		return errAck(id, err, false)
	case *relayv1.Command_Compact:
		res, err := b.Compact(ctx, connect.NewRequest(&agentv1.CompactRequest{SessionId: sid, Instructions: p.Compact.GetInstructions()}))
		a := errAck(id, err, false)
		if err == nil {
			a.Result = &relayv1.CommandAck_Compact{Compact: res.Msg}
		}
		return a
	case *relayv1.Command_Rewind:
		m := clone(p.Rewind).(*agentv1.RewindRequest)
		m.SessionId = sid
		res, err := b.Rewind(ctx, connect.NewRequest(m))
		a := errAck(id, err, false)
		if err == nil {
			a.Result = &relayv1.CommandAck_Rewind{Rewind: res.Msg}
		}
		return a
	case *relayv1.Command_SuggestFiles:
		res, err := b.SuggestFiles(ctx, connect.NewRequest(&agentv1.SuggestFilesRequest{SessionId: sid, Query: p.SuggestFiles.GetQuery(), Limit: p.SuggestFiles.GetLimit()}))
		a := errAck(id, err, false)
		if err == nil {
			a.Result = &relayv1.CommandAck_SuggestFiles{SuggestFiles: res.Msg}
		}
		return a
	case *relayv1.Command_ListCheckpoints:
		res, err := b.ListCheckpoints(ctx, connect.NewRequest(&agentv1.ListCheckpointsRequest{SessionId: sid}))
		a := errAck(id, err, false)
		if err == nil {
			a.Result = &relayv1.CommandAck_ListCheckpoints{ListCheckpoints: res.Msg}
		}
		return a
	case *relayv1.Command_ListTasks:
		res, err := b.ListTasks(ctx, connect.NewRequest(&agentv1.ListTasksRequest{SessionId: sid}))
		a := errAck(id, err, false)
		if err == nil {
			a.Result = &relayv1.CommandAck_ListTasks{ListTasks: res.Msg}
		}
		return a
	case *relayv1.Command_ListOutputStyles:
		res, err := b.ListOutputStyles(ctx, connect.NewRequest(&agentv1.ListOutputStylesRequest{SessionId: sid}))
		a := errAck(id, err, false)
		if err == nil {
			a.Result = &relayv1.CommandAck_ListOutputStyles{ListOutputStyles: res.Msg}
		}
		return a
	}
	return outcome(id, relayv1.CommandAck_OUTCOME_REJECTED, "unsupported command")
}

// execHost runs a host-level command (spawn).
func (h *Host) execHost(ctx context.Context, c *relayv1.Command) *relayv1.CommandAck {
	ctx = server.WithRemoteOrigin(ctx)
	id := c.GetCommandId()
	sp := c.GetSpawnSession()
	if sp == nil {
		return outcome(id, relayv1.CommandAck_OUTCOME_REJECTED, "unsupported host command")
	}
	if !h.o.Daemon {
		return outcome(id, relayv1.CommandAck_OUTCOME_REJECTED, "this client does not accept new sessions (not a daemon)")
	}
	cwd := sp.GetCreate().GetCwd()
	if !UnderRoots(cwd, h.o.AllowedRoots) {
		return outcome(id, relayv1.CommandAck_OUTCOME_REJECTED, cwd+" is not under an allowed root")
	}
	if sp.GetWorktree() {
		return outcome(id, relayv1.CommandAck_OUTCOME_REJECTED, "spawning into a new worktree is not supported yet")
	}
	create := proto.Clone(sp.GetCreate()).(*agentv1.CreateSessionRequest)
	res, err := h.o.Backend.CreateSession(ctx, connect.NewRequest(create))
	if err != nil {
		return errAck(id, err, false)
	}
	if first := sp.GetInitialMessage(); first != nil {
		m := proto.Clone(first).(*agentv1.SendUserMessageRequest)
		m.SessionId = res.Msg.GetSession().GetId()
		if _, err := h.o.Backend.SendUserMessage(ctx, connect.NewRequest(m)); err != nil {
			h.log.Warn("spawned session did not accept the initial message", "session", m.SessionId, "err", err)
		}
	}
	a := outcome(id, relayv1.CommandAck_OUTCOME_OK, "")
	a.Result = &relayv1.CommandAck_SpawnSession{SpawnSession: res.Msg}
	return a
}

// UnderRoots reports whether dir (absolute) lies inside one of roots.
func UnderRoots(dir string, roots []string) bool {
	if dir == "" || !filepath.IsAbs(dir) {
		return false
	}
	dir = filepath.Clean(dir)
	for _, r := range roots {
		if r == "" || !filepath.IsAbs(r) {
			continue
		}
		rel, err := filepath.Rel(filepath.Clean(r), dir)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}
