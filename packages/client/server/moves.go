package server

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/environment"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/worktree"
)

// The frontend side of a session that moves itself (the tools of tools/places.go, bootstrap/move.go): its environment
// follows the folder as when it opens there, and SessionUpdated and a notice tell the frontends. No new contract: cwd,
// git_branch and worktree of SessionInfo say it all, and a resumed session finds its way back through the transcript.

func (ls *liveSession) placeHooks() bootstrap.PlaceHooks {
	return bootstrap.PlaceHooks{Folder: ls.environmentAt, Changed: ls.placeChanged, OthersIn: ls.othersIn}
}

// environmentAt returns the additional directories and variables of the session in cwd: what it requested and gained,
// and the environment of cwd's folder (a worktree's is that of its main checkout). A move is not an edit of the
// environment, so nothing is reported here: the session_moved reminder has the directories.
func (ls *liveSession) environmentAt(cwd string, wt *session.Worktree, allowed []string) (dirs, environ []string) {
	key := environment.Key(recentFolder(cwd, wt))
	env, err := ls.srv.environments().Get(key)
	if err != nil {
		ls.srv.log().Warn("environment unavailable", "folder", key, "err", err)
	}
	var add []string
	if env != nil {
		add = existingFolders(env.Additional)
		if vars, err := environment.Parse(env.Env); err == nil {
			environ = environment.Environ(vars)
		}
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.env.folder, ls.env.env = key, add
	ls.env.requested = mergeDirList("", ls.env.requested, allowed)
	dirs = mergeDirList(cwd, ls.env.requested, add)
	ls.env.applied = envFingerprint(append([]string{cwd}, dirs...), environ)
	return dirs, environ
}

// placeChanged tells the frontends that the session moved, gained a directory or changed branch.
func (ls *liveSession) placeChanged(notice, branch string) {
	ls.mu.Lock()
	ls.branch = branch
	ls.mu.Unlock()
	ls.files.reset()
	ls.publishInfo()
	if notice != "" {
		ls.notice(agentv1.Notice_LEVEL_INFO, session.SubtypeSessionMoved, notice)
	}
	ls.refreshDiffStats()
}

// othersIn returns the other open sessions working inside path.
func (ls *liveSession) othersIn(path string) []string {
	s := ls.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, o := range s.live {
		if o != ls && o.sess != nil && pathInside(o.sess.Dir(), path) {
			ids = append(ids, id)
		}
	}
	return ids
}

func pathInside(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// movedFolder returns the folder a session moved itself to, from its transcript: the last move on record, as long as
// the transcript is still in that folder's project directory (SetSessionFolder moves it elsewhere) and the folder
// exists. "" when the session never moved itself: then the request decides.
func movedFolder(id string) string {
	path, err := session.Store{Root: config.ProjectsDir()}.Find("", id)
	if err != nil {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), headMaxLine)
	cwd := ""
	for sc.Scan() {
		if !strings.Contains(sc.Text(), `"moved":true`) {
			continue
		}
		var e struct {
			Type  string `json:"type"`
			Cwd   string `json:"cwd"`
			Moved bool   `json:"moved"`
		}
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Type == session.TypeMeta && e.Moved && e.Cwd != "" {
			cwd = e.Cwd
		}
	}
	if cwd == "" || session.Slug(cwd) != filepath.Base(filepath.Dir(path)) {
		return ""
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return ""
	}
	return cwd
}

// removedWorktreeFile reports whether a path is inside a client-made worktree that no longer exists: its changes left
// with it, and are not deletions the session made.
func removedWorktreeFile(path string) bool {
	marker := string(filepath.Separator) + filepath.FromSlash(worktree.Dir) + string(filepath.Separator)
	i := strings.Index(path, marker)
	if i < 0 {
		return false
	}
	name, _, _ := strings.Cut(path[i+len(marker):], string(filepath.Separator))
	_, err := os.Stat(path[:i+len(marker)] + name)
	return os.IsNotExist(err)
}
