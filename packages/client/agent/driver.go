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
//
// While held (SetHeld: the provider of the session's model is out of usage) no message starts a turn: submitted
// messages wait in the queue, and the queue stays there when a turn ends, until Release.
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
	// held: see SetHeld. wakeMissed: Wake was called while held (a background task's notification waits).
	held, wakeMissed bool
	// wakeLater: Wake was called during a turn, whose next request may never come; checked when it ends.
	wakeLater bool
}

// NewDriver creates a driver whose turns run under ctx.
func NewDriver(ctx context.Context, a *Agent, onResult func(TurnResult)) *Driver {
	d := &Driver{Agent: a, OnResult: onResult, ctx: ctx}
	d.idle = sync.NewCond(&d.mu)
	return d
}

// Submit starts a turn with blocks, or queues them under id if a turn is running or the driver is held.
// It reports whether the message was queued.
func (d *Driver) Submit(id string, blocks []llm.ContentBlock) (queued bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running || d.held {
		d.Agent.Enqueue(id, blocks)
		if d.stopReply != nil {
			d.stopReply()
		}
		return true
	}
	d.running = true
	d.interrupts = 0
	var ids []string
	if id != "" {
		ids = []string{id}
	}
	go d.loop(blocks, ids)
	return false
}

// Wake starts a turn without a message when the session is idle and the agent has reminders waiting (a
// background task that finished): the model answers the notification. While a turn runs the reminder rides on its
// next request; if the turn ends without another one, the next turn starts for it. No-op after Stop; while held it
// waits for Release.
func (d *Driver) Wake() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.held {
		d.wakeMissed = true
		return
	}
	if d.running {
		d.wakeLater = true
		return
	}
	if d.stopped || d.ctx.Err() != nil || !d.Agent.HasReminders() {
		return
	}
	d.running = true
	d.interrupts = 0
	go d.loop(nil, nil)
}

// wakeDue reports whether a Wake during the turn that just ended still has something to answer (d.mu held). After
// an interrupted turn it waits for the next message: the user just stopped the work.
func (d *Driver) wakeDue(interrupted bool) bool {
	due := d.wakeLater && !interrupted && !d.held && !d.stopped && d.ctx.Err() == nil && d.Agent.HasReminders()
	d.wakeLater = false
	return due
}

func (d *Driver) loop(blocks []llm.ContentBlock, ids []string) {
	for {
		res := d.Agent.RunTurnWithIDs(d.ctx, blocks, ids)
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
		var next []llm.ContentBlock
		var nextIDs []string
		if !d.held {
			next, nextIDs = d.Agent.takeQueue()
		}
		wake := d.wakeDue(res.Interrupted)
		if (len(next) == 0 && !wake) || d.ctx.Err() != nil {
			d.running = false
			d.idle.Broadcast()
			d.mu.Unlock()
			d.Agent.noteDrained(next)
			return
		}
		d.interrupts = 0
		d.mu.Unlock()
		d.Agent.noteDrained(next)
		blocks, ids = next, nextIDs
	}
}

// SetHeld turns holding on or off. Turning it off starts nothing by itself (see Release); a queue that waits goes
// out when the running turn ends.
func (d *Driver) SetHeld(held bool) {
	d.mu.Lock()
	d.held = held
	d.mu.Unlock()
}

// Held reports whether the driver holds messages.
func (d *Driver) Held() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.held
}

// Release ends holding and starts the next turn with the held messages, in order (or with the notification Wake
// could not start meanwhile). It reports whether a turn started; with a turn running the queue goes out after it.
func (d *Driver) Release() (started bool) {
	d.mu.Lock()
	d.held = false
	missed := d.wakeMissed
	d.wakeMissed = false
	if d.running || d.stopped || d.ctx.Err() != nil {
		d.mu.Unlock()
		return false
	}
	next, ids := d.Agent.takeQueue()
	if len(next) == 0 && !(missed && d.Agent.HasReminders()) {
		d.mu.Unlock()
		return false
	}
	d.running = true
	d.interrupts = 0
	d.mu.Unlock()
	d.Agent.noteDrained(next)
	go d.loop(next, ids)
	return true
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
	var next []llm.ContentBlock
	var ids []string
	if !d.held {
		next, ids = d.Agent.takeQueue()
	}
	wake := d.wakeDue(false)
	if (len(next) == 0 && !wake) || d.ctx.Err() != nil {
		d.running = false
		d.idle.Broadcast()
		d.mu.Unlock()
		d.Agent.noteDrained(next)
		return nil
	}
	d.interrupts = 0
	d.mu.Unlock()
	d.Agent.noteDrained(next)
	go d.loop(next, ids)
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
