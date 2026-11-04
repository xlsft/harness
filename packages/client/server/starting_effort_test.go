package server_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
)

func TestSessionWithoutAnEffortStartsOnTheModelsLevel(t *testing.T) {
	const low, medium, high, xhigh = llmv1.Effort_EFFORT_LOW, llmv1.Effort_EFFORT_MEDIUM, llmv1.Effort_EFFORT_HIGH, llmv1.Effort_EFFORT_XHIGH
	h := startHarnessSrv(t, fg.Sequence(), func(g *fg.Server) {
		g.Models = []string{"plain", "recommends", "medium", "around", "stale"}
		g.Capabilities = map[string]*llmv1.Capabilities{
			"recommends": {Effort: []llmv1.Effort{low, medium, high}, DefaultEffort: high},
			"medium":     {Effort: []llmv1.Effort{low, medium, high}},
			"around":     {Effort: []llmv1.Effort{low, xhigh}},
			"stale":      {Effort: []llmv1.Effort{low, medium}, DefaultEffort: high},
		}
	}, nil, nil)
	for model, want := range map[string]string{"plain": "", "recommends": "high", "medium": "medium", "around": "low", "stale": "medium"} {
		sid, _ := h.create(&agentv1.CreateSessionRequest{Model: model})
		if got := h.info(sid).Effort; got != want {
			t.Errorf("%s: effort %q, want %q", model, got, want)
		}
	}
	// A level asked for wins.
	sid, _ := h.create(&agentv1.CreateSessionRequest{Model: "recommends", Effort: low})
	if got := h.info(sid).Effort; got != "low" {
		t.Errorf("asked: effort %q", got)
	}
	// A session that exists does not take up the model's level when it is opened again.
	h.closeSession(sid)
	rs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid, Model: "recommends"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Msg.Session.GetEffort(); got != "" {
		t.Errorf("resumed: effort %q", got)
	}
}

func TestStartingEffortYieldsToTheClientsOwn(t *testing.T) {
	h := startHarnessSrv(t, fg.Sequence(), func(g *fg.Server) {
		g.Models = []string{"recommends"}
		g.Capabilities = map[string]*llmv1.Capabilities{
			"recommends": {Effort: []llmv1.Effort{llmv1.Effort_EFFORT_LOW, llmv1.Effort_EFFORT_HIGH}, DefaultEffort: llmv1.Effort_EFFORT_HIGH},
		}
	}, func(o *bootstrap.Options) { o.Effort = "low" }, nil)
	sid, _ := h.create(&agentv1.CreateSessionRequest{Model: "recommends"})
	if got := h.info(sid).Effort; got != "low" {
		t.Errorf("effort %q", got)
	}
}

func TestModelInfoSaysARecommendedLevelTheModelTakes(t *testing.T) {
	const low, medium, high = llmv1.Effort_EFFORT_LOW, llmv1.Effort_EFFORT_MEDIUM, llmv1.Effort_EFFORT_HIGH
	h := startHarnessSrv(t, fg.Sequence(), func(g *fg.Server) {
		g.Models = []string{"plain", "recommends", "stale"}
		g.Capabilities = map[string]*llmv1.Capabilities{
			"recommends": {Effort: []llmv1.Effort{low, high}, DefaultEffort: high},
			"stale":      {Effort: []llmv1.Effort{low, medium}, DefaultEffort: high},
		}
	}, nil, nil)
	init, err := h.client.Initialize(h.ctx, connect.NewRequest(&agentv1.InitializeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]llmv1.Effort{"plain": llmv1.Effort_EFFORT_UNSPECIFIED, "recommends": high, "stale": llmv1.Effort_EFFORT_UNSPECIFIED}
	for _, m := range init.Msg.Models {
		if got := m.GetDefaultEffort(); got != want[m.GetId()] {
			t.Errorf("%s: %v, want %v", m.GetId(), got, want[m.GetId()])
		}
	}
}
