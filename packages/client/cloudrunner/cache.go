package cloudrunner

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// Caches (decision 15): one directory per cache key under the runner's cache root, each with pnpm, go and uv, mounted into
// that key's containers. A key's directory is kept under a cap by removing the least recently used files, and only while
// no container of that key runs, so that nothing is pulled out from under an install.

// cacheKinds are the caches and where they are in a container. The environment points the tools at them.
var cacheKinds = []struct{ dir, target string }{
	{"pnpm", "/cache/pnpm"},
	{"go", "/cache/go"},
	{"uv", "/cache/uv"},
}

// cacheEnv points the toolchains at the mounted caches.
var cacheEnv = []string{
	"npm_config_store_dir=/cache/pnpm",
	"PNPM_STORE_DIR=/cache/pnpm",
	"GOMODCACHE=/cache/go/mod",
	"GOCACHE=/cache/go/build",
	// Module files writable by their owner, so that the runner can prune them.
	"GOFLAGS=-modcacherw",
	"UV_CACHE_DIR=/cache/uv",
}

var cacheKeyRe = regexp.MustCompile(`^(runner|u[0-9a-f]{16})$`)

// validCacheKey refuses a key that could name a path outside the root.
func validCacheKey(k string) bool { return cacheKeyRe.MatchString(k) }

// ensureCache makes a key's directories, owned by the containers' user, and returns the mounts.
func ensureCache(root, key string, uid, gid int) ([]Mount, error) {
	var out []Mount
	for _, k := range cacheKinds {
		dir := filepath.Join(root, key, k.dir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if os.Geteuid() == 0 {
			_ = os.Chown(filepath.Join(root, key), uid, gid)
			_ = os.Chown(dir, uid, gid)
		}
		out = append(out, Mount{Source: dir, Target: k.target})
	}
	return out, nil
}

type cacheFile struct {
	path string
	size int64
	used time.Time
}

// pruneCache removes the least recently used files of dir until it holds at most max bytes, and then the directories left
// empty. It returns the bytes it holds after.
func pruneCache(dir string, max int64) (int64, error) {
	var files []cacheFile
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // vanished or unreadable: skip
		}
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, cacheFile{p, info.Size(), usedAt(info)})
		total += info.Size()
		return nil
	})
	if err != nil || total <= max {
		return total, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].used.Before(files[j].used) })
	// Down to 90 %, so that the next run doesn't prune again at once.
	target := max - max/10
	for _, f := range files {
		if total <= target {
			break
		}
		_ = os.Chmod(filepath.Dir(f.path), 0o700) // the Go module cache makes its directories read-only
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
	removeEmptyDirs(dir)
	return total, nil
}

// removeEmptyDirs removes the directories under root that hold nothing, deepest first, and keeps root.
func removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != root {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // fails when not empty
	}
}
