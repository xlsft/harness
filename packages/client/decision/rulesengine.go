package decision

// This file is a line-by-line Go port of the server's deterministic `rules`
// provider, packages/core/src/decision/rules.ts. Sections, function names,
// rule ids, reasons and data tables follow the TypeScript module in the same
// order so that syncing a change stays mechanical: find the function with
// the same (Go-cased) name and port the diff. The shared test table is
// packages/core/test/fixtures/decision-rules.json (TestServerRulesFixture).
//
// Deliberate deviations from the TypeScript (each one is safer than the
// server and never loosens a verdict):
//   - TEST_TOOLS is looked up as an own-property table; the TypeScript
//     `TEST_TOOLS[tool]` also finds Object.prototype members ("toString",
//     "valueOf", "constructor", ...), which fast-allows programs with those
//     names and throws for "__proto__".
//   - JavaScript regexes are compiled with jsRe/jsReI, which translate the
//     JavaScript meaning of `\s` and of the `i` flag to RE2.

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf16"
)

// RuleDecision is "allow", "deny" or "ask".
type RuleDecision = string

// Rule decisions.
const (
	RuleAllow RuleDecision = "allow"
	RuleDeny  RuleDecision = "deny"
	RuleAsk   RuleDecision = "ask"
)

// RuleCategories are the lowercase names of harness.decision.v1.RiskCategory
// values, in the order the server sorts them.
var RuleCategories = []string{
	CatDestructive,
	CatRemoteCodeExecution,
	CatSecretExfiltration,
	CatProductionChange,
	CatGitHistoryRewrite,
	CatPrivilegeEscalation,
	CatNetworkEgress,
	CatOutOfScope,
	CatPackageInstall,
}

// RulesFacts mirrors the server's RulesFacts (ClientFacts).
type RulesFacts struct {
	Subcommands   []string `json:"subcommands,omitempty"`
	WritePaths    []string `json:"write_paths,omitempty"`
	NetworkHosts  []string `json:"network_hosts,omitempty"`
	InsideGitRepo bool     `json:"inside_git_repo,omitempty"`
	GitBranch     string   `json:"git_branch,omitempty"`
}

// RulesInput mirrors the server's RulesInput. Input is the tool input as
// JSON text (the server receives it parsed; empty text means {}).
type RulesInput struct {
	ToolName              string          `json:"tool_name"`
	Input                 json.RawMessage `json:"input"`
	Cwd                   string          `json:"cwd,omitempty"`
	AdditionalDirectories []string        `json:"additional_directories,omitempty"`
	Facts                 RulesFacts      `json:"facts"`
}

// ExtraDenyRule is an administrator hard-deny regex.
type ExtraDenyRule struct {
	// Pattern is a regular expression (case-sensitive).
	Pattern    string
	Reason     string
	Categories []string
}

// RulesOptions mirrors the server's RulesOptions.
type RulesOptions struct {
	// ExtraDeny regexes are tested against the whole Bash command and every
	// simple command; for other tools against `<tool_name> <input_json>`.
	ExtraDeny []ExtraDenyRule
	// ExtraAllow regexes make a matching simple command count as safe; for
	// other tools tested against `<tool_name> <input_json>`. Never overrides a deny.
	ExtraAllow []string
	// ProtectedBranches are branch globs force pushes may not target (`*`
	// wildcard). Default: DefaultProtectedBranches.
	ProtectedBranches []string
	// DenyAllForcePush denies force pushes to any branch.
	DenyAllForcePush bool
}

// RulesResult mirrors the server's RulesResult.
type RulesResult struct {
	Decision   RuleDecision
	Reason     string
	Categories []string
	// Rule is the id of the rule that decided (for audit and tests).
	Rule string
}

// DefaultProtectedBranches are the branch globs force pushes may not target.
var DefaultProtectedBranches = []string{"main", "master", "release*"}

// BuiltinRulesVersion is the server's BUILTIN_RULES_VERSION: the version of
// the built-in rule set this port implements (bumped with every rule change on
// both sides; the shared fixture records it).
const BuiltinRulesVersion = 2

const (
	maxCommandLength = 100_000
	maxDepth         = 6
)

// ---------------------------------------------------------------------------------------------------
// JavaScript regex semantics on RE2
// ---------------------------------------------------------------------------------------------------

// jsSpaceClass is the content of JavaScript's `\s` class.
const jsSpaceClass = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// jsRe compiles a JavaScript regex source (no flags) with `\s` meaning what
// it means in JavaScript.
func jsRe(src string) *regexp.Regexp {
	return regexp.MustCompile(translateJSSpace(src))
}

func translateJSSpace(src string) string {
	var b strings.Builder
	inClass := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c == '\\' && i+1 < len(src) {
			if src[i+1] == 's' {
				if inClass {
					b.WriteString(jsSpaceClass)
				} else {
					b.WriteString("[" + jsSpaceClass + "]")
				}
			} else {
				b.WriteByte(c)
				b.WriteByte(src[i+1])
			}
			i++
			continue
		}
		if c == '[' && !inClass {
			inClass = true
		} else if c == ']' && inClass {
			inClass = false
		}
		b.WriteByte(c)
	}
	return b.String()
}

// ciRegexp is a JavaScript regex with the `i` flag (no `u`): RE2's (?i) also
// folds U+017F (long s) to "s" and U+212A (Kelvin sign) to "k", JavaScript
// does not, so those runes are hidden from the matcher.
type ciRegexp struct{ re *regexp.Regexp }

func jsReI(src string) ciRegexp { return ciRegexp{jsRe("(?i)" + src)} }

var ciFoldOnlyInRE2 = strings.NewReplacer("ſ", "�", "K", "�")

func (r ciRegexp) MatchString(s string) bool {
	if strings.ContainsAny(s, "ſK") {
		s = ciFoldOnlyInRE2.Replace(s)
	}
	return r.re.MatchString(s)
}

// jsLength is String.prototype.length (UTF-16 code units).
func jsLength(s string) int {
	n := 0
	for _, r := range s {
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}

// jsSlice is s.slice(0, n) in UTF-16 code units (never splitting a rune).
func jsSlice(s string, n int) string {
	units := 0
	for i, r := range s {
		units += len(utf16.Encode([]rune{r}))
		if units > n {
			return s[:i]
		}
	}
	return s
}

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func anyOf(list []string, f func(string) bool) bool {
	for _, x := range list {
		if f(x) {
			return true
		}
	}
	return false
}

func filter(list []string, f func(string) bool) []string {
	var out []string
	for _, x := range list {
		if f(x) {
			out = append(out, x)
		}
	}
	return out
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

// at returns list[i] and whether it exists (JavaScript `list[i] !== undefined`).
func at(list []string, i int) (string, bool) {
	if i >= 0 && i < len(list) {
		return list[i], true
	}
	return "", false
}

// atOr returns list[i] or "" (JavaScript `list[i] ?? ""`).
func atOr(list []string, i int) string {
	s, _ := at(list, i)
	return s
}

// ---------------------------------------------------------------------------------------------------
// Shell parser
// ---------------------------------------------------------------------------------------------------

// Word is one shell word.
type Word struct {
	// Text is the value after quote removal; substitutions appear as `$(…)`,
	// parameters stay as written (`$HOME`).
	Text   string
	Quoted bool
	// Dynamic: parameter expansion or command substitution somewhere in the word.
	Dynamic bool
	// Subs are the command substitutions (`$(...)`, backticks) inside the word.
	Subs []*Script
	// ProcSubs are process substitutions (`<(...)`, `>(...)`); the whole word is the substitution.
	ProcSubs []*Script
}

// Redirect is one redirection.
type Redirect struct {
	Op     string
	Fd     string
	Target Word
	// Body is the heredoc body (`<<`, `<<-`); nil when not read.
	Body *string
}

// SimpleCommand is one simple command.
type SimpleCommand struct {
	Assigns   []Word
	Words     []Word
	Redirects []*Redirect
	// Group is a `( ... )` subshell in place of a command.
	Group *Script
}

// Pipeline is a list of simple commands joined by pipes.
type Pipeline struct {
	Cmds []*SimpleCommand
}

// ScriptItem is a pipeline and the separator after it.
type ScriptItem struct {
	Pipeline Pipeline
	Sep      string
}

// Script is a parsed command list.
type Script struct {
	Items []ScriptItem
}

// shellParseError is the ShellParseError of the TypeScript; it travels as a
// panic inside the parser and the flattener (like the thrown error) and is
// recovered at the entry points.
type shellParseError string

func (e shellParseError) Error() string { return string(e) }

func parseFail(msg string) { panic(shellParseError(msg)) }

var (
	assignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\+?=`)
	digitsRe = regexp.MustCompile(`^\d+$`)
	nameRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)
)

const wordBreak = " \t\n;&|<>()"

type shellParser struct {
	s       string
	i       int
	depth   int
	pending []*Redirect
}

func newShellParser(s string, depth int) *shellParser {
	if depth > maxDepth {
		parseFail("nesting too deep")
	}
	return &shellParser{s: s, depth: depth}
}

// ch returns s[j], or 0 past the end (JavaScript undefined).
func (p *shellParser) ch(j int) byte {
	if j >= 0 && j < len(p.s) {
		return p.s[j]
	}
	return 0
}

func (p *shellParser) has(j int) bool { return j >= 0 && j < len(p.s) }

func (p *shellParser) parse() *Script {
	script := p.parseList("")
	if p.i < len(p.s) {
		parseFail(`unexpected "` + string(p.s[p.i]) + `"`)
	}
	return script
}

func (p *shellParser) parseList(close string) *Script {
	s := p.s
	script := &Script{}
	pipeline := Pipeline{}
	var cmd *SimpleCommand
	continuation := false
	newCmd := func() *SimpleCommand { return &SimpleCommand{} }
	endCmd := func() {
		if cmd != nil && (len(cmd.Words) > 0 || len(cmd.Assigns) > 0 || len(cmd.Redirects) > 0 || cmd.Group != nil) {
			pipeline.Cmds = append(pipeline.Cmds, cmd)
		}
		cmd = nil
	}
	endPipeline := func(sep string) {
		endCmd()
		if len(pipeline.Cmds) > 0 {
			script.Items = append(script.Items, ScriptItem{Pipeline: pipeline, Sep: sep})
		}
		pipeline = Pipeline{}
	}

	for p.i < len(s) {
		c := s[p.i]
		if c == ' ' || c == '\t' || c == '\r' {
			p.i++
			continue
		}
		if c == '\\' && p.ch(p.i+1) == '\n' {
			p.i += 2
			continue
		}
		if c == '\n' {
			p.i++
			p.readHeredocs()
			if continuation {
				continue
			}
			endPipeline("\n")
			continue
		}
		if c == '#' {
			for p.i < len(s) && s[p.i] != '\n' {
				p.i++
			}
			continue
		}
		if c == ')' {
			if close != ")" {
				parseFail(`unexpected ")"`)
			}
			p.i++
			endPipeline("")
			return script
		}
		continuation = false
		if c == ';' {
			if p.ch(p.i+1) == ';' {
				p.i += 2
			} else {
				p.i++
			}
			endPipeline(";")
			continue
		}
		if c == '&' {
			if p.ch(p.i+1) == '&' {
				p.i += 2
				endPipeline("&&")
				continuation = true
				continue
			}
			if p.ch(p.i+1) == '>' {
				if cmd == nil {
					cmd = newCmd()
				}
				cmd.Redirects = append(cmd.Redirects, p.parseRedirect(""))
				continue
			}
			p.i++
			endPipeline("&")
			continue
		}
		if c == '|' {
			if p.ch(p.i+1) == '|' {
				p.i += 2
				endPipeline("||")
			} else {
				if p.ch(p.i+1) == '&' {
					p.i += 2
				} else {
					p.i++
				}
				endCmd()
			}
			continuation = true
			continue
		}
		if c == '(' {
			if cmd != nil && len(cmd.Words) > 0 {
				parseFail(`unexpected "("`)
			}
			p.i++
			inner := p.nested(func() *Script { return p.parseList(")") })
			if cmd == nil {
				cmd = newCmd()
			}
			cmd.Group = inner
			continue
		}
		if (c == '<' || c == '>') && p.ch(p.i+1) == '(' {
			p.i += 2
			inner := p.nested(func() *Script { return p.parseList(")") })
			if cmd == nil {
				cmd = newCmd()
			}
			cmd.Words = append(cmd.Words, Word{Text: string(c) + "(…)", Dynamic: true, ProcSubs: []*Script{inner}})
			continue
		}
		if c == '<' || c == '>' {
			if cmd == nil {
				cmd = newCmd()
			}
			cmd.Redirects = append(cmd.Redirects, p.parseRedirect(""))
			continue
		}
		w := p.parseWord()
		if cmd == nil {
			cmd = newCmd()
		}
		if !w.Quoted && digitsRe.MatchString(w.Text) && (p.ch(p.i) == '<' || p.ch(p.i) == '>') {
			cmd.Redirects = append(cmd.Redirects, p.parseRedirect(w.Text))
		} else if len(cmd.Words) == 0 && cmd.Group == nil && assignRe.MatchString(w.Text) {
			cmd.Assigns = append(cmd.Assigns, w)
		} else {
			cmd.Words = append(cmd.Words, w)
		}
	}
	if close != "" {
		parseFail(`missing "` + close + `"`)
	}
	endPipeline("")
	return script
}

func (p *shellParser) nested(fn func() *Script) *Script {
	if p.depth+1 > maxDepth {
		parseFail("nesting too deep")
	}
	return fn()
}

var redirectOps = []string{"<<<", "<<-", "<<", "<&", "<>", "&>>", "&>", ">>", ">&", ">|", "<", ">"}

func (p *shellParser) parseRedirect(fd string) *Redirect {
	s := p.s
	op := ""
	if p.i <= len(s) {
		for _, o := range redirectOps {
			if strings.HasPrefix(s[p.i:], o) {
				op = o
				break
			}
		}
	}
	if op == "" {
		parseFail("bad redirection")
	}
	p.i += len(op)
	for p.ch(p.i) == ' ' || p.ch(p.i) == '\t' {
		p.i++
	}
	target := p.parseWord()
	if target.Text == "" && !target.Quoted {
		parseFail("missing redirection target")
	}
	r := &Redirect{Op: op, Target: target, Fd: fd}
	if op == "<<" || op == "<<-" {
		p.pending = append(p.pending, r)
	}
	return r
}

func (p *shellParser) readHeredocs() {
	s := p.s
	for _, r := range p.pending {
		var lines []string
		for {
			if p.i >= len(s) {
				break
			}
			nl := strings.IndexByte(s[p.i:], '\n')
			if nl < 0 {
				nl = len(s)
			} else {
				nl += p.i
			}
			line := s[p.i:nl]
			p.i = min(nl+1, len(s))
			cmp := line
			if r.Op == "<<-" {
				cmp = strings.TrimLeft(line, "\t")
			}
			if cmp == r.Target.Text {
				break
			}
			lines = append(lines, line)
		}
		body := strings.Join(lines, "\n")
		r.Body = &body
	}
	p.pending = nil
}

// wordBuf accumulates a Word (strings.Builder keeps long words linear).
type wordBuf struct {
	text            strings.Builder
	quoted, dynamic bool
	subs, procSubs  []*Script
}

func (w *wordBuf) word() Word {
	return Word{Text: w.text.String(), Quoted: w.quoted, Dynamic: w.dynamic, Subs: w.subs, ProcSubs: w.procSubs}
}

func (p *shellParser) parseWord() Word {
	s := p.s
	w := &wordBuf{}
	for p.i < len(s) {
		c := s[p.i]
		if strings.IndexByte(wordBreak, c) >= 0 {
			break
		}
		if c == '\\' {
			if p.ch(p.i+1) == '\n' {
				p.i += 2
				continue
			}
			if p.has(p.i + 1) {
				w.text.WriteByte(s[p.i+1])
			}
			w.quoted = true
			p.i += 2
			continue
		}
		if c == '\'' {
			end := strings.IndexByte(s[p.i+1:], '\'')
			if end < 0 {
				parseFail("unterminated single quote")
			}
			end += p.i + 1
			w.text.WriteString(s[p.i+1 : end])
			w.quoted = true
			p.i = end + 1
			continue
		}
		if c == '"' {
			p.i++
			w.quoted = true
			p.parseDoubleQuoted(w)
			continue
		}
		if c == '$' || c == '`' {
			p.parseDollar(w, false)
			continue
		}
		w.text.WriteByte(c)
		p.i++
	}
	return w.word()
}

func (p *shellParser) parseDoubleQuoted(w *wordBuf) {
	s := p.s
	for {
		if p.i >= len(s) {
			parseFail("unterminated double quote")
		}
		c := s[p.i]
		if c == '"' {
			p.i++
			return
		}
		if c == '\\' {
			if p.has(p.i + 1) {
				n := s[p.i+1]
				if n == '\n' {
					p.i += 2
					continue
				}
				if strings.IndexByte("\"\\$`", n) >= 0 {
					w.text.WriteByte(n)
					p.i += 2
					continue
				}
			}
			w.text.WriteByte(c)
			p.i++
			continue
		}
		if c == '$' || c == '`' {
			p.parseDollar(w, true)
			continue
		}
		w.text.WriteByte(c)
		p.i++
	}
}

// parseDollar reads `$(...)`, `$((...))`, `${...}`, `$NAME`, `$'...'` and backticks.
func (p *shellParser) parseDollar(w *wordBuf, inDouble bool) {
	s := p.s
	if s[p.i] == '`' {
		j := p.i + 1
		var body strings.Builder
		for j < len(s) && s[j] != '`' {
			if s[j] == '\\' && j+1 < len(s) {
				body.WriteByte(s[j+1])
				j += 2
				continue
			}
			body.WriteByte(s[j])
			j++
		}
		if j >= len(s) {
			parseFail("unterminated backtick")
		}
		p.i = j + 1
		w.subs = append(w.subs, newShellParser(body.String(), p.depth+1).parse())
		w.text.WriteString("$(…)")
		w.dynamic = true
		return
	}
	hasN := p.has(p.i + 1)
	n := p.ch(p.i + 1)
	if hasN && n == '(' && p.ch(p.i+2) == '(' {
		// arithmetic expansion: skip to the matching "))"
		depth := 0
		j := p.i + 1
		for ; j < len(s); j++ {
			if s[j] == '(' {
				depth++
			} else if s[j] == ')' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if j >= len(s) {
			parseFail("unterminated arithmetic expansion")
		}
		w.text.WriteString(s[p.i : j+1])
		w.dynamic = true
		p.i = j + 1
		return
	}
	if hasN && n == '(' {
		p.i += 2
		w.subs = append(w.subs, p.nested(func() *Script { return p.parseList(")") }))
		w.text.WriteString("$(…)")
		w.dynamic = true
		return
	}
	if hasN && n == '{' {
		end := strings.IndexByte(s[p.i:], '}')
		if end < 0 {
			parseFail("unterminated ${")
		}
		end += p.i
		w.text.WriteString(s[p.i : end+1])
		w.dynamic = true
		p.i = end + 1
		return
	}
	if hasN && n == '\'' && !inDouble {
		j := p.i + 2
		var body strings.Builder
		for j < len(s) && s[j] != '\'' {
			if s[j] == '\\' && j+1 < len(s) {
				switch e := s[j+1]; e {
				case 'n':
					body.WriteByte('\n')
				case 't':
					body.WriteByte('\t')
				default:
					body.WriteByte(e)
				}
				j += 2
				continue
			}
			body.WriteByte(s[j])
			j++
		}
		if j >= len(s) {
			parseFail("unterminated $'")
		}
		w.text.WriteString(body.String())
		w.quoted = true
		p.i = j + 1
		return
	}
	if hasN && (n == '_' || (n >= 'A' && n <= 'Z') || (n >= 'a' && n <= 'z')) {
		m := nameRe.FindString(s[p.i+1:])
		w.text.WriteString("$" + m)
		w.dynamic = true
		p.i += 1 + len(m)
		return
	}
	if hasN && strings.IndexByte("0123456789@*#?$!-", n) >= 0 {
		w.text.WriteString("$" + string(n))
		w.dynamic = true
		p.i += 2
		return
	}
	w.text.WriteByte('$')
	p.i++
}

// parseShell parses a command line (ShellParseError becomes an error).
func parseShell(command string) (script *Script, err error) {
	defer recoverParse(&err)
	return mustParseShell(command), nil
}

func mustParseShell(command string) *Script {
	return newShellParser(command, 0).parse()
}

// recoverParse turns a shellParseError panic into an error.
func recoverParse(err *error) {
	if r := recover(); r != nil {
		if e, ok := r.(shellParseError); ok {
			*err = e
			return
		}
		panic(r)
	}
}

// ---------------------------------------------------------------------------------------------------
// Paths
// ---------------------------------------------------------------------------------------------------

// normalizePath lexically normalizes an absolute (`/…`) or home-relative (`~…`) path.
func normalizePath(p string) string {
	home := p == "~" || strings.HasPrefix(p, "~/")
	rest := p
	if home {
		rest = p[1:]
	}
	out := []string{}
	aboveHome := false
	for _, seg := range strings.Split(rest, "/") {
		if seg == "" || seg == "." {
			continue
		}
		if seg == ".." {
			if len(out) > 0 {
				out = out[:len(out)-1]
			} else if home {
				aboveHome = true
			}
			continue
		}
		out = append(out, seg)
	}
	// Above the (unknown) home directory: somewhere in /Users or /home, treat as the root.
	if home && aboveHome {
		return "/"
	}
	joined := strings.Join(out, "/")
	if home {
		if joined != "" {
			return "~/" + joined
		}
		return "~"
	}
	return "/" + joined
}

// replaceHomeVar replaces a leading `$HOME` / `${HOME}` followed by "/" (or
// by the end when atEnd) with "~": `^(\$HOME|\$\{HOME\})(?=\/|$)`.
func replaceHomeVar(p string, atEnd bool) string {
	for _, v := range []string{"$HOME", "${HOME}"} {
		if strings.HasPrefix(p, v) {
			rest := p[len(v):]
			if strings.HasPrefix(rest, "/") || (atEnd && rest == "") {
				return "~" + rest
			}
		}
	}
	return p
}

// resolvePath resolves a shell word to a path; "" (undefined) when it
// depends on unknown expansions. cwd "" is unknown.
func resolvePath(arg string, cwd string) string {
	p := replaceHomeVar(arg, true)
	if strings.Contains(p, "$(") || strings.Contains(p, "$") || strings.Contains(p, "`") {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		return normalizePath(p)
	}
	if strings.HasPrefix(p, "~") {
		return "" // ~user
	}
	if strings.HasPrefix(p, "/") {
		return normalizePath(p)
	}
	if cwd == "" {
		return ""
	}
	return normalizePath(cwd + "/" + p)
}

func isUnder(path, root string) bool {
	if root == "/" {
		return strings.HasPrefix(path, "/")
	}
	return path == root || strings.HasPrefix(path, root+"/")
}

func isInside(path string, roots []string) bool {
	return anyOf(roots, func(r string) bool { return isUnder(path, r) })
}

var tempRoots = []string{"/tmp", "/private/tmp", "/var/tmp", "/var/folders", "/private/var/folders", "/dev/shm"}

func isTempPath(path string) bool {
	return anyOf(tempRoots, func(r string) bool { return strings.HasPrefix(path, r+"/") && path != r+"/*" })
}

var systemRoots = []string{
	"/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/libx32", "/opt", "/proc", "/root", "/run", "/sbin",
	"/srv", "/sys", "/usr", "/var", "/System", "/Library", "/Applications", "/private", "/cores", "/snap", "/nix",
	"/Volumes", "/mnt", "/media", "/Users", "/home", "/tmp",
}

var homeDotRe = regexp.MustCompile(`^~/\.[^/]`)

// isCriticalPath: paths whose recursive removal (or modification) is never routine: /, home, system directories.
func isCriticalPath(path string) bool {
	p := path
	if strings.HasSuffix(p, "/*") || strings.HasSuffix(p, "/.*") {
		p = p[:strings.LastIndex(p, "/")]
		if p == "" {
			p = "/"
		}
	}
	if p == "/*" || p == "/" || p == "~" {
		return true
	}
	if homeDotRe.MatchString(p) {
		return true // dot directories and files directly in home: ~/.ssh, ~/.config/...
	}
	if isTempPath(p) {
		return false
	}
	segs := filter(strings.Split(p, "/"), func(s string) bool { return s != "" })
	if strings.HasPrefix(p, "~") {
		return false
	}
	top := "/" + atOr(segs, 0)
	if !contains(systemRoots, top) {
		return false
	}
	if top == "/Users" || top == "/home" {
		if len(segs) <= 2 {
			return true // /Users, /Users/alice
		}
		return strings.HasPrefix(segs[2], ".") // /Users/alice/.ssh
	}
	if top == "/Volumes" || top == "/mnt" || top == "/media" {
		return len(segs) <= 2
	}
	return true
}

var shellRC = []string{
	".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".profile", ".zshrc", ".zprofile", ".zshenv", ".zlogin",
	".zlogout", ".kshrc", ".cshrc", ".tcshrc", ".inputrc",
}

var (
	homeUserRe         = jsRe(`^\/(?:Users|home)\/[^/]+\/(.+)$`)
	homeRootRe         = jsRe(`^\/root\/(.+)$`)
	fishConfigRe       = jsRe(`^\.config\/fish\/(config\.fish|conf\.d\/)`)
	sshConfigRe        = jsRe(`^\.ssh\/(config|authorized_keys2?|rc)$`)
	launchAgentsRe     = jsRe(`^Library\/LaunchAgents\/`)
	autostartRe        = jsRe(`^\.config\/(systemd|autostart)\/`)
	etcRe              = jsRe(`^\/(private\/)?etc\/`)
	cronSpoolRe        = jsRe(`^\/(private\/)?var\/(spool\/cron|at)\/`)
	launchDaemonsRe    = jsRe(`^\/Library\/(LaunchAgents|LaunchDaemons)\/`)
	usrBinRe           = jsRe(`^\/usr\/(local\/)?(s?bin|lib)\/`)
	binRe              = jsRe(`^\/s?bin\/`)
	blockDeviceRe      = jsRe(`^\/dev\/(r?disk\d|sd[a-z]|hd[a-z]|vd[a-z]|xvd[a-z]|nvme\d|mmcblk\d|md\d|dm-\d|loop\d|mapper\/)`)
	pubKeyRe           = jsRe(`\.pub$`)
	envTemplateRe      = jsRe(`(^|\/)\.env\.(example|sample|template|dist|defaults)$`)
	devTCPRedirectRe   = jsRe(`^\/dev\/(tcp|udp)\/`)
	devTCPArgRe        = jsRe(`\/dev\/(tcp|udp)\/`)
	scriptNetworkUseRe = jsRe(`\b(urllib|requests|http\.client|socket|fetch\(|https?\.request|Net::HTTP|LWP)\b`)
)

// isPrivilegedWritePath: writing here grants persistence or escalation: shell rc files, ssh config, cron, launchd/systemd, /etc.
func isPrivilegedWritePath(path string) bool {
	homeRel, ok := "", false
	if strings.HasPrefix(path, "~/") {
		homeRel, ok = path[2:], true
	} else if m := homeUserRe.FindStringSubmatch(path); m != nil {
		homeRel, ok = m[1], true
	} else if m := homeRootRe.FindStringSubmatch(path); m != nil {
		homeRel, ok = m[1], true
	}
	if ok {
		if contains(shellRC, homeRel) {
			return true
		}
		if fishConfigRe.MatchString(homeRel) {
			return true
		}
		if sshConfigRe.MatchString(homeRel) {
			return true
		}
		if launchAgentsRe.MatchString(homeRel) {
			return true
		}
		if autostartRe.MatchString(homeRel) {
			return true
		}
	}
	if etcRe.MatchString(path) {
		return true
	}
	if cronSpoolRe.MatchString(path) {
		return true
	}
	if launchDaemonsRe.MatchString(path) {
		return true
	}
	if usrBinRe.MatchString(path) || binRe.MatchString(path) {
		return true
	}
	return false
}

// isBlockDevice: raw block devices (not /dev/null, /dev/tty, ...).
func isBlockDevice(path string) bool {
	return blockDeviceRe.MatchString(path)
}

var secretPatterns = []*regexp.Regexp{
	jsRe(`(^|\/)\.aws\/(credentials|config)$`),
	jsRe(`(^|\/)\.ssh\/(id_[^/]*|identity)$`),
	jsRe(`(^|\/)\.env(\.[^/]*)?$`),
	jsRe(`(^|\/)\.envrc$`),
	jsRe(`(^|\/)[._]netrc$`),
	jsRe(`(^|\/)\.docker\/config\.json$`),
	jsRe(`(^|\/)\.git-credentials$`),
	jsRe(`(^|\/)\.npmrc$`),
	jsRe(`(^|\/)\.pypirc$`),
	jsRe(`(^|\/)\.kube\/config$`),
	jsRe(`(^|\/)\.config\/gcloud(\/|$)`),
	jsRe(`(^|\/)\.config\/gh\/hosts\.yml$`),
	jsRe(`(^|\/)\.azure(\/|$)`),
	jsRe(`(^|\/)\.gnupg(\/|$)`),
	jsRe(`(^|\/)\.password-store(\/|$)`),
	jsRe(`(^|\/)Keychains(\/|$)`),
	jsRe(`^\/etc\/(shadow|gshadow|master\.passwd)$`),
	jsRe(`^\/proc\/[^/]+\/environ$`),
}

func isSecretPath(s string) bool {
	if pubKeyRe.MatchString(s) {
		return false
	}
	if envTemplateRe.MatchString(s) {
		return false
	}
	for _, re := range secretPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// mentionsSecret: a word mentions a credential file: the word itself or the part after `@` / `=` (curl -d @file, -F f=@file).
func mentionsSecret(word string) bool {
	candidates := []string{word, word[strings.LastIndex(word, "@")+1:], word[strings.LastIndex(word, "=")+1:]}
	for _, c := range candidates {
		v := replaceHomeVar(c, false)
		if v != "" && isSecretPath(v) {
			return true
		}
	}
	return false
}

func globToRegExp(glob string) ciRegexp {
	var b strings.Builder
	for _, r := range glob {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return ciRegexp{regexp.MustCompile("(?i)^" + b.String() + "$")}
}

// ---------------------------------------------------------------------------------------------------
// Command model
// ---------------------------------------------------------------------------------------------------

// Cmd is one simple command that would run.
type Cmd struct {
	// argv after wrapper stripping; argv[0] is the program.
	argv []string
	name string
	// words matching argv (same length), for dynamic / substitution checks.
	words     []Word
	assigns   []string
	redirects []*Redirect
	// cwd is the effective working directory ("" when unknown).
	cwd string
	// escalated: program run with elevated privileges (sudo, doas, su, pkexec).
	escalated string
	// unknownArgs: arguments come from stdin (xargs) or a placeholder (find -exec): positionals are unknown.
	unknownArgs bool
	// text is the source text of the simple command (assignments and wrappers included).
	text string
	// stage index inside its pipeline and the pipeline's commands.
	stage    int
	pipeline *[]*Cmd
}

func base(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

var (
	keywords   = setOf("if", "then", "else", "elif", "do", "while", "until", "!", "{")
	neutral    = setOf("", "fi", "done", "esac", "}", "for", "select", "in", ":", "true", "false")
	shellNames = setOf("sh", "bash", "zsh", "dash", "ksh", "mksh", "ash", "fish", "busybox")
	escalators = setOf("sudo", "doas", "pkexec", "run0")
)

type unwrapped struct {
	argv        []string
	words       []Word
	escalated   string
	unknownArgs bool
	// inline scripts to analyze recursively (bash -c, eval, su -c, find -exec).
	inline []string
}

func skipOptions(argv []string, withValue map[string]bool) int {
	i := 0
	for i < len(argv) {
		a := argv[i]
		if a == "--" {
			return i + 1
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			return i
		}
		if withValue[a] {
			i += 2
		} else {
			i++
		}
	}
	return i
}

var (
	shellCFlagRe = jsRe(`^-[a-zA-Z]*c[a-zA-Z]*$`)
	shellSFlagRe = jsRe(`^-[a-zA-Z]*s[a-zA-Z]*$`)

	escalatorOpts = setOf("-u", "-g", "-C", "-h", "-p", "-r", "-t", "-U", "-D", "-R", "-T")
	envValueOpts  = []string{"-u", "-C", "-S", "--unset", "--chdir"}
	execOpts      = setOf("-a", "-t", "-w")
	timeOpts      = setOf("-o", "-f")
	niceOpts      = setOf("-n", "--adjustment")
	ioniceOpts    = setOf("-c", "-n", "-p")
	stdbufOpts    = setOf("-i", "-o", "-e")
	timeoutOpts   = setOf("-s", "-k", "--signal", "--kill-after")
	watchOpts     = setOf("-n", "--interval", "-d")
	xargsOpts     = setOf("-I", "-i", "-n", "-P", "-L", "-l", "-d", "-E", "-e", "-s", "-a", "--max-args", "--max-procs", "--delimiter", "--arg-file")
	noOpts        = map[string]bool{}
)

func unwrap(argvIn []string, wordsIn []Word) unwrapped {
	argv := argvIn
	words := wordsIn
	escalated := ""
	unknownArgs := false
	var inline []string
	cut := func(n int) {
		n = min(n, len(argv))
		argv = argv[n:]
		words = words[min(n, len(words)):]
	}
	result := func() unwrapped {
		return unwrapped{argv: argv, words: words, escalated: escalated, unknownArgs: unknownArgs, inline: inline}
	}
	empty := func() unwrapped {
		return unwrapped{argv: []string{}, words: []Word{}, escalated: escalated, unknownArgs: unknownArgs, inline: inline}
	}
loop:
	for guard := 0; guard < 16 && len(argv) > 0; guard++ {
		a0 := argv[0]
		name := base(a0)
		if keywords[a0] {
			cut(1)
			continue
		}
		if assignRe.MatchString(a0) && guard > 0 {
			cut(1)
			continue
		}
		if escalators[name] {
			escalated = name
			cut(1)
			cut(skipOptions(argv, escalatorOpts))
			continue
		}
		if name == "su" {
			escalated = "su"
			if ci := indexOf(argv, "-c"); ci >= 0 {
				if body, ok := at(argv, ci+1); ok {
					inline = append(inline, body)
				}
			}
			return empty()
		}
		switch name {
		case "env":
			cut(1)
			for len(argv) > 0 && (strings.HasPrefix(argv[0], "-") || assignRe.MatchString(argv[0])) {
				if contains(envValueOpts, argv[0]) {
					cut(2)
				} else {
					cut(1)
				}
			}
			continue
		case "command", "builtin":
			if a1 := atOr(argv, 1); a1 == "-v" || a1 == "-V" {
				return result()
			}
			cut(1)
			cut(skipOptions(argv, noOpts))
			continue
		case "exec", "nohup", "noglob", "chronic", "unbuffer", "caffeinate":
			cut(1)
			cut(skipOptions(argv, execOpts))
			continue
		case "time":
			cut(1)
			cut(skipOptions(argv, timeOpts))
			continue
		case "nice":
			cut(1)
			cut(skipOptions(argv, niceOpts))
			continue
		case "ionice":
			cut(1)
			cut(skipOptions(argv, ioniceOpts))
			continue
		case "stdbuf":
			cut(1)
			cut(skipOptions(argv, stdbufOpts))
			continue
		case "timeout", "gtimeout":
			cut(1)
			cut(skipOptions(argv, timeoutOpts))
			cut(1) // duration
			continue
		case "watch":
			cut(1)
			cut(skipOptions(argv, watchOpts))
			if len(argv) > 1 {
				inline = append(inline, strings.Join(argv, " "))
				return empty()
			}
			continue
		case "xargs":
			cut(1)
			cut(skipOptions(argv, xargsOpts))
			unknownArgs = true
			if len(argv) == 0 {
				return unwrapped{argv: []string{"echo"}, words: []Word{w("echo")}, escalated: escalated, unknownArgs: unknownArgs, inline: inline}
			}
			continue
		case "eval":
			inline = append(inline, strings.Join(argv[1:], " "))
			return result()
		}
		if shellNames[name] {
			ci := -1
			for i, x := range argv {
				if i > 0 && shellCFlagRe.MatchString(x) {
					ci = i
					break
				}
			}
			if ci >= 0 {
				if body, ok := at(argv, ci+1); ok {
					inline = append(inline, body)
					return result()
				}
			}
		}
		break loop
	}
	return result()
}

func w(text string) Word { return Word{Text: text} }

func wordTexts(ws []Word) []string {
	out := make([]string, len(ws))
	for i, x := range ws {
		out[i] = x.Text
	}
	return out
}

func cmdText(sc *SimpleCommand) string {
	return strings.Join(append(wordTexts(sc.Assigns), wordTexts(sc.Words)...), " ")
}

var findExecFlags = []string{"-exec", "-execdir", "-ok", "-okdir", "-x", "--exec", "-X", "--exec-batch"}

// flatten walks a script, expanding nested scripts (subshells, substitutions, bash -c bodies) into simple commands.
// It panics with shellParseError like the TypeScript throws.
func flatten(script *Script, cwd string, out *[]*Cmd, depth int) string {
	if depth > maxDepth {
		parseFail("nesting too deep")
	}
	cur := cwd
	for _, item := range script.Items {
		stageCmds := &[]*Cmd{}
		for stage, sc := range item.Pipeline.Cmds {
			for _, a := range sc.Assigns {
				for _, sub := range a.Subs {
					flatten(sub, cur, out, depth+1)
				}
			}
			for _, word := range sc.Words {
				for _, sub := range word.Subs {
					flatten(sub, cur, out, depth+1)
				}
				for _, sub := range word.ProcSubs {
					flatten(sub, cur, out, depth+1)
				}
			}
			for _, r := range sc.Redirects {
				for _, sub := range r.Target.Subs {
					flatten(sub, cur, out, depth+1)
				}
			}
			if sc.Group != nil {
				flatten(sc.Group, cur, out, depth+1)
				continue
			}
			u := unwrap(wordTexts(sc.Words), sc.Words)
			for _, body := range u.inline {
				flatten(mustParseShell(body), cur, out, depth+1)
			}
			name := ""
			if len(u.argv) > 0 {
				name = base(u.argv[0])
			}
			cmd := &Cmd{
				argv:        u.argv,
				name:        name,
				words:       u.words,
				assigns:     wordTexts(sc.Assigns),
				redirects:   sc.Redirects,
				cwd:         cur,
				text:        cmdText(sc),
				stage:       stage,
				pipeline:    stageCmds,
				escalated:   u.escalated,
				unknownArgs: u.unknownArgs,
			}
			// A shell reading a heredoc runs its body.
			if shellNames[name] && len(u.inline) == 0 {
				for _, r := range sc.Redirects {
					if r.Body != nil {
						flatten(mustParseShell(*r.Body), cur, out, depth+1)
					}
				}
			}
			// find -exec / -execdir / -ok run a command with placeholder arguments.
			if name == "find" || name == "fd" {
				idx := -1
				for i, x := range u.argv {
					if contains(findExecFlags, x) {
						idx = i
						break
					}
				}
				if idx >= 0 {
					end := -1
					for i, x := range u.argv {
						if i > idx && (x == ";" || x == "+" || x == "\\;") {
							end = i
							break
						}
					}
					if end < 0 {
						end = len(u.argv)
					}
					sub := u.argv[idx+1 : end]
					if len(sub) > 0 {
						subWords := make([]Word, len(sub))
						for i, x := range sub {
							subWords[i] = w(x)
						}
						su := unwrap(sub, subWords)
						subName := ""
						if len(su.argv) > 0 {
							subName = base(su.argv[0])
						}
						*out = append(*out, &Cmd{
							argv:        su.argv,
							name:        subName,
							words:       su.words,
							assigns:     []string{},
							redirects:   nil,
							cwd:         cur,
							text:        strings.Join(sub, " "),
							stage:       0,
							pipeline:    &[]*Cmd{},
							unknownArgs: true,
							escalated:   su.escalated,
						})
						for _, body := range su.inline {
							flatten(mustParseShell(body), cur, out, depth+1)
						}
					}
				}
			}
			*stageCmds = append(*stageCmds, cmd)
			*out = append(*out, cmd)
			// cd / pushd change the directory for the rest of the list (not across a pipeline).
			if (name == "cd" || name == "pushd") && len(item.Pipeline.Cmds) == 1 {
				target, ok := at(u.argv, 1)
				switch {
				case !ok:
					cur = "~"
				case target == "-":
					cur = ""
				default:
					cur = resolvePath(target, cur)
				}
			}
		}
	}
	return cur
}

// flattenCommand parses and flattens a command line; any failure (a parse
// error or anything else thrown while flattening) is returned as an error,
// like the try/catch around it in evaluateRules.
func flattenCommand(src, cwd string) (cmds []*Cmd, err error) {
	defer func() {
		if r := recover(); r != nil {
			cmds = nil
			if e, ok := r.(shellParseError); ok {
				err = e
			} else {
				err = shellParseError("internal error")
			}
		}
	}()
	flatten(mustParseShell(src), cwd, &cmds, 0)
	return cmds, nil
}

// ---------------------------------------------------------------------------------------------------
// Option helpers
// ---------------------------------------------------------------------------------------------------

type parsedArgs struct {
	shorts      map[string]bool
	longs       map[string]bool
	positionals []string
	// values by option (for options listed in withValue).
	values map[string][]string
}

var lettersOptRe = jsRe(`^-[A-Za-z]+$`)

func parseArgs(args []string, withValue ...string) parsedArgs {
	p := parsedArgs{shorts: map[string]bool{}, longs: map[string]bool{}, values: map[string][]string{}}
	wv := setOf(withValue...)
	addValue := func(k, v string) { p.values[k] = append(p.values[k], v) }
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			p.positionals = append(p.positionals, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "--") && jsLength(a) > 2 {
			eq := strings.Index(a, "=")
			k := a
			if eq >= 0 {
				k = a[:eq]
			}
			if eq >= 0 {
				p.longs[k] = true
				addValue(k, a[eq+1:])
			} else if wv[k] && i+1 < len(args) {
				p.longs[k] = true
				i++
				addValue(k, args[i])
			} else {
				p.longs[k] = true
			}
			continue
		}
		if strings.HasPrefix(a, "-") && jsLength(a) > 1 {
			if wv[a] && i+1 < len(args) {
				i++
				addValue(a, args[i])
				p.shorts[a[1:]] = true
				continue
			}
			if lettersOptRe.MatchString(a) {
				for _, ch := range a[1:] {
					p.shorts[string(ch)] = true
				}
			} else {
				p.shorts[a[1:]] = true
			}
			continue
		}
		p.positionals = append(p.positionals, a)
	}
	return p
}

func hasFlag(argv []string, flags ...string) bool {
	return anyOf(argv, func(a string) bool {
		return anyOf(flags, func(f string) bool {
			return a == f || (strings.HasPrefix(f, "--") && strings.HasPrefix(a, f+"="))
		})
	})
}

// ---------------------------------------------------------------------------------------------------
// Rule context and hits
// ---------------------------------------------------------------------------------------------------

type hit struct {
	rule       string
	reason     string
	categories []string
}

type rulesCtx struct {
	roots             []string
	cwd               string
	facts             RulesFacts
	protectedBranches []ciRegexp
	denyAllForcePush  bool
}

var fetcherList = []string{"curl", "wget", "fetch", "http", "https", "xh", "httpie", "lwp-request", "aria2c"}

var (
	fetchers     = setOf(fetcherList...)
	networkTools = setOf(append(append([]string{}, fetcherList...),
		"nc", "ncat", "netcat", "socat", "telnet", "ftp", "sftp", "scp", "ssh", "rsync", "openssl", "gh", "aws", "gsutil",
		"gcloud", "az", "rclone", "s3cmd", "mail", "sendmail", "mutt", "dig", "nslookup", "host", "ping", "smbclient", "tftp",
	)...)
)

var (
	pythonRe      = jsRe(`^python[0-9.]*$`)
	interpreterRe = jsRe(`^(perl|ruby|node|nodejs|deno|bun|php|pwsh|powershell|lua|tclsh|osascript)$`)
	perlLikeRe    = jsRe(`^(perl|ruby|php|lua)$`)
	nodeLikeRe    = jsRe(`^(node|nodejs|bun)$`)
)

func isInterpreter(name string) bool {
	return shellNames[name] || pythonRe.MatchString(name) || interpreterRe.MatchString(name)
}

// readsProgramFromStdin: the interpreter takes its program from stdin (so piping a download into it executes the download).
func readsProgramFromStdin(c *Cmd) bool {
	args := tail(c.argv)
	name := c.name
	positional := func() []string {
		return filter(args, func(a string) bool { return !strings.HasPrefix(a, "-") || a == "-" })
	}
	if shellNames[name] {
		if anyOf(args, shellCFlagRe.MatchString) {
			return false
		}
		if anyOf(args, func(a string) bool { return shellSFlagRe.MatchString(a) || a == "-" }) {
			return true
		}
		return !anyOf(args, func(a string) bool { return !strings.HasPrefix(a, "-") })
	}
	if pythonRe.MatchString(name) {
		if hasFlag(args, "-c", "-m") {
			return false
		}
		pos := positional()
		return len(pos) == 0 || pos[0] == "-"
	}
	if perlLikeRe.MatchString(name) {
		if hasFlag(args, "-e", "-E", "-r") {
			return false
		}
		pos := positional()
		return len(pos) == 0 || pos[0] == "-"
	}
	if nodeLikeRe.MatchString(name) {
		if hasFlag(args, "-e", "--eval", "-p", "--print") {
			return false
		}
		pos := positional()
		return len(pos) == 0 || pos[0] == "-"
	}
	if name == "deno" {
		return contains(args, "-")
	}
	if name == "pwsh" || name == "powershell" {
		return !hasFlag(args, "-c", "-Command", "-File", "-f")
	}
	return false
}

func containsFetcher(scripts []*Script, cwd string) bool {
	for _, s := range scripts {
		var cmds []*Cmd
		func() {
			// The TypeScript lets an error escape here; the same scripts were
			// already flattened deeper without one, so this cannot fail.
			defer func() { _ = recover() }()
			flatten(s, cwd, &cmds, 1)
		}()
		for _, c := range cmds {
			if fetchers[c.name] {
				return true
			}
		}
	}
	return false
}

var inPlaceRe = jsRe(`^-[a-zA-Z]*i`)

// writeTargets: positional targets of a command that writes (for privileged-path checks).
func writeTargets(c *Cmd) []string {
	args := tail(c.argv)
	switch c.name {
	case "cp", "mv", "install", "ln", "rsync":
		p := parseArgs(args, "-t", "--target-directory", "-m", "--mode", "-o", "-g", "-S", "--suffix", "-e")
		if t, ok := p.values["-t"]; ok {
			return t
		}
		if t, ok := p.values["--target-directory"]; ok {
			return t
		}
		if len(p.positionals) >= 2 {
			return []string{p.positionals[len(p.positionals)-1]}
		}
		return nil
	case "tee", "truncate", "touch", "rm", "unlink", "shred":
		return parseArgs(args, "-s", "--size", "-r", "--reference", "-d", "-t", "-n").positionals
	case "sed", "gsed", "perl":
		if !anyOf(args, func(a string) bool { return inPlaceRe.MatchString(a) || strings.HasPrefix(a, "--in-place") }) {
			return nil
		}
		p := parseArgs(args, "-e", "-f", "--expression", "--file")
		_, e := p.values["-e"]
		_, ex := p.values["--expression"]
		_, f := p.values["-f"]
		_, fl := p.values["--file"]
		if e || ex || f || fl {
			return p.positionals
		}
		if len(p.positionals) == 0 {
			return nil
		}
		return p.positionals[1:]
	case "dd":
		var out []string
		for _, a := range args {
			if strings.HasPrefix(a, "of=") {
				out = append(out, a[3:])
			}
		}
		return out
	case "chmod", "chown", "chgrp":
		pos := parseArgs(args).positionals
		if len(pos) == 0 {
			return nil
		}
		return pos[1:]
	default:
		return nil
	}
}

var (
	writeOps   = setOf(">", ">>", ">|", "&>", "&>>", "<>")
	fdOrDashRe = jsRe(`^(\d+|-)$`)
)

func redirectWrites(c *Cmd) []*Redirect {
	var out []*Redirect
	for _, r := range c.redirects {
		if writeOps[r.Op] || (r.Op == ">&" && !fdOrDashRe.MatchString(r.Target.Text)) {
			out = append(out, r)
		}
	}
	return out
}

func redirectTargets(rs []*Redirect) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Target.Text
	}
	return out
}

// ---------------------------------------------------------------------------------------------------
// Hard-deny checks (per simple command)
// ---------------------------------------------------------------------------------------------------

var destructive = []string{CatDestructive}

func checkEscalation(c *Cmd, _ *rulesCtx) *hit {
	if c.escalated != "" {
		return &hit{"privilege.sudo", "Runs a command with elevated privileges (" + c.escalated + ")", []string{CatPrivilegeEscalation}}
	}
	return nil
}

func checkRm(c *Cmd, ctx *rulesCtx) *hit {
	if c.name != "rm" {
		return nil
	}
	p := parseArgs(tail(c.argv))
	if p.longs["--no-preserve-root"] {
		return &hit{"destructive.rm_root", "Removes files with --no-preserve-root", destructive}
	}
	recursive := p.shorts["r"] || p.shorts["R"] || p.longs["--recursive"]
	var targets []string // "" = undefined
	for _, t := range p.positionals {
		targets = append(targets, resolvePath(t, c.cwd))
	}
	if recursive && len(ctx.facts.WritePaths) > 0 {
		for _, x := range ctx.facts.WritePaths {
			targets = append(targets, resolvePath(x, ""))
		}
	}
	safeRoots := filter(ctx.roots, func(r string) bool { return !isCriticalPath(r) })
	for _, t := range targets {
		if t == "" {
			continue
		}
		bare := t
		if strings.HasSuffix(t, "/*") {
			bare = t[:len(t)-2]
			if bare == "" {
				bare = "/"
			}
		}
		if recursive && contains(ctx.roots, bare) {
			return &hit{"destructive.rm_workspace", "Recursively removes the whole working directory", destructive}
		}
		if isInside(t, safeRoots) {
			continue
		}
		if isCriticalPath(t) {
			what := t
			if t == "~" {
				what = "the home directory"
			}
			return &hit{"destructive.rm_critical", "Removes " + what + ", a system or home path", destructive}
		}
		if recursive && len(ctx.roots) > 0 && !isInside(t, ctx.roots) && !isTempPath(t) && !strings.HasPrefix(t, "~") {
			return &hit{"destructive.rm_outside", "Recursively removes " + t + ", outside the working directories", destructive}
		}
	}
	return nil
}

func checkFindDelete(c *Cmd, ctx *rulesCtx) *hit {
	if c.name != "find" {
		return nil
	}
	args := tail(c.argv)
	deletes := contains(args, "-delete")
	for i, a := range args {
		if (a == "-exec" || a == "-execdir") && contains([]string{"rm", "shred"}, base(atOr(args, i+1))) {
			deletes = true
		}
	}
	if !deletes {
		return nil
	}
	var roots []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") || a == "!" || a == "(" {
			break
		}
		roots = append(roots, a)
	}
	for _, r := range roots {
		t := resolvePath(r, c.cwd)
		if t != "" && !isInside(t, filter(ctx.roots, func(x string) bool { return !isCriticalPath(x) })) && isCriticalPath(t) {
			return &hit{"destructive.find_delete", "Deletes files found under " + t, destructive}
		}
	}
	return nil
}

var (
	diskFormatRe    = jsRe(`^(mkfs(\..+)?|mke2fs|mkswap|newfs(_.+)?|wipefs|sfdisk|fdisk|gdisk|sgdisk|parted|cfdisk|blkdiscard)$`)
	diskutilEraseRe = jsReI(`^(erase\w*|zeroDisk|randomDisk|secureErase|partitionDisk|reformat)$`)
	apfsDeleteRe    = jsReI(`^delete`)
)

func checkDevices(c *Cmd, _ *rulesCtx) *hit {
	n := c.name
	if diskFormatRe.MatchString(n) {
		return &hit{"destructive.disk_format", "Formats or repartitions a disk (" + n + ")", destructive}
	}
	dsub := atOr(c.argv, 1)
	if n == "diskutil" && (diskutilEraseRe.MatchString(dsub) || (dsub == "apfs" && apfsDeleteRe.MatchString(atOr(c.argv, 2)))) {
		return &hit{"destructive.disk_format", "Erases or repartitions a disk (diskutil)", destructive}
	}
	targets := append(writeTargets(c), redirectTargets(redirectWrites(c))...)
	if n == "shred" || n == "dd" || n == "cp" || n == "cat" || n == "tee" || len(redirectWrites(c)) > 0 {
		for _, t := range targets {
			if p := resolvePath(t, c.cwd); p != "" && isBlockDevice(p) {
				return &hit{"destructive.device_write", "Writes directly to the block device " + p, destructive}
			}
		}
	}
	return nil
}

func checkPrivilegedWrites(c *Cmd, _ *rulesCtx) *hit {
	if c.name == "crontab" {
		if parseArgs(tail(c.argv), "-u").shorts["r"] {
			return &hit{"destructive.crontab_remove", "Removes the whole crontab (crontab -r)", destructive}
		}
		args := filter(tail(c.argv), func(a string) bool { return a != "-u" })
		if !(len(args) == 1 && args[0] == "-l") {
			return &hit{"privilege.crontab", "Installs or edits a crontab", []string{CatPrivilegeEscalation}}
		}
	}
	// Deletions are judged by checkRm (destructive), not as edits.
	removes := c.name == "rm" || c.name == "unlink" || c.name == "shred"
	var targets []string
	if !removes {
		targets = writeTargets(c)
	}
	targets = append(targets, redirectTargets(redirectWrites(c))...)
	for _, t := range targets {
		if p := resolvePath(t, c.cwd); p != "" && isPrivilegedWritePath(p) && !isBlockDevice(p) {
			return &hit{"privilege.protected_write", "Modifies " + p + " (shell startup, ssh, scheduler or system file)", []string{CatPrivilegeEscalation}}
		}
	}
	return nil
}

var (
	numericModeRe = jsRe(`^0*([0-7]{3,4})$`)
	wideModeRes   = []*regexp.Regexp{jsRe(`^[ugoa]*[oa][ugoa]*[+=][rwxXst]*w`), jsRe(`^[+=][rwxXst]*w`), jsRe(`^[ugoa]*[+=][rwxXt]*s`)}
)

var setidModeRe = jsRe(`^[ugoa]*[+=][rwxXt]*s`)

// isSetidMode: modes that set the setuid or setgid bit (u+s, g+s, +s, 4xxx, 2xxx, 6xxx).
func isSetidMode(mode string) bool {
	if m := numericModeRe.FindStringSubmatch(mode); m != nil {
		num := m[1]
		return len(num) == 4 && int(num[0]-'0')&6 != 0
	}
	return anyOf(strings.Split(mode, ","), setidModeRe.MatchString)
}

// isWideMode: world-writable or setuid/setgid modes.
func isWideMode(mode string) bool {
	if m := numericModeRe.FindStringSubmatch(mode); m != nil {
		num := m[1]
		others := int(num[len(num)-1] - '0')
		special := 0
		if len(num) == 4 {
			special = int(num[0] - '0')
		}
		return others&2 != 0 || special&6 != 0
	}
	return anyOf(strings.Split(mode, ","), func(part string) bool {
		for _, re := range wideModeRes {
			if re.MatchString(part) {
				return true
			}
		}
		return false
	})
}

func checkChmod(c *Cmd, ctx *rulesCtx) *hit {
	if c.name != "chmod" && c.name != "chown" && c.name != "chgrp" {
		return nil
	}
	p := parseArgs(tail(c.argv))
	recursive := p.shorts["R"] || p.longs["--recursive"]
	if len(p.positionals) == 0 || p.positionals[0] == "" {
		return nil
	}
	mode, targets := p.positionals[0], p.positionals[1:]
	// setuid/setgid binaries are an escalation path wherever they live, the workspace included.
	if c.name == "chmod" && len(targets) > 0 && isSetidMode(mode) {
		return &hit{"privilege.chmod_setuid", "Sets the setuid or setgid bit (chmod " + mode + ")", []string{CatPrivilegeEscalation}}
	}
	wide := c.name == "chmod" && isWideMode(mode)
	safeRoots := filter(ctx.roots, func(r string) bool { return !isCriticalPath(r) })
	for _, t := range targets {
		r := resolvePath(t, c.cwd)
		if r == "" {
			continue
		}
		if recursive && !isInside(r, safeRoots) && isCriticalPath(r) {
			return &hit{"privilege.chmod_system", "Recursively changes ownership or permissions of " + r, []string{CatPrivilegeEscalation}}
		}
		if wide && !isInside(r, ctx.roots) && !isTempPath(r) {
			return &hit{"privilege.chmod_wide", "Makes " + r + " world-writable or setuid outside the working directories", []string{CatPrivilegeEscalation}}
		}
	}
	return nil
}

var gitGlobalWithValue = setOf("-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env")

func gitSub(argv []string) (sub string, args []string, globals []string) {
	i := 1
	for i < len(argv) {
		a := argv[i]
		if gitGlobalWithValue[a] {
			globals = append(globals, a, atOr(argv, i+1))
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-") {
			globals = append(globals, a)
			i++
			continue
		}
		break
	}
	sub = atOr(argv, i)
	if i+1 <= len(argv) {
		args = argv[i+1:]
	}
	return sub, args, globals
}

var (
	prodRe        = jsReI(`(^|[^a-z0-9])(prod|production|prd)([^a-z0-9]|$)`)
	remoteRefRe   = jsRe(`^(?:refs\/remotes\/)?(origin|upstream)\/(.+)$`)
	upstreamRefRe = jsRe(`@\{(u|upstream|push)\}$`)
)

func checkGit(c *Cmd, ctx *rulesCtx) *hit {
	if c.name != "git" {
		return nil
	}
	sub, args, _ := gitSub(c.argv)
	protectedBranch := func(b string) bool {
		for _, re := range ctx.protectedBranches {
			if re.MatchString(b) {
				return true
			}
		}
		return false
	}
	if sub == "push" {
		p := parseArgs(args, "-o", "--push-option", "--receive-pack", "--exec", "--repo")
		force := p.shorts["f"] || p.longs["--force"] || p.longs["--force-with-lease"] || p.longs["--mirror"] ||
			(len(p.positionals) > 1 && anyOf(p.positionals[1:], func(r string) bool { return strings.HasPrefix(r, "+") }))
		del := p.shorts["d"] || p.longs["--delete"]
		var remote string
		var refspecs []string
		if len(p.positionals) > 0 {
			remote, refspecs = p.positionals[0], p.positionals[1:]
		}
		var current *string // facts.git_branch || undefined
		if ctx.facts.GitBranch != "" {
			b := ctx.facts.GitBranch
			current = &b
		}
		var targets []*string // nil = undefined
		for _, r := range refspecs {
			s := strings.TrimPrefix(r, "+")
			dst := s
			if i := strings.Index(s, ":"); i >= 0 {
				dst = s[i+1:]
			}
			b := strings.TrimPrefix(dst, "refs/heads/")
			if b == "HEAD" {
				targets = append(targets, current)
			} else {
				targets = append(targets, &b)
			}
		}
		if len(refspecs) == 0 {
			if p.longs["--all"] || p.longs["--mirror"] {
				all := "*all*"
				targets = []*string{&all}
			} else {
				targets = []*string{current}
			}
		}
		if remote != "" && prodRe.MatchString(remote) && !strings.Contains(remote, "/") && !strings.Contains(remote, ":") {
			return &hit{"production.git_push", `Pushes to the "` + remote + `" remote, which looks like a production deploy`, []string{CatProductionChange}}
		}
		if force {
			if ctx.denyAllForcePush {
				return &hit{"git.force_push", "Force-pushes, which rewrites remote history", []string{CatGitHistoryRewrite}}
			}
			for _, t := range targets {
				if t != nil && (*t == "*all*" || protectedBranch(*t)) {
					reason := "Force-pushes to the shared branch " + *t
					if *t == "*all*" {
						reason = "Force-pushes every branch"
					}
					return &hit{"git.force_push", reason, []string{CatGitHistoryRewrite}}
				}
			}
		}
		var deletes []*string
		if del {
			deletes = targets
		} else {
			for _, r := range refspecs {
				if strings.HasPrefix(r, ":") {
					b := strings.TrimPrefix(r[1:], "refs/heads/")
					deletes = append(deletes, &b)
				}
			}
		}
		if del || len(deletes) > 0 {
			for _, t := range deletes {
				if t != nil && protectedBranch(*t) {
					return &hit{"git.delete_branch", "Deletes the shared remote branch " + *t, []string{CatGitHistoryRewrite}}
				}
			}
		}
		return nil
	}
	if sub == "reset" && contains(args, "--hard") {
		ref := ""
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				ref = a
				break
			}
		}
		if ref == "" {
			return nil
		}
		m := remoteRefRe.FindStringSubmatch(ref)
		upstreamRef := upstreamRefRe.MatchString(ref)
		if m == nil && !upstreamRef {
			return nil
		}
		target := ""
		if m != nil {
			target = m[2]
		}
		current := ctx.facts.GitBranch
		if (target != "" && protectedBranch(target)) || (current != "" && protectedBranch(current)) {
			b := target
			if current != "" && protectedBranch(current) {
				b = current
			}
			return &hit{"git.reset_shared", "Hard-resets the shared branch " + b, []string{CatGitHistoryRewrite}}
		}
	}
	return nil
}

// kubeContext returns the --context / --kube-context value (ok=false when absent or without a value).
func kubeContext(argv []string) (string, bool) {
	for i, a := range argv {
		if a == "--context" || a == "--kube-context" {
			return at(argv, i+1)
		}
		if strings.HasPrefix(a, "--context=") || strings.HasPrefix(a, "--kube-context=") {
			return a[strings.Index(a, "=")+1:], true
		}
	}
	return "", false
}

var localKubeContextRe = jsReI(`^(kind-|k3d-|minikube|docker-desktop|docker-for-desktop|rancher-desktop|colima|orbstack|microk8s|local|localhost|dev-local)`)

func isLocalKubeContext(ctxName string) bool {
	return localKubeContextRe.MatchString(ctxName)
}

var kubectlWithValue = setOf("--context", "-n", "--namespace", "--kubeconfig", "-l", "--selector", "-f", "--filename", "-o", "--output", "--cluster", "--user", "-s", "--server", "--as", "-c", "--container")

func firstPositional(args []string, withValue map[string]bool) (sub string, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if withValue[a] {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a, args[i+1:]
	}
	return "", nil
}

var (
	kubeMutating      = setOf("apply", "create", "replace", "patch", "scale", "rollout", "set", "edit", "drain", "cordon", "label", "annotate", "delete", "expose", "autoscale", "taint")
	localKubeServerRe = jsRe(`^(https?:\/\/)?(localhost|127\.0\.0\.1|\[::1\])(:|\/|$)`)
	pulumiOpts        = setOf("-s", "--stack", "-C", "--cwd")
	helmOpts          = setOf("-n", "--namespace", "--kube-context", "--kubeconfig")
)

// isLocalKube: the target cluster is clearly local: a local --context, or (without one) a loopback --server.
func isLocalKube(kctx string, hasKctx bool, server string, hasServer bool) bool {
	if hasKctx {
		return isLocalKubeContext(kctx)
	}
	return hasServer && localKubeServerRe.MatchString(server)
}

var zerosRe = jsRe(`^0+$`)

func isKubeDryRun(args []string) bool {
	return anyOf(args, func(a string) bool {
		return a == "--dry-run" || (strings.HasPrefix(a, "--dry-run=") && a[10:] != "none" && a[10:] != "false")
	})
}

// kubeDisruptive names the kubectl operation that takes workloads or nodes
// out of service ("" when the call is none of them).
func kubeDisruptive(sub string, rest, args []string) string {
	if isKubeDryRun(args) {
		return ""
	}
	switch sub {
	case "drain", "cordon", "taint":
		return sub
	case "scale":
		for i, a := range args {
			v, ok := "", false
			if a == "--replicas" {
				v, ok = at(args, i+1)
			} else if strings.HasPrefix(a, "--replicas=") {
				v, ok = a[11:], true
			}
			if ok && zerosRe.MatchString(v) {
				return "scale --replicas=0"
			}
		}
	case "replace":
		if hasFlag(args, "--force") {
			return "replace --force"
		}
	case "delete":
		if hasFlag(args, "--all", "--all-namespaces") || contains(args, "-A") {
			return "delete --all"
		}
	case "rollout":
		if undo, _ := firstPositional(rest, kubectlWithValue); undo == "undo" {
			return "rollout undo"
		}
	}
	return ""
}

func checkInfra(c *Cmd, _ *rulesCtx) *hit {
	n := c.name
	args := tail(c.argv)
	if n == "terraform" || n == "tofu" || n == "terragrunt" {
		sub, _ := firstPositional(args, noOpts)
		if sub == "destroy" || (sub == "apply" && hasFlag(args, "-destroy", "--destroy")) {
			return &hit{"production.terraform_destroy", "Destroys infrastructure (" + n + " destroy)", []string{CatDestructive, CatProductionChange}}
		}
		if sub == "apply" && hasFlag(args, "-auto-approve", "--auto-approve") {
			return &hit{"production.terraform_apply", "Applies infrastructure changes without review (" + n + " apply -auto-approve)", []string{CatProductionChange}}
		}
	}
	if n == "pulumi" {
		sub, _ := firstPositional(args, pulumiOpts)
		if sub == "destroy" {
			return &hit{"production.pulumi_destroy", "Destroys a Pulumi stack", []string{CatDestructive, CatProductionChange}}
		}
		if (sub == "up" || sub == "update") && hasFlag(args, "--yes", "-y", "--skip-preview", "-f") {
			return &hit{"production.pulumi_up", "Applies Pulumi changes without review", []string{CatProductionChange}}
		}
	}
	if n == "kubectl" || n == "oc" {
		sub, rest := firstPositional(args, kubectlWithValue)
		kctx, hasKctx := kubeContext(args)
		optValue := func(short, long string) (string, bool) {
			for i, a := range args {
				if a == short || a == long {
					return at(args, i+1)
				}
			}
			for _, a := range args {
				if strings.HasPrefix(a, long+"=") {
					return a[len(long)+1:], true
				}
			}
			return "", false
		}
		ns, _ := optValue("-n", "--namespace")
		server, hasServer := optValue("-s", "--server")
		if sub == "delete" {
			remoteServer := hasServer && !localKubeServerRe.MatchString(server)
			if (hasKctx && !isLocalKubeContext(kctx)) || remoteServer {
				where := server
				if hasKctx {
					where = kctx
				}
				return &hit{"production.kubectl_delete", "Deletes Kubernetes resources in the non-local context " + where, []string{CatDestructive, CatProductionChange}}
			}
		}
		if disruptive := kubeDisruptive(sub, rest, args); disruptive != "" && !isLocalKube(kctx, hasKctx, server, hasServer) {
			where := "the current context"
			if hasKctx {
				where = "the context " + kctx
			} else if hasServer {
				where = "the server " + server
			}
			cats := []string{CatProductionChange}
			if sub == "delete" {
				cats = []string{CatDestructive, CatProductionChange}
			}
			return &hit{"production.kubectl_disruptive", "Runs kubectl " + disruptive + " against " + where + ", which is not a local cluster", cats}
		}
		if kubeMutating[sub] && ((kctx != "" && prodRe.MatchString(kctx)) || (ns != "" && prodRe.MatchString(ns))) {
			where := ns
			if hasKctx {
				where = kctx
			}
			return &hit{"production.kubectl_prod", "Changes Kubernetes resources in production (" + where + ")", []string{CatProductionChange}}
		}
	}
	if n == "helm" {
		sub, _ := firstPositional(args, helmOpts)
		if contains([]string{"uninstall", "delete", "del", "un"}, sub) {
			return &hit{"production.helm_uninstall", "Uninstalls a Helm release", []string{CatDestructive, CatProductionChange}}
		}
	}
	return nil
}

var (
	awsWithValue = setOf("--profile", "--region", "--output", "--endpoint-url", "--query", "--color", "--ca-bundle", "--cli-read-timeout", "--cli-connect-timeout", "--cli-binary-format")
	awsDeleteOps = setOf(
		"terminate-instances", "delete-db-instance", "delete-db-cluster", "delete-stack", "delete-table", "delete-function", "delete-cluster",
		"delete-nodegroup", "delete-bucket", "delete-repository", "delete-hosted-zone", "delete-secret", "delete-file-system",
		"delete-load-balancer", "delete-vpc", "delete-log-group", "delete-queue", "delete-topic", "delete-service", "schedule-key-deletion",
	)
	// gcloud / az command groups that only touch local configuration.
	gcloudLocalGroups = setOf("config", "auth", "components", "topic", "help", "info")
	azLocalGroups     = setOf("config", "extension", "cache", "account", "login", "logout", "cloud")
	gsutilWithValue   = setOf("-o", "-h", "-p", "-u")
	herokuWithValue   = setOf("-a", "--app", "-r", "--remote")
	vercelWithValue   = setOf("--token", "-t", "--scope", "-S", "--cwd", "-A", "--local-config", "-Q", "--global-config")
	netlifyWithValue  = setOf("--auth")
	herokuDestroyRe   = jsRe(`(^|:)destroy$`)
)

// checkCloudDelete: deletes of cloud resources through the providers' CLIs
// (read and list verbs are not affected).
func checkCloudDelete(c *Cmd, _ *rulesCtx) *hit {
	n := c.name
	args := tail(c.argv)
	pos := filter(args, func(a string) bool { return !strings.HasPrefix(a, "-") })
	verbAt := func(limit int, verbs ...string) int {
		for i, x := range pos {
			if i >= limit {
				break
			}
			if contains(verbs, x) {
				return i
			}
		}
		return -1
	}
	upTo := func(i int) string { return strings.Join(pos[:i+1], " ") }
	what := ""
	switch n {
	case "aws":
		service, rest := firstPositional(args, awsWithValue)
		op, _ := firstPositional(rest, awsWithValue)
		if service == "s3" && ((op == "rm" && hasFlag(rest, "--recursive")) || (op == "rb" && hasFlag(rest, "--force"))) {
			what = "aws s3 " + op
		} else if awsDeleteOps[op] || (service == "iam" && strings.HasPrefix(op, "delete-")) {
			what = "aws " + service + " " + op
		}
	case "gcloud":
		if i := verbAt(5, "delete"); i > 0 && !gcloudLocalGroups[pos[0]] {
			what = "gcloud " + upTo(i)
		} else if atOr(pos, 0) == "storage" && atOr(pos, 1) == "rm" && hasFlag(args, "-r", "-R", "--recursive") {
			what = "gcloud storage rm --recursive"
		}
	case "gsutil":
		sub, rest := firstPositional(args, gsutilWithValue)
		p := parseArgs(rest)
		if sub == "rb" {
			what = "gsutil rb"
		} else if sub == "rm" && (p.shorts["r"] || p.shorts["R"] || p.longs["--recursive"]) {
			what = "gsutil rm -r"
		}
	case "az":
		if i := verbAt(4, "delete", "delete-batch", "purge"); i > 0 && !azLocalGroups[pos[0]] {
			what = "az " + upTo(i)
		}
	case "doctl":
		if i := verbAt(4, "delete", "rm"); i > 0 && pos[0] != "auth" {
			what = "doctl " + upTo(i)
		}
	case "fly", "flyctl":
		if i := verbAt(3, "destroy", "delete"); i >= 0 {
			what = n + " " + upTo(i)
		}
	case "heroku":
		if sub, _ := firstPositional(args, herokuWithValue); herokuDestroyRe.MatchString(sub) || sub == "pg:reset" {
			what = "heroku " + sub
		}
	case "vercel":
		sub, rest := firstPositional(args, vercelWithValue)
		if sub == "remove" || sub == "rm" {
			what = "vercel " + sub
		} else if sub == "project" || sub == "projects" {
			if verb, _ := firstPositional(rest, vercelWithValue); verb == "rm" || verb == "remove" {
				what = "vercel " + sub + " remove"
			}
		}
	case "netlify":
		if sub, _ := firstPositional(args, netlifyWithValue); sub == "sites:delete" {
			what = "netlify sites:delete"
		}
	}
	if what == "" {
		return nil
	}
	return &hit{"production.cloud_delete", "Deletes cloud resources (" + what + ")", []string{CatDestructive, CatProductionChange}}
}

var (
	systemctlWithValue = setOf("-H", "--host", "-M", "--machine")
	systemctlPower     = setOf("poweroff", "reboot", "halt", "kexec")
)

func checkShutdown(c *Cmd, _ *rulesCtx) *hit {
	n := c.name
	args := tail(c.argv)
	if len(args) == 1 && contains([]string{"--help", "-h", "--version"}, args[0]) {
		return nil
	}
	what := ""
	switch {
	case n == "shutdown" || n == "reboot" || n == "halt" || n == "poweroff":
		if !(n == "shutdown" && contains(args, "-c")) { // shutdown -c cancels a pending shutdown
			what = n
		}
	case (n == "init" || n == "telinit") && (atOr(args, 0) == "0" || atOr(args, 0) == "6"):
		what = n + " " + args[0]
	case n == "systemctl":
		if sub, _ := firstPositional(args, systemctlWithValue); systemctlPower[sub] {
			what = "systemctl " + sub
		}
	}
	if what == "" {
		return nil
	}
	return &hit{"destructive.shutdown", "Shuts down or reboots the machine (" + what + ")", destructive}
}

var (
	// metadataRe matches cloud instance metadata endpoints (AWS, Azure, GCP,
	// Alibaba, Oracle, ECS task credentials), also the decimal and hex forms
	// of 169.254.169.254. They hand out instance credentials to anything that asks.
	metadataRe    = jsReI(`(^|[^0-9a-z.-])(169\.254\.169\.254|169\.254\.170\.2|100\.100\.100\.200|192\.0\.0\.192|fd00:ec2::254|2852039166|0xa9fea9fe|metadata\.google\.internal|metadata\.goog)($|[^0-9a-z-])|:\/\/metadata($|[/:?#])`)
	metadataTools = func() map[string]bool {
		m := setOf("git")
		for k := range networkTools {
			m[k] = true
		}
		return m
	}()
	devNetRe    = jsRe(`\/dev\/(tcp|udp)\/`)
	metadataHit = hit{"exfiltration.cloud_metadata", "Contacts a cloud metadata endpoint, which hands out instance credentials", []string{CatSecretExfiltration, CatNetworkEgress}}
)

// checkMetadata: a network tool or interpreter names a metadata endpoint, or
// a /dev/tcp redirection opens one.
func checkMetadata(c *Cmd, _ *rulesCtx) *hit {
	if (metadataTools[c.name] || isInterpreter(c.name)) && anyOf(tail(c.argv), metadataRe.MatchString) {
		h := metadataHit
		return &h
	}
	dev := func(t string) bool { return devNetRe.MatchString(t) && metadataRe.MatchString(t) }
	if anyOf(redirectTargets(c.redirects), dev) || anyOf(c.argv, dev) {
		h := metadataHit
		return &h
	}
	return nil
}

func metadataHostIn(hosts []string) bool {
	return anyOf(hosts, metadataRe.MatchString)
}

var (
	dbClients       = setOf("psql", "mysql", "mariadb", "sqlite3", "sqlcmd", "mongo", "mongosh", "redis-cli", "clickhouse-client", "clickhouse", "cockroach", "cqlsh", "duckdb", "snowsql", "bq", "pgcli", "mycli", "litecli", "usql")
	destructiveSQL  = jsReI("\\b(drop\\s+(database|schema|table|keyspace|collection)|truncate\\s+(table\\s+)?[\\w.\"`]+|delete\\s+from\\s+[\\w.\"`]+\\s*(;|$)|flushall|flushdb|dropdatabase\\s*\\(|\\.drop\\s*\\(\\s*\\))")
	redisFlushRe    = jsReI(`^(flushall|flushdb)$`)
	dbTaskRe        = jsRe(`(^|\s|:)db:(drop|reset|purge)(\s|$)`)
	dbSchemaLoadRe  = jsRe(`\bdb:schema:load\b`)
	migrateResetRe  = jsRe(`\bmigrate\s+reset\b`)
	forceResetCmdRe = jsRe(`\b(db\s+push|migrate\s+\w+)\b.*--force-reset`)
	forceResetRe    = jsRe(`--force-reset`)
	djangoFlushRe   = jsRe(`\bmanage\.py\s+(flush|reset_db|sqlflush)\b`)
)

func checkDatabase(c *Cmd) *hit {
	n := c.name
	args := tail(c.argv)
	if n == "dropdb" || n == "dropuser" {
		return &hit{"destructive.db_drop", "Drops a database object (" + n + ")", destructive}
	}
	if dbClients[n] {
		texts := append([]string{}, args...)
		for _, r := range c.redirects {
			switch {
			case r.Body != nil:
				texts = append(texts, *r.Body)
			case r.Op == "<<<":
				texts = append(texts, r.Target.Text)
			default:
				texts = append(texts, "")
			}
		}
		// `echo 'DROP TABLE x' | psql`: the SQL comes from another stage of the pipeline.
		for _, other := range *c.pipeline {
			if other != c {
				texts = append(texts, tail(other.argv)...)
			}
		}
		if anyOf(texts, destructiveSQL.MatchString) {
			return &hit{"destructive.db_drop", "Drops, truncates or wipes database data", destructive}
		}
		if n == "redis-cli" && anyOf(args, redisFlushRe.MatchString) {
			return &hit{"destructive.db_drop", "Wipes a Redis database", destructive}
		}
	}
	joined := strings.Join(args, " ")
	if dbTaskRe.MatchString(" "+joined) || dbSchemaLoadRe.MatchString(joined) {
		return &hit{"destructive.db_drop", "Drops or resets a database through a framework task", destructive}
	}
	if migrateResetRe.MatchString(joined) || forceResetCmdRe.MatchString(joined) || forceResetRe.MatchString(joined) {
		return &hit{"destructive.db_drop", "Resets a database and its data", destructive}
	}
	if pythonRe.MatchString(n) && djangoFlushRe.MatchString(joined) {
		return &hit{"destructive.db_drop", "Flushes a Django database", destructive}
	}
	return nil
}

var readOnlyNames = setOf(
	"ls", "ll", "la", "cat", "bat", "head", "tail", "wc", "grep", "egrep", "fgrep", "rg", "ag", "ack", "tree", "pwd", "echo",
	"printf", "which", "whereis", "type", "file", "stat", "du", "df", "date", "whoami", "id", "uname", "hostname", "basename",
	"dirname", "realpath", "readlink", "sort", "uniq", "cut", "tr", "diff", "cmp", "comm", "jq", "nl", "xxd", "hexdump", "od",
	"strings", "md5sum", "sha1sum", "sha256sum", "sha512sum", "shasum", "md5", "cksum", "column", "seq", "sleep", "cd",
	"pushd", "popd", "dirs", "exa", "eza", "lsof", "ps", "pgrep", "uptime", "free", "nproc", "arch", "sw_vers", "locale",
	"test", "[", "[[", "true", "false", ":", "less", "more", "fold", "paste", "rev", "tac", "expand", "unexpand", "fmt",
	"numfmt", "join", "look", "units", "cal", "groups", "tty", "getconf", "vm_stat", "sysctl", "lscpu", "lsblk", "printenv",
)

var (
	deployTools = setOf("vercel", "netlify", "flyctl", "fly", "heroku", "kamal", "cap", "capistrano", "eb", "serverless", "sls", "firebase", "amplify", "wrangler", "railway", "render", "dokku", "ansible-playbook")
	deployVerb  = jsReI(`(deploy|release|rollout|promote|migrat|apply|upgrade|publish|ship)`)
)

func checkDeploy(c *Cmd, _ *rulesCtx) *hit {
	if readOnlyNames[c.name] || c.name == "git" || c.name == "" {
		return nil
	}
	tokens := append(append([]string{}, c.argv...), c.assigns...)
	verb := deployTools[c.name] || anyOf(tokens, func(t string) bool { return deployVerb.MatchString(t) && t != "--release" })
	if !verb {
		return nil
	}
	for _, t := range tokens {
		if prodRe.MatchString(t) {
			return &hit{"production.deploy", "Looks like a production deploy or migration (" + t + ")", []string{CatProductionChange}}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------------------------------
// Script-level checks
// ---------------------------------------------------------------------------------------------------

func checkPipeToShell(cmds []*Cmd) *hit {
	for _, c := range cmds {
		if !isInterpreter(c.name) || c.stage == 0 {
			continue
		}
		stages := *c.pipeline
		before := stages[:min(c.stage, len(stages))]
		fetched := false
		for _, b := range before {
			if fetchers[b.name] {
				fetched = true
			}
		}
		if fetched && readsProgramFromStdin(c) {
			return &hit{"rce.pipe_to_shell", "Pipes a download straight into " + c.name, []string{CatRemoteCodeExecution}}
		}
	}
	return nil
}

func checkSubstitutionExec(cmds []*Cmd) *hit {
	for _, c := range cmds {
		execs := isInterpreter(c.name) || c.name == "source" || c.name == "." || c.name == "eval"
		if !execs {
			continue
		}
		var scripts []*Script
		for _, x := range c.words {
			scripts = append(scripts, x.Subs...)
			scripts = append(scripts, x.ProcSubs...)
		}
		if len(scripts) > 0 && containsFetcher(scripts, c.cwd) {
			return &hit{"rce.substitution", "Runs downloaded code through " + c.name, []string{CatRemoteCodeExecution}}
		}
	}
	return nil
}

var (
	httpURLRe   = jsRe(`^https?:\/\/`)
	urlSuffixRe = jsRe(`[?#].*$`)
)

func checkDownloadThenRun(cmds []*Cmd) *hit {
	downloaded := map[string]bool{}
	for _, c := range cmds {
		if c.name == "curl" || c.name == "wget" {
			args := tail(c.argv)
			for i, a := range args {
				if (c.name == "curl" && (a == "-o" || a == "--output")) || (c.name == "wget" && (a == "-O" || a == "--output-document")) {
					if next := atOr(args, i+1); next != "" && next != "-" {
						downloaded[base(next)] = true
					}
				} else if strings.HasPrefix(a, "--output=") || strings.HasPrefix(a, "--output-document=") {
					downloaded[base(a[strings.Index(a, "=")+1:])] = true
				}
			}
			url := ""
			for _, a := range args {
				if httpURLRe.MatchString(a) {
					url = a
					break
				}
			}
			named := false
			if c.name == "wget" {
				named = !hasFlag(args, "-O", "--output-document", "-q-")
			} else {
				named = hasFlag(args, "-O", "--remote-name")
			}
			if url != "" && named {
				if b := base(urlSuffixRe.ReplaceAllString(url, "")); b != "" {
					downloaded[b] = true
				}
			}
			for _, r := range redirectWrites(c) {
				downloaded[base(r.Target.Text)] = true
			}
			continue
		}
		if len(downloaded) == 0 {
			continue
		}
		runsFile := func(f string, ok bool) bool { return ok && downloaded[base(f)] }
		if a0 := atOr(c.argv, 0); a0 != "" && strings.Contains(a0, "/") && runsFile(a0, true) {
			return &hit{"rce.download_run", "Runs the downloaded file " + a0, []string{CatRemoteCodeExecution}}
		}
		if isInterpreter(c.name) || c.name == "source" || c.name == "." {
			script, ok := "", false
			for _, a := range tail(c.argv) {
				if !strings.HasPrefix(a, "-") {
					script, ok = a, true
					break
				}
			}
			if runsFile(script, ok) {
				return &hit{"rce.download_run", "Runs the downloaded file " + script, []string{CatRemoteCodeExecution}}
			}
		}
	}
	return nil
}

var (
	keychainReadRe = jsRe(`^(dump-keychain|find-generic-password|find-internet-password|export|find-certificate)$`)
	identityFileRe = jsReI(`^IdentityFile=`)
)

func isSecretSource(c *Cmd) bool {
	n := c.name
	if (n == "env" || n == "printenv" || n == "set") && len(c.argv) <= 1 {
		return true
	}
	if n == "export" && (len(c.argv) == 1 || atOr(c.argv, 1) == "-p") {
		return true
	}
	if n == "security" && keychainReadRe.MatchString(atOr(c.argv, 1)) {
		return true
	}
	if n == "gh" && atOr(c.argv, 1) == "auth" && atOr(c.argv, 2) == "token" {
		return true
	}
	if n == "gcloud" && contains(c.argv, "print-access-token") {
		return true
	}
	if n == "aws" && atOr(c.argv, 1) == "configure" && contains([]string{"get", "export-credentials"}, atOr(c.argv, 2)) {
		return true
	}
	if n == "ssh-add" || n == "ssh-keygen" || n == "ssh-copy-id" {
		return false
	}
	all := tail(c.argv)
	sshLike := n == "ssh" || n == "scp" || n == "sftp"
	var args []string
	for i, a := range all {
		prev, _ := at(all, i-1)
		if sshLike && (prev == "-i" || identityFileRe.MatchString(a)) {
			continue
		}
		args = append(args, a)
	}
	if anyOf(args, mentionsSecret) {
		return true
	}
	for _, r := range c.redirects {
		if (r.Op == "<" || r.Op == "<<<") && mentionsSecret(r.Target.Text) {
			return true
		}
	}
	return false
}

func isNetworkCmd(c *Cmd) bool {
	if networkTools[c.name] {
		return true
	}
	for _, r := range c.redirects {
		if devTCPRedirectRe.MatchString(r.Target.Text) {
			return true
		}
	}
	if anyOf(c.argv, devTCPArgRe.MatchString) {
		return true
	}
	if (pythonRe.MatchString(c.name) || c.name == "node" || c.name == "ruby" || c.name == "perl") && anyOf(c.argv, scriptNetworkUseRe.MatchString) {
		return true
	}
	return false
}

func checkExfiltration(cmds []*Cmd, facts RulesFacts) *hit {
	var source, sink *Cmd
	for _, c := range cmds {
		if isSecretSource(c) {
			source = c
			break
		}
	}
	if source == nil {
		return nil
	}
	for _, c := range cmds {
		if isNetworkCmd(c) {
			sink = c
			break
		}
	}
	if sink != nil || len(facts.NetworkHosts) > 0 {
		what := source.name
		if what == "" {
			what = "secret file"
		}
		via := ""
		if sink != nil {
			via = " (" + sink.name + ")"
		}
		return &hit{"exfiltration.secrets", "Reads credentials (" + what + ") and contacts the network" + via + " in the same command", []string{CatSecretExfiltration}}
	}
	return nil
}

// ---------------------------------------------------------------------------------------------------
// Fast-allow
// ---------------------------------------------------------------------------------------------------

var dangerousEnv = jsRe(`^(LD_[A-Z_]+|DYLD_[A-Z_]+|PATH|BASH_ENV|ENV|PROMPT_COMMAND|GIT_SSH_COMMAND|GIT_SSH|GIT_EXTERNAL_DIFF|GIT_PAGER|PAGER|EDITOR|VISUAL|GIT_EDITOR|NODE_OPTIONS|PYTHONPATH|PYTHONSTARTUP|PERL5OPT|PERL5LIB|RUBYOPT|GIT_CONFIG[A-Z_]*|GIT_DIR|GIT_WORK_TREE|SHELLOPTS|IFS|GIT_ASKPASS|SSH_ASKPASS|LESSOPEN|MANPAGER)\+?=`)

var (
	safeScript   = jsReI(`^(test|tests|lint|build|typecheck|type-check|types|check|tsc|format:check|fmt:check|prettier:check|vet|compile|coverage|spec|e2e|unit)([:._-][\w:.-]*)?$`)
	unsafeScript = jsReI(`(deploy|publish|release|prod|push|upload|install|clean|reset|drop|migrat|seed|start|serve|dev|watch)`)
)

func safeScriptName(s string) bool {
	return s != "" && safeScript.MatchString(s) && !unsafeScript.MatchString(s)
}

func first(a []string) string { return atOr(a, 0) }

// tail is argv.slice(1).
func tail(a []string) []string {
	if len(a) == 0 {
		return nil
	}
	return a[1:]
}

// testTools are test runners, linters and type checkers with their safe-arguments predicate (same order as TEST_TOOLS).
var testTools = map[string]func(a []string) bool{
	"tsc":      func(a []string) bool { return !hasFlag(a, "--watch", "-w") },
	"vitest":   func(a []string) bool { return !contains([]string{"dev", "watch", "init"}, first(a)) },
	"jest":     func([]string) bool { return true },
	"mocha":    func([]string) bool { return true },
	"ava":      func([]string) bool { return true },
	"eslint":   func([]string) bool { return true },
	"prettier": func(a []string) bool { return hasFlag(a, "--check", "-c", "--list-different", "-l") },
	"biome": func(a []string) bool {
		return contains([]string{"check", "lint", "ci", "format"}, first(a)) && !hasFlag(a, "--write", "--apply", "--fix", "--unsafe")
	},
	"stylelint": func([]string) bool { return true },
	"pytest":    func([]string) bool { return true },
	"py.test":   func([]string) bool { return true },
	"mypy":      func([]string) bool { return true },
	"pyright":   func([]string) bool { return true },
	"ruff": func(a []string) bool {
		return first(a) == "check" || (first(a) == "format" && hasFlag(a, "--check", "--diff"))
	},
	"black":    func(a []string) bool { return hasFlag(a, "--check", "--diff") },
	"isort":    func(a []string) bool { return hasFlag(a, "--check", "--check-only", "-c", "--diff") },
	"flake8":   func([]string) bool { return true },
	"pylint":   func([]string) bool { return true },
	"unittest": func([]string) bool { return true },
	"rspec":    func([]string) bool { return true },
	"rubocop": func(a []string) bool {
		return !hasFlag(a, "-a", "-A", "--autocorrect", "--auto-correct", "--autocorrect-all")
	},
	"shellcheck":    func([]string) bool { return true },
	"staticcheck":   func([]string) bool { return true },
	"golangci-lint": func(a []string) bool { return first(a) == "run" },
	"gofmt":         func(a []string) bool { return !hasFlag(a, "-w") },
	"playwright":    func(a []string) bool { return first(a) == "test" },
	"cypress":       func(a []string) bool { return first(a) == "run" },
	"vue-tsc":       func(a []string) bool { return !hasFlag(a, "--watch", "-w") },
	"svelte-check":  func([]string) bool { return true },
	"ctest":         func([]string) bool { return true },
}

func outputInside(c *Cmd, ctx *rulesCtx, flags []string) bool {
	args := tail(c.argv)
	for i, a := range args {
		for _, f := range flags {
			v, ok := "", false
			if a == f {
				v, ok = at(args, i+1)
			} else if strings.HasPrefix(a, f+"=") {
				v, ok = a[len(f)+1:], true
			}
			if !ok {
				continue
			}
			p := resolvePath(v, c.cwd)
			if p == "" || (!isInside(p, ctx.roots) && p != "/dev/null") {
				return false
			}
		}
	}
	return true
}

// withArgv is `{ ...c, argv }`.
func withArgv(c *Cmd, argv []string) *Cmd {
	cp := *c
	cp.argv = argv
	return &cp
}

func cwdInside(c *Cmd, ctx *rulesCtx) bool {
	return c.cwd != "" && isInside(c.cwd, ctx.roots)
}

var (
	pmOpts       = setOf("--filter", "-F", "-C", "--dir", "--prefix", "--workspace", "-w")
	pyRunnerOpts = setOf("--directory", "-p", "--python", "--with", "--project")
	pipReadSubs  = []string{"list", "show", "freeze", "check"}
)

func isSafeRunner(c *Cmd, ctx *rulesCtx) bool {
	n := c.name
	args := tail(c.argv)
	if n == "npm" || n == "pnpm" || n == "yarn" || n == "bun" {
		if !outputInside(c, ctx, []string{"-C", "--dir", "--prefix"}) {
			return false
		}
		i := 0
		for i < len(args) && strings.HasPrefix(args[i], "-") {
			if pmOpts[args[i]] {
				i += 2
			} else {
				i++
			}
		}
		sub, ok := at(args, i)
		var rest []string
		if i+1 <= len(args) {
			rest = args[i+1:]
		}
		if !ok {
			return false
		}
		if contains([]string{"test", "t", "tst", "lint", "typecheck", "build", "check"}, sub) {
			return cwdInside(c, ctx)
		}
		if contains([]string{"ls", "list", "ll", "la", "outdated", "why", "explain", "audit", "view", "info", "config"}, sub) {
			return sub != "config" || first(rest) == "get" || first(rest) == "list"
		}
		if sub == "run" || sub == "run-script" {
			name := ""
			for _, x := range rest {
				if !strings.HasPrefix(x, "-") {
					name = x
					break
				}
			}
			return safeScriptName(name) && cwdInside(c, ctx)
		}
		if sub == "exec" || sub == "x" {
			return isSafeTool(rest, c, ctx)
		}
		if n == "yarn" || n == "bun" || n == "pnpm" {
			return safeScriptName(sub) && cwdInside(c, ctx)
		}
		return false
	}
	if n == "npx" || n == "pnpx" || n == "bunx" {
		var rest []string
		for i, a := range args {
			if strings.HasPrefix(a, "-") && i == 0 && a != "--" {
				continue
			}
			rest = append(rest, a)
		}
		if first(rest) == "--" {
			rest = rest[1:]
		}
		return isSafeTool(rest, c, ctx)
	}
	if n == "uv" || n == "poetry" || n == "pipenv" || n == "pdm" || n == "hatch" {
		sub, rest := firstPositional(args, pyRunnerOpts)
		if sub == "run" {
			return isSafeTool(filter(rest, func(a string) bool { return !strings.HasPrefix(a, "--") || a == "--" }), c, ctx)
		}
		if sub == "pip" {
			return contains(pipReadSubs, first(rest))
		}
		return false
	}
	if n == "bundle" && first(args) == "exec" {
		return isSafeTool(args[1:], c, ctx)
	}
	return isSafeTool(append([]string{n}, args...), c, ctx)
}

var (
	makeValueOpts = []string{"-j", "-l", "-f", "-C", "--directory", "--file", "-o", "-W"}
	jvmTaskRe     = jsRe(`^([\w-]*:)*(test|verify|compile|build|check|lint|assemble|package)$`)
)

// isSafeTool: test runners, linters and build tools (argv[0] is the tool).
func isSafeTool(argv []string, c *Cmd, ctx *rulesCtx) bool {
	tool := base(first(argv))
	args := tail(argv)
	if tool == "" {
		return false
	}
	if pythonRe.MatchString(tool) {
		if mi := indexOf(args, "-m"); mi >= 0 {
			if mod := atOr(args, mi+1); mod != "" {
				return isSafeTool(append([]string{mod}, args[mi+2:]...), c, ctx)
			}
		}
		if len(args) == 1 && (args[0] == "--version" || args[0] == "-V") {
			return true
		}
		return false
	}
	if pred, ok := testTools[tool]; ok {
		if tool == "tsc" || tool == "vue-tsc" {
			return pred(args) && outputInside(c, ctx, []string{"--outDir", "--outFile", "--declarationDir"})
		}
		return pred(args)
	}
	if tool == "go" {
		sub := first(args)
		if hasFlag(args, "-exec", "-toolexec", "--exec") {
			return false
		}
		if contains([]string{"test", "vet", "build", "list", "version", "doc"}, sub) {
			return outputInside(withArgv(c, argv), ctx, []string{"-o"}) && (sub != "build" && sub != "test" || cwdInside(c, ctx))
		}
		if sub == "env" {
			return !hasFlag(args, "-w", "-u")
		}
		if sub == "mod" {
			return contains([]string{"graph", "why", "verify"}, atOr(args, 1))
		}
		return false
	}
	if tool == "cargo" {
		sub := ""
		for _, a := range args {
			if !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+") {
				sub = a
				break
			}
		}
		if contains([]string{"test", "build", "check", "doc", "tree", "metadata", "bench", "nextest"}, sub) {
			return cwdInside(c, ctx) && outputInside(withArgv(c, argv), ctx, []string{"--target-dir", "--out-dir", "--artifact-dir"})
		}
		if sub == "clippy" {
			return !hasFlag(args, "--fix")
		}
		if sub == "fmt" {
			return hasFlag(args, "--check")
		}
		return false
	}
	if tool == "make" || tool == "gmake" {
		if !outputInside(withArgv(c, argv), ctx, []string{"-C", "--directory"}) {
			return false
		}
		var targets []string
		for i := 0; i < len(args); i++ {
			a := args[i]
			if contains(makeValueOpts, a) {
				if a == "-f" || a == "--file" || a == "-o" || a == "-W" {
					return false
				}
				i++
				continue
			}
			if strings.HasPrefix(a, "-") || assignRe.MatchString(a) {
				continue
			}
			targets = append(targets, a)
		}
		if !cwdInside(c, ctx) || len(targets) == 0 {
			return false
		}
		return !anyOf(targets, func(t string) bool { return !(safeScriptName(t) || t == "all") })
	}
	if contains([]string{"mvn", "mvnw", "gradle", "gradlew"}, tool) {
		tasks := filter(args, func(a string) bool { return !strings.HasPrefix(a, "-") })
		return cwdInside(c, ctx) && len(tasks) > 0 && !anyOf(tasks, func(t string) bool { return !jvmTaskRe.MatchString(t) })
	}
	if tool == "dotnet" || tool == "swift" {
		return contains([]string{"test", "build"}, first(args)) && cwdInside(c, ctx)
	}
	if tool == "bazel" || tool == "bazelisk" {
		return contains([]string{"test", "build", "query", "info"}, first(args)) && cwdInside(c, ctx)
	}
	if tool == "turbo" || tool == "nx" {
		t := args
		if first(args) == "run" {
			t = args[1:]
		}
		names := filter(t, func(a string) bool { return !strings.HasPrefix(a, "-") })
		return len(names) > 0 && !anyOf(names, func(x string) bool {
			parts := strings.Split(x, ":")
			return !safeScriptName(parts[len(parts)-1])
		})
	}
	if tool == "rake" {
		return len(args) > 0 && !anyOf(args, func(a string) bool {
			return !(strings.HasPrefix(a, "-") || contains([]string{"test", "spec", "lint"}, a))
		})
	}
	if tool == "pip" || tool == "pip3" {
		return contains(pipReadSubs, first(args))
	}
	return false
}

var gitRead = setOf(
	"status", "diff", "log", "show", "blame", "rev-parse", "describe", "ls-files", "ls-tree", "grep", "shortlog", "cat-file",
	"merge-base", "name-rev", "rev-list", "for-each-ref", "whatchanged", "count-objects", "check-ignore", "show-ref", "help", "version",
	"var", "range-diff", "annotate", "cherry", "verify-commit", "verify-tag", "difftool",
)

var (
	gitBranchShorts = []string{"a", "r", "v", "l", "vv"}
	gitBranchLongs  = []string{"--list", "--all", "--remotes", "--show-current", "--contains", "--merged", "--no-merged", "--sort", "--format", "--verbose", "--color", "--no-color"}
)

func isSafeGit(c *Cmd) bool {
	sub, args, globals := gitSub(c.argv)
	if anyOf(globals, func(g string) bool {
		return g == "-c" || g == "--config-env" || strings.HasPrefix(g, "--exec-path") || g == "--exec-path"
	}) {
		return false
	}
	if hasFlag(args, "--output", "--ext-diff", "--exec", "--upload-pack") {
		return false
	}
	if sub == "difftool" {
		return false
	}
	if gitRead[sub] {
		return true
	}
	p := parseArgs(args)
	switch sub {
	case "branch":
		for s := range p.shorts {
			if !contains(gitBranchShorts, s) {
				return false
			}
		}
		for k := range p.longs {
			if !contains(gitBranchLongs, k) {
				return false
			}
		}
		return len(p.positionals) == 0 || p.longs["--list"] || p.shorts["l"] || p.longs["--contains"] || p.longs["--merged"] || p.longs["--no-merged"]
	case "tag":
		return (len(args) == 0 || p.shorts["l"] || p.longs["--list"]) && !p.shorts["d"] && !p.shorts["a"] && !p.shorts["s"] && !p.shorts["f"]
	case "remote":
		return len(args) == 0 || contains([]string{"-v", "--verbose", "show", "get-url"}, args[0])
	case "stash":
		return contains([]string{"list", "show"}, first(args))
	case "config":
		return hasFlag(args, "--get", "--get-all", "--list", "-l", "--get-regexp") || contains([]string{"get", "list"}, first(args))
	case "reflog":
		return len(args) == 0 || args[0] == "show" || strings.HasPrefix(args[0], "-")
	case "worktree":
		return first(args) == "list"
	case "submodule":
		return len(args) == 0 || args[0] == "status"
	default:
		return false
	}
}

var secretReaders = []string{"cat", "bat", "head", "tail", "less", "more", "grep", "rg", "egrep", "fgrep", "ag", "xxd", "strings", "nl", "od", "hexdump"}

func isSafeReadOnly(c *Cmd) bool {
	n := c.name
	args := tail(c.argv)
	if n == "printenv" {
		return len(args) > 0 // a single variable; a full dump may hold secrets
	}
	if !readOnlyNames[n] {
		return false
	}
	if contains(secretReaders, n) && anyOf(args, mentionsSecret) {
		return false
	}
	for _, r := range c.redirects {
		if (r.Op == "<" || r.Op == "<<<") && mentionsSecret(r.Target.Text) {
			return false
		}
	}
	if n == "sort" && hasFlag(args, "-o", "--output") {
		return false
	}
	if (n == "uniq" || n == "xxd") && len(parseArgs(args).positionals) > 1 {
		return false
	}
	if n == "sysctl" && anyOf(args, func(a string) bool { return strings.Contains(a, "=") || a == "-w" }) {
		return false
	}
	if n == "rg" && hasFlag(args, "--pre") {
		return false
	}
	if (n == "less" || n == "more") && c.stage == 0 && len(args) == 0 {
		return false
	}
	if n == "date" && hasFlag(args, "-s", "--set") {
		return false
	}
	if n == "hostname" && len(parseArgs(args).positionals) > 0 {
		return false
	}
	return true
}

var (
	unknownArgsSafe = []string{"echo", "grep", "rg", "wc", "cat", "head", "ls"}
	gitDirRe        = jsRe(`(^|\/)\.git(\/|$)`)
	sedWriteRe      = jsRe(`(^|[;{}\n]\s*)[0-9,$/!]*\s*[wWe](\s|$)|\/[gIipm0-9]*[wWe](\s|$)`)
	awkUnsafeRe     = jsRe(`system\s*\(|\||>|getline|close\s*\(|fflush|inplace`)
	kubeSecretRe    = jsRe(`^secrets?$|^secrets?\/`)
	versionArgs     = []string{"--version", "-V", "--help", "version"}
	findWriteFlags  = []string{"-delete", "-fprint", "-fprint0", "-fprintf", "-fls"}
)

func isSafeCmd(c *Cmd, ctx *rulesCtx, extraAllow []*regexp.Regexp) bool {
	if c.escalated != "" {
		return false
	}
	if anyOf(c.assigns, dangerousEnv.MatchString) {
		return false
	}
	for _, re := range extraAllow {
		if re.MatchString(c.text) {
			return true
		}
	}
	if c.unknownArgs && !contains(unknownArgsSafe, c.name) {
		return false
	}
	for _, r := range redirectWrites(c) {
		t := resolvePath(r.Target.Text, c.cwd)
		if t == "/dev/null" || t == "/dev/stdout" || t == "/dev/stderr" {
			continue
		}
		if t == "" || !isInside(t, ctx.roots) || gitDirRe.MatchString(t) || isSecretPath(t) {
			return false
		}
	}
	n := c.name
	args := tail(c.argv)
	if neutral[n] {
		return true
	}
	if n == "command" && (first(args) == "-v" || first(args) == "-V") {
		return true
	}
	if len(args) == 1 && contains(versionArgs, args[0]) && !strings.Contains(n, "/") {
		return true
	}
	if isSafeReadOnly(c) {
		return true
	}
	// find -exec / fd -x: the executed command is checked as its own simple command.
	if n == "find" {
		return !anyOf(args, func(a string) bool { return contains(findWriteFlags, a) })
	}
	if n == "fd" || n == "fdfind" {
		return true
	}
	if shellNames[n] && anyOf(args, shellCFlagRe.MatchString) {
		return true // body checked separately
	}
	if n == "sed" || n == "gsed" {
		if anyOf(args, func(a string) bool {
			return inPlaceRe.MatchString(a) || strings.HasPrefix(a, "--in-place") || a == "-I"
		}) {
			return false
		}
		return !anyOf(args, sedWriteRe.MatchString)
	}
	if n == "awk" || n == "gawk" || n == "mawk" {
		if hasFlag(args, "-f", "-i", "--include") {
			return false
		}
		return !anyOf(args, awkUnsafeRe.MatchString)
	}
	if n == "yq" {
		return !hasFlag(args, "-i", "--inplace")
	}
	if n == "tee" {
		return !anyOf(parseArgs(args).positionals, func(t string) bool {
			p := resolvePath(t, c.cwd)
			return !(p != "" && (p == "/dev/null" || (isInside(p, ctx.roots) && !gitDirRe.MatchString(p))))
		})
	}
	if n == "crontab" {
		return len(args) == 1 && args[0] == "-l"
	}
	if n == "git" {
		return isSafeGit(c)
	}
	if n == "kubectl" || n == "oc" {
		sub, rest := firstPositional(args, kubectlWithValue)
		if sub == "get" || sub == "describe" {
			kind := ""
			for _, x := range rest {
				if !strings.HasPrefix(x, "-") {
					kind = x
					break
				}
			}
			return !kubeSecretRe.MatchString(kind)
		}
		return contains([]string{"logs", "top", "explain", "version", "api-resources", "api-versions", "cluster-info"}, sub) ||
			(sub == "config" && contains([]string{"get-contexts", "current-context", "view"}, first(rest)) && !hasFlag(rest, "--raw"))
	}
	if n == "docker" || n == "podman" {
		if contains([]string{"ps", "images", "logs", "inspect", "version", "info", "stats"}, first(args)) {
			return first(args) != "stats" || hasFlag(args, "--no-stream")
		}
		return first(args) == "compose" && contains([]string{"ps", "logs", "config", "ls", "images"}, atOr(args, 1))
	}
	if n == "terraform" || n == "tofu" {
		return contains([]string{"validate", "version", "providers"}, first(args)) || (first(args) == "fmt" && hasFlag(args, "-check"))
	}
	if n == "gh" {
		g, s := atOr(args, 0), atOr(args, 1)
		if g == "api" {
			return !hasFlag(args, "-X", "--method", "-f", "-F", "--field", "--raw-field", "--input")
		}
		return contains([]string{"pr", "issue", "run", "repo", "release", "workflow"}, g) && contains([]string{"view", "list", "diff", "checks", "status"}, s)
	}
	if n == "brew" {
		return contains([]string{"list", "info", "search", "outdated", "config", "doctor", "deps", "leaves"}, first(args))
	}
	return isSafeRunner(c, ctx)
}

// ---------------------------------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------------------------------

var (
	readTools  = setOf("Read", "Glob", "Grep", "LS", "NotebookRead", "TodoWrite", "WrapUp", "PushNotification", "ReadNotifications", "SendUserFile", "ReportFindings", "TaskStop", "TaskOutput", "BashOutput", "KillShell", "AskUserQuestion", "EnterPlanMode", "ExitPlanMode", "WebSearch", "Task", "Agent")
	writeTools = setOf("Write", "Edit", "MultiEdit", "NotebookEdit")
)

func ask(reason, rule string) RulesResult {
	return RulesResult{Decision: RuleAsk, Reason: reason, Categories: []string{}, Rule: rule}
}

func sortCategories(cats []string) []string {
	set := setOf(cats...)
	out := []string{}
	for _, c := range RuleCategories {
		if set[c] {
			out = append(out, c)
		}
	}
	return out
}

func denyFrom(hits []hit) RulesResult {
	var cats []string
	for _, h := range hits {
		cats = append(cats, h.categories...)
	}
	return RulesResult{Decision: RuleDeny, Reason: hits[0].reason, Categories: sortCategories(cats), Rule: hits[0].rule}
}

// compile drops invalid administrator regexes (the admin API validates on save).
func compile(patterns []string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, p := range patterns {
		if re, err := regexp.Compile(p); err == nil {
			out = append(out, re)
		}
	}
	return out
}

// parseInput decodes the tool input (an object as JSON text); ok=false when it is not an object.
func parseInput(input json.RawMessage) (map[string]any, bool) {
	if strings.TrimSpace(string(input)) == "" {
		return map[string]any{}, true
	}
	var v any
	if err := json.Unmarshal(input, &v); err != nil {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok && m != nil
}

func normRoot(p string) string {
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "/") || p == "~" || strings.HasPrefix(p, "~/") {
		return normalizePath(p)
	}
	return ""
}

type extraDeny struct {
	re *regexp.Regexp
	r  ExtraDenyRule
}

func adminReason(r ExtraDenyRule) string {
	if r.Reason != "" {
		return r.Reason
	}
	return "Blocked by an administrator rule"
}

// EvaluateRules is the server's evaluateRules: the verdict of the built-in rules for one tool call.
func EvaluateRules(req RulesInput, opts RulesOptions) RulesResult {
	input, ok := parseInput(req.Input)
	if !ok {
		return ask("Tool input is not a JSON object", "ask.bad_input")
	}
	facts := req.Facts
	cwd := normRoot(req.Cwd)
	var roots []string
	for _, r := range append([]string{cwd}, req.AdditionalDirectories...) {
		if n := normRoot(r); n != "" {
			roots = append(roots, n)
		}
	}
	branches := opts.ProtectedBranches
	if len(branches) == 0 {
		branches = DefaultProtectedBranches
	}
	ctx := &rulesCtx{roots: roots, cwd: cwd, facts: facts, denyAllForcePush: opts.DenyAllForcePush}
	for _, b := range branches {
		ctx.protectedBranches = append(ctx.protectedBranches, globToRegExp(b))
	}
	extraAllow := compile(opts.ExtraAllow)
	var extraDenies []extraDeny
	for _, r := range opts.ExtraDeny {
		for _, re := range compile([]string{r.Pattern}) {
			extraDenies = append(extraDenies, extraDeny{re, r})
		}
	}
	tool := req.ToolName

	if tool != "Bash" {
		subject := tool + " " + compactJSON(req.Input)
		for _, x := range extraDenies {
			if x.re.MatchString(subject) {
				return RulesResult{Decision: RuleDeny, Reason: adminReason(x.r), Categories: sortCategories(x.r.Categories), Rule: "admin.deny"}
			}
		}
		if url, ok := input["url"].(string); (tool == "WebFetch" && ok && metadataRe.MatchString(url)) || metadataHostIn(facts.NetworkHosts) {
			return denyFrom([]hit{metadataHit})
		}
		adminAllowed := false
		for _, re := range extraAllow {
			if re.MatchString(subject) {
				adminAllowed = true
			}
		}
		return evaluateTool(tool, input, ctx, facts, adminAllowed)
	}

	command, ok := input["command"].(string)
	if !ok {
		return ask("Bash call without a command", "ask.bad_input")
	}
	if jsLength(command) > maxCommandLength {
		return ask("Command is too long to analyze", "ask.too_long")
	}

	var hits []hit
	for _, x := range extraDenies {
		if x.re.MatchString(command) {
			hits = append(hits, hit{"admin.deny", adminReason(x.r), x.r.Categories})
		}
	}

	sources := []string{command}
	for _, s := range facts.Subcommands {
		if s != "" && s != command {
			sources = append(sources, s)
		}
	}
	var allCmds []*Cmd
	parsedMain := true
	checks := []func(*Cmd, *rulesCtx) *hit{checkEscalation, checkRm, checkFindDelete, checkDevices, checkPrivilegedWrites, checkChmod, checkGit, checkInfra, checkDeploy, checkShutdown, checkCloudDelete, checkMetadata}
	for idx, src := range sources {
		cmds, err := flattenCommand(src, cwd)
		if err != nil {
			if idx == 0 {
				parsedMain = false
				hits = append(hits, rawFallback(src)...)
			}
			continue
		}
		for _, c := range cmds {
			for _, check := range checks {
				if h := check(c, ctx); h != nil {
					hits = append(hits, *h)
				}
			}
			if db := checkDatabase(c); db != nil {
				hits = append(hits, *db)
			}
			for _, x := range extraDenies {
				if x.re.MatchString(c.text) {
					hits = append(hits, hit{"admin.deny", adminReason(x.r), x.r.Categories})
				}
			}
		}
		for _, h := range []*hit{checkPipeToShell(cmds), checkSubstitutionExec(cmds), checkDownloadThenRun(cmds)} {
			if h != nil {
				hits = append(hits, *h)
			}
		}
		allCmds = append(allCmds, cmds...)
	}
	if exfil := checkExfiltration(allCmds, facts); exfil != nil {
		hits = append(hits, *exfil)
	}
	if metadataHostIn(facts.NetworkHosts) {
		hits = append(hits, metadataHit)
	}
	// Paths the client resolved for the whole call.
	for _, p := range facts.WritePaths {
		r := resolvePath(p, "")
		if r == "" {
			continue
		}
		if isBlockDevice(r) {
			hits = append(hits, hit{"destructive.device_write", "Writes directly to the block device " + r, destructive})
		} else if isPrivilegedWritePath(r) {
			hits = append(hits, hit{"privilege.protected_write", "Modifies " + r + " (shell startup, ssh, scheduler or system file)", []string{CatPrivilegeEscalation}})
		}
	}
	if len(hits) > 0 {
		return denyFrom(dedupe(hits))
	}
	if !parsedMain {
		return ask("Could not parse the command", "ask.unparsed")
	}
	if len(facts.NetworkHosts) > 0 {
		return ask("Contacts the network", "ask.network")
	}
	if len(allCmds) > 0 {
		safe := true
		for _, c := range allCmds {
			if !isSafeCmd(c, ctx, extraAllow) {
				safe = false
				break
			}
		}
		if safe {
			return RulesResult{Decision: RuleAllow, Reason: "Read-only command or a test, lint or build run inside the workspace", Categories: []string{}, Rule: "allow.safe_command"}
		}
	}
	return ask("Not covered by the built-in rules", "ask.default")
}

// compactJSON is JSON.stringify of the parsed input (key order kept, whitespace dropped).
func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

func dedupe(hits []hit) []hit {
	seen := map[string]bool{}
	var out []hit
	for _, h := range hits {
		k := h.rule + "\x00" + h.reason
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, h)
	}
	return out
}

var (
	rawPipeToShellRe = jsRe(`\b(curl|wget)\b[^|]*\|\s*(sudo\s+)?(ba|z|da|k)?sh\b`)
	rawRmRootRe      = jsRe(`\brm\s+(-[a-zA-Z]*[rR][a-zA-Z]*\s+)+(--\s+)?["']?(\/|~|\$HOME|\$\{HOME\})["']?(\s|$|\*)`)
	rawSudoRe        = jsRe(`(^|[;&|\s])sudo\s`)
)

// rawFallback: last-resort patterns for commands the parser cannot read.
func rawFallback(src string) []hit {
	var out []hit
	if rawPipeToShellRe.MatchString(src) {
		out = append(out, hit{"rce.pipe_to_shell", "Pipes a download straight into a shell", []string{CatRemoteCodeExecution}})
	}
	if rawRmRootRe.MatchString(src) {
		out = append(out, hit{"destructive.rm_critical", "Removes the root or home directory", destructive})
	}
	if rawSudoRe.MatchString(src) {
		out = append(out, hit{"privilege.sudo", "Runs a command with elevated privileges (sudo)", []string{CatPrivilegeEscalation}})
	}
	return out
}

var gitInternalRe = jsRe(`(^|\/)\.git\/(hooks|config)(\/|$)`)

func evaluateTool(tool string, input map[string]any, ctx *rulesCtx, facts RulesFacts, adminAllowed bool) RulesResult {
	if writeTools[tool] {
		raw := ""
		if s, ok := input["file_path"].(string); ok {
			raw = s
		} else if s, ok := input["notebook_path"].(string); ok {
			raw = s
		}
		if raw == "" {
			return ask("Write without a target path", "ask.bad_input")
		}
		p := resolvePath(raw, ctx.cwd)
		paths := []string{p}
		for _, x := range facts.WritePaths {
			paths = append(paths, resolvePath(x, ""))
		}
		for _, x := range paths {
			if x != "" && isPrivilegedWritePath(x) {
				return RulesResult{Decision: RuleDeny, Reason: "Modifies " + x + " (shell startup, ssh, scheduler or system file)", Categories: []string{CatPrivilegeEscalation}, Rule: "privilege.protected_write"}
			}
			if x != "" && isBlockDevice(x) {
				return RulesResult{Decision: RuleDeny, Reason: "Writes directly to the block device " + x, Categories: destructive, Rule: "destructive.device_write"}
			}
		}
		if adminAllowed {
			return RulesResult{Decision: RuleAllow, Reason: "Allowed by an administrator rule", Categories: []string{}, Rule: "admin.allow"}
		}
		if p == "" {
			return ask("Cannot resolve the target path", "ask.path")
		}
		if gitInternalRe.MatchString(p) {
			return ask("Modifies git hooks or configuration", "ask.git_internal")
		}
		if !anyOf(paths, func(x string) bool { return !(x != "" && isInside(x, ctx.roots)) }) {
			return RulesResult{Decision: RuleAllow, Reason: "Edits a file inside the working directories", Categories: []string{}, Rule: "allow.workspace_write"}
		}
		return ask("Writes outside the working directories", "ask.outside_write")
	}
	if readTools[tool] {
		target := ""
		for _, k := range []string{"file_path", "path", "notebook_path"} {
			if s, ok := input[k].(string); ok {
				target = s
				break
			}
		}
		if target != "" {
			resolved := resolvePath(target, ctx.cwd)
			if mentionsSecret(target) || (resolved != "" && isSecretPath(resolved)) {
				return ask("Reads a credentials file", "ask.secret_read")
			}
		}
		if pattern, ok := input["pattern"].(string); ok && tool == "Glob" && mentionsSecret(pattern) {
			return ask("Looks for credentials files", "ask.secret_read")
		}
		return RulesResult{Decision: RuleAllow, Reason: "Read-only tool", Categories: []string{}, Rule: "allow.read_tool"}
	}
	if adminAllowed {
		return RulesResult{Decision: RuleAllow, Reason: "Allowed by an administrator rule", Categories: []string{}, Rule: "admin.allow"}
	}
	return ask("Not covered by the built-in rules", "ask.default")
}
