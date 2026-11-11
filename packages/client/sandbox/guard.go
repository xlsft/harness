package sandbox

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// The guard stands behind a kernel that cannot hold a name: Landlock gives a folder's rights to everything below it, and a
// bind mount needs something to bind over. So what the patterns of policy.go protect is recorded when a command starts and
// looked at again when it ends. What changed goes to <scratch>/sandbox-rejected, the original comes back, and Diagnose reports
// it as a refusal. Nothing is deleted, since an edit the user made meanwhile looks the same. It catches a command that wrote a
// hook or a settings file. It is no boundary: a process that outlives its command can write afterwards
// (docs/design/platforms.md).

const (
	// guardFileMax is the most of one file kept to put back.
	guardFileMax = 1 << 20
	// guardEntriesMax bounds a protected tree, should one be large.
	guardEntriesMax = 2000
)

type guardEntry struct {
	mode fs.FileMode
	link string
	data []byte
	big  bool
	sum  [sha256.Size]byte
}

func readGuardEntry(path string) (guardEntry, bool) {
	st, err := os.Lstat(path)
	if err != nil {
		return guardEntry{}, false
	}
	e := guardEntry{mode: st.Mode()}
	switch {
	case st.Mode()&fs.ModeSymlink != 0:
		e.link, _ = os.Readlink(path)
	case st.Mode().IsRegular():
		data, err := os.ReadFile(path)
		if err != nil {
			e.big = true
			break
		}
		e.sum = sha256.Sum256(data)
		if len(data) > guardFileMax {
			e.big = true
		} else {
			e.data = data
		}
	}
	return e, true
}

func (e guardEntry) same(o guardEntry) bool {
	return e.mode.Type() == o.mode.Type() && e.link == o.link && e.sum == o.sum && e.mode.Perm()&0o111 == o.mode.Perm()&0o111
}

type guard struct {
	policy  Policy
	aside   string
	watched map[string]guardEntry
	present map[string]bool
	trees   []string

	once   sync.Once
	result []string
}

// agentFiles are the files of a working directory that the client itself reads as settings or starts programs from.
var agentFiles = []string{".mcp.json", ".harness/settings.json", ".harness/settings.local.json", ".claude/settings.json", ".claude/settings.local.json"}

// agentHomeGuarded are what the guard looks at in an agent's data directory (Policy.AgentHomes): settings with allow rules
// and hooks, the programs and instructions the agents load, what this client trusts or starts, and its credentials. The
// rest of it (transcripts, plans, file history, caches, logs, a browser profile) the agents write while a command runs, so
// a guard over it would put their writes back, and read all of it before every command.
var agentHomeGuarded = []string{
	"settings.json", "settings.local.json",
	"plugins", "skills", "agents", "commands", "hooks", "bin", "extensions",
	"trust.json", "hook-trust.json", "mcp-local.json", "mcp-approvals.json", "import-approvals.json",
	"local-providers.json", "device-settings.json", "computer-use", "environments", "hosts", "ssh",
	"credentials.json", "agent-token", "local-provider-keys.json", "pr-marker-salt",
}

func newGuard(p Policy, scratch string) *guard {
	g := &guard{policy: p, aside: filepath.Join(scratch, "sandbox-rejected"), watched: map[string]guardEntry{}, present: map[string]bool{}}
	// What the kernel holds needs no second look, and must get none: the client itself may write there while the command
	// runs, and the guard cannot tell its writes from the command's.
	held := func(path string) bool {
		if !p.DenyHeld {
			return false
		}
		_, err := os.Lstat(path)
		return err == nil
	}
	watch := func(path string) {
		if _, ok := g.watched[path]; ok || held(path) {
			return
		}
		e, ok := readGuardEntry(path)
		g.watched[path] = e
		g.present[path] = ok
	}
	// A tree with a writable folder of the policy inside (a folderless session's) is that folder's.
	addTree := func(t string) {
		if !held(t) && !slices.ContainsFunc(p.WriteAllowAfterDeny, func(a string) bool { return within(a, t) }) {
			g.trees = appendUnique(g.trees, t)
		}
	}
	for _, l := range p.WriteDenyLiteral {
		watch(l)
	}
	for _, w := range p.Work {
		for _, n := range agentFiles {
			watch(filepath.Join(w, filepath.FromSlash(n)))
		}
		addTree(filepath.Join(w, ".git", "hooks"))
		for _, d := range gitDirs(w) {
			addTree(filepath.Join(d, "hooks"))
		}
		// A .git that is a file sends git elsewhere; a new directory is an ordinary git init.
		if st, err := os.Lstat(filepath.Join(w, ".git")); err != nil || !st.IsDir() {
			watch(filepath.Join(w, ".git"))
		}
	}
	for _, d := range p.FreeDeny {
		addTree(d)
	}
	for _, d := range p.WriteDeny {
		if !slices.ContainsFunc(p.Write, func(r string) bool { return within(d, r) }) || held(d) {
			continue
		}
		if !slices.Contains(p.AgentHomes, d) {
			addTree(d)
			continue
		}
		for _, n := range agentHomeGuarded {
			addTree(filepath.Join(d, n))
		}
	}
	for _, t := range g.trees {
		walkGuarded(t, watch)
	}
	return g
}

// walkGuarded calls f for what is in a protected path, which may also be a single file or not exist.
func walkGuarded(root string, f func(path string)) {
	n := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil && d != nil && d.IsDir():
			return filepath.SkipDir
		case err != nil || d.IsDir():
			return nil
		}
		if n++; n > guardEntriesMax {
			return filepath.SkipAll
		}
		f(path)
		return nil
	})
}

// verify puts back what changed and returns one "path: why" for each. It does its work once.
func (g *guard) verify() []string {
	g.once.Do(func() {
		for _, path := range sortedKeys(g.watched) {
			g.check(path)
		}
		for _, t := range g.trees {
			walkGuarded(t, func(path string) {
				if _, known := g.watched[path]; !known {
					g.reject(path, guardEntry{}, false)
				}
			})
		}
	})
	return g.result
}

func (g *guard) check(path string) {
	was, wasThere := g.watched[path], g.present[path]
	now, isThere := readGuardEntry(path)
	switch {
	case !wasThere && !isThere:
	case wasThere && isThere && was.same(now):
	case !wasThere && now.mode.IsDir() && strings.HasSuffix(path, "/.git"):
		// git init.
	default:
		g.reject(path, was, wasThere)
	}
}

// reject moves path aside, if it is there, and puts back what was there.
func (g *guard) reject(path string, was guardEntry, wasThere bool) {
	if _, err := os.Lstat(path); err == nil {
		if err := os.MkdirAll(g.aside, 0o700); err == nil {
			_ = os.Rename(path, filepath.Join(g.aside, strconv.Itoa(len(g.result))+"-"+filepath.Base(path)))
		}
	}
	why := g.policy.Why(path, true)
	if why == "" {
		why = "it can make programs run outside the sandbox, so no sandboxed command writes it"
	}
	note := "; what the command left there was moved to " + g.aside
	if wasThere {
		if err := restoreGuardEntry(path, was); err != nil {
			note += ", and the original could not be put back: " + err.Error()
		} else {
			note += ", and the original put back"
		}
	}
	g.result = append(g.result, path+": "+why+note)
}

func restoreGuardEntry(path string, e guardEntry) error {
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		return err
	}
	switch {
	case e.mode&fs.ModeSymlink != 0:
		return os.Symlink(e.link, path)
	case e.mode.IsRegular() && !e.big:
		if err := os.WriteFile(path, e.data, e.mode.Perm()); err != nil {
			return err
		}
		return os.Chmod(path, e.mode.Perm())
	case e.mode.IsDir():
		return os.Mkdir(path, e.mode.Perm())
	}
	return os.ErrInvalid
}

func sortedKeys(m map[string]guardEntry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Verify looks at what the guard protects, once the command has ended, and returns what it found and put back: entries as in
// Denial.Paths. Diagnose calls it; a command that was stopped gets no Diagnose, so its caller calls this.
func (w *Wrapped) Verify() []string {
	if w == nil || w.guard == nil {
		return nil
	}
	return slices.Clone(w.guard.verify())
}
