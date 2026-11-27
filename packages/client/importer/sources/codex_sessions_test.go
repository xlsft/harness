package sources

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/importer"
	"github.com/xlsft/harness/packages/client/session"
)

type fakeQueue struct{ queued, flushed []string }

func (q *fakeQueue) Queue(id, _ string) error { q.queued = append(q.queued, id); return nil }
func (q *fakeQueue) Flush(_ context.Context, id string) error {
	q.flushed = append(q.flushed, id)
	return nil
}

// A Codex session goes where a session of its folder would, is found again on the next scan, and a copy continued here
// is left alone.
func TestCodexSessionImport(t *testing.T) {
	home, _ := filepath.Abs(filepath.Join("testdata", "user"))
	projects := t.TempDir()
	q := &fakeQueue{}
	held := ""
	o := importer.EngineOptions{Env: importer.Env{Home: home, GOOS: "darwin"}, ProjectsDir: projects, Queue: q, UploadSessions: true,
		HoldSession: func(id string) string {
			if id == held {
				return "open here"
			}
			return ""
		}}
	ctx := context.Background()
	item := func() importer.SourceItem {
		t.Helper()
		scan, err := importer.ScanSource(ctx, Get("codex"), "", o)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range scan.Items {
			if it.Kind == importer.KindSession && it.Name == "Fix the failing test" {
				return it
			}
		}
		t.Fatal("no session")
		return importer.SourceItem{}
	}
	it := item()
	if it.Status != importer.StatusPlanned || it.Target != "session in /work/app" {
		t.Fatalf("scan: %+v", it)
	}
	rep, err := importer.RunSource(ctx, Get("codex"), "", []string{it.ID}, o)
	if err != nil || len(rep.Items) != 1 || rep.Items[0].Status != importer.StatusUploaded {
		t.Fatalf("run: %+v %v", rep, err)
	}
	id := importer.ImportedSessionID("codex", "abc")
	dst := session.Store{Root: projects}.Path("/work/app", id)
	data, err := os.ReadFile(dst)
	if err != nil || !strings.Contains(string(data), "Fix the failing test") || len(q.flushed) != 1 || q.flushed[0] != id {
		t.Fatalf("written: %v %s %+v", err, data, q)
	}
	if it := item(); it.Status != importer.StatusExists {
		t.Errorf("second scan: %+v", it)
	}
	if err := os.WriteFile(dst, append(data, []byte(`{"type":"user"}`+"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if it := item(); it.Status != importer.StatusSkipped || !strings.Contains(it.Note, "continued") {
		t.Errorf("continued: %+v", it)
	}
	held = id
	if it := item(); it.Status != importer.StatusSkipped || it.Note != "open here" && !strings.Contains(it.Note, "open here") {
		t.Errorf("held: %+v", it)
	}
}

// Gemini CLI's chat recordings: both formats, the latest line of an id, a rewind, thoughts left out, the folder from
// .project_root, and a chat whose folder is unknown.
func TestGeminiSessions(t *testing.T) {
	f := userFound(t, "gemini")
	slow := want(t, f, "session/List the slow endpoints.")
	if slow.Session == nil || slow.Session.Cwd != "/work/shop" || slow.Session.ID != importer.ImportedSessionID("gemini", "a1b2c3d4-0000-4000-8000-000000000001") {
		t.Fatalf("jsonl: %+v", slow)
	}
	tr := string(slow.Session.Transcript)
	for _, bad := range []string{"Looking.", "wrong project", "thinking", "Checkpoint"} {
		if strings.Contains(tr, bad) {
			t.Errorf("%q came over: %s", bad, tr)
		}
	}
	if !strings.Contains(tr, "/orders and /search are slow.") || !strings.Contains(tr, "*Used Shell: `grep -r slow logs`*") || !strings.Contains(tr, "/search, by twice.") {
		t.Errorf("transcript: %s", tr)
	}
	if slow.Session.Messages != 4 {
		t.Errorf("messages: %d", slow.Session.Messages)
	}
	if old := want(t, f, "session/Rename the config package."); old.Session == nil || old.Session.Messages != 2 {
		t.Errorf("json: %+v", old)
	}
	if lost := want(t, f, "session/Hello."); lost.Status != importer.StatusSkipped || lost.Session != nil {
		t.Errorf("no folder: %+v", lost)
	}
}
