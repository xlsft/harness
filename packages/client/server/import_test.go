package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/syncclient/synctest"
)

const (
	importIDA = "aaaaaaaa-0000-4000-8000-000000000001"
	importIDB = "bbbbbbbb-0000-4000-8000-000000000002"
)

func importLine(id, cwd, prompt string) string {
	return `{"type":"user","uuid":"` + id + `-1","parentUuid":null,"cwd":"` + cwd + `","sessionId":"` + id + `","timestamp":"2026-10-01T09:00:00Z","message":{"role":"user","content":"` + prompt + `"}}` + "\n"
}

// importFixture is a temp ~/.claude with config and two projects.
func importFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".claude")
	files := map[string]string{
		"CLAUDE.md":                           "# Me\n",
		"settings.json":                       `{"model":"opus","env":{"MY_API_KEY":"sk-secret","EDITOR":"vim"}}`,
		"agents/a.md":                         "An agent.",
		"projects/-w/" + importIDA + ".jsonl": importLine(importIDA, "/w", "first"),
		"projects/-b/" + importIDB + ".jsonl": importLine(importIDB, "/b", "second"),
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	os.Chtimes(filepath.Join(dir, "projects", "-w", importIDA+".jsonl"), old, old)
	os.WriteFile(filepath.Join(filepath.Dir(dir), ".claude.json"), []byte(`{"mcpServers":{"gh":{"url":"https://x"}}}`), 0o644)
	return dir
}

type importEnv struct {
	dir    string
	sync   *synctest.Server
	srv    *Server
	client agentv1connect.AgentServiceClient
}

func newImportEnv(t *testing.T, connected bool) *importEnv {
	t.Helper()
	t.Setenv("HARNESS_HOME", t.TempDir())
	env := &importEnv{dir: importFixture(t), sync: synctest.New()}
	env.srv = New(bootstrap.Options{})
	env.srv.DeviceID = "dev_here"
	t.Cleanup(env.srv.Close)
	if connected {
		st := env.sync.Start()
		t.Cleanup(st.Close)
		sc := syncclient.NewClient(st.URL, nil, nil)
		env.srv.ImportTo, env.srv.Sync = sc, sc
	}
	mux := http.NewServeMux()
	mux.Handle(env.srv.Handler())
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	env.client = agentv1connect.NewAgentServiceClient(hs.Client(), hs.URL)
	return env
}

func (e *importEnv) scan(t *testing.T) *agentv1.ScanClaudeImportResponse {
	t.Helper()
	res, err := e.client.ScanClaudeImport(context.Background(), connect.NewRequest(&agentv1.ScanClaudeImportRequest{ClaudeDir: proto.String(e.dir)}))
	if err != nil {
		t.Fatal(err)
	}
	return res.Msg
}

// run imports and returns the progress with the report.
func (e *importEnv) run(t *testing.T, req *agentv1.RunClaudeImportRequest) ([]*agentv1.ClaudeImportProgress, *agentv1.ClaudeImportReport) {
	t.Helper()
	req.ClaudeDir = proto.String(e.dir)
	stream, err := e.client.RunClaudeImport(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	var progress []*agentv1.ClaudeImportProgress
	var report *agentv1.ClaudeImportReport
	for stream.Receive() {
		if p := stream.Msg().GetProgress(); p != nil {
			progress = append(progress, p)
		}
		if r := stream.Msg().GetReport(); r != nil {
			report = r
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	return progress, report
}

func (e *importEnv) doc(kind syncv1.DocumentKind, key string) *syncv1.Document {
	return e.sync.Document(&syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_USER, Kind: kind, Key: key})
}

func TestScanClaudeImport(t *testing.T) {
	env := newImportEnv(t, true)
	before := config.ProjectsDir()
	m := env.scan(t)
	if !m.Found || !m.Connected || !m.ServerChecked || m.ClaudeDir != env.dir {
		t.Fatalf("scan: %v", m)
	}
	if _, err := os.Stat(before); !os.IsNotExist(err) {
		t.Fatal("a scan wrote to the projects directory")
	}
	sections := map[agentv1.ClaudeImportSection]agentv1.ClaudeImportStatus{}
	for _, it := range m.Items {
		sections[it.Section] = it.Status
	}
	if sections[agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_MEMORY] != agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_NEW ||
		sections[agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_MCP_SERVERS] != agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_MANUAL {
		t.Fatalf("sections: %v", sections)
	}
	if len(m.Projects) != 2 || m.Projects[0].Path != "/b" || m.Projects[1].Path != "/w" || m.Projects[0].SessionCount != 1 || m.Projects[0].TotalBytes == 0 || m.Projects[0].NewestAt == "" {
		t.Fatalf("projects: %v", m.Projects)
	}

	// No directory, no server.
	env.dir = filepath.Join(t.TempDir(), ".claude")
	if m := env.scan(t); m.Found {
		t.Fatalf("missing dir: %v", m)
	}
	off := newImportEnv(t, false)
	if m := off.scan(t); !m.Found || m.Connected || m.ServerChecked {
		t.Fatalf("no server: %v", m)
	}
	stream, err := off.client.RunClaudeImport(context.Background(), connect.NewRequest(&agentv1.RunClaudeImportRequest{Sections: []agentv1.ClaudeImportSection{agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_MEMORY}, ClaudeDir: proto.String(off.dir)}))
	if err == nil {
		for stream.Receive() {
		}
		err = stream.Err()
	}
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("no server: %v", err)
	}
}

func TestRunClaudeImportHonoursTheChoice(t *testing.T) {
	env := newImportEnv(t, true)
	progress, report := env.run(t, &agentv1.RunClaudeImportRequest{
		Sections:     []agentv1.ClaudeImportSection{agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_MEMORY, agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_SETTINGS},
		ProjectPaths: []string{"/w"},
	})
	if report == nil || report.Errors != 0 || report.Imported != 3 || report.Error != "" {
		t.Fatalf("report: %v", report)
	}
	last := progress[len(progress)-1]
	if last.Done != last.Total || last.Total != 3 {
		t.Fatalf("progress: %v", last)
	}
	var sawTitle bool
	for _, p := range progress {
		sawTitle = sawTitle || p.Current == "session first"
	}
	if !sawTitle {
		t.Errorf("sessions go by their titles: %v", progress)
	}
	if env.doc(syncv1.DocumentKind_DOCUMENT_KIND_MEMORY, "user") == nil || env.doc(syncv1.DocumentKind_DOCUMENT_KIND_AGENT, "a") != nil {
		t.Fatal("sections not honoured")
	}
	// The secrets rule: off by default.
	settings := env.doc(syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, "user").GetContent()
	if strings.Contains(settings, "sk-secret") || !strings.Contains(settings, "vim") {
		t.Fatalf("settings: %s", settings)
	}
	if env.sync.Session(importIDA) == nil || env.sync.Session(importIDB) != nil {
		t.Fatal("projects not honoured")
	}
	if got := env.sync.Session(importIDA).Meta.GetInfo().GetDeviceId(); got != "dev_here" {
		t.Fatalf("device: %q", got)
	}

	// It is in the session list at once, with its own time, and only once.
	list, err := env.client.ListSessions(context.Background(), connect.NewRequest(&agentv1.ListSessionsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var found *agentv1.SessionInfo
	for _, si := range list.Msg.Sessions {
		if si.Id == importIDA {
			if found != nil {
				t.Fatal("listed twice")
			}
			found = si
		}
	}
	if found == nil || found.Remote || found.Title != "first" || !strings.HasPrefix(found.UpdatedAt, "2026-03-04") {
		t.Fatalf("listed: %v", found)
	}

	// Scanning marks what came; running again changes nothing without force.
	m := env.scan(t)
	for _, p := range m.Projects {
		want := p.Path == "/w"
		if (p.ImportedCount == 1) != want || (p.Sessions[0].Status == agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_EXISTS) != want {
			t.Errorf("project after import: %v", p)
		}
	}
	for _, it := range m.Items {
		if it.Kind == "memory" && it.Status != agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_EXISTS {
			t.Errorf("memory after import: %v", it)
		}
	}
	entries := len(env.sync.Session(importIDA).Entries)
	_, report = env.run(t, &agentv1.RunClaudeImportRequest{
		Sections:     []agentv1.ClaudeImportSection{agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_MEMORY},
		ProjectPaths: []string{"/w"},
	})
	if report.Imported != 0 || report.Exists != 2 {
		t.Fatalf("second run: %v", report)
	}
	if !strings.Contains(report.Items[0].Note, importForceHint) {
		t.Errorf("hint: %q", report.Items[0].Note)
	}
	if got := len(env.sync.Session(importIDA).Entries); got != entries {
		t.Fatalf("duplicated: %d became %d", entries, got)
	}

	// Force and secrets are explicit.
	_, report = env.run(t, &agentv1.RunClaudeImportRequest{
		Sections:       []agentv1.ClaudeImportSection{agentv1.ClaudeImportSection_CLAUDE_IMPORT_SECTION_SETTINGS},
		IncludeSecrets: true, Force: true,
	})
	if report.Imported != 1 || !strings.Contains(env.doc(syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, "user").GetContent(), "sk-secret") {
		t.Fatalf("force with secrets: %v", report)
	}
}

func TestRunClaudeImportCancelAndResume(t *testing.T) {
	env := newImportEnv(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := env.client.RunClaudeImport(ctx, connect.NewRequest(&agentv1.RunClaudeImportRequest{
		ProjectPaths: []string{"/w", "/b"}, ClaudeDir: proto.String(env.dir),
	}))
	if err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
		if stream.Msg().GetReport() != nil {
			t.Fatal("a cancelled run reported as finished")
		}
		cancel() // on the first progress
	}
	if connect.CodeOf(stream.Err()) != connect.CodeCanceled {
		t.Fatalf("stream: %v", stream.Err())
	}
	// The handler ends after the call; the next run may have to wait for that.
	for i := 0; env.srv.importing.Load(); i++ {
		if i > 500 {
			t.Fatal("the run did not end")
		}
		time.Sleep(10 * time.Millisecond)
	}

	_, report := env.run(t, &agentv1.RunClaudeImportRequest{ProjectPaths: []string{"/w", "/b"}})
	if report.Errors != 0 || report.Imported+report.Exists != 2 {
		t.Fatalf("resume: %v", report)
	}
	if env.sync.Session(importIDA) == nil || env.sync.Session(importIDB) == nil {
		t.Fatal("resume did not finish")
	}
	for _, id := range []string{importIDA, importIDB} {
		if n := len(env.sync.Session(id).Entries); n != 1 {
			t.Errorf("%s has %d entries", id, n)
		}
	}
	if entries, _ := filepath.Glob(filepath.Join(config.ProjectsDir(), "*", "*.import-tmp")); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
}

func TestRunClaudeImportBySessionAndGuards(t *testing.T) {
	env := newImportEnv(t, true)
	_, report := env.run(t, &agentv1.RunClaudeImportRequest{SessionIds: []string{importIDB}})
	if report.Imported != 1 || env.sync.Session(importIDB) == nil || env.sync.Session(importIDA) != nil {
		t.Fatalf("by session: %v", report)
	}

	// Nothing chosen.
	stream, err := env.client.RunClaudeImport(context.Background(), connect.NewRequest(&agentv1.RunClaudeImportRequest{ClaudeDir: proto.String(env.dir)}))
	if err == nil {
		for stream.Receive() {
		}
		err = stream.Err()
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("nothing chosen: %v", err)
	}

	// One at a time.
	env.srv.importing.Store(true)
	stream, err = env.client.RunClaudeImport(context.Background(), connect.NewRequest(&agentv1.RunClaudeImportRequest{SessionIds: []string{importIDA}, ClaudeDir: proto.String(env.dir)}))
	if err == nil {
		for stream.Receive() {
		}
		err = stream.Err()
	}
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("second run: %v", err)
	}
	env.srv.importing.Store(false)
}

// A session the server knows from another machine is not touched, here or there.
func TestRunClaudeImportLeavesOtherMachinesSessions(t *testing.T) {
	env := newImportEnv(t, true)
	_, err := env.srv.ImportTo.UpsertSession(context.Background(), &syncv1.UpsertSessionRequest{Session: &syncv1.SessionMetadata{
		Info: &agentv1.SessionInfo{Id: importIDA, Cwd: "/w", DeviceId: "dev_other", Title: "theirs"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	m := env.scan(t)
	for _, p := range m.Projects {
		if p.Path == "/w" && (p.Sessions[0].Status != agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_SKIPPED || p.Sessions[0].Note != "on another machine") {
			t.Fatalf("scan: %v", p)
		}
	}
	_, report := env.run(t, &agentv1.RunClaudeImportRequest{ProjectPaths: []string{"/w"}})
	if report.Skipped != 1 || report.Imported != 0 {
		t.Fatalf("report: %v", report)
	}
	if got := env.sync.Session(importIDA).Meta.GetInfo(); got.GetDeviceId() != "dev_other" || got.GetTitle() != "theirs" {
		t.Fatalf("server copy changed: %v", got)
	}
	if _, err := os.Stat(filepath.Join(config.ProjectsDir(), "-w")); !os.IsNotExist(err) {
		t.Fatal("copied here")
	}

	// A session open here is left alone too.
	env.srv.mu.Lock()
	env.srv.live[importIDB] = &liveSession{}
	env.srv.mu.Unlock()
	if why := env.srv.holdSession(importIDB); why == "" {
		t.Error("an open session is not held")
	}
	env.srv.mu.Lock()
	delete(env.srv.live, importIDB)
	env.srv.mu.Unlock()
}

func TestListedTranscriptFollowsTheFile(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), importIDA+".jsonl")
	if err := os.WriteFile(p, []byte(importLine(importIDA, "/w", "before")), 0o644); err != nil {
		t.Fatal(err)
	}
	if title, _ := listedTranscript(p); title != "before" {
		t.Fatalf("title %q", title)
	}
	if err := os.WriteFile(p, []byte(importLine(importIDA, "/w", "afterwards")), 0o644); err != nil {
		t.Fatal(err)
	}
	if title, _ := listedTranscript(p); title != "afterwards" {
		t.Fatalf("title %q", title)
	}
}
