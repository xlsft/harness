// Package session persists conversations as append-only JSONL transcripts
// compatible in shape with Claude Code
// (~/.harness/projects/<cwd-slug>/<session-id>.jsonl).
package session

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/usagelimit"
)

// Entry types.
const (
	TypeUser      = "user"
	TypeAssistant = "assistant"
	TypeSystem    = "system"
	// TypeMeta is a bookkeeping record (no uuid) holding session metadata such
	// as the frozen prompt bundle version.
	TypeMeta = "harness-meta"
	// TypeFileHistorySnapshot points at the file backups of a checkpoint
	// (no uuid, Claude Code shape).
	TypeFileHistorySnapshot = "file-history-snapshot"
	// TypeAITitle holds a model-generated session title (aiTitle, Claude
	// Code shape, no uuid). The last one wins.
	TypeAITitle = "ai-title"
	// TypeCustomTitle holds a title the user set (customTitle, no uuid). It
	// always wins over an AI title.
	TypeCustomTitle = "custom-title"
	// TypeToolTitle holds a model-generated label of a tool call (toolUseId,
	// toolTitle; harness only, no uuid). Replays show it on that call.
	TypeToolTitle = "tool-title"
)

// System entry subtypes written by the client.
const (
	SubtypeCompactBoundary = "compact_boundary"
	// SubtypeRewind marks a conversation rewind: the entry branches from the
	// message before the rewound turn, so it becomes the new leaf.
	SubtypeRewind = "rewind"
	// SubtypeUserShell records a command the user ran in the composer's shell mode (userShell holds it); it is
	// shown in replays. What the model saw is the user_shell_command reminder, a meta user entry.
	SubtypeUserShell = "user_shell_command"
	// SubtypeSessionNote records a message or report from another session (sessionNote holds it), replayed as a notice.
	// The model got it as a reminder.
	SubtypeSessionNote = "session_note"
	// SubtypeUsageLimit records a usage-limit state of the session's provider (usageLimit holds it), replayed
	// as SessionEvent.usage_limit.
	SubtypeUsageLimit = "usage_limit"
	// SubtypeSessionMoved records that the running session moved to another folder or branch (content says where);
	// replays show it as a notice.
	SubtypeSessionMoved = "session_moved"
	// SubtypeHookRun records a finished hook handler (hookRun holds it), replayed as SessionEvent.hook_run.
	SubtypeHookRun = "hook_run"
	// SubtypeTodoLink records that a subagent or background task was linked to a todo (todoLink holds it). TodoWrite
	// inputs carry no links, so a resume folds these entries over them (TodoLinkFold).
	SubtypeTodoLink = "todo_link"
)

// HookRun is one handler of one hook event at work.
type HookRun struct {
	ID string `json:"id"`
	// Event is the event name; Type "command" or "http"; Command the command line or URL; Source the settings level.
	Event   string `json:"event"`
	Type    string `json:"type"`
	Command string `json:"command"`
	Matcher string `json:"matcher,omitempty"`
	Source  string `json:"source,omitempty"`
	// Status: "running" while it runs, then "ok", "blocked", "error", "timed_out" or "canceled".
	Status   string `json:"status"`
	ExitCode *int   `json:"exitCode,omitempty"`
	Output   string `json:"output,omitempty"`
	Decision string `json:"decision,omitempty"`
	// Duration is in milliseconds.
	Duration  int64  `json:"durationMs,omitempty"`
	ToolName  string `json:"toolName,omitempty"`
	ToolUseID string `json:"toolUseId,omitempty"`
	AgentID   string `json:"agentId,omitempty"`
	Async     bool   `json:"async,omitempty"`
}

// unchained entry types carry no uuid/parentUuid.
func unchained(t string) bool {
	return t == TypeMeta || t == TypeFileHistorySnapshot || t == TypeAITitle || t == TypeCustomTitle || t == TypeToolTitle
}

// CompactMetadata is attached to compact_boundary entries.
type CompactMetadata struct {
	Trigger   string `json:"trigger"` // manual | auto | rewind
	PreTokens int    `json:"preTokens"`
}

// FileBackup is one file of a file-history snapshot.
type FileBackup struct {
	// BackupFileName is nil when the file did not exist at that point.
	BackupFileName *string `json:"backupFileName"`
	Version        int     `json:"version"`
	BackupTime     string  `json:"backupTime"`
}

// FileSnapshot is the payload of a file-history-snapshot entry.
type FileSnapshot struct {
	MessageID          string                `json:"messageId"`
	TrackedFileBackups map[string]FileBackup `json:"trackedFileBackups"`
	Timestamp          string                `json:"timestamp"`
}

// Message is the persisted message. Content is kept raw so user prompts can be
// stored as a plain string (as Claude Code does) and blocks as an array.
type Message struct {
	ID           string          `json:"id,omitempty"`
	Type         string          `json:"type,omitempty"` // "message" for assistant
	Role         string          `json:"role"`
	Model        string          `json:"model,omitempty"`
	Content      json.RawMessage `json:"content"`
	StopReason   *string         `json:"stop_reason,omitempty"`
	StopSequence *string         `json:"stop_sequence,omitempty"`
	Usage        *llm.Usage      `json:"usage,omitempty"`
}

// Entry is one JSONL line.
type Entry struct {
	ParentUUID  *string `json:"parentUuid"`
	IsSidechain bool    `json:"isSidechain"`
	AgentID     string  `json:"agentId,omitempty"`
	// Subagent is a record of a subagent's state on a harness-meta entry (tools.AgentInfo as JSON): what another
	// machine lists the session's agents from.
	Subagent json.RawMessage `json:"subagent,omitempty"`
	// Workflow is the same for a run of the Workflow tool (tools.WorkflowInfo as JSON).
	Workflow   json.RawMessage `json:"workflow,omitempty"`
	UserType   string          `json:"userType,omitempty"`
	Cwd        string          `json:"cwd,omitempty"`
	SessionID  string          `json:"sessionId"`
	Version    string          `json:"version,omitempty"`
	GitBranch  string          `json:"gitBranch,omitempty"`
	Entrypoint string          `json:"entrypoint,omitempty"`
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype,omitempty"`
	Message    *Message        `json:"message,omitempty"`
	Content    string          `json:"content,omitempty"`
	Level      string          `json:"level,omitempty"`
	IsMeta     bool            `json:"isMeta,omitempty"`
	// Skill marks the meta entry holding the text of a skill that started, or came back after a compaction.
	Skill                   *SkillUse `json:"skillUse,omitempty"`
	PromptID                string    `json:"promptId,omitempty"`
	PermissionMode          string    `json:"permissionMode,omitempty"`
	RequestID               string    `json:"requestId,omitempty"`
	ToolUseResult           any       `json:"toolUseResult,omitempty"`
	SourceToolAssistantUUID string    `json:"sourceToolAssistantUUID,omitempty"`
	IsAPIErrorMessage       bool      `json:"isApiErrorMessage,omitempty"`
	UUID                    string    `json:"uuid,omitempty"`
	Timestamp               string    `json:"timestamp"`

	// ClientUUIDs are the frontend ids (SendUserMessageRequest.uuid) of the
	// messages a user entry carries: one for a prompt, one per message for
	// queued messages injected into a turn together.
	ClientUUIDs []string `json:"clientUuids,omitempty"`

	// compaction
	LogicalParentUUID         string           `json:"logicalParentUuid,omitempty"`
	CompactMetadata           *CompactMetadata `json:"compactMetadata,omitempty"`
	IsCompactSummary          bool             `json:"isCompactSummary,omitempty"`
	IsVisibleInTranscriptOnly bool             `json:"isVisibleInTranscriptOnly,omitempty"`

	// file-history-snapshot fields
	MessageID        string        `json:"messageId,omitempty"`
	Snapshot         *FileSnapshot `json:"snapshot,omitempty"`
	IsSnapshotUpdate bool          `json:"isSnapshotUpdate,omitempty"`

	// harness-meta fields
	PromptBundleVersion string `json:"promptBundleVersion,omitempty"`
	Model               string `json:"model,omitempty"`
	ForkedFrom          string `json:"forkedFrom,omitempty"`
	// DetachedFromParent clears an earlier forkedFrom: the session is no
	// longer listed as a sub-session of its parent.
	DetachedFromParent bool `json:"detachedFromParent,omitempty"`
	// OutputStyle is the active output style id or name ("default" when
	// cleared); the last one on record wins on resume.
	OutputStyle string `json:"outputStyle,omitempty"`
	// McpCatalogVersion is the MCP tool catalog snapshot the session uses
	// (frozen per session; the last one on record wins on resume).
	McpCatalogVersion string `json:"mcpCatalogVersion,omitempty"`
	// Worktree is the git worktree the session runs in (written when the client creates it or the session moves;
	// the last one on record wins). A record without a path means the session left its worktree.
	Worktree *Worktree `json:"worktree,omitempty"`
	// AllowedDirs are the working directories the session gained while it ran: folders the user allowed and
	// folders it moved away from (the whole list; the last one on record wins on resume).
	AllowedDirs []string `json:"allowedDirs,omitempty"`
	// Moved marks the record of a move the session made itself, in cwd: it resumes there. AllowedDirs of such a record
	// is complete even when empty.
	Moved bool `json:"moved,omitempty"`
	// RemoteControl is the session's remote control switch (SetSessionRelay); the last one on record wins on
	// resume, and without one the settings decide.
	RemoteControl *bool `json:"remoteControl,omitempty"`
	// AutoContinue is the session's "continue when limits return" switch (SetAutoContinue); the last one on record
	// wins on resume.
	AutoContinue *bool `json:"autoContinue,omitempty"`
	// HeldMessages are the user messages the usage limit holds, in order (the whole list; empty when it was
	// emptied). The last one on record is held again on resume.
	HeldMessages *[]HeldMessage `json:"heldMessages,omitempty"`

	// AutoDecision is set on a tool_result entry when the auto permission
	// mode decided (or deferred to the user on) that tool call.
	AutoDecision *AutoDecision `json:"autoDecision,omitempty"`

	// SessionNote is set on SubtypeSessionNote system entries.
	SessionNote *SessionNote `json:"sessionNote,omitempty"`

	// UserShell is set on SubtypeUserShell system entries.
	UserShell *UserShell `json:"userShell,omitempty"`

	// UsageLimit is set on SubtypeUsageLimit system entries.
	UsageLimit *usagelimit.Status `json:"usageLimit,omitempty"`

	// HookRun is set on SubtypeHookRun system entries.
	HookRun *HookRun `json:"hookRun,omitempty"`

	// TodoLink is set on SubtypeTodoLink system entries.
	TodoLink *TodoLink `json:"todoLink,omitempty"`

	// ai-title / custom-title fields
	AITitle     string `json:"aiTitle,omitempty"`
	CustomTitle string `json:"customTitle,omitempty"`

	// tool-title fields
	ToolUseID string `json:"toolUseId,omitempty"`
	ToolTitle string `json:"toolTitle,omitempty"`
}

// Titles are the session titles on record in a transcript (the last entry
// of each kind wins).
type Titles struct {
	Custom string
	AI     string
	// FirstPrompt is the text of the first user prompt (not meta, not a
	// tool result, not a compaction summary), untrimmed.
	FirstPrompt string
}

// Best returns the title to show: the custom title, else the AI title,
// else "".
func (t Titles) Best() string {
	if t.Custom != "" {
		return t.Custom
	}
	return t.AI
}

// ReadTitles extracts the recorded titles from entries.
func ReadTitles(entries []Entry) Titles {
	var t Titles
	for i := range entries {
		e := &entries[i]
		switch e.Type {
		case TypeCustomTitle:
			if s := strings.TrimSpace(e.CustomTitle); s != "" {
				t.Custom = s
			}
		case TypeAITitle:
			if s := strings.TrimSpace(e.AITitle); s != "" {
				t.AI = s
			}
		case TypeUser:
			if t.FirstPrompt != "" || e.IsMeta || e.IsCompactSummary || e.Message == nil || e.ToolUseResult != nil {
				continue
			}
			for _, b := range e.Blocks() {
				if b.Type == llm.BlockText && strings.TrimSpace(b.Text) != "" {
					t.FirstPrompt = b.Text
					break
				}
			}
		}
	}
	return t
}

// ReadRemoteControl returns the last remote control choice on record (harness-meta remoteControl), nil when the
// session never had one.
func ReadRemoteControl(entries []Entry) *bool {
	var on *bool
	for i := range entries {
		if e := &entries[i]; e.Type == TypeMeta && e.RemoteControl != nil {
			v := *e.RemoteControl
			on = &v
		}
	}
	return on
}

// ReadAutoContinue returns the last "continue when limits return" choice on record (off when none).
func ReadAutoContinue(entries []Entry) bool {
	on := false
	for i := range entries {
		if e := &entries[i]; e.Type == TypeMeta && e.AutoContinue != nil {
			on = *e.AutoContinue
		}
	}
	return on
}

// HeldMessage is a user message waiting for the usage limit to return.
type HeldMessage struct {
	ID       string             `json:"id"`
	Content  []llm.ContentBlock `json:"content"`
	QueuedAt string             `json:"queuedAt,omitempty"`
}

// ReadHeldMessages returns the held messages on record.
func ReadHeldMessages(entries []Entry) []HeldMessage {
	var held []HeldMessage
	for i := range entries {
		if e := &entries[i]; e.Type == TypeMeta && e.HeldMessages != nil {
			held = *e.HeldMessages
		}
	}
	return held
}

// SessionNote is a message or a turn report from another session.
type SessionNote struct {
	// ID makes delivery idempotent across routes and retries.
	ID string `json:"id,omitempty"`
	// Kind is "message" or "report".
	Kind      string `json:"kind"`
	FromID    string `json:"fromId"`
	FromName  string `json:"fromName,omitempty"`
	FromTitle string `json:"fromTitle,omitempty"`
	// Status is how the turn ended (reports).
	Status string `json:"status,omitempty"`
	Text   string `json:"text"`
}

// Worktree describes a git worktree the client created for a session.
type Worktree struct {
	// Path is the absolute path of the worktree checkout.
	Path string `json:"path"`
	// Branch is the branch checked out in the worktree.
	Branch string `json:"branch"`
	// BaseRef is the ref the branch was created from (a branch name or a
	// commit).
	BaseRef string `json:"baseRef,omitempty"`
	// RepositoryPath is the main checkout of the repository.
	RepositoryPath string `json:"repositoryPath"`
	// OriginalCwd is the folder the session entered the worktree from, where leaving it goes back to ("" for a
	// session created in the worktree: the same subdirectory of the main checkout).
	OriginalCwd string `json:"originalCwd,omitempty"`
}

// UserShell is a finished user shell command (composer shell mode). Stdout and stderr are bounded (head and
// tail kept); Truncated says so.
type UserShell struct {
	ID         string `json:"id"`
	Command    string `json:"command"`
	Cwd        string `json:"cwd"`
	ExitCode   int    `json:"exitCode"`
	Reason     string `json:"reason"` // exited | stopped | time_limit | session_closed | failed
	DurationMs int64  `json:"durationMs"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	Error      string `json:"error,omitempty"`
}

// AutoDecision records an auto-mode verdict on a tool call.
type AutoDecision struct {
	ToolUseID string `json:"toolUseId,omitempty"`
	ToolName  string `json:"toolName,omitempty"`
	// Decision is "allow", "deny" or "ask" (deferred to the user, or denied
	// for lack of a user in non-interactive sessions).
	Decision string `json:"decision"`
	// Provider is "rules", "llm", "http:<id>", "local-rules" or "none".
	Provider   string   `json:"provider"`
	Reason     string   `json:"reason,omitempty"`
	Categories []string `json:"categories,omitempty"`
	Cached     bool     `json:"cached,omitempty"`
	Confidence float64  `json:"confidence,omitempty"`
	LatencyMs  int64    `json:"latencyMs,omitempty"`
	CostUSD    float64  `json:"costUSD,omitempty"`
}

// Blocks decodes the message content.
func (e *Entry) Blocks() []llm.ContentBlock {
	if e.Message == nil {
		return nil
	}
	b, _ := llm.DecodeContent(e.Message.Content)
	return b
}

// Slug converts a cwd to a project directory name: every non-alphanumeric
// character becomes '-'; names over 200 chars are cut and suffixed with a hash.
func Slug(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	s := b.String()
	if len(s) > 200 {
		h := sha256.Sum256([]byte(cwd))
		s = s[:200] + "-" + hex.EncodeToString(h[:])[:16]
	}
	return s
}

// NewID returns a new session id (UUID v4, as Claude Code).
func NewID() string { return uuid.NewString() }

// Store locates transcripts under a projects root.
type Store struct {
	Root string // e.g. ~/.harness/projects
}

// ProjectDir returns the directory for cwd.
func (s Store) ProjectDir(cwd string) string { return filepath.Join(s.Root, Slug(cwd)) }

// Path returns the transcript path of a session for cwd.
func (s Store) Path(cwd, id string) string {
	return filepath.Join(s.ProjectDir(cwd), id+".jsonl")
}

// Latest returns the most recently modified session id for cwd.
func (s Store) Latest(cwd string) (string, error) {
	files, _ := filepath.Glob(filepath.Join(s.ProjectDir(cwd), "*.jsonl"))
	if len(files) == 0 {
		return "", errors.New("no previous session found in this directory")
	}
	sort.Slice(files, func(i, j int) bool {
		a, _ := os.Stat(files[i])
		b, _ := os.Stat(files[j])
		return a.ModTime().After(b.ModTime())
	})
	return strings.TrimSuffix(filepath.Base(files[0]), ".jsonl"), nil
}

// Find returns the transcript path for id, preferring cwd's project dir and
// falling back to any project.
func (s Store) Find(cwd, id string) (string, error) {
	p := s.Path(cwd, id)
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	matches, _ := filepath.Glob(filepath.Join(s.Root, "*", id+".jsonl"))
	if len(matches) > 0 {
		return matches[0], nil
	}
	return "", fmt.Errorf("session %s not found", id)
}

// Load reads all entries of a transcript. Malformed lines are skipped.
func Load(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 256*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Conversation is the state rebuilt from a transcript.
type Conversation struct {
	Messages []llm.Message
	// ContextInjected is true when the first user message starts with the
	// project-context meta block.
	ContextInjected     bool
	LastUUID            string
	PromptBundleVersion string
	Model               string
	ForkedFrom          string
	// OutputStyle is the last recorded output style ("" when none was ever set).
	OutputStyle string
	// McpCatalogVersion is the last recorded MCP tool catalog snapshot.
	McpCatalogVersion string
	// Worktree is the last recorded worktree (nil when the session does not
	// run in one).
	Worktree *Worktree
	// AllowedDirs is the last recorded list of working directories gained while running.
	AllowedDirs []string
	// Todos is the input of the last TodoWrite call on the active branch
	// (followed across compactions); nil when there is none.
	Todos json.RawMessage
	// TodoLinks are the links on record for the items of Todos.
	TodoLinks []TodoLink
	// ActiveSkills are the skills started on the active branch, oldest first, with the text the model read: a compaction
	// sends the latest ones again.
	ActiveSkills []ActiveSkill
	// Findings is the structured result of the last ReportFindings call on the active branch.
	Findings json.RawMessage
	// Snapshots are the file-history snapshots (last record per message id),
	// in creation order.
	Snapshots []FileSnapshot
	// Chain holds the uuids of the active branch, followed across compaction
	// boundaries (oldest first).
	Chain []string
}

// ActiveChain returns the entries of the active branch: from the last
// chained entry back through parentUuid links, oldest first. The walk stops
// at a compact boundary (parentUuid null); with logical set it continues
// through logicalParentUuid.
func ActiveChain(entries []Entry, logical bool) []Entry {
	byUUID := map[string]int{}
	leaf := -1
	for i, e := range entries {
		if unchained(e.Type) || e.UUID == "" {
			continue
		}
		byUUID[e.UUID] = i
		leaf = i
	}
	if leaf < 0 {
		return nil
	}
	var chain []Entry
	seen := map[string]bool{}
	for i := leaf; i >= 0; {
		e := entries[i]
		if seen[e.UUID] {
			break
		}
		seen[e.UUID] = true
		chain = append(chain, e)
		next := ""
		switch {
		case e.ParentUUID != nil:
			next = *e.ParentUUID
		case logical && e.LogicalParentUUID != "":
			next = e.LogicalParentUUID
		}
		j, ok := byUUID[next]
		if next == "" || !ok {
			break
		}
		i = j
	}
	for l, r := 0, len(chain)-1; l < r; l, r = l+1, r-1 {
		chain[l], chain[r] = chain[r], chain[l]
	}
	return chain
}

// Rebuild converts entries into API messages along the active branch.
// Consecutive entries of the same role are merged (Claude Code writes one
// tool_result or block per line).
// SkillUse says which skill or command a meta entry holds.
type SkillUse struct {
	Name   string `json:"name"`
	Kind   string `json:"kind,omitempty"`
	Source string `json:"source,omitempty"`
	Args   string `json:"args,omitempty"`
	// ToolUseID is the Skill tool's call; empty when the user typed it.
	ToolUseID    string   `json:"toolUseId,omitempty"`
	AllowedTools []string `json:"allowedTools,omitempty"`
	Model        string   `json:"model,omitempty"`
	Effort       string   `json:"effort,omitempty"`
	AgentID      string   `json:"agentId,omitempty"`
	// Restored: its text came back after a compaction; the skill did not start again.
	Restored bool `json:"restored,omitempty"`
}

// ActiveSkill is a skill's text as the model read it.
type ActiveSkill struct {
	Name string
	Text string
}

// NoteActiveSkill adds s to the list as its newest; the name's earlier text goes.
func NoteActiveSkill(list []ActiveSkill, s ActiveSkill) []ActiveSkill {
	out := make([]ActiveSkill, 0, len(list)+1)
	for _, o := range list {
		if o.Name != s.Name {
			out = append(out, o)
		}
	}
	return append(out, s)
}

func Rebuild(entries []Entry) Conversation {
	var c Conversation
	snaps := map[string]int{}
	for _, e := range entries {
		switch e.Type {
		case TypeMeta:
			if e.PromptBundleVersion != "" {
				c.PromptBundleVersion = e.PromptBundleVersion
			}
			if e.Model != "" {
				c.Model = e.Model
			}
			if e.ForkedFrom != "" {
				c.ForkedFrom = e.ForkedFrom
			}
			if e.DetachedFromParent {
				c.ForkedFrom = ""
			}
			if e.OutputStyle != "" {
				c.OutputStyle = e.OutputStyle
			}
			if e.McpCatalogVersion != "" {
				c.McpCatalogVersion = e.McpCatalogVersion
			}
			if e.Worktree != nil {
				w := *e.Worktree
				c.Worktree = &w
				if w.Path == "" {
					c.Worktree = nil
				}
			}
			if e.Moved || e.AllowedDirs != nil {
				c.AllowedDirs = e.AllowedDirs
			}
		case TypeFileHistorySnapshot:
			if e.Snapshot == nil {
				continue
			}
			if i, ok := snaps[e.Snapshot.MessageID]; ok {
				c.Snapshots[i] = *e.Snapshot
			} else {
				snaps[e.Snapshot.MessageID] = len(c.Snapshots)
				c.Snapshots = append(c.Snapshots, *e.Snapshot)
			}
		}
	}
	extended := ActiveChain(entries, true)
	findingCalls := map[string]bool{}
	var todoLinks TodoLinkFold
	for _, e := range extended {
		c.Chain = append(c.Chain, e.UUID)
		if e.Type == TypeSystem && e.Subtype == SubtypeTodoLink && e.TodoLink != nil {
			todoLinks.Add(*e.TodoLink)
		}
		if e.Skill != nil {
			text := ""
			for _, b := range e.Blocks() {
				text += b.Text
			}
			c.ActiveSkills = NoteActiveSkill(c.ActiveSkills, ActiveSkill{Name: e.Skill.Name, Text: text})
		}
		if e.Type == TypeUser && e.ToolUseResult != nil && len(findingCalls) > 0 {
			for _, b := range e.Blocks() {
				if b.Type == llm.BlockToolResult && findingCalls[b.ToolUseID] {
					if raw, err := json.Marshal(e.ToolUseResult); err == nil {
						c.Findings = raw
					}
				}
			}
		}
		if e.Type == TypeAssistant {
			for _, b := range e.Blocks() {
				if b.Type == llm.BlockToolUse && b.Name == "ReportFindings" {
					findingCalls[b.ID] = true
				}
				if b.Type == llm.BlockToolUse && b.Name == "TodoWrite" {
					var in struct {
						Todos json.RawMessage `json:"todos"`
					}
					if json.Unmarshal(b.Input, &in) == nil && len(in.Todos) > 0 {
						c.Todos = in.Todos
						todoLinks.Replace(in.Todos)
					}
				}
			}
		}
	}
	c.TodoLinks = todoLinks.Links()
	if n := len(extended); n > 0 {
		c.LastUUID = extended[n-1].UUID
	}
	first := true
	for _, e := range ActiveChain(entries, false) {
		if e.Type != TypeUser && e.Type != TypeAssistant {
			continue
		}
		if e.Message == nil {
			continue
		}
		blocks := e.Blocks()
		if len(blocks) == 0 {
			continue
		}
		if first && e.Type == TypeUser && e.IsMeta {
			c.ContextInjected = true
		}
		first = false
		role := e.Message.Role
		if role == "" {
			role = e.Type
		}
		n := len(c.Messages)
		if n > 0 && c.Messages[n-1].Role == role {
			c.Messages[n-1].Content = append(c.Messages[n-1].Content, blocks...)
			continue
		}
		c.Messages = append(c.Messages, llm.Message{Role: role, Content: blocks})
	}
	return c
}

// TurnParent returns the parent of the turn started by the user message
// uuid (skipping that turn's project-context meta entry): the point a
// conversation rewind branches from. nil means the start of the session.
func TurnParent(entries []Entry, uuid string) (*string, bool) {
	byUUID := map[string]Entry{}
	for _, e := range entries {
		if e.UUID != "" && !unchained(e.Type) {
			byUUID[e.UUID] = e
		}
	}
	e, ok := byUUID[uuid]
	if !ok {
		return nil, false
	}
	p := e.ParentUUID
	if p != nil {
		if pe, ok := byUUID[*p]; ok && pe.Type == TypeUser && pe.IsMeta && pe.PromptID != "" && pe.PromptID == e.PromptID {
			p = pe.ParentUUID
		}
	}
	return p, true
}

// ParentOf returns the session a transcript was forked from, "" when it was
// never forked or was detached from its parent since. The last record wins
// (a fork of a fork copies its parent's record before its own).
func ParentOf(entries []Entry) string {
	parent := ""
	for _, e := range entries {
		if e.Type != TypeMeta {
			continue
		}
		if e.ForkedFrom != "" {
			parent = e.ForkedFrom
		}
		if e.DetachedFromParent {
			parent = ""
		}
	}
	return parent
}

// FirstText returns the first text of an entry's message.
func (e *Entry) FirstText() string {
	for _, b := range e.Blocks() {
		if b.Type == llm.BlockText && b.Text != "" {
			return b.Text
		}
	}
	return ""
}

// Writer appends entries to one transcript.
type Writer struct {
	mu        sync.Mutex
	path      string
	f         *os.File
	sessionID string
	cwd       string
	gitBranch string
	version   string
	entry     string
	last      string
	disabled  bool
	agentID   string
	entries   []Entry
}

// WriterOptions configure common fields.
type WriterOptions struct {
	SessionID  string
	Cwd        string
	GitBranch  string
	Version    string
	Entrypoint string // "sdk-cli" | "cli"
	LastUUID   string // continue the parent chain on resume
	Disabled   bool   // --no-session-persistence
	// Initial are the entries already in the transcript (resume), kept in
	// memory with every appended entry for rewinds and rebuilds.
	Initial []Entry
	// AgentID marks the entries of a subagent's transcript (isSidechain, agentId).
	AgentID string
}

// OpenWriter opens (creating if needed) the transcript at path for appending.
func OpenWriter(path string, o WriterOptions) (*Writer, error) {
	w := &Writer{path: path, sessionID: o.SessionID, cwd: o.Cwd, gitBranch: o.GitBranch, version: o.Version, entry: o.Entrypoint, last: o.LastUUID, disabled: o.Disabled, agentID: o.AgentID,
		entries: append([]Entry(nil), o.Initial...)}
	if o.Disabled {
		return w, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	w.f = f
	return w, nil
}

// Seed makes entries the start of a new transcript: they are written as they are, and what is appended next chains to the
// last of them. For a transcript that starts from another's.
func (w *Writer) Seed(entries []Entry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, e := range entries {
		if w.agentID != "" {
			e.AgentID = w.agentID
		}
		w.entries = append(w.entries, e)
		if e.UUID != "" {
			w.last = e.UUID
		}
		if w.f == nil {
			continue
		}
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err := w.f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// Path returns the transcript path.
func (w *Writer) Path() string { return w.path }

// SessionID returns the session id.
func (w *Writer) SessionID() string { return w.sessionID }

// LastUUID returns the uuid of the last chained entry.
func (w *Writer) LastUUID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}

// Append fills common fields, chains parentUuid to the previous entry and
// writes the entry. It returns the entry's uuid.
func (w *Writer) Append(e Entry) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var parent *string
	if w.last != "" {
		p := w.last
		parent = &p
	}
	return w.appendLocked(e, parent)
}

// AppendBranch writes a chained entry whose parent is set explicitly (nil:
// no parent, e.g. a compact boundary or a rewind to the session start). The
// entry becomes the new leaf.
func (w *Writer) AppendBranch(e Entry, parent *string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.appendLocked(e, parent)
}

func (w *Writer) appendLocked(e Entry, parent *string) (string, error) {
	e.SessionID = w.sessionID
	e.AgentID, e.IsSidechain = w.agentID, w.agentID != ""
	if e.Timestamp == "" {
		e.Timestamp = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if !unchained(e.Type) {
		if e.UUID == "" {
			e.UUID = uuid.NewString()
		}
		e.ParentUUID = parent
		e.Cwd, e.GitBranch, e.Version, e.Entrypoint = w.cwd, w.gitBranch, w.version, w.entry
		e.UserType = "external"
		w.last = e.UUID
	}
	w.entries = append(w.entries, e)
	if w.disabled {
		return e.UUID, nil
	}
	data, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if _, err := w.f.Write(data); err != nil {
		return "", err
	}
	return e.UUID, nil
}

// SetLocation changes the folder and branch stamped on the entries written from now on.
func (w *Writer) SetLocation(cwd, gitBranch string) {
	w.mu.Lock()
	w.cwd, w.gitBranch = cwd, gitBranch
	w.mu.Unlock()
}

// Move moves the transcript file (and its sibling directory) to dst while the writer stays open; the writer goes
// on appending there. When the new file cannot be opened the move is undone.
func (w *Writer) Move(dst string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if dst == w.path {
		return nil
	}
	if w.disabled {
		w.path = dst
		return nil
	}
	if err := w.f.Close(); err != nil {
		return err
	}
	src := w.path
	moveErr := MoveFiles(src, dst)
	if moveErr == nil {
		w.path = dst
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil && moveErr == nil {
		moveErr = err
		if MoveFiles(dst, src) == nil {
			w.path = src
		}
		f, err = os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	}
	if err == nil {
		w.f = f
	}
	return moveErr
}

// MoveFiles moves a transcript file and its sibling directory (same name without .jsonl) when present.
func MoveFiles(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%s already exists", dst)
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	sd, dd := strings.TrimSuffix(src, ".jsonl"), strings.TrimSuffix(dst, ".jsonl")
	if st, err := os.Stat(sd); err == nil && st.IsDir() {
		if err := os.Rename(sd, dd); err != nil {
			return err
		}
	}
	return nil
}

// Entries returns a copy of all entries of the transcript (loaded and
// appended).
func (w *Writer) Entries() []Entry {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Entry(nil), w.entries...)
}

// Close closes the file.
func (w *Writer) Close() error {
	if w.f == nil {
		return nil
	}
	return w.f.Close()
}

// UserText builds a user entry message whose content is a plain string.
func UserText(text string) *Message {
	raw, _ := json.Marshal(text)
	return &Message{Role: llm.RoleUser, Content: raw}
}

// UserBlocks builds a user entry message with block content.
func UserBlocks(blocks []llm.ContentBlock) *Message {
	raw, _ := json.Marshal(blocks)
	return &Message{Role: llm.RoleUser, Content: raw}
}

// AssistantMessage builds an assistant entry message from a response.
func AssistantMessage(r llm.Response) *Message {
	raw, _ := json.Marshal(r.Message.Content)
	m := &Message{ID: r.ID, Type: "message", Role: llm.RoleAssistant, Model: r.Model, Content: raw}
	if r.StopReason != "" {
		sr := r.StopReason
		m.StopReason = &sr
	}
	u := r.Usage
	m.Usage = &u
	return m
}
