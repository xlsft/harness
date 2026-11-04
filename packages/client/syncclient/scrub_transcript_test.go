package syncclient

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
)

// The scrub against a line as the transcript really writes it.
func TestScrubComputerUseOnATranscriptEntry(t *testing.T) {
	entry := session.Entry{Type: session.TypeUser, ToolUseResult: map[string]any{"computer_use": true, "width": 10}, Message: session.UserBlocks([]llm.ContentBlock{{
		Type: llm.BlockToolResult, ToolUseID: "t1", Content: []llm.ContentBlock{
			llm.TextBlock("Screenshot of Notes"),
			{Type: llm.BlockImage, Source: &llm.Source{Type: "base64", MediaType: "image/jpeg", Data: "SECRETPIXELS"}},
		},
	}})}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	got := scrubComputerUse(string(raw))
	if strings.Contains(got, "SECRETPIXELS") || !strings.Contains(got, screenshotKept) || !strings.Contains(got, "Screenshot of Notes") {
		t.Fatalf("%s", got)
	}
}
