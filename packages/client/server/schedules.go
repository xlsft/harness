package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/localmodels"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/schedule"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/tools"
)

// Scheduled tasks on the agent service: starts what package schedule says is due, in a session; follows it until it
// settles; moves schedules, history and run requests through the sync feed.

const (
	// scheduleSummaryChars is how much of a final answer the history keeps.
	scheduleSummaryChars = 400
	// wakeupsPerSession bounds what the model can stack up; a repeating wake-up runs at most wakeupMaxRuns times and for
	// at most sessionScheduleLife.
	wakeupsPerSession   = 10
	wakeupMaxRuns       = 200
	wakeupDefaultRuns   = 48
	sessionScheduleLife = 7 * 24 * time.Hour
	// minWakeupDelay: a one-off comes back no sooner, or it is a busy loop on someone's bill.
	minWakeupDelay = time.Minute
	// schedulePrefTTL is how often the master switch is read from the server.
	schedulePrefTTL = time.Minute
	// scheduleOpenTimeout bounds opening a session for a run; schedulePushBackoff waits after the server was out of reach.
	scheduleOpenTimeout = 2 * time.Minute
	schedulePushBackoff = 30 * time.Second
	maxTriggerPayload   = 64 << 10
)

// scheduleHost is the part of the server that runs this machine's schedules.
type scheduleHost struct {
	s      *Server
	store  *schedule.Store
	runner *schedule.Runner

	mu     sync.Mutex
	active map[string]*activeRun
	// staleSettled: the runs a client before this one left behind are settled.
	staleSettled bool
	pushAfter    time.Time
	syncDirty    atomic.Bool

	prefMu    sync.Mutex
	prefAt    time.Time
	prefValue bool

	stop func()
	done chan struct{}
}

// activeRun is a run this client follows.
type activeRun struct {
	fire      schedule.Fire
	sessionID string
	ls        *liveSession
	// seq is the session's turn counter at the start; noTurn: what was sent started none (a slash command).
	seq    int64
	noTurn bool
	// closeAfter: this run opened the session, so it closes it again if nobody looks.
	closeAfter bool
}

func (s *Server) now() time.Time {
	if s.ScheduleClock != nil {
		return s.ScheduleClock()
	}
	return time.Now()
}

// scheduler returns the host, created once. It runs nothing until serveHandler starts it.
func (s *Server) scheduler() *scheduleHost {
	s.schedOnce.Do(func() {
		host, _ := os.Hostname()
		h := &scheduleHost{s: s, store: schedule.Open(filepath.Join(config.Home(), "schedules")), active: map[string]*activeRun{}}
		h.runner = &schedule.Runner{Store: h.store, DeviceID: s.DeviceID, DeviceName: host, Start: h.start, Enabled: h.enabled, Every: s.ScheduleEvery, FilePoll: s.ScheduleFilePoll, Logger: s.log(), Clock: s.now, OnTick: h.onTick}
		s.sched = h
	})
	return s.sched
}

// startScheduler runs the host until the server stops.
func (s *Server) startScheduler() {
	h := s.scheduler()
	ctx, cancel := context.WithCancel(s.ctx)
	h.stop = cancel
	h.done = make(chan struct{})
	if s.ScheduleSync != nil {
		h.syncDirty.Store(true)
		s.ScheduleSync.Cache().Subscribe(func(syncclient.Event) {
			h.syncDirty.Store(true)
			h.runner.Nudge()
		})
	}
	go func() {
		defer close(h.done)
		h.runner.Run(ctx)
	}()
}

// StopScheduler ends the host's loop and waits for it.
func (s *Server) StopScheduler() {
	if h := s.sched; h != nil && h.stop != nil {
		h.stop()
		<-h.done
	}
}

func (h *scheduleHost) enabled() bool {
	s := h.s
	if s.Preferences == nil {
		return true
	}
	h.prefMu.Lock()
	defer h.prefMu.Unlock()
	if !h.prefAt.IsZero() && time.Since(h.prefAt) < schedulePrefTTL {
		return h.prefValue
	}
	ctx, cancel := context.WithTimeout(s.ctx, sessionSyncTimeout)
	defer cancel()
	doc, err := s.userSettings(ctx)
	if err != nil {
		if h.prefAt.IsZero() {
			return true
		}
		return h.prefValue // the server is out of reach: the last answer stands
	}
	h.prefValue, h.prefAt = preferencesOf(doc).GetSchedulesEnabled(), time.Now()
	return h.prefValue
}

func (h *scheduleHost) forgetPreference() {
	h.prefMu.Lock()
	h.prefAt = time.Time{}
	h.prefMu.Unlock()
}

func (h *scheduleHost) onTick(now time.Time) {
	if !h.staleSettled && h.runner.Holding() {
		h.staleSettled = true
		h.mu.Lock()
		mine := make(map[string]bool, len(h.active))
		for id := range h.active {
			mine[id] = true
		}
		h.mu.Unlock()
		if err := h.store.InterruptStale(func(r schedule.Run) bool { return mine[r.ID] }, now); err != nil {
			h.s.log().Warn("schedules: settling old runs failed", "err", err)
		}
	}
	h.sweep(now)
	h.syncDocs(now)
}

// ---- runs ----

func (h *scheduleHost) start(f schedule.Fire) {
	go h.execute(f)
}

func (h *scheduleHost) fail(f schedule.Fire, why string) {
	if err := h.store.FinishRun(f.Task.ID, f.RunID, schedule.StatusFailed, "", why, h.s.now()); err != nil {
		h.s.log().Warn("schedules: recording a failed run failed", "schedule", f.Task.ID, "err", err)
	}
	h.runner.Nudge()
}

func (h *scheduleHost) track(run *activeRun) {
	h.mu.Lock()
	h.active[run.fire.RunID] = run
	h.mu.Unlock()
	_ = h.store.SetRunSession(run.fire.Task.ID, run.fire.RunID, run.sessionID)
}

func modeOfTask(mode string) (agentv1.PermissionMode, error) {
	if mode == "" {
		return agentv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED, nil
	}
	m, err := permissions.ParseMode(mode)
	if err != nil {
		return 0, err
	}
	return modeToProto(m), nil
}

func (h *scheduleHost) execute(f schedule.Fire) {
	ctx, cancel := context.WithTimeout(h.s.ctx, scheduleOpenTimeout)
	defer cancel()
	if f.Task.SessionID != "" {
		h.continueSession(ctx, f)
		return
	}
	s := h.s
	if f.Task.Cwd != "" {
		if _, err := os.Stat(f.Task.Cwd); err != nil {
			h.fail(f, "The folder isn't there: "+f.Task.Cwd)
			return
		}
	}
	mode, err := modeOfTask(f.Task.Mode)
	if err != nil {
		h.fail(f, err.Error())
		return
	}
	req := &agentv1.CreateSessionRequest{Cwd: f.Task.Cwd, Model: f.Task.Model, PermissionMode: mode, Effort: effortOfTask(f.Task.Effort), Engine: f.Task.Engine}
	if f.Task.Worktree {
		req.Worktree = &agentv1.WorktreeOptions{}
	}
	router := acpRouted{routed{s}}
	res, err := router.CreateSession(ctx, connect.NewRequest(req))
	if err != nil {
		h.fail(f, connectMessage(err))
		return
	}
	id := res.Msg.GetSession().GetId()
	title := "Scheduled: " + scheduleLabel(f.Task)
	if _, err := router.UpdateSession(ctx, connect.NewRequest(&agentv1.UpdateSessionRequest{SessionId: id, Title: &title})); err != nil {
		s.log().Debug("schedules: naming the session failed", "err", err)
	}
	message := &agentv1.SendUserMessageRequest{SessionId: id, Uuid: uuid.NewString(), Content: pbconv.Blocks(textBlocks(f.Task.Prompt))}
	ls := s.liveSession(id)
	if ls == nil {
		// An ACP agent: it has its own loop and says nothing about when it is done.
		if _, err := router.SendUserMessage(ctx, connect.NewRequest(message)); err != nil {
			h.fail(f, connectMessage(err))
			return
		}
		_ = h.store.SetRunSession(f.Task.ID, f.RunID, id)
		_ = h.store.FinishRun(f.Task.ID, f.RunID, schedule.StatusStarted, "", "", s.now())
		return
	}
	run := &activeRun{fire: f, sessionID: id, ls: ls, seq: ls.turnSeq.Load(), closeAfter: true}
	h.track(run)
	remindTrigger(ls, f)
	if _, err := router.SendUserMessage(ctx, connect.NewRequest(message)); err != nil {
		h.settle(run, schedule.StatusFailed, "", connectMessage(err))
		return
	}
	h.noteSent(run)
}

// noteSent works out whether what was sent started a turn: the turn started before Send returned, or it never will.
func (h *scheduleHost) noteSent(run *activeRun) {
	if !run.ls.driver.Busy() && run.ls.turnSeq.Load() == run.seq {
		h.mu.Lock()
		run.noTurn = true
		h.mu.Unlock()
	}
	h.runner.Nudge()
}

// continueSession is a `/loop` round or a wake-up. A loop round is the user's words, as a message; a wake-up is the
// model's own note, as a reminder.
func (h *scheduleHost) continueSession(ctx context.Context, f schedule.Fire) {
	s := h.s
	opened := s.liveSession(f.Task.SessionID) == nil
	ls, err := s.openForNote(ctx, f.Task.SessionID)
	if err != nil {
		h.fail(f, err.Error())
		return
	}
	run := &activeRun{fire: f, sessionID: f.Task.SessionID, ls: ls, seq: ls.turnSeq.Load(), closeAfter: opened}
	if f.Task.Origin == schedule.OriginWakeup {
		h.track(run)
		repeat := "one time"
		if f.Task.Trigger.Kind != schedule.KindOnce {
			repeat = "repeating"
		}
		ls.sess.Agent.Remind(agent.ReminderScheduledWakeup, map[string]string{"id": f.Task.ID, "repeat": repeat, "note": escapeTrigger(f.Task.Prompt)})
		busy := ls.driver.Busy()
		ls.driver.Wake()
		if !busy && !ls.driver.Busy() && !ls.driver.Held() {
			h.mu.Lock()
			run.noTurn = true
			h.mu.Unlock()
		}
		h.runner.Nudge()
		return
	}
	if ls.driver.Busy() {
		if err := h.store.FinishRun(f.Task.ID, f.RunID, schedule.StatusSkipped, "", "the session was working", s.now()); err != nil {
			s.log().Warn("schedules: recording a skipped run failed", "err", err)
		}
		return
	}
	h.track(run)
	message := &agentv1.SendUserMessageRequest{SessionId: f.Task.SessionID, Uuid: uuid.NewString(), Content: pbconv.Blocks(textBlocks(f.Task.Prompt))}
	if _, err := s.SendUserMessage(ctx, connect.NewRequest(message)); err != nil {
		h.settle(run, schedule.StatusFailed, "", connectMessage(err))
		return
	}
	h.noteSent(run)
}

func textBlocks(text string) []llm.ContentBlock { return []llm.ContentBlock{llm.TextBlock(text)} }

func scheduleLabel(t schedule.Task) string {
	if strings.TrimSpace(t.Name) != "" {
		return t.Name
	}
	return truncateRunes(strings.Join(strings.Fields(t.Prompt), " "), 60)
}

func effortOfTask(e string) llmv1.Effort { return pbconv.Effort(e) }

var triggerTags = strings.NewReplacer(
	"<system-reminder", "&lt;system-reminder", "</system-reminder", "&lt;/system-reminder",
	"<schedule-trigger-data", "&lt;schedule-trigger-data", "</schedule-trigger-data", "&lt;/schedule-trigger-data",
	"<wakeup-note", "&lt;wakeup-note", "</wakeup-note", "&lt;/wakeup-note",
)

func escapeTrigger(s string) string { return triggerTags.Replace(s) }

// remindTrigger tells the run what started it. What a webhook posted or a file list says is not the user's words, so it
// goes as a reminder and never as the message.
func remindTrigger(ls *liveSession, f schedule.Fire) {
	source := ""
	switch f.Source {
	case schedule.SourceFile:
		source = "a change in the watched files"
	case schedule.SourceWebhook:
		source = "a webhook"
	default:
		return
	}
	detail := f.Payload
	if len(detail) > maxTriggerPayload {
		detail = strings.ToValidUTF8(detail[:maxTriggerPayload], "") + "\n[cut]"
	}
	if strings.TrimSpace(detail) == "" {
		detail = "(nothing was sent)"
	}
	ls.sess.Agent.Remind(agent.ReminderScheduleTrigger, map[string]string{"source": source, "detail": escapeTrigger(detail)})
}

// settled reports whether a session is done with the run: nothing works in it and nothing will by itself.
func (ls *liveSession) settled(seq int64, noTurn bool) bool {
	if ls.driver.Busy() || ls.driver.Held() || ls.sess.Subagents.Active() > 0 || ls.goalWorking() {
		return false
	}
	return noTurn || ls.turnSeq.Load() > seq
}

// sweep settles the runs whose session is done.
func (h *scheduleHost) sweep(now time.Time) {
	h.mu.Lock()
	runs := make([]*activeRun, 0, len(h.active))
	for _, r := range h.active {
		runs = append(runs, r)
	}
	h.mu.Unlock()
	for _, run := range runs {
		live := h.s.liveSession(run.sessionID)
		if live == nil || live != run.ls {
			h.settle(run, schedule.StatusInterrupted, "", "the session closed while it ran")
			continue
		}
		h.mu.Lock()
		noTurn := run.noTurn
		h.mu.Unlock()
		if !live.settled(run.seq, noTurn) {
			continue
		}
		if noTurn {
			h.settle(run, schedule.StatusSucceeded, "", "")
			continue
		}
		res := live.lastTurn()
		switch {
		case res.Interrupted:
			h.settle(run, schedule.StatusInterrupted, truncateRunes(res.Result, scheduleSummaryChars), "the turn was interrupted")
		case res.Subtype != agent.SubtypeSuccess:
			h.settle(run, schedule.StatusFailed, truncateRunes(res.Result, scheduleSummaryChars), truncateRunes(strings.Join(res.Errors, "; "), scheduleSummaryChars))
		default:
			h.settle(run, schedule.StatusSucceeded, truncateRunes(res.Result, scheduleSummaryChars), "")
		}
	}
}

func (h *scheduleHost) settle(run *activeRun, status schedule.Status, summary, why string) {
	h.mu.Lock()
	_, ours := h.active[run.fire.RunID]
	delete(h.active, run.fire.RunID)
	h.mu.Unlock()
	if !ours {
		return
	}
	if err := h.store.FinishRun(run.fire.Task.ID, run.fire.RunID, status, summary, why, h.s.now()); err != nil {
		h.s.log().Warn("schedules: recording a run failed", "schedule", run.fire.Task.ID, "err", err)
	}
	// A session nobody looks at doesn't stay open: its transcript is what the history links to.
	if run.closeAfter && status != schedule.StatusInterrupted && !run.ls.hub.watched() {
		h.s.closeLive(run.sessionID, "schedule_done")
	}
}

// settleSession is told a session's turn ended: the sweep looks now rather than at the next tick.
func (s *Server) nudgeScheduler() {
	if h := s.sched; h != nil {
		h.runner.Nudge()
	}
}

// forgetSession drops what is tied to a session that was deleted.
func (s *Server) forgetSessionSchedules(id string) {
	h := s.scheduler()
	if err := h.store.Update(func(st *schedule.State) error {
		for key, rec := range st.Records {
			if rec.Task.SessionID == id {
				delete(st.Records, key)
			}
		}
		return nil
	}); err != nil {
		s.log().Warn("schedules: forgetting a session failed", "session", id, "err", err)
	}
}

// ---- sync ----

func scheduleRef(kind syncv1.DocumentKind, key string) *syncv1.DocumentRef {
	return &syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_USER, Kind: kind, Key: key}
}

type scheduleHistoryDoc struct {
	Runs []schedule.Run `json:"runs"`
}

// scheduleTriggerDoc is a DOCUMENT_KIND_SCHEDULE_TRIGGER document.
type scheduleTriggerDoc struct {
	ScheduleID string `json:"scheduleId"`
	DeviceID   string `json:"deviceId"`
	Source     string `json:"source"`
	ReceivedAt string `json:"receivedAt"`
	Payload    string `json:"payload"`
}

func (h *scheduleHost) syncDocs(now time.Time) {
	sc := h.s.ScheduleSync
	if sc == nil {
		return
	}
	if h.syncDirty.Swap(false) {
		h.applyFeed(sc.Cache(), now)
	}
	if now.Before(h.pushAfter) {
		return
	}
	ctx, cancel := context.WithTimeout(h.s.ctx, 2*sessionSyncTimeout)
	defer cancel()
	if err := h.push(ctx, sc); err != nil {
		h.s.log().Debug("schedules: the server is out of reach; retrying", "err", err)
		h.pushAfter = now.Add(schedulePushBackoff)
		return
	}
	h.pushAfter = time.Time{}
	h.runTriggers(ctx, sc, now)
}

// applyFeed takes what the feed says into the store.
func (h *scheduleHost) applyFeed(cache *syncclient.Cache, now time.Time) {
	docs := cache.List(syncv1.DocumentKind_DOCUMENT_KIND_SCHEDULE)
	histories := cache.List(syncv1.DocumentKind_DOCUMENT_KIND_SCHEDULE_HISTORY)
	synced := cache.Synced()
	err := h.store.Update(func(st *schedule.State) error {
		seen := make(map[string]bool, len(docs))
		for _, d := range docs {
			var t schedule.Task
			if err := json.Unmarshal([]byte(d.GetContent()), &t); err != nil || t.ID != d.GetKey() {
				h.s.log().Warn("schedules: a document doesn't read as a schedule", "key", d.GetKey(), "err", err)
				continue
			}
			t.DeviceLocal = false
			seen[t.ID] = true
			rec := st.Records[t.ID]
			switch {
			case rec == nil:
				st.Records[t.ID] = &schedule.Record{Task: t, DocID: d.GetId(), DocVersion: d.GetVersion()}
			case rec.PendingDelete || rec.Dirty:
				// Our change goes up next; push settles the conflict.
				rec.DocID = d.GetId()
			case d.GetVersion() > rec.DocVersion:
				rec.Task, rec.DocID, rec.DocVersion = t, d.GetId(), d.GetVersion()
			}
		}
		if synced {
			for id, rec := range st.Records {
				if !seen[id] && !rec.Task.DeviceLocal && rec.DocID != "" && !rec.Dirty && !rec.PendingDelete {
					delete(st.Records, id) // removed on another machine
				}
			}
		}
		for _, d := range histories {
			rec := st.Records[d.GetKey()]
			if rec == nil {
				continue
			}
			rec.HistoryDocID, rec.HistoryVersion = d.GetId(), d.GetVersion()
			if h.runner.Mine(rec.Task) {
				continue // ours: this machine knows better
			}
			var hd scheduleHistoryDoc
			if json.Unmarshal([]byte(d.GetContent()), &hd) == nil {
				rec.History = hd.Runs
				rec.Runs = max(rec.Runs, len(hd.Runs))
				if len(hd.Runs) > 0 {
					rec.LastFired = hd.Runs[0].StartedAt
				}
			}
		}
		return nil
	})
	if err != nil {
		h.s.log().Warn("schedules: applying the feed failed", "err", err)
	}
}

// push sends what changed here. A version conflict means another machine wrote meanwhile: this client's edit is the
// later one, and goes over it at the next tick.
func (h *scheduleHost) push(ctx context.Context, sc *syncclient.Syncer) error {
	st, err := h.store.View()
	if err != nil {
		return err
	}
	for _, rec := range st.Sorted() {
		id := rec.Task.ID
		if rec.Task.DeviceLocal {
			continue
		}
		switch {
		case rec.PendingDelete:
			if rec.DocID != "" {
				err := sc.Delete(ctx, rec.DocID, rec.DocVersion)
				var conflict *syncclient.ConflictError
				if errors.As(err, &conflict) {
					_ = h.store.Update(func(st *schedule.State) error {
						if r := st.Records[id]; r != nil {
							r.DocVersion = conflict.BaseVersion()
						}
						return nil
					})
					continue
				}
				if err != nil && !syncclient.IsNotFound(err) {
					return err
				}
				if rec.HistoryDocID != "" {
					_ = sc.Delete(ctx, rec.HistoryDocID, rec.HistoryVersion)
				}
			}
			if err := h.store.Update(func(st *schedule.State) error {
				delete(st.Records, id)
				return nil
			}); err != nil {
				return err
			}
			continue
		case rec.Dirty:
			content, err := json.Marshal(rec.Task)
			if err != nil {
				return err
			}
			d, err := sc.Put(ctx, scheduleRef(syncv1.DocumentKind_DOCUMENT_KIND_SCHEDULE, id), string(content), nil, proto.Int64(rec.DocVersion))
			var conflict *syncclient.ConflictError
			if errors.As(err, &conflict) {
				_ = h.store.Update(func(st *schedule.State) error {
					if r := st.Records[id]; r != nil {
						r.DocVersion = conflict.BaseVersion()
					}
					return nil
				})
				h.runner.Nudge()
				continue
			}
			if err != nil {
				return err
			}
			sent := rec.Task.UpdatedAt
			if err := h.store.Update(func(st *schedule.State) error {
				if r := st.Records[id]; r != nil {
					r.DocID, r.DocVersion = d.GetId(), d.GetVersion()
					r.Dirty = r.Task.UpdatedAt != sent
				}
				return nil
			}); err != nil {
				return err
			}
		}
		if rec.HistoryDirty && h.runner.Mine(rec.Task) {
			content, err := json.Marshal(scheduleHistoryDoc{Runs: rec.History})
			if err != nil {
				return err
			}
			d, err := sc.Put(ctx, scheduleRef(syncv1.DocumentKind_DOCUMENT_KIND_SCHEDULE_HISTORY, id), string(content), nil, nil)
			if err != nil {
				return err
			}
			count := len(rec.History)
			first := ""
			if count > 0 {
				first = rec.History[0].ID + string(rec.History[0].Status)
			}
			if err := h.store.Update(func(st *schedule.State) error {
				if r := st.Records[id]; r != nil {
					r.HistoryDocID, r.HistoryVersion = d.GetId(), d.GetVersion()
					now := ""
					if len(r.History) > 0 {
						now = r.History[0].ID + string(r.History[0].Status)
					}
					r.HistoryDirty = now != first || len(r.History) != count
				}
				return nil
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// runTriggers runs what was asked of this machine: a webhook, or Run now on another machine. A request is marked handled
// before it runs, so a crash or a failed delete can't run it twice.
func (h *scheduleHost) runTriggers(ctx context.Context, sc *syncclient.Syncer, now time.Time) {
	for _, d := range sc.Cache().List(syncv1.DocumentKind_DOCUMENT_KIND_SCHEDULE_TRIGGER) {
		var tr scheduleTriggerDoc
		if err := json.Unmarshal([]byte(d.GetContent()), &tr); err != nil || tr.DeviceID == "" || tr.DeviceID != h.s.DeviceID {
			continue
		}
		first := false
		if err := h.store.Update(func(st *schedule.State) error {
			if st.Handled == nil {
				st.Handled = map[string]bool{}
			}
			first = !st.Handled[d.GetId()]
			st.Handled[d.GetId()] = true
			return nil
		}); err != nil {
			continue
		}
		if first {
			source := schedule.SourceManual
			if tr.Source == string(schedule.SourceWebhook) {
				source = schedule.SourceWebhook
			}
			if err := h.runner.RunNow(tr.ScheduleID, source, tr.Payload, now); err != nil {
				h.s.log().Warn("schedules: a requested run did not start", "schedule", tr.ScheduleID, "err", err)
			}
		}
		if err := sc.Delete(ctx, d.GetId(), d.GetVersion()); err != nil {
			h.s.log().Debug("schedules: deleting a handled request failed", "err", err)
			continue
		}
		_ = h.store.Update(func(st *schedule.State) error {
			delete(st.Handled, d.GetId())
			return nil
		})
	}
}

// ---- RPCs ----

var (
	triggerKinds = map[schedule.Kind]agentv1.ScheduleTriggerKind{
		schedule.KindOnce: agentv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_KIND_ONCE, schedule.KindInterval: agentv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_KIND_INTERVAL,
		schedule.KindCron: agentv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_KIND_CRON, schedule.KindFile: agentv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_KIND_FILE_CHANGED,
		schedule.KindWebhook: agentv1.ScheduleTriggerKind_SCHEDULE_TRIGGER_KIND_WEBHOOK,
	}
	scheduleOrigins = map[schedule.Origin]agentv1.ScheduleOrigin{
		schedule.OriginUser: agentv1.ScheduleOrigin_SCHEDULE_ORIGIN_USER, schedule.OriginLoop: agentv1.ScheduleOrigin_SCHEDULE_ORIGIN_LOOP, schedule.OriginWakeup: agentv1.ScheduleOrigin_SCHEDULE_ORIGIN_WAKEUP,
	}
	runStatuses = map[schedule.Status]agentv1.ScheduleRunStatus{
		schedule.StatusRunning: agentv1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_RUNNING, schedule.StatusSucceeded: agentv1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_SUCCEEDED,
		schedule.StatusFailed: agentv1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_FAILED, schedule.StatusInterrupted: agentv1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_INTERRUPTED,
		schedule.StatusSkipped: agentv1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_SKIPPED, schedule.StatusMissed: agentv1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_MISSED,
		schedule.StatusStarted: agentv1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_STARTED,
	}
	runSources = map[schedule.Source]agentv1.ScheduleRunSource{
		schedule.SourceTime: agentv1.ScheduleRunSource_SCHEDULE_RUN_SOURCE_TIME, schedule.SourceCatchUp: agentv1.ScheduleRunSource_SCHEDULE_RUN_SOURCE_CATCH_UP,
		schedule.SourceManual: agentv1.ScheduleRunSource_SCHEDULE_RUN_SOURCE_MANUAL, schedule.SourceFile: agentv1.ScheduleRunSource_SCHEDULE_RUN_SOURCE_FILE,
		schedule.SourceWebhook: agentv1.ScheduleRunSource_SCHEDULE_RUN_SOURCE_WEBHOOK,
	}
)

func (h *scheduleHost) runProto(r schedule.Run) *agentv1.ScheduleRun {
	out := &agentv1.ScheduleRun{Id: r.ID, ScheduleId: r.ScheduleID, Status: runStatuses[r.Status], Source: runSources[r.Source], StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		SessionId: r.SessionID, Summary: strings.ToValidUTF8(r.Summary, ""), Error: strings.ToValidUTF8(r.Error, ""), DeviceName: r.DeviceName}
	if r.Active() && r.SessionID != "" {
		if ls := h.s.liveSession(r.SessionID); ls != nil && ls.info().GetStatus() == agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT {
			out.Status = agentv1.ScheduleRunStatus_SCHEDULE_RUN_STATUS_NEEDS_INPUT
		}
	}
	return out
}

func (h *scheduleHost) toProto(rec *schedule.Record) *agentv1.Schedule {
	t := rec.Task
	mine := h.runner.Mine(t)
	out := &agentv1.Schedule{
		Id: t.ID, Name: t.Name, Prompt: t.Prompt, Cwd: t.Cwd, Model: t.Model, Effort: t.Effort, PermissionMode: t.Mode, Engine: t.Engine, Worktree: t.Worktree, CatchUp: t.CatchUp, Paused: t.Paused,
		MaxRuns: int32(t.MaxRuns), ExpiresAt: t.ExpiresAt, SessionId: t.SessionID, DeviceId: t.DeviceID, DeviceName: t.DeviceName, DeviceLocal: t.DeviceLocal,
		Origin: scheduleOrigins[t.Origin], ThisDevice: mine, Runs: int32(rec.Runs), Done: t.Done, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		Trigger: &agentv1.ScheduleTrigger{Kind: triggerKinds[t.Trigger.Kind], At: t.Trigger.At, EverySeconds: t.Trigger.EverySeconds, Cron: t.Trigger.Cron, Timezone: t.Trigger.Timezone, Paths: t.Trigger.Paths, CooldownSeconds: t.Trigger.CooldownSeconds},
	}
	if next, ok := rec.NextAt(); ok {
		out.NextRunAt = next.UTC().Format(time.RFC3339)
	}
	if last, ok := rec.LastRun(); ok {
		out.LastRun = h.runProto(last)
	}
	if mine {
		if why := schedule.Blocked(rec); why != "" {
			out.Problem = why
			out.NeedsTrust = rec.Trusted != t.TrustHash()
		} else if t.Cwd != "" {
			if _, err := os.Stat(t.Cwd); err != nil {
				out.Problem = "The folder isn't there: " + t.Cwd
			}
		}
		if t.Trigger.Kind == schedule.KindWebhook && rec.WebhookSecret != "" && rec.DocID != "" && h.s.CoreURL != "" {
			out.Trigger.WebhookUrl = strings.TrimRight(h.s.CoreURL, "/") + "/api/schedules/webhook/" + rec.DocID + "." + rec.WebhookSecret
		}
	}
	return out
}

func taskFromProto(m *agentv1.Schedule) schedule.Task {
	tr := m.GetTrigger()
	t := schedule.Task{Name: strings.TrimSpace(m.GetName()), Prompt: m.GetPrompt(), Cwd: m.GetCwd(), Model: m.GetModel(), Effort: m.GetEffort(), Mode: m.GetPermissionMode(), Engine: m.GetEngine(),
		Worktree: m.GetWorktree(), CatchUp: m.GetCatchUp(), Paused: m.GetPaused(), MaxRuns: int(m.GetMaxRuns()), ExpiresAt: m.GetExpiresAt(),
		Trigger: schedule.Trigger{At: tr.GetAt(), EverySeconds: tr.GetEverySeconds(), Cron: tr.GetCron(), Timezone: tr.GetTimezone(), Paths: tr.GetPaths(), CooldownSeconds: tr.GetCooldownSeconds()}}
	for k, v := range triggerKinds {
		if v == tr.GetKind() {
			t.Trigger.Kind = k
		}
	}
	return t
}

// checkTask refuses what can't work, before it is stored.
func (s *Server) checkTask(t schedule.Task) error {
	if t.Mode != "" {
		m, err := permissions.ParseMode(t.Mode)
		if err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		if s.Base.ModePolicy != nil && !s.Base.ModePolicy.Allowed(m) {
			return connect.NewError(connect.CodePermissionDenied, &permissions.ModeDisabledError{Mode: m, Policy: true})
		}
	}
	if err := t.Validate(); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil
}

// isDeviceLocal: a schedule stays on this machine when nothing else could run it, or there's nowhere to sync it to.
func (s *Server) isDeviceLocal(t schedule.Task) bool {
	return s.ScheduleSync == nil || s.DeviceID == "" || t.SessionID != "" || t.Engine != "" || localmodels.IsLocal(t.Model)
}

func newWebhookSecret() (secret, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	secret = hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(secret))
	return secret, hex.EncodeToString(sum[:]), nil
}

// addTask stores a task made on this machine, which therefore allows it.
func (h *scheduleHost) addTask(t schedule.Task, runFirst bool) (*schedule.Record, error) {
	s := h.s
	now := s.now().UTC().Format(time.RFC3339)
	t.ID = "sch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	t.CreatedAt, t.UpdatedAt = now, now
	t.DeviceID, t.DeviceName = s.DeviceID, h.runner.DeviceName
	t.DeviceLocal = s.isDeviceLocal(t)
	if t.Origin == "" {
		t.Origin = schedule.OriginUser
	}
	if t.Trigger.Kind == schedule.KindCron && t.Trigger.Timezone == "" {
		t.Trigger.Timezone = localZoneName()
	}
	rec := &schedule.Record{Task: t, RunFirst: runFirst, Dirty: !t.DeviceLocal}
	if t.Trigger.Kind == schedule.KindWebhook {
		if t.DeviceLocal {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("a webhook arrives through the server, and this schedule stays on this machine: connect a server, or use a model and an agent that aren't local to it"))
		}
		secret, hash, err := newWebhookSecret()
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		rec.WebhookSecret, rec.Task.WebhookSecretSHA256 = secret, hash
	}
	rec.Trusted = rec.Task.TrustHash()
	if err := h.store.Update(func(st *schedule.State) error {
		st.Records[t.ID] = rec
		return nil
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	h.runner.Nudge()
	return rec, nil
}

func localZoneName() string {
	if name := time.Local.String(); name != "" && name != "Local" {
		return name
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(link, "zoneinfo/"); i >= 0 {
			return link[i+len("zoneinfo/"):]
		}
	}
	return ""
}

func scheduleNotFound(id string) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("no schedule %s", id))
}

// ListSchedules implements AgentService.ListSchedules.
func (s *Server) ListSchedules(context.Context, *connect.Request[agentv1.ListSchedulesRequest]) (*connect.Response[agentv1.ListSchedulesResponse], error) {
	h := s.scheduler()
	if s.ScheduleSync != nil {
		h.applyFeed(s.ScheduleSync.Cache(), s.now())
	}
	st, err := h.store.View()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := &agentv1.ListSchedulesResponse{DeviceId: s.DeviceID, DeviceName: h.runner.DeviceName, Synced: s.ScheduleSync != nil && s.DeviceID != "", Enabled: h.enabled()}
	for _, rec := range st.Sorted() {
		if !rec.PendingDelete {
			out.Schedules = append(out.Schedules, h.toProto(rec))
		}
	}
	return connect.NewResponse(out), nil
}

// CreateSchedule implements AgentService.CreateSchedule.
func (s *Server) CreateSchedule(_ context.Context, req *connect.Request[agentv1.CreateScheduleRequest]) (*connect.Response[agentv1.CreateScheduleResponse], error) {
	h := s.scheduler()
	t := taskFromProto(req.Msg.GetSchedule())
	if err := s.checkTask(t); err != nil {
		return nil, err
	}
	rec, err := h.addTask(t, false)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.CreateScheduleResponse{Schedule: h.toProto(rec)}), nil
}

// editTask changes a record and returns it as stored.
func (h *scheduleHost) editTask(id string, fn func(*schedule.Record) error) (*schedule.Record, error) {
	var out *schedule.Record
	err := h.store.Update(func(st *schedule.State) error {
		rec := st.Records[id]
		if rec == nil || rec.PendingDelete {
			return scheduleNotFound(id)
		}
		if err := fn(rec); err != nil {
			return err
		}
		rec.Task.UpdatedAt = h.s.now().UTC().Format(time.RFC3339)
		if !rec.Task.DeviceLocal {
			rec.Dirty = true
		}
		out = rec
		return nil
	})
	if err != nil {
		var ce *connect.Error
		if errors.As(err, &ce) {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	h.runner.Nudge()
	return out, nil
}

// UpdateSchedule implements AgentService.UpdateSchedule. Who edits here allows the result, on the machine that runs it;
// an edit of another machine's schedule waits for that machine's user.
func (s *Server) UpdateSchedule(_ context.Context, req *connect.Request[agentv1.UpdateScheduleRequest]) (*connect.Response[agentv1.UpdateScheduleResponse], error) {
	h := s.scheduler()
	m := req.Msg.GetSchedule()
	t := taskFromProto(m)
	if err := s.checkTask(t); err != nil {
		return nil, err
	}
	rec, err := h.editTask(m.GetId(), func(rec *schedule.Record) error {
		old := rec.Task
		t.ID, t.CreatedAt, t.Origin, t.SessionID = old.ID, old.CreatedAt, old.Origin, old.SessionID
		t.DeviceID, t.DeviceName, t.DeviceLocal = old.DeviceID, old.DeviceName, old.DeviceLocal
		t.WebhookSecretSHA256 = old.WebhookSecretSHA256
		if s.DeviceID != "" && m.GetDeviceId() == s.DeviceID && old.DeviceID != s.DeviceID {
			if old.DeviceLocal {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("this schedule stays on its machine"))
			}
			t.DeviceID, t.DeviceName = s.DeviceID, h.runner.DeviceName
		}
		if t.Trigger.Kind == schedule.KindCron && t.Trigger.Timezone == "" {
			t.Trigger.Timezone = localZoneName()
		}
		mine := h.runner.Mine(t)
		switch {
		case t.Trigger.Kind != schedule.KindWebhook:
			t.WebhookSecretSHA256, rec.WebhookSecret = "", ""
		case mine && (rec.WebhookSecret == "" || t.WebhookSecretSHA256 == ""):
			if t.DeviceLocal {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("a webhook arrives through the server, and this schedule stays on this machine"))
			}
			secret, hash, err := newWebhookSecret()
			if err != nil {
				return connect.NewError(connect.CodeInternal, err)
			}
			rec.WebhookSecret, t.WebhookSecretSHA256 = secret, hash
		}
		t.Done = old.Done && !rescheduled(old, t)
		if rescheduled(old, t) {
			rec.Runs, rec.LastFired = 0, ""
			t.CreatedAt = s.now().UTC().Format(time.RFC3339)
		}
		rec.Task = t
		if mine {
			rec.Trusted = t.TrustHash()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.UpdateScheduleResponse{Schedule: h.toProto(rec)}), nil
}

// rescheduled: the trigger changed, so what's past no longer counts against it.
func rescheduled(old, t schedule.Task) bool {
	a, _ := json.Marshal(old.Trigger)
	b, _ := json.Marshal(t.Trigger)
	return string(a) != string(b)
}

// DeleteSchedule implements AgentService.DeleteSchedule.
func (s *Server) DeleteSchedule(_ context.Context, req *connect.Request[agentv1.DeleteScheduleRequest]) (*connect.Response[agentv1.DeleteScheduleResponse], error) {
	h := s.scheduler()
	id := req.Msg.GetId()
	if err := h.store.Update(func(st *schedule.State) error {
		rec := st.Records[id]
		if rec == nil {
			return scheduleNotFound(id)
		}
		if rec.Task.DeviceLocal || (rec.DocID == "" && rec.DocVersion == 0) {
			delete(st.Records, id)
			return nil
		}
		rec.PendingDelete = true
		return nil
	}); err != nil {
		var ce *connect.Error
		if errors.As(err, &ce) {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	h.runner.Nudge()
	return connect.NewResponse(&agentv1.DeleteScheduleResponse{}), nil
}

// SetSchedulePaused implements AgentService.SetSchedulePaused.
func (s *Server) SetSchedulePaused(_ context.Context, req *connect.Request[agentv1.SetSchedulePausedRequest]) (*connect.Response[agentv1.SetSchedulePausedResponse], error) {
	h := s.scheduler()
	rec, err := h.editTask(req.Msg.GetId(), func(rec *schedule.Record) error {
		if rec.Task.Paused && !req.Msg.GetPaused() {
			// What came due while paused doesn't all run on the way back.
			rec.LastFired = s.now().UTC().Format(time.RFC3339)
		}
		rec.Task.Paused = req.Msg.GetPaused()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.SetSchedulePausedResponse{Schedule: h.toProto(rec)}), nil
}

// RunScheduleNow implements AgentService.RunScheduleNow.
func (s *Server) RunScheduleNow(ctx context.Context, req *connect.Request[agentv1.RunScheduleNowRequest]) (*connect.Response[agentv1.RunScheduleNowResponse], error) {
	h := s.scheduler()
	id := req.Msg.GetId()
	st, err := h.store.View()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	rec := st.Records[id]
	if rec == nil || rec.PendingDelete {
		return nil, scheduleNotFound(id)
	}
	queued := false
	if h.runner.Mine(rec.Task) {
		if err := h.runner.RunNow(id, schedule.SourceManual, "", s.now()); err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
	} else {
		if s.ScheduleSync == nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this client is not connected to the server that reaches that machine"))
		}
		content, err := json.Marshal(scheduleTriggerDoc{ScheduleID: id, DeviceID: rec.Task.DeviceID, Source: string(schedule.SourceManual), ReceivedAt: s.now().UTC().Format(time.RFC3339)})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if _, err := s.ScheduleSync.Put(ctx, scheduleRef(syncv1.DocumentKind_DOCUMENT_KIND_SCHEDULE_TRIGGER, "trg_"+uuid.NewString()), string(content), nil, proto.Int64(0)); err != nil {
			return nil, syncError(err)
		}
		queued = true
	}
	st, err = h.store.View()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.RunScheduleNowResponse{Schedule: h.toProto(st.Records[id]), QueuedOnOtherDevice: queued}), nil
}

// ListScheduleRuns implements AgentService.ListScheduleRuns.
func (s *Server) ListScheduleRuns(_ context.Context, req *connect.Request[agentv1.ListScheduleRunsRequest]) (*connect.Response[agentv1.ListScheduleRunsResponse], error) {
	h := s.scheduler()
	st, err := h.store.View()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	rec := st.Records[req.Msg.GetId()]
	if rec == nil {
		return nil, scheduleNotFound(req.Msg.GetId())
	}
	out := &agentv1.ListScheduleRunsResponse{}
	for _, r := range rec.History {
		if n := req.Msg.GetLimit(); n > 0 && len(out.Runs) >= int(n) {
			break
		}
		out.Runs = append(out.Runs, h.runProto(r))
	}
	return connect.NewResponse(out), nil
}

// TrustSchedule implements AgentService.TrustSchedule.
func (s *Server) TrustSchedule(_ context.Context, req *connect.Request[agentv1.TrustScheduleRequest]) (*connect.Response[agentv1.TrustScheduleResponse], error) {
	h := s.scheduler()
	var out *schedule.Record
	err := h.store.Update(func(st *schedule.State) error {
		rec := st.Records[req.Msg.GetId()]
		if rec == nil {
			return scheduleNotFound(req.Msg.GetId())
		}
		if !h.runner.Mine(rec.Task) {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("it runs on %s: allow it there", rec.Task.DeviceName))
		}
		rec.Trusted = rec.Task.TrustHash()
		// Coming due counts from now, not from before it was allowed.
		rec.LastFired = s.now().UTC().Format(time.RFC3339)
		out = rec
		return nil
	})
	if err != nil {
		var ce *connect.Error
		if errors.As(err, &ce) {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	h.runner.Nudge()
	return connect.NewResponse(&agentv1.TrustScheduleResponse{Schedule: h.toProto(out)}), nil
}

// RotateScheduleWebhook implements AgentService.RotateScheduleWebhook.
func (s *Server) RotateScheduleWebhook(_ context.Context, req *connect.Request[agentv1.RotateScheduleWebhookRequest]) (*connect.Response[agentv1.RotateScheduleWebhookResponse], error) {
	h := s.scheduler()
	rec, err := h.editTask(req.Msg.GetId(), func(rec *schedule.Record) error {
		if rec.Task.Trigger.Kind != schedule.KindWebhook {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("this schedule has no webhook"))
		}
		if !h.runner.Mine(rec.Task) {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("it runs on %s: rotate it there", rec.Task.DeviceName))
		}
		secret, hash, err := newWebhookSecret()
		if err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
		rec.WebhookSecret, rec.Task.WebhookSecretSHA256 = secret, hash
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.RotateScheduleWebhookResponse{Schedule: h.toProto(rec)}), nil
}

// ---- /loop, /schedule ----

func runScheduleCommand(context.Context, *Server, *liveSession, string) slashOutcome {
	return slashOutcome{action: agentv1.SlashCommandResult_ACTION_OPEN_SCHEDULED}
}

func runLoop(ctx context.Context, s *Server, ls *liveSession, args string) slashOutcome {
	args = strings.TrimSpace(args)
	h := s.scheduler()
	fields := strings.Fields(args)
	switch {
	case args == "" || strings.EqualFold(args, "list"):
		return slashOutcome{text: loopListing(h, ls.sess.ID)}
	case strings.EqualFold(fields[0], "stop"):
		n, err := h.stopSessionLoops(ls.sess.ID, strings.Join(fields[1:], " "))
		if err != nil {
			return slashError("%v", err)
		}
		if n == 0 {
			return slashOutcome{text: "No loop is running in this session."}
		}
		return slashOutcome{text: "Loop stopped."}
	}
	prompt := args
	var every time.Duration
	if d, ok := schedule.ParseEvery(fields[0]); ok {
		every, prompt = d, strings.TrimSpace(strings.TrimPrefix(args, fields[0]))
	} else if strings.EqualFold(fields[0], "every") && len(fields) > 1 {
		if d, ok := schedule.ParseEvery(fields[1]); ok {
			every = d
			prompt = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(args, fields[0])), fields[1]))
		}
	}
	if prompt == "" {
		return slashError("Say what to repeat: /loop [interval] <prompt or /command>, with an interval like 10m.")
	}
	if every == 0 {
		// The model paces itself, with ScheduleWakeup.
		ls.sess.Agent.Remind(agent.ReminderLoopSelfPaced, map[string]string{"prompt": escapeTrigger(prompt)})
		if err := s.sendText(ctx, ls, prompt); err != nil {
			return slashError("%s", connectMessage(err))
		}
		return slashOutcome{text: "Looping at the model's pace. /loop stop ends it."}
	}
	if every < schedule.MinInterval {
		return slashError("The shortest loop is %s.", schedule.MinInterval)
	}
	expires := s.now().Add(sessionScheduleLife).UTC().Format(time.RFC3339)
	t := schedule.Task{Name: truncateRunes(prompt, 60), Prompt: prompt, Origin: schedule.OriginLoop, SessionID: ls.sess.ID, ExpiresAt: expires,
		Trigger: schedule.Trigger{Kind: schedule.KindInterval, EverySeconds: int64(every / time.Second)}}
	if err := t.Validate(); err != nil {
		return slashError("%v", err)
	}
	rec, err := h.addTask(t, true)
	if err != nil {
		return slashError("%s", connectMessage(err))
	}
	return slashOutcome{text: fmt.Sprintf("Looping every %s, starting now, for up to a week (%s). /loop stop ends it.", every, rec.Task.ID)}
}

func loopListing(h *scheduleHost, session string) string {
	st, err := h.store.View()
	if err != nil {
		return err.Error()
	}
	var rows []string
	for _, rec := range st.Sorted() {
		t := rec.Task
		if t.SessionID != session || t.Done || t.Origin == schedule.OriginUser {
			continue
		}
		rows = append(rows, fmt.Sprintf("%s  %s", t.ID, truncateRunes(t.Prompt, 80)))
	}
	if len(rows) == 0 {
		return "No loop in this session. /loop [interval] <prompt or /command> starts one."
	}
	return "Running here:\n" + strings.Join(rows, "\n")
}

// stopSessionLoops ends the session's loops and wake-ups, or the one named.
func (h *scheduleHost) stopSessionLoops(session, id string) (int, error) {
	n := 0
	err := h.store.Update(func(st *schedule.State) error {
		for key, rec := range st.Records {
			t := rec.Task
			if t.SessionID != session || t.Origin == schedule.OriginUser || (id != "" && key != id) {
				continue
			}
			if !t.Done {
				n++
			}
			delete(st.Records, key)
		}
		return nil
	})
	return n, err
}

// ---- the wake-up tools (tools.Wakeups) ----

var _ tools.Wakeups = (*liveSession)(nil)

func wakeupInfo(rec *schedule.Record) tools.WakeupInfo {
	info := tools.WakeupInfo{ID: rec.Task.ID, Note: rec.Task.Prompt, Runs: rec.Runs, Max: rec.Task.MaxRuns}
	if next, ok := rec.NextAt(); ok {
		info.Next = next.UTC().Format(time.RFC3339)
	}
	switch rec.Task.Trigger.Kind {
	case schedule.KindInterval:
		info.Repeat = (time.Duration(rec.Task.Trigger.EverySeconds) * time.Second).String()
	case schedule.KindCron:
		info.Repeat = rec.Task.Trigger.Cron
	}
	return info
}

// ScheduleWakeup implements tools.Wakeups.
func (ls *liveSession) ScheduleWakeup(_ context.Context, r tools.WakeupRequest) (tools.WakeupInfo, error) {
	var none tools.WakeupInfo
	s := ls.srv
	h := s.scheduler()
	st, err := h.store.View()
	if err != nil {
		return none, err
	}
	count := 0
	for _, rec := range st.Records {
		if rec.Task.SessionID == ls.sess.ID && !rec.Task.Done {
			count++
		}
	}
	if count >= wakeupsPerSession {
		return none, fmt.Errorf("this session already has %d wake-ups; cancel one first", count)
	}
	now := s.now()
	t := schedule.Task{Name: truncateRunes(strings.Join(strings.Fields(r.Note), " "), 60), Prompt: r.Note, Origin: schedule.OriginWakeup, SessionID: ls.sess.ID, CatchUp: true}
	switch {
	case r.In > 0 || !r.At.IsZero():
		at := r.At
		if r.In > 0 {
			at = now.Add(r.In)
		}
		if at.Before(now.Add(minWakeupDelay)) {
			return none, fmt.Errorf("a wake-up comes at least %s from now", minWakeupDelay)
		}
		t.Trigger = schedule.Trigger{Kind: schedule.KindOnce, At: at.UTC().Format(time.RFC3339)}
	default:
		t.Trigger = schedule.Trigger{Kind: schedule.KindInterval, EverySeconds: int64(r.Every / time.Second)}
		if r.Cron != "" {
			t.Trigger = schedule.Trigger{Kind: schedule.KindCron, Cron: r.Cron, Timezone: localZoneName()}
		}
		t.MaxRuns = r.MaxRuns
		if t.MaxRuns <= 0 {
			t.MaxRuns = wakeupDefaultRuns
		}
		if t.MaxRuns > wakeupMaxRuns {
			t.MaxRuns = wakeupMaxRuns
		}
		t.ExpiresAt = now.Add(sessionScheduleLife).UTC().Format(time.RFC3339)
	}
	if err := t.Validate(); err != nil {
		return none, err
	}
	rec, err := h.addTask(t, false)
	if err != nil {
		return none, errors.New(connectMessage(err))
	}
	return wakeupInfo(rec), nil
}

// ListWakeups implements tools.Wakeups.
func (ls *liveSession) ListWakeups(context.Context) ([]tools.WakeupInfo, error) {
	st, err := ls.srv.scheduler().store.View()
	if err != nil {
		return nil, err
	}
	var out []tools.WakeupInfo
	for _, rec := range st.Sorted() {
		if rec.Task.SessionID == ls.sess.ID && !rec.Task.Done {
			out = append(out, wakeupInfo(rec))
		}
	}
	return out, nil
}

// CancelWakeup implements tools.Wakeups. Only the session's own, and only those made in the session (what the user made
// on the Scheduled screen isn't the model's to end).
func (ls *liveSession) CancelWakeup(_ context.Context, id string) (tools.WakeupInfo, error) {
	var out tools.WakeupInfo
	err := ls.srv.scheduler().store.Update(func(st *schedule.State) error {
		rec := st.Records[id]
		if rec == nil || rec.Task.SessionID != ls.sess.ID || rec.Task.Origin == schedule.OriginUser {
			return fmt.Errorf("this session has no wake-up %s", id)
		}
		out = wakeupInfo(rec)
		delete(st.Records, id)
		return nil
	})
	return out, err
}
