package server_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/decision"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/server"
)

type harness struct {
	t      *testing.T
	gw     *fg.Server
	client agentv1connect.AgentServiceClient
	ctx    context.Context
	proj   string
}

func startHarness(t *testing.T, respond fg.Responder, mod func(*bootstrap.Options)) *harness {
	t.Helper()
	return startHarnessGW(t, respond, nil, mod)
}

// startHarnessGW is startHarness with a hook to configure the fake gateway
// before it serves.
func startHarnessGW(t *testing.T, respond fg.Responder, gwmod func(*fg.Server), mod func(*bootstrap.Options)) *harness {
	t.Helper()
	return startHarnessSrv(t, respond, gwmod, mod, nil)
}

// startHarnessSrv is startHarnessGW with a hook to configure the server before it serves.
func startHarnessSrv(t *testing.T, respond fg.Responder, gwmod func(*fg.Server), mod func(*bootstrap.Options), srvmod func(*server.Server)) *harness {
	t.Helper()
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HARNESS_TMPDIR", t.TempDir())
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	t.Setenv("HARNESS_KEYRING", "0")  // environment keys go to a file under HARNESS_HOME, never the keychain
	t.Setenv("HARNESS_NO_HOOKS", "1") // the hooks of the developer's own ~/.claude/settings.json have no place in a test
	gw := fg.New(respond)
	if gwmod != nil {
		gwmod(gw)
	}
	url, stop, err := gw.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	tok := auth.StaticToken("hat_test")
	base := bootstrap.Options{Gateway: connectgw.New(url, nil, tok), Prompts: prompts.NewConnectSource(url, nil, tok), DecisionService: decision.NewClient(url, nil, tok)}
	if mod != nil {
		mod(&base)
	}
	srv := server.New(base)
	if srvmod != nil {
		srvmod(srv)
	}
	socket := shortSocket(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.ServeUnix(ctx, socket)
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	return &harness{t: t, gw: gw, client: agentv1connect.NewAgentServiceClient(rpc.UnixHTTPClient(socket), "http://agent", connect.WithGRPC()), ctx: ctx, proj: proj}
}

func (h *harness) create(req *agentv1.CreateSessionRequest) (string, <-chan *agentv1.SessionEvent) {
	h.t.Helper()
	if req.Cwd == "" {
		req.Cwd = h.proj
	}
	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(req))
	if err != nil {
		h.t.Fatal(err)
	}
	sid := cs.Msg.Session.Id
	return sid, h.subscribe(sid)
}

func (h *harness) subscribe(sid string) <-chan *agentv1.SessionEvent {
	stream, err := h.client.Subscribe(h.ctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: sid}))
	if err != nil {
		h.t.Fatal(err)
	}
	ch := make(chan *agentv1.SessionEvent, 4096)
	go func() {
		defer close(ch)
		for stream.Receive() {
			ch <- stream.Msg().Event
		}
	}()
	return ch
}

func (h *harness) send(sid, msg string) {
	h.t.Helper()
	if _, err := h.client.SendUserMessage(h.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Content: text(msg)})); err != nil {
		h.t.Fatal(err)
	}
}

// until consumes events, calling handle for each, until handle returns true.
func (h *harness) until(ch <-chan *agentv1.SessionEvent, handle func(*agentv1.SessionEvent) bool) []*agentv1.SessionEvent {
	h.t.Helper()
	var got []*agentv1.SessionEvent
	timeout := time.After(15 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				h.t.Fatalf("stream ended; got %d events", len(got))
			}
			got = append(got, ev)
			if handle(ev) {
				return got
			}
		case <-timeout:
			h.t.Fatalf("timeout; got %d events", len(got))
		}
	}
}

func isTurnFinished(ev *agentv1.SessionEvent) bool { return ev.GetTurnFinished() != nil }

func lastResultText(r *llm.Request) string {
	trs := fg.LastToolResults(r)
	if len(trs) == 0 {
		return ""
	}
	return fg.ResultText(trs[len(trs)-1])
}

func allText(r *llm.Request) string {
	var sb strings.Builder
	for _, m := range r.Messages {
		for _, b := range m.Content {
			sb.WriteString(b.Text)
			for _, c := range b.Content {
				sb.WriteString(c.Text)
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func TestPlanModeRoundTrip(t *testing.T) {
	var target string
	h := startHarness(t, fg.Sequence(
		func(r *llm.Request) fg.Reply {
			target = filepath.Join(fg.Cwd(r), "impl.txt")
			return fg.Reply{Events: fg.Message("m0", fg.ToolUse("p0", "EnterPlanMode", map[string]any{}))}
		},
		func(r *llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("w1", "Write", map[string]any{"file_path": target, "content": "early"}))}
		},
		func(r *llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("m2", fg.ToolUse("x1", "ExitPlanMode", map[string]any{"plan": "# Plan v1"}))}
		},
		func(r *llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("m3", fg.ToolUse("x2", "ExitPlanMode", map[string]any{"plan": "# Plan v2\n- write impl.txt"}))}
		},
		func(r *llm.Request) fg.Reply {
			return fg.Reply{Events: fg.Message("m4", fg.ToolUse("w2", "Write", map[string]any{"file_path": target, "content": "done"}))}
		},
	), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "build it")
	var proposals []*agentv1.PlanProposed
	var modes []agentv1.PermissionMode
	var resolved int
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		switch {
		case ev.GetPlanProposed() != nil:
			p := ev.GetPlanProposed()
			proposals = append(proposals, p)
			d := agentv1.RespondToPlanRequest_DECISION_KEEP_PLANNING
			if len(proposals) == 2 {
				d = agentv1.RespondToPlanRequest_DECISION_APPROVE_ACCEPT_EDITS
			}
			if _, err := h.client.RespondToPlan(h.ctx, connect.NewRequest(&agentv1.RespondToPlanRequest{SessionId: sid, RequestId: p.RequestId, Decision: d, Feedback: "add more detail"})); err != nil {
				t.Fatal(err)
			}
		case ev.GetPlanResolved() != nil:
			resolved++
		case ev.GetPermissionModeChanged() != nil:
			modes = append(modes, ev.GetPermissionModeChanged().Mode)
		case ev.GetPermissionRequest() != nil:
			t.Fatalf("unexpected permission request for %s", ev.GetPermissionRequest().ToolName)
		}
		return isTurnFinished(ev)
	})
	if len(proposals) != 2 || resolved != 2 || proposals[1].Plan != "# Plan v2\n- write impl.txt" {
		t.Fatalf("proposals %+v resolved %d", proposals, resolved)
	}
	if want := filepath.Join(config.Home(), "plans", sid, "2.md"); proposals[1].PlanFilePath != want {
		t.Fatalf("plan path %s want %s", proposals[1].PlanFilePath, want)
	}
	if data, _ := os.ReadFile(proposals[1].PlanFilePath); !strings.HasPrefix(string(data), "# Plan v2") {
		t.Fatalf("plan file %q", data)
	}
	if len(modes) != 2 || modes[0] != agentv1.PermissionMode_PERMISSION_MODE_PLAN || modes[1] != agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS {
		t.Fatalf("modes %v", modes)
	}
	reqs := h.gw.Requests()
	if !strings.Contains(lastResultText(reqs[2]), "Plan mode is active") {
		t.Fatalf("write in plan mode not denied: %q", lastResultText(reqs[2]))
	}
	if !strings.Contains(allText(reqs[1]), "Plan mode is active. Use read-only tools only") {
		t.Fatal("plan_mode reminder missing after EnterPlanMode")
	}
	if !strings.Contains(lastResultText(reqs[3]), "keep planning: add more detail") {
		t.Fatalf("plan_rejected feedback: %q", lastResultText(reqs[3]))
	}
	if !strings.Contains(allText(reqs[4]), "was approved") {
		t.Fatal("plan_approved reminder missing")
	}
	if data, _ := os.ReadFile(target); string(data) != "done" {
		t.Fatalf("implementation %q", data)
	}
}

func TestAskUserQuestion(t *testing.T) {
	h := startHarness(t, fg.Sequence(func(r *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m", fg.ToolUse("q", "AskUserQuestion", map[string]any{"questions": []any{
			map[string]any{"question": "Which database?", "header": "DB", "multiSelect": false, "options": []any{
				map[string]any{"label": "Postgres", "description": "server"}, map[string]any{"label": "SQLite", "description": "embedded"}}},
			map[string]any{"question": "Which features?", "header": "Features", "multiSelect": true, "options": []any{
				map[string]any{"label": "Auth", "description": ""}, map[string]any{"label": "Billing", "description": ""}}},
		}}))}
	}), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "set it up")
	var q *agentv1.Question
	var resolvedBy string
	var statuses []agentv1.SessionStatus
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		switch {
		case ev.GetQuestion() != nil:
			q = ev.GetQuestion()
			_, err := h.client.AnswerQuestion(h.ctx, connect.NewRequest(&agentv1.AnswerQuestionRequest{SessionId: sid, RequestId: q.RequestId,
				Answers: []*agentv1.QuestionAnswer{{Selected: []string{"SQLite"}}, {Selected: []string{"Auth", "Billing"}}}}))
			if err != nil {
				t.Fatal(err)
			}
		case ev.GetQuestionResolved() != nil:
			resolvedBy = ev.GetQuestionResolved().ResolvedBy
		case ev.GetSessionUpdated() != nil:
			statuses = append(statuses, ev.GetSessionUpdated().Status)
		}
		return isTurnFinished(ev)
	})
	if q == nil || len(q.Items) != 2 || !q.Items[1].MultiSelect || q.Items[0].Options[1].Label != "SQLite" || q.ToolUseId != "q" {
		t.Fatalf("question %+v", q)
	}
	if resolvedBy != "local" {
		t.Fatalf("resolved by %q", resolvedBy)
	}
	needsInput := false
	for _, s := range statuses {
		if s == agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT {
			needsInput = true
		}
	}
	if !needsInput {
		t.Fatal("status NEEDS_INPUT not reported")
	}
	tr := lastResultText(h.gw.Requests()[1])
	if !strings.Contains(tr, `[{"question":"Which database?","selected":["SQLite"]},{"question":"Which features?","selected":["Auth","Billing"]}]`) {
		t.Fatalf("answer result %q", tr)
	}
}

func TestBackgroundTasksAndCloseSession(t *testing.T) {
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		last := r.Messages[len(r.Messages)-1]
		if last.Role == "user" && strings.Contains(allText(&llm.Request{Messages: []llm.Message{last}}), "start\n") && last.Content[0].Type != llm.BlockToolResult {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("b"+string(rune('a'+n)), "Bash", map[string]any{"command": "echo started; sleep 30", "run_in_background": true, "description": "server"}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	}, func(o *bootstrap.Options) { o.AllowedTools = []string{"Bash"} })
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "start")
	var started *agentv1.TaskStarted
	var output strings.Builder
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if s := ev.GetTaskStarted(); s != nil {
			started = s
		}
		if o := ev.GetTaskOutput(); o != nil {
			output.WriteString(o.Text)
		}
		if tf := ev.GetToolCallFinished(); tf != nil && tf.StructuredResultJson != "" {
			var m map[string]any
			if json.Unmarshal([]byte(tf.StructuredResultJson), &m) != nil || m["backgroundTaskId"] == nil || tf.Summary == "" {
				t.Fatalf("structured result %s / %q", tf.StructuredResultJson, tf.Summary)
			}
		}
		return isTurnFinished(ev)
	})
	if started == nil || started.Command != "echo started; sleep 30" || started.Description != "server" || started.ToolUseId == "" {
		t.Fatalf("task started %+v", started)
	}
	lt, err := h.client.ListTasks(h.ctx, connect.NewRequest(&agentv1.ListTasksRequest{SessionId: sid}))
	if err != nil || len(lt.Msg.Tasks) != 1 || !lt.Msg.Tasks[0].Running || lt.Msg.Tasks[0].OutputPath != started.OutputPath {
		t.Fatalf("list tasks %v %+v", err, lt)
	}
	for i := 0; i < 200; i++ {
		if data, _ := os.ReadFile(started.OutputPath); len(data) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := h.client.StopTask(h.ctx, connect.NewRequest(&agentv1.StopTaskRequest{SessionId: sid, TaskId: started.TaskId})); err != nil {
		t.Fatal(err)
	}
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if o := ev.GetTaskOutput(); o != nil {
			output.WriteString(o.Text)
		}
		f := ev.GetTaskFinished()
		return f != nil && f.TaskId == started.TaskId && f.Reason == agentv1.TaskFinished_REASON_STOPPED
	})
	if !strings.Contains(output.String(), "started") {
		t.Fatalf("task output %q", output.String())
	}
	if _, err := h.client.StopTask(h.ctx, connect.NewRequest(&agentv1.StopTaskRequest{SessionId: sid, TaskId: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("stop unknown: %v", err)
	}
	// A second task is killed by CloseSession.
	h.send(sid, "start")
	h.until(ch, isTurnFinished)
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	closedTask, ended := false, false
	for ev := range ch {
		if f := ev.GetTaskFinished(); f != nil && f.Reason == agentv1.TaskFinished_REASON_SESSION_CLOSED {
			closedTask = true
		}
		if ev.GetSessionEnded() != nil {
			ended = true
		}
	}
	if !closedTask || !ended {
		t.Fatalf("close: task=%v ended=%v", closedTask, ended)
	}
	if _, err := h.client.ListTasks(h.ctx, connect.NewRequest(&agentv1.ListTasksRequest{SessionId: sid})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal("closed session must be gone")
	}
}

func TestAllowAlwaysAndPermissionComments(t *testing.T) {
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		last := r.Messages[len(r.Messages)-1]
		lb := last.Content[len(last.Content)-1]
		if lb.Type == llm.BlockText && strings.HasPrefix(lb.Text, "run ") {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("b"+string(rune('a'+n)), "Bash", map[string]any{"description": "test", "command": strings.TrimPrefix(lb.Text, "run ")}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	}, nil)
	os.Mkdir(filepath.Join(h.proj, ".git"), 0o755)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})

	// 1. allow always (+ comment) persists the suggested rule.
	h.send(sid, "run true && echo hi && make build")
	var pr *agentv1.PermissionRequest
	var resolvedDecision agentv1.RespondToPermissionRequest_Decision
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			pr = p
			h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: p.RequestId,
				Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW_ALWAYS, Comment: "next time use -j4"}))
		}
		if r := ev.GetPermissionResolved(); r != nil {
			resolvedDecision = r.Decision
		}
		return isTurnFinished(ev)
	})
	if pr == nil || len(pr.SuggestedRules) != 1 || pr.SuggestedRules[0] != "Bash(make build *)" {
		t.Fatalf("permission request %+v", pr)
	}
	if resolvedDecision != agentv1.RespondToPermissionRequest_DECISION_ALLOW_ALWAYS {
		t.Fatalf("resolved %v", resolvedDecision)
	}
	data, _ := os.ReadFile(filepath.Join(h.proj, ".claude", "settings.local.json"))
	if !strings.Contains(string(data), `"Bash(make build *)"`) {
		t.Fatalf("settings.local.json: %s", data)
	}
	reqs := h.gw.Requests()
	lastMsg := reqs[len(reqs)-1].Messages[len(reqs[len(reqs)-1].Messages)-1]
	if lastMsg.Content[0].Type != llm.BlockToolResult || lastMsg.Content[1].Text != "next time use -j4" {
		t.Fatalf("allow comment must follow the tool result: %+v", lastMsg.Content)
	}

	// 2. the persisted rule applies without asking.
	h.send(sid, "run make build -j4")
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if ev.GetPermissionRequest() != nil {
			t.Fatal("allowed rule must not ask")
		}
		return isTurnFinished(ev)
	})

	// 3. deny with a comment: the model continues.
	h.send(sid, "run rm -rf build")
	evs := h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: p.RequestId,
				Decision: agentv1.RespondToPermissionRequest_DECISION_DENY, Comment: "keep the build dir"}))
		}
		return isTurnFinished(ev)
	})
	if evs[len(evs)-1].GetTurnFinished().Result != agentv1.TurnFinished_RESULT_SUCCESS {
		t.Fatal("deny with comment must let the model continue")
	}
	reqs = h.gw.Requests()
	if tr := lastResultText(reqs[len(reqs)-1]); !strings.Contains(tr, "keep the build dir") {
		t.Fatalf("denial reason %q", tr)
	}

	// 4. deny without a comment stops the turn.
	h.send(sid, "run rm -rf dist")
	evs = h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: p.RequestId,
				Decision: agentv1.RespondToPermissionRequest_DECISION_DENY}))
		}
		return isTurnFinished(ev)
	})
	if evs[len(evs)-1].GetTurnFinished().Result != agentv1.TurnFinished_RESULT_INTERRUPTED {
		t.Fatalf("deny without comment must stop the turn: %v", evs[len(evs)-1].GetTurnFinished())
	}
}

func TestCheckpointsRewindAndCompactRPCs(t *testing.T) {
	var file string
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.RequestClass == "compaction" {
			return fg.Reply{Events: fg.Message("s", fg.Text("SUMMARY"))}
		}
		last := r.Messages[len(r.Messages)-1]
		lb := last.Content[len(last.Content)-1]
		if lb.Type == llm.BlockText && strings.HasPrefix(lb.Text, "write ") {
			file = filepath.Join(fg.Cwd(r), "f.txt")
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("w"+string(rune('a'+n)), "Write", map[string]any{"file_path": file, "content": strings.TrimPrefix(lb.Text, "write ")}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	}, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	var created []*agentv1.Checkpoint
	for _, p := range []string{"write one", "write two"} {
		h.send(sid, p)
		h.until(ch, func(ev *agentv1.SessionEvent) bool {
			if c := ev.GetCheckpointCreated(); c != nil {
				created = append(created, c.Checkpoint)
			}
			return isTurnFinished(ev)
		})
	}
	lc, err := h.client.ListCheckpoints(h.ctx, connect.NewRequest(&agentv1.ListCheckpointsRequest{SessionId: sid}))
	if err != nil || len(lc.Msg.Checkpoints) != 2 || len(created) != 2 || lc.Msg.Checkpoints[1].Id != created[1].Id || lc.Msg.Checkpoints[1].PromptPreview != "write two" || !lc.Msg.Checkpoints[1].HasCodeChanges {
		t.Fatalf("checkpoints %v %+v", err, lc)
	}
	rw, err := h.client.Rewind(h.ctx, connect.NewRequest(&agentv1.RewindRequest{SessionId: sid, CheckpointId: created[1].Id, Mode: agentv1.RewindRequest_MODE_CODE_AND_CONVERSATION}))
	if err != nil || rw.Msg.Result.RestoredPrompt != "write two" || rw.Msg.Result.FilesRestored != 1 {
		t.Fatalf("rewind %v %+v", err, rw)
	}
	if data, _ := os.ReadFile(file); string(data) != "one" {
		t.Fatalf("file %q", data)
	}
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetRewound() != nil })
	if _, err := h.client.Compact(h.ctx, connect.NewRequest(&agentv1.CompactRequest{SessionId: sid, Instructions: "only the file"})); err != nil {
		t.Fatal(err)
	}
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		c := ev.GetCompacted()
		return c != nil && c.Trigger == agentv1.Compacted_TRIGGER_MANUAL
	})
	if _, err := h.client.Rewind(h.ctx, connect.NewRequest(&agentv1.RewindRequest{SessionId: sid, CheckpointId: "nope", Mode: agentv1.RewindRequest_MODE_CODE})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("unknown checkpoint: %v", err)
	}

	// Fork the session into a new one.
	fk, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid, Fork: true}))
	if err != nil || fk.Msg.Session.Id == sid || fk.Msg.Session.ParentSessionId != sid {
		t.Fatalf("fork %v %+v", err, fk)
	}
}

func TestSuggestFiles(t *testing.T) {
	h := startHarness(t, nil, nil)
	for _, f := range []string{"src/main.go", "src/util/helper.go", "README.md", "build/out.go", "docs/help.md"} {
		p := filepath.Join(h.proj, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	os.WriteFile(filepath.Join(h.proj, ".gitignore"), []byte("build/\n"), 0o644)
	sid, _ := h.create(&agentv1.CreateSessionRequest{})
	r, err := h.client.SuggestFiles(h.ctx, connect.NewRequest(&agentv1.SuggestFilesRequest{SessionId: sid, Query: "hlpr", Limit: 5}))
	if err != nil || len(r.Msg.Paths) == 0 || r.Msg.Paths[0] != "src/util/helper.go" {
		t.Fatalf("suggest %v %+v", err, r)
	}
	all, _ := h.client.SuggestFiles(h.ctx, connect.NewRequest(&agentv1.SuggestFilesRequest{SessionId: sid, Query: "", Limit: 50}))
	for _, p := range all.Msg.Paths {
		if strings.HasPrefix(p, "build/") {
			t.Fatalf("gitignored file suggested: %v", all.Msg.Paths)
		}
	}
}

func TestFuzzyFilter(t *testing.T) {
	paths := []string{"src/components/Button.vue", "src/main.go", "internal/server/server.go", "server_test.go"}
	got := server.FuzzyFilter(paths, "srvgo", 10)
	if len(got) < 2 || got[0] != "internal/server/server.go" && got[0] != "server_test.go" {
		t.Fatalf("fuzzy %v", got)
	}
	if got := server.FuzzyFilter(paths, "btn", 10); len(got) != 1 || got[0] != "src/components/Button.vue" {
		t.Fatalf("fuzzy btn %v", got)
	}
	if got := server.FuzzyFilter(paths, "zzz", 10); len(got) != 0 {
		t.Fatalf("no match %v", got)
	}
}

// NOW delivery while a shell command runs moves it to the background and
// the message is read in the same turn.
func TestNowDeliveryBackgroundsRunningBash(t *testing.T) {
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		last := r.Messages[len(r.Messages)-1]
		if last.Content[len(last.Content)-1].Text == "start" {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("b", "Bash", map[string]any{"description": "test", "command": "sleep 30"}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ack"))}
	}, func(o *bootstrap.Options) { o.AllowedTools = []string{"Bash"} })
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "start")
	start := time.Now()
	var results []agentv1.TurnFinished_Result
	taskStarted := false
	var bg *agentv1.ToolBackground
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		switch {
		case ev.GetToolCallFinished() != nil:
			bg = ev.GetToolCallFinished().GetBackground()
		case ev.GetToolCallStarted() != nil:
			go func() {
				time.Sleep(300 * time.Millisecond)
				h.client.SendUserMessage(h.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "q1", Content: text("stop that"), Delivery: agentv1.SendUserMessageRequest_DELIVERY_NOW}))
			}()
		case ev.GetTaskStarted() != nil:
			taskStarted = true
		case ev.GetTurnFinished() != nil:
			results = append(results, ev.GetTurnFinished().Result)
		}
		return isTurnFinished(ev)
	})
	if len(results) != 1 || results[0] != agentv1.TurnFinished_RESULT_SUCCESS || !taskStarted || time.Since(start) > 10*time.Second {
		t.Fatalf("NOW delivery: %v task=%v after %s", results, taskStarted, time.Since(start))
	}
	reqs := h.gw.Requests()
	lm := reqs[1].Messages[len(reqs[1].Messages)-1]
	if lm.Content[0].Type != llm.BlockToolResult || !strings.Contains(fg.ResultText(lm.Content[0]), "so that a message the user sent") || lm.Content[1].Text != "stop that" {
		t.Fatalf("queued message after backgrounded tool: %+v", lm.Content)
	}
	if bg.GetReason() != agentv1.BackgroundReason_BACKGROUND_REASON_QUEUED_MESSAGE || bg.GetTaskId() == "" || !strings.Contains(fg.ResultText(lm.Content[0]), bg.GetTaskId()) {
		t.Fatalf("background marker %+v", bg)
	}
	h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid}))
}

// A queued message moves a command that keeps it waiting to the background
// after a short grace, and the model reads it in the same turn.
func TestQueuedMessageBackgroundsRunningBash(t *testing.T) {
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		last := r.Messages[len(r.Messages)-1]
		if last.Content[len(last.Content)-1].Text == "start" {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("b", "Bash", map[string]any{"description": "test", "command": "sleep 30"}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ack"))}
	}, func(o *bootstrap.Options) { o.AllowedTools = []string{"Bash"} })
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "start")
	start := time.Now()
	var bg *agentv1.ToolBackground
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		switch {
		case ev.GetToolCallFinished() != nil:
			bg = ev.GetToolCallFinished().GetBackground()
		case ev.GetToolCallStarted() != nil:
			go func() {
				time.Sleep(200 * time.Millisecond)
				h.client.SendUserMessage(h.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "q1", Content: text("hello meanwhile")}))
			}()
		}
		return isTurnFinished(ev)
	})
	if time.Since(start) > 10*time.Second || bg.GetReason() != agentv1.BackgroundReason_BACKGROUND_REASON_QUEUED_MESSAGE || bg.GetTaskId() == "" {
		t.Fatalf("after %s: background %+v", time.Since(start), bg)
	}
	reqs := h.gw.Requests()
	lm := reqs[1].Messages[len(reqs[1].Messages)-1]
	if lm.Content[len(lm.Content)-1].Text != "hello meanwhile" {
		t.Fatalf("queued message not read in the same turn: %+v", lm.Content)
	}
	h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid}))
}

// NOW delivery while the model is generating interrupts the turn; the
// message starts the next one.
func TestNowDeliveryInterruptsGeneration(t *testing.T) {
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Partial("m", false, fg.Text("thinking...")), Hold: true}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ack"))}
	}, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "start")
	var results []agentv1.TurnFinished_Result
	sent := false
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if ev.GetAssistantDelta() != nil && !sent {
			sent = true
			h.client.SendUserMessage(h.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Content: text("now"), Delivery: agentv1.SendUserMessageRequest_DELIVERY_NOW}))
		}
		if tf := ev.GetTurnFinished(); tf != nil {
			results = append(results, tf.Result)
		}
		return len(results) == 2
	})
	if results[0] != agentv1.TurnFinished_RESULT_INTERRUPTED || results[1] != agentv1.TurnFinished_RESULT_SUCCESS {
		t.Fatalf("results %v", results)
	}
}
