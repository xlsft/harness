package tools

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Background task limits.
const (
	// TaskOutputInterval throttles TaskOutput events.
	TaskOutputInterval = 500 * time.Millisecond
	// TaskOutputTail is the most text one TaskOutput event carries.
	TaskOutputTail = 4096
	// UnattendedTaskLimit is the default lifetime of a background command in
	// unattended (headless) sessions; a larger timeout may be requested up
	// to UnattendedTaskMax.
	UnattendedTaskLimit = 30 * time.Minute
	UnattendedTaskMax   = 2 * time.Hour
)

// TaskFinishReason says why a background task ended.
type TaskFinishReason string

// Finish reasons.
const (
	TaskExited        TaskFinishReason = "exited"
	TaskStopped       TaskFinishReason = "stopped"
	TaskTimeLimit     TaskFinishReason = "time_limit"
	TaskSessionClosed TaskFinishReason = "session_closed"
)

// TaskInfo describes a background task.
type TaskInfo struct {
	ID          string
	ToolUseID   string
	Command     string
	Description string
	OutputPath  string
	StartedAt   time.Time
	Running     bool
	ExitCode    int
	Reason      TaskFinishReason
	Duration    time.Duration
	// Monitor marks the entry of a monitor watching another task: no process of its own, its output is the matches.
	Monitor bool
}

// TaskEventKind enumerates task events.
type TaskEventKind int

// Task event kinds.
const (
	TaskEventStarted TaskEventKind = iota + 1
	TaskEventOutput
	TaskEventFinished
	// TaskEventNotify asks for a reminder and wakes an idle session (Reminder, Vars).
	TaskEventNotify
)

// TaskEvent is emitted by the TaskManager.
type TaskEvent struct {
	Kind TaskEventKind
	Task TaskInfo
	// Text is the new output (TaskEventOutput).
	Text string
	// Reminder and Vars are the reminder to queue (TaskEventNotify).
	Reminder string
	Vars     map[string]string
}

// TaskManager runs shell commands in the background, streaming their output
// to files in the session scratch directory.
type TaskManager struct {
	// Dir receives <task-id>.output files.
	Dir string
	// Unattended applies the background time limit (headless sessions).
	Unattended bool
	// OnEvent receives task events (may be nil).
	OnEvent func(TaskEvent)

	mu    sync.Mutex
	tasks map[string]*task
}

type task struct {
	info TaskInfo
	// kill ends the task: its process group, or a monitor's loop.
	kill    func()
	done    chan struct{}
	exited  chan struct{}
	reason  TaskFinishReason
	cleanup func()
	// readOff is where the next TaskOutput read starts.
	readOff int64
	// reported is set once a tool result told the model the task finished.
	reported bool
	// watchers are the monitors reading this task's output; their last matches go out before it counts as finished.
	watchers []*monitor
}

// NewTaskManager creates a manager writing output files to dir.
func NewTaskManager(dir string) *TaskManager {
	return &TaskManager{Dir: dir, tasks: map[string]*task{}}
}

func newTaskID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "b" + hex.EncodeToString(b)
}

func (m *TaskManager) emit(e TaskEvent) {
	if m.OnEvent != nil {
		m.OnEvent(e)
	}
}

func (m *TaskManager) outputFile(id string) (*os.File, string, error) {
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return nil, "", err
	}
	p := filepath.Join(m.Dir, id+".output")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	return f, p, err
}

// Start runs command in the background through the session shell (its cwd
// and snapshot; cwd changes made by the command do not persist).
func (m *TaskManager) Start(sh *Shell, command, description, toolUseID string, timeout time.Duration) (TaskInfo, error) {
	id := newTaskID()
	f, path, err := m.outputFile(id)
	if err != nil {
		return TaskInfo{}, err
	}
	cmd := sh.command(command, "", sh.Cwd())
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		f.Close()
		return TaskInfo{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return m.adopt(id, path, f, cmd, done, command, description, toolUseID, timeout, nil), nil
}

// adopt takes over a running command whose output now goes to path.
func (m *TaskManager) adopt(id, path string, f *os.File, cmd *exec.Cmd, done <-chan error, command, description, toolUseID string, timeout time.Duration, cleanup func()) TaskInfo {
	info := TaskInfo{ID: id, ToolUseID: toolUseID, Command: command, Description: description, OutputPath: path}
	return m.track(info, f, func() { _ = killProcessGroup(cmd) }, done, timeout, cleanup)
}

// track registers a running task and follows it until done delivers.
func (m *TaskManager) track(info TaskInfo, f *os.File, kill func(), done <-chan error, timeout time.Duration, cleanup func()) TaskInfo {
	info.StartedAt, info.Running = time.Now(), true
	t := &task{
		info:    info,
		kill:    kill,
		done:    make(chan struct{}),
		exited:  make(chan struct{}),
		cleanup: cleanup,
	}
	m.mu.Lock()
	if m.tasks == nil {
		m.tasks = map[string]*task{}
	}
	m.tasks[info.ID] = t
	m.mu.Unlock()
	m.emit(TaskEvent{Kind: TaskEventStarted, Task: t.info})

	var limit <-chan time.Time
	if m.Unattended {
		d := UnattendedTaskLimit
		if timeout > d {
			d = min(timeout, UnattendedTaskMax)
		}
		timer := time.NewTimer(d)
		limit = timer.C
		defer func() {
			go func() { <-t.done; timer.Stop() }()
		}()
	}
	tailDone := make(chan struct{})
	go func() { m.tail(t); close(tailDone) }()
	go func() {
		var err error
		select {
		case err = <-done:
		case <-limit:
			m.mu.Lock()
			if t.reason == "" {
				t.reason = TaskTimeLimit
			}
			m.mu.Unlock()
			kill()
			err = <-done
		}
		if f != nil {
			f.Close()
		}
		close(t.exited)
		<-tailDone
		m.settleWatchers(t)
		if t.cleanup != nil {
			t.cleanup()
		}
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
			code = -1
		}
		m.mu.Lock()
		t.info.Running = false
		t.info.ExitCode = code
		if t.reason == "" {
			t.reason = TaskExited
		}
		t.info.Reason = t.reason
		t.info.Duration = time.Since(t.info.StartedAt)
		info := t.info
		m.mu.Unlock()
		m.emit(TaskEvent{Kind: TaskEventFinished, Task: info})
		close(t.done)
	}()
	return t.info
}

// tail emits throttled TaskOutput events with new output.
func (m *TaskManager) tail(t *task) {
	f, err := os.Open(t.info.OutputPath)
	if err != nil {
		return
	}
	defer f.Close()
	tick := time.NewTicker(TaskOutputInterval)
	defer tick.Stop()
	var off int64
	flush := func() {
		st, err := f.Stat()
		if err != nil || st.Size() <= off {
			return
		}
		start := off
		if st.Size()-off > TaskOutputTail {
			start = st.Size() - TaskOutputTail
		}
		buf := make([]byte, st.Size()-start)
		n, _ := f.ReadAt(buf, start)
		off = start + int64(n)
		if n > 0 {
			m.emit(TaskEvent{Kind: TaskEventOutput, Task: t.Info(m), Text: string(buf[:n])})
		}
	}
	for {
		select {
		case <-t.exited:
			flush()
			return
		case <-tick.C:
			flush()
		}
	}
}

// Info returns a snapshot of the task.
func (t *task) Info(m *TaskManager) TaskInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return t.info
}

// Get returns a task by id.
func (m *TaskManager) Get(id string) (TaskInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return TaskInfo{}, false
	}
	return t.info, true
}

// List returns all tasks, oldest first.
func (m *TaskManager) List() []TaskInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TaskInfo, 0, len(m.tasks))
	for _, t := range m.tasks {
		out = append(out, t.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// Running reports how many tasks are still running.
func (m *TaskManager) Running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, t := range m.tasks {
		if t.info.Running {
			n++
		}
	}
	return n
}

// Stop kills a running task (and its process group) and waits for it.
func (m *TaskManager) Stop(id string) (TaskInfo, error) {
	return m.stop(id, TaskStopped)
}

func (m *TaskManager) stop(id string, reason TaskFinishReason) (TaskInfo, error) {
	m.mu.Lock()
	t, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return TaskInfo{}, fmt.Errorf("no background task with id %s", id)
	}
	if !t.info.Running {
		info := t.info
		m.mu.Unlock()
		return info, fmt.Errorf("task %s is not running (exit code %d)", id, info.ExitCode)
	}
	if t.reason == "" {
		t.reason = reason
	}
	m.mu.Unlock()
	t.kill()
	select {
	case <-t.done:
	case <-time.After(5 * time.Second):
	}
	info, _ := m.Get(id)
	return info, nil
}

// StopAll kills every running task (session end).
func (m *TaskManager) StopAll(reason TaskFinishReason) {
	m.mu.Lock()
	var ids []string
	for id, t := range m.tasks {
		if t.info.Running {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, _ = m.stop(id, reason)
		}(id)
	}
	wg.Wait()
}

// Done returns a channel that is closed once the task has finished and its
// final state is recorded.
func (m *TaskManager) Done(id string) (<-chan struct{}, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return nil, false
	}
	return t.done, true
}

// ReadOffset returns where the next TaskOutput read of a task starts.
func (m *TaskManager) ReadOffset(id string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[id]; ok {
		return t.readOff
	}
	return 0
}

// SetReadOffset moves a task's read position.
func (m *TaskManager) SetReadOffset(id string, off int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[id]; ok {
		t.readOff = off
	}
}

// MarkReported records that a tool result already told the model the task
// finished, so the completion reminder can be skipped.
func (m *TaskManager) MarkReported(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[id]; ok && !t.info.Running {
		t.reported = true
	}
}

// Reported reports whether the model already learned that the task finished.
func (m *TaskManager) Reported(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	return ok && t.reported
}

// TaskStatus describes how a task stands, for the model: "running",
// "exited with code 3", "killed (stopped)" and so on.
func TaskStatus(info TaskInfo) string {
	switch {
	case info.Running:
		return "running"
	case info.Reason == TaskStopped:
		return "killed (stopped with TaskStop or by the user)"
	case info.Reason == TaskTimeLimit:
		return "killed (background time limit reached)"
	case info.Reason == TaskSessionClosed:
		return "killed (session closed)"
	case info.ExitCode < 0:
		return "exited abnormally (killed by a signal)"
	default:
		return fmt.Sprintf("exited with code %d", info.ExitCode)
	}
}

// TaskState is the short machine form of TaskStatus: running, exited or
// killed.
func TaskState(info TaskInfo) string {
	switch {
	case info.Running:
		return "running"
	case info.Reason == TaskExited && info.ExitCode >= 0:
		return "exited"
	default:
		return "killed"
	}
}

// TailLines returns the last n lines of s, at most maxBytes long.
func TailLines(s string, n, maxBytes int) string {
	s = strings.TrimRight(s, "\n")
	if len(s) > maxBytes {
		s = s[len(s)-maxBytes:]
		s = trimRuneStart(s)
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// trimRuneStart drops the bytes of a UTF-8 sequence cut at the start of s.
func trimRuneStart(s string) string {
	for i := 0; i < 4 && len(s) > 0 && !utf8.RuneStart(s[0]); i++ {
		s = s[1:]
	}
	return s
}

// ReadTail returns up to n bytes from the end of a task's output file.
func ReadTail(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	start := max(0, st.Size()-n)
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	b, _ := io.ReadAll(f)
	return string(b)
}
