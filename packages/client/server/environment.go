package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/environment"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Environments: a primary folder's name, additional folders, new-session defaults and environment variables
// (package environment). Sessions started in the folder get its additional folders as working directories and
// its variables for the processes tools spawn; an update applies to the open sessions of the folder from
// their next tool call (working directories, variables, and a reminder that tells the model). What stays as
// it was until the session reopens: the environment section of the system prompt, project instructions of
// added folders (they load lazily when files there are read), MCP servers already started, and background
// tasks already running.

// Limits of an environment.
const (
	maxEnvironmentName    = 200
	maxEnvironmentFolders = 50
)

// sessionEnv is the environment state of a live session (liveSession.env, under ls.mu).
type sessionEnv struct {
	// folder is the key of the session's environment (environment.Key of its primary folder).
	folder string
	// requested are the additional directories the session got without its environment (the request, the
	// client's flags and settings); env are the ones its environment added.
	requested, env []string
	// applied is the last state applied (working directories and variables), to tell the model only about changes.
	applied string
	// warning is reported once the session is live (the environment could not be read).
	warning string
}

func (s *Server) environments() *environment.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Environments == nil {
		s.Environments = environment.NewStore(filepath.Join(config.Home(), "environments"))
	}
	return s.Environments
}

// prepareEnvironment adds the environment of folder (the folder the session was asked for) to the options of
// a session about to open: its existing additional folders and its variables. A stored environment that
// cannot be read does not stop the session; it starts without it and reports a warning.
func (s *Server) prepareEnvironment(folder string, opts *bootstrap.Options) sessionEnv {
	senv := sessionEnv{folder: environment.Key(recentFolder(folder, nil)), requested: slices.Clone(opts.AdditionalDirs)}
	if senv.folder == "" {
		return senv
	}
	env, err := s.environments().Get(senv.folder)
	if err != nil {
		s.log().Warn("environment unavailable", "folder", senv.folder, "err", err)
		senv.warning = "The environment of this folder could not be read, so the session runs without it: " + err.Error()
		return senv
	}
	if env == nil {
		return senv
	}
	senv.env = existingFolders(env.Additional)
	opts.AdditionalDirs = mergeDirList(opts.Cwd, senv.requested, senv.env)
	if vars, err := environment.Parse(env.Env); err == nil {
		opts.Env = append(slices.Clone(opts.Env), environment.Environ(vars)...)
	}
	return senv
}

// attachEnvironment records the environment state of a session that just opened.
func (ls *liveSession) attachEnvironment(senv sessionEnv) {
	if senv.warning != "" {
		ls.sess.Warnings = append(ls.sess.Warnings, senv.warning)
	}
	senv.applied = envFingerprint(ls.sess.ToolEnv.WorkingDirs(), ls.sess.ToolEnv.ExtraEnv())
	ls.mu.Lock()
	ls.env = senv
	ls.mu.Unlock()
}

// applyEnvironment applies env (nil: none) to a live session: its working directories become the requested ones
// plus the environment's existing additional folders, and its variables replace the previous ones for the next
// commands. The model is told when something changed.
func (ls *liveSession) applyEnvironment(env *environment.Environment) {
	var add []string
	var vars []environment.Var
	if env != nil {
		add = existingFolders(env.Additional)
		vars, _ = environment.Parse(env.Env)
	}
	ls.mu.Lock()
	ls.env.env = add
	dirs := mergeDirList(ls.sess.Cwd, ls.env.requested, add)
	environ := environment.Environ(vars)
	fp := envFingerprint(append([]string{ls.sess.Cwd}, dirs...), environ)
	changed := fp != ls.env.applied
	ls.env.applied = fp
	ls.mu.Unlock()
	ls.sess.ToolEnv.SetAdditionalDirs(dirs)
	ls.sess.Permissions.SetAdditionalDirectories(dirs)
	ls.sess.ToolEnv.SetExtraEnv(environ)
	if !changed {
		return
	}
	names := make([]string, len(vars))
	for i, v := range vars {
		names[i] = v.Name
	}
	list := "(none)"
	if len(names) > 0 {
		list = strings.Join(names, ", ")
	}
	ls.sess.Agent.Remind("environment_changed", map[string]string{
		"directories": strings.Join(append([]string{ls.sess.Cwd}, dirs...), "\n"),
		"variables":   list,
	})
	ls.notice(agentv1.Notice_LEVEL_INFO, "environment_applied", fmt.Sprintf(
		"Environment updated: %d working director%s, %d variable%s. It applies from the next tool call; background tasks already running and MCP servers keep their environment.",
		len(dirs)+1, plural(len(dirs)+1, "y", "ies"), len(names), plural(len(names), "", "s")))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// envFingerprint identifies a state of working directories and variables (kept in memory only).
func envFingerprint(dirs, environ []string) string {
	return strings.Join(dirs, "\x00") + "\x01" + strings.Join(environ, "\x00")
}

// mergeDirList returns the directories of lists in order, without duplicates and without cwd.
func mergeDirList(cwd string, lists ...[]string) []string {
	seen := map[string]bool{filepath.Clean(cwd): true}
	var out []string
	for _, l := range lists {
		for _, d := range l {
			c := filepath.Clean(d)
			if d == "" || seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, d)
		}
	}
	return out
}

// existingFolders returns the paths of the folders that exist as directories.
func existingFolders(fs []environment.Folder) []string {
	var out []string
	for _, f := range fs {
		if st, err := os.Stat(f.Path); err == nil && st.IsDir() {
			out = append(out, f.Path)
		}
	}
	return out
}

// applyToFolder applies env to the live sessions whose environment is folder and returns their ids.
func (s *Server) applyToFolder(folder string, env *environment.Environment) []string {
	s.mu.Lock()
	var targets []*liveSession
	for _, ls := range s.live {
		ls.mu.Lock()
		match := ls.env.folder == folder
		ls.mu.Unlock()
		if match {
			targets = append(targets, ls)
		}
	}
	s.mu.Unlock()
	ids := make([]string, 0, len(targets))
	for _, ls := range targets {
		ls.applyEnvironment(env)
		ids = append(ids, ls.sess.ID)
	}
	slices.Sort(ids)
	return ids
}

// sessionFolder returns the environment key of a local session (live, or from its transcript).
func (s *Server) sessionFolder(id string) (string, error) {
	s.mu.Lock()
	ls := s.live[id]
	s.mu.Unlock()
	if ls != nil {
		ls.mu.Lock()
		defer ls.mu.Unlock()
		return ls.env.folder, nil
	}
	if m := readAcpMeta(id); m != nil {
		return environment.Key(recentFolder(m.Cwd, nil)), nil
	}
	path, err := session.Store{Root: config.ProjectsDir()}.Find("", id)
	if err != nil {
		return "", connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s not found", id))
	}
	cwd, wt := transcriptHead(path, false)
	if cwd == "" {
		return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s has no folder yet", id))
	}
	return environment.Key(recentFolder(cwd, wt)), nil
}

// remoteReadOnly is the error for edits of a remote session's environment or folders.
func remoteReadOnly(info *agentv1.SessionInfo) error {
	return connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("session %s runs on %s: its environment and folders can only be changed there", info.Id, deviceLabel(info)))
}

// GetEnvironment returns the environment of a folder or of a session's primary folder.
func (s *Server) GetEnvironment(ctx context.Context, req *connect.Request[agentv1.GetEnvironmentRequest]) (*connect.Response[agentv1.GetEnvironmentResponse], error) {
	var folder string
	switch id := req.Msg.GetSessionId(); {
	case id != "":
		if info := s.remoteInfo(id); info != nil {
			return connect.NewResponse(&agentv1.GetEnvironmentResponse{Environment: &agentv1.Environment{Folder: info.Cwd, ReadOnly: true}}), nil
		}
		f, err := s.sessionFolder(id)
		if err != nil {
			return nil, err
		}
		folder = f
	default:
		f, err := folderArg(req.Msg.GetFolder())
		if err != nil {
			return nil, err
		}
		folder = environment.Key(f)
	}
	env, err := s.environments().Get(folder)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&agentv1.GetEnvironmentResponse{Environment: environmentToProto(ctx, folder, env)}), nil
}

// UpdateEnvironment replaces the environment of a folder and applies it to the folder's open sessions.
func (s *Server) UpdateEnvironment(ctx context.Context, req *connect.Request[agentv1.UpdateEnvironmentRequest]) (*connect.Response[agentv1.UpdateEnvironmentResponse], error) {
	if id := req.Msg.GetSessionId(); id != "" {
		if info := s.remoteInfo(id); info != nil {
			return nil, remoteReadOnly(info)
		}
	}
	pe := req.Msg.GetEnvironment()
	if pe == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("environment is required"))
	}
	env, err := environmentFromProto(pe)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetValidateOnly() {
		return connect.NewResponse(&agentv1.UpdateEnvironmentResponse{Environment: environmentToProto(ctx, env.Folder, nil, env)}), nil
	}
	stored, err := s.environments().Put(*env)
	if err != nil {
		return nil, envError(err)
	}
	ids := s.applyToFolder(stored.Folder, stored)
	return connect.NewResponse(&agentv1.UpdateEnvironmentResponse{Environment: environmentToProto(ctx, stored.Folder, stored), AppliedSessionIds: ids}), nil
}

// AddSessionDirectory adds a working directory to an open session's environment and applies it.
func (s *Server) AddSessionDirectory(ctx context.Context, req *connect.Request[agentv1.AddSessionDirectoryRequest]) (*connect.Response[agentv1.AddSessionDirectoryResponse], error) {
	id := req.Msg.GetSessionId()
	if info := s.remoteInfo(id); info != nil {
		return nil, remoteReadOnly(info)
	}
	ls, err := s.get(id)
	if err != nil {
		return nil, err
	}
	dir, err := existingDir(req.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	ls.mu.Lock()
	folder := ls.env.folder
	ls.mu.Unlock()
	if folder == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the session has no folder"))
	}
	store := s.environments()
	env, err := store.Get(folder)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if env == nil {
		env = &environment.Environment{Folder: folder}
	}
	if environment.Key(dir) != folder && !slices.ContainsFunc(env.Additional, func(f environment.Folder) bool { return filepath.Clean(f.Path) == dir }) {
		if len(env.Additional) >= maxEnvironmentFolders {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("an environment has at most %d additional folders", maxEnvironmentFolders))
		}
		env.Additional = append(env.Additional, environment.Folder{Path: dir})
	}
	stored, err := store.Put(*env)
	if err != nil {
		return nil, envError(err)
	}
	ids := s.applyToFolder(folder, stored)
	return connect.NewResponse(&agentv1.AddSessionDirectoryResponse{Environment: environmentToProto(ctx, folder, stored), AppliedSessionIds: ids}), nil
}

// GetFolderInfo returns the git facts of a folder for its menu.
func (s *Server) GetFolderInfo(ctx context.Context, req *connect.Request[agentv1.GetFolderInfoRequest]) (*connect.Response[agentv1.GetFolderInfoResponse], error) {
	path, err := folderArg(req.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	resp := &agentv1.GetFolderInfoResponse{Path: path}
	st, err := os.Stat(path)
	resp.Exists = err == nil && st.IsDir()
	if !resp.Exists {
		return connect.NewResponse(resp), nil
	}
	if resp.RepositoryPath = workspace.GitRoot(ctx, path); resp.RepositoryPath == "" {
		return connect.NewResponse(resp), nil
	}
	if b := workspace.GitBranch(ctx, path); b != "HEAD" {
		resp.CurrentBranch = b
	}
	resp.DefaultBranch = workspace.DefaultBranch(ctx, path)
	resp.RemoteUrl = workspace.GitRemote(ctx, path)
	resp.WebUrl = workspace.RemoteWebURL(resp.RemoteUrl)
	resp.Repo = workspace.NormalizeRemote(resp.RemoteUrl)
	return connect.NewResponse(resp), nil
}

// SetSessionFolder moves an idle session to another primary folder: the session is closed, its transcript
// moves to the new folder's project directory, and it is resumed there with the same model, permission mode
// and effort. If reopening fails, the transcript moves back and the session is reopened where it was.
func (s *Server) SetSessionFolder(ctx context.Context, req *connect.Request[agentv1.SetSessionFolderRequest]) (*connect.Response[agentv1.SetSessionFolderResponse], error) {
	id := req.Msg.GetSessionId()
	if info := s.remoteInfo(id); info != nil {
		return nil, remoteReadOnly(info)
	}
	cwd, err := existingDir(req.Msg.GetCwd())
	if err != nil {
		return nil, err
	}
	store := session.Store{Root: config.ProjectsDir()}
	s.mu.Lock()
	ls := s.live[id]
	s.mu.Unlock()
	reopen := &agentv1.CreateSessionRequest{ResumeSessionId: id}
	var oldCwd string
	if ls != nil {
		if ls.sess.Worktree != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the session runs in a git worktree; start a new session in the other folder instead"))
		}
		ls.mu.Lock()
		busy := ls.statusLocked() != agentv1.SessionStatus_SESSION_STATUS_IDLE || len(ls.queue) > 0
		ls.mu.Unlock()
		if busy || ls.driver.Busy() {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the session is working; change its folder once it is idle"))
		}
		oldCwd = ls.sess.Cwd
		reopen.Model = ls.sess.Agent.Model()
		reopen.PermissionMode = modeToProto(ls.sess.Permissions.Mode())
		reopen.Effort = pbconv.Effort(ls.sess.Agent.Effort())
	} else {
		path, err := store.Find("", id)
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s not found", id))
		}
		var wt *session.Worktree
		if oldCwd, wt = transcriptHead(path, false); wt != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the session runs in a git worktree; start a new session in the other folder instead"))
		}
	}
	if oldCwd != "" && environment.Key(oldCwd) == environment.Key(cwd) {
		if ls != nil {
			return connect.NewResponse(&agentv1.SetSessionFolderResponse{Session: ls.info()}), nil
		}
		cwd = oldCwd
	}
	if ls != nil {
		s.closeLive(id, "folder_changed")
	}
	src, err := store.Find(oldCwd, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s not found", id))
	}
	dst := store.Path(cwd, id)
	moved := false
	if src != dst {
		if err := moveTranscript(src, dst); err != nil {
			s.reopenAfterFailedMove(ctx, ls, oldCwd, reopen)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("move the transcript: %w", err))
		}
		moved = true
	}
	reopen.Cwd = cwd
	resp, err := s.CreateSession(ctx, connect.NewRequest(reopen))
	if err != nil {
		if moved {
			if merr := moveTranscript(dst, src); merr != nil {
				s.log().Warn("move a transcript back after a failed folder change", "session", id, "err", merr)
			}
		}
		s.reopenAfterFailedMove(ctx, ls, oldCwd, reopen)
		return nil, err
	}
	if nls, err := s.get(id); err == nil && moved {
		// Readers of the transcript (ListSessions without a cwd) take the folder from the last recorded cwd of
		// a transcript that is not in its first folder's project directory.
		_, _ = nls.sess.Transcript.Append(session.Entry{Type: session.TypeMeta, Cwd: nls.sess.Cwd})
	}
	return connect.NewResponse(&agentv1.SetSessionFolderResponse{Session: resp.Msg.Session}), nil
}

// reopenAfterFailedMove resumes a session that was live before a failed folder change in its old folder.
func (s *Server) reopenAfterFailedMove(ctx context.Context, was *liveSession, oldCwd string, reopen *agentv1.CreateSessionRequest) {
	if was == nil || oldCwd == "" {
		return
	}
	r := proto.Clone(reopen).(*agentv1.CreateSessionRequest)
	r.Cwd = oldCwd
	if _, err := s.CreateSession(ctx, connect.NewRequest(r)); err != nil {
		s.log().Warn("reopen a session after a failed folder change", "session", reopen.ResumeSessionId, "err", err)
	}
}

// moveTranscript moves a transcript file and its sibling directory (same name without .jsonl) when present.
func moveTranscript(src, dst string) error {
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

// folderArg resolves a folder argument: required, "~" expanded, absolute.
func folderArg(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("folder is required"))
	}
	home, _ := os.UserHomeDir()
	out, err := expandPath(p, home)
	if err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	return out, nil
}

// existingDir resolves a folder argument that must be an existing directory.
func existingDir(p string) (string, error) {
	dir, err := folderArg(p)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(dir)
	if err != nil {
		return "", fsError(dir, err)
	}
	if !st.IsDir() {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is not a directory", dir))
	}
	return dir, nil
}

// envError maps a store error: invalid env text becomes INVALID_ARGUMENT with an EnvironmentErrors detail.
func envError(err error) error {
	var pe *environment.ParseError
	if !errors.As(err, &pe) {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	ce := connect.NewError(connect.CodeInvalidArgument, err)
	detail := &agentv1.EnvironmentErrors{}
	for _, le := range pe.Errors {
		detail.Errors = append(detail.Errors, &agentv1.EnvironmentError{Line: int32(le.Line), Message: le.Message})
	}
	if d, derr := connect.NewErrorDetail(detail); derr == nil {
		ce.AddDetail(d)
	}
	return ce
}

// environmentFromProto validates an environment from a request (env text included).
func environmentFromProto(pe *agentv1.Environment) (*environment.Environment, error) {
	folder, err := folderArg(pe.GetFolder())
	if err != nil {
		return nil, err
	}
	key := environment.Key(folder)
	env := &environment.Environment{Folder: key, Name: strings.TrimSpace(pe.GetName()), Worktree: pe.GetWorktree(), Env: pe.GetEnv()}
	if len([]rune(env.Name)) > maxEnvironmentName {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is longer than %d characters", maxEnvironmentName))
	}
	if env.BaseRef, err = baseRefArg(pe.GetBaseRef()); err != nil {
		return nil, err
	}
	if len(pe.GetAdditionalFolders()) > maxEnvironmentFolders {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("an environment has at most %d additional folders", maxEnvironmentFolders))
	}
	seen := map[string]bool{key: true}
	for _, f := range pe.GetAdditionalFolders() {
		p, err := folderArg(f.GetPath())
		if err != nil {
			return nil, err
		}
		if seen[p] || seen[environment.Key(p)] {
			continue
		}
		seen[p] = true
		ref, err := baseRefArg(f.GetBaseRef())
		if err != nil {
			return nil, err
		}
		env.Additional = append(env.Additional, environment.Folder{Path: p, Worktree: f.GetWorktree(), BaseRef: ref})
	}
	if _, err := environment.Parse(env.Env); err != nil {
		return nil, envError(err)
	}
	return env, nil
}

// baseRefArg checks a branch or commit name loosely (git checks it when a worktree is created).
func baseRefArg(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref != "" && (strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, " \t\n\x00~^:?*[\\")) {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid base branch %q", ref))
	}
	return ref, nil
}

// environmentToProto converts an environment (nil: defaults for folder). draft, when given, is an unsaved
// environment to show instead (validate_only).
func environmentToProto(ctx context.Context, folder string, env *environment.Environment, draft ...*environment.Environment) *agentv1.Environment {
	out := &agentv1.Environment{Folder: folder}
	if len(draft) > 0 && draft[0] != nil {
		env = draft[0]
	} else if env != nil {
		out.Exists = true
		out.UpdatedAt = env.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if env == nil {
		return out
	}
	out.Name, out.Worktree, out.BaseRef, out.Env = env.Name, env.Worktree, env.BaseRef, env.Env
	out.VariableNames = environment.Names(env.Env)
	for _, f := range env.Additional {
		pf := &agentv1.EnvironmentFolder{Path: f.Path, Worktree: f.Worktree, BaseRef: f.BaseRef}
		if st, err := os.Stat(f.Path); err == nil && st.IsDir() {
			pf.Exists = true
			if b := workspace.GitBranch(ctx, f.Path); b != "HEAD" {
				pf.GitBranch = b
			}
		}
		out.AdditionalFolders = append(out.AdditionalFolders, pf)
	}
	return out
}
