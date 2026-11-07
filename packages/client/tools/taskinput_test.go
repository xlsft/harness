package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTaskInput(t *testing.T) {
	env, _ := newTestEnv(t)
	env.Tasks = NewTaskManager(t.TempDir())
	env.Shell = NewShell(env)
	defer env.Tasks.StopAll(TaskSessionClosed)
	ctx := context.Background()
	bash := func(input map[string]any) Result {
		return (&Bash{}).Run(ctx, env, Call{ID: "c", Input: inputOf(t, input)})
	}
	idOf := func(r Result) string {
		s, _ := r.Structured.(map[string]any)
		id, _ := s["backgroundTaskId"].(string)
		if id == "" {
			t.Fatalf("no task: %s", mtext(r))
		}
		return id
	}
	send := func(input map[string]any) Result {
		return (&TaskInput{}).Run(ctx, env, Call{Input: inputOf(t, input)})
	}

	// `cat` echoes what it reads, and ends when its input closes.
	id := idOf(bash(map[string]any{"command": "cat", "run_in_background": true, "stdin": true, "description": "d"}))
	if r := send(map[string]any{"task_id": id, "input": "hello\n"}); r.IsError {
		t.Fatal(mtext(r))
	}
	info, _ := env.Tasks.Get(id)
	waitForFile(t, info.OutputPath, "hello\n")
	if r := send(map[string]any{"task_id": id, "input": "bye\n", "close": true}); r.IsError {
		t.Fatal(mtext(r))
	}
	done, ok := env.Tasks.Done(id)
	if !ok {
		t.Fatal("no task")
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("closing the input did not end it")
	}
	waitForFile(t, info.OutputPath, "hello\nbye\n")
	if r := send(map[string]any{"task_id": id, "input": "more\n"}); !r.IsError {
		t.Fatal("wrote to a task that ended")
	}

	// Without stdin a command keeps what it had: no input to wait for, and none to write to.
	id = idOf(bash(map[string]any{"command": "sleep 30", "run_in_background": true, "description": "d"}))
	if r := send(map[string]any{"task_id": id, "input": "x\n"}); !r.IsError || !strings.Contains(mtext(r), "takes no input") {
		t.Fatalf("%s", mtext(r))
	}
	if r := send(map[string]any{"task_id": "nope", "close": true}); !r.IsError {
		t.Fatal("no such task")
	}
	if r := send(map[string]any{"task_id": id}); !r.IsError {
		t.Fatal("nothing to do")
	}
	if r := bash(map[string]any{"command": "cat", "stdin": true, "description": "d"}); !r.IsError {
		t.Fatal("stdin without the background")
	}
}

// waitForFile waits until the file holds exactly want.
func waitForFile(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		got, _ = os.ReadFile(path)
		if string(got) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file holds %q, want %q", got, want)
}
