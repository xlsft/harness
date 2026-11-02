package server

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/session"
)

const (
	diffStatsTimeout = 3 * time.Second
	// Untracked files are counted up to this many (their lines up to
	// maxUntrackedBytes each), so a huge untracked tree stays cheap.
	maxUntrackedFiles = 200
	maxUntrackedBytes = 1 << 20
)

// diffStats measures the uncommitted changes of the repository at cwd:
// tracked changes against HEAD plus untracked (not ignored) files. In a
// worktree the client created, committed changes since the base ref count
// too. nil outside a git repository.
func diffStats(ctx context.Context, cwd string, wt *session.Worktree) *agentv1.DiffStats {
	ctx, cancel := context.WithTimeout(ctx, diffStatsTimeout)
	defer cancel()
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		return cmd.Output()
	}
	top, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return nil
	}
	root := strings.TrimSpace(string(top))
	base := "HEAD"
	if _, err := git("rev-parse", "--verify", "-q", "HEAD"); err != nil {
		base = "" // no commit yet: everything is untracked or staged
	}
	if wt != nil && wt.BaseRef != "" && base != "" {
		if mb, err := git("merge-base", wt.BaseRef, "HEAD"); err == nil {
			base = strings.TrimSpace(string(mb))
		}
	}
	ds := &agentv1.DiffStats{}
	args := []string{"diff", "--numstat", "--no-renames"}
	if base == "" {
		args = append(args, "--cached")
	} else {
		args = append(args, base)
	}
	if out, err := git(args...); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(out))
		for sc.Scan() {
			f := strings.SplitN(sc.Text(), "\t", 3)
			if len(f) < 3 {
				continue
			}
			ds.FilesChanged++
			a, _ := strconv.Atoi(f[0]) // "-" for binary files
			d, _ := strconv.Atoi(f[1])
			ds.Additions += int32(a)
			ds.Deletions += int32(d)
		}
	}
	if out, err := git("ls-files", "--others", "--exclude-standard", "--full-name", "-z", ":/"); err == nil {
		n := 0
		for _, p := range strings.Split(string(out), "\x00") {
			if p == "" {
				continue
			}
			if n++; n > maxUntrackedFiles {
				break
			}
			ds.FilesChanged++
			ds.Additions += int32(countLines(filepath.Join(root, p)))
		}
	}
	return ds
}

// countLines counts the lines of a text file (0 for binary or large files).
func countLines(path string) int {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxUntrackedBytes {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.IndexByte(data, 0) >= 0 {
		return 0
	}
	n := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}
