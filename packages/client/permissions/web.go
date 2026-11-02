package permissions

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/xlsft/harness/packages/client/netguard"
)

// Web tools are read-only (they run in parallel and in plan mode) but, as in
// Claude Code, they are not allowed by default: WebSearch needs an allow rule
// (`WebSearch`), WebFetch a domain rule (`WebFetch(domain:example.com)`) or
// the user's approval. In auto mode the Decider handles what would prompt.
//
// WebFetch of a private-network target (localhost, RFC 1918, link-local,
// metadata addresses, names that resolve there) is never auto-approved: only
// an allow rule naming that exact host, the bypassPermissions mode or the user
// can approve it, and the auto-mode Decider is not consulted.
func isWebTool(name string) bool { return name == "WebFetch" || name == "WebSearch" }

// WebFetchHost returns the lowercase host name of a WebFetch input URL.
func WebFetchHost(input map[string]any) string {
	raw, _ := input["url"].(string)
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func (e *Engine) guard() *netguard.Guard {
	if e.netGuard != nil {
		return e.netGuard
	}
	return netguard.Default
}

// evalWeb decides a web tool call after deny / ask rules and the
// bypassPermissions mode were applied.
func (e *Engine) evalWeb(req Request, input map[string]any, mc matchContext) Decision {
	if req.ToolName == "WebFetch" {
		host := WebFetchHost(input)
		if host != "" && e.guard().IsPrivate(context.Background(), host) {
			for _, r := range e.allow {
				if r.Tool == "WebFetch" && strings.EqualFold(r.Spec, "domain:"+host) {
					return Decision{Behavior: Allow, Rule: r.String(), ResolvedBy: "rule"}
				}
			}
			return Decision{Behavior: Ask, UserOnly: true, Message: fmt.Sprintf(
				"%s is a private-network address (localhost, LAN, link-local or cloud metadata); fetching it needs the user's explicit approval.", host)}
		}
	}
	for _, r := range e.allow {
		if r.matchesCall(req, input, mc, false) {
			return Decision{Behavior: Allow, Rule: r.String(), ResolvedBy: "rule"}
		}
	}
	return Decision{Behavior: Ask}
}

// suggestWeb proposes the rule an "always allow" answer adds.
func suggestWeb(req Request) []string {
	if req.ToolName == "WebFetch" {
		if host := WebFetchHost(decodeInput(req.Input)); host != "" {
			return []string{"WebFetch(domain:" + host + ")"}
		}
	}
	return []string{req.ToolName}
}
