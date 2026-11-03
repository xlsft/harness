package acp

import (
	"context"
	"encoding/json"

	sdk "github.com/coder/acp-go-sdk"
)

// client answers the agent's requests for one session.
type client struct{ s *Session }

var _ sdk.Client = (*client)(nil)

func (c *client) SessionUpdate(ctx context.Context, n sdk.SessionNotification) error {
	c.s.update(n)
	return nil
}

func (c *client) RequestPermission(ctx context.Context, p sdk.RequestPermissionRequest) (sdk.RequestPermissionResponse, error) {
	return c.s.requestPermission(ctx, p)
}

func (c *client) ReadTextFile(ctx context.Context, p sdk.ReadTextFileRequest) (sdk.ReadTextFileResponse, error) {
	return c.s.readFile(p)
}

func (c *client) WriteTextFile(ctx context.Context, p sdk.WriteTextFileRequest) (sdk.WriteTextFileResponse, error) {
	return c.s.writeFile(p)
}

func (c *client) CreateTerminal(ctx context.Context, p sdk.CreateTerminalRequest) (sdk.CreateTerminalResponse, error) {
	id, err := c.s.terms.create(p, c.s.cfg.Cwd, c.s.cfg.Env)
	if err != nil {
		return sdk.CreateTerminalResponse{}, sdk.NewInternalError(map[string]any{"error": err.Error()})
	}
	return sdk.CreateTerminalResponse{TerminalId: id}, nil
}

func (c *client) TerminalOutput(ctx context.Context, p sdk.TerminalOutputRequest) (sdk.TerminalOutputResponse, error) {
	r, err := c.s.terms.get(p.TerminalId)
	if err != nil {
		return sdk.TerminalOutputResponse{}, err
	}
	out, truncated, exit := r.output()
	return sdk.TerminalOutputResponse{Output: out, Truncated: truncated, ExitStatus: exit}, nil
}

func (c *client) WaitForTerminalExit(ctx context.Context, p sdk.WaitForTerminalExitRequest) (sdk.WaitForTerminalExitResponse, error) {
	st, err := c.s.terms.wait(ctx, p.TerminalId)
	if err != nil {
		return sdk.WaitForTerminalExitResponse{}, err
	}
	return sdk.WaitForTerminalExitResponse{ExitCode: st.ExitCode, Signal: st.Signal}, nil
}

func (c *client) KillTerminal(ctx context.Context, p sdk.KillTerminalRequest) (sdk.KillTerminalResponse, error) {
	if err := c.s.terms.kill(p.TerminalId); err != nil {
		return sdk.KillTerminalResponse{}, err
	}
	return sdk.KillTerminalResponse{}, nil
}

func (c *client) ReleaseTerminal(ctx context.Context, p sdk.ReleaseTerminalRequest) (sdk.ReleaseTerminalResponse, error) {
	if err := c.s.terms.release(p.TerminalId); err != nil {
		return sdk.ReleaseTerminalResponse{}, sdk.NewInvalidParams(map[string]any{"terminalId": p.TerminalId, "error": err.Error()})
	}
	return sdk.ReleaseTerminalResponse{}, nil
}

// HandleExtensionMethod accepts the agent's extension notifications; those carrying subscription limits
// (e.g. rate-limit snapshots) are recorded, others are ignored as unknown.
func (c *client) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var v any
	if json.Unmarshal(params, &v) == nil && c.s.extension(v) {
		return map[string]any{}, nil
	}
	return nil, sdk.NewMethodNotFound(method)
}
