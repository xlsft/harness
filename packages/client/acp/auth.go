package acp

import (
	"context"
	"time"

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

// StateToProto converts a snapshot for SessionInfo.acp.
func StateToProto(cfg Config, st State) *agentv1.AcpSessionState {
	out := &agentv1.AcpSessionState{
		AgentId: cfg.AgentID, AgentName: cfg.AgentName, AgentVersion: cfg.AgentVersion,
		CurrentModeId: st.CurrentMode, NeedsAuth: st.NeedsAuth, AuthMethods: AuthMethodsToProto(st.AuthMethods),
		FreshAgentSession: st.Fresh, ContextUsedTokens: st.ContextUsed, ContextWindowTokens: st.ContextSize,
		Starting: st.Starting, CurrentModelId: st.CurrentModel, Limits: LimitsToProto(st.Limits),
	}
	for _, m := range st.Modes {
		out.Modes = append(out.Modes, &agentv1.AcpMode{Id: m.ID, Name: m.Name, Description: m.Description})
	}
	for _, c := range st.Commands {
		out.Commands = append(out.Commands, &agentv1.AcpCommand{Name: c.Name, Description: c.Description, InputHint: c.InputHint})
	}
	for _, m := range st.Models {
		out.Models = append(out.Models, &agentv1.AcpModel{Id: m.ID, Name: m.Name, Description: m.Description})
	}
	return out
}

// Config returns the session's configuration.
func (s *Session) Config() Config { return s.cfg }

// UnsupportedFeatures are the harness features an ACP session does not have.
func UnsupportedFeatures() []agentv1.SessionFeature {
	return []agentv1.SessionFeature{
		agentv1.SessionFeature_SESSION_FEATURE_REWIND,
		agentv1.SessionFeature_SESSION_FEATURE_FORK,
		agentv1.SessionFeature_SESSION_FEATURE_SUB_SESSIONS,
		agentv1.SessionFeature_SESSION_FEATURE_SIDE_CHAT,
		agentv1.SessionFeature_SESSION_FEATURE_COMPACTION,
		agentv1.SessionFeature_SESSION_FEATURE_CONTEXT_BREAKDOWN,
		agentv1.SessionFeature_SESSION_FEATURE_CLASSIFIER,
		agentv1.SessionFeature_SESSION_FEATURE_EFFORT,
		agentv1.SessionFeature_SESSION_FEATURE_OUTPUT_STYLES,
		agentv1.SessionFeature_SESSION_FEATURE_SHELL_COMMANDS,
		agentv1.SessionFeature_SESSION_FEATURE_BACKGROUND_TASKS,
		agentv1.SessionFeature_SESSION_FEATURE_MODEL_CATALOG,
		agentv1.SessionFeature_SESSION_FEATURE_QUESTIONS,
		agentv1.SessionFeature_SESSION_FEATURE_MOVE_FOLDER,
	}
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
