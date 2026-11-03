// Package tools implements the local tools executed by the agent loop. Tool
// names and parameters match Claude Code; descriptions and JSON schemas come
// from the prompt bundle, execution semantics live here.
package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/harness/packages/client/llm"
)

// Result is the outcome of a tool call.
type Result struct {
	// Content is the tool_result content sent to the model.
	Content []llm.ContentBlock
	IsError bool
	// Structured is persisted as toolUseResult in the session transcript.
	Structured any
	// Summary is a short human-readable outcome ("Read 42 lines").
	Summary string
}

// TextResult builds a successful text result.
func TextResult(s string, structured any) Result {
	return Result{Content: []llm.ContentBlock{llm.TextBlock(s)}, Structured: structured}
}

// ErrorResult builds an error result in the <tool_use_error> convention.
func ErrorResult(format string, args ...any) Result {
	msg := fmt.Sprintf(format, args...)
	return Result{Content: []llm.ContentBlock{llm.TextBlock("<tool_use_error>" + msg + "</tool_use_error>")}, IsError: true, Structured: "Error: " + msg}
}

// Tool is one executable tool.
type Tool interface {
	Name() string
	// ReadOnly reports whether this call has no side effects; read-only calls
	// may run in parallel and are permitted in plan mode.
	ReadOnly(input json.RawMessage) bool
	// Run executes the call. Implementations must honor ctx cancellation.
	Run(ctx context.Context, env *Env, call Call) Result
}

// AlwaysAsker is implemented by tools whose every call needs an explicit
// user confirmation, whatever the permission rules and mode say (MCP servers
// with requires_user_interaction).
type AlwaysAsker interface {
	AlwaysAsk() bool
}

// Call identifies one tool invocation.
type Call struct {
	ID    string
	Input json.RawMessage
}

// Env is the execution environment shared by tools within a session.
type Env struct {
	// Cwd is the primary working directory (project root).
	Cwd string
	// AdditionalDirs are extra directories the shell may cd into.
	AdditionalDirs []string
	// ScratchDir holds per-session temporary files (large outputs, shell state).
	ScratchDir string
	// SessionID identifies the session.
	SessionID string
	// PlansDir receives plans written by ExitPlanMode.
	PlansDir string
	// Files tracks read state for read-before-write checks.
	Files *FileState
	// Shell keeps the persistent Bash cwd.
	Shell *Shell
	// Tasks runs background shell commands (nil disables backgrounding).
	Tasks *TaskManager
	// Reminders gives access to bundle reminder templates (may be nil).
	Reminders func(key string) string
	// RipgrepPath overrides ripgrep lookup; "-" disables ripgrep.
	RipgrepPath string
	// BeforeEdit is called before a file tool modifies path (checkpoints).
	BeforeEdit func(path string)

	mu   sync.Mutex
	host Host
}

// NewEnv creates an Env with fresh state.
func NewEnv(cwd, scratch string) *Env {
	e := &Env{Cwd: cwd, ScratchDir: scratch, Files: NewFileState()}
	e.Shell = NewShell(e)
	return e
}

// SetHost installs the session host (the agent).
func (e *Env) SetHost(h Host) {
	e.mu.Lock()
	e.host = h
	e.mu.Unlock()
}

// Host returns the session host or nil.
func (e *Env) Host() Host {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.host
}

// WorkingDirs returns Cwd plus AdditionalDirs.
func (e *Env) WorkingDirs() []string { return append([]string{e.Cwd}, e.AdditionalDirs...) }

func (e *Env) reminder(key string) string {
	if e.Reminders == nil {
		return ""
	}
	return e.Reminders(key)
}

func (e *Env) beforeEdit(path string) {
	if e.BeforeEdit != nil {
		e.BeforeEdit(path)
	}
}

// Host is the session side of tools that change session state: permission
// mode, reminders for the next model request, the todo list and user
// interaction. The agent implements it.
type Host interface {
	PermissionMode() string
	SetPermissionMode(mode string) error
	// Remind queues a bundle reminder for the next model request.
	Remind(id string, vars map[string]string)
	Todos() []Todo
	SetTodos(todos []Todo)
	// Interactor returns the interactive frontend, or nil in non-interactive mode.
	Interactor() Interactor
}

// ErrNonInteractive is returned by an Interactor that cannot ask the user.
var ErrNonInteractive = errors.New("no interactive user is attached to this session")

// Interactor asks the user questions and plan approvals (AgentService).
type Interactor interface {
	// AskQuestions blocks until the user answers: one []string of selected
	// labels (or free text) per question.
	AskQuestions(ctx context.Context, toolUseID string, qs []Question) ([][]string, error)
	// ProposePlan blocks until the user approves or rejects the plan.
	ProposePlan(ctx context.Context, toolUseID, plan, path string) (PlanResponse, error)
}

// PlanDecision is the user's answer to a proposed plan.
type PlanDecision int

// Plan decisions.
const (
	PlanApproveAcceptEdits PlanDecision = iota + 1
	PlanApproveManual
	PlanKeepPlanning
)

// PlanResponse is the answer to ProposePlan.
type PlanResponse struct {
	Decision PlanDecision
	Feedback string
}

// Registry holds tools by name.
type Registry struct {
	order []string
	tools map[string]Tool
}

// NewRegistry registers the given tools.
func NewRegistry(ts ...Tool) *Registry {
	r := &Registry{tools: map[string]Tool{}}
	for _, t := range ts {
		r.Register(t)
	}
	return r
}

// Default returns the built-in tool set. Only tools the prompt bundle
// defines are exposed to the model.
func Default() *Registry {
	return NewRegistry(&Bash{}, &Glob{}, &Grep{}, &Read{}, &Edit{}, &Write{},
		&AskUserQuestion{}, &EnterPlanMode{}, &ExitPlanMode{}, &TodoWrite{}, &TaskStop{}, &Show{})
}

// Register adds or replaces a tool.
func (r *Registry) Register(t Tool) {
	if _, ok := r.tools[t.Name()]; !ok {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

// Remove drops a tool.
func (r *Registry) Remove(name string) {
	delete(r.tools, name)
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Names returns tool names in registration order.
func (r *Registry) Names() []string { return append([]string(nil), r.order...) }

// FileState records which files were read, their modification time and
// content, for the read-before-write, modified-since-read and file_changed
// checks.
type FileState struct {
	mu    sync.Mutex
	files map[string]*fileRecord
}

// maxTrackedContent is the largest file whose content is kept for diffs.
const maxTrackedContent = 256 * 1024

type fileRecord struct {
	mtime   time.Time
	size    int64
	hash    [32]byte
	content string
	hasText bool
}

// NewFileState returns an empty tracker.
func NewFileState() *FileState { return &FileState{files: map[string]*fileRecord{}} }

// MarkRead records the current state of path (reading it from disk).
func (f *FileState) MarkRead(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	f.MarkReadContent(path, data)
}

// MarkReadContent records path with content data already read.
func (f *FileState) MarkReadContent(path string, data []byte) {
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	rec := &fileRecord{mtime: st.ModTime(), size: st.Size(), hash: sha256.Sum256(data)}
	if len(data) <= maxTrackedContent && !bytes.Contains(data[:min(len(data), 8192)], []byte{0}) {
		rec.content, rec.hasText = string(data), true
	}
	f.mu.Lock()
	f.files[filepath.Clean(path)] = rec
	f.mu.Unlock()
}

// WasRead reports whether path is tracked.
func (f *FileState) WasRead(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.files[filepath.Clean(path)]
	return ok
}

// Forget removes a record.
func (f *FileState) Forget(path string) {
	f.mu.Lock()
	delete(f.files, filepath.Clean(path))
	f.mu.Unlock()
}

// CheckWritable returns an error message when an existing file was not read
// or was modified after it was read.
func (f *FileState) CheckWritable(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return "" // new file
	}
	f.mu.Lock()
	rec, ok := f.files[filepath.Clean(path)]
	f.mu.Unlock()
	if !ok {
		return "File has not been read yet. Read it first before writing to it."
	}
	if !st.ModTime().Equal(rec.mtime) || st.Size() != rec.size {
		return "File has been modified since read, either by the user or by a linter. Read it again before attempting to write it."
	}
	return ""
}

// FileChange describes a tracked file that changed on disk.
type FileChange struct {
	Path string
	// Diff is a short unified diff (empty when the old content is unknown).
	Diff string
}

// Changes detects tracked files modified on disk since a tool last read or
// wrote them, updates their records (the reminder counts as a read) and
// returns short diffs. Deleted files are forgotten.
func (f *FileState) Changes(maxDiffLines int) []FileChange {
	f.mu.Lock()
	paths := make([]string, 0, len(f.files))
	for p := range f.files {
		paths = append(paths, p)
	}
	f.mu.Unlock()
	sort.Strings(paths)
	var out []FileChange
	for _, p := range paths {
		f.mu.Lock()
		rec := f.files[p]
		f.mu.Unlock()
		if rec == nil {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			f.Forget(p)
			continue
		}
		if st.ModTime().Equal(rec.mtime) && st.Size() == rec.size {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if sha256.Sum256(data) == rec.hash {
			f.mu.Lock()
			rec.mtime, rec.size = st.ModTime(), st.Size()
			f.mu.Unlock()
			continue
		}
		ch := FileChange{Path: p}
		if rec.hasText && len(data) <= maxTrackedContent {
			ch.Diff = UnifiedDiff(DiffLines(rec.content, string(data), 2), maxDiffLines)
		}
		f.MarkReadContent(p, data)
		out = append(out, ch)
	}
	return out
}

// decode unmarshals input into v and reports input errors uniformly.
func decode(input json.RawMessage, v any) error {
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(input, &probe); err != nil {
		return fmt.Errorf("InputValidationError: input must be a JSON object: %v", err)
	}
	if bad, ok := probe["__invalid_json__"]; ok {
		return fmt.Errorf("InputValidationError: tool input is not valid JSON: %s", bad)
	}
	if err := json.Unmarshal(input, v); err != nil {
		return fmt.Errorf("InputValidationError: %v", err)
	}
	return nil
}

// resolvePath makes p absolute relative to base.
func resolvePath(base, p string) string {
	if p == "" {
		return base
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, p[2:])
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

// within reports whether path is inside one of dirs.
func within(path string, dirs []string) bool {
	path = evalPath(path)
	for _, d := range dirs {
		d = evalPath(d)
		rel, err := filepath.Rel(d, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func evalPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
