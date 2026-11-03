package server

import (
	"context"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/xlsft/harness/packages/client/agent"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/usagelimit"
)

// When the provider of a session's model is out of usage (REACHED), a user message does not start a turn: the driver
// holds it in the session's queue (UserMessage.held). It goes out when
//   - the session moves to a model of another provider that is fine (SetModel: no other action needed);
//   - the limit resets: a timer at resets_at, or, without one, a slow re-check of the provider's limits;
//   - the user asks ("Send now").
//
// Held messages go out only after ProbeModel (a tiny request outside the conversation) got through, so that a provider
// still refusing leaves them held rather than read and unanswered. Without readable limits (hidden by the server's
// policies, or no server) the probe is all there is, and it runs at most once per ProbeEvery, only when something
// waits for it (held messages, or a cut-off turn with auto-continue on).
//
// Each session in that state has one timer, stopped when the state ends or the session closes; the evaluation it
// starts runs as background work of the session (ls.bg).

// UsageWatchTimings tune the watch over a session in REACHED (zero: defaults).
type UsageWatchTimings struct {
	// ResetMargin is waited past resets_at (default 2 s).
	ResetMargin time.Duration
	// RecheckMin and RecheckMax bound the backoff of the limits re-check without a resets_at (default 1 to 5 min).
	RecheckMin, RecheckMax time.Duration
	// ProbeEvery is the least time between automatic probes (default 10 min), unless a known reset time has passed.
	ProbeEvery time.Duration
}

func (t UsageWatchTimings) withDefaults() UsageWatchTimings {
	if t.ResetMargin <= 0 {
		t.ResetMargin = 2 * time.Second
	}
	if t.RecheckMin <= 0 {
		t.RecheckMin = time.Minute
	}
	if t.RecheckMax <= 0 {
		t.RecheckMax = 5 * time.Minute
	}
	if t.ProbeEvery <= 0 {
		t.ProbeEvery = 10 * time.Minute
	}
	return t
}

// usageBusyRetry is how soon an evaluation is tried again when a turn is running.
const usageBusyRetry = time.Second

// usageWatch is the usage-limit watch of a live session.
type usageWatch struct {
	monitor *usagelimit.Monitor
	// run orders the evaluations (timer, Send now, model switch); it is held over fetches and probes.
	run sync.Mutex

	mu    sync.Mutex
	timer *time.Timer
	// backoff is the last re-check delay; lastTry when the provider last got a request we know the fate of.
	backoff time.Duration
	lastTry time.Time
}

func (w *usageWatch) noteTry() {
	w.mu.Lock()
	w.lastTry = time.Now()
	w.mu.Unlock()
}

// sinceTry is how long ago the last request went out (long ago when none did).
func (w *usageWatch) sinceTry() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lastTry.IsZero() {
		return 1<<63 - 1
	}
	return time.Since(w.lastTry)
}

// nextBackoff doubles the re-check delay up to its limit.
func (w *usageWatch) nextBackoff(t UsageWatchTimings) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.backoff *= 2
	if w.backoff < t.RecheckMin {
		w.backoff = t.RecheckMin
	}
	if w.backoff > t.RecheckMax {
		w.backoff = t.RecheckMax
	}
	return w.backoff
}

func (w *usageWatch) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.backoff = 0
}

// goBackground runs f as background work of the session, unless it is closing.
func (ls *liveSession) goBackground(f func()) {
	ls.mu.Lock()
	if ls.closed {
		ls.mu.Unlock()
		return
	}
	ls.bg.Add(1)
	ls.mu.Unlock()
	go func() {
		defer ls.bg.Done()
		f()
	}()
}

// scheduleUsageTick replaces the session's timer.
func (ls *liveSession) scheduleUsageTick(d time.Duration) {
	w := &ls.usage
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(d, func() { ls.goBackground(ls.usageTick) })
}

// armUsageWatch sets the timer for a REACHED state: at its reset time, or the next re-check without one.
func (ls *liveSession) armUsageWatch(s usagelimit.Status) {
	t := ls.srv.UsageWatch.withDefaults()
	if at, ok := resetTime(s); ok {
		ls.scheduleUsageTick(max(time.Until(at)+t.ResetMargin, 0))
		return
	}
	ls.scheduleUsageTick(ls.usage.nextBackoff(t))
}

func resetTime(s usagelimit.Status) (time.Time, bool) {
	at, err := time.Parse(time.RFC3339, s.ResetsAt)
	return at, err == nil
}

// pokeUsageWatch brings the next look forward when something new waits for it and no reset time is known: a limit
// that lifted while nothing needed it should not keep a new message waiting for the rest of the backoff.
func (ls *liveSession) pokeUsageWatch() {
	cur := ls.usage.monitor.Current()
	if cur.State != usagelimit.Reached || cur.ResetsAt != "" {
		return
	}
	t := ls.srv.UsageWatch.withDefaults()
	ls.scheduleUsageTick(min(max(t.ProbeEvery-ls.usage.sinceTry(), 0), t.RecheckMin))
}

// wantsTurn reports whether something waits for the limit: held messages, or a cut-off turn to continue.
func (ls *liveSession) wantsTurn(cur usagelimit.Status) bool {
	return ls.sess.Agent.QueueLen() > 0 || (ls.autoContinueOn() && cur.Interrupted)
}

func (ls *liveSession) autoContinueOn() bool {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.autoContinue
}

// probeResult is what a probe learned.
type probeResult int

const (
	probeOK      probeResult = iota
	probeRefused             // the provider refused for limits
	probeUnknown             // some other failure: it says nothing about the limit
)

func (ls *liveSession) probe() probeResult {
	ls.usage.noteTry()
	err := ls.sess.Agent.ProbeModel(ls.ctx)
	switch {
	case err == nil:
		return probeOK
	case usagelimit.IsReached(err):
		return probeRefused
	}
	ls.srv.log().Warn("usage limit probe failed", "session", ls.sess.ID, "err", err)
	return probeUnknown
}

// usageTick is the session's timer: the reset time has come, or it is time for a re-check.
func (ls *liveSession) usageTick() {
	w := &ls.usage
	w.run.Lock()
	defer w.run.Unlock()
	cur := w.monitor.Current()
	if ls.ctx.Err() != nil || cur.State != usagelimit.Reached {
		return
	}
	if ls.driver.Busy() {
		ls.scheduleUsageTick(usageBusyRetry)
		return
	}
	at, hasReset := resetTime(cur)
	resetPassed := hasReset && !at.After(time.Now())
	reading := w.monitor.Recheck(ls.ctx, ls.sess.Agent.Model())
	if ls.ctx.Err() != nil {
		return
	}
	t := ls.srv.UsageWatch.withDefaults()
	switch {
	case reading.Known && reading.Status.State == usagelimit.Reached:
		// Still out, maybe on another window or until later.
		next := reading.Status
		next.Interrupted = cur.Interrupted
		if next == cur {
			ls.armUsageWatch(cur)
			return
		}
		ls.publishReached(next)
		return
	case !reading.Known && !resetPassed:
		if hasReset {
			ls.armUsageWatch(cur)
			return
		}
		// Hidden limits: only a real request can tell, and only when something waits for it.
		if !ls.wantsTurn(cur) {
			ls.scheduleUsageTick(w.nextBackoff(t))
			return
		}
	}
	if ls.wantsTurn(cur) {
		if wait := t.ProbeEvery - w.sinceTry(); !resetPassed && wait > 0 {
			ls.scheduleUsageTick(wait)
			return
		}
		switch ls.probe() {
		case probeRefused:
			// The reset time did not help: go on without one.
			next := cur
			next.ResetsAt = ""
			if next == cur {
				ls.scheduleUsageTick(t.ProbeEvery)
				return
			}
			ls.publishReached(next)
			return
		case probeUnknown:
			ls.scheduleUsageTick(t.ProbeEvery)
			return
		}
	}
	ls.limitReturned(returnedState(reading, cur), cur, true)
}

// publishReached publishes REACHED with changed numbers; the state stays as it is, so the monitor would not report it.
func (ls *liveSession) publishReached(s usagelimit.Status) {
	ls.usage.monitor.Set(s)
	ls.sess.Agent.PublishUsage(s)
}

// returnedState is the state after the limit: what the reading says, with the provider of the last state when it
// names none.
func returnedState(r usagelimit.Reading, cur usagelimit.Status) usagelimit.Status {
	st := r.Status
	if st.ProviderID == "" && st.ProviderName == "" {
		st.ProviderID, st.ProviderName = cur.ProviderID, cur.ProviderName
	}
	return st
}

// limitReturned ends REACHED (the banner goes) and lets held messages out, as the next turn. With auto-continue on
// and the last turn cut off, the continue reminder rides on that turn; with no messages it starts the turn itself,
// when maySelfStart allows it.
func (ls *liveSession) limitReturned(st, cur usagelimit.Status, maySelfStart bool) {
	cont := ls.autoContinueOn() && cur.Interrupted && (maySelfStart || ls.sess.Agent.QueueLen() > 0)
	st.Interrupted = cur.Interrupted && !cont
	if ev := ls.usage.monitor.Observe(st); ev != nil {
		ls.sess.Agent.PublishUsage(*ev)
	}
	if cont {
		ls.sess.Agent.Remind(agent.ReminderUsageReturned, usagelimit.ReturnedVars(cur))
	}
	if !ls.driver.Release() && cont {
		ls.driver.Wake()
	}
}

// sendHeldNow is "Send now" on a held message: try at once, whatever the state says. A refusal leaves everything as it
// was (the messages stay held, nothing reached the conversation).
func (ls *liveSession) sendHeldNow() {
	ls.goBackground(func() {
		w := &ls.usage
		w.run.Lock()
		defer w.run.Unlock()
		cur := w.monitor.Current()
		if cur.State != usagelimit.Reached {
			ls.driver.Release()
			return
		}
		if ls.ctx.Err() != nil {
			return
		}
		if ls.probe() == probeRefused {
			ls.notice(agentv1.Notice_LEVEL_WARNING, "usage_limit_still_reached", "The provider is still refusing requests; the message stays held.")
			return
		}
		if ls.ctx.Err() != nil {
			return
		}
		ls.limitReturned(usagelimit.Status{ProviderID: cur.ProviderID, ProviderName: cur.ProviderName}, cur, true)
	})
}

// usageOnModelChange re-evaluates the usage state for the new model's provider when a state is showing: a model of the
// same provider changes nothing; another provider replaces the state (held messages go out if it takes requests).
func (ls *liveSession) usageOnModelChange(prev, model string) {
	if st := ls.usage.monitor.Current().State; prev == model || st == usagelimit.None || st == usagelimit.Cleared {
		return
	}
	ls.goBackground(func() {
		w := &ls.usage
		w.run.Lock()
		defer w.run.Unlock()
		if ls.ctx.Err() != nil || ls.sess.Agent.Model() != model {
			return // another switch follows
		}
		cur := w.monitor.Current()
		reading := w.monitor.Recheck(ls.ctx, model)
		if ls.ctx.Err() != nil || ls.sameProvider(cur, reading, prev, model) {
			return
		}
		st := reading.Status
		if st.ProviderID == "" && st.ProviderName == "" {
			st.ProviderID = ls.catalogProvider(model)
		}
		st.Interrupted = cur.Interrupted
		if st.State == usagelimit.Reached {
			ls.publishReached(st)
			return
		}
		if cur.State != usagelimit.Reached {
			if ev := w.monitor.Observe(st); ev != nil {
				ls.sess.Agent.PublishUsage(*ev)
			}
			return
		}
		if ls.sess.Agent.QueueLen() > 0 && ls.probe() == probeRefused {
			st.State = usagelimit.Reached
			ls.publishReached(st)
			return
		}
		if ls.ctx.Err() != nil {
			return
		}
		// Auto-continue is about the limit returning: a switch alone starts no turn, only held messages do.
		ls.limitReturned(st, cur, false)
	})
}

// sameProvider tells whether the new model runs on the provider of the state: by the limits when both sides name a
// provider, else by the catalog. Unknown counts as another provider (a probe decides for held messages).
func (ls *liveSession) sameProvider(cur usagelimit.Status, r usagelimit.Reading, prev, model string) bool {
	if cur.ProviderID != "" && r.Status.ProviderID != "" {
		return cur.ProviderID == r.Status.ProviderID
	}
	a, b := ls.catalogProvider(prev), ls.catalogProvider(model)
	return a != "" && a == b
}

func (ls *liveSession) catalogProvider(model string) string {
	if ls.sess.Catalog == nil {
		return ""
	}
	m, ok := ls.sess.Catalog.Find(model)
	if !ok {
		return ""
	}
	return m.Provider
}

// applyHeld marks the queue as held or not and publishes it when that changed.
func (ls *liveSession) applyHeld(held bool) {
	ls.mu.Lock()
	changed := false
	for i, m := range ls.queue {
		if m.GetHeld() != held {
			changed = true
			ls.queue[i] = &agentv1.UserMessage{Uuid: m.GetUuid(), Content: m.GetContent(), QueuedAt: m.GetQueuedAt(), Held: held}
		}
	}
	q := append([]*agentv1.UserMessage(nil), ls.queue...)
	ls.mu.Unlock()
	if changed {
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_QueueChanged{QueueChanged: &agentv1.QueueChanged{Queued: q}}})
		ls.saveHeld()
	}
}

// saveHeld records the held messages in the transcript, so they survive a restart of the client.
func (ls *liveSession) saveHeld() {
	ls.mu.Lock()
	held := []session.HeldMessage{}
	for _, m := range ls.queue {
		if m.GetHeld() {
			held = append(held, session.HeldMessage{ID: m.GetUuid(), Content: pbconv.FromBlocks(m.GetContent()), QueuedAt: m.GetQueuedAt()})
		}
	}
	changed := len(held) > 0 || ls.heldOnRecord
	ls.heldOnRecord = len(held) > 0
	ls.mu.Unlock()
	if !changed {
		return
	}
	if _, err := ls.sess.Transcript.Append(session.Entry{Type: session.TypeMeta, HeldMessages: &held}); err != nil {
		ls.srv.log().Warn("held messages not recorded", "session", ls.sess.ID, "err", err)
	}
}

// restoreHeld puts the held messages on record back in the queue (a resumed session; before it goes live).
func (ls *liveSession) restoreHeld(entries []session.Entry) {
	held := session.ReadHeldMessages(entries)
	for _, h := range held {
		ls.sess.Agent.Enqueue(h.ID, h.Content)
		ls.seen[h.ID] = true
		ls.queue = append(ls.queue, &agentv1.UserMessage{Uuid: h.ID, Content: pbconv.Blocks(h.Content), QueuedAt: h.QueuedAt, Held: true})
	}
	ls.heldOnRecord = len(held) > 0
}

// startUsageWatch picks the watch up in a resumed session: a REACHED on record re-arms from its reset time (one that
// has passed fires at once). Messages on record without REACHED have nothing left to wait for.
func (ls *liveSession) startUsageWatch() {
	ls.mu.Lock()
	q := append([]*agentv1.UserMessage(nil), ls.queue...)
	ls.mu.Unlock()
	if len(q) > 0 {
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_QueueChanged{QueueChanged: &agentv1.QueueChanged{Queued: q}}})
	}
	cur := ls.usage.monitor.Current()
	switch {
	case cur.State == usagelimit.Reached:
		ls.armUsageWatch(cur)
	case ls.sess.Agent.QueueLen() > 0:
		ls.goBackground(func() { ls.driver.Release() })
	}
}

// SetAutoContinue turns "continue when limits return" on or off for the session.
func (s *Server) SetAutoContinue(ctx context.Context, req *connect.Request[agentv1.SetAutoContinueRequest]) (*connect.Response[agentv1.SetAutoContinueResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	on := req.Msg.GetEnabled()
	ls.mu.Lock()
	changed := ls.autoContinue != on
	ls.autoContinue = on
	var u *agentv1.UsageLimit
	if ls.usageLimit != nil && changed {
		u = proto.Clone(ls.usageLimit).(*agentv1.UsageLimit)
		u.AutoContinue = on
		ls.usageLimit = u
	}
	ls.mu.Unlock()
	if !changed {
		return connect.NewResponse(&agentv1.SetAutoContinueResponse{}), nil
	}
	if _, err := ls.sess.Transcript.Append(session.Entry{Type: session.TypeMeta, AutoContinue: &on}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if u != nil {
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_UsageLimit{UsageLimit: u}})
	}
	ls.publishInfo()
	if on {
		ls.pokeUsageWatch()
	}
	return connect.NewResponse(&agentv1.SetAutoContinueResponse{}), nil
}

// newUsageMonitor creates the usage-limit monitor of a harness session: the limits of the session model's
// provider come from the server's ProviderLimitsService with the user's credentials, so the server's
// visibility policies apply (hidden blocks never reach the monitor). Without a harness server only REACHED
// (from the provider's errors) is reported.
func (s *Server) newUsageMonitor() *usagelimit.Monitor {
	return &usagelimit.Monitor{Fetch: func(ctx context.Context, model string) (*usagelimit.Snapshot, error) {
		res, err := s.providerLimits(ctx, nil, model)
		if err != nil {
			return nil, err
		}
		return pbconv.LimitsSnapshot(res.GetModel()), nil
	}}
}

// lastUsageLimit is the last usage-limit state recorded in a transcript (nil when none). A turn answered since then
// means the cut-off turn is behind us.
func lastUsageLimit(entries []session.Entry) *agentv1.UsageLimit {
	answered := false
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Type == session.TypeAssistant {
			answered = true
		}
		if e.Type == session.TypeSystem && e.Subtype == session.SubtypeUsageLimit && e.UsageLimit != nil {
			u := pbconv.UsageLimit(*e.UsageLimit)
			if u != nil && answered {
				u.Interrupted = false
			}
			return u
		}
	}
	return nil
}

// onUsageLimit publishes a usage-limit state and keeps it for SessionInfo.usage_limit. REACHED holds the messages and
// starts the watch; any other state ends both.
func (ls *liveSession) onUsageLimit(s usagelimit.Status) {
	u := pbconv.UsageLimit(s)
	if u == nil {
		return
	}
	reached := s.State == usagelimit.Reached
	if ls.driver != nil {
		ls.driver.SetHeld(reached)
	}
	if reached && s.Interrupted {
		ls.usage.noteTry() // a turn was just refused
	}
	ls.mu.Lock()
	u.AutoContinue = ls.autoContinue
	ls.usageLimit = u
	ls.mu.Unlock()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_UsageLimit{UsageLimit: u}})
	ls.publishInfo()
	if ls.driver == nil {
		return
	}
	ls.applyHeld(reached)
	if reached {
		ls.armUsageWatch(s)
	} else {
		ls.usage.stop()
	}
}
