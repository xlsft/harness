package permissions

import "testing"

func TestAgentMode(t *testing.T) {
	cases := []struct {
		session, wanted, want Mode
	}{
		{ModeDefault, "", ""},
		{ModeDefault, ModeDefault, ""},
		{ModeDefault, ModeAcceptEdits, ModeAcceptEdits},
		{ModeDefault, ModeAuto, ModeAuto},
		{ModeDefault, ModePlan, ModePlan},
		{ModeDefault, ModeDontAsk, ModeDontAsk},
		{ModeDefault, ModeBypassPermissions, ""},
		{ModeAcceptEdits, ModeAuto, ModeAuto},
		{ModeAcceptEdits, ModeDefault, ModeDefault},
		{ModeAuto, ModeAcceptEdits, ModeAcceptEdits},
		{ModeAuto, ModeBypassPermissions, ""},
		{ModeBypassPermissions, ModePlan, ModePlan},
		{ModeBypassPermissions, ModeDefault, ModeDefault},
		{ModeDontAsk, ModeDefault, ""},
		{ModeDontAsk, ModeAcceptEdits, ""},
		{ModeDontAsk, ModePlan, ModePlan},
		{ModePlan, ModeAcceptEdits, ""},
		{ModePlan, ModeBypassPermissions, ""},
		{ModeDefault, Mode("nonsense"), ""},
	}
	for _, c := range cases {
		if got := AgentMode(c.session, c.wanted, nil); got != c.want {
			t.Errorf("session %s, wanted %s: %q, want %q", c.session, c.wanted, got, c.want)
		}
	}
	noAuto := func(m Mode) bool { return m != ModeAuto }
	if got := AgentMode(ModeDefault, ModeAuto, noAuto); got != "" {
		t.Errorf("a mode policy turned off: %q", got)
	}
}
