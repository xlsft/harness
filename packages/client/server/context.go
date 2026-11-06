package server

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/agent"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/mcp"
)

// GetContextUsage reports what fills the session's context window (agent.ContextUsage for the counts and how
// they are estimated, plus the MCP tool catalog of the session).
func (s *Server) GetContextUsage(ctx context.Context, req *connect.Request[agentv1.GetContextUsageRequest]) (*connect.Response[agentv1.GetContextUsageResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	u := contextUsageToProto(ls.sess.Agent.ContextUsage(), ls.sess.MCP.Catalog())
	u.Compacting = ls.compacting.Load()
	return connect.NewResponse(&agentv1.GetContextUsageResponse{Usage: u}), nil
}

// contextUsageToProto builds the breakdown: categories in display order (summing to the window when it is
// known), the deferred definitions and the MCP tool list. The harness has no skills yet: they are left out, and no
// built-in tool is deferred.
func contextUsageToProto(u agent.ContextUsage, catalog []mcp.ToolInfo) *agentv1.ContextUsage {
	out := &agentv1.ContextUsage{
		Model: u.Model, ContextWindow: int64(u.Window), UsedTokens: int64(u.Used()),
		AutocompactThreshold: int64(u.Threshold), TokensUntilAutocompact: int64(u.UntilAutoCompact()), Estimated: u.Estimated,
	}
	add := func(c agentv1.ContextUsage_Category, n int) {
		out.Categories = append(out.Categories, &agentv1.ContextUsage_Entry{Category: c, Tokens: int64(n)})
	}
	add(agentv1.ContextUsage_CATEGORY_MESSAGES, u.Messages)
	add(agentv1.ContextUsage_CATEGORY_MCP_TOOLS, u.MCPTools)
	add(agentv1.ContextUsage_CATEGORY_SYSTEM_TOOLS, u.SystemTools)
	add(agentv1.ContextUsage_CATEGORY_SYSTEM_PROMPT, u.SystemPrompt)
	add(agentv1.ContextUsage_CATEGORY_MCP_SERVER_INSTRUCTIONS, u.MCPInstructions)
	add(agentv1.ContextUsage_CATEGORY_AUTOCOMPACT_BUFFER, u.Buffer())
	add(agentv1.ContextUsage_CATEGORY_FREE_SPACE, u.Free())
	for _, t := range catalog {
		n := agent.EstimateToolTokens(t.Def)
		out.McpTools = append(out.McpTools, &agentv1.ContextUsage_McpTool{
			Server: t.Server, Tool: t.Tool, Name: t.Def.Name, Description: t.Def.Description, Tokens: int64(n), Deferred: !t.Offered,
		})
		if !t.Offered {
			out.DeferredMcpToolsTokens += int64(n)
		}
	}
	return out
}

// compactCommand runs "/compact [instructions]" sent as a message: in the background, like the Compact RPC (the
// Compacted event reports it, a notice reports a failure); refused while a turn or another compaction runs.
func (ls *liveSession) compactCommand(instructions string) error {
	if ls.driver.Busy() || ls.compacting.Load() {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("/compact runs when the session is idle; wait for the current turn to finish"))
	}
	ls.mu.Lock()
	if ls.closed {
		ls.mu.Unlock()
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the session is closing"))
	}
	ls.bg.Add(1)
	ls.mu.Unlock()
	go func() {
		defer ls.bg.Done()
		_, err := ls.runCompaction(func() (agent.CompactInfo, error) { return ls.sess.Agent.Compact(ls.ctx, instructions) })
		if err != nil && ls.ctx.Err() == nil {
			ls.notice(agentv1.Notice_LEVEL_WARNING, "compaction_failed", "Compaction failed: "+err.Error())
		}
	}()
	return nil
}

// errCompacting refuses a compaction while another one runs in the session.
var errCompacting = errors.New("the session is already being compacted")

// runCompaction runs f (a compaction) while no turn runs, with the session marked compacting and working so
// frontends show it as busy. It fails with agent.ErrBusy while a turn runs and with errCompacting while another
// compaction runs. Messages sent meanwhile are queued and start a turn when it is done.
func (ls *liveSession) runCompaction(f func() (agent.CompactInfo, error)) (agent.CompactInfo, error) {
	if !ls.compacting.CompareAndSwap(false, true) {
		return agent.CompactInfo{}, errCompacting
	}
	defer ls.compacting.Store(false)
	var info agent.CompactInfo
	var cerr error
	err := ls.driver.Exclusive(func() {
		ls.setStatus(agentv1.SessionStatus_SESSION_STATUS_WORKING)
		info, cerr = f()
		if ls.sess.Agent.QueueLen() == 0 || ls.driver.Held() {
			ls.setStatus(agentv1.SessionStatus_SESSION_STATUS_IDLE)
		}
	})
	if err != nil {
		return info, err
	}
	return info, cerr
}
