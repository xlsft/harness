package settings

import (
	"slices"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

func orgPolicy(content string) *syncv1.Document {
	return &syncv1.Document{Scope: commonv1.Scope_SCOPE_ORG, Kind: syncv1.DocumentKind_DOCUMENT_KIND_POLICY, Content: content}
}

func TestPolicyManaged(t *testing.T) {
	if PolicyManaged(nil).Active() {
		t.Fatal("no documents impose nothing")
	}
	if PolicyManaged([]*syncv1.Document{orgPolicy(`{}`), orgPolicy(`not json`)}).Active() {
		t.Fatal("empty and broken documents impose nothing")
	}
	m := PolicyManaged([]*syncv1.Document{
		orgPolicy(`{"permissions":{"deny":["Bash(rm:*)"],"disabledModes":["bypassPermissions"]},"allowManagedPermissionRulesOnly":true,
			"sandbox":{"enabled":true,"allowOutside":false},"allowedProviders":["a","b"],"disablePersonalProviders":true,"allowedMcpServers":["github","jira"],"disableLocalModels":true}`),
		orgPolicy(`{"permissions":{"allow":["Read"]},"allowedProviders":["b","c"],"allowedMcpServers":[]}`),
		// A user's own document is not policy, whatever it says.
		{Scope: commonv1.Scope_SCOPE_USER, Content: `{"allowedProviders":["x"],"sandbox":{"enabled":false}}`},
	})
	if !m.Active() || !m.ManagedRulesOnly || !m.SandboxRequired || !m.SandboxOutsideRefused || !m.PersonalProvidersDisabled || !m.LocalModelsDisabled {
		t.Fatalf("flags: %+v", m)
	}
	if !slices.Equal(m.DenyRules, []string{"Bash(rm:*)"}) || !slices.Equal(m.AllowRules, []string{"Read"}) || !slices.Equal(m.DisabledModes, []string{"bypassPermissions"}) {
		t.Fatalf("rules: %+v", m)
	}
	if !m.RestrictProviders || !slices.Equal(m.AllowedProviders, []string{"b"}) {
		t.Fatalf("providers intersect: %+v", m.AllowedProviders)
	}
	if !m.RestrictMcpServers || len(m.AllowedMcpServers) != 0 || m.McpServerAllowed("github") {
		t.Fatalf("an empty list allows nothing: %+v", m.AllowedMcpServers)
	}
	if m.ProviderAllowed("local") || !m.ProviderAllowed("x", "b") {
		t.Fatal("provider names")
	}
	open := PolicyManaged([]*syncv1.Document{orgPolicy(`{"sandbox":{"enabled":true}}`)})
	if !open.McpServerAllowed("anything") || !open.ProviderAllowed("local") {
		t.Fatal("no list means no restriction")
	}
}
