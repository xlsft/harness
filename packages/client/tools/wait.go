package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Wait polling.
const (
	waitPortInterval = 200 * time.Millisecond
	waitDialTimeout  = time.Second
	waitFileInterval = 100 * time.Millisecond
	// waitFileMax is the largest file Wait reads to match a pattern.
	waitFileMax = 10 << 20
	// waitTailLines / waitTailBytes bound the output tail shown for a
	// finished task.
	waitTailLines = 10
	waitTailBytes = 2000
)

// Wait blocks until a condition holds: a background task finished or
// printed a matching line, a TCP port accepts connections, or a file exists,
// changes or contains a matching line. It only observes.
type Wait struct{}

type waitInput struct {
	TaskID  string `json:"task_id"`
	Port    *int   `json:"port"`
	Host    string `json:"host"`
	Path    string `json:"path"`
	Changed bool   `json:"changed"`
	Pattern string `json:"pattern"`
	Timeout *int64 `json:"timeout"`
}

func (*Wait) Name() string { return "Wait" }

// ReadOnly is true except for a port on a host that is not this machine:
// connecting there is network access, so it goes through the normal
// permission check (a file outside the working directories is checked by
// path in the permission engine, like Read).
func (*Wait) ReadOnly(input json.RawMessage) bool {
	var in waitInput
	if json.Unmarshal(input, &in) != nil {
		return true
	}
	return in.Port == nil || IsLocalHost(in.Host)
}

// IsLocalHost reports whether host names this machine: empty (the default),
// localhost, *.localhost, a loopback or an unspecified address.
func IsLocalHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	a, err := netip.ParseAddr(h)
	return err == nil && (a.IsLoopback() || a.IsUnspecified())
}

func (*Wait) Run(ctx context.Context, env *Env, call Call) Result {
	var in waitInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	n := 0
	for _, set := range []bool{in.TaskID != "", in.Port != nil, in.Path != ""} {
		if set {
			n++
		}
	}
	if n != 1 {
		return ErrorResult("InputValidationError: set exactly one of task_id, port or path")
	}
	if in.Timeout == nil || *in.Timeout <= 0 {
		return ErrorResult("InputValidationError: timeout (milliseconds) is required")
	}
	if in.Pattern != "" && in.Port != nil {
		return ErrorResult("InputValidationError: pattern applies to task_id or path, not port")
	}
	if in.Host != "" && in.Port == nil {
		return ErrorResult("InputValidationError: host applies to port only")
	}
	if in.Changed && in.Path == "" {
		return ErrorResult("InputValidationError: changed applies to path only")
	}
	re, err := compilePattern(in.Pattern)
	if err != nil {
		return ErrorResult("%v", err)
	}
	timeout := waitTimeout(in.Timeout, 0)
	capped := time.Duration(*in.Timeout)*time.Millisecond > WaitMax

	var w waitOutcome
	switch {
	case in.TaskID != "":
		w, err = waitTask(ctx, env, in.TaskID, re, timeout)
	case in.Port != nil:
		w, err = waitPort(ctx, in.Host, *in.Port, timeout)
	default:
		w, err = waitFile(ctx, resolvePath(env.Cwd, in.Path), in.Changed, re, timeout)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ErrorResult("Wait was interrupted after %s", seconds(w.elapsed))
		}
		return ErrorResult("%v", err)
	}
	msg := w.message
	if capped {
		msg += fmt.Sprintf("\n(The timeout was capped at %s.)", formatDuration(WaitMax))
	}
	structured := map[string]any{
		"condition": w.kind, "target": w.target, "met": w.met, "timed_out": !w.met,
		"elapsed_ms": w.elapsed.Milliseconds(), "message": w.message,
	}
	for k, v := range w.extra {
		structured[k] = v
	}
	r := TextResult(msg, structured)
	if w.met {
		r.Summary = fmt.Sprintf("%s %s · %s", w.kind, w.target, seconds(w.elapsed))
	} else {
		r.Summary = fmt.Sprintf("Timed out on %s %s", w.kind, w.target)
	}
	return r
}

type waitOutcome struct {
	kind    string
	target  string
	met     bool
	elapsed time.Duration
	message string
	extra   map[string]any
}

func waitTask(ctx context.Context, env *Env, id string, re *regexp.Regexp, timeout time.Duration) (waitOutcome, error) {
	w := waitOutcome{kind: "task", target: id, extra: map[string]any{}}
	if env.Tasks == nil {
		return w, fmt.Errorf("no background tasks in this session")
	}
	info, ok := env.Tasks.Get(id)
	if !ok {
		return w, fmt.Errorf("no background task with id %s", id)
	}
	// The whole output is searched, including lines printed before the
	// call; the TaskOutput read position is left alone.
	scan := newOutputScan(info.OutputPath, 0, re, 0)
	tw, err := watchTask(ctx, env.Tasks, id, scan, timeout)
	w.elapsed = tw.elapsed
	if err != nil {
		return w, err
	}
	info = tw.info
	w.extra["status"] = TaskState(info)
	if !info.Running {
		w.extra["exit_code"] = info.ExitCode
		env.Tasks.MarkReported(id)
	}
	status := TaskStatus(info)
	switch {
	case tw.matched:
		w.met = true
		w.extra["matched_line"] = tw.line
		w.message = fmt.Sprintf("Task %s printed a matching line after %s: %s\nThe task is %s.", id, seconds(tw.elapsed), tw.line, status)
	case re != nil && !info.Running:
		w.message = fmt.Sprintf("Task %s %s after %s without printing a line matching the pattern.", id, status, seconds(tw.elapsed))
	case !info.Running:
		w.met = true
		w.message = fmt.Sprintf("Task %s %s (waited %s).", id, status, seconds(tw.elapsed))
	default:
		w.message = fmt.Sprintf("Timed out after %s: task %s is still running.", seconds(tw.elapsed), id)
	}
	if !info.Running {
		if tail := TailLines(ReadTail(info.OutputPath, 4*waitTailBytes), waitTailLines, waitTailBytes); tail != "" {
			w.message += "\nLast lines of output:\n" + tail
			w.extra["tail"] = tail
		}
		w.message += "\nFull output: " + info.OutputPath
	}
	return w, nil
}

func waitPort(ctx context.Context, host string, port int, timeout time.Duration) (waitOutcome, error) {
	if host == "" {
		host = "localhost"
	}
	addr := net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
	w := waitOutcome{kind: "port", target: strconv.Itoa(port), extra: map[string]any{"address": addr}}
	if host != "localhost" {
		w.target = addr
	}
	if port < 1 || port > 65535 {
		return w, fmt.Errorf("InputValidationError: port must be between 1 and 65535")
	}
	begin := time.Now()
	deadline := begin.Add(timeout)
	var lastErr error
	for {
		dctx, cancel := context.WithTimeout(ctx, min(waitDialTimeout, max(time.Until(deadline), time.Millisecond)))
		conn, err := (&net.Dialer{}).DialContext(dctx, "tcp", addr)
		cancel()
		if err == nil {
			conn.Close()
			w.met, w.elapsed = true, time.Since(begin)
			w.message = fmt.Sprintf("%s is accepting connections (after %s).", addr, seconds(w.elapsed))
			return w, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			w.elapsed = time.Since(begin)
			return w, ctx.Err()
		}
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		select {
		case <-ctx.Done():
			w.elapsed = time.Since(begin)
			return w, ctx.Err()
		case <-time.After(min(waitPortInterval, left)):
		}
	}
	w.elapsed = time.Since(begin)
	w.message = fmt.Sprintf("Timed out after %s: nothing accepted connections on %s (last error: %v).", seconds(w.elapsed), addr, lastErr)
	return w, nil
}

type fileSnap struct {
	exists bool
	size   int64
	mtime  time.Time
}

func snapFile(path string) fileSnap {
	st, err := os.Stat(path)
	if err != nil {
		return fileSnap{}
	}
	return fileSnap{exists: true, size: st.Size(), mtime: st.ModTime()}
}

// matchFile returns the first line of path matching re.
func matchFile(path string, re *regexp.Regexp) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, waitFileMax))
	for _, line := range bytes.Split(data, []byte("\n")) {
		if re.Match(line) {
			return strings.TrimRight(string(line), "\r"), true
		}
	}
	return "", false
}

func waitFile(ctx context.Context, path string, changed bool, re *regexp.Regexp, timeout time.Duration) (waitOutcome, error) {
	w := waitOutcome{kind: "file", target: path, extra: map[string]any{}}
	begin := time.Now()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(waitFileInterval)
	defer tick.Stop()
	initial := snapFile(path)
	last := fileSnap{size: -1}
	seenChange := false
	for {
		cur := snapFile(path)
		if cur != initial {
			seenChange = true
		}
		if !changed || seenChange {
			switch {
			case re == nil && changed:
				w.met = true
				what := "changed"
				switch {
				case !cur.exists:
					what = "was deleted"
				case !initial.exists:
					what = "was created"
				}
				w.extra["exists"] = cur.exists
				w.message = fmt.Sprintf("%s %s (after %s).", path, what, seconds(time.Since(begin)))
			case re == nil && cur.exists:
				w.met = true
				w.extra["exists"] = true
				w.message = fmt.Sprintf("%s exists (after %s).", path, seconds(time.Since(begin)))
			case re != nil && cur.exists && cur != last:
				last = cur
				if line, ok := matchFile(path, re); ok {
					w.met = true
					w.extra["matched_line"] = line
					w.message = fmt.Sprintf("%s contains a matching line (after %s): %s", path, seconds(time.Since(begin)), line)
				}
			}
			if w.met {
				w.elapsed = time.Since(begin)
				return w, nil
			}
		}
		select {
		case <-ctx.Done():
			w.elapsed = time.Since(begin)
			return w, ctx.Err()
		case <-deadline.C:
			w.elapsed = time.Since(begin)
			switch {
			case changed:
				w.message = fmt.Sprintf("Timed out after %s: %s did not change", seconds(w.elapsed), path)
				if re != nil {
					w.message += " to contain a matching line"
				}
				w.message += "."
			case re != nil && cur.exists:
				w.message = fmt.Sprintf("Timed out after %s: %s has no line matching the pattern.", seconds(w.elapsed), path)
			default:
				w.message = fmt.Sprintf("Timed out after %s: %s does not exist.", seconds(w.elapsed), path)
			}
			return w, nil
		case <-tick.C:
		}
	}
}
