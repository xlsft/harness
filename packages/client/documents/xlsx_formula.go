package documents

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// The built-in formula evaluator handles the common arithmetic, text and aggregate functions, enough to report what an
// edit does to the cells that depend on it. Anything else (or anything it cannot parse) makes it say "unknown", never a
// guess: the workbook is marked to recalculate when opened, and the engine can do the full job.

// colName and colIndex convert between 1-based column numbers and letters.
func colName(n int) string {
	s := ""
	for n > 0 {
		n--
		s = string(rune('A'+n%26)) + s
		n /= 26
	}
	return s
}

func colIndex(s string) int {
	n := 0
	for _, c := range strings.ToUpper(s) {
		if c < 'A' || c > 'Z' {
			return 0
		}
		n = n*26 + int(c-'A') + 1
	}
	return n
}

var cellRefRe = regexp.MustCompile(`^\$?([A-Za-z]{1,3})\$?([0-9]{1,7})$`)

// parseCell reads "B12" or "$B$12" into column and row.
func parseCell(s string) (col, row int, ok bool) {
	m := cellRefRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	col = colIndex(m[1])
	row, _ = strconv.Atoi(m[2])
	return col, row, col >= 1 && col <= 16384 && row >= 1 && row <= 1048576
}

func cellName(col, row int) string { return colName(col) + strconv.Itoa(row) }

// area is a rectangle on one sheet ("" for the formula's own sheet). A zero bound means unbounded: a whole column or row.
type area struct {
	sheet                  string
	col1, row1, col2, row2 int
}

func (a area) contains(col, row int) bool {
	return (a.col1 == 0 || (col >= a.col1 && col <= a.col2)) && (a.row1 == 0 || (row >= a.row1 && row <= a.row2))
}

func (a area) String() string {
	if a.col1 == 0 || a.row1 == 0 {
		return fmt.Sprintf("%s%d:%s%d", colName(a.col1), a.row1, colName(a.col2), a.row2)
	}
	if a.col1 == a.col2 && a.row1 == a.row2 {
		return cellName(a.col1, a.row1)
	}
	return cellName(a.col1, a.row1) + ":" + cellName(a.col2, a.row2)
}

func parseArea(s string) (area, bool) {
	s = strings.ReplaceAll(s, "$", "")
	x, y, isRange := strings.Cut(s, ":")
	if !isRange {
		c, r, ok := parseCell(x)
		return area{col1: c, row1: r, col2: c, row2: r}, ok
	}
	c1, r1, ok1 := parseCell(x)
	c2, r2, ok2 := parseCell(y)
	if ok1 && ok2 {
		return area{col1: min(c1, c2), row1: min(r1, r2), col2: max(c1, c2), row2: max(r1, r2)}, true
	}
	// Whole columns and rows.
	if a, b := colIndex(x), colIndex(y); a > 0 && b > 0 {
		return area{col1: min(a, b), col2: max(a, b)}, true
	}
	if a, err := strconv.Atoi(x); err == nil {
		if b, err := strconv.Atoi(y); err == nil && a > 0 && b > 0 {
			return area{row1: min(a, b), row2: max(a, b)}, true
		}
	}
	return area{}, false
}

// --- Lexer ---

type tokKind int

const (
	tkEnd tokKind = iota
	tkNum
	tkStr
	tkOp
	tkRef
	tkName
	tkErr
)

type token struct {
	kind tokKind
	text string
	num  float64
}

var (
	lexNumRe = regexp.MustCompile(`^(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?`)
	// A reference, with an optional sheet in front.
	lexRefRe  = regexp.MustCompile(`^(?:('(?:[^']|'')+'|[A-Za-z_\p{L}][\w.\p{L}]*)!)?(\$?[A-Za-z]{1,3}\$?\d{1,7}(?::\$?[A-Za-z]{1,3}\$?\d{1,7})?|\$?[A-Za-z]{1,3}:\$?[A-Za-z]{1,3}|\$?\d{1,7}:\$?\d{1,7})`)
	lexNameRe = regexp.MustCompile(`^[A-Za-z_\\\p{L}][\w.\\\p{L}]*`)
	lexErrRe  = regexp.MustCompile(`^#(REF!|DIV/0!|VALUE!|NAME\?|NUM!|N/A|NULL!)`)
)

func lex(f string) ([]token, error) {
	var out []token
	for i := 0; i < len(f); {
		c := f[i]
		switch {
		case c == ' ' || c == '\n' || c == '\t' || c == '\r':
			i++
		case c == '"':
			j := i + 1
			var b strings.Builder
			for {
				if j >= len(f) {
					return nil, fmt.Errorf("unterminated string")
				}
				if f[j] == '"' {
					if j+1 < len(f) && f[j+1] == '"' {
						b.WriteByte('"')
						j += 2
						continue
					}
					break
				}
				b.WriteByte(f[j])
				j++
			}
			out = append(out, token{kind: tkStr, text: b.String()})
			i = j + 1
		case strings.ContainsRune("+-*/^&(),%", rune(c)):
			out = append(out, token{kind: tkOp, text: string(c)})
			i++
		case c == '<' || c == '>' || c == '=':
			op := string(c)
			if i+1 < len(f) && (f[i+1] == '=' || (c == '<' && f[i+1] == '>')) {
				op = f[i : i+2]
			}
			out = append(out, token{kind: tkOp, text: op})
			i += len(op)
		case c == '#':
			m := lexErrRe.FindString(f[i:])
			if m == "" {
				return nil, fmt.Errorf("unknown error literal")
			}
			out = append(out, token{kind: tkErr, text: m})
			i += len(m)
		default:
			rest := f[i:]
			if m := lexRefRe.FindString(rest); m != "" && !identFollows(rest, len(m)) {
				out = append(out, token{kind: tkRef, text: m})
				i += len(m)
				continue
			}
			if m := lexNumRe.FindString(rest); m != "" {
				n, err := strconv.ParseFloat(m, 64)
				if err != nil {
					return nil, err
				}
				out = append(out, token{kind: tkNum, num: n, text: m})
				i += len(m)
				continue
			}
			if m := lexNameRe.FindString(rest); m != "" {
				out = append(out, token{kind: tkName, text: m})
				i += len(m)
				continue
			}
			return nil, fmt.Errorf("unexpected %q", rest[:min(len(rest), 8)])
		}
	}
	return out, nil
}

func identFollows(s string, at int) bool {
	if at >= len(s) {
		return false
	}
	c := s[at]
	return c == '(' || c == '_' || c == '.' || c == '[' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// --- Parser ---

type fnode struct {
	kind string // num str bool err ref name call unary binary percent
	num  float64
	text string
	args []*fnode
	ref  fref
}

// fref is a reference as written.
type fref struct {
	sheet string
	area  area
}

type fparser struct {
	toks []token
	at   int
}

func (p *fparser) peek() token {
	if p.at < len(p.toks) {
		return p.toks[p.at]
	}
	return token{}
}

func (p *fparser) isOp(s string) bool {
	t := p.peek()
	return t.kind == tkOp && t.text == s
}

// parseFormula parses formula text (with or without the leading =).
func parseFormula(f string) (*fnode, error) {
	toks, err := lex(strings.TrimPrefix(strings.TrimSpace(f), "="))
	if err != nil {
		return nil, err
	}
	p := &fparser{toks: toks}
	n, err := p.compare()
	if err != nil {
		return nil, err
	}
	if p.at != len(p.toks) {
		return nil, fmt.Errorf("unexpected %q", p.peek().text)
	}
	return n, nil
}

func (p *fparser) binary(ops []string, next func() (*fnode, error)) (*fnode, error) {
	l, err := next()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		found := false
		if t.kind == tkOp {
			for _, o := range ops {
				found = found || o == t.text
			}
		}
		if !found {
			return l, nil
		}
		p.at++
		r, err := next()
		if err != nil {
			return nil, err
		}
		l = &fnode{kind: "binary", text: t.text, args: []*fnode{l, r}}
	}
}

func (p *fparser) compare() (*fnode, error) {
	return p.binary([]string{"=", "<>", "<", ">", "<=", ">="}, p.concat)
}
func (p *fparser) concat() (*fnode, error) { return p.binary([]string{"&"}, p.sum) }
func (p *fparser) sum() (*fnode, error)    { return p.binary([]string{"+", "-"}, p.product) }
func (p *fparser) product() (*fnode, error) {
	return p.binary([]string{"*", "/"}, p.power)
}
func (p *fparser) power() (*fnode, error) { return p.binary([]string{"^"}, p.unary) }

func (p *fparser) unary() (*fnode, error) {
	if p.isOp("-") || p.isOp("+") {
		op := p.peek().text
		p.at++
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &fnode{kind: "unary", text: op, args: []*fnode{x}}, nil
	}
	x, err := p.primary()
	if err != nil {
		return nil, err
	}
	for p.isOp("%") {
		p.at++
		x = &fnode{kind: "percent", args: []*fnode{x}}
	}
	return x, nil
}

func (p *fparser) primary() (*fnode, error) {
	t := p.peek()
	p.at++
	switch t.kind {
	case tkNum:
		return &fnode{kind: "num", num: t.num}, nil
	case tkStr:
		return &fnode{kind: "str", text: t.text}, nil
	case tkErr:
		return &fnode{kind: "err", text: t.text}, nil
	case tkRef:
		r, err := parseRefToken(t.text)
		if err != nil {
			return nil, err
		}
		return &fnode{kind: "ref", ref: r}, nil
	case tkName:
		upper := strings.ToUpper(t.text)
		if p.isOp("(") {
			p.at++
			n := &fnode{kind: "call", text: upper}
			if p.isOp(")") {
				p.at++
				return n, nil
			}
			for {
				a, err := p.compare()
				if err != nil {
					return nil, err
				}
				n.args = append(n.args, a)
				if p.isOp(",") {
					p.at++
					continue
				}
				if p.isOp(")") {
					p.at++
					return n, nil
				}
				return nil, fmt.Errorf("expected ) in %s", upper)
			}
		}
		switch upper {
		case "TRUE":
			return &fnode{kind: "bool", num: 1}, nil
		case "FALSE":
			return &fnode{kind: "bool"}, nil
		}
		return &fnode{kind: "name", text: t.text}, nil
	case tkOp:
		if t.text == "(" {
			n, err := p.compare()
			if err != nil {
				return nil, err
			}
			if !p.isOp(")") {
				return nil, fmt.Errorf("expected )")
			}
			p.at++
			return n, nil
		}
	}
	return nil, fmt.Errorf("unexpected %q", t.text)
}

func parseRefToken(s string) (fref, error) {
	r := fref{}
	if i := strings.LastIndex(s, "!"); i >= 0 {
		r.sheet = s[:i]
		if strings.HasPrefix(r.sheet, "'") {
			r.sheet = strings.ReplaceAll(r.sheet[1:len(r.sheet)-1], "''", "'")
		}
		s = s[i+1:]
	}
	a, ok := parseArea(s)
	if !ok {
		return r, fmt.Errorf("bad reference %q", s)
	}
	r.area = a
	return r, nil
}

// collectRefs lists a formula's references and the defined names it uses.
func (n *fnode) collectRefs(refs *[]fref, names *[]string) {
	if n == nil {
		return
	}
	switch n.kind {
	case "ref":
		*refs = append(*refs, n.ref)
	case "name":
		*names = append(*names, n.text)
	}
	for _, a := range n.args {
		a.collectRefs(refs, names)
	}
}

// shiftFormula moves a shared formula's relative references by the distance from its master, leaving strings alone.
func shiftFormula(f string, dCol, dRow int) string {
	if dCol == 0 && dRow == 0 {
		return f
	}
	var b strings.Builder
	in := false
	last := 0
	flush := func(to int) {
		if to > last {
			b.WriteString(shiftPlain(f[last:to], dCol, dRow))
		}
	}
	for i := 0; i < len(f); i++ {
		if f[i] == '"' {
			if !in {
				flush(i)
				last = i
				in = true
			} else if i+1 < len(f) && f[i+1] == '"' {
				i++
			} else {
				b.WriteString(f[last : i+1])
				last = i + 1
				in = false
			}
		}
	}
	if in {
		b.WriteString(f[last:])
	} else {
		flush(len(f))
	}
	return b.String()
}

var shiftRefRe = regexp.MustCompile(`(\$?)([A-Za-z]{1,3})(\$?)(\d{1,7})`)

func shiftPlain(s string, dCol, dRow int) string {
	var b strings.Builder
	at := 0
	for _, m := range shiftRefRe.FindAllStringSubmatchIndex(s, -1) {
		before := byte(0)
		if m[0] > 0 {
			before = s[m[0]-1]
		}
		after := byte(0)
		if m[1] < len(s) {
			after = s[m[1]]
		}
		if isIdentByte(before) || isIdentByte(after) || after == '(' {
			continue
		}
		b.WriteString(s[at:m[0]])
		col := colIndex(s[m[4]:m[5]])
		row, _ := strconv.Atoi(s[m[8]:m[9]])
		if s[m[2]:m[3]] == "" {
			col += dCol
		}
		if s[m[6]:m[7]] == "" {
			row += dRow
		}
		if col < 1 || row < 1 {
			b.WriteString("#REF!")
		} else {
			b.WriteString(s[m[2]:m[3]] + colName(col) + s[m[6]:m[7]] + strconv.Itoa(row))
		}
		at = m[1]
	}
	b.WriteString(s[at:])
	return b.String()
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '.' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c >= 0x80
}

// --- Evaluator ---

// fval is a computed value: float64, string, bool, ferr, nil for blank, or frange.
type fval any

type ferr string

type frange struct {
	cols, rows int
	vals       []fval
}

// errUnknown says the evaluator cannot answer: an unsupported function or an unknown name.
type errUnknown struct{ why string }

func (e errUnknown) Error() string { return e.why }

type evalCtx struct {
	// cell returns a cell's value.
	cell func(sheet string, col, row int) (fval, error)
	// bounds returns a sheet's used bottom-right corner, for whole-column references.
	bounds func(sheet string) (col, row int)
	// name resolves a defined name to a reference.
	name  func(name string) (fref, bool)
	sheet string
}

func (c *evalCtx) eval(n *fnode) (fval, error) {
	switch n.kind {
	case "num":
		return n.num, nil
	case "str":
		return n.text, nil
	case "bool":
		return n.num != 0, nil
	case "err":
		return ferr(n.text), nil
	case "ref":
		return c.ref(n.ref)
	case "name":
		if c.name != nil {
			if r, ok := c.name(n.text); ok {
				return c.ref(r)
			}
		}
		return nil, errUnknown{"the name " + n.text}
	case "percent":
		v, err := c.scalar(n.args[0])
		if err != nil {
			return nil, err
		}
		f, e := toNum(v)
		if e != "" {
			return e, nil
		}
		return f / 100, nil
	case "unary":
		v, err := c.scalar(n.args[0])
		if err != nil {
			return nil, err
		}
		f, e := toNum(v)
		if e != "" {
			return e, nil
		}
		if n.text == "-" {
			return -f, nil
		}
		return f, nil
	case "binary":
		return c.binary(n)
	case "call":
		return c.call(n)
	}
	return nil, errUnknown{"an expression"}
}

func (c *evalCtx) ref(r fref) (fval, error) {
	sheet := r.sheet
	if sheet == "" {
		sheet = c.sheet
	}
	a := r.area
	if a.col1 == 0 || a.row1 == 0 {
		mc, mr := c.bounds(sheet)
		if a.col1 == 0 {
			a.col1, a.col2 = 1, mc
		}
		if a.row1 == 0 {
			a.row1, a.row2 = 1, mr
		}
	}
	if a.col1 == a.col2 && a.row1 == a.row2 {
		return c.cell(sheet, a.col1, a.row1)
	}
	rows, cols := a.row2-a.row1+1, a.col2-a.col1+1
	if rows <= 0 || cols <= 0 {
		return frange{}, nil
	}
	if rows*cols > 2_000_000 {
		return nil, errUnknown{"a very large range"}
	}
	out := frange{cols: cols, rows: rows, vals: make([]fval, 0, rows*cols)}
	for y := a.row1; y <= a.row2; y++ {
		for x := a.col1; x <= a.col2; x++ {
			v, err := c.cell(sheet, x, y)
			if err != nil {
				return nil, err
			}
			out.vals = append(out.vals, v)
		}
	}
	return out, nil
}

// scalar evaluates to one value; a range in a scalar place is outside what is implemented (implicit intersection).
func (c *evalCtx) scalar(n *fnode) (fval, error) {
	v, err := c.eval(n)
	if err != nil {
		return nil, err
	}
	if _, isRange := v.(frange); isRange {
		return nil, errUnknown{"a range used as one value"}
	}
	return v, nil
}

func toNum(v fval) (float64, ferr) {
	switch x := v.(type) {
	case nil:
		return 0, ""
	case float64:
		return x, ""
	case bool:
		if x {
			return 1, ""
		}
		return 0, ""
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, "#VALUE!"
		}
		return f, ""
	case ferr:
		return 0, x
	}
	return 0, "#VALUE!"
}

func toText(v fval) string {
	switch x := v.(type) {
	case nil:
		return ""
	case float64:
		return formatNum(x)
	case bool:
		if x {
			return "TRUE"
		}
		return "FALSE"
	case string:
		return x
	case ferr:
		return string(x)
	}
	return ""
}

func formatNum(f float64) string { return strconv.FormatFloat(f, 'g', 15, 64) }

func (c *evalCtx) binary(n *fnode) (fval, error) {
	l, err := c.scalar(n.args[0])
	if err != nil {
		return nil, err
	}
	r, err := c.scalar(n.args[1])
	if err != nil {
		return nil, err
	}
	if e, ok := l.(ferr); ok {
		return e, nil
	}
	if e, ok := r.(ferr); ok {
		return e, nil
	}
	switch n.text {
	case "&":
		return toText(l) + toText(r), nil
	case "=", "<>", "<", ">", "<=", ">=":
		cmp := compareVals(l, r)
		switch n.text {
		case "=":
			return cmp == 0, nil
		case "<>":
			return cmp != 0, nil
		case "<":
			return cmp < 0, nil
		case ">":
			return cmp > 0, nil
		case "<=":
			return cmp <= 0, nil
		}
		return cmp >= 0, nil
	}
	a, e := toNum(l)
	if e != "" {
		return e, nil
	}
	b, e := toNum(r)
	if e != "" {
		return e, nil
	}
	switch n.text {
	case "+":
		return a + b, nil
	case "-":
		return a - b, nil
	case "*":
		return a * b, nil
	case "/":
		if b == 0 {
			return ferr("#DIV/0!"), nil
		}
		return a / b, nil
	case "^":
		return math.Pow(a, b), nil
	}
	return nil, errUnknown{"the operator " + n.text}
}

// compareVals orders values the way a spreadsheet does: numbers before text before booleans; text ignores case.
func compareVals(a, b fval) int {
	rank := func(v fval) int {
		switch v.(type) {
		case string:
			return 1
		case bool:
			return 2
		}
		return 0
	}
	if a == nil {
		switch b.(type) {
		case string:
			a = ""
		case bool:
			a = false
		default:
			a = 0.0
		}
	}
	if b == nil {
		switch a.(type) {
		case string:
			b = ""
		case bool:
			b = false
		default:
			b = 0.0
		}
	}
	if ra, rb := rank(a), rank(b); ra != rb {
		return ra - rb
	}
	switch x := a.(type) {
	case float64:
		y := b.(float64)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	case string:
		return strings.Compare(strings.ToLower(x), strings.ToLower(b.(string)))
	case bool:
		y := b.(bool)
		if x == y {
			return 0
		}
		if !x {
			return -1
		}
		return 1
	}
	return 0
}

func (c *evalCtx) flat(args []*fnode) ([]fval, []bool, error) {
	var vals []fval
	var direct []bool
	for _, a := range args {
		v, err := c.eval(a)
		if err != nil {
			return nil, nil, err
		}
		if r, ok := v.(frange); ok {
			for _, x := range r.vals {
				vals = append(vals, x)
				direct = append(direct, false)
			}
			continue
		}
		vals = append(vals, v)
		direct = append(direct, a.kind != "ref")
	}
	return vals, direct, nil
}

// numbers applies the aggregate rule: in a range only numbers count; typed directly, text is converted.
func (c *evalCtx) numbers(args []*fnode) ([]float64, ferr, error) {
	vals, direct, err := c.flat(args)
	if err != nil {
		return nil, "", err
	}
	var out []float64
	for i, v := range vals {
		if e, ok := v.(ferr); ok {
			return nil, e, nil
		}
		switch x := v.(type) {
		case float64:
			out = append(out, x)
		case bool, string:
			if direct[i] {
				f, e := toNum(x)
				if e != "" {
					return nil, e, nil
				}
				out = append(out, f)
			}
		}
	}
	return out, "", nil
}

func (c *evalCtx) call(n *fnode) (fval, error) {
	need := func(lo, hi int) error {
		if len(n.args) < lo || (hi >= 0 && len(n.args) > hi) {
			return errUnknown{n.text + " with these arguments"}
		}
		return nil
	}
	num := func(i int) (float64, ferr, error) {
		v, err := c.scalar(n.args[i])
		if err != nil {
			return 0, "", err
		}
		f, e := toNum(v)
		return f, e, nil
	}
	text := func(i int) (string, ferr, error) {
		v, err := c.scalar(n.args[i])
		if err != nil {
			return "", "", err
		}
		if e, ok := v.(ferr); ok {
			return "", e, nil
		}
		return toText(v), "", nil
	}
	switch n.text {
	case "SUM", "AVERAGE", "MIN", "MAX", "PRODUCT":
		nums, e, err := c.numbers(n.args)
		if err != nil {
			return nil, err
		}
		if e != "" {
			return e, nil
		}
		switch n.text {
		case "SUM":
			t := 0.0
			for _, x := range nums {
				t += x
			}
			return t, nil
		case "AVERAGE":
			if len(nums) == 0 {
				return ferr("#DIV/0!"), nil
			}
			t := 0.0
			for _, x := range nums {
				t += x
			}
			return t / float64(len(nums)), nil
		case "PRODUCT":
			if len(nums) == 0 {
				return 0.0, nil
			}
			t := 1.0
			for _, x := range nums {
				t *= x
			}
			return t, nil
		}
		if len(nums) == 0 {
			return 0.0, nil
		}
		t := nums[0]
		for _, x := range nums {
			if (n.text == "MIN" && x < t) || (n.text == "MAX" && x > t) {
				t = x
			}
		}
		return t, nil
	case "COUNT", "COUNTA", "COUNTBLANK":
		vals, _, err := c.flat(n.args)
		if err != nil {
			return nil, err
		}
		k := 0
		for _, v := range vals {
			switch n.text {
			case "COUNT":
				if _, ok := v.(float64); ok {
					k++
				}
			case "COUNTA":
				if v != nil {
					k++
				}
			default:
				if v == nil || v == "" {
					k++
				}
			}
		}
		return float64(k), nil
	case "IF":
		if err := need(2, 3); err != nil {
			return nil, err
		}
		v, err := c.scalar(n.args[0])
		if err != nil {
			return nil, err
		}
		if e, ok := v.(ferr); ok {
			return e, nil
		}
		f, e := toNum(v)
		if e != "" {
			return e, nil
		}
		if f != 0 {
			return c.eval(n.args[1])
		}
		if len(n.args) == 3 {
			return c.eval(n.args[2])
		}
		return false, nil
	case "IFERROR":
		if err := need(2, 2); err != nil {
			return nil, err
		}
		v, err := c.eval(n.args[0])
		if err != nil {
			return nil, err
		}
		if _, ok := v.(ferr); ok {
			return c.eval(n.args[1])
		}
		return v, nil
	case "AND", "OR":
		vals, _, err := c.flat(n.args)
		if err != nil {
			return nil, err
		}
		res := n.text == "AND"
		for _, v := range vals {
			if e, ok := v.(ferr); ok {
				return e, nil
			}
			if v == nil {
				continue
			}
			if _, isText := v.(string); isText {
				continue
			}
			f, _ := toNum(v)
			if n.text == "AND" {
				res = res && f != 0
			} else {
				res = res || f != 0
			}
		}
		return res, nil
	case "NOT":
		if err := need(1, 1); err != nil {
			return nil, err
		}
		f, e, err := num(0)
		if err != nil {
			return nil, err
		}
		if e != "" {
			return e, nil
		}
		return f == 0, nil
	case "ABS", "INT", "SQRT":
		if err := need(1, 1); err != nil {
			return nil, err
		}
		f, e, err := num(0)
		if err != nil {
			return nil, err
		}
		if e != "" {
			return e, nil
		}
		switch n.text {
		case "ABS":
			return math.Abs(f), nil
		case "INT":
			return math.Floor(f), nil
		}
		if f < 0 {
			return ferr("#NUM!"), nil
		}
		return math.Sqrt(f), nil
	case "ROUND", "ROUNDUP", "ROUNDDOWN":
		if err := need(2, 2); err != nil {
			return nil, err
		}
		f, e, err := num(0)
		if err != nil {
			return nil, err
		}
		if e != "" {
			return e, nil
		}
		d, e, err := num(1)
		if err != nil {
			return nil, err
		}
		if e != "" {
			return e, nil
		}
		p := math.Pow(10, math.Trunc(d))
		switch n.text {
		case "ROUND":
			return math.Round(f*p) / p, nil
		case "ROUNDUP":
			if f < 0 {
				return math.Floor(f*p) / p, nil
			}
			return math.Ceil(f*p) / p, nil
		}
		return math.Trunc(f*p) / p, nil
	case "MOD":
		if err := need(2, 2); err != nil {
			return nil, err
		}
		a, e, err := num(0)
		if err != nil {
			return nil, err
		}
		if e != "" {
			return e, nil
		}
		b, e, err := num(1)
		if err != nil {
			return nil, err
		}
		if e != "" {
			return e, nil
		}
		if b == 0 {
			return ferr("#DIV/0!"), nil
		}
		return a - b*math.Floor(a/b), nil
	case "CONCATENATE", "CONCAT":
		vals, _, err := c.flat(n.args)
		if err != nil {
			return nil, err
		}
		var b strings.Builder
		for _, v := range vals {
			if e, ok := v.(ferr); ok {
				return e, nil
			}
			b.WriteString(toText(v))
		}
		return b.String(), nil
	case "LEN", "UPPER", "LOWER", "TRIM":
		if err := need(1, 1); err != nil {
			return nil, err
		}
		s, e, err := text(0)
		if err != nil {
			return nil, err
		}
		if e != "" {
			return e, nil
		}
		switch n.text {
		case "LEN":
			return float64(len([]rune(s))), nil
		case "UPPER":
			return strings.ToUpper(s), nil
		case "LOWER":
			return strings.ToLower(s), nil
		}
		return strings.Join(strings.Fields(s), " "), nil
	case "LEFT", "RIGHT":
		if err := need(1, 2); err != nil {
			return nil, err
		}
		s, e, err := text(0)
		if err != nil {
			return nil, err
		}
		if e != "" {
			return e, nil
		}
		k := 1.0
		if len(n.args) == 2 {
			if k, e, err = num(1); err != nil {
				return nil, err
			} else if e != "" {
				return e, nil
			}
		}
		rs := []rune(s)
		m := min(int(k), len(rs))
		if m < 0 {
			return ferr("#VALUE!"), nil
		}
		if n.text == "LEFT" {
			return string(rs[:m]), nil
		}
		return string(rs[len(rs)-m:]), nil
	case "MID":
		if err := need(3, 3); err != nil {
			return nil, err
		}
		s, e, err := text(0)
		if err != nil {
			return nil, err
		}
		if e != "" {
			return e, nil
		}
		st, e, err := num(1)
		if err != nil {
			return nil, err
		}
		if e != "" {
			return e, nil
		}
		k, e, err := num(2)
		if err != nil {
			return nil, err
		}
		if e != "" {
			return e, nil
		}
		rs := []rune(s)
		a := int(st) - 1
		if a < 0 || k < 0 {
			return ferr("#VALUE!"), nil
		}
		if a >= len(rs) {
			return "", nil
		}
		return string(rs[a:min(a+int(k), len(rs))]), nil
	}
	return nil, errUnknown{"the function " + n.text}
}
