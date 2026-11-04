package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/acp"
	"github.com/xlsft/harness/packages/client/acp/registry"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/terminal"
)

// The ACP agent RPCs (acp.proto). All state is device-local (HARNESS_HOME/acp).

func distributionProto(k registry.Kind) agentv1.AcpDistribution {
	switch k {
	case registry.KindBinary:
		return agentv1.AcpDistribution_ACP_DISTRIBUTION_BINARY
	case registry.KindNpx:
		return agentv1.AcpDistribution_ACP_DISTRIBUTION_NPX
	case registry.KindUvx:
		return agentv1.AcpDistribution_ACP_DISTRIBUTION_UVX
	}
	return agentv1.AcpDistribution_ACP_DISTRIBUTION_UNSPECIFIED
}

// acpAgentProto merges a registry entry (zero when the registry no longer lists it) with the local state.
func (s *Server) acpAgentProto(id string, a *registry.Agent) *agentv1.AcpAgent {
	reg := s.AcpRegistry()
	st := reg.State(id)
	out := &agentv1.AcpAgent{Id: id, Name: id, Engine: enginePrefix + id, AuthState: acp.AuthStateToProto(st.Auth),
		AuthMethods: acp.AuthMethodsToProto(st.AuthMethods), IconSvg: reg.IconSVG(id), Limits: acp.LimitsToProto(st.Limits)}
	s.applyAcpModels(out, id, st)
	out.UnsupportedFeatures = acp.UnsupportedFeatures(len(out.Efforts) > 0)
	if a != nil {
		out.Name, out.Description, out.LatestVersion = a.Name, a.Description, a.Version
		out.Repository, out.Website, out.License, out.Authors, out.IconUrl = a.Repository, a.Website, a.License, a.Authors, a.Icon
		res := reg.Resolve(*a)
		out.Distribution, out.Runtime, out.Available, out.UnavailableReason = distributionProto(res.Kind), res.Runtime, res.Available, res.Reason
	}
	if in := st.Installed; in != nil {
		out.Installed, out.InstalledVersion = true, in.Version
		out.UpdateAvailable = a != nil && a.Version != in.Version
		if a == nil {
			out.Distribution = distributionProto(in.Kind)
			_, err := reg.Launch(id)
			out.Available = err == nil
			if err != nil {
				out.UnavailableReason = err.Error()
			}
		}
	}
	return out
}

func (s *Server) ListAcpAgents(ctx context.Context, req *connect.Request[agentv1.ListAcpAgentsRequest]) (*connect.Response[agentv1.ListAcpAgentsResponse], error) {
	reg := s.AcpRegistry()
	var snap registry.Snapshot
	if req.Msg.GetCachedOnly() && !req.Msg.GetRefresh() {
		snap.Registry = reg.Cached()
	} else {
		snap = reg.Load(ctx, req.Msg.GetRefresh())
	}
	s.scheduleAcpProbes(req.Msg.GetRefresh())
	resp := &agentv1.ListAcpAgentsResponse{Runtimes: reg.Runtimes(), SessionSwitchRefusal: acpSessionSwitchRefusal}
	if !snap.FetchedAt.IsZero() {
		resp.RegistryFetchedAt = snap.FetchedAt.UTC().Format(time.RFC3339)
	}
	if snap.Err != nil {
		resp.RegistryError = snap.Err.Error()
	}
	seen := map[string]bool{}
	if snap.Registry != nil {
		for i := range snap.Registry.Agents {
			a := snap.Registry.Agents[i]
			seen[a.ID] = true
			resp.Agents = append(resp.Agents, s.acpAgentProto(a.ID, &a))
		}
	}
	// Installed agents the registry does not list (any more, or offline without a cache).
	var extra []*agentv1.AcpAgent
	for _, in := range reg.InstalledAgents() {
		if !seen[in.ID] {
			extra = append(extra, s.acpAgentProto(in.ID, nil))
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].Id < extra[j].Id })
	resp.Agents = append(resp.Agents, extra...)
	return connect.NewResponse(resp), nil
}

func (s *Server) registryAgent(ctx context.Context, id string) (registry.Agent, error) {
	snap := s.AcpRegistry().Load(ctx, false)
	a, ok := snap.Registry.Find(id)
	if !ok {
		if snap.Err != nil {
			return registry.Agent{}, connect.NewError(connect.CodeUnavailable, snap.Err)
		}
		return registry.Agent{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("the ACP registry has no agent %q", id))
	}
	return a, nil
}

func phaseProto(p registry.Phase) agentv1.InstallAcpAgentResponse_Phase {
	switch p {
	case registry.PhaseResolving:
		return agentv1.InstallAcpAgentResponse_PHASE_RESOLVING
	case registry.PhaseDownloading:
		return agentv1.InstallAcpAgentResponse_PHASE_DOWNLOADING
	case registry.PhaseVerifying:
		return agentv1.InstallAcpAgentResponse_PHASE_VERIFYING
	case registry.PhaseExtracting:
		return agentv1.InstallAcpAgentResponse_PHASE_EXTRACTING
	case registry.PhaseDone:
		return agentv1.InstallAcpAgentResponse_PHASE_DONE
	}
	return agentv1.InstallAcpAgentResponse_PHASE_UNSPECIFIED
}

// installAcp installs a registry entry, sending progress; the last message carries the agent.
func (s *Server) installAcp(ctx context.Context, a registry.Agent, send func(*agentv1.InstallAcpAgentResponse) error) error {
	var sendErr error
	_, err := s.AcpRegistry().Install(ctx, a, func(p registry.Progress) {
		if sendErr != nil || p.Phase == registry.PhaseDone {
			return
		}
		sendErr = send(&agentv1.InstallAcpAgentResponse{Phase: phaseProto(p.Phase), BytesDone: p.BytesDone, BytesTotal: p.BytesTotal, Message: p.Message})
	})
	if err != nil {
		if errors.Is(err, registry.ErrUnavailable) {
			return connect.NewError(connect.CodeFailedPrecondition, err)
		}
		if ctx.Err() != nil {
			return connect.NewError(connect.CodeCanceled, ctx.Err())
		}
		return connect.NewError(connect.CodeUnavailable, err)
	}
	if sendErr != nil {
		return sendErr
	}
	s.probeAcpModels(a.ID)
	return send(&agentv1.InstallAcpAgentResponse{Phase: agentv1.InstallAcpAgentResponse_PHASE_DONE, Done: true,
		Message: a.Name + " " + a.Version + " installed", Agent: s.acpAgentProto(a.ID, &a)})
}

func (s *Server) InstallAcpAgent(ctx context.Context, req *connect.Request[agentv1.InstallAcpAgentRequest], stream *connect.ServerStream[agentv1.InstallAcpAgentResponse]) error {
	a, err := s.registryAgent(ctx, req.Msg.GetAgentId())
	if err != nil {
		return err
	}
	return s.installAcp(ctx, a, stream.Send)
}

func (s *Server) UpdateAcpAgent(ctx context.Context, req *connect.Request[agentv1.UpdateAcpAgentRequest], stream *connect.ServerStream[agentv1.UpdateAcpAgentResponse]) error {
	id := req.Msg.GetAgentId()
	st := s.AcpRegistry().State(id)
	if st.Installed == nil {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("ACP agent %s is not installed", id))
	}
	snap := s.AcpRegistry().Load(ctx, true)
	a, ok := snap.Registry.Find(id)
	if !ok {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("the ACP registry has no agent %q", id))
	}
	send := func(p *agentv1.InstallAcpAgentResponse) error {
		return stream.Send(&agentv1.UpdateAcpAgentResponse{Progress: p})
	}
	if a.Version == st.Installed.Version {
		return send(&agentv1.InstallAcpAgentResponse{Phase: agentv1.InstallAcpAgentResponse_PHASE_DONE, Done: true,
			Message: a.Name + " is up to date", Agent: s.acpAgentProto(id, &a)})
	}
	return s.installAcp(ctx, a, send)
}

func (s *Server) UninstallAcpAgent(ctx context.Context, req *connect.Request[agentv1.UninstallAcpAgentRequest]) (*connect.Response[agentv1.UninstallAcpAgentResponse], error) {
	id := req.Msg.GetAgentId()
	if err := s.AcpRegistry().Uninstall(id); err != nil {
		if errors.Is(err, registry.ErrNotInstalled) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("ACP agent %s is not installed", id))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var entry *registry.Agent
	if a, ok := s.AcpRegistry().Cached().Find(id); ok {
		entry = &a
	}
	return connect.NewResponse(&agentv1.UninstallAcpAgentResponse{Agent: s.acpAgentProto(id, entry)}), nil
}

// shellLine quotes a command for POSIX shells.
func shellLine(cmd string, args []string) string {
	q := func(s string) string {
		if s != "" && strings.IndexFunc(s, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:@%+,", r))
		}) < 0 {
			return s
		}
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	parts := []string{q(cmd)}
	for _, a := range args {
		parts = append(parts, q(a))
	}
	return strings.Join(parts, " ")
}

func (s *Server) AuthenticateAcpAgent(ctx context.Context, req *connect.Request[agentv1.AuthenticateAcpAgentRequest]) (*connect.Response[agentv1.AuthenticateAcpAgentResponse], error) {
	m := req.Msg
	reg := s.AcpRegistry()
	id := m.GetAgentId()
	var live *acpLive
	if sid := m.GetSessionId(); sid != "" {
		live = s.acpLive(sid)
		if live == nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("ACP session %s is not open", sid))
		}
		if id == "" {
			id = live.sess.Config().AgentID
		}
		if live.sess.Config().AgentID != id {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("session %s runs on %s, not %s", sid, live.sess.Config().AgentID, id))
		}
	}
	launch, err := reg.Launch(id)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	scratch := func() (acp.Config, func(), error) {
		dir, err := os.MkdirTemp("", "harness-acp-auth-")
		if err != nil {
			return acp.Config{}, nil, err
		}
		return acp.Config{AgentID: id, AgentName: id, Launch: launch, Cwd: dir, Logger: s.log(),
			OnAuth: func(st registry.AuthState, methods []registry.AuthMethod) { _ = reg.SetAuth(id, st, methods) }}, func() { os.RemoveAll(dir) }, nil
	}
	resp := &agentv1.AuthenticateAcpAgentResponse{}
	done := func() (*connect.Response[agentv1.AuthenticateAcpAgentResponse], error) {
		// A session waiting for the sign-in reports the models itself once it runs, in its own environment.
		if resp.Authenticated && live == nil {
			s.probeAcpModels(id)
		}
		var entry *registry.Agent
		if a, ok := reg.Cached().Find(id); ok {
			entry = &a
		}
		resp.Agent = s.acpAgentProto(id, entry)
		return connect.NewResponse(resp), nil
	}

	if m.GetVerify() {
		if live != nil {
			err = live.sess.Reopen(s.ctx)
		} else {
			cfg, cleanup, cerr := scratch()
			if cerr != nil {
				return nil, connect.NewError(connect.CodeInternal, cerr)
			}
			err = acp.VerifyScratch(ctx, cfg)
			cleanup()
		}
		switch {
		case err == nil:
			resp.Authenticated = true
		case errors.Is(err, acp.ErrAuthRequired):
		default:
			return nil, acpError(err)
		}
		return done()
	}

	methods := reg.State(id).AuthMethods
	if live != nil {
		methods = live.sess.State().AuthMethods
	}
	if len(methods) == 0 {
		// Never ran here: learn the methods first.
		cfg, cleanup, cerr := scratch()
		if cerr != nil {
			return nil, connect.NewError(connect.CodeInternal, cerr)
		}
		verr := acp.VerifyScratch(ctx, cfg)
		cleanup()
		if verr == nil {
			resp.Authenticated = true
			return done()
		}
		methods = reg.State(id).AuthMethods
	}
	var method *registry.AuthMethod
	for i := range methods {
		if methods[i].ID == m.GetMethodId() {
			method = &methods[i]
		}
	}
	if method == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s has no auth method %q", id, m.GetMethodId()))
	}
	switch method.Kind {
	case "env_var":
		resp.EnvVars = method.EnvVars
		return done()
	case "terminal":
		l, err := acp.TerminalLogin(launch, methods, method.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		home, _ := os.UserHomeDir()
		cwd := home
		if live != nil {
			cwd = live.meta.Cwd
		}
		resp.Terminal = &agentv1.AcpTerminalCommand{Command: l.Command, Args: l.Args, Env: l.Env, Cwd: cwd, ShellLine: shellLine(l.Command, l.Args)}
		if m.GetOpenTerminal() {
			sid := ""
			if live != nil {
				sid = live.id
			}
			mgr := s.terminals()
			t, err := mgr.Create(terminal.Options{SessionID: sid, Cwd: cwd, Title: "Sign in: " + id, Env: l.Env, Shell: l.Command, Args: l.Args})
			if err != nil {
				return nil, terminalError(err)
			}
			ticket, err := mgr.IssueTicket(t.ID())
			if err != nil {
				return nil, terminalError(err)
			}
			resp.TerminalInfo, resp.TerminalTicket = terminalInfo(t.Info()), ticket
		}
		return done()
	}
	// An agent-handled method.
	if live != nil {
		err = live.sess.Authenticate(s.ctx, method.ID)
	} else {
		cfg, cleanup, cerr := scratch()
		if cerr != nil {
			return nil, connect.NewError(connect.CodeInternal, cerr)
		}
		err = acp.AuthenticateScratch(ctx, cfg, method.ID)
		cleanup()
	}
	switch {
	case err == nil:
		resp.Authenticated = true
	case errors.Is(err, acp.ErrAuthRequired):
	default:
		return nil, acpError(err)
	}
	return done()
}
