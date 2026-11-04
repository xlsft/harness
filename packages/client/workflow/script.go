package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
)

// Meta is what a script says about itself, in its opening literal.
type Meta struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Phases      []string `json:"phases,omitempty"`
}

// script is a parsed script: its meta, and the program ready to run.
type script struct {
	meta Meta
	text string
	prog *goja.Program
}

const (
	// tickName is the host function counted at each function entry and loop iteration; a script may not name it.
	tickName = "$$tick"
	// The script becomes the body of an async function, all on the first line so that its line numbers stay its own.
	// The block lets a script declare a name the API uses. The parameters are the script's whole access to the host.
	wrapHead = `(async function(args,agent,parallel,pipeline,phase,log,` + tickName + `){"use strict";{`
	wrapTail = "\n}})"
)

// metaHead finds the export at the start of a script, after comments.
var metaHead = regexp.MustCompile(`^(?:\s|//[^\n]*\n|/\*[\s\S]*?\*/)*(export)\s+const\s+meta\b`)

// parseScript reads a script without running any of it: goja's parser gives the tree the meta is read from, and the
// tree says where to count steps. goja has no modules, so the one export a script has is blanked out in place.
func parseScript(text string) (*script, error) {
	if strings.Contains(text, tickName) {
		return nil, fmt.Errorf("a script cannot use the name %s", tickName)
	}
	m := metaHead.FindStringSubmatchIndex(text)
	if m == nil {
		return nil, errors.New("a script starts with: export const meta = { name, description, phases }")
	}
	body := text[:m[2]] + strings.Repeat(" ", m[3]-m[2]) + text[m[3]:]
	wrapped := wrapHead + body + wrapTail
	tree, err := parser.ParseFile(nil, "workflow.js", wrapped, 0, parser.WithDisableSourceMaps)
	if err != nil {
		return nil, fmt.Errorf("the script does not parse: %w", err)
	}
	fn, statements := scriptBody(tree)
	if fn == nil || len(statements) == 0 {
		return nil, errors.New("the script does not parse")
	}
	meta, err := readMeta(statements[0])
	if err != nil {
		return nil, err
	}
	prog, err := compile(instrument(wrapped, statements))
	if err != nil {
		// Should not happen, and the compute limit still holds: run the script as it was written.
		if prog, err = compile(wrapped); err != nil {
			return nil, fmt.Errorf("the script does not compile: %w", err)
		}
	}
	return &script{meta: meta, text: text, prog: prog}, nil
}

// compile never follows a sourceMappingURL comment: by default goja would read that file.
func compile(src string) (*goja.Program, error) {
	tree, err := goja.Parse("workflow.js", src, parser.WithDisableSourceMaps)
	if err != nil {
		return nil, err
	}
	return goja.CompileAST(tree, true)
}

// scriptBody finds the wrapper function in the tree, and the statements of the script.
func scriptBody(tree *ast.Program) (*ast.FunctionLiteral, []ast.Statement) {
	if len(tree.Body) != 1 {
		return nil, nil
	}
	es, ok := tree.Body[0].(*ast.ExpressionStatement)
	if !ok {
		return nil, nil
	}
	fn, ok := es.Expression.(*ast.FunctionLiteral)
	if !ok || fn.Body == nil {
		return nil, nil
	}
	for _, s := range fn.Body.List {
		if b, ok := s.(*ast.BlockStatement); ok {
			return fn, b.List
		}
	}
	return nil, nil
}

// readMeta reads the literal of `const meta = ...`. Nothing else than plain values passes: it is shown to the user
// before anything runs.
func readMeta(s ast.Statement) (Meta, error) {
	decl, ok := s.(*ast.LexicalDeclaration)
	if !ok || len(decl.List) != 1 || decl.List[0].Initializer == nil {
		return Meta{}, errors.New("a script starts with: export const meta = { name, description, phases }")
	}
	v, err := literal(decl.List[0].Initializer)
	if err != nil {
		return Meta{}, fmt.Errorf("meta must be a pure literal: %w", err)
	}
	fields, ok := v.(map[string]any)
	if !ok {
		return Meta{}, errors.New("meta must be an object literal")
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("meta: name and description are strings, phases a list of strings: %w", err)
	}
	if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Description) == "" {
		return Meta{}, errors.New("meta needs a name and a description")
	}
	return m, nil
}

// literal evaluates a tree made of plain values only.
func literal(e ast.Expression) (any, error) {
	switch n := e.(type) {
	case *ast.StringLiteral:
		return n.Value.String(), nil
	case *ast.NumberLiteral:
		return n.Value, nil
	case *ast.BooleanLiteral:
		return n.Value, nil
	case *ast.NullLiteral:
		return nil, nil
	case *ast.ArrayLiteral:
		out := make([]any, 0, len(n.Value))
		for _, x := range n.Value {
			v, err := literal(x)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case *ast.ObjectLiteral:
		out := make(map[string]any, len(n.Value))
		for _, p := range n.Value {
			kv, ok := p.(*ast.PropertyKeyed)
			if !ok || kv.Computed || kv.Kind != ast.PropertyKindValue {
				return nil, errors.New("only plain keys and values")
			}
			key, err := literal(kv.Key)
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("only plain keys and values")
			}
			if out[name], err = literal(kv.Value); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("%T is not a plain value", e)
}

// Counted code: at each function entry and each loop iteration. A loop's body may be any statement, and the form
// around it keeps it one statement that an outer else cannot bind to.
const (
	entryTick = tickName + "();"
	loopTick  = "if(" + tickName + "());else "
)

// instrument inserts the step counting. goja has no instruction counter of its own, and this makes the cap depend on
// the script alone, not on how busy the machine is. An arrow function with a bare expression has no statement to
// count in; it cannot loop by itself, and recursion hits the stack limit.
func instrument(src string, statements []ast.Statement) string {
	type insert struct {
		at   int
		text string
	}
	var inserts []insert
	loop := func(body ast.Statement) {
		// A parenthesized expression's tree starts inside the parentheses.
		at := int(body.Idx0()) - 1
		for i := at - 1; i >= 0 && (src[i] == '(' || src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r'); i-- {
			if src[i] == '(' {
				at = i
			}
		}
		inserts = append(inserts, insert{at, loopTick})
	}
	walk(reflect.ValueOf(statements), map[uintptr]bool{}, func(n any) {
		switch x := n.(type) {
		case *ast.ForStatement:
			loop(x.Body)
		case *ast.ForInStatement:
			loop(x.Body)
		case *ast.ForOfStatement:
			loop(x.Body)
		case *ast.WhileStatement:
			loop(x.Body)
		case *ast.DoWhileStatement:
			loop(x.Body)
		case *ast.FunctionLiteral:
			if x.Body != nil {
				inserts = append(inserts, insert{int(x.Body.LeftBrace), entryTick})
			}
		case *ast.ArrowFunctionLiteral:
			if b, ok := x.Body.(*ast.BlockStatement); ok {
				inserts = append(inserts, insert{int(b.LeftBrace), entryTick})
			}
		}
	})
	sort.SliceStable(inserts, func(i, j int) bool { return inserts[i].at < inserts[j].at })
	var b strings.Builder
	last := 0
	for _, in := range inserts {
		b.WriteString(src[last:in.at])
		b.WriteString(in.text)
		last = in.at
	}
	b.WriteString(src[last:])
	return b.String()
}

var astPath = reflect.TypeFor[ast.Program]().PkgPath()

// walk visits every node of goja's tree. The package has no visitor of its own for all its node types.
func walk(v reflect.Value, seen map[uintptr]bool, visit func(any)) {
	switch v.Kind() {
	case reflect.Interface:
		if !v.IsNil() {
			walk(v.Elem(), seen, visit)
		}
	case reflect.Pointer:
		if v.IsNil() || v.Type().Elem().PkgPath() != astPath || seen[v.Pointer()] {
			return
		}
		seen[v.Pointer()] = true
		visit(v.Interface())
		walk(v.Elem(), seen, visit)
	case reflect.Slice:
		for i := range v.Len() {
			walk(v.Index(i), seen, visit)
		}
	case reflect.Struct:
		if v.Type().PkgPath() != astPath {
			return
		}
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				walk(v.Field(i), seen, visit)
			}
		}
	}
}
