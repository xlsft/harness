package server_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
)

// projectTools are the tool prompts a coordinator needs, as the server's bundle has them.
func projectTools(gw *fg.Server) {
	schema := func(props string) json.RawMessage {
		return json.RawMessage(`{"type":"object","properties":{` + props + `}}`)
	}
	for name, props := range map[string]string{
		"TaskGraphAdd":       `"tasks":{"type":"array"}`,
		"TaskGraphList":      ``,
		"ProjectStatus":      ``,
		"ProjectWorkerStart": `"task_id":{"type":"string"},"prompt":{"type":"string"},"target":{"type":"string"}`,
		"ProjectWorkerMerge": `"worker":{"type":"string"}`,
		"ProjectFinish":      `"outcome":{"type":"string"},"summary":{"type":"string"}`,
	} {
		gw.Bundle.Tools[name] = prompts.ToolPrompt{Description: name, InputSchema: schema(props)}
	}
}

func lastBlock(r *llm.Request) llm.ContentBlock {
	m := r.Messages[len(r.Messages)-1]
	return m.Content[len(m.Content)-1]
}

func projText(r *llm.Request) string {
	var b strings.Builder
	for _, m := range r.Messages {
		for _, c := range m.Content {
			b.WriteString(c.Text)
			for _, x := range c.Content {
				b.WriteString(x.Text)
			}
		}
	}
	return b.String()
}

// coordinatorScript plays a coordinator that plans two tasks, starts a worker per task (the second is over the limit),
// merges the first when it reports and finishes; workers answer with a report.
func coordinatorScript(n int, r *llm.Request, secondStartReturned chan struct{}, releaseWorker func(), stopped <-chan struct{}) fg.Reply {
	first := r.Messages[0].Content
	if len(first) > 0 && strings.Contains(projText(&llm.Request{Messages: r.Messages[:1]}), "You are a worker") {
		select {
		case <-secondStartReturned:
		case <-stopped:
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("alpha done"))}
	}
	lb := lastBlock(r)
	if lb.Type == llm.BlockToolResult {
		switch lb.ToolUseID {
		case "add":
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("w1", "ProjectWorkerStart", map[string]any{"task_id": "t1", "prompt": "do alpha"}))}
		case "w1":
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("w2", "ProjectWorkerStart", map[string]any{"task_id": "t2", "prompt": "do beta"}))}
		case "w2":
			releaseWorker()
		case "mg":
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("fin", "ProjectFinish", map[string]any{"outcome": "done", "summary": "alpha merged"}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("waiting"))}
	}
	text := projText(r)
	switch {
	case strings.Contains(text, "alpha done") && !strings.Contains(text, "alpha merged"):
		return fg.Reply{Events: fg.Message("m", fg.ToolUse("mg", "ProjectWorkerMerge", map[string]any{"worker": "t1"}))}
	case strings.Contains(text, "GOAL") && n == 0:
		return fg.Reply{Events: fg.Message("m", fg.ToolUse("add", "TaskGraphAdd", map[string]any{"tasks": []map[string]any{{"title": "alpha"}, {"title": "beta"}}}))}
	}
	return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
}

func (h *harness) run(id string) *agentv1.ProjectRun {
	h.t.Helper()
	res, err := h.client.GetProjectRun(h.ctx, connect.NewRequest(&agentv1.GetProjectRunRequest{Id: id}))
	if err != nil {
		h.t.Fatal(err)
	}
	return res.Msg.GetRun()
}

func (h *harness) waitRun(id, what string, cond func(*agentv1.ProjectRun) bool) *agentv1.ProjectRun {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		run := h.run(id)
		if cond(run) {
			return run
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%s: run %+v", what, run)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestProjectRunCoordinatesWorkers(t *testing.T) {
	secondStartReturned := make(chan struct{})
	releaseWorker := sync.OnceFunc(func() { close(secondStartReturned) })
	stopped := make(chan struct{})
	defer close(stopped)
	h := startHarnessGW(t, func(n int, r *llm.Request) fg.Reply {
		return coordinatorScript(n, r, secondStartReturned, releaseWorker, stopped)
	}, projectTools, nil)
	repo := gittest.Init(t)
	res, err := h.client.CreateProjectRun(h.ctx, connect.NewRequest(&agentv1.CreateProjectRunRequest{Goal: "GOAL build two things", Cwd: repo,
		PermissionMode: "acceptEdits", Limits: &agentv1.ProjectRunLimits{MaxWorkers: 1, BudgetUsd: 100}}))
	if err != nil {
		t.Fatal(err)
	}
	run := res.Msg.GetRun()
	if run.GetLimits().GetMaxWorkers() != 1 || run.GetPermissionMode() != "acceptEdits" || run.GetCoordinatorSessionId() == "" {
		t.Fatalf("created %+v", run)
	}
	// The merge asks: answer it as the user would (the run waits for the user meanwhile).
	ch := h.subscribe(run.GetCoordinatorSessionId())
	go func() {
		for ev := range ch {
			if p := ev.GetPermissionRequest(); p != nil {
				_, _ = h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: run.GetCoordinatorSessionId(),
					RequestId: p.GetRequestId(), Decision: agentv1.RespondToPermissionRequest_DECISION_ALLOW}))
			}
		}
	}()
	done := h.waitRun(run.GetId(), "done", func(r *agentv1.ProjectRun) bool {
		return r.GetState() == agentv1.ProjectRunState_PROJECT_RUN_STATE_DONE
	})
	if len(done.GetWorkers()) != 1 {
		t.Fatalf("one worker within the limit, got %+v", done.GetWorkers())
	}
	w := done.GetWorkers()[0]
	if w.GetTaskId() != "t1" || w.GetState() != agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_MERGED || w.GetReport() != "alpha done" || w.GetBranch() == "" {
		t.Fatalf("worker %+v", w)
	}
	if done.GetReason() != "alpha merged" {
		t.Fatalf("summary %q", done.GetReason())
	}
	tasks := map[string]string{}
	for _, task := range done.GetTasks() {
		tasks[task.GetId()] = task.GetStatus().String()
	}
	if tasks["t1"] != agentv1.TaskNodeStatus_TASK_NODE_STATUS_DONE.String() || tasks["t2"] != agentv1.TaskNodeStatus_TASK_NODE_STATUS_PENDING.String() {
		t.Fatalf("tasks %v", tasks)
	}
	// The second start was refused by the limit, not by the model.
	refused := false
	for _, r := range h.gw.Requests() {
		if lb := lastBlock(r); lb.Type == llm.BlockToolResult && lb.ToolUseID == "w2" {
			refused = strings.Contains(projText(&llm.Request{Messages: r.Messages[len(r.Messages)-1:]}), "limit")
		}
	}
	if !refused {
		t.Fatal("the second worker was not refused over the limit")
	}
	// The worker got the coordinator's mode, no more.
	list, err := h.client.ListSessions(h.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list.Msg.GetSessions() {
		if s.GetId() == w.GetSessionId() {
			if s.GetPermissionMode() != agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS || s.GetParentSessionId() != run.GetCoordinatorSessionId() {
				t.Fatalf("worker session %+v", s)
			}
		}
	}
	// Ended runs can be forgotten; the record goes.
	if _, err := h.client.DeleteProjectRun(h.ctx, connect.NewRequest(&agentv1.DeleteProjectRunRequest{Id: run.GetId()})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(config.Home(), "project-runs", run.GetId()+".json")); !os.IsNotExist(err) {
		t.Fatalf("record left: %v", err)
	}
}

func TestProjectRunControlAndLimits(t *testing.T) {
	h := startHarnessGW(t, func(int, *llm.Request) fg.Reply { return fg.Reply{Hold: true} }, projectTools, nil)
	res, err := h.client.CreateProjectRun(h.ctx, connect.NewRequest(&agentv1.CreateProjectRunRequest{Goal: "a goal", Cwd: h.proj, Limits: &agentv1.ProjectRunLimits{MaxWorkers: 99}}))
	if err != nil {
		t.Fatal(err)
	}
	run := res.Msg.GetRun()
	if run.GetLimits().GetMaxWorkers() != 32 {
		t.Fatalf("limit not capped: %d", run.GetLimits().GetMaxWorkers())
	}
	ctl := func(a agentv1.ControlProjectRunRequest_Action, l *agentv1.ProjectRunLimits) (*agentv1.ProjectRun, error) {
		r, err := h.client.ControlProjectRun(h.ctx, connect.NewRequest(&agentv1.ControlProjectRunRequest{Id: run.GetId(), Action: a, Limits: l}))
		if err != nil {
			return nil, err
		}
		return r.Msg.GetRun(), nil
	}
	got, err := ctl(agentv1.ControlProjectRunRequest_ACTION_PAUSE, nil)
	if err != nil || got.GetState() != agentv1.ProjectRunState_PROJECT_RUN_STATE_PAUSED {
		t.Fatalf("pause %v %v", got.GetState(), err)
	}
	if got, err = ctl(agentv1.ControlProjectRunRequest_ACTION_SET_LIMITS, &agentv1.ProjectRunLimits{MaxWorkers: 2, BudgetTokens: 5}); err != nil || got.GetLimits().GetMaxWorkers() != 2 {
		t.Fatalf("limits %+v %v", got.GetLimits(), err)
	}
	if got, err = ctl(agentv1.ControlProjectRunRequest_ACTION_RESUME, nil); err != nil || got.GetState() != agentv1.ProjectRunState_PROJECT_RUN_STATE_RUNNING {
		t.Fatalf("resume %v %v", got.GetState(), err)
	}
	if _, err := h.client.DeleteProjectRun(h.ctx, connect.NewRequest(&agentv1.DeleteProjectRunRequest{Id: run.GetId()})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("delete of a going run: %v", err)
	}
	if got, err = ctl(agentv1.ControlProjectRunRequest_ACTION_STOP, nil); err != nil || got.GetState() != agentv1.ProjectRunState_PROJECT_RUN_STATE_STOPPED {
		t.Fatalf("stop %v %v", got.GetState(), err)
	}
	if _, err := ctl(agentv1.ControlProjectRunRequest_ACTION_RESUME, nil); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("resume of a stopped run: %v", err)
	}
	if _, err := h.client.CreateProjectRun(h.ctx, connect.NewRequest(&agentv1.CreateProjectRunRequest{Goal: "", Cwd: h.proj})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("no goal: %v", err)
	}
	if _, err := h.client.CreateProjectRun(h.ctx, connect.NewRequest(&agentv1.CreateProjectRunRequest{Goal: "x", Cwd: "relative"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("relative cwd: %v", err)
	}
}

func TestProjectRunSurvivesRestart(t *testing.T) {
	h := startHarness(t, fg.Sequence(), nil)
	// What a client left behind: a going run with a local worker that was running.
	run := &agentv1.ProjectRun{Id: "prun_restart-1", Goal: "g", State: agentv1.ProjectRunState_PROJECT_RUN_STATE_RUNNING, CoordinatorSessionId: "c",
		Limits: &agentv1.ProjectRunLimits{MaxWorkers: 2}, CreatedAt: "2026-11-07T10:00:00Z",
		Workers: []*agentv1.ProjectWorker{
			{SessionId: "w1", Name: "w-1", State: agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_RUNNING},
			{SessionId: "w2", Name: "w-2", Remote: true, State: agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_RUNNING},
		}}
	dir := filepath.Join(config.Home(), "project-runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"id": run.Id, "goal": run.Goal, "state": "PROJECT_RUN_STATE_RUNNING", "coordinatorSessionId": "c",
		"limits": map[string]any{"maxWorkers": 2}, "createdAt": run.CreatedAt,
		"workers": []map[string]any{{"sessionId": "w1", "name": "w-1", "state": "PROJECT_WORKER_STATE_RUNNING"}, {"sessionId": "w2", "name": "w-2", "remote": true, "state": "PROJECT_WORKER_STATE_RUNNING"}}})
	if err := os.WriteFile(filepath.Join(dir, run.Id+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := h.client.ListProjectRuns(h.ctx, connect.NewRequest(&agentv1.ListProjectRunsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var got *agentv1.ProjectRun
	for _, r := range res.Msg.GetRuns() {
		if r.GetId() == run.Id {
			got = r
		}
	}
	if got == nil {
		t.Fatalf("not listed: %v", res.Msg)
	}
	if got.GetState() != agentv1.ProjectRunState_PROJECT_RUN_STATE_PAUSED || !strings.Contains(got.GetReason(), "restarted") {
		t.Fatalf("state %v %q", got.GetState(), got.GetReason())
	}
	if got.GetWorkers()[0].GetState() != agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_INTERRUPTED ||
		got.GetWorkers()[1].GetState() != agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_RUNNING {
		t.Fatalf("workers %+v", got.GetWorkers())
	}
}
