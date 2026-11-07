package permissions

import "testing"

func TestForcedModesOutliveSet(t *testing.T) {
	p := NewModePolicy()
	p.Force(ModeBypassPermissions)
	if p.Allowed(ModeBypassPermissions) {
		t.Fatal("forced off")
	}
	p.Set([]Mode{ModePlan})
	if p.Allowed(ModeBypassPermissions) || p.Allowed(ModePlan) {
		t.Fatal("a policy update keeps the forced mode off")
	}
	p.Unforce(ModeBypassPermissions)
	if !p.Allowed(ModeBypassPermissions) || p.Allowed(ModePlan) {
		t.Fatal("unforced, the policy decides again")
	}
	p.Force(ModeBypassPermissions)
	p.Set([]Mode{ModeBypassPermissions})
	p.Unforce(ModeBypassPermissions)
	if p.Allowed(ModeBypassPermissions) {
		t.Fatal("the organization's own policy still disables it")
	}
}
