package permissions

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// modeOf is the mode a request is judged under.
func (e *Engine) modeOf(req Request) Mode {
	if req.Mode != "" {
		return req.Mode
	}
	return e.mode
}

// modeStrictness orders the modes, stricter first.
var modeStrictness = map[Mode]int{ModePlan: 0, ModeDontAsk: 1, ModeDefault: 2, ModeAcceptEdits: 3, ModeAuto: 4, ModeBypassPermissions: 5}

// AgentMode says which mode an agent works under when its definition asks for wanted and the session is in session; ""
// means the session's. A definition can always make its agent stricter. It can make it freer in two steps only, from
// default to acceptEdits or auto and from acceptEdits to auto, so an agent never gets what the user would have to switch
// on with a warning, and plan mode stays what the user asked for. A mode policy turned off isn't available either.
func AgentMode(session, wanted Mode, allowed func(Mode) bool) Mode {
	if wanted == "" || wanted == session || session == ModePlan {
		return ""
	}
	want, known := modeStrictness[wanted]
	have, knownSession := modeStrictness[session]
	if !known || !knownSession || (allowed != nil && !allowed(wanted)) {
		return ""
	}
	switch {
	case want < have:
		return wanted
	case session == ModeDefault && (wanted == ModeAcceptEdits || wanted == ModeAuto):
		return wanted
	case session == ModeAcceptEdits && wanted == ModeAuto:
		return wanted
	}
	return ""
}

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

// readAllowed: the path is in a working directory or a readable one, or an allow rule opens it (e.mu held).
func (e *Engine) readAllowed(req Request, path string, input map[string]any, mc matchContext) bool {
	if inAny(path, e.workDirs) || inAny(path, e.scratch) || inAny(path, e.readable) {
		return true
	}
	for _, r := range e.allow {
		if r.matchesCall(req, input, mc, true) {
			return true
		}
	}
	return false
}

func readRefusal(path string) Decision {
	return Decision{Behavior: Deny, ResolvedBy: "setting", Message: fmt.Sprintf("%s is outside the working directories, and permissions.restrictReads refuses reads there. Work with what is inside them, or ask the user to add the directory.", path)}
}

// refuseRead applies restrictReads to the read tools (e.mu held). A Glob or Grep without a path looks in the current
// directory, which the tools keep inside the session's folders.
func (e *Engine) refuseRead(req Request, input map[string]any, mc matchContext) (Decision, bool) {
	if !e.restrictReads || !(readTools[req.ToolName] || pathObservers[req.ToolName]) || req.FilePath == "" {
		return Decision{}, false
	}
	path := req.FilePath
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.cwd, path)
	}
	path = filepath.Clean(path)
	if e.readAllowed(req, path, input, mc) {
		return Decision{}, false
	}
	return readRefusal(path), true
}

// refuseBashRead applies restrictReads to the paths a command line names: arguments and input redirections. A path built at
// run time gets through, so this stops the plain case and the sandbox stays the boundary (e.mu held).
func (e *Engine) refuseBashRead(req Request, input map[string]any, top []SimpleCommand) (Decision, bool) {
	if !e.restrictReads {
		return Decision{}, false
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd = e.cwd
	}
	mc := matchContext{cwd: e.cwd, home: e.home}
	check := func(word string) (Decision, bool) {
		named := namedPath(word)
		if named == "" {
			return Decision{}, false
		}
		p := resolvePath(named, cwd, e.home)
		if p == "/dev/null" || e.readAllowed(Request{ToolName: "Read", FilePath: p}, p, map[string]any{"file_path": p}, mc) {
			return Decision{}, false
		}
		return readRefusal(p), true
	}
	for _, c := range top {
		for i, w := range c.Words {
			if i == 0 {
				continue
			}
			if d, refused := check(w.Value); refused {
				return d, true
			}
		}
		for _, r := range c.Redirects {
			if r.Op == "<" {
				if d, refused := check(r.Target); refused {
					return d, true
				}
			}
		}
	}
	return Decision{}, false
}

// namedPath is the path an argument names outright: itself or an option's value (--file=/x). A relative name without a way
// out of the folder doesn't need looking at, and one built at run time can't be, so both give "".
func namedPath(word string) string {
	if strings.ContainsAny(word, "$`") {
		return ""
	}
	if _, v, ok := strings.Cut(word, "="); ok && strings.HasPrefix(word, "-") {
		word = v
	}
	if strings.HasPrefix(word, "/") || strings.HasPrefix(word, "~") || word == ".." || strings.HasPrefix(word, "../") || strings.Contains(word, "/../") {
		return word
	}
	return ""
}
