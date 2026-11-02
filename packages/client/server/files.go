package server

import (
	"bytes"
	"context"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	gitignore "github.com/sabhiram/go-gitignore"
)

// File listing limits for SuggestFiles.
const (
	fileListTTL  = 10 * time.Second
	maxListFiles = 50000
)

// fileCache lists project files for @-mention suggestions: git ls-files
// (tracked + untracked, honoring .gitignore) or a directory walk honoring
// .gitignore files outside git repositories.
type fileCache struct {
	mu    sync.Mutex
	at    time.Time
	files []string
}

func (c *fileCache) list(ctx context.Context, cwd string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.files != nil && time.Since(c.at) < fileListTTL {
		return c.files
	}
	files := gitFiles(ctx, cwd)
	if files == nil {
		files = walkFiles(cwd)
	}
	c.files, c.at = files, time.Now()
	return files
}

func gitFiles(ctx context.Context, cwd string) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	files := []string{}
	for _, f := range bytes.Split(out, []byte{0}) {
		s := filepath.ToSlash(string(f))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		files = append(files, s)
		if len(files) >= maxListFiles {
			break
		}
	}
	return files
}

func walkFiles(root string) []string {
	type ign struct {
		dir string
		m   *gitignore.GitIgnore
	}
	var ignores []ign
	files := []string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (d.Name() == ".git" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			if m, err := gitignore.CompileIgnoreFile(filepath.Join(p, ".gitignore")); err == nil {
				ignores = append(ignores, ign{dir: p, m: m})
			}
		}
		for _, ig := range ignores {
			if r, err := filepath.Rel(ig.dir, p); err == nil && !strings.HasPrefix(r, "..") && r != "." {
				if ig.m.MatchesPath(filepath.ToSlash(r)) {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
		}
		if !d.IsDir() {
			files = append(files, rel)
			if len(files) >= maxListFiles {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return files
}

// fuzzyScore scores path against query (case-insensitive subsequence
// match). Higher is better; ok is false when query is not a subsequence.
func fuzzyScore(query, path string) (int, bool) {
	q := []rune(strings.ToLower(query))
	p := []rune(path)
	lp := []rune(strings.ToLower(path))
	base := strings.LastIndex(path, "/") + 1
	score, qi, prev := 0, 0, -2
	for i := 0; i < len(lp) && qi < len(q); i++ {
		if lp[i] != q[qi] {
			continue
		}
		s := 1
		if i == prev+1 {
			s += 5 // contiguous
		}
		if i == 0 || strings.ContainsRune("/_-. ", p[i-1]) || (unicode.IsUpper(p[i]) && i > 0 && unicode.IsLower(p[i-1])) {
			s += 4 // word start
		}
		if i >= base {
			s += 2 // in the file name
		}
		score += s
		prev = i
		qi++
	}
	if qi < len(q) {
		return 0, false
	}
	if strings.Contains(strings.ToLower(path[base:]), strings.ToLower(query)) {
		score += 10
	}
	return score*10 - len(path), true
}

// FuzzyFilter returns up to limit paths matching query, best first. An
// empty query returns the shortest paths.
func FuzzyFilter(paths []string, query string, limit int) []string {
	type hit struct {
		p string
		s int
	}
	query = strings.TrimPrefix(strings.TrimSpace(query), "@")
	var hits []hit
	for _, p := range paths {
		if query == "" {
			hits = append(hits, hit{p, -len(p)})
			continue
		}
		if s, ok := fuzzyScore(query, p); ok {
			hits = append(hits, hit{p, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].s != hits[j].s {
			return hits[i].s > hits[j].s
		}
		return hits[i].p < hits[j].p
	})
	out := []string{}
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].p)
	}
	return out
}
