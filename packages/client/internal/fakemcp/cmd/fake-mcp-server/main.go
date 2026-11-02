// Command fake-mcp-server is the fakemcp stdio MCP server as a binary (scripts/e2e).
package main

import (
	"fmt"
	"os"

	"github.com/xlsft/harness/packages/client/internal/fakemcp"
)

func main() {
	if err := fakemcp.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
