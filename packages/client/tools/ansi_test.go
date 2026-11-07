package tools

import (
	"context"
	"strings"
	"testing"
)

func TestStripTerminalCodes(t *testing.T) {
	cases := map[string]string{
		// vitest's coloured summary, as it reached the transcript.
		"\x1b[41m\x1b[1m FAIL \x1b[22m\x1b[49m test/a.test.ts\x1b[2m > \x1b[22mgroup": " FAIL  test/a.test.ts > group",
		"\x1b[2m Test Files \x1b[22m \x1b[1m\x1b[31m1 failed\x1b[39m\x1b[22m":         " Test Files  1 failed",
		"plain text, no codes": "plain text, no codes",
		"\x1b[38;5;208morange\x1b[0m \x1b[38:2::255:0:0mtrue\x1b[m": "orange true",
		"\x1b]0;window title\x07after":                              "after",
		"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\":       "link",
		"\x1b[2K\x1b[1Gprogress\x1b[?25l\x1b[?25h":                  "progress",
		"\x1b(Bcharset \x1b=keypad":                                 "charset keypad",
		"emoji 🙂 and ünïcödé\x1b[0m":                                "emoji 🙂 and ünïcödé",
	}
	for in, want := range cases {
		if got := StripTerminalCodes(in); got != want {
			t.Errorf("StripTerminalCodes(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTerminalStripperJoinsSplitSequences(t *testing.T) {
	in := "\x1b[31mred\x1b[0m and \x1b]0;title\x07done\x1b[1;32mgreen\x1b[0m"
	// Every split point, including inside each sequence.
	for cut := 0; cut <= len(in); cut++ {
		var s terminalStripper
		got := string(s.strip([]byte(in[:cut]))) + string(s.strip([]byte(in[cut:]))) + string(s.flush())
		if got != "red and donegreen" {
			t.Fatalf("cut at %d: %q", cut, got)
		}
	}
	// A lone ESC that never becomes a sequence is not held forever.
	var s terminalStripper
	got := string(s.strip([]byte("a\x1b"))) + string(s.strip([]byte("zb")))
	if got != "a\x1bzb" {
		t.Fatalf("lone escape: %q", got)
	}
}

func TestBashResultHasNoTerminalCodes(t *testing.T) {
	env := newEnv(t)
	r := call(t, env, &Bash{}, map[string]any{"description": "coloured output", "command": `printf '\033[41m\033[1m FAIL \033[22m\033[49m x\n'; printf '\033[31merr\033[0m\n' >&2`})
	out := text(r)
	if strings.Contains(out, "\x1b") || !strings.Contains(out, " FAIL  x") || !strings.Contains(out, "err") {
		t.Fatalf("result: %q", out)
	}
	for _, k := range []string{"stdout", "stderr"} {
		st, _ := r.Structured.(map[string]any)
		if s, _ := st[k].(string); strings.Contains(s, "\x1b") {
			t.Fatalf("%s keeps codes: %q", k, s)
		}
	}
}

func TestUserShellStreamsWithoutTerminalCodes(t *testing.T) {
	env := newEnv(t)
	var streamed strings.Builder
	res := env.Shell.RunUser(context.Background(), `printf '\033[32mok\033[0m\n'`, func(stream int, p []byte) { streamed.Write(p) })
	if strings.Contains(streamed.String(), "\x1b") || strings.TrimSpace(streamed.String()) != "ok" {
		t.Fatalf("streamed: %q", streamed.String())
	}
	if res.CombinedOutput() != "ok" {
		t.Fatalf("result: %q", res.CombinedOutput())
	}
}
