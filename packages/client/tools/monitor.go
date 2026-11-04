package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Monitor limits.
const (
	// MonitorDefaultTimeout is how long a monitor watches when no timeout is given.
	MonitorDefaultTimeout = time.Hour
	// MonitorMaxTimeout caps the timeout.
	MonitorMaxTimeout = 24 * time.Hour
	// monitorMaxLines and monitorMaxLineChars bound what one notification carries.
	monitorMaxLines     = 20
	monitorMaxLineChars = 500
	// monitorSettleWait bounds how long a finishing task waits for the last notices of its monitors.
	monitorSettleWait = 2 * time.Second
)

// monitorPace is how a monitor reads and throttles. Variables for the tests.
var monitorPace = struct {
	// poll is how often the output is read.
	poll time.Duration
	// batch collects the matches of a burst into one notification.
	batch time.Duration
	// minGap is the least time between two notifications of one monitor.
	minGap time.Duration
	// noisyLines is the number of matching lines within a minute that stops a monitor.
	noisyLines int
	// noisyNotices is the number of notifications within ten minutes that stops a monitor.
	noisyNotices int
}{poll: 250 * time.Millisecond, batch: time.Second, minGap: 5 * time.Second, noisyLines: 300, noisyNotices: 40}

var ansiEscapeRe = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// monitor watches the output of one task and reports matching lines through TaskEventNotify.
type monitor struct {
	m      *TaskManager
	target *task
	scan   *outputScan
	// entry is the id the model knows the monitor by: its own entry in the task list, or with owned the task itself.
	entry       string
	pattern     string
	description string
	persistent  bool
	timeout     time.Duration
	// owned: the monitor started the target's command, which ends with the monitor.
	owned bool
	// out receives the matches (the monitor's own output; nil when owned).
	out *os.File
	// stop ends the monitor on request; finished delivers to the tracking goroutine of its entry.
	stop     chan struct{}
	stopOnce sync.Once
	finished chan error
	// settled is closed once the last matches went out.
	settled chan struct{}

	pending     []string
	unlisted    int
	lastNotice  time.Time
	lineWindow  time.Time
	lineCount   int
	noticeSince time.Time
	noticeCount int
	noisyReason string
}

// settleWatchers lets the monitors of a task that ended send their last matches, so those come before its report.
func (m *TaskManager) settleWatchers(t *task) {
	m.mu.Lock()
	ws := append([]*monitor(nil), t.watchers...)
	m.mu.Unlock()
	limit := time.After(monitorSettleWait)
	for _, w := range ws {
		select {
		case <-w.settled:
		case <-limit:
			return
		}
	}
}

// watchTask starts a monitor on a running task. An owned monitor has no entry of its own: stopping the task ends it.
// Otherwise it gets one, with the matches as its output, and stopping that ends the monitor alone. Matching starts where
// TaskOutput would read next, so a line the model has not seen yet is not missed.
func (m *TaskManager) watchTask(w *monitor, re *regexp.Regexp, targetID, toolUseID string) (TaskInfo, error) {
	w.m, w.stop, w.finished, w.settled = m, make(chan struct{}), make(chan error, 1), make(chan struct{})
	m.mu.Lock()
	t, ok := m.tasks[targetID]
	if !ok {
		m.mu.Unlock()
		return TaskInfo{}, fmt.Errorf("no background task with id %s", targetID)
	}
	if t.info.Monitor {
		m.mu.Unlock()
		return TaskInfo{}, fmt.Errorf("task %s is itself a monitor", targetID)
	}
	if !t.info.Running {
		status := TaskStatus(t.info)
		m.mu.Unlock()
		return TaskInfo{}, fmt.Errorf("task %s has already finished (%s); read its output with TaskOutput", targetID, status)
	}
	w.target = t
	w.scan = newOutputScan(t.info.OutputPath, t.readOff, re, 0)
	info := t.info
	m.mu.Unlock()

	if w.owned {
		w.entry = targetID
	} else {
		id := newTaskID()
		f, path, err := m.outputFile(id)
		if err != nil {
			return TaskInfo{}, err
		}
		w.entry, w.out = id, f
		info = m.track(TaskInfo{ID: id, ToolUseID: toolUseID, Command: fmt.Sprintf("monitor %s /%s/", targetID, w.pattern), Description: w.description, OutputPath: path, Monitor: true},
			f, func() { w.stopOnce.Do(func() { close(w.stop) }) }, w.finished, w.timeout, nil)
	}
	m.mu.Lock()
	t.watchers = append(t.watchers, w)
	m.mu.Unlock()
	go w.run()
	return info, nil
}

// How a monitor ends.
type monitorEnd int

const (
	monitorEndStopped monitorEnd = iota + 1 // TaskStop, the user or the session closing its entry
	monitorEndTarget                        // the watched task ended: that has its own report
	monitorEndOnce                          // the first match of a one-shot monitor
	monitorEndTimeout
	monitorEndNoisy
)

func (w *monitor) run() {
	poll := time.NewTicker(monitorPace.poll)
	defer poll.Stop()
	deadline := time.NewTimer(w.timeout)
	defer deadline.Stop()
	var flush *time.Timer
	var flushC <-chan time.Time
	defer func() {
		if flush != nil {
			flush.Stop()
		}
	}()
	var end monitorEnd
	for end == 0 {
		select {
		case <-w.stop:
			end = monitorEndStopped
		case <-w.target.exited:
			w.drain(true)
			end = monitorEndTarget
		case <-deadline.C:
			w.drain(false)
			end = monitorEndTimeout
		case <-flushC:
			flushC = nil
			w.notify("This monitor keeps watching; TaskStop with its task id ends it.")
		case <-poll.C:
			w.drain(false)
		}
		switch {
		case end != 0:
		case w.noisyReason != "":
			end = monitorEndNoisy
		case !w.persistent && len(w.pending) > 0:
			end = monitorEndOnce
		case flushC == nil && len(w.pending) > 0:
			flush = time.NewTimer(max(monitorPace.batch, monitorPace.minGap-time.Since(w.lastNotice)))
			flushC = flush.C
		}
	}
	w.finish(end)
}

// drain matches the output written since the last read. final also takes an unterminated last line.
func (w *monitor) drain(final bool) {
	for {
		w.scan.matched = false
		w.scan.feed(final)
		if !w.scan.matched {
			return
		}
		w.add(w.scan.line)
		if len(w.scan.partial) > 0 {
			w.scan.partial = nil // that was the unterminated line
			return
		}
		if !w.persistent {
			return
		}
	}
}

func (w *monitor) add(line string) {
	line = ansiEscapeRe.ReplaceAllString(line, "")
	if w.out != nil {
		fmt.Fprintln(w.out, line)
	}
	if len(w.pending) < monitorMaxLines {
		if len(line) > monitorMaxLineChars {
			line = truncateUTF8(line, monitorMaxLineChars) + "…"
		}
		w.pending = append(w.pending, line)
	} else {
		w.unlisted++
	}
	now := time.Now()
	if now.Sub(w.lineWindow) > time.Minute {
		w.lineWindow, w.lineCount = now, 0
	}
	w.lineCount++
	if w.lineCount > monitorPace.noisyLines {
		w.noisyReason = fmt.Sprintf("more than %d lines matched within a minute", monitorPace.noisyLines)
	}
}

// notify sends the waiting matches, with status saying how the monitor stands.
func (w *monitor) notify(status string) {
	if len(w.pending) == 0 {
		return
	}
	lines := strings.Join(w.pending, "\n")
	count := len(w.pending) + w.unlisted
	if w.unlisted > 0 {
		lines += fmt.Sprintf("\n… and %d more", w.unlisted)
	}
	w.pending, w.unlisted = nil, 0
	now := time.Now()
	w.lastNotice = now
	if now.Sub(w.noticeSince) > 10*time.Minute {
		w.noticeSince, w.noticeCount = now, 0
	}
	w.noticeCount++
	if w.noticeCount > monitorPace.noisyNotices && w.noisyReason == "" {
		w.noisyReason = fmt.Sprintf("more than %d notifications within ten minutes", monitorPace.noisyNotices)
	}
	w.m.emit(TaskEvent{Kind: TaskEventNotify, Reminder: "monitor_match", Vars: map[string]string{
		"task_id": w.entry, "description": w.description, "pattern": w.pattern, "count": strconv.Itoa(count), "lines": lines, "status": status,
	}})
}

func (w *monitor) stopped(reason string) {
	w.m.emit(TaskEvent{Kind: TaskEventNotify, Reminder: "monitor_stopped", Vars: map[string]string{
		"task_id": w.entry, "description": w.description, "pattern": w.pattern, "reason": reason,
	}})
}

func (w *monitor) finish(end monitorEnd) {
	ownedNote := ""
	if w.owned {
		ownedNote = " The command it started was stopped."
	}
	switch end {
	case monitorEndStopped:
		w.pending = nil
		if w.m.finishReason(w.entry) == TaskTimeLimit {
			w.stopped("the background time limit of this session was reached.")
		}
	case monitorEndTarget:
		if r := w.m.finishReason(w.target.info.ID); r == TaskStopped || r == TaskSessionClosed {
			w.pending = nil
		}
		w.notify("The watched task has ended, and this monitor with it.")
	case monitorEndOnce:
		w.notify("This was the first match, so this monitor has ended." + ownedNote)
	case monitorEndTimeout:
		w.notify("This monitor is ending: its time is up.")
		w.stopped(fmt.Sprintf("its timeout of %s elapsed.", formatDuration(w.timeout)) + ownedNote)
	case monitorEndNoisy:
		w.notify("This monitor is ending: it matches too often.")
		w.stopped(w.noisyReason + ". Start another with a narrower pattern if you still need one." + ownedNote)
	}
	close(w.settled)
	if w.owned && end != monitorEndTarget {
		w.m.stopSilently(w.target.info.ID)
	}
	w.finished <- nil
}

// finishReason returns why a task is ending ("" when nothing asked it to).
func (m *TaskManager) finishReason(id string) TaskFinishReason {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[id]; ok {
		return t.reason
	}
	return ""
}

// stopSilently stops a task whose end the model was already told about, so no finish report follows.
func (m *TaskManager) stopSilently(id string) {
	m.mu.Lock()
	if t, ok := m.tasks[id]; ok {
		t.reported = true
	}
	m.mu.Unlock()
	_, _ = m.stop(id, TaskStopped)
}

// Monitor watches a background task, or a command it starts itself, and notifies the session of output lines matching
// a pattern. It returns at once: matches arrive as reminders and wake an idle session.
type Monitor struct{}

type monitorInput struct {
	TaskID      string   `json:"task_id"`
	Command     string   `json:"command"`
	Pattern     string   `json:"pattern"`
	Description string   `json:"description"`
	Timeout     *float64 `json:"timeout"`
	Persistent  bool     `json:"persistent"`
}

func (*Monitor) Name() string { return "Monitor" }

// ReadOnly reports a monitor on a task that already runs; one that starts a command is checked like Bash.
func (m *Monitor) ReadOnly(input json.RawMessage) bool { return m.ShellCommand(input) == "" }

// ShellCommand implements ShellCommander.
func (*Monitor) ShellCommand(input json.RawMessage) string {
	var in monitorInput
	_ = json.Unmarshal(input, &in)
	return in.Command
}

func (*Monitor) Run(ctx context.Context, env *Env, call Call) Result {
	var in monitorInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if (in.TaskID == "") == (strings.TrimSpace(in.Command) == "") {
		return ErrorResult("InputValidationError: pass either task_id or command")
	}
	if in.Pattern == "" {
		return ErrorResult("InputValidationError: pattern is required")
	}
	if strings.TrimSpace(in.Description) == "" {
		return ErrorResult("InputValidationError: description is required")
	}
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return ErrorResult("InputValidationError: invalid pattern: %v", err)
	}
	if env.Tasks == nil {
		return ErrorResult("Background tasks are not available in this session.")
	}
	timeout := MonitorDefaultTimeout
	if in.Timeout != nil && *in.Timeout > 0 {
		timeout = min(time.Duration(*in.Timeout*float64(time.Millisecond)), MonitorMaxTimeout)
	}
	w := &monitor{pattern: in.Pattern, description: in.Description, persistent: in.Persistent, timeout: timeout, owned: in.Command != ""}
	target := in.TaskID
	if w.owned {
		info, err := env.Tasks.Start(env.Shell, in.Command, in.Description, call.ID, timeout)
		if err != nil {
			return ErrorResult("failed to start command: %v", err)
		}
		target = info.ID
	}
	info, err := env.Tasks.watchTask(w, re, target, call.ID)
	if err != nil {
		return ErrorResult("%v", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Monitoring started as task %s, for lines matching /%s/, for up to %s. ", info.ID, in.Pattern, formatDuration(timeout))
	if in.Persistent {
		b.WriteString("Matches reach you as notifications, in batches, until it ends. ")
	} else {
		b.WriteString("The first match reaches you as a notification and ends it. ")
	}
	if w.owned {
		b.WriteString("The command runs as that task and stops when the monitoring ends. ")
	}
	fmt.Fprintf(&b, "Do not poll: go on with other work or end your turn. TaskStop with %s ends it early.", info.ID)
	r := TextResult(b.String(), map[string]any{"task_id": info.ID, "pattern": in.Pattern, "persistent": in.Persistent, "timeout_ms": timeout.Milliseconds(), "output_path": info.OutputPath})
	r.Summary = "Monitoring as task " + info.ID
	return r
}
