package server

import "time"

// CheckIdleAt runs one idle compaction check as if the clock read now (tests in package server_test).
func (s *Server) CheckIdleAt(now time.Time) { s.checkIdle(now) }

// ResumePRWatches runs what a client start runs for sessions watching their PR (tests in package server_test).
func (s *Server) ResumePRWatches() { s.resumePRWatches() }
