// Package environment stores the per-folder environments of sessions (AgentService GetEnvironment /
// UpdateEnvironment): additional folders, new-session defaults and environment variables in .env syntax,
// encrypted at rest.
package environment

import (
	"fmt"
	"regexp"
	"strings"
)

// Limits of the env text.
const (
	MaxEnvBytes = 256 << 10
	MaxVars     = 1000
)

// Var is one variable of the env text.
type Var struct {
	Name, Value string
	// Line is the 1-based line the definition starts on.
	Line int
}

// LineError is a parse error of the env text.
type LineError struct {
	Line    int
	Message string
}

func (e LineError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Message) }

// ParseError lists every error of the env text.
type ParseError struct{ Errors []LineError }

func (e *ParseError) Error() string {
	parts := make([]string, 0, len(e.Errors))
	for _, le := range e.Errors {
		parts = append(parts, le.Error())
	}
	return "invalid environment variables: " + strings.Join(parts, "; ")
}

var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedPrefix names variables the harness uses to run shell commands.
const reservedPrefix = "__HARNESS_"

// Parse reads env text in .env syntax: NAME=value lines with an optional "export " prefix, blank lines and
// "#" comments (also after a value, preceded by whitespace). Values may be unquoted (trimmed), in double
// quotes (escapes \n \r \t \" \\ \$; other backslashes are kept; may span lines) or in single quotes
// (literal; may span lines). There is no ${VAR} expansion. A later definition of a name wins. Errors are
// collected for every line (a *ParseError); the variables are returned only when there is none.
func Parse(text string) ([]Var, error) {
	var errs []LineError
	fail := func(line int, format string, args ...any) {
		errs = append(errs, LineError{Line: line, Message: fmt.Sprintf(format, args...)})
	}
	if len(text) > MaxEnvBytes {
		return nil, &ParseError{Errors: []LineError{{Line: 1, Message: fmt.Sprintf("the text is larger than %d KiB", MaxEnvBytes>>10)}}}
	}
	text = strings.TrimPrefix(text, "\uFEFF")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var vars []Var
	index := map[string]int{}
	for i := 0; i < len(lines); i++ {
		lineNo := i + 1
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimSpace(rest)
		}
		name, rest, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok {
			if nameRe.MatchString(name) {
				fail(lineNo, "%s has no value (expected %s=value)", name, name)
			} else {
				fail(lineNo, "expected NAME=value")
			}
			continue
		}
		switch {
		case name == "":
			fail(lineNo, "missing variable name before =")
			continue
		case !nameRe.MatchString(name):
			fail(lineNo, "invalid variable name %q (letters, digits and _, not starting with a digit)", name)
			continue
		case strings.HasPrefix(strings.ToUpper(name), reservedPrefix):
			fail(lineNo, "%s is reserved by harness", name)
			continue
		}
		rest = strings.TrimLeft(rest, " \t")
		var value string
		switch {
		case strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, `'`):
			v, end, after, err := quoted(lines, i, rest)
			if err != "" {
				fail(lineNo, "%s", err)
				i = end
				continue
			}
			if after = strings.TrimSpace(after); after != "" && !strings.HasPrefix(after, "#") {
				fail(end+1, "unexpected text after the closing quote of %s", name)
			}
			value, i = v, end
		default:
			value = unquotedValue(rest)
		}
		if strings.ContainsRune(value, 0) {
			fail(lineNo, "the value of %s contains a NUL character", name)
			continue
		}
		if j, ok := index[name]; ok {
			vars[j] = Var{Name: name, Value: value, Line: lineNo}
			continue
		}
		if len(vars) >= MaxVars {
			fail(lineNo, "more than %d variables", MaxVars)
			break
		}
		index[name] = len(vars)
		vars = append(vars, Var{Name: name, Value: value, Line: lineNo})
	}
	if len(errs) > 0 {
		return nil, &ParseError{Errors: errs}
	}
	return vars, nil
}

// unquotedValue strips an inline comment (" #") and surrounding whitespace.
func unquotedValue(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
			s = s[:i]
			break
		}
	}
	return strings.TrimSpace(s)
}

// quoted reads a quoted value that starts at rest (on line i) and may continue on the following lines. It
// returns the value, the index of the line holding the closing quote, the text after it, or an error message.
func quoted(lines []string, i int, rest string) (value string, end int, after string, err string) {
	q := rest[0]
	s := rest[1:]
	var b strings.Builder
	for line := i; ; {
		for j := 0; j < len(s); j++ {
			c := s[j]
			switch {
			case c == q:
				return b.String(), line, s[j+1:], ""
			case q == '"' && c == '\\' && j+1 < len(s):
				j++
				switch s[j] {
				case 'n':
					b.WriteByte('\n')
				case 'r':
					b.WriteByte('\r')
				case 't':
					b.WriteByte('\t')
				case '"', '\\', '$':
					b.WriteByte(s[j])
				default:
					b.WriteByte('\\')
					b.WriteByte(s[j])
				}
			default:
				b.WriteByte(c)
			}
		}
		line++
		if line >= len(lines) {
			return "", len(lines) - 1, "", fmt.Sprintf("unterminated %s-quoted value (no closing %c)", map[byte]string{'"': "double", '\'': "single"}[q], q)
		}
		b.WriteByte('\n')
		s = lines[line]
	}
}

// Names returns the variable names of env text that parses (nil otherwise), in order.
func Names(text string) []string {
	vars, err := Parse(text)
	if err != nil {
		return nil
	}
	out := make([]string, len(vars))
	for i, v := range vars {
		out[i] = v.Name
	}
	return out
}

// Environ returns the variables as NAME=value entries for exec.Cmd.Env.
func Environ(vars []Var) []string {
	out := make([]string, len(vars))
	for i, v := range vars {
		out[i] = v.Name + "=" + v.Value
	}
	return out
}
