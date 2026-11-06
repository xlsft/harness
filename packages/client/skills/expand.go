package skills

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

// ExpandOptions are what the placeholders of a body are filled with.
type ExpandOptions struct {
	Arguments string
	SessionID string
	// Dir is the skill's folder ("" for a command).
	Dir string
	// Shell runs a `!`command`` of the body and returns what stands in its place; nil: commands in bodies don't run, and
	// a note says so.
	Shell func(ctx context.Context, command string) string
}

// SplitArguments splits text the way a shell would: spaces separate, quotes hold together, a backslash escapes.
func SplitArguments(text string) []string {
	var out []string
	var cur strings.Builder
	in := false
	var quote rune
	escaped := false
	for _, r := range text {
		switch {
		case escaped:
			cur.WriteRune(r)
			in, escaped = true, false
		case r == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, in = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

// placeholderRe finds everything a body can ask for, in one pass, so text filled in is never read as a placeholder again.
var placeholderRe = regexp.MustCompile(`\$\{(?:CLAUDE|HARNESS)_(SESSION_ID|SKILL_DIR)\}|\$ARGUMENTS\[(\d+)\]|\$ARGUMENTS\b|\$(\d)\b|\$([A-Za-z_][A-Za-z0-9_]*)`)

// filler fills placeholders from one call's values.
type filler struct {
	raw       string
	args      []string
	named     map[string]string
	sessionID string
	dir       string
	used      bool
}

func newFiller(s Skill, o ExpandOptions) *filler {
	f := &filler{raw: strings.TrimSpace(o.Arguments), args: SplitArguments(o.Arguments), named: map[string]string{}, sessionID: o.SessionID, dir: o.Dir}
	for i, n := range s.Arguments {
		v := ""
		if i < len(f.args) {
			v = f.args[i]
		}
		f.named[n] = v
	}
	return f
}

func (f *filler) at(i int) string {
	if i >= 0 && i < len(f.args) {
		return f.args[i]
	}
	return ""
}

// fill replaces the placeholders of text; quote wraps what arguments bring in (shell commands quote them).
func (f *filler) fill(text string, quote func(string) string) string {
	return placeholderRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := placeholderRe.FindStringSubmatch(m)
		switch {
		case sub[1] == "SESSION_ID":
			return f.sessionID
		case sub[1] == "SKILL_DIR":
			return quote(f.dir)
		case sub[2] != "":
			n, _ := strconv.Atoi(sub[2])
			f.used = true
			return quote(f.at(n))
		case sub[3] != "":
			n, _ := strconv.Atoi(sub[3])
			f.used = true
			return quote(f.at(n - 1))
		case m == "$ARGUMENTS":
			f.used = true
			return quote(f.raw)
		}
		if v, ok := f.named[sub[4]]; ok {
			f.used = true
			return quote(v)
		}
		return m
	})
}

func plain(s string) string { return s }

// shellQuote makes a value one word of a command line.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// inlineShell finds `!`command`` at the start of a word.
var inlineShell = regexp.MustCompile("(^|[\\s(\\[])!`([^`\n]+)`")

// disabledShell stands where a command would have run.
const disabledShell = "[command not run: commands in skills are turned off]"

// Expand turns a body into the text the model reads. Commands are the body's author's: they are found in the body as
// written, so nothing an argument brings in (the user's or the model's text) can become a command, and their output isn't
// read for placeholders either. What arguments add to a command is quoted.
func Expand(ctx context.Context, s Skill, body string, o ExpandOptions) string {
	f := newFiller(s, o)
	run := func(command string) string {
		if o.Shell == nil {
			return disabledShell
		}
		return o.Shell(ctx, f.fill(command, shellQuote))
	}
	var out strings.Builder
	lines := strings.SplitAfter(body, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		t := strings.TrimSpace(line)
		if fence, ok := fenceOpening(t); ok {
			var inner []string
			j := i + 1
			for ; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == fence {
					break
				}
				inner = append(inner, lines[j])
			}
			if strings.TrimSpace(strings.TrimPrefix(t, fence)) == "!" {
				out.WriteString(run(strings.TrimSpace(strings.Join(inner, ""))))
				out.WriteString("\n")
			} else {
				// An ordinary code block: an example of the syntax does not run, placeholders do fill.
				out.WriteString(f.fill(line, plain))
				out.WriteString(f.fill(strings.Join(inner, ""), plain))
				if j < len(lines) {
					out.WriteString(lines[j])
				}
			}
			i = j
			continue
		}
		last := 0
		for _, m := range inlineShell.FindAllStringSubmatchIndex(line, -1) {
			out.WriteString(f.fill(line[last:m[3]], plain))
			out.WriteString(run(line[m[4]:m[5]]))
			last = m[1]
		}
		out.WriteString(f.fill(line[last:], plain))
	}
	text := out.String()
	if f.raw != "" && !f.used {
		text = strings.TrimRight(text, "\n") + "\n\nARGUMENTS: " + f.raw
	}
	return text
}

// fenceOpening recognizes the start of a code block and returns its fence.
func fenceOpening(line string) (string, bool) {
	for _, c := range []string{"```", "~~~"} {
		if strings.HasPrefix(line, c) {
			n := len(c)
			for n < len(line) && line[n] == c[0] {
				n++
			}
			return line[:n], true
		}
	}
	return "", false
}

// ParseSlash reads `/name args` from the start of a message. ok is false for anything else, such as a path.
func ParseSlash(text string) (name, args string, ok bool) {
	t := strings.TrimSpace(text)
	rest, has := strings.CutPrefix(t, "/")
	if !has {
		return "", "", false
	}
	end := strings.IndexAny(rest, " \t\n")
	name = rest
	if end >= 0 {
		name, args = rest[:end], strings.TrimSpace(rest[end:])
	}
	if !ValidName(name) {
		return "", "", false
	}
	return name, args, true
}
