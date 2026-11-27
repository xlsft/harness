package doctor

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// Text writes the report as the CLI prints it: one pass/warn/fail line per check, a hint under a problem, a summary.
func (r Report) Text(w io.Writer) {
	fmt.Fprintf(w, "harness-client %s\n\n", r.ClientVersion)
	width := 0
	for _, c := range r.Checks {
		width = max(width, len(c.Title))
	}
	var warns, fails int
	for _, c := range r.Checks {
		fmt.Fprintf(w, "  %-4s  %-*s  %s\n", c.Status, width, c.Title, c.Detail)
		if c.Hint != "" && (c.Status == Warn || c.Status == Fail) {
			fmt.Fprintf(w, "        %-*s  → %s\n", width, "", c.Hint)
		}
		switch c.Status {
		case Warn:
			warns++
		case Fail:
			fails++
		}
	}
	fmt.Fprintln(w)
	switch {
	case fails == 0 && warns == 0:
		fmt.Fprintln(w, "Everything checked works.")
	default:
		fmt.Fprintf(w, "%s, %s.\n", plural(fails, "failure"), plural(warns, "warning"))
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Proto is the report as AgentService.RunDoctor answers it.
func (r Report) Proto() *agentv1.RunDoctorResponse {
	out := &agentv1.RunDoctorResponse{ClientVersion: r.ClientVersion, Status: protoStatus(r.Status)}
	for _, c := range r.Checks {
		out.Checks = append(out.Checks, &agentv1.DoctorCheck{Id: c.ID, Title: c.Title, Status: protoStatus(c.Status), Detail: c.Detail, Hint: c.Hint})
	}
	return out
}

func protoStatus(s Status) agentv1.DoctorStatus {
	switch s {
	case Pass:
		return agentv1.DoctorStatus_DOCTOR_STATUS_PASS
	case Warn:
		return agentv1.DoctorStatus_DOCTOR_STATUS_WARN
	case Fail:
		return agentv1.DoctorStatus_DOCTOR_STATUS_FAIL
	case Skip:
		return agentv1.DoctorStatus_DOCTOR_STATUS_SKIP
	}
	return agentv1.DoctorStatus_DOCTOR_STATUS_UNSPECIFIED
}

// MCPInventory is what the MCP check needs of mcp.Inventory.
type MCPInventory interface {
	List(ctx context.Context, folder string) *agentv1.ListMcpServersResponse
	Inspect(ctx context.Context, id, folder string) (*agentv1.InspectMcpServerResponse, error)
}

// ProbeMCP starts every server of the inventory a session would start, a few at a time, each within perServer.
func ProbeMCP(ctx context.Context, inv MCPInventory, folder string, perServer time.Duration) ([]MCPResult, error) {
	list := inv.List(ctx, folder)
	if len(list.GetServers()) == 0 && list.GetRegistryError() != "" {
		return nil, fmt.Errorf("%s", list.GetRegistryError())
	}
	results := make([]MCPResult, len(list.GetServers()))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, e := range list.GetServers() {
		name := e.GetServer().GetName()
		switch e.GetState() {
		case agentv1.McpServerState_MCP_SERVER_STATE_READY:
		case agentv1.McpServerState_MCP_SERVER_STATE_UNAVAILABLE:
			results[i] = MCPResult{Name: name, Detail: firstNonEmpty(e.GetStateDetail(), "unavailable")}
			continue
		default:
			results[i] = MCPResult{Name: name, Skipped: true, Detail: strings.ToLower(strings.TrimPrefix(e.GetState().String(), "MCP_SERVER_STATE_"))}
			continue
		}
		wg.Add(1)
		go func(i int, id, name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, perServer)
			defer cancel()
			res, err := inv.Inspect(cctx, id, folder)
			switch {
			case err != nil:
				results[i] = MCPResult{Name: name, Detail: err.Error()}
			case res.GetConnected():
				results[i] = MCPResult{Name: name, OK: true}
			default:
				results[i] = MCPResult{Name: name, Detail: firstNonEmpty(res.GetError(), "didn't answer")}
			}
		}(i, e.GetServer().GetId(), name)
	}
	wg.Wait()
	return results, nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
