package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

// GlobMaxResults caps Glob output.
const GlobMaxResults = 100

// Glob finds files by glob pattern, newest first.
type Glob struct{}

type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

func (*Glob) Name() string                  { return "Glob" }
func (*Glob) ReadOnly(json.RawMessage) bool { return true }

var vcsDirs = map[string]bool{".git": true, ".hg": true, ".svn": true, ".jj": true}

type fileHit struct {
	path  string
	mtime time.Time
}

func (*Glob) Run(ctx context.Context, env *Env, call Call) Result {
	start := time.Now()
	var in globInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.Pattern == "" {
		return ErrorResult("InputValidationError: pattern is required")
	}
	base := resolvePath(env.Shell.Cwd(), in.Path)
	if in.Path == "" {
		base = env.Cwd
	}
	pattern := in.Pattern
	if filepath.IsAbs(pattern) {
		// Split an absolute pattern into base + relative pattern.
		b, rel := doublestar.SplitPattern(filepath.ToSlash(pattern))
		base, pattern = filepath.FromSlash(b), rel
	}
	st, err := os.Stat(base)
	if err != nil || !st.IsDir() {
		return ErrorResult("Directory does not exist: %s", base)
	}
	if !doublestar.ValidatePattern(pattern) {
		return ErrorResult("Invalid glob pattern: %s", in.Pattern)
	}
	wantVCS := false
	for d := range vcsDirs {
		if strings.Contains(pattern, d) {
			wantVCS = true
		}
	}
	var hits []fileHit
	fsys := os.DirFS(base)
	err = doublestar.GlobWalk(fsys, pattern, func(p string, d fs.DirEntry) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		hits = append(hits, fileHit{path: filepath.Join(base, filepath.FromSlash(p)), mtime: info.ModTime()})
		return nil
	}, doublestar.WithNoFollow(), doublestar.WithFailOnIOErrors(), doublestar.WithFilesOnly())
	if err != nil && ctx.Err() == nil {
		// IO errors on unreadable subdirectories are not fatal; retry leniently.
		hits = nil
		_ = doublestar.GlobWalk(fsys, pattern, func(p string, d fs.DirEntry) error {
			if info, err := d.Info(); err == nil && !d.IsDir() {
				hits = append(hits, fileHit{path: filepath.Join(base, filepath.FromSlash(p)), mtime: info.ModTime()})
			}
			return nil
		}, doublestar.WithNoFollow(), doublestar.WithFilesOnly())
	}
	if ctx.Err() != nil {
		return ErrorResult("interrupted")
	}
	if !wantVCS {
		filtered := hits[:0]
		for _, h := range hits {
			if !inVCSDir(base, h.path) {
				filtered = append(filtered, h)
			}
		}
		hits = filtered
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if !hits[i].mtime.Equal(hits[j].mtime) {
			return hits[i].mtime.After(hits[j].mtime)
		}
		return hits[i].path < hits[j].path
	})
	truncated := len(hits) > GlobMaxResults
	if truncated {
		hits = hits[:GlobMaxResults]
	}
	names := make([]string, len(hits))
	for i, h := range hits {
		names[i] = h.path
	}
	structured := map[string]any{"filenames": names, "numFiles": len(names), "truncated": truncated, "durationMs": time.Since(start).Milliseconds()}
	if len(names) == 0 {
		return TextResult("No files found", structured)
	}
	out := strings.Join(names, "\n")
	if truncated {
		out += fmt.Sprintf("\n(Results are truncated to the first %d files. Consider using a more specific path or pattern.)", GlobMaxResults)
	}
	return TextResult(out, structured)
}

func inVCSDir(base, p string) bool {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if vcsDirs[part] {
			return true
		}
	}
	return false
}
