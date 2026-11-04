package permissions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func computerEngine(t *testing.T, mode Mode, protected bool) (*Engine, string, string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "hh", "computer-use")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(home, "proj")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	e := New(Config{Cwd: cwd, DefaultMode: mode, Trusted: true, ComputerSettings: func() (string, bool) { return dir, protected }})
	return e, dir, cwd
}

func TestComputerSettingsEditIsUserOnly(t *testing.T) {
	for _, mode := range []Mode{ModeDefault, ModeAcceptEdits, ModeAuto, ModeBypassPermissions} {
		e, dir, _ := computerEngine(t, mode, true)
		for _, tool := range []string{"Edit", "Write", "MultiEdit"} {
			for _, p := range []string{filepath.Join(dir, "settings.json"), filepath.Join(dir, "x"), strings.ToUpper(filepath.Join(dir, "settings.json"))} {
				d := e.Evaluate(Request{ToolName: tool, FilePath: p, Input: []byte(`{}`)})
				if d.Behavior != Ask || !d.UserOnly {
					t.Errorf("%s %s %s: %+v", mode, tool, p, d)
				}
			}
		}
	}
	// An allow rule does not get past it.
	home, _ := filepath.EvalSymlinks(t.TempDir())
	dir := filepath.Join(home, "computer-use")
	e := New(Config{Cwd: home, DefaultMode: ModeAcceptEdits, Trusted: true, Allow: []string{"Edit", "Write"}, ComputerSettings: func() (string, bool) { return dir, true }})
	if d := e.Evaluate(Request{ToolName: "Write", FilePath: filepath.Join(dir, "settings.json"), Input: []byte(`{}`)}); d.Behavior != Ask || !d.UserOnly {
		t.Fatalf("%+v", d)
	}
	// Plan mode denies, as for any write.
	pe, pdir, _ := computerEngine(t, ModePlan, true)
	if d := pe.Evaluate(Request{ToolName: "Write", FilePath: filepath.Join(pdir, "settings.json"), Input: []byte(`{}`)}); d.Behavior != Deny {
		t.Fatalf("%+v", d)
	}
	// A read is a read.
	if d := pe.Evaluate(Request{ToolName: "Read", ReadOnly: true, FilePath: filepath.Join(pdir, "settings.json"), Input: []byte(`{}`)}); d.UserOnly {
		t.Fatalf("%+v", d)
	}
}

func TestComputerSettingsSwitchAllowsEdits(t *testing.T) {
	e, dir, _ := computerEngine(t, ModeBypassPermissions, false)
	if d := e.Evaluate(Request{ToolName: "Write", FilePath: filepath.Join(dir, "settings.json"), Input: []byte(`{}`)}); d.Behavior != Allow {
		t.Fatalf("%+v", d)
	}
	e, dir, _ = computerEngine(t, ModeDefault, false)
	if d := e.Evaluate(Request{ToolName: "Write", FilePath: filepath.Join(dir, "settings.json"), Input: []byte(`{}`)}); d.UserOnly {
		t.Fatalf("still user-only: %+v", d)
	}
	if d := e.Evaluate(Request{ToolName: "Bash", Command: "echo x > " + filepath.Join(dir, "settings.json"), Input: []byte(`{}`)}); d.UserOnly {
		t.Fatalf("still user-only: %+v", d)
	}
	// The other protected paths keep their rule.
	if d := e.Evaluate(Request{ToolName: "Write", FilePath: filepath.Join(filepath.Dir(dir), ".git", "config"), Input: []byte(`{}`)}); d.Behavior != Ask || !d.UserOnly {
		t.Fatalf("%+v", d)
	}
}

// What the Bash check catches, in every mode bypass included: writes it can see in the command line.
func TestComputerSettingsBashWritesItCanSee(t *testing.T) {
	for _, mode := range []Mode{ModeDefault, ModeAuto, ModeBypassPermissions} {
		e, dir, cwd := computerEngine(t, mode, true)
		home := filepath.Dir(filepath.Dir(dir))
		t.Setenv("HOME", home)
		rel, err := filepath.Rel(cwd, filepath.Join(dir, "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, cmd := range []string{
			"echo '{}' > " + filepath.Join(dir, "settings.json"),
			"echo '{}' >> " + rel,
			"cp /tmp/x " + filepath.Join(dir, "settings.json"),
			"mv /tmp/x " + filepath.Join(dir, "settings.json"),
			"tee " + filepath.Join(dir, "settings.json"),
			"sed -i '' s/a/b/ " + filepath.Join(dir, "settings.json"),
			"rm " + filepath.Join(dir, "settings.json"),
			"python3 -c \"open('" + filepath.Join(dir, "settings.json") + "','w')\"",
			"cd " + dir + " && echo x > settings.json",
			"echo x > $HOME/hh/computer-use/settings.json",
		} {
			d := e.Evaluate(Request{ToolName: "Bash", Command: cmd, Input: []byte(`{}`)})
			if d.Behavior != Ask || !d.UserOnly {
				t.Errorf("%s: %s: %+v", mode, cmd, d)
			}
		}
		// A read is a read, and unrelated commands are untouched.
		if d := e.Evaluate(Request{ToolName: "Bash", Command: "cat " + filepath.Join(dir, "settings.json"), Input: []byte(`{}`)}); d.UserOnly {
			t.Errorf("%s: read: %+v", mode, d)
		}
		if d := e.Evaluate(Request{ToolName: "Bash", Command: "echo x > out.txt", Input: []byte(`{}`)}); d.UserOnly {
			t.Errorf("%s: unrelated: %+v", mode, d)
		}
	}
}

// What it does not catch. This documents the limit: a shell command can write any file the user can, in ways no parser of
// the command line sees. These commands get no user-only prompt; they go through the normal check, which in default mode
// asks and in bypass mode runs them. What limits the damage is that a running client reads its settings once.
func TestComputerSettingsBashLimits(t *testing.T) {
	e, _, _ := computerEngine(t, ModeBypassPermissions, true)
	for _, cmd := range []string{
		"python3 /tmp/edit_settings.py",
		"d=computer; echo x > ~/hh/$d-use/settings.json",
		"sh -c \"$(echo ZWNobyB4 | base64 -d)\"",
	} {
		if d := e.Evaluate(Request{ToolName: "Bash", Command: cmd, Input: []byte(`{}`)}); d.Behavior != Allow {
			t.Errorf("%s is caught after all, update the claim: %+v", cmd, d)
		}
	}
}
