package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
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

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/tools"
)

// The session's changes as they are on disk (session_diff.proto): GetSessionDiff for the Diff pane, and the
// totals for SessionInfo.diff_stats, so the pane, the details and the sidebar always count the same thing.
//
// A measurement goes in up to three passes, each only when the one before does not settle the answer:
//   - scan: what changed, from `git diff --raw`, the listing of untracked files and stat. Reads no content and gives
//     the version, so an unchanged state stops here, however large;
//   - measure: the totals and flags, from `git diff --numstat` (not a patch) and the files outside git;
//   - hunks: `git diff -U3`, only for a diff under the large thresholds.

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
	// A diff with more files or changed lines than these is large: listed without hunks, polled less often.
	largeDiffFiles = 40
	largeDiffLines = 3000
	// The polls suggested to the frontend (GetSessionDiffResponse.refresh_after_ms).
	diffRefreshMs      = 5000
	largeDiffRefreshMs = 30000
	// maxDiffScanFiles caps the new files one scan stats; past it the diff counts as truncated.
	maxDiffScanFiles = maxFolderEntries
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
	src := &diffSources{cwd: ls.sess.Dir(), dirs: []string{ls.sess.Dir()}, folderStore: bootstrap.FolderBaselineDir(ls.sess.ID), harnessHome: config.Home()}
	if ls.sess.Permissions != nil {
		if d := ls.sess.Permissions.WorkingDirs(); len(d) > 0 {
			src.dirs = d
		}
	}
	if env := ls.sess.ToolEnv; env != nil {
		src.scratch = env.ScratchDir
	}
	if isNoFolder(ls.sess.ID, ls.sess.Dir()) {
		src.private = ls.sess.Dir()
	}
	if w := ls.sess.CurrentWorktree(); w != nil {
		src.baseRef = w.BaseRef
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

// diffSourcesOf finds the changes' sources of an open session, or the error for a remote or closed one.
func (s *Server) diffSourcesOf(id string) (*diffSources, error) {
	s.mu.Lock()
	ls := s.live[id]
	s.mu.Unlock()
	if ls != nil {
		return ls.diffSources(), nil
	}
	sc, err := s.fileScope(id) // an ACP session, or the error for a remote or closed one
	if err != nil {
		return nil, err
	}
	return acpDiffSources(id, sc.cwd, sc.dirs), nil
}

// diffRPCError keeps the code of an error that has one (a refused base); git failing is internal.
func diffRPCError(ctx context.Context, err error) error {
	var coded *connect.Error
	if errors.As(err, &coded) {
		return coded
	}
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func (s *Server) GetSessionDiff(ctx context.Context, req *connect.Request[agentv1.GetSessionDiffRequest]) (*connect.Response[agentv1.GetSessionDiffResponse], error) {
	id := req.Msg.GetSessionId()
	src, err := s.diffSourcesOf(id)
	if err != nil {
		return nil, err
	}
	resp, err := computeSessionDiff(ctx, src, req.Msg.GetBase(), req.Msg.GetIfNoneMatch())
	if err != nil {
		return nil, diffRPCError(ctx, err)
	}
	// The pane just measured: the counts elsewhere catch up with it. They always count the default, so another
	// base says nothing about them.
	if resp.BaseKind == agentv1.DiffBaseKind_DIFF_BASE_KIND_ALL {
		s.mu.Lock()
		ls := s.live[id]
		s.mu.Unlock()
		if ls != nil {
			ls.setDiffStats(diffStatsOf(resp))
		} else if l := s.acpLive(id); l != nil {
			l.setDiffStats(diffStatsOf(resp))
		}
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) GetSessionFileDiff(ctx context.Context, req *connect.Request[agentv1.GetSessionFileDiffRequest]) (*connect.Response[agentv1.GetSessionFileDiffResponse], error) {
	src, err := s.diffSourcesOf(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	f, err := computeSessionFileDiff(ctx, src, req.Msg.GetBase(), req.Msg.GetPath())
	if err != nil {
		return nil, diffRPCError(ctx, err)
	}
	return connect.NewResponse(&agentv1.GetSessionFileDiffResponse{File: f}), nil
}

// sessionDiffStats measures the changes of a session for SessionInfo.diff_stats; nil when that failed. It runs after
// every tool, so it stops at the totals: no patch, and only the scan when nothing changed.
func sessionDiffStats(ctx context.Context, src *diffSources) *agentv1.DiffStats {
	b, err := scanSessionDiff(ctx, src, nil)
	if err != nil {
		return nil
	}
	sum := diffSummaryGet(b.version)
	if sum == nil {
		if err := b.measure(ctx); err != nil {
			return nil
		}
		sum = b.summarize()
	}
	return &agentv1.DiffStats{FilesChanged: sum.filesChanged, Additions: sum.additions, Deletions: sum.deletions}
}

func diffStatsOf(r *agentv1.GetSessionDiffResponse) *agentv1.DiffStats {
	return &agentv1.DiffStats{FilesChanged: r.FilesChanged, Additions: r.Additions, Deletions: r.Deletions}
}

// computeSessionDiff applies the baseline rules of session_diff.proto. It fails only when git does on a
// repository of the session, or for a base it refuses: an empty list would read as "nothing changed".
func computeSessionDiff(ctx context.Context, src *diffSources, base *agentv1.DiffBase, ifNoneMatch string) (*agentv1.GetSessionDiffResponse, error) {
	b, err := scanSessionDiff(ctx, src, base)
	if err != nil {
		return nil, err
	}
	notModified := ifNoneMatch != "" && ifNoneMatch == b.version
	sum := diffSummaryGet(b.version)
	if sum == nil || !(notModified || sum.large) {
		if err := b.measure(ctx); err != nil {
			return nil, err
		}
		sum = b.summarize()
	}
	if notModified || sum.large {
		return b.response(sum, notModified), nil
	}
	if err := b.addHunks(ctx); err != nil {
		return nil, err
	}
	return b.response(b.totals(b.sortedFiles()), false), nil
}

// computeSessionFileDiff is one changed file of the same comparison. Only what the scan lists can be asked for.
func computeSessionFileDiff(ctx context.Context, src *diffSources, base *agentv1.DiffBase, path string) (*agentv1.FileDiff, error) {
	b, err := scanSessionDiff(ctx, src, base)
	if err != nil {
		return nil, err
	}
	var f *agentv1.FileDiff
	found := false
	for _, r := range b.repos {
		for _, e := range r.entries {
			if !found && r.abs(e.path) == path {
				found = true
				if f, err = b.repoFile(ctx, r, e); err != nil {
					return nil, err
				}
			}
		}
		for _, p := range r.untracked {
			if !found && r.abs(p) == path {
				found = true
				f = b.diffOnDisk(filepath.Join(r.root, filepath.FromSlash(p)), filepath.Join(r.top, filepath.FromSlash(p)), "", agentv1.FileDiffSource_FILE_DIFF_SOURCE_GIT)
			}
		}
	}
	for _, c := range b.candidates {
		if !found && sanitizeUTF8(c.path) == path {
			found = true
			f = b.diskFile(c)
		}
	}
	if f == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%s has no changes against this base", path))
	}
	f.DisplayPath = b.display(f.Path)
	return f, nil
}

// scanSessionDiff finds the repositories and folders of the session and what changed in them, reading no content.
func scanSessionDiff(ctx context.Context, src *diffSources, base *agentv1.DiffBase) (*diffBuilder, error) {
	choice, err := parseDiffBase(base)
	if err != nil {
		return nil, err
	}
	b := &diffBuilder{src: src, seen: map[string]bool{}, budget: maxDiffTotalBytes, sig: sha256.New()}
	folders := b.findRepos(ctx)
	if err := b.resolveBases(ctx, choice); err != nil {
		return nil, err
	}
	b.sign(b.kind.String(), b.baseLabel, b.headLabel, src.cwd)
	b.sign(src.dirs...)
	for _, r := range b.repos {
		if err := b.scanRepo(ctx, r); err != nil {
			return nil, fmt.Errorf("%s: %w", r.root, err)
		}
	}
	// A commit is all there is to show under COMMIT: nothing on disk is involved.
	if b.kind != agentv1.DiffBaseKind_DIFF_BASE_KIND_COMMIT {
		b.scanHistory()
		if src.folderStore != "" {
			for _, f := range folders {
				b.scanFolder(ctx, f.clean, f.real)
			}
		}
		if src.scratch != "" {
			b.scanNewFiles(src.scratch, scratchInternal)
		}
		if src.private != "" {
			b.scanNewFiles(src.private, nil)
		}
	}
	b.sign(strconv.FormatBool(b.truncated))
	b.version = hex.EncodeToString(b.sig.Sum(nil)[:12])
	return b, nil
}

// diffFolder is a working directory outside git.
type diffFolder struct{ clean, real string }

// findRepos sets the session's repositories, the cwd's first, and returns the directories outside git.
func (b *diffBuilder) findRepos(ctx context.Context) []diffFolder {
	src := b.src
	tops := map[string]string{}
	topOf := func(real string) string {
		top, known := tops[real]
		if !known {
			top = gitTopLevel(ctx, real)
			tops[real] = top
		}
		return top
	}
	cwdTop := ""
	if real, err := filepath.EvalSymlinks(src.cwd); err == nil {
		cwdTop = topOf(real)
	}
	scratch := filepath.Clean(src.scratch)
	done := map[string]bool{}
	var folders []diffFolder
	for _, d := range append([]string{src.cwd}, src.dirs...) {
		if d == "" || (src.scratch != "" && filepath.Clean(d) == scratch) {
			continue
		}
		d = filepath.Clean(d)
		real, err := filepath.EvalSymlinks(d)
		if err != nil {
			continue
		}
		top := topOf(real)
		if top == "" {
			if !done[real] {
				done[real] = true
				folders = append(folders, diffFolder{d, real})
			}
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
	return folders
}

// scratchInternal names what the client itself keeps in a session's scratch directory: output files of background
// tasks, saved tool results, the shell's state.
func scratchInternal(rel string) bool {
	return strings.HasPrefix(rel, "tool-results/") || rel == "tool-results" || syncclient.ClientScratchFile(rel)
}

// diffRepo is a repository of the session: top is resolved, root the same directory as the session knows it.
type diffRepo struct {
	top, root, baseRef string
	// What it compares (resolveBases): the base alone means against the working tree, two revisions are a commit's
	// parent and the commit. All are commits or HEAD, never a name from a request.
	revs                 []string
	kind                 agentv1.DiffBaseKind
	baseLabel, headLabel string
	// What the scan found. keep, by measure: the pathspecs of the files that get line counts and hunks.
	entries   []rawDiffEntry
	untracked []string
	keep      []string
}

// diskCandidate is a file outside the repositories, or new, that the scan could not rule out; whether it differs
// shows when it is read. A file without a copy in its snapshot (noCopy) is known to have changed, not how.
type diskCandidate struct {
	path, real, baseline string
	source               agentv1.FileDiffSource
	noCopy               bool
}

type diffBuilder struct {
	src   *diffSources
	repos []*diffRepo
	files []*agentv1.FileDiff
	// seen holds the resolved paths already decided.
	seen      map[string]bool
	budget    int64
	truncated bool
	// The base in effect and how to show it (resolveBases).
	kind                 agentv1.DiffBaseKind
	baseLabel, headLabel string
	candidates           []diskCandidate
	// sig collects what the scan saw; version is its hash.
	sig     hash.Hash
	version string
}

func (b *diffBuilder) sign(parts ...string) {
	for _, p := range parts {
		_, _ = b.sig.Write([]byte(p))
		_, _ = b.sig.Write([]byte{0})
	}
}

// signStat records the file's state without reading it. `git diff --raw` is not enough for the working tree: a file
// edited again has the same line (its new side is all zeros), so only this tells the edits apart. Two same-size
// writes within one tick of the file system's clock look alike, as they do to diffCache.
func (b *diffBuilder) signStat(path string) {
	st, err := os.Lstat(path)
	if err != nil {
		b.sign(path, "gone")
		return
	}
	b.sign(path, st.Mode().String(), strconv.FormatInt(st.Size(), 10), strconv.FormatInt(st.ModTime().UnixNano(), 10))
}

func (b *diffBuilder) addCandidate(c diskCandidate) {
	b.candidates = append(b.candidates, c)
	b.signStat(c.real)
	if c.baseline != "" {
		b.signStat(c.baseline)
	}
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

// measure fills the list with every file's flags and line counts, without a patch.
func (b *diffBuilder) measure(ctx context.Context) error {
	for _, r := range b.repos {
		if err := b.measureRepo(ctx, r); err != nil {
			return fmt.Errorf("%s: %w", r.root, err)
		}
	}
	for _, c := range b.candidates {
		if b.full() {
			continue
		}
		b.add(b.diskFile(c))
	}
	return nil
}

func (b *diffBuilder) diskFile(c diskCandidate) *agentv1.FileDiff {
	if !c.noCopy {
		return b.diffOnDisk(c.path, c.real, c.baseline, c.source)
	}
	d := &agentv1.FileDiff{Path: sanitizeUTF8(c.path), Status: agentv1.FileDiffStatus_FILE_DIFF_STATUS_MODIFIED, TooLarge: true, Source: c.source}
	if _, err := os.Lstat(c.real); err != nil {
		d.Status = agentv1.FileDiffStatus_FILE_DIFF_STATUS_DELETED
	}
	return d
}

func (b *diffBuilder) sortedFiles() []*agentv1.FileDiff {
	sort.Slice(b.files, func(i, j int) bool { return b.files[i].Path < b.files[j].Path })
	return b.files
}

// diffSummary is a measurement without hunks: all an unchanged state or a large diff needs to answer again. The
// files are shared between answers and never written to.
type diffSummary struct {
	files                              []*agentv1.FileDiff
	truncated, large                   bool
	filesChanged, additions, deletions int32
}

func (b *diffBuilder) totals(files []*agentv1.FileDiff) *diffSummary {
	sum := &diffSummary{files: files, truncated: b.truncated, filesChanged: int32(len(files))}
	for _, f := range files {
		sum.additions += f.Additions
		sum.deletions += f.Deletions
	}
	sum.large = b.truncated || len(files) > largeDiffFiles || int64(sum.additions)+int64(sum.deletions) > largeDiffLines
	return sum
}

// summarize remembers the measurement under the version. b.files keep their hunks, for addHunks and the answer.
func (b *diffBuilder) summarize() *diffSummary {
	files := b.sortedFiles()
	bare := make([]*agentv1.FileDiff, len(files))
	for i, f := range files {
		bare[i] = &agentv1.FileDiff{
			Path: f.Path, DisplayPath: f.DisplayPath, Status: f.Status, OldPath: f.OldPath, Additions: f.Additions, Deletions: f.Deletions,
			Binary: f.Binary, TooLarge: f.TooLarge, Source: f.Source,
		}
	}
	sum := b.totals(bare)
	diffSummaryPut(b.version, sum)
	return sum
}

func (b *diffBuilder) response(sum *diffSummary, notModified bool) *agentv1.GetSessionDiffResponse {
	r := &agentv1.GetSessionDiffResponse{
		Files: sum.files, Version: b.version, NotModified: notModified, Truncated: sum.truncated, FilesChanged: sum.filesChanged,
		Additions: sum.additions, Deletions: sum.deletions, BaseLabel: b.baseLabel, HeadLabel: b.headLabel, BaseKind: b.kind,
		Large: sum.large, RefreshAfterMs: diffRefreshMs,
	}
	if sum.large {
		r.RefreshAfterMs = largeDiffRefreshMs
	}
	if notModified {
		r.Files = nil
	}
	return r
}

// diffSummaries remembers measurements by version, so polling an unchanged state costs only the scan. The version
// names the session's directories and everything the scan saw, so an entry cannot go stale; it is dropped whole
// when it grows past its limit.
var diffSummaries = struct {
	sync.Mutex
	entries map[string]*diffSummary
}{entries: map[string]*diffSummary{}}

const maxDiffSummaries = 64

func diffSummaryGet(version string) *diffSummary {
	diffSummaries.Lock()
	defer diffSummaries.Unlock()
	return diffSummaries.entries[version]
}

func diffSummaryPut(version string, sum *diffSummary) {
	diffSummaries.Lock()
	defer diffSummaries.Unlock()
	if len(diffSummaries.entries) >= maxDiffSummaries {
		diffSummaries.entries = map[string]*diffSummary{}
	}
	diffSummaries.entries[version] = sum
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

// scanHistory notes the files the file tools touched outside the repositories, to compare with their first snapshot.
func (b *diffBuilder) scanHistory() {
	paths := make([]string, 0, len(b.src.baselines))
	for p := range b.src.baselines {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		real := resolvedPath(p)
		if b.seen[real] || b.inRepo(real) || removedWorktreeFile(p) {
			continue
		}
		b.seen[real] = true
		b.addCandidate(diskCandidate{path: p, real: real, baseline: b.src.baselines[p], source: agentv1.FileDiffSource_FILE_DIFF_SOURCE_HISTORY})
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

// scanNewFiles notes the files in dir (outside repositories) that have no snapshot, as new. internal, when set,
// names paths (relative to dir) to leave out.
func (b *diffBuilder) scanNewFiles(dir string, internal func(rel string) bool) {
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
		if len(b.candidates) >= maxDiffScanFiles {
			b.truncated = true
			return filepath.SkipAll
		}
		b.addCandidate(diskCandidate{path: lexicalPath(clean, real, p), real: p, source: agentv1.FileDiffSource_FILE_DIFF_SOURCE_NEW_FILE})
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

// diffOptions keep the user's git configuration out of a patch that gets parsed.
var diffOptions = []string{"--no-color", "--no-ext-diff", "--no-textconv", "--no-relative", "--ignore-submodules=dirty", "--submodule=short", "-M"}

// scanRepo notes what changed in r. Under COMMIT the two trees never change, so git's answer is all there is.
func (b *diffBuilder) scanRepo(ctx context.Context, r *diffRepo) error {
	b.sign(append([]string{r.root}, r.revs...)...)
	rawArgs := append(append([]string{"diff", "--raw", "-z", "--abbrev=40"}, diffOptions...), r.revs...)
	raw, err := diffGit(ctx, r.top, "", rawArgs...)
	if err != nil {
		return err
	}
	b.sign(string(raw))
	r.entries = parseRawDiff(raw)
	if r.kind == agentv1.DiffBaseKind_DIFF_BASE_KIND_COMMIT {
		return nil
	}
	for _, e := range r.entries {
		full := filepath.Join(r.top, filepath.FromSlash(e.path))
		b.seen[full] = true
		b.signStat(full)
	}
	out, err := diffGit(ctx, r.top, "", "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" || strings.HasSuffix(p, "/") || hasIgnoredPart(p) {
			continue // a trailing slash: a nested repository
		}
		full := filepath.Join(r.top, filepath.FromSlash(p))
		if b.seen[full] {
			continue
		}
		if len(r.untracked) >= maxDiffScanFiles {
			b.truncated = true
			break
		}
		b.seen[full] = true
		r.untracked = append(r.untracked, p)
		b.signStat(full)
	}
	return nil
}

// entrySize returns the sizes of a change's sides: the old from git, the new from disk or, under COMMIT, from git.
func (r *diffRepo) entrySize(e rawDiffEntry, blobs map[string]int64) (old, size int64) {
	if r.kind == agentv1.DiffBaseKind_DIFF_BASE_KIND_COMMIT {
		return blobs[e.oldOID], blobs[e.newOID]
	}
	if st, err := os.Lstat(filepath.Join(r.top, filepath.FromSlash(e.path))); err == nil && st.Mode().IsRegular() {
		size = st.Size()
	}
	return blobs[e.oldOID], size
}

func (r *diffRepo) tooLargeFile(e rawDiffEntry) *agentv1.FileDiff {
	return &agentv1.FileDiff{Path: r.abs(e.path), OldPath: r.absOld(e.oldPath), Status: e.status(), TooLarge: true, Source: agentv1.FileDiffSource_FILE_DIFF_SOURCE_GIT}
}

// measureRepo lists r's files with their line counts, from git's numbers rather than a patch.
func (b *diffBuilder) measureRepo(ctx context.Context, r *diffRepo) error {
	blobs := blobSizes(ctx, r.top, r.entries)

	// Decide per file before git compares any: a huge file must not reach it.
	var skip []string
	for _, e := range r.entries {
		over := len(b.files)+len(r.keep) >= maxDiffFiles
		if over {
			b.truncated = true
			skip = append(skip, e.specs()...)
			continue
		}
		old, size := r.entrySize(e, blobs)
		if max(old, size) > maxDiffFileBytes || old+size > b.budget {
			skip = append(skip, e.specs()...)
			b.add(r.tooLargeFile(e))
			continue
		}
		b.budget -= old + size
		r.keep = append(r.keep, e.specs()...)
	}
	if len(r.keep) > 0 {
		args := append(append([]string{"diff", "--numstat", "-z"}, diffOptions...), r.revs...)
		switch {
		case b.truncated:
			// Far more files than listed: name the kept ones rather than everything else.
			args = append(append(args, "--"), r.keep...)
		case len(skip) > 0:
			args = append(append(args, "--", ":(top)"), excludeSpecs(skip)...)
		}
		out, err := diffGit(ctx, r.top, "", args...)
		if err != nil {
			return err
		}
		byPath := make(map[string]rawDiffEntry, len(r.entries))
		for _, e := range r.entries {
			byPath[e.path] = e
		}
		for _, n := range parseNumstat(out) {
			e := byPath[n.path]
			old := n.oldPath
			if old == "" {
				old = e.oldPath
			}
			b.add(&agentv1.FileDiff{
				Path: r.abs(n.path), OldPath: r.absOld(old), Status: e.status(), Additions: n.additions, Deletions: n.deletions,
				Binary: n.binary, Source: agentv1.FileDiffSource_FILE_DIFF_SOURCE_GIT,
			})
		}
	}

	for _, p := range r.untracked {
		if b.full() {
			break
		}
		full := filepath.Join(r.top, filepath.FromSlash(p))
		b.add(b.diffOnDisk(filepath.Join(r.root, filepath.FromSlash(p)), full, "", agentv1.FileDiffSource_FILE_DIFF_SOURCE_GIT))
	}
	return nil
}

// repoPatch runs git for the patch of the files specs names, as FileDiffs with paths as the session knows them.
func (r *diffRepo) repoPatch(ctx context.Context, specs []string) ([]*agentv1.FileDiff, error) {
	args := append(append([]string{"diff", "-U" + strconv.Itoa(diffContextLines), "--src-prefix=a/", "--dst-prefix=b/"}, diffOptions...), r.revs...)
	out, err := diffGit(ctx, r.top, "", append(append(args, "--"), specs...)...)
	if err != nil {
		return nil, err
	}
	files := parseGitPatch(out)
	for _, f := range files {
		f.Path = r.abs(f.Path)
		f.OldPath = r.absOld(f.OldPath)
		f.Source = agentv1.FileDiffSource_FILE_DIFF_SOURCE_GIT
	}
	return files, nil
}

// addHunks gives the repositories' measured files their hunks; files outside them got theirs when read.
func (b *diffBuilder) addHunks(ctx context.Context) error {
	byPath := make(map[string]*agentv1.FileDiff, len(b.files))
	for _, f := range b.files {
		byPath[f.Path] = f
	}
	for _, r := range b.repos {
		if len(r.keep) == 0 {
			continue
		}
		patched, err := r.repoPatch(ctx, r.keep)
		if err != nil {
			return fmt.Errorf("%s: %w", r.root, err)
		}
		for _, p := range patched {
			if f := byPath[p.Path]; f != nil {
				f.Status, f.OldPath, f.Additions, f.Deletions, f.Binary, f.Hunks = p.Status, p.OldPath, p.Additions, p.Deletions, p.Binary, p.Hunks
			}
		}
	}
	return nil
}

// repoFile is one change of r with its hunks, under the same per-file rule as the listing. nil when git no longer
// reports it.
func (b *diffBuilder) repoFile(ctx context.Context, r *diffRepo, e rawDiffEntry) (*agentv1.FileDiff, error) {
	old, size := r.entrySize(e, blobSizes(ctx, r.top, []rawDiffEntry{e}))
	if max(old, size) > maxDiffFileBytes {
		return r.tooLargeFile(e), nil
	}
	files, err := r.repoPatch(ctx, e.specs())
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.Path == r.abs(e.path) {
			return f, nil
		}
	}
	return nil, nil
}

func (r *diffRepo) abs(rel string) string {
	return sanitizeUTF8(filepath.Join(r.root, filepath.FromSlash(rel)))
}

func (r *diffRepo) absOld(rel string) string {
	if rel == "" {
		return ""
	}
	return sanitizeUTF8(filepath.Join(r.root, filepath.FromSlash(rel)))
}

type rawDiffEntry struct {
	oldOID, newOID, code, path, oldPath string
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
		e := rawDiffEntry{oldOID: meta[2], newOID: meta[3], code: meta[4][:1], path: fields[i+1]}
		i += 2
		if (e.code == "R" || e.code == "C") && i < len(fields) {
			e.oldPath, e.path = e.path, fields[i]
			i++
		}
		res = append(res, e)
	}
	return res
}

// numstatEntry is a file's line of `git diff --numstat -z`.
type numstatEntry struct {
	path, oldPath        string
	additions, deletions int32
	binary               bool
}

// parseNumstat parses `git diff --numstat -z`: "add\tdel\tpath\0" or, for a rename, "add\tdel\t\0old\0new\0". Binary
// files have "-" for both counts.
func parseNumstat(out []byte) []numstatEntry {
	var res []numstatEntry
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); i++ {
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		n := numstatEntry{path: parts[2], binary: parts[0] == "-"}
		if a, err := strconv.ParseInt(parts[0], 10, 32); err == nil {
			n.additions = int32(a)
		}
		if d, err := strconv.ParseInt(parts[1], 10, 32); err == nil {
			n.deletions = int32(d)
		}
		if n.path == "" {
			if i+2 >= len(fields) {
				break
			}
			n.oldPath, n.path = fields[i+1], fields[i+2]
			i += 2
		}
		res = append(res, n)
	}
	return res
}

// blobSizes returns the sizes of the changes' sides that git has, in one git process. Failing leaves them unknown (0): the patch is
// then not bounded by them, which beats losing the diff.
func blobSizes(ctx context.Context, top string, entries []rawDiffEntry) map[string]int64 {
	var stdin strings.Builder
	for _, e := range entries {
		for _, oid := range []string{e.oldOID, e.newOID} {
			if strings.Trim(oid, "0") != "" {
				stdin.WriteString(oid + "\n")
			}
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
