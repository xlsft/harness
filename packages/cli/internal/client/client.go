// Package client is the only place where the TUI touches packages/client. It runs the client's
// own AgentService implementation (server.Server) in-process on a private unix socket and talks
// to it with the generated Connect client, so the TUI consumes exactly the harness.agent.v1
// event stream (Subscribe) and commands that the desktop UI uses. Everything else in the TUI
// depends only on the agentv1 message types.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/relay"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/server"
	"github.com/xlsft/harness/packages/client/tools"

	"github.com/xlsft/harness/packages/cli/internal/render"
)

// EditPreview computes the diff an Edit or Write call would make, for the permission prompt
// (before the tool runs, so there is no structured result yet). created reports a Write of a
// new file, in which case content is the whole file and patch is nil.
func EditPreview(toolName, inputJSON string) (path string, patch []render.PatchHunk, created bool, content string) {
	var in struct {
		FilePath   string  `json:"file_path"`
		OldString  string  `json:"old_string"`
		NewString  string  `json:"new_string"`
		ReplaceAll bool    `json:"replace_all"`
		Content    *string `json:"content"`
	}
	if json.Unmarshal([]byte(inputJSON), &in) != nil || in.FilePath == "" {
		return "", nil, false, ""
	}
	path = in.FilePath
	data, err := os.ReadFile(in.FilePath)
	before := string(data)
	var after string
	switch toolName {
	case "Write":
		if in.Content == nil {
			return path, nil, false, ""
		}
		if err != nil {
			return path, nil, true, *in.Content
		}
		after = *in.Content
	case "Edit":
		switch {
		case err != nil && in.OldString == "":
			return path, nil, true, in.NewString
		case in.ReplaceAll:
			after = strings.ReplaceAll(before, in.OldString, in.NewString)
		case strings.Contains(before, in.OldString):
			after = strings.Replace(before, in.OldString, in.NewString, 1)
		default:
			before, after = in.OldString, in.NewString
		}
	default:
		return path, nil, false, ""
	}
	for _, h := range tools.DiffLines(before, after, 3) {
		patch = append(patch, render.PatchHunk{OldStart: h.OldStart, OldLines: h.OldLines, NewStart: h.NewStart, NewLines: h.NewLines, Lines: h.Lines})
	}
	return path, patch, false, ""
}

// Options configure the connection and the first session.
type Options struct {
	Cwd   string
	Model string
	// OutputStyle is --output-style: applied to every session this connection creates (a resumed
	// session keeps its recorded style unless this is set).
	OutputStyle     string
	PermissionMode  agentv1.PermissionMode
	AllowedTools    []string
	DisallowedTools []string
	AddDirs         []string
	Resume          string
	ServerURL       string
	// Logger receives client logs; nil discards them (stderr belongs to the TUI).
	Logger *slog.Logger
	// Trust is asked before the first session in an untrusted folder (reference §1a); false
	// aborts Open with ErrUntrusted. nil skips the check.
	Trust func(*agentv1.GetWorkspaceTrustResponse) bool
}

// Conn is a live in-process AgentService plus one open session.
type Conn struct {
	Version  string
	Email    string
	ModelIDs []string
	Catalog  *llm.Catalog
	// Modes are the permission modes the client supports, in Shift+Tab cycle order.
	Modes []agentv1.PermissionMode

	api    agentv1connect.AgentServiceClient
	srv    *server.Server
	dir    string
	cancel context.CancelFunc
	opts   Options

	mu      sync.Mutex
	session *agentv1.SessionInfo
	subStop context.CancelFunc
}

// ErrUntrusted is returned by Open when the user declines to trust the workspace.
var ErrUntrusted = errors.New("workspace not trusted")

// ErrNotLoggedIn is returned by Open when no credentials are stored.
var ErrNotLoggedIn = errors.New("not signed in: run `harness login <server-url>`")

// Open starts the in-process AgentService and creates a session in o.Cwd.
func Open(ctx context.Context, o Options) (*Conn, error) {
	log := o.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	tokens, url, user, err := bootstrap.Tokens(o.ServerURL)
	if err != nil {
		if errors.Is(err, auth.ErrNotLoggedIn) {
			return nil, ErrNotLoggedIn
		}
		return nil, err
	}
	hc := rpc.HTTPClient(url)
	base := bootstrap.Options{
		ServerURL: url,
		Gateway:   connectgw.New(url, hc, tokens),
		Prompts:   prompts.NewConnectSource(url, hc, tokens),
		Logger:    log, Entrypoint: "cli", OutputStyle: o.OutputStyle,
		AllowedTools: o.AllowedTools, DisallowedTools: o.DisallowedTools,
	}
	c := &Conn{Version: config.Version, opts: o}
	if user != nil {
		c.Email = user.Email
	}
	if cat, err := base.Gateway.Models(ctx); err == nil {
		c.Catalog = cat
	}
	// Sessions start in auto mode unless a flag or settings choose another.
	if base.FallbackPermissionMode == "" {
		base.FallbackPermissionMode = permissions.ModeAuto
	}
	c.srv = server.New(base)
	c.srv.UserEmail = c.Email
	c.srv.Logger = log
	c.srv.NoPanes = true

	// A private socket: short enough for macOS (104 bytes), mode 0600 (set by ServeUnix).
	c.dir, err = os.MkdirTemp("", "hcli")
	if err != nil || len(c.dir) > 80 {
		c.dir, err = os.MkdirTemp("/tmp", "hcli")
		if err != nil {
			return nil, err
		}
	}
	socket := filepath.Join(c.dir, "agent.sock")
	sctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	// Modes the server's policy disables: not offered, refused, and a session in one is switched.
	c.srv.SetModePolicy(bootstrap.WatchModePolicy(sctx, url, hc, tokens, user, log))
	// Remote control: mirror the session to the server (HARNESS_REMOTE_CONTROL=0 or the remoteControl setting turn it off).
	if c.srv.RelayOffReason = relay.DisabledReason(true, url, tokens != nil); c.srv.RelayOffReason == "" {
		relay.Attach(sctx, c.srv, relay.Options{ServerURL: url, HTTP: hc, Tokens: tokens, Logger: log})
	}
	ready := make(chan error, 1)
	go func() { ready <- c.srv.ServeUnix(sctx, socket) }()
	c.api = agentv1connect.NewAgentServiceClient(rpc.UnixHTTPClient(socket), "http://agent", connect.WithGRPC())
	for i := 0; ; i++ {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		select {
		case err := <-ready:
			c.Close()
			return nil, fmt.Errorf("agent service: %v", err)
		default:
		}
		if i > 500 {
			c.Close()
			return nil, errors.New("agent service did not start")
		}
		sleep(ctx)
	}
	init, err := c.api.Initialize(ctx, connect.NewRequest(&agentv1.InitializeRequest{Frontend: "cli", FrontendVersion: config.Version}))
	if err != nil {
		c.Close()
		return nil, err
	}
	c.ModelIDs, c.Version = init.Msg.GetModelIds(), init.Msg.GetClientVersion()
	c.Modes = init.Msg.GetPermissionModes()
	if o.Trust != nil {
		tr, err := c.api.GetWorkspaceTrust(ctx, connect.NewRequest(&agentv1.GetWorkspaceTrustRequest{Path: o.Cwd}))
		if err == nil && !tr.Msg.GetTrusted() {
			if !o.Trust(tr.Msg) {
				c.Close()
				return nil, ErrUntrusted
			}
			if _, err := c.api.SetWorkspaceTrust(ctx, connect.NewRequest(&agentv1.SetWorkspaceTrustRequest{Path: o.Cwd, Trusted: true})); err != nil {
				c.Close()
				return nil, err
			}
		}
	}
	if err := c.NewSession(ctx, o.Resume); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func sleep(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Millisecond):
	}
}

// NewSession creates (or resumes) a session and makes it current; /clear uses it.
func (c *Conn) NewSession(ctx context.Context, resume string) error {
	o := c.opts
	mode := o.PermissionMode
	model := o.Model
	style := ""
	if cur := c.Session(); cur != nil {
		mode, model, style = cur.GetPermissionMode(), cur.GetModel(), cur.GetOutputStyle()
		_, _ = c.api.CloseSession(ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: cur.GetId()}))
	}
	resp, err := c.api.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{
		Cwd: o.Cwd, Model: model, PermissionMode: mode, ResumeSessionId: resume, AdditionalDirectories: o.AddDirs,
	}))
	if err != nil {
		return err
	}
	info := resp.Msg.GetSession()
	// /clear keeps the style picked with /output-style (a fresh session applies it at its start).
	if resume == "" && style != "" && style != info.GetOutputStyle() {
		if _, err := c.api.SetOutputStyle(ctx, connect.NewRequest(&agentv1.SetOutputStyleRequest{SessionId: info.GetId(), Style: style})); err == nil {
			info.OutputStyle = style
		}
	}
	c.mu.Lock()
	c.session = info
	c.mu.Unlock()
	return nil
}

// Session returns the current session info (updated from SessionUpdated events by the caller).
func (c *Conn) Session() *agentv1.SessionInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

// SetSession records the latest info of the current session.
func (c *Conn) SetSession(s *agentv1.SessionInfo) {
	c.mu.Lock()
	if c.session != nil && s.GetId() == c.session.GetId() {
		c.session = s
	}
	c.mu.Unlock()
}

func (c *Conn) sid() string { return c.Session().GetId() }

// Subscribe streams the current session's events to fn until ctx ends or the session changes.
// It returns immediately; fn runs on the subscription goroutine.
func (c *Conn) Subscribe(ctx context.Context, fn func(*agentv1.SessionEvent), done func(error)) {
	ctx, stop := context.WithCancel(ctx)
	c.mu.Lock()
	if c.subStop != nil {
		c.subStop()
	}
	c.subStop = stop
	sid := c.session.GetId()
	c.mu.Unlock()
	go func() {
		var after int64
		for {
			stream, err := c.api.Subscribe(ctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid, AfterSeq: after}))
			if err != nil {
				done(err)
				return
			}
			for stream.Receive() {
				ev := stream.Msg().GetEvent()
				if _, delta := ev.GetEvent().(*agentv1.SessionEvent_AssistantDelta); !delta {
					after = ev.GetSeq()
				}
				fn(ev)
			}
			err = stream.Err()
			if ctx.Err() != nil {
				return
			}
			if connect.CodeOf(err) != connect.CodeResourceExhausted {
				done(err)
				return
			}
			// Fell behind: resubscribe after the last durable event.
		}
	}()
}

// Send submits a user message (queued by the client while a turn runs).
func (c *Conn) Send(ctx context.Context, text string) error {
	_, err := c.api.SendUserMessage(ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{
		SessionId: c.sid(), Uuid: uuid.NewString(),
		Content: []*llmv1.ContentBlock{{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{Text: text}}}},
	}))
	return err
}

// Interrupt cancels the running turn.
func (c *Conn) Interrupt(ctx context.Context) error {
	_, err := c.api.Interrupt(ctx, connect.NewRequest(&agentv1.InterruptRequest{SessionId: c.sid()}))
	return err
}

// RespondToPermission answers a PermissionRequest.
func (c *Conn) RespondToPermission(ctx context.Context, requestID string, d agentv1.RespondToPermissionRequest_Decision, rule, comment string) error {
	_, err := c.api.RespondToPermission(ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{
		SessionId: c.sid(), RequestId: requestID, Decision: d, Rule: rule, Comment: comment,
	}))
	return err
}

// ResolveProposal starts or dismisses a proposal of the session (a SessionProposal event follows).
func (c *Conn) ResolveProposal(ctx context.Context, id string, start bool) error {
	action := agentv1.ResolveSessionProposalRequest_ACTION_DISMISS
	if start {
		action = agentv1.ResolveSessionProposalRequest_ACTION_START
	}
	_, err := c.api.ResolveSessionProposal(ctx, connect.NewRequest(&agentv1.ResolveSessionProposalRequest{SessionId: c.sid(), ProposalId: id, Action: action}))
	return err
}

// AnswerQuestion answers an AskUserQuestion prompt.
// Each answer is the chosen option labels plus an optional free-text answer.
func (c *Conn) AnswerQuestion(ctx context.Context, requestID string, answers []Answer) error {
	req := &agentv1.AnswerQuestionRequest{SessionId: c.sid(), RequestId: requestID}
	for _, a := range answers {
		req.Answers = append(req.Answers, &agentv1.QuestionAnswer{Selected: a.Selected, Text: a.Text})
	}
	_, err := c.api.AnswerQuestion(ctx, connect.NewRequest(req))
	return err
}

// Answer is the answer to one question of an AskUserQuestion prompt.
type Answer struct {
	Selected []string
	Text     string
}

// RespondToPlan answers a PlanProposed prompt.
func (c *Conn) RespondToPlan(ctx context.Context, requestID string, d agentv1.RespondToPlanRequest_Decision, feedback string) error {
	_, err := c.api.RespondToPlan(ctx, connect.NewRequest(&agentv1.RespondToPlanRequest{
		SessionId: c.sid(), RequestId: requestID, Decision: d, Feedback: feedback,
	}))
	return err
}

// SetPermissionMode changes the session's mode.
func (c *Conn) SetPermissionMode(ctx context.Context, m agentv1.PermissionMode) error {
	_, err := c.api.SetPermissionMode(ctx, connect.NewRequest(&agentv1.SetPermissionModeRequest{SessionId: c.sid(), Mode: m}))
	return err
}

// SetModel switches the session's model.
func (c *Conn) SetModel(ctx context.Context, model string) error {
	_, err := c.api.SetModel(ctx, connect.NewRequest(&agentv1.SetModelRequest{SessionId: c.sid(), Model: model}))
	return err
}

// SetOutputStyle switches the session's output style ("default" clears it).
func (c *Conn) SetOutputStyle(ctx context.Context, style string) error {
	_, err := c.api.SetOutputStyle(ctx, connect.NewRequest(&agentv1.SetOutputStyleRequest{SessionId: c.sid(), Style: style}))
	return err
}

// OutputStyles lists the built-in, user and project output styles and the active one.
func (c *Conn) OutputStyles(ctx context.Context) ([]*agentv1.OutputStyleInfo, string, error) {
	resp, err := c.api.ListOutputStyles(ctx, connect.NewRequest(&agentv1.ListOutputStylesRequest{SessionId: c.sid()}))
	if err != nil {
		return nil, "", err
	}
	return resp.Msg.GetStyles(), resp.Msg.GetActive(), nil
}

// SuggestFiles returns fuzzy file matches for an @-mention.
func (c *Conn) SuggestFiles(ctx context.Context, query string, limit int) ([]string, error) {
	resp, err := c.api.SuggestFiles(ctx, connect.NewRequest(&agentv1.SuggestFilesRequest{SessionId: c.sid(), Query: query, Limit: int32(limit)}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetPaths(), nil
}

// Checkpoints lists the rewind points of the session.
func (c *Conn) Checkpoints(ctx context.Context) ([]*agentv1.Checkpoint, error) {
	resp, err := c.api.ListCheckpoints(ctx, connect.NewRequest(&agentv1.ListCheckpointsRequest{SessionId: c.sid()}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetCheckpoints(), nil
}

// Rewind restores code and/or conversation to a checkpoint.
func (c *Conn) Rewind(ctx context.Context, checkpointID string, mode agentv1.RewindRequest_Mode) (*agentv1.Rewound, error) {
	resp, err := c.api.Rewind(ctx, connect.NewRequest(&agentv1.RewindRequest{SessionId: c.sid(), CheckpointId: checkpointID, Mode: mode}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetResult(), nil
}

// Compact compacts the conversation now.
func (c *Conn) Compact(ctx context.Context, instructions string) error {
	_, err := c.api.Compact(ctx, connect.NewRequest(&agentv1.CompactRequest{SessionId: c.sid(), Instructions: instructions}))
	return err
}

// Tasks lists background tasks.
func (c *Conn) Tasks(ctx context.Context) ([]*agentv1.BackgroundTask, error) {
	resp, err := c.api.ListTasks(ctx, connect.NewRequest(&agentv1.ListTasksRequest{SessionId: c.sid()}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetTasks(), nil
}

// StopTask stops a background task.
func (c *Conn) StopTask(ctx context.Context, id string) error {
	_, err := c.api.StopTask(ctx, connect.NewRequest(&agentv1.StopTaskRequest{SessionId: c.sid(), TaskId: id}))
	return err
}

// ContextWindow returns the context window of model (0 when unknown).
func (c *Conn) ContextWindow(model string) int {
	m, _ := c.Catalog.Find(model)
	return m.ContextWindow
}

// DisplayName returns the catalog display name of model.
func (c *Conn) DisplayName(model string) string {
	if m, ok := c.Catalog.Find(model); ok && m.DisplayName != "" {
		return m.DisplayName
	}
	return model
}

// Close ends the session and stops the service.
func (c *Conn) Close() {
	if s := c.Session(); s != nil && c.api != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = c.api.CloseSession(ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: s.GetId()}))
		cancel()
	}
	if c.cancel != nil {
		c.cancel()
	}
	if c.dir != "" {
		_ = os.RemoveAll(c.dir)
	}
}
