package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// PushNotification limits. A model cannot spam: at most one per pushMinGap and pushPerHour per hour, per session.
const (
	maxPushTitle = 80
	maxPushBody  = 300
	pushMinGap   = time.Minute
	pushPerHour  = 10
)

// UserNotifier is implemented by an Interactor whose frontends can show system notifications (AgentService).
type UserNotifier interface {
	// UserLooking reports whether a frontend has this session open and focused.
	UserLooking() bool
	// PushToUser hands the notification to the frontends; false when none is connected to the session.
	PushToUser(id, title, body string) bool
}

// pushThrottle is the session's record of notifications sent.
type pushThrottle struct {
	mu   sync.Mutex
	sent []time.Time
	// now is replaced by tests.
	now func() time.Time
}

// allow records a send when the limits allow it; otherwise it says how long to wait.
func (p *pushThrottle) allow() (bool, time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	kept := p.sent[:0]
	for _, t := range p.sent {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	p.sent = kept
	if n := len(p.sent); n > 0 {
		if wait := pushMinGap - now.Sub(p.sent[n-1]); wait > 0 {
			return false, wait
		}
		if n >= pushPerHour {
			return false, time.Hour - now.Sub(p.sent[0])
		}
	}
	p.sent = append(p.sent, now)
	return true, 0
}

// Outcomes of pushToUser.
const (
	pushSent     = "sent"
	pushNoViewer = "no_viewer"
	pushLooking  = "looking"
	pushLimited  = "throttled"
	pushNoUI     = "no_ui"
)

// userNotifier returns the session's notifier, nil when its frontends cannot show notifications (terminal UI, no
// frontend at all).
func userNotifier(env *Env) UserNotifier {
	h := env.Host()
	if h == nil {
		return nil
	}
	ia := h.Interactor()
	if r, ok := ia.(HTMLRenderer); ok && !r.RendersHTML() {
		return nil
	}
	n, _ := ia.(UserNotifier)
	return n
}

// pushToUser runs the checks every notification goes through. Being held back while the user looks does not count
// against the limits.
func pushToUser(env *Env, id, title, body string) (outcome string, wait time.Duration) {
	n := userNotifier(env)
	if n == nil {
		return pushNoUI, 0
	}
	if n.UserLooking() {
		return pushLooking, 0
	}
	if ok, w := env.push.allow(); !ok {
		return pushLimited, w
	}
	if !n.PushToUser(id, title, body) {
		return pushNoViewer, 0
	}
	return pushSent, 0
}

// oneLine trims s and folds its whitespace, control characters included, to single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// PushNotification alerts the user that this session needs them. It reaches them outside the conversation only, and
// changes nothing, so it needs no permission.
type PushNotification struct{}

func (*PushNotification) Name() string                  { return "PushNotification" }
func (*PushNotification) ReadOnly(json.RawMessage) bool { return true }

func (*PushNotification) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	title, body := oneLine(in.Title), oneLine(in.Body)
	switch {
	case title == "":
		return ErrorResult("InputValidationError: title is required")
	case body == "":
		return ErrorResult("InputValidationError: body is required")
	case len([]rune(title)) > maxPushTitle:
		return ErrorResult("InputValidationError: title is longer than %d characters", maxPushTitle)
	case len([]rune(body)) > maxPushBody:
		return ErrorResult("InputValidationError: body is longer than %d characters", maxPushBody)
	}
	outcome, wait := pushToUser(env, call.ID, title, body)
	structured := map[string]any{"title": title, "body": body, "outcome": outcome, "sent": outcome == pushSent}
	var r Result
	switch outcome {
	case pushSent:
		r = TextResult("Sent to the frontends connected to this session.", structured)
		r.Summary = "Notified the user"
	case pushNoViewer:
		r = TextResult("Not sent: no frontend is connected to this session right now, and there is no other route to the user. They will see your messages when they come back.", structured)
		r.Summary = "Not sent (nobody connected)"
	case pushLooking:
		r = TextResult("Not sent: the user is looking at this session, so your message reaches them directly.", structured)
		r.Summary = "Not sent (user is looking)"
	case pushLimited:
		structured["retry_after_seconds"] = int(wait.Round(time.Second).Seconds())
		r = TextResult(fmt.Sprintf("Not sent: notifications are limited, and this one could go out in %s. Do not wait for that; carry on, or end your turn with what you need from the user.", wait.Round(time.Second)), structured)
		r.Summary = "Not sent (limited)"
	default:
		r = TextResult("Not sent: no interface that can show notifications is attached to this session.", structured)
		r.Summary = "Not sent (no UI)"
	}
	return r
}
