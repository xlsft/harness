package syncclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/syncclient/synctest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func skillRef(name string) *syncv1.DocumentRef {
	return &syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SKILL, Key: name}
}

func newSyncer(t *testing.T, url, dir string, tokens *fakeTokens) *Syncer {
	t.Helper()
	cache, err := OpenCache(dir, url)
	if err != nil {
		t.Fatal(err)
	}
	var tk any = tokens
	c := NewClient(url, nil, nil)
	if tokens != nil {
		c = NewClient(url, nil, tk.(*fakeTokens))
	}
	return New(Options{Client: c, Cache: cache, PageSize: 2, MinBackoff: 10 * time.Millisecond, MaxBackoff: 40 * time.Millisecond, IdleTimeout: time.Second, Logger: quiet, Source: "client:test"})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestCatchUpPagingAndPersistence(t *testing.T) {
	fs := synctest.New()
	srv := fs.Start()
	defer srv.Close()
	for i := 0; i < 5; i++ {
		fs.Seed(skillRef(fmt.Sprintf("s%d", i)), "v1")
	}
	fs.Remove(skillRef("s4"))
	dir := t.TempDir()
	s := newSyncer(t, srv.URL, dir, nil)
	if s.Cache().Synced() {
		t.Fatal("synced before catch-up")
	}
	if err := s.CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.Online() || !s.Cache().Synced() || s.Cache().Len() != 4 || s.Cache().Cursor() != 6 {
		t.Fatalf("online %v synced %v len %d cursor %d", s.Online(), s.Cache().Synced(), s.Cache().Len(), s.Cache().Cursor())
	}
	if fs.Calls("ListChanges") < 3 {
		t.Fatalf("expected paging, calls %d", fs.Calls("ListChanges"))
	}
	d, ok := s.Cache().Lookup(commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SKILL, "s2", "")
	if !ok || d.GetContent() != "v1" {
		t.Fatalf("lookup %v %v", d, ok)
	}

	// Incremental catch-up applies updates and deletes.
	fs.Seed(skillRef("s0"), "v2")
	fs.Remove(skillRef("s1"))
	if err := s.CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Cache().Lookup(commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SKILL, "s0", ""); d.GetContent() != "v2" {
		t.Fatalf("update not applied: %v", d)
	}
	if _, ok := s.Cache().Lookup(commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SKILL, "s1", ""); ok {
		t.Fatal("tombstone not applied")
	}

	// Reopening restores documents and the cursor.
	c2, err := OpenCache(dir, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Len() != 3 || c2.Cursor() != s.Cache().Cursor() || !c2.Synced() {
		t.Fatalf("reopen: len %d cursor %d", c2.Len(), c2.Cursor())
	}
	if got := len(c2.List(syncv1.DocumentKind_DOCUMENT_KIND_SKILL)); got != 3 {
		t.Fatalf("list %d", got)
	}
	// Another identity starts empty.
	c3, _ := OpenCache(dir, "https://other|usr_2")
	if c3.Len() != 0 || c3.Cursor() != 0 || c3.Synced() {
		t.Fatal("identity change must reset the cache")
	}
}

func TestResyncDropsStaleDocuments(t *testing.T) {
	fs := synctest.New()
	srv := fs.Start()
	defer srv.Close()
	fs.Seed(skillRef("keep"), "k")
	fs.Seed(skillRef("gone"), "g")
	s := newSyncer(t, srv.URL, t.TempDir(), nil)
	if err := s.CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	var resyncEvents atomic.Int32
	s.Cache().Subscribe(func(e Event) {
		if e.Resync {
			resyncEvents.Add(1)
		}
	})
	fs.Remove(skillRef("gone"))
	fs.Seed(skillRef("new"), "n")
	fs.SetResyncBelow(100) // the client's cursor is too old
	if err := s.CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Cache().Lookup(commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SKILL, "gone", ""); ok {
		t.Fatal("stale document survived resync")
	}
	if _, ok := s.Cache().Lookup(commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SKILL, "new", ""); !ok || s.Cache().Len() != 2 {
		t.Fatalf("resync snapshot incomplete: %d", s.Cache().Len())
	}
	if resyncEvents.Load() != 1 {
		t.Fatalf("resync events %d", resyncEvents.Load())
	}
}

func TestWatchLiveReconnectAndResync(t *testing.T) {
	fs := synctest.New()
	fs.HeartbeatInterval = 20 * time.Millisecond
	srv := fs.Start()
	defer srv.Close()
	fs.Seed(skillRef("a"), "a")
	s := newSyncer(t, srv.URL, t.TempDir(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	has := func(key, content string) func() bool {
		return func() bool {
			d, ok := s.Cache().Lookup(commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SKILL, key, "")
			return ok && d.GetContent() == content
		}
	}
	waitFor(t, "catch-up via watch", has("a", "a"))
	waitFor(t, "online", s.Online)
	if !s.Cache().Synced() {
		t.Fatal("caught_up must mark the cache synced")
	}
	fs.Seed(skillRef("b"), "live")
	waitFor(t, "live change", has("b", "live"))

	// Outage: streams break and reconnects fail.
	var down atomic.Bool
	down.Store(true)
	fs.SetFail(func(p string) error {
		if down.Load() && strings.HasPrefix(p, "WatchChanges") {
			return connect.NewError(connect.CodeUnavailable, errors.New("down"))
		}
		return nil
	})
	waitFor(t, "offline", func() bool { return !s.Online() })
	fs.Seed(skillRef("c"), "while offline")
	if has("c", "while offline")() {
		t.Fatal("change applied while offline")
	}
	down.Store(false)
	waitFor(t, "reconnect catch-up", has("c", "while offline"))
	waitFor(t, "online again", s.Online)

	// Expired cursor during watch: full resync.
	fs.SetFail(func(p string) error {
		if p == "WatchChanges.heartbeat" {
			return connect.NewError(connect.CodeUnavailable, errors.New("drop"))
		}
		return nil
	})
	fs.Remove(skillRef("a"))
	fs.Seed(skillRef("d"), "d")
	time.Sleep(50 * time.Millisecond)
	fs.SetFail(nil)
	cursor := s.Cache().Cursor()
	fs.SetResyncBelow(cursor + 1000)
	fs.Seed(skillRef("e"), "after resync")
	// Force a reconnect so the server sees the old cursor.
	fs.SetFail(func(p string) error {
		if p == "WatchChanges.heartbeat" {
			return connect.NewError(connect.CodeUnavailable, errors.New("drop"))
		}
		return nil
	})
	waitFor(t, "resync", has("e", "after resync"))
	fs.SetFail(nil)
	if _, ok := s.Cache().Lookup(commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SKILL, "a", ""); ok {
		t.Fatal("deleted doc survived")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run: %v", err)
	}
}

func TestPutConflictAndUpdate(t *testing.T) {
	fs := synctest.New()
	srv := fs.Start()
	defer srv.Close()
	s := newSyncer(t, srv.URL, t.TempDir(), nil)
	ctx := context.Background()
	ref := &syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_MEMORY, Key: "user"}

	d, err := s.Put(ctx, ref, "one", nil, proto.Int64(0))
	if err != nil || d.GetVersion() == 0 || d.GetSource() != "client:test" {
		t.Fatalf("create: %v %v", d, err)
	}
	if c, ok := s.Cache().Lookup(ref.Scope, ref.Kind, ref.Key, ""); !ok || c.GetContent() != "one" {
		t.Fatal("put must update the cache")
	}
	_, err = s.Put(ctx, ref, "two", nil, proto.Int64(0))
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.Current.GetContent() != "one" || ce.BaseVersion() != d.GetVersion() {
		t.Fatalf("create-only conflict: %v", err)
	}
	// Someone else writes; our cached base is stale. Update merges and retries.
	fs.Seed(ref, "remote")
	got, err := s.Update(ctx, ref, 3, func(cur *syncv1.Document) (string, []*syncv1.DocumentFile, error) {
		return cur.GetContent() + "+local", nil, nil
	})
	if err != nil || got.GetContent() != "remote+local" {
		t.Fatalf("update: %v %v", got, err)
	}
	// Conflict against a tombstone.
	fs.Remove(ref)
	_, err = s.Put(ctx, ref, "x", nil, proto.Int64(got.GetVersion()))
	if !errors.As(err, &ce) || ce.Tombstone == nil || ce.BaseVersion() != 0 {
		t.Fatalf("tombstone conflict: %v", err)
	}
	if _, ok := s.Cache().Lookup(ref.Scope, ref.Kind, ref.Key, ""); ok {
		t.Fatal("tombstone conflict must drop the cached doc")
	}
	// Delete with CAS.
	d, _ = s.Put(ctx, skillRef("del"), "x", nil, nil)
	if err := s.Delete(ctx, d.GetId(), d.GetVersion()+1); !errors.As(err, &ce) {
		t.Fatalf("delete conflict: %v", err)
	}
	if err := s.Delete(ctx, d.GetId(), d.GetVersion()); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Cache().Get(d.GetId()); ok {
		t.Fatal("delete must update the cache")
	}
}

func TestOfflineServesCache(t *testing.T) {
	fs := synctest.New()
	srv := fs.Start()
	dir := t.TempDir()
	fs.Seed(&syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, Key: "user"}, `{"model":"m"}`)
	s, err := Open(context.Background(), OpenOptions{ServerURL: srv.URL, Dir: dir, Logger: quiet})
	if err != nil || !s.Online() || s.Cache().Len() != 1 {
		t.Fatalf("open online: %v %v", err, s.Cache().Len())
	}
	url := srv.URL
	srv.Close()
	s2, err := Open(context.Background(), OpenOptions{ServerURL: url, Dir: dir, Logger: quiet, CatchUpTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if s2.Online() {
		t.Fatal("must be offline")
	}
	if d, ok := s2.Cache().Lookup(commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, "user", ""); !ok || d.GetContent() != `{"model":"m"}` || !s2.Cache().Synced() {
		t.Fatal("cache not served offline")
	}
	if _, err := s2.Put(context.Background(), skillRef("x"), "x", nil, nil); err == nil || s2.Online() {
		t.Fatal("put must fail offline")
	}
}

type fakeTokens struct {
	mu        sync.Mutex
	token     string
	refreshed string
	refreshes int
}

func (f *fakeTokens) Token(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token, nil
}

func (f *fakeTokens) Refresh(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes++
	f.token = f.refreshed
	return f.token, nil
}

func TestAuthRefresh(t *testing.T) {
	fs := synctest.New()
	fs.Token = "fresh"
	fs.HeartbeatInterval = 20 * time.Millisecond
	srv := fs.Start()
	defer srv.Close()
	fs.Seed(skillRef("a"), "a")
	tk := &fakeTokens{token: "stale", refreshed: "fresh"}
	s := newSyncer(t, srv.URL, t.TempDir(), tk)
	if err := s.CatchUp(context.Background()); err != nil || tk.refreshes != 1 {
		t.Fatalf("catch-up with refresh: %v refreshes %d", err, tk.refreshes)
	}
	// The watch stream refreshes too.
	tk.mu.Lock()
	tk.token = "stale"
	tk.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	fs.Seed(skillRef("b"), "b")
	waitFor(t, "watch after refresh", func() bool {
		_, ok := s.Cache().Lookup(commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SKILL, "b", "")
		return ok
	})
}

// ---------------------------------------------------------------------------
// Uploader
// ---------------------------------------------------------------------------

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func line(uuid, parent, typ, extra string) string {
	p := "null"
	if parent != "" {
		p = `"` + parent + `"`
	}
	u := ""
	if uuid != "" {
		u = `"uuid":"` + uuid + `",`
	}
	return `{"parentUuid":` + p + `,` + u + `"type":"` + typ + `","sessionId":"s1","timestamp":"2026-11-02T10:00:00.000Z"` + extra + "}\n"
}

func TestUploaderDedupResumeAndMeta(t *testing.T) {
	fs := synctest.New()
	srv := fs.Start()
	defer srv.Close()
	client := NewClient(srv.URL, nil, nil)
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	transcript := filepath.Join(dir, "s1.jsonl")
	appendFile(t, transcript, line("u1", "", "user", `,"message":{"role":"user","content":"hi"}`)+
		`{"type":"harness-meta","promptBundleVersion":"b1","timestamp":"2026-11-02T10:00:00Z","sessionId":"s1","parentUuid":null}`+"\n"+
		"not json\n"+
		line("u2", "u1", "assistant", "")+
		`{"parentUuid":"u2","uuid":"u3","type":"user"`) // partial line

	newU := func() *Uploader {
		return NewUploader(UploaderOptions{API: client, StateDir: stateDir, MaxEntries: 2, DeviceID: "dev_1", DeviceName: "laptop",
			Logger: quiet, ResolveRepo: func(context.Context, string) string { return "github.com/o/r" }})
	}
	u := newU()
	if err := u.Track("s1", transcript, SessionMeta{Title: "first", Cwd: "/w", Repo: "github.com/o/r", Branch: "main", Status: agentv1.SessionStatus_SESSION_STATUS_WORKING}); err != nil {
		t.Fatal(err)
	}
	if got := u.Pending(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("pending %v", got)
	}
	ctx := context.Background()
	if err := u.Flush(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	ss := fs.Session("s1")
	if ss == nil || len(ss.Entries) != 3 {
		t.Fatalf("entries after first flush: %+v", ss)
	}
	if ss.Entries[0].GetUuid() != "u1" || ss.Entries[1].GetType() != "harness-meta" || ss.Entries[1].GetUuid() == "" || ss.Entries[2].GetParentUuid() != "u1" {
		t.Fatalf("entries: %v", ss.Entries)
	}
	if !strings.HasPrefix(ss.Entries[0].GetEntryJson(), `{"parentUuid":null,"uuid":"u1"`) || ss.Entries[0].GetTimestamp() != "2026-11-02T10:00:00Z" {
		t.Fatalf("entry json/timestamp: %v", ss.Entries[0])
	}
	info := ss.Meta.GetInfo()
	if info.GetTitle() != "first" || info.GetDeviceId() != "dev_1" || info.GetDeviceName() != "laptop" || info.GetGitBranch() != "main" || info.GetRepo() != "github.com/o/r" {
		t.Fatalf("meta %v", info)
	}
	for _, p := range ss.Upserts[0] {
		if p == "info.parent_session_id" || p == "info.model" {
			t.Fatalf("first upsert must skip empty fields: %v", ss.Upserts[0])
		}
	}
	if len(u.Pending()) != 0 {
		t.Fatalf("pending after flush: %v", u.Pending())
	}

	// Complete the partial line and add one more; a fresh uploader resumes.
	appendFile(t, transcript, "}\n"+line("u4", "u3", "assistant", ""))
	u = newU()
	if err := u.Flush(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if ss = fs.Session("s1"); len(ss.Entries) != 5 || ss.Entries[4].GetUuid() != "u4" {
		t.Fatalf("resume: %d entries", len(ss.Entries))
	}
	upserts := len(ss.Upserts)

	// Lost ack: rewind the stored offset; re-upload is deduplicated by uuid.
	st, _ := loadState(stateDir, "s1")
	st.Offset = 0
	saveState(stateDir, st)
	if err := u.Flush(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if ss = fs.Session("s1"); len(ss.Entries) != 5 {
		t.Fatalf("dedup failed: %d entries", len(ss.Entries))
	}
	if st, _ := loadState(stateDir, "s1"); st.LastSeq != 5 {
		t.Fatalf("last seq %d", st.LastSeq)
	}

	// Metadata changes send only the changed paths.
	if err := u.UpdateMeta("s1", func(m *SessionMeta) {
		m.Title = "renamed"
		m.Status = agentv1.SessionStatus_SESSION_STATUS_IDLE
	}); err != nil {
		t.Fatal(err)
	}
	if err := u.Flush(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	ss = fs.Session("s1")
	if len(ss.Upserts) != upserts+1 || strings.Join(ss.Upserts[upserts], ",") != "info.status,info.title,info.updated_at" {
		t.Fatalf("partial upsert paths: %v", ss.Upserts)
	}
	if ss.Meta.GetInfo().GetTitle() != "renamed" || ss.Meta.GetInfo().GetCwd() != "/w" {
		t.Fatalf("meta after partial upsert: %v", ss.Meta.GetInfo())
	}
	if err := u.UpdateMeta("nope", func(*SessionMeta) {}); !errors.Is(err, ErrNotTracked) {
		t.Fatalf("untracked: %v", err)
	}
}

func TestUploaderRunRetriesAndQueuedImports(t *testing.T) {
	fs := synctest.New()
	var failing atomic.Bool
	failing.Store(true)
	fs.SetFail(func(p string) error {
		if failing.Load() && p == "AppendSessionEvents" {
			return connect.NewError(connect.CodeUnavailable, errors.New("down"))
		}
		return nil
	})
	srv := fs.Start()
	defer srv.Close()
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	cwd := filepath.Join(dir, "work")
	os.MkdirAll(cwd, 0o755)

	// An imported Claude Code session: metadata is derived from the transcript.
	imported := filepath.Join(dir, "imp.jsonl")
	appendFile(t, imported,
		`{"type":"user","uuid":"i1","parentUuid":null,"cwd":"`+cwd+`","gitBranch":"dev","sessionId":"imp","timestamp":"2026-10-01T09:00:00Z","message":{"role":"user","content":"Fix the flaky test in the parser package please"}}`+"\n"+
			`{"type":"assistant","uuid":"i2","parentUuid":"i1","sessionId":"imp","timestamp":"2026-10-01T09:01:00Z","message":{"role":"assistant","model":"claude-x","content":[]}}`+"\n"+
			`{"type":"ai-title","aiTitle":"Parser flake fix","sessionId":"imp"}`+"\n")
	if err := QueueFile(stateDir, "imp", imported); err != nil {
		t.Fatal(err)
	}
	u := NewUploader(UploaderOptions{API: NewClient(srv.URL, nil, nil), StateDir: stateDir, Interval: 20 * time.Millisecond, Logger: quiet,
		ResolveRepo: func(_ context.Context, d string) string {
			if d == cwd {
				return "github.com/o/imp"
			}
			return ""
		}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go u.Run(ctx)

	live := filepath.Join(dir, "live.jsonl")
	appendFile(t, live, line("l1", "", "user", ""))
	if err := u.Track("live", live, SessionMeta{Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if fs.Session("imp") == nil || len(fs.Session("imp").Entries) != 0 {
		t.Fatal("metadata should be upserted while appends fail")
	}
	failing.Store(false)
	waitFor(t, "retry", func() bool {
		a, b := fs.Session("imp"), fs.Session("live")
		return a != nil && b != nil && len(a.Entries) == 3 && len(b.Entries) == 1
	})
	info := fs.Session("imp").Meta.GetInfo()
	if info.GetTitle() != "Parser flake fix" || info.GetCwd() != cwd || info.GetGitBranch() != "dev" || info.GetModel() != "claude-x" ||
		info.GetRepo() != "github.com/o/imp" || info.GetStatus() != agentv1.SessionStatus_SESSION_STATUS_IDLE || info.GetUpdatedAt() != "2026-10-01T09:01:00Z" {
		t.Fatalf("derived meta %v", info)
	}
	appendFile(t, live, line("l2", "l1", "assistant", ""))
	u.Notify("live")
	waitFor(t, "notify", func() bool { return len(fs.Session("live").Entries) == 2 })
	// Queueing an already tracked session keeps its state.
	if err := QueueFile(stateDir, "live", live); err != nil {
		t.Fatal(err)
	}
	if st, _ := loadState(stateDir, "live"); st.Derive || st.Offset == 0 {
		t.Fatalf("queue must not reset state: %+v", st)
	}
}

func TestUploaderRecreatesUnknownSession(t *testing.T) {
	fs := synctest.New()
	srv := fs.Start()
	defer srv.Close()
	dir := t.TempDir()
	p := filepath.Join(dir, "x.jsonl")
	appendFile(t, p, line("x1", "", "user", ""))
	stateDir := filepath.Join(dir, "state")
	// State claims the session was upserted, but the server does not know it.
	saveState(stateDir, &uploadState{SessionID: "x", Path: p, Upserted: true, Meta: SessionMeta{Cwd: "/c"}})
	u := NewUploader(UploaderOptions{API: NewClient(srv.URL, nil, nil), StateDir: stateDir, Logger: quiet})
	if err := u.Flush(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if ss := fs.Session("x"); ss == nil || len(ss.Entries) != 1 || ss.Meta.GetInfo().GetCwd() != "/c" {
		t.Fatalf("session %+v", ss)
	}
}

func TestDeriveMetaFallbackTitle(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	appendFile(t, p, `{"type":"user","isMeta":true,"message":{"role":"user","content":"<command-name>x</command-name>"}}`+"\n"+
		`{"type":"user","message":{"role":"user","content":"`+strings.Repeat("word ", 30)+`"}}`+"\n")
	m := DeriveMeta(p)
	if !strings.HasPrefix(m.Title, "word word") || len([]rune(m.Title)) != 80 {
		t.Fatalf("title %q", m.Title)
	}
}
