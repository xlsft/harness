package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// These test what the Linux driver decides, which is pure and runs on any machine. linux_test.go runs the real thing.

// bpfRun is a classic BPF interpreter for the instructions the filter uses, over a struct seccomp_data.
func bpfRun(t *testing.T, prog []sockFilter, arch, nr uint32, args ...uint32) uint32 {
	t.Helper()
	word := func(off uint32) uint32 {
		switch {
		case off == seccompNr:
			return nr
		case off == seccompArch:
			return arch
		case off >= seccompArg0 && (off-seccompArg0)%8 == 0:
			if i := int(off-seccompArg0) / 8; i < len(args) {
				return args[i]
			}
			return 0
		}
		t.Fatalf("load at %d", off)
		return 0
	}
	var a uint32
	for pc := 0; pc < len(prog); pc++ {
		in := prog[pc]
		switch in.Code {
		case bpfLdAbsW:
			a = word(in.K)
		case bpfAndK:
			a &= in.K
		case bpfRetK:
			return in.K
		case bpfJeqK, bpfJgeK, bpfJsetK:
			taken := a == in.K
			if in.Code == bpfJgeK {
				taken = a >= in.K
			} else if in.Code == bpfJsetK {
				taken = a&in.K != 0
			}
			if taken {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		default:
			t.Fatalf("unknown instruction %#x at %d", in.Code, pc)
		}
	}
	t.Fatal("the program ran off its end")
	return 0
}

func TestSeccompProgram(t *testing.T) {
	const (
		allow = seccompRetAllow
		eperm = seccompRetErrno | errnoEPERM
	)
	for arch, info := range seccompArchs {
		t.Run(arch, func(t *testing.T) {
			prog, err := seccompProgram(arch, filterOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(prog) > 1024 {
				t.Fatalf("too long for the kernel: %d", len(prog))
			}
			for name, nr := range info.calls {
				got := bpfRun(t, prog, info.audit, nr, 0, 0, 0)
				switch {
				case slices.Contains(seccompRefused, name):
					if got != eperm {
						t.Errorf("%s: %#x", name, got)
					}
				case name == "clone3":
					if got != seccompRetErrno|errnoENOSYS {
						t.Errorf("clone3: %#x", got)
					}
				case name == "socket":
				default:
					if got != allow {
						t.Errorf("%s: %#x", name, got)
					}
				}
			}
			// Everything else: read(2) on neither table.
			if got := bpfRun(t, prog, info.audit, 1000); got != allow {
				t.Errorf("an unlisted call: %#x", got)
			}
			// Another architecture is killed, whatever the number.
			if got := bpfRun(t, prog, 0x40000003, 1); got != seccompRetKillProcess {
				t.Errorf("another architecture: %#x", got)
			}
			if info.x32 {
				if got := bpfRun(t, prog, info.audit, 0x40000000|info.calls["mount"]); got != seccompRetKillProcess {
					t.Errorf("x32: %#x", got)
				}
			}
			clone := info.calls["clone"]
			if got := bpfRun(t, prog, info.audit, clone, 0x00010000); got != allow { // CLONE_THREAD
				t.Errorf("a thread: %#x", got)
			}
			if got := bpfRun(t, prog, info.audit, clone, 0x10000000); got != eperm { // CLONE_NEWUSER
				t.Errorf("a new user namespace: %#x", got)
			}
			ioctl := info.calls["ioctl"]
			if got := bpfRun(t, prog, info.audit, ioctl, 1, tiocsti); got != eperm {
				t.Errorf("TIOCSTI: %#x", got)
			}
			if got := bpfRun(t, prog, info.audit, ioctl, 1, 0x5401); got != allow {
				t.Errorf("TCGETS: %#x", got)
			}
			if got := bpfRun(t, prog, info.audit, info.calls["truncate"]); got != allow {
				t.Errorf("truncate: %#x", got)
			}
			deny, _ := seccompProgram(arch, filterOptions{DenyTruncate: true})
			if got := bpfRun(t, deny, info.audit, info.calls["truncate"]); got != eperm {
				t.Errorf("truncate refused: %#x", got)
			}

			socket := func(o filterOptions, family, typ, proto uint32) uint32 {
				t.Helper()
				p, err := seccompProgram(arch, o)
				if err != nil {
					t.Fatal(err)
				}
				return bpfRun(t, p, info.audit, info.calls["socket"], family, typ, proto)
			}
			const (
				sockDgram   = 2
				sockRaw     = 3
				sockCloexec = 0o2000000
				afPacket    = 17
				afVsock     = 40
				sctp        = 132
			)
			cases := []struct {
				name               string
				o                  filterOptions
				family, typ, proto uint32
				want               uint32
			}{
				{"private network, tcp", filterOptions{}, afInet, sockStream, 0, allow},
				{"private network, udp", filterOptions{}, afInet6, sockDgram, 0, allow},
				{"private network, unix", filterOptions{}, afUnix, sockStream, 0, allow},
				{"never a packet socket", filterOptions{}, afPacket, sockRaw, 0, eperm},
				{"never vsock", filterOptions{}, afVsock, sockStream, 0, eperm},
				{"netlink", filterOptions{DenyUnix: true, DenyInet: true}, afNetlink, sockRaw, 0, allow},
				{"tcp", filterOptions{TCPOnly: true}, afInet, sockStream, 0, allow},
				{"tcp, flags", filterOptions{TCPOnly: true}, afInet6, sockStream | sockCloexec, ipprotoTCP, allow},
				{"no udp", filterOptions{TCPOnly: true}, afInet, sockDgram | sockCloexec, 0, eperm},
				{"no raw", filterOptions{TCPOnly: true}, afInet, sockRaw, 1, eperm},
				{"no sctp", filterOptions{TCPOnly: true}, afInet, sockStream, sctp, eperm},
				{"unix refused", filterOptions{DenyUnix: true, TCPOnly: true}, afUnix, sockStream, 0, eperm},
				{"inet refused", filterOptions{DenyUnix: true, DenyInet: true, TCPOnly: true}, afInet, sockStream, 0, eperm},
				{"inet6 refused", filterOptions{DenyUnix: true, DenyInet: true, TCPOnly: true}, afInet6, sockStream, 0, eperm},
			}
			for _, c := range cases {
				if got := socket(c.o, c.family, c.typ, c.proto); got != c.want {
					t.Errorf("%s: %#x, want %#x", c.name, got, c.want)
				}
			}
		})
	}
	if _, err := seccompProgram("mips", filterOptions{}); err == nil {
		t.Fatal("an architecture without a table")
	}
}

func TestSeccompNamesExist(t *testing.T) {
	for arch, info := range seccompArchs {
		for _, name := range append(slices.Clone(seccompRefused), "socket", "clone", "clone3", "ioctl", "truncate") {
			if _, ok := info.calls[name]; !ok {
				t.Errorf("%s: no number for %s", arch, name)
			}
		}
	}
}

// fakeFS is a file system for the planning functions: its keys are paths, ending in / for a folder.
func fakeFS(paths ...string) fileView {
	kind := map[string]pathKind{"/": pathDir}
	for _, p := range paths {
		k, path := pathFile, p
		if strings.HasSuffix(p, "/") {
			k, path = pathDir, strings.TrimSuffix(p, "/")
		}
		kind[path] = k
		for d := filepath.Dir(path); d != "/"; d = filepath.Dir(d) {
			kind[d] = pathDir
		}
	}
	return fileView{
		Kind: func(p string) pathKind { return kind[p] },
		ReadDir: func(dir string) []dirEntry {
			var out []dirEntry
			for p, k := range kind {
				if p != "/" && filepath.Dir(p) == dir {
					out = append(out, dirEntry{Name: filepath.Base(p), Kind: k})
				}
			}
			slices.SortFunc(out, func(a, b dirEntry) int { return strings.Compare(a.Name, b.Name) })
			return out
		},
	}
}

func granted(rules []llRule, path string) uint64 {
	var a uint64
	for _, r := range rules {
		if r.Path == path || (r.Dir && within(path, r.Path)) {
			a |= r.Access
		}
	}
	return a
}

func TestPlanFiles(t *testing.T) {
	v := fakeFS("/usr/bin/git", "/etc/passwd", "/etc/shadow", "/home/u/.ssh/id_rsa", "/home/u/.bashrc", "/home/u/notes/a", "/work/f", "/tmp/", "/dev/null", "/dev/shm/", "/home/u/.harness/chats/one/")
	p := Policy{Home: "/home/u", Write: []string{"/work", "/tmp", "/home/u/.ssh/inside", "/home/u/.harness/x", "/home/u/.harness/chats/one"},
		ReadDeny: []string{"/home/u/.ssh", "/etc/shadow", "/run/secrets"}, WriteDeny: []string{"/home/u/.ssh", "/home/u/.harness"}, WriteAllowAfterDeny: []string{"/home/u/.harness/chats/one"}}
	rules := planFiles(p, 6, v)

	for _, path := range []string{"/usr/bin/git", "/etc/passwd", "/home/u/.bashrc", "/home/u/notes/a", "/work/f", "/tmp/x"} {
		if granted(rules, path)&llReadFile == 0 {
			t.Errorf("%s cannot be read", path)
		}
	}
	for _, path := range []string{"/home/u/.ssh/id_rsa", "/home/u/.ssh", "/etc/shadow"} {
		if granted(rules, path)&(llReadFile|llExecute) != 0 {
			t.Errorf("%s can be read: %+v", path, rules)
		}
	}
	// Writes: the roots and the few devices, no more.
	for _, path := range []string{"/work/f", "/tmp/x", "/dev/null", "/dev/shm/x", "/home/u/.harness/chats/one/f"} {
		if granted(rules, path)&llWriteFile == 0 {
			t.Errorf("%s cannot be written", path)
		}
	}
	for _, path := range []string{"/usr/bin/git", "/etc/passwd", "/home/u/.bashrc", "/home/u/.ssh/inside/f", "/home/u/.harness/x/f"} {
		if granted(rules, path)&(llWriteFile|llMakeReg) != 0 {
			t.Errorf("%s can be written", path)
		}
	}
	// The kernel refuses folder rights on a file, and rights it does not know.
	for _, r := range rules {
		if !r.Dir && r.Access&^uint64(llFileOnly) != 0 {
			t.Errorf("folder rights on a file: %+v", r)
		}
	}
	for abi, without := range map[int]uint64{1: llRefer | llTruncate, 2: llTruncate} {
		for _, r := range planFiles(p, abi, v) {
			if r.Access&without != 0 {
				t.Errorf("ABI %d: %+v", abi, r)
			}
		}
	}
	// A store that does not exist splits nothing: /run stays one rule.
	if !slices.ContainsFunc(rules, func(r llRule) bool { return r.Path == "/usr" && r.Dir }) {
		t.Errorf("/usr should be one rule: %+v", rules)
	}
	// The scratch directory stays writable, even inside the protected data directory.
	free := Policy{Write: []string{"/home/u/.harness/s/scratch"}, Free: "/home/u/.harness/s/scratch", WriteDeny: []string{"/home/u/.harness"}}
	if !slices.Contains(writableRoots(free), "/home/u/.harness/s/scratch") {
		t.Error("the scratch directory is out")
	}
}

func TestPlanNetAndFilter(t *testing.T) {
	proxy := [][2]int{{9000, 9000}}
	wide := [][2]int{{9000, 9000}, {lowestOpenPort, 65535}}
	base := stageSpec{ProxyPort: 9000}
	cases := []struct {
		name         string
		s            func(stageSpec) stageSpec
		abi          int
		want         netPlan
		deny, tcp    bool
		unix, trunc_ bool
	}{
		{"bubblewrap, localhost", func(s stageSpec) stageSpec {
			s.Backend, s.PrivateNetwork, s.Localhost = backendBubblewrap, true, true
			return s
		}, 6, netPlan{}, false, false, false, false},
		{"bubblewrap, localhost off", func(s stageSpec) stageSpec { s.Backend, s.PrivateNetwork = backendBubblewrap, true; return s }, 6, netPlan{Handled: llNetBind | llNetConnect, Connect: proxy}, false, false, false, false},
		{"bubblewrap, old kernel", func(s stageSpec) stageSpec { s.Backend, s.PrivateNetwork = backendBubblewrap, true; return s }, 3, netPlan{}, false, false, false, false},
		{"bubblewrap, server", func(s stageSpec) stageSpec { s.Backend, s.Server = backendBubblewrap, true; return s }, 4, netPlan{Handled: llNetConnect, Connect: wide}, false, true, false, false},
		{"landlock, localhost off", func(s stageSpec) stageSpec { s.Backend = backendLandlock; return s }, 4, netPlan{Handled: llNetBind | llNetConnect, Connect: proxy}, false, true, true, false},
		{"landlock, localhost", func(s stageSpec) stageSpec { s.Backend, s.Localhost = backendLandlock, true; return s }, 6, netPlan{Handled: llNetConnect, Connect: wide}, false, true, true, false},
		{"landlock, kernel without TCP rules", func(s stageSpec) stageSpec { s.Backend, s.Localhost = backendLandlock, true; return s }, 3, netPlan{}, true, true, true, false},
		{"landlock, kernel without truncate", func(s stageSpec) stageSpec { s.Backend = backendLandlock; return s }, 2, netPlan{}, true, true, true, true},
	}
	for _, c := range cases {
		s := c.s(base)
		got := planNet(s, c.abi)
		if got.Handled != c.want.Handled || !slices.Equal(got.Connect, c.want.Connect) {
			t.Errorf("%s: %+v", c.name, got)
		}
		f := planFilter(s, c.abi)
		if f.DenyInet != c.deny || f.TCPOnly != c.tcp || f.DenyUnix != c.unix || f.DenyTruncate != c.trunc_ {
			t.Errorf("%s: %+v", c.name, f)
		}
	}
	if planScope(stageSpec{Backend: backendLandlock}, 5) != 0 || planScope(stageSpec{Backend: backendLandlock}, 6) != llScopeAbstractUnix|llScopeSignal || planScope(stageSpec{Backend: backendBubblewrap, PrivateNetwork: true}, 6) != 0 {
		t.Error("scopes")
	}
}

func TestChooseLinuxBackend(t *testing.T) {
	cases := []struct {
		name    string
		pr      linuxProbe
		backend string
		says    string
	}{
		{"both", linuxProbe{Arch: "amd64", LandlockABI: 6, BwrapPath: "/usr/bin/bwrap"}, backendBubblewrap, "loopback"},
		{"both, old kernel", linuxProbe{Arch: "amd64", LandlockABI: 3, BwrapPath: "/usr/bin/bwrap"}, backendBubblewrap, "preview servers"},
		{"a default container", linuxProbe{Arch: "arm64", LandlockABI: 6, BwrapErr: "no namespaces"}, backendLandlock, "by port"},
		{"a container on an old kernel", linuxProbe{Arch: "amd64", LandlockABI: 3, BwrapErr: "no"}, backendLandlock, "no network"},
		{"neither", linuxProbe{Arch: "amd64", LandlockErr: "lsm=", BwrapErr: "not installed"}, "", "not installed"},
		{"no filter", linuxProbe{Arch: "amd64", LandlockABI: 6, BwrapPath: "/usr/bin/bwrap", FilterErr: "EINVAL"}, "", "EINVAL"},
		{"another architecture", linuxProbe{Arch: "riscv64", LandlockABI: 6, BwrapPath: "/usr/bin/bwrap"}, "", "riscv64"},
	}
	for _, c := range cases {
		got := chooseLinuxBackend(c.pr)
		if got.Backend != c.backend || !strings.Contains(got.Reason, c.says) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestStageSpecRoundTrip(t *testing.T) {
	s := stageSpec{Op: stageExec, Backend: backendLandlock, Write: []string{`/odd "quoted" dir`}, ProxyPort: 7, Localhost: true}
	got, err := decodeStageSpec(s.encode())
	if err != nil || got.Op != s.Op || got.Write[0] != s.Write[0] || got.ProxyPort != 7 || !got.Localhost {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := decodeStageSpec("!!"); err == nil {
		t.Fatal("garbage was read")
	}
}

// indexOf finds a consecutive run of arguments.
func indexOf(args []string, run ...string) int {
	for i := 0; i+len(run) <= len(args); i++ {
		if slices.Equal(args[i:i+len(run)], run) {
			return i
		}
	}
	return -1
}

func TestBubblewrapArgs(t *testing.T) {
	kinds := fakeFS("/work/.git/config", "/work/.git/hooks/", "/work/.mcp.json", "/s/git-template/", "/s/shell-snapshot.zsh", "/home/u/.ssh/id", "/home/u/.netrc", "/home/u/.zshrc", "/tmp/", "/sock/proxy.sock")
	p := Policy{Write: []string{"/work/sub", "/work", "/tmp", "/s", "/home/u"}, Free: "/s", FreeDeny: []string{"/s/git-template"},
		WriteDeny:        []string{"/home/u/.ssh", "/home/u/.zshrc", "/home/u/.missing"},
		WriteDenyLiteral: []string{"/work/.git/config", "/s/shell-snapshot.zsh"}, ReadDeny: []string{"/home/u/.ssh", "/home/u/.netrc", "/nothing/here"},
		PrivateLoopback: true, ProxySocket: "/sock/proxy.sock"}
	kinds2 := func(path string) pathKind {
		if path == "/work/sub" {
			return pathDir
		}
		return kinds.Kind(path)
	}
	in := bwrapInput{Policy: p, Kind: kinds2, Masked: "/s/sandbox-masked", Sockets: []string{"/tmp/agent.sock"}, Protected: []string{"/work/.git/hooks", "/work/.mcp.json"}, Stage: []string{"/self", stageArg, "x", "/bin/sh"}}
	args := bubblewrapArgs(in)

	order := [][]string{
		{"--unshare-net"},
		{"--ro-bind", "/", "/"},
		{"--dev", "/dev"},
		{"--tmpfs", "/run"},
		{"--ro-bind", "/sock/proxy.sock", insideProxySocket},
		{"--bind", "/work", "/work"},
		{"--bind", "/work/sub", "/work/sub"},
		{"--ro-bind", "/home/u/.zshrc", "/home/u/.zshrc"},
		{"--ro-bind", "/work/.git/hooks", "/work/.git/hooks"},
		{"--ro-bind", "/s/git-template", "/s/git-template"},
		{"--ro-bind", "/work/.git/config", "/work/.git/config"},
		{"--ro-bind", "/s/shell-snapshot.zsh", "/s/shell-snapshot.zsh"},
		{"--tmpfs", "/home/u/.ssh"},
		{"--ro-bind", "/s/sandbox-masked", "/home/u/.netrc"},
		{"--ro-bind", "/s/sandbox-masked", "/tmp/agent.sock"},
		{"--", "/self", stageArg, "x", "/bin/sh"},
	}
	at := -1
	for _, want := range order {
		i := indexOf(args, want...)
		if i < 0 {
			t.Fatalf("no %v in %v", want, args)
		}
		if i <= at {
			t.Fatalf("%v comes too early: a later mount covers an earlier one\n%v", want, args)
		}
		at = i
	}
	for _, no := range []string{"/home/u/.missing", "/nothing/here"} {
		if slices.Contains(args, no) {
			t.Errorf("%s does not exist, and bwrap would fail on it: %v", no, args)
		}
	}

	p.PrivateLoopback, p.ProxySocket = false, ""
	in.Policy = p
	if args := bubblewrapArgs(in); slices.Contains(args, "--unshare-net") || indexOf(args, "--unshare-pid", "--as-pid-1") < 0 {
		t.Fatalf("a preview server keeps the network and loses the rest: %v", args)
	}
}

func TestFindProtectedAndSockets(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{".git/hooks/pre-commit", ".git/config", ".mcp.json", "pkg/a/.harness/settings.json", "pkg/b/.git", "node_modules/x/.mcp.json", "a/b/c/d/.mcp.json"} {
		path := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := findProtected(root)
	for _, want := range []string{".git/hooks", ".git/config", ".git/config.worktree", ".mcp.json", "pkg/a/.harness", "pkg/b/.git"} {
		if !slices.Contains(got, filepath.Join(root, want)) {
			t.Errorf("%s not found: %v", want, got)
		}
	}
	for _, no := range []string{"node_modules/x/.mcp.json", "a/b/c/d/.mcp.json"} {
		if slices.Contains(got, filepath.Join(root, no)) {
			t.Errorf("%s should be out of the search: %v", no, got)
		}
	}
	if got := findSockets(filepath.Join(root, "missing")); got != nil {
		t.Fatal(got)
	}
}

func TestLinuxRoots(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".volta"), 0o755); err != nil {
		t.Fatal(err)
	}
	in := func(rel string) string { return filepath.Join(home, rel) }
	got := linuxRoots(home, []string{"/elsewhere", in("Library/Caches/pip"), in(".cache"), in(".npm"), in("go/pkg/mod"), in(".volta/tmp"), in(".cargo/registry")})
	if slices.Contains(got, in("Library/Caches/pip")) || !slices.Contains(got, "/elsewhere") {
		t.Fatalf("%v", got)
	}
	for _, made := range []string{".cache", ".npm", "go/pkg/mod", ".volta/tmp"} {
		if _, err := os.Stat(in(made)); err != nil {
			t.Errorf("%s was not made: %v", made, err)
		}
	}
	if _, err := os.Stat(in(".cargo")); err == nil {
		t.Error("a home without Cargo should not get a .cargo")
	}
}

func TestGuard(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	scratch := filepath.Join(root, "scratch")
	write := func(rel, content string, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	config := write("work/.git/config", "[core]\n", 0o644)
	hook := write("work/.git/hooks/pre-commit", "#!/bin/sh\n", 0o755)
	settings := write("work/.harness/settings.local.json", "{}\n", 0o644)
	plain := write("work/plain", "x", 0o644)
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	p := BuildPolicy(PolicyInput{Home: filepath.Join(root, "home"), HarnessHome: filepath.Join(root, "home", ".harness"), WorkDirs: []string{work}, ScratchDir: scratch, Getenv: func(string) string { return "" }})
	w := &Wrapped{Policy: p, guard: newGuard(p, scratch)}

	// What a command could do, and Landlock could not stop.
	write("work/.git/config", "[core]\n\tfsmonitor = /tmp/evil\n", 0o644)
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	newHook := write("work/.git/hooks/post-checkout", "#!/bin/sh\nevil\n", 0o755)
	newMCP := write("work/.mcp.json", "{}", 0o644)
	if err := os.Remove(settings); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp/evil", settings); err != nil {
		t.Fatal(err)
	}
	write("work/plain", "changed", 0o644)

	d := w.Diagnose("all went well", work, false)
	if d == nil {
		t.Fatal("no report")
	}
	if len(d.Paths) != 5 {
		t.Fatalf("%q", d.Paths)
	}
	if got := read(t, config); got != "[core]\n" {
		t.Errorf("the config was not put back: %q", got)
	}
	if got := read(t, hook); got != "#!/bin/sh\n" {
		t.Errorf("the hook was not put back: %q", got)
	}
	if st, _ := os.Stat(hook); st.Mode().Perm()&0o100 == 0 {
		t.Error("the hook lost its mode")
	}
	if st, err := os.Lstat(settings); err != nil || !st.Mode().IsRegular() {
		t.Errorf("the settings were not put back: %v", err)
	}
	for _, gone := range []string{newHook, newMCP} {
		if _, err := os.Lstat(gone); err == nil {
			t.Errorf("%s is still there", gone)
		}
	}
	if got := read(t, plain); got != "changed" {
		t.Error("an ordinary file was touched")
	}
	entries, err := os.ReadDir(filepath.Join(scratch, "sandbox-rejected"))
	if err != nil || len(entries) != 4 {
		t.Errorf("what the command left should be kept: %v %v", entries, err)
	}
	note := d.Note(OutsideParamForTest)
	for _, want := range []string{"[sandbox]", "put back", "post-checkout"} {
		if !strings.Contains(note, want) {
			t.Errorf("%s not in %s", want, note)
		}
	}
	// It does its work once, and a clean run reports nothing.
	if d := w.Diagnose("", work, false); d == nil || len(d.Paths) != 5 {
		t.Errorf("%+v", d)
	}
	clean := &Wrapped{Policy: p, guard: newGuard(p, scratch)}
	if d := clean.Diagnose("", work, false); d != nil {
		t.Errorf("%+v", d)
	}
	var none *Wrapped
	if none.Verify() != nil {
		t.Error("no sandbox, nothing to verify")
	}
}

// A data directory of the harness inside a writable root: a HARNESS_HOME under /tmp, or a session in the home folder. The
// client writes its transcript there while a command runs, and the guard must leave that be; a settings file it still puts
// back. Under bubblewrap, what exists is mounted read-only, so the guard looks only at what does not exist yet.
func TestGuardAgentHome(t *testing.T) {
	root := outsideTmpRoot(t)
	home := filepath.Join(root, "home")
	hh := filepath.Join(home, ".harness")
	scratch := filepath.Join(root, "scratch")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	transcript := filepath.Join(hh, "projects", "p", "s.jsonl")
	settings := filepath.Join(hh, "settings.json")
	claudeTranscript := filepath.Join(home, ".claude", "projects", "p", "s.jsonl")
	write(transcript, "{}\n")
	write(settings, "{}\n")
	write(claudeTranscript, "{}\n")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	p := BuildPolicy(PolicyInput{Home: home, HarnessHome: hh, WorkDirs: []string{home}, ScratchDir: scratch, Getenv: func(string) string { return "" }})
	if !slices.Contains(p.AgentHomes, hh) || !slices.Contains(p.AgentHomes, filepath.Join(home, ".claude")) {
		t.Fatalf("agent homes: %v", p.AgentHomes)
	}

	w := &Wrapped{Policy: p, guard: newGuard(p, scratch)}
	write(transcript, "{}\n{\"more\":1}\n")
	write(claudeTranscript, "{}\n{\"more\":1}\n")
	write(settings, `{"permissions":{"allow":["Bash"]}}`)
	newHook := filepath.Join(hh, "hooks", "h.sh")
	write(newHook, "evil")
	got := w.Verify()
	if len(got) != 2 || !strings.HasPrefix(got[0], settings+": ") || !strings.HasPrefix(got[1], newHook+": ") {
		t.Fatalf("%q", got)
	}
	if read(t, transcript) != "{}\n{\"more\":1}\n" || read(t, claudeTranscript) != "{}\n{\"more\":1}\n" {
		t.Error("a transcript was put back")
	}
	if read(t, settings) != "{}\n" {
		t.Error("the settings were not put back")
	}

	held := p
	held.DenyHeld = true
	w = &Wrapped{Policy: held, guard: newGuard(held, scratch)}
	write(settings, `{"permissions":{"allow":["Bash"]}}`)
	write(filepath.Join(home, ".zshrc"), "evil")
	if got := w.Verify(); len(got) != 1 || !strings.HasPrefix(got[0], filepath.Join(home, ".zshrc")+": ") {
		t.Fatalf("held: %q", got)
	}
}

func TestDiagnoseLinuxWordings(t *testing.T) {
	root := outsideTmpRoot(t)
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	base := BuildPolicy(PolicyInput{Home: home, HarnessHome: filepath.Join(home, ".harness"), WorkDirs: []string{work}, Getenv: func(string) string { return "" }})
	bwrap, landlock := base, base
	bwrap.PrivateLoopback, bwrap.GuardNames = true, true
	landlock.FilesByPermission, landlock.PortRules = true, true
	outside := filepath.Join(root, "elsewhere", "f")
	cases := []struct {
		name   string
		policy Policy
		output string
		want   string
	}{
		{"bind mount", bwrap, "touch: cannot touch '" + outside + "': Read-only file system", outside},
		{"masked folder", bwrap, "cat: " + filepath.Join(home, ".ssh", "id_rsa") + ": No such file or directory", "credential store"},
		{"masked file", bwrap, "cat: " + filepath.Join(home, ".netrc") + ": Permission denied", "credential store"},
		{"no route", bwrap, "curl: (7) Failed to connect to 1.1.1.1 port 443: Network is unreachable", "direct network"},
		{"no name", bwrap, "curl: (6) Could not resolve host: example.com", "direct network"},
		{"own loopback", bwrap, "curl: (7) Failed to connect to localhost port 3000: Connection refused", "loopback of its own"},
		{"over a mount", bwrap, "mv: cannot move 'x' to '" + filepath.Join(work, ".git", "config") + "': Device or resource busy", "programs run"},
		{"landlock file", landlock, "sh: 1: cannot create " + outside + ": Permission denied", outside},
		{"landlock port", landlock, "dial tcp 1.2.3.4:443: connect: permission denied", "direct network"},
		{"landlock local port", landlock, "dial tcp 127.0.0.1:80: connect: permission denied", "local port"},
		{"seccomp socket", landlock, "socket(AF_UNIX) failed: Operation not permitted", "direct network"},
		{"no new privileges", landlock, `sudo: The "no new privileges" flag is set, which prevents sudo from running as root.`, "setuid"},
	}
	for _, c := range cases {
		w := &Wrapped{Policy: c.policy}
		d := w.Diagnose(c.output, work, true)
		if d == nil {
			t.Errorf("%s: no diagnosis", c.name)
			continue
		}
		if note := d.Note(OutsideParamForTest); !strings.Contains(note, c.want) {
			t.Errorf("%s: %s", c.name, note)
		}
	}
	// Not the sandbox's doing: a missing file that is no store, and a refused port when loopback is shared.
	w := &Wrapped{Policy: bwrap}
	if d := w.Diagnose("cat: "+filepath.Join(work, "f")+": No such file or directory", work, true); d != nil {
		t.Errorf("%+v", d)
	}
	w = &Wrapped{Policy: base}
	if d := w.Diagnose("curl: (7) Failed to connect to localhost port 3000: Connection refused", work, true); d != nil && d.PrivateLoopback {
		t.Errorf("%+v", d)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
