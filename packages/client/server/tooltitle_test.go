package server_test

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/server"
	"github.com/xlsft/harness/packages/client/session"
)

// bashTurn scripts one turn that calls Bash twice in parallel, then answers.
func bashTurn() fg.Responder {
	return fg.Sequence(
		func(*llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("msg_a",
				fg.ToolUse("b1", "Bash", map[string]any{"command": "echo one", "description": "Print one"}),
				fg.ToolUse("b2", "Bash", map[string]any{"command": "echo two"}),
				fg.ToolUse("r1", "Glob", map[string]any{"pattern": "*.go"}),
			)}
		},
		func(*llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("msg_b", fg.Text("done"))} },
	)
}

func toolTitleReply(req *llm.Request) fg.Reply {
	text := req.Messages[0].Content[0].Text
	if strings.Contains(text, "echo one") {
		return fg.Reply{Events: fg.Message("msg_t1", fg.Text("Running Number one printout."))}
	}
	return fg.Reply{Events: fg.Message("msg_t2", fg.Text("\"Echo of two\""))}
}

func TestToolTitleForBash(t *testing.T) {
	h := startHarnessSrv(t, bashTurn(), func(gw *fg.Server) {
		gw.Models = []string{"fake-model", "fake-small"}
		gw.SmallModel = "fake-small"
		gw.ToolTitle = toolTitleReply
	}, func(o *bootstrap.Options) { o.AllowedTools = []string{"Bash", "Glob"} }, func(s *server.Server) { s.NoAITitles = false })
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "print some numbers")
	titles := map[string]string{}
	finished := false
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if tt := ev.GetToolTitle(); tt != nil {
			titles[tt.ToolUseId] = tt.Title
		}
		if isTurnFinished(ev) {
			finished = true
		}
		return finished && len(titles) == 2
	})
	if titles["b1"] != "number one printout" || titles["b2"] != "echo of two" {
		t.Fatalf("titles: %v", titles)
	}
	reqs := h.gw.ToolTitleRequests()
	if len(reqs) != 2 {
		t.Fatalf("tool title requests: %d", len(reqs))
	}
	for _, r := range reqs {
		text := r.Messages[0].Content[0].Text
		if r.Model != "fake-small" || r.Metadata.RequestClass != "auxiliary" || r.Metadata.SessionID != sid || len(r.Tools) != 0 ||
			!strings.Contains(text, "<command>") {
			t.Fatalf("request: model %s meta %+v text %q", r.Model, r.Metadata, text)
		}
		if strings.Contains(text, "echo one") && !strings.Contains(text, "<description>Print one</description>") {
			t.Fatalf("description missing: %q", text)
		}
	}
	if n := len(h.gw.Requests()); n != 2 {
		t.Fatalf("main requests: %d", n)
	}
	h.closeSession(sid)

	// Persisted as tool-title entries.
	entries, err := session.Load(transcriptPath(h.proj, sid))
	if err != nil {
		t.Fatal(err)
	}
	recorded := map[string]string{}
	for _, e := range entries {
		if e.Type == session.TypeToolTitle {
			if e.UUID != "" || e.ParentUUID != nil {
				t.Fatalf("tool-title entry is chained: %+v", e)
			}
			recorded[e.ToolUseID] = e.ToolTitle
		}
	}
	if recorded["b1"] != "number one printout" || recorded["b2"] != "echo of two" || len(recorded) != 2 {
		t.Fatalf("recorded: %v", recorded)
	}

	// Resume replays each title right after its ToolCallStarted.
	if _, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid})); err != nil {
		t.Fatal(err)
	}
	got := h.until(h.subscribe(sid), func(ev *agentv1.SessionEvent) bool { return ev.GetSessionUpdated() != nil })
	var ks []string
	for _, ev := range got {
		k := evKind(ev)
		if tt := ev.GetToolTitle(); tt != nil {
			k = "title:" + tt.ToolUseId + "=" + tt.Title
		}
		ks = append(ks, k)
	}
	want := "mode user turn asst start:b1 title:b1=number one printout start:b2 title:b2=echo of two start:r1"
	if s := strings.Join(ks, " "); !strings.HasPrefix(s, want) {
		t.Fatalf("replayed: %s", s)
	}
	h.closeSession(sid)
	if n := len(h.gw.ToolTitleRequests()); n != 2 {
		t.Fatalf("tool title requests after resume: %d", n)
	}
}

func TestToolTitleDisabledWithNoAITitles(t *testing.T) {
	h := startHarnessSrv(t, bashTurn(), func(gw *fg.Server) { gw.ToolTitle = toolTitleReply },
		func(o *bootstrap.Options) { o.AllowedTools = []string{"Bash", "Glob"} }, func(s *server.Server) { s.NoAITitles = true })
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "print some numbers")
	for _, ev := range h.until(ch, isTurnFinished) {
		if ev.GetToolTitle() != nil {
			t.Fatalf("unexpected tool title: %v", ev)
		}
	}
	h.closeSession(sid)
	if n := len(h.gw.ToolTitleRequests()); n != 0 {
		t.Fatalf("tool title requests: %d", n)
	}
}

func TestToolTitleFailureIsQuiet(t *testing.T) {
	// No ToolTitle script: the fake gateway fails the requests.
	h := startHarnessSrv(t, bashTurn(), nil, func(o *bootstrap.Options) { o.AllowedTools = []string{"Bash", "Glob"} }, func(s *server.Server) { s.NoAITitles = false })
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "print some numbers")
	h.until(ch, isTurnFinished)
	deadline := time.Now().Add(10 * time.Second)
	for len(h.gw.ToolTitleRequests()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	h.closeSession(sid)
	// One attempt per call, no retries.
	if n := len(h.gw.ToolTitleRequests()); n != 2 {
		t.Fatalf("tool title requests: %d", n)
	}
	entries, _ := session.Load(transcriptPath(h.proj, sid))
	for _, e := range entries {
		if e.Type == session.TypeToolTitle {
			t.Fatalf("unexpected entry: %+v", e)
		}
	}
}
