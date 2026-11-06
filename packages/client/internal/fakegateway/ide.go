package fakegateway

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xlsft/harness/packages/client/llm"
)

// IDEScript is the model of the editor extension's tests (packages/vscode/test/editor). A message says what to do:
//
//	ide:write <name> <text>   Write the text to a file in the working directory
//	ide:diagnostics           call EditorDiagnostics
//
// A tool's result comes back as the reply; anything else is echoed with every text block of the message, so that a test
// can see what the client added to it.
func IDEScript() Responder {
	return func(n int, req *llm.Request) Reply {
		id := fmt.Sprintf("msg_%d", n)
		if results := LastToolResults(req); len(results) > 0 {
			return Reply{Events: Message(id, Text("result: "+ResultText(results[0])))}
		}
		var parts []string
		if len(req.Messages) > 0 {
			for _, b := range req.Messages[len(req.Messages)-1].Content {
				if b.Type == llm.BlockText {
					parts = append(parts, b.Text)
				}
			}
		}
		for _, text := range parts {
			switch {
			case strings.HasPrefix(text, "ide:write "):
				name, content, _ := strings.Cut(strings.TrimPrefix(text, "ide:write "), " ")
				return Reply{Events: Message(id, ToolUse(fmt.Sprintf("toolu_%d", n), "Write", map[string]any{"file_path": filepath.Join(Cwd(req), name), "content": content}))}
			case strings.HasPrefix(text, "ide:diagnostics"):
				return Reply{Events: Message(id, ToolUse(fmt.Sprintf("toolu_%d", n), "EditorDiagnostics", map[string]any{}))}
			}
		}
		return Reply{Events: Message(id, Text("echo: "+strings.Join(parts, "\n")))}
	}
}
