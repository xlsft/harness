package server

import (
	"context"
	"errors"
	"sort"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/plugins"
	"github.com/xlsft/harness/packages/client/skills"
)

// PluginService and /plugin. Plugins are this device's: it keeps them, approves them, and answers for what they run. So the server
// is never told, and another machine (the relay, a viewer of this session) can look at them and change nothing.

// pluginCommandTimeout bounds one /plugin command. Each fetch has its own, shorter limit.
const pluginCommandTimeout = 15 * time.Minute

var pluginStatus = map[plugins.Status]agentv1.PluginStatus{
	plugins.StatusActive: agentv1.PluginStatus_PLUGIN_STATUS_ACTIVE, plugins.StatusNeedsApproval: agentv1.PluginStatus_PLUGIN_STATUS_NEEDS_APPROVAL,
	plugins.StatusDisabled: agentv1.PluginStatus_PLUGIN_STATUS_DISABLED, plugins.StatusUntrustedFolder: agentv1.PluginStatus_PLUGIN_STATUS_UNTRUSTED_FOLDER,
	plugins.StatusBlockedByPolicy: agentv1.PluginStatus_PLUGIN_STATUS_BLOCKED_BY_POLICY, plugins.StatusChangedOnDisk: agentv1.PluginStatus_PLUGIN_STATUS_CHANGED_ON_DISK,
	plugins.StatusBroken: agentv1.PluginStatus_PLUGIN_STATUS_BROKEN, plugins.StatusNotInstalled: agentv1.PluginStatus_PLUGIN_STATUS_NOT_INSTALLED,
}

func pluginToProto(in plugins.Info) *agentv1.Plugin {
	c := in.Contents
	out := &agentv1.PluginContents{Skills: c.Skills, Commands: c.Commands, Agents: c.Agents, OutputStyles: c.OutputStyles, AllowedTools: c.AllowedTools, Skipped: c.Skipped}
	for _, h := range c.Hooks {
		out.Hooks = append(out.Hooks, &agentv1.PluginHook{Event: h.Event, Matcher: h.Matcher, Type: h.Type, Command: h.Command, FromSkill: h.From})
	}
	for _, m := range c.MCPServers {
		out.McpServers = append(out.McpServers, &agentv1.PluginMcpServer{Name: m.Name, Transport: m.Transport, Command: m.Command})
	}
	return &agentv1.Plugin{Id: in.ID, Name: in.Name, Marketplace: in.Marketplace, DisplayName: in.DisplayName, Description: in.Description, Version: in.Version, Commit: in.Commit,
		ContentHash: in.Hash, Status: pluginStatus[in.Status], Reason: in.Reason, Enabled: in.Enabled, Approved: in.Approved, Pinned: in.Pinned, Managed: in.Managed,
		SuggestedBy: in.SuggestedBy, Source: in.Source, InstalledAt: in.InstalledAt, Contents: out, Problems: in.Problems}
}

// pluginPlace says where statuses are told for: a folder of this machine and its trust, or none.
func (s *Server) pluginPlace(cwd string) plugins.Place {
	if cwd == "" {
		return plugins.Place{}
	}
	abs, err := absDir(cwd)
	if err != nil {
		return plugins.Place{}
	}
	trusted := config.IsChatDir(abs) || permissions.IsTrusted(abs)
	if s.Base.Trusted != nil {
		trusted = *s.Base.Trusted
	}
	return plugins.Place{Cwd: abs, Trusted: trusted}
}

func pluginError(err error) error {
	return connect.NewError(connect.CodeFailedPrecondition, err)
}

// changesAllowed refuses a change from another machine.
func changesAllowed(ctx context.Context, peer string) error {
	remote, _ := ctx.Value(remoteOriginKey{}).(bool)
	if remote || !localPeer(peer) {
		return connect.NewError(connect.CodePermissionDenied, errors.New("plugins are this device's own: change them at the device"))
	}
	return nil
}

// ListPlugins implements PluginService.ListPlugins.
func (s *Server) ListPlugins(_ context.Context, req *connect.Request[agentv1.ListPluginsRequest]) (*connect.Response[agentv1.ListPluginsResponse], error) {
	store := bootstrap.PluginStore()
	place := s.pluginPlace(req.Msg.GetCwd())
	list, err := store.List(place)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	res := &agentv1.ListPluginsResponse{SourcesRestricted: store.Policy().Restricted(), FolderTrusted: place.Trusted}
	installed := map[string]bool{}
	for _, in := range list {
		res.Plugins = append(res.Plugins, pluginToProto(in))
		installed[in.ID] = in.Status != plugins.StatusNotInstalled
	}
	markets, err := store.Marketplaces()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	for _, mk := range markets {
		out := &agentv1.PluginMarketplace{Name: mk.Name, Description: mk.Description, Source: mk.Source.String(), Commit: mk.Commit, UpdatedAt: mk.UpdatedAt, Managed: mk.Managed, Refusal: mk.Refusal, Problem: mk.Problem}
		for _, e := range mk.Plugins {
			id := e.Name + "@" + mk.Name
			out.Plugins = append(out.Plugins, &agentv1.PluginListing{Id: id, Name: e.Name, Description: e.Description, Version: e.Version, Installed: installed[id]})
		}
		sort.Slice(out.Plugins, func(i, j int) bool { return out.Plugins[i].GetName() < out.Plugins[j].GetName() })
		res.Marketplaces = append(res.Marketplaces, out)
	}
	if suggested := store.Suggestions(place); len(suggested) > 0 {
		res.SuggestedMarketplaces = map[string]string{}
		for name, src := range suggested {
			res.SuggestedMarketplaces[name] = src.String()
		}
	}
	return connect.NewResponse(res), nil
}

// InstallPlugin implements PluginService.InstallPlugin.
func (s *Server) InstallPlugin(ctx context.Context, req *connect.Request[agentv1.InstallPluginRequest]) (*connect.Response[agentv1.InstallPluginResponse], error) {
	if err := changesAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	store := bootstrap.PluginStore()
	rec, err := store.Install(ctx, req.Msg.GetSpec())
	if err != nil {
		return nil, pluginError(err)
	}
	in, err := store.Find(rec.ID, s.pluginPlace(req.Msg.GetCwd()))
	if err != nil {
		return nil, pluginError(err)
	}
	return connect.NewResponse(&agentv1.InstallPluginResponse{Plugin: pluginToProto(in)}), nil
}

// UpdatePlugin implements PluginService.UpdatePlugin.
func (s *Server) UpdatePlugin(ctx context.Context, req *connect.Request[agentv1.UpdatePluginRequest]) (*connect.Response[agentv1.UpdatePluginResponse], error) {
	if err := changesAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	store := bootstrap.PluginStore()
	res, err := store.Update(ctx, req.Msg.GetId())
	if err != nil {
		return nil, pluginError(err)
	}
	in, err := store.Find(res.Record.ID, s.pluginPlace(req.Msg.GetCwd()))
	if err != nil {
		return nil, pluginError(err)
	}
	return connect.NewResponse(&agentv1.UpdatePluginResponse{Plugin: pluginToProto(in), Changed: res.Changed, Note: res.Note}), nil
}

// RemovePlugin implements PluginService.RemovePlugin.
func (s *Server) RemovePlugin(ctx context.Context, req *connect.Request[agentv1.RemovePluginRequest]) (*connect.Response[agentv1.RemovePluginResponse], error) {
	if err := changesAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	if err := bootstrap.PluginStore().Remove(req.Msg.GetId(), req.Msg.GetKeepData()); err != nil {
		return nil, pluginError(err)
	}
	return connect.NewResponse(&agentv1.RemovePluginResponse{}), nil
}

// SetPluginEnabled implements PluginService.SetPluginEnabled.
func (s *Server) SetPluginEnabled(ctx context.Context, req *connect.Request[agentv1.SetPluginEnabledRequest]) (*connect.Response[agentv1.SetPluginEnabledResponse], error) {
	if err := changesAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	if err := bootstrap.PluginStore().SetEnabled(req.Msg.GetId(), req.Msg.GetEnabled()); err != nil {
		return nil, pluginError(err)
	}
	return connect.NewResponse(&agentv1.SetPluginEnabledResponse{}), nil
}

// SetPluginApproval implements PluginService.SetPluginApproval.
func (s *Server) SetPluginApproval(ctx context.Context, req *connect.Request[agentv1.SetPluginApprovalRequest]) (*connect.Response[agentv1.SetPluginApprovalResponse], error) {
	if err := changesAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	store := bootstrap.PluginStore()
	var err error
	if req.Msg.GetApproved() {
		if req.Msg.GetContentHash() == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("content_hash is required to approve"))
		}
		err = store.Approve(req.Msg.GetId(), req.Msg.GetContentHash())
	} else {
		err = store.Revoke(req.Msg.GetId())
	}
	if err != nil {
		return nil, pluginError(err)
	}
	return connect.NewResponse(&agentv1.SetPluginApprovalResponse{}), nil
}

// SetPluginPinned implements PluginService.SetPluginPinned.
func (s *Server) SetPluginPinned(ctx context.Context, req *connect.Request[agentv1.SetPluginPinnedRequest]) (*connect.Response[agentv1.SetPluginPinnedResponse], error) {
	if err := changesAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	if err := bootstrap.PluginStore().SetPinned(req.Msg.GetId(), req.Msg.GetPinned()); err != nil {
		return nil, pluginError(err)
	}
	return connect.NewResponse(&agentv1.SetPluginPinnedResponse{}), nil
}

// AddPluginMarketplace implements PluginService.AddPluginMarketplace.
func (s *Server) AddPluginMarketplace(ctx context.Context, req *connect.Request[agentv1.AddPluginMarketplaceRequest]) (*connect.Response[agentv1.AddPluginMarketplaceResponse], error) {
	if err := changesAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	src, err := plugins.ParseSource(req.Msg.GetSource())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	rec, err := bootstrap.PluginStore().AddMarketplace(ctx, src)
	if err != nil {
		return nil, pluginError(err)
	}
	return connect.NewResponse(&agentv1.AddPluginMarketplaceResponse{Name: rec.Name}), nil
}

// UpdatePluginMarketplace implements PluginService.UpdatePluginMarketplace.
func (s *Server) UpdatePluginMarketplace(ctx context.Context, req *connect.Request[agentv1.UpdatePluginMarketplaceRequest]) (*connect.Response[agentv1.UpdatePluginMarketplaceResponse], error) {
	if err := changesAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	if err := bootstrap.PluginStore().UpdateMarketplace(ctx, req.Msg.GetName()); err != nil {
		return nil, pluginError(err)
	}
	return connect.NewResponse(&agentv1.UpdatePluginMarketplaceResponse{}), nil
}

// RemovePluginMarketplace implements PluginService.RemovePluginMarketplace.
func (s *Server) RemovePluginMarketplace(ctx context.Context, req *connect.Request[agentv1.RemovePluginMarketplaceRequest]) (*connect.Response[agentv1.RemovePluginMarketplaceResponse], error) {
	if err := changesAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	if err := bootstrap.PluginStore().RemoveMarketplace(req.Msg.GetName()); err != nil {
		return nil, pluginError(err)
	}
	return connect.NewResponse(&agentv1.RemovePluginMarketplaceResponse{}), nil
}

// runPlugin is /plugin. Its fetches take a while, so it runs apart from the message that asked for it (builtinCommand.async).
func runPlugin(ctx context.Context, s *Server, ls *liveSession, args string) slashOutcome {
	return pluginCommand(ctx, s, ls.sess.Dir(), args)
}

func pluginCommand(ctx context.Context, s *Server, cwd, args string) slashOutcome {
	ctx, cancel := context.WithTimeout(ctx, pluginCommandTimeout)
	defer cancel()
	words := skills.SplitArguments(args)
	remote, _ := ctx.Value(remoteOriginKey{}).(bool)
	text, err := plugins.RunCommand(ctx, bootstrap.PluginStore(), words, plugins.Caller{Remote: remote, Place: s.pluginPlace(cwd), Prefix: "/plugin"})
	if err != nil {
		return slashError("%v", err)
	}
	out := slashOutcome{text: text}
	if len(words) == 0 && !remote {
		out.action = agentv1.SlashCommandResult_ACTION_OPEN_PLUGINS
	}
	return out
}
