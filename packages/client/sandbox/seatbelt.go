package sandbox

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The Seatbelt profile language is a Scheme of allow and deny rules in which the last matching rule wins, so the order
// below is the policy: the writable roots first, then every protection after them. It starts from (deny default)
// because what a sandboxed process can ask of the system's services matters as much as its files: launchd, LaunchServices
// (open) and Apple events (osascript) each start something outside the sandbox, and securityd hands out keychain items.
// Those are closed by not being on the list of Mach services.

// machServices are the only system services a sandboxed process may look up. Each is here because a toolchain command
// needs it (seatbelt_darwin_test.go runs them); keep this list as short as they allow.
var machServices = []string{
	"com.apple.system.opendirectoryd.libinfo",    // getpwuid and friends: git, ssh, every tool that asks who it runs as
	"com.apple.system.opendirectoryd.membership", // group lookups
	"com.apple.system.notification_center",       // libnotify, used by libSystem itself
	"com.apple.system.logger",                    // asl, which older tools log to
	"com.apple.logd",                             // os_log
	"com.apple.diagnosticd",                      // os_log
	"com.apple.trustd.agent",                     // TLS certificate checks of Go, git and curl
	"com.apple.SystemConfiguration.configd",      // the network configuration libraries read at start
	"com.apple.FSEvents",                         // file watchers (vite, next, test runners)
	"com.apple.bsd.dirhelper",                    // the per-user temp and cache directories (confstr)
	"com.apple.cfprefsd.daemon",                  // user defaults, read by xcrun and CoreFoundation at start
	"com.apple.cfprefsd.agent",                   // user defaults
	"com.apple.CoreServices.coreservicesd",       // CoreServices file APIs (xcrun, clang)
	"com.apple.sysmond",                          // the process list of pgrep and pkill (ps is setuid: it cannot run in a sandbox)
}

// ioctlDevices are the devices terminals and tools issue ioctls on.
var ioctlDevices = []string{"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom", "/dev/tty", "/dev/ptmx", "/dev/dtracehelper"}

// SeatbeltProfile renders p. It is a pure function of the policy: no file system, no environment.
func SeatbeltProfile(p Policy) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w("(version 1)")
	w("(deny default)")
	w("")
	w("; Processes and the plain system calls every program makes.")
	w("(allow process-exec*)")
	w("(allow process-fork)")
	w("(allow process-info*)")
	w("(allow signal (target same-sandbox))")
	w("(allow sysctl-read)")
	w("(allow ipc-posix-sem)")
	w("(allow ipc-posix-shm*)")
	w("(allow ipc-sysv*)")
	w("(allow pseudo-tty)")
	w("(allow user-preference-read)")
	w("(allow file-map-executable)")
	w("(allow iokit-open (iokit-user-client-class \"RootDomainUserClient\"))")
	w("(allow system-socket)")
	w("")
	w("; System services: only these (see machServices).")
	w("(allow mach-lookup")
	for _, s := range machServices {
		w("  (global-name %s)", seatbeltString(s))
	}
	w(")")
	w("")
	w("; Devices.")
	w("(allow file-ioctl")
	for _, d := range ioctlDevices {
		w("  (literal %s)", seatbeltString(d))
	}
	w("  (regex #\"^/dev/ttys[0-9]+$\"))")
	w("(allow file-write*")
	w("  (literal \"/dev/null\") (literal \"/dev/zero\") (literal \"/dev/tty\") (literal \"/dev/ptmx\") (literal \"/dev/dtracehelper\")")
	w("  (regex #\"^/dev/ttys[0-9]+$\") (regex #\"^/dev/fd/[0-9]+$\"))")
	w("")
	w("; Reads are open, except the credential stores further down.")
	w("(allow file-read*)")
	w("")
	w("; The writable roots.")
	for _, r := range p.Write {
		w("(allow file-write* (subpath %s))", seatbeltString(r))
	}
	if len(p.Mkdir) > 0 {
		w("; The folders above a toolchain cache, on a machine that has never used the tool. Folders only: nothing in them.")
		for _, d := range p.Mkdir {
			w("(allow file-write-create (require-all (literal %s) (vnode-type DIRECTORY)))", seatbeltString(d))
		}
	}
	writeWriteDenies(w, p)
	writeReadDenies(w, p)
	writeLinks(w, p)
	writeNetwork(w, p)
	profile := b.String()
	if p.Tag != "" {
		// The kernel logs a refusal with this message: how a failure the output does not explain finds its cause.
		profile = denyRule.ReplaceAllString(profile, `(deny $1 (with message `+seatbeltString(p.Tag)+`)`)
	}
	return profile
}

var denyRule = regexp.MustCompile(`(?m)^\(deny ([a-z*-]+)`)

// writeWriteDenies renders the write protections. They come after the roots, and in the order that keeps a session whose
// own working directory is inside a protected tree (a worktree under .harness/worktrees, a folderless chat under
// HARNESS_HOME) working, while its settings files stay protected.
func writeWriteDenies(w func(string, ...any), p Policy) {
	w("")
	w("; .claude and .harness, at any depth: what the model finds there drives the agent itself.")
	w("(deny file-write* (regex %s))", seatbeltRegex(agentDirsPattern))
	for _, r := range p.Write {
		if agentDirsRe.MatchString(r) {
			w("(allow file-write* (subpath %s))", seatbeltString(r))
		}
		w("(allow file-write* (regex %s))", seatbeltRegex("^"+regexp.QuoteMeta(r)+`(/.*)?/\.(claude|harness)/worktrees(/|$)`))
	}
	w("")
	w("; The protected paths, whatever the roots say.")
	for _, d := range p.WriteDeny {
		w("(deny file-write* (subpath %s))", seatbeltString(d))
	}
	for _, r := range p.WriteAllowAfterDeny {
		w("(allow file-write* (subpath %s))", seatbeltString(r))
	}
	w("")
	w("; Files in any folder that run or configure something outside the sandbox.")
	for _, re := range protectedFilePatterns {
		w("(deny file-write* (regex %s))", seatbeltRegex(re))
	}
	w("; A .git file redirects git to another directory, and renaming a .git directory away, editing it and renaming it")
	w("; back would pass every rule on paths below: the name itself stays put once it holds something.")
	w("; A new .git directory is allowed: that is git init or git clone in a subfolder, and a repository the command made.")
	w("(deny file-write-data (regex %s))", seatbeltRegex(gitEntryPattern))
	w("(deny file-write-unlink (regex %s))", seatbeltRegex(gitEntryPattern))
	w("(deny file-write-create (require-all (regex %s) (vnode-type REGULAR-FILE SYMLINK)))", seatbeltRegex(gitEntryPattern))
	if p.Free != "" {
		w("")
		w("; The command's own temp tree holds no one else's files: what it does there, tests that make and remove a repository")
		w("; or a settings file, leaves nothing the user runs.")
		w("(allow file-write* (subpath %s))", seatbeltString(p.Free))
		// A rule on one operation outranks a rule on file-write*, whatever their order, so the operations named above are
		// named here too, and the exceptions after them.
		for _, op := range []string{"file-write-create", "file-write-data", "file-write-unlink"} {
			w("(allow %s (subpath %s))", op, seatbeltString(p.Free))
			for _, d := range p.FreeDeny {
				w("(deny %s (subpath %s))", op, seatbeltString(d))
			}
			for _, l := range p.WriteDenyLiteral {
				if within(l, p.Free) {
					w("(deny %s (literal %s))", op, seatbeltString(l))
				}
			}
		}
		for _, d := range p.FreeDeny {
			w("(deny file-write* (subpath %s))", seatbeltString(d))
		}
	}
	if len(p.WriteDenyLiteral) > 0 {
		w("(deny file-write*")
		for _, l := range p.WriteDenyLiteral {
			w("  (literal %s)", seatbeltString(l))
		}
		w(")")
	}
	if a := p.protectedAncestors(); len(a) > 0 {
		w("; Renaming a directory above a protected path moves the path out from under its rule.")
		w("(deny file-write-unlink")
		for _, l := range a {
			w("  (literal %s)", seatbeltString(l))
		}
		w(")")
	}
}

func writeReadDenies(w func(string, ...any), p Policy) {
	w("")
	w("; Credential stores. Names stay visible (a stat works, as for any file): ls and path resolution keep working.")
	for _, d := range p.ReadDeny {
		w("(deny file-read-data (subpath %s))", seatbeltString(d))
		w("(deny file-read-xattr (subpath %s))", seatbeltString(d))
	}
}

// writeLinks allows hard links only inside the writable roots and never to a protected file: a link to one inside a root
// would be the same file under a name no rule mentions, for reading and for writing. Build tools link all the time (cargo,
// pnpm from its store), inside their own trees.
func writeLinks(w func(string, ...any), p Policy) {
	w("")
	w("; Hard links, see writeLinks.")
	w("(allow file-link")
	for _, r := range p.Write {
		w("  (subpath %s)", seatbeltString(r))
	}
	w(")")
	for _, d := range p.WriteDeny {
		w("(deny file-link (subpath %s))", seatbeltString(d))
	}
	for _, d := range p.ReadDeny {
		w("(deny file-link (subpath %s))", seatbeltString(d))
	}
	for _, re := range protectedFilePatterns {
		w("(deny file-link (regex %s))", seatbeltRegex(re))
	}
}

func writeNetwork(w func(string, ...any), p Policy) {
	w("")
	w("; Network: the proxy, nothing else. Everything else, DNS included, is refused by (deny default).")
	if p.ProxyPort > 0 {
		w("(allow network-outbound (remote ip \"localhost:%d\"))", p.ProxyPort)
	}
	if p.Localhost {
		w("; Local servers of the command itself (tests), and whatever else listens on this machine's loopback.")
		w("(allow network-bind (local ip \"localhost:*\"))")
		w("(allow network-inbound (local ip \"localhost:*\"))")
		w("(allow network-outbound (remote ip \"localhost:*\"))")
	}
	if p.Server {
		w("; A dev server listens where it was told to.")
		w("(allow network-bind (local ip \"*:*\"))")
		w("(allow network-inbound (local ip \"*:*\"))")
		w("(allow network-outbound (remote ip \"localhost:*\"))")
	}
	if len(p.Sockets) > 0 {
		w("; Unix sockets a command makes for itself; no other socket on this machine is reachable.")
		for _, s := range p.Sockets {
			w("(allow network-bind (local unix-socket (subpath %s)))", seatbeltString(s))
			w("(allow network-inbound (local unix-socket (subpath %s)))", seatbeltString(s))
			w("(allow network-outbound (remote unix-socket (subpath %s)))", seatbeltString(s))
		}
	}
}

// protectedAncestors lists the directories above every protected path, which must not be renamed (the root excluded).
func (p Policy) protectedAncestors() []string {
	var out []string
	for _, list := range [][]string{p.WriteDeny, p.ReadDeny, p.WriteDenyLiteral} {
		for _, path := range list {
			for dir := parent(path); dir != "/" && dir != ""; dir = parent(dir) {
				if !slices.Contains(out, dir) {
					out = append(out, dir)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

func parent(p string) string {
	i := strings.LastIndexByte(p, '/')
	switch {
	case i < 0:
		return ""
	case i == 0:
		return "/"
	}
	return p[:i]
}

// seatbeltString quotes s as a profile string.
func seatbeltString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// seatbeltRegex quotes a POSIX extended regular expression. The #"..." form takes no escapes of its own but cannot hold a
// double quote, which a path may; the string form covers that.
func seatbeltRegex(re string) string {
	if strings.Contains(re, `"`) {
		return seatbeltString(re)
	}
	return `#"` + re + `"`
}
