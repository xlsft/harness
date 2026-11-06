package fakemcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/mcp/v1/mcpv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
)

// Service is a scripted harness.mcp.v1.McpService for tests.
type Service struct {
	mcpv1connect.UnimplementedMcpServiceHandler

	mu sync.Mutex
	// Servers returned by ListServers.
	Servers []*mcpv1.Server
	// Catalog returned by GetToolCatalog (tools of remote servers; stdio servers get reported tools).
	Catalog map[string][]*mcpv1.Tool
	// CatalogUI marks servers whose catalog entry has requires_user_interaction.
	CatalogUI map[string]bool
	// Launch is the IssueStdioLaunch answer per server id (command, args, env).
	Launch map[string]*mcpv1.IssueStdioLaunchResponse
	// CallTool answers remote calls (server id, tool, args JSON) with a result JSON or an error.
	Call func(req *mcpv1.CallToolRequest) (string, error)

	// Elicit is sent on the stream of every remote call; the call waits for RespondToElicitation.
	Elicit *mcpv1.ElicitationRequest
	// Answers records RespondToElicitation; answered wakes the waiting call.
	Answers []*mcpv1.RespondToElicitationRequest
	// Chatter is sent on the stream of every remote call before the result.
	Chatter []*mcpv1.CallToolResponse
	// CatalogVersion overrides the catalog's version; Instructions and HasResources fill catalog entries, by server id.
	CatalogVersion string
	Instructions   map[string]string
	HasResources   map[string]bool
	// What the proxy RPCs answer, by server id.
	Tools                        map[string][]*mcpv1.Tool
	Prompts                      map[string][]*mcpv1.Prompt
	Resources                    map[string][]*mcpv1.Resource
	Info                         map[string]*mcpv1.GetServerInfoResponse
	PromptResult, ResourceResult string
	InfoError                    error
	Deleted                      []string

	answered chan struct{}

	Reports  []*mcpv1.DeviceServerStatus
	Launches []string
	Calls    []*mcpv1.CallToolRequest
	Created  []*mcpv1.Server
	Updated  []*mcpv1.Server
	Catalogs []*mcpv1.GetToolCatalogRequest
	Lists    []*mcpv1.ListServersRequest
}

// NewService returns an empty service.
func NewService() *Service {
	return &Service{Catalog: map[string][]*mcpv1.Tool{}, CatalogUI: map[string]bool{}, Launch: map[string]*mcpv1.IssueStdioLaunchResponse{},
		Instructions: map[string]string{}, HasResources: map[string]bool{}, Tools: map[string][]*mcpv1.Tool{}, Prompts: map[string][]*mcpv1.Prompt{},
		Resources: map[string][]*mcpv1.Resource{}, Info: map[string]*mcpv1.GetServerInfoResponse{}, answered: make(chan struct{}, 8)}
}

// Start serves the service over HTTP for the test and returns its base URL.
func (s *Service) Start(t testing.TB) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(mcpv1connect.NewMcpServiceHandler(s))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// SelfLaunch is a launch answer that runs the current test binary as the fake server (the test's
// TestMain must call MaybeServe). env is added to the process environment.
func SelfLaunch(env map[string]string) *mcpv1.IssueStdioLaunchResponse {
	e := map[string]string{EnvMarker: "1"}
	for k, v := range env {
		e[k] = v
	}
	exe, _ := os.Executable()
	return &mcpv1.IssueStdioLaunchResponse{Command: exe, Env: e, ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}
}

// Snapshot returns copies of the recorded reports, launches and calls.
func (s *Service) Snapshot() (reports []*mcpv1.DeviceServerStatus, launches []string, calls []*mcpv1.CallToolRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(reports, s.Reports...), append(launches, s.Launches...), append(calls, s.Calls...)
}

// ListServers implements McpServiceHandler.
func (s *Service) ListServers(_ context.Context, req *connect.Request[mcpv1.ListServersRequest]) (*connect.Response[mcpv1.ListServersResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Lists = append(s.Lists, req.Msg)
	return connect.NewResponse(&mcpv1.ListServersResponse{Servers: s.Servers}), nil
}

// CreateServer implements McpServiceHandler.
func (s *Service) CreateServer(_ context.Context, req *connect.Request[mcpv1.CreateServerRequest]) (*connect.Response[mcpv1.CreateServerResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sv := req.Msg.GetServer()
	sv.Id = "mcp_created_" + sv.GetName()
	sv.Version = 1
	sv.ConfigHash = "hash-" + sv.GetName()
	s.Created = append(s.Created, sv)
	s.Servers = append(s.Servers, sv)
	return connect.NewResponse(&mcpv1.CreateServerResponse{Server: sv}), nil
}

// UpdateServer implements McpServiceHandler.
func (s *Service) UpdateServer(_ context.Context, req *connect.Request[mcpv1.UpdateServerRequest]) (*connect.Response[mcpv1.UpdateServerResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Updated = append(s.Updated, req.Msg.GetServer())
	return connect.NewResponse(&mcpv1.UpdateServerResponse{Result: &mcpv1.UpdateServerResponse_Server{Server: req.Msg.GetServer()}}), nil
}

// ReportDeviceStatus implements McpServiceHandler.
func (s *Service) ReportDeviceStatus(_ context.Context, req *connect.Request[mcpv1.ReportDeviceStatusRequest]) (*connect.Response[mcpv1.ReportDeviceStatusResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Reports = append(s.Reports, req.Msg.GetStatuses()...)
	return connect.NewResponse(&mcpv1.ReportDeviceStatusResponse{}), nil
}

// IssueStdioLaunch implements McpServiceHandler: like core, it requires a reported approval.
func (s *Service) IssueStdioLaunch(_ context.Context, req *connect.Request[mcpv1.IssueStdioLaunchRequest]) (*connect.Response[mcpv1.IssueStdioLaunchResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	approved := false
	for _, r := range s.Reports {
		if r.GetServerId() == req.Msg.GetServerId() && r.GetConfigHash() == req.Msg.GetConfigHash() {
			approved = r.GetApproval() == mcpv1.ApprovalState_APPROVAL_STATE_APPROVED
		}
	}
	if !approved {
		return nil, rpc.NewError(connect.CodeFailedPrecondition, commonv1.ErrorType_ERROR_TYPE_FAILED_PRECONDITION, "no approval reported", 0)
	}
	l := s.Launch[req.Msg.GetServerId()]
	if l == nil {
		return nil, rpc.NewError(connect.CodeNotFound, commonv1.ErrorType_ERROR_TYPE_NOT_FOUND, "unknown server", 0)
	}
	s.Launches = append(s.Launches, req.Msg.GetServerId())
	return connect.NewResponse(l), nil
}

// GetToolCatalog implements McpServiceHandler.
func (s *Service) GetToolCatalog(_ context.Context, req *connect.Request[mcpv1.GetToolCatalogRequest]) (*connect.Response[mcpv1.GetToolCatalogResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Catalogs = append(s.Catalogs, req.Msg)
	if req.Msg.GetVersion() == "cat_expired" {
		return nil, rpc.NewError(connect.CodeNotFound, commonv1.ErrorType_ERROR_TYPE_NOT_FOUND, "expired", 0)
	}
	cat := &mcpv1.ToolCatalog{Version: "cat_1"}
	if s.CatalogVersion != "" {
		cat.Version = s.CatalogVersion
	}
	for _, sv := range s.Servers {
		cs := &mcpv1.CatalogServer{ServerId: sv.GetId(), Name: sv.GetName(), Transport: sv.GetTransport(), AlwaysLoad: sv.GetAlwaysLoad(),
			Tools: s.Catalog[sv.GetId()], RequiresUserInteraction: s.CatalogUI[sv.GetId()], Instructions: s.Instructions[sv.GetId()], HasResources: s.HasResources[sv.GetId()]}
		cat.Servers = append(cat.Servers, cs)
	}
	return connect.NewResponse(&mcpv1.GetToolCatalogResponse{Catalog: cat}), nil
}

// StartOAuth implements McpServiceHandler.
func (s *Service) StartOAuth(_ context.Context, req *connect.Request[mcpv1.StartOAuthRequest]) (*connect.Response[mcpv1.StartOAuthResponse], error) {
	return connect.NewResponse(&mcpv1.StartOAuthResponse{FlowId: "mfl_1", AuthorizationUrl: "https://auth.example/authorize?server=" + req.Msg.GetServerId()}), nil
}

// CallTool implements McpServiceHandler.
func (s *Service) CallTool(ctx context.Context, req *connect.Request[mcpv1.CallToolRequest], stream *connect.ServerStream[mcpv1.CallToolResponse]) error {
	s.mu.Lock()
	s.Calls = append(s.Calls, req.Msg)
	call := s.Call
	elicit := s.Elicit
	chatter := s.Chatter
	s.mu.Unlock()
	for _, m := range chatter {
		if err := stream.Send(m); err != nil {
			return err
		}
	}
	if elicit != nil {
		if err := stream.Send(&mcpv1.CallToolResponse{Event: &mcpv1.CallToolResponse_Elicitation{Elicitation: elicit}}); err != nil {
			return err
		}
		select {
		case <-s.answered:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if call == nil {
		call = func(r *mcpv1.CallToolRequest) (string, error) {
			b, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": "remote " + r.GetToolName() + " " + r.GetArgumentsJson()}}})
			return string(b), nil
		}
	}
	if err := stream.Send(&mcpv1.CallToolResponse{Event: &mcpv1.CallToolResponse_Progress{Progress: &mcpv1.Progress{Progress: 1}}}); err != nil {
		return err
	}
	res, err := call(req.Msg)
	if err != nil {
		return err
	}
	return stream.Send(&mcpv1.CallToolResponse{Event: &mcpv1.CallToolResponse_Result{Result: &mcpv1.CallToolResult{ResultJson: res}}})
}

// RespondToElicitation implements McpServiceHandler.
func (s *Service) RespondToElicitation(_ context.Context, req *connect.Request[mcpv1.RespondToElicitationRequest]) (*connect.Response[mcpv1.RespondToElicitationResponse], error) {
	s.mu.Lock()
	s.Answers = append(s.Answers, req.Msg)
	s.mu.Unlock()
	s.answered <- struct{}{}
	return connect.NewResponse(&mcpv1.RespondToElicitationResponse{}), nil
}

// GetServer implements McpServiceHandler.
func (s *Service) GetServer(_ context.Context, req *connect.Request[mcpv1.GetServerRequest]) (*connect.Response[mcpv1.GetServerResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sv := range s.Servers {
		if sv.GetId() == req.Msg.GetServerId() {
			return connect.NewResponse(&mcpv1.GetServerResponse{Server: sv}), nil
		}
	}
	return nil, rpc.NewError(connect.CodeNotFound, commonv1.ErrorType_ERROR_TYPE_NOT_FOUND, "MCP server not found", 0)
}

// DeleteServer implements McpServiceHandler.
func (s *Service) DeleteServer(_ context.Context, req *connect.Request[mcpv1.DeleteServerRequest]) (*connect.Response[mcpv1.DeleteServerResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Deleted = append(s.Deleted, req.Msg.GetServerId())
	return connect.NewResponse(&mcpv1.DeleteServerResponse{}), nil
}

// GetServerInfo implements McpServiceHandler.
func (s *Service) GetServerInfo(_ context.Context, req *connect.Request[mcpv1.GetServerInfoRequest]) (*connect.Response[mcpv1.GetServerInfoResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.InfoError != nil {
		return nil, s.InfoError
	}
	if i := s.Info[req.Msg.GetServerId()]; i != nil {
		return connect.NewResponse(i), nil
	}
	return connect.NewResponse(&mcpv1.GetServerInfoResponse{Name: "upstream", HasTools: true}), nil
}

// ListTools implements McpServiceHandler.
func (s *Service) ListTools(_ context.Context, req *connect.Request[mcpv1.ListToolsRequest]) (*connect.Response[mcpv1.ListToolsResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return connect.NewResponse(&mcpv1.ListToolsResponse{Tools: s.Tools[req.Msg.GetServerId()]}), nil
}

// ListPrompts implements McpServiceHandler.
func (s *Service) ListPrompts(_ context.Context, req *connect.Request[mcpv1.ListPromptsRequest]) (*connect.Response[mcpv1.ListPromptsResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return connect.NewResponse(&mcpv1.ListPromptsResponse{Prompts: s.Prompts[req.Msg.GetServerId()]}), nil
}

// ListResources implements McpServiceHandler.
func (s *Service) ListResources(_ context.Context, req *connect.Request[mcpv1.ListResourcesRequest]) (*connect.Response[mcpv1.ListResourcesResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return connect.NewResponse(&mcpv1.ListResourcesResponse{Resources: s.Resources[req.Msg.GetServerId()]}), nil
}

// GetPrompt implements McpServiceHandler.
func (s *Service) GetPrompt(_ context.Context, _ *connect.Request[mcpv1.GetPromptRequest]) (*connect.Response[mcpv1.GetPromptResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return connect.NewResponse(&mcpv1.GetPromptResponse{ResultJson: s.PromptResult}), nil
}

// ReadResource implements McpServiceHandler.
func (s *Service) ReadResource(_ context.Context, _ *connect.Request[mcpv1.ReadResourceRequest]) (*connect.Response[mcpv1.ReadResourceResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return connect.NewResponse(&mcpv1.ReadResourceResponse{ResultJson: s.ResourceResult}), nil
}

// NeedsAuthError is the error core returns for a server that needs authorization.
func NeedsAuthError() error {
	return rpc.NewError(connect.CodeFailedPrecondition, commonv1.ErrorType_ERROR_TYPE_NEEDS_AUTH, "MCP server needs authorization", 0)
}
