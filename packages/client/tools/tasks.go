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
	"sync"
	"time"
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
}

// TaskEventKind enumerates task events.
type TaskEventKind int

// Task event kinds.
const (
	TaskEventStarted TaskEventKind = iota + 1
	TaskEventOutput
	TaskEventFinished
)

// TaskEvent is emitted by the TaskManager.
type TaskEvent struct {
	Kind TaskEventKind
	Task TaskInfo
	// Text is the new output (TaskEventOutput).
	Text string
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
	info    TaskInfo
	cmd     *exec.Cmd
	done    chan struct{}
	exited  chan struct{}
	reason  TaskFinishReason
	cleanup func()
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
	t := &task{
		info:    TaskInfo{ID: id, ToolUseID: toolUseID, Command: command, Description: description, OutputPath: path, StartedAt: time.Now(), Running: true},
		cmd:     cmd,
		done:    make(chan struct{}),
		exited:  make(chan struct{}),
		cleanup: cleanup,
	}
	m.mu.Lock()
	if m.tasks == nil {
		m.tasks = map[string]*task{}
	}
	m.tasks[id] = t
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
			_ = killProcessGroup(cmd)
			err = <-done
		}
		if f != nil {
			f.Close()
		}
		close(t.exited)
		<-tailDone
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
		close(t.done)
		m.emit(TaskEvent{Kind: TaskEventFinished, Task: info})
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
	_ = killProcessGroup(t.cmd)
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
