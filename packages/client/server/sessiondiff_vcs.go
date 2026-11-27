package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/vcshooks"
)

// A working directory outside git that a VcsStatus hook claims (docs/design/vcs-hooks.md) shows what its version control
// says changed, not the snapshot: the hook lists the files, with their line counts when it has them, and a VcsDiff hook
// gives the unified diff for the counts it left out and for the hunks. Under COMMIT nothing of it is involved, as for
// every folder outside git.

// vcsSource is what the diff needs of the session's VCS hooks (vcshooks.Hooks); nil when no VcsStatus hook runs.
type vcsSource interface {
	Status(ctx context.Context, dir string) (*vcshooks.Status, error)
	Diff(ctx context.Context, dir string, paths []string) ([]byte, error)
}

// hookRepo is a working copy a VcsStatus hook answered for. dir is the folder the hook ran in, root the copy's root as
// the session knows it, real the same resolved.
type hookRepo struct {
	dir, root, real string
	status          *vcshooks.Status
	// keep are the files that get hunks (relative to the root); patch the VcsDiff already read, by path.
	keep  []string
	patch map[string]*agentv1.FileDiff
}

func (r *hookRepo) abs(rel string) string {
	if rel == "" {
		return ""
	}
	return sanitizeUTF8(filepath.Join(r.root, filepath.FromSlash(rel)))
}

func hookStatus(s string) agentv1.FileDiffStatus {
	switch s {
	case "added":
		return agentv1.FileDiffStatus_FILE_DIFF_STATUS_ADDED
	case "deleted":
		return agentv1.FileDiffStatus_FILE_DIFF_STATUS_DELETED
	case "renamed":
		return agentv1.FileDiffStatus_FILE_DIFF_STATUS_RENAMED
	}
	return agentv1.FileDiffStatus_FILE_DIFF_STATUS_MODIFIED
}

// scanHookRepos asks the VcsStatus hooks about each folder outside git and returns the folders none claimed, which keep
// the snapshot rules.
func (b *diffBuilder) scanHookRepos(ctx context.Context, folders []diffFolder) ([]diffFolder, error) {
	var rest []diffFolder
	for _, f := range folders {
		if b.inHookRepo(f.real) {
			continue
		}
		st, err := b.src.vcs.Status(ctx, f.clean)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.clean, err)
		}
		if st == nil {
			rest = append(rest, f)
			continue
		}
		real := resolvedPath(st.Root)
		r := &hookRepo{dir: f.clean, real: real, root: lexicalPath(f.clean, f.real, real), status: st}
		b.hookRepos = append(b.hookRepos, r)
		b.sign("vcs", r.root, st.Base)
		for _, file := range st.Files {
			full := filepath.Join(real, filepath.FromSlash(file.Path))
			b.seen[full] = true
			b.sign(file.Path, file.Status, file.OldPath, count(file.Additions), count(file.Deletions), strconv.FormatBool(file.Binary))
			b.signStat(full)
		}
	}
	if len(b.repos) == 0 && len(b.hookRepos) > 0 {
		b.baseLabel, b.headLabel = labelVcsBase, labelWorkingCopy
		if base := b.hookRepos[0].status.Base; base != "" {
			b.baseLabel = base
		}
	}
	return rest, nil
}

const (
	labelVcsBase     = "base"
	labelWorkingCopy = "working copy"
)

func count(n *int32) string {
	if n == nil {
		return "-"
	}
	return strconv.Itoa(int(*n))
}

func (b *diffBuilder) inHookRepo(real string) bool {
	for _, r := range b.hookRepos {
		if real == r.real {
			return true
		}
		if _, ok := relWithin(r.real, real); ok {
			return true
		}
	}
	return false
}

// hookFileSize is the size of a listed file on disk (0 when it is gone).
func (r *hookRepo) fileSize(rel string) int64 {
	if st, err := os.Lstat(filepath.Join(r.real, filepath.FromSlash(rel))); err == nil && st.Mode().IsRegular() {
		return st.Size()
	}
	return 0
}

// readPatch runs VcsDiff for paths (nil: all) and indexes it by path. No VcsDiff hook: an empty patch, so the files are
// listed with what VcsStatus said.
func (r *hookRepo) readPatch(ctx context.Context, src vcsSource, paths []string) (map[string]*agentv1.FileDiff, error) {
	out, err := src.Diff(ctx, r.dir, paths)
	if errors.Is(err, vcshooks.ErrNoAnswer) {
		return map[string]*agentv1.FileDiff{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.root, err)
	}
	patch := map[string]*agentv1.FileDiff{}
	for _, f := range parseGitPatch(out) {
		patch[f.Path] = f
	}
	return patch, nil
}

// measureHookRepo lists r's files with their counts: the hook's, or, when it left some out, a VcsDiff of all of them.
func (b *diffBuilder) measureHookRepo(ctx context.Context, r *hookRepo) error {
	r.keep = nil
	need := false
	for _, f := range r.status.Files {
		if !f.Binary && (f.Additions == nil || f.Deletions == nil) {
			need = true
		}
	}
	if need && r.patch == nil {
		patch, err := r.readPatch(ctx, b.src.vcs, nil)
		if err != nil {
			return err
		}
		r.patch = patch
	}
	for _, f := range r.status.Files {
		if b.full() {
			break
		}
		d := &agentv1.FileDiff{Path: r.abs(f.Path), OldPath: r.abs(f.OldPath), Status: hookStatus(f.Status), Binary: f.Binary,
			Source: agentv1.FileDiffSource_FILE_DIFF_SOURCE_VCS}
		if f.Additions != nil && f.Deletions != nil {
			d.Additions, d.Deletions = *f.Additions, *f.Deletions
		} else if p := r.patch[f.Path]; p != nil {
			d.Additions, d.Deletions, d.Binary = p.Additions, p.Deletions, d.Binary || p.Binary
		}
		size := r.fileSize(f.Path)
		switch {
		case d.Binary:
		case size > diffFileLimit(f.Path) || size > b.budget:
			d.TooLarge = true
		default:
			b.budget -= size
			r.keep = append(r.keep, f.Path)
		}
		b.add(d)
	}
	return nil
}

// addHookHunks gives r's kept files their hunks, from the patch measuring read or from a VcsDiff of those files.
func (b *diffBuilder) addHookHunks(ctx context.Context, r *hookRepo, byPath map[string]*agentv1.FileDiff) error {
	if len(r.keep) == 0 {
		return nil
	}
	patch := r.patch
	if patch == nil {
		var err error
		if patch, err = r.readPatch(ctx, b.src.vcs, r.keep); err != nil {
			return err
		}
	}
	for _, rel := range r.keep {
		p := patch[rel]
		f := byPath[r.abs(rel)]
		if p == nil || f == nil {
			continue
		}
		f.Hunks = p.Hunks
		if p.Binary {
			f.Binary, f.Hunks = true, nil
		}
	}
	return nil
}

// hookFile is one listed file of r with its hunks; nil when the hook no longer lists it.
func (b *diffBuilder) hookFile(ctx context.Context, r *hookRepo, f vcshooks.File) (*agentv1.FileDiff, error) {
	d := &agentv1.FileDiff{Path: r.abs(f.Path), OldPath: r.abs(f.OldPath), Status: hookStatus(f.Status), Binary: f.Binary,
		Source: agentv1.FileDiffSource_FILE_DIFF_SOURCE_VCS}
	if f.Additions != nil && f.Deletions != nil {
		d.Additions, d.Deletions = *f.Additions, *f.Deletions
	}
	if d.Binary {
		return d, nil
	}
	if r.fileSize(f.Path) > diffFileLimit(f.Path) {
		d.TooLarge = true
		return d, nil
	}
	paths := []string{f.Path}
	if f.OldPath != "" {
		paths = append(paths, f.OldPath)
	}
	patch, err := r.readPatch(ctx, b.src.vcs, paths)
	if err != nil {
		return nil, err
	}
	if p := patch[f.Path]; p != nil {
		d.Additions, d.Deletions, d.Hunks, d.Binary = p.Additions, p.Deletions, p.Hunks, p.Binary
		if p.Binary {
			d.Hunks = nil
		}
	}
	return d, nil
}
