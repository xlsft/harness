package acp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"unicode/utf8"

	sdk "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"
)

// defaultOutputLimit bounds a terminal's retained output when the agent gives no limit.
const defaultOutputLimit = 1 << 20

// terminals runs the agent's terminal/* commands: plain processes (no PTY) whose combined output is kept up to
// the agent's byte limit, truncated from the start.
type terminals struct {
	mu   sync.Mutex
	runs map[string]*termRun
	// gone keeps released terminals for tool results that show their output later.
	gone map[string]*termRun
}

type termRun struct {
	cmd       *exec.Cmd
	mu        sync.Mutex
	out       []byte
	limit     int
	truncated bool
	done      chan struct{}
	exit      *sdk.TerminalExitStatus
}

func (r *termRun) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.out = append(r.out, p...)
	if over := len(r.out) - r.limit; over > 0 {
		// Cut at a character boundary.
		for over < len(r.out) && !utf8.RuneStart(r.out[over]) {
			over++
		}
		r.out = append([]byte(nil), r.out[over:]...)
		r.truncated = true
	}
	return len(p), nil
}

func (r *termRun) output() (string, bool, *sdk.TerminalExitStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.out), r.truncated, r.exit
}

func (t *terminals) create(p sdk.CreateTerminalRequest, cwd string, env []string) (string, error) {
	dir := cwd
	if p.Cwd != nil && *p.Cwd != "" {
		if !filepath.IsAbs(*p.Cwd) {
			return "", fmt.Errorf("cwd must be absolute: %s", *p.Cwd)
		}
		dir = *p.Cwd
	}
	limit := defaultOutputLimit
	if p.OutputByteLimit != nil && *p.OutputByteLimit > 0 {
		limit = *p.OutputByteLimit
	}
	extra := map[string]string{}
	for _, e := range p.Env {
		extra[e.Name] = e.Value
	}
	cmd := exec.Command(p.Command, p.Args...)
	cmd.Dir = dir
	cmd.Env = buildEnv(env, extra)
	setProcessGroup(cmd)
	r := &termRun{cmd: cmd, limit: limit, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = r, r
	if err := cmd.Start(); err != nil {
		return "", err
	}
	id := "term_" + uuid.NewString()
	go func() {
		err := cmd.Wait()
		st := &sdk.TerminalExitStatus{}
		if cmd.ProcessState != nil {
			if sig := signalName(cmd.ProcessState); sig != "" {
				st.Signal = &sig
			} else {
				code := cmd.ProcessState.ExitCode()
				st.ExitCode = &code
			}
		} else if err != nil {
			code := -1
			st.ExitCode = &code
		}
		r.mu.Lock()
		r.exit = st
		r.mu.Unlock()
		close(r.done)
	}()
	t.mu.Lock()
	if t.runs == nil {
		t.runs = map[string]*termRun{}
	}
	t.runs[id] = r
	t.mu.Unlock()
	return id, nil
}

func (t *terminals) get(id string) (*termRun, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.runs[id]
	if r == nil {
		return nil, sdk.NewInvalidParams(map[string]any{"terminalId": id, "error": "unknown terminal"})
	}
	return r, nil
}

func (t *terminals) wait(ctx context.Context, id string) (*sdk.TerminalExitStatus, error) {
	r, err := t.get(id)
	if err != nil {
		return nil, err
	}
	select {
	case <-r.done:
		_, _, st := r.output()
		return st, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *terminals) kill(id string) error {
	r, err := t.get(id)
	if err != nil {
		return err
	}
	killGroup(r.cmd)
	return nil
}

func (t *terminals) release(id string) error {
	t.mu.Lock()
	r := t.runs[id]
	delete(t.runs, id)
	if r != nil {
		if t.gone == nil || len(t.gone) > 64 {
			t.gone = map[string]*termRun{}
		}
		t.gone[id] = r
	}
	t.mu.Unlock()
	if r == nil {
		return errors.New("unknown terminal")
	}
	select {
	case <-r.done:
	default:
		killGroup(r.cmd)
	}
	return nil
}

// snapshot returns a terminal's output for tool results ("" when unknown).
func (t *terminals) snapshot(id string) (string, *sdk.TerminalExitStatus, bool) {
	t.mu.Lock()
	r := t.runs[id]
	if r == nil {
		r = t.gone[id]
	}
	t.mu.Unlock()
	if r == nil {
		return "", nil, false
	}
	out, _, st := r.output()
	return out, st, true
}

// closeAll kills every terminal.
func (t *terminals) closeAll() {
	t.mu.Lock()
	runs := t.runs
	t.runs = nil
	t.mu.Unlock()
	for _, r := range runs {
		select {
		case <-r.done:
		default:
			killGroup(r.cmd)
		}
	}
}
