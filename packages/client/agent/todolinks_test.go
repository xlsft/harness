package agent_test

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/bootstrap"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/tools"
)

type reply = []*llmv1.StreamMessageResponse

// scriptMain answers the main agent's requests with steps in order, then with a closing text; agents get child.
func scriptMain(child func(r *llm.Request) fg.Reply, steps ...reply) fg.Responder {
	var next atomic.Int32
	return func(n int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.AgentID != "" {
			if child != nil {
				return child(r)
			}
			return fg.Reply{Events: fg.Message("c", fg.Text("agent done"))}
		}
		if i := int(next.Add(1)) - 1; i < len(steps) {
			return fg.Reply{Events: steps[i]}
		}
		return fg.Reply{Events: fg.Message("end", fg.Text("ok"))}
	}
}

// writeTodos is a TodoWrite call; items are "content=status".
func writeTodos(id string, items ...string) reply {
	list := make([]any, 0, len(items))
	for _, it := range items {
		content, status, _ := strings.Cut(it, "=")
		list = append(list, map[string]any{"content": content, "activeForm": "Doing " + content, "status": status})
	}
	return fg.Message("m_"+id, fg.ToolUse(id, "TodoWrite", map[string]any{"todos": list}))
}

func startAgent(id string) reply {
	return fg.Message("m_"+id, fg.ToolUse(id, "Agent", map[string]any{"description": "help", "prompt": "do it"}))
}

func startTask(id string) reply {
	return fg.Message("m_"+id, fg.ToolUse(id, "Bash", map[string]any{"command": "echo hi", "description": "bg", "run_in_background": true}))
}

func allowEcho(o *bootstrap.Options) { o.AllowedTools = []string{"Bash(echo *)"} }

func todoNamed(t *testing.T, list []tools.Todo, content string) tools.Todo {
	t.Helper()
	for _, it := range list {
		if it.Content == content {
			return it
		}
	}
	t.Fatalf("no todo %q in %+v", content, list)
	return tools.Todo{}
}

func todoLinkEntries(t *testing.T, s *bootstrap.Session) []session.TodoLink {
	t.Helper()
	entries, err := session.Load(s.Transcript.Path())
	if err != nil {
		t.Fatal(err)
	}
	var out []session.TodoLink
	for _, e := range entries {
		if e.Subtype == session.SubtypeTodoLink {
			out = append(out, *e.TodoLink)
		}
	}
	return out
}

func TestTodoLinkGoesToFirstInProgress(t *testing.T) {
	e := setup(t, scriptMain(nil,
		writeTodos("t1", "A=completed", "B=in_progress", "C=in_progress", "D=pending"),
		startAgent("a1"),
		startTask("b1"),
	))
	rec := &recorder{}
	s := e.open(t, func(o *bootstrap.Options) { allowEcho(o); o.OnEvent = rec.on })
	s.Agent.RunTurn(context.Background(), prompt("go"))

	list := s.Agent.Todos()
	b := todoNamed(t, list, "B")
	agents, tasks := s.Subagents.List(), s.Tasks.List()
	if len(agents) != 1 || len(tasks) != 1 {
		t.Fatalf("agents %+v tasks %+v", agents, tasks)
	}
	if !slices.Equal(b.AgentIDs, []string{agents[0].ID}) || !slices.Equal(b.TaskIDs, []string{tasks[0].ID}) {
		t.Fatalf("B %+v, agent %s, task %s", b, agents[0].ID, tasks[0].ID)
	}
	for _, other := range []string{"A", "C", "D"} {
		if it := todoNamed(t, list, other); len(it.AgentIDs)+len(it.TaskIDs) != 0 {
			t.Fatalf("%s got links: %+v", other, it)
		}
	}
	// Each link goes out as a new list, with no call from the model.
	evs := rec.find(agent.EventTodos)
	if len(evs) != 3 {
		t.Fatalf("todo events %d", len(evs))
	}
	if last := todoNamed(t, evs[2].Todos, "B"); len(last.AgentIDs) != 1 || len(last.TaskIDs) != 1 {
		t.Fatalf("last event %+v", last)
	}
	// The model sees nothing of it.
	for _, r := range mainRequests(e.srv) {
		if txt := reqText(r); strings.Contains(txt, "agentIds") || strings.Contains(txt, "taskIds") || strings.Contains(txt, "todo_link") {
			t.Fatalf("links reached the model: %s", txt)
		}
	}
}

func TestTodoLinkNeedsAnItemInProgress(t *testing.T) {
	e := setup(t, scriptMain(nil,
		startAgent("a0"), // no list yet
		writeTodos("t1", "A=completed", "B=pending"),
		startAgent("a1"),
		startTask("b1"),
	))
	s := e.open(t, allowEcho)
	s.Agent.RunTurn(context.Background(), prompt("go"))
	for _, it := range s.Agent.Todos() {
		if len(it.AgentIDs)+len(it.TaskIDs) != 0 {
			t.Fatalf("linked with nothing in progress: %+v", it)
		}
	}
	if got := todoLinkEntries(t, s); len(got) != 0 {
		t.Fatalf("links recorded: %+v", got)
	}
}

func TestTodoLinksCarryOverByContent(t *testing.T) {
	e := setup(t, scriptMain(nil,
		writeTodos("t1", "Kept=in_progress", "Later=pending"),
		startAgent("a1"),
		// Status changes keep the links; the next item in progress gets new work.
		writeTodos("t2", "Kept=completed", "Later=in_progress"),
		startTask("b1"),
		// Reworded: the link is gone, not moved to whatever sits at that position.
		writeTodos("t3", "Kept=completed", "Later, reworded=in_progress"),
		// A model naming ids in its input changes nothing.
		fg.Message("m_t4", fg.ToolUse("t4", "TodoWrite", map[string]any{"todos": []any{
			map[string]any{"content": "Kept", "activeForm": "Keeping", "status": "completed"},
			map[string]any{"content": "Later, reworded", "activeForm": "Going", "status": "in_progress", "agentIds": []string{"forged"}, "taskIds": []string{"forged"}},
		}})),
	))
	s := e.open(t, allowEcho)
	s.Agent.RunTurn(context.Background(), prompt("go"))

	list := s.Agent.Todos()
	kept := todoNamed(t, list, "Kept")
	if len(kept.AgentIDs) != 1 || kept.AgentIDs[0] != s.Subagents.List()[0].ID {
		t.Fatalf("completed item lost its ids: %+v", kept)
	}
	if re := todoNamed(t, list, "Later, reworded"); len(re.AgentIDs)+len(re.TaskIDs) != 0 {
		t.Fatalf("reworded item has links: %+v", re)
	}
}

func TestTodoLinksSurviveResumeAndFollowRewind(t *testing.T) {
	e := setup(t, scriptMain(nil,
		writeTodos("t1", "Work=in_progress", "Gone=pending"),
		fg.Message("m1", fg.Text("first turn done")),
		startAgent("a1"),
		startTask("b1"),
		writeTodos("t2", "Work=completed", "Next=in_progress"),
	))
	ctx := context.Background()
	s := e.open(t, allowEcho)
	s.Agent.RunTurn(ctx, prompt("plan"))
	s.Agent.RunTurn(ctx, prompt("work"))
	want := todoNamed(t, s.Agent.Todos(), "Work")
	if len(want.AgentIDs) != 1 || len(want.TaskIDs) != 1 {
		t.Fatalf("before resume %+v", want)
	}
	id := s.ID
	s.Close()

	s2 := e.open(t, func(o *bootstrap.Options) { o.Resume = id })
	got := todoNamed(t, s2.Agent.Todos(), "Work")
	if got.Status != "completed" || !slices.Equal(got.AgentIDs, want.AgentIDs) || !slices.Equal(got.TaskIDs, want.TaskIDs) {
		t.Fatalf("after resume %+v, want %+v", got, want)
	}

	// Back to before the second turn: the links made in it are on the branch left behind.
	cps := s2.Agent.Checkpoints()
	if _, err := s2.Agent.Rewind(ctx, cps[1].ID, agent.RewindConversation, ""); err != nil {
		t.Fatal(err)
	}
	back := todoNamed(t, s2.Agent.Todos(), "Work")
	if back.Status != "in_progress" || len(back.AgentIDs)+len(back.TaskIDs) != 0 {
		t.Fatalf("after rewind %+v", back)
	}
}

func TestSubagentTodosStaySeparate(t *testing.T) {
	var childStep atomic.Int32
	child := func(r *llm.Request) fg.Reply {
		switch childStep.Add(1) {
		case 1:
			return fg.Reply{Events: writeTodos("ct", "Own item=in_progress")}
		case 2:
			return fg.Reply{Events: startTask("cb")}
		}
		return fg.Reply{Events: fg.Message("cend", fg.Text("agent done"))}
	}
	e := setup(t, scriptMain(child,
		writeTodos("t1", "Main item=in_progress"),
		startAgent("a1"),
	))
	s := e.open(t, allowEcho)
	s.Agent.RunTurn(context.Background(), prompt("go"))
	waitUntil(t, 10*time.Second, func() bool {
		list := s.Subagents.List()
		return len(list) == 1 && !list[0].State.Active()
	})

	list := s.Agent.Todos()
	if len(list) != 1 || list[0].Content != "Main item" {
		t.Fatalf("the agent's list reached the session: %+v", list)
	}
	if len(list[0].AgentIDs) != 1 || len(list[0].TaskIDs) != 0 {
		t.Fatalf("main item %+v: the agent's own task must not link", list[0])
	}
	for _, l := range todoLinkEntries(t, s) {
		if l.Content != "Main item" {
			t.Fatalf("link to an agent's item: %+v", l)
		}
	}
}

// A monitor is an entry in the task list, but only the command it starts is work.
func TestMonitorLinksOnlyItsCommand(t *testing.T) {
	e := setup(t, scriptMain(nil,
		writeTodos("t1", "Watch=in_progress"),
		fg.Message("m_mon", fg.ToolUse("mon", "Monitor", map[string]any{"command": "echo hi", "pattern": "nothing", "description": "watch"})),
	))
	s := e.open(t, allowEcho)
	s.Agent.RunTurn(context.Background(), prompt("go"))
	it := todoNamed(t, s.Agent.Todos(), "Watch")
	if len(it.TaskIDs) != 1 {
		t.Fatalf("task ids %+v, tasks %+v", it.TaskIDs, s.Tasks.List())
	}
	if info, ok := s.Tasks.Get(it.TaskIDs[0]); !ok || info.Monitor {
		t.Fatalf("linked %+v", info)
	}
}
