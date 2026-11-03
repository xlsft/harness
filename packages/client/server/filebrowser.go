package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	gitignore "github.com/sabhiram/go-gitignore"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/permissions"
)

// Files (files.proto): the files pane browses a session's working directories. Every path must resolve, symlinks
// followed, inside one of them; listings carry git status and .gitignore marks, reads are capped.

const (
	// maxListEntries caps one directory listing (and GetGitStatus's paths).
	maxListEntries = 5000
	// maxReadText is the most text ReadFile returns (longer files are cut); maxReadImage the largest image it
	// returns at all.
	maxReadText  = 2 << 20
	maxReadImage = 10 << 20
	// sniffBytes are read to tell text from binary.
	sniffBytes = 8000
	// fileGitTimeout bounds each git command; gitStatusTTL is how long a repository's status is reused, so
	// expanding several folders runs git status once.
	fileGitTimeout = 3 * time.Second
	gitStatusTTL   = 2 * time.Second
)

// alwaysIgnored are hidden like .gitignore'd entries everywhere, in a repository or not.
var alwaysIgnored = map[string]bool{".git": true, "node_modules": true, ".DS_Store": true}

// fileScope is what a session may browse: its cwd (relative paths start there) and working directories.
type fileScope struct {
	cwd  string
	dirs []string
}

// fileScope returns the working directories of a live session of this machine.
func (s *Server) fileScope(id string) (*fileScope, error) {
	s.mu.Lock()
	ls := s.live[id]
	s.mu.Unlock()
	if ls == nil {
		if remote := s.remoteInfo(id); remote != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s runs on %s; its files can only be browsed on that machine", id, deviceLabel(remote)))
		}
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s is not open; call CreateSession first", id))
	}
	sc := &fileScope{cwd: ls.sess.Cwd, dirs: []string{ls.sess.Cwd}}
	if ls.sess.Permissions != nil {
		if d := ls.sess.Permissions.WorkingDirs(); len(d) > 0 {
			sc.dirs = d
		}
	}
	return sc, nil
}

// resolve turns p (absolute, relative to the cwd, or empty for the cwd) into a clean absolute path, and its
// real path (symlinks resolved), which must be inside the working directories. A path that does not exist
// is NOT_FOUND; one outside is PERMISSION_DENIED.
func (sc *fileScope) resolve(p string) (clean, real string, err error) {
	p = strings.TrimSpace(p)
	switch {
	case p == "":
		p = sc.cwd
	case strings.HasPrefix(p, "~"):
		return "", "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s: use an absolute path or one relative to the session's directory", p))
	case !filepath.IsAbs(p):
		p = filepath.Join(sc.cwd, p)
	}
	clean = filepath.Clean(p)
	if !permissions.WithinAny(clean, sc.dirs) {
		return "", "", outsideError(clean)
	}
	real, err = filepath.EvalSymlinks(clean)
	if err != nil {
		return "", "", fsError(clean, err)
	}
	if !permissions.WithinAny(real, sc.dirs) {
		return "", "", outsideError(clean)
	}
	return clean, real, nil
}

func outsideError(p string) error {
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s is outside the session's working directories", p))
}

func (s *Server) ListFileRoots(ctx context.Context, req *connect.Request[agentv1.ListFileRootsRequest]) (*connect.Response[agentv1.ListFileRootsResponse], error) {
	sc, err := s.fileScope(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	resp := &agentv1.ListFileRootsResponse{}
	seen := map[string]bool{}
	for _, d := range sc.dirs {
		d = filepath.Clean(d)
		if seen[d] {
			continue
		}
		seen[d] = true
		root := &agentv1.FileRoot{Path: d, Name: filepath.Base(d)}
		if real, err := filepath.EvalSymlinks(d); err == nil {
			if top := gitTopLevel(ctx, real); top != "" {
				root.GitRoot = lexicalPath(d, real, top)
			}
		}
		resp.Roots = append(resp.Roots, root)
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) ListFiles(ctx context.Context, req *connect.Request[agentv1.ListFilesRequest]) (*connect.Response[agentv1.ListFilesResponse], error) {
	sc, err := s.fileScope(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	dir, real, err := sc.resolve(req.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	resp, err := listDir(ctx, sc, dir, real, req.Msg.GetShowIgnored())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// listDir lists the directory dir (real: its resolved path) for the files pane.
func listDir(ctx context.Context, sc *fileScope, dir, real string, showIgnored bool) (*agentv1.ListFilesResponse, error) {
	st, err := os.Stat(real)
	if err != nil {
		return nil, fsError(dir, err)
	}
	if !st.IsDir() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is not a directory", dir))
	}
	des, err := os.ReadDir(real)
	if err != nil {
		return nil, fsError(dir, err)
	}
	resp := &agentv1.ListFilesResponse{Path: dir}
	entries := make([]*agentv1.FileEntry, 0, len(des))
	for _, de := range des {
		entries = append(entries, fileEntry(sc, dir, real, de))
	}

	repo := repoStatus(ctx, real)
	markIgnored(ctx, sc, real, repo != nil, entries)
	if repo != nil {
		rel := relSlash(repo.root, real)
		for _, e := range entries {
			e.GitStatus = repo.status(joinRel(rel, e.Name))
		}
		// Tracked files deleted from this directory.
		have := map[string]bool{}
		for _, e := range entries {
			have[e.Name] = true
		}
		for _, p := range repo.deletedIn(rel) {
			name := p[strings.LastIndex(p, "/")+1:]
			if !have[name] {
				entries = append(entries, &agentv1.FileEntry{
					Name: name, Path: filepath.Join(dir, name), Kind: agentv1.FileKind_FILE_KIND_FILE,
					GitStatus: agentv1.GitFileStatus_GIT_FILE_STATUS_DELETED, Deleted: true,
				})
			}
		}
	}

	out := entries[:0]
	for _, e := range entries {
		if showIgnored || !e.Ignored {
			out = append(out, e)
		}
	}
	sortEntries(out)
	if len(out) > maxListEntries {
		out, resp.Truncated = out[:maxListEntries], true
	}
	resp.Entries = out
	return resp, nil
}

func fileEntry(sc *fileScope, dir, real string, de fs.DirEntry) *agentv1.FileEntry {
	e := &agentv1.FileEntry{Name: de.Name(), Path: filepath.Join(dir, de.Name()), Kind: agentv1.FileKind_FILE_KIND_OTHER}
	info, err := de.Info()
	if err != nil {
		return e
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		e.Symlink = true
		e.Kind = agentv1.FileKind_FILE_KIND_SYMLINK
		target, terr := filepath.EvalSymlinks(filepath.Join(real, de.Name()))
		if terr != nil || !permissions.WithinAny(target, sc.dirs) {
			e.ModifiedAt = rfc3339(info.ModTime())
			return e
		}
		if ti, err := os.Stat(target); err == nil {
			info = ti
		}
	}
	switch {
	case info.IsDir():
		e.Kind = agentv1.FileKind_FILE_KIND_DIRECTORY
	case info.Mode().IsRegular():
		e.Kind = agentv1.FileKind_FILE_KIND_FILE
		e.Size = info.Size()
	}
	e.ModifiedAt = rfc3339(info.ModTime())
	return e
}

func sortEntries(es []*agentv1.FileEntry) {
	sort.SliceStable(es, func(i, j int) bool {
		di, dj := es[i].Kind == agentv1.FileKind_FILE_KIND_DIRECTORY, es[j].Kind == agentv1.FileKind_FILE_KIND_DIRECTORY
		if di != dj {
			return di
		}
		li, lj := strings.ToLower(es[i].Name), strings.ToLower(es[j].Name)
		if li != lj {
			return li < lj
		}
		return es[i].Name < es[j].Name
	})
}

// markIgnored sets FileEntry.ignored: .git / node_modules always, then .gitignore rules (git check-ignore in a
// repository, the .gitignore files from the working directory down to dir outside one).
func markIgnored(ctx context.Context, sc *fileScope, real string, inRepo bool, entries []*agentv1.FileEntry) {
	var rest []*agentv1.FileEntry
	for _, e := range entries {
		if alwaysIgnored[e.Name] {
			e.Ignored = true
		} else if !e.Deleted {
			rest = append(rest, e)
		}
	}
	if len(rest) == 0 {
		return
	}
	names := make([]string, len(rest))
	for i, e := range rest {
		names[i] = e.Name
		if e.Kind == agentv1.FileKind_FILE_KIND_DIRECTORY {
			names[i] += "/"
		}
	}
	var ignored map[string]bool
	if inRepo {
		ignored = gitCheckIgnore(ctx, real, names)
	} else {
		ignored = gitignoreMatches(ignoreBase(sc, real), real, names)
	}
	for i, e := range rest {
		if ignored[names[i]] {
			e.Ignored = true
		}
	}
}

// gitCheckIgnore returns which of names (relative to dir; directories with a trailing slash) git ignores.
func gitCheckIgnore(ctx context.Context, dir string, names []string) map[string]bool {
	ctx, cancel := context.WithTimeout(ctx, fileGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "check-ignore", "-z", "--stdin")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	cmd.Stdin = strings.NewReader(strings.Join(names, "\x00") + "\x00")
	out, _ := cmd.Output() // exit status 1: nothing is ignored
	res := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			res[p] = true
		}
	}
	return res
}

// ignoreBase is the working directory containing real (resolved), where .gitignore files start applying.
func ignoreBase(sc *fileScope, real string) string {
	best := ""
	for _, d := range sc.dirs {
		rd, err := filepath.EvalSymlinks(d)
		if err != nil {
			continue
		}
		if r, err := filepath.Rel(rd, real); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && len(rd) > len(best) {
			best = rd
		}
	}
	if best == "" {
		return real
	}
	return best
}

// gitignoreMatches applies the .gitignore files of base and every directory down to dir to names (relative to
// dir; directories with a trailing slash).
func gitignoreMatches(base, dir string, names []string) map[string]bool {
	type ign struct {
		dir string
		m   *gitignore.GitIgnore
	}
	var chain []ign
	for d := dir; ; d = filepath.Dir(d) {
		if m, err := gitignore.CompileIgnoreFile(filepath.Join(d, ".gitignore")); err == nil {
			chain = append(chain, ign{d, m})
		}
		if d == base || filepath.Dir(d) == d {
			break
		}
	}
	res := map[string]bool{}
	for _, n := range names {
		for _, ig := range chain {
			rel := relSlash(ig.dir, filepath.Join(dir, n))
			if strings.HasSuffix(n, "/") {
				rel += "/"
			}
			if ig.m.MatchesPath(rel) {
				res[n] = true
				break
			}
		}
	}
	return res
}

func (s *Server) ReadFile(ctx context.Context, req *connect.Request[agentv1.ReadFileRequest]) (*connect.Response[agentv1.ReadFileResponse], error) {
	sc, err := s.fileScope(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Msg.GetPath()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("path is required"))
	}
	p, real, err := sc.resolve(req.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	resp, err := readFile(p, real, req.Msg.GetMaxBytes(), req.Msg.GetIfNoneMatch())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// imageMimes are the image types ReadFile returns as bytes (SVG is text: it comes back as text).
var imageMimes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".bmp": "image/bmp", ".ico": "image/x-icon", ".avif": "image/avif",
}

// textMimes name common text files mime.TypeByExtension may not know.
var textMimes = map[string]string{
	".md": "text/markdown", ".markdown": "text/markdown", ".mdx": "text/markdown", ".svg": "image/svg+xml",
	".go": "text/x-go", ".ts": "text/typescript", ".tsx": "text/typescript", ".vue": "text/x-vue",
	".py": "text/x-python", ".rs": "text/x-rust", ".json": "application/json", ".yaml": "text/yaml",
	".yml": "text/yaml", ".toml": "text/x-toml", ".proto": "text/x-protobuf", ".sh": "text/x-shellscript",
}

// readFile reads p (real: its resolved path) within the limits: text up to maxReadText (cut beyond it), images
// up to maxReadImage (nothing beyond it), nothing for other binary files. maxBytes > 0 lowers both limits.
func readFile(p, real string, maxBytes int64, ifNoneMatch string) (*agentv1.ReadFileResponse, error) {
	st, err := os.Stat(real)
	if err != nil {
		return nil, fsError(p, err)
	}
	if st.IsDir() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is a directory", p))
	}
	if !st.Mode().IsRegular() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is not a regular file", p))
	}
	resp := &agentv1.ReadFileResponse{
		Path: p, Size: st.Size(), ModifiedAt: rfc3339(st.ModTime()),
		Etag: fmt.Sprintf("%x-%x", st.Size(), st.ModTime().UnixNano()),
	}
	if ifNoneMatch != "" && ifNoneMatch == resp.Etag {
		resp.NotModified = true
		return resp, nil
	}
	textLimit, imageLimit := int64(maxReadText), int64(maxReadImage)
	if maxBytes > 0 {
		textLimit, imageLimit = min(textLimit, maxBytes), min(imageLimit, maxBytes)
	}

	f, err := os.Open(real)
	if err != nil {
		return nil, fsError(p, err)
	}
	defer f.Close()
	head := make([]byte, sniffBytes)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fsError(p, err)
	}
	head = head[:n]

	ext := strings.ToLower(filepath.Ext(p))
	if m, ok := imageMimes[ext]; ok {
		resp.Mime, resp.IsBinary = m, true
		if st.Size() > imageLimit {
			resp.TooLarge = true
			return resp, nil
		}
		data, err := readRest(f, head, st.Size())
		if err != nil {
			return nil, fsError(p, err)
		}
		resp.Data = data
		return resp, nil
	}

	resp.Mime = fileMime(ext, head)
	if isBinary(head, int64(n) < st.Size()) {
		resp.IsBinary = true
		if !strings.HasPrefix(resp.Mime, "image/") && !strings.HasPrefix(resp.Mime, "application/") {
			resp.Mime = "application/octet-stream"
		}
		return resp, nil
	}
	data, err := readRest(f, head, min(st.Size(), textLimit))
	if err != nil {
		return nil, fsError(p, err)
	}
	if int64(len(data)) < st.Size() {
		resp.Truncated = true
		data = trimPartialRune(data)
	}
	if !utf8.Valid(data) {
		resp.IsBinary, resp.Mime = true, "application/octet-stream"
		return resp, nil
	}
	resp.Text = string(data)
	return resp, nil
}

// readRest returns head followed by the rest of f, total bytes in all.
func readRest(f *os.File, head []byte, total int64) ([]byte, error) {
	if int64(len(head)) >= total {
		return head[:total], nil
	}
	buf := make([]byte, total)
	copy(buf, head)
	n, err := io.ReadFull(f, buf[len(head):])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:len(head)+n], nil
}

// isBinary tells binary content from text by its first bytes: a NUL byte, or bytes that are not UTF-8 (a rune
// cut at the end of a partial read does not count).
func isBinary(head []byte, partial bool) bool {
	if bytes.IndexByte(head, 0) >= 0 {
		return true
	}
	if partial {
		head = trimPartialRune(head)
	}
	return !utf8.Valid(head)
}

// trimPartialRune drops an incomplete UTF-8 sequence at the end of b.
func trimPartialRune(b []byte) []byte {
	for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
		c := b[len(b)-i]
		if c < utf8.RuneSelf {
			return b
		}
		if utf8.RuneStart(c) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			return b
		}
	}
	return b
}

func fileMime(ext string, head []byte) string {
	if m, ok := textMimes[ext]; ok {
		return m
	}
	if m := mime.TypeByExtension(ext); m != "" {
		if i := strings.IndexByte(m, ';'); i >= 0 {
			m = m[:i]
		}
		return m
	}
	m := http.DetectContentType(head)
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = m[:i]
	}
	return m
}

func (s *Server) GetGitStatus(ctx context.Context, req *connect.Request[agentv1.GetGitStatusRequest]) (*connect.Response[agentv1.GetGitStatusResponse], error) {
	sc, err := s.fileScope(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	dir, real, err := sc.resolve(req.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(real); err != nil {
		return nil, fsError(dir, err)
	} else if !st.IsDir() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is not a directory", dir))
	}
	resp := &agentv1.GetGitStatusResponse{Files: map[string]agentv1.GitFileStatus{}}
	repo := repoStatus(ctx, real)
	if repo == nil {
		return connect.NewResponse(resp), nil
	}
	root := lexicalPath(dir, real, repo.root)
	resp.Root = root
	keys := make([]string, 0, len(repo.files)+len(repo.untrackedDirs))
	for p := range repo.files {
		keys = append(keys, p)
	}
	keys = append(keys, repo.untrackedDirs...)
	sort.Strings(keys)
	for i, p := range keys {
		if i >= maxListEntries {
			resp.Truncated = true
			break
		}
		st, ok := repo.files[p]
		if !ok {
			st = agentv1.GitFileStatus_GIT_FILE_STATUS_UNTRACKED
		}
		resp.Files[filepath.Join(root, filepath.FromSlash(p))] = st
	}
	return connect.NewResponse(resp), nil
}

// gitRepoStatus is the parsed `git status` of a repository: paths relative to root, slash-separated.
type gitRepoStatus struct {
	root  string
	files map[string]agentv1.GitFileStatus
	// Untracked directories as git reports them (without listing their files), without the trailing slash.
	untrackedDirs []string
	at            time.Time
}

// status returns the status of rel (a file or directory, relative to the root).
func (r *gitRepoStatus) status(rel string) agentv1.GitFileStatus {
	if st, ok := r.files[rel]; ok {
		return st
	}
	for _, d := range r.untrackedDirs {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			return agentv1.GitFileStatus_GIT_FILE_STATUS_UNTRACKED
		}
	}
	return agentv1.GitFileStatus_GIT_FILE_STATUS_UNSPECIFIED
}

// deletedIn returns the deleted files directly inside the directory rel ("" for the root).
func (r *gitRepoStatus) deletedIn(rel string) []string {
	var out []string
	for p, st := range r.files {
		if st != agentv1.GitFileStatus_GIT_FILE_STATUS_DELETED {
			continue
		}
		parent := ""
		if i := strings.LastIndex(p, "/"); i >= 0 {
			parent = p[:i]
		}
		if parent == rel {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

var gitStatusCache = struct {
	mu    sync.Mutex
	repos map[string]*gitRepoStatus
}{repos: map[string]*gitRepoStatus{}}

// repoStatus returns the status of the repository containing dir (resolved), reused for gitStatusTTL; nil
// outside a repository or when git fails.
func repoStatus(ctx context.Context, dir string) *gitRepoStatus {
	top := gitTopLevel(ctx, dir)
	if top == "" {
		return nil
	}
	gitStatusCache.mu.Lock()
	if r := gitStatusCache.repos[top]; r != nil && time.Since(r.at) < gitStatusTTL {
		gitStatusCache.mu.Unlock()
		return r
	}
	gitStatusCache.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, fileGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", top, "status", "--porcelain=v1", "-z", "--untracked-files=normal", "--ignore-submodules=dirty")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	files, untracked := parseGitStatus(out)
	r := &gitRepoStatus{root: top, files: files, untrackedDirs: untracked, at: time.Now()}
	gitStatusCache.mu.Lock()
	gitStatusCache.repos[top] = r
	gitStatusCache.mu.Unlock()
	return r
}

// gitTopLevel returns the top level of the repository containing dir, or "".
func gitTopLevel(ctx context.Context, dir string) string {
	ctx, cancel := context.WithTimeout(ctx, fileGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	top := strings.TrimSpace(string(out))
	if r, err := filepath.EvalSymlinks(top); err == nil {
		top = r
	}
	return top
}

// parseGitStatus parses `git status --porcelain=v1 -z`: files with their status, and untracked directories
// ("?? dir/") separately.
func parseGitStatus(out []byte) (map[string]agentv1.GitFileStatus, []string) {
	files := map[string]agentv1.GitFileStatus{}
	var untracked []string
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		x, y, p := f[0], f[1], f[3:]
		if x == 'R' || x == 'C' {
			i++ // the original path follows
		}
		if x == '!' || hasIgnoredPart(p) {
			continue
		}
		if x == '?' && strings.HasSuffix(p, "/") {
			untracked = append(untracked, strings.TrimSuffix(p, "/"))
			continue
		}
		files[p] = gitStatusCode(x, y)
	}
	sort.Strings(untracked)
	return files, untracked
}

// hasIgnoredPart reports whether a slash-separated path goes through an always-ignored name (node_modules
// without a .gitignore rule is still hidden, so its folder gets no change mark).
func hasIgnoredPart(p string) bool {
	for _, part := range strings.Split(strings.TrimSuffix(p, "/"), "/") {
		if alwaysIgnored[part] {
			return true
		}
	}
	return false
}

// gitStatusCode maps a porcelain XY code to a GitFileStatus.
func gitStatusCode(x, y byte) agentv1.GitFileStatus {
	switch {
	case x == '?' || y == '?':
		return agentv1.GitFileStatus_GIT_FILE_STATUS_UNTRACKED
	case x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D'):
		return agentv1.GitFileStatus_GIT_FILE_STATUS_CONFLICTED
	case x == 'R':
		return agentv1.GitFileStatus_GIT_FILE_STATUS_RENAMED
	case x == 'A' || x == 'C':
		return agentv1.GitFileStatus_GIT_FILE_STATUS_ADDED
	case x == 'D' || y == 'D':
		return agentv1.GitFileStatus_GIT_FILE_STATUS_DELETED
	case x == ' ' && y == ' ':
		return agentv1.GitFileStatus_GIT_FILE_STATUS_UNSPECIFIED
	}
	return agentv1.GitFileStatus_GIT_FILE_STATUS_MODIFIED
}

// lexicalPath expresses target (a resolved path) through clean, the path the session knows for real (also
// resolved): /tmp/p and /private/tmp/p name the same directory on macOS, and the UI keys its tree by the former.
func lexicalPath(clean, real, target string) string {
	rel, err := filepath.Rel(real, target)
	if err != nil {
		return target
	}
	return filepath.Clean(filepath.Join(clean, rel))
}

// relSlash returns target relative to base, slash-separated ("" for base itself).
func relSlash(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}
