package vcshooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/hooks"
	"github.com/xlsft/harness/packages/client/settings"
)

// engine runs the given hooks block as project settings, with gate deciding whether it may run.
func engine(t *testing.T, gate settings.HookGate, block map[string]string) *hooks.Engine {
	t.Helper()
	cfg := map[string]any{}
	for ev, cmd := range block {
		cfg[ev] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd}}}}
	}
	raw, _ := json.Marshal(cfg)
	st := &settings.Settings{Hooks: []settings.HookLayer{{Source: settings.Source{Level: settings.LevelProject}, Raw: raw, Gate: gate,
		Hash: settings.ContentHash(raw)}}}
	e := hooks.New(hooks.Build(st, nil), hooks.Options{SessionID: "s1"})
	t.Cleanup(e.Close)
	return e
}

func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCreateReadsPlainPathAndJSON(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	plain := script(t, t.TempDir(), "create.sh", `cat > "$0.in"; mkdir -p "`+out+`/plain"; echo "chatter"; echo "`+out+`/plain"`+"\n")
	h := Of(engine(t, settings.HookGateNone, map[string]string{"WorktreeCreate": plain}))
	if !h.Isolates() {
		t.Fatal("a configured WorktreeCreate hook should isolate")
	}
	c, err := h.Create(context.Background(), CreateRequest{Source: src, Name: "one", BaseRevision: "@-"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Path != filepath.Join(out, "plain") || c.Cwd != c.Path || c.Name != "one" || c.Source != src {
		t.Fatalf("created %+v", c)
	}
	var in map[string]any
	data, _ := os.ReadFile(plain + ".in")
	if err := json.Unmarshal(data, &in); err != nil {
		t.Fatal(err)
	}
	if in["hook_event_name"] != "WorktreeCreate" || in["name"] != "one" || in["source_dir"] != src || in["cwd"] != src || in["worktree_kind"] != "session" ||
		in["base_revision"] != "@-" || in["session_id"] != "s1" {
		t.Fatalf("input %v", in)
	}

	sub := filepath.Join(out, "json", "pkg")
	jsonHook := script(t, t.TempDir(), "create.sh", `mkdir -p "`+sub+`"; printf '{"hookSpecificOutput":{"hookEventName":"WorktreeCreate","worktreePath":"%s","baseRevision":"abc","cwd":"%s"}}' "`+out+`/json" "`+sub+`"`+"\n")
	h = Of(engine(t, settings.HookGateNone, map[string]string{"WorktreeCreate": jsonHook}))
	c, err = h.Create(context.Background(), CreateRequest{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	if c.Path != filepath.Join(out, "json") || c.Cwd != sub || c.Base != "abc" || c.Name == "" {
		t.Fatalf("created %+v", c)
	}
}

func TestCreateFailures(t *testing.T) {
	src := t.TempDir()
	for _, tc := range []struct {
		name, body, want string
	}{
		{"exit", "echo broken >&2; exit 1\n", "broken"},
		{"no path", "true\n", "does not handle"},
		{"missing folder", "echo /nonexistent/vcs-hooks-test\n", "not a folder"},
		{"source itself", `echo "` + src + `"` + "\n", "source folder itself"},
		{"cwd outside", `printf '{"worktreePath":"%s","cwd":"/"}' "` + t.TempDir() + `"` + "\n", "not inside"},
		{"bad json", "echo '{nope'\n", "JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := Of(engine(t, settings.HookGateNone, map[string]string{"WorktreeCreate": script(t, t.TempDir(), "c.sh", tc.body)}))
			_, err := h.Create(context.Background(), CreateRequest{Source: src})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
		})
	}
	h := Of(engine(t, settings.HookGateNone, map[string]string{"WorktreeCreate": "true"}))
	if _, err := h.Create(context.Background(), CreateRequest{Source: src, Name: "../x"}); err == nil {
		t.Fatal("a name that is not one path segment should be refused")
	}
}

func TestGatesKeepHooksFromIsolating(t *testing.T) {
	for _, gate := range []settings.HookGate{settings.HookGateWorkspace, settings.HookGateManaged, settings.HookGateDevice} {
		h := Of(engine(t, gate, map[string]string{"WorktreeCreate": "true", "VcsStatus": "true"}))
		if h.Isolates() || h.Reports() {
			t.Errorf("gate %v: hooks that may not run should not replace git", gate)
		}
	}
	t.Setenv("HARNESS_NO_HOOKS", "1")
	if h := Of(engine(t, settings.HookGateNone, map[string]string{"WorktreeCreate": "true"})); h.Isolates() {
		t.Error("HARNESS_NO_HOOKS should turn the replacement off")
	}
	if (Hooks{}).Isolates() || (Hooks{}).Reports() {
		t.Error("no engine, no hooks")
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	rm := script(t, dir, "rm.sh", `cat > "$0.in"`+"\n")
	h := Of(engine(t, settings.HookGateNone, map[string]string{"WorktreeCreate": "true", "WorktreeRemove": rm}))
	if err := h.Remove(context.Background(), RemoveRequest{Path: "/x/copy", Source: dir, Name: "copy", Force: true}); err != nil {
		t.Fatal(err)
	}
	var in map[string]any
	data, _ := os.ReadFile(rm + ".in")
	_ = json.Unmarshal(data, &in)
	if in["worktree_path"] != "/x/copy" || in["force"] != true || in["name"] != "copy" || in["cwd"] != dir {
		t.Fatalf("input %v", in)
	}
	h = Of(engine(t, settings.HookGateNone, map[string]string{"WorktreeRemove": "echo no >&2; exit 2"}))
	if err := h.Remove(context.Background(), RemoveRequest{Path: "/x", Source: dir}); err == nil || !strings.Contains(err.Error(), "no") {
		t.Fatalf("a refusing hook should fail the removal: %v", err)
	}
	h = Of(engine(t, settings.HookGateNone, map[string]string{"WorktreeCreate": "true"}))
	if err := h.Remove(context.Background(), RemoveRequest{Path: "/x", Source: dir}); !errors.Is(err, ErrNoRemoveHook) {
		t.Fatalf("without a WorktreeRemove hook: %v", err)
	}
}

func TestStatusAndDiff(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "pkg")
	_ = os.MkdirAll(sub, 0o755)
	status := script(t, t.TempDir(), "st.sh", `printf '{"hookSpecificOutput":{"hookEventName":"VcsStatus","root":"%s","base":"@-","files":[{"path":"a.txt","status":"M","additions":1,"deletions":2},{"path":"%s/b.txt","status":"added"},{"path":"c.txt","status":"renamed","oldPath":"old/c.txt"}]}}' "`+root+`" "`+root+`"`+"\n")
	diff := script(t, t.TempDir(), "diff.sh", `cat > "$0.in"; printf 'diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-x\n+y\n'`+"\n")
	h := Of(engine(t, settings.HookGateNone, map[string]string{"VcsStatus": status, "VcsDiff": diff}))
	if !h.Reports() {
		t.Fatal("VcsStatus should report")
	}
	st, err := h.Status(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if st.Root != root || st.Base != "@-" || len(st.Files) != 3 {
		t.Fatalf("status %+v", st)
	}
	if f := st.Files[0]; f.Path != "a.txt" || f.Status != "modified" || *f.Additions != 1 || *f.Deletions != 2 {
		t.Errorf("file %+v", f)
	}
	if f := st.Files[1]; f.Path != "b.txt" || f.Status != "added" || f.Additions != nil {
		t.Errorf("file %+v", f)
	}
	if f := st.Files[2]; f.Status != "renamed" || f.OldPath != "old/c.txt" {
		t.Errorf("file %+v", f)
	}
	out, err := h.Diff(context.Background(), sub, []string{"a.txt"})
	if err != nil || !strings.HasPrefix(string(out), "diff --git a/a.txt") {
		t.Fatalf("diff %q %v", out, err)
	}
	data, _ := os.ReadFile(diff + ".in")
	if !strings.Contains(string(data), `"paths":["a.txt"]`) {
		t.Errorf("diff input %s", data)
	}

	jsonDiff := script(t, t.TempDir(), "diff.sh", `printf '{"diff":"diff --git a/z b/z\\n"}'`+"\n")
	h = Of(engine(t, settings.HookGateNone, map[string]string{"VcsDiff": jsonDiff}))
	if out, err := h.Diff(context.Background(), sub, nil); err != nil || string(out) != "diff --git a/z b/z\n" {
		t.Fatalf("json diff %q %v", out, err)
	}
}

func TestStatusRefusesAndPasses(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, body, want string
	}{
		{"escape", `printf '{"files":[{"path":"../x","status":"M"}]}'` + "\n", "not inside"},
		{"other root", `printf '{"root":"/somewhere/else","files":[]}'` + "\n", "does not hold"},
		{"plain", "echo M a.txt\n", "not JSON"},
		{"fails", "exit 3\n", "Exited with 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := Of(engine(t, settings.HookGateNone, map[string]string{"VcsStatus": script(t, t.TempDir(), "s.sh", tc.body)}))
			if _, err := h.Status(context.Background(), dir); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
		})
	}
	h := Of(engine(t, settings.HookGateNone, map[string]string{"VcsStatus": "true"}))
	if st, err := h.Status(context.Background(), dir); st != nil || err != nil {
		t.Fatalf("empty output is not this hook's folder: %v %v", st, err)
	}
}

func TestQuietEventsStayOutOfTheTranscriptUnlessTheyFail(t *testing.T) {
	cfg := `{"VcsStatus":[{"hooks":[{"type":"command","command":"true"}]}],"VcsDiff":[{"hooks":[{"type":"command","command":"exit 1"}]}]}`
	st := &settings.Settings{Hooks: []settings.HookLayer{{Source: settings.Source{Level: settings.LevelProject}, Raw: json.RawMessage(cfg)}}}
	var runs []hooks.Run
	e := hooks.New(hooks.Build(st, nil), hooks.Options{OnRun: func(r hooks.Run) { runs = append(runs, r) }})
	defer e.Close()
	h := Of(e)
	_, _ = h.Status(context.Background(), t.TempDir())
	if len(runs) != 0 {
		t.Fatalf("a VcsStatus that answered should not show: %+v", runs)
	}
	_, _ = h.Diff(context.Background(), t.TempDir(), nil)
	if len(runs) != 1 || runs[0].Status != hooks.RunError {
		t.Fatalf("a failing VcsDiff should show once: %+v", runs)
	}
}

func TestDetect(t *testing.T) {
	t.Setenv("P4CONFIG", "")
	for _, tc := range []struct{ marker, kind string }{{".jj", Jujutsu}, {".hg", Mercurial}, {".p4config", Perforce}} {
		root := t.TempDir()
		if strings.HasPrefix(tc.marker, ".p4") {
			_ = os.WriteFile(filepath.Join(root, tc.marker), nil, 0o644)
		} else {
			_ = os.Mkdir(filepath.Join(root, tc.marker), 0o755)
		}
		deep := filepath.Join(root, "a", "b")
		_ = os.MkdirAll(deep, 0o755)
		if kind, at := Detect(deep); kind != tc.kind || at != root {
			t.Errorf("%s: %q %q", tc.marker, kind, at)
		}
	}
	colocated := t.TempDir()
	_ = os.Mkdir(filepath.Join(colocated, ".jj"), 0o755)
	_ = os.Mkdir(filepath.Join(colocated, ".git"), 0o755)
	if kind, _ := Detect(colocated); kind != "" {
		t.Errorf("a repository git covers needs no hooks: %q", kind)
	}
	t.Setenv("P4CONFIG", ".p4env")
	p4 := t.TempDir()
	_ = os.WriteFile(filepath.Join(p4, ".p4env"), nil, 0o644)
	if kind, _ := Detect(p4); kind != Perforce {
		t.Errorf("P4CONFIG names the file: %q", kind)
	}
}

func TestNoticesOnce(t *testing.T) {
	n := &Notices{Path: filepath.Join(t.TempDir(), "vcs-notices.json")}
	if !n.First("/r") || n.First("/r") || !n.First("/s") {
		t.Fatal("each root once")
	}
	if (&Notices{Path: n.Path}).First("/r") {
		t.Fatal("the record outlives the process")
	}
}
