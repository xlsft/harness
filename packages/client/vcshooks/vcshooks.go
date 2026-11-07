// Package vcshooks is version control through hooks, for folders git does not cover (Mercurial, Jujutsu, Perforce): the
// WorktreeCreate and WorktreeRemove events give sessions and agents an isolated working copy, VcsStatus and VcsDiff give
// the Changes pane what changed. The contract, and why it is shaped so, is in docs/design/vcs-hooks.md.
//
// The hooks are the session's own (hooks.Engine), so they keep its gates: a project's hooks run only in a trusted
// folder, what the server delivered only once this device trusted it, and a policy with allowManagedHooksOnly leaves
// only its own.
package vcshooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xlsft/harness/packages/client/hooks"
	"github.com/xlsft/harness/packages/client/worktree"
)

// Kinds of a working copy (WorktreeCreate's worktree_kind).
const (
	KindSession = "session"
	KindAgent   = "agent"
)

// Errors.
var (
	// ErrNoAnswer: the hooks ran and none gave what the event asks for.
	ErrNoAnswer = errors.New("no hook answered")
	// ErrNoRemoveHook: WorktreeCreate is configured, WorktreeRemove is not, so a copy stays where it is.
	ErrNoRemoveHook = errors.New("no WorktreeRemove hook is configured")
)

// Hooks are a session's VCS hooks. The zero value (no engine) has none.
type Hooks struct{ e *hooks.Engine }

// Of returns the VCS hooks of a session's engine.
func Of(e *hooks.Engine) Hooks { return Hooks{e: e} }

// Isolates reports whether WorktreeCreate hooks run: they then replace git worktrees everywhere.
func (h Hooks) Isolates() bool { return h.e.Runs(hooks.WorktreeCreate) }

// Reports reports whether VcsStatus hooks run: folders outside git then show the VCS's own changes.
func (h Hooks) Reports() bool { return h.e.Runs(hooks.VcsStatus) }

// CreateRequest asks for an isolated working copy of Source.
type CreateRequest struct {
	// Source is the folder the session or agent works in now.
	Source string
	// Name is a single path segment; RandomName when empty.
	Name string
	// Kind is KindSession or KindAgent; AgentID names the agent.
	Kind, AgentID string
	// BaseRevision is what the user or model asked to start from ("": the hook decides, usually the current revision).
	BaseRevision string
}

// Created is a working copy a WorktreeCreate hook made.
type Created struct {
	Name string
	// Path is the copy's root; Cwd the folder in it to work in (Path unless the hook said otherwise).
	Path, Cwd string
	// Base is the hook's label for what the copy started from ("" when it gave none).
	Base string
	// Source is the folder it is a copy of.
	Source string
}

// reply is what the events with an output contract may answer, at the top level or under hookSpecificOutput.
type reply struct {
	WorktreePath string  `json:"worktreePath"`
	BaseRevision string  `json:"baseRevision"`
	Cwd          string  `json:"cwd"`
	Root         string  `json:"root"`
	Base         string  `json:"base"`
	Files        []File  `json:"files"`
	Diff         *string `json:"diff"`
}

func parseReply(text string, ev hooks.Event) (*reply, bool, error) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "{") {
		return nil, false, nil
	}
	var top struct {
		reply
		Specific *struct {
			HookEventName string `json:"hookEventName"`
			reply
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(text), &top); err != nil {
		return nil, true, fmt.Errorf("its output is not valid JSON: %w", err)
	}
	if s := top.Specific; s != nil {
		if s.HookEventName != "" && s.HookEventName != string(ev) {
			return nil, true, fmt.Errorf("its hookSpecificOutput is for %s", s.HookEventName)
		}
		return &s.reply, true, nil
	}
	return &top.reply, true, nil
}

func failed(ev hooks.Event, out hooks.Outcome) error {
	return fmt.Errorf("the %s hook failed: %s", ev, strings.Join(out.Failures, "; "))
}

// Create runs WorktreeCreate. The last handler in configuration order that gives a path wins (configure one); a handler
// that fails fails the whole, after the others finished, so nothing is used half made.
func (h Hooks) Create(ctx context.Context, req CreateRequest) (*Created, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = worktree.RandomName()
	} else if err := worktree.ValidateName(name); err != nil {
		return nil, err
	}
	source, err := filepath.Abs(req.Source)
	if err != nil {
		return nil, err
	}
	kind := req.Kind
	if kind == "" {
		kind = KindSession
	}
	out := h.e.Dispatch(ctx, hooks.WorktreeCreate, "", hooks.Input{Cwd: source, SourceDir: source, Name: name, WorktreeKind: kind,
		AgentID: req.AgentID, BaseRevision: req.BaseRevision})
	if len(out.Failures) > 0 {
		return nil, failed(hooks.WorktreeCreate, out)
	}
	if out.Ran == 0 {
		return nil, fmt.Errorf("%w: no WorktreeCreate hook ran", ErrNoAnswer)
	}
	for i := len(out.Replies) - 1; i >= 0; i-- {
		c, err := createdFrom(out.Replies[i], source)
		if err != nil {
			return nil, fmt.Errorf("the WorktreeCreate hook answered, but %w", err)
		}
		if c != nil {
			c.Name, c.Source = name, source
			return c, nil
		}
	}
	return nil, fmt.Errorf("%w: the WorktreeCreate hook printed no path", ErrNoAnswer)
}

// createdFrom reads one handler's answer: JSON with worktreePath, or the path as the last line of plain output. nil when
// it gave no path.
func createdFrom(text, source string) (*Created, error) {
	r, isJSON, err := parseReply(text, hooks.WorktreeCreate)
	if err != nil {
		return nil, err
	}
	c := &Created{}
	if isJSON {
		c.Path, c.Cwd, c.Base = r.WorktreePath, r.Cwd, r.BaseRevision
	} else {
		lines := strings.Split(strings.TrimSpace(text), "\n")
		c.Path = strings.TrimSpace(lines[len(lines)-1])
	}
	if c.Path == "" {
		return nil, nil
	}
	abs := func(p string) string {
		if !filepath.IsAbs(p) {
			p = filepath.Join(source, p)
		}
		return filepath.Clean(p)
	}
	c.Path = abs(c.Path)
	if st, err := os.Stat(c.Path); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", c.Path)
	}
	if same(c.Path, source) {
		return nil, fmt.Errorf("%s is the source folder itself, not a copy", c.Path)
	}
	c.Cwd = c.Path
	if r != nil && r.Cwd != "" {
		cwd := abs(r.Cwd)
		if !same(cwd, c.Path) && !within(c.Path, cwd) {
			return nil, fmt.Errorf("its cwd %s is not inside %s", cwd, c.Path)
		}
		if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("its cwd %s is not a folder", cwd)
		}
		c.Cwd = cwd
	}
	return c, nil
}

// RemoveRequest asks to take a working copy away.
type RemoveRequest struct {
	Path, Source, Name string
	Kind, AgentID      string
	// Force: the user or model agreed to lose what the copy holds that the repository does not.
	Force bool
}

// Remove runs WorktreeRemove. ErrNoRemoveHook when none runs: the copy stays.
func (h Hooks) Remove(ctx context.Context, req RemoveRequest) error {
	if !h.e.Runs(hooks.WorktreeRemove) {
		return ErrNoRemoveHook
	}
	force := req.Force
	cwd := req.Source
	if st, err := os.Stat(cwd); cwd == "" || err != nil || !st.IsDir() {
		cwd = filepath.Dir(req.Path)
	}
	out := h.e.Dispatch(ctx, hooks.WorktreeRemove, "", hooks.Input{Cwd: cwd, SourceDir: req.Source, WorktreePath: req.Path, Name: req.Name,
		WorktreeKind: req.Kind, AgentID: req.AgentID, Force: &force})
	if len(out.Failures) > 0 {
		return failed(hooks.WorktreeRemove, out)
	}
	return nil
}

// File is one changed file of VcsStatus.
type File struct {
	// Path is relative to Status.Root (slash-separated), or absolute inside it.
	Path string `json:"path"`
	// Status is added, modified, deleted or renamed (A, M, D, R do too).
	Status  string `json:"status"`
	OldPath string `json:"oldPath,omitempty"`
	// Additions and Deletions, when the hook counts them, spare a VcsDiff for the totals.
	Additions *int32 `json:"additions,omitempty"`
	Deletions *int32 `json:"deletions,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
}

// Status is a folder's changes as its VCS sees them.
type Status struct {
	// Root is the working copy's root, absolute; Base the label of what it compares with ("@-", "tip", "have").
	Root, Base string
	Files      []File
}

// Status runs VcsStatus in dir. nil without an error when no hook claims the folder (empty output): it is not theirs.
func (h Hooks) Status(ctx context.Context, dir string) (*Status, error) {
	out := h.e.Dispatch(ctx, hooks.VcsStatus, "", hooks.Input{Cwd: dir})
	if len(out.Failures) > 0 {
		return nil, failed(hooks.VcsStatus, out)
	}
	for i := len(out.Replies) - 1; i >= 0; i-- {
		text := out.Replies[i]
		if strings.TrimSpace(text) == "" {
			continue
		}
		r, isJSON, err := parseReply(text, hooks.VcsStatus)
		if err == nil && !isJSON {
			err = errors.New("its output is not JSON")
		}
		if err != nil {
			return nil, fmt.Errorf("the VcsStatus hook answered, but %w", err)
		}
		return statusFrom(r, dir)
	}
	return nil, nil
}

func statusFrom(r *reply, dir string) (*Status, error) {
	st := &Status{Root: r.Root, Base: r.Base}
	if st.Root == "" {
		st.Root = dir
	}
	if !filepath.IsAbs(st.Root) {
		return nil, fmt.Errorf("the VcsStatus hook answered, but its root %s is not absolute", st.Root)
	}
	st.Root = filepath.Clean(st.Root)
	if !same(st.Root, dir) && !within(st.Root, dir) {
		return nil, fmt.Errorf("the VcsStatus hook answered, but its root %s does not hold %s", st.Root, dir)
	}
	for _, f := range r.Files {
		var err error
		if f.Path, err = relPath(st.Root, f.Path); err != nil {
			return nil, err
		}
		if f.OldPath != "" {
			if f.OldPath, err = relPath(st.Root, f.OldPath); err != nil {
				return nil, err
			}
		}
		switch strings.ToLower(f.Status) {
		case "a", "added":
			f.Status = "added"
		case "d", "deleted", "removed":
			f.Status = "deleted"
		case "r", "renamed":
			f.Status = "renamed"
		default:
			f.Status = "modified"
		}
		st.Files = append(st.Files, f)
	}
	return st, nil
}

// relPath turns a reported path into a slash-separated path inside root; one that leaves it is refused.
func relPath(root, p string) (string, error) {
	if p == "" {
		return "", errors.New("the VcsStatus hook answered, but a file has no path")
	}
	if filepath.IsAbs(p) {
		if rel, err := filepath.Rel(root, filepath.Clean(p)); err == nil && within(root, filepath.Clean(p)) {
			return filepath.ToSlash(rel), nil
		}
		return "", fmt.Errorf("the VcsStatus hook answered, but %s is not inside %s", p, root)
	}
	clean := filepath.Clean(filepath.FromSlash(p))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("the VcsStatus hook answered, but %s is not inside %s", p, root)
	}
	return filepath.ToSlash(clean), nil
}

// Diff runs VcsDiff in dir for the paths given (relative to the root VcsStatus reported; none: every change) and returns
// the unified diff, git style (`diff --git a/x b/x` headers).
func (h Hooks) Diff(ctx context.Context, dir string, paths []string) ([]byte, error) {
	out := h.e.Dispatch(ctx, hooks.VcsDiff, "", hooks.Input{Cwd: dir, Paths: paths})
	if len(out.Failures) > 0 {
		return nil, failed(hooks.VcsDiff, out)
	}
	if out.Ran == 0 {
		return nil, fmt.Errorf("%w: no VcsDiff hook is configured", ErrNoAnswer)
	}
	for i := len(out.Replies) - 1; i >= 0; i-- {
		text := out.Replies[i]
		r, isJSON, err := parseReply(text, hooks.VcsDiff)
		if err != nil {
			return nil, fmt.Errorf("the VcsDiff hook answered, but %w", err)
		}
		if !isJSON {
			return []byte(text), nil
		}
		if r.Diff != nil {
			return []byte(*r.Diff), nil
		}
	}
	return nil, nil
}

func same(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// within reports whether path is strictly inside dir, lexically or with symlinks resolved.
func within(dir, path string) bool {
	in := func(dir, path string) bool {
		rel, err := filepath.Rel(dir, path)
		return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	if in(dir, path) {
		return true
	}
	rd, err1 := filepath.EvalSymlinks(dir)
	rp, err2 := filepath.EvalSymlinks(path)
	return err1 == nil && err2 == nil && in(rd, rp)
}
