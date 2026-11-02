package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	gitignore "github.com/sabhiram/go-gitignore"
)

// GrepDefaultHeadLimit is applied when head_limit is not given.
const GrepDefaultHeadLimit = 250

// Grep searches file contents with ripgrep semantics. It shells out to rg
// when available and otherwise uses a Go implementation honoring .gitignore.
type Grep struct{}

type grepInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	Type       string `json:"type"`
	OutputMode string `json:"output_mode"`
	I          bool   `json:"-i"`
	N          *bool  `json:"-n"`
	A          *int   `json:"-A"`
	B          *int   `json:"-B"`
	C          *int   `json:"-C"`
	Context    *int   `json:"context"`
	Multiline  bool   `json:"multiline"`
	HeadLimit  *int   `json:"head_limit"`
	Offset     int    `json:"offset"`
}

func (*Grep) Name() string                  { return "Grep" }
func (*Grep) ReadOnly(json.RawMessage) bool { return true }

func (in *grepInput) contextBA() (before, after int) {
	if in.Context != nil {
		before, after = *in.Context, *in.Context
	}
	if in.C != nil {
		before, after = *in.C, *in.C
	}
	if in.B != nil {
		before = *in.B
	}
	if in.A != nil {
		after = *in.A
	}
	return
}

func (in *grepInput) lineNumbers() bool { return in.N == nil || *in.N }

func ripgrepPath(env *Env) string {
	if env.RipgrepPath == "-" {
		return ""
	}
	if env.RipgrepPath != "" {
		return env.RipgrepPath
	}
	if p := os.Getenv("HARNESS_RG"); p != "" {
		if p == "-" {
			return ""
		}
		return p
	}
	p, err := exec.LookPath("rg")
	if err != nil {
		return ""
	}
	return p
}

func (*Grep) Run(ctx context.Context, env *Env, call Call) Result {
	var in grepInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.Pattern == "" {
		return ErrorResult("InputValidationError: pattern is required")
	}
	switch in.OutputMode {
	case "":
		in.OutputMode = "files_with_matches"
	case "files_with_matches", "content", "count":
	default:
		return ErrorResult("InputValidationError: output_mode must be files_with_matches, content or count")
	}
	target := env.Cwd
	if in.Path != "" {
		target = resolvePath(env.Shell.Cwd(), in.Path)
	}
	if _, err := os.Stat(target); err != nil {
		return ErrorResult("Path does not exist: %s", target)
	}

	var lines []string
	var err error
	if rg := ripgrepPath(env); rg != "" {
		lines, err = runRipgrep(ctx, rg, &in, target)
	} else {
		lines, err = goGrep(ctx, &in, target)
	}
	if ctx.Err() != nil {
		return ErrorResult("interrupted")
	}
	if err != nil {
		return ErrorResult("%v", err)
	}

	if in.OutputMode == "files_with_matches" {
		sortByMtime(lines)
	}
	total := len(lines)
	limit := GrepDefaultHeadLimit
	if in.HeadLimit != nil {
		limit = *in.HeadLimit // 0 = unlimited
	}
	offset := max(in.Offset, 0)
	if offset > len(lines) {
		offset = len(lines)
	}
	lines = lines[offset:]
	truncated := false
	if limit > 0 && len(lines) > limit {
		lines = lines[:limit]
		truncated = true
	}
	page := ""
	if truncated || offset > 0 {
		page = fmt.Sprintf("\n\n[Showing results with pagination = limit: %d, offset: %d]", limit, offset)
	}
	structured := map[string]any{"mode": in.OutputMode, "numLines": len(lines), "total": total, "truncated": truncated}

	switch in.OutputMode {
	case "files_with_matches":
		if total == 0 {
			return TextResult("No files found", structured)
		}
		structured["filenames"] = lines
		return TextResult(fmt.Sprintf("Found %d file%s\n%s%s", total, plural(total), strings.Join(lines, "\n"), page), structured)
	case "count":
		if total == 0 {
			return TextResult("No matches found", structured)
		}
		sum := 0
		for _, l := range lines {
			if i := strings.LastIndex(l, ":"); i >= 0 {
				n, _ := strconv.Atoi(l[i+1:])
				sum += n
			}
		}
		return TextResult(fmt.Sprintf("%s\n\nFound %d total occurrence%s across %d file%s.%s", strings.Join(lines, "\n"), sum, plural(sum), len(lines), plural(len(lines)), page), structured)
	default:
		if total == 0 {
			return TextResult("No matches found", structured)
		}
		return TextResult(strings.Join(lines, "\n")+page, structured)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func sortByMtime(paths []string) {
	mt := make(map[string]time.Time, len(paths))
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			mt[p] = st.ModTime()
		}
	}
	sort.SliceStable(paths, func(i, j int) bool {
		a, b := mt[paths[i]], mt[paths[j]]
		if !a.Equal(b) {
			return a.After(b)
		}
		return paths[i] < paths[j]
	})
}

func runRipgrep(ctx context.Context, rg string, in *grepInput, target string) ([]string, error) {
	args := []string{"--hidden", "--no-config", "--max-columns", "500", "--color", "never"}
	for d := range vcsDirs {
		args = append(args, "--glob", "!"+d)
	}
	switch in.OutputMode {
	case "files_with_matches":
		args = append(args, "--files-with-matches")
	case "count":
		args = append(args, "--count", "--with-filename")
	case "content":
		args = append(args, "--no-heading", "--with-filename")
		if in.lineNumbers() {
			args = append(args, "--line-number")
		}
		b, a := in.contextBA()
		if b > 0 {
			args = append(args, "-B", strconv.Itoa(b))
		}
		if a > 0 {
			args = append(args, "-A", strconv.Itoa(a))
		}
	}
	if in.I {
		args = append(args, "--ignore-case")
	}
	if in.Multiline {
		args = append(args, "--multiline", "--multiline-dotall")
	}
	if in.Glob != "" {
		for _, g := range splitGlobs(in.Glob) {
			args = append(args, "--glob", g)
		}
	}
	if in.Type != "" {
		args = append(args, "--type", in.Type)
	}
	args = append(args, "-e", in.Pattern, "--", target)
	cmd := exec.CommandContext(ctx, rg, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 1 {
			return nil, nil // no matches
		}
		if stdout.Len() == 0 {
			return nil, fmt.Errorf("ripgrep: %s", strings.TrimSpace(stderr.String()))
		}
	} else if err != nil {
		return nil, err
	}
	return splitLines(stdout.String()), nil
}

// splitGlobs splits a glob parameter on whitespace, keeping brace groups.
func splitGlobs(s string) []string {
	if strings.Contains(s, "{") {
		return []string{s}
	}
	return strings.Fields(s)
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// fileTypes is a small subset of ripgrep's --type definitions for the Go fallback.
var fileTypes = map[string][]string{
	"go": {"*.go"}, "js": {"*.js", "*.mjs", "*.cjs", "*.jsx"}, "ts": {"*.ts", "*.tsx", "*.mts", "*.cts"},
	"py": {"*.py", "*.pyi"}, "rust": {"*.rs"}, "java": {"*.java"}, "c": {"*.c", "*.h"},
	"cpp": {"*.cpp", "*.cc", "*.cxx", "*.hpp", "*.hh", "*.h"}, "md": {"*.md", "*.markdown"},
	"markdown": {"*.md", "*.markdown"}, "json": {"*.json"}, "yaml": {"*.yaml", "*.yml"},
	"toml": {"*.toml"}, "sh": {"*.sh", "*.bash", "*.zsh"}, "html": {"*.html", "*.htm"},
	"css": {"*.css", "*.scss", "*.sass", "*.less"}, "rb": {"*.rb"}, "ruby": {"*.rb"},
	"php": {"*.php"}, "swift": {"*.swift"}, "kotlin": {"*.kt", "*.kts"}, "vue": {"*.vue"},
	"sql": {"*.sql"}, "proto": {"*.proto"}, "txt": {"*.txt"},
}

type ignoreLayer struct {
	dir string // absolute
	m   *gitignore.GitIgnore
}

func ignored(layers []ignoreLayer, abs string, isDir bool) bool {
	for _, l := range layers {
		rel, err := filepath.Rel(l.dir, abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		rel = filepath.ToSlash(rel)
		if isDir {
			rel += "/"
		}
		if l.m.MatchesPath(rel) {
			return true
		}
	}
	return false
}

func goGrep(ctx context.Context, in *grepInput, target string) ([]string, error) {
	expr := in.Pattern
	flags := ""
	if in.I {
		flags += "i"
	}
	if in.Multiline {
		flags += "s"
	} else {
		flags += "m"
	}
	re, err := regexp.Compile("(?" + flags + ")" + expr)
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %v", err)
	}
	var globs []string
	if in.Glob != "" {
		globs = splitGlobs(in.Glob)
	}
	var types []string
	if in.Type != "" {
		t, ok := fileTypes[in.Type]
		if !ok {
			return nil, fmt.Errorf("unrecognized file type: %s", in.Type)
		}
		types = t
	}
	var out []string
	visit := func(path string) {
		if !matchFilters(path, target, globs, types) {
			return
		}
		out = append(out, grepFile(in, re, path)...)
	}
	st, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		globs, types = nil, nil
		visit(target)
		return out, nil
	}
	layers := map[string][]ignoreLayer{}
	err = filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		parent := filepath.Dir(path)
		inherited := layers[parent]
		if d.IsDir() {
			if path != target && (vcsDirs[d.Name()] || ignored(inherited, path, true)) {
				return fs.SkipDir
			}
			own := inherited
			if gi, err := gitignore.CompileIgnoreFile(filepath.Join(path, ".gitignore")); err == nil {
				own = append(append([]ignoreLayer(nil), inherited...), ignoreLayer{dir: path, m: gi})
			}
			layers[path] = own
			return nil
		}
		if !d.Type().IsRegular() || ignored(inherited, path, false) {
			return nil
		}
		visit(path)
		return nil
	})
	if err != nil && ctx.Err() == nil {
		return nil, err
	}
	return out, nil
}

func matchFilters(path, root string, globs, types []string) bool {
	base := filepath.Base(path)
	rel, _ := filepath.Rel(root, path)
	rel = filepath.ToSlash(rel)
	if len(types) > 0 {
		ok := false
		for _, t := range types {
			if m, _ := doublestar.Match(t, base); m {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(globs) > 0 {
		include, matched := false, false
		for _, g := range globs {
			neg := strings.HasPrefix(g, "!")
			g = strings.TrimPrefix(g, "!")
			var m bool
			if strings.Contains(g, "/") {
				m, _ = doublestar.Match(strings.TrimPrefix(g, "/"), rel)
			} else {
				m, _ = doublestar.Match(g, base)
			}
			if neg {
				if m {
					return false
				}
				continue
			}
			include = true
			if m {
				matched = true
			}
		}
		if include && !matched {
			return false
		}
	}
	return true
}

func grepFile(in *grepInput, re *regexp.Regexp, path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return nil
	}
	content := string(data)
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	matched := make([]bool, len(lines))
	count := 0
	if in.Multiline {
		lineStarts := []int{0}
		for i, c := range content {
			if c == '\n' {
				lineStarts = append(lineStarts, i+1)
			}
		}
		lineOf := func(off int) int { return sort.SearchInts(lineStarts, off+1) - 1 }
		for _, m := range re.FindAllStringIndex(content, -1) {
			count++
			endOff := m[1]
			if endOff > m[0] {
				endOff--
			}
			for l := lineOf(m[0]); l <= lineOf(endOff) && l < len(lines); l++ {
				matched[l] = true
			}
		}
	} else {
		for i, l := range lines {
			if re.MatchString(l) {
				matched[i] = true
				count++
			}
		}
	}
	if count == 0 {
		return nil
	}
	switch in.OutputMode {
	case "files_with_matches":
		return []string{path}
	case "count":
		return []string{fmt.Sprintf("%s:%d", path, count)}
	}
	before, after := in.contextBA()
	show := make([]bool, len(lines))
	for i, m := range matched {
		if !m {
			continue
		}
		for j := max(0, i-before); j <= min(len(lines)-1, i+after); j++ {
			show[j] = true
		}
	}
	var out []string
	last := -2
	for i := range lines {
		if !show[i] {
			continue
		}
		if last >= 0 && i > last+1 && (before > 0 || after > 0) {
			out = append(out, "--")
		}
		sep := "-"
		if matched[i] {
			sep = ":"
		}
		text := lines[i]
		if len(text) > 500 {
			text = truncateUTF8(text, 500) + " [... omitted end of long line]"
		}
		if in.lineNumbers() {
			out = append(out, fmt.Sprintf("%s%s%d%s%s", path, sep, i+1, sep, text))
		} else {
			out = append(out, path+sep+text)
		}
		last = i
	}
	return out
}
