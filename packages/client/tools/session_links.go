package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The tools that start, list and message other sessions, and MarkChapter. Sessions run on the client's server, so the
// tools reach them through the session's Interactor (SessionCoordinator, implemented by the live session); elsewhere
// (print mode) they report that they are not available.

// SessionStart initiations.
const (
	// SessionOffer asks the user with a card and waits for the answer.
	SessionOffer = "offer"
	// SessionUserAsked starts at once: the user asked for it.
	SessionUserAsked = "user_asked"
	// SessionOwnInitiative leaves a card the user may start later; the call does not wait.
	SessionOwnInitiative = "own_initiative"
)

// SessionStart contexts.
const (
	SessionContextFresh = "fresh"
	SessionContextFork  = "fork"
)

// Answers of the offer card.
const (
	SessionOfferStart = "New session"
	SessionOfferHere  = "Do it here"
)

// Limits of the session tools' input and output.
const (
	maxSessionTitle  = 120
	maxChapterTitle  = 80
	offerPromptChars = 400
	listedTitleChars = 80
)

// SessionRequest is a session to start.
type SessionRequest struct {
	ToolUseID, Title, Prompt string
	Fork, Worktree           bool
	// Model and Cwd default to the ones of this session.
	Model, Cwd string
}

// LinkedSession is a session started from this one.
type LinkedSession struct {
	ID, Name, Title, Cwd string
	// WorktreePath and WorktreeBranch are set when it runs in a new worktree.
	WorktreePath, WorktreeBranch string
}

// SessionProposalInfo is a proposal the user has not started yet.
type SessionProposalInfo struct {
	ID        string
	ExpiresAt time.Time
}

// SessionSummary is one row of SessionList.
type SessionSummary struct {
	ID string `json:"id"`
	// Name is the short name of a session started from another one.
	Name  string `json:"name,omitempty"`
	Title string `json:"title"`
	// State is idle, working, needs_input, waiting_for_connection, error, closed (not open on this machine) or
	// remote (on another machine; Device names it).
	State string `json:"state"`
	// Relation is parent, child or self; empty for unrelated sessions.
	Relation string `json:"relation,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Device   string `json:"device,omitempty"`
	Archived bool   `json:"archived,omitempty"`
}

// SessionDelivery is what became of a SessionSend.
type SessionDelivery struct {
	ID, Name, Title string
	// Woke: the message started a turn there. Otherwise it is read with that session's next model request.
	Woke bool
	// Held: too many wakes in a row without a user message; it waits for the next user turn.
	Held bool
}

// SessionCoordinator is implemented by an Interactor whose sessions can start and reach other sessions (AgentService).
type SessionCoordinator interface {
	StartSession(ctx context.Context, r SessionRequest) (LinkedSession, error)
	ProposeSession(r SessionRequest) (SessionProposalInfo, error)
	DismissProposal(id string) error
	ListSessions(ctx context.Context, linked bool) ([]SessionSummary, error)
	SendToSession(ctx context.Context, to, message string) (SessionDelivery, error)
	// ArchiveSession archives a session started from this one and returns its title.
	ArchiveSession(ctx context.Context, to string) (string, error)
	// DescribeSession says which session `to` is, for the permission prompt ("" when unknown).
	DescribeSession(to string) string
}

func sessionCoordinator(env *Env) SessionCoordinator {
	h := env.Host()
	if h == nil {
		return nil
	}
	c, _ := h.Interactor().(SessionCoordinator)
	return c
}

const noCoordinator = "Other sessions cannot be reached from here: this session does not run on the client's agent service."

// RegisterSessionTools adds the session tools to r.
func RegisterSessionTools(r *Registry) {
	for _, t := range []Tool{&SessionStart{}, &SessionHandoff{}, &SessionDismiss{}, &SessionList{}, &SessionSend{}, &SessionArchive{}, &MarkChapter{}} {
		r.Register(t)
	}
}

// ---- SessionStart ----

// SessionStart starts another session, or offers or proposes one to the user.
type SessionStart struct{}

type sessionStartInput struct {
	Title      string `json:"title"`
	Prompt     string `json:"prompt"`
	Initiation string `json:"initiation"`
	Context    string `json:"context"`
	Worktree   bool   `json:"worktree"`
	Model      string `json:"model"`
	Cwd        string `json:"cwd"`
}

func (*SessionStart) Name() string { return "SessionStart" }

// ReadOnly: an offer and a proposal change nothing until the user starts them, so they stay available in plan mode.
func (*SessionStart) ReadOnly(input json.RawMessage) bool {
	var in sessionStartInput
	if json.Unmarshal(input, &in) != nil {
		return false
	}
	return in.Initiation == SessionOffer || in.Initiation == SessionOwnInitiative
}

func parseSessionStart(env *Env, input json.RawMessage) (sessionStartInput, SessionRequest, error) {
	var in sessionStartInput
	if err := decode(input, &in); err != nil {
		return in, SessionRequest{}, err
	}
	bad := func(format string, args ...any) (sessionStartInput, SessionRequest, error) {
		return in, SessionRequest{}, fmt.Errorf("InputValidationError: "+format, args...)
	}
	in.Title = strings.Join(strings.Fields(in.Title), " ")
	switch {
	case in.Title == "":
		return bad("title is required")
	case len([]rune(in.Title)) > maxSessionTitle:
		return bad("title is longer than %d characters", maxSessionTitle)
	case strings.TrimSpace(in.Prompt) == "":
		return bad("prompt is required")
	case !slices.Contains([]string{SessionOffer, SessionUserAsked, SessionOwnInitiative}, in.Initiation):
		return bad("initiation must be offer, user_asked or own_initiative")
	case in.Context != SessionContextFresh && in.Context != SessionContextFork:
		return bad("context must be fresh or fork")
	}
	r := SessionRequest{Title: in.Title, Prompt: in.Prompt, Fork: in.Context == SessionContextFork, Worktree: in.Worktree, Model: in.Model}
	if in.Cwd != "" {
		r.Cwd = in.Cwd
		if !filepath.IsAbs(r.Cwd) {
			r.Cwd = filepath.Join(env.Cwd, r.Cwd)
		}
		st, err := os.Stat(r.Cwd)
		if err != nil || !st.IsDir() {
			return bad("cwd %s is not a directory", r.Cwd)
		}
	}
	return in, r, nil
}

// SessionOfferQuestions is the card of an offer (a SessionStart input with initiation "offer"), nil for any other input.
func SessionOfferQuestions(input json.RawMessage) []Question {
	var in sessionStartInput
	if json.Unmarshal(input, &in) != nil || in.Initiation != SessionOffer {
		return nil
	}
	preview := strings.TrimSpace(in.Prompt)
	if r := []rune(preview); len(r) > offerPromptChars {
		preview = string(r[:offerPromptChars]) + "…"
	}
	return []Question{{Question: "Start a new session for this? " + strings.Join(strings.Fields(in.Title), " "), Header: "New session", Type: QuestionSingle,
		AllowOther: boolPtr(false), Options: []QuestionOption{
			{Label: SessionOfferStart, Description: preview, Recommended: true},
			{Label: SessionOfferHere, Description: "Stay in this session and do it here."},
		}}}
}

func (*SessionStart) Run(ctx context.Context, env *Env, call Call) Result {
	in, req, err := parseSessionStart(env, call.Input)
	if err != nil {
		return ErrorResult("%v", err)
	}
	req.ToolUseID = call.ID
	c := sessionCoordinator(env)
	if c == nil {
		return ErrorResult(noCoordinator)
	}
	switch in.Initiation {
	case SessionUserAsked:
		return startedResult(ctx, c, req, map[string]any{"initiation": in.Initiation})
	case SessionOwnInitiative:
		p, err := c.ProposeSession(req)
		if err != nil {
			return ErrorResult("could not propose the session: %v", err)
		}
		expires := p.ExpiresAt.UTC().Format(time.RFC3339)
		r := TextResult(fmt.Sprintf("Proposal %s is on a card in the transcript. The user may start it later; it expires on %s. Do not wait for it, and do not do its work here unless asked. Withdraw it with SessionDismiss if it stops making sense.", p.ID, expires),
			map[string]any{"initiation": in.Initiation, "proposal_id": p.ID, "expires_at": expires})
		r.Summary = "Proposed a session"
		return r
	}
	qs, err := NormalizeQuestions(SessionOfferQuestions(call.Input))
	if err != nil {
		return ErrorResult("InputValidationError: %v", err)
	}
	resp, err := env.Host().Interactor().AskQuestions(ctx, call.ID, qs)
	if errors.Is(err, ErrNonInteractive) {
		return ErrorResult("No user is attached to this session to answer the offer. Carry on here, or use initiation own_initiative to leave a card for later.")
	}
	if err != nil {
		return ErrorResult("the offer was not answered: %v", err)
	}
	r := QuestionResult(qs, resp)
	structured, _ := r.Structured.(map[string]any)
	structured["initiation"] = in.Initiation
	switch {
	case resp.Outcome == QuestionMessage:
		return r
	case resp.Outcome == QuestionAnswered && len(resp.Answers) > 0 && slices.Contains(resp.Answers[0].Selected, SessionOfferStart):
		return startedResult(ctx, c, req, structured)
	case resp.Outcome == QuestionAnswered:
		r = TextResult("The user wants this done here. Do not start a session for it; carry on with the work in this session.", structured)
		r.Summary = "Staying here"
		return r
	}
	r = TextResult("The user closed the card without choosing. Do not start a session; carry on here if the work is still wanted.", structured)
	r.Summary = "Offer skipped"
	return r
}

// startedResult starts the session and reports it, with the fields frontends link to.
func startedResult(ctx context.Context, c SessionCoordinator, req SessionRequest, structured map[string]any) Result {
	s, err := c.StartSession(ctx, req)
	if err != nil {
		msg := fmt.Sprintf("the session could not be started: %v", err)
		r := ErrorResult("%s", msg)
		structured["error"] = msg
		r.Structured = structured
		r.Summary = "Could not start"
		return r
	}
	structured["session_id"], structured["name"], structured["title"], structured["cwd"] = s.ID, s.Name, s.Title, s.Cwd
	where := ""
	if s.WorktreePath != "" {
		structured["worktree_path"], structured["worktree_branch"] = s.WorktreePath, s.WorktreeBranch
		where = fmt.Sprintf(" It runs in a new worktree at %s (branch %s).", s.WorktreePath, s.WorktreeBranch)
	}
	r := TextResult(fmt.Sprintf("Session %q started (name %s, id %s) and is working on the prompt.%s You get a note with its latest report when it ends a turn; do not poll it, and do not do its task here. Reach it with SessionSend.",
		s.Title, s.Name, s.ID, where), structured)
	r.Summary = "Started " + s.Name
	return r
}

// ---- SessionHandoff ----

// SessionHandoff moves the task in progress to a fork of this session.
type SessionHandoff struct{}

func (*SessionHandoff) Name() string                  { return "SessionHandoff" }
func (*SessionHandoff) ReadOnly(json.RawMessage) bool { return false }

func (*SessionHandoff) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Title         string `json:"title"`
		RemainingPlan string `json:"remaining_plan"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	in.Title = strings.Join(strings.Fields(in.Title), " ")
	if in.Title == "" || strings.TrimSpace(in.RemainingPlan) == "" {
		return ErrorResult("InputValidationError: title and remaining_plan are required")
	}
	if len([]rune(in.Title)) > maxSessionTitle {
		return ErrorResult("InputValidationError: title is longer than %d characters", maxSessionTitle)
	}
	c := sessionCoordinator(env)
	if c == nil {
		return ErrorResult(noCoordinator)
	}
	s, err := c.StartSession(ctx, SessionRequest{ToolUseID: call.ID, Title: in.Title, Fork: true,
		Prompt: "This session is a fork that takes over a task from the session it was forked from, which has moved on to something else. The call that made the fork shows as interrupted; that is expected. Carry on from this plan:\n\n" + in.RemainingPlan})
	if err != nil {
		r := ErrorResult("the task could not be moved: %v", err)
		r.Summary = "Could not move"
		return r
	}
	r := TextResult(fmt.Sprintf("The task moved to session %q (name %s, id %s), which goes on with it. Stop working on it here, make no more changes for it, and answer what the user just asked.", s.Title, s.Name, s.ID),
		map[string]any{"session_id": s.ID, "name": s.Name, "title": s.Title, "cwd": s.Cwd})
	r.Summary = "Moved to " + s.Name
	return r
}

// ---- SessionDismiss ----

// SessionDismiss withdraws a proposal of this session the user has not started.
type SessionDismiss struct{}

func (*SessionDismiss) Name() string { return "SessionDismiss" }

// ReadOnly: it only takes back this session's own card.
func (*SessionDismiss) ReadOnly(json.RawMessage) bool { return true }

func (*SessionDismiss) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		TaskID string `json:"task_id"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.TaskID) == "" {
		return ErrorResult("InputValidationError: task_id is required")
	}
	c := sessionCoordinator(env)
	if c == nil {
		return ErrorResult(noCoordinator)
	}
	if err := c.DismissProposal(in.TaskID); err != nil {
		return ErrorResult("%v", err)
	}
	r := TextResult("The proposal is withdrawn.", map[string]any{"proposal_id": in.TaskID})
	r.Summary = "Withdrew the proposal"
	return r
}

// ---- SessionList ----

// SessionList lists the user's sessions.
type SessionList struct{}

func (*SessionList) Name() string                  { return "SessionList" }
func (*SessionList) ReadOnly(json.RawMessage) bool { return true }

func (*SessionList) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Linked bool `json:"linked"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	c := sessionCoordinator(env)
	if c == nil {
		return ErrorResult(noCoordinator)
	}
	list, err := c.ListSessions(ctx, in.Linked)
	if err != nil {
		return ErrorResult("could not list sessions: %v", err)
	}
	if len(list) == 0 {
		r := TextResult("No sessions.", map[string]any{"sessions": []SessionSummary{}})
		r.Summary = "No sessions"
		return r
	}
	var sb strings.Builder
	for _, s := range list {
		title := s.Title
		if r := []rune(title); len(r) > listedTitleChars {
			title = string(r[:listedTitleChars]) + "…"
		}
		fmt.Fprintf(&sb, "%s  %s  %q", s.ID, s.State, title)
		for _, f := range [][2]string{{"name", s.Name}, {"relation", s.Relation}, {"device", s.Device}} {
			if f[1] != "" {
				fmt.Fprintf(&sb, "  %s=%s", f[0], f[1])
			}
		}
		if s.Archived {
			sb.WriteString("  archived")
		}
		sb.WriteByte('\n')
	}
	r := TextResult(sb.String(), map[string]any{"sessions": list})
	r.Summary = fmt.Sprintf("%d session(s)", len(list))
	return r
}

// ---- SessionSend ----

// SessionSend delivers a message to another session. It arrives there as a notice from the system, never as a user
// message.
type SessionSend struct{}

func (*SessionSend) Name() string                  { return "SessionSend" }
func (*SessionSend) ReadOnly(json.RawMessage) bool { return false }

func (*SessionSend) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		To      string `json:"to"`
		Message string `json:"message"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.To) == "" || strings.TrimSpace(in.Message) == "" {
		return ErrorResult("InputValidationError: to and message are required")
	}
	c := sessionCoordinator(env)
	if c == nil {
		return ErrorResult(noCoordinator)
	}
	d, err := c.SendToSession(ctx, in.To, in.Message)
	if err != nil {
		return ErrorResult("%v", err)
	}
	text, summary := "Delivered: that session reads it with its next model request.", "Delivered"
	switch {
	case d.Held:
		text, summary = "Not delivered yet: the sessions have woken each other several times in a row with no user message, so the message waits for the next user turn. Stop here and tell the user where things stand.", "Held for the next user turn"
	case d.Woke:
		text, summary = "Delivered: that session was idle and started a turn to read it.", "Delivered, woke it"
	}
	r := TextResult(text, map[string]any{"session_id": d.ID, "name": d.Name, "title": d.Title, "woke": d.Woke, "held": d.Held})
	r.Summary = summary
	return r
}

// ---- SessionArchive ----

// SessionArchive archives a session started from this one. Every call asks the user.
type SessionArchive struct{}

func (*SessionArchive) Name() string                  { return "SessionArchive" }
func (*SessionArchive) ReadOnly(json.RawMessage) bool { return false }
func (*SessionArchive) AlwaysAsk() bool               { return true }

// PermissionHints names the session in the prompt: an id alone tells the user nothing.
func (*SessionArchive) PermissionHints(ctx context.Context, env *Env, call Call) PermissionHints {
	var in struct {
		ID string `json:"id"`
	}
	c := sessionCoordinator(env)
	if c == nil || json.Unmarshal(call.Input, &in) != nil {
		return PermissionHints{}
	}
	if d := c.DescribeSession(in.ID); d != "" {
		return PermissionHints{Sensitive: "Archive " + d + "? It stops the session and hides it from the session list."}
	}
	return PermissionHints{}
}

func (*SessionArchive) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		ID string `json:"id"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.ID) == "" {
		return ErrorResult("InputValidationError: id is required")
	}
	c := sessionCoordinator(env)
	if c == nil {
		return ErrorResult(noCoordinator)
	}
	title, err := c.ArchiveSession(ctx, in.ID)
	if err != nil {
		return ErrorResult("%v", err)
	}
	r := TextResult(fmt.Sprintf("Session %q is archived and stopped.", title), map[string]any{"session_id": in.ID, "title": title})
	r.Summary = "Archived"
	return r
}

// ---- MarkChapter ----

// MarkChapter marks where a new stretch of work starts. Frontends read the divider and the table of contents from the
// calls themselves, so chapters come back with the transcript on replay.
type MarkChapter struct{}

func (*MarkChapter) Name() string                  { return "MarkChapter" }
func (*MarkChapter) ReadOnly(json.RawMessage) bool { return true }

func (*MarkChapter) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return ErrorResult("InputValidationError: title is required")
	}
	if len([]rune(title)) > maxChapterTitle {
		return ErrorResult("InputValidationError: title is longer than %d characters", maxChapterTitle)
	}
	r := TextResult("Chapter marked.", nil)
	r.Summary = title
	return r
}
