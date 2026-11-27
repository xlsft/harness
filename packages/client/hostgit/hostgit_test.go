package hostgit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheCredentialHelperAnswersFromTheEnvironmentOnly(t *testing.T) {
	cmd := exec.Command("git", "-c", "credential.helper=", "-c", "credential.helper="+credentialHelper, "credential", "fill")
	cmd.Env = append(env(false), "HARNESS_GIT_USERNAME=u", "HARNESS_GIT_PASSWORD=p")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=code.test\n\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "username=u\n") || !strings.Contains(string(out), "password=p\n") {
		t.Fatalf("fill: %s", out)
	}
	if strings.Contains(credentialHelper, "p\n") {
		t.Fatal("the helper holds a secret")
	}
}

func TestCheckRefNameRefusesOptionsAndExpressions(t *testing.T) {
	for _, bad := range []string{"", "-x", "a b", "a..b", "a:b", "a^", "a~1", "a?"} {
		if CheckRefName(bad) == nil {
			t.Errorf("%q passed", bad)
		}
	}
	if err := CheckRefName("harness/hwk_1"); err != nil {
		t.Error(err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestPushCommitNeverForcesAndDeleteTakesOnlyHarnessBranches(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	git(t, root, "init", "-q", "--bare", "-b", "main", bare)
	work := filepath.Join(root, "work")
	git(t, root, "clone", "-q", bare, work)
	git(t, work, "commit", "-q", "--allow-empty", "-m", "one")
	git(t, work, "push", "-q", "origin", "HEAD:main")
	first := git(t, work, "rev-parse", "HEAD")
	g := Git{AllowFile: true}
	cred := Credentials{CloneURL: "file://" + bare}
	if _, err := g.PushCommit(ctx, work, first, "main", "main", cred); err == nil {
		t.Fatal("pushed to the base")
	}
	if _, err := g.PushCommit(ctx, work, first, "feature", "main", cred); err == nil {
		t.Fatal("pushed outside harness/")
	}
	if pushed, err := g.PushCommit(ctx, work, first, "harness/s1", "main", cred); err != nil || !pushed {
		t.Fatalf("push: %v %v", pushed, err)
	}
	if pushed, err := g.PushCommit(ctx, work, first, "harness/s1", "main", cred); err != nil || pushed {
		t.Fatalf("again: %v %v", pushed, err)
	}
	// A commit that is not on top of what the branch has is refused: no force.
	git(t, work, "commit", "-q", "--allow-empty", "-m", "two")
	second := git(t, work, "rev-parse", "HEAD")
	if _, err := g.PushCommit(ctx, work, second, "harness/s1", "main", cred); err != nil {
		t.Fatal(err)
	}
	if _, err := g.PushCommit(ctx, work, first, "harness/s1", "main", cred); err == nil {
		t.Fatal("moved the branch back")
	}
	if err := g.DeleteBranch(ctx, work, "main", cred); err == nil {
		t.Fatal("deleted main")
	}
	if err := g.DeleteBranch(ctx, work, "harness/s1", cred); err != nil {
		t.Fatal(err)
	}
	if out := git(t, root, "--git-dir", bare, "branch", "--list", "harness/*"); out != "" {
		t.Fatalf("still there: %s", out)
	}
	if err := g.DeleteBranch(ctx, work, "harness/s1", cred); err != nil {
		t.Fatalf("a branch already gone: %v", err)
	}
}
