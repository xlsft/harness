package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/workspace"
	"github.com/xlsft/harness/packages/client/worktree"
)

const (
	defaultRecentFolders = 20
	maxRecentFolders     = 200
	// maxDirectoryEntries caps ListDirectory.
	maxDirectoryEntries = 500
)

// ListRecentFolders returns the folders of the sessions in the local
// transcript store (all projects) and of the open sessions, most recently
// used first and deduplicated by path. Sessions in a harness worktree count
// for the matching folder of the repository's main checkout.
func (s *Server) ListRecentFolders(ctx context.Context, req *connect.Request[agentv1.ListRecentFoldersRequest]) (*connect.Response[agentv1.ListRecentFoldersResponse], error) {
	limit := int(req.Msg.GetLimit())
	if limit <= 0 {
		limit = defaultRecentFolders
	}
	limit = min(limit, maxRecentFolders)

	last := map[string]time.Time{}
	note := func(path string, at time.Time) {
		if path == "" || config.IsChatDir(path) { // folderless sessions have no folder to offer
			return
		}
		if t, ok := last[path]; !ok || at.After(t) {
			last[path] = at
		}
	}
	s.mu.Lock()
	for _, ls := range s.live {
		info := ls.info()
		at, _ := time.Parse(time.RFC3339, info.GetUpdatedAt())
		note(recentFolder(ls.sess.Cwd, ls.sess.Worktree), at)
	}
	s.mu.Unlock()

	for _, f := range recentTranscripts(0) {
		if len(last) >= limit {
			break
		}
		cwd, wt := transcriptHead(f.path, true)
		note(recentFolder(cwd, wt), f.mod)
	}

	out := make([]*agentv1.RecentFolder, 0, len(last))
	for p, at := range last {
		out = append(out, &agentv1.RecentFolder{Path: p, Name: filepath.Base(p), LastUsedAt: rfc3339(at)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastUsedAt != out[j].LastUsedAt {
			return out[i].LastUsedAt > out[j].LastUsedAt
		}
		return out[i].Path < out[j].Path
	})
	if len(out) > limit {
		out = out[:limit]
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, f := range out {
		st, err := os.Stat(f.Path)
		f.Exists = err == nil && st.IsDir()
		if !f.Exists {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			f.GitBranch = workspace.GitBranch(ctx, f.Path)
			if f.GitBranch != "" {
				f.Repo = workspace.Repo(ctx, f.Path)
			}
		}()
	}
	wg.Wait()
	return connect.NewResponse(&agentv1.ListRecentFoldersResponse{Folders: out}), nil
}

// maxListedSessions caps ListSessions without a cwd (all projects).
const maxListedSessions = 200

type transcriptFile struct {
	path string
	mod  time.Time
}

// recentTranscripts returns the local transcripts of all projects, newest first (at most limit; 0 = all).
func recentTranscripts(limit int) []transcriptFile {
	var files []transcriptFile
	matches, _ := filepath.Glob(filepath.Join(config.ProjectsDir(), "*", "*.jsonl"))
	for _, f := range matches {
		if st, err := os.Stat(f); err == nil && st.Mode().IsRegular() {
			files = append(files, transcriptFile{f, st.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}
	return files
}

// recentFolder maps a session directory to the folder a user picked: inside
// a harness worktree, the same subdirectory of the repository's main
// checkout.
func recentFolder(cwd string, wt *session.Worktree) string {
	if cwd != "" {
		cwd = filepath.Clean(cwd)
	}
	if wt != nil && wt.RepositoryPath != "" {
		if cwd != "" && wt.Path != "" {
			if rel, err := filepath.Rel(wt.Path, cwd); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return filepath.Join(wt.RepositoryPath, rel)
			}
		}
		return filepath.Clean(wt.RepositoryPath)
	}
	// Recognize worktree paths without metadata (e.g. forked sessions).
	marker := string(filepath.Separator) + filepath.FromSlash(worktree.Dir) + string(filepath.Separator)
	if i := strings.Index(cwd, marker); i > 0 {
		rest := cwd[i+len(marker):]
		_, sub, _ := strings.Cut(rest, string(filepath.Separator))
		return filepath.Join(cwd[:i], sub)
	}
	return cwd
}

// ListDirectory lists the subdirectories of a folder for a folder browser.
func (s *Server) ListDirectory(ctx context.Context, req *connect.Request[agentv1.ListDirectoryRequest]) (*connect.Response[agentv1.ListDirectoryResponse], error) {
	home, _ := os.UserHomeDir()
	path, err := expandPath(req.Msg.GetPath(), home)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, fsError(path, err)
	}
	if !st.IsDir() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is not a directory", path))
	}
	des, err := os.ReadDir(path)
	if err != nil && len(des) == 0 {
		return nil, fsError(path, err)
	}
	resp := &agentv1.ListDirectoryResponse{Path: path, Home: home}
	if parent := filepath.Dir(path); parent != path {
		resp.Parent = parent
	}
	for _, de := range des {
		name := de.Name()
		if !req.Msg.GetIncludeHidden() && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(path, name)
		if !de.IsDir() {
			// Follow symlinks to directories.
			if de.Type()&fs.ModeSymlink == 0 {
				continue
			}
			if st, err := os.Stat(full); err != nil || !st.IsDir() {
				continue
			}
		}
		_, gerr := os.Lstat(filepath.Join(full, ".git"))
		resp.Entries = append(resp.Entries, &agentv1.DirectoryEntry{Name: name, GitRepository: gerr == nil})
	}
	sort.Slice(resp.Entries, func(i, j int) bool {
		a, b := strings.ToLower(resp.Entries[i].Name), strings.ToLower(resp.Entries[j].Name)
		if a != b {
			return a < b
		}
		return resp.Entries[i].Name < resp.Entries[j].Name
	})
	if len(resp.Entries) > maxDirectoryEntries {
		resp.Entries = resp.Entries[:maxDirectoryEntries]
	}
	return connect.NewResponse(resp), nil
}

// expandPath resolves a ListDirectory path: empty means home, "~" and "~/"
// prefixes expand to home, and the result must be absolute.
func expandPath(p, home string) (string, error) {
	p = strings.TrimSpace(p)
	switch {
	case p == "" || p == "~":
		if home == "" {
			return "", errors.New("the home directory is unknown")
		}
		return home, nil
	case strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~"+string(filepath.Separator)):
		if home == "" {
			return "", errors.New("the home directory is unknown")
		}
		p = filepath.Join(home, p[2:])
	case strings.HasPrefix(p, "~"):
		return "", fmt.Errorf("%s: only ~ and ~/ are expanded", p)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%s: path must be absolute", p)
	}
	return filepath.Clean(p), nil
}

func fsError(path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s does not exist", path))
	case errors.Is(err, fs.ErrPermission):
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s is not readable", path))
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s: %w", path, err))
}

// ListBranches lists the local branches of the repository containing cwd;
// the response is empty outside a repository.
func (s *Server) ListBranches(ctx context.Context, req *connect.Request[agentv1.ListBranchesRequest]) (*connect.Response[agentv1.ListBranchesResponse], error) {
	home, _ := os.UserHomeDir()
	if strings.TrimSpace(req.Msg.GetCwd()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cwd is required"))
	}
	cwd, err := expandPath(req.Msg.GetCwd(), home)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	bl, err := worktree.Branches(ctx, cwd)
	if errors.Is(err, worktree.ErrNotRepository) {
		return connect.NewResponse(&agentv1.ListBranchesResponse{}), nil
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentv1.ListBranchesResponse{RepositoryPath: bl.Repository, Current: bl.Current}
	for _, b := range bl.Branches {
		pb := &agentv1.Branch{Name: b.Name, CheckedOut: b.CheckedOut}
		if !b.CommittedAt.IsZero() {
			pb.CommittedAt = rfc3339(b.CommittedAt)
		}
		resp.Branches = append(resp.Branches, pb)
	}
	return connect.NewResponse(resp), nil
}
