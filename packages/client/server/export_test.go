package server

import "time"

// RefuseTerminals says why a session's agent cannot use terminals here (tests in package server_test).
func (s *Server) RefuseTerminals(id string) error { return s.refuseTerminals(id) }

// CheckIdleAt runs one idle compaction check as if the clock read now (tests in package server_test).
func (s *Server) CheckIdleAt(now time.Time) { s.checkIdle(now) }
