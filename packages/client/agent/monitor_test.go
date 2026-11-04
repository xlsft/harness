package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/bootstrap"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/tools"
)

// A match wakes the idle session: a turn starts by itself with the matching line as its input, and the task's stopped
// command sends no second report.
func TestMonitorMatchWakesIdleSession(t *testing.T) {
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("t1", "Monitor", map[string]any{"command": "sleep 0.5; echo READY now; sleep 30", "pattern": "READY", "description": "server"}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	})
	s := e.open(t, func(o *bootstrap.Options) { o.AllowedTools = []string{"Bash(sleep *)", "Bash(echo *)"} })
	d := agent.NewDriver(context.Background(), s.Agent, nil)
	s.Agent.SetWake(d.Wake)
	d.Submit("u1", prompt("start it"))
	waitUntil(t, 10*time.Second, func() bool { return len(mainRequests(e.srv)) >= 3 })
	d.Stop()
	reqs := mainRequests(e.srv)
	if txt := reqText(reqs[2]); !strings.Contains(txt, "READY now") || !strings.Contains(txt, "first match") {
		t.Fatalf("wake turn:\n%s", txt)
	}
	waitUntil(t, 5*time.Second, func() bool { return s.Tasks.Running() == 0 })
	if s.Agent.HasReminders() {
		t.Fatal("a command its monitor stopped must not report again")
	}
}

// A notice that arrives during a turn's last model request starts a turn after it, instead of waiting for the user.
func TestWakeDuringLastRequestFollowsTheTurn(t *testing.T) {
	var s *bootstrap.Session
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			s.Agent.OnTaskEvent(notice("late match"))
			time.Sleep(50 * time.Millisecond)
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	})
	s = e.open(t, nil)
	d := agent.NewDriver(context.Background(), s.Agent, nil)
	s.Agent.SetWake(d.Wake)
	d.Submit("u1", prompt("hello"))
	waitUntil(t, 10*time.Second, func() bool { return len(mainRequests(e.srv)) >= 2 })
	d.Stop()
	if txt := reqText(mainRequests(e.srv)[1]); !strings.Contains(txt, "late match") {
		t.Fatalf("following turn:\n%s", txt)
	}
}

// Several monitors' notices before the next request all reach the model.
func TestMonitorNoticesAreNotDeduplicated(t *testing.T) {
	e := setup(t, func(n int, r *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("ok"))} })
	s := e.open(t, nil)
	s.Agent.OnTaskEvent(notice("first line"))
	s.Agent.OnTaskEvent(notice("second line"))
	s.Agent.RunTurn(context.Background(), prompt("next"))
	txt := reqText(mainRequests(e.srv)[0])
	if !strings.Contains(txt, "first line") || !strings.Contains(txt, "second line") {
		t.Fatalf("request:\n%s", txt)
	}
}

// A monitor that would run a command goes through the Bash rules: a denied command does not run.
func TestMonitorCommandIsCheckedAsBash(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	var results []string
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("t1", "Monitor", map[string]any{"command": "touch " + marker, "pattern": "x", "description": "d"}))}
		}
		for _, tr := range fg.LastToolResults(r) {
			results = append(results, fg.ResultText(tr))
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	})
	s := e.open(t, func(o *bootstrap.Options) { o.DisallowedTools = []string{"Bash(touch *)"} })
	s.Agent.RunTurn(context.Background(), prompt("go"))
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the denied command ran")
	}
	if len(results) != 1 || !strings.Contains(results[0], "denied") {
		t.Fatalf("results %q", results)
	}
	if len(s.Tasks.List()) != 0 {
		t.Fatalf("tasks %+v", s.Tasks.List())
	}
}

func notice(line string) tools.TaskEvent {
	return tools.TaskEvent{Kind: tools.TaskEventNotify, Reminder: "monitor_match", Vars: map[string]string{
		"task_id": "b1", "description": "d", "pattern": "x", "count": "1", "lines": line, "status": "s",
	}}
}
