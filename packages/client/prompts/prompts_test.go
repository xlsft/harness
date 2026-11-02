package prompts_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/rpc"
)

func TestLoaderCachingAndNotModified(t *testing.T) {
	srv, url := fakegateway.Start(t, nil)
	dir := t.TempDir()
	l := &prompts.Loader{Source: prompts.NewConnectSource(url, rpc.HTTPClient(url), auth.StaticToken("hat_x")), Dir: dir}
	ctx := context.Background()
	b, err := l.Get(ctx, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != srv.Bundle.Version || len(b.Tools) != 6 {
		t.Fatalf("bundle %s tools=%d", b.Version, len(b.Tools))
	}
	if _, err := os.Stat(filepath.Join(dir, "fake-model", b.Version+".json")); err != nil {
		t.Fatal("bundle not cached:", err)
	}
	b2, err := l.Get(ctx, "fake-model")
	if err != nil || b2.Version != b.Version {
		t.Fatal(err)
	}
	if n, nm, _ := srv.Stats(); n != 2 || nm != 1 {
		t.Fatalf("fetches=%d notModified=%d", n, nm)
	}
	// Server unreachable: the cached bundle is used.
	off := &prompts.Loader{Source: prompts.NewConnectSource("http://127.0.0.1:1", nil, nil), Dir: dir}
	b3, err := off.Get(ctx, "fake-model")
	if err != nil || b3.Version != b.Version {
		t.Fatalf("offline fallback: %v", err)
	}
	// Frozen version served from cache without a fetch.
	if fb, err := off.Frozen(ctx, "fake-model", b.Version); err != nil || fb.Version != b.Version {
		t.Fatalf("frozen: %v", err)
	}
}

func TestRenderAndBuildSystem(t *testing.T) {
	var warned []string
	out := prompts.Render("a={{a}} b={{ b }} c={{missing}}", map[string]string{"a": "1", "b": "2"}, func(n string) { warned = append(warned, n) })
	if out != "a=1 b=2 c=" || len(warned) != 1 || warned[0] != "missing" {
		t.Fatalf("render %q %v", out, warned)
	}
	b := fakegateway.DefaultBundle()
	sys := b.BuildSystem(map[string]string{"cwd": "/x", "platform": "linux", "shell": "bash", "os_version": "L", "date": "d", "is_git": "true", "model_display_name": "M", "model_id": "m"}, nil)
	if len(sys) != 3 {
		t.Fatalf("system blocks %d", len(sys))
	}
	if sys[0].Cache || !sys[1].Cache || sys[2].Cache {
		t.Fatalf("cache marker must be on the last static block: %+v", sys)
	}
	if sys[2].Text[:22] != "Working directory: /x\n" {
		t.Fatalf("dynamic section not rendered: %q", sys[2].Text)
	}
	defs := b.ToolDefs([]string{"Read", "Unknown", "Bash"})
	if len(defs) != 2 || defs[0].Name != "Read" || defs[1].Name != "Bash" {
		t.Fatalf("tool defs %+v", defs)
	}
}
