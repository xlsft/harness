package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Policy is what one command may do, with every path resolved (Seatbelt compares the real path, so /tmp must be written
// /private/tmp). SeatbeltProfile renders it on macOS, bubblewrapArgs and planFiles on Linux.
type Policy struct {
	// Home is the user's home directory.
	Home string
	// Work are the working directories, resolved: where the protected names matter most (guard.go).
	Work []string
	// Write are the writable roots.
	Write []string
	// Mkdir are folders that may be created, and nothing else done to them.
	Mkdir []string
	// WriteDeny are trees no command writes, whatever the roots say.
	WriteDeny []string
	// WriteAllowAfterDeny are writable roots inside WriteDeny: the private folder of a folderless session.
	WriteAllowAfterDeny []string
	// Free is the session's scratch directory, where the rules for protected file names do not apply; FreeDeny are the
	// parts of it that matter to what runs outside the sandbox.
	Free     string
	FreeDeny []string
	// WriteDenyLiteral are single files, which may not exist yet.
	WriteDenyLiteral []string
	// ReadDeny are the credential stores.
	ReadDeny []string
	// Sockets are the roots in which unix sockets can be made and reached.
	Sockets []string
	// ProxyPort is the local proxy.
	ProxyPort int
	// Localhost and Server open the loopback interface (Settings.AllowLocalhost, KindServer).
	Localhost, Server bool
	// Tag marks this command's refusals in the system log, where a driver can read them (Reporter).
	Tag string

	// What a Preparer sets, for the drivers that need it and for explaining failures.

	// ProxySocket is the proxy's unix socket, for a command in a network namespace of its own.
	ProxySocket string
	// PrivateLoopback says the command has a loopback interface of its own: nothing on this machine is reachable on it.
	PrivateLoopback bool
	// GuardNames says the kernel cannot hold the protected names in the writable roots here, so the guard looks at them.
	GuardNames bool
	// FilesByPermission says a refused file access shows as EACCES, as Landlock's does, and not as EPERM.
	FilesByPermission bool
	// PortRules says a refused connection shows as EACCES, as Landlock's does.
	PortRules bool
}

// The file patterns that apply at any depth. They are POSIX extended regular expressions, which RE2 reads the same way, so
// Policy.Why gives the same answer as the kernel.
const (
	agentDirsPattern = `/\.(claude|harness)(/|$)`
	gitEntryPattern  = `/\.git$`
)

var (
	agentDirsRe = regexp.MustCompile(agentDirsPattern)
	gitEntryRe  = regexp.MustCompile(gitEntryPattern)
)

// protectedFilePatterns are written in no folder.
var protectedFilePatterns = []string{
	// Hooks, including those of submodules and linked worktrees, run on the user's next git command.
	`/\.git/(.*/)?hooks(/|$)`,
	// Agent settings: allow rules, hooks and this switch itself.
	`/\.(claude|harness)/settings(\.local)?\.json$`,
	// MCP servers a project declares are programs the client starts.
	`/\.mcp\.json$`,
}

var protectedFileRes = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(protectedFilePatterns))
	for i, p := range protectedFilePatterns {
		out[i] = regexp.MustCompile(p)
	}
	return out
}()

// homeWriteDeny are the paths under the home directory that matter even when a working directory contains them. Shell
// startup files and git's own configuration run code in every later shell or git command, outside the sandbox, and so do
// launch agents at the next login.
var homeWriteDeny = []string{
	".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".profile",
	".zshrc", ".zprofile", ".zshenv", ".zlogin", ".zlogout",
	".config/fish", ".gitconfig", ".config/git",
	".claude", ".harness",
	"Library/LaunchAgents",
	".config/autostart", ".config/systemd/user", ".config/environment.d", ".pam_environment",
}

// homeReadDeny are the credential stores under the home directory, also closed for writing. decision.IsSecretPath knows
// these places as patterns over one path; a profile needs them spelled out (TestHomeReadDenyAreSecrets keeps the two in
// step). A project's own .env stays readable: it is the project's, and its builds and tests read it.
var homeReadDeny = []string{
	".ssh", ".aws", ".gnupg", "Library/Keychains",
	".netrc", ".npmrc", ".pypirc", ".git-credentials", ".docker/config.json", ".kube/config",
	".config/gcloud", ".config/gh/hosts.yml", ".azure", ".password-store", ".local/share/keyrings",
}

// harnessSecrets are what HARNESS_HOME holds of credentials: sign-in, the loopback token, provider keys, folder
// environments, the browser's logged-in profile, ACP agents' state. The rest of it stays readable (a plan, a transcript)
// and none of it writable, which covers the computer use settings.
var harnessSecrets = []string{
	"credentials.json", "agent-token", "local-provider-keys.json", "pr-marker-salt", "environments", "browser", "acp",
}

// systemReadDeny are the credential stores outside the home directory: the keychains of macOS; on Linux the password
// hashes, which a command that runs as root in a container could otherwise read, and the secrets Docker and Kubernetes mount
// into one.
var systemReadDeny = []string{"/Library/Keychains", "/Network/Library/Keychains", "/etc/shadow", "/etc/gshadow", "/run/secrets", "/var/run/secrets"}

// PolicyInput is what a policy is built from.
type PolicyInput struct {
	Home, HarnessHome string
	WorkDirs          []string
	ScratchDir        string
	// TempDir is the command's TMPDIR.
	TempDir string
	// ExtraWrite are Settings.AllowedWritePaths: absolute, or starting with ~/.
	ExtraWrite []string
	// Getenv reads the client's environment for toolchain locations (nil: os.Getenv).
	Getenv func(string) string
	// ProxyPort is the local proxy.
	ProxyPort int
	// Localhost and Server are Policy's.
	Localhost, Server bool
}

// BuildPolicy turns the session's state into a policy. It reads the file system only to resolve symlinks and to find the
// git directories of the working directories.
func BuildPolicy(in PolicyInput) Policy {
	getenv := in.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	home := canon(in.Home)
	harnessHome := canon(in.HarnessHome)
	p := Policy{Home: home, ProxyPort: in.ProxyPort, Localhost: in.Localhost, Server: in.Server}

	var roots []string
	addRoot := func(path string) {
		if path == "" {
			return
		}
		if c := canon(path); c != "/" && !slices.Contains(roots, c) {
			roots = append(roots, c)
		}
	}
	for _, d := range in.WorkDirs {
		addRoot(d)
		p.Work = appendUnique(p.Work, canon(d))
	}
	addRoot(in.ScratchDir)
	addRoot(in.TempDir)
	for _, d := range in.ExtraWrite {
		if d == "~" || strings.HasPrefix(d, "~/") {
			d = filepath.Join(in.Home, strings.TrimPrefix(d, "~"))
		}
		if filepath.IsAbs(d) {
			addRoot(d)
		}
	}
	// The temp files of programs that insist on /tmp. No place for sockets: those of tmux and others live there.
	addRoot(systemTmp)
	for _, t := range toolchainPaths(in.Home, getenv) {
		addRoot(t.Path)
		if t.Path != "" && home != "" {
			for dir := filepath.Dir(canon(t.Path)); dir != home && within(dir, home); dir = filepath.Dir(dir) {
				p.Mkdir = appendUnique(p.Mkdir, dir)
			}
		}
	}

	// Git: a working directory may be a linked worktree or a subfolder of a repository, whose git directory is elsewhere.
	// Committing writes there, so it joins the roots, and its config and hooks stay protected: a config can name a program
	// to run (core.fsmonitor, core.hooksPath), and this client runs git in the session's folders outside the sandbox.
	for _, d := range in.WorkDirs {
		dir := canon(d)
		p.WriteDenyLiteral = appendUnique(p.WriteDenyLiteral, filepath.Join(dir, ".git", "config"), filepath.Join(dir, ".git", "config.worktree"))
		for _, g := range gitDirs(dir) {
			addRoot(g)
			p.WriteDenyLiteral = appendUnique(p.WriteDenyLiteral, filepath.Join(g, "config"), filepath.Join(g, "config.worktree"))
		}
	}

	// The session's shell snapshot runs in every later command, the unconfined ones included.
	if in.ScratchDir != "" {
		s := canon(in.ScratchDir)
		p.WriteDenyLiteral = appendUnique(p.WriteDenyLiteral, filepath.Join(s, "shell-snapshot.bash"), filepath.Join(s, "shell-snapshot.zsh"))
		p.Free, p.FreeDeny = s, []string{filepath.Join(s, "git-template")}
	}

	if home != "" {
		for _, n := range homeWriteDeny {
			p.WriteDeny = appendUnique(p.WriteDeny, filepath.Join(home, filepath.FromSlash(n)))
		}
		for _, n := range homeReadDeny {
			h := filepath.Join(home, filepath.FromSlash(n))
			p.ReadDeny = appendUnique(p.ReadDeny, h)
			p.WriteDeny = appendUnique(p.WriteDeny, h)
		}
	}
	if v := getenv("ZDOTDIR"); v != "" {
		for _, n := range []string{".zshrc", ".zprofile", ".zshenv", ".zlogin", ".zlogout"} {
			p.WriteDeny = appendUnique(p.WriteDeny, filepath.Join(canon(v), n))
		}
	}
	p.ReadDeny = appendUnique(p.ReadDeny, systemReadDeny...)
	if harnessHome != "" {
		p.WriteDeny = appendUnique(p.WriteDeny, harnessHome)
		for _, n := range harnessSecrets {
			p.ReadDeny = appendUnique(p.ReadDeny, filepath.Join(harnessHome, n))
		}
		// A folderless session's working directory is under HARNESS_HOME/chats.
		chats := filepath.Join(harnessHome, "chats")
		for _, r := range roots {
			if r != chats && within(r, chats) {
				p.WriteAllowAfterDeny = appendUnique(p.WriteAllowAfterDeny, r)
			}
		}
	}

	p.Write = roots
	for _, r := range roots {
		if r != systemTmp {
			p.Sockets = append(p.Sockets, r)
		}
	}
	return p
}

// systemTmp is /tmp as the kernel sees it: /private/tmp on macOS.
var systemTmp = canon("/tmp")

// gitDirs returns the git directories a folder works with: for the nearest .git at or above dir, the directory it names
// and the common directory of a linked worktree. No git command runs: this is about what a command could do to them.
func gitDirs(dir string) []string {
	for d := dir; d != "/" && d != "." && d != ""; d = filepath.Dir(d) {
		entry := filepath.Join(d, ".git")
		st, err := os.Stat(entry)
		if err != nil {
			continue
		}
		if st.IsDir() {
			return []string{entry}
		}
		data, err := os.ReadFile(entry)
		if err != nil {
			return nil
		}
		rest, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if !ok {
			return nil
		}
		g := strings.TrimSpace(rest)
		if !filepath.IsAbs(g) {
			g = filepath.Join(d, g)
		}
		g = canon(g)
		out := []string{g}
		if c, err := os.ReadFile(filepath.Join(g, "commondir")); err == nil {
			common := strings.TrimSpace(string(c))
			if !filepath.IsAbs(common) {
				common = filepath.Join(g, common)
			}
			out = append(out, canon(common))
		}
		return out
	}
	return nil
}

func appendUnique(list []string, items ...string) []string {
	for _, it := range items {
		if !slices.Contains(list, it) {
			list = append(list, it)
		}
	}
	return list
}

// canon resolves symlinks of the longest existing prefix of p, so a path that does not exist yet compares correctly too.
func canon(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	dir, base := filepath.Split(p)
	dir = filepath.Clean(dir)
	if dir == p || dir == "." || dir == "" {
		return p
	}
	return filepath.Join(canon(dir), base)
}

// within reports whether path is dir or below it.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

// Why says why the policy refuses an access to path ("" when nothing would): what a failure message can tell the model.
// write says which access to ask about.
func (p Policy) Why(path string, write bool) string {
	path = canon(path)
	for _, d := range p.ReadDeny {
		if within(path, d) {
			return "it is a credential store, which the sandbox keeps from every command"
		}
	}
	if !write {
		return ""
	}
	if p.Free != "" && within(path, p.Free) {
		if slices.Contains(p.WriteDenyLiteral, path) || slices.ContainsFunc(p.FreeDeny, func(d string) bool { return within(path, d) }) {
			return "it is shell state of this session that commands outside the sandbox use too"
		}
		return ""
	}
	if gitEntryRe.MatchString(path) {
		return "it is a .git entry: the sandbox lets a command use a repository, not replace or remove its git directory"
	}
	for _, r := range protectedFileRes {
		if r.MatchString(path) {
			return "it is a protected file (git hooks, agent settings, MCP servers), which no sandboxed command writes"
		}
	}
	if slices.Contains(p.WriteDenyLiteral, path) {
		return "it can make programs run outside the sandbox, so no sandboxed command writes it"
	}
	inside := func(list []string) bool {
		return slices.ContainsFunc(list, func(d string) bool { return within(path, d) })
	}
	if inside(p.WriteDeny) && !inside(p.WriteAllowAfterDeny) {
		return "it is a protected path (shell startup files, credentials, harness settings), which no sandboxed command writes"
	}
	if !inside(p.Write) {
		return "it is outside the writable paths (the working directories, the scratch and temp directories, toolchain caches, and those added in Settings)"
	}
	if agentDirsRe.MatchString(path) && !slices.ContainsFunc(p.Write, func(r string) bool {
		return within(path, r) && agentDirsRe.MatchString(r)
	}) && !strings.Contains(path, "/worktrees/") {
		return "it is in a .claude or .harness folder, which only the file tools write"
	}
	return ""
}
