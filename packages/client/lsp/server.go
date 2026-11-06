package lsp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// initializeWait is how long a server may take to answer initialize before it counts as failed.
	initializeWait = 2 * time.Minute
	// stopWait bounds an orderly shutdown; after it the process group is killed.
	stopWait = 2 * time.Second
	// settleAfterStart is how long after initialize an empty answer is not yet believed: progress reports may not have
	// begun.
	settleAfterStart = 2 * time.Second
	// maxDocumentBytes is the largest file sent to a server.
	maxDocumentBytes = 8 << 20
	// maxOpenDocuments bounds the documents one server holds open; the least recently used is closed.
	maxOpenDocuments = 64
	// stderrTailBytes is how much of a server's stderr is kept for its crash report.
	stderrTailBytes = 2048
)

// server is one running language server for one language root.
type server struct {
	spec    *Spec
	root    string
	name    string
	conn    *conn
	cmd     *exec.Cmd
	stderr  *tailBuffer
	started time.Time

	// ready closes when initialize finished; readyErr then says how. done closes when the process has ended.
	ready    chan struct{}
	readyErr error
	done     chan struct{}
	exitErr  error
	// stopping marks an exit the client asked for, which is no crash.
	stopping atomic.Bool
	// documents is guarded by docMu, held across sending each change so that they reach the server in order.
	docMu     sync.Mutex
	documents map[string]*document

	mu         sync.Mutex
	caps       capabilities
	readyAt    time.Time
	lastUsed   time.Time
	progress   map[string]string
	versions   map[string]int
	diags      map[string]*fileDiagnostics
	seq        uint64
	changed    chan struct{} // closed and replaced on every update of diags and progress
	collectors map[string]*collector
	reported   map[string]map[string]map[string]bool // agent, URI, key of the error
}

type document struct {
	version int
	hash    [sha256.Size]byte
	mtime   time.Time
	size    int64
	used    time.Time
}

type fileDiagnostics struct {
	items []diagnostic
	seq   uint64
}

type capabilities struct {
	definition, references, hover, documentSymbol, workspaceSymbol bool
	save                                                           bool
}

// collector is an agent's interest in the errors a server publishes for a while after an edit.
type collector struct {
	until  time.Time
	cwd    string
	dirs   []string
	edited string
	dirty  map[string]bool
}

// startServer runs bin and begins the handshake, without waiting for it.
func startServer(spec *Spec, root, name, bin string, args, environ []string) (*server, error) {
	s := &server{spec: spec, root: root, name: name, stderr: &tailBuffer{}, started: time.Now(),
		ready: make(chan struct{}), done: make(chan struct{}), documents: map[string]*document{},
		lastUsed: time.Now(), progress: map[string]string{}, versions: map[string]int{}, diags: map[string]*fileDiagnostics{},
		changed: make(chan struct{}), collectors: map[string]*collector{}, reported: map[string]map[string]map[string]bool{}}
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env, cmd.Stderr = root, environ, s.stderr
	cmd.WaitDelay = stopWait
	setProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// A pipe of our own: with exec's, Wait would close the read side or block on helpers that keep the write side open.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = stdoutW
	if err := cmd.Start(); err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return nil, err
	}
	stdoutW.Close()
	s.cmd = cmd
	s.conn = newConn(stdin, stdoutR, s.onNotify, s.onRequest)
	go func() {
		err := cmd.Wait()
		s.exitErr = err
		killGroup(cmd)
		close(s.done)
		s.conn.close()
		stdoutR.Close()
	}()
	go func() {
		// A server that closed its stdout is of no use, whatever its process does.
		<-s.conn.closed
		select {
		case <-s.done:
		case <-time.After(stopWait):
			killGroup(cmd)
		}
	}()
	go s.initialize()
	return s, nil
}

func (s *server) initialize() {
	defer close(s.ready)
	ctx, cancel := context.WithTimeout(context.Background(), initializeWait)
	defer cancel()
	params := map[string]any{
		"processId":        os.Getpid(),
		"clientInfo":       map[string]any{"name": "harness"},
		"rootUri":          pathURI(s.root),
		"rootPath":         s.root,
		"workspaceFolders": s.workspaceFolders(),
		"capabilities": map[string]any{
			"general": map[string]any{"positionEncodings": []string{"utf-16"}},
			"window":  map[string]any{"workDoneProgress": true},
			"workspace": map[string]any{
				"workspaceFolders": true,
				"configuration":    true,
				"symbol":           map[string]any{},
			},
			"textDocument": map[string]any{
				"synchronization":    map[string]any{"didSave": true},
				"hover":              map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				"definition":         map[string]any{"linkSupport": true},
				"references":         map[string]any{},
				"documentSymbol":     map[string]any{"hierarchicalDocumentSymbolSupport": true},
				"publishDiagnostics": map[string]any{"versionSupport": true},
			},
		},
	}
	if len(s.spec.InitOptions) > 0 {
		params["initializationOptions"] = s.spec.InitOptions
	}
	var res struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := s.conn.call(ctx, "initialize", params, &res); err != nil {
		s.readyErr = err
		killGroup(s.cmd)
		return
	}
	s.mu.Lock()
	s.caps = parseCapabilities(res.Capabilities)
	s.readyAt = time.Now()
	s.mu.Unlock()
	if err := s.conn.notify(ctx, "initialized", map[string]any{}); err != nil {
		s.readyErr = err
		killGroup(s.cmd)
	}
}

func (s *server) workspaceFolders() []map[string]any {
	return []map[string]any{{"uri": pathURI(s.root), "name": s.root}}
}

func parseCapabilities(raw map[string]json.RawMessage) capabilities {
	has := func(k string) bool {
		v := strings.TrimSpace(string(raw[k]))
		return v != "" && v != "false" && v != "null"
	}
	c := capabilities{definition: has("definitionProvider"), references: has("referencesProvider"), hover: has("hoverProvider"),
		documentSymbol: has("documentSymbolProvider"), workspaceSymbol: has("workspaceSymbolProvider")}
	var sync struct {
		Save json.RawMessage `json:"save"`
	}
	if json.Unmarshal(raw["textDocumentSync"], &sync) == nil {
		v := strings.TrimSpace(string(sync.Save))
		c.save = v != "" && v != "false" && v != "null"
	}
	return c
}

// onRequest answers what servers ask of a client: nothing configured, nothing registered, progress tokens accepted.
func (s *server) onRequest(method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "workspace/configuration":
		var p struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(params, &p)
		return make([]any, len(p.Items)), nil
	case "workspace/workspaceFolders":
		return s.workspaceFolders(), nil
	case "window/workDoneProgress/create", "client/registerCapability", "client/unregisterCapability", "window/showMessageRequest",
		"workspace/diagnostic/refresh", "workspace/semanticTokens/refresh", "workspace/inlayHint/refresh", "workspace/codeLens/refresh":
		return nil, nil
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: "not supported: " + method}
}

func (s *server) onNotify(method string, params json.RawMessage) {
	switch method {
	case "textDocument/publishDiagnostics":
		s.onPublish(params)
	case "$/progress":
		s.onProgress(params)
	}
}

func (s *server) onProgress(params json.RawMessage) {
	var p struct {
		Token json.RawMessage `json:"token"`
		Value struct {
			Kind    string `json:"kind"`
			Title   string `json:"title"`
			Message string `json:"message"`
		} `json:"value"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch p.Value.Kind {
	case "begin":
		title := p.Value.Title
		if title == "" {
			title = p.Value.Message
		}
		s.progress[string(p.Token)] = title
	case "end":
		delete(s.progress, string(p.Token))
	default:
		return
	}
	s.broadcastLocked()
}

func (s *server) onPublish(params json.RawMessage) {
	var p struct {
		URI         string       `json:"uri"`
		Version     *int         `json:"version"`
		Diagnostics []diagnostic `json:"diagnostics"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	path := uriPath(p.URI)
	if path == "" {
		return
	}
	uri := pathURI(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, open := s.versions[uri]; open && p.Version != nil && *p.Version < v {
		return // about a text the document no longer has
	}
	s.seq++
	s.diags[uri] = &fileDiagnostics{items: p.Diagnostics, seq: s.seq}
	current := errorKeys(p.Diagnostics)
	for _, byURI := range s.reported {
		for k := range byURI[uri] {
			if !current[k] {
				delete(byURI[uri], k) // gone: should it come back, it is news again
			}
		}
	}
	now := time.Now()
	for _, c := range s.collectors {
		if now.Before(c.until) {
			c.dirty[uri] = true
		}
	}
	s.broadcastLocked()
}

func (s *server) broadcastLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// errorKeys names each error of a list in a way that survives lines moving: two equal messages count as two.
func errorKeys(items []diagnostic) map[string]bool {
	keys := map[string]bool{}
	for _, k := range keyedErrors(items) {
		keys[k.key] = true
	}
	return keys
}

type keyedError struct {
	key  string
	item diagnostic
}

func keyedErrors(items []diagnostic) []keyedError {
	var out []keyedError
	seen := map[string]int{}
	for _, d := range items {
		if !d.isError() {
			continue
		}
		base := d.Source + "\x00" + d.code() + "\x00" + d.Message
		out = append(out, keyedError{key: fmt.Sprintf("%s\x00%d", base, seen[base]), item: d})
		seen[base]++
	}
	return out
}

func (s *server) alive() bool {
	select {
	case <-s.done:
		return false
	case <-s.conn.closed:
		return false
	default:
		return true
	}
}

func (s *server) touch() {
	s.mu.Lock()
	s.lastUsed = time.Now()
	s.mu.Unlock()
}

func (s *server) idleSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUsed
}

// waitReady waits for the handshake. The error is ctx's, errConnClosed, or the handshake's.
func (s *server) waitReady(ctx context.Context) error {
	select {
	case <-s.ready:
		return s.readyErr
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return errConnClosed
	}
}

func (s *server) capabilities() capabilities {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.caps
}

// indexing returns what the server reports working on ("" when nothing); a server still in its handshake is "starting".
func (s *server) indexing() string {
	select {
	case <-s.ready:
	default:
		return "starting"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	titles := make([]string, 0, len(s.progress))
	for _, t := range s.progress {
		titles = append(titles, t)
	}
	if len(titles) == 0 {
		return ""
	}
	sort.Strings(titles)
	if titles[0] == "" {
		return "working"
	}
	return titles[0]
}

// justStarted reports whether the handshake finished so recently that an empty answer proves nothing yet.
func (s *server) justStarted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.readyAt.IsZero() && time.Since(s.readyAt) < settleAfterStart
}

// waitIdle waits until the server is past its first moments and reports no work, or ctx ends.
func (s *server) waitIdle(ctx context.Context) {
	for s.indexing() != "" || s.justStarted() {
		s.mu.Lock()
		ch := s.changed
		s.mu.Unlock()
		tick := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ch:
		case <-tick.C:
		case <-ctx.Done():
			tick.Stop()
			return
		case <-s.done:
			tick.Stop()
			return
		}
		tick.Stop()
	}
}

// stop ends the server: shutdown and exit, then a kill after stopWait.
func (s *server) stop() {
	if !s.stopping.CompareAndSwap(false, true) {
		<-s.done
		return
	}
	select {
	case <-s.ready:
		if s.readyErr == nil {
			ctx, cancel := context.WithTimeout(context.Background(), stopWait)
			defer cancel()
			if s.conn.call(ctx, "shutdown", nil, nil) == nil {
				_ = s.conn.notify(ctx, "exit", nil)
			}
		}
	default:
	}
	select {
	case <-s.done:
	case <-time.After(stopWait):
		killGroup(s.cmd)
		<-s.done
	}
}

// failure describes how a server that is gone ended, with the end of its stderr.
func (s *server) failure() string {
	msg := "it stopped"
	select {
	case <-s.done:
		if s.exitErr != nil {
			msg = s.exitErr.Error()
		}
	default:
	}
	select {
	case <-s.ready:
		if s.readyErr != nil && !errors.Is(s.readyErr, errConnClosed) {
			msg = "initialize failed: " + s.readyErr.Error()
		}
	default:
	}
	if tail := s.stderr.String(); tail != "" {
		msg += "; its last output: " + tail
	}
	return msg
}

// syncDocument brings the server's copy of a file up to what the disk holds, and returns the file's content.
func (s *server) syncDocument(ctx context.Context, path, language string, save bool) ([]byte, error) {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > maxDocumentBytes {
		return nil, fmt.Errorf("%s is too large for the language server (%d MB at most)", path, maxDocumentBytes>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	uri := pathURI(path)
	sum := sha256.Sum256(data)
	d := s.documents[uri]
	changed := false
	switch {
	case d == nil:
		d = &document{version: 1, hash: sum}
		s.setVersion(uri, 1)
		s.documents[uri] = d
		err = s.conn.notify(ctx, "textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{"uri": uri, "languageId": language, "version": 1, "text": string(data)}})
	case d.hash != sum:
		d.version++
		d.hash = sum
		changed = true
		s.setVersion(uri, d.version)
		err = s.notifyChange(ctx, uri, d.version, data)
	}
	if err != nil {
		return nil, err
	}
	d.mtime, d.size, d.used = st.ModTime(), st.Size(), time.Now()
	if (save || changed) && s.capabilities().save {
		err = s.conn.notify(ctx, "textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri}})
	}
	if err == nil {
		err = s.closeLeastUsedLocked(ctx)
	}
	return data, err
}

func (s *server) setVersion(uri string, v int) {
	s.mu.Lock()
	s.versions[uri] = v
	s.mu.Unlock()
}

// notifyChange sends the whole text: a change without a range means that for every kind of sync.
func (s *server) notifyChange(ctx context.Context, uri string, version int, data []byte) error {
	return s.conn.notify(ctx, "textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": version},
		"contentChanges": []map[string]any{{"text": string(data)}}})
}

func (s *server) closeLeastUsedLocked(ctx context.Context) error {
	for len(s.documents) > maxOpenDocuments {
		oldest := ""
		var at time.Time
		for uri, d := range s.documents {
			if oldest == "" || d.used.Before(at) {
				oldest, at = uri, d.used
			}
		}
		if err := s.closeDocumentLocked(ctx, oldest); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) closeDocumentLocked(ctx context.Context, uri string) error {
	delete(s.documents, uri)
	s.mu.Lock()
	delete(s.versions, uri)
	s.mu.Unlock()
	return s.conn.notify(ctx, "textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uri}})
}

// refreshOpenDocuments tells the server about open files that changed on disk behind its back (Bash, the user, a
// formatter), and closes the ones that are gone.
func (s *server) refreshOpenDocuments(ctx context.Context) error {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	for uri, d := range s.documents {
		path := uriPath(uri)
		st, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				if err := s.closeDocumentLocked(ctx, uri); err != nil {
					return err
				}
			}
			continue
		}
		if st.ModTime().Equal(d.mtime) && st.Size() == d.size {
			continue
		}
		if st.Size() > maxDocumentBytes {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		d.mtime, d.size = st.ModTime(), st.Size()
		if sum := sha256.Sum256(data); sum != d.hash {
			d.hash = sum
			d.version++
			s.setVersion(uri, d.version)
			if err := s.notifyChange(ctx, uri, d.version, data); err != nil {
				return err
			}
		}
	}
	return nil
}

// request sends one request and returns its raw result.
func (s *server) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	var raw json.RawMessage
	err := s.conn.call(ctx, method, params, &raw)
	return bytes.TrimSpace(raw), err
}

// collect makes the agent's collector hear this server's publications until the given time. It returns the sequence
// number publications must exceed to be news.
func (s *server) collect(agentID, cwd string, dirs []string, edited string, until time.Time) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.collectors[agentID]
	if c == nil {
		c = &collector{dirty: map[string]bool{}}
		s.collectors[agentID] = c
	}
	c.until, c.cwd, c.dirs, c.edited = until, cwd, dirs, edited
	return s.seq
}

// waitPublished waits for a publication about uri newer than mark, then for settle more: servers often publish a file
// twice in a row (syntax first, types next).
func (s *server) waitPublished(ctx context.Context, uri string, mark uint64, settle time.Duration) {
	for {
		s.mu.Lock()
		ch := s.changed
		d := s.diags[uri]
		published := d != nil && d.seq > mark
		s.mu.Unlock()
		if published {
			break
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return
		case <-s.done:
			return
		}
	}
	select {
	case <-time.After(settle):
	case <-ctx.Done():
	case <-s.done:
	}
}

// pendingError is one error to tell an agent about.
type pendingError struct {
	path string
	item diagnostic
}

// takeErrors returns up to budget errors the agent has not been told about, in the files published since the last
// call: the edited file first. An error fixed in the meantime is not among them. more counts what did not fit; those
// errors stay untold, so a later publication of their file brings them up again. keep says whether a path may be shown.
func (s *server) takeErrors(agentID string, budget int, keep func(c *collector, path string) bool) (out []pendingError, more int, cwd string, dirs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.collectors[agentID]
	if c == nil || len(c.dirty) == 0 {
		return nil, 0, "", nil
	}
	uris := make([]string, 0, len(c.dirty))
	for uri := range c.dirty {
		uris = append(uris, uri)
	}
	sort.Slice(uris, func(i, j int) bool {
		if (uris[i] == c.edited) != (uris[j] == c.edited) {
			return uris[i] == c.edited
		}
		return uris[i] < uris[j]
	})
	c.dirty = map[string]bool{}
	byURI := s.reported[agentID]
	if byURI == nil {
		byURI = map[string]map[string]bool{}
		s.reported[agentID] = byURI
	}
	for _, uri := range uris {
		d := s.diags[uri]
		path := uriPath(uri)
		if d == nil || !keep(c, path) {
			continue
		}
		told := byURI[uri]
		if told == nil {
			told = map[string]bool{}
			byURI[uri] = told
		}
		errs := keyedErrors(d.items)
		sort.SliceStable(errs, func(i, j int) bool { return errs[i].item.Range.Start.Line < errs[j].item.Range.Start.Line })
		for _, e := range errs {
			switch {
			case told[e.key]:
			case budget <= 0:
				more++
			default:
				told[e.key] = true
				budget--
				out = append(out, pendingError{path: path, item: e.item})
			}
		}
	}
	return out, more, c.cwd, c.dirs
}

// tailBuffer keeps the end of what a server writes to stderr.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > stderrTailBytes {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-stderrTailBytes:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(bytes.ToValidUTF8(t.buf, []byte("?"))))
}
