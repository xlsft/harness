package server_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/syncclient"
)

func evKind(ev *agentv1.SessionEvent) string {
	switch e := ev.Event.(type) {
	case *agentv1.SessionEvent_UserMessage:
		return "user"
	case *agentv1.SessionEvent_TurnStarted:
		return "turn"
	case *agentv1.SessionEvent_AssistantMessage:
		return "asst"
	case *agentv1.SessionEvent_ToolCallStarted:
		return "start:" + e.ToolCallStarted.ToolUseId
	case *agentv1.SessionEvent_ToolCallFinished:
		return "end:" + e.ToolCallFinished.ToolUseId
	case *agentv1.SessionEvent_TurnFinished:
		return "done"
	case *agentv1.SessionEvent_PermissionModeChanged:
		return "mode"
	case *agentv1.SessionEvent_SessionUpdated:
		return "updated"
	}
	return fmt.Sprintf("%T", ev.Event)
}

func transcriptPath(cwd, sid string) string {
	return session.Store{Root: config.ProjectsDir()}.Path(cwd, sid)
}

func titleEntries(t *testing.T, path string) (ai, custom []string) {
	t.Helper()
	entries, err := session.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch e.Type {
		case session.TypeAITitle:
			ai = append(ai, e.AITitle)
		case session.TypeCustomTitle:
			custom = append(custom, e.CustomTitle)
		}
	}
	return ai, custom
}

func (h *harness) closeSession(sid string) {
	h.t.Helper()
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) listed(cwd, sid string) *agentv1.SessionInfo {
	h.t.Helper()
	list, err := h.client.ListSessions(h.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{Cwd: cwd}))
	if err != nil {
		h.t.Fatal(err)
	}
	for _, s := range list.Msg.Sessions {
		if s.Id == sid {
			return s
		}
	}
	h.t.Fatalf("session %s not listed", sid)
	return nil
}

func TestResumeReplaysHistory(t *testing.T) {
	h := startHarness(t, fg.Sequence(
		func(req *llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("msg_a", fg.Text("Reading."), fg.ToolUse("t1", "Read", map[string]string{"file_path": filepath.Join(fg.Cwd(req), "notes.txt")}))}
		},
		func(req *llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("msg_b", fg.Text("It says hello."))}
		},
	), nil)
	repo := gittest.Init(t)
	gittest.Write(t, repo, "notes.txt", "hello\n")

	sid, ch := h.create(&agentv1.CreateSessionRequest{Cwd: repo})
	h.send(sid, "read notes.txt please")
	live := h.until(ch, isTurnFinished)
	h.closeSession(sid)

	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: repo, ResumeSessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	info := cs.Msg.Session
	if info.Id != sid || info.Title != "read notes.txt please" || info.Model != "fake-model" || info.GitBranch != "main" {
		t.Fatalf("resumed info: %+v", info)
	}
	if ds := info.DiffStats; ds == nil || ds.FilesChanged != 1 || ds.Additions != 1 || ds.Deletions != 0 {
		t.Fatalf("diff stats: %+v", info.DiffStats)
	}

	got := h.until(h.subscribe(sid), func(ev *agentv1.SessionEvent) bool { return ev.GetSessionUpdated() != nil })
	var ks []string
	for i, ev := range got {
		if ev.Seq != int64(i+1) || ev.SessionId != sid || ev.Timestamp == "" {
			t.Fatalf("event %d: seq %d session %q ts %q", i, ev.Seq, ev.SessionId, ev.Timestamp)
		}
		ks = append(ks, evKind(ev))
	}
	if s := strings.Join(ks, " "); s != "mode user turn asst start:t1 end:t1 asst done updated" {
		t.Fatalf("replayed: %s", s)
	}
	// The replay carries what the live stream carried.
	var liveUser *agentv1.UserMessage
	for _, ev := range live {
		if u := ev.GetUserMessage(); u != nil {
			liveUser = u
		}
	}
	if u := got[1].GetUserMessage(); u.Uuid != liveUser.Uuid || u.Content[0].GetText().GetText() != "read notes.txt please" {
		t.Fatalf("user message: %+v (live %+v)", u, liveUser)
	}
	if f := got[5].GetToolCallFinished(); f.IsError || !strings.Contains(f.Content[0].GetText().GetText(), "hello") || f.StructuredResultJson == "" {
		t.Fatalf("tool result: %+v", f)
	}
	if a := got[6].GetAssistantMessage(); a.MessageId != "msg_b" || a.Content[0].GetText().GetText() != "It says hello." {
		t.Fatalf("answer: %+v", a)
	}

	// A new turn continues the seq after the history.
	ch2 := h.subscribe(sid)
	h.send(sid, "thanks")
	next := h.until(ch2, isTurnFinished)
	for i := 1; i < len(next); i++ {
		if next[i].Seq != next[i-1].Seq+1 && next[i].GetAssistantDelta() == nil && next[i-1].GetAssistantDelta() == nil {
			t.Fatalf("gap at %d: %d -> %d", i, next[i-1].Seq, next[i].Seq)
		}
	}
}

func TestAITitleAfterFirstTurn(t *testing.T) {
	h := startHarnessGW(t, nil, func(gw *fg.Server) {
		gw.Models = []string{"fake-model", "fake-small"}
		gw.SmallModel = "fake-small"
		gw.Title = func(*llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("msg_t", fg.Text("\"Lexer parser bug.\""))}
		}
	}, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "fix the parser bug in lexer.go\nit breaks on unicode")
	var provisional bool
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if ev.GetSessionUpdated().GetTitle() == "fix the parser bug in lexer.go" {
			provisional = true
		}
		return isTurnFinished(ev)
	})
	if !provisional {
		t.Fatal("no provisional title before the turn finished")
	}
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetSessionUpdated().GetTitle() == "Lexer parser bug" })

	reqs := h.gw.TitleRequests()
	if len(reqs) != 1 {
		t.Fatalf("title requests: %d", len(reqs))
	}
	r := reqs[0]
	conv := r.Messages[0].Content[0].Text
	if r.Model != "fake-small" || r.Metadata.RequestClass != "auxiliary" || r.Metadata.SessionID != sid || len(r.Tools) != 0 ||
		!strings.Contains(conv, "fix the parser bug in lexer.go") || !strings.Contains(conv, "Assistant: ok") || !strings.Contains(conv, "<conversation>") {
		t.Fatalf("title request: model %s meta %+v conv %q", r.Model, r.Metadata, conv)
	}
	if n := len(h.gw.Requests()); n != 1 {
		t.Fatalf("main requests: %d", n)
	}

	// A second turn does not ask again.
	h.send(sid, "and the tokenizer too")
	h.until(ch, isTurnFinished)
	h.closeSession(sid)
	if n := len(h.gw.TitleRequests()); n != 1 {
		t.Fatalf("title requests after the second turn: %d", n)
	}
	path := transcriptPath(h.proj, sid)
	if ai, _ := titleEntries(t, path); len(ai) != 1 || ai[0] != "Lexer parser bug" {
		t.Fatalf("ai-title entries: %v", ai)
	}
	if got := h.listed(h.proj, sid).Title; got != "Lexer parser bug" {
		t.Fatalf("listed title: %q", got)
	}
	if got := h.listed("", sid).Title; got != "Lexer parser bug" {
		t.Fatalf("listed title (all projects): %q", got)
	}
	// The synced metadata derives the same title from the transcript.
	if got := syncclient.DeriveMeta(path).Title; got != "Lexer parser bug" {
		t.Fatalf("derived title: %q", got)
	}
	// Resume shows the recorded title and does not ask again.
	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid}))
	if err != nil || cs.Msg.Session.Title != "Lexer parser bug" {
		t.Fatalf("resume: %v %+v", err, cs)
	}
	ch = h.subscribe(sid)
	h.send(sid, "one more")
	h.until(ch, isTurnFinished)
	h.closeSession(sid)
	if n := len(h.gw.TitleRequests()); n != 1 {
		t.Fatalf("title requests after resume: %d", n)
	}
}

func TestAITitleUsesTitleSlot(t *testing.T) {
	h := startHarnessGW(t, nil, func(gw *fg.Server) {
		gw.Models = []string{"fake-model", "fake-small", "fake-title"}
		gw.TitleModel = "fake-title"
		gw.Title = func(*llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("msg_t", fg.Text("Lexer parser bug"))}
		}
	}, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "fix the parser bug in lexer.go")
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetSessionUpdated().GetTitle() == "Lexer parser bug" })
	reqs := h.gw.TitleRequests()
	if len(reqs) != 1 {
		t.Fatalf("title requests: %d", len(reqs))
	}
	if reqs[0].Model != "fake-title" {
		t.Fatalf("title model: %q", reqs[0].Model)
	}
	h.closeSession(sid)
}

func TestCustomTitleWins(t *testing.T) {
	h := startHarnessGW(t, nil, func(gw *fg.Server) {
		gw.Title = func(req *llm.Request) fg.Reply {
			// The user renames the session while the title is generated.
			ps, _ := filepath.Glob(filepath.Join(config.ProjectsDir(), "*", req.Metadata.SessionID+".jsonl"))
			if len(ps) == 1 {
				f, _ := os.OpenFile(ps[0], os.O_APPEND|os.O_WRONLY, 0o600)
				fmt.Fprintf(f, `{"type":"custom-title","customTitle":"My rename","sessionId":%q}`+"\n", req.Metadata.SessionID)
				f.Close()
			}
			return fg.Reply{Events: fg.Message("msg_t", fg.Text("AI name"))}
		}
	}, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "refactor the config loader")
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetSessionUpdated().GetTitle() == "My rename" })
	h.closeSession(sid)
	path := transcriptPath(h.proj, sid)
	if ai, custom := titleEntries(t, path); len(ai) != 0 || len(custom) != 1 {
		t.Fatalf("title entries: ai %v custom %v", ai, custom)
	}
	if got := h.listed(h.proj, sid).Title; got != "My rename" {
		t.Fatalf("listed: %q", got)
	}

	// A session whose transcript already has a custom title (and an older
	// AI title) resumes with the custom one and never asks for another.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	fmt.Fprintf(f, `{"type":"ai-title","aiTitle":"Old AI","sessionId":%q}`+"\n", sid)
	f.Close()
	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid}))
	if err != nil || cs.Msg.Session.Title != "My rename" {
		t.Fatalf("resume: %v %+v", err, cs)
	}
	ch = h.subscribe(sid)
	h.send(sid, "continue")
	h.until(ch, isTurnFinished)
	h.closeSession(sid)
	if n := len(h.gw.TitleRequests()); n != 1 {
		t.Fatalf("title requests: %d", n)
	}
	if got := h.listed("", sid).Title; got != "My rename" {
		t.Fatalf("listed after resume: %q", got)
	}
}

func TestTitleFailureKeepsProvisional(t *testing.T) {
	// No Title script: the fake gateway fails title requests.
	h := startHarness(t, nil, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	// Too little to go on: the title waits for the second turn.
	h.send(sid, "hi")
	h.until(ch, isTurnFinished)
	h.send(sid, "please explain the retry policy in gateway.go")
	h.until(ch, isTurnFinished)
	deadline := time.Now().Add(10 * time.Second)
	for len(h.gw.TitleRequests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	reqs := h.gw.TitleRequests()
	if len(reqs) != 1 || !strings.Contains(reqs[0].Messages[0].Content[0].Text, "retry policy") {
		t.Fatalf("title requests: %d", len(reqs))
	}
	h.closeSession(sid) // waits for the title attempt
	if n := len(h.gw.TitleRequests()); n != 1 {
		t.Fatalf("title requests: %d", n)
	}
	if ai, _ := titleEntries(t, transcriptPath(h.proj, sid)); len(ai) != 0 {
		t.Fatalf("ai-title entries: %v", ai)
	}
	if got := h.listed(h.proj, sid).Title; got != "hi" {
		t.Fatalf("listed: %q", got)
	}
}
