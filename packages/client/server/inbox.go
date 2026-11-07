package server

import (
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/rpc"
)

// The inbox on the client (docs/design/coordinator.md): every live session of this client is folded from the events it
// publishes (inboxfold.go); the user's other machines and hosts come from the harness server (InboxService). ListInbox
// and WatchInbox merge the two, the local item winning for a session of this machine. Without a server the inbox is
// this machine's sessions, and the read markers live in memory.

const (
	// inboxClosedKeep is how long a closed session stays while unread (as on the server).
	inboxClosedKeep = 14 * 24 * time.Hour
	inboxPageDefault = 50
	inboxPageMax     = 200
	// inboxWatchBuffer is how many changes a watcher may fall behind before it is told to list again.
	inboxWatchBuffer = 256
	inboxCallTimeout = 5 * time.Second
	inboxRetryMax    = 30 * time.Second
)

var inboxGroups = []agentv1.InboxGroup{agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT, agentv1.InboxGroup_INBOX_GROUP_FAILED,
	agentv1.InboxGroup_INBOX_GROUP_FINISHED, agentv1.InboxGroup_INBOX_GROUP_RUNNING}

// inboxWatcher is one WatchInbox stream.
type inboxWatcher struct {
	ch   chan *agentv1.WatchInboxResponse
	lost bool
}

// inboxState is the local inbox (Server.inbox).
type inboxState struct {
	mu    sync.Mutex
	folds map[string]*inboxFold
	// mirrored: the session is mirrored to the server, which lists it too.
	mirrored map[string]bool
	// reads are the read markers of this client, by session.
	reads    map[string]time.Time
	watchers map[*inboxWatcher]struct{}
	// remote are the server's items of other machines' sessions seen last, by session (for answers and Open);
	// serverCounts its last counts.
	remote       map[string]*agentv1.InboxItem
	serverCounts []*agentv1.InboxCount
	// now is the clock (tests).
	now func() time.Time
}

func (in *inboxState) init() {
	if in.folds == nil {
		in.folds, in.mirrored, in.reads = map[string]*inboxFold{}, map[string]bool{}, map[string]time.Time{}
		in.watchers, in.remote = map[*inboxWatcher]struct{}{}, map[string]*agentv1.InboxItem{}
	}
	if in.now == nil {
		in.now = time.Now
	}
}

// unreadLocked: something needed attention after the last read.
func (in *inboxState) unreadLocked(f *inboxFold) bool {
	return !f.attention.IsZero() && f.attention.After(in.reads[f.item.GetSessionId()])
}

// visibleLocked: listed now (as on the server: needs something, open or unread lately).
func (in *inboxState) visibleLocked(f *inboxFold) bool {
	if f.group() == agentv1.InboxGroup_INBOX_GROUP_UNSPECIFIED && f.item.GetProjectRun() == nil {
		return false
	}
	if f.item.GetOpen() {
		return true
	}
	return in.unreadLocked(f) && in.now().Sub(f.attention) < inboxClosedKeep
}

// presentLocked is the item as a frontend sees it.
func (in *inboxState) presentLocked(f *inboxFold, deviceID string) *agentv1.InboxItem {
	it := proto.Clone(f.item).(*agentv1.InboxItem)
	it.Group = f.group()
	it.Unread = in.unreadLocked(f)
	it.DeviceId = deviceID
	on := true
	it.DeviceOnline = &on
	return it
}

// observe folds an event a live session published.
func (s *Server) inboxObserve(ls *liveSession, ev *agentv1.SessionEvent) {
	in := &s.inbox
	switch ev.GetEvent().(type) {
	case *agentv1.SessionEvent_AssistantDelta, *agentv1.SessionEvent_ToolCallStarted, *agentv1.SessionEvent_ToolCallFinished,
		*agentv1.SessionEvent_Notice, *agentv1.SessionEvent_TaskOutput, *agentv1.SessionEvent_UserShellOutput, *agentv1.SessionEvent_HookRun:
		return
	}
	id := ls.sess.ID
	mirrored := s.relay() != nil && ls.relayWanted()
	in.mu.Lock()
	in.init()
	f := in.folds[id]
	if f == nil {
		f = newInboxFold(id)
		f.item.Cwd = ls.sess.Dir()
		in.folds[id] = f
	}
	in.mirrored[id] = mirrored
	changed := f.fold(ev, in.now())
	var item *agentv1.InboxItem
	removed := false
	if changed {
		if in.visibleLocked(f) {
			item = in.presentLocked(f, s.DeviceID)
		} else {
			removed = true
		}
		if !f.item.GetOpen() && !in.unreadLocked(f) {
			delete(in.folds, id)
			delete(in.mirrored, id)
		}
	}
	in.mu.Unlock()
	if item != nil {
		s.inboxEmit(&agentv1.InboxChange{Item: item})
	} else if removed {
		s.inboxEmit(&agentv1.InboxChange{RemovedSessionId: id})
	}
}

// inboxEmit sends a change to every watcher, with the counts after it.
func (s *Server) inboxEmit(c *agentv1.InboxChange) {
	in := &s.inbox
	in.mu.Lock()
	defer in.mu.Unlock()
	in.init()
	if len(in.watchers) == 0 {
		return
	}
	counts := in.countsLocked()
	for w := range in.watchers {
		select {
		case w.ch <- &agentv1.WatchInboxResponse{Event: &agentv1.WatchInboxResponse_Change{Change: c}, Counts: counts}:
		default:
			w.lost = true
		}
	}
}

// countsLocked: the server's last counts plus the local sessions the server does not have.
func (in *inboxState) countsLocked() []*agentv1.InboxCount {
	by := map[agentv1.InboxGroup]*agentv1.InboxCount{}
	for _, g := range inboxGroups {
		by[g] = &agentv1.InboxCount{Group: g}
	}
	for _, c := range in.serverCounts {
		if x := by[c.GetGroup()]; x != nil {
			x.Total, x.Unread = c.GetTotal(), c.GetUnread()
		}
	}
	for id, f := range in.folds {
		if in.mirrored[id] || !in.visibleLocked(f) {
			continue
		}
		if x := by[f.group()]; x != nil {
			x.Total++
			if in.unreadLocked(f) {
				x.Unread++
			}
		}
	}
	out := make([]*agentv1.InboxCount, 0, len(inboxGroups))
	for _, g := range inboxGroups {
		out = append(out, by[g])
	}
	return out
}

// takeServerItem records a server item; false when the session is this machine's (the local fold wins). A local
// session read on another device is read here too.
func (in *inboxState) takeServerItemLocked(it *agentv1.InboxItem, deviceID string) bool {
	id := it.GetSessionId()
	if f := in.folds[id]; f != nil {
		if !it.GetUnread() && !f.attention.IsZero() && in.reads[id].Before(f.attention) && it.GetAttentionAt() >= f.item.GetAttentionAt() {
			in.reads[id] = f.attention
		}
		return false
	}
	it.Remote = deviceID == "" || it.GetDeviceId() != deviceID
	in.remote[id] = proto.Clone(it).(*agentv1.InboxItem)
	return true
}

func sortInbox(items []*agentv1.InboxItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.GetUnread() != b.GetUnread() {
			return a.GetUnread()
		}
		if a.GetAttentionAt() != b.GetAttentionAt() {
			return a.GetAttentionAt() > b.GetAttentionAt()
		}
		return a.GetSessionId() < b.GetSessionId()
	})
}

// inboxBearer sets the bearer token of a call to the server.
func (v *RelayViewer) inboxBearer(ctx context.Context, h interface{ Set(string, string) }, refresh bool) error {
	if v.tokens == nil {
		return nil
	}
	tok, err := v.tokens.Token(ctx)
	if refresh {
		tok, err = v.tokens.Refresh(ctx)
	}
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, err)
	}
	h.Set("Authorization", "Bearer "+tok)
	return nil
}

func inboxCall[Req, Res any](ctx context.Context, v *RelayViewer, fn func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error), msg *Req) (*Res, error) {
	for attempt := 0; ; attempt++ {
		req := connect.NewRequest(msg)
		if v.tokens != nil {
			tok, err := v.tokens.Token(ctx)
			if attempt > 0 {
				tok, err = v.tokens.Refresh(ctx)
			}
			if err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, err)
			}
			rpc.SetBearer(req.Header(), tok)
		}
		res, err := fn(ctx, req)
		if err == nil {
			return res.Msg, nil
		}
		if connect.CodeOf(err) != connect.CodeUnauthenticated || attempt > 0 || v.tokens == nil {
			return nil, err
		}
	}
}

func inboxOffset(token string) int {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(string(b))
	return n
}

// ListInbox implements AgentService. The first page carries this machine's sessions; every page carries the server's
// page of the same group without them. Without a server, local sessions are paged here.
func (s *Server) ListInbox(ctx context.Context, req *connect.Request[agentv1.ListInboxRequest]) (*connect.Response[agentv1.ListInboxResponse], error) {
	m := req.Msg
	if !m.GetProjectRunsOnly() && m.GetGroup() == agentv1.InboxGroup_INBOX_GROUP_UNSPECIFIED {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("group is required"))
	}
	size := int(m.GetPageSize())
	if size <= 0 {
		size = inboxPageDefault
	}
	size = min(size, inboxPageMax)
	var srv *agentv1.ListInboxResponse
	var srvErr error
	if s.Relay != nil {
		cctx, cancel := context.WithTimeout(ctx, inboxCallTimeout)
		res, err := inboxCall(cctx, s.Relay, s.Relay.inbox.ListInbox, &relayv1.ListInboxRequest{Request: m})
		cancel()
		srv, srvErr = res.GetResponse(), err
		if err != nil {
			s.log().Debug("inbox: server unavailable", "err", err)
		}
	}
	in := &s.inbox
	in.mu.Lock()
	in.init()
	var local []*agentv1.InboxItem
	for _, f := range in.folds {
		if !in.visibleLocked(f) {
			continue
		}
		if m.GetProjectRunsOnly() {
			if f.item.GetProjectRun() == nil {
				continue
			}
		} else if f.group() != m.GetGroup() {
			continue
		}
		local = append(local, in.presentLocked(f, s.DeviceID))
	}
	sortInbox(local)
	out := &agentv1.ListInboxResponse{}
	if srv != nil {
		in.serverCounts = srv.GetCounts()
		if m.GetPageToken() == "" {
			out.Items = local
		}
		for _, it := range srv.GetItems() {
			if in.takeServerItemLocked(it, s.DeviceID) {
				out.Items = append(out.Items, it)
			}
		}
		out.NextPageToken = srv.GetNextPageToken()
	} else {
		// Local only: page here.
		off := inboxOffset(m.GetPageToken())
		if off < len(local) {
			end := min(off+size, len(local))
			out.Items = local[off:end]
			if end < len(local) {
				out.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
			}
		}
		if srvErr == nil {
			in.serverCounts = nil
		}
	}
	out.Counts = in.countsLocked()
	in.mu.Unlock()
	return connect.NewResponse(out), nil
}

// WatchInbox implements AgentService: caught_up, then local changes and the server's, merged. A stream that falls
// behind, or whose server stream broke and came back, gets resync.
func (s *Server) WatchInbox(ctx context.Context, _ *connect.Request[agentv1.WatchInboxRequest], stream *connect.ServerStream[agentv1.WatchInboxResponse]) error {
	in := &s.inbox
	w := &inboxWatcher{ch: make(chan *agentv1.WatchInboxResponse, inboxWatchBuffer)}
	in.mu.Lock()
	in.init()
	in.watchers[w] = struct{}{}
	in.mu.Unlock()
	defer func() {
		in.mu.Lock()
		delete(in.watchers, w)
		in.mu.Unlock()
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if s.Relay != nil {
		go s.inboxFollowServer(ctx, w)
	}
	if err := stream.Send(&agentv1.WatchInboxResponse{Event: &agentv1.WatchInboxResponse_CaughtUp{CaughtUp: true}}); err != nil {
		return err
	}
	beat := s.Heartbeat
	if beat <= 0 {
		beat = DefaultHeartbeat
	}
	t := time.NewTicker(beat)
	defer t.Stop()
	for {
		in.mu.Lock()
		lost := w.lost
		w.lost = false
		in.mu.Unlock()
		if lost {
			for len(w.ch) > 0 {
				<-w.ch
			}
			if err := stream.Send(&agentv1.WatchInboxResponse{Event: &agentv1.WatchInboxResponse_Resync{Resync: true}}); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case r := <-w.ch:
			if err := stream.Send(r); err != nil {
				return err
			}
		case <-t.C:
			if err := stream.Send(&agentv1.WatchInboxResponse{Event: &agentv1.WatchInboxResponse_Heartbeat{Heartbeat: true}}); err != nil {
				return err
			}
		}
	}
}

// inboxFollowServer forwards the server's inbox changes to one watcher until ctx ends, reconnecting with backoff.
func (s *Server) inboxFollowServer(ctx context.Context, w *inboxWatcher) {
	in := &s.inbox
	delay := time.Second
	first := true
	for ctx.Err() == nil {
		req := connect.NewRequest(&relayv1.WatchInboxRequest{})
		if err := s.Relay.inboxBearer(ctx, req.Header(), false); err != nil {
			return
		}
		st, err := s.Relay.inbox.WatchInbox(ctx, req)
		if err == nil {
			for st.Receive() {
				r := st.Msg().GetResponse()
				delay = time.Second
				switch r.GetEvent().(type) {
				case *agentv1.WatchInboxResponse_CaughtUp:
					if !first {
						// Changes may have been missed while the stream was down.
						in.mu.Lock()
						w.lost = true
						in.mu.Unlock()
					}
					first = false
					continue
				case *agentv1.WatchInboxResponse_Change:
				default:
					continue
				}
				c := r.GetChange()
				in.mu.Lock()
				in.serverCounts = r.GetCounts()
				fwd := true
				if it := c.GetItem(); it != nil {
					fwd = in.takeServerItemLocked(it, s.DeviceID)
				} else if id := c.GetRemovedSessionId(); id != "" {
					fwd = in.folds[id] == nil
					delete(in.remote, id)
				}
				var out *agentv1.WatchInboxResponse
				if fwd {
					out = &agentv1.WatchInboxResponse{Event: r.GetEvent(), Counts: in.countsLocked()}
				}
				in.mu.Unlock()
				if out != nil {
					select {
					case w.ch <- out:
					default:
						in.mu.Lock()
						w.lost = true
						in.mu.Unlock()
					}
				}
			}
			_ = st.Close()
		}
		if connect.CodeOf(err) == connect.CodeUnauthenticated {
			_ = s.Relay.inboxBearer(ctx, connect.NewRequest(&relayv1.WatchInboxRequest{}).Header(), true)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, inboxRetryMax)
	}
}

// MarkInboxRead implements AgentService: the local markers, the server's (all devices), and the sidebar's unread flag.
func (s *Server) MarkInboxRead(ctx context.Context, req *connect.Request[agentv1.MarkInboxReadRequest]) (*connect.Response[agentv1.MarkInboxReadResponse], error) {
	ids := req.Msg.GetSessionIds()
	read := req.Msg.GetRead()
	if len(ids) == 0 {
		return connect.NewResponse(&agentv1.MarkInboxReadResponse{}), nil
	}
	if s.Relay != nil {
		cctx, cancel := context.WithTimeout(ctx, inboxCallTimeout)
		_, err := inboxCall(cctx, s.Relay, s.Relay.inbox.MarkInboxRead, &relayv1.MarkInboxReadRequest{Request: req.Msg})
		cancel()
		if err != nil {
			s.log().Debug("inbox: read marker not sent", "err", err)
		}
	}
	in := &s.inbox
	var changes []*agentv1.InboxChange
	in.mu.Lock()
	in.init()
	now := in.now()
	for _, id := range ids {
		if read {
			in.reads[id] = now
		} else {
			delete(in.reads, id)
		}
		if f := in.folds[id]; f != nil {
			if in.visibleLocked(f) {
				changes = append(changes, &agentv1.InboxChange{Item: in.presentLocked(f, s.DeviceID)})
			} else {
				changes = append(changes, &agentv1.InboxChange{RemovedSessionId: id})
			}
		}
		if it := in.remote[id]; it != nil {
			it.Unread = !read
		}
	}
	in.mu.Unlock()
	if read {
		s.stateMu.Lock()
		for _, id := range ids {
			if st, ok := s.states[id]; ok {
				st.unread = false
				s.states[id] = st
			}
		}
		s.stateMu.Unlock()
	}
	for _, c := range changes {
		s.inboxEmit(c)
	}
	return connect.NewResponse(&agentv1.MarkInboxReadResponse{}), nil
}

// inboxRemoteInfo is what the inbox knows of a session of another machine that the session listing does not have
// yet: enough to answer it and to follow it through the relay.
func (s *Server) inboxRemoteInfo(id string) *agentv1.SessionInfo {
	in := &s.inbox
	in.mu.Lock()
	defer in.mu.Unlock()
	it := in.remote[id]
	if it == nil || !it.GetRemote() {
		return nil
	}
	info := &agentv1.SessionInfo{Id: id, Title: it.GetTitle(), Cwd: it.GetCwd(), Repo: it.GetRepo(), DeviceId: it.GetDeviceId(),
		DeviceName: it.GetDeviceName(), Remote: true, ParentSessionId: it.GetParentSessionId(), UpdatedAt: it.GetUpdatedAt(),
		Status: agentv1.SessionStatus_SESSION_STATUS_IDLE}
	if it.DeviceOnline != nil {
		on := it.GetDeviceOnline()
		info.DeviceOnline = &on
	}
	info.RemoteControllable = it.GetOpen() && it.GetDeviceOnline()
	switch {
	case len(it.GetRequests()) > 0:
		info.Status = agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT
	case it.GetWorking():
		info.Status = agentv1.SessionStatus_SESSION_STATUS_WORKING
	}
	return info
}
