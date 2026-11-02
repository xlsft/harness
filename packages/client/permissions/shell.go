package permissions

import (
	"strings"
)

// Word is one shell word. Raw is the source text (quotes included), Value is
// the word after quote removal.
type Word struct {
	Raw   string
	Value string
}

// Redirect is one I/O redirection of a simple command.
type Redirect struct {
	// Op is the operator without the fd number: ">", ">>", "<", "<<", "<<<",
	// "&>", "&>>", ">&", "<&", ">|".
	Op string
	// FD is the explicit file descriptor ("" when absent).
	FD string
	// Target is the word after the operator (unquoted).
	Target string
}

// SimpleCommand is one command of a compound command line.
type SimpleCommand struct {
	Words     []Word
	Redirects []Redirect
	// Substitutions holds the inner text of $(...), `...`, <(...) and >(...).
	Substitutions []string
	// Unsafe is set when the command contains substitutions or constructs
	// whose effect cannot be determined statically.
	Unsafe bool
}

// Name returns the command name (first word value) or "".
func (c SimpleCommand) Name() string {
	if len(c.Words) == 0 {
		return ""
	}
	return c.Words[0].Value
}

// Args returns the argument values.
func (c SimpleCommand) Args() []string {
	var out []string
	for _, w := range c.Words[min(1, len(c.Words)):] {
		out = append(out, w.Value)
	}
	return out
}

// Text renders the command as matched by permission rules: raw words joined
// by single spaces, followed by redirections.
func (c SimpleCommand) Text() string {
	parts := make([]string, 0, len(c.Words)+len(c.Redirects))
	for _, w := range c.Words {
		parts = append(parts, w.Raw)
	}
	for _, r := range c.Redirects {
		parts = append(parts, r.FD+r.Op+" "+r.Target)
	}
	return strings.Join(parts, " ")
}

// SplitCommand splits a shell command line into simple commands at the
// operators && || ; | |& & and newlines (outside quotes, substitutions and
// heredocs). Subshell parentheses and braces are treated as separators.
func SplitCommand(cmd string) []SimpleCommand {
	p := &shellParser{src: []rune(cmd)}
	p.parse()
	var out []SimpleCommand
	for _, c := range p.cmds {
		c.Words = stripKeywords(c.Words)
		if len(c.Words) == 0 && len(c.Redirects) == 0 {
			continue
		}
		out = append(out, c)
	}
	return out
}

type shellParser struct {
	src  []rune
	i    int
	cmds []SimpleCommand
	cur  SimpleCommand
	// pending heredoc delimiters (strip leading tabs when true)
	heredocs []heredoc
}

type heredoc struct {
	delim string
	strip bool
}

func (p *shellParser) peek(off int) rune {
	if p.i+off < len(p.src) {
		return p.src[p.i+off]
	}
	return 0
}

func (p *shellParser) flush() {
	if len(p.cur.Words) > 0 || len(p.cur.Redirects) > 0 || len(p.cur.Substitutions) > 0 {
		p.cmds = append(p.cmds, p.cur)
	}
	p.cur = SimpleCommand{}
}

func (p *shellParser) parse() {
	for p.i < len(p.src) {
		c := p.src[p.i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			p.i++
		case c == '\\' && p.peek(1) == '\n':
			p.i += 2
		case c == '\n':
			p.i++
			p.flush()
			p.readHeredocs()
		case c == '#':
			for p.i < len(p.src) && p.src[p.i] != '\n' {
				p.i++
			}
		case c == ';':
			p.i++
			if p.peek(0) == ';' {
				p.i++
			}
			p.flush()
		case c == '&':
			if p.peek(1) == '&' {
				p.i += 2
				p.flush()
			} else if p.peek(1) == '>' {
				p.readRedirect("")
			} else {
				p.i++
				p.flush()
			}
		case c == '|':
			p.i++
			if p.peek(0) == '|' || p.peek(0) == '&' {
				p.i++
			}
			p.flush()
		case c == '(' || c == ')':
			p.i++
			p.flush()
		case c == '<' || c == '>':
			if (c == '<' || c == '>') && p.peek(1) == '(' {
				// process substitution as a word
				p.readWord()
			} else {
				p.readRedirect("")
			}
		default:
			p.readWord()
		}
	}
	p.flush()
}

// readHeredocs consumes heredoc bodies that start after a newline.
func (p *shellParser) readHeredocs() {
	for len(p.heredocs) > 0 {
		h := p.heredocs[0]
		p.heredocs = p.heredocs[1:]
		for p.i < len(p.src) {
			start := p.i
			for p.i < len(p.src) && p.src[p.i] != '\n' {
				p.i++
			}
			line := string(p.src[start:p.i])
			if p.i < len(p.src) {
				p.i++
			}
			if h.strip {
				line = strings.TrimLeft(line, "\t")
			}
			if line == h.delim {
				break
			}
		}
	}
}

func (p *shellParser) readRedirect(fd string) {
	start := p.i
	for p.i < len(p.src) && (p.src[p.i] == '<' || p.src[p.i] == '>' || p.src[p.i] == '&' || p.src[p.i] == '|' || p.src[p.i] == '-') {
		// stop "-" unless part of "<<-"
		if p.src[p.i] == '-' && string(p.src[start:p.i]) != "<<" {
			break
		}
		if p.src[p.i] == '|' && string(p.src[start:p.i]) != ">" {
			break
		}
		if p.src[p.i] == '&' && p.i > start && !(string(p.src[start:p.i]) == ">" || string(p.src[start:p.i]) == "<") {
			break
		}
		p.i++
	}
	op := string(p.src[start:p.i])
	for p.i < len(p.src) && (p.src[p.i] == ' ' || p.src[p.i] == '\t') {
		p.i++
	}
	r := Redirect{Op: strings.TrimSuffix(op, "-"), FD: fd}
	if op == ">&" || op == "<&" {
		// fd duplication: target is a digit or "-"
		j := p.i
		for j < len(p.src) && (p.src[j] >= '0' && p.src[j] <= '9' || p.src[j] == '-') {
			j++
		}
		if j > p.i {
			r.Target = string(p.src[p.i:j])
			p.i = j
			p.cur.Redirects = append(p.cur.Redirects, r)
			return
		}
	}
	w, ok := p.scanWord()
	if ok {
		r.Target = w.Value
	}
	if op == "<<" || op == "<<-" {
		p.heredocs = append(p.heredocs, heredoc{delim: w.Value, strip: op == "<<-"})
	}
	p.cur.Redirects = append(p.cur.Redirects, r)
}

func (p *shellParser) readWord() {
	w, ok := p.scanWord()
	if !ok {
		p.i++
		return
	}
	// "2>" style fd redirect: an all-digit word immediately followed by < or >.
	if p.i < len(p.src) && (p.src[p.i] == '<' || p.src[p.i] == '>') && p.peek(1) != '(' && isDigits(w.Raw) {
		p.readRedirect(w.Raw)
		return
	}
	if len(p.cur.Words) == 0 && (w.Raw == "{" || w.Raw == "}") {
		return
	}
	if w.Raw == "}" {
		return
	}
	p.cur.Words = append(p.cur.Words, w)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// scanWord reads one word starting at p.i.
func (p *shellParser) scanWord() (Word, bool) {
	start := p.i
	var val strings.Builder
	for p.i < len(p.src) {
		c := p.src[p.i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ';' || c == '|' || c == ')':
			goto done
		case c == '&':
			goto done
		case c == '(' && p.i == start:
			goto done
		case (c == '<' || c == '>') && p.peek(1) == '(':
			p.i += 2
			inner := p.readBalanced(')')
			p.cur.Substitutions = append(p.cur.Substitutions, inner)
			p.cur.Unsafe = true
			val.WriteString("<(" + inner + ")")
		case c == '<' || c == '>':
			goto done
		case c == '\\':
			if p.i+1 < len(p.src) {
				if p.src[p.i+1] != '\n' {
					val.WriteRune(p.src[p.i+1])
				}
				p.i += 2
			} else {
				p.i++
			}
		case c == '\'':
			p.i++
			for p.i < len(p.src) && p.src[p.i] != '\'' {
				val.WriteRune(p.src[p.i])
				p.i++
			}
			p.i++
		case c == '"':
			p.i++
			for p.i < len(p.src) && p.src[p.i] != '"' {
				d := p.src[p.i]
				switch {
				case d == '\\' && p.i+1 < len(p.src) && strings.ContainsRune("$`\"\\\n", p.src[p.i+1]):
					if p.src[p.i+1] != '\n' {
						val.WriteRune(p.src[p.i+1])
					}
					p.i += 2
				case d == '$' && p.peek(1) == '(':
					p.i += 2
					inner := p.readBalanced(')')
					p.cur.Substitutions = append(p.cur.Substitutions, inner)
					p.cur.Unsafe = true
					val.WriteString("$(" + inner + ")")
				case d == '`':
					p.i++
					inner := p.readUntil('`')
					p.cur.Substitutions = append(p.cur.Substitutions, inner)
					p.cur.Unsafe = true
				default:
					val.WriteRune(d)
					p.i++
				}
			}
			p.i++
		case c == '$' && p.peek(1) == '(':
			p.i += 2
			inner := p.readBalanced(')')
			p.cur.Substitutions = append(p.cur.Substitutions, inner)
			p.cur.Unsafe = true
			val.WriteString("$(" + inner + ")")
		case c == '$' && p.peek(1) == '{':
			s := p.i
			p.i += 2
			p.readBalanced('}')
			val.WriteString(string(p.src[s:p.i]))
		case c == '`':
			p.i++
			inner := p.readUntil('`')
			p.cur.Substitutions = append(p.cur.Substitutions, inner)
			p.cur.Unsafe = true
		default:
			val.WriteRune(c)
			p.i++
		}
	}
done:
	if p.i == start {
		return Word{}, false
	}
	end := min(p.i, len(p.src))
	return Word{Raw: string(p.src[start:end]), Value: val.String()}, true
}

// readBalanced reads until the matching close rune (the opener was consumed),
// honoring nested parentheses and quotes. It returns the inner text.
func (p *shellParser) readBalanced(close rune) string {
	open := '('
	if close == '}' {
		open = '{'
	}
	start := p.i
	depth := 1
	for p.i < len(p.src) {
		c := p.src[p.i]
		switch c {
		case '\\':
			p.i += 2
			continue
		case '\'':
			p.i++
			for p.i < len(p.src) && p.src[p.i] != '\'' {
				p.i++
			}
		case '"':
			p.i++
			for p.i < len(p.src) && p.src[p.i] != '"' {
				if p.src[p.i] == '\\' {
					p.i++
				}
				p.i++
			}
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				inner := string(p.src[start:p.i])
				p.i++
				return inner
			}
		}
		p.i++
	}
	if start > len(p.src) {
		return ""
	}
	return string(p.src[start:])
}

func (p *shellParser) readUntil(r rune) string {
	start := p.i
	for p.i < len(p.src) && p.src[p.i] != r {
		if p.src[p.i] == '\\' {
			p.i++
		}
		p.i++
	}
	end := min(p.i, len(p.src))
	p.i++
	return string(p.src[start:end])
}

var shellKeywords = map[string]bool{
	"then": true, "do": true, "else": true, "elif": true, "if": true, "while": true, "until": true, "!": true,
}

var closingKeywords = map[string]bool{"fi": true, "done": true, "esac": true}

func stripKeywords(ws []Word) []Word {
	for len(ws) > 0 && shellKeywords[ws[0].Raw] {
		ws = ws[1:]
	}
	if len(ws) == 1 && closingKeywords[ws[0].Raw] {
		return nil
	}
	return ws
}

// StripWrappers removes leading VAR=value assignments and wrapper commands
// that run their arguments as a command: timeout, time, nice, nohup, stdbuf,
// command, builtin, noglob and flagless xargs.
func StripWrappers(ws []Word) []Word {
	for len(ws) > 0 {
		name := ws[0].Value
		switch {
		case isAssignment(ws[0]):
			ws = ws[1:]
		case name == "timeout":
			ws = ws[1:]
			for len(ws) > 0 && strings.HasPrefix(ws[0].Value, "-") {
				f := ws[0].Value
				ws = ws[1:]
				if (f == "-s" || f == "-k" || f == "--signal" || f == "--kill-after") && len(ws) > 0 {
					ws = ws[1:]
				}
			}
			if len(ws) > 0 {
				ws = ws[1:] // duration
			}
		case name == "time":
			ws = ws[1:]
			for len(ws) > 0 && (ws[0].Value == "-p" || ws[0].Value == "--") {
				ws = ws[1:]
			}
		case name == "nice":
			ws = ws[1:]
			for len(ws) > 0 && strings.HasPrefix(ws[0].Value, "-") {
				f := ws[0].Value
				ws = ws[1:]
				if (f == "-n" || f == "--adjustment") && len(ws) > 0 {
					ws = ws[1:]
				}
			}
		case name == "nohup" || name == "builtin" || name == "noglob":
			ws = ws[1:]
		case name == "command":
			if len(ws) > 1 && (ws[1].Value == "-v" || ws[1].Value == "-V") {
				return ws
			}
			ws = ws[1:]
			for len(ws) > 0 && ws[0].Value == "-p" {
				ws = ws[1:]
			}
		case name == "stdbuf":
			ws = ws[1:]
			for len(ws) > 0 && strings.HasPrefix(ws[0].Value, "-") {
				f := ws[0].Value
				ws = ws[1:]
				if (f == "-i" || f == "-o" || f == "-e") && len(ws) > 0 {
					ws = ws[1:]
				}
			}
		case name == "xargs":
			if len(ws) > 1 && !strings.HasPrefix(ws[1].Value, "-") {
				ws = ws[1:]
			} else {
				return ws
			}
		default:
			return ws
		}
	}
	return ws
}

func isAssignment(w Word) bool {
	i := strings.IndexByte(w.Raw, '=')
	if i <= 0 {
		return false
	}
	name := w.Raw[:i]
	for j, r := range name {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || j > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// Normalize returns a copy of c with wrappers stripped.
func (c SimpleCommand) Normalize() SimpleCommand {
	c.Words = StripWrappers(c.Words)
	return c
}
