package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
)

// Who manages a server, for display.
const (
	managedDevice = "This device"
	managedUser   = "You"
	managedAdmin  = "Admin"
	managedFile   = "Repository file"
)

// inspectTimeout bounds connecting to one server and listing what it has.
const inspectTimeout = 30 * time.Second

// ProjectInfo describes a folder for its .mcp.json.
type ProjectInfo struct {
	// Root is the repository root (the folder outside a repository); Key the project key.
	Root, Key string
	Trusted   bool
}

// Inventory is what Settings > MCP works on: every server of this device, whoever manages it. It holds no session.
type Inventory struct {
	// Client is the McpService client; nil without an xlsft`s harness server (device servers still work).
	Client *Client
	// Local is this device's own list.
	Local *LocalStore
	// Approvals is this device's decisions.
	Approvals *Approvals
	// Project describes a folder.
	Project func(ctx context.Context, folder string) ProjectInfo
	// Rules returns the user's permission rules that apply to MCP tools (may be nil).
	Rules func(ctx context.Context) (allow, ask, deny []string)
	// Cwd is where a server starts when it has no directory of its own.
	Cwd string
}

// List returns the servers. The registry being unreachable is reported, not an error: device servers keep working.
func (inv *Inventory) List(ctx context.Context, folder string) *agentv1.ListMcpServersResponse {
	out := &agentv1.ListMcpServersResponse{}
	for _, s := range inv.Local.List() {
		out.Servers = append(out.Servers, inv.localEntry(s))
	}
	var info ProjectInfo
	var file []*mcpv1.Server
	if folder != "" && inv.Project != nil {
		info = inv.Project(ctx, folder)
		out.ProjectRoot, out.ProjectTrusted = info.Root, info.Trusted
		path := filepath.Join(info.Root, ".mcp.json")
		var err error
		file, err = ReadProjectConfig(path, info.Key)
		switch {
		case err == nil:
			out.ProjectHasFile = true
		case !os.IsNotExist(err):
			out.ProjectError = fmt.Sprintf(".mcp.json could not be read: %v", err)
		}
	}
	if inv.Client == nil {
		out.RegistryError = "This client is not connected to an xlsft`s harness server."
	} else {
		key := ""
		if info.Trusted {
			key = info.Key
		}
		all, canManage, err := inv.Client.ListServersInfo(ctx, key)
		if err == nil && info.Trusted && len(file) > 0 {
			if changed, problems := syncProjectFile(ctx, inv.Client, filepath.Join(info.Root, ".mcp.json"), info.Key, all); changed || len(problems) > 0 {
				if len(problems) > 0 {
					out.ProjectError = strings.Join(problems, "; ")
				}
				if again, _, err2 := inv.Client.ListServersInfo(ctx, key); err2 == nil {
					all = again
				}
			}
		}
		if err != nil {
			out.RegistryError = errorMessage(err)
		}
		out.CanManageOrg = canManage
		inFile := func(name string) bool {
			return slices.ContainsFunc(file, func(s *mcpv1.Server) bool { return s.GetName() == name })
		}
		for _, s := range all {
			if s.GetScope() == commonv1.Scope_SCOPE_PROJECT {
				if s.GetProjectKey() != info.Key || !inFile(s.GetName()) {
					continue
				}
			}
			out.Servers = append(out.Servers, inv.registryEntry(s))
		}
	}
	// What the file lists but the registry does not (yet): an untrusted folder's servers are shown, never registered.
	for _, f := range file {
		if slices.ContainsFunc(out.Servers, func(e *agentv1.McpServerEntry) bool {
			return e.GetSource() == agentv1.McpServerSource_MCP_SERVER_SOURCE_PROJECT && e.GetServer().GetName() == f.GetName()
		}) {
			continue
		}
		e := &agentv1.McpServerEntry{Server: f, Source: agentv1.McpServerSource_MCP_SERVER_SOURCE_PROJECT, ManagedBy: managedFile, LaunchSummary: launchLine(f)}
		if info.Trusted {
			e.State, e.StateDetail = agentv1.McpServerState_MCP_SERVER_STATE_UNAVAILABLE, "Not registered yet: the xlsft`s harness server could not be reached."
		} else {
			e.State, e.StateDetail = agentv1.McpServerState_MCP_SERVER_STATE_NEEDS_TRUST, "Trust this folder to use the servers of its .mcp.json."
		}
		out.Servers = append(out.Servers, e)
	}
	return out
}

func (inv *Inventory) localEntry(s *mcpv1.Server) *agentv1.McpServerEntry {
	e := &agentv1.McpServerEntry{Server: s, Source: agentv1.McpServerSource_MCP_SERVER_SOURCE_DEVICE, ManagedBy: managedDevice, CanEdit: true, CanRemove: true, LaunchSummary: launchLine(s)}
	inv.fillState(e, true)
	return e
}

func (inv *Inventory) registryEntry(s *mcpv1.Server) *agentv1.McpServerEntry {
	e := &agentv1.McpServerEntry{Server: s, LaunchSummary: launchLine(s)}
	switch s.GetScope() {
	case commonv1.Scope_SCOPE_ORG:
		e.Source, e.ManagedBy = agentv1.McpServerSource_MCP_SERVER_SOURCE_ORG, managedAdmin
		e.CanEdit, e.CanRemove = s.GetCanEdit(), s.GetCanEdit()
	case commonv1.Scope_SCOPE_PROJECT:
		e.Source, e.ManagedBy = agentv1.McpServerSource_MCP_SERVER_SOURCE_PROJECT, managedFile
		// Its config is the file's; this device decides with its approval.
	default:
		e.Source, e.ManagedBy = agentv1.McpServerSource_MCP_SERVER_SOURCE_ACCOUNT, managedUser
		e.CanEdit, e.CanRemove = s.GetCanEdit(), s.GetCanEdit()
	}
	stdio := s.GetTransport() == mcpv1.Transport_TRANSPORT_STDIO
	inv.fillState(e, stdio || s.GetScope() == commonv1.Scope_SCOPE_PROJECT)
	return e
}

func (inv *Inventory) fillState(e *agentv1.McpServerEntry, needsApproval bool) {
	s := e.GetServer()
	if needsApproval {
		e.Approval = inv.Approvals.State(s.GetId(), s.GetConfigHash())
	}
	set := func(st agentv1.McpServerState, detail string) { e.State, e.StateDetail = st, detail }
	if !s.GetEnabled() {
		set(agentv1.McpServerState_MCP_SERVER_STATE_DISABLED, "")
		return
	}
	if r := checkAvailable(s); r != "" {
		set(agentv1.McpServerState_MCP_SERVER_STATE_UNAVAILABLE, r)
		return
	}
	if needsApproval {
		switch e.GetApproval() {
		case mcpv1.ApprovalState_APPROVAL_STATE_DENIED:
			set(agentv1.McpServerState_MCP_SERVER_STATE_DENIED, "Not allowed on this device.")
			return
		case mcpv1.ApprovalState_APPROVAL_STATE_PENDING:
			set(agentv1.McpServerState_MCP_SERVER_STATE_NEEDS_APPROVAL, "Needs your approval on this device; a changed config asks again.")
			return
		}
	}
	if s.GetAuth().GetState() == mcpv1.AuthState_AUTH_STATE_NEEDS_AUTH || s.GetAuth().GetState() == mcpv1.AuthState_AUTH_STATE_ERROR {
		set(agentv1.McpServerState_MCP_SERVER_STATE_NEEDS_AUTH, s.GetAuth().GetMessage())
		return
	}
	set(agentv1.McpServerState_MCP_SERVER_STATE_READY, "")
}

// find returns the entry of a server; the folder lets project servers be found.
func (inv *Inventory) find(ctx context.Context, id, folder string) (*agentv1.McpServerEntry, error) {
	if IsLocalID(id) {
		s := inv.Local.Get(id)
		if s == nil {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no such MCP server on this device"))
		}
		return inv.localEntry(s), nil
	}
	if inv.Client == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this client is not connected to an xlsft`s harness server"))
	}
	s, err := inv.Client.GetServer(ctx, id)
	if err != nil {
		return nil, err
	}
	return inv.registryEntry(s), nil
}

// Inspect connects to a server that may be contacted and lists what it has.
func (inv *Inventory) Inspect(ctx context.Context, id, folder string) (*agentv1.InspectMcpServerResponse, error) {
	e, err := inv.find(ctx, id, folder)
	if err != nil {
		return nil, err
	}
	if st := e.GetState(); st != agentv1.McpServerState_MCP_SERVER_STATE_READY {
		// Disabled, unapproved or signed-out servers are not contacted; the state says why.
		return &agentv1.InspectMcpServerResponse{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, inspectTimeout)
	defer cancel()
	s := e.GetServer()
	out := &agentv1.InspectMcpServerResponse{}
	var toolsList []*mcpv1.Tool
	var prompts []*mcpv1.Prompt
	var res []*mcpv1.Resource
	var tpl []*mcpv1.ResourceTemplate
	if s.GetTransport() != mcpv1.Transport_TRANSPORT_STDIO && !IsLocalID(id) {
		info, err := inv.Client.GetServerInfo(ctx, id)
		if err != nil {
			out.Error = errorMessage(err)
			return out, nil
		}
		out.ServerName, out.ServerVersion, out.InstructionsChars = info.GetName(), info.GetVersion(), int64(len(info.GetInstructions()))
		if toolsList, err = inv.Client.AllTools(ctx, id); err != nil {
			out.Error = errorMessage(err)
			return out, nil
		}
		if info.GetHasPrompts() {
			if prompts, err = inv.Client.AllPrompts(ctx, id); err != nil {
				out.Error = errorMessage(err)
			}
		}
		if info.GetHasResources() {
			if res, tpl, err = inv.Client.AllResources(ctx, id); err != nil {
				out.Error = errorMessage(err)
			}
		}
	} else {
		// A throwaway connection; the agent's sessions have their own.
		conn, err := connectDirect(ctx, s, IsLocalID(id), inv.Client, inv.Local, inv.Cwd, connHooks{root: inv.Cwd})
		if err != nil {
			out.Error = err.Error()
			return out, nil
		}
		defer conn.close()
		ir := conn.session.InitializeResult()
		if ir.ServerInfo != nil {
			out.ServerName, out.ServerVersion = ir.ServerInfo.Name, ir.ServerInfo.Version
		}
		out.InstructionsChars = int64(len(ir.Instructions))
		if toolsList, err = conn.listTools(ctx); err != nil {
			out.Error = err.Error()
			return out, nil
		}
		if prompts, err = conn.listPrompts(ctx); err != nil {
			out.Error = err.Error()
		}
		if res, tpl, err = conn.listResources(ctx); err != nil {
			out.Error = err.Error()
		}
	}
	out.Connected = true
	var allow, ask, deny []string
	if inv.Rules != nil {
		allow, ask, deny = inv.Rules(ctx)
	}
	la, lq, ld := inv.Local.Rules()
	allow, ask, deny = append(allow, la...), append(ask, lq...), append(deny, ld...)
	for _, t := range toolsList {
		if !toolVisible(s, t.GetName()) {
			continue
		}
		full := ToolName(s.GetName(), t.GetName())
		out.Tools = append(out.Tools, &agentv1.McpToolInfo{Name: t.GetName(), Title: t.GetTitle(), Description: t.GetDescription(), FullName: full, Rule: ruleOf(s.GetName(), full, allow, ask, deny)})
	}
	for _, p := range prompts {
		pi := &agentv1.McpPromptInfo{Name: p.GetName(), Title: p.GetTitle(), Description: p.GetDescription()}
		for _, a := range parsePromptArguments(p.GetArgumentsJson()) {
			pi.Arguments = append(pi.Arguments, &agentv1.McpPromptArgument{Name: a.Name, Description: a.Description, Required: a.Required})
		}
		out.Prompts = append(out.Prompts, pi)
	}
	for _, r := range res {
		out.Resources = append(out.Resources, &agentv1.McpResourceInfo{Uri: r.GetUri(), Name: r.GetName(), Title: r.GetTitle(), Description: r.GetDescription(), MimeType: r.GetMimeType()})
	}
	for _, r := range tpl {
		out.Resources = append(out.Resources, &agentv1.McpResourceInfo{UriTemplate: r.GetUriTemplate(), Name: r.GetName(), Title: r.GetTitle(), Description: r.GetDescription(), MimeType: r.GetMimeType()})
	}
	return out, nil
}

// ruleOf returns the decision the rules give a tool, the way the permission engine ranks them.
func ruleOf(server, full string, allow, ask, deny []string) string {
	hit := func(list []string) bool {
		return slices.ContainsFunc(list, func(r string) bool {
			return r == full || r == Prefix+NormalizeName(server) || (strings.Contains(r, "*") && globMatch(r, full))
		})
	}
	switch {
	case hit(deny):
		return "deny"
	case hit(ask):
		return "ask"
	case hit(allow):
		return "allow"
	}
	return ""
}

// RuleString returns the permission rule for a tool of a server, or the whole server when tool is empty.
func RuleString(server, tool string) string {
	if tool == "" {
		return Prefix + NormalizeName(server)
	}
	return ToolName(server, tool)
}

// Create adds a server: on this device or in the registry.
func (inv *Inventory) Create(ctx context.Context, source agentv1.McpServerSource, s *mcpv1.Server, secrets *mcpv1.SecretChanges) (*agentv1.McpServerEntry, error) {
	if s == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("server is required"))
	}
	switch source {
	case agentv1.McpServerSource_MCP_SERVER_SOURCE_DEVICE:
		cur, err := inv.Local.Create(s, secrets)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		// Adding it on this device is the approval. A file edited behind our back has another hash and asks.
		if err := inv.Approvals.Set(cur.GetId(), cur.GetName(), cur.GetConfigHash(), true); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return inv.localEntry(cur), nil
	case agentv1.McpServerSource_MCP_SERVER_SOURCE_ACCOUNT, agentv1.McpServerSource_MCP_SERVER_SOURCE_ORG:
		if inv.Client == nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this client is not connected to an xlsft`s harness server"))
		}
		s.Scope = commonv1.Scope_SCOPE_USER
		if source == agentv1.McpServerSource_MCP_SERVER_SOURCE_ORG {
			s.Scope = commonv1.Scope_SCOPE_ORG
		}
		if s.GetTransport() == mcpv1.Transport_TRANSPORT_STDIO && source == agentv1.McpServerSource_MCP_SERVER_SOURCE_ACCOUNT {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a personal stdio server stays on this device: add it to this device instead"))
		}
		cur, err := inv.Client.CreateServerSecrets(ctx, s, secrets)
		if err != nil {
			return nil, err
		}
		return inv.registryEntry(cur), nil
	}
	return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a server goes on this device, in your account or in the organization"))
}

// Update changes a server. conflict is set when the registry's copy moved on.
func (inv *Inventory) Update(ctx context.Context, s *mcpv1.Server, secrets *mcpv1.SecretChanges, base int64) (entry, conflict *agentv1.McpServerEntry, err error) {
	if s == nil || s.GetId() == "" {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("server.id is required"))
	}
	if IsLocalID(s.GetId()) {
		cur, err := inv.Local.Update(s, secrets)
		if err != nil {
			return nil, nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		if err := inv.Approvals.Set(cur.GetId(), cur.GetName(), cur.GetConfigHash(), true); err != nil {
			return nil, nil, connect.NewError(connect.CodeInternal, err)
		}
		return inv.localEntry(cur), nil, nil
	}
	if inv.Client == nil {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this client is not connected to an xlsft`s harness server"))
	}
	cur, err := inv.Client.UpdateServerSecrets(ctx, s, secrets, base)
	if errors.Is(err, errConflict) {
		return nil, inv.registryEntry(cur), nil
	}
	if err != nil {
		return nil, nil, err
	}
	return inv.registryEntry(cur), nil, nil
}

// Delete removes a server.
func (inv *Inventory) Delete(ctx context.Context, id string) error {
	if IsLocalID(id) {
		if err := inv.Local.Delete(id); err != nil {
			return connect.NewError(connect.CodeNotFound, err)
		}
		_ = inv.Approvals.Forget(id)
		return nil
	}
	if inv.Client == nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("this client is not connected to an xlsft`s harness server"))
	}
	return inv.Client.DeleteServer(ctx, id)
}

// SetApproval records this device's decision for the server's current config and tells the xlsft`s harness server about the
// registry's stdio servers, whose launch it checks against that report.
func (inv *Inventory) SetApproval(ctx context.Context, id, folder string, approved bool) (*agentv1.McpServerEntry, error) {
	e, err := inv.find(ctx, id, folder)
	if err != nil {
		return nil, err
	}
	s := e.GetServer()
	if e.GetSource() == agentv1.McpServerSource_MCP_SERVER_SOURCE_PROJECT && inv.Project != nil {
		// The trust rule: a repository's servers are only ever approved in a folder the user trusts.
		if folder == "" || !inv.Project(ctx, folder).Trusted {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("trust this folder first: the servers of a .mcp.json only run in a trusted folder"))
		}
	}
	if err := inv.Approvals.Set(id, s.GetName(), s.GetConfigHash(), approved); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !IsLocalID(id) && s.GetTransport() == mcpv1.Transport_TRANSPORT_STDIO && inv.Client != nil {
		st := mcpv1.ApprovalState_APPROVAL_STATE_DENIED
		if approved {
			st = mcpv1.ApprovalState_APPROVAL_STATE_APPROVED
		}
		status := &mcpv1.DeviceServerStatus{ServerId: id, ConfigHash: s.GetConfigHash(), Approval: st, Availability: mcpv1.Availability_AVAILABILITY_AVAILABLE}
		if r := checkAvailable(s); r != "" {
			status.Availability, status.Reason = mcpv1.Availability_AVAILABILITY_UNAVAILABLE_ON_DEVICE, r
		}
		if err := inv.Client.ReportDeviceStatus(ctx, []*mcpv1.DeviceServerStatus{status}); err != nil {
			return nil, err
		}
	}
	return inv.find(ctx, id, folder)
}
