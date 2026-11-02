package tui

import (
	"testing"
	"time"
)

func TestParagraphCut(t *testing.T) {
	cases := []struct {
		in   string
		want string // text before the cut ("" = no cut)
	}{
		{"one line", ""},
		{"para one\n\npara two", "para one\n\n"},
		{"para one\n\n", ""},
		{"```go\na\n\nb\n```\n\nafter", "```go\na\n\nb\n```\n\n"},
		{"```go\na\n\nb", ""},
		{"a\n\nb\n\nc", "a\n\nb\n\n"},
	}
	for _, c := range cases {
		cut := paragraphCut(c.in)
		got := ""
		if cut > 0 {
			got = c.in[:cut]
		}
		if got != c.want {
			t.Errorf("paragraphCut(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAppendTextSplitsParagraphs(t *testing.T) {
	full := "# Go\n\nGo is fast. It is simple.\n\n- Fast compilation\n- Concurrency\n- Stdlib\n\n```go\npackage main\n\nfunc main() {}\n```\n"
	for _, chunk := range []int{1, 3, 7, 50} {
		m := &Model{}
		for i := 0; i < len(full); i += chunk {
			m.appendText(full[i:min(i+chunk, len(full))])
		}
		var got []string
		for _, it := range m.pending {
			got = append(got, it.text)
		}
		joined := ""
		for i, g := range got {
			if i > 0 {
				joined += "\n\n"
			}
			joined += g
		}
		if joined != "# Go\n\nGo is fast. It is simple.\n\n- Fast compilation\n- Concurrency\n- Stdlib\n\n```go\npackage main\n\nfunc main() {}\n```\n" {
			t.Errorf("chunk %d: items %q", chunk, got)
		}
	}
}

func TestParseRetry(t *testing.T) {
	r, at := parseRetry("Retrying (attempt 2 of 10) in 4000 ms: overloaded")
	if r.Attempt != 2 || r.Max != 10 || r.In != 4*time.Second || r.Error != "Unable to reach the model (overloaded)" {
		t.Fatalf("%+v", r)
	}
	if d := time.Until(at); d < 3*time.Second || d > 5*time.Second {
		t.Fatalf("retry at %v", d)
	}
}

func TestInterruptMarker(t *testing.T) {
	if !isInterruptMarker("[Request interrupted by user]") || !isInterruptMarker("[Request interrupted by user for tool use]") {
		t.Fatal("marker not recognized")
	}
	if isInterruptMarker("please don't interrupt") {
		t.Fatal("false positive")
	}
}

func TestFuzzyMatch(t *testing.T) {
	if idx := fuzzyMatch("/status", "/st"); len(idx) != 3 || idx[2] != 2 {
		t.Fatalf("%v", idx)
	}
	if fuzzyMatch("/help", "/x") != nil {
		t.Fatal("unexpected match")
	}
	if idx := fuzzyMatch("/compact", "/cpt"); idx == nil {
		t.Fatal("subsequence not matched")
	}
}

func TestEditor(t *testing.T) {
	var e editor
	e.Insert("hello world")
	e.DeleteWord()
	if e.String() != "hello " {
		t.Fatalf("%q", e.String())
	}
	e.Insert("\nsecond")
	e.Up()
	if e.OnFirstLine() != true || e.cursor != 6 {
		t.Fatalf("cursor %d", e.cursor)
	}
	e.End()
	e.Down()
	if !e.OnLastLine() {
		t.Fatal("not on last line")
	}
	w, from := e.WordBefore()
	if w != "second" || from != 7 {
		t.Fatalf("%q %d", w, from)
	}
	e.ReplaceRange(from, "@calc.go ")
	if e.String() != "hello \n@calc.go " {
		t.Fatalf("%q", e.String())
	}
}
