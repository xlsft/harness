package agent

import (
	"context"
	"sync"

	"github.com/xlsft/harness/packages/client/llm"
)

// Driver runs turns in the background and implements the message queue:
// a message submitted while a turn is running is injected after the current
// tool calls complete (same turn); if the turn ends first, queued messages
// start the next turn. Interrupting a turn keeps the queue, which is sent next.
type Driver struct {
	Agent *Agent
	// OnResult is called after every turn.
	OnResult func(TurnResult)

	ctx     context.Context
	mu      sync.Mutex
	running bool
	idle    *sync.Cond
}

// NewDriver creates a driver whose turns run under ctx.
func NewDriver(ctx context.Context, a *Agent, onResult func(TurnResult)) *Driver {
	d := &Driver{Agent: a, OnResult: onResult, ctx: ctx}
	d.idle = sync.NewCond(&d.mu)
	return d
}

// Submit starts a turn with blocks, or queues them under id if a turn is running.
// It reports whether the message was queued.
func (d *Driver) Submit(id string, blocks []llm.ContentBlock) (queued bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		d.Agent.Enqueue(id, blocks)
		return true
	}
	d.running = true
	go d.loop(blocks)
	return false
}

func (d *Driver) loop(blocks []llm.ContentBlock) {
	for {
		res := d.Agent.RunTurn(d.ctx, blocks)
		if d.OnResult != nil {
			d.OnResult(res)
		}
		d.mu.Lock()
		next := d.Agent.DrainQueue()
		if len(next) == 0 || d.ctx.Err() != nil {
			d.running = false
			d.idle.Broadcast()
			d.mu.Unlock()
			return
		}
		d.mu.Unlock()
		blocks = next
	}
}

// Busy reports whether a turn is running.
func (d *Driver) Busy() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running
}

// Interrupt cancels the running turn (no-op when idle).
func (d *Driver) Interrupt() { d.Agent.Interrupt() }

// Exclusive runs f while no turn is running (compaction, rewind). Messages
// submitted meanwhile are queued and start a turn when f returns. It
// returns ErrBusy when a turn is running.
func (d *Driver) Exclusive(f func()) error {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return ErrBusy
	}
	d.running = true
	d.mu.Unlock()
	f()
	d.mu.Lock()
	next := d.Agent.DrainQueue()
	if len(next) == 0 || d.ctx.Err() != nil {
		d.running = false
		d.idle.Broadcast()
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()
	go d.loop(next)
	return nil
}

// Wait blocks until no turn is running.
func (d *Driver) Wait() {
	d.mu.Lock()
	for d.running {
		d.idle.Wait()
	}
	d.mu.Unlock()
}
