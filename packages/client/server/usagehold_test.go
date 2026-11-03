package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// modelLimits answers ProviderLimitsService per model.
type modelLimits struct {
	llmv1connect.UnimplementedProviderLimitsServiceHandler
	mu     sync.Mutex
	limits func(model string) *llmv1.ProviderLimits
}

func (c *modelLimits) GetProviderLimits(_ context.Context, req *connect.Request[llmv1.GetProviderLimitsRequest]) (*connect.Response[llmv1.GetProviderLimitsResponse], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return connect.NewResponse(&llmv1.GetProviderLimitsResponse{Model: c.limits(req.Msg.GetModelId())}), nil
}

func (c *modelLimits) set(f func(string) *llmv1.ProviderLimits) {
	c.mu.Lock()
	c.limits = f
	c.mu.Unlock()
}

// holdRig is a server whose gateway refuses requests for limits while refuse says so.
type holdRig struct {
	*harness
	refuse atomic.Bool
	// refuseModel, when set, decides per request instead of refuse.
	refuseModel func(model string) bool
	// gate, when set, blocks the first conversation request until it is closed; entered is closed when it arrives.
	gate, entered chan struct{}
	once          sync.Once
	probes        atomic.Int32
	limits        *modelLimits
}

func isProbe(r *llm.Request) bool { return r.Metadata != nil && r.Metadata.RequestClass == "auxiliary" }

func window(provider string, used float64, resets string) *llmv1.ProviderLimits {
	return &llmv1.ProviderLimits{ProviderId: provider, ProviderName: provider, Billing: "plan", Windows: []*llmv1.LimitWindow{{Label: "5h", UsedPercent: used, ResetsAt: resets}}}
}

// resetsIn is a reset time about d away (RFC 3339 has whole seconds).
func resetsIn(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339) }

const farReset = "2099-01-01T00:00:00Z"

func startHold(t *testing.T, limits func(string) *llmv1.ProviderLimits, tune func(*holdRig), srvmods ...func(*server.Server)) *holdRig {
	t.Helper()
	rig := &holdRig{limits: &modelLimits{limits: limits}}
	if tune != nil {
		tune(rig)
	}
	rig.refuse.Store(true)
	mux := http.NewServeMux()
	mux.Handle(llmv1connect.NewProviderLimitsServiceHandler(rig.limits))
	core := httptest.NewServer(mux)
	t.Cleanup(core.Close)
	rig.harness = startHarnessSrv(t, func(_ int, r *llm.Request) fg.Reply {
		if isProbe(r) {
			rig.probes.Add(1)
		} else if rig.gate != nil {
			rig.once.Do(func() {
				close(rig.entered)
				<-rig.gate
			})
		}
		refused := rig.refuse.Load()
		if rig.refuseModel != nil {
			refused = rig.refuseModel(r.Model)
		}
		if refused {
			return fg.Reply{Err: rpc.NewError(connect.CodeResourceExhausted, commonv1.ErrorType_ERROR_TYPE_QUOTA_EXCEEDED, "insufficient_quota", 0)}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ack"))}
	}, func(g *fg.Server) { g.Models = []string{"fake-model", "same-provider", "other-provider"} }, nil, func(s *server.Server) {
		s.NoAITitles = true
		s.Core = &server.CoreProxy{URL: core.URL, HTTP: core.Client(), Tokens: auth.StaticToken("hat_test")}
		s.UsageWatch = server.UsageWatchTimings{ResetMargin: 20 * time.Millisecond, RecheckMin: 20 * time.Millisecond, RecheckMax: 40 * time.Millisecond, ProbeEvery: time.Hour}
		for _, m := range srvmods {
			m(s)
		}
	})
	return rig
}

func tunedWatch(probeEvery time.Duration) func(*server.Server) {
	return func(s *server.Server) { s.UsageWatch.ProbeEvery = probeEvery }
}

// conversation is the number of requests that are not probes.
func (r *holdRig) conversation() []*llm.Request {
	var out []*llm.Request
	for _, q := range r.gw.Requests() {
		if !isProbe(q) {
			out = append(out, q)
		}
	}
	return out
}

// reach cuts a first turn off with the limit.
func (r *holdRig) reach(t *testing.T) (string, <-chan *agentv1.SessionEvent) {
	t.Helper()
	sid, ch := r.create(&agentv1.CreateSessionRequest{})
	r.send(sid, "first")
	u := r.untilUsage(ch, agentv1.UsageLimit_STATE_REACHED)
	if !u.GetInterrupted() {
		t.Fatalf("reached without interrupted: %v", u)
	}
	r.until(ch, isTurnFinished)
	return sid, ch
}

func (r *holdRig) untilUsage(ch <-chan *agentv1.SessionEvent, st agentv1.UsageLimit_State) *agentv1.UsageLimit {
	r.t.Helper()
	var u *agentv1.UsageLimit
	r.until(ch, func(ev *agentv1.SessionEvent) bool {
		if x := ev.GetUsageLimit(); x != nil && x.GetState() == st {
			u = x
			return true
		}
		return false
	})
	return u
}

// untilQueue waits for a QueueChanged naming exactly these held ids.
func (r *holdRig) untilQueue(ch <-chan *agentv1.SessionEvent, held ...string) {
	r.t.Helper()
	r.until(ch, func(ev *agentv1.SessionEvent) bool {
		q := ev.GetQueueChanged()
		if q == nil || len(q.GetQueued()) != len(held) {
			return false
		}
		for i, m := range q.GetQueued() {
			if m.GetUuid() != held[i] || !m.GetHeld() {
				return false
			}
		}
		return true
	})
}

func (r *holdRig) setAuto(sid string, on bool) {
	r.t.Helper()
	if _, err := r.client.SetAutoContinue(r.ctx, connect.NewRequest(&agentv1.SetAutoContinueRequest{SessionId: sid, Enabled: on})); err != nil {
		r.t.Fatal(err)
	}
}

func (r *holdRig) setModel(sid, model string) {
	r.t.Helper()
	if _, err := r.client.SetModel(r.ctx, connect.NewRequest(&agentv1.SetModelRequest{SessionId: sid, Model: model})); err != nil {
		r.t.Fatal(err)
	}
}

// quiet asserts nothing reaches the model for a moment.
func (r *holdRig) quiet(t *testing.T, conversation int) {
	t.Helper()
	time.Sleep(150 * time.Millisecond)
	if got := r.conversation(); len(got) != conversation {
		t.Fatalf("conversation requests: %d, want %d", len(got), conversation)
	}
}

func lastRequestText(r *llm.Request) string {
	var parts []string
	for _, b := range r.Messages[len(r.Messages)-1].Content {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "|")
}

func TestHeldOnReached(t *testing.T) {
	rig := startHold(t, func(string) *llmv1.ProviderLimits { return window("a", 100, farReset) }, nil)
	sid, ch := rig.reach(t)
	if info := rig.info(sid); info.GetStatus() != agentv1.SessionStatus_SESSION_STATUS_IDLE || info.GetUsageLimit().GetResetsAt() != farReset {
		t.Fatalf("after the refusal: %v", info)
	}
	rig.sendID(sid, "m2", "second", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	rig.untilQueue(ch, "m2")
	rig.quiet(t, 1)
	if st := rig.info(sid).GetStatus(); st != agentv1.SessionStatus_SESSION_STATUS_IDLE {
		t.Fatalf("status while held: %v", st)
	}

	// A held message is cancelled like any queued one.
	res, err := rig.client.RemoveQueuedMessage(rig.ctx, connect.NewRequest(&agentv1.RemoveQueuedMessageRequest{SessionId: sid, Uuid: "m2"}))
	if err != nil || !res.Msg.GetRemoved() {
		t.Fatalf("remove: %v %v", res, err)
	}
	rig.untilQueue(ch)
	rig.closeSession(sid)
}

func TestTurnFailingWithReachedKeepsQueueHeld(t *testing.T) {
	rig := startHold(t, func(string) *llmv1.ProviderLimits { return window("a", 100, farReset) }, func(r *holdRig) {
		r.gate, r.entered = make(chan struct{}), make(chan struct{})
	})
	sid, ch := rig.create(&agentv1.CreateSessionRequest{})
	rig.send(sid, "first")
	<-rig.entered
	rig.sendID(sid, "m2", "second", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	close(rig.gate)
	rig.untilQueue(ch, "m2")
	rig.quiet(t, 1)
	rig.closeSession(sid)
}

func TestHeldReleaseOnModelSwitch(t *testing.T) {
	rig := startHold(t, func(model string) *llmv1.ProviderLimits {
		if model == "other-provider" {
			return window("b", 10, farReset)
		}
		return window("a", 100, farReset)
	}, func(r *holdRig) { r.refuseModel = func(m string) bool { return m != "other-provider" } })
	sid, ch := rig.reach(t)
	rig.sendID(sid, "m2", "second", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	rig.sendID(sid, "m3", "third", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	rig.untilQueue(ch, "m2", "m3")

	// Another model of the same provider changes nothing.
	rig.setModel(sid, "same-provider")
	rig.quiet(t, 1)
	if u := rig.info(sid).GetUsageLimit(); u.GetState() != agentv1.UsageLimit_STATE_REACHED || u.GetProviderId() != "a" {
		t.Fatalf("after a model of the same provider: %v", u)
	}

	// A provider that is fine: the state describes it, and the messages go out in order, one turn.
	rig.setModel(sid, "other-provider")
	u := rig.untilUsage(ch, agentv1.UsageLimit_STATE_CLEARED)
	if u.GetProviderId() != "b" {
		t.Fatalf("cleared: %v", u)
	}
	evs := rig.until(ch, isTurnFinished)
	um := userMessages(evs)
	if len(um) != 1 || !slices.Equal(um[0].ClientUuids, []string{"m2", "m3"}) || userText(um[0]) != "second|third" {
		t.Fatalf("released: %+v", um)
	}
	reqs := rig.conversation()
	if len(reqs) != 2 || reqs[1].Model != "other-provider" {
		t.Fatalf("requests: %d", len(reqs))
	}
	rig.closeSession(sid)
}

func TestModelSwitchToProviderAlsoRefusing(t *testing.T) {
	// The other provider's limits are hidden, so only its refusal tells: the messages stay held, and the state names it.
	rig := startHold(t, func(model string) *llmv1.ProviderLimits {
		if model == "other-provider" {
			return &llmv1.ProviderLimits{ProviderId: "b", ProviderName: "b", Billing: "metered"}
		}
		return window("a", 100, farReset)
	}, nil)
	sid, ch := rig.reach(t)
	rig.sendID(sid, "m2", "second", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	rig.untilQueue(ch, "m2")
	rig.setModel(sid, "other-provider")
	var u *agentv1.UsageLimit
	rig.until(ch, func(ev *agentv1.SessionEvent) bool {
		if x := ev.GetUsageLimit(); x != nil && x.GetProviderId() == "b" {
			u = x
			return true
		}
		return false
	})
	if u.GetState() != agentv1.UsageLimit_STATE_REACHED || u.GetResetsAt() != "" || u.GetWindowLabel() != "" {
		t.Fatalf("other provider refusing: %v", u)
	}
	rig.quiet(t, 1)
	if rig.probes.Load() != 1 {
		t.Fatalf("probes: %d", rig.probes.Load())
	}
	rig.closeSession(sid)
}

func TestHeldReleasedAtResetWithoutAutoContinue(t *testing.T) {
	resets := resetsIn(2 * time.Second)
	rig := startHold(t, func(string) *llmv1.ProviderLimits { return window("a", 100, resets) }, nil)
	sid, ch := rig.reach(t)
	if u := rig.info(sid).GetUsageLimit(); u.GetResetsAt() != resets || u.GetAutoContinue() {
		t.Fatalf("reached: %v", u)
	}
	rig.sendID(sid, "m2", "second", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	rig.untilQueue(ch, "m2")
	rig.refuse.Store(false)
	u := rig.untilUsage(ch, agentv1.UsageLimit_STATE_CLEARED)
	if !u.GetInterrupted() {
		t.Fatalf("cleared without a continuation should still say the turn was cut off: %v", u)
	}
	evs := rig.until(ch, isTurnFinished)
	if um := userMessages(evs); len(um) != 1 || userText(um[0]) != "second" {
		t.Fatalf("released: %+v", um)
	}
	reqs := rig.conversation()
	if len(reqs) != 2 || strings.Contains(lastRequestText(reqs[1]), "cut off the previous turn") {
		t.Fatalf("no reminder without auto-continue: %d %q", len(reqs), lastRequestText(reqs[len(reqs)-1]))
	}
	if u := rig.info(sid).GetUsageLimit(); u.GetInterrupted() {
		t.Fatalf("after the next turn: %v", u)
	}
	rig.closeSession(sid)
}

func TestResetOffDoesNothingButClear(t *testing.T) {
	resets := resetsIn(2 * time.Second)
	rig := startHold(t, func(string) *llmv1.ProviderLimits { return window("a", 100, resets) }, nil)
	sid, ch := rig.reach(t)
	rig.refuse.Store(false)
	rig.untilUsage(ch, agentv1.UsageLimit_STATE_CLEARED)
	rig.quiet(t, 1)
	if rig.probes.Load() != 0 {
		t.Fatalf("probes without anything waiting: %d", rig.probes.Load())
	}
	rig.closeSession(sid)
}

func TestAutoContinueInterrupted(t *testing.T) {
	resets := resetsIn(2 * time.Second)
	rig := startHold(t, func(string) *llmv1.ProviderLimits { return window("a", 100, resets) }, nil)
	sid, ch := rig.reach(t)
	rig.setAuto(sid, true)
	if u := rig.untilUsage(ch, agentv1.UsageLimit_STATE_REACHED); !u.GetAutoContinue() || !u.GetInterrupted() {
		t.Fatalf("after SetAutoContinue: %v", u)
	}
	if !rig.info(sid).GetAutoContinue() {
		t.Fatal("SessionInfo.auto_continue")
	}
	rig.refuse.Store(false)
	u := rig.untilUsage(ch, agentv1.UsageLimit_STATE_CLEARED)
	if u.GetInterrupted() {
		t.Fatalf("continuing: %v", u)
	}
	evs := rig.until(ch, isTurnFinished)
	if um := userMessages(evs); len(um) != 0 {
		t.Fatalf("no user message in a continuation: %+v", um)
	}
	reqs := rig.conversation()
	if len(reqs) != 2 || !strings.Contains(lastRequestText(reqs[1]), "cut off the previous turn") || !strings.Contains(lastRequestText(reqs[1]), "usage limit on a ") {
		t.Fatalf("continue reminder: %d %q", len(reqs), lastRequestText(reqs[len(reqs)-1]))
	}
	rig.closeSession(sid)
}

func TestAutoContinueWithHeldMessages(t *testing.T) {
	resets := resetsIn(2 * time.Second)
	rig := startHold(t, func(string) *llmv1.ProviderLimits { return window("a", 100, resets) }, nil)
	sid, ch := rig.reach(t)
	rig.setAuto(sid, true)
	rig.sendID(sid, "m2", "second", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	rig.untilQueue(ch, "m2")
	rig.refuse.Store(false)
	evs := rig.until(ch, isTurnFinished)
	if um := userMessages(evs); len(um) != 1 || userText(um[0]) != "second" {
		t.Fatalf("held message is the input: %+v", um)
	}
	reqs := rig.conversation()
	if len(reqs) != 2 {
		t.Fatalf("requests: %d", len(reqs))
	}
	if last := lastRequestText(reqs[1]); !strings.Contains(last, "second") || !strings.Contains(last, "cut off the previous turn") {
		t.Fatalf("the reminder rides along: %q", last)
	}
	rig.closeSession(sid)
}

func TestAutoContinueWithoutInterruptedTurn(t *testing.T) {
	// The last turn finished: the other provider is simply out, and its reset has nothing to continue.
	resets := resetsIn(3 * time.Second)
	rig := startHold(t, func(model string) *llmv1.ProviderLimits {
		if model == "other-provider" {
			return window("b", 100, resets)
		}
		return window("a", 92, farReset)
	}, nil)
	rig.refuse.Store(false)
	sid, ch := rig.create(&agentv1.CreateSessionRequest{})
	rig.send(sid, "first")
	rig.untilUsage(ch, agentv1.UsageLimit_STATE_NEAR)
	rig.until(ch, isTurnFinished)
	rig.setAuto(sid, true)
	rig.setModel(sid, "other-provider")
	u := rig.untilUsage(ch, agentv1.UsageLimit_STATE_REACHED)
	if u.GetInterrupted() || u.GetProviderId() != "b" || u.GetResetsAt() != resets {
		t.Fatalf("reached by the switch: %v", u)
	}
	rig.untilUsage(ch, agentv1.UsageLimit_STATE_CLEARED)
	rig.quiet(t, 1)
	if rig.probes.Load() != 0 {
		t.Fatalf("probes: %d", rig.probes.Load())
	}
	rig.closeSession(sid)
}

func TestSendNowOnHeldMessage(t *testing.T) {
	rig := startHold(t, func(string) *llmv1.ProviderLimits { return window("a", 100, farReset) }, nil)
	sid, ch := rig.reach(t)
	rig.sendID(sid, "m2", "second", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	rig.untilQueue(ch, "m2")

	// Still refused: held as before, nothing in the conversation.
	rig.sendID(sid, "m2", "second", agentv1.SendUserMessageRequest_DELIVERY_NOW)
	evs := rig.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetNotice().GetKind() == "usage_limit_still_reached" })
	if um := userMessages(evs); len(um) != 0 {
		t.Fatalf("refused send now reached the conversation: %+v", um)
	}
	rig.quiet(t, 1)
	if rig.probes.Load() != 1 {
		t.Fatalf("probes: %d", rig.probes.Load())
	}
	if u := rig.info(sid).GetUsageLimit(); u.GetState() != agentv1.UsageLimit_STATE_REACHED {
		t.Fatalf("state: %v", u)
	}

	// Recovered: the state clears and the message goes, once.
	rig.refuse.Store(false)
	rig.sendID(sid, "m2", "second", agentv1.SendUserMessageRequest_DELIVERY_NOW)
	rig.untilUsage(ch, agentv1.UsageLimit_STATE_CLEARED)
	evs = rig.until(ch, isTurnFinished)
	if um := userMessages(evs); len(um) != 1 || userText(um[0]) != "second" {
		t.Fatalf("sent now: %+v", um)
	}
	reqs := rig.conversation()
	if len(reqs) != 2 || strings.Count(allText(reqs[1]), "second") != 1 {
		t.Fatalf("message duplicated: %d %q", len(reqs), allText(reqs[len(reqs)-1]))
	}
	rig.closeSession(sid)
}

func TestResetStillRefusedStaysHeld(t *testing.T) {
	resets := resetsIn(2 * time.Second)
	rig := startHold(t, func(string) *llmv1.ProviderLimits { return window("a", 100, resets) }, nil, tunedWatch(100*time.Millisecond))
	sid, ch := rig.reach(t)
	rig.sendID(sid, "m2", "second", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	rig.untilQueue(ch, "m2")
	// Past the reset the provider still refuses: REACHED goes on without a reset time.
	var u *agentv1.UsageLimit
	rig.until(ch, func(ev *agentv1.SessionEvent) bool {
		if x := ev.GetUsageLimit(); x != nil && x.GetState() == agentv1.UsageLimit_STATE_REACHED && x.GetResetsAt() == "" {
			u = x
			return true
		}
		return false
	})
	if !u.GetInterrupted() || len(rig.conversation()) != 1 {
		t.Fatalf("still refused: %v, %d requests", u, len(rig.conversation()))
	}
	rig.refuse.Store(false)
	evs := rig.until(ch, isTurnFinished)
	if um := userMessages(evs); len(um) != 1 || userText(um[0]) != "second" {
		t.Fatalf("released later: %+v", um)
	}
	rig.closeSession(sid)
}

func TestBalanceRecoveredClears(t *testing.T) {
	balance := func(left float64) func(string) *llmv1.ProviderLimits {
		return func(string) *llmv1.ProviderLimits {
			return &llmv1.ProviderLimits{ProviderId: "a", ProviderName: "a", Billing: "metered", Balance: &llmv1.ProviderBalance{Currency: "USD", Balance: &left}}
		}
	}
	rig := startHold(t, balance(0), nil)
	sid, ch := rig.reach(t)
	if u := rig.info(sid).GetUsageLimit(); u.GetResetsAt() != "" || u.GetBalanceText() != "$0.00 left" {
		t.Fatalf("out of credit: %v", u)
	}
	rig.limits.set(balance(20))
	rig.untilUsage(ch, agentv1.UsageLimit_STATE_CLEARED)
	rig.quiet(t, 1)
	if rig.probes.Load() != 0 {
		t.Fatalf("no model requests for a re-check: %d", rig.probes.Load())
	}
	rig.closeSession(sid)
}

func TestResumeRearmsWatch(t *testing.T) {
	resets := resetsIn(3 * time.Second)
	rig := startHold(t, func(string) *llmv1.ProviderLimits { return window("a", 100, resets) }, nil)
	sid, ch := rig.reach(t)
	rig.setAuto(sid, true)
	rig.sendID(sid, "m2", "second", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	rig.untilQueue(ch, "m2")
	rig.closeSession(sid)

	cs, err := rig.client.CreateSession(rig.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: rig.proj, ResumeSessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if u := cs.Msg.GetSession().GetUsageLimit(); u.GetState() != agentv1.UsageLimit_STATE_REACHED || !u.GetInterrupted() || !u.GetAutoContinue() || u.GetResetsAt() != resets {
		t.Fatalf("resumed state: %v", u)
	}
	if !cs.Msg.GetSession().GetAutoContinue() {
		t.Fatal("auto_continue did not survive resume")
	}
	ch = rig.subscribe(sid)
	rig.untilQueue(ch, "m2")
	rig.refuse.Store(false)
	rig.untilUsage(ch, agentv1.UsageLimit_STATE_CLEARED)
	evs := rig.until(ch, isTurnFinished)
	if um := userMessages(evs); len(um) != 1 || !slices.Equal(um[0].ClientUuids, []string{"m2"}) {
		t.Fatalf("released after resume: %+v", um)
	}
	reqs := rig.conversation()
	if last := lastRequestText(reqs[len(reqs)-1]); !strings.Contains(last, "cut off the previous turn") {
		t.Fatalf("continue reminder after resume: %q", last)
	}
	rig.closeSession(sid)

	// Nothing is held any more on the next resume.
	cs, err = rig.client.CreateSession(rig.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: rig.proj, ResumeSessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if u := cs.Msg.GetSession().GetUsageLimit(); u.GetInterrupted() {
		t.Fatalf("after the continuation: %v", u)
	}
	rig.quiet(t, len(reqs))
	rig.closeSession(sid)
}

func TestResumeAfterResetPassed(t *testing.T) {
	resets := resetsIn(time.Second)
	rig := startHold(t, func(string) *llmv1.ProviderLimits { return window("a", 100, resets) }, nil)
	rig.refuse.Store(true)
	sid, ch := rig.reach(t)
	rig.sendID(sid, "m2", "second", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	rig.untilQueue(ch, "m2")
	rig.closeSession(sid)
	at, _ := time.Parse(time.RFC3339, resets)
	time.Sleep(time.Until(at) + 50*time.Millisecond)
	rig.refuse.Store(false)

	if _, err := rig.client.CreateSession(rig.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: rig.proj, ResumeSessionId: sid})); err != nil {
		t.Fatal(err)
	}
	ch = rig.subscribe(sid)
	// The replay has the old REACHED first: the state after it is what counts.
	rig.untilUsage(ch, agentv1.UsageLimit_STATE_CLEARED)
	evs := rig.until(ch, isTurnFinished)
	if um := userMessages(evs); len(um) != 1 || userText(um[0]) != "second" {
		t.Fatalf("released on resume: %+v", um)
	}
	rig.closeSession(sid)
}

func TestForkKeepsNoHeldMessages(t *testing.T) {
	rig := startHold(t, func(string) *llmv1.ProviderLimits { return window("a", 100, farReset) }, nil)
	sid, ch := rig.reach(t)
	rig.setAuto(sid, true)
	rig.sendID(sid, "m2", "second", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	rig.untilQueue(ch, "m2")
	cs, err := rig.client.CreateSession(rig.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: rig.proj, ResumeSessionId: sid, Fork: true}))
	if err != nil {
		t.Fatal(err)
	}
	if s := cs.Msg.GetSession(); s.GetAutoContinue() || s.GetUsageLimit().GetInterrupted() {
		t.Fatalf("fork: %v", s)
	}
	rig.closeSession(cs.Msg.GetSession().GetId())
	rig.closeSession(sid)
}

func TestHiddenLimitsProbeSlowlyOnlyWhenSomethingWaits(t *testing.T) {
	hidden := func(string) *llmv1.ProviderLimits {
		return &llmv1.ProviderLimits{ProviderId: "team", ProviderName: "Team", Billing: "metered"}
	}
	rig := startHold(t, hidden, nil, tunedWatch(300*time.Millisecond))
	sid, ch := rig.reach(t)
	u := rig.info(sid).GetUsageLimit()
	if u.GetState() != agentv1.UsageLimit_STATE_REACHED || u.GetResetsAt() != "" || u.GetWindowLabel() != "" || u.GetUsedPercent() != 0 || u.GetBalanceText() != "" {
		t.Fatalf("hidden limits leaked numbers: %v", u)
	}
	// Nothing waits: no real attempts, however long.
	time.Sleep(450 * time.Millisecond)
	if n := rig.probes.Load(); n != 0 {
		t.Fatalf("probes with nothing waiting: %d", n)
	}
	// A held message: attempts at the slow pace only.
	rig.sendID(sid, "m2", "second", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	rig.untilQueue(ch, "m2")
	start := time.Now()
	time.Sleep(700 * time.Millisecond)
	n := rig.probes.Load()
	if limit := int32(time.Since(start)/(300*time.Millisecond)) + 1; n < 1 || n > limit {
		t.Fatalf("%d probes, at most %d expected", n, limit)
	}
	if len(rig.conversation()) != 1 {
		t.Fatalf("a probe entered the conversation: %d", len(rig.conversation()))
	}
	rig.refuse.Store(false)
	u = rig.untilUsage(ch, agentv1.UsageLimit_STATE_CLEARED)
	if u.GetProviderName() != "Team" {
		t.Fatalf("cleared: %v", u)
	}
	evs := rig.until(ch, isTurnFinished)
	if um := userMessages(evs); len(um) != 1 || userText(um[0]) != "second" {
		t.Fatalf("released: %+v", um)
	}
	rig.closeSession(sid)
}

func TestUsageWatchStopsOnClose(t *testing.T) {
	hidden := func(string) *llmv1.ProviderLimits {
		return &llmv1.ProviderLimits{ProviderId: "team", ProviderName: "Team", Billing: "metered"}
	}
	rig := startHold(t, hidden, nil, tunedWatch(10*time.Millisecond))
	base := runtime.NumGoroutine()
	var ids []string
	for range 4 {
		sid, ch := rig.reach(t)
		rig.sendID(sid, "m", "held", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
		rig.untilQueue(ch, "m")
		ids = append(ids, sid)
	}
	time.Sleep(100 * time.Millisecond)
	if rig.probes.Load() == 0 {
		t.Fatal("the watch never ran")
	}
	for _, sid := range ids {
		rig.closeSession(sid)
	}
	n := rig.probes.Load()
	time.Sleep(150 * time.Millisecond)
	if got := rig.probes.Load(); got != n {
		t.Fatalf("probes after close: %d -> %d", n, got)
	}
	// Connections linger a while; the sessions' own work is gone.
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > base+20 {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines: %d, started from %d", runtime.NumGoroutine(), base)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
