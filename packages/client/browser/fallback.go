package browser

import (
	_ "embed"
	"encoding/json"
	"sort"

	"github.com/xlsft/harness/packages/client/llm"
)

// Tool definitions come from the prompt bundle (packages/core/prompts/tools/Browser*). A server whose bundle
// predates the browser tools sends none of them; the client then offers these fallbacks, a copy of the bundle
// files (kept in sync by TestFallbackMatchesPromptBundle; regenerate with `go test ./browser -run Fallback
// -update`).

//go:embed fallback_tools.json
var fallbackJSON []byte

// ToolNames are the browser tools, in registration order.
var ToolNames = []string{ToolNavigate, ToolSnapshot, ToolClick, ToolType, ToolKey, ToolScroll, ToolScreenshot, ToolEval, ToolTabs,
	ToolConsole, ToolNetwork, ToolWait}

type fallbackTool struct {
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// FallbackToolDefs returns the built-in definitions of the browser tools.
func FallbackToolDefs() []llm.ToolDef {
	var m map[string]fallbackTool
	if json.Unmarshal(fallbackJSON, &m) != nil {
		return nil
	}
	order := map[string]int{}
	for i, n := range ToolNames {
		order[n] = i
	}
	out := make([]llm.ToolDef, 0, len(m))
	for name, t := range m {
		out = append(out, llm.ToolDef{Name: name, Description: t.Description, InputSchema: t.InputSchema})
	}
	sort.Slice(out, func(i, j int) bool { return order[out[i].Name] < order[out[j].Name] })
	return out
}
