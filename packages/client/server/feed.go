package server

import (
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// SetSessionObserver registers fn to be called (in its own goroutine) with
// the id of every session opened from now on. The relay host uses it to
// mirror sessions to the harness server.
func (s *Server) SetSessionObserver(fn func(sessionID string)) {
	s.mu.Lock()
	s.observer = fn
	s.mu.Unlock()
}

func (s *Server) sessionOpened(id string) {
	s.mu.Lock()
	fn := s.observer
	s.mu.Unlock()
	if fn != nil {
		go fn(id)
	}
}

// LiveSessionIDs lists the open sessions.
func (s *Server) LiveSessionIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.live))
	for id := range s.live {
		ids = append(ids, id)
	}
	return ids
}

// Feed is read access to one live session's event log, the same log
// Subscribe serves to frontends.
type Feed struct{ ls *liveSession }

// SessionFeed returns the feed of an open session.
func (s *Server) SessionFeed(id string) (Feed, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ls := s.live[id]
	if ls == nil {
		return Feed{}, false
	}
	return Feed{ls: ls}, true
}

// Subscribe returns the stored events after seq `after` and a channel of
// live events (history then live, no gaps or duplicates). Events are shared
// with other subscribers and must not be modified. The channel is closed
// when the session ends, or when the subscriber falls too far behind (then
// Closed reports false and the caller resubscribes).
func (f Feed) Subscribe(after int64) ([]*agentv1.SessionEvent, <-chan *agentv1.SessionEvent, func()) {
	return f.ls.hub.subscribe(after)
}

// HistoryLen is the number of events at the start of the log that were
// replayed from the transcript of a resumed session (0 for a new session).
func (f Feed) HistoryLen() int64 { return f.ls.history }

// Closed reports whether the session ended.
func (f Feed) Closed() bool { return f.ls.hub.isClosed() }

// Info is the current session summary.
func (f Feed) Info() *agentv1.SessionInfo { return f.ls.info() }

// RemoteControl reports whether the session may be mirrored and controlled
// through the server: the session's remote control switch on record
// (SetSessionRelay), else the merged settings ("remoteControl", default
// true); never when policy settings turn it off.
func (f Feed) RemoteControl() bool { return f.ls.relayWanted() }
