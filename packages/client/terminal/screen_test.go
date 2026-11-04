package terminal

import (
	"reflect"
	"strings"
	"testing"
)

func TestRenderScreen(t *testing.T) {
	x := strings.Repeat
	for _, c := range []struct {
		name string
		in   string
		want []string
	}{
		{"plain", "one\r\ntwo\r\n", []string{"one", "two"}},
		{"colors and titles go", "\x1b]0;my title\x07\x1b[1;32mgreen\x1b[0m \x1b]133;A\x1b\\text\r\n", []string{"green text"}},
		{"a progress bar overwrites itself", "10%\r50%\r100%\r\ndone\r\n", []string{"100%", "done"}},
		{"backspace and erase to the end", "hello\b\b\x1b[Kp!\r\n", []string{"help!"}},
		{"a redrawn prompt", "$ gti\r\x1b[K$ git status\r\n", []string{"$ git status"}},
		{"cursor up over a line", "aaa\r\nbbb\r\n\x1b[2A\x1b[2Kccc\r\n", []string{"ccc", "bbb"}},
		{"zsh's end of line mark", "%" + x(" ", 79) + "\r \r$ ls\r\n", []string{"$ ls"}},
		{"long lines are joined", x("x", 100) + "\r\nnext\r\n", []string{x("x", 100), "next"}},
		{"a line that fills the width does not wrap", x("y", 80) + "\r\nnext\r\n", []string{x("y", 80), "next"}},
		{"delete and insert characters", "abcdef\r\x1b[2C\x1b[2P\x1b[1@-\r\n", []string{"ab-ef"}},
		{"clear keeps nothing visible", "old\r\n\x1b[H\x1b[2J\x1b[3Jnew\r\n", []string{"new"}},
		{"the alternate screen is what shows", "shell\r\n\x1b[?1049h\x1b[2J\x1b[Hvim here", []string{"vim here"}},
		{"and the shell is back after it", "shell\r\n\x1b[?1049h\x1b[Hvim\x1b[?1049l$ \r\n", []string{"shell", "$"}},
		{"absolute positions", "\x1b[3;5Hmid\x1b[1;1Htop", []string{"top", "", "    mid"}},
		{"unicode", "привет\r\n日本語\r\n", []string{"привет", "日本語"}},
		{"after a wide character", "日本\r\x1b[4Cx\r\n", []string{"日本x"}},
		{"combining marks stay", "й\r\n", []string{"й"}},
		{"sequences cut at the end are dropped", "text\x1b[1;3", []string{"text"}},
		{"an unterminated OSC ends at the next sequence", "a\x1b]0;title\x1b[1mb", []string{"ab"}},
		{"controls and invalid bytes are dropped", "a\x07\x01\xffb\r\n", []string{"ab"}},
		{"bare line feed keeps the column", "ab\ncd\r\n", []string{"ab", "  cd"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := renderScreen([]byte(c.in), 80, 24); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestRenderScreenScrolling(t *testing.T) {
	in := "1\r\n2\r\n3\r\n4\r\n5\r\n"
	// More lines than rows: they all stay, as scrollback.
	if got := renderScreen([]byte(in), 10, 3); !reflect.DeepEqual(got, []string{"1", "2", "3", "4", "5"}) {
		t.Fatalf("scrollback: %q", got)
	}
	// A position is relative to the visible screen, not to the scrollback.
	if got := renderScreen([]byte(in+"\x1b[1;1Htop"), 10, 3); !reflect.DeepEqual(got, []string{"1", "2", "3", "top", "5"}) {
		t.Fatalf("visible screen: %q", got)
	}
	// A scroll region on the alternate screen: the header stays, the rest scrolls.
	alt := "\x1b[?1049h\x1b[2;3r\x1b[1;1Hhead\x1b[2;1Ha\r\nb\r\nc\r\n"
	if got := renderScreen([]byte(alt), 10, 3); !reflect.DeepEqual(got, []string{"head", "c"}) {
		t.Fatalf("scroll region: %q", got)
	}
}
