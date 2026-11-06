//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These run the real helper, bwrap and kernel. They never touch real credentials: HOME and HARNESS_HOME are folders of the
// test holding fake ones. Each runs under each backend the machine allows and skips the others; a machine that must prove one
// names it, comma separated, in HARNESS_SANDBOX_REQUIRE, and a missing one fails instead (CI does this: client.yml).
//
// The box is not under the system's temp folder, since /tmp is writable by policy.

type lbox struct {
	t                   *testing.T
	backend             string
	abi                 int
	home, harness, work string
	outside, scratch    string
	session             *Session
	settings            Settings
	realEnv             map[string]string
	wrapped             *Wrapped
}

func required(backend string) bool {
	for _, b := range strings.Split(os.Getenv("HARNESS_SANDBOX_REQUIRE"), ",") {
		if strings.TrimSpace(b) == backend {
			return true
		}
	}
	return false
}

func forBackends(t *testing.T, f func(t *testing.T, b *lbox)) {
	t.Helper()
	for _, backend := range []string{backendBubblewrap, backendLandlock} {
		t.Run(backend, func(t *testing.T) {
			d := linuxDriver{backend: backend}
			info := d.Info()
			if !info.Supported {
				if required(backend) {
					t.Fatalf("%s is required here, and: %s", backend, info.Reason)
				}
				t.Skipf("%s is not available: %s", backend, info.Reason)
			}
			f(t, newLbox(t, d, backend))
		})
	}
}

func newLbox(t *testing.T, d Driver, backend string) *lbox {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("no folder for the box outside /tmp: %v", err)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Skipf("no folder for the box outside /tmp: %v", err)
	}
	root, err := os.MkdirTemp(base, "hsbt")
	if err != nil {
		t.Skipf("no folder for the box outside /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	abi, _ := landlockVersion()
	b := &lbox{t: t, backend: backend, abi: abi, home: filepath.Join(root, "home"), harness: filepath.Join(root, "home", ".harness"),
		work: filepath.Join(root, "work"), outside: filepath.Join(root, "outside"), scratch: filepath.Join(root, "scratch"), realEnv: map[string]string{}}
	for _, dir := range []string{b.home, b.harness, b.work, b.outside, b.scratch} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b.settings = Settings{Enabled: true}
	b.session = &Session{Driver: d, Home: b.home, HarnessHome: b.harness, Settings: func() Settings { return b.settings }, Getenv: func(k string) string { return b.realEnv[k] }}
	t.Cleanup(b.session.Close)
	return b
}

func (b *lbox) write(path, content string) string {
	b.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		b.t.Fatal(err)
	}
	return path
}

func (b *lbox) cmd(kind Kind, script string) *exec.Cmd {
	b.t.Helper()
	w, err := b.session.Wrap(Request{Kind: kind, WorkDirs: []string{b.work}, ScratchDir: b.scratch}, []string{"/bin/sh", "-c", script})
	if err != nil {
		b.t.Fatal(err)
	}
	if w == nil {
		b.t.Fatal("the sandbox did not apply")
	}
	b.wrapped = w
	c := exec.Command(w.Argv[0], w.Argv[1:]...)
	c.Dir = b.work
	c.Env = append(append(os.Environ(), "HOME="+b.home), w.Env...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.WaitDelay = 2 * time.Second
	return c
}

// sh runs script in the sandbox and returns what it printed, what the guard and the diagnosis made of it, and whether it
// succeeded. The guard has run by the time this returns, as it has for a tool's result.
func (b *lbox) sh(script string) (string, *Denial, bool) {
	b.t.Helper()
	var out bytes.Buffer
	c := b.cmd(KindCommand, script)
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	return out.String(), b.wrapped.Diagnose(out.String(), b.work, err != nil), err == nil
}

func (b *lbox) ok(script string) string {
	b.t.Helper()
	out, _, ok := b.sh(script)
	if !ok {
		b.t.Fatalf("%s failed:\n%s", script, out)
	}
	return out
}

func (b *lbox) refused(script string) string {
	b.t.Helper()
	out, _, ok := b.sh(script)
	if ok {
		b.t.Fatalf("%s should have been refused:\n%s", script, out)
	}
	return out
}

func present(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func need(t *testing.T, programs ...string) {
	t.Helper()
	for _, p := range programs {
		if _, err := exec.LookPath(p); err != nil {
			if os.Getenv("HARNESS_SANDBOX_REQUIRE") != "" {
				t.Fatalf("%s is needed here", p)
			}
			t.Skipf("no %s", p)
		}
	}
}

func TestLinuxInfoSaysWhy(t *testing.T) {
	info := NewDriver().Info()
	t.Logf("supported=%v: %s", info.Supported, info.Reason)
	if info.Platform != "linux" || info.Reason == "" {
		t.Fatalf("%+v", info)
	}
	if !info.Supported {
		// Unsupported is a state the frontends show, and a required sandbox refuses.
		s := &Session{Settings: func() Settings { return Settings{Enabled: true} }}
		if st := s.State(); st.Active || st.Reason == "" {
			t.Fatalf("%+v", st)
		}
		s = &Session{Settings: func() Settings { return Settings{Enabled: true, Forced: true} }}
		if _, err := s.Wrap(Request{ScratchDir: t.TempDir()}, []string{"/bin/true"}); err == nil {
			t.Fatal("a required sandbox ran a command without a driver")
		}
	}
}

func TestLinuxHelperFailsClosed(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"garbage":    {stageArg, "!!", "/bin/true"},
		"no command": {stageArg, stageSpec{Op: stageExec, Backend: backendLandlock}.encode()},
		"unknown op": {stageArg, stageSpec{Op: "nope"}.encode(), "/bin/true"},
		"no program": {stageArg, stageSpec{Op: stageExec, Backend: backendLandlock}.encode(), "/no/such/program"},
	} {
		out, err := exec.Command(self, args...).CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 126 || !strings.Contains(string(out), "did not run") {
			t.Errorf("%s: %v %s", name, err, out)
		}
	}
}

func TestLinuxWrites(t *testing.T) {
	forBackends(t, func(t *testing.T, b *lbox) {
		b.ok("echo hi > inside.txt && mkdir -p a/b && mv inside.txt a/b/moved.txt && rm a/b/moved.txt")
		b.ok("echo hi > " + b.scratch + "/note")
		b.ok(`echo hi > "$TMPDIR/note"`)
		b.ok("echo hi > /tmp/harness-sandbox-test-$$ && rm /tmp/harness-sandbox-test-$$")

		target := filepath.Join(b.outside, "evil")
		b.refused("echo hi > " + target)
		if present(target) {
			t.Fatal("a file appeared outside")
		}
		// Nor by the other ways of changing a file.
		kept := b.write(filepath.Join(b.outside, "kept"), "kept\n")
		b.refused("rm " + kept)
		b.refused("truncate -s 0 " + kept)
		b.refused("mv " + kept + " " + b.work + "/taken")
		if read(t, kept) != "kept\n" {
			t.Fatal("a file outside changed")
		}
		// A refusal is explained, under either wording of the kernel.
		_, d, _ := b.sh("touch " + target)
		if d == nil || !strings.Contains(d.Note("x"), "outside the writable") {
			t.Fatalf("%+v", d)
		}
	})
}

func TestLinuxAllowedWritePath(t *testing.T) {
	forBackends(t, func(t *testing.T, b *lbox) {
		b.settings.AllowedWritePaths = []string{b.outside, "~/extra"}
		b.ok("echo hi > " + b.outside + "/ok")
		if err := os.MkdirAll(filepath.Join(b.home, "extra"), 0o755); err != nil {
			t.Fatal(err)
		}
		b.ok("mkdir -p " + b.home + "/extra/x")
		// A protected path wins over the list.
		b.settings.AllowedWritePaths = []string{"~/.ssh", b.home}
		rc := b.write(filepath.Join(b.home, ".zshrc"), "# rc\n")
		_, _, _ = b.sh("echo evil >> " + rc)
		if strings.Contains(read(t, rc), "evil") {
			t.Fatal("the rc file kept the write")
		}
		key := b.write(filepath.Join(b.home, ".ssh", "config"), "fine\n")
		_, _, _ = b.sh("echo evil >> " + key)
		if strings.Contains(read(t, key), "evil") {
			t.Fatal("the ssh config kept the write")
		}
		b.ok("echo hi > " + b.home + "/plain")
	})
}

// What a command must not leave behind, whether the kernel refused it or the guard took it back.
func TestLinuxProtectedNames(t *testing.T) {
	forBackends(t, func(t *testing.T, b *lbox) {
		config := b.write(filepath.Join(b.work, ".git", "config"), "[core]\n")
		hook := b.write(filepath.Join(b.work, ".git", "hooks", "pre-commit"), "#!/bin/sh\n")
		settings := b.write(filepath.Join(b.work, ".harness", "settings.local.json"), "{}\n")

		for _, attempt := range []struct {
			script string
			path   string
			want   string
		}{
			{"echo evil >> .git/config", config, "[core]\n"},
			{"echo evil > .git/config.lock; mv .git/config.lock .git/config", config, "[core]\n"},
			{"echo evil >> .git/hooks/pre-commit", hook, "#!/bin/sh\n"},
			{"rm .git/hooks/pre-commit", hook, "#!/bin/sh\n"},
			{`echo '{"sandbox":{"enabled":false}}' > .harness/settings.local.json`, settings, "{}\n"},
		} {
			_, d, _ := b.sh(attempt.script)
			if d == nil {
				t.Errorf("%s: nothing reported", attempt.script)
			}
			if got := read(t, attempt.path); got != attempt.want {
				t.Fatalf("%s: %s holds %q", attempt.script, attempt.path, got)
			}
		}
		// Those that do not exist yet.
		for _, f := range []string{".git/hooks/post-checkout", ".mcp.json", ".harness/settings.json", ".claude/settings.json"} {
			_, d, _ := b.sh("mkdir -p $(dirname " + f + "); echo evil > " + f)
			if present(filepath.Join(b.work, f)) {
				t.Fatalf("%s stayed", f)
			}
			if d == nil {
				t.Errorf("%s: nothing reported", f)
			}
		}
		// A .git file would send git elsewhere.
		b.ok("mkdir -p sub2")
		if err := os.WriteFile(filepath.Join(b.work, "sub2", "kept"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		// The rest of .git works.
		b.ok("echo x > .git/ORIG_HEAD && rm .git/ORIG_HEAD")
	})
}

func TestLinuxSharedStateOfTheScratchDirectory(t *testing.T) {
	forBackends(t, func(t *testing.T, b *lbox) {
		snap := b.write(filepath.Join(b.scratch, "shell-snapshot.zsh"), "# snapshot\n")
		_, _, _ = b.sh("echo evil >> " + snap)
		if read(t, snap) != "# snapshot\n" {
			t.Fatal("a command changed the shell snapshot")
		}
		_, _, _ = b.sh("mkdir -p " + filepath.Join(b.scratch, "git-template", "hooks") + " && echo evil > " + filepath.Join(b.scratch, "git-template", "hooks", "h"))
		if present(filepath.Join(b.scratch, "git-template", "hooks", "h")) {
			t.Fatal("a command put a hook in the template")
		}
		// Tests that make a repository in their temp directory and remove it again are ordinary.
		need(t, "git")
		g := "git -c user.name=t -c user.email=t@example.test -c init.defaultBranch=main "
		b.ok(`cd "$TMPDIR" && ` + g + `init -q r && cd r && ` + g + `commit -q --allow-empty -m x && mkdir -p .git/hooks && echo x > .git/hooks/h && cd .. && rm -rf r`)
	})
}

func TestLinuxSecretsUnreadable(t *testing.T) {
	forBackends(t, func(t *testing.T, b *lbox) {
		secrets := []string{".ssh/id_ed25519", ".aws/credentials", ".gnupg/private-keys", ".local/share/keyrings/login.keyring",
			".netrc", ".npmrc", ".git-credentials", ".docker/config.json", ".kube/config", ".config/gh/hosts.yml",
			".harness/credentials.json", ".harness/agent-token", ".harness/local-provider-keys.json", ".harness/environments/x"}
		for _, s := range secrets {
			p := b.write(filepath.Join(b.home, s), "FAKE SECRET\n")
			out, d, _ := b.sh("cat " + p)
			if strings.Contains(out, "FAKE SECRET") {
				t.Fatalf("%s was readable: %s", s, out)
			}
			if d == nil || !strings.Contains(d.Note("x"), "credential store") {
				t.Errorf("%s: not explained: %s %+v", s, out, d)
			}
			// Nor with a link to it.
			out, _, _ = b.sh("ln -s " + p + " sym; cat sym; rm -f sym; ln " + p + " hard; cat hard; rm -f hard")
			if strings.Contains(out, "FAKE SECRET") {
				t.Fatalf("%s was readable through a link: %s", s, out)
			}
		}
		// Other files of the home directory and of HARNESS_HOME are.
		if out := b.ok("cat " + b.write(filepath.Join(b.home, "notes"), "fine\n")); !strings.Contains(out, "fine") {
			t.Fatal(out)
		}
		if out := b.ok("cat " + b.write(filepath.Join(b.harness, "plans", "p.md"), "plan\n")); !strings.Contains(out, "plan") {
			t.Fatal(out)
		}
		if out := b.ok("ls -la " + b.home); !strings.Contains(out, ".ssh") {
			t.Fatal(out)
		}
		b.ok("cat /etc/passwd > /dev/null && /bin/ls / > /dev/null")
	})
}

func TestLinuxChildInherits(t *testing.T) {
	forBackends(t, func(t *testing.T, b *lbox) {
		target := filepath.Join(b.outside, "child")
		b.refused(`/bin/sh -c '/bin/sh -c "echo hi > ` + target + `"'`)
		if present(target) {
			t.Fatal("a child wrote outside")
		}
	})
}

func TestLinuxOffSwitch(t *testing.T) {
	forBackends(t, func(t *testing.T, b *lbox) {
		target := filepath.Join(b.outside, "off")
		b.refused("echo hi > " + target)
		b.settings.Enabled = false
		if w, err := b.session.Wrap(Request{WorkDirs: []string{b.work}, ScratchDir: b.scratch}, []string{"/bin/true"}); w != nil || err != nil {
			t.Fatalf("the sandbox should not apply: %v %v", w, err)
		}
		b.settings.Forced = true
		if st := b.session.State(); !st.Active {
			t.Fatalf("forced: %+v", st)
		}
		b.refused("echo hi >> " + target)
	})
}

func TestLinuxExitCodeAndArgs(t *testing.T) {
	forBackends(t, func(t *testing.T, b *lbox) {
		var exit *exec.ExitError
		c := b.cmd(KindCommand, "exit 7")
		if err := c.Run(); !errors.As(err, &exit) || exit.ExitCode() != 7 {
			t.Fatalf("exit: %v", err)
		}
		if out := b.ok(`printf '%s' "a b  c"`); out != "a b  c" {
			t.Fatalf("%q", out)
		}
		if out := b.ok("pwd -P"); strings.TrimSpace(out) != b.work {
			t.Fatalf("started in %s", out)
		}
		if out := b.ok(`echo "$HARNESS_SANDBOX"`); strings.TrimSpace(out) != "1" {
			t.Fatalf("%q", out)
		}
	})
}

// The driver of a tool kills the process group, and nothing it started may stay, in the namespace or out of it.
func TestLinuxKillLeavesNothing(t *testing.T) {
	need(t, "pgrep")
	forBackends(t, func(t *testing.T, b *lbox) {
		mark := "31" + strconv.Itoa(os.Getpid())
		c := b.cmd(KindCommand, "sleep "+mark+" & sleep "+mark+"; wait")
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		running := func() int {
			out, _ := exec.Command("pgrep", "-f", "^sleep "+mark).Output()
			return len(strings.Fields(string(out)))
		}
		waitFor(t, "the command to start", func() bool { return running() > 0 })
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		_ = c.Wait()
		waitFor(t, "the command's processes to end", func() bool { return running() == 0 })
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func lupstream(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "hello from upstream") }))
	t.Cleanup(srv.Close)
	return srv, strings.TrimPrefix(srv.URL, "http://")
}

func TestLinuxNetwork(t *testing.T) {
	need(t, "curl")
	forBackends(t, func(t *testing.T, b *lbox) {
		srv, addr := lupstream(t)
		// Direct, to this machine and to the internet: refused by the kernel, without the proxy.
		b.refused("curl --noproxy '*' -sS --max-time 5 " + srv.URL)
		out := b.refused("curl --noproxy '*' -sS --max-time 5 http://1.1.1.1/")
		if _, d, _ := b.sh("curl --noproxy '*' -sS --max-time 5 http://1.1.1.1/"); d == nil {
			t.Errorf("not explained: %s", out)
		}
		if b.backend == backendLandlock && b.abi < 4 {
			b.refused(`curl --noproxy "" -sS --fail --max-time 5 ` + srv.URL)
			return
		}
		// Through the proxy, not listed: a 403 from the proxy, remembered.
		o, d, ok := b.sh(`curl --noproxy "" -sS --fail --max-time 5 ` + srv.URL)
		if ok {
			t.Fatalf("not on the list, but it went through: %s", o)
		}
		if got := b.wrapped.BlockedHosts(); len(got) != 1 || got[0] != addr {
			t.Fatalf("blocked hosts: %v, output %s", got, o)
		}
		if d == nil || !strings.Contains(d.Note("x"), addr) {
			t.Fatalf("%+v", d)
		}
		// On the list: through. The list does not open the address directly.
		b.settings.AllowedHosts = []string{addr}
		if out := b.ok(`curl --noproxy "" -sS --fail --max-time 5 ` + srv.URL); !strings.Contains(out, "hello from upstream") {
			t.Fatal(out)
		}
		b.refused("curl --noproxy '*' -sS --max-time 5 " + srv.URL)
		// A proxy's tunnel works too: what TLS goes through.
		if out := b.ok(`curl --noproxy "" -p -sS --fail --max-time 5 ` + srv.URL); !strings.Contains(out, "hello from upstream") {
			t.Fatal(out)
		}
	})
}

func TestLinuxOtherSocketsAndProcesses(t *testing.T) {
	need(t, "python3")
	forBackends(t, func(t *testing.T, b *lbox) {
		// No UDP out, no name lookups: a command asks the proxy for names.
		b.refused(`python3 -c 'import socket; socket.getaddrinfo("example.com", 443)'`)
		// A raw or a packet socket, also for a command that runs as root.
		b.refused(`python3 -c 'import socket; socket.socket(socket.AF_PACKET, socket.SOCK_RAW)'`)
		// A socket of another program, as docker.sock would be.
		root, err := os.MkdirTemp("/tmp", "hsb-other")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(root)
		sock := filepath.Join(root, "s.sock")
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		client := `import socket,sys; s=socket.socket(socket.AF_UNIX); s.connect(sys.argv[1]); print("connected")`
		b.refused(`python3 -c '` + client + `' ` + sock)
		// /run is not there to find any under bubblewrap, and no unix socket can be made under Landlock.
		own := `import socket; s=socket.socket(socket.AF_UNIX); s.bind("own.sock"); s.listen(1); c=socket.socket(socket.AF_UNIX); c.connect("own.sock"); print("own ok")`
		if b.backend == backendBubblewrap {
			if out := b.ok(`python3 -c '` + own + `'`); !strings.Contains(out, "own ok") {
				t.Fatal(out)
			}
		} else {
			b.refused(`python3 -c '` + own + `'`)
			if out := b.ok(`python3 -c 'import socket; a,b=socket.socketpair(); print("pair ok")'`); !strings.Contains(out, "pair ok") {
				t.Fatal(out)
			}
		}
		// Other processes: the client's environment holds its tokens.
		b.refused(fmt.Sprintf("cat /proc/%d/environ", os.Getpid()))
		// Namespaces, which is how to start over.
		b.refused(`python3 -c 'import os; os.unshare(os.CLONE_NEWUSER)'`)
		// Threads still work, and so does a forked child, through whichever call libc makes.
		if out := b.ok(`python3 -c 'import threading,os; t=threading.Thread(target=print,args=("thread ok",)); t.start(); t.join(); os.wait() if os.fork() else os._exit(0)'`); !strings.Contains(out, "thread ok") {
			t.Fatal(out)
		}
	})
}

func TestLinuxLocalhost(t *testing.T) {
	need(t, "python3")
	forBackends(t, func(t *testing.T, b *lbox) {
		server := `import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); s.listen(1); c=socket.socket(); c.connect(s.getsockname()); print("loop ok")`
		if b.abi >= 4 {
			b.refused(`python3 -c '` + server + `'`)
		} else if b.backend == backendLandlock {
			b.refused(`python3 -c '` + server + `'`)
		}
		b.settings.AllowLocalhost = true
		if b.backend == backendLandlock && b.abi < 4 {
			b.refused(`python3 -c '` + server + `'`)
			return
		}
		if out := b.ok(`python3 -c '` + server + `'`); !strings.Contains(out, "loop ok") {
			t.Fatal(out)
		}
		if b.backend == backendBubblewrap {
			// The loopback is the command's own: a server on this machine is not on it, and the note says so.
			need(t, "curl")
			srv, _ := lupstream(t)
			out, d, ok := b.sh("curl --noproxy '*' -sS --max-time 5 " + srv.URL)
			if ok {
				t.Fatalf("reached this machine: %s", out)
			}
			if d == nil || !d.PrivateLoopback {
				t.Fatalf("%s: %+v", out, d)
			}
		} else {
			// Ports below 1024 stay closed, on any address: where the traffic to stop is.
			b.refused(`python3 -c 'import socket; socket.create_connection(("1.1.1.1", 443), 5)'`)
		}
	})
}

func TestLinuxPreviewServer(t *testing.T) {
	need(t, "python3")
	forBackends(t, func(t *testing.T, b *lbox) {
		if b.backend == backendLandlock && b.abi < 4 {
			t.Skip("this kernel has no way to give a server the network without giving it all")
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		b.write(filepath.Join(b.work, "index.html"), "served page\n")
		c := b.cmd(KindServer, fmt.Sprintf("exec python3 -m http.server %d --bind 127.0.0.1", port))
		var out bytes.Buffer
		c.Stdout, c.Stderr = &out, &out
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL); _ = c.Wait() }()
		url := fmt.Sprintf("http://127.0.0.1:%d/index.html", port)
		var body string
		waitFor(t, "the server to answer", func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				return false
			}
			defer res.Body.Close()
			var b bytes.Buffer
			_, _ = b.ReadFrom(res.Body)
			body = b.String()
			return true
		})
		if !strings.Contains(body, "served page") {
			t.Fatalf("no page: %q, server said %s", body, out.String())
		}
		// The same file rules hold.
		c2 := b.cmd(KindServer, "echo hi > "+b.outside+"/from-server")
		if err := c2.Run(); err == nil {
			t.Fatal("a preview server wrote outside")
		}
	})
}

func TestLinuxToolchain(t *testing.T) {
	forBackends(t, func(t *testing.T, b *lbox) {
		if _, err := exec.LookPath("go"); err == nil {
			goroot, _ := exec.Command("go", "env", "GOROOT").Output()
			env := "export GOTOOLCHAIN=local GOFLAGS=-mod=mod GOROOT=" + strings.TrimSpace(string(goroot)) + "; "
			b.write(filepath.Join(b.work, "go.mod"), "module example.test/x\n\ngo 1.21\n")
			b.write(filepath.Join(b.work, "main.go"), "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"built and ran\") }\n")
			if out := b.ok(env + "go build -o app . && ./app"); !strings.Contains(out, "built and ran") {
				t.Fatal(out)
			}
			// Installing to a directory on PATH is the one thing a cache must not become.
			b.refused(env + "mkdir -p $HOME/go/bin && go build -o $HOME/go/bin/x .")
		}
		if _, err := exec.LookPath("git"); err == nil {
			g := "git -c user.name=t -c user.email=t@example.test -c init.defaultBranch=main "
			repo := filepath.Join(b.work, "repo")
			if o, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
				t.Fatalf("git init: %v %s", err, o)
			}
			work := b.work
			b.work = repo
			b.write(filepath.Join(repo, "a.txt"), "a\n")
			o := b.ok(g + "status --short && " + g + "add a.txt && " + g + "commit -qm first && " + g + "log --oneline && " + g + "branch feature && " + g + "stash list")
			if !strings.Contains(o, "first") {
				t.Fatal(o)
			}
			// Writing the config of the repository it works in does not stay, with a message that names it.
			before := read(t, filepath.Join(repo, ".git", "config"))
			_, d, _ := b.sh(g + "config core.hooksPath /tmp/x")
			if read(t, filepath.Join(repo, ".git", "config")) != before {
				t.Fatal("the repository's config changed")
			}
			if d == nil || !strings.Contains(d.Note("x"), "config") {
				t.Fatalf("%+v", d)
			}
			// A new repository in a subfolder (a clone, say) works, without hooks.
			o = b.ok(g + "init -q sub && ls sub/.git && cd sub && " + g + "commit -q --allow-empty -m one")
			if strings.Contains(o, "hooks") {
				t.Fatalf("a hooks folder appeared: %s", o)
			}
			b.ok(g + "clone -q sub cloned && ls cloned/.git")
			b.work = work
		}
	})
}

func TestLinuxLinkedWorktreeCanCommit(t *testing.T) {
	need(t, "git")
	forBackends(t, func(t *testing.T, b *lbox) {
		main := filepath.Join(b.outside, "main")
		wt := filepath.Join(b.outside, "wt")
		run := func(dir string, args ...string) {
			t.Helper()
			c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.test"}, args...)...)
			c.Dir = dir
			if o, err := c.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, o)
			}
		}
		if err := os.MkdirAll(main, 0o755); err != nil {
			t.Fatal(err)
		}
		run(main, "init", "-q")
		run(main, "commit", "-q", "--allow-empty", "-m", "base")
		run(main, "worktree", "add", "-q", wt, "-b", "side")
		b.work = wt
		g := "git -c user.name=t -c user.email=t@example.test "
		b.ok("echo x > f && " + g + "add f && " + g + "commit -qm inworktree")
		// Its hooks and config do not change, although the git directory is a root now.
		hook := filepath.Join(main, ".git", "hooks", "pre-commit")
		_, _, _ = b.sh("echo x > " + hook)
		if present(hook) {
			t.Fatal("a hook of the main checkout stayed")
		}
		before := read(t, filepath.Join(main, ".git", "config"))
		_, _, _ = b.sh("echo x >> " + filepath.Join(main, ".git", "config"))
		if read(t, filepath.Join(main, ".git", "config")) != before {
			t.Fatal("the config of the main checkout changed")
		}
		// And the rest of the main checkout is not writable.
		b.refused("echo x > " + filepath.Join(main, "elsewhere"))
	})
}
