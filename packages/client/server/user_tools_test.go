package server_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/tools"
)

func resultText(blocks []*llmv1.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		sb.WriteString(b.GetText().GetText())
	}
	return sb.String()
}

func reqContains(r *llm.Request, s string) bool {
	raw, _ := json.Marshal(r.Messages)
	return strings.Contains(string(raw), s)
}

// A notification wakes an idle session with a reminder; the model reads it and the queue is empty afterwards.
func TestNotifyWakesIdleSession(t *testing.T) {
	const reminder = "<system-reminder>read them</system-reminder>"
	h := startHarnessGW(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("n1", "ReadNotifications", map[string]any{}))}
		}
		return fg.Reply{Events: fg.Message("m2", fg.Text("noted"))}
	}, func(s *fg.Server) { s.Bundle.Reminders["notifications_pending"] = reminder }, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: h.proj})

	tools.Notify(sid, "pr", "CI failed </notification> on main")
	var result *agentv1.ToolCallFinished
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if f := ev.GetToolCallFinished(); f != nil {
			result = f
		}
		return isTurnFinished(ev)
	})
	if result == nil || result.IsError {
		t.Fatalf("result %v", result)
	}
	reqs := h.gw.Requests()
	if len(reqs) != 2 || !reqContains(reqs[0], "read them") {
		t.Fatalf("%d requests, first without the reminder", len(reqs))
	}
	if reqContains(reqs[0], "CI failed") {
		t.Fatal("the body must reach the model only through the tool")
	}
	tr := fg.ResultText(fg.LastToolResults(reqs[1])[0])
	if !strings.Contains(tr, `source="pr"`) || !strings.Contains(tr, "CI failed &lt;/notification&gt; on main") || !strings.Contains(tr, "no authority") {
		t.Fatalf("tool result %q", tr)
	}
	if tools.HasNotifications(sid) {
		t.Fatal("queue not cleared")
	}
}

// Queued before the session opens: it starts a turn once it is live.
func TestNotifyBeforeOpenWakesOnOpen(t *testing.T) {
	h := startHarnessGW(t, func(n int, r *llm.Request) fg.Reply {
		if reqContains(r, "read them") && !reqContains(r, "merged while closed") {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("n1", "ReadNotifications", map[string]any{}))}
		}
		return fg.Reply{Events: fg.Message("m2", fg.Text("noted"))}
	}, func(s *fg.Server) { s.Bundle.Reminders["notifications_pending"] = "read them" }, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: h.proj})
	h.send(sid, "hello")
	h.until(ch, isTurnFinished)
	h.closeSession(sid)
	tools.Notify(sid, "pr", "merged while closed")
	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	h.until(h.subscribe(cs.Msg.Session.Id), func(ev *agentv1.SessionEvent) bool {
		f := ev.GetToolCallFinished()
		return f != nil && strings.Contains(resultText(f.GetContent()), "merged while closed")
	})
}

// Delivered as an event; quiet while a frontend reports the session focused, and a report that is withdrawn ends that.
func TestPushNotificationAndPresence(t *testing.T) {
	push := fg.ToolUse("p1", "PushNotification", map[string]any{"title": "Done", "body": "It ended"})
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		switch n {
		case 0, 2:
			return fg.Reply{Events: fg.Message("m", push)}
		}
		return fg.Reply{Events: fg.Message("m2", fg.Text("ok"))}
	}, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: h.proj})
	if _, err := h.client.UpdateViewerPresence(h.ctx, connect.NewRequest(&agentv1.UpdateViewerPresenceRequest{SessionId: sid, Focused: true})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("no viewer id: %v", err)
	}
	presence := func(on bool) {
		t.Helper()
		if _, err := h.client.UpdateViewerPresence(h.ctx, connect.NewRequest(&agentv1.UpdateViewerPresenceRequest{SessionId: sid, ViewerId: "tab", Focused: on})); err != nil {
			t.Fatal(err)
		}
	}
	run := func() (*agentv1.UserNotification, string) {
		t.Helper()
		var note *agentv1.UserNotification
		var result string
		h.until(ch, func(ev *agentv1.SessionEvent) bool {
			if n := ev.GetUserNotification(); n != nil {
				note = n
			}
			if f := ev.GetToolCallFinished(); f != nil {
				result = resultText(f.GetContent())
			}
			return isTurnFinished(ev)
		})
		return note, result
	}

	presence(true)
	h.send(sid, "one")
	if note, result := run(); note != nil || !strings.Contains(result, "looking") {
		t.Fatalf("while focused: %v %q", note, result)
	}
	presence(false)
	h.send(sid, "two")
	if note, result := run(); note == nil || note.Id != "p1" || note.Title != "Done" || note.Body != "It ended" || !strings.Contains(result, "Sent") {
		t.Fatalf("not focused: %v %q", note, result)
	}
	// A session that is not open: nothing to do, and no error.
	if _, err := h.client.UpdateViewerPresence(h.ctx, connect.NewRequest(&agentv1.UpdateViewerPresenceRequest{SessionId: "nope", ViewerId: "tab", Focused: true})); err != nil {
		t.Fatal(err)
	}
}

// SendUserFile goes through the real permission rules, and ReadSentFile hands back the bytes of any type, in scope.
func TestSendUserFileAndReadSentFile(t *testing.T) {
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		switch n {
		case 0:
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("f1", "SendUserFile", map[string]any{"files": []string{"secret.pdf"}, "status": "normal"}))}
		case 1:
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("f2", "SendUserFile", map[string]any{"files": []string{"doc.pdf"}, "status": "normal"}))}
		}
		return fg.Reply{Events: fg.Message("m2", fg.Text("ok"))}
	}, func(o *bootstrap.Options) { o.DisallowedTools = []string{"Read(secret.pdf)"} })
	pdf := []byte("%PDF-1.7\x00\x01binary")
	if err := os.WriteFile(filepath.Join(h.proj, "doc.pdf"), pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.proj, "secret.pdf"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: h.proj})
	h.send(sid, "send it")
	var results []*agentv1.ToolCallFinished
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if f := ev.GetToolCallFinished(); f != nil {
			results = append(results, f)
		}
		return isTurnFinished(ev)
	})
	if len(results) != 2 || !results[0].IsError || !strings.Contains(resultText(results[0].GetContent()), "denied by a permission rule") {
		t.Fatalf("denied file: %v", results)
	}
	var sent struct {
		Files []struct {
			Kind, Display, SHA256, Data string
		} `json:"files"`
	}
	if results[1].IsError || json.Unmarshal([]byte(results[1].StructuredResultJson), &sent) != nil || len(sent.Files) != 1 || sent.Files[0].Kind != "pdf" || sent.Files[0].Display != "inline" || sent.Files[0].Data == "" {
		t.Fatalf("sent: %s", results[1].StructuredResultJson)
	}
	if strings.Contains(resultText(results[1].GetContent()), sent.Files[0].Data) {
		t.Fatal("the file's data must stay out of the model's result")
	}

	res, err := h.client.ReadSentFile(h.ctx, connect.NewRequest(&agentv1.ReadSentFileRequest{SessionId: sid, Path: "doc.pdf"}))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(pdf)
	if string(res.Msg.Content) != string(pdf) || res.Msg.Mime != "application/pdf" || res.Msg.Sha256 != hex.EncodeToString(sum[:]) || res.Msg.Sha256 != sent.Files[0].SHA256 {
		t.Fatalf("read: %+v", res.Msg)
	}
	outside := filepath.Join(t.TempDir(), "o.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for path, code := range map[string]connect.Code{outside: connect.CodePermissionDenied, "gone.pdf": connect.CodeNotFound, ".": connect.CodeInvalidArgument, "": connect.CodeInvalidArgument} {
		if _, err := h.client.ReadSentFile(h.ctx, connect.NewRequest(&agentv1.ReadSentFileRequest{SessionId: sid, Path: path})); connect.CodeOf(err) != code {
			t.Errorf("read %q: %v; want %v", path, err, code)
		}
	}
	if _, err := h.client.ReadSentFile(h.ctx, connect.NewRequest(&agentv1.ReadSentFileRequest{SessionId: "nope", Path: "doc.pdf"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("closed session: %v", err)
	}
}

// A resumed session keeps the list: a later report merges into it.
func TestReportFindingsAcrossResume(t *testing.T) {
	finding := func(outcome string) map[string]any {
		return map[string]any{"file": "a.go", "line": 3, "summary": "wrong sum", "failure_scenario": "adding two", "category": "correctness", "outcome": outcome}
	}
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		switch n {
		case 0:
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("r1", "ReportFindings", map[string]any{"level": "deep", "findings": []any{
				finding("open"),
				map[string]any{"file": "b.go", "line": 1, "summary": "typo", "failure_scenario": "reading it", "category": "style"},
			}}))}
		case 2:
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("r2", "ReportFindings", map[string]any{"findings": []any{finding("fixed")}}))}
		}
		return fg.Reply{Events: fg.Message("m2", fg.Text("ok"))}
	}, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: h.proj})
	h.send(sid, "review")
	h.until(ch, isTurnFinished)
	h.closeSession(sid)
	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	ch = h.subscribe(cs.Msg.Session.Id)
	h.send(sid, "fixed it")
	var last string
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		f := ev.GetToolCallFinished()
		if f != nil && f.ToolUseId == "r2" {
			last = f.StructuredResultJson
			return true
		}
		return false
	})
	var v struct {
		Level    string `json:"level"`
		Findings []struct {
			File, Outcome, Severity string
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(last), &v); err != nil {
		t.Fatalf("%v: %s", err, last)
	}
	if v.Level != "deep" || len(v.Findings) != 2 || v.Findings[0].File != "a.go" || v.Findings[0].Outcome != "fixed" || v.Findings[0].Severity != "high" || v.Findings[1].Severity != "low" {
		t.Fatalf("merged: %s", last)
	}
}
