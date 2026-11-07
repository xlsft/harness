//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"
)

// The sandbox's helper: the program that embeds this package, run again with stageArg. What applies between fork and exec has
// to be applied by a process, and what it applies stays through exec: Landlock's domain, the system call filter and
// no_new_privs belong to the thread that calls exec, which is this one. It does this in an init, since this package is a
// library and any binary may hold it, and it never returns: it becomes the command or exits. Nothing it can be asked removes a
// restriction, so calling it directly gains nothing.

func init() {
	if len(os.Args) < 3 || os.Args[1] != stageArg {
		return
	}
	runtime.LockOSThread()
	spec, err := decodeStageSpec(os.Args[2])
	if err != nil {
		stageFail(fmt.Errorf("the sandbox's helper was given something it cannot read: %w", err))
	}
	args := os.Args[3:]
	switch spec.Op {
	case stageExec:
		err = runStageExec(spec, args)
	case stageForward:
		err = runStageForward(spec)
	case stageProbeFilter:
		err = runProbeFilter(spec)
	case stageProbeFiles:
		err = runProbeFiles(spec, args)
	default:
		err = fmt.Errorf("the sandbox's helper does not know %q", spec.Op)
	}
	if err != nil {
		stageFail(err)
	}
	os.Exit(0)
}

// stageFail ends the helper without running anything: 126 is what a shell answers for a command that could not be executed.
func stageFail(err error) {
	fmt.Fprintf(os.Stderr, "harness: the command sandbox could not be applied, so the command did not run: %v\n", err)
	os.Exit(126)
}

func runStageExec(spec stageSpec, argv []string) error {
	if len(argv) == 0 {
		return errors.New("no command")
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	if spec.ProxySocket != "" {
		if err := startForwarder(spec); err != nil {
			return fmt.Errorf("no way to the network proxy: %w", err)
		}
	}
	if err := enforce(spec); err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ())
}

// enforce applies everything to this thread.
func enforce(spec stageSpec) error {
	if err := prctl(prSetNoNewPrivs, 1); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	abi, _ := landlockVersion()
	if spec.Backend == backendLandlock && abi == 0 {
		return errors.New("this kernel has no Landlock")
	}
	if err := applyLandlock(spec, abi); err != nil {
		return fmt.Errorf("landlock: %w", err)
	}
	if err := applyFilter(planFilter(spec, abi)); err != nil {
		return fmt.Errorf("seccomp: %w", err)
	}
	return nil
}

// startForwarder starts the process that listens on the proxy's port in this network namespace and answers once it does. It
// ends with the PID namespace, whose first process is the command.
func startForwarder(spec stageSpec) error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	spec.Op = stageForward
	cmd := exec.Command("/proc/self/exe", stageArg, spec.encode())
	cmd.ExtraFiles = []*os.File{w}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	_ = w.Close()
	if err != nil {
		return err
	}
	if _, err := io.ReadFull(r, make([]byte, 1)); err != nil {
		return errors.New("the forwarder did not start")
	}
	return nil
}

// runStageForward relays 127.0.0.1:<port> to the proxy's unix socket. It runs in the command's namespaces without the filter,
// which belongs to one thread and this is a Go program with many. It is not dumpable, and the filter refuses ptrace: a command
// cannot take it over.
func runStageForward(spec stageSpec) error {
	if err := prctl(prSetDumpable, 0); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(spec.ProxyPort)))
	if err != nil {
		return err
	}
	ready := os.NewFile(3, "ready")
	_, _ = ready.Write([]byte{1})
	_ = ready.Close()
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go relay(c, spec.ProxySocket)
	}
}

func relay(c net.Conn, socket string) {
	defer c.Close()
	up, err := net.Dial("unix", socket)
	if err != nil {
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	pump := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if t, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = t.CloseWrite()
		}
		done <- struct{}{}
	}
	go pump(up, c)
	go pump(c, up)
	<-done
	<-done
}

// runProbeFilter checks that the filter goes in and refuses what it should.
func runProbeFilter(spec stageSpec) error {
	if err := enforce(spec); err != nil {
		return err
	}
	if err := syscall.Unshare(syscall.CLONE_NEWUTS); !errors.Is(err, syscall.EPERM) {
		return fmt.Errorf("the filter was accepted and does not refuse: unshare gave %v", err)
	}
	return nil
}

// runProbeFiles checks that Landlock holds: a write in the allowed folder works, one next to it does not.
func runProbeFiles(spec stageSpec, args []string) error {
	if len(args) != 2 {
		return errors.New("a probe takes two paths")
	}
	if err := enforce(spec); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(args[0], "f"), nil, 0o600); err != nil {
		return fmt.Errorf("Landlock refused what it was told to allow: %w", err)
	}
	if err := os.WriteFile(args[1], nil, 0o600); err == nil {
		return errors.New("Landlock was accepted and does not refuse")
	}
	return nil
}

// System calls the standard library has no name for.
const (
	sysLandlockCreateRuleset = 444
	sysLandlockAddRule       = 445
	sysLandlockRestrictSelf  = 446

	llCreateRulesetVersion = 1
	llRulePathBeneath      = 1
	llRuleNetPort          = 2

	prSetDumpable     = 4
	prSetSeccomp      = 22
	prSetNoNewPrivs   = 38
	seccompModeFilter = 2
)

func prctl(option, arg uintptr) error {
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, option, arg, 0); e != 0 {
		return e
	}
	return nil
}

// landlockVersion returns the kernel's ABI version, or why there is none.
func landlockVersion() (int, string) {
	v, _, e := syscall.RawSyscall(sysLandlockCreateRuleset, 0, 0, llCreateRulesetVersion)
	switch {
	case e == 0:
		return int(v), ""
	case e == syscall.ENOSYS:
		return 0, "this kernel has none (5.13 or later is needed)"
	case e == syscall.EOPNOTSUPP:
		return 0, "the kernel was started without it (it is not in lsm=)"
	case e == syscall.EPERM:
		return 0, "this container's system call filter refuses it"
	}
	return 0, "the kernel answered " + e.Error()
}

// llRulesetAttr is struct landlock_ruleset_attr.
type llRulesetAttr struct {
	HandledFS  uint64
	HandledNet uint64
	Scoped     uint64
}

// applyLandlock builds the ruleset of a spec and applies it, if there is anything to apply.
func applyLandlock(spec stageSpec, abi int) error {
	if abi == 0 {
		return nil
	}
	var attr llRulesetAttr
	var files []llRule
	if spec.Backend == backendLandlock {
		files = planFiles(spec.policy(), abi, osFileView())
		attr.HandledFS = llSupportedFS(abi)
	}
	net := planNet(spec, abi)
	attr.HandledNet = net.Handled
	attr.Scoped = planScope(spec, abi)
	if attr == (llRulesetAttr{}) {
		return nil
	}
	fd, _, e := syscall.RawSyscall(sysLandlockCreateRuleset, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if e != 0 {
		return fmt.Errorf("create_ruleset: %w", e)
	}
	defer syscall.Close(int(fd))
	for _, r := range files {
		if err := addPathRule(int(fd), r); err != nil {
			return fmt.Errorf("%s: %w", r.Path, err)
		}
	}
	for _, span := range net.Connect {
		for port := span[0]; port <= span[1] && port > 0; port++ {
			if err := addPortRule(int(fd), llNetConnect, port); err != nil {
				return fmt.Errorf("port %d: %w", port, err)
			}
		}
	}
	if _, _, e := syscall.RawSyscall(sysLandlockRestrictSelf, fd, 0, 0); e != 0 {
		return fmt.Errorf("restrict_self: %w", e)
	}
	return nil
}

// addPathRule grants a rule on a path. A path that has gone is no longer anything to grant, and one this user cannot open
// gets no right at all, which is the safe side.
func addPathRule(ruleset int, r llRule) error {
	fd, err := syscall.Open(r.Path, unix.O_PATH|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ELOOP) {
			return nil
		}
		return err
	}
	defer syscall.Close(fd)
	// struct landlock_path_beneath_attr is packed: 12 bytes.
	var buf [12]byte
	*(*uint64)(unsafe.Pointer(&buf[0])) = r.Access
	*(*int32)(unsafe.Pointer(&buf[8])) = int32(fd)
	_, _, e := syscall.RawSyscall6(sysLandlockAddRule, uintptr(ruleset), llRulePathBeneath, uintptr(unsafe.Pointer(&buf[0])), 0, 0, 0)
	runtime.KeepAlive(&buf)
	if e != 0 {
		return e
	}
	return nil
}

func addPortRule(ruleset int, access uint64, port int) error {
	attr := [2]uint64{access, uint64(port)}
	_, _, e := syscall.RawSyscall6(sysLandlockAddRule, uintptr(ruleset), llRuleNetPort, uintptr(unsafe.Pointer(&attr)), 0, 0, 0)
	runtime.KeepAlive(&attr)
	if e != 0 {
		return e
	}
	return nil
}

func osFileView() fileView {
	return fileView{Kind: statKind, ReadDir: func(path string) []dirEntry {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil
		}
		out := make([]dirEntry, 0, len(entries))
		for _, e := range entries {
			kind := pathOther
			switch {
			case e.IsDir():
				kind = pathDir
			case e.Type().IsRegular():
				kind = pathFile
			}
			out = append(out, dirEntry{Name: e.Name(), Kind: kind})
		}
		return out
	}}
}

// sockFprog is struct sock_fprog.
type sockFprog struct {
	Len    uint16
	Filter *sockFilter
}

func applyFilter(o filterOptions) error {
	prog, err := seccompProgram(runtime.GOARCH, o)
	if err != nil {
		return err
	}
	fprog := sockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	_, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, prSetSeccomp, seccompModeFilter, uintptr(unsafe.Pointer(&fprog)))
	runtime.KeepAlive(&fprog)
	runtime.KeepAlive(prog)
	if e != 0 {
		return e
	}
	return nil
}
