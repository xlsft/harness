package permissions

import (
	"path/filepath"
	"testing"
)

func TestRestrictReads(t *testing.T) {
	dir := t.TempDir()
	scratch := filepath.Join(t.TempDir(), "scratch")
	outside := filepath.Join(t.TempDir(), "secret.txt")
	engine := func(restrict bool, mode Mode, allow ...string) *Engine {
		return New(Config{Cwd: dir, DefaultMode: mode, Trusted: true, RestrictReads: restrict, ScratchDirs: []string{scratch}, Allow: allow, Home: t.TempDir()})
	}
	read := func(p string) Request {
		return Request{ToolName: "Read", FilePath: p, ReadOnly: true}
	}
	bash := func(cmd string) Request {
		return Request{ToolName: "Bash", Command: cmd, Input: []byte(`{"command":` + quote(cmd) + `}`), Cwd: dir}
	}

	// Without the setting: a prompt, as ever; bypass goes through.
	if d := engine(false, ModeDefault).Evaluate(read(outside)); d.Behavior != Ask {
		t.Fatalf("off: %+v", d)
	}
	if d := engine(false, ModeBypassPermissions).Evaluate(bash("cat " + outside)); d.Behavior != Allow {
		t.Fatalf("off, bypass: %+v", d)
	}

	for _, mode := range []Mode{ModeDefault, ModeAcceptEdits, ModeBypassPermissions, ModePlan} {
		e := engine(true, mode)
		if d := e.Evaluate(read(outside)); d.Behavior != Deny {
			t.Errorf("%s: read outside: %+v", mode, d)
		}
		if d := e.Evaluate(read(filepath.Join(dir, "a.txt"))); d.Behavior != Allow {
			t.Errorf("%s: read inside: %+v", mode, d)
		}
		if d := e.Evaluate(read(filepath.Join(scratch, "a"))); d.Behavior != Allow {
			t.Errorf("%s: scratch: %+v", mode, d)
		}
		for _, cmd := range []string{"cat " + outside, "wc -l < " + outside, "cat ../../etc/passwd", "grep --file=" + outside + " x", "ls ~/.ssh"} {
			if d := e.Evaluate(bash(cmd)); d.Behavior != Deny {
				t.Errorf("%s: %s: %+v", mode, cmd, d)
			}
		}
		for _, cmd := range []string{"cat a.txt", "ls", "cat " + filepath.Join(dir, "a.txt"), "echo hi > /dev/null"} {
			if d := e.Evaluate(bash(cmd)); d.Behavior == Deny {
				t.Errorf("%s: %s refused: %+v", mode, cmd, d)
			}
		}
	}

	// An allow rule opens a path.
	e := engine(true, ModeDefault, "Read(/"+outside+")")
	if d := e.Evaluate(read(outside)); d.Behavior == Deny {
		t.Fatalf("allow rule: %+v", d)
	}
	if d := e.Evaluate(bash("cat " + outside)); d.Behavior == Deny {
		t.Fatalf("allow rule, shell: %+v", d)
	}
}

func quote(s string) string {
	out := []byte{'"'}
	for _, c := range []byte(s) {
		if c == '"' || c == '\\' {
			out = append(out, '\\')
		}
		out = append(out, c)
	}
	return string(append(out, '"'))
}
