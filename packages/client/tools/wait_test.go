package tools

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func taskEnv(t *testing.T) *Env {
	t.Helper()
	env := newEnv(t)
	env.Tasks = NewTaskManager(filepath.Join(env.ScratchDir, "tasks"))
	t.Cleanup(func() { env.Tasks.StopAll(TaskSessionClosed) })
	return env
}

func startTask(t *testing.T, env *Env, command string) string {
	t.Helper()
	info, err := env.Tasks.Start(env.Shell, command, "", "toolu_bg", 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

func st(r Result) map[string]any {
	m, _ := r.Structured.(map[string]any)
	return m
}

func TestTaskOutputIncrementalReads(t *testing.T) {
	env := taskEnv(t)
	id := startTask(t, env, "echo one; sleep 0.4; echo two; exit 2")

	r := call(t, env, &TaskOutput{}, map[string]any{"task_id": id, "pattern": "^one$", "timeout": 5000})
	s := st(r)
	if r.IsError || s["condition_met"] != true || s["matched_line"] != "one" || s["status"] != "running" {
		t.Fatalf("pattern read: %q %+v", text(r), s)
	}
	if s["stdout"] != "one\n" || !strings.Contains(text(r), "Matched after") {
		t.Fatalf("pattern read output: %q", text(r))
	}

	r = call(t, env, &TaskOutput{}, map[string]any{"task_id": id, "block": true, "timeout": 5000})
	s = st(r)
	if s["stdout"] != "two\n" || s["status"] != "exited" || s["exit_code"] != 2 || s["condition_met"] != true {
		t.Fatalf("block read: %q %+v", text(r), s)
	}
	if !strings.Contains(text(r), "exited with code 2") || !env.Tasks.Reported(id) {
		t.Fatalf("finished status not reported: %q", text(r))
	}

	r = call(t, env, &TaskOutput{}, map[string]any{"task_id": id})
	if st(r)["stdout"] != "" || !strings.Contains(text(r), "No new output since byte 8") {
		t.Fatalf("drained read: %q", text(r))
	}
	r = call(t, env, &TaskOutput{}, map[string]any{"task_id": id, "offset": 0})
	if st(r)["stdout"] != "one\ntwo\n" {
		t.Fatalf("offset read: %q", text(r))
	}
}

func TestTaskOutputTimeoutAndErrors(t *testing.T) {
	env := taskEnv(t)
	id := startTask(t, env, "sleep 5")
	start := time.Now()
	r := call(t, env, &TaskOutput{}, map[string]any{"task_id": id, "block": true, "timeout": 200})
	s := st(r)
	if el := time.Since(start); el < 200*time.Millisecond || el > 2*time.Second {
		t.Fatalf("waited %s", el)
	}
	if r.IsError || s["timed_out"] != true || s["status"] != "running" || !strings.Contains(text(r), "Timed out after") {
		t.Fatalf("timeout: %q %+v", text(r), s)
	}
	if env.Tasks.Reported(id) {
		t.Fatal("a running task is not reported")
	}
	if _, err := env.Tasks.Stop(id); err != nil {
		t.Fatal(err)
	}
	r = call(t, env, &TaskOutput{}, map[string]any{"task_id": id})
	if st(r)["status"] != "killed" || !strings.Contains(text(r), "killed (stopped") {
		t.Fatalf("killed: %q", text(r))
	}

	for _, in := range []map[string]any{{}, {"task_id": "bnope"}, {"task_id": id, "pattern": "("}, {"task_id": id, "offset": -1}} {
		if r := call(t, env, &TaskOutput{}, in); !r.IsError {
			t.Errorf("%v: expected an error, got %q", in, text(r))
		}
	}
}

func TestTaskOutputPatternMissAndPartialLine(t *testing.T) {
	env := taskEnv(t)
	id := startTask(t, env, "echo nothing here")
	r := call(t, env, &TaskOutput{}, map[string]any{"task_id": id, "pattern": "never", "timeout": 5000})
	if s := st(r); s["condition_met"] != false || s["timed_out"] != false || !strings.Contains(text(r), "without a line matching") {
		t.Fatalf("miss: %q %+v", text(r), s)
	}
	id = startTask(t, env, "sleep 0.2; printf 'ready'")
	r = call(t, env, &TaskOutput{}, map[string]any{"task_id": id, "pattern": "ready$", "timeout": 5000})
	if s := st(r); s["matched_line"] != "ready" {
		t.Fatalf("unterminated last line: %q %+v", text(r), s)
	}
}

func TestTaskOutputTruncatesKeepingTheEnd(t *testing.T) {
	env := taskEnv(t)
	id := startTask(t, env, "seq 1 20000")
	r := call(t, env, &TaskOutput{}, map[string]any{"task_id": id, "block": true, "timeout": 10000})
	s := st(r)
	out, _ := s["stdout"].(string)
	if s["truncated"] != true || len(out) > TaskOutputMaxChars || !strings.HasSuffix(out, "\n20000\n") {
		t.Fatalf("truncated=%v len=%d tail=%q", s["truncated"], len(out), out[max(0, len(out)-20):])
	}
	if !strings.Contains(text(r), "earlier bytes not shown") {
		t.Fatalf("note missing: %q", text(r)[:200])
	}
	// Starts on a line boundary: the first line is a whole number.
	first := strings.SplitN(out, "\n", 2)[0]
	if n, err := strconv.Atoi(first); err != nil || n < 2 {
		t.Fatalf("first kept line %q", first)
	}
	if s["next_offset"] != int64(108894) {
		t.Fatalf("next_offset %v", s["next_offset"])
	}
}

func TestWaitTask(t *testing.T) {
	env := taskEnv(t)
	id := startTask(t, env, "echo booting; sleep 0.3; echo 'listening on 3000'; sleep 0.3; echo done")
	r := call(t, env, &Wait{}, map[string]any{"task_id": id, "pattern": "listening on \\d+", "timeout": 5000})
	s := st(r)
	if r.IsError || s["met"] != true || s["matched_line"] != "listening on 3000" || s["status"] != "running" {
		t.Fatalf("pattern: %q %+v", text(r), s)
	}
	r = call(t, env, &Wait{}, map[string]any{"task_id": id, "timeout": 5000})
	s = st(r)
	if s["met"] != true || s["status"] != "exited" || !strings.Contains(text(r), "exited with code 0") || !strings.Contains(text(r), "done") {
		t.Fatalf("finish: %q %+v", text(r), s)
	}
	// Wait matches lines printed earlier and leaves the TaskOutput position alone.
	r = call(t, env, &Wait{}, map[string]any{"task_id": id, "pattern": "booting", "timeout": 1000})
	if st(r)["met"] != true || env.Tasks.ReadOffset(id) != 0 {
		t.Fatalf("earlier line: %q offset %d", text(r), env.Tasks.ReadOffset(id))
	}

	id = startTask(t, env, "sleep 5")
	r = call(t, env, &Wait{}, map[string]any{"task_id": id, "timeout": 150})
	if s := st(r); r.IsError || s["met"] != false || s["timed_out"] != true || !strings.Contains(text(r), "still running") {
		t.Fatalf("timeout: %q %+v", text(r), s)
	}
}

func TestWaitPort(t *testing.T) {
	env := newEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Nothing listens yet: times out.
	r := call(t, env, &Wait{}, map[string]any{"port": port, "host": "127.0.0.1", "timeout": 300})
	if s := st(r); r.IsError || s["met"] != false || !strings.Contains(text(r), "Timed out") {
		t.Fatalf("closed port: %q %+v", text(r), s)
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			return
		}
		t.Cleanup(func() { l.Close() })
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	r = call(t, env, &Wait{}, map[string]any{"port": port, "host": "127.0.0.1", "timeout": 5000})
	s := st(r)
	if r.IsError || s["met"] != true || s["target"] != "127.0.0.1:"+strconv.Itoa(port) || !strings.Contains(text(r), "accepting connections") {
		t.Fatalf("open port: %q %+v", text(r), s)
	}
	if el := s["elapsed_ms"].(int64); el < 150 {
		t.Fatalf("met too early: %dms", el)
	}
}

func TestWaitFile(t *testing.T) {
	env := newEnv(t)
	p := filepath.Join(env.Cwd, "out", "build.log")
	go func() {
		time.Sleep(200 * time.Millisecond)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("starting\n"), 0o644)
	}()
	r := call(t, env, &Wait{}, map[string]any{"path": "out/build.log", "timeout": 5000})
	if s := st(r); r.IsError || s["met"] != true || s["target"] != p {
		t.Fatalf("exists: %q %+v", text(r), s)
	}
	// Already there: met at once.
	if r := call(t, env, &Wait{}, map[string]any{"path": p, "timeout": 1000}); st(r)["elapsed_ms"].(int64) > 500 {
		t.Fatalf("existing file: %q", text(r))
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		os.WriteFile(p, []byte("starting\nBUILD OK in 3s\n"), 0o644)
	}()
	r = call(t, env, &Wait{}, map[string]any{"path": p, "pattern": "BUILD (OK|FAILED)", "timeout": 5000})
	if s := st(r); s["met"] != true || s["matched_line"] != "BUILD OK in 3s" {
		t.Fatalf("pattern: %q %+v", text(r), s)
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		os.WriteFile(p, []byte("rewritten, longer content\n"), 0o644)
	}()
	r = call(t, env, &Wait{}, map[string]any{"path": p, "changed": true, "timeout": 5000})
	if s := st(r); s["met"] != true || !strings.Contains(text(r), "changed") {
		t.Fatalf("changed: %q %+v", text(r), s)
	}
	r = call(t, env, &Wait{}, map[string]any{"path": p, "changed": true, "timeout": 200})
	if s := st(r); s["met"] != false || !strings.Contains(text(r), "did not change") {
		t.Fatalf("unchanged: %q %+v", text(r), s)
	}
	r = call(t, env, &Wait{}, map[string]any{"path": "missing.txt", "timeout": 150})
	if s := st(r); s["met"] != false || !strings.Contains(text(r), "does not exist") {
		t.Fatalf("missing: %q %+v", text(r), s)
	}
}

func TestWaitCancelsPromptly(t *testing.T) {
	env := taskEnv(t)
	id := startTask(t, env, "sleep 5")
	for _, in := range []map[string]any{
		{"port": 1, "timeout": 10000},
		{"path": "never.txt", "timeout": 10000},
		{"task_id": id, "timeout": 10000},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(100*time.Millisecond, cancel)
		raw, _ := json.Marshal(in)
		start := time.Now()
		r := (&Wait{}).Run(ctx, env, Call{ID: "w", Input: raw})
		if el := time.Since(start); el > time.Second || !r.IsError || !strings.Contains(text(r), "interrupted") {
			t.Errorf("%v: %q after %s", in, text(r), el)
		}
		cancel()
	}
}

func TestWaitValidationAndReadOnly(t *testing.T) {
	env := taskEnv(t)
	for _, in := range []map[string]any{
		{"timeout": 1000},
		{"port": 80, "path": "x", "timeout": 1000},
		{"port": 80},
		{"port": 80, "pattern": "x", "timeout": 1000},
		{"path": "x", "host": "example.com", "timeout": 1000},
		{"task_id": "b1", "changed": true, "timeout": 1000},
		{"port": 70000, "timeout": 1000},
		{"task_id": "bnope", "timeout": 1000},
		{"path": "x", "pattern": "[", "timeout": 1000},
	} {
		if r := call(t, env, &Wait{}, in); !r.IsError {
			t.Errorf("%v: expected an error, got %q", in, text(r))
		}
	}
	ro := func(in map[string]any) bool { raw, _ := json.Marshal(in); return (&Wait{}).ReadOnly(raw) }
	if !ro(map[string]any{"port": 3000}) || !ro(map[string]any{"port": 3000, "host": "127.0.0.1"}) || !ro(map[string]any{"path": "/etc/hosts"}) {
		t.Error("local waits are read-only")
	}
	if ro(map[string]any{"port": 443, "host": "example.com"}) || ro(map[string]any{"port": 22, "host": "10.0.0.5"}) {
		t.Error("a remote port is not read-only")
	}
	for h, want := range map[string]bool{"": true, "localhost": true, "LOCALHOST.": true, "app.localhost": true, "127.0.0.2": true, "[::1]": true, "0.0.0.0": true, "192.168.1.1": false, "example.com": false} {
		if IsLocalHost(h) != want {
			t.Errorf("IsLocalHost(%q) != %v", h, want)
		}
	}
}

func TestTailLines(t *testing.T) {
	if got := TailLines("a\nb\nc\nd\n", 2, 100); got != "c\nd" {
		t.Fatalf("lines: %q", got)
	}
	if got := TailLines("aaaa\nbbbbbb\n", 10, 4); got != "bbbb" {
		t.Fatalf("bytes: %q", got)
	}
	if got := TailLines("xé", 1, 1); got != "" {
		t.Fatalf("cut rune: %q", got)
	}
}
