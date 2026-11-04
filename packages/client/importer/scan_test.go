package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/syncclient/synctest"
)

const (
	sessionA = "aaaaaaaa-0000-4000-8000-000000000001"
	sessionB = "bbbbbbbb-0000-4000-8000-000000000002"
)

func transcriptLine(id, cwd, prompt string) string {
	return fmt.Sprintf(`{"type":"user","uuid":"%s-1","parentUuid":null,"cwd":%q,"sessionId":"%s","timestamp":"2026-10-01T09:00:00Z","message":{"role":"user","content":%q}}`+"\n", id, cwd, id, prompt)
}

// twoProjects adds a second project to the fixture and returns its directories.
func twoProjects(t *testing.T) (ch, cj, hh string) {
	t.Helper()
	ch, cj, hh = fakeHome(t)
	p := filepath.Join(ch, "projects", "-b")
	os.MkdirAll(p, 0o755)
	if err := os.WriteFile(filepath.Join(p, sessionB+".jsonl"), []byte(transcriptLine(sessionB, "/b", "second project")), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(filepath.Join(p, sessionB+".jsonl"), old, old)
	return ch, cj, hh
}

// snapshot is every path under dirs with its size and time.
func snapshot(t *testing.T, dirs ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			out[p] = fmt.Sprint(info.Size(), info.ModTime().UnixNano())
			return nil
		})
	}
	return out
}

func TestScanWritesNothingAndGroupsSessions(t *testing.T) {
	ch, cj, hh := twoProjects(t)
	before := snapshot(t, ch, hh, cj)
	res, err := Scan(context.Background(), ScanOptions{ClaudeHome: ch, ClaudeJSON: cj, ProjectsDir: filepath.Join(hh, "projects")})
	if err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, ch, hh, cj)
	if len(before) != len(after) {
		t.Fatalf("scan wrote: %d paths became %d", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Errorf("scan changed %s", p)
		}
	}
	if !res.Found || res.ServerChecked {
		t.Fatalf("found %v, checked %v", res.Found, res.ServerChecked)
	}
	var mem, mcp, broken bool
	for _, it := range res.Items {
		mem = mem || (it.Kind == "memory" && it.Status == StatusPlanned)
		mcp = mcp || (it.Kind == "mcp" && it.Status == StatusNoted)
		broken = broken || (it.Key == "broken" && it.Status == StatusSkipped)
	}
	if !mem || !mcp || !broken {
		t.Fatalf("items: %+v", res.Items)
	}
	if len(res.Projects) != 2 {
		t.Fatalf("projects: %+v", res.Projects)
	}
	w, b := res.Projects[0], res.Projects[1] // newest first; -b is dated long ago
	if w.Folder != "-w" || b.Folder != "-b" {
		t.Fatalf("order: %s, %s", w.Folder, b.Folder)
	}
	if w.Path != "/w" || len(w.Sessions) != 1 || w.Sessions[0].ID != sessionID || w.Sessions[0].Title != "hello" {
		t.Fatalf("project -w: %+v", w)
	}
	// The size counts the subagent transcript, which is no session of its own.
	st, _ := os.Stat(filepath.Join(ch, "projects", "-w", sessionID+".jsonl"))
	if w.Bytes <= st.Size() || w.Sessions[0].Bytes != w.Bytes {
		t.Fatalf("bytes: %d, session %d, file %d", w.Bytes, w.Sessions[0].Bytes, st.Size())
	}
	if w.Sessions[0].Status != StatusPlanned || w.Imported != 0 {
		t.Fatalf("new session: %+v", w)
	}

	missing, err := Scan(context.Background(), ScanOptions{ClaudeHome: filepath.Join(hh, "nowhere")})
	if err != nil || missing.Found || len(missing.Projects) != 0 {
		t.Fatalf("missing dir: %+v %v", missing, err)
	}
}

func TestScanAsksTheServerWhichDocumentsExist(t *testing.T) {
	ch, cj, hh := fakeHome(t)
	exists := func(_ context.Context, ref *syncv1.DocumentRef) (bool, error) {
		return ref.GetKind() == syncv1.DocumentKind_DOCUMENT_KIND_MEMORY, nil
	}
	res, err := Scan(context.Background(), ScanOptions{ClaudeHome: ch, ClaudeJSON: cj, ProjectsDir: filepath.Join(hh, "projects"), Exists: exists})
	if err != nil {
		t.Fatal(err)
	}
	if !res.ServerChecked {
		t.Fatal("not checked")
	}
	for _, it := range res.Items {
		switch {
		case it.Kind == "memory" && it.Status != StatusExists:
			t.Errorf("memory: %+v", it)
		case it.Kind == "agent" && it.Status != StatusPlanned:
			t.Errorf("agent: %+v", it)
		}
	}
	// A server that does not answer leaves them new, and says so.
	res, err = Scan(context.Background(), ScanOptions{ClaudeHome: ch, ClaudeJSON: cj, Exists: func(context.Context, *syncv1.DocumentRef) (bool, error) {
		return false, errors.New("unreachable")
	}})
	if err != nil || res.ServerChecked {
		t.Fatalf("unreachable: checked %v, %v", res.ServerChecked, err)
	}
}

func TestTranscriptHead(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) string {
		p := filepath.Join(dir, "t.jsonl")
		os.WriteFile(p, []byte(content), 0o644)
		return p
	}
	huge := `{"type":"user","message":{"content":"` + strings.Repeat("x", 200<<10) + `"}}` + "\n"
	cases := []struct{ name, content, cwd, title string }{
		{"plain", transcriptLine("x", "/p", "fix  the\nbug"), "/p", "fix the bug"},
		{"a long line is passed over", huge + transcriptLine("x", "/p", "after"), "/p", "after"},
		{"summary first", `{"type":"summary","summary":"A summary"}` + "\n" + transcriptLine("x", "/p", "ignored"), "/p", "A summary"},
		{"custom title wins", `{"type":"ai-title","aiTitle":"AI"}` + "\n" + `{"type":"custom-title","customTitle":"Mine"}` + "\n" + transcriptLine("x", "/p", "p"), "/p", "Mine"},
		{"commands are no prompt", transcriptLine("x", "/p", "<command-name>/x</command-name>") + transcriptLine("x", "/p", "real"), "/p", "real"},
		{"blocks", `{"type":"user","cwd":"/p","message":{"content":[{"type":"image"},{"type":"text","text":"from a block"}]}}` + "\n", "/p", "from a block"},
		{"no newline at the end", strings.TrimSuffix(transcriptLine("x", "/p", "last"), "\n"), "/p", "last"},
		{"junk", "not json\n", "", ""},
	}
	for _, c := range cases {
		cwd, title := transcriptHead(write(c.content))
		if cwd != c.cwd || title != c.title {
			t.Errorf("%s: %q, %q", c.name, cwd, title)
		}
	}
}

// The real case behind the head-only rule: 151 projects, 289 transcripts, 2.1 GB (sparse files here).
func TestScanLargeDirectory(t *testing.T) {
	root := t.TempDir()
	ch := filepath.Join(root, ".claude")
	const projects, sessions, size = 151, 289, int64(2100<<20) / 289
	for i := range sessions {
		dir := filepath.Join(ch, "projects", fmt.Sprintf("-p%d", i%projects))
		os.MkdirAll(dir, 0o755)
		id := fmt.Sprintf("%08x-0000-4000-8000-000000000000", i)
		p := filepath.Join(dir, id+".jsonl")
		if err := os.WriteFile(p, []byte(transcriptLine(id, fmt.Sprintf("/p%d", i%projects), "hi")), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(p, size); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	res, err := Scan(context.Background(), ScanOptions{ClaudeHome: ch, ProjectsDir: filepath.Join(root, "projects")})
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	t.Logf("scanned in %s", took)
	if len(res.Projects) != projects {
		t.Fatalf("%d projects", len(res.Projects))
	}
	var count int
	var total int64
	for _, p := range res.Projects {
		count += len(p.Sessions)
		total += p.Bytes
	}
	if count != sessions || total != size*sessions {
		t.Fatalf("%d sessions, %d bytes", count, total)
	}
	if took > 3*time.Second {
		t.Errorf("scan took %s", took)
	}
}

func newImportTarget(t *testing.T, hh string) (*synctest.Server, Options) {
	t.Helper()
	fs := synctest.New()
	srv := fs.Start()
	t.Cleanup(srv.Close)
	client := syncclient.NewClient(srv.URL, nil, nil)
	up := syncclient.NewUploader(syncclient.UploaderOptions{API: client, StateDir: filepath.Join(hh, "state"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return fs, Options{ProjectsDir: filepath.Join(hh, "projects"), Sessions: true, Putter: client, Queue: up, UploadSessions: true,
		SessionDone: func(id string) bool { return syncclient.Uploaded(filepath.Join(hh, "state"), id) }}
}

func TestSelectionIsHonoured(t *testing.T) {
	ch, cj, hh := twoProjects(t)
	fs, o := newImportTarget(t, hh)
	o.ClaudeHome, o.ClaudeJSON = ch, cj
	o.Sections = map[Section]bool{SectionAgents: true}
	o.SelectSession = func(folder, _ string) bool { return folder == "-w" }
	var last Progress
	var finished []string
	o.OnProgress = func(p Progress) {
		last = p
		if p.Item != nil {
			finished = append(finished, p.Item.Kind+":"+p.Item.Key)
		}
	}
	rep, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	user := func(kind syncv1.DocumentKind, key string) *syncv1.Document {
		return fs.Document(&syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_USER, Kind: kind, Key: key})
	}
	if user(syncv1.DocumentKind_DOCUMENT_KIND_AGENT, "plain") == nil {
		t.Error("the chosen agents are missing")
	}
	if user(syncv1.DocumentKind_DOCUMENT_KIND_MEMORY, "user") != nil || user(syncv1.DocumentKind_DOCUMENT_KIND_SKILL, "pdf") != nil {
		t.Error("sections not chosen were uploaded")
	}
	if it := find(rep, "mcp", "fs"); it != nil {
		t.Errorf("mcp not chosen: %+v", it)
	}
	if fs.Session(sessionID) == nil || fs.Session(sessionB) != nil {
		t.Error("session selection not honoured")
	}
	if _, err := os.Stat(filepath.Join(hh, "projects", "-b")); !os.IsNotExist(err) {
		t.Error("a project not chosen was copied")
	}
	if _, err := os.Stat(filepath.Join(hh, "projects", "-w", sessionID, "subagents", "agent-1.jsonl")); err != nil {
		t.Error("subagents travel with their session")
	}
	if last.Done != last.Total || last.Total != len(finished) || last.Total != 3 {
		t.Errorf("progress %+v, finished %v", last, finished)
	}
}

func TestSelectingBySessionTakesOnlyThatSession(t *testing.T) {
	ch, cj, hh := twoProjects(t)
	fs, o := newImportTarget(t, hh)
	o.ClaudeHome, o.ClaudeJSON = ch, cj
	o.Sections = map[Section]bool{}
	o.SelectSession = func(_, id string) bool { return id == sessionB }
	rep, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 1 || fs.Session(sessionB) == nil || fs.Session(sessionID) != nil {
		t.Fatalf("%+v", rep.Items)
	}
}

func TestCancelThenResumeDuplicatesNothing(t *testing.T) {
	ch, cj, hh := twoProjects(t)
	fs, o := newImportTarget(t, hh)
	o.ClaudeHome, o.ClaudeJSON = ch, cj
	o.Sections = map[Section]bool{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o.OnProgress = func(p Progress) {
		if p.Item != nil {
			cancel() // after the first session is done
		}
	}
	rep, err := Run(ctx, o)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err: %v", err)
	}
	if len(rep.Items) != 1 || rep.Items[0].Status != StatusUploaded {
		t.Fatalf("cancelled run: %+v", rep.Items)
	}
	first := strings.Split(rep.Items[0].Key, "/")[0]
	other := map[string]string{"-b": "-w", "-w": "-b"}[first]
	if _, err := os.Stat(filepath.Join(hh, "projects", other)); !os.IsNotExist(err) {
		t.Fatal("the cancelled run went on")
	}
	entries := len(fs.Session(map[string]string{"-b": sessionB, "-w": sessionID}[first]).Entries)

	o.OnProgress = nil
	rep, err = Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if it := find(rep, "session", first+"/"+map[string]string{"-b": sessionB, "-w": sessionID}[first]+".jsonl"); it == nil || it.Status != StatusExists {
		t.Fatalf("resume: %+v", rep.Items)
	}
	if it := find(rep, "session", other+"/"+map[string]string{"-b": sessionB, "-w": sessionID}[other]+".jsonl"); it == nil || it.Status != StatusUploaded {
		t.Fatalf("resume: %+v", rep.Items)
	}
	if got := len(fs.Session(map[string]string{"-b": sessionB, "-w": sessionID}[first]).Entries); got != entries {
		t.Fatalf("entries went from %d to %d", entries, got)
	}

	// Scanning now marks them, and an unfinished upload would not be.
	res, err := Scan(context.Background(), ScanOptions{ClaudeHome: ch, ClaudeJSON: cj, ProjectsDir: o.ProjectsDir, SessionDone: o.SessionDone})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res.Projects {
		if p.Imported != 1 || p.Sessions[0].Status != StatusExists {
			t.Errorf("after import: %+v", p)
		}
	}
	res, err = Scan(context.Background(), ScanOptions{ClaudeHome: ch, ClaudeJSON: cj, ProjectsDir: o.ProjectsDir, SessionDone: func(string) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	if s := res.Projects[0].Sessions[0]; s.Status != StatusPlanned || !strings.Contains(s.Note, "did not finish") {
		t.Errorf("unfinished upload: %+v", s)
	}
}

func TestStoppedCopyLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "out", "dst")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyFile(ctx, src, dst); !errors.Is(err, context.Canceled) {
		t.Fatalf("err: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(dst)); len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
}

func TestCopyKeepsTheTranscriptsTime(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	os.WriteFile(src, []byte("data"), 0o644)
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(src, old, old)
	if err := copyFile(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dst); !st.ModTime().Equal(old) {
		t.Fatalf("time: %s", st.ModTime())
	}
}

func TestHeldSessionStaysAsItIs(t *testing.T) {
	ch, cj, hh := twoProjects(t)
	fs, o := newImportTarget(t, hh)
	o.ClaudeHome, o.ClaudeJSON = ch, cj
	o.Sections = map[Section]bool{}
	o.HoldSession = func(id string) string {
		if id == sessionB {
			return "on another machine"
		}
		return ""
	}
	rep, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if it := find(rep, "session", "-b/"+sessionB+".jsonl"); it == nil || it.Status != StatusSkipped || it.Note != "on another machine" {
		t.Fatalf("held: %+v", it)
	}
	if fs.Session(sessionB) != nil {
		t.Fatal("a held session was uploaded")
	}
	if _, err := os.Stat(filepath.Join(hh, "projects", "-b")); !os.IsNotExist(err) {
		t.Fatal("a held session was copied")
	}
	res, err := Scan(context.Background(), ScanOptions{ClaudeHome: ch, ProjectsDir: o.ProjectsDir, HoldSession: o.HoldSession})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res.Projects {
		if p.Folder == "-b" && (p.Sessions[0].Status != StatusSkipped || p.Sessions[0].Note != "on another machine") {
			t.Fatalf("scan: %+v", p)
		}
	}
}

func TestRunHintsNameTheSwitches(t *testing.T) {
	ch, cj, hh := fakeHome(t)
	_, o := newImportTarget(t, hh)
	o.ClaudeHome, o.ClaudeJSON, o.Sessions = ch, cj, false
	o.ForceHint, o.SecretsHint = "Overwrite", "Secrets"
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if it := find(rep, "memory", "user"); !strings.Contains(it.Note, "use Overwrite") {
		t.Errorf("force hint: %q", it.Note)
	}
	if it := find(rep, "settings", "user"); !strings.Contains(it.Note, "use Secrets") {
		t.Errorf("secrets hint: %q", it.Note)
	}
}
