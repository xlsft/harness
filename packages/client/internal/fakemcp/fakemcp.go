// Package fakemcp is a tiny stdio MCP server for tests and the e2e script. Tools:
//
//	echo {text}        returns "echo: <text>" (plus the FAKE_MCP_SECRET env value when reveal=true)
//	add  {a, b}        returns the sum as text and structured content
//	fail {}            returns an isError result
package fakemcp

import (
	"context"
	"fmt"
	"os"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// EnvMarker makes a test binary act as the fake server (see Serve).
const EnvMarker = "HARNESS_FAKE_MCP_SERVER"

type echoIn struct {
	Text   string `json:"text" jsonschema:"text to echo back"`
	Reveal bool   `json:"reveal,omitempty" jsonschema:"also return the FAKE_MCP_SECRET environment value"`
}

type addIn struct {
	A float64 `json:"a" jsonschema:"first addend"`
	B float64 `json:"b" jsonschema:"second addend"`
}

type addOut struct {
	Sum float64 `json:"sum"`
}

// Server builds the fake server.
func Server() *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "fake-mcp", Version: "1.0.0"}, nil)
	sdk.AddTool(s, &sdk.Tool{Name: "echo", Description: "Echoes the given text back."},
		func(_ context.Context, _ *sdk.CallToolRequest, in echoIn) (*sdk.CallToolResult, any, error) {
			text := "echo: " + in.Text
			if in.Reveal {
				text += " secret=" + os.Getenv("FAKE_MCP_SECRET")
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}}, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "add", Description: "Adds two numbers."},
		func(_ context.Context, _ *sdk.CallToolRequest, in addIn) (*sdk.CallToolResult, addOut, error) {
			sum := in.A + in.B
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprint(sum)}}}, addOut{Sum: sum}, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "fail", Description: "Always fails."},
		func(_ context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "it failed"}}}, nil, nil
		})
	return s
}

// Serve runs the fake server on stdin/stdout until the client disconnects.
func Serve() error {
	return Server().Run(context.Background(), &sdk.StdioTransport{})
}

// MaybeServe runs the server and exits when EnvMarker is set (call it first in TestMain).
func MaybeServe() {
	if os.Getenv(EnvMarker) != "1" {
		return
	}
	if err := Serve(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
