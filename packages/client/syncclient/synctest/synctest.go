// Package synctest is an in-memory harness.sync.v1.SyncService for tests:
// versioned documents with tombstones and CAS, the change feed (ListChanges,
// WatchChanges with heartbeats and resync), and session sync with uuid
// dedup. Failure injection hooks let tests simulate outages.
package synctest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/sync/v1/syncv1connect"
)

type ref struct {
	scope      commonv1.Scope
	kind       syncv1.DocumentKind
	key        string
	projectKey string
}

type entry struct {
	doc  *syncv1.Document  // nil when deleted
	tomb *syncv1.Tombstone // set when deleted
}

func (e *entry) version() int64 {
	if e.doc != nil {
		return e.doc.GetVersion()
	}
	return e.tomb.GetVersion()
}

// Session is the server copy of a session.
type Session struct {
	Meta    *syncv1.SessionMetadata
	Entries []*syncv1.TranscriptEntry
	uuids   map[string]bool
	// Upserts records the update_paths of every UpsertSession call.
	Upserts [][]string
}

// Server is the fake. Zero value is not usable; call New.
type Server struct {
	syncv1connect.UnimplementedSyncServiceHandler

	// Token, when set, is required as the bearer token.
	Token string
	// HeartbeatInterval of WatchChanges (default 20 s).
	HeartbeatInterval time.Duration

	mu sync.Mutex
	// resyncBelow makes cursors older than this version get resync_required.
	resyncBelow int64
	fail        func(procedure string) error
	version     int64
	nextID      int
	entries     map[ref]*entry
	watchers    map[int]chan *syncv1.Change
	watchID     int
	sessions    map[string]*Session
	calls       map[string]int
}

// New creates an empty server.
func New() *Server {
	return &Server{entries: map[ref]*entry{}, watchers: map[int]chan *syncv1.Change{}, sessions: map[string]*Session{}, calls: map[string]int{}}
}

// Start serves the fake over HTTP (Connect, gRPC and gRPC-Web) and returns
// the test server; close it when done.
func (s *Server) Start() *httptest.Server {
	mux := http.NewServeMux()
	mux.Handle(syncv1connect.NewSyncServiceHandler(s))
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.Start()
	return srv
}

// SetResyncBelow makes cursors older than v get resync_required (simulates
// expired tombstones). since_version 0 is always accepted.
func (s *Server) SetResyncBelow(v int64) {
	s.mu.Lock()
	s.resyncBelow = v
	s.mu.Unlock()
}

// SetFail installs a failure hook: when fn returns an error for a procedure
// name ("ListChanges", "WatchChanges", "AppendSessionEvents", ...), that call
// fails. "WatchChanges.heartbeat" is consulted before every heartbeat and
// ends the stream. nil removes the hook.
func (s *Server) SetFail(fn func(procedure string) error) {
	s.mu.Lock()
	s.fail = fn
	s.mu.Unlock()
}

// Calls returns how many times a procedure was called.
func (s *Server) Calls(procedure string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[procedure]
}

func (s *Server) begin(ctx context.Context, h http.Header, procedure string) error {
	s.mu.Lock()
	s.calls[procedure]++
	fail := s.fail
	s.mu.Unlock()
	if s.Token != "" && h.Get("Authorization") != "Bearer "+s.Token {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("bad token"))
	}
	if fail != nil {
		if err := fail(procedure); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func refOfReq(r *syncv1.DocumentRef) ref {
	return ref{r.GetScope(), r.GetKind(), r.GetKey(), r.GetProjectKey()}
}

func hash(content string, files []*syncv1.DocumentFile) string {
	h := sha256.New()
	h.Write([]byte(content))
	fs := append([]*syncv1.DocumentFile(nil), files...)
	sort.Slice(fs, func(i, j int) bool { return fs[i].GetPath() < fs[j].GetPath() })
	for _, f := range fs {
		fmt.Fprintf(h, "\x00%s\x00%v\x00", f.GetPath(), f.GetExecutable())
		h.Write(f.GetContent())
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Seed stores a document unconditionally (bypassing auth and CAS) and
// returns it.
func (s *Server) Seed(r *syncv1.DocumentRef, content string) *syncv1.Document {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := s.put(&syncv1.PutDocumentRequest{Ref: r, Content: content, Source: "seed"})
	return res.GetDocument()
}

// Remove deletes a document by ref (bypassing CAS) and returns the tombstone.
func (s *Server) Remove(r *syncv1.DocumentRef) *syncv1.Tombstone {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[refOfReq(r)]
	if !ok || e.doc == nil {
		return nil
	}
	return s.delete(e)
}

// Document returns the current document for a ref.
func (s *Server) Document(r *syncv1.DocumentRef) *syncv1.Document {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[refOfReq(r)]; ok && e.doc != nil {
		return proto.Clone(e.doc).(*syncv1.Document)
	}
	return nil
}

// Documents returns all live documents ordered by version.
func (s *Server) Documents() []*syncv1.Document {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*syncv1.Document
	for _, e := range s.entries {
		if e.doc != nil {
			out = append(out, proto.Clone(e.doc).(*syncv1.Document))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetVersion() < out[j].GetVersion() })
	return out
}

// Session returns a copy of the server session.
func (s *Server) Session(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[id]
	if !ok {
		return nil
	}
	cp := &Session{Meta: proto.Clone(ss.Meta).(*syncv1.SessionMetadata), Upserts: append([][]string(nil), ss.Upserts...)}
	for _, e := range ss.Entries {
		cp.Entries = append(cp.Entries, proto.Clone(e).(*syncv1.TranscriptEntry))
	}
	return cp
}

func (s *Server) broadcast(ch *syncv1.Change) {
	for _, w := range s.watchers {
		select {
		case w <- ch:
		default: // slow watcher: drop; it will catch up after reconnecting
		}
	}
}

// put implements the CAS write; caller holds mu.
func (s *Server) put(req *syncv1.PutDocumentRequest) *syncv1.BatchPutResult {
	r := req.GetRef()
	if r.GetKind() == syncv1.DocumentKind_DOCUMENT_KIND_UNSPECIFIED || r.GetScope() == commonv1.Scope_SCOPE_UNSPECIFIED || r.GetKey() == "" {
		return &syncv1.BatchPutResult{Result: &syncv1.BatchPutResult_Error{Error: &syncv1.ItemError{Code: "invalid_argument", Message: "ref is incomplete"}}}
	}
	if r.GetKind() == syncv1.DocumentKind_DOCUMENT_KIND_POLICY && r.GetScope() != commonv1.Scope_SCOPE_ORG {
		return &syncv1.BatchPutResult{Result: &syncv1.BatchPutResult_Error{Error: &syncv1.ItemError{Code: "invalid_argument", Message: "policy is org scope only"}}}
	}
	if r.GetKind() == syncv1.DocumentKind_DOCUMENT_KIND_MCP_SERVER {
		return &syncv1.BatchPutResult{Result: &syncv1.BatchPutResult_Error{Error: &syncv1.ItemError{Code: "invalid_argument", Message: "mcp servers are written through McpService"}}}
	}
	k := refOfReq(r)
	e := s.entries[k]
	if req.BaseVersion != nil {
		base := req.GetBaseVersion()
		switch {
		case base == 0 && e != nil && e.doc != nil:
			return conflictResult(e)
		case base != 0 && (e == nil || e.doc == nil || e.doc.GetVersion() != base):
			if e == nil {
				return &syncv1.BatchPutResult{Result: &syncv1.BatchPutResult_Conflict{Conflict: &syncv1.Conflict{}}}
			}
			return conflictResult(e)
		}
	}
	s.version++
	now := time.Now().UTC().Format(time.RFC3339)
	d := &syncv1.Document{
		Scope: r.GetScope(), Kind: r.GetKind(), Key: r.GetKey(), Version: s.version, Content: req.GetContent(),
		Files: req.GetFiles(), ContentHash: hash(req.GetContent(), req.GetFiles()), Source: req.GetSource(),
		TargetRoles: req.GetTargetRoles(), UpdatedAt: now, UpdatedBy: "usr_test", ProjectKey: r.GetProjectKey(),
	}
	if e != nil && e.doc != nil {
		d.Id, d.CreatedAt = e.doc.GetId(), e.doc.GetCreatedAt()
	} else if e != nil && e.tomb != nil {
		d.Id, d.CreatedAt = e.tomb.GetId(), now
	} else {
		s.nextID++
		d.Id, d.CreatedAt = fmt.Sprintf("doc_%d", s.nextID), now
	}
	s.entries[k] = &entry{doc: d}
	s.broadcast(&syncv1.Change{Change: &syncv1.Change_Document{Document: proto.Clone(d).(*syncv1.Document)}})
	return &syncv1.BatchPutResult{Result: &syncv1.BatchPutResult_Document{Document: proto.Clone(d).(*syncv1.Document)}}
}

func conflictResult(e *entry) *syncv1.BatchPutResult {
	c := &syncv1.Conflict{}
	if e.doc != nil {
		c.Current = &syncv1.Conflict_Document{Document: proto.Clone(e.doc).(*syncv1.Document)}
	} else {
		c.Current = &syncv1.Conflict_Tombstone{Tombstone: proto.Clone(e.tomb).(*syncv1.Tombstone)}
	}
	return &syncv1.BatchPutResult{Result: &syncv1.BatchPutResult_Conflict{Conflict: c}}
}

func (s *Server) delete(e *entry) *syncv1.Tombstone {
	s.version++
	d := e.doc
	t := &syncv1.Tombstone{Id: d.GetId(), Scope: d.GetScope(), Kind: d.GetKind(), Key: d.GetKey(), Version: s.version,
		DeletedAt: time.Now().UTC().Format(time.RFC3339), ProjectKey: d.GetProjectKey()}
	e.doc, e.tomb = nil, t
	s.broadcast(&syncv1.Change{Change: &syncv1.Change_Tombstone{Tombstone: proto.Clone(t).(*syncv1.Tombstone)}})
	return proto.Clone(t).(*syncv1.Tombstone)
}

// changesSince returns the feed after since, ordered by version; caller holds mu.
func (s *Server) changesSince(since int64, kinds []syncv1.DocumentKind) []*syncv1.Change {
	var es []*entry
	for _, e := range s.entries {
		if e.version() <= since {
			continue
		}
		kind := e.tomb.GetKind()
		if e.doc != nil {
			kind = e.doc.GetKind()
		}
		if len(kinds) > 0 && !containsKind(kinds, kind) {
			continue
		}
		es = append(es, e)
	}
	sort.Slice(es, func(i, j int) bool { return es[i].version() < es[j].version() })
	out := make([]*syncv1.Change, 0, len(es))
	for _, e := range es {
		if e.doc != nil {
			out = append(out, &syncv1.Change{Change: &syncv1.Change_Document{Document: proto.Clone(e.doc).(*syncv1.Document)}})
		} else {
			out = append(out, &syncv1.Change{Change: &syncv1.Change_Tombstone{Tombstone: proto.Clone(e.tomb).(*syncv1.Tombstone)}})
		}
	}
	return out
}

func containsKind(ks []syncv1.DocumentKind, k syncv1.DocumentKind) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}

func changeVersion(ch *syncv1.Change) int64 {
	if d := ch.GetDocument(); d != nil {
		return d.GetVersion()
	}
	return ch.GetTombstone().GetVersion()
}

func (s *Server) ListChanges(ctx context.Context, req *connect.Request[syncv1.ListChangesRequest]) (*connect.Response[syncv1.ListChangesResponse], error) {
	if err := s.begin(ctx, req.Header(), "ListChanges"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	since := req.Msg.GetSinceVersion()
	if since > 0 && since < s.resyncBelow {
		return connect.NewResponse(&syncv1.ListChangesResponse{ResyncRequired: true, NextVersion: since}), nil
	}
	size := int(req.Msg.GetPageSize())
	if size <= 0 {
		size = 200
	}
	all := s.changesSince(since, req.Msg.GetKinds())
	resp := &syncv1.ListChangesResponse{NextVersion: since}
	if len(all) > size {
		all, resp.HasMore = all[:size], true
	}
	resp.Changes = all
	if len(all) > 0 {
		resp.NextVersion = changeVersion(all[len(all)-1])
	}
	if !resp.HasMore && s.version > resp.NextVersion {
		resp.NextVersion = s.version
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) WatchChanges(ctx context.Context, req *connect.Request[syncv1.WatchChangesRequest], stream *connect.ServerStream[syncv1.WatchChangesResponse]) error {
	if err := s.begin(ctx, req.Header(), "WatchChanges"); err != nil {
		return err
	}
	since := req.Msg.GetSinceVersion()
	s.mu.Lock()
	if since > 0 && since < s.resyncBelow {
		s.mu.Unlock()
		return stream.Send(&syncv1.WatchChangesResponse{Event: &syncv1.WatchChangesResponse_ResyncRequired{ResyncRequired: &syncv1.ResyncRequired{}}})
	}
	backlog := s.changesSince(since, req.Msg.GetKinds())
	head := s.version
	ch := make(chan *syncv1.Change, 256)
	s.watchID++
	id := s.watchID
	s.watchers[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.watchers, id)
		s.mu.Unlock()
	}()
	for _, c := range backlog {
		if err := stream.Send(&syncv1.WatchChangesResponse{Event: &syncv1.WatchChangesResponse_Change{Change: c}}); err != nil {
			return err
		}
	}
	if err := stream.Send(&syncv1.WatchChangesResponse{Event: &syncv1.WatchChangesResponse_CaughtUp{CaughtUp: &syncv1.CaughtUp{Version: head}}}); err != nil {
		return err
	}
	hb := s.HeartbeatInterval
	if hb <= 0 {
		hb = 20 * time.Second
	}
	ticker := time.NewTicker(hb)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case c := <-ch:
			if changeVersion(c) <= head {
				continue
			}
			if len(req.Msg.GetKinds()) > 0 {
				k := c.GetTombstone().GetKind()
				if d := c.GetDocument(); d != nil {
					k = d.GetKind()
				}
				if !containsKind(req.Msg.GetKinds(), k) {
					continue
				}
			}
			if err := stream.Send(&syncv1.WatchChangesResponse{Event: &syncv1.WatchChangesResponse_Change{Change: c}}); err != nil {
				return err
			}
		case <-ticker.C:
			s.mu.Lock()
			v := s.version
			fail := s.fail
			s.mu.Unlock()
			if fail != nil {
				if err := fail("WatchChanges.heartbeat"); err != nil {
					return err
				}
			}
			if err := stream.Send(&syncv1.WatchChangesResponse{Event: &syncv1.WatchChangesResponse_Heartbeat{Heartbeat: &syncv1.Heartbeat{Version: v}}}); err != nil {
				return err
			}
		}
	}
}

func (s *Server) GetDocument(ctx context.Context, req *connect.Request[syncv1.GetDocumentRequest]) (*connect.Response[syncv1.GetDocumentResponse], error) {
	if err := s.begin(ctx, req.Header(), "GetDocument"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.entries {
		if e.doc == nil {
			continue
		}
		if (req.Msg.GetId() != "" && e.doc.GetId() == req.Msg.GetId()) || (req.Msg.GetRef() != nil && k == refOfReq(req.Msg.GetRef())) {
			return connect.NewResponse(&syncv1.GetDocumentResponse{Document: proto.Clone(e.doc).(*syncv1.Document)}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, errors.New("document not found"))
}

func (s *Server) PutDocument(ctx context.Context, req *connect.Request[syncv1.PutDocumentRequest]) (*connect.Response[syncv1.PutDocumentResponse], error) {
	if err := s.begin(ctx, req.Header(), "PutDocument"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res := s.put(req.Msg)
	switch r := res.GetResult().(type) {
	case *syncv1.BatchPutResult_Document:
		return connect.NewResponse(&syncv1.PutDocumentResponse{Result: &syncv1.PutDocumentResponse_Document{Document: r.Document}}), nil
	case *syncv1.BatchPutResult_Conflict:
		return connect.NewResponse(&syncv1.PutDocumentResponse{Result: &syncv1.PutDocumentResponse_Conflict{Conflict: r.Conflict}}), nil
	case *syncv1.BatchPutResult_Error:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(r.Error.GetMessage()))
	}
	return nil, connect.NewError(connect.CodeInternal, errors.New("unreachable"))
}

func (s *Server) BatchPutDocuments(ctx context.Context, req *connect.Request[syncv1.BatchPutDocumentsRequest]) (*connect.Response[syncv1.BatchPutDocumentsResponse], error) {
	if err := s.begin(ctx, req.Header(), "BatchPutDocuments"); err != nil {
		return nil, err
	}
	if len(req.Msg.GetItems()) > 500 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("at most 500 items"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := &syncv1.BatchPutDocumentsResponse{}
	for _, it := range req.Msg.GetItems() {
		resp.Results = append(resp.Results, s.put(it))
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) DeleteDocument(ctx context.Context, req *connect.Request[syncv1.DeleteDocumentRequest]) (*connect.Response[syncv1.DeleteDocumentResponse], error) {
	if err := s.begin(ctx, req.Header(), "DeleteDocument"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		id := e.tomb.GetId()
		if e.doc != nil {
			id = e.doc.GetId()
		}
		if id != req.Msg.GetId() {
			continue
		}
		if req.Msg.BaseVersion != nil && (e.doc == nil || e.doc.GetVersion() != req.Msg.GetBaseVersion()) {
			c := conflictResult(e).GetConflict()
			return connect.NewResponse(&syncv1.DeleteDocumentResponse{Result: &syncv1.DeleteDocumentResponse_Conflict{Conflict: c}}), nil
		}
		if e.doc == nil {
			return connect.NewResponse(&syncv1.DeleteDocumentResponse{Result: &syncv1.DeleteDocumentResponse_Tombstone{Tombstone: proto.Clone(e.tomb).(*syncv1.Tombstone)}}), nil
		}
		return connect.NewResponse(&syncv1.DeleteDocumentResponse{Result: &syncv1.DeleteDocumentResponse_Tombstone{Tombstone: s.delete(e)}}), nil
	}
	return nil, connect.NewError(connect.CodeNotFound, errors.New("document not found"))
}

func (s *Server) UpsertSession(ctx context.Context, req *connect.Request[syncv1.UpsertSessionRequest]) (*connect.Response[syncv1.UpsertSessionResponse], error) {
	if err := s.begin(ctx, req.Header(), "UpsertSession"); err != nil {
		return nil, err
	}
	in := req.Msg.GetSession()
	id := in.GetInfo().GetId()
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session id required"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[id]
	if !ok {
		ss = &Session{Meta: &syncv1.SessionMetadata{CreatedAt: time.Now().UTC().Format(time.RFC3339), Info: &agentv1.SessionInfo{Id: id}}, uuids: map[string]bool{}}
		s.sessions[id] = ss
	}
	paths := req.Msg.GetUpdatePaths()
	ss.Upserts = append(ss.Upserts, append([]string(nil), paths...))
	applyPaths(ss.Meta, in, paths)
	return connect.NewResponse(&syncv1.UpsertSessionResponse{Session: proto.Clone(ss.Meta).(*syncv1.SessionMetadata)}), nil
}

// applyPaths copies the listed fields (all client-writable ones when empty).
func applyPaths(dst, src *syncv1.SessionMetadata, paths []string) {
	all := len(paths) == 0
	has := func(p string) bool {
		if all {
			return true
		}
		for _, x := range paths {
			if x == p || x == strings.TrimPrefix(p, "info.") {
				return true
			}
		}
		return false
	}
	d, si := dst.Info, src.GetInfo()
	if has("info.title") {
		d.Title = si.GetTitle()
	}
	if has("info.cwd") {
		d.Cwd = si.GetCwd()
	}
	if has("info.git_branch") {
		d.GitBranch = si.GetGitBranch()
	}
	if has("info.model") {
		d.Model = si.GetModel()
	}
	if has("info.permission_mode") {
		d.PermissionMode = si.GetPermissionMode()
	}
	if has("info.status") {
		d.Status = si.GetStatus()
	}
	if has("info.updated_at") {
		d.UpdatedAt = si.GetUpdatedAt()
	}
	if has("info.repo") {
		d.Repo = si.GetRepo()
	}
	if has("info.device_id") {
		d.DeviceId = si.GetDeviceId()
	}
	if has("info.device_name") {
		d.DeviceName = si.GetDeviceName()
	}
	if has("info.diff_stats") {
		d.DiffStats = si.GetDiffStats()
	}
	if has("info.archived") {
		d.Archived = si.GetArchived()
	}
	if has("info.pinned") {
		d.Pinned = si.GetPinned()
	}
	if has("info.parent_session_id") {
		d.ParentSessionId = si.GetParentSessionId()
	}
	if has("prompt_bundle_version") {
		dst.PromptBundleVersion = src.GetPromptBundleVersion()
	}
}

func (s *Server) AppendSessionEvents(ctx context.Context, req *connect.Request[syncv1.AppendSessionEventsRequest]) (*connect.Response[syncv1.AppendSessionEventsResponse], error) {
	if err := s.begin(ctx, req.Header(), "AppendSessionEvents"); err != nil {
		return nil, err
	}
	if len(req.Msg.GetEntries()) > 500 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("at most 500 entries"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[req.Msg.GetSessionId()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("unknown session"))
	}
	resp := &syncv1.AppendSessionEventsResponse{}
	for _, e := range req.Msg.GetEntries() {
		if e.GetUuid() == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("entry uuid required"))
		}
		if ss.uuids[e.GetUuid()] {
			resp.Duplicates++
			continue
		}
		ss.uuids[e.GetUuid()] = true
		c := proto.Clone(e).(*syncv1.TranscriptEntry)
		c.Seq = int64(len(ss.Entries) + 1)
		ss.Entries = append(ss.Entries, c)
		resp.Accepted++
	}
	ss.Meta.LastSeq = int64(len(ss.Entries))
	ss.Meta.LastActivityAt = time.Now().UTC().Format(time.RFC3339)
	resp.LastSeq = ss.Meta.LastSeq
	return connect.NewResponse(resp), nil
}
