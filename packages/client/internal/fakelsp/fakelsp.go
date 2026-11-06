// Package fakelsp is a language server for tests: a test binary calls MaybeServe from TestMain and then runs itself as
// the server, with Enable's environment.
//
// Its language is plain text. "def NAME" declares a symbol, and an indented "field NAME" a member of the one before it.
// Words are identifiers. A line with "ERROR:text", "WARN:text" or "HINT:text" yields a diagnostic of that severity on
// that line; "ALSO:path:text" yields an error in another file.
package fakelsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"
)

const (
	envServe = "HARNESS_FAKE_LSP"
	envModes = "HARNESS_FAKE_LSP_MODES"
	envDelay = "HARNESS_FAKE_LSP_DIAG_DELAY_MS"
	envLog   = "HARNESS_FAKE_LSP_LOG"
)

// Modes of the server.
const (
	// CrashOnHover exits with status 3 on a hover request, after a line on stderr.
	CrashOnHover = "crash_on_hover"
	// CrashAtStart exits with status 4 before answering initialize.
	CrashAtStart = "crash_at_start"
	// IndexForever reports work begun after initialized and never answers a request.
	IndexForever = "index_forever"
	// EmptyWhileIndexing reports work for 300 ms after initialized and answers with nothing until it ends.
	EmptyWhileIndexing = "empty_while_indexing"
	// NoStdinReads stops reading after initialized, as a wedged server does.
	NoStdinReads = "no_stdin_reads"
)

// Options is what Enable passes on to the server.
type Options struct {
	Modes []string
	// DiagnosticsDelay delays each publication.
	DiagnosticsDelay time.Duration
	// Log, when set, is a file that gets one line per message received and "start <pid> <cwd>" at start.
	Log string
}

// Environ returns the environment entries that make a test binary run as this server.
func Environ(o Options) []string {
	return []string{envServe + "=1", envModes + "=" + strings.Join(o.Modes, ","), envDelay + "=" + strconv.FormatInt(o.DiagnosticsDelay.Milliseconds(), 10), envLog + "=" + o.Log}
}

// MaybeServe runs the server and exits when the environment asks for it.
func MaybeServe() {
	if os.Getenv(envServe) != "1" {
		return
	}
	delay, _ := strconv.Atoi(os.Getenv(envDelay))
	s := &server{out: os.Stdout, docs: map[string]string{}, delay: time.Duration(delay) * time.Millisecond, modes: map[string]bool{}}
	for _, m := range strings.Split(os.Getenv(envModes), ",") {
		s.modes[m] = true
	}
	if p := os.Getenv(envLog); p != "" {
		s.log, _ = os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	}
	cwd, _ := os.Getwd()
	s.record(fmt.Sprintf("start %d %s", os.Getpid(), cwd))
	if s.modes[CrashAtStart] {
		fmt.Fprintln(os.Stderr, "fake server: cannot start")
		os.Exit(4)
	}
	s.serve(bufio.NewReader(os.Stdin))
	os.Exit(0)
}

type server struct {
	out   io.Writer
	mu    sync.Mutex
	log   *os.File
	docs  map[string]string
	delay time.Duration
	modes map[string]bool

	indexingUntil time.Time
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

func (s *server) record(line string) {
	if s.log != nil {
		fmt.Fprintln(s.log, line)
	}
}

func (s *server) send(v map[string]any) {
	v["jsonrpc"] = "2.0"
	body, _ := json.Marshal(v)
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

func (s *server) serve(in *bufio.Reader) {
	for {
		body, err := readFrame(in)
		if err != nil {
			return
		}
		var m message
		if json.Unmarshal(body, &m) != nil {
			continue
		}
		s.record(m.Method + " " + string(body))
		if m.Method == "" {
			continue // the answer to one of our requests
		}
		if m.Method == "exit" {
			os.Exit(0)
		}
		if m.Method == "initialized" {
			s.onInitialized()
			if s.modes[NoStdinReads] {
				time.Sleep(time.Hour)
			}
			continue
		}
		if len(m.ID) == 0 {
			s.onNotify(m)
			continue
		}
		if s.modes[IndexForever] && m.Method != "initialize" && m.Method != "shutdown" {
			continue
		}
		s.send(map[string]any{"id": m.ID, "result": s.answer(m)})
	}
}

func readFrame(in *bufio.Reader) ([]byte, error) {
	size := 0
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Content-Length:"); ok {
			size, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	body := make([]byte, size)
	_, err := io.ReadFull(in, body)
	return body, err
}

func (s *server) onInitialized() {
	// What real servers do at this point: ask for their configuration and take a progress token.
	s.send(map[string]any{"id": 9001, "method": "workspace/configuration", "params": map[string]any{"items": []any{map[string]any{"section": "fake"}}}})
	s.send(map[string]any{"id": 9002, "method": "window/workDoneProgress/create", "params": map[string]any{"token": "index"}})
	switch {
	case s.modes[IndexForever]:
		s.progress("begin", "Indexing the fake project")
	case s.modes[EmptyWhileIndexing]:
		s.indexingUntil = time.Now().Add(300 * time.Millisecond)
		s.progress("begin", "Indexing the fake project")
		time.AfterFunc(300*time.Millisecond, func() { s.progress("end", "") })
	}
}

func (s *server) progress(kind, title string) {
	s.send(map[string]any{"method": "$/progress", "params": map[string]any{"token": "index", "value": map[string]any{"kind": kind, "title": title}}})
}

func (s *server) onNotify(m message) {
	var p struct {
		TextDocument struct {
			URI     string `json:"uri"`
			Text    string `json:"text"`
			Version int    `json:"version"`
		} `json:"textDocument"`
		ContentChanges []struct {
			Text string `json:"text"`
		} `json:"contentChanges"`
	}
	_ = json.Unmarshal(m.Params, &p)
	uri, version := p.TextDocument.URI, p.TextDocument.Version
	switch m.Method {
	case "textDocument/didOpen":
		s.docs[uri] = p.TextDocument.Text
	case "textDocument/didChange":
		s.docs[uri] = p.ContentChanges[len(p.ContentChanges)-1].Text
	case "textDocument/didClose":
		delete(s.docs, uri)
		return
	default:
		return
	}
	text := s.docs[uri]
	go func() {
		time.Sleep(s.delay)
		s.publish(uri, version, text)
	}()
}

func (s *server) publish(uri string, version int, text string) {
	diags := []map[string]any{}
	others := map[string][]map[string]any{}
	for i, line := range strings.Split(text, "\n") {
		for tag, severity := range map[string]int{"ERROR:": 1, "WARN:": 2, "HINT:": 4} {
			if _, msg, ok := strings.Cut(line, tag); ok {
				at := strings.Index(line, tag)
				diags = append(diags, map[string]any{"range": rangeAt(i, units(line[:at]), len(tag)), "severity": severity, "source": "fake", "code": "E1", "message": msg})
			}
		}
		if _, rest, ok := strings.Cut(line, "ALSO:"); ok {
			if path, msg, ok := strings.Cut(rest, ":"); ok {
				other := (&url.URL{Scheme: "file", Path: path}).String()
				others[other] = append(others[other], map[string]any{"range": rangeAt(0, 0, 1), "severity": 1, "source": "fake", "message": msg})
			}
		}
	}
	s.send(map[string]any{"method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": uri, "version": version, "diagnostics": diags}})
	for other, list := range others {
		s.send(map[string]any{"method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": other, "diagnostics": list}})
	}
}

func rangeAt(line, char, length int) map[string]any {
	return map[string]any{"start": map[string]any{"line": line, "character": char}, "end": map[string]any{"line": line, "character": char + length}}
}

func units(s string) int { return len(utf16.Encode([]rune(s))) }

func (s *server) answer(m message) any {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"position"`
		Query string `json:"query"`
	}
	_ = json.Unmarshal(m.Params, &p)
	if m.Method == "initialize" {
		return map[string]any{"capabilities": map[string]any{
			"textDocumentSync":   map[string]any{"openClose": true, "change": 1, "save": true},
			"definitionProvider": true, "referencesProvider": true, "hoverProvider": true,
			"documentSymbolProvider": true, "workspaceSymbolProvider": true,
		}}
	}
	if m.Method == "shutdown" {
		return nil
	}
	if time.Now().Before(s.indexingUntil) {
		return nil
	}
	uri := p.TextDocument.URI
	word := wordAt(s.docs[uri], p.Position.Line, p.Position.Character)
	switch m.Method {
	case "textDocument/definition":
		locs := occurrences(uri, s.docs[uri], "def "+word, len("def "))
		if len(locs) == 0 {
			return nil
		}
		return locs[0]
	case "textDocument/references":
		return occurrences(uri, s.docs[uri], word, 0)
	case "textDocument/hover":
		if s.modes[CrashOnHover] {
			fmt.Fprintln(os.Stderr, "fake server: hover is broken")
			os.Exit(3)
		}
		if word == "" {
			return nil
		}
		return map[string]any{"contents": map[string]any{"kind": "markdown", "value": "**" + word + "** is a fake symbol"}}
	case "textDocument/documentSymbol":
		return documentSymbols(s.docs[uri])
	case "workspace/symbol":
		var out []map[string]any
		for docURI, text := range s.docs {
			for i, line := range strings.Split(text, "\n") {
				if name, ok := strings.CutPrefix(line, "def "); ok && strings.Contains(name, p.Query) {
					out = append(out, map[string]any{"name": name, "kind": 12, "containerName": "fake", "location": map[string]any{"uri": docURI, "range": rangeAt(i, 4, len(name))}})
				}
			}
		}
		return out
	}
	return nil
}

// wordAt finds the identifier at a position given in UTF-16 units.
func wordAt(text string, line, char int) string {
	lines := strings.Split(text, "\n")
	if line >= len(lines) {
		return ""
	}
	u := utf16.Encode([]rune(lines[line]))
	isWord := func(i int) bool {
		return i >= 0 && i < len(u) && (unicode.IsLetter(rune(u[i])) || unicode.IsDigit(rune(u[i])) || u[i] == '_')
	}
	if !isWord(char) {
		return ""
	}
	from, to := char, char
	for isWord(from - 1) {
		from--
	}
	for isWord(to + 1) {
		to++
	}
	return string(utf16.Decode(u[from : to+1]))
}

func occurrences(uri, text, needle string, skip int) []map[string]any {
	out := []map[string]any{}
	if strings.TrimSpace(needle) == "" {
		return out
	}
	for i, line := range strings.Split(text, "\n") {
		for from := 0; ; {
			at := strings.Index(line[from:], needle)
			if at < 0 {
				break
			}
			at += from
			out = append(out, map[string]any{"uri": uri, "range": rangeAt(i, units(line[:at])+skip, len(needle)-skip)})
			from = at + len(needle)
		}
	}
	return out
}

func documentSymbols(text string) []map[string]any {
	out := []map[string]any{}
	for i, line := range strings.Split(text, "\n") {
		if name, ok := strings.CutPrefix(line, "def "); ok {
			out = append(out, map[string]any{"name": name, "kind": 12, "detail": "fake", "range": rangeAt(i, 0, len(line)), "selectionRange": rangeAt(i, 4, len(name)), "children": []map[string]any{}})
		} else if name, ok := strings.CutPrefix(strings.TrimSpace(line), "field "); ok && len(out) > 0 {
			last := out[len(out)-1]
			last["children"] = append(last["children"].([]map[string]any), map[string]any{"name": name, "kind": 8, "range": rangeAt(i, 0, len(line)), "selectionRange": rangeAt(i, 0, len(line))})
			last["range"] = map[string]any{"start": last["range"].(map[string]any)["start"], "end": map[string]any{"line": i, "character": len(line)}}
		}
	}
	return out
}
