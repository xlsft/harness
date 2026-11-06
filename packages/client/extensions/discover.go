package extensions

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	skillsv1 "github.com/xlsft/harness/packages/client/gen/harness/skills/v1"
	"github.com/xlsft/harness/packages/client/plugins"
)

// Labels of the sources that aren't a marketplace.
const (
	LabelCatalog  = "Default catalog"
	LabelRegistry = "MCP registry"
)

// Section keys.
const (
	sectionExternal = "external"
	sectionCatalog  = "catalog"
	sectionRegistry = "registry"
	sectionResults  = "results"
	marketPrefix    = "market:"
)

const (
	overviewItems = 6
	allItems      = 500
)

// Inputs is what Discover is built from.
type Inputs struct {
	Catalog  *Catalog
	Markets  []plugins.MarketListings
	Policy   plugins.Policy
	Registry *Registry
	// Configured holds the MCP servers this device already has.
	Configured []*agentv1.McpServerEntry
	// PluginServers are the commands and URLs of installed plugins' servers.
	PluginServers []*agentv1.PluginMcpServer
	// ServerAllowed is the managed policy's word on a server's name.
	ServerAllowed func(name string) bool
	// RemoteDefault is where a remote server goes unless the user says otherwise.
	RemoteDefault agentv1.McpServerSource
	// Search is external skill search; nil: there is none.
	Search SkillSearch
	// ExternalBlocked says the organization turned it off.
	ExternalBlocked bool
	// ExternalInstalled holds the ids already stored.
	ExternalInstalled map[string]bool
}

func listingContents(l plugins.Listing) *agentv1.PluginContents {
	if l.Contents == nil {
		return nil
	}
	c := l.Contents
	out := &agentv1.PluginContents{Skills: c.Skills, Commands: c.Commands, Agents: c.Agents, OutputStyles: c.OutputStyles, AllowedTools: c.AllowedTools, Skipped: c.Skipped}
	for _, h := range c.Hooks {
		out.Hooks = append(out.Hooks, &agentv1.PluginHook{Event: h.Event, Matcher: h.Matcher, Type: h.Type, Command: h.Command, FromSkill: h.From})
	}
	for _, m := range c.MCPServers {
		out.McpServers = append(out.McpServers, &agentv1.PluginMcpServer{Name: m.Name, Transport: m.Transport, Command: m.Command})
	}
	return out
}

func listingItem(l plugins.Listing, mk plugins.MarketListings) *agentv1.DiscoverItem {
	name := l.DisplayName
	if name == "" {
		name = l.Name
	}
	return &agentv1.DiscoverItem{Id: l.ID, Kind: agentv1.ExtensionKind_EXTENSION_KIND_PLUGIN, Name: name, Description: l.Description, Publisher: l.Meta.Publisher, SourceLabel: mk.Name,
		Version: l.Version, Homepage: l.Meta.Homepage, License: l.Meta.License, Category: l.Meta.Category, Tags: l.Meta.Tags, UpdatedAt: mk.UpdatedAt, Installed: l.Installed,
		Refusal: mk.Refusal, PluginId: l.ID, Contents: listingContents(l), Problem: l.Problem}
}

// skillItems are the skills a marketplace's plugin holds. Adding one installs that plugin: a skill has no install of its own.
func skillItems(l plugins.Listing, mk plugins.MarketListings) []*agentv1.DiscoverItem {
	out := make([]*agentv1.DiscoverItem, 0, len(l.Skills))
	base := listingItem(l, mk)
	for _, s := range l.Skills {
		// A plugin that is one skill of its name reads as that name.
		name := s.Name
		if name == l.Name+":"+l.Name {
			name = l.Name
		}
		out = append(out, &agentv1.DiscoverItem{Id: "skill:" + s.Name + "@" + mk.Name, Kind: agentv1.ExtensionKind_EXTENSION_KIND_SKILL, Name: name, Description: s.Description, Publisher: base.GetPublisher(),
			SourceLabel: mk.Name, Version: base.GetVersion(), Homepage: base.GetHomepage(), License: base.GetLicense(), Category: base.GetCategory(), Tags: base.GetTags(), UpdatedAt: base.GetUpdatedAt(),
			Installed: base.GetInstalled(), Refusal: base.GetRefusal(), PluginId: base.GetPluginId(), Contents: base.GetContents()})
	}
	return out
}

func connectorInstalled(in Inputs, remote bool, endpoint string) bool {
	k := endpointKey(remote, endpoint)
	if k == "" {
		return false
	}
	for _, e := range in.Configured {
		if endpointKey(isRemoteTransport(transportName(e.GetServer().GetTransport())), e.GetLaunchSummary()) == k {
			return true
		}
	}
	return slices.ContainsFunc(in.PluginServers, func(m *agentv1.PluginMcpServer) bool {
		return endpointKey(isRemoteTransport(m.GetTransport()), m.GetCommand()) == k
	})
}

// safeServerName makes a name the server form accepts out of a registry name.
func safeServerName(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	name := strings.TrimLeft(b.String(), "-_.")
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

func remoteTransport(t string) mcpv1.Transport {
	if strings.EqualFold(t, "sse") {
		return mcpv1.Transport_TRANSPORT_SSE
	}
	return mcpv1.Transport_TRANSPORT_STREAMABLE_HTTP
}

func catalogConnectorItem(c CatalogConnector, in Inputs) *agentv1.DiscoverItem {
	it := &agentv1.DiscoverItem{Id: "catalog:" + c.ID, Kind: agentv1.ExtensionKind_EXTENSION_KIND_CONNECTOR, Name: c.Name, Description: c.Description, Publisher: c.Publisher, SourceLabel: LabelCatalog, Homepage: c.Homepage}
	name := safeServerName(strings.ToLower(c.ID))
	if c.URL != "" {
		it.Options = []*agentv1.ConnectorOption{{Label: "Remote", Remote: true, SuggestedSource: in.RemoteDefault,
			Server: &mcpv1.Server{Name: name, Enabled: true, Transport: remoteTransport(c.Transport), Remote: &mcpv1.RemoteConfig{Url: c.URL}}}}
		it.Installed = connectorInstalled(in, true, c.URL)
	} else {
		it.Options = []*agentv1.ConnectorOption{{Label: "Local command", SuggestedSource: agentv1.McpServerSource_MCP_SERVER_SOURCE_DEVICE,
			Server: &mcpv1.Server{Name: name, Enabled: true, Transport: mcpv1.Transport_TRANSPORT_STDIO, Stdio: &mcpv1.StdioConfig{Command: c.Command, Args: c.Args}}}}
		it.Installed = connectorInstalled(in, false, strings.TrimSpace(c.Command+" "+strings.Join(c.Args, " ")))
	}
	if in.ServerAllowed != nil && !in.ServerAllowed(name) {
		it.Refusal = "Your organization's policy doesn't allow this server."
	}
	return it
}

// registryPublisher is the namespace the registry's name is under, written the way people read it. It is all the registry says about
// who publishes a server.
func registryPublisher(name string) string {
	ns, _, ok := strings.Cut(name, "/")
	if !ok {
		return ""
	}
	if rest, ok := strings.CutPrefix(ns, "io.github."); ok {
		return "github.com/" + rest
	}
	parts := strings.Split(ns, ".")
	slices.Reverse(parts)
	return strings.Join(parts, ".")
}

func registryOptions(s RegistryServer, in Inputs) []*agentv1.ConnectorOption {
	var out []*agentv1.ConnectorOption
	name := safeServerName(s.Name)
	for _, r := range s.Remotes {
		// A URL with a blank to fill in, or not over TLS, isn't offered.
		if !strings.HasPrefix(r.URL, "https://") || strings.ContainsAny(r.URL, "{}") {
			continue
		}
		cfg := &mcpv1.RemoteConfig{Url: r.URL}
		opt := &agentv1.ConnectorOption{Label: "Remote", Remote: true, SuggestedSource: in.RemoteDefault, Server: &mcpv1.Server{Name: name, Enabled: true, Transport: remoteTransport(r.Type), Remote: cfg}}
		for _, h := range r.Headers {
			if !h.Required {
				continue
			}
			if cfg.Headers == nil {
				cfg.Headers = map[string]string{}
			}
			cfg.Headers[h.Name] = "${secret:" + secretName(h.Name) + "}"
			opt.Required = append(opt.Required, &agentv1.ConnectorRequiredValue{Name: secretName(h.Name), Description: h.Description, Secret: true})
		}
		out = append(out, opt)
		break
	}
	for _, p := range s.Packages {
		if opt := packageOption(p, name); opt != nil {
			out = append(out, opt)
			break
		}
	}
	return out
}

// packageOption is the command line of a package. Its version is pinned to the one the registry lists.
func packageOption(p RegistryPackage, name string) *agentv1.ConnectorOption {
	if p.Transport.Type != "" && p.Transport.Type != "stdio" {
		return nil
	}
	required := func(a RegistryArg) bool { return a.Required }
	if slices.ContainsFunc(p.RuntimeArgs, required) || slices.ContainsFunc(p.PackageArgs, required) {
		return nil
	}
	var cfg *mcpv1.StdioConfig
	label := ""
	switch p.RegistryType {
	case "npm":
		spec := p.Identifier
		if p.Version != "" {
			spec += "@" + p.Version
		}
		cfg, label = &mcpv1.StdioConfig{Command: "npx", Args: []string{"-y", spec}}, "npm package"
	case "pypi":
		spec := p.Identifier
		if p.Version != "" {
			spec += "==" + p.Version
		}
		cfg, label = &mcpv1.StdioConfig{Command: "uvx", Args: []string{spec}}, "PyPI package"
	case "oci":
		cfg, label = &mcpv1.StdioConfig{Command: "docker", Args: []string{"run", "-i", "--rm", p.Identifier}}, "Container image"
	default:
		return nil
	}
	opt := &agentv1.ConnectorOption{Label: label, SuggestedSource: agentv1.McpServerSource_MCP_SERVER_SOURCE_DEVICE, Server: &mcpv1.Server{Name: name, Enabled: true, Transport: mcpv1.Transport_TRANSPORT_STDIO, Stdio: cfg}}
	for _, e := range p.Env {
		if !e.Required {
			continue
		}
		if cfg.Env == nil {
			cfg.Env = map[string]string{}
		}
		if e.Secret {
			cfg.Env[e.Name] = "${secret:" + e.Name + "}"
		} else {
			cfg.Env[e.Name] = ""
		}
		opt.Required = append(opt.Required, &agentv1.ConnectorRequiredValue{Name: e.Name, Description: e.Description, Secret: e.Secret})
	}
	return opt
}

// secretName turns a header's name into a name a secret reference can hold.
func secretName(header string) string {
	return strings.ToUpper(strings.NewReplacer("-", "_", " ", "_").Replace(header))
}

func registryItem(s RegistryServer, in Inputs) *agentv1.DiscoverItem {
	name := s.Title
	if name == "" {
		name = s.Name
	}
	home := s.Website
	if home == "" {
		home = s.Repository.URL
	}
	it := &agentv1.DiscoverItem{Id: "registry:" + s.Name, Kind: agentv1.ExtensionKind_EXTENSION_KIND_CONNECTOR, Name: name, Description: s.Description, Publisher: registryPublisher(s.Name), SourceLabel: LabelRegistry,
		Version: s.Version, Homepage: home, UpdatedAt: s.UpdatedAt, Options: registryOptions(s, in)}
	for _, o := range it.GetOptions() {
		endpoint := o.GetServer().GetRemote().GetUrl()
		if !o.GetRemote() {
			cfg := o.GetServer().GetStdio()
			endpoint = strings.TrimSpace(cfg.GetCommand() + " " + strings.Join(cfg.GetArgs(), " "))
		}
		if connectorInstalled(in, o.GetRemote(), endpoint) {
			it.Installed = true
		}
	}
	if len(it.GetOptions()) == 0 {
		it.Problem = "The registry doesn't say how to add this one without details only you can fill in. Add it by hand."
	}
	if in.ServerAllowed != nil && !in.ServerAllowed(safeServerName(s.Name)) {
		it.Refusal = "Your organization's policy doesn't allow this server."
	}
	return it
}

func matches(it *agentv1.DiscoverItem, q string) bool {
	if q == "" {
		return true
	}
	hay := strings.ToLower(strings.Join(append([]string{it.GetName(), it.GetDescription(), it.GetPublisher(), it.GetCategory(), it.GetSourceLabel()}, it.GetTags()...), " "))
	for _, w := range strings.Fields(strings.ToLower(q)) {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

func optionKind(it *agentv1.DiscoverItem, want string) bool {
	return slices.ContainsFunc(it.GetOptions(), func(o *agentv1.ConnectorOption) bool { return o.GetRemote() == (want == "remote") })
}

func keep(it *agentv1.DiscoverItem, req *agentv1.DiscoverExtensionsRequest) bool {
	// What the registry or the skill search matched for the query may match on what isn't shown; it stays.
	if it.GetSourceLabel() != LabelRegistry && it.GetSourceLabel() != LabelExternal && !matches(it, req.GetQuery()) {
		return false
	}
	if c := req.GetCategory(); c != "" && !strings.EqualFold(it.GetCategory(), c) {
		return false
	}
	if s := req.GetSource(); s != "" && it.GetSourceLabel() != s {
		return false
	}
	if t := req.GetConnectorType(); t != "" && !optionKind(it, t) {
		return false
	}
	return !req.GetHideInstalled() || !it.GetInstalled()
}

func sortItems(items []*agentv1.DiscoverItem, how string) {
	switch how {
	case "name":
		slices.SortStableFunc(items, func(a, b *agentv1.DiscoverItem) int {
			return strings.Compare(strings.ToLower(a.GetName()), strings.ToLower(b.GetName()))
		})
	case "updated":
		slices.SortStableFunc(items, func(a, b *agentv1.DiscoverItem) int { return cmp.Compare(b.GetUpdatedAt(), a.GetUpdatedAt()) })
	}
}

func sourcesOf(sections []*agentv1.DiscoverSection) []string {
	seen := []string{}
	for _, s := range sections {
		for _, it := range s.GetItems() {
			if !slices.Contains(seen, it.GetSourceLabel()) {
				seen = append(seen, it.GetSourceLabel())
			}
		}
	}
	return seen
}

// Discover builds the answer for one kind. The overview has a featured item and short sections; with a query or a filter there is one
// flat section of results; a section asked for comes whole. Nothing here is made up: an empty section isn't sent, a category exists
// only if an entry names it, and the featured item comes from the catalog, which nobody paid or ranked for.
func Discover(ctx context.Context, req *agentv1.DiscoverExtensionsRequest, in Inputs) *agentv1.DiscoverExtensionsResponse {
	res := &agentv1.DiscoverExtensionsResponse{}
	var sections []*agentv1.DiscoverSection
	if req.GetKind() == agentv1.ExtensionKind_EXTENSION_KIND_CONNECTOR {
		sections = connectorSections(ctx, req, in, res)
	} else {
		sections = pluginSections(ctx, req, in, res)
	}
	res.Sources = sourcesOf(sections)
	searching := req.GetQuery() != "" || req.GetCategory() != "" || req.GetSource() != "" || req.GetConnectorType() != "" || req.GetHideInstalled()

	kept := make([]*agentv1.DiscoverSection, 0, len(sections))
	for _, s := range sections {
		items := make([]*agentv1.DiscoverItem, 0, len(s.GetItems()))
		for _, it := range s.GetItems() {
			if keep(it, req) {
				items = append(items, it)
			}
		}
		s.Items, s.Total = items, int32(len(items))
		if len(items) > 0 {
			kept = append(kept, s)
		}
	}
	switch {
	case req.GetSection() != "":
		res.Featured = nil
		for _, s := range kept {
			if s.GetKey() == req.GetSection() {
				sortItems(s.Items, req.GetSort())
				if len(s.Items) > allItems {
					s.Items = s.Items[:allItems]
				}
				res.Sections = []*agentv1.DiscoverSection{s}
			}
		}
	case searching:
		res.Featured = nil
		all := &agentv1.DiscoverSection{Key: sectionResults, Title: "Results"}
		seen := map[string]bool{}
		for _, s := range kept {
			for _, it := range s.GetItems() {
				if !seen[it.GetId()] {
					seen[it.GetId()] = true
					all.Items = append(all.Items, it)
				}
			}
			if s.GetNextCursor() != "" {
				all.NextCursor = s.GetNextCursor()
			}
		}
		sortItems(all.Items, req.GetSort())
		all.Total = int32(len(all.Items))
		if len(all.Items) > allItems {
			all.Items = all.Items[:allItems]
		}
		if all.Total > 0 {
			res.Sections = []*agentv1.DiscoverSection{all}
		}
	default:
		for _, s := range kept {
			sortItems(s.Items, req.GetSort())
			if len(s.Items) > overviewItems {
				s.Items = s.Items[:overviewItems]
			}
		}
		res.Sections = kept
	}
	return res
}

func pluginSections(ctx context.Context, req *agentv1.DiscoverExtensionsRequest, in Inputs, res *agentv1.DiscoverExtensionsResponse) []*agentv1.DiscoverSection {
	kind := "plugins"
	if req.GetKind() == agentv1.ExtensionKind_EXTENSION_KIND_SKILL {
		kind = "skills"
	}
	var sections []*agentv1.DiscoverSection
	counts := map[string]int32{}
	names := map[string]string{}
	markets := slices.Clone(in.Markets)
	// What the organization distributes comes first.
	slices.SortStableFunc(markets, func(a, b plugins.MarketListings) int {
		rank := func(m plugins.MarketListings) int {
			switch {
			case m.Managed:
				return 0
			case m.Builtin:
				return 1
			}
			return 2
		}
		return rank(a) - rank(b)
	})
	for _, mk := range markets {
		s := &agentv1.DiscoverSection{Key: marketPrefix + mk.Name, Title: mk.Name}
		if mk.Builtin {
			s.Title = "The xlsft`s harness catalog"
		}
		for _, l := range mk.Listings {
			items := []*agentv1.DiscoverItem{listingItem(l, mk)}
			if kind == "skills" {
				items = skillItems(l, mk)
			}
			s.Items = append(s.Items, items...)
			if mk.Builtin && res.Featured == nil {
				if i := slices.IndexFunc(items, func(it *agentv1.DiscoverItem) bool {
					return !it.GetInstalled() && it.GetRefusal() == "" && it.GetProblem() == ""
				}); i >= 0 {
					res.Featured = items[i]
				}
			}
			if c := l.Meta.Category; c != "" && len(items) > 0 {
				key := strings.ToLower(c)
				counts[key] += int32(len(items))
				if names[key] == "" {
					names[key] = c
				}
			}
		}
		sections = append(sections, s)
	}
	if kind == "skills" {
		if ext := externalSection(ctx, req, in, res); ext != nil {
			sections = append(sections, ext)
		}
	}
	for k, n := range counts {
		res.Categories = append(res.Categories, &agentv1.DiscoverCategory{Name: names[k], Count: n})
	}
	slices.SortFunc(res.Categories, func(a, b *agentv1.DiscoverCategory) int {
		if c := cmp.Compare(b.GetCount(), a.GetCount()); c != 0 {
			return c
		}
		return strings.Compare(a.GetName(), b.GetName())
	})
	return sections
}

func connectorSections(ctx context.Context, req *agentv1.DiscoverExtensionsRequest, in Inputs, res *agentv1.DiscoverExtensionsResponse) []*agentv1.DiscoverSection {
	var sections []*agentv1.DiscoverSection
	seen := map[string]bool{}
	if in.Catalog != nil {
		s := &agentv1.DiscoverSection{Key: sectionCatalog, Title: "Well-known connectors"}
		for _, c := range in.Catalog.Connectors {
			it := catalogConnectorItem(c, in)
			if c.URL != "" {
				seen[endpointKey(true, c.URL)] = true
			}
			if c.Featured && !it.GetInstalled() && it.GetRefusal() == "" && res.Featured == nil {
				res.Featured = it
			}
			if req.GetCursor() == "" {
				s.Items = append(s.Items, it)
			}
		}
		sections = append(sections, s)
	}
	if in.Registry == nil {
		res.RegistryOff = true
		return sections
	}
	page := in.Registry.Page(ctx, req.GetQuery(), req.GetCursor(), req.GetRefresh())
	res.RegistryOff, res.RegistryProblem = page.Off, page.Problem
	if !page.FetchedAt.IsZero() {
		res.RegistryFetchedAt = page.FetchedAt.UTC().Format(time.RFC3339)
	}
	s := &agentv1.DiscoverSection{Key: sectionRegistry, Title: "From the MCP registry", NextCursor: page.NextCursor}
	for _, srv := range page.Servers {
		dup := slices.ContainsFunc(srv.Remotes, func(r RegistryRemote) bool { return seen[endpointKey(true, r.URL)] })
		if !dup {
			s.Items = append(s.Items, registryItem(srv, in))
		}
	}
	return append(sections, s)
}

// externalSection is what external search has: for a query, what it finds; before one, its curated set. Hidden when it isn't available.
// When it fails, the rest of Discover still stands and the answer says so.
func externalSection(ctx context.Context, req *agentv1.DiscoverExtensionsRequest, in Inputs, res *agentv1.DiscoverExtensionsResponse) *agentv1.DiscoverSection {
	if in.ExternalBlocked {
		res.ExternalBlockedByPolicy = true
		return nil
	}
	if in.Search == nil {
		return nil
	}
	ok, reason, err := in.Search.Available(ctx)
	if err != nil {
		res.ExternalProblem = "Skill search can't be reached right now."
		return nil
	}
	if !ok {
		res.ExternalBlockedByPolicy = reason == "policy"
		return nil
	}
	var found []*skillsv1.ExternalSkill
	if q := strings.TrimSpace(req.GetQuery()); len(q) >= 2 {
		found, err = in.Search.Search(ctx, q, 50)
	} else if req.GetQuery() == "" {
		found, err = in.Search.Curated(ctx, 50)
	}
	if err != nil {
		res.ExternalProblem = "Skill search can't be reached right now."
		return nil
	}
	s := &agentv1.DiscoverSection{Key: sectionExternal, Title: "From skills.sh"}
	for _, f := range found {
		owner, _, _ := strings.Cut(f.GetSource(), "/")
		s.Items = append(s.Items, &agentv1.DiscoverItem{Id: "external:" + f.GetId(), Kind: agentv1.ExtensionKind_EXTENSION_KIND_SKILL, Name: f.GetName(), Publisher: owner, SourceLabel: LabelExternal,
			Homepage: f.GetUrl(), Installs: f.GetInstalls(), Audits: f.GetAudits(), ExternalId: f.GetId(), Installed: in.ExternalInstalled[f.GetId()]})
	}
	return s
}
