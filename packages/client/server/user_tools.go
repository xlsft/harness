package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/tools"
)

// What the tools that reach the user need from the frontends (user_tools.proto): who is looking at a session, the
// notifications PushNotification sends, and the bytes of files SendUserFile shows.

// viewerPresenceTTL is how long a frontend's "focused" holds without being renewed: a frontend that went away must not
// keep notifications quiet.
const viewerPresenceTTL = 45 * time.Second

// viewerPresence is who looks at a session, by frontend.
type viewerPresence struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func (p *viewerPresence) set(viewer string, focused bool, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !focused {
		delete(p.until, viewer)
		return
	}
	if p.until == nil {
		p.until = map[string]time.Time{}
	}
	p.until[viewer] = now.Add(viewerPresenceTTL)
}

func (p *viewerPresence) looking(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	found := false
	for v, t := range p.until {
		if now.After(t) {
			delete(p.until, v)
			continue
		}
		found = true
	}
	return found
}

// UserLooking implements tools.UserNotifier.
func (ls *liveSession) UserLooking() bool { return ls.presence.looking(time.Now()) }

// PushToUser implements tools.UserNotifier: the event goes to every frontend of the session (the relay mirror counts,
// so viewers on other devices get it while they follow the session).
func (ls *liveSession) PushToUser(id, title, body string) bool {
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_UserNotification{UserNotification: &agentv1.UserNotification{Id: id, Title: title, Body: body}}})
	return ls.hub.watched()
}

func (s *Server) UpdateViewerPresence(ctx context.Context, req *connect.Request[agentv1.UpdateViewerPresenceRequest]) (*connect.Response[agentv1.UpdateViewerPresenceResponse], error) {
	if strings.TrimSpace(req.Msg.GetViewerId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("viewer_id is required"))
	}
	s.mu.Lock()
	ls := s.live[req.Msg.GetSessionId()]
	s.mu.Unlock()
	if ls != nil {
		ls.presence.set(req.Msg.GetViewerId(), req.Msg.GetFocused(), time.Now())
	}
	return connect.NewResponse(&agentv1.UpdateViewerPresenceResponse{}), nil
}

func (s *Server) ReadSentFile(ctx context.Context, req *connect.Request[agentv1.ReadSentFileRequest]) (*connect.Response[agentv1.ReadSentFileResponse], error) {
	sc, err := s.fileScope(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Msg.GetPath()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("path is required"))
	}
	p, real, err := sc.resolve(req.Msg.GetPath())
	if err != nil {
		return nil, err
	}
	data, err := readSentFileBytes(real)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return connect.NewResponse(&agentv1.ReadSentFileResponse{Content: data, Mime: tools.SentFileMime(p, data), Sha256: hex.EncodeToString(sum[:])}), nil
}

// readSentFileBytes reads a regular file within tools.MaxSentFileBytes.
func readSentFileBytes(real string) ([]byte, error) {
	st, err := os.Stat(real)
	if err != nil {
		return nil, fsError(real, err)
	}
	if !st.Mode().IsRegular() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is not a regular file", real))
	}
	tooLarge := connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("%s is larger than %d MB", real, tools.MaxSentFileBytes>>20))
	if st.Size() > tools.MaxSentFileBytes {
		return nil, tooLarge
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, fsError(real, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, tools.MaxSentFileBytes+1))
	if err != nil {
		return nil, fsError(real, err)
	}
	if len(data) > tools.MaxSentFileBytes {
		return nil, tooLarge
	}
	return data, nil
}
