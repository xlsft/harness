package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	libraryv1 "github.com/xlsft/harness/packages/client/gen/harness/library/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/memory"
	"github.com/xlsft/harness/packages/client/session"
)

// writeTranscript puts a session's JSONL on this machine and returns its id.
func writeTranscript(t *testing.T, id string, lines ...string) {
	t.Helper()
	dir := filepath.Join(config.ProjectsDir(), "proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func userLine(uuid, parent, text string) string {
	p := "null"
	if parent != "" {
		p = `"` + parent + `"`
	}
	content, _ := json.Marshal(text)
	return `{"type":"user","uuid":"` + uuid + `","parentUuid":` + p + `,"sessionId":"s","timestamp":"2026-01-02T03:04:05Z","cwd":"/work","message":{"role":"user","content":` + string(content) + `}}`
}

func assistantLine(uuid, parent, text string) string {
	content, _ := json.Marshal(text)
	return `{"type":"assistant","uuid":"` + uuid + `","parentUuid":"` + parent + `","sessionId":"s","timestamp":"2026-01-02T03:04:06Z","message":{"role":"assistant","content":[{"type":"text","text":` + string(content) + `},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`
}

func toolResultLine(uuid, parent, text string) string {
	content, _ := json.Marshal(text)
	return `{"type":"user","uuid":"` + uuid + `","parentUuid":"` + parent + `","sessionId":"s","timestamp":"2026-01-02T03:04:07Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":` + string(content) + `}]}]}}`
}

func metaLine(uuid, parent, text string) string {
	content, _ := json.Marshal(text)
	return `{"type":"user","isMeta":true,"uuid":"` + uuid + `","parentUuid":"` + parent + `","sessionId":"s","timestamp":"2026-01-02T03:04:04Z","message":{"role":"user","content":` + string(content) + `}}`
}

func library(t *testing.T) *libraryService {
	t.Helper()
	t.Setenv("HARNESS_HOME", t.TempDir())
	return &libraryService{s: &Server{}}
}

func TestExportSessionLocal(t *testing.T) {
	l := library(t)
	id := "11111111-1111-4111-8111-111111111111"
	lines := []string{userLine("u1", "", "Please list the files"), metaLine("m1", "u1", "<system-reminder>private instructions</system-reminder>"), assistantLine("a1", "m1", "Listing them."), toolResultLine("r1", "a1", "main.go\n```\nweird")}
	writeTranscript(t, id, lines...)

	res, err := l.ExportSession(context.Background(), connect.NewRequest(&agentv1.ExportSessionRequest{SessionId: id, Format: agentv1.ExportFormat_EXPORT_FORMAT_JSONL}))
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Msg.GetContent()) != strings.Join(lines, "\n")+"\n" {
		t.Fatalf("JSONL is the transcript's own lines: %s", res.Msg.GetContent())
	}
	if !strings.HasSuffix(res.Msg.GetName(), ".jsonl") || res.Msg.GetMediaType() != "application/x-ndjson" {
		t.Fatalf("%v", res.Msg)
	}

	res, err = l.ExportSession(context.Background(), connect.NewRequest(&agentv1.ExportSessionRequest{SessionId: id, Format: agentv1.ExportFormat_EXPORT_FORMAT_MARKDOWN}))
	if err != nil {
		t.Fatal(err)
	}
	md := string(res.Msg.GetContent())
	for _, want := range []string{"## User\n\nPlease list the files", "## Assistant\n\nListing them.", "### Tool: Bash", `"command": "ls"`, "Session: `" + id + "`", "````\nmain.go\n```\nweird\n````"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
	if strings.Contains(md, "private instructions") {
		t.Errorf("what only the model sees stays out:\n%s", md)
	}
	if !strings.HasSuffix(res.Msg.GetName(), ".md") || res.Msg.GetMediaType() != "text/markdown" {
		t.Fatalf("%v", res.Msg)
	}

	for _, bad := range []*agentv1.ExportSessionRequest{
		{Format: agentv1.ExportFormat_EXPORT_FORMAT_JSONL},
		{SessionId: id},
		{SessionId: "22222222-2222-4222-8222-222222222222", Format: agentv1.ExportFormat_EXPORT_FORMAT_JSONL},
	} {
		if _, err := l.ExportSession(context.Background(), connect.NewRequest(bad)); err == nil {
			t.Errorf("%v should fail", bad)
		}
	}
}

// remoteSync is the server's copy of a session of another machine.
type remoteSync struct {
	SessionSync
	entries []*syncv1.TranscriptEntry
}

func (r *remoteSync) GetSessionTranscript(_ context.Context, req *syncv1.GetSessionTranscriptRequest) (*syncv1.GetSessionTranscriptResponse, error) {
	if req.GetAfterSeq() > 0 {
		return &syncv1.GetSessionTranscriptResponse{}, nil
	}
	return &syncv1.GetSessionTranscriptResponse{Entries: r.entries}, nil
}

func TestExportSessionOfAnotherMachine(t *testing.T) {
	l := library(t)
	line := userLine("u1", "", "From the other machine")
	l.s.Sync = &remoteSync{entries: []*syncv1.TranscriptEntry{{Uuid: "u1", Seq: 1, EntryJson: line}}}
	res, err := l.ExportSession(context.Background(), connect.NewRequest(&agentv1.ExportSessionRequest{SessionId: "s-remote", Format: agentv1.ExportFormat_EXPORT_FORMAT_MARKDOWN}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Msg.GetContent()), "From the other machine") {
		t.Fatalf("%s", res.Msg.GetContent())
	}
	res, err = l.ExportSession(context.Background(), connect.NewRequest(&agentv1.ExportSessionRequest{SessionId: "s-remote", Format: agentv1.ExportFormat_EXPORT_FORMAT_JSONL}))
	if err != nil || string(res.Msg.GetContent()) != line+"\n" {
		t.Fatalf("%s %v", res.Msg.GetContent(), err)
	}
	l.s.Sync = &remoteSync{}
	if _, err := l.ExportSession(context.Background(), connect.NewRequest(&agentv1.ExportSessionRequest{SessionId: "s-remote", Format: agentv1.ExportFormat_EXPORT_FORMAT_JSONL})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("%v", err)
	}
}

type fakeSearcher struct {
	hits []*libraryv1.TranscriptHit
	err  error
}

func (f *fakeSearcher) SearchTranscripts(context.Context, *libraryv1.SearchTranscriptsRequest) (*libraryv1.SearchTranscriptsResponse, error) {
	return &libraryv1.SearchTranscriptsResponse{Hits: f.hits}, f.err
}

func TestSearchTranscripts(t *testing.T) {
	l := library(t)
	one := "11111111-1111-4111-8111-111111111111"
	two := "22222222-2222-4222-8222-222222222222"
	three := "33333333-3333-4333-8333-333333333333"
	writeTranscript(t, one, userLine("u1", "", strings.Repeat("padding words ", 30)+"The KERBEROS ticket expired again"), assistantLine("a1", "u1", "kerberos in an answer"))
	writeTranscript(t, two, userLine("u2", "", "unrelated"), toolResultLine("r2", "u2", "tool said kerberos ticket too"))
	// A reminder only the model saw is not what a person searches for.
	writeTranscript(t, three, metaLine("m3", "", "kerberos ticket in a reminder"), userLine("u3", "m3", "nothing"))

	search := func(q string) []*agentv1.TranscriptHit {
		t.Helper()
		res, err := l.SearchTranscripts(context.Background(), connect.NewRequest(&agentv1.SearchTranscriptsRequest{Query: q}))
		if err != nil {
			t.Fatal(err)
		}
		return res.Msg.GetHits()
	}
	got := search("kerberos TICKET")
	ids := map[string]string{}
	for _, h := range got {
		ids[h.GetSessionId()] = h.GetSnippet()
		if h.GetRemote() {
			t.Errorf("%s is on this machine", h.GetSessionId())
		}
	}
	if len(got) != 2 || ids[one] == "" || ids[two] == "" {
		t.Fatalf("%v", got)
	}
	if !strings.Contains(ids[one], "KERBEROS ticket expired") || !strings.HasPrefix(ids[one], "…") || strings.Contains(ids[one], "\n") {
		t.Fatalf("snippet: %q", ids[one])
	}
	if hits := search("kerberos nosuchword"); len(hits) != 0 {
		t.Fatalf("all the words must match: %v", hits)
	}
	if hits := search("key\"with\\quote"); len(hits) != 0 {
		t.Fatalf("%v", hits)
	}
	if _, err := l.SearchTranscripts(context.Background(), connect.NewRequest(&agentv1.SearchTranscriptsRequest{Query: "  "})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("%v", err)
	}

	// The server adds sessions of other machines, not the ones found here.
	remote := "44444444-4444-4444-8444-444444444444"
	l.s.Transcripts = &fakeSearcher{hits: []*libraryv1.TranscriptHit{
		{SessionId: one, Title: "dup", Snippet: "server's copy"},
		{SessionId: remote, Title: "Elsewhere", Snippet: "kerberos ticket elsewhere", LastActivityAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
	}}
	got = search("kerberos ticket")
	if len(got) != 3 || got[0].GetSessionId() != remote || !got[0].GetRemote() {
		t.Fatalf("%v", got)
	}
	for _, h := range got {
		if h.GetSnippet() == "server's copy" {
			t.Fatal("this machine's copy wins")
		}
	}

	// A server that can't be reached leaves what is here.
	l.s.Transcripts = &fakeSearcher{err: errors.New("down")}
	if got := search("kerberos ticket"); len(got) != 2 {
		t.Fatalf("%v", got)
	}
}

func TestSearchFindsTextTheJSONEscapes(t *testing.T) {
	l := library(t)
	id := "11111111-1111-4111-8111-111111111111"
	writeTranscript(t, id, userLine("u1", "", `say "hello" and кириллица <tag>`))
	for _, q := range []string{`"hello"`, "кириллица", "КИРИЛЛИЦА", "<tag>"} {
		res, err := l.SearchTranscripts(context.Background(), connect.NewRequest(&agentv1.SearchTranscriptsRequest{Query: q}))
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Msg.GetHits()) != 1 {
			t.Errorf("%q: %v", q, res.Msg.GetHits())
		}
	}
}

func TestAutoMemoryRPCs(t *testing.T) {
	l := library(t)
	ctx := context.Background()
	folder := t.TempDir()
	res, err := l.ListAutoMemory(ctx, connect.NewRequest(&agentv1.ListAutoMemoryRequest{Folder: folder}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Msg.GetProjects()) != 1 || !res.Msg.GetProjects()[0].GetCurrent() || len(res.Msg.GetProjects()[0].GetFiles()) != 0 {
		t.Fatalf("the folder's project is listed even when empty: %v", res.Msg)
	}
	key := res.Msg.GetProjects()[0].GetProjectKey()
	saved, err := l.SaveAutoMemoryFile(ctx, connect.NewRequest(&agentv1.SaveAutoMemoryFileRequest{ProjectKey: key, Name: "style", Content: memory.BuildAutoFile("Style", "How to write", "feedback", "Short.")}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(saved.Msg.GetProject().GetIndex(), "- [Style](style.md) - How to write") || saved.Msg.GetProject().GetFiles()[0].GetDescription() != "How to write" {
		t.Fatalf("%v", saved.Msg)
	}
	if _, err := l.SaveAutoMemoryFile(ctx, connect.NewRequest(&agentv1.SaveAutoMemoryFileRequest{ProjectKey: key, Name: "../up", Content: "x"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("%v", err)
	}
	if _, err := l.SaveAutoMemoryFile(ctx, connect.NewRequest(&agentv1.SaveAutoMemoryFileRequest{Name: "a", Content: "x"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("%v", err)
	}
	// Another project, seen on another call without a folder.
	other := "github.com/x/y"
	if _, err := l.SaveAutoMemoryFile(ctx, connect.NewRequest(&agentv1.SaveAutoMemoryFileRequest{ProjectKey: other, Name: "a", Content: "x"})); err != nil {
		t.Fatal(err)
	}
	res, err = l.ListAutoMemory(ctx, connect.NewRequest(&agentv1.ListAutoMemoryRequest{}))
	if err != nil || len(res.Msg.GetProjects()) != 2 {
		t.Fatalf("%v %v", res, err)
	}
	del, err := l.DeleteAutoMemoryFile(ctx, connect.NewRequest(&agentv1.DeleteAutoMemoryFileRequest{ProjectKey: key, Name: "style"}))
	if err != nil || del.Msg.GetProject().GetIndex() != "" || len(del.Msg.GetProject().GetFiles()) != 0 {
		t.Fatalf("%v %v", del, err)
	}
	if _, err := l.DeleteAutoMemoryFile(ctx, connect.NewRequest(&agentv1.DeleteAutoMemoryFileRequest{ProjectKey: key, Name: "style"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("%v", err)
	}
}

func TestPreferencesCarryTheAutoMemorySwitch(t *testing.T) {
	if !preferencesOf(nil).GetAutoMemoryEnabled() {
		t.Fatal("on by default")
	}
	if preferencesOf(&syncv1.Document{Content: `{"autoMemoryEnabled":false}`}).GetAutoMemoryEnabled() {
		t.Fatal("off when the document says so")
	}
}

func TestArtifactProxyAndTickets(t *testing.T) {
	var seen *http.Request
	csp := "sandbox allow-scripts; default-src 'none'"
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		seen = r
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("Set-Cookie", "session=core")
		_, _ = io.WriteString(w, "<h1>page</h1>")
	}))
	defer core.Close()

	p := NewObjectProxy(core.URL, core.Client(), &objectTokens{})
	h := p.artifactHandler()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	ticket, _ := p.artifactTicket("art_one")
	rec := get("/artifacts/art_one?ticket=" + ticket + "&version=2&download=1&evil=1")
	if rec.Code != http.StatusOK || rec.Body.String() != "<h1>page</h1>" {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if seen.URL.Path != "/api/artifacts/art_one/content" || strings.Contains(seen.URL.RawQuery, "ticket") || strings.Contains(seen.URL.RawQuery, "evil") || !strings.Contains(seen.URL.RawQuery, "version=2") || !strings.Contains(seen.URL.RawQuery, "download=1") {
		t.Fatalf("core saw %v", seen.URL)
	}
	hdr := rec.Header()
	if hdr.Get("Content-Security-Policy") != csp || hdr.Get("X-Content-Type-Options") != "nosniff" || hdr.Get("Set-Cookie") != "" {
		t.Fatalf("%v", hdr)
	}

	// A ticket reads its artifact and nothing else: a page can see its own URL.
	if rec := get("/artifacts/art_two?ticket=" + ticket); rec.Code != http.StatusUnauthorized {
		t.Fatalf("another artifact: %d", rec.Code)
	}
	objects := httptest.NewRecorder()
	p.ServeHTTP(objects, httptest.NewRequest(http.MethodGet, "/objects/"+objectSHA+"?ticket="+ticket, nil))
	if objects.Code != http.StatusUnauthorized {
		t.Fatalf("the object store: %d", objects.Code)
	}
	general, _ := p.Ticket()
	if rec := get("/artifacts/art_one?ticket=" + general); rec.Code != http.StatusUnauthorized {
		t.Fatalf("an object ticket on an artifact: %d", rec.Code)
	}
	if rec := get("/artifacts/art_one"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no ticket: %d", rec.Code)
	}
	if rec := get("/artifacts/..%2fapi?ticket=" + ticket); rec.Code != http.StatusNotFound {
		t.Fatalf("not an id: %d", rec.Code)
	}

	// An expired ticket.
	p.now = func() time.Time { return time.Now().Add(objectTicketTTL + time.Hour) }
	if rec := get("/artifacts/art_one?ticket=" + ticket); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired: %d", rec.Code)
	}
	p.now = time.Now

	// Whatever a server answers, nothing runs with this origin.
	bare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<script>alert(1)</script>")
	}))
	defer bare.Close()
	p2 := NewObjectProxy(bare.URL, bare.Client(), &objectTokens{refreshed: 1})
	t2, _ := p2.artifactTicket("art_one")
	rec2 := httptest.NewRecorder()
	p2.artifactHandler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/artifacts/art_one?ticket="+t2, nil))
	if !strings.HasPrefix(rec2.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("%v", rec2.Header())
	}
}

func TestCreateArtifactTicket(t *testing.T) {
	l := &libraryService{s: &Server{}}
	res, err := l.CreateArtifactTicket(context.Background(), connect.NewRequest(&agentv1.CreateArtifactTicketRequest{ArtifactId: "art_a"}))
	if err != nil || res.Msg.GetAvailable() {
		t.Fatalf("without a server: %v %v", res, err)
	}
	l.s.Objects = NewObjectProxy("http://core", http.DefaultClient, &objectTokens{})
	res, err = l.CreateArtifactTicket(context.Background(), connect.NewRequest(&agentv1.CreateArtifactTicketRequest{ArtifactId: "art_a"}))
	if err != nil || !res.Msg.GetAvailable() || !l.s.Objects.validArtifactTicket("art_a", res.Msg.GetTicket()) {
		t.Fatalf("%v %v", res, err)
	}
	if _, err := l.CreateArtifactTicket(context.Background(), connect.NewRequest(&agentv1.CreateArtifactTicketRequest{ArtifactId: "../x"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("%v", err)
	}
}

// A frame cannot send the agent token, so the artifact route checks its own ticket; the rest of the library still needs it.
func TestTCPGuardLetsOnlyTheArtifactRouteThrough(t *testing.T) {
	h := TCPGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }), TCPOptions{Token: "secret", Addr: "127.0.0.1:1"})
	code := func(path string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "127.0.0.1:1"
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := code("/artifacts/art_a"); got != http.StatusTeapot {
		t.Fatalf("artifact route: %d", got)
	}
	if got := code("/harness.agent.v1.LibraryService/ListAutoMemory"); got != http.StatusUnauthorized {
		t.Fatalf("library service: %d", got)
	}
}

func TestCoreProxyForwardsTheLibraryScreens(t *testing.T) {
	for _, want := range []string{"harness.library.v1.ArtifactService", "harness.library.v1.KnowledgeService", "harness.library.v1.TrackerService"} {
		found := false
		for _, s := range coreProxyServices {
			found = found || s == want
		}
		if !found {
			t.Errorf("%s is not forwarded", want)
		}
	}
}

func TestParseEntriesSkipsWhatItCannotRead(t *testing.T) {
	got := parseEntries([]byte(userLine("u1", "", "x") + "\nnot json\n\n"))
	if len(got) != 1 || got[0].Type != session.TypeUser {
		t.Fatalf("%v", got)
	}
}
