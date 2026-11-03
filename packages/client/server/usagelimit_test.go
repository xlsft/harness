package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/auth"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/llm/v1/llmv1connect"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/server"
)

// limitsCore answers ProviderLimitsService.GetProviderLimits with a fixed provider for the model.
type limitsCore struct {
	llmv1connect.UnimplementedProviderLimitsServiceHandler
	mu     sync.Mutex
	model  *llmv1.ProviderLimits
	models []string
}

func (c *limitsCore) GetProviderLimits(_ context.Context, req *connect.Request[llmv1.GetProviderLimitsRequest]) (*connect.Response[llmv1.GetProviderLimitsResponse], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = append(c.models, req.Msg.GetModelId())
	return connect.NewResponse(&llmv1.GetProviderLimitsResponse{Model: c.model}), nil
}

func withLimitsCore(t *testing.T, c *limitsCore) func(*server.Server) {
	mux := http.NewServeMux()
	mux.Handle(llmv1connect.NewProviderLimitsServiceHandler(c))
	core := httptest.NewServer(mux)
	t.Cleanup(core.Close)
	return func(s *server.Server) {
		s.Core = &server.CoreProxy{URL: core.URL, HTTP: core.Client(), Tokens: auth.StaticToken("hat_test")}
	}
}

func usageEvents(evs []*agentv1.SessionEvent) []*agentv1.UsageLimit {
	var out []*agentv1.UsageLimit
	for _, e := range evs {
		if u := e.GetUsageLimit(); u != nil {
			out = append(out, u)
		}
	}
	return out
}

func TestUsageLimitWrapUpAndReplay(t *testing.T) {
	core := &limitsCore{model: &llmv1.ProviderLimits{ProviderId: "anthropic", ProviderName: "Anthropic", Billing: "plan",
		Windows: []*llmv1.LimitWindow{{Label: "5h", UsedPercent: 98, ResetsAt: "2099-01-01T23:00:00Z"}, {Label: "weekly", UsedPercent: 40}}}}
	h := startHarnessSrv(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("t1", "Read", map[string]any{"file_path": filepath.Join(fg.Cwd(r), "nothing.txt")}))}
		}
		return fg.Reply{Events: fg.Message("m2", fg.Text("Done; the rest is left."))}
	}, nil, nil, withLimitsCore(t, core))
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "refactor everything")
	evs := h.until(ch, isTurnFinished)
	got := usageEvents(evs)
	if len(got) != 2 {
		t.Fatalf("usage events: %v", got)
	}
	if u := got[0]; u.GetState() != agentv1.UsageLimit_STATE_WRAP_UP || u.GetProviderName() != "Anthropic" || u.GetWindowLabel() != "5h" ||
		u.GetUsedPercent() != 98 || u.GetResetsAt() != "2099-01-01T23:00:00Z" || u.GetWrappedUp() {
		t.Fatalf("wrap-up event: %v", u)
	}
	if !got[1].GetWrappedUp() || got[1].GetState() != agentv1.UsageLimit_STATE_WRAP_UP {
		t.Fatalf("wrapped-up event: %v", got[1])
	}
	reqs := h.gw.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: %d", len(reqs))
	}
	last := reqs[1].Messages[len(reqs[1].Messages)-1].Content
	if txt := last[len(last)-1].Text; !strings.Contains(txt, "Anthropic") || !strings.Contains(txt, "5h: 98% used") {
		t.Fatalf("no wrap-up reminder in the second request: %q", txt)
	}
	if info := h.info(sid); !info.GetUsageLimit().GetWrappedUp() || info.GetUsageLimit().GetState() != agentv1.UsageLimit_STATE_WRAP_UP {
		t.Fatalf("SessionInfo.usage_limit: %v", info.GetUsageLimit())
	}
	core.mu.Lock()
	if len(core.models) != 1 || core.models[0] != "fake-model" {
		t.Errorf("limits lookups (cached ~30 s): %v", core.models)
	}
	core.mu.Unlock()
	h.closeSession(sid)

	// Resumed: the history replays the usage events and SessionInfo knows the last state.
	if _, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid})); err != nil {
		t.Fatal(err)
	}
	var info *agentv1.SessionInfo
	replayed := h.until(h.subscribe(sid), func(ev *agentv1.SessionEvent) bool {
		info = ev.GetSessionUpdated()
		return info != nil
	})
	if r := usageEvents(replayed); len(r) != 2 || r[0].GetState() != agentv1.UsageLimit_STATE_WRAP_UP || !r[1].GetWrappedUp() {
		t.Fatalf("replayed usage events: %v", r)
	}
	if info.GetUsageLimit().GetState() != agentv1.UsageLimit_STATE_WRAP_UP {
		t.Fatalf("resumed SessionInfo.usage_limit: %v", info.GetUsageLimit())
	}
	h.closeSession(sid)
}

func TestUsageLimitHiddenLimitsOnlyReached(t *testing.T) {
	// The admin hid this shared provider's limits from the user: the server returns the provider without blocks.
	core := &limitsCore{model: &llmv1.ProviderLimits{ProviderId: "team-openai", ProviderName: "Team OpenAI", Billing: "metered"}}
	h := startHarnessSrv(t, func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("t1", "Read", map[string]any{"file_path": filepath.Join(fg.Cwd(r), "nothing.txt")}))}
		}
		return fg.Reply{Err: rpc.NewError(connect.CodeResourceExhausted, commonv1.ErrorType_ERROR_TYPE_QUOTA_EXCEEDED, "insufficient_quota", 0)}
	}, nil, nil, withLimitsCore(t, core))
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	got := usageEvents(h.until(ch, isTurnFinished))
	if len(got) != 1 {
		t.Fatalf("usage events: %v", got)
	}
	u := got[0]
	if u.GetState() != agentv1.UsageLimit_STATE_REACHED || u.GetProviderName() != "Team OpenAI" {
		t.Fatalf("reached: %v", u)
	}
	if u.GetWindowLabel() != "" || u.GetUsedPercent() != 0 || u.GetResetsAt() != "" || u.GetBalanceText() != "" || u.GetWrappedUp() {
		t.Fatalf("hidden limits leaked numbers: %v", u)
	}
	h.closeSession(sid)
}
