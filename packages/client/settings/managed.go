package settings

import (
	"encoding/json"
	"slices"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/permissions"
)

// PolicyManaged merges the org POLICY documents into what the organization imposes: the view frontends show as "set by your
// administrator", and the allowlists the client applies where only it can (a repository's .mcp.json, local models, agents
// it starts itself). Session settings take the rest from the same documents through the "policy" level. Lists of rules add
// up, allowlists intersect, and a restriction in any document stands.
func PolicyManaged(docs []*syncv1.Document) permissions.ManagedPolicy {
	var m permissions.ManagedPolicy
	for _, d := range docs {
		if d.GetScope() != commonv1.Scope_SCOPE_ORG {
			continue
		}
		var raw struct {
			Permissions struct {
				Allow []string `json:"allow"`
				Ask   []string `json:"ask"`
				Deny  []string `json:"deny"`
			} `json:"permissions"`
			ManagedOnly bool `json:"allowManagedPermissionRulesOnly"`
			Sandbox     struct {
				Enabled      *bool `json:"enabled"`
				AllowOutside *bool `json:"allowOutside"`
			} `json:"sandbox"`
			AllowedProviders *[]string       `json:"allowedProviders"`
			DisablePersonal  bool            `json:"disablePersonalProviders"`
			AllowedMcp       *[]string       `json:"allowedMcpServers"`
			DisableLocal     json.RawMessage `json:"disableLocalModels"`
		}
		if json.Unmarshal([]byte(d.GetContent()), &raw) != nil {
			continue
		}
		m.AllowRules = appendNew(m.AllowRules, raw.Permissions.Allow)
		m.AskRules = appendNew(m.AskRules, raw.Permissions.Ask)
		m.DenyRules = appendNew(m.DenyRules, raw.Permissions.Deny)
		m.ManagedRulesOnly = m.ManagedRulesOnly || raw.ManagedOnly
		m.SandboxRequired = m.SandboxRequired || (raw.Sandbox.Enabled != nil && *raw.Sandbox.Enabled)
		m.SandboxOutsideRefused = m.SandboxOutsideRefused || (raw.Sandbox.AllowOutside != nil && !*raw.Sandbox.AllowOutside)
		m.PersonalProvidersDisabled = m.PersonalProvidersDisabled || raw.DisablePersonal
		if raw.AllowedProviders != nil {
			m.AllowedProviders = intersect(m.RestrictProviders, m.AllowedProviders, *raw.AllowedProviders)
			m.RestrictProviders = true
		}
		if raw.AllowedMcp != nil {
			m.AllowedMcpServers = intersect(m.RestrictMcpServers, m.AllowedMcpServers, *raw.AllowedMcp)
			m.RestrictMcpServers = true
		}
		m.LocalModelsDisabled = m.LocalModelsDisabled || disableFlag(raw.DisableLocal)
	}
	m.DisabledModes = PolicyDisabledModes(docs)
	return m
}

func intersect(restricted bool, have, other []string) []string {
	if !restricted {
		return append([]string{}, other...)
	}
	out := []string{}
	for _, s := range have {
		if slices.Contains(other, s) {
			out = append(out, s)
		}
	}
	return out
}
