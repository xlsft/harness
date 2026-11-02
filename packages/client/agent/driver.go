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
//
// After an interrupted turn (and its OnResult) the driver asks the model for a
// short reply to the interruption (Agent.ReplyToInterrupt), still counted as
// running so that messages are queued meanwhile. A submitted message, another
// Interrupt, Exclusive or Stop cancels the reply; it is skipped when a message
// is already queued.
type Driver struct {
	Agent *Agent
	// OnResult is called after every turn.
	OnResult func(TurnResult)

	ctx     context.Context
	mu      sync.Mutex
	running bool
	idle    *sync.Cond
	// interrupts counts Interrupt calls since the current turn started; a
	// second one skips or cancels the interrupt reply.
	interrupts int
	// replying is set from the start of the interrupt reply until the loop
	// has decided what runs next; stopReply cancels the reply request.
	replying  bool
	stopReply context.CancelFunc
	stopped   bool
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
		if d.stopReply != nil {
			d.stopReply()
		}
		return true
	}
	d.running = true
	d.interrupts = 0
	go d.loop(blocks)
	return false
}

func (d *Driver) loop(blocks []llm.ContentBlock) {
	for {
		res := d.Agent.RunTurn(d.ctx, blocks)
		if d.OnResult != nil {
			d.OnResult(res)
		}
		if res.Interrupted {
			d.reply()
		}
		d.mu.Lock()
		if d.replying {
			d.replying = false
			d.idle.Broadcast()
		}
		next := d.Agent.DrainQueue()
		if len(next) == 0 || d.ctx.Err() != nil {
			d.running = false
			d.idle.Broadcast()
			d.mu.Unlock()
			return
		}
		d.interrupts = 0
		d.mu.Unlock()
		blocks = next
	}
}

// reply runs the interrupt reply unless a message is queued, the turn was
// interrupted twice or the driver is stopping. It blocks until the reply is
// done, cancelled or timed out (InterruptReplyTimeout).
func (d *Driver) reply() {
	d.mu.Lock()
	if d.stopped || d.interrupts > 1 || d.ctx.Err() != nil || d.Agent.QueueLen() > 0 {
		d.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, InterruptReplyTimeout)
	defer cancel()
	d.replying, d.stopReply = true, cancel
	d.mu.Unlock()
	d.Agent.ReplyToInterrupt(ctx)
	d.mu.Lock()
	d.stopReply = nil
	d.mu.Unlock()
}

// Busy reports whether a turn (or the reply to an interrupted one) is running.
func (d *Driver) Busy() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running
}

// Interrupt cancels the running turn, or the interrupt reply (no-op when idle).
func (d *Driver) Interrupt() {
	d.mu.Lock()
	d.interrupts++
	if d.stopReply != nil {
		d.stopReply()
	}
	d.mu.Unlock()
	d.Agent.Interrupt()
}

// Stop cancels the running turn without an interrupt reply, now or later
// (the session is closing).
func (d *Driver) Stop() {
	d.mu.Lock()
	d.stopped = true
	d.mu.Unlock()
	d.Interrupt()
}

// Exclusive runs f while no turn is running (compaction, rewind). Messages
// submitted meanwhile are queued and start a turn when f returns. A running
// interrupt reply is cancelled first. It returns ErrBusy when a turn is
// running.
func (d *Driver) Exclusive(f func()) error {
	d.mu.Lock()
	for d.running && d.replying {
		if d.stopReply != nil {
			d.stopReply()
		}
		d.idle.Wait()
	}
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
	d.interrupts = 0
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
