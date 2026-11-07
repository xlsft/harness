package sandbox

import "fmt"

// The system call filter of the Linux driver, as a pure function: seccomp_test.go runs it in a small interpreter on any
// machine. It allows by default and refuses a list: a list of what to allow breaks with every new kernel and libc, and the
// rest of the policy is not its job. See "Decisions" in docs/design/platforms.md.

// sockFilter is struct sock_filter: one classic BPF instruction.
type sockFilter struct {
	Code uint16
	Jt   uint8
	Jf   uint8
	K    uint32
}

// The BPF opcodes used here.
const (
	bpfLdAbsW = 0x20 // BPF_LD | BPF_W | BPF_ABS
	bpfAndK   = 0x54 // BPF_ALU | BPF_AND | BPF_K
	bpfJeqK   = 0x15 // BPF_JMP | BPF_JEQ | BPF_K
	bpfJgeK   = 0x35 // BPF_JMP | BPF_JGE | BPF_K
	bpfJsetK  = 0x45 // BPF_JMP | BPF_JSET | BPF_K
	bpfRetK   = 0x06 // BPF_RET | BPF_K
)

// The offsets in struct seccomp_data, and the actions. Both architectures are little-endian: an argument's low word comes
// first, and the calls filtered by argument take an int.
const (
	seccompNr   = 0
	seccompArch = 4
	seccompArg0 = 16

	seccompRetAllow       = 0x7fff0000
	seccompRetErrno       = 0x00050000
	seccompRetKillProcess = 0x80000000
)

const (
	errnoEPERM  = 1
	errnoENOSYS = 38
)

// Socket families and types.
const (
	afUnix    = 1
	afInet    = 2
	afInet6   = 10
	afNetlink = 16

	sockStream   = 1
	sockTypeMask = 0xf

	ipprotoIP  = 0
	ipprotoTCP = 6
)

// cloneNamespaces are CLONE_NEWNS, NEWCGROUP, NEWUTS, NEWIPC, NEWUSER, NEWPID and NEWNET.
const cloneNamespaces = 0x00020000 | 0x02000000 | 0x04000000 | 0x08000000 | 0x10000000 | 0x20000000 | 0x40000000

// tiocsti pushes bytes into a terminal's input: a command could type at the shell it was started from.
const tiocsti = 0x5412

// seccompArchInfo is what the filter needs to know of an architecture.
type seccompArchInfo struct {
	audit uint32
	// x32: the numbers of this ABI have this bit set and share the architecture value.
	x32 bool
	// calls by name.
	calls map[string]uint32
}

// seccompArchs has amd64 and arm64, the ones CI builds. The numbers are the kernel's, and fixed.
var seccompArchs = map[string]seccompArchInfo{
	"amd64": {audit: 0xc000003e, x32: true, calls: map[string]uint32{
		"socket": 41, "clone": 56, "clone3": 435, "ioctl": 16, "truncate": 76,
		"ptrace": 101, "process_vm_readv": 310, "process_vm_writev": 311, "pidfd_getfd": 438,
		"keyctl": 250, "add_key": 248, "request_key": 249,
		"bpf": 321, "perf_event_open": 298, "userfaultfd": 323,
		"io_uring_setup": 425, "io_uring_enter": 426, "io_uring_register": 427,
		"mount": 165, "umount2": 166, "pivot_root": 155, "chroot": 161, "unshare": 272, "setns": 308,
		"open_tree": 428, "move_mount": 429, "fsopen": 430, "fsconfig": 431, "fsmount": 432, "fspick": 433, "mount_setattr": 442,
		"kexec_load": 246, "kexec_file_load": 320, "init_module": 175, "finit_module": 313, "delete_module": 176,
		"swapon": 167, "swapoff": 168, "reboot": 169, "name_to_handle_at": 303, "open_by_handle_at": 304,
	}},
	"arm64": {audit: 0xc00000b7, calls: map[string]uint32{
		"socket": 198, "clone": 220, "clone3": 435, "ioctl": 29, "truncate": 45,
		"ptrace": 117, "process_vm_readv": 270, "process_vm_writev": 271, "pidfd_getfd": 438,
		"keyctl": 219, "add_key": 217, "request_key": 218,
		"bpf": 280, "perf_event_open": 241, "userfaultfd": 282,
		"io_uring_setup": 425, "io_uring_enter": 426, "io_uring_register": 427,
		"mount": 40, "umount2": 39, "pivot_root": 41, "chroot": 51, "unshare": 97, "setns": 268,
		"open_tree": 428, "move_mount": 429, "fsopen": 430, "fsconfig": 431, "fsmount": 432, "fspick": 433, "mount_setattr": 442,
		"kexec_load": 104, "kexec_file_load": 294, "init_module": 105, "finit_module": 273, "delete_module": 106,
		"swapon": 224, "swapoff": 225, "reboot": 142, "name_to_handle_at": 264, "open_by_handle_at": 265,
	}},
}

// seccompRefused are the calls refused outright, with EPERM, so that what fails says "Operation not permitted".
var seccompRefused = []string{
	// Out of the sandbox's view of the machine: namespaces and mounts. Inside bwrap's they would be the command's own, but
	// they are no use to a build, and Landlock stops at them only in part.
	"unshare", "setns", "mount", "umount2", "pivot_root", "chroot", "open_tree", "move_mount", "fsopen", "fsconfig", "fsmount", "fspick", "mount_setattr",
	// Other processes.
	"ptrace", "process_vm_readv", "process_vm_writev", "pidfd_getfd",
	// The kernel keyring holds credentials, as the keychain does on macOS.
	"keyctl", "add_key", "request_key",
	// io_uring's operations do not pass this filter: a socket made through it would not be seen.
	"io_uring_setup", "io_uring_enter", "io_uring_register",
	// The kernel's attack surface, and what root in a container could otherwise do.
	"bpf", "perf_event_open", "userfaultfd", "kexec_load", "kexec_file_load", "init_module", "finit_module", "delete_module",
	"swapon", "swapoff", "reboot", "name_to_handle_at", "open_by_handle_at",
}

// filterOptions is what differs between commands.
type filterOptions struct {
	// DenyInet refuses internet sockets altogether: a kernel without Landlock's TCP rules, sharing the machine's network.
	DenyInet bool
	// TCPOnly allows only TCP among internet sockets: Landlock's rules see nothing else, and UDP or raw sockets would pass.
	TCPOnly bool
	// DenyUnix refuses making unix sockets: without a mount namespace, every socket on the machine would be reachable.
	DenyUnix bool
	// DenyTruncate refuses truncate(2): Landlock only handles it from ABI 3, and a path needs no open file to be cut.
	DenyTruncate bool
}

// seccompProgram builds the filter for an architecture.
func seccompProgram(arch string, o filterOptions) ([]sockFilter, error) {
	info, ok := seccompArchs[arch]
	if !ok {
		return nil, fmt.Errorf("no system call table for %s", arch)
	}
	var prog []sockFilter
	emit := func(code uint16, jt, jf uint8, k uint32) { prog = append(prog, sockFilter{code, jt, jf, k}) }
	load := func(offset uint32) { emit(bpfLdAbsW, 0, 0, offset) }
	eperm := seccompRetErrno | errnoEPERM

	// Another architecture has other numbers, so nothing below would mean what it says.
	load(seccompArch)
	emit(bpfJeqK, 1, 0, info.audit)
	emit(bpfRetK, 0, 0, seccompRetKillProcess)
	load(seccompNr)
	if info.x32 {
		emit(bpfJgeK, 0, 1, 0x40000000)
		emit(bpfRetK, 0, 0, seccompRetKillProcess)
	}

	refused := append([]string(nil), seccompRefused...)
	if o.DenyTruncate {
		refused = append(refused, "truncate")
	}
	for _, name := range refused {
		emit(bpfJeqK, 0, 1, info.calls[name])
		emit(bpfRetK, 0, 0, uint32(eperm))
	}
	// clone3 keeps its flags in memory the filter cannot read. ENOSYS sends libc back to clone.
	emit(bpfJeqK, 0, 1, info.calls["clone3"])
	emit(bpfRetK, 0, 0, seccompRetErrno|errnoENOSYS)

	// Loading an argument replaces the number, so a block that loads one ends in returns, and any other call jumps over it.

	// clone: everything but new namespaces.
	emit(bpfJeqK, 0, 4, info.calls["clone"])
	load(seccompArg0)
	emit(bpfJsetK, 0, 1, cloneNamespaces)
	emit(bpfRetK, 0, 0, uint32(eperm))
	emit(bpfRetK, 0, 0, seccompRetAllow)

	// ioctl: everything but TIOCSTI.
	emit(bpfJeqK, 0, 4, info.calls["ioctl"])
	load(seccompArg0 + 8)
	emit(bpfJeqK, 0, 1, tiocsti)
	emit(bpfRetK, 0, 0, uint32(eperm))
	emit(bpfRetK, 0, 0, seccompRetAllow)

	// socket: the families a command may make, then what it may do with an internet one.
	emit(bpfJeqK, 1, 0, info.calls["socket"])
	emit(bpfRetK, 0, 0, seccompRetAllow)
	prog = append(prog, socketRules(o)...)
	return prog, nil
}

// socketRules are the end of the program, for a socket call: every path returns.
func socketRules(o filterOptions) []sockFilter {
	eperm := uint32(seccompRetErrno | errnoEPERM)
	var p []sockFilter
	emit := func(code uint16, jt, jf uint8, k uint32) { p = append(p, sockFilter{code, jt, jf, k}) }
	load := func(offset uint32) { emit(bpfLdAbsW, 0, 0, offset) }

	load(seccompArg0)
	// Netlink stays: libc asks it for the interfaces when it resolves a name, and it changes nothing without privileges.
	emit(bpfJeqK, 0, 1, afNetlink)
	emit(bpfRetK, 0, 0, seccompRetAllow)
	if !o.DenyUnix {
		emit(bpfJeqK, 0, 1, afUnix)
		emit(bpfRetK, 0, 0, seccompRetAllow)
	}
	if o.DenyInet {
		emit(bpfRetK, 0, 0, eperm)
		return p
	}
	// What is left and not internet is refused: packet sockets, vsock (a way to the host of a VM), and the rest.
	emit(bpfJeqK, 2, 0, afInet)
	emit(bpfJeqK, 1, 0, afInet6)
	emit(bpfRetK, 0, 0, eperm)
	if !o.TCPOnly {
		emit(bpfRetK, 0, 0, seccompRetAllow)
		return p
	}
	load(seccompArg0 + 8)
	emit(bpfAndK, 0, 0, sockTypeMask)
	emit(bpfJeqK, 1, 0, sockStream)
	emit(bpfRetK, 0, 0, eperm)
	// SCTP and MPTCP are stream sockets too, and not TCP to Landlock.
	load(seccompArg0 + 16)
	emit(bpfJeqK, 2, 0, ipprotoIP)
	emit(bpfJeqK, 1, 0, ipprotoTCP)
	emit(bpfRetK, 0, 0, eperm)
	emit(bpfRetK, 0, 0, seccompRetAllow)
	return p
}
