package environment

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	text := strings.Join([]string{
		"\uFEFF# a comment",
		"",
		"PLAIN=value",
		"  SPACED = padded value   # trailing comment",
		"export EXPORTED=1",
		"EMPTY=",
		"HASH=a#b",
		`DOUBLE="line1\nline2 \"q\" \\ \$HOME \x"`,
		`SINGLE='literal \n $HOME'`,
		`MULTI="first`,
		`second # not a comment`,
		`  third"  # comment`,
		`MULTI_SINGLE='a`,
		`b'`,
		"URL=https://example.com/?a=1&b=2",
		"PLAIN=again",
		"exportNOT=x",
		"CRLF=yes\r",
	}, "\n")
	vars, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	var order []string
	for _, v := range vars {
		got[v.Name] = v.Value
		order = append(order, v.Name)
	}
	want := map[string]string{
		"PLAIN":        "again",
		"SPACED":       "padded value",
		"EXPORTED":     "1",
		"EMPTY":        "",
		"HASH":         "a#b",
		"DOUBLE":       "line1\nline2 \"q\" \\ $HOME \\x",
		"SINGLE":       `literal \n $HOME`,
		"MULTI":        "first\nsecond # not a comment\n  third",
		"MULTI_SINGLE": "a\nb",
		"URL":          "https://example.com/?a=1&b=2",
		"exportNOT":    "x",
		"CRLF":         "yes",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
	// A later definition replaces the value in place.
	if order[0] != "PLAIN" || vars[0].Line != 16 {
		t.Fatalf("order %v, PLAIN line %d", order, vars[0].Line)
	}
	if vars[len(vars)-1].Name != "CRLF" || vars[len(vars)-1].Line != 18 {
		t.Fatalf("last %+v", vars[len(vars)-1])
	}
	if env := Environ(vars[:2]); !reflect.DeepEqual(env, []string{"PLAIN=again", "SPACED=padded value"}) {
		t.Fatalf("environ %v", env)
	}
}

func TestParseErrors(t *testing.T) {
	text := strings.Join([]string{
		"OK=1",
		"1BAD=x",
		"NOVALUE",
		"=nothing",
		"has space=1",
		"__HARNESS_CMD=x",
		`AFTER="quoted" junk`,
		"just some words",
		`OPEN="never closed`,
		"STILL_INSIDE=1",
	}, "\n")
	_, err := Parse(text)
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err %v", err)
	}
	lines := map[int]string{}
	for _, e := range pe.Errors {
		lines[e.Line] = e.Message
	}
	for line, sub := range map[int]string{
		2: "invalid variable name",
		3: "NOVALUE has no value",
		4: "missing variable name",
		5: "invalid variable name",
		6: "reserved",
		7: "after the closing quote",
		8: "expected NAME=value",
		9: "unterminated double-quoted",
	} {
		if !strings.Contains(lines[line], sub) {
			t.Errorf("line %d: %q, want %q", line, lines[line], sub)
		}
	}
	if len(pe.Errors) != 8 {
		t.Fatalf("errors %+v", pe.Errors)
	}
	if !strings.Contains(err.Error(), "line 2: ") {
		t.Fatalf("message %q", err.Error())
	}
	// The values never appear in errors.
	if _, err := Parse("SECRET=\"hunter2"); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("err %v", err)
	}
	if Names("A=1\nB=2") == nil || Names("1=2") != nil {
		t.Fatal("names")
	}
}

func TestParseLimits(t *testing.T) {
	if _, err := Parse(strings.Repeat("A=1\n", MaxEnvBytes/4+1)); err == nil {
		t.Fatal("oversized text accepted")
	}
	var b strings.Builder
	for i := 0; i <= MaxVars; i++ {
		b.WriteString("V")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString("_")
		b.WriteString(strings.Repeat("y", i/7))
		b.WriteString("=1\n")
	}
	if _, err := Parse(b.String()); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("err %v", err)
	}
	if _, err := Parse("NUL=\"a\x00b\""); err == nil {
		t.Fatal("NUL accepted")
	}
}
