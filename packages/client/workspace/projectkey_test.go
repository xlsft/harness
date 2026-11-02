package workspace

import (
	"context"
	"strings"
	"testing"
)

func TestNormalizeRemote(t *testing.T) {
	cases := map[string]string{
		"https://github.com/xlsft/harness.git":           "github.com/xlsft/harness",
		"https://user:tok@GitHub.com:443/XLSFT/Harness/": "github.com/xlsft/harness",
		"git@github.com:xlsft/harness.git":               "github.com/xlsft/harness",
		"ssh://git@gitlab.example.com:2222/a/b/c.git":    "gitlab.example.com/a/b/c",
		"git://host.xz/path/to/repo":                     "host.xz/path/to/repo",
		"/srv/git/repo.git":                              "",
		"file:///srv/git/repo.git":                       "",
		`C:\repos\x`:                                     "",
		"":                                               "",
	}
	for in, want := range cases {
		if got := NormalizeRemote(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	if k := PathKey("/a/b"); !strings.HasPrefix(k, "path:") || len(k) != 5+64 {
		t.Errorf("path key %q", k)
	}
}

func TestProjectKeyGit(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	if _, err := run(ctx, dir, "git", "init", "-q"); err != nil {
		t.Skip("git unavailable")
	}
	if k := ProjectKey(ctx, dir); !strings.HasPrefix(k, "path:") {
		t.Fatalf("no remote: %q", k)
	}
	run(ctx, dir, "git", "remote", "add", "upstream", "git@github.com:Org/Repo.git")
	if k := ProjectKey(ctx, dir); k != "github.com/org/repo" {
		t.Fatalf("first remote: %q", k)
	}
	run(ctx, dir, "git", "remote", "add", "origin", "https://example.com/o/r.git")
	if k := ProjectKey(ctx, dir); k != "example.com/o/r" {
		t.Fatalf("origin: %q", k)
	}
}
