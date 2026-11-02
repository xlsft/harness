package fakegateway

import (
	"context"
	"sync"

	"connectrpc.com/connect"

	decisionv1 "github.com/xlsft/harness/packages/client/gen/harness/decision/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/decision/v1/decisionv1connect"
)

// DecisionScript answers DecisionService.Decide; nil answers ASK from provider "none".
type DecisionScript func(*decisionv1.DecideRequest) *decisionv1.DecideResponse

type decisionState struct {
	mu     sync.Mutex
	script DecisionScript
	reqs   []*decisionv1.DecideRequest
}

// SetDecide scripts DecisionService.Decide.
func (s *Server) SetDecide(f DecisionScript) {
	s.decision.mu.Lock()
	s.decision.script = f
	s.decision.mu.Unlock()
}

// DecideRequests returns the recorded Decide requests.
func (s *Server) DecideRequests() []*decisionv1.DecideRequest {
	s.decision.mu.Lock()
	defer s.decision.mu.Unlock()
	return append([]*decisionv1.DecideRequest(nil), s.decision.reqs...)
}

type decisionHandler struct {
	decisionv1connect.UnimplementedDecisionServiceHandler
	s *Server
}

func (h decisionHandler) Decide(_ context.Context, req *connect.Request[decisionv1.DecideRequest]) (*connect.Response[decisionv1.DecideResponse], error) {
	if err := h.s.checkAuth(req.Header()); err != nil {
		return nil, err
	}
	d := &h.s.decision
	d.mu.Lock()
	d.reqs = append(d.reqs, req.Msg)
	script := d.script
	d.mu.Unlock()
	if script == nil {
		return connect.NewResponse(&decisionv1.DecideResponse{Decision: decisionv1.Decision_DECISION_ASK, Provider: "none", Reason: "No provider decided."}), nil
	}
	return connect.NewResponse(script(req.Msg)), nil
}

func (h decisionHandler) ListProviders(_ context.Context, req *connect.Request[decisionv1.ListProvidersRequest]) (*connect.Response[decisionv1.ListProvidersResponse], error) {
	if err := h.s.checkAuth(req.Header()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&decisionv1.ListProvidersResponse{Providers: []*decisionv1.Provider{
		{Id: "rules", Type: "rules", DisplayName: "Rules", Healthy: true},
	}}), nil
}
