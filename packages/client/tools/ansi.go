package tools

import (
	"bytes"
	"regexp"
)

// terminalCodeRe matches terminal control sequences: CSI (colours, cursor moves, erases), OSC (titles, hyperlinks), and
// the two-byte ESC sequences (charset selection, keypad modes). Command output reaches the model and the transcript as
// plain text, so these are noise there: the UI shows them as stray "[41m" and the model pays tokens for them.
var terminalCodeRe = regexp.MustCompile(`\x1b\[[0-9;:?<=>!]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()*+][0-9A-Za-z]|\x1b[=>78DEHMNOZc]`)

// StripTerminalCodes removes terminal control sequences from s and leaves everything else as it is.
func StripTerminalCodes(s string) string {
	if !containsEsc(s) {
		return s
	}
	return terminalCodeRe.ReplaceAllString(s, "")
}

func containsEsc(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			return true
		}
	}
	return false
}

// maxHeldEscape is how much of an unfinished sequence a terminalStripper keeps for the next chunk; a longer one
// is not a real sequence and goes out as it is.
const maxHeldEscape = 256

// terminalStripper strips terminal codes from output that arrives in chunks, where a sequence can be split between
// two of them: an unfinished sequence at the end of a chunk is held back and joined to the next.
type terminalStripper struct {
	held []byte
}

// strip returns the chunk's text without terminal codes, holding back an unfinished sequence at its end.
func (t *terminalStripper) strip(p []byte) []byte {
	b := append(t.held, p...)
	t.held = nil
	if i := bytes.LastIndexByte(b, 0x1b); i >= 0 && len(b)-i <= maxHeldEscape && !terminalCodeRe.Match(b[i:]) && unfinished(b[i:]) {
		t.held = append([]byte(nil), b[i:]...)
		b = b[:i]
	}
	if bytes.IndexByte(b, 0x1b) < 0 {
		return b
	}
	return terminalCodeRe.ReplaceAll(b, nil)
}

// flush returns what is still held back, stripped.
func (t *terminalStripper) flush() []byte {
	b := t.held
	t.held = nil
	return terminalCodeRe.ReplaceAll(b, nil)
}

// unfinished reports whether seq, starting at ESC, could still become a sequence with more bytes.
func unfinished(seq []byte) bool {
	if len(seq) == 1 {
		return true
	}
	switch seq[1] {
	case '[':
		for _, c := range seq[2:] {
			if c >= '@' && c <= '~' {
				return false
			}
		}
		return true
	case ']':
		return !bytes.ContainsAny(seq[2:], "\x07") && !bytes.Contains(seq[2:], []byte("\x1b\\"))
	case '(', ')', '*', '+':
		return len(seq) == 2
	}
	return false
}
