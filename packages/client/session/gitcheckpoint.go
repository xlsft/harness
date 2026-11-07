package session

import (
	"bufio"
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
	"strings"
	"sync"
	"time"
)

// Turn checkpoints of whole working trees through git (docs/design/turn-snapshots.md). At the start of every turn
// the state of each repository holding a working directory goes into a tree object, written through a private index
// so the user's index, HEAD, branches and stash stay as they are. Working directories outside git get the same in a
// private repository of the session. Rewinding restores what differs from that tree, whatever changed the files.

const (
	// GitCheckpointBudget bounds one turn's capture; tool calls wait for it at most that long from the turn's start.
	GitCheckpointBudget = 5 * time.Second
	// gitRestoreBudget bounds a rewind's git work.
	gitRestoreBudget = 2 * time.Minute
	// An untracked file larger than this is never captured (nor deleted by a rewind).
	maxCheckpointFileBytes = 8 << 20
	// CheckpointRefPrefix names the private refs that keep the checkpoint trees from git gc.
	CheckpointRefPrefix = "refs/harness/checkpoints/"
	// gitStateDir is the file history's folder for the private indexes, repositories and the roots list.
	gitStateDir   = "git"
	rootsFileName = "roots"
)

// More new untracked files than this, or more bytes of them, since the last capture: the capture is skipped.
// Variables for the tests.
var (
	maxCheckpointNewFiles       = 20000
	maxCheckpointNewBytes int64 = 128 << 20
)

// GitTree is a repository's checkpoint at a turn.
type GitTree struct {
	// Root is the work tree's top level, resolved.
	Root string `json:"root"`
	Tree string `json:"tree"`
	// Private: the folder is outside git and the tree lives in the session's own repository.
	Private bool `json:"private,omitempty"`
}

// GitOptions turns on git checkpoints (EnableGit).
type GitOptions struct {
	SessionID string
	// Dirs returns the session's working directories at the start of a turn.
	Dirs func() []string
	// Budget bounds a capture; GitCheckpointBudget when 0.
	Budget time.Duration
	// Folders also snapshots working directories outside git, in a private repository.
	Folders bool
	// Exclude are directories never captured (the client's own home).
	Exclude []string
}

// errCheckpointLimit: the capture would exceed a cap.
var errCheckpointLimit = errors.New("limit")

// gitAlwaysSkipped are path parts never captured, ignored or not.
var gitAlwaysSkipped = map[string]bool{".git": true, "node_modules": true, ".DS_Store": true}

type gitState struct {
	opts GitOptions
	// mu serializes the git work of the session (captures, listings, restores); taken before FileHistory.mu.
	mu      sync.Mutex
	pending *pendingCapture
	// off holds the roots captures stopped for in this session; told the reasons already said.
	off  map[string]bool
	told map[string]bool
}

type pendingCapture struct {
	done    chan struct{}
	notices []string
	taken   bool
}

// gitTarget is one tree to capture: a repository's work tree, or a folder in the session's private repository.
type gitTarget struct {
	root      string
	gitDir    string // private repository; "" for the root's own
	realIndex string // the repository's index, to seed the private one from
	private   bool
}

var gitAvailable = sync.OnceValue(func() bool {
	_, err := exec.LookPath("git")
	return err == nil
})

// EnableGit turns on git checkpoints for the turns that start after it. Without git it does nothing.
func (h *FileHistory) EnableGit(o GitOptions) {
	if !gitAvailable() || o.Dirs == nil || o.SessionID == "" {
		return
	}
	if o.Budget <= 0 {
		o.Budget = GitCheckpointBudget
	}
	h.mu.Lock()
	h.git = &gitState{opts: o, off: map[string]bool{}, told: map[string]bool{}}
	h.mu.Unlock()
}

func (h *FileHistory) gitDir() string { return filepath.Join(h.dir, gitStateDir) }

func rootKey(root string, private bool) string {
	s := sha256.Sum256([]byte(root))
	k := hex.EncodeToString(s[:8])
	if private {
		k += "-folder"
	}
	return k
}

func (h *FileHistory) indexOf(t gitTarget) string {
	return filepath.Join(h.gitDir(), rootKey(t.root, t.private)+".index")
}

// startGitCheckpoint captures the working trees for checkpoint messageID in the background. Called under h.mu.
func (h *FileHistory) startGitCheckpoint(messageID string) {
	g := h.git
	if g == nil {
		return
	}
	p := &pendingCapture{done: make(chan struct{})}
	g.pending = p
	dirs := g.opts.Dirs()
	go func() {
		defer close(p.done)
		ctx, cancel := context.WithTimeout(context.Background(), g.opts.Budget)
		defer cancel()
		p.notices = h.captureTurn(ctx, messageID, dirs)
	}()
}

// WaitCheckpoint waits for the current turn's git checkpoint (bounded by its budget) and returns, once, the notices
// it left: the repositories it had to skip. Tools call it before running, so no edit comes before its turn's snapshot.
func (h *FileHistory) WaitCheckpoint(ctx context.Context) []string {
	h.mu.Lock()
	var p *pendingCapture
	if h.git != nil {
		p = h.git.pending
	}
	h.mu.Unlock()
	if p == nil {
		return nil
	}
	select {
	case <-p.done:
	case <-ctx.Done():
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if p.taken {
		return nil
	}
	p.taken = true
	return p.notices
}

// Close waits for a turn checkpoint still being taken (bounded by its budget).
func (h *FileHistory) Close() { h.waitPending() }

func (h *FileHistory) waitPending() {
	h.mu.Lock()
	var p *pendingCapture
	if h.git != nil {
		p = h.git.pending
	}
	h.mu.Unlock()
	if p != nil {
		<-p.done
	}
}

// captureTurn captures every target and records the trees in checkpoint messageID.
func (h *FileHistory) captureTurn(ctx context.Context, messageID string, dirs []string) []string {
	g := h.git
	g.mu.Lock()
	defer g.mu.Unlock()
	var notices []string
	note := func(root, text string, stop bool) {
		if stop {
			g.off[root] = true
		}
		if g.told[root+"\x00"+text] {
			return
		}
		g.told[root+"\x00"+text] = true
		notices = append(notices, text)
	}
	var trees []GitTree
	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	targets := h.resolveTargets(rctx, dirs)
	rcancel()
	for _, t := range targets {
		if g.off[t.root] {
			continue
		}
		tree, err := h.capture(ctx, t, true)
		switch {
		case err == nil:
			trees = append(trees, GitTree{Root: t.root, Tree: tree, Private: t.private})
		case errors.Is(err, errCheckpointLimit):
			note(t.root, fmt.Sprintf("Turn checkpoints are off for %s in this session: more than %d new files or %d MiB since the last one. Rewind covers only the files the file tools changed there.",
				t.root, maxCheckpointNewFiles, maxCheckpointNewBytes>>20), true)
		case ctx.Err() != nil:
			note(t.root, fmt.Sprintf("Turn checkpoints are off for %s in this session: taking one took longer than %s. Rewind covers only the files the file tools changed there.",
				t.root, g.opts.Budget), true)
		default:
			note(t.root, fmt.Sprintf("Turn checkpoint skipped for %s: %v. Rewind covers only the files the file tools changed there.", t.root, err), false)
		}
	}
	if len(trees) == 0 {
		return notices
	}
	h.mu.Lock()
	var s *FileSnapshot
	for _, x := range h.snapshots {
		if x.MessageID == messageID {
			s = x
		}
	}
	keep := map[string]map[string]string{} // root -> message id -> tree
	if s != nil {
		s.GitTrees = trees
		h.record(s, true)
		for _, x := range h.snapshots {
			for _, gt := range x.GitTrees {
				if keep[gt.Root] == nil {
					keep[gt.Root] = map[string]string{}
				}
				keep[gt.Root][x.MessageID] = gt.Tree
			}
		}
	}
	h.mu.Unlock()
	// The refs keep the trees from gc. A failure costs only the protection, so it is not reported. The private
	// repositories need none: nothing runs gc in them.
	refCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, gt := range trees {
		if !gt.Private && h.writeRef(refCtx, h.targetOf(gt), keep[gt.Root]) == nil {
			h.noteRoot(gt.Root)
		}
	}
	return notices
}

// resolveTargets finds the repositories of the working directories, and the folders outside git.
func (h *FileHistory) resolveTargets(ctx context.Context, dirs []string) []gitTarget {
	o := h.git.opts
	seen := map[string]bool{}
	var out []gitTarget
	for _, d := range dirs {
		if d == "" {
			continue
		}
		real, err := filepath.EvalSymlinks(d)
		if err != nil || hasPart(real, ".git") || tooBroad(real) || h.excluded(real, o.Exclude) {
			continue
		}
		top, index, err := gitTopLevel(ctx, real)
		if err == nil {
			if seen[top] || tooBroad(top) || h.excluded(top, o.Exclude) {
				continue
			}
			seen[top] = true
			out = append(out, gitTarget{root: top, realIndex: index})
			continue
		}
		if !o.Folders || seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, h.privateTarget(real))
	}
	return out
}

// excluded: dir is or is inside a directory never captured (the client's own files). One that holds such a directory
// is captured without it (addAndWrite leaves it out of the new files).
func (h *FileHistory) excluded(dir string, exclude []string) bool {
	for _, e := range append([]string{h.dir}, exclude...) {
		if e == "" {
			continue
		}
		e = resolvePath(e)
		if e == dir || within(e, dir) {
			return true
		}
	}
	return false
}

func (h *FileHistory) privateTarget(root string) gitTarget {
	return gitTarget{root: root, gitDir: filepath.Join(h.gitDir(), rootKey(root, true)+".git"), private: true}
}

func (h *FileHistory) targetOf(gt GitTree) gitTarget {
	if gt.Private {
		return h.privateTarget(gt.Root)
	}
	return gitTarget{root: gt.Root}
}

// gitTopLevel returns the work tree's top level of dir and the path of its index.
func gitTopLevel(ctx context.Context, dir string) (top, index string, err error) {
	out, err := runGit(ctx, gitTarget{root: dir}, "", nil, "rev-parse", "--show-toplevel", "--git-path", "index")
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 || lines[0] == "" {
		return "", "", errors.New("not a work tree")
	}
	top = lines[0]
	if r, err := filepath.EvalSymlinks(top); err == nil {
		top = r
	}
	index = lines[1]
	if !filepath.IsAbs(index) {
		index = filepath.Join(dir, index)
	}
	return top, index, nil
}

// gitEnv is the environment without the variables that would point git elsewhere.
func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_PREFIX", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_LITERAL_PATHSPECS", "GIT_GLOB_PATHSPECS",
			"GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_LITERAL_PATHSPECS=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
}

// runGit runs git on the target, with index as its index when set.
func runGit(ctx context.Context, t gitTarget, index string, stdin []byte, args ...string) ([]byte, error) {
	full := append([]string{"-c", "gc.auto=0", "-c", "core.quotepath=off", "-c", "advice.addEmbeddedRepo=false",
		"-c", "advice.addIgnoredFile=false", "-c", "core.safecrlf=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = t.root
	env := gitEnv()
	if index != "" {
		env = append(env, "GIT_INDEX_FILE="+index)
	}
	if t.gitDir != "" {
		env = append(env, "GIT_DIR="+t.gitDir, "GIT_WORK_TREE="+t.root)
	}
	cmd.Env = env
	cmd.WaitDelay = time.Second
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		if msg == "" {
			return nil, fmt.Errorf("git %s: %w", args[0], err)
		}
		return nil, fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out, nil
}

// ensurePrivateRepo creates the session's repository for a folder outside git.
func ensurePrivateRepo(ctx context.Context, t gitTarget) error {
	if _, err := os.Stat(filepath.Join(t.gitDir, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(t.gitDir, 0o700); err != nil {
		return err
	}
	if _, err := runGit(ctx, t, "", nil, "init", "-q", "--template="); err != nil {
		return err
	}
	info := filepath.Join(t.gitDir, "info")
	_ = os.MkdirAll(info, 0o700)
	return os.WriteFile(filepath.Join(info, "exclude"), []byte(".git\nnode_modules/\n.DS_Store\n"), 0o600)
}

// capture writes the target's working tree into a tree object and returns its id. limits applies the caps on new
// files; a rewind lifts them, since it must see everything a turn created.
func (h *FileHistory) capture(ctx context.Context, t gitTarget, limits bool) (string, error) {
	if t.private {
		if err := ensurePrivateRepo(ctx, t); err != nil {
			return "", err
		}
	}
	idx := h.indexOf(t)
	if err := os.MkdirAll(filepath.Dir(idx), 0o700); err != nil {
		return "", err
	}
	_ = os.Remove(idx + ".lock") // only ever ours: a capture killed at its deadline leaves it
	seeded := false
	if _, err := os.Stat(idx); err != nil && t.realIndex != "" {
		if data, err := os.ReadFile(t.realIndex); err == nil && writeAtomic(idx, data, 0o600) == nil {
			seeded = true
		}
	}
	tree, err := h.addAndWrite(ctx, t, idx, limits)
	if err != nil && ctx.Err() == nil && !errors.Is(err, errCheckpointLimit) {
		// A seeded index git cannot use (a split index, say), or a file gone between listing and adding: once more
		// from an empty index when it was seeded, from the same otherwise.
		if seeded {
			_ = os.Remove(idx)
		}
		_ = os.Remove(idx + ".lock")
		tree, err = h.addAndWrite(ctx, t, idx, limits)
	}
	return tree, err
}

func (h *FileHistory) addAndWrite(ctx context.Context, t gitTarget, idx string, limits bool) (string, error) {
	if _, err := runGit(ctx, t, idx, nil, "add", "-u"); err != nil {
		return "", err
	}
	out, err := runGit(ctx, t, idx, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	var add bytes.Buffer
	var files int
	var total int64
	exclude := append([]string{h.dir}, h.excludes()...)
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" || strings.HasSuffix(p, "/") || hasSkippedPart(p) {
			continue // a trailing slash: a nested repository
		}
		full := filepath.Join(t.root, filepath.FromSlash(p))
		if insideAny(full, exclude) {
			continue
		}
		st, err := os.Lstat(full)
		if err != nil || !(st.Mode().IsRegular() || st.Mode()&fs.ModeSymlink != 0) || st.Size() > maxCheckpointFileBytes {
			continue
		}
		files++
		total += st.Size()
		if limits && (files > maxCheckpointNewFiles || total > maxCheckpointNewBytes) {
			return "", errCheckpointLimit
		}
		add.WriteString(p)
		add.WriteByte(0)
	}
	if add.Len() > 0 {
		// update-index takes a list in linear time (pathspecs in `git add` are matched against every file) and
		// cleans the content through the same filters.
		if _, err := runGit(ctx, t, idx, add.Bytes(), "update-index", "--add", "--remove", "-z", "--stdin"); err != nil {
			return "", err
		}
	}
	out, err = runGit(ctx, t, idx, nil, "write-tree", "--missing-ok") // the blobs were just added: no need to stat each one
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (h *FileHistory) excludes() []string {
	if h.git == nil {
		return nil
	}
	return h.git.opts.Exclude
}

// writeRef points the session's ref at a tree of its checkpoints in the target, so gc keeps them.
func (h *FileHistory) writeRef(ctx context.Context, t gitTarget, trees map[string]string) error {
	ref := CheckpointRefPrefix + h.git.opts.SessionID
	names := make([]string, 0, len(trees))
	for n := range trees {
		if n != "" && !strings.ContainsAny(n, "/\x00") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var in bytes.Buffer
	for _, n := range names {
		fmt.Fprintf(&in, "040000 tree %s\t%s\x00", trees[n], n)
	}
	out, err := runGit(ctx, t, "", in.Bytes(), "mktree", "-z")
	if err != nil {
		// A tree that is gone (gc after the ref was dropped): keep the ones still there.
		in.Reset()
		for _, n := range names {
			if _, err := runGit(ctx, t, "", nil, "cat-file", "-e", trees[n]+"^{tree}"); err == nil {
				fmt.Fprintf(&in, "040000 tree %s\t%s\x00", trees[n], n)
			}
		}
		if out, err = runGit(ctx, t, "", in.Bytes(), "mktree", "-z"); err != nil {
			return err
		}
	}
	_, err = runGit(ctx, t, "", nil, "update-ref", ref, strings.TrimSpace(string(out)))
	return err
}

// noteRoot adds a repository to the roots list (what deleting the session cleans up) and touches it (its age is the
// session's last checkpoint, for the cleanup period).
func (h *FileHistory) noteRoot(root string) {
	p := filepath.Join(h.gitDir(), rootsFileName)
	roots := readRoots(p)
	for _, r := range roots {
		if r == root {
			now := time.Now()
			_ = os.Chtimes(p, now, now)
			return
		}
	}
	_ = writeAtomic(p, []byte(strings.Join(append(roots, root), "\n")+"\n"), 0o600)
}

func readRoots(p string) []string {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// DropGitCheckpoints deletes the checkpoint ref of session id in every repository it has one in. historyDir is the
// session's file history folder; call it before removing that.
func DropGitCheckpoints(historyDir, id string) {
	if !gitAvailable() || id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, root := range readRoots(filepath.Join(historyDir, gitStateDir, rootsFileName)) {
		_, _ = runGit(ctx, gitTarget{root: root}, "", nil, "update-ref", "-d", CheckpointRefPrefix+id)
	}
}

// ForkGitCheckpoints gives a forked session the checkpoints of its source: the same trees under its own ref, and the
// roots list. The private repositories of folders outside git are copied.
func ForkGitCheckpoints(srcDir, dstDir, srcID, dstID string) error {
	src := filepath.Join(srcDir, gitStateDir)
	if _, err := os.Stat(src); err != nil {
		return nil
	}
	dst := filepath.Join(dstDir, gitStateDir)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	if err := copyTree(src, dst); err != nil {
		return err
	}
	if !gitAvailable() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, root := range readRoots(filepath.Join(src, rootsFileName)) {
		t := gitTarget{root: root}
		if out, err := runGit(ctx, t, "", nil, "rev-parse", "-q", "--verify", CheckpointRefPrefix+srcID); err == nil {
			_, _ = runGit(ctx, t, "", nil, "update-ref", CheckpointRefPrefix+dstID, strings.TrimSpace(string(out)))
		}
	}
	// The private repositories carry their own copy of the source's ref, which keeps the same trees.
	return nil
}

// copyTree copies the regular files under src into dst, keeping the layout (no symlinks are followed).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if de.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !de.Type().IsRegular() || strings.HasSuffix(p, ".lock") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}

// SweepGitCheckpoints deletes the checkpoint refs in repo whose session left no file history on this client (dirOf
// gives its folder), or whose last checkpoint is older than maxAge. It returns the session ids it dropped.
func SweepGitCheckpoints(ctx context.Context, repo string, dirOf func(id string) string, maxAge time.Duration, now time.Time) []string {
	if !gitAvailable() {
		return nil
	}
	t := gitTarget{root: repo}
	out, err := runGit(ctx, t, "", nil, "for-each-ref", "--format=%(refname)", CheckpointRefPrefix)
	if err != nil {
		return nil
	}
	var dropped []string
	for _, ref := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		id := strings.TrimPrefix(ref, CheckpointRefPrefix)
		if ref == "" || id == ref || strings.Contains(id, "/") {
			continue
		}
		st, err := os.Stat(filepath.Join(dirOf(id), gitStateDir, rootsFileName))
		if err == nil && (maxAge <= 0 || now.Sub(st.ModTime()) < maxAge) {
			continue
		}
		if _, err := runGit(ctx, t, "", nil, "update-ref", "-d", ref); err == nil {
			dropped = append(dropped, id)
		}
	}
	return dropped
}

// --- Rewind ---

// diffEntry is a line of `git diff-tree -r -z`.
type diffEntry struct {
	oldMode, newMode string
	status           byte
	path             string
}

func parseDiffTree(out []byte) []diffEntry {
	parts := strings.Split(string(out), "\x00")
	var es []diffEntry
	for i := 0; i+1 < len(parts); i += 2 {
		head := strings.TrimPrefix(parts[i], ":")
		f := strings.Fields(head)
		if len(f) < 5 || f[4] == "" {
			break
		}
		es = append(es, diffEntry{oldMode: f[0], newMode: f[1], status: f[4][0], path: parts[i+1]})
	}
	return es
}

// treePaths returns which of rels (slash-separated, relative to the root) tree holds as files.
func treePaths(ctx context.Context, t gitTarget, tree string, rels []string) map[string]bool {
	out := map[string]bool{}
	if len(rels) == 0 {
		return out
	}
	args := append([]string{"ls-tree", "-r", "-z", "--name-only", "--full-tree", tree, "--"}, rels...)
	res, err := runGit(ctx, t, "", nil, args...)
	if err != nil {
		return out
	}
	for _, p := range strings.Split(string(res), "\x00") {
		if p != "" {
			out[p] = true
		}
	}
	return out
}

// restoreGit puts the target's working tree back to tree, recording what it did in res. It returns the files
// among paths it is responsible for (in the checkpoint or in the current state), and false when it could not run,
// so the per-file backups take over.
func (h *FileHistory) restoreGit(ctx context.Context, gt GitTree, paths []string, res *RestoreResult) (map[string]bool, bool) {
	t := h.targetOf(gt)
	if !gt.Private {
		top, index, err := gitTopLevel(ctx, gt.Root)
		if err != nil || top != gt.Root {
			return nil, false
		}
		t.realIndex = index
	} else if _, err := os.Stat(gt.Root); err != nil {
		return nil, false
	}
	if _, err := runGit(ctx, t, "", nil, "cat-file", "-e", gt.Tree+"^{tree}"); err != nil {
		return nil, false
	}
	cur, err := h.capture(ctx, t, false)
	if err != nil {
		return nil, false
	}
	byRel := map[string]string{}
	var rels []string
	for _, p := range paths {
		if rel, ok := relSlash(gt.Root, resolvePath(p)); ok {
			byRel[rel] = p
			rels = append(rels, rel)
		}
	}
	covered := map[string]bool{}
	for _, tree := range []string{gt.Tree, cur} {
		for rel := range treePaths(ctx, t, tree, rels) {
			covered[byRel[rel]] = true
		}
	}
	if cur == gt.Tree {
		return covered, true
	}
	out, err := runGit(ctx, t, "", nil, "diff-tree", "-r", "-z", "--no-renames", gt.Tree, cur)
	if err != nil {
		return nil, false
	}
	var checkout, remove []string
	for _, e := range parseDiffTree(out) {
		if e.oldMode == "160000" || e.newMode == "160000" {
			continue // submodules are theirs to manage
		}
		if e.status == 'A' {
			remove = append(remove, e.path)
		} else {
			checkout = append(checkout, e.path)
		}
	}
	abs := func(rel string) string { return filepath.Join(gt.Root, filepath.FromSlash(rel)) }
	skip := func(rel, why string) {
		p := abs(rel)
		res.Skipped = append(res.Skipped, p)
		res.Reasons[p] = why
	}
	parents := map[string]bool{}
	for _, rel := range remove {
		if err := os.Remove(abs(rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			skip(rel, err.Error())
			continue
		}
		res.Restored = append(res.Restored, abs(rel))
		for d := pathDir(rel); d != ""; d = pathDir(d) {
			parents[d] = true
		}
	}
	h.removeNewDirs(ctx, t, gt.Tree, parents)
	if len(checkout) > 0 {
		h.checkoutPaths(ctx, t, gt.Tree, checkout, abs, skip, res)
	}
	return covered, true
}

func pathDir(rel string) string {
	i := strings.LastIndexByte(rel, '/')
	if i < 0 {
		return ""
	}
	return rel[:i]
}

// removeNewDirs removes the directories (relative) that the checkpoint did not have and that are now empty.
func (h *FileHistory) removeNewDirs(ctx context.Context, t gitTarget, tree string, dirs map[string]bool) {
	if len(dirs) == 0 {
		return
	}
	list := make([]string, 0, len(dirs))
	for d := range dirs {
		list = append(list, d)
	}
	// Deepest first, so a parent is empty by the time it is tried.
	sort.Slice(list, func(i, j int) bool { return strings.Count(list[i], "/") > strings.Count(list[j], "/") })
	var in bytes.Buffer
	for _, d := range list {
		in.WriteString(tree + ":" + d + "\n")
	}
	out, err := runGit(ctx, t, "", in.Bytes(), "cat-file", "--batch-check")
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for i, d := range list {
		if i < len(lines) && strings.HasSuffix(lines[i], " missing") {
			_ = os.Remove(filepath.Join(t.root, filepath.FromSlash(d))) // fails unless empty
		}
	}
}

// checkoutPaths writes rels back from tree through a private index, as git would check them out.
func (h *FileHistory) checkoutPaths(ctx context.Context, t gitTarget, tree string, rels []string, abs func(string) string, skip func(string, string), res *RestoreResult) {
	idx := filepath.Join(h.gitDir(), "restore.index")
	_ = os.Remove(idx)
	_ = os.Remove(idx + ".lock")
	defer os.Remove(idx)
	if _, err := runGit(ctx, t, idx, nil, "read-tree", tree); err != nil {
		for _, rel := range rels {
			skip(rel, err.Error())
		}
		return
	}
	run := func(batch []string) error {
		var in bytes.Buffer
		for _, rel := range batch {
			in.WriteString(rel)
			in.WriteByte(0)
		}
		_, err := runGit(ctx, t, idx, in.Bytes(), "checkout-index", "-f", "-z", "--stdin")
		return err
	}
	clear := func(rel string) {
		// A directory where the file was, or a file where a directory of it was, would stop git.
		p := abs(rel)
		if st, err := os.Lstat(p); err == nil && st.IsDir() {
			_ = os.RemoveAll(p)
		}
		for d := pathDir(rel); d != ""; d = pathDir(d) {
			if st, err := os.Lstat(abs(d)); err == nil && !st.IsDir() {
				_ = os.Remove(abs(d))
			}
		}
	}
	for _, rel := range rels {
		clear(rel)
	}
	if run(rels) == nil {
		for _, rel := range rels {
			res.Restored = append(res.Restored, abs(rel))
		}
		return
	}
	for _, rel := range rels {
		if err := run([]string{rel}); err != nil {
			skip(rel, err.Error())
			continue
		}
		res.Restored = append(res.Restored, abs(rel))
	}
}

// currentTrees captures the current state of each root with a checkpoint, for HasChanges; a root that fails maps
// to "".
func (h *FileHistory) currentTrees(ctx context.Context, roots map[string]GitTree) map[string]string {
	out := map[string]string{}
	for root, gt := range roots {
		t := h.targetOf(gt)
		if !gt.Private {
			top, index, err := gitTopLevel(ctx, root)
			if err != nil || top != root {
				out[root] = ""
				continue
			}
			t.realIndex = index
		}
		tree, err := h.capture(ctx, t, true)
		if err != nil {
			tree = ""
		}
		out[root] = tree
	}
	return out
}

// --- paths ---

func hasPart(p, part string) bool {
	for _, x := range strings.Split(filepath.ToSlash(p), "/") {
		if x == part {
			return true
		}
	}
	return false
}

func hasSkippedPart(rel string) bool {
	for _, x := range strings.Split(rel, "/") {
		if gitAlwaysSkipped[x] {
			return true
		}
	}
	return false
}

// tooBroad: the file system root and the home directory are far too big to snapshot.
func tooBroad(real string) bool {
	if real == filepath.Dir(real) {
		return true
	}
	home, err := os.UserHomeDir()
	return err == nil && resolvePath(home) == real
}

// within: path is strictly inside base.
func within(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func insideAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if d != "" && (p == d || within(d, p) || within(resolvePath(d), p)) {
			return true
		}
	}
	return false
}

func relSlash(base, p string) (string, bool) {
	if !within(base, p) {
		return "", false
	}
	rel, _ := filepath.Rel(base, p)
	return filepath.ToSlash(rel), true
}

// resolvePath resolves symlinks in p; the part that does not exist (a deleted file) stays as it is.
func resolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolvePath(parent), filepath.Base(p))
}
