//go:build unix

package terminal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shell starts /bin/sh (not the user's login shell: rc files would make output unpredictable).
func shell(t *testing.T, m *Manager, o Options) *Terminal {
	t.Helper()
	if o.Cwd == "" {
		o.Cwd = t.TempDir()
	}
	o.Shell = "/bin/sh"
	if o.Args == nil {
		o.Args = []string{}
	}
	term, err := m.Create(o)
	if err != nil {
		t.Fatal(err)
	}
	return term
}

func newManager(t *testing.T) *Manager {
	m := &Manager{}
	t.Cleanup(m.Close)
	return m
}

// readUntil reads a viewer until the output contains want (or the exit), and returns all output seen.
func readUntil(t *testing.T, v *Viewer, want string) (string, *Event) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out bytes.Buffer
	for {
		ev, err := v.Next(ctx)
		if err != nil {
			t.Fatalf("waiting for %q: %v; output so far: %q", want, err, out.String())
		}
		out.Write(ev.Data)
		if ev.Exited {
			return out.String(), &ev
		}
		if want != "" && strings.Contains(out.String(), want) {
			return out.String(), nil
		}
	}
}

func TestTerminalLifecycle(t *testing.T) {
	m := newManager(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	term := shell(t, m, Options{SessionID: "s1", Cwd: dir, Cols: 100, Rows: 30, Env: map[string]string{"GREETING": "hi there"}})
	info := term.Info()
	if !info.Running || info.Pid <= 0 || info.Shell != "/bin/sh" || info.Title != filepath.Base(dir) || info.Cols != 100 || info.Rows != 30 {
		t.Fatalf("info: %+v", info)
	}
	v := term.Attach()
	defer v.Close()

	if _, err := term.Write([]byte("echo \"[$GREETING|$TERM|$COLORTERM|$(pwd)|$(stty size)]\"\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, v, "[hi there|xterm-256color|truecolor|"+dir+"|30 100]")

	if err := term.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	_, _ = term.Write([]byte("stty size\n"))
	readUntil(t, v, "40 120")

	_, _ = term.Write([]byte("exit 3\n"))
	_, ev := readUntil(t, v, "")
	if ev == nil || ev.ExitCode != 3 {
		t.Fatalf("exit event: %+v", ev)
	}
	if _, err := v.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("after the exit: %v", err)
	}
	info = term.Info()
	if info.Running || info.ExitCode != 3 {
		t.Fatalf("info after exit: %+v", info)
	}
	if _, err := term.Write([]byte("x")); !errors.Is(err, ErrExited) {
		t.Fatalf("write after exit: %v", err)
	}
	// An exited terminal stays listed until it is killed (closed).
	if l := m.List("s1"); len(l) != 1 || len(m.List("other")) != 0 || len(m.List("")) != 1 {
		t.Fatalf("list: %v", l)
	}
	if !m.Kill(term.ID()) || m.Get(term.ID()) != nil || m.Kill(term.ID()) {
		t.Fatal("kill of an exited terminal")
	}
}

func TestReattachReplaysRecentOutput(t *testing.T) {
	m := newManager(t)
	term := shell(t, m, Options{})
	v := term.Attach()
	_, _ = term.Write([]byte("echo first-$((1+1))\n"))
	readUntil(t, v, "first-2")
	v.Close() // the UI disconnects; the shell keeps running

	_, _ = term.Write([]byte("echo second-$((2+2))\n"))
	time.Sleep(200 * time.Millisecond)
	v2 := term.Attach()
	defer v2.Close()
	out, _ := readUntil(t, v2, "second-4")
	if !strings.Contains(out, "first-2") {
		t.Fatalf("replay misses earlier output: %q", out)
	}
	if !term.Info().Running {
		t.Fatal("the shell stopped while detached")
	}
}

func TestReplayIsCutAtALineBoundary(t *testing.T) {
	m := &Manager{BufferSize: 1024}
	t.Cleanup(m.Close)
	term := shell(t, m, Options{})
	v := term.Attach()
	_, _ = term.Write([]byte("i=0; while [ $i -lt 200 ]; do echo line-$i-xxxxxxxxxxxxxxxx; i=$((i+1)); done; echo done-$((0))\n"))
	readUntil(t, v, "done-0")
	v.Close()

	v2 := term.Attach()
	defer v2.Close()
	ev, err := v2.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Skipped != 0 || len(ev.Data) == 0 || len(ev.Data) > 1024 {
		t.Fatalf("replay: skipped=%d len=%d", ev.Skipped, len(ev.Data))
	}
	if !bytes.HasPrefix(ev.Data, []byte("line-")) {
		t.Fatalf("replay does not start at a line: %q", ev.Data[:min(40, len(ev.Data))])
	}
}

func TestSlowViewerSkipsInsteadOfQueueing(t *testing.T) {
	m := &Manager{BufferSize: 1024}
	t.Cleanup(m.Close)
	term := shell(t, m, Options{})
	slow := term.Attach() // never reads while the output is produced
	defer slow.Close()
	fast := term.Attach()
	defer fast.Close()
	_, _ = term.Write([]byte("i=0; while [ $i -lt 300 ]; do echo row-$i-yyyyyyyyyyyyyyyy; i=$((i+1)); done; echo end-$((0))\n"))
	readUntil(t, fast, "end-0")

	ev, err := slow.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Skipped == 0 || !bytes.HasPrefix(ev.Data, []byte("row-")) {
		t.Fatalf("slow viewer: skipped=%d data=%q", ev.Skipped, ev.Data[:min(40, len(ev.Data))])
	}
}

func TestKillEndsTheShell(t *testing.T) {
	m := newManager(t)
	term := shell(t, m, Options{SessionID: "s"})
	other := shell(t, m, Options{SessionID: "keep"})
	v := term.Attach()
	defer v.Close()
	m.KillSession("s")
	select {
	case <-term.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the shell did not exit")
	}
	if term.Info().Running || term.Info().ExitCode != 128+1 { // SIGHUP
		t.Fatalf("info: %+v", term.Info())
	}
	if _, ev := readUntil(t, v, ""); ev == nil {
		t.Fatal("no exit event")
	}
	if m.Get(term.ID()) != nil || m.Get(other.ID()) == nil {
		t.Fatal("KillSession removed the wrong terminals")
	}
	m.Close()
	select {
	case <-other.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not end the shells")
	}
	if _, err := m.Create(Options{Cwd: t.TempDir(), Shell: "/bin/sh"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("create after close: %v", err)
	}
}

func TestTerminalCap(t *testing.T) {
	m := &Manager{Max: 2}
	t.Cleanup(m.Close)
	a := shell(t, m, Options{})
	shell(t, m, Options{})
	if _, err := m.Create(Options{Cwd: t.TempDir(), Shell: "/bin/sh"}); !errors.Is(err, ErrLimit) {
		t.Fatalf("third terminal: %v", err)
	}
	// An exited terminal makes room.
	_, _ = a.Write([]byte("exit\n"))
	<-a.Done()
	c := shell(t, m, Options{})
	if m.Get(a.ID()) != nil || m.Get(c.ID()) == nil || len(m.List("")) != 2 {
		t.Fatal("the exited terminal was not replaced")
	}
}

func TestTickets(t *testing.T) {
	now := time.Unix(1000, 0)
	m := &Manager{TicketTTL: 30 * time.Second, Now: func() time.Time { return now }}
	t.Cleanup(m.Close)
	term := shell(t, m, Options{})

	if _, err := m.IssueTicket("term_unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ticket for an unknown terminal: %v", err)
	}
	tk, err := m.IssueTicket(term.ID())
	if err != nil || len(tk) < 32 {
		t.Fatalf("ticket %q: %v", tk, err)
	}
	if got, err := m.Redeem(tk); err != nil || got != term {
		t.Fatalf("valid ticket: %v", err)
	}
	if _, err := m.Redeem(tk); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("reused ticket: %v", err)
	}
	if _, err := m.Redeem(""); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("empty ticket: %v", err)
	}
	if _, err := m.Redeem("nope"); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("unknown ticket: %v", err)
	}

	tk, _ = m.IssueTicket(term.ID())
	now = now.Add(31 * time.Second)
	if _, err := m.Redeem(tk); !errors.Is(err, ErrTicketExpired) {
		t.Fatalf("expired ticket: %v", err)
	}
	if _, err := m.Redeem(tk); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("an expired ticket is consumed too: %v", err)
	}

	tk, _ = m.IssueTicket(term.ID())
	m.Kill(term.ID())
	if _, err := m.Redeem(tk); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ticket of a killed terminal: %v", err)
	}
}

func TestBuildEnv(t *testing.T) {
	t.Setenv("HARNESS_AGENT_TOKEN", "secret")
	t.Setenv("TERM", "dumb")
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Setenv("KEEP_ME", "1")
	env := buildEnv(map[string]string{"EXTRA": "x", "KEEP_ME": "2", "TERM": "vt100"})
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := got[k]; dup {
			t.Fatalf("%s set twice", k)
		}
		got[k] = v
	}
	if _, ok := got["HARNESS_AGENT_TOKEN"]; ok {
		t.Fatal("the agent token leaks into the shell")
	}
	if _, ok := got["TERM_PROGRAM"]; ok {
		t.Fatal("TERM_PROGRAM of the client's terminal leaks into the shell")
	}
	if got["TERM"] != "xterm-256color" || got["COLORTERM"] != "truecolor" || got["EXTRA"] != "x" || got["KEEP_ME"] != "2" || got["PATH"] != os.Getenv("PATH") {
		t.Fatalf("env: %v", got)
	}
}
