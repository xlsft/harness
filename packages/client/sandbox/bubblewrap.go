package sandbox

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The bubblewrap command line of a policy, as a pure function. bwrap applies its options in order and a later mount covers an
// earlier one, so the order below is the policy, as in the Seatbelt profile: a read-only root, the writable roots over it, and
// every protection over those.

// insideProxySocket is where the proxy's socket appears in the namespace, on the tmpfs that covers /run.
const insideProxySocket = "/run/harness-sandbox/proxy.sock"

// bwrapInput is what the command line is made from, besides the policy.
type bwrapInput struct {
	Policy Policy
	Kind   func(path string) pathKind
	// Masked is a file nobody can read, bound over the files that must not be read.
	Masked string
	// Sockets are the sockets of other programs found under /tmp.
	Sockets []string
	// Protected are the entries with protected names found under the working directories (findProtected).
	Protected []string
	// Stage is what runs inside.
	Stage []string
}

func bubblewrapArgs(in bwrapInput) []string {
	p := in.Policy
	// The sandbox's process is PID 1 of its namespace: when the command ends, so does everything it left behind, and bwrap
	// does not wait for those.
	args := []string{"--unshare-user", "--unshare-pid", "--as-pid-1", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try", "--die-with-parent", "--cap-drop", "ALL"}
	if p.PrivateLoopback {
		args = append(args, "--unshare-net")
	}
	add := func(a ...string) { args = append(args, a...) }
	inRoot := func(path string) bool {
		return slices.ContainsFunc(p.Write, func(r string) bool { return within(path, r) })
	}

	add("--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc")
	// /run holds the sockets of D-Bus, Docker and the user's agents, and secrets in a container. NixOS keeps programs in it.
	add("--tmpfs", "/run", "--ro-bind-try", "/run/current-system", "/run/current-system")
	if p.ProxySocket != "" {
		add("--ro-bind", p.ProxySocket, insideProxySocket)
	}

	roots := slices.Clone(p.Write)
	// Outer ones first, so that a root inside another is not covered by it.
	slices.SortStableFunc(roots, func(a, b string) int { return strings.Count(a, "/") - strings.Count(b, "/") })
	for _, r := range roots {
		if in.Kind(r) != pathMissing {
			add("--bind", r, r)
		}
	}

	readOnly := func(path string) {
		if in.Kind(path) != pathMissing && inRoot(path) {
			add("--ro-bind", path, path)
		}
	}
	for _, d := range p.WriteDeny {
		readOnly(d)
	}
	for _, r := range p.WriteAllowAfterDeny {
		if in.Kind(r) != pathMissing {
			add("--bind", r, r)
		}
	}
	// The command's own temp tree, also when the data directory around it is protected.
	if p.Free != "" && in.Kind(p.Free) != pathMissing {
		add("--bind", p.Free, p.Free)
	}
	for _, f := range in.Protected {
		readOnly(f)
	}
	// What is protected inside an agent folder's worktrees is its own: a session works there.
	for _, r := range p.Write {
		for _, name := range []string{".harness", ".claude"} {
			if w := filepath.Join(r, name, "worktrees"); in.Kind(w) == pathDir {
				add("--bind", w, w)
			}
		}
	}
	for _, d := range p.FreeDeny {
		readOnly(d)
	}
	for _, f := range p.WriteDenyLiteral {
		readOnly(f)
	}

	for _, d := range p.ReadDeny {
		switch in.Kind(d) {
		case pathMissing:
		case pathDir:
			add("--tmpfs", d)
		default:
			add("--ro-bind", in.Masked, d)
		}
	}
	for _, s := range in.Sockets {
		add("--ro-bind", in.Masked, s)
	}
	add("--")
	return append(args, in.Stage...)
}

// The protected names below a working directory are looked for only so deep and only so long: the common cases are a
// repository's own and those of a monorepo's packages, and a command starts on this path.
const (
	protectedDepth   = 3
	protectedEntries = 20000
	socketsDepth     = 3
)

// skippedDirs hold many folders and nothing of the user's.
var skippedDirs = []string{"node_modules", "vendor", "target", "dist", "build", ".venv", "__pycache__"}

// findProtected returns what exists below root with a protected name: the git entries, agent folders and MCP files that
// the bind mounts cover. New ones are the guard's (guard.go).
func findProtected(root string) []string {
	var out []string
	n := 0
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if n++; n > protectedEntries {
				return
			}
			path := filepath.Join(dir, e.Name())
			switch e.Name() {
			case ".mcp.json", ".harness", ".claude":
				out = append(out, path)
				continue
			case ".git":
				if e.IsDir() {
					out = append(out, filepath.Join(path, "hooks"), filepath.Join(path, "config"), filepath.Join(path, "config.worktree"))
				} else {
					out = append(out, path)
				}
				continue
			}
			if e.IsDir() && depth < protectedDepth && !slices.Contains(skippedDirs, e.Name()) {
				walk(path, depth+1)
			}
		}
	}
	walk(root, 0)
	return out
}

// findSockets returns the unix sockets below dir, which a read-only or writable bind leaves as reachable as ever.
func findSockets(dir string) []string {
	var out []string
	var walk func(d string, depth int)
	walk = func(d string, depth int) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			path := filepath.Join(d, e.Name())
			switch {
			case e.Type()&fs.ModeSocket != 0:
				out = append(out, path)
			case e.IsDir() && depth < socketsDepth:
				walk(path, depth+1)
			}
		}
	}
	walk(dir, 0)
	return out
}
