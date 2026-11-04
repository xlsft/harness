package server

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/worktree"
)

// What a view of the changes compares with (session_diff.proto, DiffBase), and ListDiffBases to choose from. A name
// from a request is checked here and turned into a commit's full name: nothing after that passes it on to git.

const (
	// maxDiffCommits and maxDiffBranches cap ListDiffBases.
	maxDiffCommits  = 200
	maxDiffBranches = 500
	maxDiffRefBytes = 1024

	labelWorkingTree  = "working tree"
	labelHead         = "HEAD"
	labelNoCommits    = "no commits"
	labelEmptyTree    = "empty"
	labelSessionStart = "session start"
	shortSHALen       = 7
)

// diffChoice is a checked DiffBase; ref is the name for REF and COMMIT.
type diffChoice struct {
	kind agentv1.DiffBaseKind
	ref  string
}

var diffSHARe = regexp.MustCompile(`^[0-9a-fA-F]{4,64}$`)

func parseDiffBase(m *agentv1.DiffBase) (diffChoice, error) {
	switch m.GetKind() {
	case agentv1.DiffBaseKind_DIFF_BASE_KIND_UNSPECIFIED, agentv1.DiffBaseKind_DIFF_BASE_KIND_ALL:
		return diffChoice{kind: agentv1.DiffBaseKind_DIFF_BASE_KIND_ALL}, nil
	case agentv1.DiffBaseKind_DIFF_BASE_KIND_UNCOMMITTED:
		return diffChoice{kind: m.GetKind()}, nil
	case agentv1.DiffBaseKind_DIFF_BASE_KIND_REF:
		if !validDiffRef(m.GetRef()) {
			return diffChoice{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid ref %q", m.GetRef()))
		}
		return diffChoice{kind: m.GetKind(), ref: m.GetRef()}, nil
	case agentv1.DiffBaseKind_DIFF_BASE_KIND_COMMIT:
		if !diffSHARe.MatchString(m.GetSha()) {
			return diffChoice{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid commit %q", m.GetSha()))
		}
		return diffChoice{kind: m.GetKind(), ref: m.GetSha()}, nil
	}
	return diffChoice{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown base kind %d", m.GetKind()))
}

// validDiffRef accepts a plain ref name: what `git check-ref-format` allows, so none of the revision syntax. Above
// all, nothing that git could read as an option.
func validDiffRef(ref string) bool {
	if ref == "" || len(ref) > maxDiffRefBytes || ref[0] == '-' || !utf8.ValidString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "@{") {
		return false
	}
	for _, c := range ref {
		if c <= ' ' || c == 0x7f || strings.ContainsRune("~^:?*[\\", c) {
			return false
		}
	}
	return true
}

// gitSaidNo: git ran and refused, as opposed to it failing to run or running out of time.
func gitSaidNo(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit)
}

// commitOf resolves a name to its commit's full name; "" when it names none. The name must not start with "-".
func commitOf(ctx context.Context, top, name string) (string, error) {
	out, err := diffGit(ctx, top, "", "rev-parse", "--verify", "-q", name+"^{commit}")
	if err != nil {
		if gitSaidNo(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// mergeBase of a commit and HEAD; "" when they share no history.
func mergeBase(ctx context.Context, top, commit string) (string, error) {
	out, err := diffGit(ctx, top, "", "merge-base", commit, "HEAD")
	if err != nil {
		if gitSaidNo(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func emptyTree(ctx context.Context, top string) (string, error) {
	out, err := diffGit(ctx, top, "", "hash-object", "-t", "tree", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func shortSHA(sha string) string {
	if len(sha) > shortSHALen {
		return sha[:shortSHALen]
	}
	return sha
}

// resolveBases sets what each repository compares with for the choice, and the base in effect. Outside git only the
// snapshot rules exist, so whatever was asked for is ALL.
func (b *diffBuilder) resolveBases(ctx context.Context, c diffChoice) error {
	if len(b.repos) == 0 {
		b.kind, b.baseLabel, b.headLabel = agentv1.DiffBaseKind_DIFF_BASE_KIND_ALL, labelSessionStart, labelWorkingTree
		return nil
	}
	b.kind = c.kind
	var repos []*diffRepo
	found := false
	for _, r := range b.repos {
		switch c.kind {
		case agentv1.DiffBaseKind_DIFF_BASE_KIND_UNCOMMITTED:
			if err := r.setUncommitted(ctx); err != nil {
				return err
			}
			found = true
		case agentv1.DiffBaseKind_DIFF_BASE_KIND_REF:
			ok, err := r.setRef(ctx, c.ref)
			if err != nil {
				return err
			}
			if !ok {
				if err := r.setAll(ctx); err != nil {
					return err
				}
			}
			found = found || ok
		case agentv1.DiffBaseKind_DIFF_BASE_KIND_COMMIT:
			if found {
				continue
			}
			ok, err := r.setCommit(ctx, c.ref)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			found = true
		default:
			if err := r.setAll(ctx); err != nil {
				return err
			}
			found = true
		}
		repos = append(repos, r)
	}
	if !found {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s is not in the session's repositories", c.ref))
	}
	b.repos = repos
	shown := b.repos[0]
	for _, r := range b.repos {
		if r.kind == c.kind {
			shown = r
			break
		}
	}
	b.baseLabel, b.headLabel = shown.baseLabel, shown.headLabel
	return nil
}

// setAgainst makes r compare its working tree with rev.
func (r *diffRepo) setAgainst(kind agentv1.DiffBaseKind, rev, label string) {
	r.kind, r.revs, r.baseLabel, r.headLabel = kind, []string{rev}, sanitizeUTF8(label), labelWorkingTree
}

// setUnborn: before the first commit there is only the empty tree to compare with. False when HEAD exists.
func (r *diffRepo) setUnborn(ctx context.Context, kind agentv1.DiffBaseKind) (bool, error) {
	head, err := commitOf(ctx, r.top, "HEAD")
	if err != nil || head != "" {
		return false, err
	}
	tree, err := emptyTree(ctx, r.top)
	if err != nil {
		return false, err
	}
	r.setAgainst(kind, tree, labelNoCommits)
	return true, nil
}

func (r *diffRepo) setUncommitted(ctx context.Context) error {
	kind := agentv1.DiffBaseKind_DIFF_BASE_KIND_UNCOMMITTED
	if unborn, err := r.setUnborn(ctx, kind); err != nil || unborn {
		return err
	}
	r.setAgainst(kind, "HEAD", labelHead)
	return nil
}

func (r *diffRepo) setAll(ctx context.Context) error {
	kind := agentv1.DiffBaseKind_DIFF_BASE_KIND_ALL
	if unborn, err := r.setUnborn(ctx, kind); err != nil || unborn {
		return err
	}
	base, label, err := r.allBase(ctx)
	if err != nil {
		return err
	}
	r.setAgainst(kind, base, label)
	return nil
}

// allBase is what ALL compares with when HEAD exists: a commit (or HEAD itself) and how to call it.
func (r *diffRepo) allBase(ctx context.Context) (rev, label string, err error) {
	// The ref of a worktree of the client; gone or unrelated by now, HEAD is left.
	if r.baseRef != "" {
		if !strings.HasPrefix(r.baseRef, "-") {
			mb, err := mergeBase(ctx, r.top, r.baseRef)
			if err != nil {
				return "", "", err
			}
			if mb != "" {
				label := r.baseRef
				if worktree.IsCommit(label) {
					label = shortSHA(label)
				}
				return mb, label, nil
			}
		}
		return "HEAD", labelHead, nil
	}
	full, short, err := defaultDiffBranch(ctx, r.top)
	if err != nil || full == "" {
		return "HEAD", labelHead, err
	}
	head, err := diffGit(ctx, r.top, "", "symbolic-ref", "-q", "HEAD")
	if err != nil && !gitSaidNo(err) {
		return "", "", err
	}
	// A detached HEAD is on no branch, so not on this one either.
	if err == nil && strings.TrimSpace(string(head)) == "refs/heads/"+strings.TrimPrefix(short, "origin/") {
		return "HEAD", labelHead, nil
	}
	mb, err := mergeBase(ctx, r.top, full)
	if err != nil || mb == "" {
		return "HEAD", labelHead, err
	}
	return mb, short, nil
}

// defaultDiffBranch finds the repository's default branch: origin/HEAD, then main, then master; "" when it has none.
// short is its name for showing: "origin/main" or "main". workspace.DefaultBranch answers a different question: a
// bare name, which may have no local branch.
func defaultDiffBranch(ctx context.Context, top string) (full, short string, err error) {
	out, err := diffGit(ctx, top, "", "symbolic-ref", "-q", "refs/remotes/origin/HEAD")
	if err != nil && !gitSaidNo(err) {
		return "", "", err
	}
	if err == nil {
		full = strings.TrimSpace(string(out))
		sha, err := commitOf(ctx, top, full)
		if err != nil {
			return "", "", err
		}
		if sha != "" { // else a remote branch since deleted
			return full, strings.TrimPrefix(full, "refs/remotes/"), nil
		}
	}
	for _, name := range []string{"main", "master"} {
		sha, err := commitOf(ctx, top, "refs/heads/"+name)
		if err != nil {
			return "", "", err
		}
		if sha != "" {
			return "refs/heads/" + name, name, nil
		}
	}
	return "", "", nil
}

// setRef makes r compare with the merge base with ref. False when r does not have the ref; an error when they share
// no history, since there is then nothing to answer with.
func (r *diffRepo) setRef(ctx context.Context, ref string) (bool, error) {
	sha, err := commitOf(ctx, r.top, ref)
	if err != nil || sha == "" {
		return false, err
	}
	mb, err := mergeBase(ctx, r.top, sha)
	if err != nil {
		return false, err
	}
	if mb == "" {
		return false, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s and HEAD share no history", ref))
	}
	r.setAgainst(agentv1.DiffBaseKind_DIFF_BASE_KIND_REF, mb, ref)
	return true, nil
}

// setCommit makes r compare the commit with its first parent, or the empty tree for a root commit. False when r does
// not have it.
func (r *diffRepo) setCommit(ctx context.Context, name string) (bool, error) {
	sha, err := commitOf(ctx, r.top, name)
	if err != nil || sha == "" {
		return false, err
	}
	parent, err := commitOf(ctx, r.top, sha+"^1")
	if err != nil {
		return false, err
	}
	label := shortSHA(parent)
	if parent == "" {
		label = labelEmptyTree
		if parent, err = emptyTree(ctx, r.top); err != nil {
			return false, err
		}
	}
	r.kind, r.revs, r.baseLabel, r.headLabel = agentv1.DiffBaseKind_DIFF_BASE_KIND_COMMIT, []string{parent, sha}, label, shortSHA(sha)
	return true, nil
}

func (s *Server) ListDiffBases(ctx context.Context, req *connect.Request[agentv1.ListDiffBasesRequest]) (*connect.Response[agentv1.ListDiffBasesResponse], error) {
	src, err := s.diffSourcesOf(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	resp, err := listDiffBases(ctx, src)
	if err != nil {
		return nil, diffRPCError(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

func listDiffBases(ctx context.Context, src *diffSources) (*agentv1.ListDiffBasesResponse, error) {
	b := &diffBuilder{src: src}
	b.findRepos(ctx)
	if len(b.repos) == 0 {
		return &agentv1.ListDiffBasesResponse{}, nil
	}
	r := b.repos[0]
	resp := &agentv1.ListDiffBasesResponse{InGit: true}
	if err := r.addBranches(ctx, resp); err != nil {
		return nil, err
	}
	if err := r.setAll(ctx); err != nil {
		return nil, err
	}
	resp.DefaultBaseLabel = r.baseLabel
	head, err := commitOf(ctx, r.top, "HEAD")
	if err != nil {
		return nil, err
	}
	if head == "" {
		return resp, nil // no commits yet: no base to name, nothing since it
	}
	resp.DefaultBaseRef = r.baseLabel
	resp.DefaultBaseSha = r.revs[0]
	if r.revs[0] == "HEAD" {
		resp.DefaultBaseSha = head
		return resp, nil // on the base itself: nothing since it
	}
	return resp, r.addCommits(ctx, resp)
}

func (r *diffRepo) addBranches(ctx context.Context, resp *agentv1.ListDiffBasesResponse) error {
	out, err := diffGit(ctx, r.top, "", "for-each-ref", "--sort=-committerdate", "--format=%(refname)%00%(refname:short)%00%(symref)%00%(HEAD)", "refs/heads", "refs/remotes")
	if err != nil {
		return err
	}
	var local, remote []*agentv1.DiffBranch
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\x00")
		if len(f) != 4 || f[2] != "" { // a symbolic ref: origin/HEAD, not a branch
			continue
		}
		d := &agentv1.DiffBranch{Name: sanitizeUTF8(f[1]), Current: f[3] == "*", Remote: strings.HasPrefix(f[0], "refs/remotes/")}
		if d.Remote {
			remote = append(remote, d)
		} else {
			local = append(local, d)
		}
	}
	resp.Branches = append(local, remote...)
	if len(resp.Branches) > maxDiffBranches {
		resp.Branches, resp.BranchesTruncated = resp.Branches[:maxDiffBranches], true
	}
	return nil
}

// addCommits lists HEAD's commits since r's base, a full name from setAll.
func (r *diffRepo) addCommits(ctx context.Context, resp *agentv1.ListDiffBasesResponse) error {
	out, err := diffGit(ctx, r.top, "", "-c", "log.showSignature=false", "log", "-z", "--format=%H%x00%s%x00%ct", "--max-count="+strconv.Itoa(maxDiffCommits+1), r.revs[0]+"..HEAD")
	if err != nil {
		return err
	}
	f := strings.Split(string(out), "\x00")
	for i := 0; i+2 < len(f); i += 3 {
		if len(resp.Commits) == maxDiffCommits {
			resp.CommitsTruncated = true
			break
		}
		c := &agentv1.DiffCommit{Sha: f[i], Subject: sanitizeUTF8(f[i+1])}
		if sec, err := strconv.ParseInt(f[i+2], 10, 64); err == nil {
			c.CommittedAt = time.Unix(sec, 0).UTC().Format(time.RFC3339)
		}
		resp.Commits = append(resp.Commits, c)
	}
	return nil
}
