package mcp

import (
	"context"
	"errors"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	"github.com/xlsft/harness/packages/client/importer"
)

// ImportTarget is where the import engine puts the MCP servers of other tools: servers of this device, added through
// the same path as Settings > MCP (Inventory.Create, which also records this device's approval).
type ImportTarget struct {
	Inv *Inventory
}

var _ importer.MCPTarget = ImportTarget{}

// Servers lists every server the inventory knows, whoever manages it; project servers are not included.
func (t ImportTarget) Servers(ctx context.Context) ([]importer.ExistingMCP, error) {
	list := t.Inv.List(ctx, "")
	out := make([]importer.ExistingMCP, 0, len(list.GetServers()))
	for _, e := range list.GetServers() {
		out = append(out, importer.ExistingMCP{ID: e.GetServer().GetId(), Name: e.GetServer().GetName(), ManagedBy: e.GetManagedBy(),
			CanEdit: e.GetCanEdit(), Version: e.GetServer().GetVersion()})
	}
	return out, nil
}

// Create adds a server to this device.
func (t ImportTarget) Create(ctx context.Context, s *mcpv1.Server, secrets *mcpv1.SecretChanges) error {
	_, err := t.Inv.Create(ctx, agentv1.McpServerSource_MCP_SERVER_SOURCE_DEVICE, s, secrets)
	return err
}

// Replace replaces an existing server's config, keeping its id (and, for a registry server, its scope).
func (t ImportTarget) Replace(ctx context.Context, id string, s *mcpv1.Server, secrets *mcpv1.SecretChanges, version int64) error {
	s.Id = id
	_, conflict, err := t.Inv.Update(ctx, s, secrets, version)
	if err == nil && conflict != nil {
		err = errors.New("it changed meanwhile; import again")
	}
	return err
}
