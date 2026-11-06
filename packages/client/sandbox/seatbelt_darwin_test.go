//go:build darwin

package sandbox

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These run the real sandbox-exec. They never touch real credentials: HOME and HARNESS_HOME are temp directories holding
// fake ones. What a toolchain needs from the real machine (its binary, Volta's real directory) is only read.

type box struct {
	t                   *testing.T
	home, harness, work string
	outside, scratch    string
	session             *Session
	settings            Settings
	realEnv             map[string]string
}

func newBox(t *testing.T) *box {
	t.Helper()
	if _, err := os.Stat(sandboxExec); err != nil {
		t.Skip("no sandbox-exec on this machine")
	}
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	b := &box{t: t, home: filepath.Join(root, "home"), harness: filepath.Join(root, "home", ".harness"),
		work: filepath.Join(root, "work"), outside: filepath.Join(root, "outside"), scratch: filepath.Join(root, "scratch"),
		realEnv: map[string]string{}}
	for _, d := range []string{b.home, b.harness, b.work, b.outside, b.scratch} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b.settings = Settings{Enabled: true}
	b.session = &Session{Home: b.home, HarnessHome: b.harness, Settings: func() Settings { return b.settings },
		Getenv: func(k string) string { return b.realEnv[k] }}
	t.Cleanup(b.session.Close)
	return b
}

func (b *box) write(path, content string) string {
	b.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		b.t.Fatal(err)
	}
	return path
}

func (b *box) cmd(kind Kind, script string) *exec.Cmd {
	b.t.Helper()
	w, err := b.session.Wrap(Request{Kind: kind, WorkDirs: []string{b.work}, ScratchDir: b.scratch}, []string{"/bin/sh", "-c", script})
	if err != nil {
		b.t.Fatal(err)
	}
	if w == nil {
		c := exec.Command("/bin/sh", "-c", script)
		c.Dir = b.work
		c.Env = append(os.Environ(), "HOME="+b.home)
		return c
	}
	c := exec.Command(w.Argv[0], w.Argv[1:]...)
	c.Dir = b.work
	c.Env = append(append(os.Environ(), "HOME="+b.home), w.Env...)
	return c
}

// sh runs script in the sandbox and returns its combined output and whether it succeeded.
func (b *box) sh(script string) (string, bool) {
	b.t.Helper()
	var out bytes.Buffer
	c := b.cmd(KindCommand, script)
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	return out.String(), err == nil
}

func (b *box) ok(script string) string {
	b.t.Helper()
	out, ok := b.sh(script)
	if !ok {
		b.t.Fatalf("%s failed:\n%s", script, out)
	}
	return out
}

func (b *box) refused(script string) string {
	b.t.Helper()
	out, ok := b.sh(script)
	if ok {
		b.t.Fatalf("%s should have been refused:\n%s", script, out)
	}
	return out
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestSeatbeltWrites(t *testing.T) {
	b := newBox(t)
	b.ok("echo hi > inside.txt && mkdir -p a/b && mv inside.txt a/b/moved.txt && rm a/b/moved.txt")
	b.ok("echo hi > " + b.scratch + "/note")
	b.ok(`echo hi > "$TMPDIR/note"`)
	b.ok("echo hi > /tmp/harness-sandbox-test-$$ && rm /tmp/harness-sandbox-test-$$")

	target := filepath.Join(b.outside, "evil")
	out := b.refused("echo hi > " + target)
	if !strings.Contains(strings.ToLower(out), "operation not permitted") {
		t.Fatalf("unexpected refusal: %s", out)
	}
	if exists(target) {
		t.Fatal("a file appeared outside")
	}
	// Under the temp directory the real TMPDIR is not ours to write: only the session's own.
	b.refused("echo hi > " + filepath.Join(filepath.Dir(b.work), "sibling"))
}

func TestSeatbeltAllowedWritePath(t *testing.T) {
	b := newBox(t)
	b.settings.AllowedWritePaths = []string{b.outside, "~/extra"}
	b.ok("echo hi > " + b.outside + "/ok")
	b.ok("mkdir -p " + b.home + "/extra/x")

	// A protected path wins over the list.
	b.settings.AllowedWritePaths = []string{"~/.ssh", b.home}
	b.refused("echo hi > " + b.home + "/.ssh/config")
	b.refused("echo hi > " + b.home + "/.zshrc")
	b.ok("echo hi > " + b.home + "/plain")
}

func TestSeatbeltProtectedPaths(t *testing.T) {
	b := newBox(t)
	// The working directory is the home directory: the worst case for rules that rely on the roots alone.
	b.settings.AllowedWritePaths = []string{b.home}
	for _, p := range []string{
		".ssh/authorized_keys", ".zshrc", ".bashrc", ".profile", ".gitconfig", ".harness/computer-use/settings.json",
		".harness/agent-token", ".claude/settings.json", "Library/LaunchAgents/evil.plist",
	} {
		b.refused("mkdir -p $(dirname " + b.home + "/" + p + "); echo hi > " + b.home + "/" + p)
		if exists(filepath.Join(b.home, p)) {
			t.Fatalf("%s was written", p)
		}
	}
	// A HARNESS_HOME elsewhere is covered by its own path.
	other := filepath.Join(filepath.Dir(b.home), "harness-elsewhere")
	b.session.HarnessHome = other
	b.settings.AllowedWritePaths = []string{filepath.Dir(other)}
	b.refused("mkdir -p " + other + " && echo hi > " + other + "/agent-token")
}

func TestSeatbeltProjectSettingsAndHooks(t *testing.T) {
	b := newBox(t)
	b.write(filepath.Join(b.work, ".git", "hooks", "pre-commit"), "#!/bin/sh\n")
	b.write(filepath.Join(b.work, ".git", "config"), "[core]\n")
	for _, p := range []string{".git/hooks/pre-commit", ".git/hooks/new", ".git/config", ".claude/settings.json", ".claude/settings.local.json", ".harness/settings.json", ".harness/settings.local.json", ".mcp.json", ".harness/anything", ".claude/commands/x.md", "sub/.claude/settings.json"} {
		b.refused("mkdir -p $(dirname " + p + "); echo hi >> " + p)
	}
	// .git itself cannot be moved aside to edit its hooks, nor its parent's rule dodged.
	b.refused("mv .git .git2")
	b.refused("mv .git/hooks .git/hooks2")
	b.refused("rm -rf .git/hooks")
	// Git's lock file dance does not get around the config.
	b.ok("echo x > .git/config.lock")
	b.refused("mv .git/config.lock .git/config")
	// The rest of .git works.
	b.ok("echo x > .git/ORIG_HEAD && rm .git/ORIG_HEAD")
	// A .git file would send git elsewhere.
	b.ok("mkdir -p sub2")
	b.refused("echo 'gitdir: /tmp/evil' > sub2/.git")
}

// Tests that make a repository in their temp directory and remove it again are ordinary. The temp tree is the command's.
func TestSeatbeltTempTreeIsFree(t *testing.T) {
	b := newBox(t)
	g := "git -c user.name=t -c user.email=t@example.test -c init.defaultBranch=main "
	b.ok(`cd "$TMPDIR" && ` + g + `init -q r && cd r && ` + g + `commit -q --allow-empty -m x && mkdir -p .git/hooks && echo x > .git/hooks/h && cd .. && rm -rf r`)
	b.ok(`cd "$TMPDIR" && mkdir -p p/.claude && echo '{}' > p/.claude/settings.json && rm -rf p`)
	// But not the shell state that runs outside the sandbox, nor what a later git init copies.
	for _, kind := range []string{"zsh", "bash"} {
		b.refused("echo evil > " + filepath.Join(b.scratch, "shell-snapshot."+kind))
	}
	b.refused("mkdir -p " + filepath.Join(b.scratch, "git-template", "hooks"))
	b.ok("true") // Wrap made the template.
	if err := os.WriteFile(filepath.Join(b.scratch, "shell-snapshot.zsh"), []byte("# snapshot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.refused("mv " + filepath.Join(b.scratch, "shell-snapshot.zsh") + " " + filepath.Join(b.scratch, "moved"))
	b.refused("rm " + filepath.Join(b.scratch, "shell-snapshot.zsh"))
	// The project's own are still closed, in the same breath.
	b.refused("mkdir -p .claude; echo '{}' > .claude/settings.json")
}

func TestSeatbeltLinksCannotReachProtectedFiles(t *testing.T) {
	b := newBox(t)
	key := b.write(filepath.Join(b.home, ".ssh", "id_ed25519"), "FAKE KEY\n")
	rc := b.write(filepath.Join(b.home, ".zshrc"), "# rc\n")
	hook := b.write(filepath.Join(b.work, ".git", "hooks", "pre-commit"), "#!/bin/sh\n")
	if out, ok := b.sh("ln " + key + " stolen; cat stolen"); ok || strings.Contains(out, "FAKE KEY") {
		t.Fatalf("hard link to a key worked: %s", out)
	}
	b.refused("ln " + rc + " linked")
	b.refused("ln " + hook + " linked-hook")
	// A symlink is resolved by the kernel, so it reaches nothing new.
	if out, ok := b.sh("ln -s " + key + " sym; cat sym"); ok || strings.Contains(out, "FAKE KEY") {
		t.Fatalf("symlink to a key worked: %s", out)
	}
	if out, _ := b.sh("ln -s " + rc + " symrc; echo evil >> symrc"); strings.Contains(read(t, rc), "evil") {
		t.Fatalf("write through a symlink reached the rc file: %s", out)
	}
	// Links among a command's own files work.
	b.ok("echo x > a && ln a b && cat b")
}

func TestSeatbeltSecretsUnreadable(t *testing.T) {
	b := newBox(t)
	secrets := []string{".ssh/id_ed25519", ".aws/credentials", ".gnupg/private-keys", "Library/Keychains/login.keychain-db",
		".netrc", ".npmrc", ".git-credentials", ".docker/config.json", ".kube/config", ".config/gh/hosts.yml",
		".harness/credentials.json", ".harness/agent-token", ".harness/local-provider-keys.json", ".harness/environments/x"}
	for _, s := range secrets {
		p := b.write(filepath.Join(b.home, s), "FAKE SECRET\n")
		out, ok := b.sh("cat " + p)
		if ok || strings.Contains(out, "FAKE SECRET") {
			t.Fatalf("%s was readable: %s", s, out)
		}
	}
	// Other files of the home directory and of HARNESS_HOME are.
	if out := b.ok("cat " + b.write(filepath.Join(b.home, "notes"), "fine\n")); !strings.Contains(out, "fine") {
		t.Fatal(out)
	}
	if out := b.ok("cat " + b.write(filepath.Join(b.harness, "plans", "p.md"), "plan\n")); !strings.Contains(out, "plan") {
		t.Fatal(out)
	}
	// ls of the home directory does not trip over a store it may not read.
	if out := b.ok("ls -la " + b.home); !strings.Contains(out, ".ssh") {
		t.Fatal(out)
	}
	// The default macOS volume takes any case: the same file under other spelling stays closed.
	if out, ok := b.sh("cat " + filepath.Join(b.home, ".SSH", "id_ed25519")); ok || strings.Contains(out, "FAKE SECRET") {
		t.Fatalf("a different case read the key: %s", out)
	}
}

func TestSeatbeltChildInherits(t *testing.T) {
	b := newBox(t)
	target := filepath.Join(b.outside, "child")
	b.refused(`/bin/sh -c '/bin/sh -c "echo hi > ` + target + `"'`)
	b.refused(`python3 -c 'import os; f=os.open("` + target + `", os.O_CREAT|os.O_WRONLY)' 2>&1 || exit 1`)
	if exists(target) {
		t.Fatal("a child wrote outside")
	}
	// A detached child too: it keeps the sandbox after its parent is gone.
	b.ok(`(nohup sh -c 'sleep 0.2; echo hi > ` + target + `' >/dev/null 2>&1 &) ; exit 0`)
	time.Sleep(500 * time.Millisecond)
	if exists(target) {
		t.Fatal("a detached child wrote outside")
	}
}

func TestSeatbeltOffSwitch(t *testing.T) {
	b := newBox(t)
	target := filepath.Join(b.outside, "off")
	b.refused("echo hi > " + target)
	b.settings.Enabled = false
	if w, err := b.session.Wrap(Request{WorkDirs: []string{b.work}, ScratchDir: b.scratch}, []string{"/bin/true"}); w != nil || err != nil {
		t.Fatalf("the sandbox should not apply: %v %v", w, err)
	}
	b.ok("echo hi > " + target)
	if !exists(target) {
		t.Fatal("off did not take")
	}
	if st := b.session.State(); st.Active || st.Refusing {
		t.Fatalf("state: %+v", st)
	}
	// A policy document wins over the switch.
	b.settings.Forced = true
	if st := b.session.State(); !st.Active {
		t.Fatalf("forced: %+v", st)
	}
	b.refused("echo hi >> " + target)
}

// upstream is a server standing in for the internet, on the loopback interface. The list names it by address, which is how
// a private address can be allowed at all.
func upstream(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "hello from upstream") }))
	t.Cleanup(srv.Close)
	return srv, strings.TrimPrefix(srv.URL, "http://")
}

func TestSeatbeltNetwork(t *testing.T) {
	b := newBox(t)
	srv, addr := upstream(t)

	// Direct: refused by the kernel, without the proxy.
	out := b.refused("curl --noproxy '*' -sS --max-time 5 " + srv.URL)
	if !strings.Contains(strings.ToLower(out), "not permitted") && !strings.Contains(out, "Failed to connect") && !strings.Contains(out, "Couldn't connect") {
		t.Logf("direct refusal: %s", out)
	}
	// Through the proxy, not listed: a 403 from the proxy, remembered.
	w, err := b.session.Wrap(Request{WorkDirs: []string{b.work}, ScratchDir: b.scratch}, []string{"/usr/bin/curl", "--noproxy", "", "-sS", "--fail", "--max-time", "5", srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(w.Argv[0], w.Argv[1:]...)
	c.Env = append(os.Environ(), w.Env...)
	if o, err := c.CombinedOutput(); err == nil {
		t.Fatalf("not on the list, but it went through: %s", o)
	}
	if got := w.BlockedHosts(); len(got) != 1 || got[0] != addr {
		t.Fatalf("blocked hosts: %v", got)
	}
	// On the list: through.
	b.settings.AllowedHosts = []string{addr}
	if out := b.ok(`curl --noproxy "" -sS --fail --max-time 5 ` + srv.URL); !strings.Contains(out, "hello from upstream") {
		t.Fatal(out)
	}
	// The list does not open the address directly.
	b.refused("curl --noproxy '*' -sS --max-time 5 " + srv.URL)
}

func TestSeatbeltProxyRefusesNamesOfPrivateAddresses(t *testing.T) {
	b := newBox(t)
	_, addr := upstream(t)
	_, port, _ := net.SplitHostPort(addr)
	// localhost is on the list by name: it is the same thing as an address, so that one works.
	b.settings.AllowedHosts = []string{"localhost:" + port}
	if out := b.ok(`curl --noproxy "" -sS --fail --max-time 5 http://localhost:` + port); !strings.Contains(out, "hello from upstream") {
		t.Fatal(out)
	}
	// Any other way of naming a private address is not: localhost.localdomain style names resolve at connect time.
	b.settings.AllowedHosts = []string{"*.localhost:" + port}
	if out, ok := b.sh(`curl --noproxy "" -sS --fail --max-time 5 http://x.localhost:` + port); ok {
		t.Fatalf("a wildcard reached the loopback interface: %s", out)
	}
}

func TestSeatbeltDNSAndUnixSockets(t *testing.T) {
	b := newBox(t)
	// No resolver: a command asks the proxy for names.
	b.refused(`python3 -c 'import socket; socket.getaddrinfo("example.com", 443)'`)
	// A socket somewhere else on this machine is not reachable.
	root, err := os.MkdirTemp("/tmp", "hsb")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	ln, err := net.Listen("unix", filepath.Join(root, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client := `import socket,sys; s=socket.socket(socket.AF_UNIX); s.connect(sys.argv[1]); print("connected")`
	b.refused(`python3 -c '` + client + `' ` + filepath.Join(root, "s.sock"))
	// A command's own socket in its own folder works.
	own := `import socket; s=socket.socket(socket.AF_UNIX); s.bind("own.sock"); s.listen(1); c=socket.socket(socket.AF_UNIX); c.connect("own.sock"); print("own ok")`
	if out := b.ok(`python3 -c '` + own + `'`); !strings.Contains(out, "own ok") {
		t.Fatal(out)
	}
}

func TestSeatbeltLocalhost(t *testing.T) {
	b := newBox(t)
	server := `import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); s.listen(1); c=socket.socket(); c.connect(s.getsockname()); print("loop ok")`
	out := b.refused(`python3 -c '` + server + `'`)
	if !strings.Contains(strings.ToLower(out), "not permitted") {
		t.Fatalf("a readable failure: %s", out)
	}
	b.settings.AllowLocalhost = true
	if out := b.ok(`python3 -c '` + server + `'`); !strings.Contains(out, "loop ok") {
		t.Fatal(out)
	}
}

func TestSeatbeltPreviewServer(t *testing.T) {
	b := newBox(t)
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
	defer func() { _ = c.Process.Kill(); _ = c.Wait() }()
	url := fmt.Sprintf("http://127.0.0.1:%d/index.html", port)
	var body string
	for i := 0; i < 50; i++ {
		res, err := http.Get(url)
		if err == nil {
			var b bytes.Buffer
			_, _ = b.ReadFrom(res.Body)
			res.Body.Close()
			body = b.String()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(body, "served page") {
		t.Fatalf("no page: %q, server said %s", body, out.String())
	}
	// The same file rules hold.
	c2 := b.cmd(KindServer, "echo hi > "+b.outside+"/from-server")
	if err := c2.Run(); err == nil {
		t.Fatal("a preview server wrote outside")
	}
}

func TestSeatbeltToolchain(t *testing.T) {
	b := newBox(t)
	for _, k := range []string{"VOLTA_HOME"} {
		b.realEnv[k] = os.Getenv(k)
	}
	if b.realEnv["VOLTA_HOME"] == "" {
		if h, _ := os.UserHomeDir(); h != "" {
			if _, err := os.Stat(filepath.Join(h, ".volta")); err == nil {
				b.realEnv["VOLTA_HOME"] = filepath.Join(h, ".volta")
			}
		}
	}
	env := "export GOTOOLCHAIN=local GOFLAGS=-mod=mod GOPATH=" + b.home + "/go; "
	if b.realEnv["VOLTA_HOME"] != "" {
		env += "export VOLTA_HOME=" + b.realEnv["VOLTA_HOME"] + "; "
	}
	if _, err := exec.LookPath("go"); err == nil {
		b.write(filepath.Join(b.work, "go.mod"), "module example.test/x\n\ngo 1.21\n")
		b.write(filepath.Join(b.work, "main.go"), "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"built and ran\") }\n")
		if out := b.ok(env + "go build -o app . && ./app"); !strings.Contains(out, "built and ran") {
			t.Fatal(out)
		}
		// cgo goes through clang and xcrun.
		b.write(filepath.Join(b.work, "cgo", "main.go"), "package main\n\n// int two(void) { return 2; }\nimport \"C\"\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"cgo\", C.two()) }\n")
		if out := b.ok(env + "cd cgo && CGO_ENABLED=1 go run main.go"); !strings.Contains(out, "cgo 2") {
			t.Fatal(out)
		}
		// Installing to a directory on PATH is the one thing a cache must not become.
		b.refused(env + "mkdir -p $GOPATH/bin && go build -o $GOPATH/bin/x .")
	}
	if _, err := exec.LookPath("git"); err == nil {
		g := "git -c user.name=t -c user.email=t@example.test -c init.defaultBranch=main "
		// In the working directory itself, a new repository's config is refused like an existing one's, so make it here
		// and work in it.
		repo := filepath.Join(b.work, "repo")
		out := exec.Command("git", "init", "-q", repo)
		if o, err := out.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, o)
		}
		b.session = &Session{Home: b.home, HarnessHome: b.harness, Settings: b.session.Settings, Getenv: b.session.Getenv}
		t.Cleanup(b.session.Close)
		work := b.work
		b.work = repo
		b.write(filepath.Join(repo, "a.txt"), "a\n")
		o := b.ok(g + "status --short && " + g + "add a.txt && " + g + "commit -qm first && " + g + "log --oneline && " + g + "branch feature && " + g + "stash list")
		if !strings.Contains(o, "first") {
			t.Fatal(o)
		}
		// Writing the config of the repository it works in is refused, with a message that names it.
		if o := b.refused(g + "config core.hooksPath /tmp/x"); !strings.Contains(o, "config") {
			t.Fatal(o)
		}
		// A new repository in a subfolder (a clone, say) works, without hooks.
		o = b.ok(g + "init -q sub && ls sub/.git && cd sub && " + g + "commit -q --allow-empty -m one")
		if strings.Contains(o, "hooks") {
			t.Fatalf("a hooks folder appeared: %s", o)
		}
		b.ok(g + "clone -q sub cloned && ls cloned/.git")
		b.work = work
	}
	if _, err := exec.LookPath("node"); err == nil {
		if out := b.ok(env + `node -e 'require("fs").writeFileSync("n.txt","ok"); console.log("node ok")'`); !strings.Contains(out, "node ok") {
			t.Fatal(out)
		}
		b.write(filepath.Join(b.home, ".npmrc"), "//registry.npmjs.org/:_authToken=FAKE\n")
		if out := b.ok(env + "npm --version"); strings.TrimSpace(out) == "" {
			t.Fatal("npm printed nothing")
		}
	}
}

func TestSeatbeltLinkedWorktreeCanCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	b := newBox(t)
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
	// Its hooks and config are still closed, although the git directory is a root now.
	b.refused("echo x > " + filepath.Join(main, ".git", "hooks", "pre-commit"))
	b.refused("echo x >> " + filepath.Join(main, ".git", "config"))
	// And the rest of the main checkout is not writable.
	b.refused("echo x > " + filepath.Join(main, "elsewhere"))
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
