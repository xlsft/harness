package lsp

import (
	"bytes"
	"encoding/json"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

// The parts of the Language Server Protocol the client uses. Lines and characters here are the protocol's: 0-based,
// characters in UTF-16 code units (the only position encoding the client offers).

type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

type location struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

type diagnostic struct {
	Range lspRange `json:"range"`
	// Severity: 1 error, 2 warning, 3 information, 4 hint; omitted reads as an error.
	Severity int             `json:"severity"`
	Code     json.RawMessage `json:"code"`
	Source   string          `json:"source"`
	Message  string          `json:"message"`
}

func (d diagnostic) isError() bool { return d.Severity == 0 || d.Severity == 1 }

// code returns the diagnostic's code as text ("" when it has none); it may be a number or a string.
func (d diagnostic) code() string {
	s := strings.TrimSpace(string(d.Code))
	if s == "" || s == "null" {
		return ""
	}
	return strings.Trim(s, `"`)
}

// symbol is one symbol in the shape the output needs, from a DocumentSymbol tree or a flat SymbolInformation list.
type symbol struct {
	Name      string
	Detail    string
	Kind      int
	Container string
	Loc       location
	Children  []symbol
}

type rawSymbol struct {
	Name          string      `json:"name"`
	Detail        string      `json:"detail"`
	Kind          int         `json:"kind"`
	ContainerName string      `json:"containerName"`
	Range         *lspRange   `json:"range"`    // DocumentSymbol
	Location      *location   `json:"location"` // SymbolInformation, WorkspaceSymbol
	Children      []rawSymbol `json:"children"`
}

func (r rawSymbol) symbol(uri string) symbol {
	s := symbol{Name: r.Name, Detail: r.Detail, Kind: r.Kind, Container: r.ContainerName}
	switch {
	case r.Location != nil:
		s.Loc = *r.Location
	case r.Range != nil:
		s.Loc = location{URI: uri, Range: *r.Range}
	}
	for _, c := range r.Children {
		s.Children = append(s.Children, c.symbol(uri))
	}
	return s
}

func parseSymbols(raw json.RawMessage, uri string) []symbol {
	var list []rawSymbol
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	out := make([]symbol, 0, len(list))
	for _, r := range list {
		out = append(out, r.symbol(uri))
	}
	return out
}

// parseLocations reads what definition and references return: null, one Location, Locations or LocationLinks.
func parseLocations(raw json.RawMessage) []location {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] != '[' {
		raw = append(append([]byte{'['}, raw...), ']')
	}
	var items []struct {
		URI                  string   `json:"uri"`
		Range                lspRange `json:"range"`
		TargetURI            string   `json:"targetUri"`
		TargetSelectionRange lspRange `json:"targetSelectionRange"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	out := make([]location, 0, len(items))
	for _, it := range items {
		if it.TargetURI != "" {
			out = append(out, location{URI: it.TargetURI, Range: it.TargetSelectionRange})
		} else if it.URI != "" {
			out = append(out, location{URI: it.URI, Range: it.Range})
		}
	}
	return out
}

// parseHover reads Hover.contents: MarkupContent, a MarkedString or a list of MarkedStrings.
func parseHover(raw json.RawMessage) string {
	var h struct {
		Contents json.RawMessage `json:"contents"`
	}
	if json.Unmarshal(raw, &h) != nil || len(h.Contents) == 0 {
		return ""
	}
	return strings.TrimSpace(markedText(h.Contents))
}

func markedText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		parts := make([]string, 0, len(list))
		for _, x := range list {
			if t := markedText(x); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	var o struct {
		Value    string `json:"value"`
		Language string `json:"language"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return ""
	}
	if o.Language != "" {
		return "```" + o.Language + "\n" + o.Value + "\n```"
	}
	return o.Value
}

var symbolKinds = map[int]string{1: "file", 2: "module", 3: "namespace", 4: "package", 5: "class", 6: "method", 7: "property", 8: "field",
	9: "constructor", 10: "enum", 11: "interface", 12: "function", 13: "variable", 14: "constant", 15: "string", 16: "number", 17: "boolean",
	18: "array", 19: "object", 20: "key", 21: "null", 22: "enum member", 23: "struct", 24: "event", 25: "operator", 26: "type parameter"}

func symbolKind(k int) string {
	if s, ok := symbolKinds[k]; ok {
		return s
	}
	return "symbol"
}

// pathURI makes a file:// URI of an absolute path.
func pathURI(path string) string {
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// uriPath returns the path of a file:// URI ("" for any other scheme: a server's virtual documents).
func uriPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	p := u.Path
	if runtime.GOOS == "windows" {
		p = strings.TrimPrefix(p, "/")
	}
	return filepath.FromSlash(p)
}

// utf16Offset converts a 0-based count of characters in line to UTF-16 code units; past the end it stops at the end.
func utf16Offset(line string, chars int) int {
	units := 0
	for i := 0; i < len(line) && chars > 0; chars-- {
		r, size := utf8.DecodeRuneInString(line[i:])
		i += size
		units += runeUnits(r)
	}
	return units
}

// charOffset converts a UTF-16 offset in line to a 0-based count of characters.
func charOffset(line string, units int) int {
	chars := 0
	for i := 0; i < len(line) && units > 0; chars++ {
		r, size := utf8.DecodeRuneInString(line[i:])
		i += size
		units -= runeUnits(r)
	}
	return chars
}

func runeUnits(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}
