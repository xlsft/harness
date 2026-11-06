package server_test

import (
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
)

// goalModel answers working turns with `work` and checks with what verdict says, by the check's call number.
func goalModel(checks *atomic.Int32, verdict func(n int32) string) fg.Responder {
	return func(_ int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.RequestClass == "auxiliary" {
			return answer(verdict(checks.Add(1)))
		}
		return answer("worked on it")
	}
}

func notMet(n int32) string { return `{"met": false, "reason": "the tests still fail"}` }

// goalUntil follows the events until the goal is in one of the states and returns it.
func goalUntil(h *harness, ch <-chan *agentv1.SessionEvent, states ...agentv1.GoalState) *agentv1.Goal {
	h.t.Helper()
	var out *agentv1.Goal
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		g := ev.GetGoalUpdated()
		if g == nil {
			return false
		}
		for _, s := range states {
			if g.GetGoal().GetState() == s {
				out = g.GetGoal()
				return true
			}
		}
		return false
	})
	return out
}

func TestGoalKeepsWorkingUntilTheCheckSaysItHolds(t *testing.T) {
	var checks atomic.Int32
	h, _ := startScheduling(t, goalModel(&checks, func(n int32) string {
		if n < 3 {
			return `{"met": false, "reason": "the tests still fail"}`
		}
		return "```json\n{\"met\": true, \"reason\": \"the tests pass\"}\n```"
	}), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "/goal --max 10 the tests pass")
	g := goalUntil(h, ch, agentv1.GoalState_GOAL_STATE_MET)
	if g.GetTurns() != 3 || g.GetMaxTurns() != 10 || g.GetReason() != "the tests pass" || g.GetCondition() != "the tests pass" {
		t.Fatalf("goal: %+v", g)
	}
	reqs := h.gw.Requests()
	var work []string
	check := ""
	for _, r := range reqs {
		if r.Metadata != nil && r.Metadata.RequestClass == "auxiliary" {
			check = allRequestText(r)
		} else {
			work = append(work, allRequestText(r))
		}
	}
	if len(work) != 3 {
		t.Fatalf("%d working requests", len(work))
	}
	if !strings.Contains(work[0], "Goal: the tests pass") || !strings.Contains(work[0], "<goal>") {
		t.Fatalf("first: %s", work[0])
	}
	if !strings.Contains(work[1], "the tests still fail") {
		t.Fatalf("second should say what the check said: %s", work[1])
	}
	if !strings.Contains(check, "<condition>") || !strings.Contains(check, "worked on it") {
		t.Fatalf("check: %s", check)
	}
	// Met: no more turns.
	if r := slashText(h, ch, sid, "/goal"); !strings.Contains(r.GetText(), "met") {
		t.Fatalf("status: %+v", r)
	}
}

func TestGoalStopsAtTheCap(t *testing.T) {
	var checks atomic.Int32
	h, _ := startScheduling(t, goalModel(&checks, notMet), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "/goal --max 2 impossible")
	g := goalUntil(h, ch, agentv1.GoalState_GOAL_STATE_CAP_REACHED)
	if g.GetTurns() != 2 {
		t.Fatalf("goal: %+v", g)
	}
	// Resume allows as many more.
	h.send(sid, "/goal resume")
	g = goalUntil(h, ch, agentv1.GoalState_GOAL_STATE_CAP_REACHED)
	if g.GetTurns() != 4 || g.GetMaxTurns() != 4 {
		t.Fatalf("after resume: %+v", g)
	}
}

func TestGoalRefusals(t *testing.T) {
	var checks atomic.Int32
	h, _ := startScheduling(t, goalModel(&checks, func(int32) string { return `{"met": true, "reason": "ok"}` }), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	if r := slashText(h, ch, sid, "/goal"); r.GetIsError() || !strings.Contains(r.GetText(), "No goal") {
		t.Fatalf("%+v", r)
	}
	if r := slashText(h, ch, sid, "/goal --max 9999 x"); !r.GetIsError() {
		t.Fatalf("%+v", r)
	}
	if r := slashText(h, ch, sid, "/goal --max 3"); !r.GetIsError() {
		t.Fatalf("%+v", r)
	}
	if r := slashText(h, ch, sid, "/goal resume"); !r.GetIsError() {
		t.Fatalf("%+v", r)
	}
}

func TestGoalPausesWhenTheCheckCannotBeRead(t *testing.T) {
	var checks atomic.Int32
	h, _ := startScheduling(t, goalModel(&checks, func(int32) string { return "I think so?" }), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "/goal done")
	g := goalUntil(h, ch, agentv1.GoalState_GOAL_STATE_PAUSED)
	if !strings.Contains(g.GetReason(), "could not run") || checks.Load() != 3 {
		t.Fatalf("goal: %+v after %d checks", g, checks.Load())
	}
}

func TestGoalStopsOnRequestAndComesBackPaused(t *testing.T) {
	gate := make(chan struct{})
	var checks atomic.Int32
	h, _ := startScheduling(t, func(_ int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.RequestClass == "auxiliary" {
			checks.Add(1)
			return answer(notMet(0))
		}
		<-gate
		return answer("worked")
	}, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "/goal --max 50 never")
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetTurnStarted() != nil })
	// Interrupting pauses it.
	if _, err := h.client.Interrupt(h.ctx, connect.NewRequest(&agentv1.InterruptRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	g := goalUntil(h, ch, agentv1.GoalState_GOAL_STATE_PAUSED)
	if !strings.Contains(g.GetReason(), "interrupted") {
		t.Fatalf("goal: %+v", g)
	}
	close(gate)
	if checks.Load() != 0 {
		t.Fatalf("%d checks after the pause", checks.Load())
	}

	// Closed and opened again: the goal is on record and doesn't start by itself.
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid})); err != nil {
		t.Fatal(err)
	}
	ch2 := h.subscribe(sid)
	g = goalUntil(h, ch2, agentv1.GoalState_GOAL_STATE_PAUSED)
	if g.GetCondition() != "never" {
		t.Fatalf("resumed: %+v", g)
	}
	if r := slashText(h, ch2, sid, "/goal stop"); !strings.Contains(r.GetText(), "stopped") {
		t.Fatalf("%+v", r)
	}
}
