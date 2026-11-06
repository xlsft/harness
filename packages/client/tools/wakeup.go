package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The tools a session uses to continue itself later, one time or on a repeat. A wake-up belongs to the session and to
// this machine: when its time comes the session reopens if it has to and gets a reminder, which starts a turn when idle.

// WakeupRequest asks for a wake-up. Exactly one of In, At, Every or Cron is set.
type WakeupRequest struct {
	In, Every time.Duration
	At        time.Time
	Cron      string
	// Note is what the session tells itself to do then.
	Note string
	// MaxRuns ends a repeating wake-up (0: the default limit).
	MaxRuns int
}

// WakeupInfo describes a wake-up of the session.
type WakeupInfo struct {
	ID     string `json:"id"`
	Note   string `json:"note"`
	Next   string `json:"next,omitempty"`
	Repeat string `json:"repeat,omitempty"`
	Runs   int    `json:"runs"`
	Max    int    `json:"max_runs,omitempty"`
}

// Wakeups is implemented by an Interactor that runs on the client's agent service, where the scheduler lives.
type Wakeups interface {
	ScheduleWakeup(ctx context.Context, r WakeupRequest) (WakeupInfo, error)
	ListWakeups(ctx context.Context) ([]WakeupInfo, error)
	CancelWakeup(ctx context.Context, id string) (WakeupInfo, error)
}

const noWakeups = "Wake-ups are not available here: this session does not run on the client's agent service."

func wakeupsOf(env *Env) Wakeups {
	h := env.Host()
	if h == nil {
		return nil
	}
	w, _ := h.Interactor().(Wakeups)
	return w
}

// RegisterWakeupTools adds the tools to r.
func RegisterWakeupTools(r *Registry) {
	for _, t := range []Tool{&ScheduleWakeup{}, &ScheduleList{}, &ScheduleCancel{}} {
		r.Register(t)
	}
}

func wakeupRow(w WakeupInfo) string {
	line := w.ID
	if w.Next != "" {
		line += "  next " + w.Next
	}
	if w.Repeat != "" {
		line += "  repeats " + w.Repeat
	}
	if w.Max > 0 {
		line += fmt.Sprintf("  run %d of %d", w.Runs, w.Max)
	}
	return line + "  " + truncateLine(w.Note, 120)
}

// ---- ScheduleWakeup ----

// ScheduleWakeup asks for a turn later.
type ScheduleWakeup struct{}

func (*ScheduleWakeup) Name() string                  { return "ScheduleWakeup" }
func (*ScheduleWakeup) ReadOnly(json.RawMessage) bool { return true }

func (*ScheduleWakeup) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		DelaySeconds int64  `json:"delay_seconds"`
		At           string `json:"at"`
		EverySeconds int64  `json:"every_seconds"`
		Cron         string `json:"cron"`
		Note         string `json:"note"`
		MaxRuns      int    `json:"max_runs"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.Note) == "" {
		return ErrorResult("InputValidationError: note is required: it is what you will read when you wake up")
	}
	set := 0
	req := WakeupRequest{Note: in.Note, MaxRuns: in.MaxRuns, Cron: strings.TrimSpace(in.Cron)}
	if in.DelaySeconds > 0 {
		set++
		req.In = time.Duration(in.DelaySeconds) * time.Second
	}
	if in.At != "" {
		set++
		at, err := time.Parse(time.RFC3339, in.At)
		if err != nil {
			return ErrorResult("InputValidationError: at must be an RFC 3339 time")
		}
		req.At = at
	}
	if in.EverySeconds > 0 {
		set++
		req.Every = time.Duration(in.EverySeconds) * time.Second
	}
	if req.Cron != "" {
		set++
	}
	if set != 1 {
		return ErrorResult("InputValidationError: set exactly one of delay_seconds, at, every_seconds, cron")
	}
	w := wakeupsOf(env)
	if w == nil {
		return ErrorResult(noWakeups)
	}
	info, err := w.ScheduleWakeup(ctx, req)
	if err != nil {
		return ErrorResult("%v", err)
	}
	r := TextResult("Scheduled: "+wakeupRow(info)+"\nYou get a reminder then, which starts a turn if the session is idle. If you have nothing else to do, end your turn now.", map[string]any{"wakeup": info})
	r.Summary = "Wake-up " + info.Next
	return r
}

// ---- ScheduleList ----

// ScheduleList shows the session's wake-ups.
type ScheduleList struct{}

func (*ScheduleList) Name() string                  { return "ScheduleList" }
func (*ScheduleList) ReadOnly(json.RawMessage) bool { return true }

func (*ScheduleList) Run(ctx context.Context, env *Env, call Call) Result {
	w := wakeupsOf(env)
	if w == nil {
		return ErrorResult(noWakeups)
	}
	list, err := w.ListWakeups(ctx)
	if err != nil {
		return ErrorResult("%v", err)
	}
	if len(list) == 0 {
		return TextResult("This session has no wake-ups.", map[string]any{"wakeups": list})
	}
	rows := make([]string, 0, len(list))
	for _, i := range list {
		rows = append(rows, wakeupRow(i))
	}
	r := TextResult(strings.Join(rows, "\n"), map[string]any{"wakeups": list})
	r.Summary = fmt.Sprintf("%d wake-ups", len(list))
	return r
}

// ---- ScheduleCancel ----

// ScheduleCancel ends a wake-up of the session.
type ScheduleCancel struct{}

func (*ScheduleCancel) Name() string                  { return "ScheduleCancel" }
func (*ScheduleCancel) ReadOnly(json.RawMessage) bool { return true }

func (*ScheduleCancel) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		ID string `json:"id"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.ID == "" {
		return ErrorResult("InputValidationError: id is required")
	}
	w := wakeupsOf(env)
	if w == nil {
		return ErrorResult(noWakeups)
	}
	info, err := w.CancelWakeup(ctx, in.ID)
	if err != nil {
		return ErrorResult("%v", err)
	}
	r := TextResult("Cancelled "+info.ID, map[string]any{"id": info.ID})
	r.Summary = "Cancelled " + info.ID
	return r
}
