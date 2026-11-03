package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

// TaskOutput and Wait limits.
const (
	// TaskOutputDefaultWait is how long a blocking TaskOutput call waits
	// when no timeout is given.
	TaskOutputDefaultWait = 30 * time.Second
	// WaitMax caps the timeout of TaskOutput and Wait.
	WaitMax = 10 * time.Minute
	// TaskOutputMaxChars is the most output one TaskOutput result carries;
	// longer output keeps its end.
	TaskOutputMaxChars = BashMaxInlineChars
	// taskPollInterval is how often task output files are re-read while
	// waiting.
	taskPollInterval = 100 * time.Millisecond
	// maxScanLine bounds a line held for pattern matching.
	maxScanLine = 64 << 10
)

// outputScan reads a task's output file from an offset, keeps the end of
// what it read and optionally stops after the first line matching a pattern.
// Only complete lines are matched until the task has finished; then the last
// unterminated line is matched too.
type outputScan struct {
	path string
	re   *regexp.Regexp
	// keep bounds the kept text (0 keeps nothing).
	keep int

	start   int64
	off     int64
	partial []byte
	kept    []byte
	dropped int64

	matched bool
	line    string
}

func newOutputScan(path string, from int64, re *regexp.Regexp, keep int) *outputScan {
	return &outputScan{path: path, re: re, keep: keep, start: from, off: from}
}

// feed reads new output up to the end of the file, or up to the end of the
// first matching line. final also matches an unterminated last line.
func (s *outputScan) feed(final bool) {
	if s.matched {
		return
	}
	f, err := os.Open(s.path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(s.off, io.SeekStart); err != nil {
		return
	}
	buf := make([]byte, 64<<10)
	for !s.matched {
		n, err := f.Read(buf)
		if n > 0 {
			s.consume(buf[:n])
		}
		if err != nil || n == 0 {
			break
		}
	}
	if final && !s.matched && len(s.partial) > 0 && s.re != nil && s.re.Match(s.partial) {
		s.matched, s.line = true, string(s.partial)
	}
}

func (s *outputScan) consume(b []byte) {
	if s.re == nil {
		s.off += int64(len(b))
		s.store(b)
		return
	}
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			s.partial = append(s.partial, b...)
			if len(s.partial) > maxScanLine {
				s.partial = s.partial[len(s.partial)-maxScanLine:]
			}
			s.off += int64(len(b))
			s.store(b)
			return
		}
		seg := b[:i+1]
		line := append(s.partial, b[:i]...)
		s.partial = s.partial[:0]
		s.off += int64(len(seg))
		s.store(seg)
		b = b[i+1:]
		if s.re.Match(line) {
			s.matched, s.line = true, strings.TrimRight(string(line), "\r")
			return
		}
	}
}

func (s *outputScan) store(b []byte) {
	if s.keep <= 0 {
		s.dropped += int64(len(b))
		return
	}
	s.kept = append(s.kept, b...)
	if len(s.kept) > 2*s.keep {
		cut := len(s.kept) - s.keep
		s.dropped += int64(cut)
		s.kept = append([]byte(nil), s.kept[cut:]...)
	}
}

// text returns the kept output and how many earlier bytes were dropped.
func (s *outputScan) text() (string, int64) {
	b, dropped := s.kept, s.dropped
	if len(b) > s.keep {
		cut := len(b) - s.keep
		// Start at a line boundary when one is near.
		if i := bytes.IndexByte(b[cut:], '\n'); i >= 0 && i < 1024 {
			cut += i + 1
		}
		dropped += int64(cut)
		b = b[cut:]
	}
	out := string(b)
	if dropped > 0 {
		trimmed := trimRuneStart(out)
		dropped += int64(len(out) - len(trimmed))
		out = trimmed
	}
	return out, dropped
}

// taskWait is the outcome of watching a task.
type taskWait struct {
	info     TaskInfo
	matched  bool
	line     string
	finished bool
	timedOut bool
	elapsed  time.Duration
}

// watchTask feeds scan until the pattern matches (when scan has one), the
// task finishes (always ends the wait), the timeout passes or ctx ends.
// A zero timeout reads once without waiting.
func watchTask(ctx context.Context, m *TaskManager, id string, scan *outputScan, timeout time.Duration) (taskWait, error) {
	begin := time.Now()
	done, ok := m.Done(id)
	if !ok {
		return taskWait{}, fmt.Errorf("no background task with id %s", id)
	}
	var deadline <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		deadline = timer.C
	}
	tick := time.NewTicker(taskPollInterval)
	defer tick.Stop()
	res := func(finished, timedOut bool) taskWait {
		info, _ := m.Get(id)
		return taskWait{info: info, matched: scan.matched, line: scan.line, finished: finished, timedOut: timedOut, elapsed: time.Since(begin)}
	}
	for {
		select {
		case <-done:
			scan.feed(true)
			return res(true, false), nil
		default:
		}
		scan.feed(false)
		if scan.matched || timeout <= 0 {
			return res(false, false), nil
		}
		select {
		case <-ctx.Done():
			return res(false, false), ctx.Err()
		case <-deadline:
			scan.feed(false)
			return res(false, !scan.matched), nil
		case <-done:
		case <-tick.C:
		}
	}
}

// compilePattern compiles a TaskOutput / Wait regex.
func compilePattern(p string) (*regexp.Regexp, error) {
	if p == "" {
		return nil, nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, fmt.Errorf("InputValidationError: invalid pattern: %v", err)
	}
	return re, nil
}

// waitTimeout converts a timeout in milliseconds, applying the default and
// the cap.
func waitTimeout(ms *int64, def time.Duration) time.Duration {
	d := def
	if ms != nil {
		d = time.Duration(*ms) * time.Millisecond
	}
	if d > WaitMax {
		d = WaitMax
	}
	if d < 0 {
		d = 0
	}
	return d
}

func seconds(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// ---- TaskOutput ----

// TaskOutput reads a background task's new output, optionally waiting for
// it to finish or for a line matching a pattern.
type TaskOutput struct{}

func (*TaskOutput) Name() string                  { return "TaskOutput" }
func (*TaskOutput) ReadOnly(json.RawMessage) bool { return true }

func (*TaskOutput) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		TaskID  string `json:"task_id"`
		Offset  *int64 `json:"offset"`
		Block   bool   `json:"block"`
		Pattern string `json:"pattern"`
		Timeout *int64 `json:"timeout"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.TaskID == "" {
		return ErrorResult("InputValidationError: task_id is required")
	}
	if in.Offset != nil && *in.Offset < 0 {
		return ErrorResult("InputValidationError: offset must not be negative")
	}
	re, err := compilePattern(in.Pattern)
	if err != nil {
		return ErrorResult("%v", err)
	}
	if env.Tasks == nil {
		return ErrorResult("no background tasks in this session")
	}
	info, ok := env.Tasks.Get(in.TaskID)
	if !ok {
		return ErrorResult("no background task with id %s", in.TaskID)
	}
	from := env.Tasks.ReadOffset(in.TaskID)
	if in.Offset != nil {
		from = *in.Offset
	}
	if st, err := os.Stat(info.OutputPath); err == nil && from > st.Size() {
		from = st.Size()
	}
	waiting := in.Block || re != nil
	timeout := time.Duration(0)
	if waiting {
		timeout = waitTimeout(in.Timeout, TaskOutputDefaultWait)
	}
	scan := newOutputScan(info.OutputPath, from, re, TaskOutputMaxChars)
	w, err := watchTask(ctx, env.Tasks, in.TaskID, scan, timeout)
	if err != nil {
		return ErrorResult("%v", err)
	}
	env.Tasks.SetReadOffset(in.TaskID, scan.off)
	if !w.info.Running {
		env.Tasks.MarkReported(in.TaskID)
	}
	out, dropped := scan.text()

	var sb strings.Builder
	fmt.Fprintf(&sb, "Task %s: %s\n", w.info.ID, TaskStatus(w.info))
	met := false
	if waiting {
		switch {
		case w.matched:
			met = true
			fmt.Fprintf(&sb, "Matched after %s: %s\n", seconds(w.elapsed), w.line)
		case w.finished && re != nil:
			fmt.Fprintf(&sb, "The task finished after %s without a line matching the pattern.\n", seconds(w.elapsed))
		case w.finished || !w.info.Running:
			met = re == nil
			fmt.Fprintf(&sb, "Finished after %s of waiting.\n", seconds(w.elapsed))
		case w.timedOut:
			fmt.Fprintf(&sb, "Timed out after %s; the task is still running.\n", seconds(w.elapsed))
		}
	}
	if out == "" {
		fmt.Fprintf(&sb, "No new output since byte %d.\n", from)
	} else {
		fmt.Fprintf(&sb, "Output from byte %d to %d:\n", from, scan.off)
		if dropped > 0 {
			fmt.Fprintf(&sb, "[%d earlier bytes not shown; the full output is in %s]\n", dropped, info.OutputPath)
		}
		sb.WriteString(strings.TrimRight(out, "\n"))
		sb.WriteString("\n")
	}
	if w.info.Running {
		fmt.Fprintf(&sb, "The next TaskOutput call continues from byte %d.", scan.off)
	}
	structured := map[string]any{
		"task_id": w.info.ID, "command": w.info.Command, "status": TaskState(w.info), "reason": string(w.info.Reason),
		"stdout": out, "offset": from, "next_offset": scan.off, "truncated": dropped > 0, "output_file": info.OutputPath,
		"waited": waiting, "condition_met": met, "timed_out": w.timedOut, "elapsed_ms": w.elapsed.Milliseconds(),
	}
	if !w.info.Running {
		structured["exit_code"] = w.info.ExitCode
	}
	if w.matched {
		structured["matched_line"] = w.line
	}
	r := TextResult(strings.TrimRight(sb.String(), "\n"), structured)
	r.Summary = fmt.Sprintf("%s: %s", w.info.ID, TaskState(w.info))
	return r
}
