package permissions_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/xlsft/harness/packages/client/permissions"
)

func TestClosestAllowed(t *testing.T) {
	allow := func(ms ...permissions.Mode) func(permissions.Mode) bool {
		return func(m permissions.Mode) bool { return slices.Contains(ms, m) }
	}
	cases := []struct {
		from    permissions.Mode
		allowed func(permissions.Mode) bool
		want    permissions.Mode
	}{
		{permissions.ModeBypassPermissions, allow(permissions.ModeAuto), permissions.ModeAuto},
		{permissions.ModeBypassPermissions, allow(permissions.ModeAcceptEdits), permissions.ModeDefault},
		{permissions.ModeAuto, allow(permissions.ModeAcceptEdits), permissions.ModeDefault},
		{permissions.ModeDontAsk, allow(), permissions.ModeDefault},
		{permissions.ModePlan, allow(permissions.ModePlan), permissions.ModePlan},
	}
	for _, c := range cases {
		if got := permissions.ClosestAllowed(c.from, c.allowed); got != c.want {
			t.Errorf("%s -> %s, want %s", c.from, got, c.want)
		}
	}
}

func TestModePolicySet(t *testing.T) {
	p := permissions.NewModePolicy(permissions.ModeDefault, permissions.ModeBypassPermissions, "bogus")
	if !slices.Equal(p.Disabled(), []permissions.Mode{permissions.ModeBypassPermissions}) || !p.Allowed(permissions.ModeDefault) {
		t.Fatalf("default and unknown modes are never disabled: %v", p.Disabled())
	}
	var calls [][]permissions.Mode
	stop := p.OnChange(func(d []permissions.Mode) { calls = append(calls, d) })
	if p.Set([]permissions.Mode{permissions.ModeBypassPermissions}) || len(calls) != 0 {
		t.Fatal("an unchanged set must not notify")
	}
	p.Set([]permissions.Mode{permissions.ModeAuto, permissions.ModeDontAsk})
	if len(calls) != 1 || !slices.Equal(calls[0], []permissions.Mode{permissions.ModeAuto, permissions.ModeDontAsk}) {
		t.Fatalf("calls %v", calls)
	}
	stop()
	p.Set(nil)
	if len(calls) != 1 {
		t.Fatal("unsubscribed")
	}
	var nilPolicy *permissions.ModePolicy
	if !nilPolicy.Allowed(permissions.ModeBypassPermissions) || nilPolicy.Disabled() != nil {
		t.Fatal("a nil policy allows everything")
	}
}

func TestEnginePolicyDisabled(t *testing.T) {
	dir := t.TempDir()
	e := permissions.New(permissions.Config{Cwd: dir, Home: dir, DefaultMode: permissions.ModeBypassPermissions, DisabledModes: []permissions.Mode{permissions.ModeBypassPermissions}})
	if e.Mode() != permissions.ModeDefault {
		t.Fatalf("a disabled start mode falls back to default, got %s", e.Mode())
	}
	var de *permissions.ModeDisabledError
	if err := e.SetMode(permissions.ModeBypassPermissions); !errors.As(err, &de) || !de.Policy {
		t.Fatalf("SetMode(bypass) = %v", err)
	}
	if err := e.SetMode(permissions.ModeBypassPermissions); err == nil {
		t.Fatal("still disabled")
	}

	// The Shift+Tab cycle skips disabled modes.
	e.SetPolicyDisabled([]permissions.Mode{permissions.ModeAcceptEdits, permissions.ModeAuto})
	if e.NextMode() != permissions.ModePlan {
		t.Fatalf("next after default: %s", e.NextMode())
	}

	// A live switch away from a newly disabled mode, to the closest allowed one.
	e.SetPolicyDisabled(nil)
	if err := e.SetMode(permissions.ModeBypassPermissions); err != nil {
		t.Fatal(err)
	}
	var changed []permissions.Mode
	e.OnModeChange(func(m permissions.Mode) { changed = append(changed, m) })
	from, to, switched := e.SetPolicyDisabled([]permissions.Mode{permissions.ModeBypassPermissions})
	if !switched || from != permissions.ModeBypassPermissions || to != permissions.ModeAuto || e.Mode() != permissions.ModeAuto || len(changed) != 1 {
		t.Fatalf("switch %s -> %s (%v), mode %s, changes %v", from, to, switched, e.Mode(), changed)
	}
	if _, _, switched := e.SetPolicyDisabled([]permissions.Mode{permissions.ModeBypassPermissions, permissions.ModePlan}); switched {
		t.Fatal("an allowed mode is kept")
	}
	if _, to, _ := e.SetPolicyDisabled([]permissions.Mode{permissions.ModeAuto}); to != permissions.ModeDefault {
		t.Fatalf("auto falls back to default, got %s", to)
	}
}
