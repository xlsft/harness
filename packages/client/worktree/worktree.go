package worktree

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Dir is where worktrees live, relative to the repository's main checkout.
const Dir = ".harness/worktrees"

// excludePattern is the line added to .git/info/exclude.
const excludePattern = "/" + Dir + "/"

// BranchPrefix prefixes the default branch name of a worktree.
const BranchPrefix = "harness/"

// Errors returned by Create. The server maps ErrInvalidName and
// ErrInvalidRef to INVALID_ARGUMENT and the others to FAILED_PRECONDITION.
var (
	ErrInvalidName      = errors.New("invalid worktree name")
	ErrInvalidRef       = errors.New("invalid ref")
	ErrBranchCheckedOut = errors.New("branch is already checked out in another worktree")
	ErrNoCommits        = errors.New("the repository has no commits yet")
	ErrNameTaken        = errors.New("no free worktree name")
)

// Options select the worktree to create (harness.agent.v1.WorktreeOptions).
type Options struct {
	// Name of the worktree directory; derived (adjective-noun-xxxx) when
	// empty. A taken name gets a numeric suffix (name-2, name-3, ...).
	Name string
	// Branch to create; default BranchPrefix+name. An existing branch is
	// checked out instead when no worktree has it checked out.
	Branch string
	// BaseRef the new branch starts from; default the current branch (or
	// commit, when detached) of the work tree containing cwd.
	BaseRef string
}

// Result describes a created worktree.
type Result struct {
	// Name is the final worktree name (after collision handling).
	Name string
	// Path is the absolute worktree path: <RepositoryPath>/.harness/worktrees/<Name>.
	Path string
	// Branch is checked out in the worktree.
	Branch string
	// NewBranch reports that Create created Branch.
	NewBranch bool
	// BaseRef is the ref the new branch was created from; for an existing
	// branch, the commit it pointed at when it was checked out.
	BaseRef string
	// RepositoryPath is the repository's main checkout.
	RepositoryPath string
	// Cwd is the session directory inside the worktree: the same path
	// relative to the work tree root as the requested cwd.
	Cwd string
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidateName checks that name is usable as a single path segment and as
// a branch name suffix.
func ValidateName(name string) error {
	if !nameRe.MatchString(name) || strings.Contains(name, "..") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return fmt.Errorf("%w %q: use 1-64 letters, digits, '.', '_' or '-', starting with a letter or digit", ErrInvalidName, name)
	}
	return nil
}

// Create adds a worktree of the repository containing cwd (see Options).
// It returns ErrNotRepository outside a git work tree.
func Create(ctx context.Context, cwd string, o Options) (*Result, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	abs = resolve(abs)
	repo, err := Find(ctx, abs)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(repo.Toplevel, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		rel = "."
	}
	name := strings.TrimSpace(o.Name)
	explicit := name != ""
	if explicit {
		if err := ValidateName(name); err != nil {
			return nil, err
		}
	}
	branch := strings.TrimSpace(o.Branch)
	if branch != "" {
		if err := checkBranchName(ctx, repo.Toplevel, branch); err != nil {
			return nil, err
		}
	}
	checkedOut, err := checkedOutBranches(ctx, repo.Toplevel)
	if err != nil {
		return nil, err
	}

	res := &Result{RepositoryPath: repo.Main}
	base := strings.TrimSpace(o.BaseRef)
	existing := branch != "" && branchExists(ctx, repo.Toplevel, branch)
	if existing {
		if where, ok := checkedOut[branch]; ok {
			return nil, fmt.Errorf("%w: %s is checked out at %s", ErrBranchCheckedOut, branch, where)
		}
		sha, err := git(ctx, QueryTimeout, repo.Toplevel, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidRef, branch)
		}
		res.BaseRef = strings.TrimSpace(sha)
	} else {
		if base == "" {
			if base, err = currentRef(ctx, repo.Toplevel); err != nil {
				return nil, err
			}
		} else if err := verifyCommit(ctx, repo.Toplevel, base); err != nil {
			return nil, err
		}
		res.BaseRef = base
	}

	if err := EnsureExcluded(repo.CommonDir); err != nil {
		return nil, fmt.Errorf("update .git/info/exclude: %w", err)
	}
	root := filepath.Join(repo.Main, filepath.FromSlash(Dir))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}

	for attempt := 0; attempt < 20; attempt++ {
		cand := name
		switch {
		case !explicit:
			cand = RandomName()
		case attempt > 0:
			cand = fmt.Sprintf("%s-%d", name, attempt+1)
		}
		path := filepath.Join(root, cand)
		if _, err := os.Lstat(path); err == nil {
			continue
		}
		br := branch
		if br == "" {
			br = BranchPrefix + cand
			if err := checkBranchName(ctx, repo.Toplevel, br); err != nil {
				return nil, err
			}
			if branchExists(ctx, repo.Toplevel, br) {
				continue
			}
		}
		args := []string{"worktree", "add"}
		if existing {
			args = append(args, path, br)
		} else {
			args = append(args, "--no-track", "-b", br, path, base)
		}
		if _, err := git(ctx, AddTimeout, repo.Toplevel, args...); err != nil {
			return nil, err
		}
		res.Name, res.Path, res.Branch, res.NewBranch = cand, path, br, !existing
		res.Cwd = filepath.Join(path, rel)
		if st, err := os.Stat(res.Cwd); err != nil || !st.IsDir() {
			// The directory is untracked or ignored in the source checkout.
			if err := os.MkdirAll(res.Cwd, 0o755); err != nil {
				res.Cwd = path
			}
		}
		return res, nil
	}
	return nil, fmt.Errorf("%w for %q in %s", ErrNameTaken, name, root)
}

// Rollback removes a worktree that Create just made (used when the session
// fails to start), and its branch when Create created it. It must not be used
// on worktrees a session has worked in.
func Rollback(ctx context.Context, r *Result) error {
	if r == nil {
		return nil
	}
	_, err := git(ctx, AddTimeout, r.RepositoryPath, "worktree", "remove", "--force", r.Path)
	if err == nil && r.NewBranch {
		_, err = git(ctx, QueryTimeout, r.RepositoryPath, "branch", "-D", r.Branch)
	}
	return err
}

// RemoveIfUntouched removes a worktree when nothing was done in it: no uncommitted change and no commit past the
// point Create started it from. It reports whether it did.
func RemoveIfUntouched(ctx context.Context, r *Result) (bool, error) {
	if r == nil {
		return false, nil
	}
	status, err := git(ctx, QueryTimeout, r.Path, "status", "--porcelain")
	if err != nil || strings.TrimSpace(status) != "" {
		return false, err
	}
	ahead, err := git(ctx, QueryTimeout, r.Path, "rev-list", "--count", r.BaseRef+"..HEAD")
	if err != nil || strings.TrimSpace(ahead) != "0" {
		return false, err
	}
	return true, Rollback(ctx, r)
}

// EnsureExcluded adds "/.harness/worktrees/" to <commonDir>/info/exclude
// unless an equivalent line is already there.
func EnsureExcluded(commonDir string) error {
	path := filepath.Join(commonDir, "info", "exclude")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, l := range strings.Split(string(data), "\n") {
		switch strings.TrimSpace(l) {
		case excludePattern, Dir + "/", Dir, "/" + Dir, ".harness/", "/.harness/", ".harness", "/.harness":
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	add := "# harness session worktrees\n" + excludePattern + "\n"
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		add = "\n" + add
	}
	_, err = f.WriteString(add)
	return err
}

func checkBranchName(ctx context.Context, dir, branch string) error {
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("%w: branch %q", ErrInvalidRef, branch)
	}
	if _, err := git(ctx, QueryTimeout, dir, "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("%w: branch %q", ErrInvalidRef, branch)
	}
	return nil
}

func branchExists(ctx context.Context, dir, branch string) bool {
	_, err := git(ctx, QueryTimeout, dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func verifyCommit(ctx context.Context, dir, ref string) error {
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("%w: base %q", ErrInvalidRef, ref)
	}
	if _, err := git(ctx, QueryTimeout, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
		return fmt.Errorf("%w: base %q is not a commit", ErrInvalidRef, ref)
	}
	return nil
}

// currentRef returns the current branch of dir, or the HEAD commit when
// detached.
func currentRef(ctx context.Context, dir string) (string, error) {
	if _, err := git(ctx, QueryTimeout, dir, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		return "", ErrNoCommits
	}
	if out, err := git(ctx, QueryTimeout, dir, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		if b := strings.TrimSpace(out); b != "" {
			return b, nil
		}
	}
	out, err := git(ctx, QueryTimeout, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// checkedOutBranches maps branch names to the worktree that has them
// checked out.
func checkedOutBranches(ctx context.Context, dir string) (map[string]string, error) {
	out, err := git(ctx, QueryTimeout, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	wt := ""
	for _, l := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(l, "worktree "); ok {
			wt = p
		} else if b, ok := strings.CutPrefix(l, "branch refs/heads/"); ok {
			m[b] = wt
		}
	}
	return m, nil
}

var (
	adjectives = []string{"amber", "brave", "brisk", "calm", "clever", "cosmic", "crisp", "daring", "eager", "fancy",
		"gentle", "golden", "happy", "humble", "jolly", "keen", "lively", "lucky", "mellow", "merry", "nimble", "noble",
		"plucky", "proud", "quick", "quiet", "rapid", "shiny", "silent", "snappy", "steady", "sunny", "swift", "tidy",
		"vivid", "witty", "zesty"}
	nouns = []string{"badger", "beacon", "breeze", "canyon", "cedar", "comet", "coral", "falcon", "fern", "harbor",
		"heron", "island", "lantern", "maple", "meadow", "meteor", "otter", "panda", "pebble", "pine", "planet", "raven",
		"river", "rocket", "sparrow", "spruce", "summit", "thistle", "tiger", "tulip", "walrus", "willow", "zephyr"}
)

// RandomName returns a name of the form adjective-noun-xxxx (xxxx: four
// lowercase letters or digits).
func RandomName() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	suffix := make([]byte, 4)
	for i := range suffix {
		suffix[i] = alphabet[randInt(len(alphabet))]
	}
	return adjectives[randInt(len(adjectives))] + "-" + nouns[randInt(len(nouns))] + "-" + string(suffix)
}

func randInt(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}
