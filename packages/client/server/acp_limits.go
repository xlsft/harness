package server

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/xlsft/harness/packages/client/acp"
)

// An agent's plan limits, from its limits source (acp.LimitsSource) if it has one: while a session of the agent is
// live here, when it opens and after a turn. An acpLive runs on this machine, so a session of another machine never
// gets here. The outcome stays on this machine as well: HARNESS_HOME/acp/state.json and this machine's sessions.

const (
	// acpLimitsInterval is how often an agent is asked at most.
	acpLimitsInterval = 5 * time.Minute
	// acpLimitsTimeout bounds one fetch; the agent is known to start, a session of it just did.
	acpLimitsTimeout = 90 * time.Second
)

// acpLimits runs one fetch per agent at a time, and one per acpLimitsInterval.
type acpLimits struct {
	mu      sync.Mutex
	running map[string]bool
	last    map[string]time.Time
	// off: asking this agent reached the model. It is not asked again until the client restarts.
	off map[string]bool
	// sources and now override acp.LimitsSourceFor and time.Now (tests).
	sources map[string]acp.LimitsSource
	now     func() time.Time
	wg      sync.WaitGroup
}

func (p *acpLimits) wait() { p.wg.Wait() }

func (p *acpLimits) time() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// SetAcpLimitsSource gives an agent a limits source (tests: a source for the fake agent).
func (s *Server) SetAcpLimitsSource(id string, src acp.LimitsSource) {
	p := &s.acp.limits
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sources == nil {
		p.sources = map[string]acp.LimitsSource{}
	}
	p.sources[id] = src
}

// SetAcpLimitsClock replaces the clock of the limits throttle (tests).
func (s *Server) SetAcpLimitsClock(now func() time.Time) {
	p := &s.acp.limits
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now = now
}

func (s *Server) acpLimitsSource(id string) acp.LimitsSource {
	p := &s.acp.limits
	p.mu.Lock()
	defer p.mu.Unlock()
	if src, ok := p.sources[id]; ok {
		return src
	}
	return acp.LimitsSourceFor(id)
}

// acpSessionsOf returns the open sessions of an agent.
func (s *Server) acpSessionsOf(id string) []*acpLive {
	s.acp.mu.Lock()
	defer s.acp.mu.Unlock()
	var out []*acpLive
	for _, l := range s.acp.live {
		if l.sess.Config().AgentID == id {
			out = append(out, l)
		}
	}
	return out
}

// acpNoPermissionOpen says whether none of the agent's sessions waits for a permission answer.
func (s *Server) acpNoPermissionOpen(id string) bool {
	for _, l := range s.acpSessionsOf(id) {
		if l.sess.WaitsOnPermission() {
			return false
		}
	}
	return true
}

// refreshAcpLimits fetches the agent's limits in the background, when it is time to. An open permission prompt skips
// this time without counting: the next turn asks again.
func (s *Server) refreshAcpLimits(id string) {
	src := s.acpLimitsSource(id)
	if src == nil || !s.acpNoPermissionOpen(id) {
		return
	}
	p := &s.acp.limits
	p.mu.Lock()
	defer p.mu.Unlock()
	if s.ctx.Err() != nil || p.off[id] || p.running[id] {
		return
	}
	now := p.time()
	if last, ok := p.last[id]; ok && now.Sub(last) < acpLimitsInterval {
		return
	}
	if p.running == nil {
		p.running, p.last, p.off = map[string]bool{}, map[string]time.Time{}, map[string]bool{}
	}
	p.running[id], p.last[id] = true, now
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		s.runAcpLimits(id, src)
		p.mu.Lock()
		delete(p.running, id)
		p.mu.Unlock()
	}()
}

// runAcpLimits asks once and publishes the outcome to the registry and the agent's sessions. A failure keeps the last
// numbers, quietly.
func (s *Server) runAcpLimits(id string, src acp.LimitsSource) {
	reg := s.AcpRegistry()
	in := reg.State(id).Installed
	if in == nil {
		return
	}
	launch, err := reg.Launch(id)
	if err != nil {
		return
	}
	// A neutral folder: the agent must not pick up a project's settings or leave anything in one.
	dir, err := os.MkdirTemp("", "harness-acp-limits-")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	name := id
	if a, ok := reg.Cached().Find(id); ok {
		name = a.Name
	}
	ctx, cancel := context.WithTimeout(s.ctx, acpLimitsTimeout)
	defer cancel()
	lim, err := src.Fetch(ctx, acp.Config{AgentID: id, AgentName: name, AgentVersion: in.Version, Launch: launch, Cwd: dir, Logger: s.log()},
		func() bool { return s.acpNoPermissionOpen(id) })
	if errors.Is(err, acp.ErrLimitsCostTokens) {
		p := &s.acp.limits
		p.mu.Lock()
		p.off[id] = true
		p.mu.Unlock()
		s.log().Warn("acp limits are no longer asked for", "agent", id, "err", err)
		return
	}
	if err != nil {
		s.log().Debug("acp limits fetch failed", "agent", id, "err", err)
		return
	}
	if lim == nil {
		s.log().Debug("acp limits fetch found nothing", "agent", id)
		return
	}
	if err := reg.SetLimits(id, lim); err != nil {
		s.log().Debug("acp limits not stored", "agent", id, "err", err)
	}
	for _, l := range s.acpSessionsOf(id) {
		l.sess.ApplyLimits(lim)
	}
}
