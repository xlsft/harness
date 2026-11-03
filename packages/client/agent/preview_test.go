package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/xlsft/harness/packages/client/llm"
)

// A long non-ASCII prompt is cut by runes: a byte cut leaves invalid UTF-8 that fails the event's proto marshal.
func TestPreviewCutsByRunes(t *testing.T) {
	p := preview([]llm.ContentBlock{{Type: llm.BlockText, Text: strings.Repeat("изучи ", 40)}})
	if !utf8.ValidString(p) || utf8.RuneCountInString(p) != 121 {
		t.Fatalf("preview %q: valid=%v runes=%d", p, utf8.ValidString(p), utf8.RuneCountInString(p))
	}
}
