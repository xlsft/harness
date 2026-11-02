// Package decision implements the deciders behind the `auto` permission
// mode (permissions.Decider):
//
//   - Rules: a Go port of the server's deterministic `rules` provider
//     (packages/core/src/decision/rules.ts, ported in rulesengine.go);
//   - Client: the Connect client of harness.decision.v1.DecisionService;
//   - Chain: local rules first (a local DENY or fast ALLOW is final and
//     skips the network), then the server; when the server is unreachable or
//     errors the local verdict stands and anything not fast-allowed is ASK;
//   - Cache: a session-level cache of ALLOW/DENY verdicts keyed by tool and
//     normalized input, cleared when the permission mode changes;
//   - RulesSync: keeps the local rules on the administrator's rule options
//     (GetRulesOptions), cached on disk per server and user and refetched
//     when a verdict reports a different rules_version.
//
// New wires them together: Cache(Chain(Rules, Client)); NewWithRulesSync
// adds a RulesSync.
package decision

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Facts are what the client already knows about a call (ClientFacts). They
// are sent to the server and fed to the local rules exactly as sent.
type Facts struct {
	// Subcommands are the simple commands of a Bash call as the rules see
	// them (wrappers stripped; substitutions, `sh -c` / eval bodies and
	// `find -exec` commands included), rendered as shell text that parses
	// back to the same words.
	Subcommands []string
	// WritePaths are the absolute paths the call writes to.
	WritePaths []string
	// NetworkHosts are the hosts the call contacts, when detectable.
	NetworkHosts  []string
	InsideGitRepo bool
	GitBranch     string
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
// Bash commands are read with the rules' own parser (rulesengine.go), so the
// facts describe the same simple commands the rules check.
func ComputeFacts(ctx context.Context, req permissions.AutoRequest, git GitInfo) Facts {
	if f, ok := factsFrom(ctx); ok {
		return f
	}
	var f Facts
	input := decode(req.Input)
	cwd := normRoot(req.Cwd)
	switch {
	case req.ToolName == "Bash":
		cmd, ok := input["command"].(string)
		if !ok {
			cmd = req.Command
		}
		cmds, err := flattenCommand(cmd, cwd)
		if err != nil {
			break // unparsable: the rules ask (or deny through their raw fallback)
		}
		for _, c := range cmds {
			if s := renderSubcommand(c); s != "" {
				f.Subcommands = append(f.Subcommands, s)
			}
			f.NetworkHosts = appendUnique(f.NetworkHosts, commandHosts(c)...)
		}
		f.WritePaths = commandWritePaths(cmds, req.Home)
	case permissions.IsEditTool(req.ToolName):
		p := req.FilePath
		if p == "" {
			p, _ = input["file_path"].(string)
		}
		if p == "" {
			p, _ = input["notebook_path"].(string)
		}
		if p != "" {
			if r := resolvePath(p, cwd); r != "" {
				f.WritePaths = []string{absolutePath(r, req.Home)}
			}
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

// absolutePath writes a resolved path ("~/x" or "/x") with the home
// directory expanded; "~" paths stay as they are when home is unknown.
func absolutePath(p, home string) string {
	home = strings.TrimSuffix(filepath.ToSlash(home), "/")
	if home == "" || !strings.HasPrefix(p, "~") {
		return p
	}
	return home + p[1:]
}

// renderSubcommand writes a simple command (assignments, argv after wrapper
// stripping, redirections other than heredocs) as shell text whose words
// parse back to the same values.
func renderSubcommand(c *Cmd) string {
	var parts []string
	for _, a := range c.assigns {
		parts = append(parts, shellQuote(a))
	}
	for _, a := range c.argv {
		parts = append(parts, shellQuote(a))
	}
	for _, r := range c.redirects {
		if r.Op == "<<" || r.Op == "<<-" {
			continue
		}
		sep := " "
		if strings.HasSuffix(r.Op, "&") {
			sep = "" // 2>&1
		}
		parts = append(parts, r.Fd+r.Op+sep+shellQuote(r.Target.Text))
	}
	return strings.Join(parts, " ")
}

// shellSafeWord matches words the rules' parser reads literally.
var shellSafeWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./~^-]+$`)

func shellQuote(s string) string {
	if shellSafeWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// commandWritePaths returns the absolute paths the simple commands write to:
// output redirections and the write targets the rules know (cp/mv/ln/install/
// rsync destinations, tee/touch/truncate operands, sed -i files, dd of=,
// chmod/chown targets) plus mkdir operands; deletions (rm, unlink, shred)
// are not included. Words with
// unknown expansions and commands with unknown arguments (xargs, find -exec)
// are skipped.
func commandWritePaths(cmds []*Cmd, home string) []string {
	var out []string
	for _, c := range cmds {
		if c.unknownArgs {
			continue
		}
		var targets []string
		// Deletions are not writes: the rules judge rm targets themselves
		// (as destructive), and a privileged path among the write paths would
		// add privilege_escalation to that verdict.
		if c.name != "rm" && c.name != "unlink" && c.name != "shred" {
			targets = append(targets, writeTargets(c)...)
		}
		if c.name == "mkdir" {
			targets = append(targets, parseArgs(tail(c.argv), "-m", "--mode").positionals...)
		}
		targets = append(targets, redirectTargets(redirectWrites(c))...)
		for _, t := range targets {
			if t == "" || t == "-" {
				continue
			}
			p := resolvePath(t, c.cwd)
			if p == "" || p == "/dev/null" {
				continue
			}
			out = appendUnique(out, absolutePath(p, home))
		}
	}
	return out
}

// hostTools contact the hosts named in their URL or user@host arguments.
var hostTools = setOf("curl", "wget", "http", "https", "xh", "aria2c", "git", "ssh", "scp", "sftp", "rsync", "nc", "ncat", "telnet", "ftp")

func commandHosts(c *Cmd) []string {
	name := c.name
	if !hostTools[name] {
		return nil
	}
	var hosts []string
	for _, a := range tail(c.argv) {
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
		if it == "" || contains(dst, it) {
			continue
		}
		dst = append(dst, it)
	}
	return dst
}

func decode(raw json.RawMessage) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}
