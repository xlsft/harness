package server_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
)

func offered(req *llm.Request) []string {
	var names []string
	for _, t := range req.Tools {
		names = append(names, t.Name)
	}
	return names
}

func TestAdvisorConsultsTheChosenModelWithTheConversation(t *testing.T) {
	var asked *llm.Request
	respond := fg.Responder(func(_ int, req *llm.Request) fg.Reply {
		if req.Model == "wise" {
			asked = req
			return fg.Reply{Events: fg.Message("msg_a", fg.Text("Prefer the smaller change."))}
		}
		if res := fg.LastToolResults(req); len(res) > 0 {
			return fg.Reply{Events: fg.Message("msg_2", fg.Text("Done: "+fg.ResultText(res[0])))}
		}
		return fg.Reply{Events: fg.Message("msg_1", fg.ToolUse("t1", "Advisor", map[string]any{"question": "Which way?"}))}
	})
	h := startHooked(t, respond, []byte(`{"advisorModel":"wise","speedTier":"fast","cacheTtl":"1h"}`))
	h.gw.Models = []string{"main", "wise"}
	h.gw.Capabilities = map[string]*llmv1.Capabilities{"wise": {Effort: []llmv1.Effort{llmv1.Effort_EFFORT_LOW, llmv1.Effort_EFFORT_HIGH}}}
	sid, ch := h.create(&agentv1.CreateSessionRequest{Model: "main"})
	h.send(sid, "the conversation so far")
	h.until(ch, func(e *agentv1.SessionEvent) bool { return e.GetTurnFinished() != nil })

	reqs := h.gw.Requests()
	if !slices.Contains(offered(reqs[0]), "Advisor") {
		t.Fatalf("the session has no Advisor tool: %v", offered(reqs[0]))
	}
	if asked == nil {
		t.Fatal("the advisor model was not asked")
	}
	if len(asked.Tools) != 0 {
		t.Errorf("the advisor got tools: %v", offered(asked))
	}
	if asked.Effort != "high" {
		t.Errorf("advisor effort %q", asked.Effort)
	}
	last := asked.Messages[len(asked.Messages)-1].Text()
	if !strings.Contains(last, "Which way?") {
		t.Errorf("the question is missing: %q", last)
	}
	var system []string
	for _, s := range asked.System {
		system = append(system, s.Text)
	}
	if joined := strings.Join(system, "\n"); !strings.Contains(joined, "<main_session>") || !strings.Contains(joined, "the conversation so far") {
		t.Errorf("the advisor did not get the conversation: %q", joined)
	}
	final := reqs[len(reqs)-1]
	if got := fg.ResultText(fg.LastToolResults(final)[0]); got != "Prefer the smaller change." {
		t.Errorf("tool result %q", got)
	}
	// The session's requests carry the tuning; the advisor's own request does not.
	if reqs[0].Speed != "fast" || reqs[0].CacheTTL != "1h" {
		t.Errorf("speed %q, cache %q", reqs[0].Speed, reqs[0].CacheTTL)
	}
	if asked.Speed != "" {
		t.Errorf("advisor speed %q", asked.Speed)
	}
}

func TestNoAdvisorModelMeansNoAdvisorTool(t *testing.T) {
	h := startHooked(t, fg.Sequence(func(*llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("ok"))} }), []byte(`{}`))
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "hi")
	h.until(ch, func(e *agentv1.SessionEvent) bool { return e.GetTurnFinished() != nil })
	req := h.gw.Requests()[0]
	if slices.Contains(offered(req), "Advisor") {
		t.Error("Advisor offered without a model")
	}
	if req.Speed != "" || req.CacheTTL != "" {
		t.Errorf("speed %q, cache %q", req.Speed, req.CacheTTL)
	}
}

func TestUnknownAdvisorModelLeavesTheToolOutWithAWarning(t *testing.T) {
	h := startHooked(t, fg.Sequence(func(*llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("ok"))} }), []byte(`{"advisorModel":"gone"}`))
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "hi")
	h.until(ch, func(e *agentv1.SessionEvent) bool { return e.GetTurnFinished() != nil })
	if slices.Contains(offered(h.gw.Requests()[0]), "Advisor") {
		t.Error("Advisor offered with a model the catalog does not have")
	}
}

func TestSpeedAndCacheTravelTheWire(t *testing.T) {
	// Through JSON too: the HTTP gateway path uses the same names as the proto.
	b, err := json.Marshal(&llm.Request{Model: "m", Speed: "fast", CacheTTL: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); !strings.Contains(s, `"speed":"fast"`) || !strings.Contains(s, `"cache_ttl":"1h"`) {
		t.Errorf("%s", s)
	}
}
