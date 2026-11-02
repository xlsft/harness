package permissions

import (
	"path/filepath"
	"strings"
)

// readOnlyCommands run without asking in every mode. The value, when set,
// rejects specific argument forms that make the command write or execute.
var readOnlyCommands = map[string]func(args []string) bool{
	"ls": nil, "cat": nil, "echo": nil, "printf": nil, "pwd": nil, "head": nil, "tail": nil,
	"grep": nil, "egrep": nil, "fgrep": nil, "rg": rgSafe, "wc": nil, "which": nil, "whereis": nil,
	"diff": nil, "stat": nil, "du": nil, "df": nil, "cd": nil, "file": nil, "tree": nil,
	"basename": nil, "dirname": nil, "realpath": nil, "readlink": nil, "whoami": nil, "id": nil,
	"uname": nil, "hostname": noArgs, "printenv": nil, "lsof": nil, "pgrep": nil, "ps": nil, "ss": nil,
	"netstat": nil, "nl": nil, "cut": nil, "tr": nil, "true": nil, "false": nil, "test": nil, "[": nil,
	"type": nil, "groups": nil, "uptime": nil, "cmp": nil, "comm": nil, "md5sum": nil, "sha1sum": nil,
	"sha256sum": nil, "shasum": nil, "md5": nil, "column": nil, "jq": nil, "less": nil, "more": nil,
	"date": dateSafe, "find": findSafe, "fd": fdSafe, "sort": sortSafe, "uniq": uniqSafe, "git": gitSafe,
	"command": commandLookup,
}

func noArgs(args []string) bool { return len(args) == 0 }

func dateSafe(args []string) bool {
	for _, a := range args {
		if a == "-s" || strings.HasPrefix(a, "--set") {
			return false
		}
	}
	return true
}

func findSafe(args []string) bool {
	for _, a := range args {
		switch a {
		case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls":
			return false
		}
	}
	return true
}

func fdSafe(args []string) bool {
	for _, a := range args {
		if a == "-x" || a == "-X" || a == "--exec" || a == "--exec-batch" {
			return false
		}
	}
	return true
}

func rgSafe(args []string) bool {
	for _, a := range args {
		if a == "--pre" || strings.HasPrefix(a, "--pre=") {
			return false
		}
	}
	return true
}

func sortSafe(args []string) bool {
	for _, a := range args {
		if a == "-o" || strings.HasPrefix(a, "--output") || (strings.HasPrefix(a, "-o") && !strings.HasPrefix(a, "--")) {
			return false
		}
	}
	return true
}

func uniqSafe(args []string) bool {
	n := 0
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			n++
		}
	}
	return n <= 1 // uniq IN OUT writes OUT
}

func commandLookup(args []string) bool {
	return len(args) > 0 && (args[0] == "-v" || args[0] == "-V")
}

// gitReadOnly lists git subcommands with no side effects; the value checks
// the remaining arguments.
var gitReadOnly = map[string]func(args []string) bool{
	"status": nil, "diff": nil, "log": nil, "show": nil, "blame": nil, "rev-parse": nil, "ls-files": nil,
	"ls-tree": nil, "cat-file": nil, "describe": nil, "shortlog": nil, "grep": nil, "merge-base": nil,
	"rev-list": nil, "show-ref": nil, "name-rev": nil, "whatchanged": nil, "count-objects": nil,
	"version": nil, "help": nil, "check-ignore": nil, "ls-remote": nil, "reflog": reflogSafe,
	"branch": branchSafe, "tag": tagSafe, "remote": remoteSafe, "config": configSafe, "stash": stashSafe,
	"worktree": func(a []string) bool { return len(a) > 0 && a[0] == "list" },
}

func reflogSafe(a []string) bool {
	return len(a) == 0 || a[0] == "show" || strings.HasPrefix(a[0], "-")
}

func branchSafe(a []string) bool {
	for _, x := range a {
		switch x {
		case "-d", "-D", "-m", "-M", "-c", "-C", "--delete", "--move", "--copy", "--set-upstream-to", "-u", "--unset-upstream", "--edit-description", "-f", "--force":
			return false
		}
		if !strings.HasPrefix(x, "-") {
			return false // creating a branch
		}
	}
	return true
}

func tagSafe(a []string) bool {
	if len(a) == 0 {
		return true
	}
	for _, x := range a {
		if x == "-l" || x == "--list" || x == "--contains" || x == "--points-at" || x == "-n" {
			return true
		}
	}
	return false
}

func remoteSafe(a []string) bool {
	return len(a) == 0 || a[0] == "-v" || a[0] == "--verbose" || a[0] == "show" || a[0] == "get-url"
}

func configSafe(a []string) bool {
	for _, x := range a {
		if x == "--get" || x == "--get-all" || x == "--list" || x == "-l" || x == "--get-regexp" {
			return true
		}
	}
	return false
}

func stashSafe(a []string) bool { return len(a) > 0 && (a[0] == "list" || a[0] == "show") }

func gitSafe(args []string) bool {
	// Skip global options; options that take a value are not read-only-safe
	// to reason about except -C (directory) and --no-pager.
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		switch args[i] {
		case "-C":
			i += 2
			continue
		case "--no-pager", "--no-optional-locks", "-P", "--paginate":
			i++
			continue
		}
		return args[i] == "--version" || args[i] == "--help"
	}
	if i >= len(args) {
		return false
	}
	check, ok := gitReadOnly[args[i]]
	if !ok {
		return false
	}
	rest := args[i+1:]
	for _, a := range rest {
		if a == "--output" || strings.HasPrefix(a, "--output=") || strings.HasPrefix(a, "--ext-diff") {
			return false
		}
	}
	return check == nil || check(rest)
}

// isReadOnlyCommand reports whether a normalized simple command is in the
// built-in read-only allowlist and writes nowhere.
func isReadOnlyCommand(c SimpleCommand) bool {
	if c.Unsafe || len(c.Words) == 0 {
		return false
	}
	for _, r := range c.Redirects {
		switch r.Op {
		case ">", ">>", "&>", "&>>", ">|":
			if r.Target != "/dev/null" {
				return false
			}
		case ">&":
			if r.Target != "1" && r.Target != "2" && r.Target != "-" {
				return false
			}
		}
	}
	name := c.Name()
	if strings.Contains(name, "/") {
		return false
	}
	check, ok := readOnlyCommands[name]
	if !ok {
		return false
	}
	return check == nil || check(c.Args())
}

// IsReadOnlyCommandLine reports whether every subcommand of a command line
// is read-only (used by tools to decide parallel execution).
func IsReadOnlyCommandLine(cmd string) bool {
	cmds := SplitCommand(cmd)
	if len(cmds) == 0 {
		return false
	}
	for _, c := range cmds {
		if !isReadOnlyCommand(c.Normalize()) {
			return false
		}
	}
	return true
}

// acceptEditsCommands are filesystem commands auto-approved in acceptEdits
// mode when all their path arguments are inside the working directories.
var acceptEditsCommands = map[string]bool{"mkdir": true, "touch": true, "rm": true, "rmdir": true, "mv": true, "cp": true, "sed": true}

func (e *Engine) acceptEditsCommand(c SimpleCommand, cwd string, workDirs []string) bool {
	if c.Unsafe || !acceptEditsCommands[c.Name()] {
		return false
	}
	args := c.Args()
	paths := 0
	sedScript := c.Name() == "sed"
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			if c.Name() == "sed" && (a == "-e" || a == "-f") {
				i++
				sedScript = false
			}
			continue
		}
		if sedScript {
			sedScript = false // first operand is the script
			continue
		}
		if strings.ContainsAny(a, "$`") {
			return false
		}
		p := a
		if strings.HasPrefix(p, "~") {
			p = filepath.Join(e.home, strings.TrimPrefix(p, "~"))
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		// Static prefix of a glob.
		if i := strings.IndexAny(p, "*?["); i >= 0 {
			p = filepath.Dir(p[:i] + "x")
		}
		p = filepath.Clean(p)
		if !inAny(p, workDirs) || isProtected(p, workDirs, e.home) {
			return false
		}
		paths++
	}
	for _, r := range c.Redirects {
		if r.Op == ">" || r.Op == ">>" || r.Op == "&>" {
			return false
		}
	}
	return paths > 0
}
