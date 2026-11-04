package syncclient

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScrubComputerUse(t *testing.T) {
	plain := `{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"image","source":{"data":"AAAA"}}]}]},"toolUseResult":{"file":1}}`
	if got := scrubComputerUse(plain); got != plain {
		t.Fatalf("an ordinary line changed: %s", got)
	}
	line := `{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"Screenshot"},{"type":"image","source":{"type":"base64","data":"SECRETPIXELS"}}]}]},"toolUseResult":{"computer_use":true,"width":10}}`
	got := scrubComputerUse(line)
	if strings.Contains(got, "SECRETPIXELS") || !strings.Contains(got, screenshotKept) {
		t.Fatalf("%s", got)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(got), &rec); err != nil || rec["uuid"] != "u1" {
		t.Fatalf("%v %v", err, rec)
	}
	if !strings.Contains(got, `"text":"Screenshot"`) {
		t.Fatalf("the text went too: %s", got)
	}
	if bad := `{"toolUseResult":{"computer_use":true}`; scrubComputerUse(bad) != bad {
		t.Fatal("a malformed line changed")
	}
}
