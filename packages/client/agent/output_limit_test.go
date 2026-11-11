package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/agent"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
)

// A reply that spent the whole output limit on thinking has nothing to show: the turn ends with an error that says
// what to change, instead of ending quietly as if the model had answered.
func TestOutputLimitSpentOnThinkingFailsTheTurn(t *testing.T) {
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.CutOff(fg.Message("m", fg.Thinking("planning the page...", "sig")), 8192)}
	})
	s := e.open(t, nil)
	defer s.Close()
	res := s.Agent.RunTurn(context.Background(), prompt("make an artifact"))
	if !res.IsError || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "8192 tokens") || !strings.Contains(res.Errors[0], "Settings > Models") {
		t.Fatalf("result: %+v", res)
	}
	if n := len(e.srv.Requests()); n != 1 {
		t.Fatalf("requests: %d", n)
	}
	for _, m := range s.Agent.Messages() {
		if m.Role == llm.RoleAssistant {
			t.Fatalf("the cut-off reply stays out of the history: %+v", m)
		}
	}
}

// A tool call cut off midway has incomplete input: it never runs, and the turn ends with the error.
func TestOutputLimitInsideAToolCallDoesNotRunIt(t *testing.T) {
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.CutOff(fg.Message("m", fg.Text("Writing it."), fg.ToolUse("t1", "Bash", map[string]any{"description": "x", "command": "touch ran"})), 8192)}
	})
	s := e.open(t, nil)
	defer s.Close()
	res := s.Agent.RunTurn(context.Background(), prompt("go"))
	if !res.IsError || len(e.srv.Requests()) != 1 {
		t.Fatalf("result: %+v, requests %d", res, len(e.srv.Requests()))
	}
	for _, m := range s.Agent.Messages() {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolUse || b.Type == llm.BlockToolResult {
				t.Fatalf("the cut-off call reached the history: %+v", m)
			}
		}
	}
}

// An answer cut off midway is kept, and the model is asked to go on from where it stopped.
func TestOutputLimitMidAnswerResumes(t *testing.T) {
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.CutOff(fg.Message("m1", fg.Text("first half")), 8192)}
		}
		return fg.Reply{Events: fg.Message("m2", fg.Text(" second half"))}
	})
	s := e.open(t, nil)
	defer s.Close()
	res := s.Agent.RunTurn(context.Background(), prompt("write"))
	if res.IsError || res.Subtype != agent.SubtypeSuccess || res.Result != " second half" {
		t.Fatalf("result: %+v", res)
	}
	reqs := e.srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: %d", len(reqs))
	}
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Text(), "output token limit") {
		t.Fatalf("resume request ends with %+v", last)
	}
}

// Resuming has a limit: an answer that keeps getting cut off ends the turn with the error.
func TestOutputLimitResumesAreCapped(t *testing.T) {
	e := setup(t, func(n int, r *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.CutOff(fg.Message("m", fg.Text("more")), 8192)}
	})
	s := e.open(t, nil)
	defer s.Close()
	res := s.Agent.RunTurn(context.Background(), prompt("write"))
	if !res.IsError || len(e.srv.Requests()) != 4 {
		t.Fatalf("result: %+v, requests %d", res, len(e.srv.Requests()))
	}
}
