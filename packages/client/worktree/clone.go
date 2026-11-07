package worktree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Copy-on-write clones of a checkout's folders (node_modules above all) into a new worktree: on APFS with clonefile(2)
// (`cp -c`), on btrfs and XFS with reflinks (`cp --reflink=always`). A clone takes no time and no space, and the two
// copies part as soon as either is written: Nuxt or vite writing into the worktree's node_modules never touches the
// original. Nothing is ever linked to the other checkout; where the file system can't clone, the caller installs.

// CloneSupported reports whether this platform has a copy-on-write copy to try. Whether the file system has one is
// only known by trying (CloneFolder).
func CloneSupported() bool { return runtime.GOOS == "darwin" || runtime.GOOS == "linux" }

// cloneArgs is the cp command line that clones from into to, or fails when the file system can't: never a plain copy,
// which would take minutes and the space of a second node_modules. -R keeps symbolic links as links.
func cloneArgs(from, to string) []string {
	if runtime.GOOS == "darwin" {
		return []string{"cp", "-c", "-R", "-p", from, to}
	}
	return []string{"cp", "-R", "-p", "--reflink=always", from, to}
}

// CloneFolder clones the folder rel of the checkout at src into the same place under dst, copy-on-write. It refuses a
// folder that is there already, clones into a temporary name first so that a failure leaves nothing half done, and
// then points symbolic links that lead into src at the same place in dst. It returns a line for the step's output.
func CloneFolder(ctx context.Context, src, dst, rel string) (string, error) {
	if !CloneSupported() {
		return "", errors.New("this platform has no copy-on-write copy")
	}
	from := filepath.Join(src, filepath.FromSlash(rel))
	to := filepath.Join(dst, filepath.FromSlash(rel))
	if st, err := os.Lstat(from); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s is not a folder", from)
	}
	if _, err := os.Lstat(to); err == nil {
		return "", fmt.Errorf("%s exists already", to)
	}
	if err := insideRoot(dst, filepath.Dir(to)); err != nil {
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return "", err
		}
		if err := insideRoot(dst, filepath.Dir(to)); err != nil {
			return "", err
		}
	}
	tmp := to + ".harness-clone"
	_ = os.RemoveAll(tmp)
	args := cloneArgs(from, tmp)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(tmp)
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("the file system can't clone: %s", msg)
	}
	relinked, err := relink(tmp, src, dst)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	if err := os.Rename(tmp, to); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	note := fmt.Sprintf("cloned %s copy-on-write from %s", rel, src)
	if relinked > 0 {
		note += fmt.Sprintf("; %d link(s) into that checkout now point into this worktree", relinked)
	}
	return note + "\n", nil
}

// relink rewrites the symbolic links under dir whose absolute target is inside src to the same path inside dst, so
// that nothing in the clone leads back into the other checkout. Relative links (pnpm's, npm workspaces') resolve in
// the worktree already.
func relink(dir, src, dst string) (int, error) {
	srcReal, _ := filepath.EvalSymlinks(src)
	n := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink == 0 {
			return err
		}
		target, err := os.Readlink(p)
		if err != nil || !filepath.IsAbs(target) {
			return nil
		}
		var rest string
		for _, base := range []string{src, srcReal} {
			if r, err := filepath.Rel(base, target); base != "" && err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
				rest = r
				break
			}
		}
		if rest == "" {
			return nil
		}
		if err := os.Remove(p); err != nil {
			return err
		}
		n++
		return os.Symlink(filepath.Join(dst, rest), p)
	})
	return n, err
}
