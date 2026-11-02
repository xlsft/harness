package permissions

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// pathPattern is a gitignore-style pattern anchored at a root directory.
type pathPattern struct {
	root     string
	pattern  string
	anchored bool
}

// compilePath resolves a path specifier:
//
//	//abs/path   absolute path
//	~/path       relative to the home directory
//	/path        relative to the rule source root (settings file location)
//	./path, path relative to the current working directory
//
// Patterns without a slash (other than a trailing one) match a path
// component at any depth below the root, as in .gitignore. A pattern that
// matches a directory also matches everything beneath it.
func compilePath(spec, sourceRoot, cwd, home string) pathPattern {
	spec = filepath.ToSlash(spec)
	switch {
	case strings.HasPrefix(spec, "//"):
		return pathPattern{root: "/", pattern: strings.TrimPrefix(spec, "//"), anchored: true}
	case spec == "~" || strings.HasPrefix(spec, "~/"):
		return pathPattern{root: home, pattern: strings.TrimPrefix(strings.TrimPrefix(spec, "~"), "/"), anchored: true}
	case strings.HasPrefix(spec, "/"):
		return pathPattern{root: sourceRoot, pattern: strings.TrimPrefix(spec, "/"), anchored: true}
	case strings.HasPrefix(spec, "./"):
		return pathPattern{root: cwd, pattern: strings.TrimPrefix(spec, "./"), anchored: true}
	}
	trimmed := strings.TrimSuffix(spec, "/")
	return pathPattern{root: cwd, pattern: spec, anchored: strings.Contains(trimmed, "/")}
}

func (p pathPattern) match(abs string) bool {
	pat := strings.TrimSuffix(p.pattern, "/")
	if p.root == "" {
		return false
	}
	rel, err := filepath.Rel(p.root, abs)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return false
	}
	if pat == "" || pat == "**" {
		return true
	}
	if rel == "." {
		rel = ""
	}
	comps := strings.Split(rel, "/")
	if !p.anchored {
		for _, c := range comps {
			if ok, _ := doublestar.Match(pat, c); ok {
				return true
			}
		}
		return false
	}
	// The pattern matches the path itself or one of its parent directories.
	for i := len(comps); i > 0; i-- {
		if ok, _ := doublestar.Match(pat, strings.Join(comps[:i], "/")); ok {
			return true
		}
	}
	return false
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func inAny(path string, dirs []string) bool {
	path = evalPath(path)
	for _, d := range dirs {
		if within(path, evalPath(d)) {
			return true
		}
	}
	return false
}

// evalPath resolves symlinks of the longest existing prefix of p so paths of
// files that do not exist yet compare correctly (e.g. /tmp vs /private/tmp).
func evalPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	dir, base := filepath.Split(p)
	dir = filepath.Clean(dir)
	if dir == p || dir == "." || dir == "" {
		return p
	}
	return filepath.Join(evalPath(dir), base)
}

// protectedNames are never auto-approved for writing (except in
// bypassPermissions mode).
var (
	protectedDirs  = map[string]bool{".git": true, ".claude": true, ".harness": true}
	protectedFiles = map[string]bool{
		".mcp.json": true, ".bashrc": true, ".bash_profile": true, ".bash_login": true, ".bash_logout": true,
		".profile": true, ".zshrc": true, ".zprofile": true, ".zshenv": true, ".zlogin": true, ".zlogout": true,
		".gitconfig": true, ".gitmodules": true,
	}
)

// isProtected reports whether writing path needs explicit approval. Path
// components are checked relative to the working directory that contains
// the path (or the home directory), so a project that itself lives under a
// ".claude" directory is not entirely protected.
func isProtected(path string, workDirs []string, home string) bool {
	path = evalPath(path)
	base := ""
	for _, d := range workDirs {
		d = evalPath(d)
		if within(path, d) && len(d) > len(base) {
			base = d
		}
	}
	if base == "" && home != "" && within(path, evalPath(home)) {
		base = evalPath(home)
	}
	rel := path
	if base != "" {
		if r, err := filepath.Rel(base, path); err == nil {
			rel = r
		}
	}
	comps := strings.Split(filepath.ToSlash(rel), "/")
	for i, c := range comps {
		if i < len(comps)-1 && protectedDirs[c] {
			return true
		}
	}
	last := comps[len(comps)-1]
	return protectedFiles[last] || protectedDirs[last]
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
