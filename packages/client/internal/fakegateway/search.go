package fakegateway

import (
	"context"
	"sync"

	"connectrpc.com/connect"

	searchv1 "github.com/xlsft/harness/packages/client/gen/harness/search/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/search/v1/searchv1connect"
)

// SearchScript answers SearchService.Search; a nil script makes Search fail
// with UNIMPLEMENTED (a server without a search service).
type SearchScript func(*searchv1.SearchRequest) (*searchv1.SearchResponse, error)

type searchState struct {
	mu     sync.Mutex
	script SearchScript
	reqs   []*searchv1.SearchRequest
}

// SetSearch scripts SearchService.Search.
func (s *Server) SetSearch(f SearchScript) {
	s.search.mu.Lock()
	s.search.script = f
	s.search.mu.Unlock()
}

// SearchRequests returns the recorded Search requests.
func (s *Server) SearchRequests() []*searchv1.SearchRequest {
	s.search.mu.Lock()
	defer s.search.mu.Unlock()
	return append([]*searchv1.SearchRequest(nil), s.search.reqs...)
}

type searchHandler struct {
	searchv1connect.UnimplementedSearchServiceHandler
	s *Server
}

func (h searchHandler) Search(_ context.Context, req *connect.Request[searchv1.SearchRequest]) (*connect.Response[searchv1.SearchResponse], error) {
	if err := h.s.checkAuth(req.Header()); err != nil {
		return nil, err
	}
	st := &h.s.search
	st.mu.Lock()
	st.reqs = append(st.reqs, req.Msg)
	script := st.script
	st.mu.Unlock()
	if script == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, nil)
	}
	resp, err := script(req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
