// Package decision implements the deciders behind the `auto` permission
// mode (permissions.Decider):
//
//   - Rules: a Go port of the server's deterministic `rules` provider
//     (hard-deny and fast-allow tables, rules.go);
//   - Client: the Connect client of harness.decision.v1.DecisionService;
//   - Chain: local rules first (a local DENY or fast ALLOW is final and
//     skips the network), then the server; when the server is unreachable or
//     errors the local verdict stands and anything not fast-allowed is ASK;
//   - Cache: a session-level cache of ALLOW/DENY verdicts keyed by tool and
//     normalized input, cleared when the permission mode changes.
//
// New wires them together: Cache(Chain(Rules, Client)).
package decision

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Facts are what the client already knows about a call (ClientFacts).
type Facts struct {
	// Subcommands are the normalized simple commands of a Bash call
	// (wrappers stripped, substitutions and `sh -c` scripts included).
	Subcommands []string
	// WritePaths are the absolute paths the call writes to.
	WritePaths []string
	// NetworkHosts are the hosts the call contacts, when detectable.
	NetworkHosts  []string
	InsideGitRepo bool
	GitBranch     string
	// Unsafe is set when the command contains constructs whose effect
	// cannot be determined statically (command substitution, eval, ...).
	Unsafe bool
}

// GitInfo reports whether dir is inside a git work tree and its branch.
type GitInfo func(ctx context.Context, dir string) (inside bool, branch string)

// DefaultGitInfo asks git.
func DefaultGitInfo(ctx context.Context, dir string) (bool, string) {
	root := workspace.GitRoot(ctx, dir)
	if root == "" {
		return false, ""
	}
	return true, workspace.GitBranch(ctx, dir)
}

type factsKey struct{}

// withFacts memoizes facts for the deciders of one Chain call.
func withFacts(ctx context.Context, f Facts) context.Context {
	return context.WithValue(ctx, factsKey{}, f)
}

func factsFrom(ctx context.Context) (Facts, bool) {
	f, ok := ctx.Value(factsKey{}).(Facts)
	return f, ok
}

// ComputeFacts derives the facts of a call. git may be nil (no git facts).
func ComputeFacts(ctx context.Context, req permissions.AutoRequest, git GitInfo) Facts {
	if f, ok := factsFrom(ctx); ok {
		return f
	}
	var f Facts
	input := decode(req.Input)
	switch {
	case req.ToolName == "Bash":
		cmd := req.Command
		if cmd == "" {
			cmd, _ = input["command"].(string)
		}
		for _, c := range expand(cmd, 0) {
			f.Subcommands = append(f.Subcommands, c.text)
			if c.unsafe {
				f.Unsafe = true
			}
			f.NetworkHosts = appendUnique(f.NetworkHosts, commandHosts(c.cmd)...)
		}
		f.WritePaths = permissions.CommandWritePaths(cmd, req.Cwd, req.Home)
	case permissions.IsEditTool(req.ToolName):
		p := req.FilePath
		if p == "" {
			p, _ = input["file_path"].(string)
		}
		if p == "" {
			p, _ = input["notebook_path"].(string)
		}
		if p != "" {
			if !filepath.IsAbs(p) {
				p = filepath.Join(req.Cwd, p)
			}
			f.WritePaths = []string{filepath.Clean(p)}
		}
	case req.ToolName == "WebFetch":
		if u, _ := input["url"].(string); u != "" {
			f.NetworkHosts = appendUnique(nil, urlHost(u))
		}
	}
	if git != nil && req.Cwd != "" {
		f.InsideGitRepo, f.GitBranch = git(ctx, req.Cwd)
	}
	return f
}

// subcommand is one normalized simple command with its rule text.
type subcommand struct {
	cmd    permissions.SimpleCommand
	text   string
	unsafe bool
}

// shells run their `-c` argument as a script.
var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true}

// expand splits a command line into normalized simple commands, descending
// into substitutions and `sh -c` scripts (depth-limited).
func expand(cmd string, depth int) []subcommand {
	var out []subcommand
	for _, raw := range permissions.SplitCommand(cmd) {
		c := raw.Normalize()
		sc := subcommand{cmd: c, text: ruleText(c), unsafe: raw.Unsafe}
		if n := filepath.Base(c.Name()); n == "eval" || n == "source" || n == "." {
			sc.unsafe = true
		}
		out = append(out, sc)
		if depth >= 3 {
			continue
		}
		for _, s := range raw.Substitutions {
			out = append(out, expand(s, depth+1)...)
		}
		if shells[filepath.Base(c.Name())] {
			args := c.Args()
			for i, a := range args {
				if a == "-c" && i+1 < len(args) {
					out = append(out, expand(args[i+1], depth+1)...)
					break
				}
			}
		}
	}
	return out
}

// ruleText renders a simple command for the rule patterns: unquoted word
// values joined by single spaces, then redirections ("> target").
func ruleText(c permissions.SimpleCommand) string {
	parts := make([]string, 0, len(c.Words)+len(c.Redirects))
	for _, w := range c.Words {
		parts = append(parts, w.Value)
	}
	for _, r := range c.Redirects {
		parts = append(parts, r.FD+r.Op+" "+r.Target)
	}
	return strings.Join(parts, " ")
}

// networkTools contact the hosts named in their URL or user@host arguments.
var networkTools = map[string]bool{
	"curl": true, "wget": true, "http": true, "https": true, "xh": true, "aria2c": true, "git": true,
	"ssh": true, "scp": true, "sftp": true, "rsync": true, "nc": true, "ncat": true, "telnet": true, "ftp": true,
}

func commandHosts(c permissions.SimpleCommand) []string {
	name := filepath.Base(c.Name())
	if !networkTools[name] {
		return nil
	}
	var hosts []string
	for _, a := range c.Args() {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if strings.Contains(a, "://") {
			if h := urlHost(a); h != "" {
				hosts = append(hosts, h)
			}
			continue
		}
		switch name {
		case "git", "ssh", "scp", "sftp", "rsync":
			// user@host[:path] or (scp/rsync/git) host:path
			at := strings.LastIndexByte(a, '@')
			rest := a
			if at >= 0 {
				rest = a[at+1:]
			}
			if i := strings.IndexByte(rest, ':'); i > 0 {
				rest = rest[:i]
			} else if at < 0 {
				continue
			}
			if rest != "" && !strings.ContainsAny(rest, "/ ") {
				hosts = append(hosts, strings.ToLower(rest))
			}
		}
	}
	return hosts
}

func urlHost(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func appendUnique(dst []string, items ...string) []string {
	for _, it := range items {
		if it == "" {
			continue
		}
		dup := false
		for _, d := range dst {
			if d == it {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, it)
		}
	}
	return dst
}

func decode(raw json.RawMessage) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}
