// Package workspace gathers the machine and project context injected into
// prompts: environment placeholders, git snapshot and project instructions
// (CLAUDE.md / AGENTS.md, see package memory) and the project_key.
package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/xlsft/harness/packages/client/memory"
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
const MaxInstructionBytes = memory.MaxFileBytes

// ProjectInstructions loads the project instruction files for cwd: CLAUDE.md,
// .claude/CLAUDE.md and CLAUDE.local.md from the filesystem root down to cwd
// (with @imports inside cwd and rules without paths: frontmatter), or
// AGENTS.md / .claude/AGENTS.md when none exist. It is the project-only view
// of memory.Loader; sessions should use memory.New directly to also get
// managed files, user memory, auto memory and lazily loaded files.
func ProjectInstructions(cwd string) []InstructionFile {
	l := memory.New(memory.Options{Cwd: cwd, ProjectOnly: true})
	var out []InstructionFile
	for _, in := range l.Initial() {
		out = append(out, InstructionFile{Path: in.Path, Content: in.Content})
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
