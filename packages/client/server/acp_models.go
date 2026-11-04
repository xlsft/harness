package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/acp"
	"github.com/xlsft/harness/packages/client/acp/registry"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// An ACP agent's models, which are known only once it has a session. A short-lived probe asks the agent (see
// acp.ProbeModels) in the background; the outcome is cached per agent in HARNESS_HOME/acp/state.json and listed by
// ListAcpAgents, which never waits for it. Like everything about ACP agents it stays on this machine: it is not
// part of Initialize's catalog, not sent to the harness server and not shown to admins.
//
// Probes run on install and update, after a sign-in, on a manual refresh (ListAcpAgents.refresh) and when an agent
// is listed that was not checked at its installed version yet. A real session that reports other models updates the
// cache directly. A failed probe is not retried until one of those happens: an agent that cannot start must not be
// started again on every listing.

// acpSessionSwitchRefusal is why a running harness session cannot pick an agent's model (ListAcpAgents, SetModel).
const acpSessionSwitchRefusal = "Start a new session to use this agent"

const (
	// acpProbeTimeout bounds a probe. Long enough for npx or uvx to fetch the agent's package the first time.
	acpProbeTimeout = 2 * time.Minute
	// maxAcpProbes bounds the agent processes started at once for probes.
	maxAcpProbes = 2
	// maxAcpProbeError bounds the stored failure, which may quote the agent's stderr.
	maxAcpProbeError = 500
)

// acpProbes runs one probe per agent at a time; a request during a probe runs another one after it.
type acpProbes struct {
	mu      sync.Mutex
	running map[string]bool
	again   map[string]bool
	slots   chan struct{}
	wg      sync.WaitGroup
}

func (p *acpProbes) active(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running[id]
}

func (p *acpProbes) wait() { p.wg.Wait() }

// probeAcpModels starts a probe of the agent in the background.
func (s *Server) probeAcpModels(id string) {
	p := &s.acp.probes
	p.mu.Lock()
	defer p.mu.Unlock()
	if s.ctx.Err() != nil {
		return
	}
	if p.running == nil {
		p.running, p.again, p.slots = map[string]bool{}, map[string]bool{}, make(chan struct{}, maxAcpProbes)
	}
	if p.running[id] {
		p.again[id] = true
		return
	}
	p.running[id] = true
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case p.slots <- struct{}{}:
				s.runAcpProbe(id)
				<-p.slots
			case <-s.ctx.Done():
			}
			p.mu.Lock()
			if !p.again[id] || s.ctx.Err() != nil {
				delete(p.running, id)
				delete(p.again, id)
				p.mu.Unlock()
				return
			}
			delete(p.again, id)
			p.mu.Unlock()
		}
	}()
}

// runAcpProbe probes the agent and records the outcome: a failure keeps the previous list and says why.
func (s *Server) runAcpProbe(id string) {
	reg := s.AcpRegistry()
	in := reg.State(id).Installed
	if in == nil {
		return
	}
	version := in.Version
	var res acp.Probe
	err := func() error {
		launch, err := reg.Launch(id)
		if err != nil {
			return err
		}
		// A neutral folder: the agent must not pick up a project's settings or leave anything in one.
		dir, err := os.MkdirTemp("", "harness-acp-probe-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		name := id
		if a, ok := reg.Cached().Find(id); ok {
			name = a.Name
		}
		ctx, cancel := context.WithTimeout(s.ctx, acpProbeTimeout)
		defer cancel()
		res, err = acp.ProbeModels(ctx, acp.Config{AgentID: id, AgentName: name, AgentVersion: version, Launch: launch, Cwd: dir, Logger: s.log(),
			OnAuth: func(state registry.AuthState, methods []registry.AuthMethod) { _ = reg.SetAuth(id, state, methods) }})
		return err
	}()
	if err != nil && s.ctx.Err() != nil {
		return // Shutting down: says nothing about the agent.
	}
	if err != nil && !errors.Is(err, acp.ErrAuthRequired) {
		s.log().Warn("acp model probe failed", "agent", id, "err", err)
	}
	_, uerr := reg.UpdateCatalog(id, version, func(c *registry.Catalog) {
		c.Version = version
		c.Error, c.NeedsAuth = "", false
		switch {
		case err == nil:
			c.Models, c.CurrentModel, c.Modes, c.CurrentMode = res.Models, res.CurrentModel, res.Modes, res.CurrentMode
			c.Efforts, c.CurrentEffort = res.Efforts, res.CurrentEffort
			if c.CurrentModel == "" && len(c.Models) > 0 {
				c.CurrentModel = c.Models[0].ID
			}
			c.UpdatedAt = time.Now().UTC()
		case errors.Is(err, acp.ErrAuthRequired):
			c.NeedsAuth = true
		default:
			c.Error = truncateProbeError(err.Error())
		}
	})
	if uerr != nil {
		s.log().Warn("acp model cache not written", "agent", id, "err", uerr)
	}
}

func truncateProbeError(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxAcpProbeError {
		return s
	}
	cut := maxAcpProbeError
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// scheduleAcpProbes starts the probes a listing calls for, without waiting for them: every installed agent on a
// manual refresh; otherwise agents that were not checked at their installed version and may be able to answer.
func (s *Server) scheduleAcpProbes(refresh bool) {
	reg := s.AcpRegistry()
	for _, in := range reg.InstalledAgents() {
		if _, err := reg.Launch(in.ID); err != nil {
			continue
		}
		if refresh {
			s.probeAcpModels(in.ID)
			continue
		}
		// In this order: a probe records its outcome before it stops counting as active.
		if s.acp.probes.active(in.ID) {
			continue
		}
		if st := reg.State(in.ID); st.Auth != registry.AuthRequired && (st.Catalog == nil || st.Catalog.Version != in.Version) {
			s.probeAcpModels(in.ID)
		}
	}
}

// applyAcpModels fills in an agent's models, their status and whether it belongs in the model list.
func (s *Server) applyAcpModels(out *agentv1.AcpAgent, id string, st registry.AgentState) {
	out.ModelsStatus = agentv1.AcpModelsStatus_ACP_MODELS_STATUS_NOT_PROBED
	if st.Installed == nil {
		return
	}
	if _, err := s.AcpRegistry().Launch(id); err == nil {
		out.Selectable = st.Auth == registry.AuthNotRequired || st.Auth == registry.AuthAuthenticated
	}
	if c := st.Catalog; c != nil {
		out.Models, out.CurrentModelId = acp.ModelsToProto(c.Models), c.CurrentModel
		out.Modes, out.CurrentModeId = acp.ModesToProto(c.Modes), c.CurrentMode
		out.Efforts, out.CurrentEffortId = acp.EffortsToProto(c.Efforts), c.CurrentEffort
		if !c.UpdatedAt.IsZero() {
			out.ModelsUpdatedAt = c.UpdatedAt.UTC().Format(time.RFC3339)
		}
		switch {
		case c.Error != "":
			out.ModelsStatus, out.ModelsError = agentv1.AcpModelsStatus_ACP_MODELS_STATUS_FAILED, c.Error
		case c.NeedsAuth:
			out.ModelsStatus = agentv1.AcpModelsStatus_ACP_MODELS_STATUS_NEEDS_AUTH
		case c.Version != "":
			out.ModelsStatus = agentv1.AcpModelsStatus_ACP_MODELS_STATUS_OK
		}
	}
	if st.Auth == registry.AuthRequired {
		out.ModelsStatus, out.ModelsError = agentv1.AcpModelsStatus_ACP_MODELS_STATUS_NEEDS_AUTH, ""
	}
	if s.acp.probes.active(id) {
		out.ModelsStatus = agentv1.AcpModelsStatus_ACP_MODELS_STATUS_PROBING
	}
}

// acpSessionState is SessionInfo.acp of an open session. While the agent has not said (it starts, or waits for a
// sign-in), what it last reported stands in, so the session offers its models, modes and efforts from the first moment.
func (s *Server) acpSessionState(cfg acp.Config, st acp.State) *agentv1.AcpSessionState {
	out := acp.StateToProto(cfg, st)
	c := s.AcpRegistry().State(cfg.AgentID).Catalog
	if c == nil {
		return out
	}
	if open := st.AgentSessionID != "" && !st.Starting && !st.NeedsAuth; !open && len(out.Efforts) == 0 {
		out.Efforts = acp.EffortsToProto(c.Efforts)
		if out.CurrentEffortId == "" {
			out.CurrentEffortId = c.CurrentEffort
		}
	}
	if len(out.Models) > 0 {
		return out
	}
	out.Models = acp.ModelsToProto(c.Models)
	if out.CurrentModelId == "" {
		out.CurrentModelId = c.CurrentModel
	}
	if len(out.Modes) == 0 {
		out.Modes = acp.ModesToProto(c.Modes)
		if out.CurrentModeId == "" {
			out.CurrentModeId = c.CurrentMode
		}
	}
	return out
}

// observeState keeps what follows from a session's state: the model, mode and effort to restore on a resume, and the agent's
// catalog when the agent offers something else than last reported. Off this goroutine, which delivers the session's
// events: the registry's lock can be held across a registry fetch.
func (l *acpLive) observeState() {
	st := l.sess.State()
	if st.AgentSessionID == "" || st.Starting || st.NeedsAuth {
		return
	}
	models := acp.ModelsOrDefault(st.Models)
	key := fmt.Sprint(models, st.Modes, st.Efforts)
	l.mu.Lock()
	first, changed := l.reported == "", l.reported != key
	l.reported = key
	stored := l.meta.Model != st.CurrentModel || l.meta.Mode != st.CurrentMode || l.meta.Effort != st.CurrentEffort
	if stored {
		l.meta.Model, l.meta.Mode, l.meta.Effort = st.CurrentModel, st.CurrentMode, st.CurrentEffort
	}
	meta := l.meta
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return
	}
	if stored {
		_ = writeAcpMeta(meta)
	}
	if !first && !changed {
		return
	}
	cfg := l.sess.Config()
	go func() {
		_, err := l.srv.AcpRegistry().UpdateCatalog(cfg.AgentID, cfg.AgentVersion, func(c *registry.Catalog) {
			c.Models, c.Modes = models, st.Modes
			if !hasAcpModel(models, c.CurrentModel) {
				c.CurrentModel = st.CurrentModel
			}
			if c.CurrentModel == "" {
				c.CurrentModel = models[0].ID
			}
			if !hasAcpMode(st.Modes, c.CurrentMode) {
				c.CurrentMode = st.CurrentMode
			}
			// Levels can depend on the session's model: one without any says nothing about the agent's other models.
			if len(st.Efforts) > 0 {
				c.Efforts = st.Efforts
				if !hasAcpEffort(st.Efforts, c.CurrentEffort) {
					c.CurrentEffort = st.CurrentEffort
				}
			}
			c.Version, c.Error, c.NeedsAuth = cfg.AgentVersion, "", false
			c.UpdatedAt = time.Now().UTC()
		})
		if err != nil {
			l.srv.log().Warn("acp model cache not written", "agent", cfg.AgentID, "err", err)
		}
	}()
}

func hasAcpModel(models []acp.Model, id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}

func hasAcpMode(modes []acp.Mode, id string) bool {
	for _, m := range modes {
		if m.ID == id {
			return true
		}
	}
	return false
}

func hasAcpEffort(efforts []acp.Effort, id string) bool {
	for _, e := range efforts {
		if e.ID == id {
			return true
		}
	}
	return false
}

// effortRefusal says why an ACP session cannot take an effort level: only the agent's own levels exist there.
func (l *acpLive) effortRefusal(level string) error {
	cfg := l.sess.Config()
	st := l.srv.acpSessionState(cfg, l.sess.State())
	refuse := func(why string) error { return connect.NewError(connect.CodeFailedPrecondition, errors.New(why)) }
	offered := st.GetEfforts()
	if len(offered) == 0 {
		if st.GetStarting() || st.GetNeedsAuth() {
			return nil // Nothing known yet: the choice waits for the agent, which has the last word.
		}
		return refuse(fmt.Sprintf("%s does not offer effort levels", cfg.AgentName))
	}
	for _, e := range offered {
		if e.GetId() == level {
			return nil
		}
	}
	return refuse(fmt.Sprintf("%s has no effort level %q", cfg.AgentName, level))
}

// modelRefusal says why an ACP session cannot take a model: a session stays on its agent. engine is the engine the
// caller says the model belongs to (empty: this session's).
func (l *acpLive) modelRefusal(engine, model string) error {
	cfg := l.sess.Config()
	own := enginePrefix + cfg.AgentID
	refuse := func(what string) error {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this session runs on %s and cannot switch to %s; start a new session to use it", cfg.AgentName, what))
	}
	if engine != "" && engine != own {
		if strings.HasPrefix(engine, enginePrefix) {
			return refuse("another agent")
		}
		return refuse(fmt.Sprintf("the server's model %q", model))
	}
	offered := l.srv.acpSessionState(cfg, l.sess.State()).GetModels()
	if len(offered) == 0 {
		return nil // Nothing known yet: the choice waits for the agent, which has the last word.
	}
	for _, m := range offered {
		if m.GetId() == model {
			return nil
		}
	}
	return refuse(fmt.Sprintf("%q, which is not one of its models", model))
}

// setHarnessModel is SetModel for a session that does not run on an ACP agent here: an agent's model is refused,
// also when named without its engine and the catalog does not know it.
func (r acpRouted) setHarnessModel(ctx context.Context, req *connect.Request[agentv1.SetModelRequest]) (*connect.Response[agentv1.SetModelResponse], error) {
	m := req.Msg
	refuse := func(agent string) error {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this session does not run on %s and cannot switch to its model %q: %s", agent, m.GetModel(), strings.ToLower(acpSessionSwitchRefusal)))
	}
	if e := m.GetEngine(); strings.HasPrefix(e, enginePrefix) {
		return nil, refuse(strings.TrimPrefix(e, enginePrefix))
	}
	res, err := r.routed.SetModel(ctx, req)
	if connect.CodeOf(err) == connect.CodeInvalidArgument {
		if agent := r.agentOfModel(m.GetModel()); agent != "" {
			return nil, refuse(agent)
		}
	}
	return res, err
}

// agentOfModel returns the installed agent whose cached list has the model ("" when none).
func (s *Server) agentOfModel(model string) string {
	reg := s.AcpRegistry()
	for _, in := range reg.InstalledAgents() {
		if c := reg.State(in.ID).Catalog; c != nil && hasAcpModel(c.Models, model) {
			return in.ID
		}
	}
	return ""
}
