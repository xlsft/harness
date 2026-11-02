// Package tools implements the local tools executed by the agent loop. Tool
// names and parameters match Claude Code; descriptions and JSON schemas come
// from the prompt bundle, execution semantics live here.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	// may run in parallel and are always permitted.
	ReadOnly(input json.RawMessage) bool
	// Run executes the call. Implementations must honor ctx cancellation.
	Run(ctx context.Context, env *Env, call Call) Result
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
	// Files tracks read state for read-before-write checks.
	Files *FileState
	// Shell keeps the persistent Bash cwd.
	Shell *Shell
	// Reminders gives access to bundle reminder templates (may be nil).
	Reminders func(key string) string
	// RipgrepPath overrides ripgrep lookup; "-" disables ripgrep.
	RipgrepPath string
}

// NewEnv creates an Env with fresh state.
func NewEnv(cwd, scratch string) *Env {
	e := &Env{Cwd: cwd, ScratchDir: scratch, Files: NewFileState()}
	e.Shell = NewShell(e)
	return e
}

// WorkingDirs returns Cwd plus AdditionalDirs.
func (e *Env) WorkingDirs() []string { return append([]string{e.Cwd}, e.AdditionalDirs...) }

func (e *Env) reminder(key string) string {
	if e.Reminders == nil {
		return ""
	}
	return e.Reminders(key)
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

// Default returns the M0 tool set.
func Default() *Registry {
	return NewRegistry(&Bash{}, &Glob{}, &Grep{}, &Read{}, &Edit{}, &Write{})
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

// FileState records which files were read and their modification time, for
// the read-before-write and modified-since-read checks.
type FileState struct {
	mu    sync.Mutex
	files map[string]fileRecord
}

type fileRecord struct {
	mtime time.Time
	size  int64
}

// NewFileState returns an empty tracker.
func NewFileState() *FileState { return &FileState{files: map[string]fileRecord{}} }

// MarkRead records the current mtime of path.
func (f *FileState) MarkRead(path string) {
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.files[filepath.Clean(path)] = fileRecord{mtime: st.ModTime(), size: st.Size()}
	f.mu.Unlock()
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
