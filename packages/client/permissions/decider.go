package permissions

import (
	"context"
	"path/filepath"
	"strings"
)

// AutoRequest is a tool call handed to the auto-mode Decider: the call that
// would otherwise prompt plus the engine's view of the workspace.
type AutoRequest struct {
	Request
	// Cwd is the effective working directory (the shell cwd for Bash).
	Cwd string
	// WorkDirs are the session cwd followed by AdditionalDirs.
	WorkDirs       []string
	AdditionalDirs []string
	Home           string
}

// AutoResult is the Decider's verdict.
type AutoResult struct {
	// Behavior is Allow, Deny or Ask.
	Behavior Behavior
	// Reason is one sentence for the user and, on Deny, for the model.
	Reason string
	// Categories are risk categories in the lower-case form of
	// harness.decision.v1.RiskCategory without its prefix ("destructive",
	// "remote_code_execution", ...).
	Categories []string
	// Provider decided: "rules", "llm", "http:<id>", "local-rules", "cache"
	// or "none" when nobody decided.
	Provider   string
	Confidence float64
	// Cached is set when the verdict came from a cache (server or session).
	Cached    bool
	LatencyMs int64
	CostUSD   float64
}

// Decider decides tool calls that would otherwise prompt in auto mode.
// It must be safe for concurrent use.
type Decider interface {
	Decide(ctx context.Context, req AutoRequest) (AutoResult, error)
}

// Resetter is implemented by Deciders with a session cache; the engine
// calls Reset when the permission mode changes.
type Resetter interface {
	Reset()
}

// IsReadOnlyCommand reports whether a simple command (normalize it first)
// is in the built-in read-only allowlist and writes nowhere.
func IsReadOnlyCommand(c SimpleCommand) bool { return isReadOnlyCommand(c) }

// WithinAny reports whether path is one of dirs or inside one of them
// (symlinks of existing prefixes resolved).
func WithinAny(path string, dirs []string) bool { return inAny(path, dirs) }

// IsProtectedPath reports whether writing path always needs the user's
// approval (.git, .claude, shell rc files, ...).
func IsProtectedPath(path string, workDirs []string, home string) bool {
	return isProtected(path, workDirs, home)
}

// Subcommands returns the normalized simple commands of a command line
// (wrappers stripped) including those inside substitutions, as rule text.
func Subcommands(cmd string) []string {
	var out []string
	var walk func(cmd string, depth int)
	walk = func(cmd string, depth int) {
		for _, c := range SplitCommand(cmd) {
			if t := c.Normalize().Text(); t != "" {
				out = append(out, t)
			}
			if depth < 3 {
				for _, s := range c.Substitutions {
					walk(s, depth+1)
				}
			}
		}
	}
	walk(cmd, 0)
	return out
}

// CommandWritePaths returns the absolute paths a command line writes to, as
// far as they can be told statically: output redirections, tee, cp/mv/ln/
// install targets, touch/mkdir/rm/rmdir/truncate/chmod/chown operands,
// sed -i files and dd of=. Arguments with expansions are skipped.
func CommandWritePaths(cmd, cwd, home string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || strings.ContainsAny(p, "$`") || p == "/dev/null" || p == "-" {
			return
		}
		p = resolvePath(p, cwd, home)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	var walk func(cmd string, depth int)
	walk = func(cmd string, depth int) {
		for _, raw := range SplitCommand(cmd) {
			c := raw.Normalize()
			for _, r := range c.Redirects {
				switch r.Op {
				case ">", ">>", "&>", "&>>", ">|":
					add(r.Target)
				}
			}
			for _, p := range commandWriteOperands(c) {
				add(p)
			}
			if depth < 3 {
				for _, s := range c.Substitutions {
					walk(s, depth+1)
				}
			}
		}
	}
	walk(cmd, 0)
	return out
}

func resolvePath(p, cwd, home string) string {
	switch {
	case p == "~":
		p = home
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(home, p[2:])
	case !filepath.IsAbs(p):
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

// commandWriteOperands returns the operands a simple command writes to.
func commandWriteOperands(c SimpleCommand) []string {
	name := filepath.Base(c.Name())
	args := c.Args()
	var ops []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			ops = append(ops, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			// Options that take a separate value.
			switch {
			case name == "sed" && (a == "-e" || a == "-f"):
				ops = append(ops, "\x00script")
				i++
			case (name == "chmod" || name == "chown") && a == "--reference":
				i++
			case (name == "cp" || name == "mv" || name == "install" || name == "ln") && (a == "-t" || a == "--target-directory"):
				if i+1 < len(args) {
					return []string{args[i+1]}
				}
			case name == "install" && (a == "-m" || a == "-o" || a == "-g"):
				i++
			}
			continue
		}
		ops = append(ops, a)
	}
	switch name {
	case "tee", "touch", "mkdir", "rm", "rmdir", "truncate", "shred", "unlink":
		return dropScript(ops)
	case "cp", "mv", "install", "ln", "rsync", "scp":
		if len(ops) > 1 {
			return ops[len(ops)-1:]
		}
	case "chmod", "chown", "chgrp":
		if len(ops) > 1 {
			return ops[1:]
		}
	case "sed":
		inPlace := false
		for _, a := range args {
			if a == "-i" || strings.HasPrefix(a, "-i") || a == "--in-place" || strings.HasPrefix(a, "--in-place=") {
				inPlace = true
			}
		}
		if !inPlace {
			return nil
		}
		if len(ops) > 0 && ops[0] == "\x00script" {
			return dropScript(ops)
		}
		if len(ops) > 1 {
			return ops[1:] // first operand is the script
		}
	case "dd":
		for _, a := range args {
			if strings.HasPrefix(a, "of=") {
				return []string{strings.TrimPrefix(a, "of=")}
			}
		}
	}
	return nil
}

func dropScript(ops []string) []string {
	var out []string
	for _, o := range ops {
		if o != "\x00script" {
			out = append(out, o)
		}
	}
	return out
}
