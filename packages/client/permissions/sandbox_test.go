package permissions

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func sandboxEngine(t *testing.T, mode Mode, st SandboxState, allow ...string) *Engine {
	t.Helper()
	return New(Config{Cwd: t.TempDir(), Home: t.TempDir(), DefaultMode: mode, Allow: allow, Sandbox: func() SandboxState { return st }})
}

func bashReq(command string, outside bool) Request {
	in, _ := json.Marshal(map[string]any{"command": command})
	return Request{ToolName: "Bash", Command: command, Input: in, OutsideSandbox: outside}
}

func TestOutsideSandboxIsUserOnlyInEveryMode(t *testing.T) {
	for _, mode := range []Mode{ModeDefault, ModeAcceptEdits, ModeAuto, ModeBypassPermissions, ModeDontAsk} {
		e := sandboxEngine(t, mode, SandboxState{Active: true}, "Bash")
		d := e.Evaluate(bashReq("ls", true))
		if d.Behavior != Ask || !d.UserOnly {
			t.Fatalf("%s: %+v", mode, d)
		}
		if got := e.Suggest(bashReq("rm -rf build", true)); got != nil {
			t.Fatalf("%s offered a rule for leaving the sandbox: %v", mode, got)
		}
	}
	// Plan mode runs nothing that writes.
	if d := sandboxEngine(t, ModePlan, SandboxState{Active: true}).Evaluate(bashReq("ls", true)); d.Behavior != Deny {
		t.Fatalf("plan: %+v", d)
	}
	// A deny rule still comes first.
	if d := sandboxEngine(t, ModeDefault, SandboxState{Active: true}).Evaluate(bashReq("ls", true)); d.Behavior != Ask {
		t.Fatalf("no rule: %+v", d)
	}
	e := New(Config{Cwd: t.TempDir(), Home: t.TempDir(), Deny: []string{"Bash(ls)"}, Sandbox: func() SandboxState { return SandboxState{Active: true} }})
	if d := e.Evaluate(bashReq("ls", true)); d.Behavior != Deny {
		t.Fatalf("deny: %+v", d)
	}
}

func TestOutsideSandboxWithSetting(t *testing.T) {
	// The setting gives the normal check back: bypass passes, default asks, and that ask may go to the classifier.
	st := SandboxState{Active: true, OutsideWithoutPrompt: true}
	if d := sandboxEngine(t, ModeBypassPermissions, st).Evaluate(bashReq("touch x", true)); d.Behavior != Allow {
		t.Fatalf("bypass: %+v", d)
	}
	if d := sandboxEngine(t, ModeDefault, st).Evaluate(bashReq("touch x", true)); d.Behavior != Ask || d.UserOnly {
		t.Fatalf("default: %+v", d)
	}
	// A policy that refuses wins over the setting, in bypass too.
	st.OutsideRefused = true
	if d := sandboxEngine(t, ModeBypassPermissions, st).Evaluate(bashReq("touch x", true)); d.Behavior != Deny {
		t.Fatalf("refused: %+v", d)
	}
}

func TestOutsideSandboxWithoutASandbox(t *testing.T) {
	// Sandbox off, or nothing to leave: the parameter changes nothing.
	if d := sandboxEngine(t, ModeDefault, SandboxState{}).Evaluate(bashReq("touch x", true)); d.UserOnly {
		t.Fatalf("off: %+v", d)
	}
	e := New(Config{Cwd: t.TempDir(), Home: t.TempDir(), DefaultMode: ModeBypassPermissions})
	if d := e.Evaluate(bashReq("touch x", true)); d.Behavior != Allow {
		t.Fatalf("none: %+v", d)
	}
}

func TestOutsideSandboxDoesNotAskWithoutAnyone(t *testing.T) {
	e := sandboxEngine(t, ModeBypassPermissions, SandboxState{Active: true})
	if d := e.Check(context.Background(), bashReq("ls", true)); d.Behavior != Deny {
		t.Fatalf("nobody to ask: %+v", d)
	}
}

func TestSandboxAutoAllow(t *testing.T) {
	// Off by default: a sandboxed command still asks.
	if d := sandboxEngine(t, ModeDefault, SandboxState{Active: true}).Evaluate(bashReq("touch x", false)); d.Behavior != Ask {
		t.Fatalf("default: %+v", d)
	}
	on := SandboxState{Active: true, AutoAllow: true}
	if d := sandboxEngine(t, ModeDefault, on).Evaluate(bashReq("touch x", false)); d.Behavior != Allow || d.ResolvedBy != "sandbox" {
		t.Fatalf("on: %+v", d)
	}
	// Not without a sandbox doing the work, not for leaving it, not in plan mode, not over rules.
	if d := sandboxEngine(t, ModeDefault, SandboxState{AutoAllow: true}).Evaluate(bashReq("touch x", false)); d.Behavior != Ask {
		t.Fatalf("inactive: %+v", d)
	}
	if d := sandboxEngine(t, ModeDefault, on).Evaluate(bashReq("touch x", true)); d.Behavior != Ask {
		t.Fatalf("outside: %+v", d)
	}
	if d := sandboxEngine(t, ModePlan, on).Evaluate(bashReq("touch x", false)); d.Behavior != Deny {
		t.Fatalf("plan: %+v", d)
	}
	e := New(Config{Cwd: t.TempDir(), Home: t.TempDir(), Ask: []string{"Bash(touch:*)"}, Sandbox: func() SandboxState { return on }})
	if d := e.Evaluate(bashReq("touch x", false)); d.Behavior != Ask || !d.UserOnly {
		t.Fatalf("ask rule: %+v", d)
	}
}

func TestSandboxSettingsFilesAreTheUsers(t *testing.T) {
	cwd := t.TempDir()
	req := func(rel string) Request {
		return Request{ToolName: "Write", FilePath: filepath.Join(cwd, rel), Input: json.RawMessage(`{}`)}
	}
	active := func() SandboxState { return SandboxState{Active: true} }
	e := New(Config{Cwd: cwd, Home: t.TempDir(), DefaultMode: ModeBypassPermissions, Sandbox: active})
	for _, rel := range []string{".harness/settings.local.json", ".claude/settings.json", "sub/.CLAUDE/Settings.Local.json"} {
		if d := e.Evaluate(req(rel)); d.Behavior != Ask || !d.UserOnly {
			t.Fatalf("%s: %+v", rel, d)
		}
	}
	if d := e.Evaluate(req("settings.json")); d.Behavior != Allow {
		t.Fatalf("an ordinary file: %+v", d)
	}
	// Bypass keeps its meaning where no sandbox is at stake.
	e = New(Config{Cwd: cwd, Home: t.TempDir(), DefaultMode: ModeBypassPermissions})
	if d := e.Evaluate(req(".harness/settings.local.json")); d.Behavior != Allow {
		t.Fatalf("no sandbox: %+v", d)
	}
}
