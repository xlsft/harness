package server

import (
	"context"
	"errors"
	"path/filepath"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/browser"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
)

// The session's browser in the user's real Chrome, and recordings (real_browser.proto). The rules that keep the agent to
// the sites the user allowed live in package browser; here are the RPCs and the session events. Grants are device-local.

var _ agentv1connect.RealBrowserServiceHandler = (*realBrowserService)(nil)

type realBrowserService struct {
	agentv1connect.UnimplementedRealBrowserServiceHandler
	s *Server
}

// onBrowserSite asks the session's viewers about a site.
func (s *Server) onBrowserSite(sessionID string, r browser.SiteRequest) {
	s.mu.Lock()
	ls := s.live[sessionID]
	s.mu.Unlock()
	if ls == nil {
		return
	}
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_BrowserSiteRequested{BrowserSiteRequested: &agentv1.BrowserSiteRequested{RequestId: r.ID, Site: r.Site, Url: r.URL}}})
}

func (r *realBrowserService) GetRealChrome(ctx context.Context, req *connect.Request[agentv1.GetRealChromeRequest]) (*connect.Response[agentv1.GetRealChromeResponse], error) {
	m := r.s.browsers()
	out := &agentv1.GetRealChromeResponse{Available: m.RealChromeAvailable(ctx), AllowedSites: m.Grants().List()}
	if id := req.Msg.GetSessionId(); id != "" {
		if b := m.Lookup(id); b != nil {
			out.InUse = b.Attached()
		}
	}
	return connect.NewResponse(out), nil
}

func (r *realBrowserService) UseRealChrome(ctx context.Context, req *connect.Request[agentv1.UseRealChromeRequest]) (*connect.Response[agentv1.UseRealChromeResponse], error) {
	b, err := r.s.sessionBrowser(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	if err := b.UseRealChrome(ctx, req.Msg.GetOn()); err != nil {
		if errors.Is(err, browser.ErrRealChromeNotFound) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return nil, err
	}
	return connect.NewResponse(&agentv1.UseRealChromeResponse{}), nil
}

func (r *realBrowserService) RespondToBrowserSite(_ context.Context, req *connect.Request[agentv1.RespondToBrowserSiteRequest]) (*connect.Response[agentv1.RespondToBrowserSiteResponse], error) {
	b, err := r.s.sessionBrowser(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	decision := browser.SiteDeny
	switch req.Msg.GetDecision() {
	case agentv1.BrowserSiteDecision_BROWSER_SITE_DECISION_ALLOW_SESSION:
		decision = browser.SiteAllowSession
	case agentv1.BrowserSiteDecision_BROWSER_SITE_DECISION_ALLOW_ALWAYS:
		decision = browser.SiteAllowAlways
	}
	if !b.AnswerSite(req.Msg.GetRequestId(), decision) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("nothing waits for this answer"))
	}
	r.s.mu.Lock()
	ls := r.s.live[req.Msg.GetSessionId()]
	r.s.mu.Unlock()
	if ls != nil {
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_BrowserSiteResolved{BrowserSiteResolved: &agentv1.BrowserSiteResolved{RequestId: req.Msg.GetRequestId(), Decision: req.Msg.GetDecision()}}})
	}
	return connect.NewResponse(&agentv1.RespondToBrowserSiteResponse{}), nil
}

func (r *realBrowserService) RevokeBrowserSite(_ context.Context, req *connect.Request[agentv1.RevokeBrowserSiteRequest]) (*connect.Response[agentv1.RevokeBrowserSiteResponse], error) {
	g := r.s.browsers().Grants()
	if err := g.Remove(req.Msg.GetSite()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.RevokeBrowserSiteResponse{AllowedSites: g.List()}), nil
}

func (r *realBrowserService) RecordBrowser(ctx context.Context, req *connect.Request[agentv1.RecordBrowserRequest]) (*connect.Response[agentv1.RecordBrowserResponse], error) {
	b, err := r.s.sessionBrowser(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetOn() {
		if err := b.StartRecording(ctx); err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return connect.NewResponse(&agentv1.RecordBrowserResponse{}), nil
	}
	rec, err := b.StopRecording(filepath.Join(config.Home(), "browser", "recordings", req.Msg.GetSessionId()))
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&agentv1.RecordBrowserResponse{Path: rec.Path, SizeBytes: rec.Size, Frames: int32(rec.Frames)}), nil
}
