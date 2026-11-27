package server

import (
	"context"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/internal/gittest"
)

func TestSecretFile(t *testing.T) {
	for name, want := range map[string]bool{
		".env": true, "app/.env.local": true, ".env.example": false, "config/.env.sample": false, "certs/server.pem": true,
		"deploy/id_ed25519": true, ".npmrc": true, "src/main.go": false, "README.md": false, "keys.go": false,
	} {
		if got := secretFile(name); got != want {
			t.Errorf("secretFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSplitStatusV2(t *testing.T) {
	out := strings.Join([]string{
		"1 .M N... 100644 100644 100644 abc abc README.md",
		"1 D. N... 100644 000000 000000 abc 000 gone.txt",
		"2 R. N... 100644 100644 100644 abc abc R100 new name.txt", "old name.txt",
		"1 .M SC.. 160000 160000 160000 abc abc vendor/lib",
		"1 .M S... 160000 160000 160000 abc abc vendor/clean",
		"? untracked.txt",
		"",
	}, "\x00")
	got := splitStatusV2(out)
	want := []statusEntry{{path: "README.md"}, {path: "gone.txt", deleted: true}, {path: "new name.txt"}, {path: "vendor/lib", submodule: true}, {path: "vendor/clean"}, {path: "untracked.txt"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The move's commit carries the working tree, .gitignore holding, and leaves the user's checkout as it was.
func TestMoveCommitLeavesTheCheckout(t *testing.T) {
	repo := gittest.Init(t)
	gittest.Write(t, repo, ".gitignore", "build/\n")
	gittest.Run(t, repo, "add", ".gitignore")
	gittest.Run(t, repo, "commit", "-qm", "ignore")
	gittest.Write(t, repo, "README.md", "changed\n")
	gittest.Write(t, repo, "notes/new.txt", "new\n")
	gittest.Write(t, repo, "build/out.bin", "ignored\n")
	gittest.Run(t, repo, "add", "README.md")
	head := gittest.Run(t, repo, "rev-parse", "HEAD")
	before := gittest.Run(t, repo, "status", "--porcelain")

	s := &Server{}
	plan := &movePlan{top: repo, host: "box", title: "Fix the widget"}
	commit, err := s.moveCommit(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if commit == head {
		t.Fatal("no commit for the uncommitted work")
	}
	if got := gittest.Run(t, repo, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved: %s", got)
	}
	if got := gittest.Run(t, repo, "status", "--porcelain"); got != before {
		t.Errorf("status changed:\n%s\nwas\n%s", got, before)
	}
	if got := gittest.Run(t, repo, "rev-parse", commit+"^"); got != head {
		t.Errorf("parent = %s, want %s", got, head)
	}
	files := gittest.Run(t, repo, "ls-tree", "-r", "--name-only", commit)
	for _, f := range []string{"README.md", "notes/new.txt", ".gitignore"} {
		if !strings.Contains(files, f) {
			t.Errorf("%s is not in the commit: %s", f, files)
		}
	}
	if strings.Contains(files, "build/") {
		t.Errorf("an ignored file went up: %s", files)
	}
	if got := gittest.Run(t, repo, "show", commit+":README.md"); got != "changed" {
		t.Errorf("README.md = %q", got)
	}
	if msg := gittest.Run(t, repo, "log", "-1", "--format=%s", commit); msg != "Fix the widget" {
		t.Errorf("subject = %q", msg)
	}

	// Nothing uncommitted: the move pushes HEAD itself.
	gittest.Run(t, repo, "add", "-A")
	gittest.Run(t, repo, "commit", "-qm", "all")
	head = gittest.Run(t, repo, "rev-parse", "HEAD")
	if commit, err := s.moveCommit(context.Background(), plan); err != nil || commit != head {
		t.Errorf("clean tree: %s, %v; want %s", commit, err, head)
	}
}
