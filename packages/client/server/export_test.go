package server

import "time"

// RefuseTerminals says why a session's agent cannot use terminals here (tests in package server_test).
func (s *Server) RefuseTerminals(id string) error { return s.refuseTerminals(id) }

// CheckIdleAt runs one idle compaction check as if the clock read now (tests in package server_test).
func (s *Server) CheckIdleAt(now time.Time) { s.checkIdle(now) }

// ResumePRWatches runs what a client start runs for sessions watching their PR (tests in package server_test).
func (s *Server) ResumePRWatches() { s.resumePRWatches() }

// SchedulerStarted reports whether the scheduler's loop runs (tests in package server_test).
func (s *Server) SchedulerStarted() bool { return s.sched != nil && s.sched.done != nil }
