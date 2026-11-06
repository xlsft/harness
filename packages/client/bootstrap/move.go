package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/memory"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Moving a running session (tools.Mover): the tools call this in the middle of a turn, so nothing is closed and
// reopened as SetSessionFolder does for an idle session. Everything that depends on the folder follows at once: the
// tools' Cwd and the shell, permissions (working directories and the project's rules), the system prompt's environment
// section, project instructions, the transcript with its upload, and language servers, which stop. What stays until the session reopens: MCP servers
// already started and background tasks already running.

// ReminderSessionMoved tells the model where the session works now.
const ReminderSessionMoved = "session_moved"

// IsTrusted reports whether the session's folder is trusted (it can change with the folder).
func (s *Session) IsTrusted() bool {
	s.placeMu.RLock()
	defer s.placeMu.RUnlock()
	return s.Trusted
}

// Dir returns the session's primary folder, which changes when the session moves.
func (s *Session) Dir() string {
	s.placeMu.RLock()
	defer s.placeMu.RUnlock()
	return s.Cwd
}

// CurrentWorktree returns the worktree the session runs in (a copy; nil when none).
func (s *Session) CurrentWorktree() *session.Worktree {
	s.placeMu.RLock()
	defer s.placeMu.RUnlock()
	if s.Worktree == nil {
		return nil
	}
	w := *s.Worktree
	return &w
}

// AllowedDirs returns the working directories the session gained while it ran (they survive a restart in its transcript).
func (s *Session) AllowedDirs() []string {
	s.placeMu.RLock()
	defer s.placeMu.RUnlock()
	return slices.Clone(s.allowedDirs)
}

// PlaceHooks connect a session to its frontend (AgentService). All are optional and none is called with session locks
// held.
type PlaceHooks struct {
	// Folder gives the additional directories and variables the session has in cwd, for sessions whose folders have
	// environments. Without it the session keeps its variables and works in the directories it gained.
	Folder func(cwd string, wt *session.Worktree, allowed []string) (dirs, environ []string)
	// Changed runs after the session moved, changed its working directories or its branch. Notice says what happened
	// ("" when nothing worth a line); branch is the branch it is on now.
	Changed func(notice, branch string)
	// OthersIn returns the ids of other open sessions working inside path.
	OthersIn func(path string) []string
}

// SetPlaceHooks installs the hooks.
func (s *Session) SetPlaceHooks(h PlaceHooks) {
	s.placeMu.Lock()
	s.hooks = h
	s.placeMu.Unlock()
}

func (s *Session) placeHooks() PlaceHooks {
	s.placeMu.RLock()
	defer s.placeMu.RUnlock()
	return s.hooks
}

// mover adapts the session to tools.Mover (the field Worktree takes the method's name on Session).
type mover struct{ s *Session }

var _ tools.Mover = mover{}

func (m mover) Worktree() *session.Worktree { return m.s.CurrentWorktree() }
func (m mover) Move(ctx context.Context, mv tools.Move) error {
	return m.s.move(ctx, mv)
}
func (m mover) AllowDirectory(dir string) error { return m.s.allowDirectory(dir) }
func (m mover) SetBranch(ctx context.Context, branch, baseRef, notice string) {
	m.s.setBranch(ctx, branch, baseRef, notice)
}
func (m mover) OthersIn(path string) []string {
	if f := m.s.placeHooks().OthersIn; f != nil {
		return f(path)
	}
	return nil
}

// move switches the running session to another folder; see the comment at the top of the file.
func (s *Session) move(ctx context.Context, mv tools.Move) error {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	ctx = context.WithoutCancel(ctx) // once begun a move finishes, whatever happens to the turn
	cwd, err := filepath.Abs(mv.Cwd)
	if err != nil {
		return err
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a folder", cwd)
	}
	prev := s.Dir()
	allowed := s.AllowedDirs()
	if mv.KeepPrevious {
		allowed = dedupeDirs(slices.Concat(allowed, s.Permissions.WorkingDirs()))
	}
	allowed = slices.DeleteFunc(allowed, func(d string) bool { return filepath.Clean(d) == filepath.Clean(cwd) })

	branch := workspace.GitBranch(ctx, cwd)
	// The transcript comes first: it is the one step that can fail, and a transcript in the new folder's project
	// directory is what readers take the folder from.
	dst := session.Store{Root: config.ProjectsDir()}.Path(cwd, s.ID)
	if s.Uploader != nil {
		err = s.Uploader.MoveTranscript(s.ID, dst, func() error { return s.Transcript.Move(dst) })
	} else {
		err = s.Transcript.Move(dst)
	}
	if err != nil {
		return fmt.Errorf("move the transcript: %w", err)
	}
	s.Transcript.SetLocation(cwd, branch)
	mark := mv.Worktree
	if mark == nil {
		mark = &session.Worktree{} // on record: the session left its worktree
	}
	_, _ = s.Transcript.Append(session.Entry{Type: session.TypeMeta, Moved: true, Cwd: cwd, Worktree: mark, AllowedDirs: allowed})
	if mv.Notice != "" {
		_, _ = s.Transcript.Append(session.Entry{Type: session.TypeSystem, Subtype: session.SubtypeSessionMoved, Content: mv.Notice, Level: "info"})
	}

	hooks := s.placeHooks()
	dirs := mergeDirs(s.open.AdditionalDirs, allowed)
	dirs = slices.DeleteFunc(dirs, func(d string) bool { return filepath.Clean(d) == filepath.Clean(cwd) })
	if hooks.Folder != nil {
		var environ []string
		dirs, environ = hooks.Folder(cwd, mv.Worktree, allowed)
		s.ToolEnv.SetExtraEnv(environ)
	}

	trusted := folderTrust(s.open, cwd)
	s.placeMu.Lock()
	s.Cwd = cwd
	s.Worktree = mv.Worktree
	s.allowedDirs = allowed
	s.Trusted = trusted
	s.placeMu.Unlock()

	// Settings of the folder: its project's permission rules replace those of the folder the session left.
	var docs settings.DocSource
	var memDocs memory.DocSource
	if s.Sync != nil {
		docs, memDocs = s.Sync.Cache(), s.Sync.Cache()
	}
	st, sources := loadSettings(ctx, s.open, cwd, trusted, docs, s.log)
	if s.fixedSources {
		sources = s.open.PermissionSources
	}
	s.ToolEnv.Relocate(cwd, dirs)
	s.Permissions.Relocate(permissions.Relocation{Cwd: cwd, Additional: dirs, Sources: sources, Trusted: trusted})
	s.relocateHooks(st, prev, cwd)
	if s.LSP != nil {
		s.LSP.Reset() // the servers belong to the folder the session left; the first use in the new one starts its own
	}

	// What the model knows: the environment section from the next request on, and a reminder for this one.
	s.Memory = projectMemory(ctx, cwd, dirs, st, memDocs, config.IsChatDir(cwd))
	env := workspace.Detect(ctx, cwd)
	gitForContext := ""
	if !s.Bundle.UsesPlaceholder("git_status") {
		gitForContext = env.GitStatus
	}
	initial := s.Memory.Initial()
	projectContext := memory.ProjectContext(initial, gitForContext)
	details := ""
	if text := instructionText(initial); text != s.instructions {
		s.instructions = text
		details = "\nThe project instructions of this folder replace what you were given before:\n" + memory.ProjectContext(initial, "")
		if text == "" {
			details = "\nThis folder has no project instructions; what you were given before no longer applies."
		}
	}
	s.Agent.SetPlace(map[string]string{
		"cwd": cwd, "is_git": fmt.Sprint(env.IsGit), "git_status": env.GitStatus,
		"additional_directories": strings.Join(dirs, ", "),
	}, projectContext)
	s.Agent.Remind(ReminderSessionMoved, map[string]string{
		"cwd": cwd, "previous": prev, "branch": orNone(branch), "directories": strings.Join(append([]string{cwd}, dirs...), "\n"), "details": details,
	})

	if s.Uploader != nil {
		repo := workspace.Repo(ctx, cwd)
		_ = s.Uploader.UpdateMeta(s.ID, func(m *syncclient.SessionMeta) {
			m.Cwd, m.Repo, m.Branch, m.Worktree = cwd, repo, branch, syncWorktree(mv.Worktree)
		})
	}
	if s.VCS != nil {
		s.VCS.SetDir(cwd)
	}
	if hooks.Changed != nil {
		hooks.Changed(mv.Notice, branch)
	}
	return nil
}

// allowDirectory makes dir a working directory of the session, on the user's answer.
func (s *Session) allowDirectory(dir string) error {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	cwd := s.Dir()
	allowed := dedupeDirs(append(s.AllowedDirs(), dir))
	wt := s.CurrentWorktree()
	_, _ = s.Transcript.Append(session.Entry{Type: session.TypeMeta, AllowedDirs: allowed})
	notice := "Added " + dir + " as a working directory."
	_, _ = s.Transcript.Append(session.Entry{Type: session.TypeSystem, Subtype: session.SubtypeSessionMoved, Content: notice, Level: "info"})
	s.placeMu.Lock()
	s.allowedDirs = allowed
	s.placeMu.Unlock()

	hooks := s.placeHooks()
	dirs := slices.DeleteFunc(mergeDirs(s.open.AdditionalDirs, allowed), func(d string) bool { return filepath.Clean(d) == filepath.Clean(cwd) })
	if hooks.Folder != nil {
		var environ []string
		dirs, environ = hooks.Folder(cwd, wt, allowed)
		s.ToolEnv.SetExtraEnv(environ)
	}
	s.ToolEnv.SetAdditionalDirs(dirs)
	s.Permissions.SetAdditionalDirectories(dirs)
	s.Agent.SetPlace(map[string]string{"additional_directories": strings.Join(dirs, ", ")}, s.Agent.ProjectContext())
	if hooks.Changed != nil {
		hooks.Changed(notice, workspace.GitBranch(context.Background(), cwd))
	}
	return nil
}

// setBranch records a change of branch or of the worktree's base.
func (s *Session) setBranch(ctx context.Context, branch, baseRef, notice string) {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	cwd := s.Dir()
	s.Transcript.SetLocation(cwd, branch)
	s.placeMu.Lock()
	var wt *session.Worktree
	if s.Worktree != nil {
		w := *s.Worktree
		if branch != "" {
			w.Branch = branch
		}
		if baseRef != "" {
			w.BaseRef = baseRef
		}
		s.Worktree, wt = &w, &w
	}
	s.placeMu.Unlock()
	if wt != nil {
		_, _ = s.Transcript.Append(session.Entry{Type: session.TypeMeta, Cwd: cwd, Worktree: wt})
	}
	if notice != "" {
		_, _ = s.Transcript.Append(session.Entry{Type: session.TypeSystem, Subtype: session.SubtypeSessionMoved, Content: notice, Level: "info"})
	}
	if s.Uploader != nil {
		_ = s.Uploader.UpdateMeta(s.ID, func(m *syncclient.SessionMeta) {
			m.Branch = branch
			m.Worktree = syncWorktree(wt)
		})
	}
	if s.VCS != nil {
		s.VCS.Poke()
	}
	if h := s.placeHooks().Changed; h != nil {
		h(notice, branch)
	}
}

func syncWorktree(w *session.Worktree) *syncclient.Worktree {
	if w == nil {
		return nil
	}
	return &syncclient.Worktree{Path: w.Path, Branch: w.Branch, BaseRef: w.BaseRef, RepositoryPath: w.RepositoryPath}
}

// instructionText is the content of project instructions without the paths they came from: a worktree of the same
// repository has the same instructions.
func instructionText(in []memory.Instruction) string {
	parts := make([]string, len(in))
	for i, x := range in {
		parts[i] = x.Content
	}
	return strings.Join(parts, "\x00")
}

func dedupeDirs(dirs []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, d := range dirs {
		c := filepath.Clean(d)
		if d != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

func orNone(s string) string {
	if s == "" || s == "HEAD" {
		return "(none)"
	}
	return s
}

// folderTrust is the trust of a folder the session moves to: as Open decides it.
func folderTrust(o Options, cwd string) bool {
	switch {
	case o.Trusted != nil:
		return *o.Trusted
	case config.IsChatDir(cwd):
		return true
	}
	return permissions.IsTrusted(cwd)
}
