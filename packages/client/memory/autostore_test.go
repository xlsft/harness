package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

// memoryDocs is an in-memory server with an outage switch.
type memoryDocs struct {
	mu      sync.Mutex
	index   map[string]string
	files   map[string]map[string]string
	version map[string]int64
	down    bool
	// conflicts makes the next Saves answer with a conflict, as another machine winning the race does.
	conflicts int
	saves     int
}

func newMemoryDocs() *memoryDocs {
	return &memoryDocs{index: map[string]string{}, files: map[string]map[string]string{}, version: map[string]int64{}}
}

func (d *memoryDocs) Load(_ context.Context, key string) (string, map[string]string, int64, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.down {
		return "", nil, 0, false, errors.New("unreachable")
	}
	v, ok := d.version[key]
	if !ok {
		return "", nil, 0, false, nil
	}
	copied := map[string]string{}
	for k, s := range d.files[key] {
		copied[k] = s
	}
	return d.index[key], copied, v, true, nil
}

func (d *memoryDocs) Save(_ context.Context, key, index string, files map[string]string, base int64) (int64, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.down {
		return 0, false, errors.New("unreachable")
	}
	if d.conflicts > 0 {
		d.conflicts--
		return 0, true, nil
	}
	if d.version[key] != base {
		return 0, true, nil
	}
	d.saves++
	d.version[key]++
	d.index[key], d.files[key] = index, files
	return d.version[key], false, nil
}

func (d *memoryDocs) Projects(context.Context) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for k := range d.version {
		out = append(out, k)
	}
	return out, nil
}

func TestAutoFileName(t *testing.T) {
	for _, bad := range []string{"", "../x", "a/b", "MEMORY", "memory", "memory.md", "UPPER", ".hidden", "a b", strings.Repeat("x", 81)} {
		if _, err := AutoFileName(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	for in, want := range map[string]string{"build": "build.md", "build.md": "build.md", "a_b-1": "a_b-1.md"} {
		if got, err := AutoFileName(in); err != nil || got != want {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
}

func TestAutoStoreWritesFilesAndIndexAndTheNextSessionLoadsIt(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "mem")
	s := &AutoStore{Dir: dir, ProjectKey: "p"}
	if _, err := s.Write(ctx, "release", BuildAutoFile("Release flow", "How we ship\nwith two lines", "project", "Tag, then deploy.")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(ctx, "alpha.md", BuildAutoFile("Alpha", "Comes first", "user", "Body.")); err != nil {
		t.Fatal(err)
	}
	want := "- [Alpha](alpha.md) - Comes first\n- [Release flow](release.md) - How we ship with two lines\n"
	if got := s.Index(); got != want {
		t.Fatalf("index:\n%s", got)
	}
	got, err := s.Read("release")
	if err != nil || !strings.Contains(got, "Tag, then deploy.") || !strings.HasPrefix(got, "---\nname: Release flow\n") {
		t.Fatalf("%q %v", got, err)
	}
	files := s.List()
	if len(files) != 2 || files[1].Type != "project" || files[1].Title != "Release flow" {
		t.Fatalf("%+v", files)
	}

	l := New(Options{Cwd: t.TempDir(), Home: t.TempDir(), ManagedFiles: []string{}, AutoMemoryDir: dir, NoAncestors: true})
	var loaded string
	for _, in := range l.Initial() {
		if in.Origin == OriginAutoMemory {
			loaded = in.Content
		}
	}
	if loaded != strings.TrimSpace(want) {
		t.Fatalf("loaded %q", loaded)
	}

	// Empty dir option is the switch being off: nothing is loaded.
	off := New(Options{Cwd: t.TempDir(), Home: t.TempDir(), ManagedFiles: []string{}, NoAncestors: true})
	for _, in := range off.Initial() {
		if in.Origin == OriginAutoMemory {
			t.Fatal("loaded with the switch off")
		}
	}

	if err := s.Delete(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if s.Index() != "- [Release flow](release.md) - How we ship with two lines\n" {
		t.Fatalf("index after delete: %q", s.Index())
	}
	if err := s.Delete(ctx, "alpha"); err == nil {
		t.Fatal("deleting twice should say so")
	}
	if _, err := s.Read("alpha"); err == nil {
		t.Fatal("read of a deleted note")
	}
	if _, err := s.Write(ctx, "big", strings.Repeat("x", AutoMemoryMaxFileBytes+1)); err == nil {
		t.Fatal("too large")
	}
	if _, err := s.Write(ctx, "empty", "  "); err == nil {
		t.Fatal("empty")
	}
}

func TestParseAutoFileWithoutFrontmatter(t *testing.T) {
	f := ParseAutoFile("notes.md", "# Heading\n\nmore")
	if f.Description != "Heading" || f.Title != "notes" {
		t.Fatalf("%+v", f)
	}
}

func TestAutoStoreSyncsToOtherMachines(t *testing.T) {
	ctx := context.Background()
	docs := newMemoryDocs()
	a := &AutoStore{Dir: filepath.Join(t.TempDir(), "a"), ProjectKey: "p", Docs: docs}
	b := &AutoStore{Dir: filepath.Join(t.TempDir(), "b"), ProjectKey: "p", Docs: docs}
	if _, err := a.Write(ctx, "one", BuildAutoFile("One", "first", "user", "1")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(docs.index["p"], "one.md") || docs.files["p"]["one.md"] == "" {
		t.Fatalf("not sent: %q", docs.index["p"])
	}
	if HasPendingAutoMemory(a.Dir) {
		t.Fatal("nothing should be pending")
	}
	if err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Read("one"); err != nil || !strings.Contains(got, "first") {
		t.Fatalf("%q %v", got, err)
	}
	if !strings.Contains(b.Index(), "one.md") {
		t.Fatalf("index on b: %q", b.Index())
	}
	// A delete travels too.
	if err := a.Delete(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if len(b.List()) != 0 || strings.Contains(b.Index(), "one.md") {
		t.Fatalf("deleted note still on b: %+v", b.List())
	}
}

func TestAutoStoreOfflineKeepsChangesUntilTheServerIsBack(t *testing.T) {
	ctx := context.Background()
	docs := newMemoryDocs()
	s := &AutoStore{Dir: filepath.Join(t.TempDir(), "a"), ProjectKey: "p", Docs: docs}
	docs.down = true
	if _, err := s.Write(ctx, "offline", BuildAutoFile("Offline", "written offline", "user", "x")); err != nil {
		t.Fatalf("a server that is down must not fail the write: %v", err)
	}
	if !HasPendingAutoMemory(s.Dir) || !strings.Contains(s.Index(), "offline.md") {
		t.Fatal("the change should be on disk and pending")
	}
	if err := s.Sync(ctx); err == nil {
		t.Fatal("sync while down should say so")
	}
	docs.down = false
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if HasPendingAutoMemory(s.Dir) || !strings.Contains(docs.index["p"], "offline.md") {
		t.Fatalf("not sent: %q", docs.index["p"])
	}
}

func TestAutoStoreMergesAfterAConflict(t *testing.T) {
	ctx := context.Background()
	docs := newMemoryDocs()
	a := &AutoStore{Dir: filepath.Join(t.TempDir(), "a"), ProjectKey: "p", Docs: docs}
	b := &AutoStore{Dir: filepath.Join(t.TempDir(), "b"), ProjectKey: "p", Docs: docs}
	docs.down = true
	if _, err := a.Write(ctx, "from_a", BuildAutoFile("A", "a", "user", "a")); err != nil {
		t.Fatal(err)
	}
	docs.down = false
	if _, err := b.Write(ctx, "from_b", BuildAutoFile("B", "b", "user", "b")); err != nil {
		t.Fatal(err)
	}
	docs.conflicts = 1
	if err := a.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if docs.files["p"]["from_a.md"] == "" || docs.files["p"]["from_b.md"] == "" {
		t.Fatalf("both notes should survive: %v", docs.files["p"])
	}
	if !strings.Contains(a.Index(), "from_b.md") {
		t.Fatalf("a should have b's note: %q", a.Index())
	}
	docs.conflicts = 99
	if _, err := a.Write(ctx, "stuck", BuildAutoFile("S", "s", "user", "s")); err != nil {
		t.Fatalf("a losing race must not fail the write: %v", err)
	}
	if !HasPendingAutoMemory(a.Dir) {
		t.Fatal("what did not go up stays pending")
	}
}

func TestFilesFromBeforeTheServerGoUp(t *testing.T) {
	ctx := context.Background()
	docs := newMemoryDocs()
	dir := filepath.Join(t.TempDir(), "a")
	local := &AutoStore{Dir: dir, ProjectKey: "p"}
	if _, err := local.Write(ctx, "old", BuildAutoFile("Old", "from before", "user", "x")); err != nil {
		t.Fatal(err)
	}
	s := &AutoStore{Dir: dir, ProjectKey: "p", Docs: docs}
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if docs.files["p"]["old.md"] == "" {
		t.Fatalf("not uploaded: %v", docs.files)
	}
}

func TestLoaderPrefersPendingLocalIndexOverTheDocument(t *testing.T) {
	ctx := context.Background()
	docs := newMemoryDocs()
	dir := filepath.Join(t.TempDir(), "a")
	s := &AutoStore{Dir: dir, ProjectKey: "p", Docs: docs}
	docs.down = true
	if _, err := s.Write(ctx, "newest", BuildAutoFile("Newest", "offline", "user", "x")); err != nil {
		t.Fatal(err)
	}
	k, d := memDoc(commonv1.Scope_SCOPE_PROJECT, "p", "doc", "- [Older](older.md)")
	cache := &fakeDocs{synced: true, docs: map[string]*syncv1.Document{k: d}}
	load := func() string {
		l := New(Options{Cwd: t.TempDir(), Home: t.TempDir(), ManagedFiles: []string{}, AutoMemoryDir: dir, Docs: cache, ProjectKey: "p", NoAncestors: true})
		for _, in := range l.Initial() {
			if in.Origin == OriginAutoMemory {
				return in.Content
			}
		}
		return ""
	}
	if got := load(); !strings.Contains(got, "newest.md") {
		t.Fatalf("pending: %q", got)
	}
	docs.down = false
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := load(); !strings.Contains(got, "older.md") {
		t.Fatalf("after sync the cached document applies: %q", got)
	}
}

func TestLocalAutoMemoryProjects(t *testing.T) {
	home := t.TempDir()
	key := "github.com/a/b"
	s := &AutoStore{Dir: AutoMemoryDir(home, key), ProjectKey: key}
	if _, err := s.Write(context.Background(), "x", BuildAutoFile("X", "d", "user", "b")); err != nil {
		t.Fatal(err)
	}
	if got := LocalAutoMemoryProjects(home); len(got) != 1 || got[0] != key {
		t.Fatalf("%v", got)
	}
	if err := os.WriteFile(filepath.Join(home, "memory", "stray"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LocalAutoMemoryProjects(home); len(got) != 1 {
		t.Fatalf("%v", got)
	}
}
