package permissions

import "testing"

func TestWidensMode(t *testing.T) {
	all := []Mode{ModePlan, ModeDefault, ModeAcceptEdits, ModeAuto, ModeDontAsk, ModeBypassPermissions}
	for _, from := range all {
		if WidensMode(from, ModePlan) {
			t.Errorf("%s -> plan is a tightening", from)
		}
		if WidensMode(from, from) {
			t.Errorf("%s -> itself", from)
		}
		if from != ModeBypassPermissions && !WidensMode(from, ModeBypassPermissions) {
			t.Errorf("%s -> bypass must count as widening", from)
		}
	}
	for _, c := range []struct {
		from, to Mode
		want     bool
	}{
		{ModeDefault, ModeAcceptEdits, true},
		{ModeAcceptEdits, ModeAuto, true},
		{ModePlan, ModeDefault, true},
		{ModeAuto, ModeDefault, false},
		{ModeAuto, ModeAcceptEdits, false},
		{ModeAcceptEdits, ModeDefault, false},
		{ModeDefault, ModeDontAsk, true},
		{ModeDontAsk, ModeDefault, true},
		{Mode("weird"), ModeDefault, true},
		{ModeDefault, Mode("weird"), true},
	} {
		if got := WidensMode(c.from, c.to); got != c.want {
			t.Errorf("%s -> %s: %v", c.from, c.to, got)
		}
	}
}

// A call the tool marks as read-only with a reason only the user may approve asks in every mode that can ask: plan and
// bypass included, and allow rules do not answer for the user.
func TestSensitiveReadOnlyAsksTheUser(t *testing.T) {
	for _, mode := range []Mode{ModeDefault, ModePlan, ModeAcceptEdits, ModeAuto, ModeBypassPermissions} {
		e := New(Config{Cwd: "/p", Home: "/h", DefaultMode: mode, Allow: []string{"SessionSetMode", "SettingsSet"}})
		d := e.Evaluate(Request{ToolName: "SessionSetMode", ReadOnly: true, Sensitive: "from a to b"})
		if d.Behavior != Ask || !d.UserOnly || d.Message != "from a to b" {
			t.Errorf("%s: %+v", mode, d)
		}
	}
	// Deny rules still apply.
	e := New(Config{Cwd: "/p", Home: "/h", DefaultMode: ModeDefault, Deny: []string{"SettingsSet"}})
	if d := e.Evaluate(Request{ToolName: "SettingsSet", ReadOnly: true, Sensitive: "x"}); d.Behavior != Deny {
		t.Errorf("%+v", d)
	}
	// Without a reason, read-only passes, in plan mode too.
	e = New(Config{Cwd: "/p", Home: "/h", DefaultMode: ModePlan})
	if d := e.Evaluate(Request{ToolName: "SessionSetMode", ReadOnly: true}); d.Behavior != Allow {
		t.Errorf("%+v", d)
	}
}

// The file tools' path check and Read deny rules cover what opens a file for the user.
func TestOpenToolsFollowTheReadCheck(t *testing.T) {
	e := New(Config{Cwd: "/p", Home: "/h", DefaultMode: ModeDefault, Deny: []string{"Read(//p/secret/**)"}})
	for _, tool := range []string{"OpenInEditor", "RevealPath"} {
		if d := e.Evaluate(Request{ToolName: tool, ReadOnly: true, FilePath: "/p/a.go"}); d.Behavior != Allow {
			t.Errorf("%s inside: %+v", tool, d)
		}
		if d := e.Evaluate(Request{ToolName: tool, ReadOnly: true, FilePath: "/p/secret/k"}); d.Behavior != Deny {
			t.Errorf("%s denied path: %+v", tool, d)
		}
	}
}
