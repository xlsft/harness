package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/worktree"
)

// Sessions started from a session (the SessionStart and SessionHandoff tools) and what passes between sessions.
//
// A started session has a short name and is linked to the session that started it; the links and the session's
// proposals are kept per session in HARNESS_HOME/session-links/<id>.json, on this machine only. A started session has
// the permission mode and rules of its parent, so starting one never gains anything; its brief is left out of the
// prompts the auto mode decider reads.
//
// Notes are the messages (SessionSend) and turn reports that reach a session. They arrive as reminders, not as user
// messages: they do not enter the message queue, so they can never become the answer to a question, and they are not
// prompts, so the auto mode decider does not read them as the user's wishes. A note wakes an idle session (Driver.Wake),
// a report only when none of the session's children is still working. After maxLinkedWakes wakes in a row without a
// user message notes wait, in memory, for the next user message to one of the two sessions.

const (
	// proposalTTL is how long a proposal waits for the user.
	proposalTTL = 7 * 24 * time.Hour
	// maxLinkedWakes is how many notes in a row may wake a session with no user message in between.
	maxLinkedWakes = 5
	// maxHeldNotes bounds the notes waiting for a user message; the oldest are dropped.
	maxHeldNotes = 20
	// sessionReportChars is how much of a session's latest answer its parent gets.
	sessionReportChars = 400
	// maxSessionMessage bounds a SessionSend message.
	maxSessionMessage = 16 << 10
	// maxSessionRows bounds a SessionList result.
	maxSessionRows = 100
)

// Note kinds (session.SessionNote.Kind), and the Notice kinds they are shown with.
const (
	noteMessage = "message"
	noteReport  = "report"
)

type linkedSession struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
}

type proposalRecord struct {
	ID          string    `json:"id"`
	ToolUseID   string    `json:"toolUseId"`
	Title       string    `json:"title"`
	Prompt      string    `json:"prompt"`
	Fork        bool      `json:"fork,omitempty"`
	Worktree    bool      `json:"worktree,omitempty"`
	Model       string    `json:"model,omitempty"`
	Cwd         string    `json:"cwd,omitempty"`
	State       string    `json:"state"`
	ExpiresAt   time.Time `json:"expiresAt"`
	SessionID   string    `json:"sessionId,omitempty"`
	SessionName string    `json:"sessionName,omitempty"`
}

// Proposal states. A start in progress is proposalStarting, on record only until it is done.
const (
	proposalPending   = "pending"
	proposalStarting  = "starting"
	proposalStarted   = "started"
	proposalDismissed = "dismissed"
)

// sessionLinks is the file of one session.
type sessionLinks struct {
	// Name is this session's short name and Parent the session that started it; empty for a session the user started.
	Name      string            `json:"name,omitempty"`
	Parent    string            `json:"parent,omitempty"`
	Children  []linkedSession   `json:"children,omitempty"`
	Proposals []*proposalRecord `json:"proposals,omitempty"`
}

// linkState is the links state of a live session.
type linkState struct {
	// origin is the request that opened the session: a started session inherits its permission rules. Set before the
	// session starts its first turn.
	origin *agentv1.CreateSessionRequest

	mu     sync.Mutex
	loaded bool
	file   sessionLinks
	// wakes counts the notes that woke the session since its last user message; held are the notes past the limit.
	wakes int
	held  []session.SessionNote
}

func sessionLinksPath(id string) string {
	return filepath.Join(config.Home(), "session-links", id+".json")
}

func readSessionLinks(id string) sessionLinks {
	var f sessionLinks
	if !validSessionID(id) {
		return f
	}
	if data, err := os.ReadFile(sessionLinksPath(id)); err == nil {
		_ = json.Unmarshal(data, &f)
	}
	return f
}

// withLinks runs fn on the session's links (links.mu held) and saves them when fn returns true.
func (ls *liveSession) withLinks(fn func(f *sessionLinks) bool) error {
	ls.links.mu.Lock()
	defer ls.links.mu.Unlock()
	if !ls.links.loaded {
		ls.links.file = readSessionLinks(ls.sess.ID)
		ls.links.loaded = true
	}
	if !fn(&ls.links.file) {
		return nil
	}
	data, err := json.MarshalIndent(&ls.links.file, "", "  ")
	if err != nil {
		return err
	}
	path := sessionLinksPath(ls.sess.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (ls *liveSession) readLinks() sessionLinks {
	var out sessionLinks
	_ = ls.withLinks(func(f *sessionLinks) bool {
		out = *f
		out.Children = slices.Clone(f.Children)
		return false
	})
	return out
}

func (s *Server) liveSession(id string) *liveSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[id]
}

// ---- Starting sessions ----

// StartSession implements tools.SessionCoordinator.
func (ls *liveSession) StartSession(ctx context.Context, r tools.SessionRequest) (tools.LinkedSession, error) {
	s := ls.srv
	m := &agentv1.CreateSessionRequest{Cwd: r.Cwd, Model: r.Model, PermissionMode: modeToProto(ls.sess.Permissions.Mode())}
	if m.Cwd == "" && !isNoFolder(ls.sess.ID, ls.sess.Cwd) {
		m.Cwd = ls.sess.Cwd
	}
	if m.Model == "" {
		m.Model = ls.sess.Agent.Model()
	}
	if o := ls.links.origin; o != nil {
		m.AllowedTools, m.DisallowedTools, m.AdditionalDirectories = o.GetAllowedTools(), o.GetDisallowedTools(), o.GetAdditionalDirectories()
	}
	if r.Fork {
		m.ResumeSessionId, m.Fork = ls.sess.ID, true
	}
	if r.Worktree {
		m.Worktree = &agentv1.WorktreeOptions{}
	}
	resp, err := s.CreateSession(ctx, connect.NewRequest(m))
	if err != nil {
		return tools.LinkedSession{}, err
	}
	id := resp.Msg.GetSession().GetId()
	child := s.liveSession(id)
	if child == nil {
		return tools.LinkedSession{}, fmt.Errorf("session %s closed right after it opened", id)
	}
	name := worktree.RandomName()
	if err := child.withLinks(func(f *sessionLinks) bool {
		f.Name, f.Parent = name, ls.sess.ID
		return true
	}); err != nil {
		return tools.LinkedSession{}, err
	}
	if err := ls.withLinks(func(f *sessionLinks) bool {
		f.Children = append(f.Children, linkedSession{ID: id, Name: name, Title: r.Title})
		return true
	}); err != nil {
		return tools.LinkedSession{}, err
	}
	if !r.Fork {
		// A fork records its parent itself; this makes a fresh session a sub-session of this one too.
		if err := child.setParent(ls.sess.ID); err != nil {
			s.log().Warn("session link: parent not recorded", "err", err)
		}
	}
	if err := child.setCustomTitle(r.Title); err != nil {
		s.log().Warn("session link: title not recorded", "err", err)
	}
	child.sess.Agent.ExcludePrompt(r.Prompt)
	if _, err := s.submitMessage(child, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: id, Uuid: uuid.NewString(),
		Content: pbconv.Blocks([]llm.ContentBlock{llm.TextBlock(r.Prompt)})})); err != nil {
		return tools.LinkedSession{}, err
	}
	child.publishInfo()
	out := tools.LinkedSession{ID: id, Name: name, Title: r.Title, Cwd: child.sess.Cwd}
	if wt := child.sess.Worktree; wt != nil {
		out.WorktreePath, out.WorktreeBranch = wt.Path, wt.Branch
	}
	return out, nil
}

// setParent records that the session was started from parent (see detachFromParent for the reverse).
func (ls *liveSession) setParent(parent string) error {
	if _, err := ls.sess.Transcript.Append(session.Entry{Type: session.TypeMeta, ForkedFrom: parent}); err != nil {
		return err
	}
	ls.mu.Lock()
	ls.sess.ParentSessionID = parent
	ls.mu.Unlock()
	ls.syncMeta(func(m *syncclient.SessionMeta) { m.ParentSessionID = parent })
	return nil
}

// ---- Proposals ----

func proposalToProto(p *proposalRecord) *agentv1.SessionProposal {
	out := &agentv1.SessionProposal{Id: p.ID, ToolUseId: p.ToolUseID, Title: p.Title, ExpiresAt: p.ExpiresAt.UTC().Format(time.RFC3339),
		SessionId: p.SessionID, SessionName: p.SessionName}
	switch {
	case p.State == proposalStarted:
		out.State = agentv1.SessionProposal_STATE_STARTED
	case p.State == proposalDismissed:
		out.State = agentv1.SessionProposal_STATE_DISMISSED
	case time.Now().After(p.ExpiresAt):
		out.State = agentv1.SessionProposal_STATE_EXPIRED
	default:
		out.State = agentv1.SessionProposal_STATE_PENDING
	}
	return out
}

func (ls *liveSession) publishProposal(p *agentv1.SessionProposal) {
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SessionProposal{SessionProposal: p}})
}

// publishProposals tells the frontends about the proposals on record (when the session opens).
func (ls *liveSession) publishProposals() {
	var list []*agentv1.SessionProposal
	_ = ls.withLinks(func(f *sessionLinks) bool {
		for _, p := range f.Proposals {
			list = append(list, proposalToProto(p))
		}
		return false
	})
	for _, p := range list {
		ls.publishProposal(p)
	}
}

// ProposeSession implements tools.SessionCoordinator.
func (ls *liveSession) ProposeSession(r tools.SessionRequest) (tools.SessionProposalInfo, error) {
	p := &proposalRecord{ID: newUUID("prop_"), ToolUseID: r.ToolUseID, Title: r.Title, Prompt: r.Prompt, Fork: r.Fork, Worktree: r.Worktree,
		Model: r.Model, Cwd: r.Cwd, State: proposalPending, ExpiresAt: time.Now().Add(proposalTTL)}
	if err := ls.withLinks(func(f *sessionLinks) bool {
		f.Proposals = append(f.Proposals, p)
		return true
	}); err != nil {
		return tools.SessionProposalInfo{}, err
	}
	ls.publishProposal(proposalToProto(p))
	return tools.SessionProposalInfo{ID: p.ID, ExpiresAt: p.ExpiresAt}, nil
}

// claimProposal moves a proposal that waits for the user to state; an unknown one is connect.CodeNotFound, a resolved or
// expired one connect.CodeFailedPrecondition.
func (ls *liveSession) claimProposal(id, state string) (*proposalRecord, error) {
	var out *proposalRecord
	var refused error
	err := ls.withLinks(func(f *sessionLinks) bool {
		i := slices.IndexFunc(f.Proposals, func(p *proposalRecord) bool { return p.ID == id })
		if i < 0 {
			refused = connect.NewError(connect.CodeNotFound, fmt.Errorf("no proposal %s in this session", id))
			return false
		}
		p := f.Proposals[i]
		switch {
		case p.State != proposalPending:
			refused = connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("proposal %s is already %s", id, p.State))
			return false
		case time.Now().After(p.ExpiresAt):
			refused = connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("proposal %s has expired", id))
			return false
		}
		p.State = state
		copied := *p
		out = &copied
		return state != proposalStarting
	})
	if refused != nil {
		return nil, refused
	}
	return out, err
}

// DismissProposal implements tools.SessionCoordinator.
func (ls *liveSession) DismissProposal(id string) error {
	p, err := ls.claimProposal(id, proposalDismissed)
	if err != nil {
		return err
	}
	ls.publishProposal(proposalToProto(p))
	return nil
}

// ResolveSessionProposal starts or dismisses a proposal: the user's click on its card.
func (s *Server) ResolveSessionProposal(ctx context.Context, req *connect.Request[agentv1.ResolveSessionProposalRequest]) (*connect.Response[agentv1.ResolveSessionProposalResponse], error) {
	if info := s.remoteInfo(req.Msg.GetSessionId()); info != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the proposal belongs to a session on %s; start it there", deviceLabel(info)))
	}
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	id := req.Msg.GetProposalId()
	switch req.Msg.GetAction() {
	case agentv1.ResolveSessionProposalRequest_ACTION_DISMISS:
		p, err := ls.claimProposal(id, proposalDismissed)
		if err != nil {
			return nil, err
		}
		pb := proposalToProto(p)
		ls.publishProposal(pb)
		return connect.NewResponse(&agentv1.ResolveSessionProposalResponse{Proposal: pb}), nil
	case agentv1.ResolveSessionProposalRequest_ACTION_START:
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("action is required"))
	}
	p, err := ls.claimProposal(id, proposalStarting)
	if err != nil {
		return nil, err
	}
	started, err := ls.StartSession(ctx, tools.SessionRequest{ToolUseID: p.ToolUseID, Title: p.Title, Prompt: p.Prompt, Fork: p.Fork, Worktree: p.Worktree, Model: p.Model, Cwd: p.Cwd})
	var done *proposalRecord
	saveErr := ls.withLinks(func(f *sessionLinks) bool {
		for _, rec := range f.Proposals {
			if rec.ID == id {
				rec.State = proposalPending
				if err == nil {
					rec.State, rec.SessionID, rec.SessionName = proposalStarted, started.ID, started.Name
				}
				copied := *rec
				done = &copied
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if saveErr != nil {
		s.log().Warn("session link: proposal state not saved", "err", saveErr)
	}
	pb := proposalToProto(done)
	ls.publishProposal(pb)
	return connect.NewResponse(&agentv1.ResolveSessionProposalResponse{Proposal: pb}), nil
}

// ---- Finding sessions ----

// linkedIDs are the sessions linked to this one: its parent and the sessions started from it.
func (ls *liveSession) linkedIDs() []string {
	f := ls.readLinks()
	var ids []string
	if f.Parent != "" {
		ids = append(ids, f.Parent)
	}
	for _, c := range f.Children {
		ids = append(ids, c.ID)
	}
	return ids
}

// resolveSession turns a session id or the short name of a linked session into an id.
func (ls *liveSession) resolveSession(to string) (string, error) {
	to = strings.TrimSpace(to)
	if validSessionID(to) {
		return to, nil
	}
	f := ls.readLinks()
	for _, c := range f.Children {
		if c.Name == to {
			return c.ID, nil
		}
	}
	if f.Parent != "" && readSessionLinks(f.Parent).Name == to {
		return f.Parent, nil
	}
	return "", fmt.Errorf("no session named %q is linked to this one; use an id from SessionList", to)
}

var sessionStateNames = map[agentv1.SessionStatus]string{
	agentv1.SessionStatus_SESSION_STATUS_IDLE:                   "idle",
	agentv1.SessionStatus_SESSION_STATUS_WORKING:                "working",
	agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT:            "needs_input",
	agentv1.SessionStatus_SESSION_STATUS_WAITING_FOR_CONNECTION: "waiting_for_connection",
	agentv1.SessionStatus_SESSION_STATUS_ERROR:                  "error",
}

// ListSessions implements tools.SessionCoordinator: what AgentService.ListSessions gives the user's frontends, so it
// holds this user's sessions only, and sessions of other machines are marked as such.
func (ls *liveSession) ListSessions(ctx context.Context, linked bool) ([]tools.SessionSummary, error) {
	s := ls.srv
	resp, err := s.ListSessions(ctx, connect.NewRequest(&agentv1.ListSessionsRequest{IncludeArchived: linked}))
	if err != nil {
		return nil, err
	}
	f := ls.readLinks()
	relation := map[string]string{ls.sess.ID: "self"}
	for _, c := range f.Children {
		relation[c.ID] = "child"
	}
	if f.Parent != "" {
		relation[f.Parent] = "parent"
	}
	var out []tools.SessionSummary
	seen := map[string]bool{}
	add := func(info *agentv1.SessionInfo) {
		seen[info.GetId()] = true
		rel := relation[info.GetId()]
		if linked && (rel == "" || rel == "self") {
			return
		}
		row := tools.SessionSummary{ID: info.GetId(), Title: info.GetTitle(), Relation: rel, Cwd: info.GetCwd(), Archived: info.GetArchived()}
		if row.Title == "" {
			row.Title = "(untitled)"
		}
		switch {
		case info.GetRemote():
			row.State, row.Device = "remote", deviceLabel(info)
		case s.liveSession(info.GetId()) == nil && s.acpLive(info.GetId()) == nil:
			row.State = "closed"
		default:
			row.State = sessionStateNames[info.GetStatus()]
		}
		if !info.GetRemote() {
			row.Name = readSessionLinks(info.GetId()).Name
		}
		out = append(out, row)
	}
	for _, info := range resp.Msg.GetSessions() {
		add(info)
	}
	// A linked session older than the recent ones ListSessions reads.
	if linked {
		for _, id := range ls.linkedIDs() {
			if _, path := s.locate(id); !seen[id] && path != "" {
				info := fileInfo(path)
				s.applyState(info)
				add(info)
			}
		}
	}
	// Linked sessions first: they are what the model most likely looks for.
	slices.SortStableFunc(out, func(a, b tools.SessionSummary) int {
		rank := func(r tools.SessionSummary) int {
			if r.Relation == "parent" || r.Relation == "child" {
				return 0
			}
			return 1
		}
		return rank(a) - rank(b)
	})
	if len(out) > maxSessionRows {
		out = out[:maxSessionRows]
	}
	return out, nil
}

// ---- Notes ----

// noteTags are the tags a note must not be able to close or forge.
var noteTags = regexp.MustCompile(`(?i)<(/?\s*(?:system-reminder|session_message|session_report))`)

func neutralizeNote(text string) string { return noteTags.ReplaceAllString(text, "&lt;$1") }

// SessionNoteNotice is the JSON in Notice.text of kinds "session_message" and "session_report": Text is the line a
// frontend without a row for them shows.
type SessionNoteNotice struct {
	session.SessionNote
	Line string `json:"line"`
}

func noteLabel(n session.SessionNote) string {
	switch {
	case n.FromName != "":
		return n.FromName
	case n.FromTitle != "":
		return n.FromTitle
	}
	return n.FromID
}

func sessionNoteEvent(n session.SessionNote) *agentv1.SessionEvent {
	kind, line := "session_message", "Message from session "+noteLabel(n)+": "+n.Text
	if n.Kind == noteReport {
		kind, line = "session_report", "Session "+noteLabel(n)+" ended a turn ("+n.Status+"): "+n.Text
	}
	text := line
	if b, err := json.Marshal(SessionNoteNotice{SessionNote: n, Line: line}); err == nil {
		text = string(b)
	}
	return &agentv1.SessionEvent{Event: &agentv1.SessionEvent_Notice{Notice: &agentv1.Notice{Level: agentv1.Notice_LEVEL_INFO, Kind: kind, Text: text}}}
}

// recordNote shows the note to the user, keeps it in the transcript for replays, and queues it for the model.
func (ls *liveSession) recordNote(n session.SessionNote) {
	ls.publish(sessionNoteEvent(n))
	if _, err := ls.sess.Transcript.Append(session.Entry{Type: session.TypeSystem, Subtype: session.SubtypeSessionNote, Level: "info", Content: n.Text, SessionNote: &n}); err != nil {
		ls.srv.log().Warn("transcript write failed", "err", err)
	}
	title := n.FromTitle
	if title == "" {
		title = "untitled"
	}
	text := neutralizeNote(n.Text)
	if n.Kind == noteReport {
		ls.sess.Agent.Remind(agent.ReminderSessionReport, map[string]string{"session_id": n.FromID, "name": noteLabel(n), "title": title, "status": n.Status, "report": text})
		return
	}
	ls.sess.Agent.Remind(agent.ReminderSessionMessage, map[string]string{"from_id": n.FromID, "from_name": noteLabel(n), "from_title": title, "message": text})
}

// childWorking reports whether a session started from this one is working now.
func (ls *liveSession) childWorking() bool {
	for _, c := range ls.readLinks().Children {
		if o := ls.srv.liveSession(c.ID); o != nil && o.working() {
			return true
		}
	}
	return false
}

func (ls *liveSession) working() bool {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.statusLocked() == agentv1.SessionStatus_SESSION_STATUS_WORKING
}

// receiveNote delivers a note, or holds it past the wake limit. A message counts against the limit whether or not it
// wakes the session (two running turns could otherwise feed each other forever); a report counts when it wakes it.
func (ls *liveSession) receiveNote(n session.SessionNote) (woke, held bool) {
	busy := ls.driver.Busy()
	wakes := n.Kind == noteMessage || !busy
	ls.links.mu.Lock()
	if wakes && ls.links.wakes >= maxLinkedWakes {
		ls.links.held = append(ls.links.held, n)
		if len(ls.links.held) > maxHeldNotes {
			ls.links.held = slices.Clone(ls.links.held[len(ls.links.held)-maxHeldNotes:])
		}
		ls.links.mu.Unlock()
		return false, true
	}
	if n.Kind == noteMessage {
		ls.links.wakes++
	}
	ls.links.mu.Unlock()
	ls.recordNote(n)
	if n.Kind == noteReport && ls.childWorking() {
		return false, false
	}
	if busy {
		ls.driver.WakeAfterTurn()
		return false, false
	}
	if n.Kind == noteReport {
		ls.links.mu.Lock()
		ls.links.wakes++
		ls.links.mu.Unlock()
	}
	ls.driver.Wake()
	return true, false
}

// userSpoke is called for every user message: the wake count starts over here and on the linked sessions, and the
// notes that waited go out. Here they ride on the turn the message starts; a linked session is woken for them.
func (ls *liveSession) userSpoke() {
	ls.releaseNotes(false)
	for _, id := range ls.linkedIDs() {
		if o := ls.srv.liveSession(id); o != nil {
			o.releaseNotes(true)
		}
	}
}

func (ls *liveSession) releaseNotes(wake bool) {
	ls.links.mu.Lock()
	ls.links.wakes = 0
	held := ls.links.held
	ls.links.held = nil
	if len(held) > 0 && wake {
		ls.links.wakes = 1
	}
	ls.links.mu.Unlock()
	for _, n := range held {
		ls.recordNote(n)
	}
	if len(held) > 0 && wake && !ls.childWorking() {
		ls.driver.Wake()
	}
}

// reportToParent gives the parent a note with the latest report when this session ends a turn.
func (ls *liveSession) reportToParent(res agent.TurnResult) {
	f := ls.readLinks()
	if f.Parent == "" || ls.srv.ctx.Err() != nil {
		return
	}
	ls.mu.Lock()
	detached := ls.sess.ParentSessionID != f.Parent
	title := ls.title
	ls.mu.Unlock()
	parent := ls.srv.liveSession(f.Parent)
	if detached || parent == nil {
		return
	}
	status := "finished"
	report := res.Result
	switch {
	case res.Interrupted:
		status = "interrupted"
	case res.Subtype != agent.SubtypeSuccess:
		status = "failed"
		if report == "" {
			report = strings.Join(res.Errors, "; ")
		}
	}
	if strings.TrimSpace(report) == "" {
		report = "(no text)"
	}
	parent.receiveNote(session.SessionNote{Kind: noteReport, FromID: ls.sess.ID, FromName: f.Name, FromTitle: title, Status: status, Text: truncateRunes(report, sessionReportChars)})
}

// SendToSession implements tools.SessionCoordinator.
func (ls *liveSession) SendToSession(ctx context.Context, to, message string) (tools.SessionDelivery, error) {
	s := ls.srv
	var none tools.SessionDelivery
	if len(message) > maxSessionMessage {
		return none, fmt.Errorf("the message is longer than %d bytes; put the details in a file and send its path", maxSessionMessage)
	}
	id, err := ls.resolveSession(to)
	if err != nil {
		return none, err
	}
	if id == ls.sess.ID {
		return none, errors.New("that is this session")
	}
	target, err := s.openForNote(ctx, id)
	if err != nil {
		return none, err
	}
	f := ls.readLinks()
	ls.mu.Lock()
	title := ls.title
	ls.mu.Unlock()
	woke, held := target.receiveNote(session.SessionNote{Kind: noteMessage, FromID: ls.sess.ID, FromName: f.Name, FromTitle: title, Text: message})
	info := target.info()
	return tools.SessionDelivery{ID: id, Name: target.readLinks().Name, Title: info.GetTitle(), Woke: woke, Held: held}, nil
}

// openForNote returns the live session id, reopening it if it is closed. What cannot take a note is refused: a session
// of another machine (the relay carries user messages only, and a note must never read as one), an ACP session (the
// external agent has no such channel) and an archived one.
func (s *Server) openForNote(ctx context.Context, id string) (*liveSession, error) {
	if ls := s.liveSession(id); ls != nil {
		return ls, nil
	}
	if s.acpLive(id) != nil {
		return nil, fmt.Errorf("session %s runs on an external agent, which cannot receive messages from other sessions", id)
	}
	_, path := s.locate(id)
	if path == "" {
		s.refreshListing(ctx, "")
		if info := s.remoteInfo(id); info != nil {
			return nil, fmt.Errorf("session %s runs on %s and cannot be messaged from here: the relay only carries the user's own messages, and this one must not read as coming from the user", id, deviceLabel(info))
		}
		return nil, fmt.Errorf("session %s not found", id)
	}
	if s.stateOf(id).archived {
		return nil, fmt.Errorf("session %s is archived", id)
	}
	if _, err := s.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: id})); err != nil {
		return nil, err
	}
	ls := s.liveSession(id)
	if ls == nil {
		return nil, fmt.Errorf("session %s closed right after it opened", id)
	}
	return ls, nil
}

// ---- Archiving ----

// startedChild returns the session started from this one that `to` names.
func (ls *liveSession) startedChild(to string) (linkedSession, error) {
	id, err := ls.resolveSession(to)
	if err != nil {
		return linkedSession{}, err
	}
	for _, c := range ls.readLinks().Children {
		if c.ID == id {
			return c, nil
		}
	}
	return linkedSession{}, fmt.Errorf("session %s was not started from this one; only those can be archived from here", id)
}

// DescribeSession implements tools.SessionCoordinator.
func (ls *liveSession) DescribeSession(to string) string {
	c, err := ls.startedChild(to)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("session %q (%s)", c.Title, c.Name)
}

// ArchiveSession implements tools.SessionCoordinator; the permission engine has asked the user by now (the tool always
// asks). It is the same archive as the session menu's: the state lives on the harness server, and the session stops.
func (ls *liveSession) ArchiveSession(ctx context.Context, to string) (string, error) {
	c, err := ls.startedChild(to)
	if err != nil {
		return "", err
	}
	archived := true
	if _, err := ls.srv.UpdateSession(ctx, connect.NewRequest(&agentv1.UpdateSessionRequest{SessionId: c.ID, Archived: &archived})); err != nil {
		return "", err
	}
	return c.Title, nil
}
