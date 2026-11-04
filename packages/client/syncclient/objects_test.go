package syncclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	objectsv1 "github.com/xlsft/harness/packages/client/gen/harness/objects/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/objects/v1/objectsv1connect"
	"github.com/xlsft/harness/packages/client/llm"

	"connectrpc.com/connect"
)

// fakeObjects is core's ObjectsService in memory, chunk rules included, reached over real Connect.
type fakeObjects struct {
	objectsv1connect.UnimplementedObjectsServiceHandler
	mu      sync.Mutex
	chunk   int64
	blobs   map[string][]byte
	uploads map[string]*fakeUpload
	records map[string]bool // session|kind|name|sha
	deleted []string
	starts  int
	chunks  int
	// failChunks makes that many UploadChunk calls fail, like a connection that drops.
	failChunks int
}

type fakeUpload struct {
	sha   string
	size  int64
	parts map[int32][]byte
}

func newFakeObjects() *fakeObjects {
	return &fakeObjects{chunk: 16, blobs: map[string][]byte{}, uploads: map[string]*fakeUpload{}, records: map[string]bool{}}
}

func recordKey(use *objectsv1.ObjectUse, sha string) string {
	return use.GetSessionId() + "|" + use.GetKind().String() + "|" + use.GetName() + "|" + sha
}

func (f *fakeObjects) StartUpload(_ context.Context, r *connect.Request[objectsv1.StartUploadRequest]) (*connect.Response[objectsv1.StartUploadResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	sha := r.Msg.GetSha256()
	if _, ok := f.blobs[sha]; ok {
		f.records[recordKey(r.Msg.GetUse(), sha)] = true
		return connect.NewResponse(&objectsv1.StartUploadResponse{AlreadyStored: true}), nil
	}
	u := f.uploads[sha]
	if u == nil {
		u = &fakeUpload{sha: sha, size: r.Msg.GetSize(), parts: map[int32][]byte{}}
		f.uploads[sha] = u
	}
	got := []int32{}
	for i := range u.parts {
		got = append(got, i)
	}
	return connect.NewResponse(&objectsv1.StartUploadResponse{UploadId: sha, ChunkSize: f.chunk, ReceivedChunks: got}), nil
}

func (f *fakeObjects) UploadChunk(_ context.Context, r *connect.Request[objectsv1.UploadChunkRequest]) (*connect.Response[objectsv1.UploadChunkResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failChunks > 0 {
		f.failChunks--
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
	}
	f.chunks++
	u := f.uploads[r.Msg.GetUploadId()]
	if u == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no upload"))
	}
	u.parts[r.Msg.GetIndex()] = append([]byte(nil), r.Msg.GetData()...)
	return connect.NewResponse(&objectsv1.UploadChunkResponse{}), nil
}

func (f *fakeObjects) FinishUpload(_ context.Context, r *connect.Request[objectsv1.FinishUploadRequest]) (*connect.Response[objectsv1.FinishUploadResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.uploads[r.Msg.GetUploadId()]
	if u == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no upload"))
	}
	var all []byte
	for i := int32(0); int64(len(all)) < u.size; i++ {
		p, ok := u.parts[i]
		if !ok {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("missing chunk"))
		}
		all = append(all, p...)
	}
	sum := sha256.Sum256(all)
	if hex.EncodeToString(sum[:]) != u.sha {
		delete(f.uploads, u.sha)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("do not match"))
	}
	f.blobs[u.sha] = all
	f.records[recordKey(r.Msg.GetUse(), u.sha)] = true
	delete(f.uploads, u.sha)
	return connect.NewResponse(&objectsv1.FinishUploadResponse{}), nil
}

func (f *fakeObjects) DeleteObject(_ context.Context, r *connect.Request[objectsv1.DeleteObjectRequest]) (*connect.Response[objectsv1.DeleteObjectResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, r.Msg.GetName()+"@"+r.Msg.GetSha256())
	return connect.NewResponse(&objectsv1.DeleteObjectResponse{}), nil
}

func (f *fakeObjects) GetObjectLimits(context.Context, *connect.Request[objectsv1.GetObjectLimitsRequest]) (*connect.Response[objectsv1.GetObjectLimitsResponse], error) {
	return connect.NewResponse(&objectsv1.GetObjectLimitsResponse{ChunkSize: f.chunk}), nil
}

func (f *fakeObjects) blob(sha string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blobs[sha]
}

func (f *fakeObjects) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.blobs)
}

type objectsFixture struct {
	fake *fakeObjects
	srv  *httptest.Server
	dir  string
}

func newObjectsFixture(t *testing.T) *objectsFixture {
	t.Helper()
	f := newFakeObjects()
	mux := http.NewServeMux()
	path, h := objectsv1connect.NewObjectsServiceHandler(f)
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &objectsFixture{fake: f, srv: srv, dir: t.TempDir()}
}

func (x *objectsFixture) queue() *ObjectQueue {
	return NewObjectQueue(ObjectQueueOptions{
		API: NewClient(x.srv.URL, nil, nil), Dir: x.dir, Interval: 10 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func sumOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestObjectQueueUploadsAndDedupes(t *testing.T) {
	x := newObjectsFixture(t)
	q := x.queue()
	data := []byte(strings.Repeat("abcdefgh", 10)) // 5 chunks of the fake's 16 bytes
	ctx := context.Background()
	if !q.Probe(ctx) {
		t.Fatal("the server has a store")
	}
	ref, err := q.AddBytes("s1", ObjectImage, "", "image/png", data)
	if err != nil {
		t.Fatal(err)
	}
	if ref.GetSha256() != sumOf(data) || ref.GetSize() != int64(len(data)) {
		t.Fatalf("%+v", ref)
	}
	q.Flush(ctx)
	if got := x.fake.blob(ref.GetSha256()); string(got) != string(data) {
		t.Fatalf("server has %q", got)
	}
	if q.Pending() != 0 {
		t.Fatalf("still pending: %d", q.Pending())
	}
	if _, err := os.Stat(filepath.Join(x.dir, "spool", ref.GetSha256())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the spool should be empty: %v", err)
	}

	// The same bytes elsewhere: a record, no chunks.
	chunks := x.fake.chunks
	if _, err := q.AddBytes("s2", ObjectImage, "", "image/png", data); err != nil {
		t.Fatal(err)
	}
	q.Flush(ctx)
	if x.fake.chunks != chunks {
		t.Fatalf("re-upload sent %d chunks", x.fake.chunks-chunks)
	}
	if !x.fake.records["s2|OBJECT_KIND_IMAGE||"+ref.GetSha256()] {
		t.Fatalf("no record: %v", x.fake.records)
	}
}

func TestObjectQueueSurvivesRestartAndDrops(t *testing.T) {
	x := newObjectsFixture(t)
	ctx := context.Background()
	data := []byte(strings.Repeat("0123456789abcdef", 8))
	q := x.queue()
	q.Probe(ctx)
	x.fake.failChunks = 1
	if _, err := q.AddBytes("s1", ObjectSentFile, "report.bin", "application/octet-stream", data); err != nil {
		t.Fatal(err)
	}
	q.Flush(ctx)
	if q.Pending() != 1 || x.fake.count() != 0 {
		t.Fatalf("pending %d, stored %d", q.Pending(), x.fake.count())
	}

	// A dropped connection after two chunks: a new process picks up from what the server says it has.
	x.fake.mu.Lock()
	x.fake.uploads[sumOf(data)] = &fakeUpload{sha: sumOf(data), size: int64(len(data)), parts: map[int32][]byte{0: data[:16], 1: data[16:32]}}
	x.fake.mu.Unlock()
	again := x.queue()
	before := x.fake.chunks
	again.Flush(ctx)
	if string(x.fake.blob(sumOf(data))) != string(data) {
		t.Fatal("not uploaded after the restart")
	}
	if n := x.fake.chunks - before; n != 6 {
		t.Fatalf("sent %d chunks, want the 6 the server did not have", n)
	}
	if !x.fake.records["s1|OBJECT_KIND_SENT_FILE|report.bin|"+sumOf(data)] {
		t.Fatalf("%v", x.fake.records)
	}
}

func TestObjectQueueOfflineCatchesUp(t *testing.T) {
	x := newObjectsFixture(t)
	q := x.queue()
	ctx := context.Background()
	q.Probe(ctx)
	x.srv.Close() // offline
	data := []byte("offline")
	if _, err := q.AddBytes("s1", ObjectImage, "", "image/png", data); err != nil {
		t.Fatal(err)
	}
	q.Flush(ctx)
	if !q.Available() || q.Pending() != 1 {
		t.Fatalf("available %v, pending %d", q.Available(), q.Pending())
	}

	// Back online, a new process: it remembered there is a store.
	y := newObjectsFixture(t)
	y.dir = x.dir
	again := y.queue()
	if !again.Available() {
		t.Fatal("forgot the server has a store")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = again.Run(runCtx) }()
	deadline := time.Now().Add(5 * time.Second)
	for again.Pending() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if string(y.fake.blob(sumOf(data))) != "offline" {
		t.Fatal("did not catch up")
	}
}

func TestObjectQueueOldServerKeepsEverythingLocal(t *testing.T) {
	// A server from before the store: unknown paths are plain 404s.
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	dir := t.TempDir()
	q := NewObjectQueue(ObjectQueueOptions{API: NewClient(srv.URL, nil, nil), Dir: dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ctx := context.Background()
	if q.Probe(ctx) || q.Available() {
		t.Fatal("no store there")
	}
	q.Flush(ctx)
	var nilQueue *ObjectQueue
	if nilQueue.Available() || nilQueue.Probe(ctx) || nilQueue.AddSentFile("s", "/a", "", nil) {
		t.Fatal("a nil queue is no store")
	}
	nilQueue.Flush(ctx)
}

func TestObjectQueueScratchRules(t *testing.T) {
	x := newObjectsFixture(t)
	q := NewObjectQueue(ObjectQueueOptions{API: NewClient(x.srv.URL, nil, nil), Dir: x.dir, ScratchMaxBytes: 100, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ctx := context.Background()
	scratch := filepath.Join(t.TempDir(), "scratchpad")
	if err := q.TrackScratch("s1", scratch); err != nil { // does not exist yet
		t.Fatal(err)
	}
	q.Flush(ctx)
	old := time.Now().Add(-time.Minute)
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(scratch, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	write("notes.md", "keep me")
	write("tool-results/call_1.txt", "saved output")
	write("shell-snapshot.zsh", "internal")
	write("cwd-123", "internal")
	write("tasks/task_1.output", "internal")
	write(".env", "SECRET=1")
	write("deep/.ssh/id_rsa", "PRIVATE")
	write("big.bin", strings.Repeat("x", 101))
	secret := filepath.Join(t.TempDir(), "id_rsa")
	if err := os.WriteFile(secret, []byte("linked secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(scratch, "link")); err != nil {
		t.Fatal(err)
	}

	q.Flush(ctx)
	want := map[string]string{"s1|OBJECT_KIND_SCRATCH|notes.md|" + sumOf([]byte("keep me")): "", "s1|OBJECT_KIND_TOOL_RESULT|tool-results/call_1.txt|" + sumOf([]byte("saved output")): ""}
	if len(x.fake.records) != len(want) {
		t.Fatalf("uploaded %v", x.fake.records)
	}
	for k := range want {
		if !x.fake.records[k] {
			t.Fatalf("missing %s in %v", k, x.fake.records)
		}
	}

	// Unchanged: nothing new. Changed: the new version goes up and replaces the old one's record.
	starts := x.fake.starts
	q.Flush(ctx)
	if x.fake.starts != starts {
		t.Fatal("unchanged files went up again")
	}
	write("notes.md", "keep me too")
	if err := os.Chtimes(filepath.Join(scratch, "notes.md"), old.Add(time.Second), old.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	q.Flush(ctx)
	if !x.fake.records["s1|OBJECT_KIND_SCRATCH|notes.md|"+sumOf([]byte("keep me too"))] {
		t.Fatalf("%v", x.fake.records)
	}
	if len(x.fake.deleted) != 1 || x.fake.deleted[0] != "notes.md@"+sumOf([]byte("keep me")) {
		t.Fatalf("deleted %v", x.fake.deleted)
	}

	// A file being written waits.
	if err := os.WriteFile(filepath.Join(scratch, "fresh.txt"), []byte("just written"), 0o600); err != nil {
		t.Fatal(err)
	}
	q.Flush(ctx)
	if x.fake.blob(sumOf([]byte("just written"))) != nil {
		t.Fatal("a file being written went up")
	}

	// A deleted session: nothing more goes up for it.
	if err := Forget(filepath.Join(filepath.Dir(x.dir), filepath.Base(x.dir), "x", "sessions"), "s1"); err != nil {
		t.Fatal(err)
	}
	q.Forget("s1")
	if err := os.Chtimes(filepath.Join(scratch, "fresh.txt"), old, old); err != nil {
		t.Fatal(err)
	}
	q.Flush(ctx)
	if x.fake.blob(sumOf([]byte("just written"))) != nil {
		t.Fatal("uploaded for a deleted session")
	}
}

func TestSentFileSensitivePathsStayLocal(t *testing.T) {
	x := newObjectsFixture(t)
	q := x.queue()
	q.Probe(context.Background())
	for _, p := range []string{"/home/me/project/.env", "/home/me/.ssh/id_ed25519", "/home/me/.aws/credentials", "/home/me/.npmrc"} {
		if q.AddSentFile("s1", p, "text/plain", []byte("secret")) {
			t.Fatalf("%s was queued", p)
		}
	}
	if q.Pending() != 0 {
		t.Fatal("something was queued")
	}
	if !q.AddSentFile("s1", "/home/me/project/.env.example", "text/plain", []byte("A=")) {
		t.Fatal("a template is no secret")
	}
	if !q.AddSentFile("s1", "/home/me/project/report.pdf", "application/pdf", []byte("%PDF")) {
		t.Fatal("an ordinary file stays out")
	}
}

func TestReferObjectsReplacesBytes(t *testing.T) {
	x := newObjectsFixture(t)
	q := x.queue()
	q.Probe(context.Background())
	shot := []byte("screenshot pixels")
	photo := []byte("attached photo")
	line, _ := json.Marshal(map[string]any{
		"uuid": "u1", "type": "user", "big": json.Number("12345678901234567890"),
		"message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "typed text stays"},
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/jpeg", "data": base64.StdEncoding.EncodeToString(photo)}},
			map[string]any{"type": "tool_result", "tool_use_id": "t", "content": []any{
				map[string]any{"type": "text", "text": "Screenshot"},
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(shot)}},
			}},
		}},
		"toolUseResult": map[string]any{"computer_use": true},
	})
	got := q.referObjects("s1", string(line))
	for _, secret := range []string{base64.StdEncoding.EncodeToString(shot), base64.StdEncoding.EncodeToString(photo), `"base64"`} {
		if strings.Contains(got, secret) {
			t.Fatalf("%q left in %s", secret, got)
		}
	}
	if !strings.Contains(got, "typed text stays") || !strings.Contains(got, "12345678901234567890") {
		t.Fatalf("%s", got)
	}

	// Anything parsing transcripts sees text blocks with the reference.
	var rec struct {
		Message llm.Message `json:"message"`
	}
	if err := json.Unmarshal([]byte(got), &rec); err != nil {
		t.Fatal(err)
	}
	att, tool := rec.Message.Content[1], rec.Message.Content[2].Content[1]
	if att.Type != llm.BlockText || att.Object == nil || att.Object.SHA256 != sumOf(photo) || att.Object.MediaType != "image/jpeg" {
		t.Fatalf("%+v", att)
	}
	if tool.Type != llm.BlockText || tool.Object == nil || tool.Object.SHA256 != sumOf(shot) || !strings.Contains(tool.Text, sumOf(shot)) {
		t.Fatalf("%+v", tool)
	}

	q.Flush(context.Background())
	if string(x.fake.blob(sumOf(shot))) != string(shot) || string(x.fake.blob(sumOf(photo))) != string(photo) {
		t.Fatal("the bytes did not go up")
	}
	if !x.fake.records["s1|OBJECT_KIND_ATTACHMENT||"+sumOf(photo)] || !x.fake.records["s1|OBJECT_KIND_IMAGE||"+sumOf(shot)] {
		t.Fatalf("%v", x.fake.records)
	}

	plain := `{"uuid":"u","message":{"content":[{"type":"text","text":"plain"}]}}`
	if q.referObjects("s1", plain) != plain {
		t.Fatal("a line without bytes changed")
	}
	if bad := `{"source":{"type":"base64"`; q.referObjects("s1", bad) != bad {
		t.Fatal("a broken line changed")
	}
}

type staticTokens string

func (t staticTokens) Token(context.Context) (string, error)   { return string(t), nil }
func (t staticTokens) Refresh(context.Context) (string, error) { return string(t), nil }

// A real core, when given one: HARNESS_TEST_OBJECTS_URL and HARNESS_TEST_OBJECTS_TOKEN (a device access token). Skipped
// otherwise; the rest of this file runs against the fake.
func TestObjectQueueAgainstRealServer(t *testing.T) {
	url, token := os.Getenv("HARNESS_TEST_OBJECTS_URL"), os.Getenv("HARNESS_TEST_OBJECTS_TOKEN")
	if url == "" || token == "" {
		t.Skip("no server given")
	}
	ctx := context.Background()
	q := NewObjectQueue(ObjectQueueOptions{API: NewClient(url, nil, staticTokens(token)), Dir: t.TempDir()})
	if !q.Probe(ctx) {
		t.Fatal("no store on the server")
	}
	data := make([]byte, 17<<20+5)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	ref, err := q.AddBytes("ses_go", ObjectSentFile, "random.bin", "application/octet-stream", data)
	if err != nil {
		t.Fatal(err)
	}
	q.Flush(ctx)
	if q.Pending() != 0 {
		t.Fatal("still pending")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/objects/"+ref.GetSha256(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	got, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != 200 || !bytes.Equal(got, data) {
		t.Fatalf("%d, %d bytes, err %v", res.StatusCode, len(got), err)
	}
	if _, err := q.o.API.DeleteObject(ctx, &objectsv1.DeleteObjectRequest{Sha256: ref.GetSha256()}); err != nil {
		t.Fatal(err)
	}
}
