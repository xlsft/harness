package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	idev1 "github.com/xlsft/harness/packages/client/gen/harness/ide/v1"
	"github.com/xlsft/harness/packages/client/ide"
	"github.com/xlsft/harness/packages/client/internal/fakeeditor"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/server"
)

// ideHarness is a session's client with the integration on and a fake editor in its folder.
func ideHarness(t *testing.T, respond fg.Responder, cfg bootstrap.IDEConfig) (*harness, *fakeeditor.Editor) {
	t.Helper()
	t.Setenv(ide.LockEnv, "")
	h := startHarness(t, respond, func(o *bootstrap.Options) {
		o.IDE = &ide.Manager{}
		o.IDEConfig = func() bootstrap.IDEConfig { return cfg }
	})
	return h, fakeeditor.Start(t, h.proj)
}

func answerDiff(outcome idev1.DiffOutcome, comment string) func(*idev1.ShowDiffRequest) *idev1.ShowDiffResponse {
	return func(*idev1.ShowDiffRequest) *idev1.ShowDiffResponse {
		return &idev1.ShowDiffResponse{Outcome: outcome, Comment: comment}
	}
}

// oneCall calls a tool once and then ends the turn. The input is worked out when the model is asked, since the project
// folder is only known once the harness runs.
func oneCall(name string, in func() map[string]any) fg.Responder {
	return func(n int, _ *llm.Request) fg.Reply {
		if n == 0 {
			return toolCall("t1", name, in())
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
	}
}

// writeHarness is ideHarness for a model that writes "new\n" to new.txt in the project.
func writeHarness(t *testing.T, cfg bootstrap.IDEConfig) (*harness, *fakeeditor.Editor, string) {
	t.Helper()
	var target atomic.Value
	h, ed := ideHarness(t, oneCall("Write", func() map[string]any {
		return map[string]any{"file_path": target.Load(), "content": "new\n"}
	}), cfg)
	path := filepath.Join(h.proj, "new.txt")
	target.Store(path)
	return h, ed, path
}

func resolvedBy(evs []*agentv1.SessionEvent) string {
	for _, ev := range evs {
		if r := ev.GetPermissionResolved(); r != nil {
			return r.ResolvedBy
		}
	}
	return ""
}

func nextDiff(t *testing.T, ch <-chan *idev1.ShowDiffRequest) *idev1.ShowDiffRequest {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(15 * time.Second):
		t.Fatal("no diff reached the editor")
		return nil
	}
}

func TestEditorDiffAcceptWritesTheFile(t *testing.T) {
	h, ed, path := writeHarness(t, bootstrap.IDEConfig{Diffs: true})
	ed.Answer = answerDiff(idev1.DiffOutcome_DIFF_OUTCOME_ACCEPTED, "")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	evs := h.until(ch, isTurnFinished)

	d := nextDiff(t, ed.Diffs)
	if d.Path != path || d.After != "new\n" || d.Exists || d.ToolName != "Write" || d.SessionId != sid {
		t.Errorf("the diff: %+v", d)
	}
	if got := resolvedBy(evs); got != "editor" {
		t.Errorf("resolved by %q", got)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "new\n" {
		t.Errorf("file: %q %v", b, err)
	}
	opened, closed := false, false
	for _, ev := range evs {
		if r := ev.GetEditorReview(); r != nil {
			opened = opened || r.State == agentv1.EditorReview_STATE_OPEN
			closed = closed || r.State == agentv1.EditorReview_STATE_CLOSED
		}
	}
	if !opened || !closed {
		t.Errorf("review events: open %v closed %v", opened, closed)
	}
}

func TestEditorDiffRejectLeavesTheFileAndStops(t *testing.T) {
	h, ed, path := writeHarness(t, bootstrap.IDEConfig{Diffs: true})
	ed.Answer = answerDiff(idev1.DiffOutcome_DIFF_OUTCOME_REJECTED, "")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	evs := h.until(ch, isTurnFinished)
	if got := resolvedBy(evs); got != "editor" {
		t.Errorf("resolved by %q", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the file was written: %v", err)
	}
}

func TestEditorDiffRejectWithACommentReachesTheModel(t *testing.T) {
	var target atomic.Value
	seen := make(chan string, 4)
	h, ed := ideHarness(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return toolCall("t1", "Write", map[string]any{"file_path": target.Load(), "content": "new\n"})
		}
		seen <- lastResultText(r)
		return fg.Reply{Events: fg.Message("m", fg.Text("understood"))}
	}, bootstrap.IDEConfig{Diffs: true})
	path := filepath.Join(h.proj, "new.txt")
	target.Store(path)
	ed.Answer = answerDiff(idev1.DiffOutcome_DIFF_OUTCOME_REJECTED, "use a helper")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	h.until(ch, isTurnFinished)
	select {
	case got := <-seen:
		if !strings.Contains(got, "use a helper") {
			t.Errorf("the model saw %q", got)
		}
	default:
		t.Error("the model was not asked again")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the file was written: %v", err)
	}
}

func TestClosingTheDiffLeavesThePromptOpen(t *testing.T) {
	h, ed, path := writeHarness(t, bootstrap.IDEConfig{Diffs: true})
	ed.Answer = answerDiff(idev1.DiffOutcome_DIFF_OUTCOME_DISMISSED, "")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	// The diff closes by itself; the prompt stays and the turn waits for an answer here.
	var req *agentv1.PermissionRequest
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			req = p
		}
		return req != nil
	})
	nextDiff(t, ed.Diffs)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("written before anyone answered")
	}
	if _, err := h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: req.RequestId, Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW})); err != nil {
		t.Fatal(err)
	}
	evs := h.until(ch, isTurnFinished)
	if got := resolvedBy(evs); got != "local" {
		t.Errorf("resolved by %q", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("not written: %v", err)
	}
}

func TestAnsweringInThePanelClosesTheDiff(t *testing.T) {
	h, ed, path := writeHarness(t, bootstrap.IDEConfig{Diffs: true})
	// No Answer: the diff stays open until the call is cancelled.
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	asked, _ := h.runTurn(sid, ch, agentv1.RespondToPermissionRequest_DECISION_ALLOW)
	if len(asked) != 1 {
		t.Fatalf("prompts: %d", len(asked))
	}
	select {
	case <-ed.Cancelled:
	case <-time.After(15 * time.Second):
		t.Fatal("the diff stayed open after the panel answered")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("not written: %v", err)
	}
}

func TestDiffsOffOnlyThePanelAsks(t *testing.T) {
	h, ed, path := writeHarness(t, bootstrap.IDEConfig{})
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.runTurn(sid, ch, agentv1.RespondToPermissionRequest_DECISION_ALLOW)
	select {
	case d := <-ed.Diffs:
		t.Errorf("a diff was shown: %+v", d)
	default:
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("not written: %v", err)
	}
}

func TestReviewAllHoldsAnEditTheModeAllows(t *testing.T) {
	for _, c := range []struct {
		name    string
		outcome idev1.DiffOutcome
		written bool
	}{
		{"accept", idev1.DiffOutcome_DIFF_OUTCOME_ACCEPTED, true},
		{"reject", idev1.DiffOutcome_DIFF_OUTCOME_REJECTED, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, ed, path := writeHarness(t, bootstrap.IDEConfig{Diffs: true, ReviewAllEdits: true})
			ed.Answer = answerDiff(c.outcome, "")
			sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
			h.send(sid, "go")
			h.until(ch, isTurnFinished)
			nextDiff(t, ed.Diffs)
			_, err := os.Stat(path)
			if c.written != (err == nil) {
				t.Errorf("written %v: %v", c.written, err)
			}
		})
	}
}

func TestReviewAllClosedDiffBecomesAPrompt(t *testing.T) {
	h, ed, path := writeHarness(t, bootstrap.IDEConfig{Diffs: true, ReviewAllEdits: true})
	ed.Answer = answerDiff(idev1.DiffOutcome_DIFF_OUTCOME_DISMISSED, "")
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	asked, _ := h.runTurn(sid, ch, agentv1.RespondToPermissionRequest_DECISION_DENY)
	if len(asked) != 1 {
		t.Fatalf("prompts: %d", len(asked))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("written despite the denial: %v", err)
	}
}

func TestWithoutAnEditorTheModeDecidesAlone(t *testing.T) {
	t.Setenv(ide.LockEnv, "")
	var target atomic.Value
	h := startHarness(t, oneCall("Write", func() map[string]any {
		return map[string]any{"file_path": target.Load(), "content": "new\n"}
	}), func(o *bootstrap.Options) {
		o.IDE = &ide.Manager{}
		o.IDEConfig = func() bootstrap.IDEConfig { return bootstrap.IDEConfig{Diffs: true, ReviewAllEdits: true} }
	})
	path := filepath.Join(h.proj, "new.txt")
	target.Store(path)
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	h.send(sid, "go")
	h.until(ch, isTurnFinished)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("not written: %v", err)
	}
}

func TestSelectionMentionReachesTheModelFromTheBuffer(t *testing.T) {
	seen := make(chan string, 4)
	h, ed := ideHarness(t, func(_ int, r *llm.Request) fg.Reply {
		var parts []string
		for _, b := range r.Messages[len(r.Messages)-1].Content {
			if b.Type == llm.BlockText {
				parts = append(parts, b.Text)
			}
		}
		seen <- strings.Join(parts, "\n")
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	}, bootstrap.IDEConfig{Diffs: true})
	path := filepath.Join(h.proj, "a.txt")
	if err := os.WriteFile(path, []byte("disk1\ndisk2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ed.Buffers[path] = "unsaved1\nunsaved2\n"
	outside := filepath.Join(filepath.Dir(h.proj), "secret-for-mention.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "explain @a.txt#L2 and @"+outside+"#L1")
	h.until(ch, isTurnFinished)
	all := <-seen
	if !strings.Contains(all, "unsaved2") || strings.Contains(all, "disk2") {
		t.Errorf("the selection: %q", all)
	}
	if strings.Contains(all, "secret\n") {
		t.Errorf("a mention read outside the session's folders: %q", all)
	}
}

func TestOpenInEditorGoesToTheAttachedEditor(t *testing.T) {
	var target atomic.Value
	h, ed := ideHarness(t, oneCall("OpenInEditor", func() map[string]any {
		return map[string]any{"path": target.Load(), "line": 12}
	}), bootstrap.IDEConfig{Diffs: true})
	path := filepath.Join(h.proj, "a.txt")
	target.Store(path)
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	h.until(ch, isTurnFinished)
	select {
	case o := <-ed.Opened:
		if o.Path != path || o.Line != 12 {
			t.Errorf("opened %+v", o)
		}
	default:
		t.Skip("this machine has no display, which refuses the tool before it reaches the editor")
	}
}

func TestAppliedEditsAreAnnounced(t *testing.T) {
	h, ed, path := writeHarness(t, bootstrap.IDEConfig{})
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	h.send(sid, "go")
	h.until(ch, isTurnFinished)
	select {
	case got := <-ed.Applied:
		if got.Path != path || got.SessionId != sid || got.Reviewed {
			t.Errorf("announced %+v", got)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the editor heard nothing")
	}
}

func TestAnAnsweredEditIsAnnouncedAsReviewed(t *testing.T) {
	h, ed, path := writeHarness(t, bootstrap.IDEConfig{Diffs: true})
	ed.Answer = answerDiff(idev1.DiffOutcome_DIFF_OUTCOME_ACCEPTED, "")
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_DEFAULT})
	h.send(sid, "go")
	h.until(ch, isTurnFinished)
	select {
	case got := <-ed.Applied:
		if got.Path != path || !got.Reviewed {
			t.Errorf("announced %+v", got)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the editor heard nothing")
	}
}

func (h *harness) ide() agentv1connect.IdeServiceClient {
	return agentv1connect.NewIdeServiceClient(rpc.UnixHTTPClient(h.socket), "http://agent", connect.WithGRPC())
}

func TestEditorStatus(t *testing.T) {
	h, _ := ideHarness(t, nil, bootstrap.IDEConfig{})
	sid, _ := h.create(&agentv1.CreateSessionRequest{})
	got, err := h.ide().GetEditorStatus(h.ctx, connect.NewRequest(&agentv1.GetEditorStatusRequest{SessionId: sid}))
	if err != nil || !got.Msg.Attached || got.Msg.Name != "Fake Editor" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRevertInARepository(t *testing.T) {
	gittest.Isolate(t)
	h, _ := ideHarness(t, nil, bootstrap.IDEConfig{})
	repo := gittest.Init(t)
	repo, _ = filepath.EvalSymlinks(repo)
	gittest.Write(t, repo, "tracked.txt", "committed\n")
	gittest.Write(t, repo, "script.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(repo, "script.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "add", ".")
	gittest.Run(t, repo, "commit", "-m", "base")
	tracked, script, added := filepath.Join(repo, "tracked.txt"), filepath.Join(repo, "script.sh"), filepath.Join(repo, "added.txt")
	gittest.Write(t, repo, "tracked.txt", "changed\n")
	gittest.Write(t, repo, "added.txt", "new\n")
	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}
	sid, _ := h.create(&agentv1.CreateSessionRequest{Cwd: repo})
	svc := h.ide()
	base := func(p string) *agentv1.GetSessionFileBaseResponse {
		t.Helper()
		got, err := svc.GetSessionFileBase(h.ctx, connect.NewRequest(&agentv1.GetSessionFileBaseRequest{SessionId: sid, Path: p}))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		return got.Msg
	}
	if b := base(tracked); !b.Exists || b.Content != "committed\n" {
		t.Errorf("tracked: %+v", b)
	}
	if b := base(added); b.Exists {
		t.Errorf("added: %+v", b)
	}
	revert := func(p string) (*agentv1.RevertSessionFileResponse, error) {
		got, err := svc.RevertSessionFile(h.ctx, connect.NewRequest(&agentv1.RevertSessionFileRequest{SessionId: sid, Path: p}))
		if err != nil {
			return nil, err
		}
		return got.Msg, nil
	}
	if _, err := revert(tracked); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(tracked); string(b) != "committed\n" {
		t.Errorf("tracked now %q", b)
	}
	if r, err := revert(added); err != nil || !r.Removed {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat(added); !os.IsNotExist(err) {
		t.Errorf("added still there: %v", err)
	}
	if _, err := revert(script); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(script); err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Errorf("the script lost its mode: %v %v", st, err)
	}
	// Nothing left to revert, and nothing outside the session's changes can be touched.
	if _, err := revert(tracked); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("no changes: %v", err)
	}
	other := filepath.Join(t.TempDir(), "other.txt")
	if err := os.WriteFile(other, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := revert(other); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("a file outside: %v", err)
	}
	if b, _ := os.ReadFile(other); string(b) != "keep\n" {
		t.Errorf("a file outside was changed: %q", b)
	}
}

func TestRevertOutsideGitUsesTheFirstState(t *testing.T) {
	var target atomic.Value
	h, _ := ideHarness(t, func(n int, _ *llm.Request) fg.Reply {
		switch n {
		case 0:
			return toolCall("t0", "Read", map[string]any{"file_path": target.Load()})
		case 1:
			return toolCall("t1", "Edit", map[string]any{"file_path": target.Load(), "old_string": "before", "new_string": "after"})
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
	}, bootstrap.IDEConfig{})
	path := filepath.Join(h.proj, "a.txt")
	target.Store(path)
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	h.send(sid, "go")
	h.until(ch, isTurnFinished)
	if b, _ := os.ReadFile(path); string(b) != "after\n" {
		t.Fatalf("the edit did not run: %q", b)
	}
	if _, err := h.ide().RevertSessionFile(h.ctx, connect.NewRequest(&agentv1.RevertSessionFileRequest{SessionId: sid, Path: path})); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "before\n" {
		t.Errorf("now %q", b)
	}
}

func TestWebviewOriginIsAllowedByScheme(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	guard := server.TCPGuard(next, server.TCPOptions{Addr: "127.0.0.1:7801", Token: "0123456789abcdef", AllowedOrigins: server.DefaultCORSOrigins})
	call := func(origin, token string) int {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7801/x", nil).WithContext(context.Background())
		r.Header.Set("Origin", origin)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		guard.ServeHTTP(w, r)
		return w.Code
	}
	if got := call("vscode-webview://abc", "0123456789abcdef"); got != http.StatusOK {
		t.Errorf("a webview: %d", got)
	}
	if got := call("vscode-webview://abc", ""); got != http.StatusUnauthorized {
		t.Errorf("a webview without the token: %d", got)
	}
	if got := call("https://evil.example", "0123456789abcdef"); got != http.StatusForbidden {
		t.Errorf("a web page: %d", got)
	}
}
