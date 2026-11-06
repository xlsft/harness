package permissions

import (
	"fmt"
	"slices"
	"sync"
)

// AllModes lists every mode: the Shift+Tab cycle, then the modes outside it.
func AllModes() []Mode {
	return append(ModeCycle(), ModeDontAsk, ModeBypassPermissions)
}

// ModeDisabledError is returned when a mode is switched on that settings or
// the server's policy forbid.
type ModeDisabledError struct {
	Mode Mode
	// Policy is set when the server's mode policy (an admin) disabled it;
	// otherwise a setting did (disableBypassPermissionsMode, disableAutoMode).
	Policy bool
}

func (e *ModeDisabledError) Error() string {
	switch {
	case e.Policy:
		return fmt.Sprintf("permission mode %q is disabled by your administrator", e.Mode)
	case e.Mode == ModeBypassPermissions:
		return "bypassPermissions mode is disabled by policy"
	case e.Mode == ModeAuto:
		return "auto mode is disabled by settings (disableAutoMode)"
	}
	return fmt.Sprintf("permission mode %q is disabled", e.Mode)
}

// closer lists, per mode, the modes to fall back to when it is disabled
// while a session is in it, nearest first; default ends every chain and can
// never be disabled.
var closer = map[Mode][]Mode{
	ModeBypassPermissions: {ModeAuto},
}

// ClosestAllowed returns m when allowed, else the nearest allowed fallback
// (bypassPermissions -> auto -> default; every other mode -> default).
func ClosestAllowed(m Mode, allowed func(Mode) bool) Mode {
	if m == ModeDefault || allowed(m) {
		return m
	}
	for _, c := range closer[m] {
		if allowed(c) {
			return c
		}
	}
	return ModeDefault
}

// ModePolicy is the server-wide set of disabled permission modes (harness.auth.v1.AdminService
// SetPermissionModePolicy, read from the org POLICY documents). One instance is shared by every session of a
// client and updated live; it is safe for concurrent use. Default is never disabled.
type ModePolicy struct {
	mu       sync.RWMutex
	disabled map[Mode]bool
	subs     map[int]func([]Mode)
	subID    int
	// The same documents can require the command sandbox and refuse running outside it (settings.PolicySandbox).
	sandboxRequired, outsideRefused bool
	managed                         ManagedPolicy
}

// SetSandbox records what the policy documents say of the command sandbox.
func (p *ModePolicy) SetSandbox(required, outsideRefused bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sandboxRequired, p.outsideRefused = required, outsideRefused
}

// Sandbox returns what SetSandbox recorded. Nil-safe.
func (p *ModePolicy) Sandbox() (required, outsideRefused bool) {
	if p == nil {
		return false, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sandboxRequired, p.outsideRefused
}

// NewModePolicy returns a policy with the given modes disabled.
func NewModePolicy(disabled ...Mode) *ModePolicy {
	p := &ModePolicy{disabled: map[Mode]bool{}, subs: map[int]func([]Mode){}}
	p.Set(disabled)
	return p
}

// Set replaces the disabled modes; unknown modes and default are ignored.
// Listeners run (synchronously, outside the lock) when the set changed.
func (p *ModePolicy) Set(modes []Mode) bool {
	next := map[Mode]bool{}
	for _, m := range modes {
		if m != ModeDefault && slices.Contains(AllModes(), m) {
			next[m] = true
		}
	}
	p.mu.Lock()
	changed := len(next) != len(p.disabled)
	for m := range next {
		if !p.disabled[m] {
			changed = true
		}
	}
	p.disabled = next
	subs := make([]func([]Mode), 0, len(p.subs))
	for _, f := range p.subs {
		subs = append(subs, f)
	}
	p.mu.Unlock()
	if changed {
		d := p.Disabled()
		for _, f := range subs {
			f(d)
		}
	}
	return changed
}

// Disabled returns the disabled modes in AllModes order. Nil-safe.
func (p *ModePolicy) Disabled() []Mode {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []Mode
	for _, m := range AllModes() {
		if p.disabled[m] {
			out = append(out, m)
		}
	}
	return out
}

// Allowed reports whether m may be used. Nil-safe (a nil policy allows everything).
func (p *ModePolicy) Allowed(m Mode) bool {
	if p == nil {
		return true
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return !p.disabled[m]
}

// OnChange registers f, called with the new disabled set after every change.
// The returned func unregisters it.
func (p *ModePolicy) OnChange(f func([]Mode)) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subID++
	id := p.subID
	p.subs[id] = f
	return func() {
		p.mu.Lock()
		delete(p.subs, id)
		p.mu.Unlock()
	}
}
