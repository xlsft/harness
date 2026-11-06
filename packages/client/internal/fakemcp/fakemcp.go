// Package fakemcp is a tiny stdio MCP server for tests and the e2e script. Tools:
//
//	echo {text}        returns "echo: <text>" (plus the FAKE_MCP_SECRET env value when reveal=true)
//	add  {a, b}        returns the sum as text and structured content
//	fail {}            returns an isError result
package fakemcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// EnvMarker makes a test binary act as the fake server (see Serve).
const EnvMarker = "HARNESS_FAKE_MCP_SERVER"

// EnvExtras makes the fake server also offer what ExtendedServer adds.
const EnvExtras = "HARNESS_FAKE_MCP_EXTRAS"

// EnvModern lets ExtendedServer speak the newest protocol version (the tool ask_new then uses multi round-trip).
const EnvModern = "HARNESS_FAKE_MCP_MODERN"

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
func Server() *sdk.Server { return Server0(nil) }

// Server0 builds the fake server with options.
func Server0(o *sdk.ServerOptions) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "fake-mcp", Version: "1.0.0"}, o)
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
	if os.Getenv(EnvExtras) == "1" {
		return ExtendedServer().Run(context.Background(), &sdk.StdioTransport{})
	}
	return Server().Run(context.Background(), &sdk.StdioTransport{})
}

// Instructions is the text ExtendedServer offers the model.
const Instructions = "Call echo before anything else."

type askIn struct {
	Tries int `json:"tries,omitempty" jsonschema:"unused"`
}

type sleepIn struct {
	Ms int `json:"ms" jsonschema:"milliseconds"`
}

// ExtendedServer is Server plus instructions, a prompt, resources and the tools
//
//	ask {}          elicits a form and returns the action and the answers
//	ask_link {}     elicits a URL
//	slow {}         reports progress, then answers
//	grow {}         adds the tool grown (which sends tools/list_changed)
//	shrink {}       removes the tool add
//	touch {}        adds a prompt and a resource (which send their list_changed)
//	sleep {ms}      answers after ms milliseconds
//	exit {}         ends the process
//	large {}        returns 5000 characters
func ExtendedServer() *sdk.Server {
	o := &sdk.ServerOptions{Instructions: Instructions}
	if os.Getenv(EnvModern) != "1" {
		// Session.Elicit only works on the protocol versions before multi round-trip requests.
		o.SupportedProtocolVersions = []string{"2025-11-25"}
	}
	s := Server0(o)
	sdk.AddTool(s, &sdk.Tool{Name: "ask_new", Description: "Asks the user with an input-required result."},
		func(_ context.Context, req *sdk.CallToolRequest, _ askIn) (*sdk.CallToolResult, any, error) {
			if r, ok := req.Params.InputResponses["form"].(*sdk.ElicitResult); ok {
				b, _ := json.Marshal(r.Content)
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: r.Action + ":" + string(b)}}}, nil, nil
			}
			return &sdk.CallToolResult{InputRequests: sdk.InputRequestMap{"form": &sdk.ElicitParams{Mode: "form", Message: "Name?", RequestedSchema: map[string]any{
				"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []any{"name"},
			}}}}, nil, nil
		})
	s.AddPrompt(&sdk.Prompt{Name: "greet", Description: "Greets someone.", Arguments: []*sdk.PromptArgument{{Name: "name", Description: "Who", Required: true}, {Name: "tone"}}},
		func(_ context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
			return &sdk.GetPromptResult{Messages: []*sdk.PromptMessage{{Role: "user", Content: &sdk.TextContent{Text: fmt.Sprintf("Say hello to %s (%s).", req.Params.Arguments["name"], req.Params.Arguments["tone"])}}}}, nil
		})
	s.AddResource(&sdk.Resource{URI: "mem://hello", Name: "hello", Description: "A greeting", MIMEType: "text/plain"},
		func(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: req.Params.URI, MIMEType: "text/plain", Text: "hello from a resource"}}}, nil
		})
	s.AddResourceTemplate(&sdk.ResourceTemplate{URITemplate: "mem://items/{id}", Name: "item"},
		func(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: req.Params.URI, Text: "item at " + req.Params.URI}}}, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "ask", Description: "Asks the user."},
		func(ctx context.Context, req *sdk.CallToolRequest, _ askIn) (*sdk.CallToolResult, any, error) {
			r, err := req.Session.Elicit(ctx, &sdk.ElicitParams{Mode: "form", Message: "Tell me more", RequestedSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"colour": map[string]any{"type": "string", "enum": []any{"red", "blue"}, "title": "Colour"},
					"age":    map[string]any{"type": "integer", "minimum": 18},
					"ok":     map[string]any{"type": "boolean"},
					"note":   map[string]any{"type": "string"},
				},
				"required": []any{"colour", "age"},
			}})
			if err != nil {
				return nil, nil, err
			}
			b, _ := json.Marshal(r.Content)
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: r.Action + ":" + string(b)}}}, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "ask_link", Description: "Sends the user to a page."},
		func(ctx context.Context, req *sdk.CallToolRequest, _ askIn) (*sdk.CallToolResult, any, error) {
			r, err := req.Session.Elicit(ctx, &sdk.ElicitParams{Mode: "url", Message: "Sign in", URL: "https://auth.example/page", ElicitationID: "e1"})
			if err != nil {
				return nil, nil, err
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: r.Action}}}, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "slow", Description: "Reports progress."},
		func(ctx context.Context, req *sdk.CallToolRequest, _ askIn) (*sdk.CallToolResult, any, error) {
			for i := 1; i <= 2; i++ {
				_ = req.Session.NotifyProgress(ctx, &sdk.ProgressNotificationParams{ProgressToken: req.Params.GetProgressToken(), Progress: float64(i), Total: 2, Message: fmt.Sprintf("step %d", i)})
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "slow done"}}}, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "grow", Description: "Adds a tool."},
		func(context.Context, *sdk.CallToolRequest, askIn) (*sdk.CallToolResult, any, error) {
			sdk.AddTool(s, &sdk.Tool{Name: "grown", Description: "A new tool."},
				func(context.Context, *sdk.CallToolRequest, askIn) (*sdk.CallToolResult, any, error) {
					return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "grown"}}}, nil, nil
				})
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "grew"}}}, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "shrink", Description: "Removes a tool."},
		func(context.Context, *sdk.CallToolRequest, askIn) (*sdk.CallToolResult, any, error) {
			s.RemoveTools("add")
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "shrank"}}}, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "touch", Description: "Changes prompts and resources."},
		func(context.Context, *sdk.CallToolRequest, askIn) (*sdk.CallToolResult, any, error) {
			s.AddPrompt(&sdk.Prompt{Name: "later"}, func(context.Context, *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
				return &sdk.GetPromptResult{Messages: []*sdk.PromptMessage{{Role: "user", Content: &sdk.TextContent{Text: "later"}}}}, nil
			})
			s.AddResource(&sdk.Resource{URI: "mem://later", Name: "later"}, func(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
				return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: req.Params.URI, Text: "later"}}}, nil
			})
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "touched"}}}, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "sleep", Description: "Waits."},
		func(ctx context.Context, _ *sdk.CallToolRequest, in sleepIn) (*sdk.CallToolResult, any, error) {
			select {
			case <-ctx.Done():
			case <-time.After(time.Duration(in.Ms) * time.Millisecond):
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "slept"}}}, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "exit", Description: "Ends the process."},
		func(context.Context, *sdk.CallToolRequest, askIn) (*sdk.CallToolResult, any, error) {
			os.Exit(3)
			return nil, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "large", Description: "A long answer."},
		func(context.Context, *sdk.CallToolRequest, askIn) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: strings.Repeat("x", 5000)}}}, nil, nil
		})
	return s
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
