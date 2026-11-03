package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/llm/v1/llmv1connect"
)

type fakeLimits struct {
	llmv1connect.UnimplementedProviderLimitsServiceHandler
	auth []string
	last *llmv1.GetProviderLimitsRequest
}

func (f *fakeLimits) GetProviderLimits(_ context.Context, req *connect.Request[llmv1.GetProviderLimitsRequest]) (*connect.Response[llmv1.GetProviderLimitsResponse], error) {
	f.auth = append(f.auth, req.Header().Get("Authorization"))
	if req.Header().Get("Authorization") != "Bearer hat_fresh" {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}
	f.last = req.Msg
	balance := 12.4
	or := &llmv1.ProviderLimits{
		ProviderId: "openrouter",
		Balance:    &llmv1.ProviderBalance{Currency: "USD", Balance: &balance, Label: "key limit"},
		Windows:    []*llmv1.LimitWindow{{Label: "requests", UsedPercent: 10}},
	}
	return connect.NewResponse(&llmv1.GetProviderLimitsResponse{Providers: []*llmv1.ProviderLimits{or}, Model: or}), nil
}

func TestGetProviderLimitsForwardsToCoreWithServerCredentials(t *testing.T) {
	fake := &fakeLimits{}
	mux := http.NewServeMux()
	mux.Handle(llmv1connect.NewProviderLimitsServiceHandler(fake))
	core := httptest.NewServer(mux)
	defer core.Close()

	tokens := &proxyTokens{token: "hat_stale"}
	s := New(bootstrap.Options{})
	defer s.Close()
	s.Core = &CoreProxy{URL: core.URL, HTTP: core.Client(), Tokens: tokens}

	res, err := s.GetProviderLimits(context.Background(), connect.NewRequest(&agentv1.GetProviderLimitsRequest{ModelId: "or-model", ProviderIds: []string{"openrouter"}}))
	if err != nil {
		t.Fatal(err)
	}
	if tokens.refreshed != 1 || len(fake.auth) != 2 {
		t.Fatalf("want one refresh and a retry, got refreshed=%d calls=%v", tokens.refreshed, fake.auth)
	}
	if fake.last.GetModelId() != "or-model" || len(fake.last.GetProviderIds()) != 1 {
		t.Fatalf("request not forwarded: %v", fake.last)
	}
	if len(res.Msg.GetProviders()) != 1 || res.Msg.GetModel().GetBalance().GetBalance() != 12.4 || !proto.Equal(res.Msg.GetModel(), res.Msg.GetProviders()[0]) {
		t.Fatalf("bad response: %v", res.Msg)
	}
}

func TestGetProviderLimitsWithoutServer(t *testing.T) {
	s := New(bootstrap.Options{})
	defer s.Close()
	_, err := s.GetProviderLimits(context.Background(), connect.NewRequest(&agentv1.GetProviderLimitsRequest{}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("want UNAVAILABLE, got %v", err)
	}
}

func TestGetProviderLimitsPassesServerErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(llmv1connect.NewProviderLimitsServiceHandler(llmv1connect.UnimplementedProviderLimitsServiceHandler{}))
	core := httptest.NewServer(mux)
	defer core.Close()
	s := New(bootstrap.Options{})
	defer s.Close()
	s.Core = &CoreProxy{URL: core.URL, HTTP: core.Client(), Tokens: &proxyTokens{token: "hat_fresh"}}
	_, err := s.GetProviderLimits(context.Background(), connect.NewRequest(&agentv1.GetProviderLimitsRequest{}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("want UNIMPLEMENTED, got %v", err)
	}
}
