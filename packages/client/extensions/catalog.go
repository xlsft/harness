// Package extensions joins what the Extensions screen shows: the skills, plugins and MCP servers of this device, and what could be
// added from the marketplaces, the public MCP registry and a small catalog that ships with the client. It decides nothing about what
// may run: packages plugins and mcp still do.
package extensions

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// The default catalog is what a first run can offer with no marketplace added and no network: real public marketplaces and
// connectors, in our own words. Nothing in it is added by itself.
//
//go:embed catalog.json
var catalogFile []byte

// CatalogMarketplace is a public marketplace that can be added.
type CatalogMarketplace struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Source      string   `json:"source"`
	Publisher   string   `json:"publisher"`
	Description string   `json:"description"`
	Homepage    string   `json:"homepage"`
	Kinds       []string `json:"kinds"`
	Featured    bool     `json:"featured"`
}

// CatalogConnector is a public MCP server that can be added: a URL, or a command.
type CatalogConnector struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Publisher   string   `json:"publisher"`
	Description string   `json:"description"`
	Homepage    string   `json:"homepage"`
	URL         string   `json:"url"`
	Transport   string   `json:"transport"`
	Command     string   `json:"command"`
	Args        []string `json:"args"`
	Featured    bool     `json:"featured"`
}

// Catalog is the default catalog.
type Catalog struct {
	Marketplaces []CatalogMarketplace `json:"marketplaces"`
	Connectors   []CatalogConnector   `json:"connectors"`
}

// DefaultCatalog reads the file that ships with the client.
func DefaultCatalog() (*Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(catalogFile, &c); err != nil {
		return nil, fmt.Errorf("the default catalog: %w", err)
	}
	return &c, nil
}
