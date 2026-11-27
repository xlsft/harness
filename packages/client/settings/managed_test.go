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

func TestPolicyDeferBuiltinTools(t *testing.T) {
	if PolicyManaged([]*syncv1.Document{orgPolicy(`{}`)}).DeferBuiltinTools != nil {
		t.Fatal("unset: the user chooses")
	}
	on := PolicyManaged([]*syncv1.Document{orgPolicy(`{"deferBuiltinTools":true}`)})
	if on.DeferBuiltinTools == nil || !*on.DeferBuiltinTools || !on.Active() {
		t.Fatalf("forced on: %+v", on)
	}
	both := PolicyManaged([]*syncv1.Document{orgPolicy(`{"deferBuiltinTools":false}`), orgPolicy(`{"deferBuiltinTools":true}`)})
	if both.DeferBuiltinTools == nil || *both.DeferBuiltinTools {
		t.Fatalf("off in any document wins: %+v", both)
	}
}

func TestPolicyClientVersions(t *testing.T) {
	m := PolicyManaged([]*syncv1.Document{
		orgPolicy(`{"minimumClientVersion":"0.2.0","maximumClientVersion":"v0.5.0"}`),
		orgPolicy(`{"minimumClientVersion":"v0.3.1","maximumClientVersion":"0.4.0"}`),
		orgPolicy(`{"minimumClientVersion":"soon","maximumClientVersion":"later"}`),
	})
	if m.MinClientVersion != "0.3.1" || m.MaxClientVersion != "0.4.0" {
		t.Fatalf("the strictest bounds win: %q %q", m.MinClientVersion, m.MaxClientVersion)
	}
	if !m.Active() {
		t.Fatal("a version bound is policy in force")
	}
	if got := PolicyManaged([]*syncv1.Document{orgPolicy(`{"minimumClientVersion":"latest"}`)}); got.MinClientVersion != "" || got.Active() {
		t.Fatalf("a bound that isn't a version: %+v", got)
	}
}
