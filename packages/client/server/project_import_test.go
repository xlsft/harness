package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/settings"
)

func projectFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestProjectImportOffer(t *testing.T) {
	root := projectFixture(t, map[string]string{
		"CLAUDE.md":             "Project text\n",
		".claude/settings.json": `{"permissions":{"allow":["Bash(make *)"]},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`,
	})
	s := &Server{}
	ctx := context.Background()
	scan := func() *agentv1.ScanProjectImportResponse {
		t.Helper()
		r, err := s.ScanProjectImport(ctx, connect.NewRequest(&agentv1.ScanProjectImportRequest{Path: root}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}

	r := scan()
	if !r.Offer || r.Dismissed || r.Trusted || r.Root != root || len(r.Items) != 2 {
		t.Fatalf("%+v", r)
	}
	var trust bool
	for _, it := range r.Items {
		if it.Status != agentv1.ClaudeImportStatus_CLAUDE_IMPORT_STATUS_NEW {
			t.Fatalf("%+v", it)
		}
		trust = trust || it.NeedsTrust
	}
	if !trust {
		t.Fatalf("the hooks and allow rules are not flagged: %+v", r.Items)
	}
	// Nothing took effect, and nothing was written, by asking.
	if _, err := os.Stat(filepath.Join(root, ".harness")); err == nil {
		t.Fatal("a scan wrote")
	}
	if st, _ := settings.Load(ctx, settings.Options{Cwd: root, Trusted: true}); len(st.Permissions.Allow)+len(st.Hooks) != 0 {
		t.Fatalf("took effect without consent: %+v", st)
	}

	// Dismissed: remembered for this folder, not for others; it can come back.
	if _, err := s.DismissProjectImport(ctx, connect.NewRequest(&agentv1.DismissProjectImportRequest{Path: root, Dismissed: true})); err != nil {
		t.Fatal(err)
	}
	if r := scan(); r.Offer || !r.Dismissed || len(r.Items) != 2 {
		t.Fatalf("%+v", r)
	}
	if _, err := s.DismissProjectImport(ctx, connect.NewRequest(&agentv1.DismissProjectImportRequest{Path: root})); err != nil {
		t.Fatal(err)
	}
	if !scan().Offer {
		t.Fatal("the offer did not come back")
	}

	// Imported: the files are there, and there is nothing left to offer.
	run, err := s.RunProjectImport(ctx, connect.NewRequest(&agentv1.RunProjectImportRequest{Path: root}))
	if err != nil {
		t.Fatal(err)
	}
	if run.Msg.Imported != 2 || run.Msg.Errors != 0 {
		t.Fatalf("%+v", run.Msg)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "AGENTS.md")); !strings.Contains(string(data), "Project text") {
		t.Fatalf("%q", data)
	}
	if r := scan(); r.Offer {
		t.Fatalf("offered again: %+v", r)
	}
	// The hooks and rules brought over still need the folder trusted.
	st, err := settings.Load(ctx, settings.Options{Cwd: root, Trusted: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Permissions.Allow) != 0 || len(st.Hooks) != 1 || st.Hooks[0].Gate != settings.HookGateWorkspace {
		t.Fatalf("%+v", st)
	}
}

func TestProjectImportNoOffer(t *testing.T) {
	s := &Server{}
	ctx := context.Background()
	offer := func(root string) bool {
		t.Helper()
		r, err := s.ScanProjectImport(ctx, connect.NewRequest(&agentv1.ScanProjectImportRequest{Path: root}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg.Offer
	}
	if offer(projectFixture(t, map[string]string{"README.md": "x"})) {
		t.Fatal("nothing to import")
	}
	if offer(projectFixture(t, map[string]string{"CLAUDE.md": "x", ".harness/settings.json": "{}"})) {
		t.Fatal("the folder has its own .harness")
	}
	if !offer(projectFixture(t, map[string]string{"CLAUDE.md": "x", ".harness/settings.local.json": "{}"})) {
		t.Fatal("an \"always allow\" of the client's does not count as its own .harness")
	}
	if offer(projectFixture(t, map[string]string{".claude/worktrees/w/CLAUDE.md": "x"})) {
		t.Fatal(".claude holding only worktrees has nothing to bring")
	}
	// The home folder's .claude is the user's.
	root := projectFixture(t, map[string]string{".claude/settings.json": "{}", "CLAUDE.md": "x"})
	t.Setenv("HOME", root)
	if offer(root) {
		t.Fatal("the home folder")
	}
}

// GetWorkspaceTrust says what trusting enables: what is under .claude enables nothing until it is imported.
func TestWorkspaceTrustIgnoresClaudeSettings(t *testing.T) {
	root := projectFixture(t, map[string]string{".claude/settings.json": `{"hooks":{"Stop":[]},"permissions":{"allow":["Bash"]}}`})
	r, err := (&Server{}).GetWorkspaceTrust(context.Background(), connect.NewRequest(&agentv1.GetWorkspaceTrustRequest{Path: root}))
	if err != nil {
		t.Fatal(err)
	}
	if r.Msg.HasProjectHooks || r.Msg.HasCommittedAllowRules {
		t.Fatalf("%+v", r.Msg)
	}
}
