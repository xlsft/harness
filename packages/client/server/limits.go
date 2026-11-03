package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/llm/v1/llmv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
)

// GetProviderLimits forwards to the server's harness.llm.v1.ProviderLimitsService with this client's
// credentials (one refresh-and-retry on UNAUTHENTICATED). The server filters what the user may see; nothing
// is cached or stored here.
func (s *Server) GetProviderLimits(ctx context.Context, req *connect.Request[agentv1.GetProviderLimitsRequest]) (*connect.Response[agentv1.GetProviderLimitsResponse], error) {
	if s.Core == nil || s.Core.URL == "" || s.Core.Tokens == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("not connected to a harness server"))
	}
	hc := s.Core.HTTP
	if hc == nil {
		hc = rpc.HTTPClient(s.Core.URL)
	}
	client := llmv1connect.NewProviderLimitsServiceClient(hc, strings.TrimRight(s.Core.URL, "/"), rpc.ClientOptions()...)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	call := func(token string) (*connect.Response[llmv1.GetProviderLimitsResponse], error) {
		r := connect.NewRequest(&llmv1.GetProviderLimitsRequest{ProviderIds: req.Msg.GetProviderIds(), ModelId: req.Msg.GetModelId()})
		rpc.SetBearer(r.Header(), token)
		return client.GetProviderLimits(ctx, r)
	}
	// Like rpc.WithAuth, but the server's Connect error reaches the frontend as it is (code and message).
	token, err := s.Core.Tokens.Token(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not signed in to the harness server"))
	}
	res, err := call(token)
	if connect.CodeOf(err) == connect.CodeUnauthenticated {
		if fresh, rerr := s.Core.Tokens.Refresh(ctx); rerr == nil {
			res, err = call(fresh)
		}
	}
	if err != nil {
		if ce := new(connect.Error); errors.As(err, &ce) {
			return nil, connect.NewError(ce.Code(), errors.New(ce.Message()))
		}
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("harness server is unreachable"))
	}
	return connect.NewResponse(&agentv1.GetProviderLimitsResponse{Providers: res.Msg.GetProviders(), Model: res.Msg.GetModel()}), nil
}
