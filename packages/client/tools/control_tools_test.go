package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeControl is a session: an Interactor with SessionControl and DeviceOwner.
type fakeControl struct {
	fakeIA
	settings map[string]string
	applied  []string
	notes    []string
	model    string
	awake    bool
	refuse   error
	away     string
}

func (c *fakeControl) SessionModel() string { return c.model }
func (c *fakeControl) SwitchModel(_ context.Context, m string) (string, error) {
	if c.refuse != nil {
		return "", c.refuse
	}
	c.model = m
	return "Display " + m, nil
}
func (c *fakeControl) SessionEffort() string      { return "" }
func (c *fakeControl) SwitchEffort(string) error  { return c.refuse }
func (c *fakeControl) SessionTitle() string       { return "old" }
func (c *fakeControl) SetKeepAwake(on bool) error { c.awake = on; return c.refuse }
func (c *fakeControl) NoteChange(_, text string)  { c.notes = append(c.notes, text) }
func (c *fakeControl) ElsewhereReason() string    { return c.away }
func (c *fakeControl) RenameSession(_ context.Context, t string) (string, error) {
	return t, c.refuse
}
func (c *fakeControl) Setting(_ context.Context, key string) (string, error) {
	v, ok := c.settings[key]
	if !ok {
		return "", errors.New("unset")
	}
	return v, nil
}
func (c *fakeControl) ApplySetting(_ context.Context, key, value string) error {
	c.applied = append(c.applied, key+"="+value)
	return nil
}

func controlEnv(t *testing.T, mode string) (*Env, *fakeHost, *fakeControl) {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	env := NewEnv(dir, t.TempDir())
	env.PlansDir = t.TempDir()
	c := &fakeControl{settings: map[string]string{SettingTheme: "system", SettingNotifications: "true", SettingOutputStyle: "default", SettingKeepAwake: "false"}, model: "m1"}
	h := &fakeHost{mode: mode, ia: c}
	env.SetHost(h)
	return env, h, c
}

func input(v map[string]any) Call {
	b, _ := json.Marshal(v)
	return Call{ID: "c", Input: b}
}

func resultText(r Result) string {
	var sb strings.Builder
	for _, b := range r.Content {
		sb.WriteString(b.Text)
	}
	return sb.String()
}

// Everything that guards the user stays unreachable, by any spelling.
var securityKeys = []string{
	"permissions", "permissions.allow", "permissions.deny", "permissions.defaultMode", "defaultMode", "allowedSites",
	"browserSites", "apiKey", "ANTHROPIC_API_KEY", "bypass", "bypassPermissions", "disableBypassPermissionsMode",
	"computerUse", "computerUseEnabled", "env", "remoteControl", "additionalDirectories", "editor", "THEME", "theme.mode",
}

func TestSettingsSecurityKeysAreRefused(t *testing.T) {
	env, _, c := controlEnv(t, "bypassPermissions")
	for _, key := range securityKeys {
		call := input(map[string]any{"key": key, "value": "true"})
		if r := (&SettingsSet{}).Run(context.Background(), env, call); !r.IsError {
			t.Errorf("SettingsSet %s: %s", key, resultText(r))
		}
		if r := (&SettingsGet{}).Run(context.Background(), env, input(map[string]any{"key": key})); !r.IsError {
			t.Errorf("SettingsGet %s: %s", key, resultText(r))
		}
		// Refused without bothering the user.
		if h := (&SettingsSet{}).PermissionHints(context.Background(), env, call); h.Sensitive != "" {
			t.Errorf("hint for %s: %+v", key, h)
		}
	}
	if len(c.applied) != 0 {
		t.Fatalf("applied %v", c.applied)
	}
}

func TestSettingDefsStayHarmless(t *testing.T) {
	for _, d := range SettingDefs {
		if slices.Contains(securityKeys, d.Key) || strings.Contains(strings.ToLower(d.Key), "permission") || strings.Contains(strings.ToLower(d.Key), "key") && d.Key != SettingKeepAwake {
			t.Errorf("%s must not be reachable", d.Key)
		}
		if d.Scope != "device" && d.Scope != "session" && d.Scope != "user" {
			t.Errorf("%s: scope %q", d.Key, d.Scope)
		}
	}
}

func TestSettingsSetAsksWithOldAndNewValue(t *testing.T) {
	env, _, c := controlEnv(t, "plan")
	call := input(map[string]any{"key": "theme", "value": "dark"})
	h := (&SettingsSet{}).PermissionHints(context.Background(), env, call)
	if !strings.Contains(h.Sensitive, `"system"`) || !strings.Contains(h.Sensitive, `"dark"`) || !h.ReadOnly {
		t.Fatalf("hint %+v", h)
	}
	if (&SettingsSet{}).ReadOnly(call.Input) {
		t.Fatal("SettingsSet is checked and run on its own")
	}
	r := (&SettingsSet{}).Run(context.Background(), env, call)
	if r.IsError || len(c.applied) != 1 || c.applied[0] != "theme=dark" || len(c.notes) != 1 {
		t.Fatalf("%s %v %v", resultText(r), c.applied, c.notes)
	}
	// A value that cannot be read still asks.
	h = (&SettingsSet{}).PermissionHints(context.Background(), env, input(map[string]any{"key": "compactIdleSessions", "value": false}))
	if !strings.Contains(h.Sensitive, `"unknown"`) || !strings.Contains(h.Sensitive, `"false"`) {
		t.Fatalf("hint %+v", h)
	}
}

func TestSettingsSetValidates(t *testing.T) {
	env, _, c := controlEnv(t, "default")
	for _, v := range []map[string]any{
		{"key": "theme", "value": "neon"},
		{"key": "notifications", "value": "maybe"},
		{"key": "notifications", "value": 3},
		{"key": "outputStyle", "value": " "},
		{"key": "theme"},
	} {
		if r := (&SettingsSet{}).Run(context.Background(), env, input(v)); !r.IsError {
			t.Errorf("%v: %s", v, resultText(r))
		}
		if h := (&SettingsSet{}).PermissionHints(context.Background(), env, input(v)); h.Sensitive != "" {
			t.Errorf("%v asked: %+v", v, h)
		}
	}
	if len(c.applied) != 0 {
		t.Fatalf("applied %v", c.applied)
	}
	if r := (&SettingsSet{}).Run(context.Background(), env, input(map[string]any{"key": "notifications", "value": false})); r.IsError || c.applied[0] != "notifications=false" {
		t.Fatalf("%s %v", resultText(r), c.applied)
	}
}

func TestSettingsGetListsTheAllowlist(t *testing.T) {
	env, _, _ := controlEnv(t, "default")
	r := (&SettingsGet{}).Run(context.Background(), env, Call{Input: json.RawMessage(`{}`)})
	text := resultText(r)
	if r.IsError {
		t.Fatal(text)
	}
	for _, d := range SettingDefs {
		if !strings.Contains(text, `"`+d.Key+`"`) {
			t.Errorf("%s missing from %s", d.Key, text)
		}
	}
	// One unreadable value does not hide the others.
	if !strings.Contains(text, `"error": "unset"`) || !strings.Contains(text, `"value": "system"`) {
		t.Fatal(text)
	}
}

func TestSessionSetModeHints(t *testing.T) {
	for _, c := range []struct {
		from, to string
		ask      bool
	}{
		{"default", "plan", false},
		{"acceptEdits", "plan", false},
		{"auto", "plan", false},
		{"auto", "default", false},
		{"auto", "acceptEdits", false},
		{"acceptEdits", "default", false},
		{"bypassPermissions", "default", false},
		{"bypassPermissions", "plan", false},
		{"dontAsk", "plan", false},
		{"default", "default", false},
		{"default", "acceptEdits", true},
		{"default", "auto", true},
		{"acceptEdits", "auto", true},
		{"plan", "default", true},
		{"plan", "acceptEdits", true},
		{"plan", "auto", true},
		{"default", "dontAsk", true},
		{"dontAsk", "default", true},
		{"dontAsk", "auto", true},
	} {
		env, _, _ := controlEnv(t, c.from)
		h := (&SessionSetMode{}).PermissionHints(context.Background(), env, input(map[string]any{"mode": c.to}))
		if (h.Sensitive != "") != c.ask || !h.ReadOnly {
			t.Errorf("%s -> %s: %+v", c.from, c.to, h)
		}
	}
}

func TestSessionSetModeNeverBypass(t *testing.T) {
	for _, from := range []string{"default", "auto", "plan", "acceptEdits", "bypassPermissions"} {
		env, h, _ := controlEnv(t, from)
		call := input(map[string]any{"mode": "bypassPermissions"})
		r := (&SessionSetMode{}).Run(context.Background(), env, call)
		if !r.IsError || !strings.Contains(resultText(r), "only the user") || len(h.setModes) != 0 {
			t.Errorf("from %s: %s %v", from, resultText(r), h.setModes)
		}
		// Not even offered to the user through this tool.
		if hint := (&SessionSetMode{}).PermissionHints(context.Background(), env, call); hint.Sensitive != "" {
			t.Errorf("from %s: %+v", from, hint)
		}
	}
}

func TestSessionSetModeRun(t *testing.T) {
	env, h, c := controlEnv(t, "auto")
	if (&SessionSetMode{}).ReadOnly(nil) {
		t.Fatal("each call must be checked against the mode right when it runs")
	}
	r := (&SessionSetMode{}).Run(context.Background(), env, input(map[string]any{"mode": "plan"}))
	if r.IsError || h.mode != "plan" || len(h.reminds) != 1 || !strings.HasPrefix(h.reminds[0], "plan_mode:") || len(c.notes) != 1 {
		t.Fatalf("%s %+v %v", resultText(r), h, c.notes)
	}
	r = (&SessionSetMode{}).Run(context.Background(), env, input(map[string]any{"mode": "plan"}))
	if r.IsError || len(h.setModes) != 1 {
		t.Fatalf("no-op: %s %v", resultText(r), h.setModes)
	}
	for _, bad := range []string{"", "root"} {
		if r := (&SessionSetMode{}).Run(context.Background(), env, input(map[string]any{"mode": bad})); !r.IsError {
			t.Errorf("mode %q: %s", bad, resultText(r))
		}
	}
}

func TestSessionToolsWithoutControlRefuse(t *testing.T) {
	env := NewEnv(t.TempDir(), t.TempDir())
	env.SetHost(&fakeHost{mode: "default"})
	for name, c := range map[Tool]map[string]any{
		&SessionSetModel{}:  {"model": "m"},
		&SessionSetEffort{}: {"effort": "low"},
		&SessionSetTitle{}:  {"title": "t"},
		&KeepAwake{}:        {"enabled": true},
		&SettingsGet{}:      {},
		&SettingsSet{}:      {"key": "theme", "value": "dark"},
	} {
		if r := name.Run(context.Background(), env, input(c)); !r.IsError {
			t.Errorf("%s: %s", name.Name(), resultText(r))
		}
	}
}

func TestSessionSetModelNotesAndRefusals(t *testing.T) {
	env, _, c := controlEnv(t, "default")
	r := (&SessionSetModel{}).Run(context.Background(), env, input(map[string]any{"model": "m2"}))
	if r.IsError || c.model != "m2" || len(c.notes) != 1 || !strings.Contains(c.notes[0], "Display m2") {
		t.Fatalf("%s %v", resultText(r), c.notes)
	}
	if r := (&SessionSetModel{}).Run(context.Background(), env, input(map[string]any{"model": "m2"})); r.IsError || len(c.notes) != 1 {
		t.Fatalf("no-op: %s %v", resultText(r), c.notes)
	}
	c.refuse = errors.New("out of usage")
	if r := (&SessionSetModel{}).Run(context.Background(), env, input(map[string]any{"model": "m3"})); !r.IsError || !strings.Contains(resultText(r), "out of usage") {
		t.Fatal(resultText(r))
	}
}

// ---- Desktop ----

// fakeDesktop records what would start; nothing really opens.
func fakeDesktop(goos string, env map[string]string, path ...string) (*Desktop, *[][]string) {
	var started [][]string
	return &Desktop{
		GOOS:   goos,
		Getenv: func(k string) string { return env[k] },
		LookPath: func(n string) (string, error) {
			if slices.Contains(path, n) {
				return "/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
		Start: func(argv []string) error {
			started = append(started, argv)
			return nil
		},
	}, &started
}

func TestEditorSelection(t *testing.T) {
	for _, c := range []struct {
		name string
		goos string
		env  map[string]string
		path []string
		line int
		want []string
	}{
		{"visual wins", "darwin", map[string]string{"VISUAL": "zed", "EDITOR": "code"}, []string{"zed", "code"}, 0, []string{"/bin/zed", "/p/a b.go"}},
		{"flags kept and no shell", "darwin", map[string]string{"EDITOR": "code --wait"}, []string{"code"}, 7, []string{"/bin/code", "--wait", "-g", "/p/a b.go:7"}},
		{"terminal editors are skipped", "linux", map[string]string{"EDITOR": "vim"}, []string{"vim", "cursor"}, 2, []string{"/bin/cursor", "-g", "/p/a b.go:2"}},
		{"missing env editor falls through", "linux", map[string]string{"VISUAL": "nosuch"}, []string{"subl"}, 3, []string{"/bin/subl", "/p/a b.go:3"}},
		{"jetbrains line", "linux", map[string]string{"VISUAL": "idea"}, []string{"idea"}, 3, []string{"/bin/idea", "--line", "3", "/p/a b.go"}},
		{"plus line", "linux", map[string]string{"VISUAL": "emacsclient -n"}, []string{"emacsclient"}, 3, []string{"/bin/emacsclient", "-n", "+3", "/p/a b.go"}},
		{"macOS text fallback", "darwin", nil, []string{"open"}, 0, []string{"/bin/open", "-t", "/p/a b.go"}},
		{"Linux fallback", "linux", nil, []string{"xdg-open"}, 0, []string{"/bin/xdg-open", "/p/a b.go"}},
		{"Windows fallback", "windows", nil, []string{"notepad.exe"}, 0, []string{"/bin/notepad.exe", "/p/a b.go"}},
	} {
		d, _ := fakeDesktop(c.goos, c.env, c.path...)
		argv, _, err := d.editorArgv("/p/a b.go", c.line)
		if err != nil || !slices.Equal(argv, c.want) {
			t.Errorf("%s: %v %v, want %v", c.name, argv, err, c.want)
		}
	}
}

func TestEditorMissing(t *testing.T) {
	d, _ := fakeDesktop("linux", map[string]string{"EDITOR": "vim"}, "vim")
	if _, _, err := d.editorArgv("/p/a.go", 0); err == nil || !strings.Contains(err.Error(), "no editor found") || !strings.Contains(err.Error(), "VISUAL") {
		t.Fatalf("%v", err)
	}
	// xdg-open would launch a .desktop file.
	d, _ = fakeDesktop("linux", nil, "xdg-open")
	if _, _, err := d.editorArgv("/p/run.desktop", 0); err == nil {
		t.Fatal("a .desktop file must not reach xdg-open")
	}
}

func TestEditorWindowsBatchLaunchers(t *testing.T) {
	d, _ := fakeDesktop("windows", map[string]string{"EDITOR": "code"}, "code", "notepad.exe")
	d.LookPath = func(n string) (string, error) {
		if n == "code" {
			return `C:\vs\code.cmd`, nil
		}
		return `C:\win\` + n, nil
	}
	// cmd.exe would read & in the path as a command separator: past the launcher, to something that takes it as data.
	argv, _, err := d.editorArgv(`C:\p\a&calc.go`, 0)
	if err != nil || argv[0] == `C:\vs\code.cmd` {
		t.Fatalf("%v %v", argv, err)
	}
	if argv, _, err = d.editorArgv(`C:\p\a.go`, 0); err != nil || argv[0] != `C:\vs\code.cmd` {
		t.Fatalf("%v %v", argv, err)
	}
}

func TestRevealArgv(t *testing.T) {
	for _, c := range []struct {
		goos string
		dir  bool
		want []string
	}{
		{"darwin", false, []string{"/bin/open", "-R", "/p/a b"}},
		{"darwin", true, []string{"/bin/open", "-R", "/p/a b"}},
		{"linux", false, []string{"/bin/xdg-open", "/p"}},
		{"linux", true, []string{"/bin/xdg-open", "/p/a b"}},
		{"windows", false, []string{"/bin/explorer.exe", "/select,/p/a b"}},
		{"windows", true, []string{"/bin/explorer.exe", "/p/a b"}},
	} {
		d, _ := fakeDesktop(c.goos, nil, "open", "xdg-open", "explorer.exe")
		if argv, err := d.revealArgv("/p/a b", c.dir); err != nil || !slices.Equal(argv, c.want) {
			t.Errorf("%s dir=%v: %v %v", c.goos, c.dir, argv, err)
		}
	}
	d, _ := fakeDesktop("linux", nil)
	if _, err := d.revealArgv("/p/a", false); err == nil || !strings.Contains(err.Error(), "xdg-open") {
		t.Fatalf("%v", err)
	}
}

func TestOpenAndRevealRun(t *testing.T) {
	env, _, c := controlEnv(t, "default")
	file := filepath.Join(env.Cwd, "a.go")
	if err := os.WriteFile(file, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, started := fakeDesktop("darwin", map[string]string{"VISUAL": "code"}, "code", "open")
	r := (&OpenInEditor{Desktop: d}).Run(context.Background(), env, input(map[string]any{"path": "a.go", "line": 1}))
	if r.IsError || len(*started) != 1 || !slices.Equal((*started)[0], []string{"/bin/code", "-g", file + ":1"}) {
		t.Fatalf("%s %v", resultText(r), *started)
	}
	r = (&RevealPath{Desktop: d}).Run(context.Background(), env, input(map[string]any{"path": "a.go"}))
	if r.IsError || !slices.Equal((*started)[1], []string{"/bin/open", "-R", file}) {
		t.Fatalf("%s %v", resultText(r), *started)
	}

	// A file that is not one, or not there.
	for _, in := range []map[string]any{{"path": "."}, {"path": "nosuch"}, {"path": ""}} {
		if r := (&OpenInEditor{Desktop: d}).Run(context.Background(), env, input(in)); !r.IsError {
			t.Errorf("%v: %s", in, resultText(r))
		}
	}

	// A line the editor cannot take is reported, not hidden.
	d2, _ := fakeDesktop("darwin", nil, "open")
	r = (&OpenInEditor{Desktop: d2}).Run(context.Background(), env, input(map[string]any{"path": "a.go", "line": 9}))
	if r.IsError || !strings.Contains(resultText(r), "not at line 9") {
		t.Fatal(resultText(r))
	}

	// The user is elsewhere: nothing starts, and the model is told why.
	c.away = "the user is on another machine"
	n := len(*started)
	for _, tool := range []Tool{&OpenInEditor{Desktop: d}, &RevealPath{Desktop: d}} {
		if r := tool.Run(context.Background(), env, input(map[string]any{"path": "a.go"})); !r.IsError || !strings.Contains(resultText(r), "another machine") {
			t.Fatalf("%s: %s", tool.Name(), resultText(r))
		}
	}
	if len(*started) != n {
		t.Fatalf("started %v", *started)
	}

	// No graphical session on Linux.
	c.away = ""
	d3, s3 := fakeDesktop("linux", nil, "xdg-open")
	if r := (&RevealPath{Desktop: d3}).Run(context.Background(), env, input(map[string]any{"path": "a.go"})); !r.IsError || !strings.Contains(resultText(r), "graphical") || len(*s3) != 0 {
		t.Fatalf("%s %v", resultText(r), *s3)
	}
}

func TestScopeHints(t *testing.T) {
	env, _, _ := controlEnv(t, "default")
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	scratchFile := filepath.Join(env.ScratchDir, "o.txt")
	if err := os.WriteFile(scratchFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(env.Cwd, "link")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		ask  bool
	}{
		{"a.go", false},
		{filepath.Join(env.Cwd, "sub", "b.go"), false},
		{scratchFile, false},
		{secret, true},
		{"../" + filepath.Base(env.Cwd) + "x", true},
		{"link", true}, // a symlink out of the folder is outside
	} {
		call := input(map[string]any{"path": c.path})
		if h := (&OpenInEditor{}).PermissionHints(context.Background(), env, call); (h.Sensitive != "") != c.ask {
			t.Errorf("OpenInEditor %s: %+v", c.path, h)
		}
		if h := (&RevealPath{}).PermissionHints(context.Background(), env, call); (h.Sensitive != "") != c.ask {
			t.Errorf("RevealPath %s: %+v", c.path, h)
		}
	}
}

func TestKeepAwakeTool(t *testing.T) {
	env, _, c := controlEnv(t, "default")
	r := (&KeepAwake{}).Run(context.Background(), env, input(map[string]any{"enabled": true}))
	if r.IsError || !c.awake || len(c.notes) != 1 {
		t.Fatalf("%s %v", resultText(r), c.notes)
	}
	if r := (&KeepAwake{}).Run(context.Background(), env, input(map[string]any{})); !r.IsError {
		t.Fatal(resultText(r))
	}
	c.refuse = errors.New("caffeinate is not installed")
	if r := (&KeepAwake{}).Run(context.Background(), env, input(map[string]any{"enabled": true})); !r.IsError || !strings.Contains(resultText(r), "caffeinate") {
		t.Fatal(resultText(r))
	}
}
