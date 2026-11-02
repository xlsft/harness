package memory

import (
	"regexp"
	"strings"
)

// segment is a run of markdown text that is either code (a fenced block or
// an inline code span, delimiters included) or prose.
type segment struct {
	text string
	code bool
}

// splitCode splits markdown into prose and code segments. Fenced blocks
// (``` or ~~~, up to three spaces of indentation) run to a closing fence of
// the same character and at least the same length, or to the end of the
// text. Inside prose, inline code spans are delimited by equal backtick
// runs; an unmatched run is literal text.
func splitCode(s string) []segment {
	var out []segment
	var prose strings.Builder
	flushProse := func() {
		if prose.Len() > 0 {
			out = append(out, splitSpans(prose.String())...)
			prose.Reset()
		}
	}
	var fence strings.Builder
	fenceChar, fenceLen := byte(0), 0
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		line := s
		if i >= 0 {
			line = s[:i+1]
		}
		s = s[len(line):]
		if fenceLen > 0 {
			fence.WriteString(line)
			if c, n := fenceRun(line); c == fenceChar && n >= fenceLen && strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), string(c))) == "" {
				out = append(out, segment{text: fence.String(), code: true})
				fence.Reset()
				fenceLen = 0
			}
			continue
		}
		if c, n := fenceRun(line); n >= 3 {
			// A backtick fence's info string may not contain backticks.
			rest := strings.TrimLeft(strings.TrimSpace(line), string(c))
			if c == '~' || !strings.Contains(rest, "`") {
				flushProse()
				fenceChar, fenceLen = c, n
				fence.WriteString(line)
				continue
			}
		}
		prose.WriteString(line)
	}
	if fenceLen > 0 {
		out = append(out, segment{text: fence.String(), code: true})
	}
	flushProse()
	return out
}

// fenceRun returns the fence character and run length at the start of line
// (after at most three spaces), or 0.
func fenceRun(line string) (byte, int) {
	j := 0
	for j < len(line) && j < 3 && line[j] == ' ' {
		j++
	}
	if j >= len(line) || (line[j] != '`' && line[j] != '~') {
		return 0, 0
	}
	c := line[j]
	n := 0
	for j+n < len(line) && line[j+n] == c {
		n++
	}
	return c, n
}

func splitSpans(s string) []segment {
	var out []segment
	start := 0 // start of the pending prose
	i := 0
	for i < len(s) {
		if s[i] != '`' {
			i++
			continue
		}
		n := 0
		for i+n < len(s) && s[i+n] == '`' {
			n++
		}
		// Find a closing run of exactly n backticks.
		j := i + n
		closeAt := -1
		for j < len(s) {
			if s[j] != '`' {
				j++
				continue
			}
			m := 0
			for j+m < len(s) && s[j+m] == '`' {
				m++
			}
			if m == n {
				closeAt = j
				break
			}
			j += m
		}
		if closeAt < 0 {
			i += n
			continue
		}
		if i > start {
			out = append(out, segment{text: s[start:i]})
		}
		out = append(out, segment{text: s[i : closeAt+n], code: true})
		i = closeAt + n
		start = i
	}
	if start < len(s) {
		out = append(out, segment{text: s[start:]})
	}
	return out
}

var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// stripComments removes HTML comments outside code.
func stripComments(s string) string {
	if !strings.Contains(s, "<!--") {
		return s
	}
	var b strings.Builder
	for _, seg := range splitCode(s) {
		if seg.code {
			b.WriteString(seg.text)
		} else {
			b.WriteString(htmlComment.ReplaceAllString(seg.text, ""))
		}
	}
	return b.String()
}

// importRef matches "@path" at the start of the text or after whitespace.
// Backslash-escaped spaces are part of the path.
var importRef = regexp.MustCompile(`(?:^|\s)@((?:\\ |[^\s])+)`)

// findImports returns the raw import paths referenced outside code, in
// order, with "\ " unescaped. Quoted paths (@"x", @'x') are not imports.
func findImports(s string) []string {
	if !strings.Contains(s, "@") {
		return nil
	}
	var out []string
	for _, seg := range splitCode(s) {
		if seg.code {
			continue
		}
		for _, m := range importRef.FindAllStringSubmatch(seg.text, -1) {
			p := m[1]
			if p == "" || p[0] == '"' || p[0] == '\'' || p[0] == '`' || p[0] == '@' {
				continue
			}
			out = append(out, strings.ReplaceAll(p, `\ `, " "))
		}
	}
	return out
}

// frontmatter splits a leading YAML frontmatter block ("---" lines) from
// body. It returns ok=false when there is none.
func frontmatter(s string) (fm, body string, ok bool) {
	t := strings.TrimPrefix(s, "\uFEFF")
	if !strings.HasPrefix(t, "---") {
		return "", s, false
	}
	first, rest, found := strings.Cut(t, "\n")
	if !found || strings.TrimSpace(first) != "---" {
		return "", s, false
	}
	var fmLines []string
	for {
		line, after, more := strings.Cut(rest, "\n")
		if strings.TrimSpace(strings.TrimRight(line, "\r")) == "---" {
			return strings.Join(fmLines, "\n"), after, true
		}
		fmLines = append(fmLines, line)
		if !more {
			return "", s, false
		}
		rest = after
	}
}

// frontmatterList reads a string list field from simple YAML frontmatter.
// Supported forms:
//
//	key: value
//	key: "a", "b"
//	key: [a, "b"]
//	key:
//	  - a
//	  - "b"
func frontmatterList(fm, key string) []string {
	lines := strings.Split(fm, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(k) != key || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		v = strings.TrimSpace(v)
		if v != "" {
			v = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
			var out []string
			for _, item := range splitList(v) {
				if item = unquote(strings.TrimSpace(item)); item != "" {
					out = append(out, item)
				}
			}
			return out
		}
		var out []string
		for j := i + 1; j < len(lines); j++ {
			l := strings.TrimSpace(strings.TrimRight(lines[j], "\r"))
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			if !strings.HasPrefix(l, "-") {
				break
			}
			if item := unquote(strings.TrimSpace(strings.TrimPrefix(l, "-"))); item != "" {
				out = append(out, item)
			}
		}
		return out
	}
	return nil
}

// frontmatterString reads a scalar string field from simple YAML frontmatter.
func frontmatterString(fm, key string) string {
	for _, line := range strings.Split(fm, "\n") {
		k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), ":")
		if ok && strings.TrimSpace(k) == key && !strings.HasPrefix(line, " ") {
			return unquote(strings.TrimSpace(v))
		}
	}
	return ""
}

// SplitFrontmatter splits a leading YAML frontmatter block from the body of
// markdown; ok is false (and body is markdown) when there is none.
func SplitFrontmatter(markdown string) (fm, body string, ok bool) {
	return frontmatter(markdown)
}

// FrontmatterString is the exported form of frontmatterString for other
// packages reading markdown with frontmatter (agents, output styles).
func FrontmatterString(markdown, key string) string {
	fm, _, ok := frontmatter(markdown)
	if !ok {
		return ""
	}
	return frontmatterString(fm, key)
}

// splitList splits on commas outside quotes.
func splitList(s string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
			cur.WriteByte(c)
		case c == ',':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(out, cur.String())
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
