package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	libraryv1 "github.com/xlsft/harness/packages/client/gen/harness/library/v1"
	"github.com/xlsft/harness/packages/client/memory"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/workspace"
)

// LibraryService: memory, export, search and artifact tickets (proto/harness/agent/v1/library.proto).

const (
	// ArtifactProxyPath is where frontends read an artifact: GET /artifacts/<id>?ticket=...
	ArtifactProxyPath = "/artifacts/"
	// libraryCallTimeout bounds the server's part of a library call: the screen must still show what is on this machine.
	libraryCallTimeout = 8 * time.Second
	// How much of this machine's transcripts one search reads.
	transcriptSearchFiles = 2000
	transcriptSearchBytes = 1 << 30
	transcriptSearchLimit = 100
	transcriptLineMax     = 64 << 20
	snippetContext        = 90
)

// TranscriptSearcher is the server's full-text search over uploaded transcripts (implemented by *syncclient.Client).
type TranscriptSearcher interface {
	SearchTranscripts(ctx context.Context, req *libraryv1.SearchTranscriptsRequest) (*libraryv1.SearchTranscriptsResponse, error)
}

// libraryService implements agentv1connect.LibraryServiceHandler.
type libraryService struct {
	agentv1connect.UnimplementedLibraryServiceHandler
	s *Server
}

func (s *Server) registerLibrary(mux *http.ServeMux) {
	path, h := agentv1connect.NewLibraryServiceHandler(&libraryService{s: s})
	mux.Handle(path, h)
}

// ---- auto memory ----

func (s *Server) autoStore(key string) *memory.AutoStore {
	return &memory.AutoStore{Dir: memory.AutoMemoryDir(config.Home(), key), ProjectKey: key, Docs: s.AutoMemory}
}

func autoProjectProto(st *memory.AutoStore, current bool) *agentv1.AutoMemoryProject {
	p := &agentv1.AutoMemoryProject{ProjectKey: st.ProjectKey, Current: current, Index: st.Index()}
	for _, f := range st.List() {
		p.Files = append(p.Files, &agentv1.AutoMemoryFile{Name: f.Name, Description: f.Description, Content: f.Content})
	}
	return p
}

func (l *libraryService) ListAutoMemory(ctx context.Context, req *connect.Request[agentv1.ListAutoMemoryRequest]) (*connect.Response[agentv1.ListAutoMemoryResponse], error) {
	s := l.s
	current := ""
	if f := req.Msg.GetFolder(); f != "" {
		current = workspace.ProjectKey(ctx, f)
	}
	seen := map[string]bool{}
	for _, k := range memory.LocalAutoMemoryProjects(config.Home()) {
		seen[k] = true
	}
	ctx, cancel := context.WithTimeout(ctx, libraryCallTimeout)
	defer cancel()
	if s.AutoMemory != nil {
		// Without the server the screen still shows this machine's copy.
		if keys, err := s.AutoMemory.Projects(ctx); err == nil {
			for _, k := range keys {
				seen[k] = true
			}
		}
	}
	if current != "" {
		seen[current] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if (keys[i] == current) != (keys[j] == current) {
			return keys[i] == current
		}
		return keys[i] < keys[j]
	})
	res := &agentv1.ListAutoMemoryResponse{}
	for _, k := range keys {
		st := s.autoStore(k)
		_ = st.Sync(ctx)
		res.Projects = append(res.Projects, autoProjectProto(st, k == current))
	}
	return connect.NewResponse(res), nil
}

func (l *libraryService) SaveAutoMemoryFile(ctx context.Context, req *connect.Request[agentv1.SaveAutoMemoryFileRequest]) (*connect.Response[agentv1.SaveAutoMemoryFileResponse], error) {
	if req.Msg.GetProjectKey() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_key is required"))
	}
	st := l.s.autoStore(req.Msg.GetProjectKey())
	ctx, cancel := context.WithTimeout(ctx, libraryCallTimeout)
	defer cancel()
	if _, err := st.Write(ctx, req.Msg.GetName(), req.Msg.GetContent()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentv1.SaveAutoMemoryFileResponse{Project: autoProjectProto(st, false)}), nil
}

func (l *libraryService) DeleteAutoMemoryFile(ctx context.Context, req *connect.Request[agentv1.DeleteAutoMemoryFileRequest]) (*connect.Response[agentv1.DeleteAutoMemoryFileResponse], error) {
	if req.Msg.GetProjectKey() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_key is required"))
	}
	st := l.s.autoStore(req.Msg.GetProjectKey())
	ctx, cancel := context.WithTimeout(ctx, libraryCallTimeout)
	defer cancel()
	if err := st.Delete(ctx, req.Msg.GetName()); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&agentv1.DeleteAutoMemoryFileResponse{Project: autoProjectProto(st, false)}), nil
}

// ---- export ----

var exportNameRe = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func exportFileName(title, id, ext string) string {
	base := strings.Trim(exportNameRe.ReplaceAllString(title, "-"), "-")
	if r := []rune(base); len(r) > 60 {
		base = strings.Trim(string(r[:60]), "-")
	}
	if base == "" {
		base = "session-" + id
	}
	return base + "." + ext
}

func (l *libraryService) ExportSession(ctx context.Context, req *connect.Request[agentv1.ExportSessionRequest]) (*connect.Response[agentv1.ExportSessionResponse], error) {
	id := req.Msg.GetSessionId()
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id is required"))
	}
	format := req.Msg.GetFormat()
	if format != agentv1.ExportFormat_EXPORT_FORMAT_MARKDOWN && format != agentv1.ExportFormat_EXPORT_FORMAT_JSONL {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("format must be Markdown or JSONL"))
	}
	raw, title, cwd, err := l.s.transcriptBytes(ctx, id)
	if err != nil {
		return nil, err
	}
	if format == agentv1.ExportFormat_EXPORT_FORMAT_JSONL {
		return connect.NewResponse(&agentv1.ExportSessionResponse{Name: exportFileName(title, id, "jsonl"), MediaType: "application/x-ndjson", Content: raw}), nil
	}
	entries := parseEntries(raw)
	return connect.NewResponse(&agentv1.ExportSessionResponse{
		Name: exportFileName(title, id, "md"), MediaType: "text/markdown",
		Content: session.RenderMarkdown(entries, session.ExportMeta{ID: id, Title: title, Cwd: cwd}),
	}), nil
}

// transcriptBytes reads a session's transcript: from this machine, else the copy on the server.
func (s *Server) transcriptBytes(ctx context.Context, id string) (raw []byte, title, cwd string, err error) {
	if path, findErr := (session.Store{Root: config.ProjectsDir()}).Find("", id); findErr == nil {
		raw, err = os.ReadFile(path)
		if err != nil {
			return nil, "", "", connect.NewError(connect.CodeInternal, err)
		}
		title, _ = listedTranscript(path)
		return raw, title, "", nil
	}
	if s.Sync == nil {
		return nil, "", "", connect.NewError(connect.CodeNotFound, errors.New("no such session on this machine"))
	}
	lines, err := s.remoteThread(ctx, id, "")
	if err != nil {
		return nil, "", "", err
	}
	if len(lines) == 0 {
		return nil, "", "", connect.NewError(connect.CodeNotFound, errors.New("no such session"))
	}
	var b bytes.Buffer
	for _, e := range lines {
		b.WriteString(e.GetEntryJson())
		b.WriteByte('\n')
	}
	return b.Bytes(), "", "", nil
}

// parseEntries reads JSONL, skipping lines that do not parse (a line being written, a kind this version doesn't know).
func parseEntries(raw []byte) []session.Entry {
	var out []session.Entry
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e session.Entry
		if json.Unmarshal(line, &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// ---- transcript search ----

func (l *libraryService) SearchTranscripts(ctx context.Context, req *connect.Request[agentv1.SearchTranscriptsRequest]) (*connect.Response[agentv1.SearchTranscriptsResponse], error) {
	query := strings.TrimSpace(req.Msg.GetQuery())
	if query == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("query is required"))
	}
	limit := int(req.Msg.GetLimit())
	if limit <= 0 {
		limit = 30
	}
	limit = min(limit, transcriptSearchLimit)

	hits := searchLocalTranscripts(ctx, query, limit)
	seen := map[string]bool{}
	for _, h := range hits {
		seen[h.GetSessionId()] = true
	}
	if t := l.s.Transcripts; t != nil {
		sctx, cancel := context.WithTimeout(ctx, libraryCallTimeout)
		defer cancel()
		// A server that cannot be reached leaves what this machine found.
		if res, err := t.SearchTranscripts(sctx, &libraryv1.SearchTranscriptsRequest{Query: query, Limit: int32(limit)}); err == nil {
			for _, h := range res.GetHits() {
				if seen[h.GetSessionId()] {
					continue
				}
				_, findErr := session.Store{Root: config.ProjectsDir()}.Find("", h.GetSessionId())
				hits = append(hits, &agentv1.TranscriptHit{SessionId: h.GetSessionId(), Title: h.GetTitle(), Snippet: h.GetSnippet(), LastActivityAt: h.GetLastActivityAt(), Remote: findErr != nil})
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].GetLastActivityAt() > hits[j].GetLastActivityAt() })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return connect.NewResponse(&agentv1.SearchTranscriptsResponse{Hits: hits}), nil
}

// searchWords lowers a query into the words an entry must all contain.
func searchWords(query string) [][]byte {
	var out [][]byte
	for _, w := range strings.Fields(strings.ToLower(query)) {
		out = append(out, []byte(w))
	}
	return out
}

func containsAll(text []byte, words [][]byte) bool {
	for _, w := range words {
		if !bytes.Contains(text, w) {
			return false
		}
	}
	return true
}

// searchLocalTranscripts reads this machine's transcripts, newest first, for at most one hit per session. A line is
// parsed only if its raw bytes could match, which makes the common case cheap: the raw test may be wrong for text the
// JSON escapes, so words with such characters are looked for in the parsed text alone.
func searchLocalTranscripts(ctx context.Context, query string, limit int) []*agentv1.TranscriptHit {
	words := searchWords(query)
	if len(words) == 0 {
		return nil
	}
	var rawWords [][]byte
	for _, w := range words {
		if json.Valid(append(append([]byte{'"'}, w...), '"')) && !bytes.ContainsAny(w, "<>&") {
			rawWords = append(rawWords, w)
		}
	}
	var out []*agentv1.TranscriptHit
	budget := int64(transcriptSearchBytes)
	for _, f := range recentTranscripts(transcriptSearchFiles) {
		if len(out) >= limit || budget <= 0 || ctx.Err() != nil {
			break
		}
		snippet, read := searchTranscriptFile(f.path, words, rawWords)
		budget -= read
		if snippet == "" {
			continue
		}
		title, _ := listedTranscript(f.path)
		out = append(out, &agentv1.TranscriptHit{
			SessionId: strings.TrimSuffix(filepath.Base(f.path), ".jsonl"), Title: title, Snippet: snippet,
			LastActivityAt: f.mod.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// searchTranscriptFile returns the first matching entry's snippet, and how many bytes it read.
func searchTranscriptFile(path string, words, rawWords [][]byte) (string, int64) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0
	}
	defer f.Close()
	var read int64
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		read += int64(len(line))
		if len(line) > 0 && len(line) <= transcriptLineMax && containsAll(bytes.ToLower(line), rawWords) {
			var e session.Entry
			if json.Unmarshal(line, &e) == nil {
				text := session.EntryText(&e)
				if lower := []byte(strings.ToLower(text)); len(lower) > 0 && containsAll(lower, words) {
					return transcriptSnippet(text, string(words[0])), read
				}
			}
		}
		if err != nil {
			return "", read
		}
	}
}

// transcriptSnippet is the text around word, on one line.
func transcriptSnippet(text, word string) string {
	at := strings.Index(strings.ToLower(text), word)
	if at < 0 || len(strings.ToLower(text)) != len(text) {
		// Lowering changed the offsets, or the word is elsewhere: the start of the text will do.
		at = 0
	}
	start := max(at-snippetContext, 0)
	end := min(at+2*snippetContext, len(text))
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	out := strings.Join(strings.Fields(text[start:end]), " ")
	if start > 0 {
		out = "…" + out
	}
	if end < len(text) {
		out += "…"
	}
	return out
}

// ---- artifact tickets ----

func (l *libraryService) CreateArtifactTicket(_ context.Context, req *connect.Request[agentv1.CreateArtifactTicketRequest]) (*connect.Response[agentv1.CreateArtifactTicketResponse], error) {
	p := l.s.Objects
	if p == nil {
		return connect.NewResponse(&agentv1.CreateArtifactTicketResponse{}), nil
	}
	if !artifactIDRe.MatchString(req.Msg.GetArtifactId()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("not an artifact id"))
	}
	t, at := p.artifactTicket(req.Msg.GetArtifactId())
	return connect.NewResponse(&agentv1.CreateArtifactTicketResponse{Available: true, Ticket: t, ExpiresAt: at.UTC().Format(time.RFC3339)}), nil
}

var artifactIDRe = regexp.MustCompile(`^art_[A-Za-z0-9]{1,64}$`)

func (p *ObjectProxy) artifactTicket(id string) (string, time.Time) {
	at := p.now().Add(objectTicketTTL)
	exp := strconv.FormatInt(at.Unix(), 10)
	return exp + "." + p.signArtifact(id, exp), at
}

func (p *ObjectProxy) validArtifactTicket(id, t string) bool {
	exp, sig, ok := strings.Cut(t, ".")
	if !ok || !hmacEqual(sig, p.signArtifact(id, exp)) {
		return false
	}
	at, err := strconv.ParseInt(exp, 10, 64)
	return err == nil && p.now().Unix() < at
}

// artifactHandler serves GET /artifacts/<id>?ticket=...[&version=N][&download=1]: core's artifact route with this
// client's credentials. A ticket reads that artifact and nothing else, since a page in a frame can read its own URL.
func (p *ObjectProxy) artifactHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, ArtifactProxyPath)
		if !artifactIDRe.MatchString(id) {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if !p.validArtifactTicket(id, q.Get("ticket")) {
			http.Error(w, "missing or expired ticket", http.StatusUnauthorized)
			return
		}
		up := url.Values{}
		if v, err := strconv.Atoi(q.Get("version")); err == nil && v > 0 {
			up.Set("version", strconv.Itoa(v))
		}
		if q.Get("download") == "1" {
			up.Set("download", "1")
		}
		target := p.url + "/api/artifacts/" + id + "/content"
		if len(up) > 0 {
			target += "?" + up.Encode()
		}
		ctx := r.Context()
		send := func(token string) (*http.Response, error) {
			req, err := http.NewRequestWithContext(ctx, r.Method, target, nil)
			if err != nil {
				return nil, err
			}
			if v := r.Header.Get("If-None-Match"); v != "" {
				req.Header.Set("If-None-Match", v)
			}
			req.Header.Set("User-Agent", config.UserAgent())
			rpc.SetBearer(req.Header, token)
			return p.http.Do(req)
		}
		token, err := p.tokens.Token(ctx)
		if err != nil {
			http.Error(w, "not signed in to the server", http.StatusUnauthorized)
			return
		}
		res, err := send(token)
		if err == nil && res.StatusCode == http.StatusUnauthorized {
			res.Body.Close()
			if token, err = p.tokens.Refresh(ctx); err == nil {
				res, err = send(token)
			}
		}
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				http.Error(w, "harness server is unreachable", http.StatusBadGateway)
			}
			return
		}
		defer res.Body.Close()
		for _, h := range objectResponseHeaders {
			if v := res.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		// Whoever answered, nothing from here runs with this origin. A policy from core is at least this strict.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !strings.HasPrefix(w.Header().Get("Content-Security-Policy"), "sandbox") {
			w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		}
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
	})
}
