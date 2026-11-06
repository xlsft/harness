// Package schedule keeps the scheduled tasks of this machine and decides when they run. It knows nothing of sessions or
// the server: the client's agent service (server/schedules.go) starts what a Fire asks for and moves documents.
package schedule

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

// Kind is what makes a task run.
type Kind string

// Trigger kinds.
const (
	KindOnce     Kind = "once"
	KindInterval Kind = "interval"
	KindCron     Kind = "cron"
	KindFile     Kind = "file"
	KindWebhook  Kind = "webhook"
)

// Origin says who made a task.
type Origin string

// Origins.
const (
	OriginUser   Origin = "user"
	OriginLoop   Origin = "loop"
	OriginWakeup Origin = "wakeup"
)

// Limits on what a task may ask for.
const (
	// MinInterval is the shortest repeat: a schedule is not a busy loop.
	MinInterval = 60 * time.Second
	// DefaultCooldown is the least time between two runs of a file trigger.
	DefaultCooldown = 60 * time.Second
	maxPrompt       = 64 << 10
	maxName         = 120
	maxPaths        = 32
)

// Trigger says when a task runs.
type Trigger struct {
	Kind            Kind     `json:"kind"`
	At              string   `json:"at,omitempty"`
	EverySeconds    int64    `json:"everySeconds,omitempty"`
	Cron            string   `json:"cron,omitempty"`
	Timezone        string   `json:"timezone,omitempty"`
	Paths           []string `json:"paths,omitempty"`
	CooldownSeconds int64    `json:"cooldownSeconds,omitempty"`
}

// Task is a schedule as it is stored and synced.
type Task struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Prompt     string  `json:"prompt"`
	Trigger    Trigger `json:"trigger"`
	Cwd        string  `json:"cwd,omitempty"`
	Model      string  `json:"model,omitempty"`
	Effort     string  `json:"effort,omitempty"`
	Mode       string  `json:"permissionMode,omitempty"`
	Engine     string  `json:"engine,omitempty"`
	Worktree   bool    `json:"worktree,omitempty"`
	CatchUp    bool    `json:"catchUp,omitempty"`
	Paused     bool    `json:"paused,omitempty"`
	Done       bool    `json:"done,omitempty"`
	MaxRuns    int     `json:"maxRuns,omitempty"`
	ExpiresAt  string  `json:"expiresAt,omitempty"`
	SessionID  string  `json:"sessionId,omitempty"`
	Origin     Origin  `json:"origin,omitempty"`
	DeviceID   string  `json:"deviceId,omitempty"`
	DeviceName string  `json:"deviceName,omitempty"`
	// AwayHostID is the always-on host that runs this while its machine is away; RepositoryKey is what the host checks out.
	AwayHostID    string `json:"awayHostId,omitempty"`
	RepositoryKey string `json:"repositoryKey,omitempty"`
	// DeviceLocal never leaves this machine.
	DeviceLocal bool `json:"deviceLocal,omitempty"`
	// WebhookSecretSHA256 is what the server's webhook route checks a posted secret against; the secret itself stays on
	// the machine that made it.
	WebhookSecretSHA256 string `json:"webhookSecretSha256,omitempty"`
	CreatedAt           string `json:"createdAt,omitempty"`
	UpdatedAt           string `json:"updatedAt,omitempty"`
}

// Status of a run.
type Status string

// Run statuses.
const (
	StatusRunning     Status = "running"
	StatusSucceeded   Status = "succeeded"
	StatusFailed      Status = "failed"
	StatusInterrupted Status = "interrupted"
	StatusSkipped     Status = "skipped"
	StatusMissed      Status = "missed"
	StatusStarted     Status = "started"
)

// Source is what made a run start.
type Source string

// Run sources.
const (
	SourceTime    Source = "time"
	SourceCatchUp Source = "catch_up"
	SourceManual  Source = "manual"
	SourceFile    Source = "file"
	SourceWebhook Source = "webhook"
)

// Run is one entry of a task's history.
type Run struct {
	ID         string `json:"id"`
	ScheduleID string `json:"scheduleId"`
	Status     Status `json:"status"`
	Source     Source `json:"source"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
	SessionID  string `json:"sessionId,omitempty"`
	Summary    string `json:"summary,omitempty"`
	Error      string `json:"error,omitempty"`
	DeviceName string `json:"deviceName,omitempty"`
}

// Active reports whether the run still works.
func (r Run) Active() bool { return r.Status == StatusRunning }

// EventTriggered reports whether something outside decides when the task runs.
func (t Task) EventTriggered() bool {
	return t.Trigger.Kind == KindFile || t.Trigger.Kind == KindWebhook
}

// Location is the zone of a cron trigger.
func (t Task) Location() *time.Location {
	if t.Trigger.Timezone != "" {
		if l, err := time.LoadLocation(t.Trigger.Timezone); err == nil {
			return l
		}
	}
	return time.Local
}

// Validate checks what a user or the model asked for, and what a synced document says.
func (t Task) Validate() error {
	if strings.TrimSpace(t.Prompt) == "" {
		return errors.New("a schedule needs a prompt")
	}
	if len(t.Prompt) > maxPrompt {
		return fmt.Errorf("the prompt is longer than %d bytes", maxPrompt)
	}
	if len([]rune(t.Name)) > maxName {
		return fmt.Errorf("the name is longer than %d characters", maxName)
	}
	if t.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, t.ExpiresAt); err != nil {
			return errors.New("the end date is not a time")
		}
	}
	if t.MaxRuns < 0 {
		return errors.New("the run limit can't be negative")
	}
	switch t.Trigger.Kind {
	case KindOnce:
		if _, err := time.Parse(time.RFC3339, t.Trigger.At); err != nil {
			return errors.New("a one-off needs a time (RFC 3339)")
		}
	case KindInterval:
		if time.Duration(t.Trigger.EverySeconds)*time.Second < MinInterval {
			return fmt.Errorf("the shortest repeat is %s", MinInterval)
		}
	case KindCron:
		if _, err := ParseCron(t.Trigger.Cron); err != nil {
			return fmt.Errorf("the cron expression doesn't parse: %w", err)
		}
		if t.Trigger.Timezone != "" {
			if _, err := time.LoadLocation(t.Trigger.Timezone); err != nil {
				return fmt.Errorf("unknown time zone %q", t.Trigger.Timezone)
			}
		}
	case KindFile:
		if t.Cwd == "" {
			return errors.New("a file trigger needs a folder")
		}
		if len(t.Trigger.Paths) == 0 || len(t.Trigger.Paths) > maxPaths {
			return fmt.Errorf("a file trigger needs between 1 and %d patterns", maxPaths)
		}
		for _, p := range t.Trigger.Paths {
			if p == "" || path.IsAbs(p) || strings.HasPrefix(p, "..") || !doublestar.ValidatePattern(p) {
				return fmt.Errorf("%q is not a pattern inside the folder", p)
			}
		}
	case KindWebhook:
	default:
		return errors.New("a schedule needs a trigger: once, interval, cron, file or webhook")
	}
	if t.EventTriggered() && t.Mode == "bypassPermissions" {
		return errors.New("a run started from outside can't skip permission checks: choose another mode")
	}
	return nil
}

// Next returns when a task's time comes after base. False: its time isn't known in advance, or it never comes.
func (t Task) Next(base time.Time) (time.Time, bool) {
	switch t.Trigger.Kind {
	case KindOnce:
		at, err := time.Parse(time.RFC3339, t.Trigger.At)
		return at, err == nil
	case KindInterval:
		if t.Trigger.EverySeconds <= 0 {
			return time.Time{}, false
		}
		return base.Add(time.Duration(t.Trigger.EverySeconds) * time.Second), true
	case KindCron:
		c, err := ParseCron(t.Trigger.Cron)
		if err != nil {
			return time.Time{}, false
		}
		return c.Next(base, t.Location())
	}
	return time.Time{}, false
}

// Cooldown is the least time between two runs of a file trigger.
func (t Task) Cooldown() time.Duration {
	if t.Trigger.CooldownSeconds > 0 {
		return time.Duration(t.Trigger.CooldownSeconds) * time.Second
	}
	return DefaultCooldown
}

// TrustHash covers everything that decides what a run does and when: an edit from another machine that changes it has
// to be allowed on the machine that runs it. Pausing, renaming and the bookkeeping fields are not in it.
func (t Task) TrustHash() string {
	b, _ := json.Marshal([]any{t.Prompt, t.Trigger, t.Cwd, t.Model, t.Effort, t.Mode, t.Engine, t.Worktree, t.SessionID, t.DeviceID})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

var everyPattern = regexp.MustCompile(`^(\d+)\s*(s|sec|secs|m|min|mins|h|hr|hrs|d|day|days)$`)

// ParseEvery reads an interval as people write it ("90s", "5m", "2h", "1d").
func ParseEvery(s string) (time.Duration, bool) {
	m := everyPattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(s)))
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 32)
	if err != nil || n <= 0 {
		return 0, false
	}
	unit := time.Second
	switch m[2][0] {
	case 'm':
		unit = time.Minute
	case 'h':
		unit = time.Hour
	case 'd':
		unit = 24 * time.Hour
	}
	return time.Duration(n) * unit, true
}
