package mcp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"connectrpc.com/connect"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/mcp/v1/mcpv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
)

// Client calls harness.mcp.v1.McpService with the device's bearer token (refreshed once on
// UNAUTHENTICATED). Errors keep their Connect form so callers can inspect codes and ErrorInfo.
type Client struct {
	rpc    mcpv1connect.McpServiceClient
	tokens rpc.Tokens
}

// NewClient creates the client. httpClient may be nil.
func NewClient(baseURL string, httpClient *http.Client, tokens rpc.Tokens) *Client {
	if httpClient == nil {
		httpClient = rpc.HTTPClient(baseURL)
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{rpc: mcpv1connect.NewMcpServiceClient(httpClient, baseURL, rpc.ClientOptions()...), tokens: tokens}
}

func unary[Req, Res any](ctx context.Context, c *Client, msg *Req, fn func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error)) (*Res, error) {
	do := func(token string) (*Res, error) {
		req := connect.NewRequest(msg)
		rpc.SetBearer(req.Header(), token)
		resp, err := fn(ctx, req)
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	out, err := do(token)
	if err != nil && c.tokens != nil && connect.CodeOf(err) == connect.CodeUnauthenticated {
		if token, err = c.tokens.Refresh(ctx); err != nil {
			return nil, err
		}
		out, err = do(token)
	}
	return out, err
}

func (c *Client) token(ctx context.Context) (string, error) {
	if c.tokens == nil {
		return "", nil
	}
	return c.tokens.Token(ctx)
}

// ListServers lists the servers visible to the caller (user, org and the project's servers),
// disabled ones included.
func (c *Client) ListServers(ctx context.Context, projectKey string) ([]*mcpv1.Server, error) {
	req := &mcpv1.ListServersRequest{ProjectKey: projectKey, IncludeDisabled: true}
	if projectKey == "" {
		// Without a project key the project scope would match every project's servers.
		req.Scopes = []commonv1.Scope{commonv1.Scope_SCOPE_USER, commonv1.Scope_SCOPE_ORG}
	}
	r, err := unary(ctx, c, req, c.rpc.ListServers)
	return r.GetServers(), err
}

// ListServersInfo is ListServers that also says whether the caller may manage org servers.
func (c *Client) ListServersInfo(ctx context.Context, projectKey string) ([]*mcpv1.Server, bool, error) {
	req := &mcpv1.ListServersRequest{ProjectKey: projectKey, IncludeDisabled: true}
	if projectKey == "" {
		req.Scopes = []commonv1.Scope{commonv1.Scope_SCOPE_USER, commonv1.Scope_SCOPE_ORG}
	}
	r, err := unary(ctx, c, req, c.rpc.ListServers)
	return r.GetServers(), r.GetCanManageOrg(), err
}

// GetServer returns one server.
func (c *Client) GetServer(ctx context.Context, id string) (*mcpv1.Server, error) {
	r, err := unary(ctx, c, &mcpv1.GetServerRequest{ServerId: id}, c.rpc.GetServer)
	return r.GetServer(), err
}

// DeleteServer removes a server from the registry.
func (c *Client) DeleteServer(ctx context.Context, id string) error {
	_, err := unary(ctx, c, &mcpv1.DeleteServerRequest{ServerId: id}, c.rpc.DeleteServer)
	return err
}

// CreateServerSecrets registers a server with write-only secrets.
func (c *Client) CreateServerSecrets(ctx context.Context, s *mcpv1.Server, ch *mcpv1.SecretChanges) (*mcpv1.Server, error) {
	r, err := unary(ctx, c, &mcpv1.CreateServerRequest{Server: s, Secrets: ch}, c.rpc.CreateServer)
	return r.GetServer(), err
}

// UpdateServerSecrets is UpdateServer with secret changes.
func (c *Client) UpdateServerSecrets(ctx context.Context, s *mcpv1.Server, ch *mcpv1.SecretChanges, base int64) (*mcpv1.Server, error) {
	r, err := unary(ctx, c, &mcpv1.UpdateServerRequest{Server: s, Secrets: ch, BaseVersion: base}, c.rpc.UpdateServer)
	if err != nil {
		return nil, err
	}
	if cf := r.GetConflict(); cf != nil {
		return cf.GetCurrent(), errConflict
	}
	return r.GetServer(), nil
}

// GetServerInfo asks a remote server about itself, through the proxy.
func (c *Client) GetServerInfo(ctx context.Context, id string) (*mcpv1.GetServerInfoResponse, error) {
	return unary(ctx, c, &mcpv1.GetServerInfoRequest{ServerId: id}, c.rpc.GetServerInfo)
}

// AllTools lists a remote server's tools, through the proxy.
func (c *Client) AllTools(ctx context.Context, id string) ([]*mcpv1.Tool, error) {
	var out []*mcpv1.Tool
	cursor := ""
	for range maxListPages {
		r, err := unary(ctx, c, &mcpv1.ListToolsRequest{ServerId: id, Cursor: cursor}, c.rpc.ListTools)
		if err != nil {
			return nil, err
		}
		out = append(out, r.GetTools()...)
		if cursor = r.GetNextCursor(); cursor == "" {
			break
		}
	}
	return out, nil
}

// AllPrompts lists a remote server's prompts, through the proxy.
func (c *Client) AllPrompts(ctx context.Context, id string) ([]*mcpv1.Prompt, error) {
	var out []*mcpv1.Prompt
	cursor := ""
	for range maxListPages {
		r, err := unary(ctx, c, &mcpv1.ListPromptsRequest{ServerId: id, Cursor: cursor}, c.rpc.ListPrompts)
		if err != nil {
			return nil, err
		}
		out = append(out, r.GetPrompts()...)
		if cursor = r.GetNextCursor(); cursor == "" {
			break
		}
	}
	return out, nil
}

// AllResources lists a remote server's resources and templates, through the proxy.
func (c *Client) AllResources(ctx context.Context, id string) ([]*mcpv1.Resource, []*mcpv1.ResourceTemplate, error) {
	var out []*mcpv1.Resource
	var tpl []*mcpv1.ResourceTemplate
	cursor := ""
	for range maxListPages {
		r, err := unary(ctx, c, &mcpv1.ListResourcesRequest{ServerId: id, Cursor: cursor}, c.rpc.ListResources)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, r.GetResources()...)
		tpl = append(tpl, r.GetTemplates()...)
		if cursor = r.GetNextCursor(); cursor == "" {
			break
		}
	}
	return out, tpl, nil
}

// GetPrompt fetches a prompt through the proxy; the MCP GetPromptResult JSON.
func (c *Client) GetPrompt(ctx context.Context, id, name string, args map[string]string) (string, error) {
	r, err := unary(ctx, c, &mcpv1.GetPromptRequest{ServerId: id, Name: name, Arguments: args}, c.rpc.GetPrompt)
	return r.GetResultJson(), err
}

// ReadResource reads a resource through the proxy; the MCP ReadResourceResult JSON.
func (c *Client) ReadResource(ctx context.Context, id, uri string) (string, error) {
	r, err := unary(ctx, c, &mcpv1.ReadResourceRequest{ServerId: id, Uri: uri}, c.rpc.ReadResource)
	return r.GetResultJson(), err
}

// CreateServer registers a server.
func (c *Client) CreateServer(ctx context.Context, s *mcpv1.Server) (*mcpv1.Server, error) {
	r, err := unary(ctx, c, &mcpv1.CreateServerRequest{Server: s}, c.rpc.CreateServer)
	return r.GetServer(), err
}

// UpdateServer replaces a server's config (CAS on base). A conflict returns the current server and
// errConflict.
func (c *Client) UpdateServer(ctx context.Context, s *mcpv1.Server, base int64) (*mcpv1.Server, error) {
	r, err := unary(ctx, c, &mcpv1.UpdateServerRequest{Server: s, BaseVersion: base}, c.rpc.UpdateServer)
	if err != nil {
		return nil, err
	}
	if cf := r.GetConflict(); cf != nil {
		return cf.GetCurrent(), errConflict
	}
	return r.GetServer(), nil
}

var errConflict = errors.New("mcp server changed concurrently")

// ReportDeviceStatus reports availability and approval of servers on this device.
func (c *Client) ReportDeviceStatus(ctx context.Context, sts []*mcpv1.DeviceServerStatus) error {
	_, err := unary(ctx, c, &mcpv1.ReportDeviceStatusRequest{Statuses: sts}, c.rpc.ReportDeviceStatus)
	return err
}

// IssueStdioLaunch fetches the resolved launch data of an approved stdio server. The result holds
// secrets: keep it in memory only and drop it once the process is spawned.
func (c *Client) IssueStdioLaunch(ctx context.Context, serverID, hash string) (*mcpv1.IssueStdioLaunchResponse, error) {
	return unary(ctx, c, &mcpv1.IssueStdioLaunchRequest{ServerId: serverID, ConfigHash: hash}, c.rpc.IssueStdioLaunch)
}

// GetToolCatalog fetches the tool catalog: the snapshot `version` when set, else the current one.
func (c *Client) GetToolCatalog(ctx context.Context, version, projectKey string) (*mcpv1.ToolCatalog, error) {
	r, err := unary(ctx, c, &mcpv1.GetToolCatalogRequest{Version: version, ProjectKey: projectKey}, c.rpc.GetToolCatalog)
	return r.GetCatalog(), err
}

// StartOAuth starts an authorization of a remote server and returns the URL the user must open.
func (c *Client) StartOAuth(ctx context.Context, serverID string) (*mcpv1.StartOAuthResponse, error) {
	return unary(ctx, c, &mcpv1.StartOAuthRequest{ServerId: serverID}, c.rpc.StartOAuth)
}

// CallHandlers receive what a proxied call sends besides its result. All optional.
type CallHandlers struct {
	OnProgress    func(*mcpv1.Progress)
	OnListChanged func(mcpv1.ListChanged_Kind)
	// OnElicit answers an elicitation. It runs on its own goroutine, and the call goes on meanwhile; ctx ends with the call.
	OnElicit func(ctx context.Context, e *mcpv1.ElicitationRequest) *mcpv1.RespondToElicitationRequest
}

// CallTool calls a tool of a remote server through the proxy and returns the MCP CallToolResult JSON.
func (c *Client) CallTool(ctx context.Context, req *mcpv1.CallToolRequest, h CallHandlers) (*mcpv1.CallToolResult, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	res, started, err := c.callTool(ctx, req, token, h)
	if err != nil && !started && c.tokens != nil && connect.CodeOf(err) == connect.CodeUnauthenticated {
		if token, err = c.tokens.Refresh(ctx); err != nil {
			return nil, err
		}
		res, _, err = c.callTool(ctx, req, token, h)
	}
	return res, err
}

func (c *Client) callTool(ctx context.Context, msg *mcpv1.CallToolRequest, token string, h CallHandlers) (*mcpv1.CallToolResult, bool, error) {
	req := connect.NewRequest(msg)
	rpc.SetBearer(req.Header(), token)
	stream, err := c.rpc.CallTool(ctx, req)
	if err != nil {
		return nil, false, err
	}
	defer stream.Close()
	started := false
	// The call ends: an open question is pointless and its goroutine stops.
	callCtx, done := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { done(); wg.Wait() }()
	for stream.Receive() {
		started = true
		m := stream.Msg()
		if p := m.GetProgress(); p != nil && h.OnProgress != nil {
			h.OnProgress(p)
		}
		if l := m.GetListChanged(); l != nil && h.OnListChanged != nil {
			h.OnListChanged(l.GetKind())
		}
		if e := m.GetElicitation(); e != nil && h.OnElicit != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				answer := h.OnElicit(callCtx, e)
				if callCtx.Err() != nil {
					return
				}
				// A call that ended meanwhile answers NOT_FOUND: nothing waits any more.
				_, _ = unary(callCtx, c, answer, c.rpc.RespondToElicitation)
			}()
		}
		if r := m.GetResult(); r != nil {
			return r, true, nil
		}
	}
	if err := stream.Err(); err != nil {
		return nil, started, err
	}
	return nil, started, errors.New("the MCP proxy ended the call without a result")
}

// IsNeedsAuth reports whether err says the user must (re)authorize the server.
func IsNeedsAuth(err error) bool {
	info := rpc.ErrorInfo(err)
	return info != nil && info.GetType() == commonv1.ErrorType_ERROR_TYPE_NEEDS_AUTH
}

// errorMessage returns the Connect message of err without the code prefix.
func errorMessage(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return err.Error()
}
