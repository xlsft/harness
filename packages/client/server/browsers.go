package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/browser"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// Browsers (browser.proto): a headless Chromium per harness session, shared by the Browser pane and the agent's
// Browser* tools. The RPCs manage it; the live view goes over the WebSocket of browser.Manager.SocketHandler at
// browser.SocketPath, authenticated by the one-time tickets OpenBrowser returns (TCPGuard lets that path through
// without the bearer token). The agent's actions are published as BrowserAction session events.

// browsers returns the client's browser manager (created on first use).
func (s *Server) browsers() *browser.Manager {
	s.browserOnce.Do(func() {
		if s.Browsers == nil {
			s.Browsers = &browser.Manager{Dir: filepath.Join(config.Home(), "browser"), Logger: s.log()}
		}
		if s.Browsers.OnAction == nil {
			s.Browsers.OnAction = s.onBrowserAction
		}
		if s.Browsers.OnSiteRequest == nil {
			s.Browsers.OnSiteRequest = s.onBrowserSite
		}
	})
	return s.Browsers
}

// registerBrowserSocket serves the browser WebSocket on mux; requests with an Origin header must come from
// origins.
func (s *Server) registerBrowserSocket(mux *http.ServeMux, origins []string) {
	mux.Handle(browser.SocketPath, s.browsers().SocketHandler(origins))
}

// onBrowserAction publishes the agent's browser action to the session's subscribers.
func (s *Server) onBrowserAction(sessionID string, a *agentv1.BrowserAction) {
	s.mu.Lock()
	ls := s.live[sessionID]
	s.mu.Unlock()
	if ls != nil {
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_BrowserAction{BrowserAction: a}})
	}
}

// sessionBrowser returns the browser of a live harness session of this machine.
func (s *Server) sessionBrowser(id string) (*browser.Browser, error) {
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id is required"))
	}
	s.mu.Lock()
	ls := s.live[id]
	s.mu.Unlock()
	if ls == nil {
		if _, _, ok := s.acpScope(id); ok {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s runs on an ACP agent; the browser is only available to harness sessions", id))
		}
		if remote := s.remoteInfo(id); remote != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s runs on %s; the browser is only available for sessions of this machine", id, deviceLabel(remote)))
		}
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s is not open; call CreateSession first", id))
	}
	b := s.browsers().Lookup(id)
	if b == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s has no browser (HARNESS_NO_BROWSER is set)", id))
	}
	return b, nil
}

func browserError(err error) error {
	var ce *connect.Error
	switch {
	case errors.As(err, &ce):
		return err
	case errors.Is(err, browser.ErrNoBrowser):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, browser.ErrClosed):
		return connect.NewError(connect.CodeUnavailable, err)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func (s *Server) GetBrowserAvailability(context.Context, *connect.Request[agentv1.GetBrowserAvailabilityRequest]) (*connect.Response[agentv1.GetBrowserAvailabilityResponse], error) {
	return connect.NewResponse(&agentv1.GetBrowserAvailabilityResponse{Availability: s.browsers().Availability()}), nil
}

func (s *Server) InstallBrowser(ctx context.Context, _ *connect.Request[agentv1.InstallBrowserRequest], stream *connect.ServerStream[agentv1.InstallBrowserResponse]) error {
	m := s.browsers()
	// The download belongs to the client, not to this request: it continues when the stream goes away.
	d, err := m.Install(s.ctx)
	if err != nil {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return connect.NewError(connect.CodeCanceled, ctx.Err())
		case <-d.Done():
			if err := d.Err(); err != nil {
				return connect.NewError(connect.CodeUnavailable, err)
			}
			n, total := d.Progress()
			return stream.Send(&agentv1.InstallBrowserResponse{DownloadedBytes: n, TotalBytes: total, Done: true, Availability: m.Availability()})
		case <-tick.C:
			n, total := d.Progress()
			if err := stream.Send(&agentv1.InstallBrowserResponse{DownloadedBytes: n, TotalBytes: total}); err != nil {
				return err
			}
		}
	}
}

func (s *Server) OpenBrowser(ctx context.Context, req *connect.Request[agentv1.OpenBrowserRequest]) (*connect.Response[agentv1.OpenBrowserResponse], error) {
	m := req.Msg
	b, err := s.sessionBrowser(m.GetSessionId())
	if err != nil {
		return nil, err
	}
	var vp *browser.Viewport
	if m.Viewport != nil {
		v, err := browser.ViewportFromProto(m.Viewport)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		vp = &v
	}
	u := ""
	if m.GetUrl() != "" {
		if u, err = browser.NormalizeURL(m.GetUrl()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	if err := b.Ensure(ctx); err != nil {
		return nil, browserError(err)
	}
	if vp != nil {
		if err := b.SetViewport(ctx, *vp); err != nil {
			return nil, browserError(err)
		}
	}
	if u != "" {
		if _, err := b.NewTab(ctx, u); err != nil {
			return nil, browserError(err)
		}
	}
	ticket, err := s.browsers().IssueTicket(m.GetSessionId())
	if err != nil {
		return nil, browserError(err)
	}
	return connect.NewResponse(&agentv1.OpenBrowserResponse{State: b.State(), Ticket: ticket}), nil
}

func (s *Server) ListBrowserTabs(ctx context.Context, req *connect.Request[agentv1.ListBrowserTabsRequest]) (*connect.Response[agentv1.ListBrowserTabsResponse], error) {
	b, err := s.sessionBrowser(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.ListBrowserTabsResponse{State: b.State()}), nil
}

func (s *Server) CloseBrowser(ctx context.Context, req *connect.Request[agentv1.CloseBrowserRequest]) (*connect.Response[agentv1.CloseBrowserResponse], error) {
	b, err := s.sessionBrowser(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	b.Stop("closed")
	return connect.NewResponse(&agentv1.CloseBrowserResponse{}), nil
}

func (s *Server) ClearBrowserData(ctx context.Context, req *connect.Request[agentv1.ClearBrowserDataRequest]) (*connect.Response[agentv1.ClearBrowserDataResponse], error) {
	b, err := s.sessionBrowser(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	if err := b.ClearData(ctx); err != nil {
		return nil, browserError(err)
	}
	return connect.NewResponse(&agentv1.ClearBrowserDataResponse{}), nil
}
