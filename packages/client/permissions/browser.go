package permissions

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
)

// Browser tools (packages/client/browser) act on web pages in the session's browser. Read-only ones (snapshot,
// screenshot, console, network, wait, listing tabs) are allowed like any read-only tool. Actions are checked
// per site, from the page they act on (Request.URL, filled from tools.PermissionHints):
//
//   - pages of this machine (localhost, *.localhost, 127.0.0.0/8, ::1) and about:blank are allowed, and so are
//     file: pages inside the working, scratch and readable directories (other files ask, as Read would);
//   - a site the user approved in this session (any "allow" answer to a browser action on it) is allowed;
//   - a site matched by an allow rule is allowed: Browser(domain:example.com) covers every site action
//     (BrowserNavigate, BrowserClick, BrowserType, BrowserKey, BrowserScroll, BrowserTabs), or one tool's
//     rule such as BrowserClick(domain:*.example.com); the "always allow" answer adds Browser(domain:<host>);
//   - other private-network hosts (LAN, link-local, metadata) ask the user, never the auto-mode Decider;
//   - any other site asks (the first agent action on a new site), or goes to the Decider in auto mode.
//
// BrowserEval runs arbitrary JavaScript, so it asks on every site, local ones included, unless an allow rule
// names it (BrowserEval, or BrowserEval(domain:...)). Typing into a password or payment field
// (Request.Sensitive) always asks the user, whatever the mode or rules.

// BrowserRuleTool is the rule tool name that covers every browser site action.
const BrowserRuleTool = "Browser"

// browserSiteTools act on a site and share the per-site approval.
var browserSiteTools = map[string]bool{
	"BrowserNavigate": true, "BrowserClick": true, "BrowserType": true, "BrowserKey": true, "BrowserScroll": true,
	"BrowserTabs": true,
}

func isBrowserTool(name string) bool { return browserSiteTools[name] || name == "BrowserEval" }

// IsBrowserTool reports whether name is a browser action tool.
func IsBrowserTool(name string) bool { return isBrowserTool(name) }

// browserSite returns the lowercase host a browser call acts on, and whether it is local (this machine, a file
// or a blank page) and needs no approval.
func browserSite(raw string) (host string, local bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "about:blank" {
		return "", true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "file":
		return "", true
	case "http", "https":
	default:
		return "", false
	}
	host = strings.ToLower(u.Hostname())
	return host, localHost(host)
}

// browserFilePath returns the path of a file: URL ("" for anything else).
func browserFilePath(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "file") {
		return ""
	}
	return filepath.Clean(filepath.FromSlash(u.Path))
}

func localHost(h string) bool {
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}

// evalBrowser decides a browser action after deny / ask rules and the bypass mode were applied. Caller holds
// e.mu (read).
func (e *Engine) evalBrowser(req Request, input map[string]any, mc matchContext) Decision {
	for _, r := range e.allow {
		if r.matchesCall(req, input, mc, false) {
			return Decision{Behavior: Allow, Rule: r.String(), ResolvedBy: "rule"}
		}
	}
	host, local := browserSite(req.URL)
	if path := browserFilePath(req.URL); path != "" && !inAny(path, e.workDirs) && !inAny(path, e.scratch) && !inAny(path, e.readable) {
		return Decision{Behavior: Ask, Message: fmt.Sprintf("The browser would open %s, which is outside the working directories.", path)}
	}
	if req.ToolName == "BrowserEval" {
		where := "this page"
		if host != "" {
			where = host
		}
		return Decision{Behavior: Ask, Message: fmt.Sprintf("BrowserEval runs arbitrary JavaScript on %s with the page's cookies and storage.", where)}
	}
	if local {
		return Decision{Behavior: Allow, ResolvedBy: "site"}
	}
	if host == "" {
		u, _ := url.Parse(req.URL)
		scheme := ""
		if u != nil {
			scheme = u.Scheme
		}
		return Decision{Behavior: Ask, UserOnly: true, Message: fmt.Sprintf("The browser would act on a %q page; only http, https and file pages are expected.", scheme)}
	}
	if e.sites[host] {
		return Decision{Behavior: Allow, ResolvedBy: "site"}
	}
	if e.guard().IsPrivate(context.Background(), host) {
		return Decision{Behavior: Ask, UserOnly: true, Message: fmt.Sprintf(
			"%s is a private-network address (LAN, link-local or cloud metadata); the agent may use it in the browser only with the user's explicit approval.", host)}
	}
	return Decision{Behavior: Ask, Message: fmt.Sprintf("The agent wants to use %s in the browser for the first time in this session.", host)}
}

// suggestBrowser proposes the rule an "always allow" answer adds.
func suggestBrowser(req Request) []string {
	host, local := browserSite(req.URL)
	if req.ToolName == "BrowserEval" {
		if host != "" && !local {
			return []string{"BrowserEval(domain:" + host + ")", "BrowserEval"}
		}
		return []string{"BrowserEval"}
	}
	if host == "" || local {
		return []string{req.ToolName}
	}
	return []string{BrowserRuleTool + "(domain:" + host + ")"}
}

// grantSite remembers that the user approved a browser action on req's site, for the rest of the session.
func (e *Engine) grantSite(req Request) {
	if !browserSiteTools[req.ToolName] {
		return
	}
	host, local := browserSite(req.URL)
	if host == "" || local {
		return
	}
	e.mu.Lock()
	if e.sites == nil {
		e.sites = map[string]bool{}
	}
	e.sites[host] = true
	e.mu.Unlock()
}

// AllowSite approves a site for the browser tools for the rest of the session.
func (e *Engine) AllowSite(host string) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return
	}
	e.mu.Lock()
	if e.sites == nil {
		e.sites = map[string]bool{}
	}
	e.sites[host] = true
	e.mu.Unlock()
}

// RevokeSite withdraws a session approval of a site.
func (e *Engine) RevokeSite(host string) {
	e.mu.Lock()
	delete(e.sites, strings.ToLower(strings.TrimSpace(host)))
	e.mu.Unlock()
}

// AllowedSites lists the sites approved for the browser tools in this session, sorted.
func (e *Engine) AllowedSites() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]string, 0, len(e.sites))
	for h := range e.sites {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}
