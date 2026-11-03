package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runBash runs a foreground Bash call with the given tool use id in the
// background of the test and returns its result channel.
func runBash(env *Env, id string, input map[string]any) <-chan Result {
	ch := make(chan Result, 1)
	raw, _ := json.Marshal(input)
	go func() { ch <- (&Bash{}).Run(context.Background(), env, Call{ID: id, Input: raw}) }()
	return ch
}

// waitRunning waits until the tool call's foreground command is registered.
func waitRunning(t *testing.T, env *Env, id string) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool {
		env.Shell.mu.Lock()
		defer env.Shell.mu.Unlock()
		for fg := range env.Shell.running {
			if fg.toolUseID == id {
				return true
			}
		}
		return false
	})
}

func TestBackgroundToolCallKeepsProcessRunning(t *testing.T) {
	env := newEnv(t)
	env.Tasks = NewTaskManager(filepath.Join(env.ScratchDir, "tasks"))
	defer env.Tasks.StopAll(TaskSessionClosed)
	marker := filepath.Join(env.Cwd, "done")
	ch := runBash(env, "toolu_bg", map[string]any{"command": "echo before; sleep 0.6; echo after; touch " + marker})
	waitRunning(t, env, "toolu_bg")
	if env.Shell.BackgroundToolCall("toolu_other", BackgroundUser) {
		t.Fatal("another tool call must not match")
	}
	time.Sleep(150 * time.Millisecond) // let "before" be captured
	if !env.Shell.BackgroundToolCall("toolu_bg", BackgroundUser) {
		t.Fatal("running command not found")
	}
	var r Result
	select {
	case r = <-ch:
	case <-time.After(400 * time.Millisecond):
		t.Fatal("tool call did not complete right away")
	}
	sm := r.Structured.(map[string]any)
	id, _ := sm["backgroundTaskId"].(string)
	if r.IsError || id == "" || sm["backgroundReason"] != BackgroundUser {
		t.Fatalf("result %q %+v", text(r), sm)
	}
	if !strings.Contains(text(r), id) || !strings.Contains(text(r), "The user moved this command") || !strings.Contains(text(r), "TaskOutput") || !strings.Contains(text(r), "TaskStop") {
		t.Fatalf("note %q", text(r))
	}
	// The process keeps running and its output lands in the task file.
	info, ok := env.Tasks.Get(id)
	if !ok || !info.Running || info.ToolUseID != "toolu_bg" {
		t.Fatalf("task %+v", info)
	}
	waitFor(t, 5*time.Second, func() bool { info, _ := env.Tasks.Get(id); return !info.Running })
	info, _ = env.Tasks.Get(id)
	if data, _ := os.ReadFile(info.OutputPath); string(data) != "before\nafter\n" || info.ExitCode != 0 {
		t.Fatalf("task output %q exit %d", data, info.ExitCode)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("process was killed instead of backgrounded")
	}
	if env.Shell.BackgroundToolCall("toolu_bg", BackgroundUser) {
		t.Fatal("finished call must not match")
	}
}

func TestBackgroundNotes(t *testing.T) {
	env := newEnv(t)
	env.Tasks = NewTaskManager(filepath.Join(env.ScratchDir, "tasks"))
	defer env.Tasks.StopAll(TaskSessionClosed)
	check := func(r Result, reason, phrase string) {
		t.Helper()
		sm := r.Structured.(map[string]any)
		id, _ := sm["backgroundTaskId"].(string)
		if r.IsError || id == "" || sm["backgroundReason"] != reason || !strings.Contains(text(r), id) || !strings.Contains(text(r), phrase) {
			t.Fatalf("%s: %q %+v", reason, text(r), sm)
		}
	}
	for _, reason := range []string{BackgroundUser, BackgroundQueuedMessage} {
		ch := runBash(env, "toolu_"+reason, map[string]any{"command": "sleep 5"})
		waitRunning(t, env, "toolu_"+reason)
		env.Shell.BackgroundToolCall("toolu_"+reason, reason)
		check(<-ch, reason, map[string]string{BackgroundUser: "The user moved", BackgroundQueuedMessage: "so that a message the user sent"}[reason])
	}
	check(call(t, env, &Bash{}, map[string]any{"command": "sleep 5", "run_in_background": true}), BackgroundAgent, "You started this command in the background")
	check(call(t, env, &Bash{}, map[string]any{"command": "echo x; sleep 5", "timeout": 200}), BackgroundAuto, "did not finish within its 200ms timeout")

	// Bundle templates override the fallbacks.
	env.Reminders = func(k string) string {
		if k == "bash_background_user" {
			return "custom {{task_id}} at {{output_path}}"
		}
		return ""
	}
	if got := BackgroundNote(env, BackgroundUser, "b1", "/o", 0); got != "custom b1 at /o" {
		t.Fatalf("template: %q", got)
	}
	if got := BackgroundNote(env, BackgroundAgent, "b2", "/o", 0); !strings.Contains(got, "b2") {
		t.Fatalf("fallback: %q", got)
	}
}
