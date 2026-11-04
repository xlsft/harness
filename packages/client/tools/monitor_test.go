package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fastMonitors speeds up the monitor pace for one test.
func fastMonitors(t *testing.T) {
	t.Helper()
	saved := monitorPace
	monitorPace.poll, monitorPace.batch, monitorPace.minGap = 10*time.Millisecond, 50*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { monitorPace = saved })
}

type noticeLog struct {
	mu     sync.Mutex
	events []TaskEvent
}

func (l *noticeLog) on(e TaskEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *noticeLog) of(kind TaskEventKind) []TaskEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []TaskEvent
	for _, e := range l.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func (l *noticeLog) notices(reminder string) []map[string]string {
	var out []map[string]string
	for _, e := range l.of(TaskEventNotify) {
		if e.Reminder == reminder {
			out = append(out, e.Vars)
		}
	}
	return out
}

func monitorEnv(t *testing.T) (*Env, *noticeLog) {
	t.Helper()
	fastMonitors(t)
	env := newEnv(t)
	env.Tasks = NewTaskManager(filepath.Join(env.ScratchDir, "tasks"))
	log := &noticeLog{}
	env.Tasks.OnEvent = log.on
	t.Cleanup(func() { env.Tasks.StopAll(TaskSessionClosed) })
	return env, log
}

func monitorID(t *testing.T, r Result) string {
	t.Helper()
	if r.IsError {
		t.Fatalf("monitor failed: %s", text(r))
	}
	return r.Structured.(map[string]any)["task_id"].(string)
}

func bgTask(t *testing.T, env *Env, command string) string {
	t.Helper()
	info, err := env.Tasks.Start(env.Shell, command, "test", "toolu_bg", 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

func TestMonitorInputValidation(t *testing.T) {
	env, _ := monitorEnv(t)
	for name, in := range map[string]map[string]any{
		"neither":    {"pattern": "x", "description": "d"},
		"both":       {"pattern": "x", "description": "d", "task_id": "b1", "command": "true"},
		"no pattern": {"command": "true", "description": "d"},
		"bad regex":  {"command": "true", "description": "d", "pattern": "("},
		"unknown":    {"task_id": "nope", "description": "d", "pattern": "x"},
	} {
		if r := call(t, env, &Monitor{}, in); !r.IsError {
			t.Errorf("%s: want an error, got %q", name, text(r))
		}
	}
	finished := bgTask(t, env, "true")
	done, _ := env.Tasks.Done(finished)
	<-done
	if r := call(t, env, &Monitor{}, map[string]any{"task_id": finished, "description": "d", "pattern": "x"}); !r.IsError || !strings.Contains(text(r), "already finished") {
		t.Fatalf("finished task: %q", text(r))
	}
}

func TestMonitorReadOnlyOnlyWithoutCommand(t *testing.T) {
	m := &Monitor{}
	if !m.ReadOnly(json.RawMessage(`{"task_id":"b1","pattern":"x"}`)) {
		t.Fatal("watching a task has no side effects")
	}
	if m.ReadOnly(json.RawMessage(`{"command":"rm -rf x","pattern":"x"}`)) || m.ShellCommand(json.RawMessage(`{"command":"rm -rf x"}`)) != "rm -rf x" {
		t.Fatal("a monitor that starts a command must be checked as that command")
	}
}

func TestMonitorOnceOnTaskEndsAndLeavesTaskRunning(t *testing.T) {
	env, log := monitorEnv(t)
	task := bgTask(t, env, "echo hello; sleep 0.2; printf '\\033[31mERROR\\033[0m one\\nERROR two\\n'; sleep 30")
	id := monitorID(t, call(t, env, &Monitor{}, map[string]any{"task_id": task, "pattern": "ERROR", "description": "errors"}))
	if id == task {
		t.Fatal("a monitor on a task has its own entry")
	}
	if info, ok := env.Tasks.Get(id); !ok || !info.Running || !info.Monitor {
		t.Fatalf("entry %+v", info)
	}
	waitFor(t, 5*time.Second, func() bool { return len(log.notices("monitor_match")) == 1 })
	n := log.notices("monitor_match")[0]
	if n["task_id"] != id || n["count"] != "1" || n["lines"] != "ERROR one" || !strings.Contains(n["status"], "ended") {
		t.Fatalf("notice %+v", n)
	}
	waitFor(t, 5*time.Second, func() bool {
		info, _ := env.Tasks.Get(id)
		return !info.Running
	})
	if info, _ := env.Tasks.Get(task); !info.Running {
		t.Fatal("a one-shot monitor must leave the task it watches running")
	}
	info, _ := env.Tasks.Get(id)
	if data, _ := os.ReadFile(info.OutputPath); string(data) != "ERROR one\n" {
		t.Fatalf("monitor output %q", data)
	}
}

func TestMonitorCommandEndsWithFirstMatch(t *testing.T) {
	env, log := monitorEnv(t)
	id := monitorID(t, call(t, env, &Monitor{}, map[string]any{"command": "echo READY; sleep 30", "pattern": "^READY$", "description": "server"}))
	waitFor(t, 5*time.Second, func() bool {
		info, _ := env.Tasks.Get(id)
		return !info.Running
	})
	n := log.notices("monitor_match")
	if len(n) != 1 || n[0]["task_id"] != id || !strings.Contains(n[0]["status"], "stopped") {
		t.Fatalf("notices %+v", n)
	}
	if len(env.Tasks.List()) != 1 {
		t.Fatalf("a command started by a monitor is one entry: %+v", env.Tasks.List())
	}
	if !env.Tasks.Reported(id) {
		t.Fatal("no finish report may follow a command the monitor stopped")
	}
}

// A command that is over by the time the monitor looks: its match still arrives, and its own report stays.
func TestMonitorCommandThatExitsAtOnce(t *testing.T) {
	for i := 0; i < 10; i++ {
		env, log := monitorEnv(t)
		id := monitorID(t, call(t, env, &Monitor{}, map[string]any{"command": "echo READY; exit 3", "pattern": "READY", "description": "quick"}))
		waitFor(t, 5*time.Second, func() bool { return len(log.notices("monitor_match")) == 1 })
		waitFor(t, 5*time.Second, func() bool {
			info, _ := env.Tasks.Get(id)
			return !info.Running
		})
		info, _ := env.Tasks.Get(id)
		if n := log.notices("monitor_match")[0]; n["lines"] != "READY" || info.ExitCode != 3 {
			t.Fatalf("notice %+v, task %+v", n, info)
		}
	}
}

func TestMonitorThrottlesBursts(t *testing.T) {
	env, log := monitorEnv(t)
	task := bgTask(t, env, "for i in 1 2 3 4 5 6 7 8 9 10; do echo hit $i; done; sleep 0.5; echo hit last; sleep 30")
	id := monitorID(t, call(t, env, &Monitor{}, map[string]any{"task_id": task, "pattern": "hit", "description": "hits", "persistent": true}))
	waitFor(t, 5*time.Second, func() bool { return len(log.notices("monitor_match")) == 2 })
	n := log.notices("monitor_match")
	if n[0]["count"] != "10" || !strings.Contains(n[0]["lines"], "hit 10") || !strings.Contains(n[0]["status"], "keeps watching") {
		t.Fatalf("burst must arrive as one notice: %+v", n[0])
	}
	if n[1]["lines"] != "hit last" {
		t.Fatalf("second notice %+v", n[1])
	}
	if info, _ := env.Tasks.Get(id); !info.Running {
		t.Fatal("a persistent monitor keeps watching")
	}
}

func TestMonitorCapsLinesPerNotice(t *testing.T) {
	env, log := monitorEnv(t)
	task := bgTask(t, env, "i=0; while [ $i -lt 50 ]; do echo hit $i; i=$((i+1)); done; sleep 30")
	monitorID(t, call(t, env, &Monitor{}, map[string]any{"task_id": task, "pattern": "hit", "description": "hits", "persistent": true}))
	waitFor(t, 5*time.Second, func() bool { return len(log.notices("monitor_match")) > 0 })
	n := log.notices("monitor_match")[0]
	if n["count"] != "50" || !strings.Contains(n["lines"], "and 30 more") {
		t.Fatalf("notice %+v", n)
	}
}

func TestMonitorNoisyStopsItself(t *testing.T) {
	env, log := monitorEnv(t)
	monitorPace.noisyLines = 20
	id := monitorID(t, call(t, env, &Monitor{}, map[string]any{"command": "while true; do echo spam; sleep 0.001; done", "pattern": "spam", "description": "spam", "persistent": true}))
	waitFor(t, 10*time.Second, func() bool { return len(log.notices("monitor_stopped")) == 1 })
	n := log.notices("monitor_stopped")[0]
	if n["task_id"] != id || !strings.Contains(n["reason"], "within a minute") {
		t.Fatalf("notice %+v", n)
	}
	waitFor(t, 5*time.Second, func() bool {
		info, _ := env.Tasks.Get(id)
		return !info.Running
	})
}

func TestMonitorTimeout(t *testing.T) {
	env, log := monitorEnv(t)
	task := bgTask(t, env, "sleep 30")
	id := monitorID(t, call(t, env, &Monitor{}, map[string]any{"task_id": task, "pattern": "never", "description": "d", "timeout": 50}))
	waitFor(t, 5*time.Second, func() bool { return len(log.notices("monitor_stopped")) == 1 })
	if !strings.Contains(log.notices("monitor_stopped")[0]["reason"], "timeout") {
		t.Fatalf("notice %+v", log.notices("monitor_stopped"))
	}
	waitFor(t, 5*time.Second, func() bool {
		info, _ := env.Tasks.Get(id)
		return !info.Running
	})
	if info, _ := env.Tasks.Get(task); !info.Running {
		t.Fatal("the watched task goes on")
	}
}

func TestMonitorTaskStopIsSilent(t *testing.T) {
	env, log := monitorEnv(t)
	task := bgTask(t, env, "sleep 30")
	id := monitorID(t, call(t, env, &Monitor{}, map[string]any{"task_id": task, "pattern": "x", "description": "d", "persistent": true}))
	if r := call(t, env, &TaskStop{}, map[string]any{"task_id": id}); r.IsError {
		t.Fatalf("TaskStop: %s", text(r))
	}
	info, _ := env.Tasks.Get(id)
	if info.Running || info.Reason != TaskStopped {
		t.Fatalf("entry %+v", info)
	}
	if len(log.of(TaskEventNotify)) != 0 {
		t.Fatalf("stopping on request sends no notice: %+v", log.of(TaskEventNotify))
	}
	if info, _ := env.Tasks.Get(task); !info.Running {
		t.Fatal("stopping the monitor leaves the task running")
	}
}

// The last lines go out before the task's own finish event, unterminated ones too.
func TestMonitorTaskExitFlushesFirst(t *testing.T) {
	env, log := monitorEnv(t)
	monitorPace.batch, monitorPace.minGap = time.Minute, time.Minute
	task := bgTask(t, env, "sleep 0.2; echo hit one; printf 'hit two'")
	id := monitorID(t, call(t, env, &Monitor{}, map[string]any{"task_id": task, "pattern": "hit", "description": "d", "persistent": true}))
	done, _ := env.Tasks.Done(task)
	<-done
	log.mu.Lock()
	var order []string
	for _, e := range log.events {
		if e.Kind == TaskEventNotify {
			order = append(order, "notice:"+e.Vars["lines"])
		} else if e.Kind == TaskEventFinished && e.Task.ID == task {
			order = append(order, "finished")
		}
	}
	log.mu.Unlock()
	if len(order) != 2 || order[0] != "notice:hit one\nhit two" || order[1] != "finished" {
		t.Fatalf("order %q", order)
	}
	waitFor(t, 5*time.Second, func() bool {
		info, _ := env.Tasks.Get(id)
		return !info.Running && info.Reason == TaskExited
	})
}

// A line the model has not read yet counts; what it read with TaskOutput does not.
func TestMonitorStartsAtReadOffset(t *testing.T) {
	env, log := monitorEnv(t)
	task := bgTask(t, env, "echo hit old; sleep 0.3; echo hit new; sleep 30")
	waitFor(t, 5*time.Second, func() bool {
		info, _ := env.Tasks.Get(task)
		st, err := os.Stat(info.OutputPath)
		return err == nil && st.Size() > 0
	})
	info, _ := env.Tasks.Get(task)
	st, _ := os.Stat(info.OutputPath)
	env.Tasks.SetReadOffset(task, st.Size())
	monitorID(t, call(t, env, &Monitor{}, map[string]any{"task_id": task, "pattern": "hit", "description": "d"}))
	waitFor(t, 5*time.Second, func() bool { return len(log.notices("monitor_match")) == 1 })
	if n := log.notices("monitor_match")[0]; n["lines"] != "hit new" {
		t.Fatalf("notice %+v", n)
	}
}
