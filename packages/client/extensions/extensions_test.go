package extensions_test

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/extensions"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	skillsv1 "github.com/xlsft/harness/packages/client/gen/harness/skills/v1"
	"github.com/xlsft/harness/packages/client/plugins"
)

const registryBody = `{"servers":[
 {"server":{"name":"io.github.acme/tracker","title":"Tracker","description":"Track things","version":"1.2.0","remotes":[{"type":"streamable-http","url":"https://tracker.example/mcp","headers":[{"name":"X-Api-Key","isRequired":true,"isSecret":true}]}]},
  "_meta":{"io.modelcontextprotocol.registry/official":{"status":"active","updatedAt":"2026-10-01T00:00:00Z"}}},
 {"server":{"name":"com.example/local-tool","description":"Runs here","version":"3.0.0","packages":[{"registryType":"npm","identifier":"@example/tool","version":"3.0.0","transport":{"type":"stdio"},"environmentVariables":[{"name":"TOOL_TOKEN","isRequired":true,"isSecret":true}]}]},
  "_meta":{"io.modelcontextprotocol.registry/official":{"status":"active","updatedAt":"2026-11-01T00:00:00Z"}}},
 {"server":{"name":"com.example/gone","description":"Old","version":"1.0.0","remotes":[{"type":"sse","url":"https://gone.example/sse"}]},
  "_meta":{"io.modelcontextprotocol.registry/official":{"status":"deleted"}}},
 {"server":{"name":"com.example/blank","description":"Needs a token in the URL","version":"1.0.0","remotes":[{"type":"streamable-http","url":"https://blank.example/{token}"}]},"_meta":{}},
 {"server":{"name":"app.linear/linear","title":"Linear twin","description":"The catalog has this one","version":"1.0.0","remotes":[{"type":"streamable-http","url":"https://mcp.linear.app/mcp"}]},"_meta":{}}
],"metadata":{"nextCursor":"more"}}`

// fakeRegistry counts requests and can go down.
type fakeRegistry struct {
	*httptest.Server
	hits  atomic.Int32
	down  atomic.Bool
	last  atomic.Value
	empty atomic.Bool
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	t.Helper()
	f := &fakeRegistry{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.last.Store(r.URL.RawQuery)
		if f.down.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if f.empty.Load() {
			_, _ = w.Write([]byte(`{"servers":[],"metadata":{}}`))
			return
		}
		_, _ = w.Write([]byte(registryBody))
	}))
	t.Cleanup(f.Close)
	return f
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newRegistry(t *testing.T, f *fakeRegistry) (*extensions.Registry, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
	return &extensions.Registry{BaseURL: f.URL, HTTP: f.Client(), CachePath: filepath.Join(t.TempDir(), "x", "registry.json"), Now: c.now}, c
}

func TestRegistryCachesAndServesOffline(t *testing.T) {
	f := newFakeRegistry(t)
	r, c := newRegistry(t, f)
	ctx := context.Background()

	p := r.Page(ctx, "", "", false)
	if p.Problem != "" || len(p.Servers) != 4 {
		t.Fatalf("first page: problem %q, %d servers", p.Problem, len(p.Servers))
	}
	if slices.ContainsFunc(p.Servers, func(s extensions.RegistryServer) bool { return s.Name == "com.example/gone" }) {
		t.Fatal("a deleted server is listed")
	}
	if p.NextCursor != "more" {
		t.Fatalf("cursor %q", p.NextCursor)
	}

	// Fresh: the file answers.
	r.Page(ctx, "", "", false)
	if n := f.hits.Load(); n != 1 {
		t.Fatalf("%d requests while the cache is fresh", n)
	}

	// Stale and the registry is down: the saved list, saying so.
	f.down.Store(true)
	c.t = c.t.Add(7 * time.Hour)
	p = r.Page(ctx, "", "", false)
	if p.Problem == "" || len(p.Servers) != 4 {
		t.Fatalf("offline: problem %q, %d servers", p.Problem, len(p.Servers))
	}

	// Backing off: no request, the same answer, and search filters the saved list.
	before := f.hits.Load()
	p = r.Page(ctx, "tracker", "", false)
	if f.hits.Load() != before {
		t.Fatal("asked again while backing off")
	}
	if p.Problem == "" || len(p.Servers) != 1 || p.Servers[0].Title != "Tracker" {
		t.Fatalf("offline search: %+v", p)
	}

	// Back up after the wait.
	f.down.Store(false)
	c.t = c.t.Add(2 * time.Minute)
	if p = r.Page(ctx, "", "", false); p.Problem != "" {
		t.Fatalf("still a problem: %q", p.Problem)
	}
}

func TestRegistryWithNoCacheAndNoNetwork(t *testing.T) {
	f := newFakeRegistry(t)
	f.down.Store(true)
	r, _ := newRegistry(t, f)
	p := r.Page(context.Background(), "", "", false)
	if p.Problem == "" || len(p.Servers) != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestRegistrySearchGoesToTheRegistryAndIsRemembered(t *testing.T) {
	f := newFakeRegistry(t)
	r, _ := newRegistry(t, f)
	ctx := context.Background()
	r.Page(ctx, "track", "", false)
	if q, _ := f.last.Load().(string); !strings.Contains(q, "search=track") || !strings.Contains(q, "version=latest") {
		t.Fatalf("query %q", q)
	}
	r.Page(ctx, "track", "", false)
	if n := f.hits.Load(); n != 1 {
		t.Fatalf("%d requests for one search", n)
	}
	if _, err := os.Stat(r.CachePath); err == nil {
		t.Fatal("a search replaced the saved list")
	}
}

func TestRegistryOffAsksNobody(t *testing.T) {
	f := newFakeRegistry(t)
	r, _ := newRegistry(t, f)
	r.BaseURL = "off"
	if p := r.Page(context.Background(), "", "", true); !p.Off || f.hits.Load() != 0 {
		t.Fatalf("%+v, %d requests", p, f.hits.Load())
	}
}

func TestDefaultCatalogIsSound(t *testing.T) {
	c, err := extensions.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Connectors) == 0 {
		t.Fatal("the catalog is empty")
	}
	for _, s := range c.Connectors {
		if (s.URL == "") == (s.Command == "") {
			t.Errorf("%s needs a URL or a command", s.ID)
		}
		if s.URL != "" && !strings.HasPrefix(s.URL, "https://") {
			t.Errorf("%s isn't over TLS", s.ID)
		}
	}
}

func connectorInputs(t *testing.T, f *fakeRegistry) extensions.Inputs {
	t.Helper()
	r, _ := newRegistry(t, f)
	c, err := extensions.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return extensions.Inputs{Catalog: c, Registry: r, RemoteDefault: agentv1.McpServerSource_MCP_SERVER_SOURCE_ACCOUNT}
}

func findItem(res *agentv1.DiscoverExtensionsResponse, id string) *agentv1.DiscoverItem {
	for _, s := range res.GetSections() {
		for _, it := range s.GetItems() {
			if it.GetId() == id {
				return it
			}
		}
	}
	return nil
}

func TestDiscoverConnectorsFromRegistryAndCatalog(t *testing.T) {
	f := newFakeRegistry(t)
	in := connectorInputs(t, f)
	kind := agentv1.ExtensionKind_EXTENSION_KIND_CONNECTOR
	res := extensions.Discover(context.Background(), &agentv1.DiscoverExtensionsRequest{Kind: kind, Section: "registry"}, in)

	if res.GetRegistryFetchedAt() == "" {
		t.Fatal("no time of fetching")
	}
	remote := findItem(res, "registry:io.github.acme/tracker")
	if remote == nil {
		t.Fatalf("no tracker in %+v", res.GetSections())
	}
	if remote.GetPublisher() != "github.com/acme" || remote.GetSourceLabel() != extensions.LabelRegistry {
		t.Fatalf("%+v", remote)
	}
	o := remote.GetOptions()[0]
	if !o.GetRemote() || o.GetSuggestedSource() != agentv1.McpServerSource_MCP_SERVER_SOURCE_ACCOUNT || o.GetServer().GetRemote().GetHeaders()["X-Api-Key"] != "${secret:X_API_KEY}" {
		t.Fatalf("remote option %+v", o)
	}
	local := findItem(res, "registry:com.example/local-tool")
	lo := local.GetOptions()[0]
	if lo.GetRemote() || lo.GetServer().GetStdio().GetCommand() != "npx" || !slices.Contains(lo.GetServer().GetStdio().GetArgs(), "@example/tool@3.0.0") || lo.GetSuggestedSource() != agentv1.McpServerSource_MCP_SERVER_SOURCE_DEVICE {
		t.Fatalf("local option %+v", lo)
	}
	if lo.GetRequired()[0].GetName() != "TOOL_TOKEN" || !lo.GetRequired()[0].GetSecret() {
		t.Fatalf("required %+v", lo.GetRequired())
	}
	if blank := findItem(res, "registry:com.example/blank"); len(blank.GetOptions()) != 0 || blank.GetProblem() == "" {
		t.Fatalf("a URL with a blank is offered: %+v", blank)
	}
	if findItem(res, "registry:app.linear/linear") != nil {
		t.Fatal("what the catalog has is listed twice")
	}

	over := extensions.Discover(context.Background(), &agentv1.DiscoverExtensionsRequest{Kind: kind}, in)
	if over.GetFeatured().GetId() != "catalog:linear" {
		t.Fatalf("featured %+v", over.GetFeatured())
	}
	if len(over.GetCategories()) != 0 {
		t.Fatalf("categories nobody named: %+v", over.GetCategories())
	}
	if found := extensions.Discover(context.Background(), &agentv1.DiscoverExtensionsRequest{Kind: kind, ConnectorType: "local"}, in); found.GetFeatured() != nil || findItem(found, "catalog:linear") != nil || findItem(found, "catalog:memory") == nil {
		t.Fatalf("the local filter: %+v", found.GetSections())
	}
}

func TestDiscoverWorksWithNoNetwork(t *testing.T) {
	f := newFakeRegistry(t)
	f.down.Store(true)
	res := extensions.Discover(context.Background(), &agentv1.DiscoverExtensionsRequest{Kind: agentv1.ExtensionKind_EXTENSION_KIND_CONNECTOR}, connectorInputs(t, f))
	if res.GetRegistryProblem() == "" {
		t.Fatal("nothing says the registry is away")
	}
	if findItem(res, "catalog:github") == nil {
		t.Fatal("the catalog needs no network")
	}
	if slices.ContainsFunc(res.GetSections(), func(s *agentv1.DiscoverSection) bool { return s.GetKey() == "registry" }) {
		t.Fatal("an empty section was sent")
	}
}

func TestDiscoverMarksWhatIsThere(t *testing.T) {
	f := newFakeRegistry(t)
	in := connectorInputs(t, f)
	in.Configured = []*agentv1.McpServerEntry{{Server: &mcpv1.Server{Name: "mine", Transport: mcpv1.Transport_TRANSPORT_STREAMABLE_HTTP}, LaunchSummary: "https://mcp.notion.com/mcp/"}}
	in.PluginServers = []*agentv1.PluginMcpServer{{Name: "plugin:kit:sentry", Transport: "http", Command: "https://MCP.sentry.dev/mcp"}}
	in.ServerAllowed = func(name string) bool { return name != "stripe" }
	res := extensions.Discover(context.Background(), &agentv1.DiscoverExtensionsRequest{Kind: agentv1.ExtensionKind_EXTENSION_KIND_CONNECTOR, Section: "catalog"}, in)
	if !findItem(res, "catalog:notion").GetInstalled() || !findItem(res, "catalog:sentry").GetInstalled() || findItem(res, "catalog:github").GetInstalled() {
		t.Fatal("installed marks are wrong")
	}
	if findItem(res, "catalog:stripe").GetRefusal() == "" {
		t.Fatal("policy isn't told")
	}
}

// localMarket registers a marketplace in a folder: one plugin here with a skill and a category, one in a repository of its own.
func localMarket(t *testing.T) (*plugins.Manager, plugins.Source) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		".claude-plugin/marketplace.json": `{"name":"acme","owner":{"name":"Acme Inc"},"plugins":[
			{"name":"kit","source":"./kit","description":"A kit","version":"1.0.0","category":"Testing","tags":"odd","author":{"name":"Kit Maker"}},
			{"name":"far","source":{"source":"github","repo":"acme/far"},"description":"Elsewhere","category":["not","a","string"]}]}`,
		"kit/skills/fmt/SKILL.md": "---\ndescription: Format things\n---\nDo it.",
	}
	for rel, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src, err := plugins.ParseSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := &plugins.Manager{Root: filepath.Join(t.TempDir(), "plugins"), PolicyOf: func() plugins.Policy { return plugins.Policy{} }}
	if _, err := m.AddMarketplace(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	return m, src
}

func TestDiscoverReadsMarketplacesAndInventsNothing(t *testing.T) {
	m, _ := localMarket(t)
	markets, err := m.Browse()
	if err != nil {
		t.Fatal(err)
	}
	c, _ := extensions.DefaultCatalog()
	in := extensions.Inputs{Catalog: c, Markets: markets}
	ctx := context.Background()

	res := extensions.Discover(ctx, &agentv1.DiscoverExtensionsRequest{Kind: agentv1.ExtensionKind_EXTENSION_KIND_PLUGIN}, in)
	kit, far := findItem(res, "kit@acme"), findItem(res, "far@acme")
	if kit == nil || far == nil {
		t.Fatalf("%+v", res.GetSections())
	}
	if kit.GetPublisher() != "Kit Maker" || far.GetPublisher() != "Acme Inc" || kit.GetCategory() != "Testing" || far.GetCategory() != "" {
		t.Fatalf("kit %+v far %+v", kit, far)
	}
	if len(kit.GetContents().GetSkills()) != 1 || far.GetContents() != nil {
		t.Fatalf("contents: kit %+v far %+v", kit.GetContents(), far.GetContents())
	}
	if len(res.GetCategories()) != 1 || res.GetCategories()[0].GetName() != "Testing" || res.GetCategories()[0].GetCount() != 1 {
		t.Fatalf("categories %+v", res.GetCategories())
	}
	if !slices.Contains(res.GetSources(), "acme") {
		t.Fatalf("sources %v", res.GetSources())
	}

	skills := extensions.Discover(ctx, &agentv1.DiscoverExtensionsRequest{Kind: agentv1.ExtensionKind_EXTENSION_KIND_SKILL}, in)
	sk := findItem(skills, "skill:kit:fmt@acme")
	if sk == nil || sk.GetPluginId() != "kit@acme" || sk.GetDescription() != "Format things" {
		t.Fatalf("skill %+v", sk)
	}
	if findItem(skills, "far@acme") != nil {
		t.Fatal("a plugin without known skills is listed under skills")
	}

	found := extensions.Discover(ctx, &agentv1.DiscoverExtensionsRequest{Kind: agentv1.ExtensionKind_EXTENSION_KIND_PLUGIN, Query: "elsewhere"}, in)
	if len(found.GetSections()) != 1 || found.GetSections()[0].GetKey() != "results" || found.GetSections()[0].GetTotal() != 1 || found.GetFeatured() != nil {
		t.Fatalf("search %+v", found)
	}
	byCategory := extensions.Discover(ctx, &agentv1.DiscoverExtensionsRequest{Kind: agentv1.ExtensionKind_EXTENSION_KIND_PLUGIN, Category: "testing"}, in)
	if findItem(byCategory, "kit@acme") == nil || findItem(byCategory, "far@acme") != nil {
		t.Fatalf("category filter %+v", byCategory.GetSections())
	}
}

func TestFoldConnectors(t *testing.T) {
	shared := "https://mcp.asana.example/sse"
	entries := []*agentv1.McpServerEntry{
		{Server: &mcpv1.Server{Id: "mcl_1", Name: "zed", Transport: mcpv1.Transport_TRANSPORT_STDIO}, Source: agentv1.McpServerSource_MCP_SERVER_SOURCE_DEVICE, State: agentv1.McpServerState_MCP_SERVER_STATE_READY, LaunchSummary: "npx zed"},
		{Server: &mcpv1.Server{Id: "mcp_2", Name: "tracker", Transport: mcpv1.Transport_TRANSPORT_STREAMABLE_HTTP, Auth: &mcpv1.AuthStatus{State: mcpv1.AuthState_AUTH_STATE_CONNECTED}}, Source: agentv1.McpServerSource_MCP_SERVER_SOURCE_ACCOUNT, State: agentv1.McpServerState_MCP_SERVER_STATE_READY, LaunchSummary: "https://tracker.example/mcp"},
		{Server: &mcpv1.Server{Id: "mcp_3", Name: "docs", Transport: mcpv1.Transport_TRANSPORT_SSE, Auth: &mcpv1.AuthStatus{State: mcpv1.AuthState_AUTH_STATE_NEEDS_AUTH}}, Source: agentv1.McpServerSource_MCP_SERVER_SOURCE_ORG, State: agentv1.McpServerState_MCP_SERVER_STATE_NEEDS_AUTH, LaunchSummary: "https://docs.example/sse"},
	}
	info := func(id string, st plugins.Status, servers ...plugins.MCPInfo) plugins.Info {
		name, _, _ := strings.Cut(id, "@")
		return plugins.Info{ID: id, Name: name, Status: st, Reason: string(st), Contents: plugins.Contents{MCPServers: servers}}
	}
	installed := []plugins.Info{
		info("eng@a", plugins.StatusNeedsApproval, plugins.MCPInfo{Name: "plugin:eng:asana", Transport: "sse", Command: shared}),
		info("pm@a", plugins.StatusActive, plugins.MCPInfo{Name: "plugin:pm:asana", Transport: "sse", Command: shared + "/"}),
		info("design@a", plugins.StatusDisabled, plugins.MCPInfo{Name: "plugin:design:asana", Transport: "sse", Command: shared}),
		info("trk@a", plugins.StatusActive, plugins.MCPInfo{Name: "plugin:trk:tracker", Transport: "http", Command: "https://TRACKER.example/mcp"}),
		info("one@a", plugins.StatusActive, plugins.MCPInfo{Name: "plugin:one:run", Transport: "stdio", Command: "node ${CLAUDE_PLUGIN_ROOT}/s.js"}),
		info("two@a", plugins.StatusActive, plugins.MCPInfo{Name: "plugin:two:run", Transport: "stdio", Command: "node ${CLAUDE_PLUGIN_ROOT}/s.js"}),
		info("gone@a", plugins.StatusNotInstalled, plugins.MCPInfo{Name: "plugin:gone:x", Transport: "stdio", Command: "x"}),
		info("nope@a", plugins.StatusActive, plugins.MCPInfo{Name: "plugin:nope:blocked", Transport: "http", Command: "https://blocked.example"}),
	}
	pol := plugins.Policy{RestrictMCP: true, AllowedMCP: []string{"asana", "run", "tracker"}}
	rows := extensions.FoldConnectors(entries, installed, pol)
	names := []string{}
	for _, r := range rows {
		names = append(names, r.GetName())
	}
	if want := []string{"docs", "tracker", "zed", "asana", "blocked", "run", "run"}; !slices.Equal(names, want) {
		t.Fatalf("rows %v, want %v", names, want)
	}
	by := func(n string) *agentv1.ExtensionConnector {
		return rows[slices.Index(names, n)]
	}
	asana := by("asana")
	if len(asana.GetProviders()) != 3 || asana.GetEntry() != nil || asana.GetStatus() != agentv1.ConnectorStatus_CONNECTOR_STATUS_IN_SESSIONS || !asana.GetRemote() || !slices.Equal(asana.GetBadges(), []string{"Plugin"}) {
		t.Fatalf("asana %+v", asana)
	}
	tracker := by("tracker")
	if tracker.GetStatus() != agentv1.ConnectorStatus_CONNECTOR_STATUS_CONNECTED || len(tracker.GetProviders()) != 1 || !slices.Equal(tracker.GetBadges(), []string{"Account", "Plugin"}) {
		t.Fatalf("tracker %+v", tracker)
	}
	if d := by("docs"); d.GetStatus() != agentv1.ConnectorStatus_CONNECTOR_STATUS_NEEDS_SIGN_IN || !slices.Equal(d.GetBadges(), []string{"Admin"}) {
		t.Fatalf("docs %+v", d)
	}
	if z := by("zed"); z.GetStatus() != agentv1.ConnectorStatus_CONNECTOR_STATUS_IN_SESSIONS || z.GetRemote() || !slices.Equal(z.GetBadges(), []string{"Device"}) {
		t.Fatalf("zed %+v", z)
	}
	if b := by("blocked"); b.GetStatus() != agentv1.ConnectorStatus_CONNECTOR_STATUS_BLOCKED {
		t.Fatalf("blocked %+v", b)
	}
}

// fakeSearch is external skill search with canned answers.
type fakeSearch struct {
	available bool
	reason    string
	err       error
	asked     []string
}

func (f *fakeSearch) Available(context.Context) (bool, string, error) {
	return f.available, f.reason, nil
}

func (f *fakeSearch) Search(_ context.Context, q string, _ int) ([]*skillsv1.ExternalSkill, error) {
	f.asked = append(f.asked, "search:"+q)
	if f.err != nil {
		return nil, f.err
	}
	return []*skillsv1.ExternalSkill{{Id: "acme/skills/pdf", Name: "PDF", Slug: "pdf", Source: "acme/skills", Installs: 42, Audits: []*skillsv1.SkillAudit{{Provider: "Socket", Status: "warn"}}}}, nil
}

func (f *fakeSearch) Curated(context.Context, int) ([]*skillsv1.ExternalSkill, error) {
	f.asked = append(f.asked, "curated")
	return []*skillsv1.ExternalSkill{{Id: "acme/skills/big", Name: "Big", Slug: "big", Source: "acme/skills"}}, nil
}

func (f *fakeSearch) Snapshot(context.Context, string) (*skillsv1.GetSkillSnapshotResponse, error) {
	return nil, errors.New("not used here")
}

func TestDiscoverExternalSearch(t *testing.T) {
	kind := agentv1.ExtensionKind_EXTENSION_KIND_SKILL
	ctx := context.Background()
	f := &fakeSearch{available: true}
	in := extensions.Inputs{Search: f, ExternalInstalled: map[string]bool{"acme/skills/pdf": true}}

	over := extensions.Discover(ctx, &agentv1.DiscoverExtensionsRequest{Kind: kind}, in)
	if findItem(over, "external:acme/skills/big") == nil || !slices.Equal(f.asked, []string{"curated"}) {
		t.Fatalf("overview %+v, asked %v", over.GetSections(), f.asked)
	}
	res := extensions.Discover(ctx, &agentv1.DiscoverExtensionsRequest{Kind: kind, Query: "pdf"}, in)
	it := findItem(res, "external:acme/skills/pdf")
	if it == nil || it.GetInstalls() != 42 || it.GetAudits()[0].GetStatus() != "warn" || it.GetPublisher() != "acme" || it.GetSourceLabel() != extensions.LabelExternal || !it.GetInstalled() {
		t.Fatalf("item %+v", it)
	}

	// Hidden: no token on the server, or the policy.
	for _, c := range []extensions.Inputs{{Search: &fakeSearch{reason: "not_configured"}}, {}} {
		if r := extensions.Discover(ctx, &agentv1.DiscoverExtensionsRequest{Kind: kind}, c); len(r.GetSections()) != 0 || r.GetExternalProblem() != "" || r.GetExternalBlockedByPolicy() {
			t.Fatalf("not hidden: %+v", r)
		}
	}
	blocked := &fakeSearch{available: true}
	if r := extensions.Discover(ctx, &agentv1.DiscoverExtensionsRequest{Kind: kind}, extensions.Inputs{Search: blocked, ExternalBlocked: true}); !r.GetExternalBlockedByPolicy() || len(blocked.asked) != 0 {
		t.Fatalf("the policy didn't stop it: %+v, asked %v", r, blocked.asked)
	}

	// Down: the rest stands.
	down := &fakeSearch{available: true, err: errors.New("no")}
	if r := extensions.Discover(ctx, &agentv1.DiscoverExtensionsRequest{Kind: kind, Query: "pdf"}, extensions.Inputs{Search: down}); r.GetExternalProblem() == "" {
		t.Fatalf("no problem told: %+v", r)
	}
}

func TestBuiltinMarketplaceIsFeaturedAndStays(t *testing.T) {
	ctx := context.Background()
	m := &plugins.Manager{Root: filepath.Join(t.TempDir(), "plugins"), PolicyOf: func() plugins.Policy { return plugins.Policy{} }}
	if err := extensions.EnsureBuiltin(ctx, m); err != nil {
		t.Fatal(err)
	}
	markets, err := m.Browse()
	if err != nil {
		t.Fatal(err)
	}
	if len(markets) != 1 || !markets[0].Builtin || len(markets[0].Listings) == 0 {
		t.Fatalf("%+v", markets)
	}
	res := extensions.Discover(ctx, &agentv1.DiscoverExtensionsRequest{Kind: agentv1.ExtensionKind_EXTENSION_KIND_SKILL}, extensions.Inputs{Markets: markets})
	if res.GetFeatured() == nil || res.GetFeatured().GetKind() != agentv1.ExtensionKind_EXTENSION_KIND_SKILL {
		t.Fatalf("featured %+v", res.GetFeatured())
	}
	if err := m.RemoveMarketplace(markets[0].Name); err == nil {
		t.Fatal("the built-in marketplace was removed")
	}
	if _, err := m.Install(ctx, res.GetFeatured().GetPluginId()); err != nil {
		t.Fatalf("install: %v", err)
	}
	list, err := m.List(plugins.Place{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Status != plugins.StatusNeedsApproval {
		t.Fatalf("installing skipped the approval: %+v", list)
	}

	// A policy that refuses local sources leaves it out without a fuss.
	refusing := &plugins.Manager{Root: filepath.Join(t.TempDir(), "plugins"), PolicyOf: func() plugins.Policy { return plugins.Policy{Allowlists: [][]plugins.Rule{{}}} }}
	if err := extensions.EnsureBuiltin(ctx, refusing); err != nil {
		t.Fatal(err)
	}
	if got, _ := refusing.Browse(); len(got) != 0 {
		t.Fatalf("registered against policy: %+v", got)
	}
}

// The embedded copy must be the repository's catalog/, once there is one.
func TestBuiltinMatchesTheCatalogFolder(t *testing.T) {
	catalog := filepath.Join("..", "..", "..", "catalog")
	if _, err := os.Stat(catalog); err != nil {
		t.Skip("no catalog/ in this tree yet")
	}
	read := func(root string) map[string]string {
		out := map[string]string{}
		if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			out[rel] = string(data)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if !maps.Equal(read(catalog), read("builtin")) {
		t.Fatal("extensions/builtin is stale: run scripts/sync-catalog.sh")
	}
}
