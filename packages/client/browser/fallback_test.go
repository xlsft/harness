package browser

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "regenerate fallback_tools.json from packages/core/prompts")

// promptTools reads the browser tools of the prompt bundle sources, as the bundle builder trims them.
func promptTools(t *testing.T) map[string]fallbackTool {
	dir := filepath.Join("..", "..", "core", "prompts", "tools")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("packages/core/prompts is not next to this module")
	}
	out := map[string]fallbackTool{}
	for _, name := range ToolNames {
		desc, err := os.ReadFile(filepath.Join(dir, name, "description.md"))
		if err != nil {
			t.Fatal(err)
		}
		schema, err := os.ReadFile(filepath.Join(dir, name, "schema.json"))
		if err != nil {
			t.Fatal(err)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, schema); err != nil {
			t.Fatalf("%s/schema.json: %v", name, err)
		}
		out[name] = fallbackTool{Description: strings.TrimRight(string(desc), " \t\r\n"), InputSchema: compact.Bytes()}
	}
	return out
}

func TestFallbackMatchesPromptBundle(t *testing.T) {
	want := promptTools(t)
	if *update {
		b, err := json.MarshalIndent(want, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("fallback_tools.json", append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	defs := FallbackToolDefs()
	if len(defs) != len(ToolNames) {
		t.Fatalf("fallback has %d tools, want %d (run go test ./browser -run Fallback -update)", len(defs), len(ToolNames))
	}
	for i, d := range defs {
		if d.Name != ToolNames[i] {
			t.Fatalf("fallback order: %s at %d", d.Name, i)
		}
		w := want[d.Name]
		var got bytes.Buffer
		_ = json.Compact(&got, d.InputSchema)
		if d.Description != w.Description || got.String() != string(w.InputSchema) {
			t.Errorf("fallback of %s is out of date (run go test ./browser -run Fallback -update)", d.Name)
		}
	}
	names := map[string]bool{}
	for _, tl := range (&Manager{}).Tools() {
		names[tl.Name()] = true
	}
	for _, n := range ToolNames {
		if !names[n] {
			t.Errorf("%s has no implementation", n)
		}
	}
}
