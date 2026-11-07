package server

import (
	"context"
	"errors"
	"sort"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	libraryv1 "github.com/xlsft/harness/packages/client/gen/harness/library/v1"
	"github.com/xlsft/harness/packages/client/tracker"
	_ "github.com/xlsft/harness/packages/client/tracker/youtrack" // registers the provider
)

// Trackers on this device (trackers.proto). The work is in package tracker.

const trackerCallTimeout = 60 * time.Second

type localTrackerService struct {
	agentv1connect.UnimplementedLocalTrackerServiceHandler
	s *Server
}

var _ agentv1connect.LocalTrackerServiceHandler = (*localTrackerService)(nil)

// trackers is this device's tracker service, made on first use.
func (s *Server) trackers() *tracker.Service {
	s.trackersOnce.Do(func() {
		if s.Trackers == nil {
			s.Trackers = tracker.New(config.Home(), nil)
		}
	})
	return s.Trackers
}

func trackerConnectError(err error) error {
	switch {
	case errors.Is(err, tracker.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, tracker.ErrAuth), errors.Is(err, tracker.ErrForbidden):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, tracker.ErrTransient):
		return connect.NewError(connect.CodeUnavailable, err)
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	return connect.NewError(connect.CodeInvalidArgument, err)
}

func (l *localTrackerService) ListLocalTrackerConnections(context.Context, *connect.Request[agentv1.ListLocalTrackerConnectionsRequest]) (*connect.Response[agentv1.ListLocalTrackerConnectionsResponse], error) {
	t := l.s.trackers()
	list, err := t.Store.List()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	res := &agentv1.ListLocalTrackerConnectionsResponse{Providers: tracker.CapabilitiesProto()}
	for _, c := range list {
		res.Connections = append(res.Connections, t.ConnectionProto(c))
	}
	return connect.NewResponse(res), nil
}

func (l *localTrackerService) SaveLocalTrackerConnection(_ context.Context, req *connect.Request[agentv1.SaveLocalTrackerConnectionRequest]) (*connect.Response[agentv1.SaveLocalTrackerConnectionResponse], error) {
	if req.Msg.GetConnection() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("connection is required"))
	}
	t := l.s.trackers()
	c, err := t.Store.Save(tracker.InputFromProto(req.Msg.GetConnection()))
	if err != nil {
		return nil, trackerConnectError(err)
	}
	return connect.NewResponse(&agentv1.SaveLocalTrackerConnectionResponse{Connection: t.ConnectionProto(c)}), nil
}

func (l *localTrackerService) DeleteLocalTrackerConnection(ctx context.Context, req *connect.Request[agentv1.DeleteLocalTrackerConnectionRequest]) (*connect.Response[agentv1.DeleteLocalTrackerConnectionResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, trackerCallTimeout)
	defer cancel()
	if err := l.s.trackers().Forget(ctx, req.Msg.GetId()); err != nil {
		return nil, trackerConnectError(err)
	}
	return connect.NewResponse(&agentv1.DeleteLocalTrackerConnectionResponse{}), nil
}

func (l *localTrackerService) TestLocalTrackerConnection(ctx context.Context, req *connect.Request[agentv1.TestLocalTrackerConnectionRequest]) (*connect.Response[agentv1.TestLocalTrackerConnectionResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, trackerCallTimeout)
	defer cancel()
	t := l.s.trackers()
	var me tracker.Identity
	var err error
	if id := req.Msg.GetId(); id != "" {
		var c tracker.Connection
		if c, err = t.Store.Get(id); err != nil {
			return nil, trackerConnectError(err)
		}
		var p tracker.Provider
		if p, err = t.Store.Open(c); err == nil {
			me, err = p.Test(ctx)
		}
		t.Store.Record(id, err, time.Now())
	} else if d := req.Msg.GetDraft(); d != nil {
		me, err = tracker.TestDraft(ctx, tracker.InputFromProto(d))
	} else {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("pass a connection to test"))
	}
	if err != nil {
		return connect.NewResponse(&agentv1.TestLocalTrackerConnectionResponse{Error: err.Error()}), nil
	}
	return connect.NewResponse(&agentv1.TestLocalTrackerConnectionResponse{Ok: true, Login: me.Login, DisplayName: me.DisplayName}), nil
}

func (l *localTrackerService) SearchLocalTrackerIssues(ctx context.Context, req *connect.Request[agentv1.SearchLocalTrackerIssuesRequest]) (*connect.Response[agentv1.SearchLocalTrackerIssuesResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, trackerCallTimeout)
	defer cancel()
	_, p, err := l.s.trackers().Store.OpenByID(req.Msg.GetConnectionId())
	if err != nil {
		return nil, trackerConnectError(err)
	}
	limit := int(req.Msg.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	issues, more, err := p.Search(ctx, req.Msg.GetQuery(), tracker.Page{Limit: min(limit, 50)})
	if err != nil {
		return nil, trackerConnectError(err)
	}
	res := &agentv1.SearchLocalTrackerIssuesResponse{HasMore: more}
	for i := range issues {
		res.Issues = append(res.Issues, tracker.IssueProto(&issues[i]))
	}
	return connect.NewResponse(res), nil
}

func (l *localTrackerService) GetLocalTrackerIssue(ctx context.Context, req *connect.Request[agentv1.GetLocalTrackerIssueRequest]) (*connect.Response[agentv1.GetLocalTrackerIssueResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, trackerCallTimeout)
	defer cancel()
	i, err := l.s.trackers().GetIssue(ctx, req.Msg.GetConnectionId(), req.Msg.GetId())
	if err != nil {
		return nil, trackerConnectError(err)
	}
	return connect.NewResponse(&agentv1.GetLocalTrackerIssueResponse{Issue: i}), nil
}

func (l *localTrackerService) ListLocalTrackerFields(ctx context.Context, req *connect.Request[agentv1.ListLocalTrackerFieldsRequest]) (*connect.Response[agentv1.ListLocalTrackerFieldsResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, trackerCallTimeout)
	defer cancel()
	_, p, err := l.s.trackers().Store.OpenByID(req.Msg.GetConnectionId())
	if err != nil {
		return nil, trackerConnectError(err)
	}
	fields, err := p.Fields(ctx, req.Msg.GetProject())
	if err != nil {
		return nil, trackerConnectError(err)
	}
	res := &agentv1.ListLocalTrackerFieldsResponse{}
	for _, f := range fields {
		res.Fields = append(res.Fields, &libraryv1.TrackerField{Name: f.Name, Kind: f.Kind, Multi: f.Multi, Values: f.Values})
	}
	return connect.NewResponse(res), nil
}

func (l *localTrackerService) ListLocalProviderProjects(ctx context.Context, req *connect.Request[agentv1.ListLocalProviderProjectsRequest]) (*connect.Response[agentv1.ListLocalProviderProjectsResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, trackerCallTimeout)
	defer cancel()
	list, err := l.s.trackers().ProjectsProto(ctx, req.Msg.GetConnectionId())
	if err != nil {
		return nil, trackerConnectError(err)
	}
	return connect.NewResponse(&agentv1.ListLocalProviderProjectsResponse{Projects: list}), nil
}

func (l *localTrackerService) StartLocalTrackerOAuth(ctx context.Context, req *connect.Request[agentv1.StartLocalTrackerOAuthRequest]) (*connect.Response[agentv1.StartLocalTrackerOAuthResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, trackerCallTimeout)
	defer cancel()
	id, u, err := l.s.trackers().StartOAuth(ctx, req.Msg.GetConnectionId())
	if err != nil {
		return nil, trackerConnectError(err)
	}
	return connect.NewResponse(&agentv1.StartLocalTrackerOAuthResponse{FlowId: id, AuthorizeUrl: u}), nil
}

func (l *localTrackerService) GetLocalTrackerOAuth(_ context.Context, req *connect.Request[agentv1.GetLocalTrackerOAuthRequest]) (*connect.Response[agentv1.GetLocalTrackerOAuthResponse], error) {
	done, msg, err := l.s.trackers().OAuthStatus(req.Msg.GetFlowId())
	if err != nil {
		return nil, trackerConnectError(err)
	}
	return connect.NewResponse(&agentv1.GetLocalTrackerOAuthResponse{Done: done, Error: msg}), nil
}

func (l *localTrackerService) SyncLocalTrackers(ctx context.Context, _ *connect.Request[agentv1.SyncLocalTrackersRequest]) (*connect.Response[agentv1.SyncLocalTrackersResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, trackerCallTimeout)
	defer cancel()
	n, errs := l.s.trackers().SyncOnce(ctx)
	return connect.NewResponse(&agentv1.SyncLocalTrackersResponse{NewCards: int32(n), Errors: errs}), nil
}

func (l *localTrackerService) ListFolderProjects(context.Context, *connect.Request[agentv1.ListFolderProjectsRequest]) (*connect.Response[agentv1.ListFolderProjectsResponse], error) {
	links, err := l.s.trackers().Folder.List()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	res := &agentv1.ListFolderProjectsResponse{}
	for folder, project := range links {
		res.Links = append(res.Links, &agentv1.FolderProject{Folder: folder, ProjectId: project})
	}
	sort.Slice(res.Links, func(i, j int) bool { return res.Links[i].Folder < res.Links[j].Folder })
	return connect.NewResponse(res), nil
}

func (l *localTrackerService) SetFolderProject(_ context.Context, req *connect.Request[agentv1.SetFolderProjectRequest]) (*connect.Response[agentv1.SetFolderProjectResponse], error) {
	if err := l.s.trackers().Folder.Set(req.Msg.GetFolder(), req.Msg.GetProjectId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentv1.SetFolderProjectResponse{}), nil
}

func (l *localTrackerService) PrepareIssueSession(ctx context.Context, req *connect.Request[agentv1.PrepareIssueSessionRequest]) (*connect.Response[agentv1.PrepareIssueSessionResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, trackerCallTimeout)
	defer cancel()
	folder, branch, prompt, err := l.s.trackers().PrepareSession(ctx, req.Msg.GetCardId())
	if err != nil {
		return nil, trackerConnectError(err)
	}
	return connect.NewResponse(&agentv1.PrepareIssueSessionResponse{Folder: folder, Branch: branch, Prompt: prompt, CardId: req.Msg.GetCardId()}), nil
}

func (l *localTrackerService) ListAllKnowledgeSources(ctx context.Context, _ *connect.Request[agentv1.ListAllKnowledgeSourcesRequest]) (*connect.Response[agentv1.ListAllKnowledgeSourcesResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, trackerCallTimeout)
	defer cancel()
	list, err := l.s.trackers().Sources(ctx)
	if err != nil {
		return nil, trackerConnectError(err)
	}
	return connect.NewResponse(&agentv1.ListAllKnowledgeSourcesResponse{Sources: list}), nil
}

func (l *localTrackerService) SearchAllKnowledge(ctx context.Context, req *connect.Request[agentv1.SearchAllKnowledgeRequest]) (*connect.Response[agentv1.SearchAllKnowledgeResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, trackerCallTimeout)
	defer cancel()
	hits, warnings, err := l.s.trackers().Search(ctx, req.Msg.GetQuery(), req.Msg.GetLimit(), commonv1.Scope_SCOPE_UNSPECIFIED, req.Msg.GetSourceIds())
	if err != nil {
		return nil, trackerConnectError(err)
	}
	return connect.NewResponse(&agentv1.SearchAllKnowledgeResponse{Hits: hits, Warnings: warnings}), nil
}

func (l *localTrackerService) BrowseKnowledge(ctx context.Context, req *connect.Request[agentv1.BrowseKnowledgeRequest]) (*connect.Response[agentv1.BrowseKnowledgeResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, trackerCallTimeout)
	defer cancel()
	docs, more, err := l.s.trackers().Browse(ctx, req.Msg.GetSourceId(), req.Msg.GetParentId(), req.Msg.GetLimit(), req.Msg.GetOffset())
	if err != nil {
		return nil, trackerConnectError(err)
	}
	return connect.NewResponse(&agentv1.BrowseKnowledgeResponse{Documents: docs, HasMore: more}), nil
}

func (l *localTrackerService) ReadKnowledge(ctx context.Context, req *connect.Request[agentv1.ReadKnowledgeRequest]) (*connect.Response[agentv1.ReadKnowledgeResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, trackerCallTimeout)
	defer cancel()
	doc, content, err := l.s.trackers().Read(ctx, req.Msg.GetDocumentId())
	if err != nil {
		return nil, trackerConnectError(err)
	}
	return connect.NewResponse(&agentv1.ReadKnowledgeResponse{Document: doc, Content: content}), nil
}
