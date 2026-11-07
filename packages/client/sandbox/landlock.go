package sandbox

import (
	"path/filepath"
	"slices"
)

// The Landlock rules of a policy, as pure functions over a view of the file system. Landlock only allows: it refuses
// everything of the kinds it handles unless a rule grants it, for a path and everything below, and a rule cannot be taken
// back further down. So "read everything but the credential stores" is written out as every path around them, and the
// protected paths inside a writable root are left to the guard (guard.go).

// Landlock's file rights.
const (
	llExecute    = 1 << 0
	llWriteFile  = 1 << 1
	llReadFile   = 1 << 2
	llReadDir    = 1 << 3
	llRemoveDir  = 1 << 4
	llRemoveFile = 1 << 5
	llMakeChar   = 1 << 6
	llMakeDir    = 1 << 7
	llMakeReg    = 1 << 8
	llMakeSock   = 1 << 9
	llMakeFifo   = 1 << 10
	llMakeBlock  = 1 << 11
	llMakeSym    = 1 << 12
	llRefer      = 1 << 13 // ABI 2
	llTruncate   = 1 << 14 // ABI 3
)

// Landlock's network rights (ABI 4) and scopes (ABI 6).
const (
	llNetBind    = 1 << 0
	llNetConnect = 1 << 1

	llScopeAbstractUnix = 1 << 0
	llScopeSignal       = 1 << 1
)

const (
	llReadRights  = llExecute | llReadFile | llReadDir
	llWriteRights = llWriteFile | llRemoveDir | llRemoveFile | llMakeDir | llMakeReg | llMakeSock | llMakeFifo | llMakeSym | llRefer | llTruncate
	// llFileOnly can be granted on a file; the others only make sense on a folder, and the kernel refuses them there.
	llFileOnly = llExecute | llWriteFile | llReadFile | llTruncate
	// llHandledFS is what a ruleset refuses unless granted. Devices are in it so that none can be made.
	llHandledFS = llReadRights | llWriteRights | llMakeChar | llMakeBlock
)

// llSupportedFS returns the file rights of an ABI version: a right the kernel does not know makes it refuse the call.
func llSupportedFS(abi int) uint64 {
	var all uint64 = llHandledFS
	if abi < 3 {
		all &^= llTruncate
	}
	if abi < 2 {
		all &^= llRefer
	}
	return all
}

// pathKind is what is at a path.
type pathKind int

const (
	pathMissing pathKind = iota
	pathFile
	pathDir
	pathOther
)

// dirEntry is one name in a folder.
type dirEntry struct {
	Name string
	Kind pathKind
}

// fileView is how the planning functions see the file system: without following a last symlink, which has no rights of its own.
type fileView struct {
	Kind    func(path string) pathKind
	ReadDir func(path string) []dirEntry
}

// llRule grants rights on a path and, for a folder, everything below.
type llRule struct {
	Path   string
	Access uint64
	Dir    bool
}

// llDevices are written by ordinary programs: the rest of /dev stays read-only.
var llDevices = []string{"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom", "/dev/tty", "/dev/ptmx", "/dev/pts", "/dev/shm"}

// planFiles returns the rules of a policy for an ABI version.
func planFiles(p Policy, abi int, v fileView) []llRule {
	mask := llSupportedFS(abi)
	var denied []string
	for _, d := range p.ReadDeny {
		if v.Kind(d) != pathMissing {
			denied = append(denied, d)
		}
	}
	var rules []llRule
	add := func(path string, access uint64, dir bool) {
		if !dir {
			access &= llFileOnly
		}
		rules = append(rules, llRule{Path: path, Access: access & mask, Dir: dir})
	}

	var walk func(dir string)
	walk = func(dir string) {
		if slices.Contains(denied, dir) {
			return
		}
		if !slices.ContainsFunc(denied, func(d string) bool { return within(d, dir) }) {
			add(dir, llReadRights, true)
			return
		}
		// A store is below: names can be listed here, and the rest is taken one by one.
		add(dir, llReadDir, true)
		for _, e := range v.ReadDir(dir) {
			child := filepath.Join(dir, e.Name)
			switch {
			case slices.Contains(denied, child):
			case e.Kind == pathDir:
				walk(child)
			case e.Kind == pathFile:
				add(child, llExecute|llReadFile, false)
			}
			// A symlink resolves to a path that has its own rule. Anything else here (a socket) needs no reading.
		}
	}
	walk("/")

	for _, r := range writableRoots(p) {
		switch v.Kind(r) {
		case pathDir:
			add(r, llWriteRights, true)
		case pathFile:
			add(r, llWriteRights, false)
		}
	}
	for _, d := range llDevices {
		switch v.Kind(d) {
		case pathDir:
			add(d, llWriteRights, true)
		case pathFile, pathOther:
			add(d, llWriteFile, false)
		}
	}
	return rules
}

// writableRoots are the roots a rule can be given to. A root inside a protected path stays out, since nothing could take
// back what a rule grants; the exceptions the policy names (a folderless session's folder, the scratch directory) stay in.
func writableRoots(p Policy) []string {
	var out []string
	for _, r := range p.Write {
		inside := func(list []string) bool {
			return slices.ContainsFunc(list, func(d string) bool { return within(r, d) })
		}
		free := p.Free != "" && within(r, p.Free)
		if inside(p.ReadDeny) || (inside(p.WriteDeny) && !inside(p.WriteAllowAfterDeny) && !free) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// netPlan is the TCP part of a ruleset.
type netPlan struct {
	// Handled are the rights the ruleset refuses unless granted.
	Handled uint64
	// Connect are the ranges of ports connect may reach, Handled has llNetConnect.
	Connect [][2]int
}

// lowestOpenPort is where the ports a test's own server can get begin. Lower ones are where the traffic to stop is: 22, 25,
// 80, 443.
const lowestOpenPort = 1024

// planNet returns the TCP rules, which name a port and no address. A command in a network namespace of its own has nothing to
// reach but needs them for allowLocalhost off; one sharing the machine's network has nothing else. docs/design/platforms.md
// says what that leaves open.
func planNet(s stageSpec, abi int) netPlan {
	if abi < 4 {
		return netPlan{}
	}
	proxy := [2]int{s.ProxyPort, s.ProxyPort}
	open := s.Localhost || s.Server
	if s.PrivateNetwork {
		if open {
			return netPlan{}
		}
		return netPlan{Handled: llNetBind | llNetConnect, Connect: [][2]int{proxy}}
	}
	if open {
		return netPlan{Handled: llNetConnect, Connect: [][2]int{proxy, {lowestOpenPort, 65535}}}
	}
	return netPlan{Handled: llNetBind | llNetConnect, Connect: [][2]int{proxy}}
}

// planFilter returns the options of the system call filter.
func planFilter(s stageSpec, abi int) filterOptions {
	landlock := s.Backend == backendLandlock
	return filterOptions{
		DenyInet:     landlock && abi < 4,
		TCPOnly:      !s.PrivateNetwork,
		DenyUnix:     landlock,
		DenyTruncate: landlock && abi < 3,
	}
}

// planScope returns the scopes of a ruleset: what a process sharing the machine's PID and network namespaces should not reach.
func planScope(s stageSpec, abi int) uint64 {
	if abi < 6 || s.PrivateNetwork {
		return 0
	}
	if s.Backend == backendLandlock {
		return llScopeAbstractUnix | llScopeSignal
	}
	return llScopeAbstractUnix
}
