package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/syncclient/synctest"
)

const sessionID = "0b9f6c1e-4a2d-4f7e-9c3b-1d2e3f4a5b6c"

func fakeHome(t *testing.T) (claudeHome, claudeJSON, harnessHome string) {
	t.Helper()
	root := t.TempDir()
	claudeHome = filepath.Join(root, ".claude")
	files := map[string]string{
		"CLAUDE.md": "# Me\nPrefer small diffs.\n",
		"settings.json": `{
			"model": "opus", "outputStyle": "Explanatory", "apiKeyHelper": "~/bin/key.sh",
			"env": {"ANTHROPIC_API_KEY": "sk-ant-123", "GH": "ghp_abcdef", "EDITOR": "vim"},
			"permissions": {"allow": ["Bash(go test *)"], "deny": ["Read(./.env)"], "defaultMode": "acceptEdits"},
			"hooks": {"PostToolUse": [{"matcher": "Edit", "hooks": [{"type": "command", "command": "gofmt -w"}]}]}
		}`,
		"skills/pdf/SKILL.md":       "---\nname: pdf\ndescription: PDFs\n---\nUse it.",
		"skills/pdf/scripts/run.sh": "#!/bin/sh\necho hi\n",
		"skills/pdf/ref/notes.md":   "notes",
		"skills/pdf/.DS_Store":      "junk",
		"skills/broken/README.md":   "no skill file",
		"agents/reviewer.md":        "---\nname: code-reviewer\ndescription: reviews\n---\nReview.",
		"agents/plain.md":           "Plain agent.",
		"agents/nested/ignored.md":  "not top level",
		"commands/deploy.md":        "Deploy it.",
		"commands/git/commit.md":    "---\ndescription: commit\n---\nCommit.",
		"output-styles/terse.md":    "---\nname: Terse\n---\nBe terse.",
		"projects/-w/" + sessionID + ".jsonl": `{"type":"user","uuid":"a1","parentUuid":null,"cwd":"/w","sessionId":"` + sessionID + `","timestamp":"2026-10-01T09:00:00Z","message":{"role":"user","content":"hello"}}` + "\n" +
			`{"type":"assistant","uuid":"a2","parentUuid":"a1","sessionId":"` + sessionID + `","timestamp":"2026-10-01T09:00:05Z","message":{"role":"assistant","model":"m","content":[]}}` + "\n",
		"projects/-w/" + sessionID + "/subagents/agent-1.jsonl": `{"type":"user","uuid":"s1","isSidechain":true}` + "\n",
	}
	for name, content := range files {
		p := filepath.Join(claudeHome, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	os.Symlink("/etc/hosts", filepath.Join(claudeHome, "skills", "pdf", "hosts"))
	claudeJSON = filepath.Join(root, ".claude.json")
	os.WriteFile(claudeJSON, []byte(`{"theme":"dark","mcpServers":{"github":{"type":"http","url":"https://api.example.com/mcp","headers":{"Authorization":"Bearer x"}},"fs":{"description":"test","command":"npx","args":["fs"],"env":{"TOKEN":"t"}}}}`), 0o644)
	return claudeHome, claudeJSON, filepath.Join(root, ".harness")
}

func find(rep *Report, kind, key string) *Item {
	for i := range rep.Items {
		if rep.Items[i].Kind == kind && rep.Items[i].Key == key {
			return &rep.Items[i]
		}
	}
	return nil
}

func TestDryRun(t *testing.T) {
	ch, cj, hh := fakeHome(t)
	rep, err := Run(context.Background(), Options{ClaudeHome: ch, ClaudeJSON: cj, ProjectsDir: filepath.Join(hh, "projects"), DryRun: true, Sessions: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][2]string{{"memory", "user"}, {"settings", "user"}, {"permissions", "user"}, {"hooks", "user"}, {"skill", "pdf"},
		{"agent", "code-reviewer"}, {"agent", "plain"}, {"command", "deploy"}, {"command", "git:commit"}, {"output-style", "Terse"}} {
		it := find(rep, want[0], want[1])
		if it == nil || it.Status != StatusPlanned {
			t.Errorf("%v: %+v", want, it)
		}
	}
	if it := find(rep, "agent", "nested/ignored"); it != nil {
		t.Errorf("nested agents must be ignored: %+v", it)
	}
	if it := find(rep, "skill", "broken"); it == nil || it.Status != StatusSkipped {
		t.Errorf("broken skill: %+v", it)
	}
	if it := find(rep, "mcp", "fs"); it == nil || it.Status != StatusNoted || !strings.Contains(it.Note, "stdio npx") || !strings.Contains(it.Note, "1 env/header") {
		t.Errorf("mcp: %+v", it)
	}
	if it := find(rep, "session", "-w/"+sessionID+".jsonl"); it == nil || it.Status != StatusPlanned {
		t.Errorf("session: %+v", it)
	}
	if _, err := os.Stat(filepath.Join(hh, "projects")); !os.IsNotExist(err) {
		t.Error("dry run must not copy sessions")
	}
	var out bytes.Buffer
	rep.Print(&out)
	if !strings.Contains(out.String(), "KIND") || !strings.Contains(out.String(), "dry-run") {
		t.Errorf("table: %s", out.String())
	}
	if _, err := Run(context.Background(), Options{ClaudeHome: ch}); err == nil {
		t.Error("non-dry run without a server must fail")
	}
}

func TestImportUploads(t *testing.T) {
	ch, cj, hh := fakeHome(t)
	fs := synctest.New()
	srv := fs.Start()
	defer srv.Close()
	client := syncclient.NewClient(srv.URL, nil, nil)
	up := syncclient.NewUploader(syncclient.UploaderOptions{API: client, StateDir: filepath.Join(hh, "cache", "sync", "sessions"),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	opts := Options{ClaudeHome: ch, ClaudeJSON: cj, ProjectsDir: filepath.Join(hh, "projects"), Sessions: true, Putter: client, Queue: up, UploadSessions: true}
	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.HasErrors() {
		t.Fatalf("errors: %+v", rep.Items)
	}
	user := func(kind syncv1.DocumentKind, key string) *syncv1.Document {
		return fs.Document(&syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_USER, Kind: kind, Key: key})
	}
	if d := user(syncv1.DocumentKind_DOCUMENT_KIND_MEMORY, "user"); d == nil || !strings.Contains(d.GetContent(), "Prefer small diffs") || d.GetSource() != Source {
		t.Fatalf("memory: %v", d)
	}
	var settings map[string]any
	json.Unmarshal([]byte(user(syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, "user").GetContent()), &settings)
	env, _ := settings["env"].(map[string]any)
	if env["EDITOR"] != "vim" || env["ANTHROPIC_API_KEY"] != nil || env["GH"] != nil {
		t.Fatalf("secrets must be filtered: %v", env)
	}
	if settings["hooks"] != nil || settings["apiKeyHelper"] != nil || settings["model"] != "opus" {
		t.Fatalf("settings: %v", settings)
	}
	if p, _ := settings["permissions"].(map[string]any); p["defaultMode"] != "acceptEdits" || p["allow"] != nil {
		t.Fatalf("settings permissions: %v", p)
	}
	if it := find(rep, "settings", "user"); !strings.Contains(it.Note, "ANTHROPIC_API_KEY, GH") {
		t.Fatalf("secret note: %q", it.Note)
	}
	var rules map[string][]string
	json.Unmarshal([]byte(user(syncv1.DocumentKind_DOCUMENT_KIND_PERMISSION_RULES, "user").GetContent()), &rules)
	if len(rules["allow"]) != 1 || rules["deny"][0] != "Read(./.env)" {
		t.Fatalf("rules: %v", rules)
	}
	if d := user(syncv1.DocumentKind_DOCUMENT_KIND_HOOK, "user"); d == nil || !strings.Contains(d.GetContent(), "gofmt -w") {
		t.Fatalf("hooks: %v", d)
	}
	skill := user(syncv1.DocumentKind_DOCUMENT_KIND_SKILL, "pdf")
	if skill == nil || len(skill.GetFiles()) != 2 {
		t.Fatalf("skill files: %v", skill)
	}
	for _, f := range skill.GetFiles() {
		if f.GetPath() == "scripts/run.sh" && !f.GetExecutable() {
			t.Error("executable bit lost")
		}
		if f.GetPath() == "hosts" || f.GetPath() == ".DS_Store" {
			t.Errorf("unexpected file %s", f.GetPath())
		}
	}
	if it := find(rep, "skill", "pdf"); !strings.Contains(it.Note, "hosts (symlink)") {
		t.Errorf("skill note: %q", it.Note)
	}
	for _, k := range []string{"code-reviewer", "plain"} {
		if user(syncv1.DocumentKind_DOCUMENT_KIND_AGENT, k) == nil {
			t.Errorf("agent %s missing", k)
		}
	}
	if user(syncv1.DocumentKind_DOCUMENT_KIND_COMMAND, "git:commit") == nil || user(syncv1.DocumentKind_DOCUMENT_KIND_OUTPUT_STYLE, "Terse") == nil {
		t.Error("command / output style missing")
	}
	// Sessions: copied with layout, uploaded along with their subagents.
	dst := filepath.Join(hh, "projects", "-w", sessionID+".jsonl")
	if _, err := os.Stat(dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(hh, "projects", "-w", sessionID, "subagents", "agent-1.jsonl")); err != nil {
		t.Fatal("subagent transcript not copied")
	}
	ss := fs.Session(sessionID)
	if ss == nil || len(ss.Entries) != 3 || ss.Meta.GetInfo().GetCwd() != "/w" || ss.Meta.GetInfo().GetTitle() != "hello" {
		t.Fatalf("session upload: %+v", ss)
	}
	if it := find(rep, "session", "-w/"+sessionID+".jsonl"); it.Status != StatusUploaded {
		t.Fatalf("session item: %+v", it)
	}
	if it := find(rep, "session", "(subagent transcripts)"); it == nil || it.Status != StatusNoted {
		t.Fatalf("subagent item: %+v", it)
	}

	// A second run reports existing documents and skips copied sessions.
	rep, err = Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if it := find(rep, "memory", "user"); it.Status != StatusExists {
		t.Fatalf("second run: %+v", it)
	}
	if it := find(rep, "session", "-w/"+sessionID+".jsonl"); it.Status != StatusUploaded || !strings.Contains(it.Note, "already copied") {
		t.Fatalf("second run session: %+v", it)
	}
	threads := map[string]int{}
	for _, e := range fs.Session(sessionID).Entries {
		threads[e.GetAgentId()]++
	}
	if threads[""] != 2 || threads["1"] != 1 || len(threads) != 2 {
		t.Fatalf("session re-upload duplicated entries: %v", threads)
	}

	// --force overwrites; --include-secrets keeps env secrets.
	opts.Force, opts.IncludeSecrets, opts.Sessions = true, true, false
	rep, err = Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if it := find(rep, "memory", "user"); it.Status != StatusUploaded {
		t.Fatalf("force: %+v", it)
	}
	if !strings.Contains(user(syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, "user").GetContent(), "sk-ant-123") {
		t.Fatal("include-secrets")
	}
}

func TestIsSecret(t *testing.T) {
	cases := map[[2]string]bool{
		{"OPENAI_API_KEY", "x"}:       true,
		{"MY_TOKEN", "x"}:             true,
		{"DB_PASSWORD", "x"}:          true,
		{"FOO", "ghp_123"}:            true,
		{"FOO", "AKIAABCDEFGHIJKLMN"}: true,
		{"EDITOR", "vim"}:             false,
		{"NODE_ENV", "production"}:    false,
	}
	for in, want := range cases {
		if got := isSecret(in[0], in[1]); got != want {
			t.Errorf("%v: %v", in, got)
		}
	}
}
