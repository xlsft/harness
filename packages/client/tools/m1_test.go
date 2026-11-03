package tools

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDiffLines(t *testing.T) {
	old := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	upd := "a\nb\nC\nd\ne\nf\ng\nh\ni\nj\nk\n"
	h := DiffLines(old, upd, 1)
	if len(h) != 2 {
		t.Fatalf("hunks %+v", h)
	}
	if h[0].OldStart != 2 || h[0].OldLines != 3 || h[0].NewStart != 2 || h[0].NewLines != 3 || !reflect.DeepEqual(h[0].Lines, []string{" b", "-c", "+C", " d"}) {
		t.Fatalf("hunk 0 %+v", h[0])
	}
	if !reflect.DeepEqual(h[1].Lines, []string{" j", "+k"}) || h[1].NewStart != 10 {
		t.Fatalf("hunk 1 %+v", h[1])
	}
	if a, d := DiffStats(h); a != 2 || d != 1 {
		t.Fatalf("stats +%d -%d", a, d)
	}
	if got := DiffLines("", "x\ny\n", 3); len(got) != 1 || got[0].OldStart != 1 || got[0].OldLines != 0 || got[0].NewLines != 2 {
		t.Fatalf("create %+v", got)
	}
	if u := UnifiedDiff(h, 3); !strings.Contains(u, "@@ -2,3 +2,3 @@") || !strings.Contains(u, "truncated") {
		t.Fatalf("unified %q", u)
	}
}

func TestEditStructuredPatchAndBeforeEdit(t *testing.T) {
	env := newEnv(t)
	var touched []string
	env.BeforeEdit = func(p string) { touched = append(touched, p) }
	p := filepath.Join(env.Cwd, "a.txt")
	write(t, p, "one\ntwo\nthree\n")
	call(t, env, &Read{}, map[string]any{"file_path": p})
	r := call(t, env, &Edit{}, map[string]any{"file_path": p, "old_string": "two", "new_string": "2"})
	s := r.Structured.(map[string]any)
	patch := s["structuredPatch"].([]Hunk)
	if r.IsError || len(patch) != 1 || !reflect.DeepEqual(patch[0].Lines, []string{" one", "-two", "+2", " three"}) || s["originalFile"] != "one\ntwo\nthree\n" {
		t.Fatalf("edit structured %+v", s)
	}
	if r.Summary != "Updated (+1 -1)" {
		t.Fatalf("summary %q", r.Summary)
	}
	n := filepath.Join(env.Cwd, "new.txt")
	r = call(t, env, &Write{}, map[string]any{"file_path": n, "content": "x\n"})
	if s := r.Structured.(map[string]any); s["type"] != "create" || s["originalFile"] != (*string)(nil) {
		t.Fatalf("write structured %+v", s)
	}
	if !reflect.DeepEqual(touched, []string{p, n}) {
		t.Fatalf("BeforeEdit calls %v", touched)
	}
}

func TestFileChanges(t *testing.T) {
	env := newEnv(t)
	p := filepath.Join(env.Cwd, "f.go")
	write(t, p, "package a\n\nfunc A() {}\n")
	call(t, env, &Read{}, map[string]any{"file_path": p})
	if ch := env.Files.Changes(20); len(ch) != 0 {
		t.Fatalf("no changes expected: %+v", ch)
	}
	time.Sleep(10 * time.Millisecond)
	write(t, p, "package a\n\nfunc B() {}\n")
	ch := env.Files.Changes(20)
	if len(ch) != 1 || ch[0].Path != p || !strings.Contains(ch[0].Diff, "-func A() {}") || !strings.Contains(ch[0].Diff, "+func B() {}") {
		t.Fatalf("changes %+v", ch)
	}
	if env.Files.CheckWritable(p) != "" {
		t.Fatal("the reminder counts as a read")
	}
	if ch := env.Files.Changes(20); len(ch) != 0 {
		t.Fatal("reported once")
	}
	// Own edits are not reported.
	call(t, env, &Edit{}, map[string]any{"file_path": p, "old_string": "B", "new_string": "C"})
	if ch := env.Files.Changes(20); len(ch) != 0 {
		t.Fatalf("own edit reported: %+v", ch)
	}
}

func TestBashReadsSatisfyReadBeforeEdit(t *testing.T) {
	env := newEnv(t)
	for i, cmd := range []string{"cat %s", "head -n 5 %s", "tail -3 %s", "sed -n '1,2p' %s", "grep -n x %s", "rg x %s", "nl %s"} {
		p := filepath.Join(env.Cwd, "f"+string(rune('a'+i))+".txt")
		write(t, p, "x\ny\n")
		if env.Files.CheckWritable(p) == "" {
			t.Fatal("precondition")
		}
		c := strings.ReplaceAll(cmd, "%s", filepath.Base(p))
		if strings.HasPrefix(c, "rg") {
			if _, err := os.Stat("/opt/homebrew/bin/rg"); err != nil {
				if _, err := os.Stat("/usr/bin/rg"); err != nil {
					continue
				}
			}
		}
		r := call(t, env, &Bash{}, map[string]any{"command": c})
		if r.IsError {
			continue // tool missing on this machine
		}
		if msg := env.Files.CheckWritable(p); msg != "" {
			t.Errorf("%s did not count as a read: %s", c, msg)
		}
	}
	p := filepath.Join(env.Cwd, "g.txt")
	write(t, p, "x\n")
	call(t, env, &Bash{}, map[string]any{"command": "grep -r x ."})
	call(t, env, &Bash{}, map[string]any{"command": "sed -i.bak 's/x/y/' g.txt"})
	if env.Files.CheckWritable(p) == "" {
		t.Fatal("recursive grep / sed -i must not count as a read")
	}
}

func collectTasks(m *TaskManager) (*sync.Mutex, *[]TaskEvent) {
	var mu sync.Mutex
	var evs []TaskEvent
	m.OnEvent = func(e TaskEvent) {
		mu.Lock()
		evs = append(evs, e)
		mu.Unlock()
	}
	return &mu, &evs
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBackgroundTaskLifecycle(t *testing.T) {
	env := newEnv(t)
	env.Tasks = NewTaskManager(filepath.Join(env.ScratchDir, "tasks"))
	mu, evs := collectTasks(env.Tasks)
	r := call(t, env, &Bash{}, map[string]any{"command": "echo hello; sleep 0.3; echo bye; exit 3", "run_in_background": true, "description": "greet"})
	id, _ := r.Structured.(map[string]any)["backgroundTaskId"].(string)
	if r.IsError || id == "" || !strings.Contains(text(r), "running in background with ID: "+id) {
		t.Fatalf("start: %q", text(r))
	}
	waitFor(t, 5*time.Second, func() bool { info, _ := env.Tasks.Get(id); return !info.Running })
	info, _ := env.Tasks.Get(id)
	if info.ExitCode != 3 || info.Reason != TaskExited || info.Description != "greet" {
		t.Fatalf("info %+v", info)
	}
	if data, _ := os.ReadFile(info.OutputPath); string(data) != "hello\nbye\n" {
		t.Fatalf("output %q", data)
	}
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(*evs) > 0 && (*evs)[len(*evs)-1].Kind == TaskEventFinished
	})
	mu.Lock()
	var kinds []TaskEventKind
	var out strings.Builder
	for _, e := range *evs {
		if len(kinds) == 0 || kinds[len(kinds)-1] != e.Kind {
			kinds = append(kinds, e.Kind)
		}
		out.WriteString(e.Text)
	}
	mu.Unlock()
	if kinds[0] != TaskEventStarted || kinds[len(kinds)-1] != TaskEventFinished || !strings.Contains(out.String(), "hello") {
		t.Fatalf("events %v %q", kinds, out.String())
	}

	// TaskStop kills a running task.
	r = call(t, env, &Bash{}, map[string]any{"command": "sleep 30", "run_in_background": true})
	id = r.Structured.(map[string]any)["backgroundTaskId"].(string)
	start := time.Now()
	r = call(t, env, &TaskStop{}, map[string]any{"task_id": id})
	if r.IsError || time.Since(start) > 4*time.Second {
		t.Fatalf("stop: %q", text(r))
	}
	if info, _ := env.Tasks.Get(id); info.Running || info.Reason != TaskStopped {
		t.Fatalf("stopped info %+v", info)
	}
	if r := call(t, env, &TaskStop{}, map[string]any{"task_id": id}); !r.IsError {
		t.Fatal("stopping a finished task is an error")
	}
	// StopAll on session close.
	call(t, env, &Bash{}, map[string]any{"command": "sleep 30", "run_in_background": true})
	call(t, env, &Bash{}, map[string]any{"command": "sleep 30", "run_in_background": true})
	env.Tasks.StopAll(TaskSessionClosed)
	if env.Tasks.Running() != 0 {
		t.Fatal("tasks still running")
	}
}

func TestAutoBackgroundOnTimeout(t *testing.T) {
	env := newEnv(t)
	env.Tasks = NewTaskManager(filepath.Join(env.ScratchDir, "tasks"))
	start := time.Now()
	r := call(t, env, &Bash{}, map[string]any{"command": "echo early; sleep 1; echo late", "timeout": 300})
	id, _ := r.Structured.(map[string]any)["backgroundTaskId"].(string)
	if r.IsError || id == "" || !strings.Contains(text(r), "did not complete within its") || time.Since(start) > 900*time.Millisecond {
		t.Fatalf("auto-background: %q after %s", text(r), time.Since(start))
	}
	waitFor(t, 5*time.Second, func() bool { info, _ := env.Tasks.Get(id); return !info.Running })
	info, _ := env.Tasks.Get(id)
	if data, _ := os.ReadFile(info.OutputPath); string(data) != "early\nlate\n" || info.ExitCode != 0 {
		t.Fatalf("handed-off output %q exit %d", data, info.ExitCode)
	}
	// Commands starting with sleep time out instead.
	r = call(t, env, &Bash{}, map[string]any{"command": "sleep 2", "timeout": 200})
	if !r.IsError || !strings.Contains(text(r), "timed out") {
		t.Fatalf("sleep must time out: %q", text(r))
	}
	// Manual backgrounding (SendUserMessage NOW).
	go func() {
		time.Sleep(200 * time.Millisecond)
		env.Shell.BackgroundRunning()
	}()
	r = call(t, env, &Bash{}, map[string]any{"command": "sleep 2"})
	if r.IsError || !strings.Contains(text(r), "moved this command to the background") {
		t.Fatalf("manual background: %q", text(r))
	}
	env.Tasks.StopAll(TaskSessionClosed)
}

func TestUnattendedTimeLimit(t *testing.T) {
	env := newEnv(t)
	m := NewTaskManager(filepath.Join(env.ScratchDir, "tasks"))
	m.Unattended = true
	info, err := m.Start(env.Shell, "sleep 30", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the limit by stopping with the time-limit reason.
	if _, err := m.stop(info.ID, TaskTimeLimit); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Get(info.ID); got.Reason != TaskTimeLimit {
		t.Fatalf("reason %s", got.Reason)
	}
}

type fakeHost struct {
	mu       sync.Mutex
	mode     string
	todos    []Todo
	reminds  []string
	ia       Interactor
	setModes []string
}

func (h *fakeHost) PermissionMode() string { h.mu.Lock(); defer h.mu.Unlock(); return h.mode }
func (h *fakeHost) SetPermissionMode(m string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mode = m
	h.setModes = append(h.setModes, m)
	return nil
}
func (h *fakeHost) Remind(id string, vars map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reminds = append(h.reminds, id+":"+vars["plan_file_path"])
}
func (h *fakeHost) Todos() []Todo          { return h.todos }
func (h *fakeHost) SetTodos(t []Todo)      { h.todos = t }
func (h *fakeHost) Interactor() Interactor { return h.ia }

type fakeIA struct {
	resp    QuestionResponse
	asked   []Question
	plan    PlanResponse
	gotPlan string
}

func (f *fakeIA) AskQuestions(ctx context.Context, id string, qs []Question) (QuestionResponse, error) {
	f.asked = qs
	return f.resp, nil
}
func (f *fakeIA) ProposePlan(ctx context.Context, id, plan, path string) (PlanResponse, error) {
	f.gotPlan = plan
	return f.plan, nil
}

func TestPlanModeTools(t *testing.T) {
	env := newEnv(t)
	env.PlansDir = filepath.Join(t.TempDir(), "plans", "sid")
	h := &fakeHost{mode: "default"}
	env.SetHost(h)
	if r := call(t, env, &ExitPlanMode{}, map[string]any{"plan": "x"}); !r.IsError {
		t.Fatal("ExitPlanMode outside plan mode")
	}
	r := call(t, env, &EnterPlanMode{}, map[string]any{})
	if r.IsError || h.mode != "plan" || len(h.reminds) != 1 || !strings.HasPrefix(h.reminds[0], "plan_mode:") {
		t.Fatalf("enter: %q %+v", text(r), h)
	}
	// Headless: plan saved, error result, still planning.
	r = call(t, env, &ExitPlanMode{}, map[string]any{"plan": "# Plan\n1. do it"})
	if !r.IsError || h.mode != "plan" {
		t.Fatalf("headless exit: %q", text(r))
	}
	if data, _ := os.ReadFile(filepath.Join(env.PlansDir, "1.md")); string(data) != "# Plan\n1. do it" {
		t.Fatalf("plan file %q", data)
	}
	ia := &fakeIA{plan: PlanResponse{Decision: PlanKeepPlanning, Feedback: "add tests"}}
	h.ia = ia
	r = call(t, env, &ExitPlanMode{}, map[string]any{"plan": "v2"})
	if !r.IsError || !strings.Contains(text(r), "add tests") || h.mode != "plan" {
		t.Fatalf("keep planning: %q", text(r))
	}
	ia.plan = PlanResponse{Decision: PlanApproveAcceptEdits}
	r = call(t, env, &ExitPlanMode{}, map[string]any{"plan": "v3"})
	if r.IsError || h.mode != "acceptEdits" || ia.gotPlan != "v3" || !strings.HasSuffix(h.reminds[len(h.reminds)-1], "3.md") || !strings.HasPrefix(h.reminds[len(h.reminds)-1], "plan_approved:") {
		t.Fatalf("approve: %q %+v", text(r), h)
	}
}

func TestTodoWrite(t *testing.T) {
	env := newEnv(t)
	h := &fakeHost{}
	env.SetHost(h)
	todos := []any{map[string]any{"content": "Run tests", "activeForm": "Running tests", "status": "in_progress"}, map[string]any{"content": "Fix", "activeForm": "Fixing", "status": "pending"}}
	r := call(t, env, &TodoWrite{}, map[string]any{"todos": todos})
	if r.IsError || len(h.todos) != 2 || h.todos[0].ActiveForm != "Running tests" {
		t.Fatalf("todo: %q %+v", text(r), h.todos)
	}
	if r := call(t, env, &TodoWrite{}, map[string]any{"todos": []any{map[string]any{"content": "x", "activeForm": "y", "status": "done"}}}); !r.IsError {
		t.Fatal("invalid status")
	}
}
