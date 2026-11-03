// Package acp runs ACP (Agent Client Protocol) agents as session engines: the client spawns the agent's CLI,
// speaks JSON-RPC over its stdio as an ACP client (github.com/coder/acp-go-sdk) and maps the agent's updates
// onto harness.agent.v1 session events.
package acp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/xlsft/harness/packages/client/acp/registry"
)

// process is a running agent with its ACP connection.
type process struct {
	cmd    *exec.Cmd
	conn   *sdk.ClientSideConnection
	stdin  io.WriteCloser
	stderr *tailBuffer
	done   chan struct{}
	waitMu sync.Mutex
	err    error
}

// buildEnv is the agent's environment: the client's, then the session's variables, then the launch's own.
func buildEnv(session []string, launch map[string]string) []string {
	env := map[string]string{}
	var order []string
	add := func(kv string) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return
		}
		if _, seen := env[k]; !seen {
			order = append(order, k)
		}
		env[k] = v
	}
	for _, kv := range os.Environ() {
		add(kv)
	}
	for _, kv := range session {
		add(kv)
	}
	keys := make([]string, 0, len(launch))
	for k := range launch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		add(k + "=" + launch[k])
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+env[k])
	}
	return out
}

// spawn starts the agent in cwd and connects to it; client handles the agent's requests.
func spawn(l registry.Launch, cwd string, env []string, client sdk.Client) (*process, error) {
	if l.Command == "" {
		return nil, errors.New("no agent command")
	}
	cmd := exec.Command(l.Command, l.Args...)
	cmd.Dir = cwd
	cmd.Env = buildEnv(env, l.Env)
	setProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	p := &process{cmd: cmd, stdin: stdin, stderr: &tailBuffer{max: 16 << 10}, done: make(chan struct{})}
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", l.Command, err)
	}
	p.conn = sdk.NewClientSideConnection(client, stdin, stdout)
	go func() {
		err := cmd.Wait()
		p.waitMu.Lock()
		p.err = err
		p.waitMu.Unlock()
		close(p.done)
	}()
	return p, nil
}

// exitError describes why the process ended, with the tail of its stderr.
func (p *process) exitError() error {
	select {
	case <-p.done:
	default:
		return nil
	}
	p.waitMu.Lock()
	err := p.err
	p.waitMu.Unlock()
	msg := "the agent exited"
	if err != nil {
		msg += ": " + err.Error()
	}
	if tail := strings.TrimSpace(p.stderr.String()); tail != "" {
		msg += "\n" + tail
	}
	return errors.New(msg)
}

// alive reports whether the process still runs.
func (p *process) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// stop closes stdin, then kills the process group if it does not exit soon.
func (p *process) stop() {
	if p == nil {
		return
	}
	_ = p.stdin.Close()
	select {
	case <-p.done:
		return
	case <-time.After(1500 * time.Millisecond):
	}
	killGroup(p.cmd)
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
	}
}

// call wraps an ACP request so a dead process fails it with the reason.
func (p *process) failure(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if e := p.exitError(); e != nil {
		return e
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}
