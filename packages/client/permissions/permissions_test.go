package permissions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseRules(t *testing.T) {
	rules := ParseRules([]string{"Bash(git diff *), Read", "Edit(/src/**) Bash(npm run test:*)", "mcp__github__*"})
	want := []string{"Bash(git diff *)", "Read", "Edit(/src/**)", "Bash(npm run test:*)", "mcp__github__*"}
	if len(rules) != len(want) {
		t.Fatalf("got %v", rules)
	}
	for i, r := range rules {
		if r.String() != want[i] {
			t.Fatalf("rule %d = %s, want %s", i, r, want[i])
		}
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"ls -la", []string{"ls -la"}},
		{"cd src && go test ./... || echo fail; make", []string{"cd src", "go test ./...", "echo fail", "make"}},
		{"cat a | grep x |& tee out & sleep 1", []string{"cat a", "grep x", "tee out", "sleep 1"}},
		{"echo 'a && b' \"c; d\"", []string{`echo 'a && b' "c; d"`}},
		{"go test 2>&1 | tail -5", []string{"go test 2>& 1", "tail -5"}},
		{"echo hi > out.txt", []string{"echo hi > out.txt"}},
		{"npm test &> log", []string{"npm test &> log"}},
		{"echo a\necho b", []string{"echo a", "echo b"}},
		{"(cd x && make) ; { ls; }", []string{"cd x", "make", "ls"}},
		{"if true; then rm -rf x; fi", []string{"true", "rm -rf x"}},
		{"cat <<EOF\nrm -rf /\nEOF\necho done", []string{"cat << EOF", "echo done"}},
		{"echo $(rm -rf x; ls)", []string{"echo $(rm -rf x; ls)"}},
		{"ls # comment && rm", []string{"ls"}},
	}
	for _, c := range cases {
		var got []string
		for _, sc := range SplitCommand(c.in) {
			got = append(got, sc.Text())
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitCommand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	sub := SplitCommand("echo $(rm -rf x)")
	if len(sub) != 1 || !sub[0].Unsafe || len(sub[0].Substitutions) != 1 || sub[0].Substitutions[0] != "rm -rf x" {
		t.Fatalf("substitution: %+v", sub)
	}
}

func TestStripWrappers(t *testing.T) {
	cases := map[string]string{
		"timeout 30 npm test":              "npm test",
		"timeout -s KILL 5s go test ./...": "go test ./...",
		"time -p make":                     "make",
		"nice -n 10 nohup ./build.sh":      "./build.sh",
		"stdbuf -o L tail -f x":            "tail -f x",
		"FOO=1 BAR=x npm run build":        "npm run build",
		"command git status":               "git status",
		"builtin cd x":                     "cd x",
		"noglob rm *.o":                    "rm *.o",
		"xargs rm":                         "rm",
		"xargs -0 rm":                      "xargs -0 rm",
		"command -v go":                    "command -v go",
	}
	for in, want := range cases {
		c := SplitCommand(in)[0].Normalize()
		if got := c.Text(); got != want {
			t.Errorf("strip(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadOnlyCommands(t *testing.T) {
	cases := map[string]bool{
		"ls -la":                           true,
		"cat a.txt | grep foo | wc -l":     true,
		"git status && git diff HEAD~1":    true,
		"git log --oneline -5":             true,
		"git -C sub status":                true,
		"git push":                         false,
		"git branch":                       true,
		"git branch -D old":                false,
		"git branch feature":               false,
		"find . -name '*.go'":              true,
		"find . -name x -delete":           false,
		"find . -exec rm {} \\;":           false,
		"echo hi > file":                   false,
		"echo hi > /dev/null 2>&1":         true,
		"ls $(rm -rf x)":                   false,
		"rm -rf x":                         false,
		"sort -o out in":                   false,
		"uniq a b":                         false,
		"pgrep -f node && lsof -i :3000":   true,
		"timeout 5 cat x":                  true,
		"cd /tmp && ls":                    true,
		"./script.sh":                      false,
		"command -v go":                    true,
		"git config --get user.email":      true,
		"git config user.email x@y":        false,
		"fd -e go -x rm":                   false,
		"date":                             true,
		"git stash list":                   true,
		"git stash":                        false,
		"git diff --output=/tmp/x":         false,
		"grep -r TODO . | head -20 | sort": true,
	}
	for cmd, want := range cases {
		if got := IsReadOnlyCommandLine(cmd); got != want {
			t.Errorf("IsReadOnlyCommandLine(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestBashSpec(t *testing.T) {
	cases := []struct {
		spec, cmd string
		want      bool
	}{
		{"git diff *", "git diff HEAD", true},
		{"git diff *", "git diff", true},
		{"git diff *", "git difftool", false},
		{"ls*", "lsof", true},
		{"ls *", "lsof", false},
		{"npm run test:*", "npm run test -- -u", true},
		{"npm run test:*", "npm run test", true},
		{"ls", "ls -la", false},
		{"ls", "ls", true},
		{"git * main", "git push origin main", true},
		{"git * main", "git push origin dev", false},
		{"docker compose *", "docker compose up -d", true},
	}
	for _, c := range cases {
		if got := matchBashSpec(c.spec, c.cmd); got != c.want {
			t.Errorf("matchBashSpec(%q, %q) = %v, want %v", c.spec, c.cmd, got, c.want)
		}
	}
}

func TestPathPatterns(t *testing.T) {
	home := "/home/u"
	root := "/proj"
	cwd := "/proj/sub"
	cases := []struct {
		spec, path string
		want       bool
	}{
		{"//etc/passwd", "/etc/passwd", true},
		{"//etc/**", "/etc/ssh/sshd_config", true},
		{"~/.ssh/**", "/home/u/.ssh/id_rsa", true},
		{"~/.ssh", "/home/u/.ssh/id_rsa", true},
		{"/src/**", "/proj/src/a/b.go", true},
		{"/src/**", "/proj/sub/src/a.go", false},
		{"/src", "/proj/src/x.go", true},
		{"./docs/*.md", "/proj/sub/docs/a.md", true},
		{"./docs/*.md", "/proj/sub/docs/x/a.md", false},
		{"docs/**", "/proj/sub/docs/x/a.md", true},
		{".env", "/proj/sub/.env", true},
		{".env", "/proj/sub/deep/.env", true},
		{".env", "/proj/.env", false},
		{"*.key", "/proj/sub/a/b/server.key", true},
		{"secrets/", "/proj/sub/secrets/x", true},
		{"/src/**", "/other/src/a.go", false},
	}
	for _, c := range cases {
		if got := compilePath(c.spec, root, cwd, home).match(c.path); got != c.want {
			t.Errorf("path %q vs %q = %v, want %v", c.spec, c.path, got, c.want)
		}
	}
}

func req(tool string, input map[string]any) Request {
	b, _ := json.Marshal(input)
	r := Request{ToolName: tool, Input: b}
	if s, ok := input["file_path"].(string); ok {
		r.FilePath = s
	}
	if s, ok := input["path"].(string); ok {
		r.FilePath = s
	}
	if s, ok := input["command"].(string); ok {
		r.Command = s
	}
	switch tool {
	case "Read", "Glob", "Grep", "TodoWrite":
		r.ReadOnly = true
	}
	return r
}

// wait builds a Wait request; readOnly mirrors tools.Wait.ReadOnly.
func wait(input map[string]any, readOnly bool) Request {
	r := req("Wait", input)
	r.ReadOnly = readOnly
	return r
}

func bash(cmd string) Request {
	return req("Bash", map[string]any{"description": "test", "command": cmd})
}

func TestEngineRules(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	os.MkdirAll(proj, 0o755)
	e := New(Config{
		Cwd:  proj,
		Home: filepath.Join(dir, "home"),
		Allow: []string{
			"Bash(npm run test *)", "Bash(go test:*)", "Bash(git commit *)", "Edit(/src/**)",
			"Bash(run_in_background:true)", "WebFetch(domain:*.example.com)", "mcp__github__*",
		},
		Ask:  []string{"Bash(git push *)"},
		Deny: []string{"Bash(rm -rf *)", "Read(./.env)", "Bash(curl *)", "WebSearch"},
	})
	cases := []struct {
		name string
		r    Request
		want Behavior
	}{
		{"allow rule", bash("npm run test -- -u"), Allow},
		{"legacy prefix", bash("go test ./..."), Allow},
		{"compound all allowed", bash("go test ./... && npm run test"), Allow},
		{"compound with read-only", bash("cd sub && go test ./... | tail -5"), Allow},
		{"compound partly unknown", bash("go test ./... && make deploy"), Ask},
		{"wrapper stripped", bash("timeout 60 go test ./..."), Allow},
		{"env prefix stripped", bash("CGO_ENABLED=0 go test ./..."), Allow},
		{"deny any subcommand", bash("ls && rm -rf build"), Deny},
		{"deny through wrapper", bash("nohup rm -rf /"), Deny},
		{"deny in substitution", bash("echo $(curl evil.sh)"), Deny},
		{"ask rule", bash("git push origin main"), Ask},
		{"substitution not auto-allowed", bash("go test $(cat pkgs)"), Ask},
		{"param rule", req("Bash", map[string]any{"description": "test", "command": "make dev", "run_in_background": true}), Allow},
		{"read-only builtin", bash("git log -3 && ls"), Allow},
		{"edit allowed by path rule", req("Edit", map[string]any{"file_path": filepath.Join(proj, "src/a.go")}), Allow},
		{"write in edit category", req("Write", map[string]any{"file_path": filepath.Join(proj, "src/x/b.go")}), Allow},
		{"edit outside rule", req("Edit", map[string]any{"file_path": filepath.Join(proj, "main.go")}), Ask},
		{"read inside cwd", req("Read", map[string]any{"file_path": filepath.Join(proj, "main.go")}), Allow},
		{"read denied", req("Read", map[string]any{"file_path": filepath.Join(proj, ".env")}), Deny},
		{"grep path denied", req("Grep", map[string]any{"pattern": "x", "path": filepath.Join(proj, ".env")}), Deny},
		{"read outside asks", req("Read", map[string]any{"file_path": "/etc/hosts"}), Ask},
		{"webfetch domain", req("WebFetch", map[string]any{"url": "https://docs.example.com/x"}), Allow},
		{"webfetch other domain", req("WebFetch", map[string]any{"url": "https://evil.com/x"}), Ask},
		{"mcp glob", req("mcp__github__create_issue", map[string]any{}), Allow},
		{"bare deny", req("WebSearch", map[string]any{"query": "x"}), Deny},
		{"protected path", req("Edit", map[string]any{"file_path": filepath.Join(proj, "src/.git/config")}), Ask},
		{"read-only tool", req("TodoWrite", map[string]any{}), Allow},
		{"wait on a port", wait(map[string]any{"port": 3000}, true), Allow},
		{"wait on a remote port", wait(map[string]any{"port": 443, "host": "example.com"}, false), Ask},
		{"wait on a file inside", wait(map[string]any{"path": filepath.Join(proj, "out.log")}, true), Allow},
		{"wait on a file outside", wait(map[string]any{"path": "/etc/hosts"}, true), Ask},
		{"wait on a denied file", wait(map[string]any{"path": filepath.Join(proj, ".env")}, true), Deny},
	}
	for _, c := range cases {
		if d := e.Evaluate(c.r); d.Behavior != c.want {
			t.Errorf("%s: got %s (%s), want %s", c.name, d.Behavior, d.Message, c.want)
		}
	}
	if !e.IsDisabled("WebSearch") || e.IsDisabled("Bash") {
		t.Fatal("bare deny removes the tool from the tool list")
	}
}

type fixedAsker struct {
	d    Decision
	last *Request
}

func (f *fixedAsker) Ask(_ context.Context, r Request) (Decision, error) {
	f.last = &r
	return f.d, nil
}

func TestModes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	write := req("Write", map[string]any{"file_path": filepath.Join(dir, "a.txt")})
	read := req("Read", map[string]any{"file_path": filepath.Join(dir, "a.txt")})
	e := New(Config{Cwd: dir, Home: t.TempDir(), Deny: []string{"Bash(rm *)"}})
	if e.Check(ctx, read).Behavior != Allow {
		t.Fatal("read-only must be allowed")
	}
	if d := e.Check(ctx, write); d.Behavior != Deny || d.Message == "" {
		t.Fatalf("non-interactive ask must deny: %+v", d)
	}
	a := &fixedAsker{d: Decision{Behavior: Allow}}
	e.SetAsker(a)
	if e.Check(ctx, write).Behavior != Allow {
		t.Fatal("asker allow")
	}
	if e.Check(ctx, bash("rm -rf x")).Behavior != Deny {
		t.Fatal("deny rule wins")
	}
	a.d = Decision{Behavior: Deny}
	if d := e.Check(ctx, write); d.Behavior != Deny || !d.Interrupt {
		t.Fatalf("deny without comment stops the turn: %+v", d)
	}
	a.d = Decision{Behavior: Deny, Message: "use a temp file"}
	if d := e.Check(ctx, write); d.Behavior != Deny || d.Interrupt || !strings.Contains(d.Message, "use a temp file") {
		t.Fatalf("deny with comment continues: %+v", d)
	}
	e.SetAsker(nil)

	e.SetMode(ModeAcceptEdits)
	if e.Check(ctx, write).Behavior != Allow {
		t.Fatal("acceptEdits allows edits in workdir")
	}
	if e.Check(ctx, req("Write", map[string]any{"file_path": "/etc/x"})).Behavior == Allow {
		t.Fatal("acceptEdits outside workdir must not allow")
	}
	if e.Check(ctx, req("Write", map[string]any{"file_path": filepath.Join(dir, ".claude", "settings.json")})).Behavior == Allow {
		t.Fatal("protected paths are never auto-approved")
	}
	if e.Check(ctx, bash("mkdir -p build/out && touch build/x && mv a b && cp -r a c")).Behavior != Allow {
		t.Fatal("acceptEdits allows filesystem commands inside workdir")
	}
	if e.Check(ctx, bash("cp a /etc/x")).Behavior == Allow {
		t.Fatal("acceptEdits fs commands outside workdir must ask")
	}
	if e.Check(ctx, bash("npm install")).Behavior == Allow {
		t.Fatal("acceptEdits does not allow other commands")
	}

	var modes []Mode
	e.OnModeChange(func(m Mode) { modes = append(modes, m) })
	e.SetMode(ModePlan)
	if e.Check(ctx, write).Behavior != Deny || e.Check(ctx, read).Behavior != Allow {
		t.Fatal("plan mode is read-only")
	}
	if e.Check(ctx, bash("git status && ls")).Behavior != Allow || e.Check(ctx, bash("make")).Behavior != Deny {
		t.Fatal("plan mode allows read-only bash only")
	}
	e.SetMode(ModeBypassPermissions)
	if e.Check(ctx, bash("make deploy")).Behavior != Allow || e.Check(ctx, req("Write", map[string]any{"file_path": filepath.Join(dir, ".git", "x")})).Behavior != Allow {
		t.Fatal("bypass allows everything but deny rules")
	}
	if e.Check(ctx, bash("rm x")).Behavior != Deny {
		t.Fatal("deny applies in bypass")
	}
	e.SetMode(ModeDontAsk)
	e.SetAsker(&fixedAsker{d: Decision{Behavior: Allow}})
	if e.Check(ctx, bash("make")).Behavior != Deny || e.Check(ctx, bash("ls")).Behavior != Allow {
		t.Fatal("dontAsk denies instead of asking, read-only still allowed")
	}
	if !reflect.DeepEqual(modes, []Mode{ModePlan, ModeBypassPermissions, ModeDontAsk}) {
		t.Fatalf("mode events %v", modes)
	}
	e2 := New(Config{Cwd: dir, DisableBypassPermissions: true})
	if e2.SetMode(ModeBypassPermissions) == nil {
		t.Fatal("bypass disabled by policy")
	}
}

func TestTrustGatesCommittedAllowRules(t *testing.T) {
	dir := t.TempDir()
	src := []Source{{Name: "projectSettings", Root: dir, Allow: []string{"Bash(make *)"}, Deny: []string{"Bash(make clean)"}, RequiresTrust: true}}
	e := New(Config{Cwd: dir, Sources: src})
	if e.Evaluate(bash("make build")).Behavior != Ask {
		t.Fatal("untrusted workspace ignores committed allow rules")
	}
	if e.Evaluate(bash("make clean")).Behavior != Deny {
		t.Fatal("deny rules apply without trust")
	}
	e.SetTrusted(true)
	if e.Evaluate(bash("make build")).Behavior != Allow {
		t.Fatal("trusted workspace applies allow rules")
	}

	ts := TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}
	if ts.IsTrusted(dir) {
		t.Fatal("not trusted yet")
	}
	if err := ts.Trust(dir); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "a", "b")
	if !ts.IsTrusted(dir) || !ts.IsTrusted(sub) || ts.IsTrusted(filepath.Dir(dir)) {
		t.Fatal("trust covers the directory and its children only")
	}
}

func TestAllowAlwaysPersistsAndSuggests(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	sub := filepath.Join(dir, "pkg")
	os.Mkdir(sub, 0o755)
	os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
	os.WriteFile(filepath.Join(dir, ".claude", "settings.local.json"), []byte(`{"env":{"A":"1"},"permissions":{"allow":["Read"],"deny":["Bash(rm *)"]}}`), 0o644)
	e := New(Config{Cwd: sub, Home: t.TempDir()})
	r := bash("npm run test -- -u && go build ./... && git status && python -m pytest -x && ./tool.sh a")
	got := e.Suggest(r)
	want := []string{"Bash(npm run test *)", "Bash(go build *)", "Bash(python -m pytest *)", "Bash(./tool.sh *)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("suggestions %q, want %q", got, want)
	}
	if s := e.Suggest(req("Edit", map[string]any{"file_path": filepath.Join(sub, "x", "a.go")})); !reflect.DeepEqual(s, []string{"Edit(/pkg/x/**)"}) {
		t.Fatalf("edit suggestion %q", s)
	}
	if err := e.AllowAlways(got); err != nil {
		t.Fatal(err)
	}
	if e.Evaluate(r).Behavior != Allow {
		t.Fatal("rules apply for the rest of the session")
	}
	if e.LocalSettingsPath() != filepath.Join(dir, ".claude", "settings.local.json") {
		t.Fatalf("settings path %s", e.LocalSettingsPath())
	}
	var doc struct {
		Env         map[string]string `json:"env"`
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	data, _ := os.ReadFile(e.LocalSettingsPath())
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Env["A"] != "1" || len(doc.Permissions.Deny) != 1 || !reflect.DeepEqual(doc.Permissions.Allow, append([]string{"Read"}, want...)) {
		t.Fatalf("merged settings: %s", data)
	}
	// Persisted rule loads in a new engine via a local settings source.
	e2 := New(Config{Cwd: sub, Sources: []Source{{Name: "localSettings", Root: dir, Allow: doc.Permissions.Allow}}})
	if e2.Evaluate(bash("go build ./cmd")).Behavior != Allow {
		t.Fatal("persisted rule applies")
	}
	// Invalid JSON is not clobbered.
	os.WriteFile(e.LocalSettingsPath(), []byte("{broken"), 0o644)
	if err := e.AllowAlways([]string{"Bash(ls *)"}); err == nil {
		t.Fatal("expected error for invalid settings JSON")
	}
}

func TestProtectedRelativeToWorkdir(t *testing.T) {
	home := "/home/u"
	work := []string{"/x/.claude/worktrees/agent/proj"}
	if isProtected("/x/.claude/worktrees/agent/proj/main.go", work, home) {
		t.Fatal("a project under .claude/ is not itself protected")
	}
	if !isProtected("/x/.claude/worktrees/agent/proj/.claude/settings.json", work, home) {
		t.Fatal(".claude inside the project is protected")
	}
	if !isProtected("/home/u/.zshrc", work, home) || !isProtected("/home/u/.claude/settings.json", work, home) {
		t.Fatal("shell rc and ~/.claude are protected")
	}
}

func TestRelocateFollowsTheFolder(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	e := New(Config{Cwd: a, Trusted: true, DefaultMode: ModeAcceptEdits, Sources: []Source{{Name: "projectSettings", Root: a, Deny: []string{"Edit(/secret/**)"}}}})
	edit := func(p string) Request {
		return Request{ToolName: "Edit", FilePath: p, Input: []byte(`{"file_path":"` + p + `"}`)}
	}
	if e.Evaluate(edit(filepath.Join(a, "x.go"))).Behavior != Allow || e.Evaluate(edit(filepath.Join(b, "x.go"))).Behavior != Ask {
		t.Fatal("before the move")
	}
	if e.Evaluate(edit(filepath.Join(a, "secret", "k"))).Behavior != Deny {
		t.Fatal("deny rule of the first folder")
	}
	e.Relocate(Relocation{Cwd: b, Sources: []Source{{Name: "projectSettings", Root: b, Deny: []string{"Edit(/secret/**)"}}}, Trusted: true})
	if e.Evaluate(edit(filepath.Join(b, "x.go"))).Behavior != Allow || e.Evaluate(edit(filepath.Join(a, "x.go"))).Behavior != Ask {
		t.Fatal("working directories did not follow")
	}
	if e.Evaluate(edit(filepath.Join(b, "secret", "k"))).Behavior != Deny {
		t.Fatal("the project's deny rule must apply in the new folder")
	}
	if d := e.WorkingDirs(); len(d) != 1 || d[0] != b {
		t.Fatalf("dirs %v", d)
	}
}

// A subagent's worktree sits under .harness, a protected directory, but is a working directory like any other.
func TestAddWorkDirStaysAndIsNotProtected(t *testing.T) {
	cwd := t.TempDir()
	wt := filepath.Join(cwd, ".harness", "worktrees", "agent-a1")
	file := filepath.Join(wt, "main.go")
	e := New(Config{Cwd: cwd, DefaultMode: ModeAcceptEdits})
	req := Request{ToolName: "Edit", FilePath: file}
	if d := e.Evaluate(req); d.Behavior != Ask {
		t.Fatalf("before: %v", d.Behavior)
	}
	e.AddWorkDir(wt)
	e.SetAdditionalDirectories(nil)
	if d := e.Evaluate(req); d.Behavior != Allow {
		t.Fatalf("after: %v %s", d.Behavior, d.Message)
	}
}

// Edit rules and the working directories scope NotebookEdit through the same file path as Edit.
func TestNotebookEditPathScoping(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	os.MkdirAll(proj, 0o755)
	e := New(Config{Cwd: proj, Home: filepath.Join(dir, "home"), Allow: []string{"Edit(/src/**)"}, Deny: []string{"Edit(/secret/**)"}})
	check := func(path string) Behavior {
		return e.Evaluate(Request{ToolName: "NotebookEdit", FilePath: path, Input: []byte(`{}`)}).Behavior
	}
	if b := check(filepath.Join(proj, "src", "a.ipynb")); b != Allow {
		t.Errorf("allowed path: %v", b)
	}
	if b := check(filepath.Join(proj, "secret", "a.ipynb")); b != Deny {
		t.Errorf("denied path: %v", b)
	}
	if b := check(filepath.Join(dir, "elsewhere", "a.ipynb")); b != Ask {
		t.Errorf("outside: %v", b)
	}
}
