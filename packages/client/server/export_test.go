package server

import (
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// RefuseTerminals says why a session's agent cannot use terminals here (tests in package server_test).
func (s *Server) RefuseTerminals(id string) error { return s.refuseTerminals(id) }

// CheckIdleAt runs one idle compaction check as if the clock read now (tests in package server_test).
func (s *Server) CheckIdleAt(now time.Time) { s.checkIdle(now) }

// ResumePRWatches runs what a client start runs for sessions watching their PR (tests in package server_test).
func (s *Server) ResumePRWatches() { s.resumePRWatches() }

// SchedulerStarted reports whether the scheduler's loop runs (tests in package server_test).
func (s *Server) SchedulerStarted() bool {
	h := s.sched.Load()
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stop != nil
}

// MarkMoved makes a live session read-only as a move does, without a host (tests in package server_test).
func (s *Server) MarkMoved(id, hostName string) {
	s.mu.Lock()
	ls := s.live[id]
	s.mu.Unlock()
	ls.markMoved(&agentv1.SessionMoved{HostId: "ahs_test", HostName: hostName, WorkId: "hwk_test", Branch: "harness/" + id, MovedAt: "2026-11-07T12:00:00Z"})
}
