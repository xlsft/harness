package tools

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestGraphDependencies(t *testing.T) {
	g := NewTaskGraph("")
	added, err := g.Add([]GraphTaskSpec{{Key: "build", Title: "Build"}, {Key: "test", Title: "Test", BlockedBy: []string{"build"}}})
	if err != nil {
		t.Fatal(err)
	}
	if added[0].ID != "t1" || added[1].BlockedBy[0] != "t1" {
		t.Fatalf("added: %+v", added)
	}
	if _, err := g.Claim("", "", "", "t2"); err == nil || !strings.Contains(err.Error(), "waits for t1") {
		t.Fatalf("claimed a blocked task: %v", err)
	}
	got, err := g.Claim("", "", "", "")
	if err != nil || got.ID != "t1" || got.OwnerName != GraphLead {
		t.Fatalf("claim: %+v %v", got, err)
	}
	if _, err := g.Claim("ag1", "one", "", ""); err == nil {
		t.Fatal("t2 is not free yet")
	}
	done := GraphDone
	if _, err := g.Update("", GraphUpdate{ID: "t1", Status: &done}); err != nil {
		t.Fatal(err)
	}
	if got, err := g.Claim("ag1", "one", "", ""); err != nil || got.ID != "t2" {
		t.Fatalf("claim after: %+v %v", got, err)
	}
}

func TestGraphRefusesCycles(t *testing.T) {
	g := NewTaskGraph("")
	if _, err := g.Add([]GraphTaskSpec{{Key: "a", Title: "A", BlockedBy: []string{"b"}}, {Key: "b", Title: "B", BlockedBy: []string{"a"}}}); err == nil || !strings.Contains(err.Error(), "wait for each other") {
		t.Fatalf("err: %v", err)
	}
	if len(g.List()) != 0 {
		t.Fatal("a refused call must add nothing")
	}
	if _, err := g.Add([]GraphTaskSpec{{Title: "A"}, {Title: "B", BlockedBy: []string{"t1"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Update("", GraphUpdate{ID: "t1", AddBlockedBy: []string{"t2"}}); err == nil {
		t.Fatal("a cycle through an update got in")
	}
	if len(g.List()[0].BlockedBy) != 0 {
		t.Fatal("the refused update stuck")
	}
	if _, err := g.Add([]GraphTaskSpec{{Title: "C", BlockedBy: []string{"nope"}}}); err == nil {
		t.Fatal("a dependency on nothing got in")
	}
}

func TestGraphClaimIsAtomic(t *testing.T) {
	g := NewTaskGraph("")
	specs := make([]GraphTaskSpec, 20)
	for i := range specs {
		specs[i].Title = "work"
	}
	if _, err := g.Add(specs); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]string{}
	var wg sync.WaitGroup
	for _, who := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				got, err := g.Claim(who, who, "", "")
				if err != nil {
					return
				}
				mu.Lock()
				if prev, dup := seen[got.ID]; dup {
					t.Errorf("%s claimed by %s and %s", got.ID, prev, who)
				}
				seen[got.ID] = who
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != 20 {
		t.Fatalf("%d claimed", len(seen))
	}
}

func TestGraphOwnershipAndTeams(t *testing.T) {
	g := NewTaskGraph("")
	if _, err := g.Add([]GraphTaskSpec{{Title: "Ours", Team: "red"}, {Title: "Anyone"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Claim("b1", "b", "blue", "t1"); err == nil {
		t.Fatal("another team took it")
	}
	if got := g.Ready("b1", "blue"); len(got) != 1 || got[0].ID != "t2" {
		t.Fatalf("ready: %+v", got)
	}
	if _, err := g.Claim("r1", "r", "red", "t1"); err != nil {
		t.Fatal(err)
	}
	failed := GraphFailed
	if _, err := g.Update("b1", GraphUpdate{ID: "t1", Status: &failed}); err == nil {
		t.Fatal("changed a task someone else holds")
	}
	if _, err := g.Update("", GraphUpdate{ID: "t1", Status: &failed}); err != nil {
		t.Fatal("the lead may")
	}
	in := GraphInProgress
	if _, err := g.Update("", GraphUpdate{ID: "t2", Status: &in}); err == nil {
		t.Fatal("in_progress goes through a claim")
	}
	if _, err := g.Claim("b1", "b", "blue", "t2"); err != nil {
		t.Fatal(err)
	}
	g.Release("b1", "gone")
	got := g.List()[1]
	if got.Status != GraphPending || got.Owner != "" || got.Note != "gone" {
		t.Fatalf("after release: %+v", got)
	}
}

func TestGraphPersistsAndFreesClaims(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s", "task-graph.json")
	g := NewTaskGraph(path)
	var told [][]GraphTask
	g.SetOnChange(func(l []GraphTask) { told = append(told, l) })
	if _, err := g.Add([]GraphTaskSpec{{Title: "A"}, {Title: "B"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Claim("ag", "x", "", "t1"); err != nil {
		t.Fatal(err)
	}
	if len(told) != 2 || told[1][0].Status != GraphInProgress {
		t.Fatalf("told: %+v", told)
	}
	again := NewTaskGraph(path)
	list := again.List()
	if len(list) != 2 || list[0].Status != GraphPending || list[0].Owner != "" {
		t.Fatalf("reloaded: %+v", list)
	}
	added, err := again.Add([]GraphTaskSpec{{Title: "C"}})
	if err != nil || added[0].ID != "t3" {
		t.Fatalf("ids restart: %+v %v", added, err)
	}
}

func TestGraphTools(t *testing.T) {
	env := NewEnv(t.TempDir(), t.TempDir())
	env.TaskGraph = NewTaskGraph("")
	run := func(tool Tool, in any) Result {
		b, _ := json.Marshal(in)
		return tool.Run(t.Context(), env, Call{ID: "c", Input: b})
	}
	if r := run(&TaskGraphAdd{}, map[string]any{"tasks": []map[string]any{{"title": "One"}, {"title": "Two", "blocked_by": []string{"t1"}}}}); r.IsError {
		t.Fatalf("%+v", r)
	}
	if r := run(&TaskGraphAdd{}, map[string]any{"tasks": []map[string]any{{"key": "x", "title": "Three", "blocked_by": []string{"x"}}}}); !r.IsError {
		t.Fatal("a task waiting for itself got in")
	}
	if r := run(&TaskGraphClaim{}, map[string]any{}); r.IsError || !strings.Contains(r.Content[0].Text, "t1") {
		t.Fatalf("%+v", r)
	}
	if r := run(&TaskGraphUpdate{}, map[string]any{"id": "t1", "status": "done", "note": "built"}); r.IsError {
		t.Fatalf("%+v", r)
	}
	list := run(&TaskGraphList{}, map[string]any{}).Content[0].Text
	if !strings.Contains(list, "t1 [done] One | built") || !strings.Contains(list, "t2 [pending] Two") || strings.Contains(list, "waits for") {
		t.Fatalf("list: %s", list)
	}
	env.TaskGraph = nil
	if r := run(&TaskGraphList{}, map[string]any{}); !r.IsError {
		t.Fatal("no graph should be an error")
	}
}

func TestScheduleWakeupInput(t *testing.T) {
	env := NewEnv(t.TempDir(), t.TempDir())
	run := func(in map[string]any) Result {
		b, _ := json.Marshal(in)
		return (&ScheduleWakeup{}).Run(t.Context(), env, Call{ID: "c", Input: b})
	}
	if r := run(map[string]any{"delay_seconds": 60}); !r.IsError {
		t.Fatal("a wake-up needs a note")
	}
	if r := run(map[string]any{"note": "x"}); !r.IsError {
		t.Fatal("a wake-up needs a time")
	}
	if r := run(map[string]any{"note": "x", "delay_seconds": 60, "every_seconds": 60}); !r.IsError {
		t.Fatal("only one time")
	}
	if r := run(map[string]any{"note": "x", "at": "tomorrow"}); !r.IsError {
		t.Fatal("not a time")
	}
	// Outside the agent service there's no scheduler to reach.
	if r := run(map[string]any{"note": "x", "delay_seconds": 60}); !r.IsError || !strings.Contains(r.Content[0].Text, "agent service") {
		t.Fatalf("%+v", r)
	}
}
