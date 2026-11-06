package subagent_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/bootstrap"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/subagent"
	"github.com/xlsft/harness/packages/client/tools"
)

func eventuallyTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func reqText(r *llm.Request) string {
	var b strings.Builder
	for _, m := range r.Messages {
		for _, c := range m.Content {
			if c.Type == llm.BlockText {
				b.WriteString(c.Text + "\n")
			}
		}
	}
	return b.String()
}

func graphOf(s *bootstrap.Session) *tools.TaskGraph { return s.ToolEnv.TaskGraph }

// A member that ends a turn with free tasks is sent on to them until none is left; then it settles and reports. Two
// members divide the work between them, and no task is done twice.
func TestTeamMembersWorkThroughTheGraph(t *testing.T) {
	var mu sync.Mutex
	turns := map[string]int{}
	claimed := map[string]int{}
	var s *bootstrap.Session
	s = open(t, func(n int, r *llm.Request) fg.Reply {
		if r.Metadata == nil || r.Metadata.AgentID == "" {
			return fg.Reply{Events: fg.Message("lead", fg.Text("noted"))}
		}
		id := r.Metadata.AgentID
		mu.Lock()
		turns[id]++
		step := turns[id]
		mu.Unlock()
		if step == 1 {
			text := reqText(r)
			if !strings.Contains(text, "team backend") {
				t.Errorf("no team context in the first message: %s", text)
			}
		}
		// Each round: take a task, finish it, end the turn.
		switch step % 3 {
		case 1:
			return fg.Reply{Events: fg.Message("c", fg.ToolUse("claim", "TaskGraphClaim", map[string]any{}))}
		case 2:
			held := graphOf(s).Held(id)
			if len(held) == 0 {
				return fg.Reply{Events: fg.Message("c", fg.Text("nothing left for me"))}
			}
			mu.Lock()
			claimed[held[0].ID]++
			mu.Unlock()
			return fg.Reply{Events: fg.Message("c", fg.ToolUse("up", "TaskGraphUpdate", map[string]any{"id": held[0].ID, "status": "done", "note": "did it"}))}
		default:
			return fg.Reply{Events: fg.Message("c", fg.Text("finished that one"))}
		}
	}, nil)
	ctx := context.Background()
	if _, err := graphOf(s).Add([]tools.GraphTaskSpec{{Title: "one"}, {Title: "two"}, {Key: "last", Title: "three", BlockedBy: []string{"t1", "t2"}}, {Title: "other team", Team: "frontend"}}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, name := range []string{"first", "second"} {
		info, err := s.Subagents.Spawn(ctx, "", tools.AgentSpec{Description: name, Prompt: "work on it", Team: "backend"})
		if err != nil {
			t.Fatal(err)
		}
		if info.Team != "backend" {
			t.Fatalf("info: %+v", info)
		}
		ids = append(ids, info.ID)
	}
	eventuallyTrue(t, "the team's tasks to be done", func() bool {
		list := graphOf(s).List()
		return list[0].Status == tools.GraphDone && list[1].Status == tools.GraphDone && list[2].Status == tools.GraphDone
	})
	eventuallyTrue(t, "the members to settle", func() bool { return s.Subagents.Active() == 0 })
	mu.Lock()
	defer mu.Unlock()
	for id, n := range claimed {
		if n != 1 {
			t.Fatalf("%s done %d times", id, n)
		}
	}
	if got := graphOf(s).List()[3]; got.Status != tools.GraphPending {
		t.Fatalf("a task of another team moved: %+v", got)
	}
	for _, id := range ids {
		if a, ok := s.Subagents.Get(id); !ok || a.State != tools.AgentFinished {
			t.Fatalf("agent: %+v", a)
		}
	}
}

// A settled member is woken when a task comes free; one that took nothing the last time isn't asked again, so an agent
// that won't work can't loop.
func TestSettledMemberIsWokenOnceForNewWork(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	s := open(t, func(n int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.AgentID != "" {
			mu.Lock()
			requests++
			mu.Unlock()
		}
		return fg.Reply{Events: fg.Message("c", fg.Text("not me"))}
	}, nil)
	ctx := context.Background()
	h, err := s.Subagents.SpawnSubagent(ctx, subagent.Options{AgentSpec: tools.AgentSpec{Description: "idler", Prompt: "wait", Team: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	eventuallyTrue(t, "it to settle", func() bool {
		a, _ := s.Subagents.Get(h.ID)
		return a.State == tools.AgentFinished
	})
	if _, err := graphOf(s).Add([]tools.GraphTaskSpec{{Title: "new"}}); err != nil {
		t.Fatal(err)
	}
	eventuallyTrue(t, "it to be told", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return requests == 2
	})
	eventuallyTrue(t, "it to settle again", func() bool {
		a, _ := s.Subagents.Get(h.ID)
		return a.State == tools.AgentFinished
	})
	// Nothing taken: another task doesn't ask it again.
	if _, err := graphOf(s).Add([]tools.GraphTaskSpec{{Title: "newer"}}); err != nil {
		t.Fatal(err)
	}
	a, _ := s.Subagents.Get(h.ID)
	if a.State != tools.AgentFinished {
		t.Fatal("woken again without having taken anything")
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Fatalf("%d requests", requests)
	}
}

// What a member holds when it stops goes free for the others.
func TestStoppedMemberReleasesItsTasks(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	s := open(t, func(n int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.AgentID != "" {
			<-gate
		}
		return fg.Reply{Events: fg.Message("c", fg.Text("x"))}
	}, nil)
	ctx := context.Background()
	if _, err := graphOf(s).Add([]tools.GraphTaskSpec{{Title: "one"}}); err != nil {
		t.Fatal(err)
	}
	info, err := s.Subagents.Spawn(ctx, "", tools.AgentSpec{Description: "holder", Prompt: "work", Team: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graphOf(s).Claim(info.ID, "holder", "t", "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Subagents.Stop("", info.ID); err != nil {
		t.Fatal(err)
	}
	eventuallyTrue(t, "the claim to go", func() bool { return graphOf(s).List()[0].Status == tools.GraphPending })
}

// Members reach each other by name, and the lead as "lead", which wakes it.
func TestPeersMessageEachOtherAndTheLead(t *testing.T) {
	var mu sync.Mutex
	var leadSaw, peerSaw string
	s := open(t, func(n int, r *llm.Request) fg.Reply {
		mu.Lock()
		defer mu.Unlock()
		if r.Metadata == nil || r.Metadata.AgentID == "" {
			leadSaw += reqText(r)
		} else if strings.Contains(reqText(r), "the schema changed") {
			peerSaw = reqText(r)
		}
		return fg.Reply{Events: fg.Message("c", fg.Text("ok"))}
	}, nil)
	ctx := context.Background()
	drv := agent.NewDriver(ctx, s.Agent, nil)
	s.Agent.SetWake(drv.Wake)
	t.Cleanup(func() {
		drv.Stop()
		drv.Wait()
	})
	a, err := s.Subagents.Spawn(ctx, "", tools.AgentSpec{Description: "alpha", Prompt: "p", Team: "t"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Subagents.Spawn(ctx, "", tools.AgentSpec{Description: "beta", Prompt: "p", Team: "t"})
	if err != nil {
		t.Fatal(err)
	}
	eventuallyTrue(t, "both to settle", func() bool { return s.Subagents.Active() == 0 })
	if _, _, err := s.Subagents.Send(ctx, a.ID, "beta", "the schema changed"); err != nil {
		t.Fatal(err)
	}
	eventuallyTrue(t, "the peer to read it", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(peerSaw, "alpha")
	})
	if _, queued, err := s.Subagents.Send(ctx, b.ID, tools.GraphLead, "I need the credentials"); err != nil || !queued {
		t.Fatalf("to the lead: %v %v", queued, err)
	}
	eventuallyTrue(t, "the lead to read it", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(leadSaw, "I need the credentials") && strings.Contains(leadSaw, "beta")
	})
	// The lead itself has no one called lead to write to.
	if _, _, err := s.Subagents.Send(ctx, "", tools.GraphLead, "x"); err == nil {
		t.Fatal("the lead wrote to itself")
	}
}
