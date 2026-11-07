package server

import (
	"context"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/doctor"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/permissions"
)

// RunDoctor runs the checks of `harness-client doctor` for Settings > About. The policy is the one this client enforces
// live, and the MCP servers are this client's inventory, so the answer is about this running client.
func (s *Server) RunDoctor(ctx context.Context, req *connect.Request[agentv1.RunDoctorRequest]) (*connect.Response[agentv1.RunDoctorResponse], error) {
	var o doctor.Options
	if s.DoctorOptions != nil {
		o = s.DoctorOptions(ctx)
	}
	if o.ClientVersion == "" {
		o.ClientVersion = config.Version
	}
	if o.DataDir == "" {
		o.DataDir = config.Home()
	}
	o.Policy = func(context.Context) (permissions.ManagedPolicy, bool) { return s.Base.ModePolicy.Managed(), true }
	if !req.Msg.GetSkipMcp() {
		inv := s.mcpInventory()
		folder := req.Msg.GetFolder()
		o.MCP = func(ctx context.Context) ([]doctor.MCPResult, error) {
			return doctor.ProbeMCP(ctx, inv, folder, 20*time.Second)
		}
	}
	return connect.NewResponse(doctor.Run(ctx, o).Proto()), nil
}
