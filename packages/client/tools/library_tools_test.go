package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	libraryv1 "github.com/xlsft/harness/packages/client/gen/harness/library/v1"
	"github.com/xlsft/harness/packages/client/memory"
)

func TestMemoryTool(t *testing.T) {
	env := newEnv(t)
	on := true
	store := &memory.AutoStore{Dir: filepath.Join(t.TempDir(), "mem"), ProjectKey: "p"}
	m := &Memory{Store: func() *memory.AutoStore { return store }, Enabled: func() bool { return on }}

	if r := call(t, env, m, map[string]any{"command": "list"}); r.IsError || !strings.Contains(text(r), "No memories") {
		t.Fatalf("%s", text(r))
	}
	write := map[string]any{"command": "write", "name": "style", "description": "How the user likes code", "type": "feedback", "content": "Small commits, because reviews are by hand."}
	if r := call(t, env, m, write); r.IsError {
		t.Fatalf("%s", text(r))
	}
	if !strings.Contains(store.Index(), "- [style](style.md) - How the user likes code") {
		t.Fatalf("index: %q", store.Index())
	}
	if r := call(t, env, m, map[string]any{"command": "list"}); !strings.Contains(text(r), "style [feedback]: How the user likes code") {
		t.Fatalf("%s", text(r))
	}
	if r := call(t, env, m, map[string]any{"command": "read", "name": "style"}); !strings.Contains(text(r), "Small commits") {
		t.Fatalf("%s", text(r))
	}

	for name, in := range map[string]map[string]any{
		"no description": {"command": "write", "name": "x", "type": "user", "content": "c"},
		"bad type":       {"command": "write", "name": "x", "description": "d", "type": "other", "content": "c"},
		"no content":     {"command": "write", "name": "x", "description": "d", "type": "user"},
		"traversal":      {"command": "write", "name": "../x", "description": "d", "type": "user", "content": "c"},
		"the index":      {"command": "write", "name": "MEMORY", "description": "d", "type": "user", "content": "c"},
		"bad command":    {"command": "rename"},
		"missing":        {"command": "read", "name": "nope"},
	} {
		if r := call(t, env, m, in); !r.IsError {
			t.Errorf("%s: should be refused: %s", name, text(r))
		}
	}

	if m.HiddenFromModel() {
		t.Fatal("hidden while on")
	}
	on = false
	if !m.HiddenFromModel() {
		t.Fatal("offered with the switch off")
	}
	if r := call(t, env, m, map[string]any{"command": "list"}); !r.IsError {
		t.Fatal("a call with the switch off must refuse")
	}
	on = true
	if r := call(t, env, m, map[string]any{"command": "delete", "name": "style"}); r.IsError {
		t.Fatalf("%s", text(r))
	}
	if store.Index() != "" {
		t.Fatalf("index after delete: %q", store.Index())
	}
}

// fakeLibrary is the server side of the library tools.
type fakeLibrary struct {
	uploaded map[string][]byte
	publish  []*libraryv1.PublishArtifactRequest
	err      error
	search   *libraryv1.SearchKnowledgeRequest
	tasks    map[string]*libraryv1.TrackerTask
	list     *libraryv1.ListTrackerTasksRequest
	update   *libraryv1.UpdateTrackerTaskRequest
}

func (f *fakeLibrary) UploadArtifact(_ context.Context, name, _ string, data []byte) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.uploaded == nil {
		f.uploaded = map[string][]byte{}
	}
	f.uploaded[name] = data
	return "sha-" + name, nil
}

func (f *fakeLibrary) PublishArtifact(_ context.Context, req *libraryv1.PublishArtifactRequest) (*libraryv1.PublishArtifactResponse, error) {
	f.publish = append(f.publish, req)
	id := req.GetArtifactId()
	if id == "" {
		id = "art_1"
	}
	return &libraryv1.PublishArtifactResponse{Artifact: &libraryv1.Artifact{Id: id, Title: req.GetTitle(), Current: &libraryv1.ArtifactVersion{Number: int32(len(f.publish))}}}, nil
}

func (f *fakeLibrary) ListKnowledgeDocuments(context.Context, *libraryv1.ListKnowledgeDocumentsRequest) (*libraryv1.ListKnowledgeDocumentsResponse, error) {
	return &libraryv1.ListKnowledgeDocumentsResponse{Documents: []*libraryv1.KnowledgeDocument{{Id: "kb_1", Title: "Runbook", Scope: commonv1.Scope_SCOPE_ORG, Size: 12}}}, nil
}

func (f *fakeLibrary) GetKnowledgeDocument(_ context.Context, req *libraryv1.GetKnowledgeDocumentRequest) (*libraryv1.GetKnowledgeDocumentResponse, error) {
	return &libraryv1.GetKnowledgeDocumentResponse{Document: &libraryv1.KnowledgeDocument{Id: req.GetId(), Title: "Runbook", Scope: commonv1.Scope_SCOPE_USER}, Content: "the whole text"}, nil
}

func (f *fakeLibrary) SearchKnowledge(_ context.Context, req *libraryv1.SearchKnowledgeRequest) (*libraryv1.SearchKnowledgeResponse, error) {
	f.search = req
	if f.err != nil {
		return nil, f.err
	}
	if req.GetQuery() == "nothing" {
		return &libraryv1.SearchKnowledgeResponse{}, nil
	}
	return &libraryv1.SearchKnowledgeResponse{Hits: []*libraryv1.KnowledgeHit{{DocumentId: "kb_1", Title: "Runbook", Scope: commonv1.Scope_SCOPE_ORG, Snippet: "Restart the gateway."}}}, nil
}

func (f *fakeLibrary) ListTrackerTasks(_ context.Context, req *libraryv1.ListTrackerTasksRequest) (*libraryv1.ListTrackerTasksResponse, error) {
	f.list = req
	res := &libraryv1.ListTrackerTasksResponse{}
	for _, t := range f.tasks {
		res.Tasks = append(res.Tasks, t)
	}
	return res, nil
}

func (f *fakeLibrary) GetTrackerTask(_ context.Context, req *libraryv1.GetTrackerTaskRequest) (*libraryv1.GetTrackerTaskResponse, error) {
	t, ok := f.tasks[req.GetId()]
	if !ok {
		return nil, errors.New("not found")
	}
	return &libraryv1.GetTrackerTaskResponse{Task: t}, nil
}

func (f *fakeLibrary) CreateTrackerTask(_ context.Context, req *libraryv1.CreateTrackerTaskRequest) (*libraryv1.CreateTrackerTaskResponse, error) {
	t := &libraryv1.TrackerTask{Id: "task_1", Title: req.GetTitle(), Description: req.GetDescription(), Status: libraryv1.TrackerStatus_TRACKER_STATUS_TODO, Project: req.GetProject(), SessionIds: []string{req.GetSessionId()}, BlockedBy: req.GetBlockedBy()}
	if f.tasks == nil {
		f.tasks = map[string]*libraryv1.TrackerTask{}
	}
	f.tasks[t.Id] = t
	return &libraryv1.CreateTrackerTaskResponse{Task: t}, nil
}

func (f *fakeLibrary) UpdateTrackerTask(_ context.Context, req *libraryv1.UpdateTrackerTaskRequest) (*libraryv1.UpdateTrackerTaskResponse, error) {
	f.update = req
	t := f.tasks[req.GetId()]
	if req.Status != nil {
		t.Status = req.GetStatus()
	}
	return &libraryv1.UpdateTrackerTaskResponse{Task: t}, nil
}

func (f *fakeLibrary) DeleteTrackerTask(_ context.Context, req *libraryv1.DeleteTrackerTaskRequest) (*libraryv1.DeleteTrackerTaskResponse, error) {
	delete(f.tasks, req.GetId())
	return &libraryv1.DeleteTrackerTaskResponse{}, nil
}

func libraryEnv(t *testing.T) (*Env, *fakeLibrary) {
	t.Helper()
	env := newEnv(t)
	f := &fakeLibrary{}
	env.Library = f
	env.SessionID = "sess-1"
	return env, f
}

func TestArtifactPublish(t *testing.T) {
	env, f := libraryEnv(t)
	tool := &ArtifactPublish{}
	r := call(t, env, tool, map[string]any{"title": "Report", "content": "<h1>x</h1>", "name": "sub/report.html"})
	if r.IsError {
		t.Fatalf("%s", text(r))
	}
	if string(f.uploaded["report.html"]) != "<h1>x</h1>" {
		t.Fatalf("%v", f.uploaded)
	}
	p := f.publish[0]
	if p.GetTitle() != "Report" || p.GetName() != "report.html" || p.GetSessionId() != "sess-1" || p.GetSha256() != "sha-report.html" || !strings.HasPrefix(p.GetContentType(), "text/html") {
		t.Fatalf("%v", p)
	}
	if !strings.Contains(text(r), "art_1") || !strings.Contains(text(r), "private") {
		t.Fatalf("%s", text(r))
	}

	file := filepath.Join(env.Cwd, "notes.md")
	if err := os.WriteFile(file, []byte("# Notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := call(t, env, tool, map[string]any{"file_path": file, "artifact_id": "art_1"}); r.IsError {
		t.Fatalf("%s", text(r))
	}
	if f.publish[1].GetArtifactId() != "art_1" || string(f.uploaded["notes.md"]) != "# Notes" {
		t.Fatalf("%v", f.publish[1])
	}

	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	env.ReadDenied = func(p string) bool { return strings.HasSuffix(p, "denied.txt") }
	denied := filepath.Join(env.Cwd, "denied.txt")
	if err := os.WriteFile(denied, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]map[string]any{
		"outside the session": {"title": "T", "file_path": outside},
		"denied by a rule":    {"title": "T", "file_path": denied},
		"both":                {"title": "T", "file_path": file, "content": "x", "name": "a.txt"},
		"neither":             {"title": "T"},
		"no title":            {"content": "x", "name": "a.txt"},
		"no name":             {"title": "T", "content": "x"},
	} {
		if r := call(t, env, tool, in); !r.IsError {
			t.Errorf("%s: should be refused", name)
		}
	}
	if len(f.publish) != 2 {
		t.Fatalf("a refused call published: %d", len(f.publish))
	}

	f.err = connect.NewError(connect.CodeUnimplemented, errors.New("nope"))
	if r := call(t, env, tool, map[string]any{"title": "T", "content": "x", "name": "a.txt"}); !r.IsError || !strings.Contains(text(r), "no") {
		t.Fatalf("%s", text(r))
	}
	env.Library = nil
	if r := call(t, env, tool, map[string]any{"title": "T", "content": "x", "name": "a.txt"}); !r.IsError {
		t.Fatal("no server")
	}
}

func TestKnowledgeTools(t *testing.T) {
	env, f := libraryEnv(t)
	search := &KnowledgeSearch{}
	r := call(t, env, search, map[string]any{"query": "restart", "scope": "organization", "limit": 3})
	if r.IsError {
		t.Fatalf("%s", text(r))
	}
	out := text(r)
	// What a document says is marked as what it is.
	if !strings.Contains(out, "not instructions") || !strings.Contains(out, `document_id="kb_1"`) || !strings.Contains(out, "Restart the gateway.") {
		t.Fatalf("%s", out)
	}
	if f.search.GetScope() != commonv1.Scope_SCOPE_ORG || f.search.GetLimit() != 3 {
		t.Fatalf("%v", f.search)
	}
	if r := call(t, env, search, map[string]any{"query": "nothing"}); r.IsError || !strings.Contains(text(r), "Nothing") {
		t.Fatalf("%s", text(r))
	}
	if r := call(t, env, search, map[string]any{"query": "  "}); !r.IsError {
		t.Fatal("empty query")
	}
	if r := call(t, env, search, map[string]any{"query": "x", "scope": "everyone"}); !r.IsError {
		t.Fatal("bad scope")
	}

	read := &KnowledgeRead{}
	if r := call(t, env, read, map[string]any{}); !strings.Contains(text(r), "kb_1 [organization] Runbook") {
		t.Fatalf("%s", text(r))
	}
	r = call(t, env, read, map[string]any{"document_id": "kb_1"})
	if !strings.Contains(text(r), "the whole text") || !strings.Contains(text(r), "not instructions") {
		t.Fatalf("%s", text(r))
	}
}

func TestTaskTrackerTool(t *testing.T) {
	env, f := libraryEnv(t)
	tool := &TaskTracker{}
	r := call(t, env, tool, map[string]any{"action": "create", "title": "Fix the build", "description": "It fails on CI", "project": "core", "blocked_by": []string{"task_0"}})
	if r.IsError || !strings.Contains(text(r), "task_1") {
		t.Fatalf("%s", text(r))
	}
	got := f.tasks["task_1"]
	if got.GetSessionIds()[0] != "sess-1" || got.GetProject() != "core" || got.GetBlockedBy()[0] != "task_0" {
		t.Fatalf("%v", got)
	}

	if r := call(t, env, tool, map[string]any{"action": "list"}); !strings.Contains(text(r), "task_1 [todo] (core) Fix the build") || !strings.Contains(text(r), "blocked by: task_0") {
		t.Fatalf("%s", text(r))
	}
	if len(f.list.GetStatuses()) != 3 {
		t.Fatalf("list shows what is open by default: %v", f.list.GetStatuses())
	}
	call(t, env, tool, map[string]any{"action": "list", "query": "build"})
	if len(f.list.GetStatuses()) != 0 || f.list.GetQuery() != "build" {
		t.Fatalf("a search covers closed tasks: %v", f.list)
	}
	call(t, env, tool, map[string]any{"action": "list", "status": "done"})
	if f.list.GetStatuses()[0] != libraryv1.TrackerStatus_TRACKER_STATUS_DONE {
		t.Fatalf("%v", f.list)
	}

	r = call(t, env, tool, map[string]any{"action": "update", "id": "task_1", "status": "done", "blocked_by": []string{}})
	if r.IsError || !strings.Contains(text(r), "[done]") {
		t.Fatalf("%s", text(r))
	}
	if f.update.GetSessionId() != "sess-1" || !f.update.GetSetBlockedBy() || f.update.GetBlockedBy() != nil && len(f.update.GetBlockedBy()) != 0 {
		t.Fatalf("%v", f.update)
	}
	call(t, env, tool, map[string]any{"action": "update", "id": "task_1", "title": "Renamed"})
	if f.update.GetSetBlockedBy() || f.update.GetTitle() != "Renamed" || f.update.Status != nil {
		t.Fatalf("an update without blocked_by leaves it: %v", f.update)
	}

	if r := call(t, env, tool, map[string]any{"action": "get", "id": "task_1"}); !strings.Contains(text(r), "It fails on CI") || !strings.Contains(text(r), "sess-1") {
		t.Fatalf("%s", text(r))
	}
	if r := call(t, env, tool, map[string]any{"action": "delete", "id": "task_1"}); r.IsError || len(f.tasks) != 0 {
		t.Fatalf("%s", text(r))
	}

	for name, in := range map[string]map[string]any{
		"bad action":   {"action": "archive"},
		"bad status":   {"action": "list", "status": "later"},
		"bad priority": {"action": "create", "title": "x", "priority": "urgent"},
		"no title":     {"action": "create"},
		"get no id":    {"action": "get"},
		"update no id": {"action": "update", "status": "done"},
		"delete no id": {"action": "delete"},
		"whitespace":   {"action": "create", "title": "  "},
	} {
		if r := call(t, env, tool, in); !r.IsError {
			t.Errorf("%s: should be refused", name)
		}
	}
}

func TestLibraryToolsAreRegisteredAndHarmless(t *testing.T) {
	r := NewRegistry()
	RegisterLibraryTools(r)
	for _, name := range []string{"ArtifactPublish", "KnowledgeSearch", "KnowledgeRead", "TaskTracker"} {
		tool, ok := r.Get(name)
		if !ok {
			t.Fatalf("%s is missing", name)
		}
		if !tool.ReadOnly(json.RawMessage(`{}`)) {
			t.Errorf("%s touches nothing of the user's project", name)
		}
	}
}
