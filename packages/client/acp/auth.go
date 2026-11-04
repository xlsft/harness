package acp

import (
	"context"
	"time"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/xlsft/harness/packages/client/acp/registry"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// AuthenticateScratch signs an agent in from a scratch process (no harness session): spawn, initialize,
// authenticate with methodID, then check that a session starts. The process is stopped afterwards.
func AuthenticateScratch(ctx context.Context, cfg Config, methodID string) error {
	cfg.ResumeAgentSessionID = ""
	s := New(cfg)
	defer s.Close()
	return s.Authenticate(ctx, methodID)
}

// VerifyScratch starts an agent in a scratch process and checks that it opens a session without asking for a
// sign-in (ErrAuthRequired when it still asks). It also learns the agent's auth methods (cfg.OnAuth).
func VerifyScratch(ctx context.Context, cfg Config) error {
	cfg.ResumeAgentSessionID = ""
	s := New(cfg)
	defer s.Close()
	return s.Open(ctx)
}

// DefaultModelID and DefaultModelName are the one entry listed for an agent that reports no model options.
const (
	DefaultModelID   = "default"
	DefaultModelName = "Default"
)

// ModelsOrDefault returns what an agent with an open session offers: its models, else the single "Default" entry.
func ModelsOrDefault(models []Model) []Model {
	if len(models) > 0 {
		return models
	}
	return []Model{{ID: DefaultModelID, Name: DefaultModelName}}
}

// Probe is what a model probe learned about an agent.
type Probe struct {
	Models        []Model
	CurrentModel  string
	Modes         []Mode
	CurrentMode   string
	Efforts       []Effort
	CurrentEffort string
}

// ProbeModels finds out an agent's models and modes: it starts the agent in cfg.Cwd, creates a session, reads its
// config options and closes it. No prompt is sent. ErrAuthRequired when the agent asks for a sign-in first.
func ProbeModels(ctx context.Context, cfg Config) (Probe, error) {
	cfg.ResumeAgentSessionID, cfg.InitialModel, cfg.InitialMode, cfg.InitialEffort = "", "", "", ""
	cfg.Sink, cfg.OnLimits, cfg.OnAgentSession = nil, nil, nil
	s := New(cfg)
	defer s.Close()
	if err := s.Open(ctx); err != nil {
		return Probe{}, err
	}
	s.closeAgentSession(ctx)
	st := s.State()
	return Probe{Models: ModelsOrDefault(st.Models), CurrentModel: st.CurrentModel, Modes: st.Modes, CurrentMode: st.CurrentMode,
		Efforts: st.Efforts, CurrentEffort: st.CurrentEffort}, nil
}

// closeAgentSession ends the agent's session when the agent supports session/close, so that a probe leaves no
// session behind in the agent's own records; stopping the process follows either way.
func (s *Session) closeAgentSession(ctx context.Context) {
	s.mu.Lock()
	p, id, supported := s.proc, s.sessionID, s.caps.SessionCapabilities.Close != nil
	s.mu.Unlock()
	if p == nil || id == "" || !supported {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := p.conn.CloseSession(cctx, sdk.CloseSessionRequest{SessionId: id}); err != nil {
		s.log.Debug("acp session/close failed", "agent", s.cfg.AgentID, "err", err)
	}
}

// StateToProto converts a snapshot for SessionInfo.acp.
func StateToProto(cfg Config, st State) *agentv1.AcpSessionState {
	out := &agentv1.AcpSessionState{
		AgentId: cfg.AgentID, AgentName: cfg.AgentName, AgentVersion: cfg.AgentVersion,
		CurrentModeId: st.CurrentMode, NeedsAuth: st.NeedsAuth, AuthMethods: AuthMethodsToProto(st.AuthMethods),
		FreshAgentSession: st.Fresh, ContextUsedTokens: st.ContextUsed, ContextWindowTokens: st.ContextSize,
		Starting: st.Starting, CurrentModelId: st.CurrentModel, Limits: LimitsToProto(st.Limits),
	}
	out.Modes = ModesToProto(st.Modes)
	out.Efforts, out.CurrentEffortId = EffortsToProto(st.Efforts), st.CurrentEffort
	for _, c := range st.Commands {
		out.Commands = append(out.Commands, &agentv1.AcpCommand{Name: c.Name, Description: c.Description, InputHint: c.InputHint})
	}
	models := st.Models
	// Open, the agent's list is final: no model option means the single "Default" entry.
	if st.AgentSessionID != "" && !st.Starting && !st.NeedsAuth {
		models = ModelsOrDefault(models)
		if out.CurrentModelId == "" && len(st.Models) == 0 {
			out.CurrentModelId = DefaultModelID
		}
	}
	out.Models = ModelsToProto(models)
	return out
}

// ModelsToProto converts model choices.
func ModelsToProto(models []Model) []*agentv1.AcpModel {
	var out []*agentv1.AcpModel
	for _, m := range models {
		out = append(out, &agentv1.AcpModel{Id: m.ID, Name: m.Name, Description: m.Description})
	}
	return out
}

// EffortsToProto converts effort levels.
func EffortsToProto(efforts []Effort) []*agentv1.AcpEffort {
	var out []*agentv1.AcpEffort
	for _, e := range efforts {
		out = append(out, &agentv1.AcpEffort{Id: e.ID, Name: e.Name, Description: e.Description})
	}
	return out
}

// ModesToProto converts session modes.
func ModesToProto(modes []Mode) []*agentv1.AcpMode {
	var out []*agentv1.AcpMode
	for _, m := range modes {
		out = append(out, &agentv1.AcpMode{Id: m.ID, Name: m.Name, Description: m.Description})
	}
	return out
}

// Config returns the session's configuration.
func (s *Session) Config() Config { return s.cfg }

// UnsupportedFeatures are the harness features an ACP session does not have. Effort depends on the agent: it has it
// with an effort option of its own (effort).
func UnsupportedFeatures(effort bool) []agentv1.SessionFeature {
	out := []agentv1.SessionFeature{
		agentv1.SessionFeature_SESSION_FEATURE_REWIND,
		agentv1.SessionFeature_SESSION_FEATURE_FORK,
		agentv1.SessionFeature_SESSION_FEATURE_SUB_SESSIONS,
		agentv1.SessionFeature_SESSION_FEATURE_SIDE_CHAT,
		agentv1.SessionFeature_SESSION_FEATURE_COMPACTION,
		agentv1.SessionFeature_SESSION_FEATURE_CONTEXT_BREAKDOWN,
		agentv1.SessionFeature_SESSION_FEATURE_CLASSIFIER,
		agentv1.SessionFeature_SESSION_FEATURE_OUTPUT_STYLES,
		agentv1.SessionFeature_SESSION_FEATURE_SHELL_COMMANDS,
		agentv1.SessionFeature_SESSION_FEATURE_BACKGROUND_TASKS,
		agentv1.SessionFeature_SESSION_FEATURE_MODEL_CATALOG,
		agentv1.SessionFeature_SESSION_FEATURE_QUESTIONS,
		agentv1.SessionFeature_SESSION_FEATURE_MOVE_FOLDER,
		agentv1.SessionFeature_SESSION_FEATURE_WORKTREE,
	}
	if !effort {
		out = append(out, agentv1.SessionFeature_SESSION_FEATURE_EFFORT)
	}
	return out
}

// AuthStateToProto converts a stored sign-in state.
func AuthStateToProto(s registry.AuthState) agentv1.AcpAuthState {
	switch s {
	case registry.AuthNotRequired:
		return agentv1.AcpAuthState_ACP_AUTH_STATE_NOT_REQUIRED
	case registry.AuthRequired:
		return agentv1.AcpAuthState_ACP_AUTH_STATE_REQUIRED
	case registry.AuthAuthenticated:
		return agentv1.AcpAuthState_ACP_AUTH_STATE_AUTHENTICATED
	}
	return agentv1.AcpAuthState_ACP_AUTH_STATE_UNKNOWN
}

// LimitsToProto converts a limits snapshot (nil stays nil).
func LimitsToProto(l *registry.Limits) *agentv1.AcpAgentLimits {
	if l == nil {
		return nil
	}
	out := &agentv1.AcpAgentLimits{PlanName: l.PlanName, UpdatedAt: l.UpdatedAt.UTC().Format(time.RFC3339)}
	for _, w := range l.Windows {
		pw := &agentv1.AcpLimitWindow{Label: w.Label, UsedPercent: w.UsedPercent, WindowMinutes: w.WindowMinutes}
		if !w.ResetsAt.IsZero() {
			pw.ResetsAt = w.ResetsAt.UTC().Format(time.RFC3339)
		}
		out.Windows = append(out.Windows, pw)
	}
	return out
}
