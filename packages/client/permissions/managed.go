package permissions

import "slices"

// ManagedPolicy is what the organization's managed policy imposes (settings.PolicyManaged), kept with the live mode policy.
// The zero value imposes nothing.
type ManagedPolicy struct {
	AllowRules, AskRules, DenyRules []string
	ManagedRulesOnly                bool
	SandboxRequired                 bool
	SandboxOutsideRefused           bool
	DisabledModes                   []string
	RestrictProviders               bool
	AllowedProviders                []string
	PersonalProvidersDisabled       bool
	RestrictMcpServers              bool
	AllowedMcpServers               []string
	LocalModelsDisabled             bool
}

// Active reports whether it imposes anything.
func (m ManagedPolicy) Active() bool {
	return len(m.AllowRules)+len(m.AskRules)+len(m.DenyRules)+len(m.DisabledModes) > 0 || m.ManagedRulesOnly || m.SandboxRequired ||
		m.SandboxOutsideRefused || m.RestrictProviders || m.PersonalProvidersDisabled || m.RestrictMcpServers || m.LocalModelsDisabled
}

// McpServerAllowed reports whether an MCP server of this name may be used.
func (m ManagedPolicy) McpServerAllowed(name string) bool {
	return !m.RestrictMcpServers || slices.Contains(m.AllowedMcpServers, name)
}

// ProviderAllowed reports whether a provider the client talks to itself may be used, by any of its names: "local" for
// local models, "acp" or "acp:<id>" for an external agent. The server applies the list to its own providers.
func (m ManagedPolicy) ProviderAllowed(names ...string) bool {
	if !m.RestrictProviders {
		return true
	}
	for _, n := range names {
		if slices.Contains(m.AllowedProviders, n) {
			return true
		}
	}
	return false
}

// SetManaged records what the policy documents impose beyond modes and the sandbox.
func (p *ModePolicy) SetManaged(m ManagedPolicy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.managed = m
}

// Managed returns what SetManaged recorded. Nil-safe.
func (p *ModePolicy) Managed() ManagedPolicy {
	if p == nil {
		return ManagedPolicy{}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.managed
}
