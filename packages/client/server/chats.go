package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/session"
)

// Sessions without a folder (CreateSessionRequest.cwd empty) run in a private working directory the client
// creates for them: config.ChatDir(<session id>), 0700. It is not a repository and carries no project settings,
// instructions or MCP servers (bootstrap.Options.NoFolder). Everything else works as in a folder: tools, the
// terminal and files panes, listing, resume, fork and sync (the transcript records the private directory as its
// cwd). The environment of such a session is keyed by its private directory, so it is per-session. Deleting the
// session removes the directory and its environment.

// chatFolder is the folder decision of CreateSession.
type chatFolder struct {
	// cwd is the session's folder (the request's, or a private directory).
	cwd string
	// id is the id of the new session when it was chosen here (its private directory is named after it).
	id string
	// noFolder says cwd is a private directory.
	noFolder bool
	// created is the private directory created for this request, removed by rollback.
	created string
}

// rollback removes a private directory created for a session that did not open.
func (c chatFolder) rollback() {
	if c.created != "" {
		_ = os.RemoveAll(c.created)
	}
}

// chatFolder resolves the folder of a CreateSession request. An empty cwd starts a session without a folder,
// resumes a session in its recorded folder, or forks into a new private directory. A fork asked for in the
// private directory of its parent gets its own directory too (seeded with a copy of the parent's files): two
// sessions never share one, so deleting one leaves the other's files alone.
func (s *Server) chatFolder(m *agentv1.CreateSessionRequest, remote *agentv1.SessionInfo) (chatFolder, error) {
	cwd := m.GetCwd()
	resume, fork := m.GetResumeSessionId(), m.GetFork()
	if cwd != "" && !(fork && config.IsChatDir(cwd)) {
		if !config.IsChatDir(cwd) {
			return chatFolder{cwd: cwd}, nil
		}
		// Resuming a folderless session: its directory comes back if it was removed by hand.
		if m.Worktree != nil {
			return chatFolder{}, connect.NewError(connect.CodeInvalidArgument, errors.New("a session without a folder cannot run in a worktree"))
		}
		if err := os.MkdirAll(cwd, 0o700); err != nil {
			return chatFolder{}, connect.NewError(connect.CodeInternal, err)
		}
		return chatFolder{cwd: cwd, noFolder: true}, nil
	}
	if m.Worktree != nil {
		return chatFolder{}, connect.NewError(connect.CodeInvalidArgument, errors.New("worktree requires a folder (cwd)"))
	}
	if resume != "" && !fork {
		// Resume in the recorded folder.
		if !validSessionID(resume) {
			return chatFolder{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid session id %q", resume))
		}
		path, err := session.Store{Root: config.ProjectsDir()}.Find("", resume)
		if err != nil {
			return chatFolder{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s not found", resume))
		}
		rec, _ := transcriptHead(path, false)
		if rec == "" {
			return chatFolder{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s has no recorded folder; pass cwd", resume))
		}
		c := chatFolder{cwd: rec, noFolder: config.IsChatDir(rec)}
		if c.noFolder {
			if err := os.MkdirAll(rec, 0o700); err != nil {
				return chatFolder{}, connect.NewError(connect.CodeInternal, err)
			}
		}
		return c, nil
	}
	// A new session or a fork: a new private directory named after the new session.
	id := session.NewID()
	dir := config.ChatDir(id)
	if err := os.MkdirAll(config.ChatsDir(), 0o700); err != nil {
		return chatFolder{}, connect.NewError(connect.CodeInternal, err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return chatFolder{}, connect.NewError(connect.CodeInternal, err)
	}
	c := chatFolder{cwd: dir, id: id, noFolder: true, created: dir}
	if fork && remote == nil {
		src := cwd
		if src == "" && validSessionID(resume) {
			src = config.ChatDir(resume)
		}
		if config.IsChatDir(src) {
			if err := copyChatDir(src, dir); err != nil {
				s.log().Warn("fork: copying the parent's private directory failed", "from", src, "err", err)
			}
		}
	}
	return c, nil
}

// copyChatDir copies the files of a private directory into a new one (forks).
func copyChatDir(src, dst string) error {
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		return nil // the parent had no files
	}
	return os.CopyFS(dst, os.DirFS(src))
}

// removeChatDir removes the private directory of a folderless session and its environment (DeleteSession).
func (s *Server) removeChatDir(id string) {
	dir := config.ChatDir(id)
	if _, err := os.Stat(dir); err != nil {
		return
	}
	// The environment first: its key resolves symlinks of the directory while it exists.
	if err := s.environments().Delete(dir); err != nil {
		s.log().Warn("delete session: environment not removed", "session", id, "err", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		s.log().Warn("delete session: private directory not removed", "session", id, "err", err)
	}
}

// isNoFolder reports whether a session runs in a private directory: this client's (config.ChatDir), or, for a
// session of another machine, a path ending in chats/<session id>.
func isNoFolder(id, cwd string) bool {
	if cwd == "" {
		return false
	}
	if config.IsChatDir(cwd) {
		return true
	}
	clean := filepath.Clean(cwd)
	return id != "" && filepath.Base(clean) == id && filepath.Base(filepath.Dir(clean)) == "chats"
}
