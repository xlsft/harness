package syncclient

import (
	"bytes"
	"context"
	"testing"

	"github.com/xlsft/harness/packages/client/memory"
	"github.com/xlsft/harness/packages/client/syncclient/synctest"
)

func TestUploadArtifactSendsEveryChunkOnce(t *testing.T) {
	x := newObjectsFixture(t)
	c := NewClient(x.srv.URL, nil, nil)
	data := bytes.Repeat([]byte("artifact bytes "), 40) // Many chunks of the fake's 16 bytes.
	sha, err := c.UploadArtifact(context.Background(), "page.html", "text/html", data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(x.fake.blobs[sha], data) {
		t.Fatalf("%d bytes stored", len(x.fake.blobs[sha]))
	}
	// The same bytes again send nothing.
	before := x.fake.chunks
	again, err := c.UploadArtifact(context.Background(), "page.html", "text/html", data)
	if err != nil || again != sha {
		t.Fatalf("%s %v", again, err)
	}
	if x.fake.chunks != before {
		t.Fatal("known bytes should not go up again")
	}
	// An empty page has no chunks to send.
	if _, err := c.UploadArtifact(context.Background(), "empty.txt", "text/plain", nil); err != nil {
		t.Fatal(err)
	}
}

func TestAutoMemoryDocsOverSync(t *testing.T) {
	fs := synctest.New()
	srv := fs.Start()
	defer srv.Close()
	ctx := context.Background()
	docs := AutoMemoryDocs{API: NewClient(srv.URL, nil, nil)}

	if _, _, _, found, err := docs.Load(ctx, "p"); err != nil || found {
		t.Fatalf("%v %v", found, err)
	}
	v, conflict, err := docs.Save(ctx, "p", "index", map[string]string{"a.md": "A", "b.md": "B"}, 0)
	if err != nil || conflict || v == 0 {
		t.Fatalf("%d %v %v", v, conflict, err)
	}
	index, files, got, found, err := docs.Load(ctx, "p")
	if err != nil || !found || index != "index" || files["b.md"] != "B" || got != v {
		t.Fatalf("%q %v %d %v %v", index, files, got, found, err)
	}
	if _, conflict, err := docs.Save(ctx, "p", "stale", nil, v-1); err != nil || !conflict {
		t.Fatalf("a stale write must conflict: %v %v", conflict, err)
	}
	if _, conflict, err := docs.Save(ctx, "p", "new", nil, 0); err != nil || !conflict {
		t.Fatalf("create over an existing one must conflict: %v %v", conflict, err)
	}
	if _, _, err := docs.Save(ctx, "q", "other", nil, 0); err != nil {
		t.Fatal(err)
	}
	keys, err := docs.Projects(ctx)
	if err != nil || len(keys) != 2 {
		t.Fatalf("%v %v", keys, err)
	}

	// Two machines through the real document shape.
	a := &memory.AutoStore{Dir: t.TempDir(), ProjectKey: "shared", Docs: docs}
	b := &memory.AutoStore{Dir: t.TempDir(), ProjectKey: "shared", Docs: docs}
	if _, err := a.Write(ctx, "note", memory.BuildAutoFile("Note", "From a", "user", "hello")); err != nil {
		t.Fatal(err)
	}
	if err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if text, err := b.Read("note"); err != nil || text == "" {
		t.Fatalf("%q %v", text, err)
	}
}
