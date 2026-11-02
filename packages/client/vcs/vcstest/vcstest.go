// Package vcstest is an in-memory harness.vcs.v1.VcsService for tests of the device providers: it
// records ReportDeviceProviders and ReportDeviceSnapshot calls, checks the bearer token, validates
// snapshots the way the server contract requires, and can inject failures.
package vcstest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/vcs/v1/vcsv1connect"
)

// Server is the fake. Create it with New.
type Server struct {
	vcsv1connect.UnimplementedVcsServiceHandler

	// Token, when set, is the required bearer token.
	Token string

	mu        sync.Mutex
	providers []*vcsv1.ReportDeviceProvidersRequest
	snapshots []*vcsv1.ReportDeviceSnapshotRequest
	failNext  error
	// Unimplemented makes both RPCs return UNIMPLEMENTED (an older server).
	unimplemented bool
	notify        chan struct{}

	HTTP *httptest.Server
}

// New starts the fake on an httptest server.
func New() *Server {
	s := &Server{notify: make(chan struct{}, 64)}
	mux := http.NewServeMux()
	path, h := vcsv1connect.NewVcsServiceHandler(s)
	mux.Handle(path, h)
	s.HTTP = httptest.NewServer(mux)
	return s
}

// URL is the base URL of the fake.
func (s *Server) URL() string { return s.HTTP.URL }

// Close stops the fake.
func (s *Server) Close() { s.HTTP.Close() }

// FailNext makes the next device RPC fail with err.
func (s *Server) FailNext(err error) {
	s.mu.Lock()
	s.failNext = err
	s.mu.Unlock()
}

// SetUnimplemented toggles UNIMPLEMENTED for the device RPCs.
func (s *Server) SetUnimplemented(v bool) {
	s.mu.Lock()
	s.unimplemented = v
	s.mu.Unlock()
}

// Providers returns the recorded ReportDeviceProviders requests.
func (s *Server) Providers() []*vcsv1.ReportDeviceProvidersRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*vcsv1.ReportDeviceProvidersRequest(nil), s.providers...)
}

// Snapshots returns the recorded ReportDeviceSnapshot requests.
func (s *Server) Snapshots() []*vcsv1.ReportDeviceSnapshotRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*vcsv1.ReportDeviceSnapshotRequest(nil), s.snapshots...)
}

// WaitSnapshots blocks until at least n snapshots were recorded or the timeout passes.
func (s *Server) WaitSnapshots(n int, timeout time.Duration) []*vcsv1.ReportDeviceSnapshotRequest {
	deadline := time.After(timeout)
	for {
		if got := s.Snapshots(); len(got) >= n {
			return got
		}
		select {
		case <-s.notify:
		case <-deadline:
			return s.Snapshots()
		}
	}
}

func (s *Server) check(h http.Header) error {
	if s.Token != "" && h.Get("Authorization") != "Bearer "+s.Token {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("bad token"))
	}
	if s.unimplemented {
		return connect.NewError(connect.CodeUnimplemented, errors.New("not implemented"))
	}
	if err := s.failNext; err != nil {
		s.failNext = nil
		return err
	}
	return nil
}

func (s *Server) ReportDeviceProviders(_ context.Context, req *connect.Request[vcsv1.ReportDeviceProvidersRequest]) (*connect.Response[vcsv1.ReportDeviceProvidersResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(req.Header()); err != nil {
		return nil, err
	}
	for _, p := range req.Msg.GetProviders() {
		if t := p.GetType(); t != vcsv1.ProviderType_PROVIDER_TYPE_GH && t != vcsv1.ProviderType_PROVIDER_TYPE_GLAB {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("not a device provider: %v", t))
		}
	}
	s.providers = append(s.providers, proto.Clone(req.Msg).(*vcsv1.ReportDeviceProvidersRequest))
	return connect.NewResponse(&vcsv1.ReportDeviceProvidersResponse{}), nil
}

func (s *Server) ReportDeviceSnapshot(_ context.Context, req *connect.Request[vcsv1.ReportDeviceSnapshotRequest]) (*connect.Response[vcsv1.ReportDeviceSnapshotResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(req.Header()); err != nil {
		return nil, err
	}
	if err := validate(req.Msg); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	events := int32(0)
	if n := len(s.snapshots); n == 0 || !sameState(s.snapshots[n-1], req.Msg) {
		events = 1
	}
	s.snapshots = append(s.snapshots, proto.Clone(req.Msg).(*vcsv1.ReportDeviceSnapshotRequest))
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return connect.NewResponse(&vcsv1.ReportDeviceSnapshotResponse{EventsEmitted: events}), nil
}

// validate applies the contract: a device provider type, RFC 3339 observed_at, and checks keyed by
// "repository_key#number" of a reported PR.
func validate(m *vcsv1.ReportDeviceSnapshotRequest) error {
	if t := m.GetProvider(); t != vcsv1.ProviderType_PROVIDER_TYPE_GH && t != vcsv1.ProviderType_PROVIDER_TYPE_GLAB {
		return fmt.Errorf("not a device provider: %v", t)
	}
	if _, err := time.Parse(time.RFC3339, m.GetObservedAt()); err != nil || !strings.HasSuffix(m.GetObservedAt(), "Z") {
		return fmt.Errorf("observed_at %q is not RFC 3339 UTC", m.GetObservedAt())
	}
	keys := map[string]bool{}
	for _, pr := range m.GetPullRequests() {
		if pr.GetRepositoryKey() == "" || pr.GetNumber() <= 0 {
			return fmt.Errorf("pull request without repository_key/number: %v", pr)
		}
		if pr.GetRepositoryKey() != strings.ToLower(pr.GetRepositoryKey()) {
			return fmt.Errorf("repository_key %q is not lowercased", pr.GetRepositoryKey())
		}
		keys[fmt.Sprintf("%s#%d", pr.GetRepositoryKey(), pr.GetNumber())] = true
	}
	for k := range m.GetChecks() {
		if !keys[k] {
			return fmt.Errorf("checks key %q does not match a reported pull request", k)
		}
	}
	return nil
}

func sameState(a, b *vcsv1.ReportDeviceSnapshotRequest) bool {
	a2 := proto.Clone(a).(*vcsv1.ReportDeviceSnapshotRequest)
	b2 := proto.Clone(b).(*vcsv1.ReportDeviceSnapshotRequest)
	a2.ObservedAt, b2.ObservedAt = "", ""
	return proto.Equal(a2, b2)
}
