package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xlsft/harness/packages/client/tools"
)

// Output limits of the tool.
const (
	maxResultBytes = 12000
	maxHoverChars  = 6000
	maxPreviewRune = 160
)

// The operations of the LSP tool.
const (
	opDefinition       = "definition"
	opReferences       = "references"
	opHover            = "hover"
	opDocumentSymbols  = "document_symbols"
	opWorkspaceSymbols = "workspace_symbols"
)

// Tool returns the LSP tool of this Manager.
func (m *Manager) Tool() tools.Tool { return &lspTool{m: m} }

type lspTool struct{ m *Manager }

type toolInput struct {
	Operation string `json:"operation"`
	FilePath  string `json:"file_path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
	Query     string `json:"query"`
}

func (*lspTool) Name() string                  { return "LSP" }
func (*lspTool) ReadOnly(json.RawMessage) bool { return true }

// HiddenFromModel implements tools.ModelHider: with the setting off the model does not see the tool.
func (t *lspTool) HiddenFromModel() bool { return !t.m.config().Enabled }

func (t *lspTool) Run(ctx context.Context, env *tools.Env, call tools.Call) tools.Result {
	var in toolInput
	if err := json.Unmarshal(call.Input, &in); err != nil {
		return tools.ErrorResult("InputValidationError: %v", err)
	}
	switch in.Operation {
	case opDefinition, opReferences, opHover:
		if in.Line < 1 || in.Character < 1 {
			return tools.ErrorResult("InputValidationError: %s needs line and character, both counted from 1 as Read shows them", in.Operation)
		}
	case opDocumentSymbols:
	case opWorkspaceSymbols:
		if strings.TrimSpace(in.Query) == "" {
			return tools.ErrorResult("InputValidationError: workspace_symbols needs a query")
		}
	default:
		return tools.ErrorResult("InputValidationError: operation must be one of definition, references, hover, document_symbols, workspace_symbols")
	}
	if in.FilePath == "" {
		return tools.ErrorResult("InputValidationError: file_path is required")
	}
	if !filepath.IsAbs(in.FilePath) {
		return tools.ErrorResult("file_path must be an absolute path, got %q", in.FilePath)
	}
	if st, err := os.Stat(in.FilePath); err != nil {
		return tools.ErrorResult("File does not exist: %s", in.FilePath)
	} else if st.IsDir() {
		return tools.ErrorResult("%s is a directory", in.FilePath)
	}
	cfg := t.m.config()
	if !cfg.Enabled {
		return tools.ErrorResult("The LSP tool is turned off in the user's settings.")
	}
	real := realPath(in.FilePath)
	ext := filepath.Ext(real)
	spec, language := specFor(specTable(cfg.Servers), real)
	if spec == nil {
		return tools.ErrorResult("No language server is known for %s files (known: %s). The user can add one with the lspServers setting; use Grep and Read here.", orNoExtension(ext), knownLanguages(specTable(cfg.Servers)))
	}
	if spec.disabled {
		return tools.ErrorResult("The %s language server is turned off in the user's settings (lspServers).", spec.ID)
	}
	ctx, cancel := context.WithTimeout(ctx, t.m.opts.RequestTimeout)
	defer cancel()
	at := placeOf(env)
	var srv *server
	var raw json.RawMessage
	var err error
	// A server that dies under a call gets one more chance, within the restart limit.
	for attempt := 0; attempt < 2; attempt++ {
		srv, err = t.m.acquire(spec, real, at, true)
		if err != nil {
			return tools.ErrorResult("%s", err.Error())
		}
		raw, err = t.ask(ctx, srv, in, real, language)
		if !errors.Is(err, errConnClosed) || ctx.Err() != nil {
			break
		}
	}
	return t.result(ctx, srv, in, real, at, raw, err)
}

func orNoExtension(ext string) string {
	if ext == "" {
		return "extensionless"
	}
	return ext
}

func knownLanguages(table []Spec) string {
	ids := make([]string, 0, len(table))
	for _, s := range table {
		ids = append(ids, s.ID)
	}
	return strings.Join(ids, ", ")
}

// ask brings the server up to date and sends the request. While a young or indexing server answers with nothing, it
// waits for the server and asks once more.
func (t *lspTool) ask(ctx context.Context, srv *server, in toolInput, path, language string) (json.RawMessage, error) {
	if err := srv.waitReady(ctx); err != nil {
		return nil, err
	}
	if err := srv.refreshOpenDocuments(ctx); err != nil {
		return nil, err
	}
	data, err := srv.syncDocument(ctx, path, language, false)
	if err != nil {
		return nil, err
	}
	method, params, err := requestFor(srv.capabilities(), in, path, string(data))
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		raw, err := srv.request(ctx, method, params)
		var rerr *rpcError
		busy := errors.As(err, &rerr) && (rerr.Code == codeContentModified || rerr.Code == codeServerCancelled || rerr.Code == codeRequestCancelled)
		if attempt == 0 && (busy || (err == nil && isEmptyResult(raw) && (srv.indexing() != "" || srv.justStarted()))) {
			srv.waitIdle(ctx)
			if ctx.Err() == nil {
				continue
			}
		}
		if busy && ctx.Err() == nil {
			return raw, nil
		}
		return raw, err
	}
}

func isEmptyResult(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null" || s == "[]"
}

// requestFor builds the request of an operation, or says that the server cannot do it.
func requestFor(c capabilities, in toolInput, path, text string) (string, any, error) {
	doc := map[string]any{"uri": pathURI(path)}
	supported := map[string]bool{opDefinition: c.definition, opReferences: c.references, opHover: c.hover,
		opDocumentSymbols: c.documentSymbol, opWorkspaceSymbols: c.workspaceSymbol}
	if !supported[in.Operation] {
		return "", nil, &refusedError{fmt.Sprintf("This language server does not support %s.", in.Operation)}
	}
	switch in.Operation {
	case opDocumentSymbols:
		return "textDocument/documentSymbol", map[string]any{"textDocument": doc}, nil
	case opWorkspaceSymbols:
		return "workspace/symbol", map[string]any{"query": in.Query}, nil
	}
	lines := strings.Split(text, "\n")
	if in.Line > len(lines) {
		return "", nil, &refusedError{fmt.Sprintf("line %d is past the end of the file (%d lines)", in.Line, len(lines))}
	}
	pos := map[string]any{"line": in.Line - 1, "character": utf16Offset(strings.TrimRight(lines[in.Line-1], "\r"), in.Character-1)}
	params := map[string]any{"textDocument": doc, "position": pos}
	switch in.Operation {
	case opDefinition:
		return "textDocument/definition", params, nil
	case opReferences:
		params["context"] = map[string]any{"includeDeclaration": true}
		return "textDocument/references", params, nil
	}
	return "textDocument/hover", params, nil
}

// result turns what happened into the tool's result. A server that is not ready is a normal answer: the model can go on
// without it, and the server keeps working in the background.
func (t *lspTool) result(ctx context.Context, srv *server, in toolInput, path string, at place, raw json.RawMessage, err error) tools.Result {
	base := map[string]any{"operation": in.Operation, "filePath": in.FilePath, "server": srv.name}
	var refused *refusedError
	switch {
	case errors.As(err, &refused):
		return tools.ErrorResult("%s", refused.msg)
	case errors.Is(err, errConnClosed):
		return tools.ErrorResult("The %s language server stopped while answering (%s).", srv.name, srv.failure())
	case errors.Is(err, context.DeadlineExceeded) || (err != nil && ctx.Err() == context.DeadlineExceeded):
		base["state"] = "timeout"
		state := srv.indexing()
		secs := int(t.m.opts.RequestTimeout.Seconds())
		var text string
		if state != "" {
			base["state"] = "indexing"
			text = fmt.Sprintf("The %s language server is still indexing this workspace (%s): no answer within %d s. It keeps going in the background; try again shortly, and use Grep and Read meanwhile.", srv.name, state, secs)
		} else {
			text = fmt.Sprintf("The %s language server did not answer within %d s. Use Grep and Read, or try again later.", srv.name, secs)
		}
		r := tools.TextResult(text, base)
		r.Summary = fmt.Sprint(base["state"])
		return r
	case errors.Is(err, context.Canceled):
		return tools.ErrorResult("interrupted")
	case err != nil:
		return tools.ErrorResult("%s: %v", srv.name, err)
	}
	out := formatter{m: t.m, at: at, cache: fileLines{}}
	var text, summary string
	switch in.Operation {
	case opDefinition:
		text, summary = out.locations(parseLocations(raw), "definition", "definitions", "No definition found")
	case opReferences:
		text, summary = out.locations(parseLocations(raw), "reference", "references", "No references found")
	case opHover:
		text = parseHover(raw)
		summary = "hover"
		if text == "" {
			text, summary = "No hover information at this position.", "nothing"
		} else if r := []rune(text); len(r) > maxHoverChars {
			text = string(r[:maxHoverChars]) + "\n... (cut)"
		}
	case opDocumentSymbols:
		text, summary = out.documentSymbols(parseSymbols(raw, pathURI(path)))
	case opWorkspaceSymbols:
		text, summary = out.workspaceSymbols(parseSymbols(raw, ""))
	}
	if isEmptyResult(raw) {
		if state := srv.indexing(); state != "" {
			text += fmt.Sprintf(" The %s language server is still indexing (%s), so this may change; try again shortly.", srv.name, state)
			base["state"] = "indexing"
		}
	}
	srv.touch()
	base["summary"] = summary
	r := tools.TextResult(text, base)
	r.Summary = summary
	return r
}

// formatter writes results compactly: paths relative to the cwd, positions counted from 1 in characters.
type formatter struct {
	m      *Manager
	at     place
	cache  fileLines
	hidden int
}

// place resolves a location for the output. ok is false when a permission rule denies reading the file (the location is
// then left out and counted); inside says whether the file is in a working directory, where source lines may be shown.
func (f *formatter) place(uri string, pos position) (shown string, line, char int, preview string, ok bool) {
	path := uriPath(uri)
	if path == "" {
		return uri, pos.Line + 1, pos.Character + 1, "", true
	}
	session, inside := sessionPath(path, f.at.dirs)
	if f.m.readDenied(session) || f.m.readDenied(path) {
		f.hidden++
		return "", 0, 0, "", false
	}
	text := f.cache.line(path, pos.Line)
	if inside {
		preview = strings.TrimSpace(text)
		if r := []rune(preview); len(r) > maxPreviewRune {
			preview = string(r[:maxPreviewRune]) + "..."
		}
	}
	return displayPath(session, f.at.cwd), pos.Line + 1, charOffset(text, pos.Character) + 1, preview, true
}

func (f *formatter) hiddenNote() string {
	if f.hidden == 0 {
		return ""
	}
	return fmt.Sprintf("\n(%d in files that permission rules keep from being read are left out)", f.hidden)
}

func (f *formatter) locations(locs []location, one, many, none string) (string, string) {
	var lines []string
	for _, l := range locs {
		shown, line, char, preview, ok := f.place(l.URI, l.Range.Start)
		if !ok {
			continue
		}
		s := fmt.Sprintf("%s:%d:%d", shown, line, char)
		if preview != "" {
			s += ": " + preview
		}
		lines = append(lines, s)
	}
	if len(lines) == 0 {
		return none + "." + f.hiddenNote(), "nothing"
	}
	label := fmt.Sprintf("%d %s", len(lines), many)
	if len(lines) == 1 {
		label = "1 " + one
	}
	return capLines(lines) + f.hiddenNote(), label
}

func (f *formatter) documentSymbols(symbols []symbol) (string, string) {
	var lines []string
	var walk func(list []symbol, depth int)
	walk = func(list []symbol, depth int) {
		for _, s := range list {
			line := fmt.Sprintf("%s%s %s", strings.Repeat("  ", depth), symbolKind(s.Kind), s.Name)
			if s.Detail != "" {
				line += " " + strings.Join(strings.Fields(s.Detail), " ")
			}
			if s.Container != "" {
				line += " (in " + s.Container + ")"
			}
			line += fmt.Sprintf(" :%d", s.Loc.Range.Start.Line+1)
			if end := s.Loc.Range.End.Line; end > s.Loc.Range.Start.Line {
				line += fmt.Sprintf("-%d", end+1)
			}
			lines = append(lines, line)
			walk(s.Children, depth+1)
		}
	}
	walk(symbols, 0)
	if len(lines) == 0 {
		return "No symbols found.", "nothing"
	}
	return capLines(lines), fmt.Sprintf("%d symbols", len(lines))
}

func (f *formatter) workspaceSymbols(symbols []symbol) (string, string) {
	var lines []string
	for _, s := range symbols {
		shown, line, char, _, ok := f.place(s.Loc.URI, s.Loc.Range.Start)
		if !ok {
			continue
		}
		l := symbolKind(s.Kind) + " " + s.Name
		if s.Container != "" {
			l += " (in " + s.Container + ")"
		}
		lines = append(lines, fmt.Sprintf("%s %s:%d:%d", l, shown, line, char))
	}
	if len(lines) == 0 {
		return "No symbols found." + f.hiddenNote(), "nothing"
	}
	return capLines(lines) + f.hiddenNote(), fmt.Sprintf("%d symbols", len(lines))
}

// capLines joins lines up to the size limit and says how many it left out.
func capLines(lines []string) string {
	var b strings.Builder
	for i, l := range lines {
		if b.Len()+len(l) > maxResultBytes {
			fmt.Fprintf(&b, "... and %d more; narrow the question or read the file", len(lines)-i)
			break
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
