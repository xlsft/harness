package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/extensions"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	skillsv1 "github.com/xlsft/harness/packages/client/gen/harness/skills/v1"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/skills"
)

// ExtensionsService, the Extensions screen. This file gathers what the stores know and package extensions joins it. What changes a
// plugin or an MCP server stays with PluginService and the MCP calls; here only skill files are written, and only by this machine.

const (
	skillBodyLimit  = 64 << 10
	skillFilesLimit = 200
)

var skillNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

var (
	extensionsRegistryMu sync.Mutex
	extensionsRegistries = map[string]*extensions.Registry{}
)

// extensionsRegistry is the public MCP registry through this device's cache. HARNESS_MCP_REGISTRY_URL points it elsewhere; "off" stops
// it. The cache is one per home, so a failure seen once isn't waited for again on the next keystroke.
func extensionsRegistry() *extensions.Registry {
	path := filepath.Join(config.Home(), "extensions", "mcp-registry.json")
	base := os.Getenv("HARNESS_MCP_REGISTRY_URL")
	key := path + "\x00" + base
	extensionsRegistryMu.Lock()
	defer extensionsRegistryMu.Unlock()
	r := extensionsRegistries[key]
	if r == nil {
		r = &extensions.Registry{BaseURL: base, CachePath: path}
		extensionsRegistries[key] = r
	}
	return r
}

func skillGroupOf(sk skills.Skill, pluginTitle func(id string) string) (key, title string) {
	switch sk.Source {
	case skills.SourceProject:
		return "project", "In this project"
	case skills.SourcePersonal:
		return "personal", "Personal"
	case skills.SourceImported:
		return "imported", "Imported"
	case skills.SourceOrganization:
		return "organization", "Organization"
	case skills.SourcePlugin:
		return "plugin:" + sk.Plugin, pluginTitle(sk.Plugin)
	}
	return sk.Source, sk.Source
}

// skillGroupRank is the order of groups: the folder, the user's, plugins, then what the server keeps.
func skillGroupRank(key string) int {
	switch {
	case key == "project":
		return 0
	case key == "personal":
		return 1
	case key == "external":
		return 2
	case strings.HasPrefix(key, "plugin:"):
		return 2
	case key == "imported":
		return 3
	case key == "organization":
		return 4
	}
	return 5
}

// ListExtensionSkills implements ExtensionsService.ListExtensionSkills.
func (s *Server) ListExtensionSkills(ctx context.Context, req *connect.Request[agentv1.ListExtensionSkillsRequest]) (*connect.Response[agentv1.ListExtensionSkillsResponse], error) {
	place := s.pluginPlace(req.Msg.GetCwd())
	m := s.folderSkills(ctx, place.Cwd)
	home := bootstrap.SkillHomeDir(s.Base)
	external, err := skills.ListExternal(home)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	shown := map[string]bool{}
	_, report := m.Listing(0, true)
	catalog := slashCatalog(m, report)
	cat := m.Catalog(false)

	titles := map[string]string{}
	if list, err := bootstrap.PluginStore().List(place); err == nil {
		for _, in := range list {
			titles[in.ID] = in.DisplayName
			if titles[in.ID] == "" {
				titles[in.ID] = in.Name
			}
		}
	}
	pluginTitle := func(id string) string {
		if t := titles[id]; t != "" {
			return t
		}
		return id
	}
	type where struct{ name, path string }
	byFile := map[where]skills.Skill{}
	for _, sk := range cat.All {
		byFile[where{sk.Name, sk.Path}] = sk
	}
	groups := map[string]*agentv1.ExtensionSkillGroup{}
	res := &agentv1.ListExtensionSkillsResponse{Cwd: place.Cwd, FolderTrusted: place.Trusted, ProjectSkippedUntrusted: cat.UntrustedProject}
	for _, c := range catalog.GetCommands() {
		if c.GetKind() == agentv1.SlashCommandKind_SLASH_COMMAND_KIND_BUILTIN || c.GetKind() == agentv1.SlashCommandKind_SLASH_COMMAND_KIND_AGENT {
			continue
		}
		sk := byFile[where{c.GetName(), c.GetPath()}]
		key, title := skillGroupOf(sk, pluginTitle)
		rec := externalRecordOf(external, home, sk)
		if rec != nil {
			key, title = "external", extensions.LabelExternal
			shown[rec.Name] = true
		}
		g := groups[key]
		if g == nil {
			g = &agentv1.ExtensionSkillGroup{Key: key, Title: title}
			groups[key] = g
		}
		row := &agentv1.ExtensionSkill{Command: c, PluginId: sk.Plugin, CanRemove: removableSkill(sk) && rec == nil, BlockedByPolicy: cat.BlockedByPolicy(c.GetName())}
		if row.GetBlockedByPolicy() {
			c.Switchable = false
		}
		if rec != nil {
			row.External = externalView(home, rec)
		}
		if onDisk(sk.Path) {
			if st, err := os.Stat(sk.Path); err == nil {
				row.UpdatedAt = st.ModTime().UTC().Format("2006-01-02T15:04:05Z")
			}
			row.Folder = sk.Dir
			if row.Folder == "" {
				row.Folder = filepath.Dir(sk.Path)
			}
		}
		g.Skills = append(g.Skills, row)
	}
	for name, rec := range external {
		if !shown[name] {
			res.Waiting = append(res.Waiting, &agentv1.ExtensionSkill{External: externalView(home, rec)})
		}
	}
	slices.SortFunc(res.Waiting, func(a, b *agentv1.ExtensionSkill) int {
		return strings.Compare(a.GetExternal().GetName(), b.GetExternal().GetName())
	})
	res.PolicyNote = skillPolicyNote(s.Base.ModePolicy.Managed())
	for _, g := range groups {
		res.Groups = append(res.Groups, g)
	}
	slices.SortFunc(res.Groups, func(a, b *agentv1.ExtensionSkillGroup) int {
		if d := skillGroupRank(a.GetKey()) - skillGroupRank(b.GetKey()); d != 0 {
			return d
		}
		return strings.Compare(a.GetTitle(), b.GetTitle())
	})
	return connect.NewResponse(res), nil
}

// onDisk tells a file from a server document ("sync:") and a plugin's inline command ("plugin:").
func onDisk(path string) bool {
	return path != "" && !strings.HasPrefix(path, "sync:") && !strings.HasPrefix(path, "plugin:")
}

func removableSkill(sk skills.Skill) bool {
	return onDisk(sk.Path) && sk.Plugin == "" && (sk.Source == skills.SourcePersonal || sk.Source == skills.SourceProject)
}

// ListExtensionPlugins implements ExtensionsService.ListExtensionPlugins.
func (s *Server) ListExtensionPlugins(ctx context.Context, req *connect.Request[agentv1.ListExtensionPluginsRequest]) (*connect.Response[agentv1.ListExtensionPluginsResponse], error) {
	store := bootstrap.PluginStore()
	// A policy or a read-only home costs the built-in marketplace, not the screen.
	_ = extensions.EnsureBuiltin(ctx, store)
	place := s.pluginPlace(req.Msg.GetCwd())
	list, err := store.List(place)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	markets, err := store.Browse()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	res := &agentv1.ListExtensionPluginsResponse{Cwd: place.Cwd, FolderTrusted: place.Trusted, SourcesRestricted: store.Policy().Restricted()}
	groups := map[string]*agentv1.ExtensionPluginGroup{}
	counts := map[string]int32{}
	for _, in := range list {
		key, title := "marketplace:"+in.Marketplace, in.Marketplace
		switch {
		case in.SuggestedBy != "":
			key, title = "project", "In this project"
		case in.Managed:
			key, title = "organization", "Organization"
		case in.Marketplace == "local":
			key, title = "local", "Installed directly"
		}
		g := groups[key]
		if g == nil {
			g = &agentv1.ExtensionPluginGroup{Key: key, Title: title}
			groups[key] = g
		}
		g.Plugins = append(g.Plugins, &agentv1.ExtensionPlugin{Plugin: pluginToProto(in), Category: in.Meta.Category, Tags: in.Meta.Tags, Publisher: in.Meta.Publisher, Homepage: in.Meta.Homepage, License: in.Meta.License, Folder: in.Root})
		if in.Status != "not_installed" {
			counts[in.Marketplace]++
		}
	}
	rank := func(key string) int {
		switch {
		case key == "project":
			return 0
		case strings.HasPrefix(key, "marketplace:"):
			return 1
		case key == "local":
			return 2
		}
		return 3
	}
	for _, g := range groups {
		res.Groups = append(res.Groups, g)
	}
	sort.Slice(res.Groups, func(i, j int) bool {
		a, b := res.Groups[i], res.Groups[j]
		if rank(a.GetKey()) != rank(b.GetKey()) {
			return rank(a.GetKey()) < rank(b.GetKey())
		}
		return a.GetTitle() < b.GetTitle()
	})
	for _, mk := range markets {
		res.Marketplaces = append(res.Marketplaces, &agentv1.ExtensionMarketplace{Name: mk.Name, Description: mk.Description, Source: mk.Source.String(), Commit: mk.Commit, UpdatedAt: mk.UpdatedAt, Managed: mk.Managed,
			Refusal: mk.Refusal, Problem: mk.Problem, PluginCount: int32(len(mk.Listings)), InstalledCount: counts[mk.Name], Builtin: mk.Builtin})
	}
	if suggested := store.Suggestions(place); len(suggested) > 0 {
		res.SuggestedMarketplaces = map[string]string{}
		for name, src := range suggested {
			res.SuggestedMarketplaces[name] = src.String()
		}
	}
	return connect.NewResponse(res), nil
}

// ListExtensionConnectors implements ExtensionsService.ListExtensionConnectors.
func (s *Server) ListExtensionConnectors(ctx context.Context, req *connect.Request[agentv1.ListExtensionConnectorsRequest]) (*connect.Response[agentv1.ListExtensionConnectorsResponse], error) {
	inv := s.mcpInventory()
	place := s.pluginPlace(req.Msg.GetCwd())
	listed := inv.List(ctx, place.Cwd)
	store := bootstrap.PluginStore()
	installed, err := store.List(place)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.ListExtensionConnectorsResponse{
		Connectors: extensions.FoldConnectors(listed.GetServers(), installed, store.Policy()), RegistryError: listed.GetRegistryError(), CanManageOrg: listed.GetCanManageOrg(),
		ProjectRoot: listed.GetProjectRoot(), ProjectTrusted: listed.GetProjectTrusted(), ProjectHasFile: listed.GetProjectHasFile(), ProjectError: listed.GetProjectError(), AccountAvailable: inv.Client != nil,
	}), nil
}

// DiscoverExtensions implements ExtensionsService.DiscoverExtensions.
func (s *Server) DiscoverExtensions(ctx context.Context, req *connect.Request[agentv1.DiscoverExtensionsRequest]) (*connect.Response[agentv1.DiscoverExtensionsResponse], error) {
	store := bootstrap.PluginStore()
	catalog, err := extensions.DefaultCatalog()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	in := extensions.Inputs{Catalog: catalog, Policy: store.Policy()}
	if req.Msg.GetKind() == agentv1.ExtensionKind_EXTENSION_KIND_CONNECTOR {
		inv := s.mcpInventory()
		place := s.pluginPlace(req.Msg.GetCwd())
		in.Configured = inv.List(ctx, place.Cwd).GetServers()
		if list, err := store.List(place); err == nil {
			for _, p := range list {
				in.PluginServers = append(in.PluginServers, pluginToProto(p).GetContents().GetMcpServers()...)
			}
		}
		managed := s.Base.ModePolicy.Managed()
		in.ServerAllowed = managed.McpServerAllowed
		in.RemoteDefault = agentv1.McpServerSource_MCP_SERVER_SOURCE_DEVICE
		if inv.Client != nil {
			in.RemoteDefault = agentv1.McpServerSource_MCP_SERVER_SOURCE_ACCOUNT
		}
		// With nothing allowed there is no point in asking the registry what exists.
		if !managed.RestrictMcpServers || len(managed.AllowedMcpServers) > 0 {
			in.Registry = extensionsRegistry()
		}
	} else {
		_ = extensions.EnsureBuiltin(ctx, store)
		if in.Markets, err = store.Browse(); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		in.Search = s.SkillSearch
		in.ExternalBlocked = s.Base.ModePolicy.Managed().ExternalSkillSearchDisabled
		if stored, err := skills.ListExternal(bootstrap.SkillHomeDir(s.Base)); err == nil {
			in.ExternalInstalled = map[string]bool{}
			for _, rec := range stored {
				in.ExternalInstalled[rec.ID] = true
			}
		}
	}
	return connect.NewResponse(extensions.Discover(ctx, req.Msg, in)), nil
}

// findSkill looks a skill up in what the client lists, so these calls only reach files the catalog knows of.
func (s *Server) findSkill(ctx context.Context, cwd, name, path string) (skills.Skill, error) {
	place := s.pluginPlace(cwd)
	for _, sk := range s.folderSkills(ctx, place.Cwd).Catalog(false).All {
		if sk.Name == name && sk.Path == path {
			return sk, nil
		}
	}
	return skills.Skill{}, connect.NewError(connect.CodeNotFound, errors.New("no such skill here"))
}

// GetSkillDetail implements ExtensionsService.GetSkillDetail.
func (s *Server) GetSkillDetail(ctx context.Context, req *connect.Request[agentv1.GetSkillDetailRequest]) (*connect.Response[agentv1.GetSkillDetailResponse], error) {
	sk, err := s.findSkill(ctx, req.Msg.GetCwd(), req.Msg.GetName(), req.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	res := &agentv1.GetSkillDetailResponse{}
	if body, err := sk.LoadBody(); err == nil {
		if len(body) > skillBodyLimit {
			body, res.Truncated = strings.ToValidUTF8(body[:skillBodyLimit], ""), true
		}
		res.Body = body
	}
	if sk.Dir != "" {
		_ = filepath.WalkDir(sk.Dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if len(res.Files) >= skillFilesLimit {
				return filepath.SkipAll
			}
			if !d.IsDir() {
				if rel, err := filepath.Rel(sk.Dir, p); err == nil {
					res.Files = append(res.Files, filepath.ToSlash(rel))
				}
			}
			return nil
		})
	}
	return connect.NewResponse(res), nil
}

// CreateSkill implements ExtensionsService.CreateSkill.
func (s *Server) CreateSkill(ctx context.Context, req *connect.Request[agentv1.CreateSkillRequest]) (*connect.Response[agentv1.CreateSkillResponse], error) {
	if err := changesAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if !skillNameRe.MatchString(name) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a skill's name uses letters, digits, \"-\" and \"_\", and starts with a letter or digit"))
	}
	root := filepath.Join(bootstrap.SkillHomeDir(s.Base), "skills")
	if cwd := req.Msg.GetCwd(); cwd != "" {
		place := s.pluginPlace(cwd)
		if place.Cwd == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("that folder can't be read"))
		}
		if !place.Trusted {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("trust this folder first: its skills don't load until you do"))
		}
		root = filepath.Join(place.Cwd, ".harness", "skills")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("a skill of that name is already there"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	path := filepath.Join(dir, skills.SkillFile)
	if err := os.WriteFile(path, []byte(skillFile(name, req.Msg.GetDescription(), req.Msg.GetBody())), 0o644); err != nil {
		_ = os.RemoveAll(dir)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.CreateSkillResponse{Path: path}), nil
}

// skillFile is a new SKILL.md. A JSON string is a YAML string, so whatever the description holds stays one value.
func skillFile(name, description, body string) string {
	quoted, _ := json.Marshal(strings.Join(strings.Fields(description), " "))
	if strings.TrimSpace(body) == "" {
		body = "Write here what the agent should do when this skill runs."
	}
	return "---\nname: " + name + "\ndescription: " + string(quoted) + "\n---\n\n" + strings.TrimSpace(body) + "\n"
}

// RemoveSkill implements ExtensionsService.RemoveSkill.
func (s *Server) RemoveSkill(ctx context.Context, req *connect.Request[agentv1.RemoveSkillRequest]) (*connect.Response[agentv1.RemoveSkillResponse], error) {
	if err := changesAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	sk, err := s.findSkill(ctx, req.Msg.GetCwd(), req.Msg.GetName(), req.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	if !removableSkill(sk) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this one isn't a file of yours here: change it where it comes from"))
	}
	if sk.Dir != "" {
		err = os.RemoveAll(sk.Dir)
	} else {
		err = os.Remove(sk.Path)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.RemoveSkillResponse{}), nil
}

func skillPolicyNote(m permissions.ManagedPolicy) string {
	var parts []string
	if m.RestrictSkills || len(m.BlockedSkills) > 0 {
		parts = append(parts, "Your administrator limits which skills can run.")
	}
	if m.ExternalSkillSearchDisabled {
		parts = append(parts, "Your administrator turned external skill search off.")
	}
	return strings.Join(parts, " ")
}

func externalRecordOf(st map[string]*skills.ExternalRecord, home string, sk skills.Skill) *skills.ExternalRecord {
	if sk.Dir == "" || filepath.Dir(sk.Dir) != filepath.Join(home, "skills") {
		return nil
	}
	return st[filepath.Base(sk.Dir)]
}

// externalView says how a stored skill stands now, reading its files.
func externalView(home string, rec *skills.ExternalRecord) *agentv1.InstalledExternalSkill {
	dir := filepath.Join(home, "skills", rec.Name)
	out := &agentv1.InstalledExternalSkill{Id: rec.ID, Source: rec.Source, Name: rec.Name, PinnedHash: rec.PinnedHash, License: rec.License, Installs: rec.Installs, InstalledAt: rec.InstalledAt, Folder: dir}
	for _, a := range rec.Audits {
		out.Audits = append(out.Audits, &skillsv1.SkillAudit{Provider: a.Provider, Status: a.Status, Summary: a.Summary, AuditedAt: a.AuditedAt, RiskLevel: a.RiskLevel})
	}
	if hash, err := skills.HashSkillDir(dir); err == nil {
		out.ContentHash = hash
		out.ChangedOnDisk = hash != rec.ContentHash
		out.Approved = rec.ApprovedHash != "" && hash == rec.ApprovedHash
	} else {
		out.ChangedOnDisk = true
	}
	if data, err := os.ReadFile(filepath.Join(dir, skills.SkillFile)); err == nil {
		out.Description = skills.Parse(string(data), rec.Name, skills.KindSkill).Description
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && len(out.Files) < skillFilesLimit {
			if rel, err := filepath.Rel(dir, p); err == nil {
				out.Files = append(out.Files, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	return out
}

var licenseRe = regexp.MustCompile(`(?m)^license:[ \t]*["']?([^"'\n]+?)["']?[ \t]*$`)

// skillLicense reads the license a SKILL.md's frontmatter names.
func skillLicense(text string) string {
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return ""
	}
	front, _, ok := strings.Cut(rest, "\n---")
	if !ok {
		return ""
	}
	if m := licenseRe.FindStringSubmatch(front); m != nil {
		return m[1]
	}
	return ""
}

func (s *Server) externalSearch() (extensions.SkillSearch, error) {
	if s.Base.ModePolicy.Managed().ExternalSkillSearchDisabled {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("your organization's policy turned external skill search off"))
	}
	if s.SkillSearch == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this client is not connected to an xlsft`s harness server"))
	}
	return s.SkillSearch, nil
}

func snapshotSkillText(files []*skillsv1.SkillFile) string {
	for _, f := range files {
		if f.GetPath() == skills.SkillFile {
			return f.GetContents()
		}
	}
	return ""
}

// GetExternalSkill implements ExtensionsService.GetExternalSkill.
func (s *Server) GetExternalSkill(ctx context.Context, req *connect.Request[agentv1.GetExternalSkillRequest]) (*connect.Response[agentv1.GetExternalSkillResponse], error) {
	search, err := s.externalSearch()
	if err != nil {
		return nil, err
	}
	snap, err := search.Snapshot(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	sk := snap.GetSkill()
	text := snapshotSkillText(snap.GetFiles())
	name := skills.ExternalSkillName(sk.GetSlug())
	res := &agentv1.GetExternalSkillResponse{Hash: snap.GetHash(), Files: snap.GetFiles(), Audits: sk.GetAudits(), License: skillLicense(text), Installs: sk.GetInstalls(), Name: name, Source: sk.GetSource()}
	if text != "" {
		res.Description = skills.Parse(text, name, skills.KindSkill).Description
	}
	if st, err := skills.ListExternal(bootstrap.SkillHomeDir(s.Base)); err == nil {
		rec := st[name]
		res.Installed = rec != nil && rec.ID == sk.GetId()
	}
	return connect.NewResponse(res), nil
}

// InstallExternalSkill implements ExtensionsService.InstallExternalSkill.
func (s *Server) InstallExternalSkill(ctx context.Context, req *connect.Request[agentv1.InstallExternalSkillRequest]) (*connect.Response[agentv1.InstallExternalSkillResponse], error) {
	if err := changesAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	search, err := s.externalSearch()
	if err != nil {
		return nil, err
	}
	snap, err := search.Snapshot(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if snap.GetHash() == "" || len(snap.GetFiles()) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the source holds no copy of this skill's files"))
	}
	if snap.GetHash() != req.Msg.GetHash() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this skill changed since you looked at it; look again"))
	}
	sk := snap.GetSkill()
	name := skills.ExternalSkillName(sk.GetSlug())
	if !s.Base.ModePolicy.Managed().SkillAllowed(name) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("your organization's policy doesn't allow this skill"))
	}
	rec := skills.ExternalRecord{ID: sk.GetId(), Source: sk.GetSource(), Name: name, PinnedHash: snap.GetHash(), License: skillLicense(snapshotSkillText(snap.GetFiles())), Installs: sk.GetInstalls()}
	for _, a := range sk.GetAudits() {
		rec.Audits = append(rec.Audits, skills.ExternalAudit{Provider: a.GetProvider(), Status: a.GetStatus(), Summary: a.GetSummary(), AuditedAt: a.GetAuditedAt(), RiskLevel: a.GetRiskLevel()})
	}
	files := make([]skills.ExternalFile, 0, len(snap.GetFiles()))
	for _, f := range snap.GetFiles() {
		files = append(files, skills.ExternalFile{Path: f.GetPath(), Contents: f.GetContents()})
	}
	home := bootstrap.SkillHomeDir(s.Base)
	stored, err := skills.InstallExternal(home, rec, files)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&agentv1.InstallExternalSkillResponse{Skill: externalView(home, stored)}), nil
}

// SetExternalSkillApproval implements ExtensionsService.SetExternalSkillApproval.
func (s *Server) SetExternalSkillApproval(ctx context.Context, req *connect.Request[agentv1.SetExternalSkillApprovalRequest]) (*connect.Response[agentv1.SetExternalSkillApprovalResponse], error) {
	if err := changesAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	var err error
	if req.Msg.GetApproved() {
		err = skills.ApproveExternal(bootstrap.SkillHomeDir(s.Base), req.Msg.GetName(), req.Msg.GetContentHash())
	} else {
		err = skills.RevokeExternal(bootstrap.SkillHomeDir(s.Base), req.Msg.GetName())
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&agentv1.SetExternalSkillApprovalResponse{}), nil
}

// RemoveExternalSkill implements ExtensionsService.RemoveExternalSkill.
func (s *Server) RemoveExternalSkill(ctx context.Context, req *connect.Request[agentv1.RemoveExternalSkillRequest]) (*connect.Response[agentv1.RemoveExternalSkillResponse], error) {
	if err := changesAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	if err := skills.RemoveExternal(bootstrap.SkillHomeDir(s.Base), req.Msg.GetName()); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&agentv1.RemoveExternalSkillResponse{}), nil
}
