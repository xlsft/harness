package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/vcs"
)

// Per-session user state: pinned, unread, archived and a rename.
//
// The state is stored per user on the harness server, in the session's SyncService record (SessionInfo fields
// written with UpsertSession update paths, so client-owned fields are never clobbered). The client keeps the
// last state it saw in memory: ListSessions refreshes it from the server (and falls back to the last known
// state when the server does not answer) and overlays it on the local sessions. UpdateSession writes to the
// server first and changes nothing locally when that fails; there is no offline queue for this state. A rename
// is also recorded as a custom-title transcript entry, which wins over AI titles and travels with the
// transcript (local list, resume, the uploaded copy).

// SessionSync is the part of harness.sync.v1.SyncService that keeps the user state, lists and reads the sessions
// of the user's other machines (remote.go) and deletes server copies (implemented by *syncclient.Client).
type SessionSync interface {
	UpsertSession(ctx context.Context, req *syncv1.UpsertSessionRequest) (*syncv1.UpsertSessionResponse, error)
	ListSessions(ctx context.Context, req *syncv1.ListSessionsRequest) (*syncv1.ListSessionsResponse, error)
	GetSessionTranscript(ctx context.Context, req *syncv1.GetSessionTranscriptRequest) (*syncv1.GetSessionTranscriptResponse, error)
	DeleteSession(ctx context.Context, req *syncv1.DeleteSessionRequest) (*syncv1.DeleteSessionResponse, error)
}

type userState struct {
	pinned, unread, archived bool
}

const (
	// sessionSyncTimeout bounds one round of SyncService calls of the session RPCs.
	sessionSyncTimeout = 5 * time.Second
	// stateListPages caps the pages (of stateListPageSize) read from the server per ListSessions.
	stateListPages    = 5
	stateListPageSize = 200
	maxCustomTitle    = 200
)

func (s *Server) stateOf(id string) userState {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.states[id]
}

func (s *Server) setState(id string, st userState) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.states == nil {
		s.states = map[string]userState{}
	}
	if st == (userState{}) {
		delete(s.states, id)
		return
	}
	s.states[id] = st
}

// applyState copies the known user state of the session into info.
func (s *Server) applyState(info *agentv1.SessionInfo) {
	st := s.stateOf(info.GetId())
	info.Pinned, info.Unread, info.Archived = st.pinned, st.unread, st.archived
}

// refreshStates reads the caller's sessions (of cwd, or all) from the server: their user state, and the server
// copies that remote.go lists as sessions of other machines. On failure the last known state stays.
func (s *Server) refreshStates(ctx context.Context, cwd string) {
	if s.Sync == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
	defer cancel()
	got := map[string]userState{}
	infos := map[string]*agentv1.SessionInfo{}
	token := ""
	for range stateListPages {
		resp, err := s.Sync.ListSessions(ctx, &syncv1.ListSessionsRequest{Cwd: cwd, IncludeArchived: true, PageSize: stateListPageSize, PageToken: token})
		if err != nil {
			s.log().Debug("session state: server list failed; using the last known state", "err", err)
			return
		}
		for _, m := range resp.GetSessions() {
			i := m.GetInfo()
			got[i.GetId()] = userState{pinned: i.GetPinned(), unread: i.GetUnread(), archived: i.GetArchived()}
			infos[i.GetId()] = i
		}
		if token = resp.GetNextPageToken(); token == "" {
			break
		}
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	complete := cwd == "" && token == ""
	if complete || s.states == nil {
		// A complete listing replaces what was known.
		s.states = map[string]userState{}
	}
	if complete || s.listed == nil {
		s.listed = map[string]*agentv1.SessionInfo{}
	}
	for id, i := range infos {
		s.listed[id] = i
	}
	for id, st := range got {
		if st == (userState{}) {
			delete(s.states, id)
		} else {
			s.states[id] = st
		}
	}
}

// validSessionID reports whether id is a session id (a UUID, as the transcript file name). It guards every
// path built from a session id.
func validSessionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	_, err := uuid.Parse(id)
	return err == nil
}

// locate returns the live session and the transcript path of a session; both are empty when it is unknown here.
func (s *Server) locate(id string) (*liveSession, string) {
	s.mu.Lock()
	ls := s.live[id]
	s.mu.Unlock()
	if ls != nil {
		return ls, ls.sess.Transcript.Path()
	}
	path, err := session.Store{Root: config.ProjectsDir()}.Find("", id)
	if err != nil {
		return nil, ""
	}
	return nil, path
}

// fileInfo is the SessionInfo of a transcript that is not live.
func fileInfo(path string) *agentv1.SessionInfo {
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	cwd, wt := transcriptHead(path, false)
	info := &agentv1.SessionInfo{Id: id, Cwd: cwd, Title: sessionTitle(path), Status: agentv1.SessionStatus_SESSION_STATUS_IDLE,
		ParentSessionId: forkedFrom(path), Worktree: worktreeToProto(wt), NoFolder: isNoFolder(id, cwd)}
	if st, err := os.Stat(path); err == nil {
		info.UpdatedAt = st.ModTime().UTC().Format(time.RFC3339)
	}
	return info
}

// syncError maps a SyncService failure: refusals keep their meaning, everything else means the server could not
// be reached.
func syncError(err error) error {
	var ae *llm.APIError
	if errors.As(err, &ae) {
		switch ae.Status {
		case http.StatusBadRequest:
			return connect.NewError(connect.CodeInvalidArgument, err)
		case http.StatusForbidden:
			return connect.NewError(connect.CodePermissionDenied, err)
		case http.StatusNotFound:
			return connect.NewError(connect.CodeNotFound, err)
		}
	}
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("harness server unreachable; nothing was changed: %w", err))
}

func (s *Server) UpdateSession(ctx context.Context, req *connect.Request[agentv1.UpdateSessionRequest]) (*connect.Response[agentv1.UpdateSessionResponse], error) {
	m := req.Msg
	id := m.GetSessionId()
	if !validSessionID(id) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid session id %q", id))
	}
	title := ""
	if m.Title != nil {
		if title = truncateRunes(strings.Join(strings.Fields(m.GetTitle()), " "), maxCustomTitle); title == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("title must not be empty"))
		}
	}
	ls, path := s.locate(id)
	if ls == nil && path == "" {
		if info := s.remoteInfo(id); info != nil {
			return s.updateRemote(ctx, info, m, title)
		}
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s not found", id))
	}
	flags := m.Pinned != nil || m.Unread != nil || m.Archived != nil
	if flags && s.Sync == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("pin, unread and archive are stored on the harness server, and this client does not sync with it (not signed in, or sync disabled)"))
	}
	st := s.stateOf(id)
	var paths []string
	if m.Pinned != nil {
		st.pinned = m.GetPinned()
		paths = append(paths, "info.pinned")
	}
	if m.Unread != nil {
		st.unread = m.GetUnread()
		paths = append(paths, "info.unread")
	}
	if m.Archived != nil {
		st.archived = m.GetArchived()
		paths = append(paths, "info.archived")
	}
	if m.Title != nil {
		paths = append(paths, "info.title")
	}
	detach := m.GetDetachFromParent()
	if detach {
		paths = append(paths, "info.parent_session_id")
	}
	if len(paths) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nothing to update"))
	}
	if s.Sync != nil {
		// The whole local info goes along: it creates the server record when the session was never uploaded;
		// otherwise only the update paths are written.
		var info *agentv1.SessionInfo
		if ls != nil {
			info = ls.info()
		} else {
			info = fileInfo(path)
		}
		info.Pinned, info.Unread, info.Archived = st.pinned, st.unread, st.archived
		if m.Title != nil {
			info.Title = title
		}
		if detach {
			info.ParentSessionId = ""
		}
		cctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
		_, err := s.Sync.UpsertSession(cctx, &syncv1.UpsertSessionRequest{Session: &syncv1.SessionMetadata{Info: info}, UpdatePaths: paths})
		cancel()
		if err != nil {
			return nil, syncError(err)
		}
	}
	s.setState(id, st)
	if m.Title != nil {
		var err error
		if ls != nil {
			err = ls.setCustomTitle(title)
		} else {
			err = appendCustomTitle(path, id, title)
		}
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("record the title: %w", err))
		}
	}
	if detach {
		var err error
		if ls != nil {
			err = ls.detachFromParent()
		} else {
			err = appendEntry(path, session.Entry{Type: session.TypeMeta, SessionID: id, DetachedFromParent: true,
				Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
		}
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("record the detach: %w", err))
		}
	}
	var info *agentv1.SessionInfo
	if ls != nil {
		ls.publishInfo()
		info = ls.info()
		if st.archived {
			s.closeLive(id, "archived")
			info.Status = agentv1.SessionStatus_SESSION_STATUS_IDLE
		}
	} else {
		info = fileInfo(path)
		s.applyState(info)
	}
	return connect.NewResponse(&agentv1.UpdateSessionResponse{Session: info}), nil
}

// appendCustomTitle records a rename in the transcript of a session that is not live.
func appendCustomTitle(path, id, title string) error {
	return appendEntry(path, session.Entry{Type: session.TypeCustomTitle, CustomTitle: title, SessionID: id,
		Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
}

// appendEntry appends an unchained entry to the transcript of a session that is not live.
func appendEntry(path string, e session.Entry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (s *Server) DeleteSession(ctx context.Context, req *connect.Request[agentv1.DeleteSessionRequest]) (*connect.Response[agentv1.DeleteSessionResponse], error) {
	id := req.Msg.GetSessionId()
	if !validSessionID(id) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid session id %q", id))
	}
	ls, path := s.locate(id)
	if ls == nil && path == "" && s.Sync == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s not found", id))
	}
	// Stop first: closing flushes the session's upload, which must not recreate the server copy afterwards.
	if ls != nil {
		s.closeLive(id, "deleted")
	}
	vcs.RemovePRState(bootstrap.PRStateDir(), id)
	// Dropping the upload state before the server delete keeps other uploaders from re-uploading it; if the
	// delete fails, a later open re-uploads idempotently from the start.
	if err := syncclient.Forget("", id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if s.Sync != nil {
		cctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
		_, err := s.Sync.DeleteSession(cctx, &syncv1.DeleteSessionRequest{SessionId: id})
		cancel()
		switch {
		case err == nil:
		case syncclient.IsNotFound(err):
			// Never uploaded.
			if path == "" {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s not found", id))
			}
		default:
			return nil, syncError(err)
		}
	}
	if path != "" {
		// The transcript and its directory (subagent transcripts).
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		_ = os.RemoveAll(strings.TrimSuffix(path, ".jsonl"))
	}
	_ = os.RemoveAll(bootstrap.FileHistoryDir(id))
	_ = os.RemoveAll(bootstrap.FolderBaselineDir(id))
	_ = os.RemoveAll(bootstrap.PlansDir(id))
	_ = os.Remove(sessionLinksPath(id))
	_ = s.browsers().DeleteProfile(id)
	s.previews().CloseSession(id)
	s.removeChatDir(id)
	s.setState(id, userState{})
	s.forgetListed(id)
	return connect.NewResponse(&agentv1.DeleteSessionResponse{}), nil
}
