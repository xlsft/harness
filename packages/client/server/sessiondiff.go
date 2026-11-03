package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/tools"
)

// The session's changes as they are on disk (session_diff.proto): GetSessionDiff for the Diff pane, and the
// totals for SessionInfo.diff_stats, so the pane, the details and the sidebar always count the same thing.

const (
	// maxDiffFiles caps the files listed (the response says when more changed); maxDiffFileBytes the size of a
	// file, on either side, that still gets hunks; maxDiffTotalBytes what one response diffs in all, past which
	// files are flagged too large as well.
	maxDiffFiles      = 500
	maxDiffFileBytes  = 1 << 20
	maxDiffTotalBytes = 64 << 20
	diffContextLines  = 3
	// diffGitTimeout bounds each git command.
	diffGitTimeout = 5 * time.Second
	// sessionOpenDiffTimeout bounds the first measurement, which opening a session waits for.
	sessionOpenDiffTimeout = 3 * time.Second
	// diffRefreshGap separates measurements of diff_stats while tools keep finishing.
	diffRefreshGap = 500 * time.Millisecond
)

// diffSources says where a session's changes are.
type diffSources struct {
	cwd string
	// dirs are the working directories, each of which may be in a repository.
	dirs []string
	// scratch is the session's scratch directory; private its no-folder directory. Both only hold what the session
	// wrote, so a file without a snapshot there is new. Empty when it has none.
	scratch, private string
	// baseRef: the session runs in a worktree the client created from this ref.
	baseRef string
	// baselines are the file checkpoints' first states (session.FileHistory.Baselines).
	baselines map[string]string
	// folderStore is where the snapshots of its directories outside git are kept (folderbaseline.go); "" for none.
	// harnessHome stays out of them: the client's own files change all the time.
	folderStore, harnessHome string
}

func (ls *liveSession) diffSources() *diffSources {
	src := &diffSources{cwd: ls.sess.Cwd, dirs: []string{ls.sess.Cwd}, folderStore: bootstrap.FolderBaselineDir(ls.sess.ID), harnessHome: config.Home()}
	if ls.sess.Permissions != nil {
		if d := ls.sess.Permissions.WorkingDirs(); len(d) > 0 {
			src.dirs = d
		}
	}
	if env := ls.sess.ToolEnv; env != nil {
		src.scratch = env.ScratchDir
	}
	if isNoFolder(ls.sess.ID, ls.sess.Cwd) {
		src.private = ls.sess.Cwd
	}
	if ls.sess.Worktree != nil {
		src.baseRef = ls.sess.Worktree.BaseRef
	}
	if ls.sess.FileHistory != nil {
		src.baselines = ls.sess.FileHistory.Baselines()
	}
	return src
}

func acpDiffSources(id, cwd string, dirs []string) *diffSources {
	src := &diffSources{cwd: cwd, dirs: append([]string{cwd}, dirs...), folderStore: bootstrap.FolderBaselineDir(id), harnessHome: config.Home()}
	if isNoFolder(id, cwd) {
		src.private = cwd
	}
	return src
}

func (s *Server) GetSessionDiff(ctx context.Context, req *connect.Request[agentv1.GetSessionDiffRequest]) (*connect.Response[agentv1.GetSessionDiffResponse], error) {
	id := req.Msg.GetSessionId()
	s.mu.Lock()
	ls := s.live[id]
	s.mu.Unlock()
	var src *diffSources
	if ls != nil {
		src = ls.diffSources()
	} else {
		sc, err := s.fileScope(id) // an ACP session, or the error for a remote or closed one
		if err != nil {
			return nil, err
		}
		src = acpDiffSources(id, sc.cwd, sc.dirs)
	}
	resp, err := computeSessionDiff(ctx, src)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return nil, connect.NewError(connect.CodeDeadlineExceeded, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// The pane just measured: the counts elsewhere catch up with it.
	if ls != nil {
		ls.setDiffStats(diffStatsOf(resp))
	} else if l := s.acpLive(id); l != nil {
		l.setDiffStats(diffStatsOf(resp))
	}
	if m := req.Msg.GetIfNoneMatch(); m != "" && m == resp.Version {
		resp.Files, resp.NotModified = nil, true
	}
	return connect.NewResponse(resp), nil
}

// sessionDiffStats measures the changes of a session for SessionInfo.diff_stats; nil when that failed.
func sessionDiffStats(ctx context.Context, src *diffSources) *agentv1.DiffStats {
	resp, err := computeSessionDiff(ctx, src)
	if err != nil {
		return nil
	}
	return diffStatsOf(resp)
}

func diffStatsOf(r *agentv1.GetSessionDiffResponse) *agentv1.DiffStats {
	return &agentv1.DiffStats{FilesChanged: r.FilesChanged, Additions: r.Additions, Deletions: r.Deletions}
}

// computeSessionDiff applies the baseline rules of session_diff.proto. It fails only when git does on a
// repository of the session: an empty list would read as "nothing changed".
func computeSessionDiff(ctx context.Context, src *diffSources) (*agentv1.GetSessionDiffResponse, error) {
	b := &diffBuilder{src: src, seen: map[string]bool{}, budget: maxDiffTotalBytes}
	cwdTop := ""
	if real, err := filepath.EvalSymlinks(src.cwd); err == nil {
		cwdTop = gitTopLevel(ctx, real)
	}
	scratch := filepath.Clean(src.scratch)
	done := map[string]bool{}
	type folder struct{ clean, real string }
	var folders []folder
	for _, d := range append([]string{src.cwd}, src.dirs...) {
		if d == "" || (src.scratch != "" && filepath.Clean(d) == scratch) {
			continue
		}
		d = filepath.Clean(d)
		real, err := filepath.EvalSymlinks(d)
		if err != nil {
			continue
		}
		top := gitTopLevel(ctx, real)
		if top == "" {
			folders = append(folders, folder{d, real})
			continue
		}
		if done[top] {
			continue
		}
		done[top] = true
		repo := &diffRepo{top: top, root: lexicalPath(d, real, top)}
		if top == cwdTop {
			repo.baseRef = src.baseRef
		}
		b.repos = append(b.repos, repo)
	}
	for _, r := range b.repos {
		if err := b.addRepo(ctx, r); err != nil {
			return nil, fmt.Errorf("%s: %w", r.root, err)
		}
	}
	b.addHistory()
	if src.folderStore != "" {
		for _, f := range folders {
			b.addFolder(ctx, f.clean, f.real)
		}
	}
	if src.scratch != "" {
		b.addNewFiles(src.scratch, scratchInternal)
	}
	if src.private != "" {
		b.addNewFiles(src.private, nil)
	}
	return b.response(), nil
}

// scratchInternal names what the client itself keeps in a session's scratch directory: output files of background
// tasks, saved tool results, the shell's state.
func scratchInternal(rel string) bool {
	first, _, _ := strings.Cut(rel, "/")
	return first == "tasks" || first == "tool-results" || strings.HasPrefix(first, "shell-snapshot.") || strings.HasPrefix(first, "cwd-")
}

// diffRepo is a repository of the session: top is resolved, root the same directory as the session knows it.
type diffRepo struct {
	top, root, baseRef string
}

type diffBuilder struct {
	src   *diffSources
	repos []*diffRepo
	files []*agentv1.FileDiff
	// seen holds the resolved paths already decided.
	seen      map[string]bool
	budget    int64
	truncated bool
}

func (b *diffBuilder) full() bool {
	if len(b.files) >= maxDiffFiles {
		b.truncated = true
		return true
	}
	return false
}

func (b *diffBuilder) add(f *agentv1.FileDiff) {
	if f == nil || b.full() {
		return
	}
	f.DisplayPath = b.display(f.Path)
	b.files = append(b.files, f)
}

func (b *diffBuilder) response() *agentv1.GetSessionDiffResponse {
	sort.Slice(b.files, func(i, j int) bool { return b.files[i].Path < b.files[j].Path })
	r := &agentv1.GetSessionDiffResponse{Files: b.files, Truncated: b.truncated, FilesChanged: int32(len(b.files))}
	for _, f := range b.files {
		r.Additions += f.Additions
		r.Deletions += f.Deletions
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(&agentv1.GetSessionDiffResponse{Files: r.Files, Truncated: r.Truncated})
	if err == nil {
		sum := sha256.Sum256(raw)
		r.Version = hex.EncodeToString(sum[:12])
	}
	return r
}

// display shortens path for showing: relative to the scratch dir, the cwd, a working directory or a repository.
func (b *diffBuilder) display(path string) string {
	if rel, ok := relWithinEither(b.src.scratch, path); ok {
		return "scratchpad/" + rel
	}
	if rel, ok := relWithinEither(b.src.cwd, path); ok {
		return rel
	}
	for _, d := range b.src.dirs {
		if rel, ok := relWithinEither(d, path); ok {
			return filepath.Base(filepath.Clean(d)) + "/" + rel
		}
	}
	for _, r := range b.repos {
		if rel, ok := relWithinEither(r.root, path); ok {
			return rel
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rel, ok := relWithin(home, path); ok {
			return "~/" + rel
		}
	}
	return path
}

// relWithinEither is relWithin lexically, then with symlinks resolved (/tmp and /private/tmp on macOS).
func relWithinEither(base, path string) (string, bool) {
	if base == "" {
		return "", false
	}
	if rel, ok := relWithin(base, path); ok {
		return rel, true
	}
	return relWithin(resolvedPath(base), resolvedPath(path))
}

// relWithin returns path relative to base, slash-separated, when it is strictly inside it.
func relWithin(base, path string) (string, bool) {
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// resolvedPath resolves symlinks in path; the part that does not exist (a deleted file) stays as it is.
func resolvedPath(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path
	}
	return filepath.Join(resolvedPath(parent), filepath.Base(path))
}

// --- Files outside repositories ---

// addHistory adds the files the file tools touched outside the repositories, against their first snapshot.
func (b *diffBuilder) addHistory() {
	paths := make([]string, 0, len(b.src.baselines))
	for p := range b.src.baselines {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		real := resolvedPath(p)
		if b.seen[real] || b.inRepo(real) {
			continue
		}
		b.seen[real] = true
		if b.full() {
			continue
		}
		b.add(b.diffOnDisk(p, real, b.src.baselines[p], agentv1.FileDiffSource_FILE_DIFF_SOURCE_HISTORY))
	}
}

func (b *diffBuilder) inRepo(real string) bool {
	for _, r := range b.repos {
		if real == r.top {
			return true
		}
		if _, ok := relWithin(r.top, real); ok {
			return true
		}
	}
	return false
}

// addNewFiles adds the files in dir (outside repositories) that have no snapshot, as new. internal, when set,
// names paths (relative to dir) to leave out.
func (b *diffBuilder) addNewFiles(dir string, internal func(rel string) bool) {
	clean := filepath.Clean(dir)
	real, err := filepath.EvalSymlinks(clean)
	if err != nil || b.inRepo(real) {
		return
	}
	_ = filepath.WalkDir(real, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			if de != nil && de.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if p == real {
			return nil
		}
		rel := relSlash(real, p)
		if alwaysIgnored[de.Name()] || (internal != nil && internal(rel)) {
			if de.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if de.IsDir() || b.seen[p] {
			return nil
		}
		b.seen[p] = true
		if b.full() {
			return filepath.SkipAll
		}
		b.add(b.diffOnDisk(lexicalPath(clean, real, p), p, "", agentv1.FileDiffSource_FILE_DIFF_SOURCE_NEW_FILE))
		return nil
	})
}

// diffResult is what comparing two states of a file found; cached, so only ever read after it is stored.
type diffResult struct {
	unchanged, binary bool
	hunks             []*agentv1.FileDiffHunk
	additions         int32
	deletions         int32
}

func (r *diffResult) cost() int64 {
	n := int64(256)
	for _, h := range r.hunks {
		for _, l := range h.Lines {
			n += int64(len(l)) + 32
		}
	}
	return n
}

type diffCacheKey struct {
	path, baseline string
	mtime          time.Time
	size           int64
}

// diffCache remembers comparisons by the file's path, mtime and size and its baseline (backups never change), so
// polling does not read and diff unchanged files again. It is dropped whole when it grows past its limit.
var diffCache = struct {
	sync.Mutex
	entries map[diffCacheKey]*diffResult
	bytes   int64
}{entries: map[diffCacheKey]*diffResult{}}

const maxDiffCacheBytes = 32 << 20

func diffCacheGet(k diffCacheKey) *diffResult {
	diffCache.Lock()
	defer diffCache.Unlock()
	return diffCache.entries[k]
}

func diffCachePut(k diffCacheKey, r *diffResult) {
	c := r.cost()
	diffCache.Lock()
	defer diffCache.Unlock()
	if diffCache.bytes+c > maxDiffCacheBytes {
		diffCache.entries, diffCache.bytes = map[diffCacheKey]*diffResult{}, 0
	}
	if c <= maxDiffCacheBytes {
		diffCache.entries[k] = r
		diffCache.bytes += c
	}
}

// diffOnDisk compares the file real (path: as the session knows it) with its baseline: a backup file, or nothing
// when baseline is "" (the file did not exist). nil when nothing changed.
func (b *diffBuilder) diffOnDisk(path, real, baseline string, source agentv1.FileDiffSource) *agentv1.FileDiff {
	f := &agentv1.FileDiff{Path: sanitizeUTF8(path), Source: source, Status: agentv1.FileDiffStatus_FILE_DIFF_STATUS_MODIFIED}
	var baseInfo os.FileInfo
	if baseline != "" {
		st, err := os.Stat(baseline)
		if err != nil {
			return nil // its snapshot is gone: nothing to compare with
		}
		baseInfo = st
	}
	st, err := os.Lstat(real)
	exists := err == nil
	if exists && !st.Mode().IsRegular() && st.Mode()&fs.ModeSymlink == 0 {
		return nil
	}
	switch {
	case !exists && baseline == "":
		return nil
	case !exists:
		f.Status = agentv1.FileDiffStatus_FILE_DIFF_STATUS_DELETED
	case baseline == "":
		f.Status = agentv1.FileDiffStatus_FILE_DIFF_STATUS_ADDED
	}

	var size, baseSize int64
	k := diffCacheKey{path: real, baseline: baseline}
	if exists {
		size = st.Size()
		k.size, k.mtime = size, st.ModTime()
	}
	if baseInfo != nil {
		baseSize = baseInfo.Size()
	}
	if max(size, baseSize) > maxDiffFileBytes || size+baseSize > b.budget {
		// Not read. Same size and not written since the snapshot: taken as unchanged.
		if exists && baseInfo != nil && size == baseSize && !st.ModTime().After(baseInfo.ModTime()) {
			return nil
		}
		f.TooLarge = true
		return f
	}
	b.budget -= size + baseSize

	r := diffCacheGet(k)
	if r == nil {
		r = compareFiles(real, exists && st.Mode()&fs.ModeSymlink != 0, exists, baseline)
		if r == nil {
			return nil
		}
		diffCachePut(k, r)
	}
	if r.unchanged {
		return nil
	}
	f.Binary, f.Hunks, f.Additions, f.Deletions = r.binary, r.hunks, r.additions, r.deletions
	return f
}

// compareFiles reads both sides; nil when one cannot be read now. A symlink is compared by its target, as git
// does: it is never followed, so what it points to stays out of the diff.
func compareFiles(real string, symlink, exists bool, baseline string) *diffResult {
	var cur, old []byte
	var err error
	if symlink {
		t, lerr := os.Readlink(real)
		if lerr != nil {
			return nil
		}
		cur = []byte(t)
	} else if exists {
		if cur, err = os.ReadFile(real); err != nil {
			return nil
		}
	}
	if baseline != "" {
		if old, err = os.ReadFile(baseline); err != nil {
			return nil
		}
	}
	if baseline != "" && exists && bytes.Equal(old, cur) {
		return &diffResult{unchanged: true}
	}
	if isBinary(cur, false) || isBinary(old, false) {
		return &diffResult{binary: true}
	}
	r := &diffResult{}
	hunks := tools.DiffLines(string(old), string(cur), diffContextLines)
	add, del := tools.DiffStats(hunks)
	r.additions, r.deletions = int32(add), int32(del)
	for _, h := range hunks {
		r.hunks = append(r.hunks, &agentv1.FileDiffHunk{OldStart: int32(h.OldStart), OldLines: int32(h.OldLines), NewStart: int32(h.NewStart), NewLines: int32(h.NewLines), Lines: h.Lines})
	}
	return r
}

// --- Repositories ---

func diffGit(ctx context.Context, top string, stdin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, diffGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", top, "-c", "core.quotepath=off"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return out, nil
}

// repoBase is what the working tree of r compares with: the merge base in a worktree of the client, else HEAD, the
// empty tree before the first commit.
func repoBase(ctx context.Context, r *diffRepo) (string, error) {
	if _, err := diffGit(ctx, r.top, "", "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return "", err
		}
		out, err := diffGit(ctx, r.top, "", "hash-object", "-t", "tree", "--stdin")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	if r.baseRef != "" {
		if mb, err := diffGit(ctx, r.top, "", "merge-base", r.baseRef, "HEAD"); err == nil {
			return strings.TrimSpace(string(mb)), nil
		}
	}
	return "HEAD", nil
}

// diffOptions keep the user's git configuration out of a patch that gets parsed.
var diffOptions = []string{"--no-color", "--no-ext-diff", "--no-textconv", "--no-relative", "--ignore-submodules=dirty", "--submodule=short", "-M"}

func (b *diffBuilder) addRepo(ctx context.Context, r *diffRepo) error {
	base, err := repoBase(ctx, r)
	if err != nil {
		return err
	}
	rawArgs := append(append([]string{"diff", "--raw", "-z", "--abbrev=40"}, diffOptions...), base)
	raw, err := diffGit(ctx, r.top, "", rawArgs...)
	if err != nil {
		return err
	}
	entries := parseRawDiff(raw)
	oldSizes := blobSizes(ctx, r.top, entries)

	// Decide per file before git writes any patch: a huge file must not reach it.
	var keep, skip []string
	for _, e := range entries {
		full := filepath.Join(r.top, filepath.FromSlash(e.path))
		b.seen[full] = true
		over := len(b.files)+len(keep) >= maxDiffFiles
		if over {
			b.truncated = true
			skip = append(skip, e.specs()...)
			continue
		}
		var size int64
		if st, err := os.Lstat(full); err == nil && st.Mode().IsRegular() {
			size = st.Size()
		}
		old := oldSizes[e.oldOID]
		if max(old, size) > maxDiffFileBytes || old+size > b.budget {
			skip = append(skip, e.specs()...)
			b.add(&agentv1.FileDiff{
				Path: sanitizeUTF8(filepath.Join(r.root, filepath.FromSlash(e.path))), OldPath: r.absOld(e.oldPath), Status: e.status(),
				TooLarge: true, Source: agentv1.FileDiffSource_FILE_DIFF_SOURCE_GIT,
			})
			continue
		}
		b.budget -= old + size
		keep = append(keep, e.specs()...)
	}
	if len(keep) > 0 {
		args := append(append([]string{"diff", "-U" + strconv.Itoa(diffContextLines), "--src-prefix=a/", "--dst-prefix=b/"}, diffOptions...), base)
		switch {
		case b.truncated:
			// Far more files than listed: name the kept ones rather than everything else.
			args = append(append(args, "--"), keep...)
		case len(skip) > 0:
			args = append(append(args, "--", ":(top)"), excludeSpecs(skip)...)
		}
		out, err := diffGit(ctx, r.top, "", args...)
		if err != nil {
			return err
		}
		for _, f := range parseGitPatch(out) {
			f.Path = sanitizeUTF8(filepath.Join(r.root, filepath.FromSlash(f.Path)))
			f.OldPath = r.absOld(f.OldPath)
			f.Source = agentv1.FileDiffSource_FILE_DIFF_SOURCE_GIT
			b.add(f)
		}
	}

	out, err := diffGit(ctx, r.top, "", "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" || strings.HasSuffix(p, "/") || hasIgnoredPart(p) {
			continue // a trailing slash: a nested repository
		}
		if b.full() {
			break
		}
		full := filepath.Join(r.top, filepath.FromSlash(p))
		if b.seen[full] {
			continue
		}
		b.seen[full] = true
		b.add(b.diffOnDisk(filepath.Join(r.root, filepath.FromSlash(p)), full, "", agentv1.FileDiffSource_FILE_DIFF_SOURCE_GIT))
	}
	return nil
}

func (r *diffRepo) absOld(rel string) string {
	if rel == "" {
		return ""
	}
	return sanitizeUTF8(filepath.Join(r.root, filepath.FromSlash(rel)))
}

type rawDiffEntry struct {
	oldOID, code, path, oldPath string
}

// specs are the pathspecs naming this change (both sides of a rename).
func (e rawDiffEntry) specs() []string {
	s := []string{":(top,literal)" + e.path}
	if e.oldPath != "" {
		s = append(s, ":(top,literal)"+e.oldPath)
	}
	return s
}

func (e rawDiffEntry) status() agentv1.FileDiffStatus {
	switch e.code {
	case "A":
		return agentv1.FileDiffStatus_FILE_DIFF_STATUS_ADDED
	case "D":
		return agentv1.FileDiffStatus_FILE_DIFF_STATUS_DELETED
	case "R":
		return agentv1.FileDiffStatus_FILE_DIFF_STATUS_RENAMED
	}
	return agentv1.FileDiffStatus_FILE_DIFF_STATUS_MODIFIED
}

func excludeSpecs(specs []string) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = ":(top,literal,exclude)" + strings.TrimPrefix(s, ":(top,literal)")
	}
	return out
}

// parseRawDiff parses `git diff --raw -z`: ":old new oldOID newOID status\0path\0" and, for renames and copies,
// the new path after the old one.
func parseRawDiff(out []byte) []rawDiffEntry {
	var res []rawDiffEntry
	fields := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(fields); {
		meta := strings.Fields(fields[i])
		if len(meta) < 5 || !strings.HasPrefix(fields[i], ":") {
			i++
			continue
		}
		e := rawDiffEntry{oldOID: meta[2], code: meta[4][:1], path: fields[i+1]}
		i += 2
		if (e.code == "R" || e.code == "C") && i < len(fields) {
			e.oldPath, e.path = e.path, fields[i]
			i++
		}
		res = append(res, e)
	}
	return res
}

// blobSizes returns the sizes of the old sides, in one git process. Failing leaves them unknown (0): the patch is
// then not bounded by them, which beats losing the diff.
func blobSizes(ctx context.Context, top string, entries []rawDiffEntry) map[string]int64 {
	var stdin strings.Builder
	for _, e := range entries {
		if strings.Trim(e.oldOID, "0") != "" {
			stdin.WriteString(e.oldOID + "\n")
		}
	}
	res := map[string]int64{}
	if stdin.Len() == 0 {
		return res
	}
	out, err := diffGit(ctx, top, stdin.String(), "cat-file", "--batch-check")
	if err != nil {
		return res
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[1] == "blob" {
			if n, err := strconv.ParseInt(f[2], 10, 64); err == nil {
				res[f[0]] = n
			}
		}
	}
	return res
}

// parseGitPatch parses `git diff` output (with the a/ b/ prefixes) into one FileDiff per file, paths relative to
// the repository.
func parseGitPatch(out []byte) []*agentv1.FileDiff {
	var res []*agentv1.FileDiff
	var cur *agentv1.FileDiff
	lines := strings.Split(string(out), "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "diff --git "):
			cur = &agentv1.FileDiff{Path: gitHeaderPath(line[len("diff --git "):]), Status: agentv1.FileDiffStatus_FILE_DIFF_STATUS_MODIFIED}
			res = append(res, cur)
		case cur == nil:
		case strings.HasPrefix(line, "new file mode "):
			cur.Status = agentv1.FileDiffStatus_FILE_DIFF_STATUS_ADDED
		case strings.HasPrefix(line, "deleted file mode "):
			cur.Status = agentv1.FileDiffStatus_FILE_DIFF_STATUS_DELETED
		case strings.HasPrefix(line, "rename from "):
			cur.Status = agentv1.FileDiffStatus_FILE_DIFF_STATUS_RENAMED
			cur.OldPath = gitUnquote(line[len("rename from "):])
		case strings.HasPrefix(line, "rename to "):
			cur.Path = gitUnquote(line[len("rename to "):])
		case strings.HasPrefix(line, "+++ "):
			if p := gitPatchName(line[4:]); p != "" {
				cur.Path = p
			}
		case strings.HasPrefix(line, "--- "):
			if p := gitPatchName(line[4:]); p != "" {
				cur.Path = p
			}
		case strings.HasPrefix(line, "Binary files "):
			cur.Binary = true
		case strings.HasPrefix(line, "@@ "):
			h, ok := parseHunkHeader(line)
			if !ok {
				continue
			}
			// The body by its line counts: a removed line may read like a header.
			oldLeft, newLeft := h.OldLines, h.NewLines
			for (oldLeft > 0 || newLeft > 0) && i+1 < len(lines) {
				i++
				l := lines[i]
				switch {
				case strings.HasPrefix(l, "+"):
					newLeft--
					cur.Additions++
				case strings.HasPrefix(l, "-"):
					oldLeft--
					cur.Deletions++
				case strings.HasPrefix(l, " "):
					oldLeft--
					newLeft--
				default:
					continue // "\ No newline at end of file"
				}
				h.Lines = append(h.Lines, sanitizeUTF8(l))
			}
			cur.Hunks = append(cur.Hunks, h)
		}
	}
	return res
}

// parseHunkHeader parses "@@ -a,b +c,d @@ header".
func parseHunkHeader(line string) (*agentv1.FileDiffHunk, bool) {
	rest := strings.TrimPrefix(line, "@@ ")
	end := strings.Index(rest, " @@")
	if end < 0 {
		return nil, false
	}
	f := strings.Fields(rest[:end])
	if len(f) != 2 {
		return nil, false
	}
	span := func(s string) (int32, int32) {
		a, c, found := strings.Cut(s[1:], ",")
		start, _ := strconv.Atoi(a)
		count := 1
		if found {
			count, _ = strconv.Atoi(c)
		}
		return int32(start), int32(count)
	}
	h := &agentv1.FileDiffHunk{Header: sanitizeUTF8(strings.TrimSpace(rest[end+3:]))}
	h.OldStart, h.OldLines = span(f[0])
	h.NewStart, h.NewLines = span(f[1])
	return h, true
}

// gitHeaderPath takes the path from `a/P b/P` (P the same on both sides, as git prints for a file without ---/+++
// lines: an empty file, a mode change, a binary file; a rename says its names on other lines).
func gitHeaderPath(rest string) string {
	if strings.HasPrefix(rest, `"`) {
		if at := strings.Index(rest, `" "`); at >= 0 {
			return strings.TrimPrefix(gitUnquote(rest[at+2:]), "b/")
		}
	}
	if at := (len(rest) - 1) / 2; at < len(rest) && rest[at] == ' ' {
		return strings.TrimPrefix(rest[at+1:], "b/")
	}
	if at := strings.LastIndex(rest, " b/"); at >= 0 {
		return rest[at+3:]
	}
	return rest
}

// gitPatchName takes the path from a ---/+++ line; "" for /dev/null.
func gitPatchName(s string) string {
	s = strings.TrimSuffix(s, "\t") // git marks a name with spaces this way
	s = gitUnquote(s)
	if s == "/dev/null" {
		return ""
	}
	if len(s) > 2 && (strings.HasPrefix(s, "a/") || strings.HasPrefix(s, "b/")) {
		return s[2:]
	}
	return s
}

// gitUnquote undoes git's C-style quoting of unusual names.
func gitUnquote(s string) string {
	if !strings.HasPrefix(s, `"`) {
		return s
	}
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return s
}

func sanitizeUTF8(s string) string { return strings.ToValidUTF8(s, "�") }
