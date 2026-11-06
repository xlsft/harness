package plugins

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

// The keys of a POLICY document this package reads. They are the managed-settings keys of the format; the aliases are read too.
const (
	KeyAllowedSources = "strictKnownMarketplaces"
	KeyBlockedSources = "blockedMarketplaces"
	KeyExtra          = "extraKnownMarketplaces"
	KeyEnabled        = "enabledPlugins"
	keyAllowedAlias   = "allowedMarketplaces"
	keyExtraAlias     = "additionalMarketplaces"
	keyAllowedMCP     = "allowedMcpServers"
)

// Rule matches marketplace sources in an allowlist or a blocklist. Beyond the source types it can be hostPattern or pathPattern.
type Rule struct {
	Source      string `json:"source"`
	Repo        string `json:"repo,omitempty"`
	URL         string `json:"url,omitempty"`
	Path        string `json:"path,omitempty"`
	Ref         string `json:"ref,omitempty"`
	HostPattern string `json:"hostPattern,omitempty"`
	PathPattern string `json:"pathPattern,omitempty"`
}

// Policy is what the organization says about plugins: enforced here, before a download and again when a plugin would load.
type Policy struct {
	// Allowlists holds one list per document that set one: a source must be on all of them. An empty list allows nothing.
	Allowlists [][]Rule `json:"allowlists,omitempty"`
	Blocked    []Rule   `json:"blocked,omitempty"`
	// Distributed are the marketplaces the organization registers on every device.
	Distributed map[string]Source `json:"distributed,omitempty"`
	// Plugins holds enabledPlugins: true distributes and locks on, false blocks at every level.
	Plugins map[string]bool `json:"plugins,omitempty"`
	// RestrictMCP and AllowedMCP are allowedMcpServers, which covers plugins' servers too.
	RestrictMCP bool     `json:"restrictMcp,omitempty"`
	AllowedMCP  []string `json:"allowedMcp,omitempty"`
}

// Restricted reports whether an allowlist is in force.
func (p Policy) Restricted() bool { return len(p.Allowlists) > 0 }

// SourceRefusal says why a source may not be used, "" when it may.
func (p Policy) SourceRefusal(s Source) string {
	for _, r := range p.Blocked {
		if r.matches(s) {
			return s.String() + " is blocked by your organization's policy."
		}
	}
	for _, list := range p.Allowlists {
		if !slices.ContainsFunc(list, func(r Rule) bool { return r.matches(s) }) {
			return s.String() + " is not on your organization's list of allowed plugin sources."
		}
	}
	return ""
}

// PluginBlocked reports enabledPlugins false for the id.
func (p Policy) PluginBlocked(id string) bool {
	on, ok := p.Plugins[id]
	return ok && !on
}

// PluginForced reports enabledPlugins true for the id.
func (p Policy) PluginForced(id string) bool { return p.Plugins[id] }

// MCPAllowed says whether a plugin's server may be used, under its full name or its name inside the plugin.
func (p Policy) MCPAllowed(full, short string) bool {
	return !p.RestrictMCP || slices.Contains(p.AllowedMCP, full) || slices.Contains(p.AllowedMCP, short)
}

func (r Rule) matches(s Source) bool {
	switch r.Source {
	case "hostPattern":
		re, err := regexp.Compile(r.HostPattern)
		host := s.Host()
		return err == nil && host != "" && re.MatchString(host)
	case "pathPattern":
		re, err := regexp.Compile(r.PathPattern)
		return err == nil && (s.Source == SourceFile || s.Source == SourceDirectory) && re.MatchString(s.Path)
	case SourceURL:
		return s.Source == SourceURL && canonicalURL(r.URL) == canonicalURL(s.URL)
	case SourceFile, SourceDirectory:
		return s.Source == r.Source && filepath.Clean(r.Path) == filepath.Clean(s.Path)
	case SourceGitHub, SourceGit:
		if s.Source != SourceGitHub && s.Source != SourceGit {
			return false
		}
		if r.Ref != s.Ref {
			return false
		}
		if r.Source == SourceGitHub && strings.HasSuffix(r.Repo, "/*") {
			owner := strings.TrimSuffix(r.Repo, "/*")
			repo := strings.TrimPrefix(canonicalURL(s.cloneURL()), "https://github.com/")
			return !strings.Contains(owner, "/") && strings.HasPrefix(canonicalURL(s.cloneURL()), "https://github.com/") && strings.HasPrefix(strings.ToLower(repo), strings.ToLower(owner)+"/")
		}
		if r.Path != "" && r.Path != s.Path {
			return false
		}
		want := Source{Source: r.Source, Repo: r.Repo, URL: r.URL}
		return canonicalURL(want.cloneURL()) == canonicalURL(s.cloneURL())
	}
	return false
}

// canonicalURL gives the spellings of one repository one form: scp syntax and ssh become https, the host is lower case, and a
// .git suffix and trailing slashes go.
func canonicalURL(raw string) string {
	s := strings.TrimSpace(raw)
	if m := scpRe.FindStringSubmatch(s); m != nil {
		s = "https://" + m[1] + "/" + m[2]
	}
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		if u.Scheme == "ssh" {
			u.Scheme = "https"
		}
		u.User = nil
		u.Host = strings.ToLower(u.Host)
		s = u.String()
	}
	return strings.TrimSuffix(strings.TrimRight(s, "/"), ".git")
}

// PolicyFromDocs merges the org POLICY documents. A restriction in any of them stands.
func PolicyFromDocs(docs []*syncv1.Document) Policy {
	p := Policy{Distributed: map[string]Source{}, Plugins: map[string]bool{}}
	for _, d := range docs {
		if d.GetScope() != commonv1.Scope_SCOPE_ORG {
			continue
		}
		var raw map[string]json.RawMessage
		if json.Unmarshal([]byte(d.GetContent()), &raw) != nil {
			continue
		}
		list := raw[KeyAllowedSources]
		if list == nil {
			list = raw[keyAllowedAlias]
		}
		if list != nil {
			var rules []Rule
			if json.Unmarshal(list, &rules) != nil {
				rules = []Rule{} // a list that can't be read allows nothing
			}
			if rules == nil {
				rules = []Rule{}
			}
			p.Allowlists = append(p.Allowlists, rules)
		}
		if b := raw[KeyBlockedSources]; b != nil {
			var rules []Rule
			if json.Unmarshal(b, &rules) == nil {
				p.Blocked = append(p.Blocked, rules...)
			}
		}
		extra := raw[KeyExtra]
		if extra == nil {
			extra = raw[keyExtraAlias]
		}
		var known map[string]struct {
			Source Source `json:"source"`
		}
		if extra != nil && json.Unmarshal(extra, &known) == nil {
			for name, m := range known {
				p.Distributed[name] = m.Source
			}
		}
		var enabled map[string]bool
		if e := raw[KeyEnabled]; e != nil && json.Unmarshal(e, &enabled) == nil {
			for id, on := range enabled {
				if prev, ok := p.Plugins[id]; !ok || prev {
					p.Plugins[id] = on
				}
			}
		}
		if a := raw[keyAllowedMCP]; a != nil {
			var names []string
			if json.Unmarshal(a, &names) == nil {
				if p.RestrictMCP {
					p.AllowedMCP = slices.DeleteFunc(p.AllowedMCP, func(n string) bool { return !slices.Contains(names, n) })
				} else {
					p.AllowedMCP, p.RestrictMCP = names, true
				}
			}
		}
	}
	return p
}

// The policy in force in this process, which the document watcher sets. An unset one is read from what the last run left, so
// a command-line call and an offline start obey it too.
var (
	current atomic.Pointer[Policy]
)

const policyFile = "policy.json"

// SetPolicy records the policy for this process and for later ones starting offline.
func SetPolicy(root string, p Policy) {
	current.Store(&p)
	if root == "" {
		return
	}
	if data, err := json.MarshalIndent(p, "", "  "); err == nil {
		_ = writeFileAtomic(filepath.Join(root, policyFile), data)
	}
}

// CurrentPolicy returns what SetPolicy recorded, in this process or an earlier one under root.
func CurrentPolicy(root string) Policy {
	if p := current.Load(); p != nil {
		return *p
	}
	var p Policy
	if data, err := os.ReadFile(filepath.Join(root, policyFile)); err == nil {
		_ = json.Unmarshal(data, &p)
	}
	return p
}

// ResetPolicy forgets this process's policy (tests).
func ResetPolicy() { current.Store(nil) }

// writeFileAtomic replaces a file of this device: mode 0600, never half written.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
