// Package workspace gathers the machine and project context injected into
// prompts: environment placeholders, git snapshot and project instructions
// (CLAUDE.md / AGENTS.md).
package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Env holds the values for env placeholders of the prompt bundle.
type Env struct {
	Cwd              string
	Platform         string
	Shell            string
	OSVersion        string
	Date             string
	IsGit            bool
	ModelID          string
	ModelDisplayName string
	GitStatus        string
}

// Vars returns placeholder values keyed by placeholder name.
func (e Env) Vars() map[string]string {
	return map[string]string{
		"cwd":                e.Cwd,
		"platform":           e.Platform,
		"shell":              e.Shell,
		"os_version":         e.OSVersion,
		"date":               e.Date,
		"is_git":             strconv.FormatBool(e.IsGit),
		"model":              e.ModelID,
		"model_id":           e.ModelID,
		"model_display_name": e.ModelDisplayName,
		"git_status":         e.GitStatus,
	}
}

// Detect collects environment information for cwd.
func Detect(ctx context.Context, cwd string) Env {
	e := Env{
		Cwd:      cwd,
		Platform: runtime.GOOS,
		Shell:    shellName(),
		Date:     time.Now().Format("2006-01-02"),
	}
	e.OSVersion = osVersion(ctx)
	if root := GitRoot(ctx, cwd); root != "" {
		e.IsGit = true
		e.GitStatus = GitSnapshot(ctx, cwd)
	}
	return e
}

func shellName() string {
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	if s := os.Getenv("SHELL"); s != "" {
		return filepath.Base(s)
	}
	return "sh"
}

func osVersion(ctx context.Context) string {
	if runtime.GOOS == "windows" {
		return "Windows"
	}
	out, err := run(ctx, "", "uname", "-sr")
	if err != nil {
		return runtime.GOOS
	}
	return strings.TrimSpace(out)
}

func run(ctx context.Context, dir string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// GitRoot returns the repository top-level for dir, or "".
func GitRoot(ctx context.Context, dir string) string {
	out, err := run(ctx, dir, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// GitBranch returns the current branch name, or "".
func GitBranch(ctx context.Context, dir string) string {
	out, err := run(ctx, dir, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func mainBranch(ctx context.Context, dir string) string {
	if out, err := run(ctx, dir, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(strings.TrimSpace(out), "origin/")
	}
	for _, b := range []string{"main", "master"} {
		if _, err := run(ctx, dir, "git", "rev-parse", "--verify", "--quiet", b); err == nil {
			return b
		}
	}
	return ""
}

// MaxGitStatusChars caps the status part of the snapshot.
const MaxGitStatusChars = 2000

// GitSnapshot returns a plain-data snapshot of the repository: branch, main
// branch, short status and recent commits. Empty when dir is not a repo.
func GitSnapshot(ctx context.Context, dir string) string {
	branch := GitBranch(ctx, dir)
	if branch == "" {
		return ""
	}
	status, _ := run(ctx, dir, "git", "--no-optional-locks", "status", "--short")
	status = strings.TrimRight(status, "\n")
	if len(status) > MaxGitStatusChars {
		status = status[:MaxGitStatusChars] + "\n… (truncated)"
	}
	if status == "" {
		status = "(clean)"
	}
	log, _ := run(ctx, dir, "git", "--no-optional-locks", "log", "--oneline", "-n", "5")
	var b strings.Builder
	b.WriteString("Current branch: " + branch + "\n")
	if mb := mainBranch(ctx, dir); mb != "" {
		b.WriteString("Main branch: " + mb + "\n")
	}
	b.WriteString("\nStatus:\n" + status + "\n")
	if l := strings.TrimRight(log, "\n"); l != "" {
		b.WriteString("\nRecent commits:\n" + l + "\n")
	}
	return b.String()
}

// InstructionFile is one loaded project instructions file.
type InstructionFile struct {
	Path    string
	Content string
}

// MaxInstructionBytes skips larger instruction files.
const MaxInstructionBytes = 4 << 20

var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// ProjectInstructions loads CLAUDE.md files (CLAUDE.md, .claude/CLAUDE.md,
// CLAUDE.local.md) from the filesystem root down to cwd. When none exist,
// AGENTS.md / .claude/AGENTS.md are loaded instead.
func ProjectInstructions(cwd string) []InstructionFile {
	dirs := ancestors(cwd)
	claude := collect(dirs, []string{"CLAUDE.md", filepath.Join(".claude", "CLAUDE.md"), "CLAUDE.local.md"})
	if len(claude) > 0 {
		return claude
	}
	return collect(dirs, []string{"AGENTS.md", filepath.Join(".claude", "AGENTS.md")})
}

// ancestors returns dirs from root to cwd.
func ancestors(cwd string) []string {
	var dirs []string
	d := filepath.Clean(cwd)
	for {
		dirs = append(dirs, d)
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

func collect(dirs, names []string) []InstructionFile {
	var out []InstructionFile
	for _, d := range dirs {
		for _, n := range names {
			p := filepath.Join(d, n)
			st, err := os.Stat(p)
			if err != nil || st.IsDir() || st.Size() > MaxInstructionBytes {
				continue
			}
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			text := strings.TrimSpace(htmlComment.ReplaceAllString(string(data), ""))
			if text == "" {
				continue
			}
			out = append(out, InstructionFile{Path: p, Content: text})
		}
	}
	return out
}

// FormatInstructions renders instruction files as plain data for the
// bundle's project_instructions reminder ({{content}}).
func FormatInstructions(files []InstructionFile, gitStatus string) string {
	var parts []string
	for _, f := range files {
		parts = append(parts, "Contents of "+f.Path+":\n\n"+f.Content)
	}
	if gitStatus != "" {
		parts = append(parts, "gitStatus:\n"+gitStatus)
	}
	return strings.Join(parts, "\n\n")
}
