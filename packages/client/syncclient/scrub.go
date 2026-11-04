package syncclient

import (
	"encoding/json"
	"strings"
)

// computerUseMarker is in the toolUseResult of a computer use result that holds screenshots (computeruse/tools.go).
const computerUseMarker = `"computer_use":true`

// screenshotKept stands in for a screenshot on the server's copy of a transcript.
const screenshotKept = "[screenshot kept on the machine that took it]"

// scrubComputerUse removes the screenshots of a computer use result from a transcript line before upload: what the
// agent saw of the machine's desktop stays on that machine, in the local transcript. Other lines come back unchanged,
// and so does one this cannot parse, which the uploader then skips as malformed.
func scrubComputerUse(line string) string {
	if !strings.Contains(line, computerUseMarker) {
		return line
	}
	var rec map[string]any
	if json.Unmarshal([]byte(line), &rec) != nil {
		return line
	}
	msg, _ := rec["message"].(map[string]any)
	blocks, _ := msg["content"].([]any)
	for _, raw := range blocks {
		block, _ := raw.(map[string]any)
		parts, _ := block["content"].([]any)
		for i, p := range parts {
			if part, _ := p.(map[string]any); part["type"] == "image" {
				parts[i] = map[string]any{"type": "text", "text": screenshotKept}
			}
		}
	}
	out, err := json.Marshal(rec)
	if err != nil {
		return line
	}
	return string(out)
}
