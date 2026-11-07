package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/decision"
)

// renamedTempRoot is a scratch folder that is nobody's writable root. On Linux CI it lands in /tmp, which every policy
// writes to (the temp of programs), so a BuildPolicy over it makes the whole folder writable and no refusal below /tmp
// has a reason. macOS keeps it under /private/var/folders, which is why the tests passed locally.
func renamedTempRoot(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(filepath.Dir(t.TempDir()), filepath.Base(t.TempDir())+"-away", "work")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(root)) })
	return root
}

func TestSeatbeltProfileIsAPureRendering(t *testing.T) {
	p := Policy{
		Write: []string{"/work", `/odd "quoted\" dir`}, Mkdir: []string{"/home/go"},
		WriteDeny: []string{"/home/.ssh"}, WriteAllowAfterDeny: []string{"/hh/chats/one"}, WriteDenyLiteral: []string{"/work/.git/config"},
		ReadDeny: []string{"/home/.aws"}, Sockets: []string{"/work"}, ProxyPort: 1234,
	}
	profile := SeatbeltProfile(p)
	if profile != SeatbeltProfile(p) {
		t.Fatal("the same policy rendered differently")
	}
	order := []string{
		"(deny default)",
		`(allow file-write* (subpath "/work"))`,
		`(subpath "/odd \"quoted\\\" dir")`,
		`(deny file-write* (regex #"/\.(claude|harness)(/|$)"))`,
		`(deny file-write* (subpath "/home/.ssh"))`,
		`(allow file-write* (subpath "/hh/chats/one"))`,
		`(deny file-write* (regex #"/\.git/(.*/)?hooks(/|$)"))`,
		`(literal "/work/.git/config")`,
		`(deny file-read-data (subpath "/home/.aws"))`,
		`(allow network-outbound (remote ip "localhost:1234"))`,
	}
	at := -1
	for _, want := range order {
		i := strings.Index(profile, want)
		if want == order[2] {
			// A quoted path is rendered inside the rule before it.
			i = strings.Index(profile, strings.TrimSuffix(strings.TrimPrefix(want, "(subpath "), ")"))
		}
		if i < 0 {
			t.Fatalf("missing %s in\n%s", want, profile)
		}
		if i < at {
			t.Fatalf("%s comes too early: the last matching rule wins\n%s", want, profile)
		}
		at = i
	}
	// Protections hold against renaming what is above them.
	if !strings.Contains(profile, `(deny file-write-unlink`) || !strings.Contains(profile, `(literal "/work/.git")`) || !strings.Contains(profile, `(literal "/home")`) {
		t.Fatalf("no protection of the directories above:\n%s", profile)
	}
	// Nothing opens the network by itself.
	for _, off := range []string{"network-bind", "network-inbound", `remote ip "localhost:*"`} {
		if strings.Contains(strings.ReplaceAll(profile, "unix-socket", ""), off+" (local ip") || strings.Contains(profile, off+`"`) {
			t.Fatalf("%s without a setting:\n%s", off, profile)
		}
	}
	if s := SeatbeltProfile(Policy{Localhost: true}); !strings.Contains(s, `(allow network-bind (local ip "localhost:*"))`) || strings.Contains(s, `local ip "*:*"`) {
		t.Fatalf("localhost:\n%s", s)
	}
	if s := SeatbeltProfile(Policy{Server: true}); !strings.Contains(s, `(allow network-bind (local ip "*:*"))`) {
		t.Fatalf("server:\n%s", s)
	}
	// The services that start something outside the sandbox stay off the list.
	for _, s := range []string{"launchservicesd", "appleevents", "SecurityServer", "securityd", "pasteboard", "windowserver", "tccd"} {
		if strings.Contains(strings.ToLower(profile), strings.ToLower(s)) {
			t.Fatalf("%s is reachable:\n%s", s, profile)
		}
	}
}

func TestBuildPolicy(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	hh := filepath.Join(home, ".harness")
	work := filepath.Join(root, "work")
	chat := filepath.Join(hh, "chats", "s1")
	p := BuildPolicy(PolicyInput{Home: home, HarnessHome: hh, WorkDirs: []string{work, chat}, ScratchDir: filepath.Join(root, "scratch"), TempDir: filepath.Join(root, "scratch", "tmp"),
		ExtraWrite: []string{"~/extra", "relative/ignored"}, Getenv: func(k string) string {
			if k == "GOMODCACHE" {
				return filepath.Join(root, "mods")
			}
			return ""
		}, ProxyPort: 9})
	for _, want := range []string{work, filepath.Join(root, "scratch"), filepath.Join(home, "extra"), systemTmp, filepath.Join(root, "mods"), filepath.Join(home, ".npm")} {
		if !slices.Contains(p.Write, want) {
			t.Errorf("%s is not writable: %v", want, p.Write)
		}
	}
	if slices.ContainsFunc(p.Write, func(s string) bool { return strings.Contains(s, "relative") }) {
		t.Errorf("a relative path got in: %v", p.Write)
	}
	// Install locations are not caches.
	for _, no := range []string{"go", "go/bin", ".cargo", ".cargo/bin", ".volta/bin", ".local/bin"} {
		if slices.Contains(p.Write, filepath.Join(home, no)) {
			t.Errorf("%s must not be writable", no)
		}
	}
	if !slices.Contains(p.Mkdir, filepath.Join(home, ".cargo")) {
		t.Errorf("the folder above a cache can be made: %v", p.Mkdir)
	}
	// /tmp is shared with everything else on the machine: no sockets there.
	if slices.Contains(p.Sockets, systemTmp) {
		t.Error("sockets in /tmp")
	}
	if !slices.Contains(p.WriteAllowAfterDeny, chat) {
		t.Errorf("a chat's folder must stay writable: %v", p.WriteAllowAfterDeny)
	}
	if got := p.Why(filepath.Join(chat, "f"), true); got != "" {
		t.Errorf("chat folder: %s", got)
	}
	cases := []struct {
		path  string
		write bool
		want  string
	}{
		{filepath.Join(work, "ok.txt"), true, ""},
		{filepath.Join(work, ".git", "hooks", "x"), true, "protected file"},
		{filepath.Join(work, ".git", "config"), true, "programs run"},
		{filepath.Join(work, ".harness", "settings.local.json"), true, "protected file"},
		{filepath.Join(work, ".claude", "x.md"), true, ".claude"},
		{filepath.Join(work, ".harness", "worktrees", "w", "f"), true, ""},
		{filepath.Join(home, ".ssh", "id_rsa"), false, "credential"},
		{filepath.Join(home, "notes"), false, ""},
		{filepath.Join(home, "notes"), true, "outside the writable"},
		{filepath.Join(home, ".zshrc"), true, "protected path"},
		{filepath.Join(hh, "computer-use", "settings.json"), true, "protected path"},
		{filepath.Join(hh, "agent-token"), false, "credential"},
		{filepath.Join(hh, "plans", "p.md"), false, ""},
	}
	for _, c := range cases {
		got := p.Why(c.path, c.write)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("Why(%s, %v) = %q, want %q", c.path, c.write, got, c.want)
		}
	}
}

func TestGitDirsOfLinkedWorktree(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(root, "main", ".git")
	gd := filepath.Join(common, "worktrees", "w")
	wt := filepath.Join(root, "w", "sub")
	for _, d := range []string{gd, wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "w", ".git"), []byte("gitdir: "+gd+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gd, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitDirs(wt); !slices.Equal(got, []string{gd, common}) {
		t.Fatalf("got %v", got)
	}
	if got := gitDirs(filepath.Join(root, "main")); !slices.Equal(got, []string{common}) {
		t.Fatalf("got %v", got)
	}
}

// The stores a profile spells out are the ones decision.IsSecretPath knows. .ssh and .aws are closed whole, where the
// pattern names their key files; a store on either list and not the other should be a decision, not an accident.
func TestHomeReadDenyAreSecrets(t *testing.T) {
	inside := map[string]string{".ssh": ".ssh/id_rsa", ".aws": ".aws/credentials", ".gnupg": ".gnupg/x", "Library/Keychains": "Library/Keychains/x",
		".config/gcloud": ".config/gcloud/x", ".azure": ".azure/x", ".password-store": ".password-store/x"}
	for _, n := range homeReadDeny {
		p := n
		if v, ok := inside[n]; ok {
			p = v
		}
		if !decision.IsSecretPath("/home/u/" + p) {
			t.Errorf("%s is closed to commands, but decision.IsSecretPath does not call it a secret", p)
		}
	}
}

func TestToolchainPathsAreInOnePlaceAndReasoned(t *testing.T) {
	list := toolchainPaths("/home/u", func(k string) string {
		if k == "GOPATH" {
			return "/gp"
		}
		return ""
	})
	var got []string
	for _, tp := range list {
		if tp.Why == "" {
			t.Errorf("%s has no reason", tp.Path)
		}
		got = append(got, tp.Path)
	}
	for _, want := range []string{"/gp/pkg/mod", "/gp/pkg/sumdb", "/home/u/.npm", "/home/u/Library/pnpm/store", "/home/u/.cache", "/home/u/Library/Caches/go-build"} {
		if !slices.Contains(got, want) {
			t.Errorf("no %s in %v", want, got)
		}
	}
}

func TestHosts(t *testing.T) {
	h := NewHosts([]string{"example.com", "*.wild.test", "ports.test:8080", "127.0.0.1:9000", "not a host", "", "http://nope", "*"})
	cases := []struct {
		host    string
		port    int
		ok      bool
		private bool
	}{
		{"example.com", 443, true, false},
		{"EXAMPLE.com", 80, true, false},
		{"example.com", 22, false, false},
		{"sub.example.com", 443, false, false},
		{"a.wild.test", 443, true, false},
		{"wild.test", 443, false, false},
		{"evilwild.test", 443, false, false},
		{"ports.test", 8080, true, false},
		{"ports.test", 443, false, false},
		{"127.0.0.1", 9000, true, true},
		{"127.0.0.1", 9001, false, false},
		{"localhost", 443, false, false},
	}
	for _, c := range cases {
		if ok, private := h.allows(c.host, c.port); ok != c.ok || private != c.private {
			t.Errorf("%s:%d = %v %v", c.host, c.port, ok, private)
		}
	}
	if ValidHost("*") || ValidHost("a b") || !ValidHost("[::1]:80") {
		t.Error("ValidHost")
	}
	// The built-in list stays usable and small.
	for _, d := range DefaultHosts {
		if !ValidHost(d) {
			t.Errorf("default host %s", d)
		}
	}
}

func TestStub(t *testing.T) {
	d := stubDriver{platform: "testos"}
	if d.Info().Supported || !strings.Contains(d.Info().Reason, "testos") {
		t.Fatalf("%+v", d.Info())
	}
	// Off, or on without a driver: it runs as is, and says why.
	s := &Session{Driver: d, Settings: func() Settings { return Settings{Enabled: true} }}
	if st := s.State(); st.Active || st.Refusing || st.Reason == "" {
		t.Fatalf("%+v", st)
	}
	if w, err := s.Wrap(Request{ScratchDir: t.TempDir()}, []string{"true"}); w != nil || err != nil {
		t.Fatal(w, err)
	}
	// Required without a driver: nothing runs.
	s = &Session{Driver: d, Settings: func() Settings { return Settings{Enabled: true, Forced: true} }}
	if st := s.State(); !st.Refusing {
		t.Fatalf("%+v", st)
	}
	if _, err := s.Wrap(Request{ScratchDir: t.TempDir()}, []string{"true"}); err == nil {
		t.Fatal("a required sandbox without a driver must refuse")
	}
	// No session: no sandbox.
	var none *Session
	if st := none.State(); st.Active {
		t.Fatal(st)
	}
	if w, err := none.Wrap(Request{}, []string{"true"}); w != nil || err != nil {
		t.Fatal(w, err)
	}
}

func TestDiagnose(t *testing.T) {
	root := renamedTempRoot(t)
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	w := &Wrapped{Policy: BuildPolicy(PolicyInput{Home: home, HarnessHome: filepath.Join(home, ".harness"), WorkDirs: []string{work}, Getenv: func(string) string { return "" }})}
	outside := filepath.Join(root, "elsewhere", "f")
	cases := []struct {
		name, output string
		failed       bool
		want         string
	}{
		{"shell", "/bin/sh: " + outside + ": Operation not permitted", true, outside},
		{"go", "open " + outside + ": operation not permitted", true, outside},
		{"node", "Error: EPERM: operation not permitted, open '" + outside + "'", true, outside},
		{"tilde", "touch: ~/.zshrc: Operation not permitted", true, "~/.zshrc"},
		{"relative", "error: could not lock config file .git/config: Operation not permitted", true, ".git/config"},
		{"secret", "cat: " + filepath.Join(home, ".ssh", "id_rsa") + ": Permission denied", true, "credential store"},
		{"ssh", "ssh: Could not resolve hostname github.com: nodename nor servname provided\nfatal: Could not read from remote repository.", true, "SSH"},
		{"dns", "curl: (6) Could not resolve host: example.com", true, "direct network"},
		{"go dial", "dial tcp 1.1.1.1:443: connect: operation not permitted", true, "direct network"},
		{"bind", "listen tcp 127.0.0.1:0: bind: operation not permitted", true, "local port"},
		{"docker", "Cannot connect to the Docker daemon at unix:///var/run/docker.sock.", true, "local port"},
		{"proxy", "harness sandbox: example.org is not on the allowed hosts", true, "example.org"},
		{"proxy tolerated", "harness sandbox: example.org is not on the allowed hosts", false, "example.org"},
		{"keychain", "fatal: could not read Username for 'https://github.com': Device not configured", true, "stored credentials"},
		{"unnamed", "something: Operation not permitted", true, "does not say what"},
	}
	for _, c := range cases {
		d := w.Diagnose(c.output, work, c.failed)
		if d == nil {
			t.Errorf("%s: no diagnosis", c.name)
			continue
		}
		note := d.Note(OutsideParamForTest)
		if !strings.Contains(note, c.want) || !strings.Contains(note, "[sandbox]") || !strings.Contains(note, "do not retry") || !strings.Contains(note, OutsideParamForTest) {
			t.Errorf("%s: %s", c.name, note)
		}
		if d.Summary() == "" {
			t.Errorf("%s: no summary", c.name)
		}
	}
	for name, out := range map[string]string{
		"plain failure":      "FAIL: expected 2, got 3",
		"own mode":           "cat: " + filepath.Join(work, "f") + ": Permission denied",
		"mode elsewhere":     "cat: /etc/sudoers: Permission denied",
		"allowed path":       "rm: " + filepath.Join(work, "f") + ": Operation not permitted",
		"success with eperm": "",
	} {
		if d := w.Diagnose(out, work, name != "success with eperm"); d != nil && (len(d.Paths) > 0 || d.Network || d.SSH || d.Local) {
			t.Errorf("%s: %+v", name, d)
		}
	}
	// A command that succeeded and printed an EPERM dealt with it.
	if d := w.Diagnose("warning: "+outside+": Operation not permitted", work, false); d != nil {
		t.Errorf("success: %+v", d)
	}
	// xcrun's complaint about its cache is not a failure of the command.
	if d := w.Diagnose("git: error: couldn't create cache file '/var/folders/x/T/xcrun_db-abc' (errno=Operation not permitted)", work, true); d != nil {
		t.Errorf("xcrun: %+v", d)
	}
	var none *Wrapped
	if none.Diagnose("x: Operation not permitted", work, true) != nil {
		t.Error("no sandbox, no diagnosis")
	}
}

// OutsideParamForTest stands for tools.OutsideSandboxParam, which this package does not know.
const OutsideParamForTest = "run_outside"
