package relay

import (
	"context"
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
)

// uuidNamespace scopes the deterministic envelope uuids of durable events.
var uuidNamespace = uuid.MustParse("4f0d8a52-3b1e-4c47-9d0e-6a8b2f1c7e55")

// hostSession mirrors one live session: it registers it (getting an epoch),
// publishes its events in batches and runs its commands in order.
//
// The local event log numbers events 1, 2, 3, ... per process, while the
// server's seq continues across processes (a resumed session keeps its
// history). base maps one onto the other: server seq = base + local seq.
type hostSession struct {
	h    *Host
	id   string
	feed Feed
	cmds chan *relayv1.Command

	mu         sync.Mutex
	epoch      int64
	base       int64
	baseSet    bool
	lastLocal  int64 // highest local durable seq the server acknowledged
	superseded bool
	stop       chan struct{}
}

func newHostSession(h *Host, id string, feed Feed) *hostSession {
	return &hostSession{h: h, id: id, feed: feed, cmds: make(chan *relayv1.Command, 256), stop: make(chan struct{})}
}

func (hs *hostSession) currentEpoch() int64 {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	return hs.epoch
}

func (hs *hostSession) supersede() {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	if !hs.superseded {
		hs.superseded = true
		close(hs.stop)
	}
}

func (hs *hostSession) isSuperseded() bool {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	return hs.superseded
}

// rebase records a new registration and returns the local seq to publish after.
func (hs *hostSession) rebase(epoch, acked int64) int64 {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.epoch = epoch
	switch {
	case !hs.baseSet:
		// First registration from this process: local seq 1 follows the stored history.
		hs.base, hs.baseSet = acked, true
	case acked > hs.base+hs.lastLocal:
		// Another host published meanwhile: continue after its events.
		hs.base = acked - hs.lastLocal
	}
	return max(acked-hs.base, 0)
}

type stopReason int

const (
	reasonStop       stopReason = iota // ctx ended
	reasonEnded                        // the session ended locally
	reasonSuperseded                   // another host took the session over
	reasonReregister                   // epoch or host lost: register again
	reasonRewind                       // resend from the server's acked seq
)

// run keeps the session registered and published until it ends, is
// superseded or ctx is done.
func (hs *hostSession) run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go hs.worker(ctx)
	h := hs.h
	attempt := 0
	for ctx.Err() == nil && !hs.isSuperseded() {
		if attempt > 0 && !sleep(ctx, h.backoff(attempt-1)) {
			return
		}
		attempt++
		hostID, err := h.waitHost(ctx)
		if err != nil {
			return
		}
		res, err := call(ctx, h, h.rpc.RegisterSession, &relayv1.RegisterSessionRequest{HostId: hostID, Session: hs.feed.Info()})
		if err != nil {
			h.log.Debug("register session failed", "session", hs.id, "err", err)
			continue
		}
		epoch := res.GetEpoch()
		after := hs.rebase(epoch, res.GetAckedSeq())
		rewinds := 0
		for {
			reason, acked := hs.publish(ctx, hostID, epoch, after)
			switch reason {
			case reasonEnded:
				uctx, c := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				_, _ = call(uctx, h, h.rpc.UnregisterSession, &relayv1.UnregisterSessionRequest{HostId: hostID, SessionId: hs.id, Epoch: epoch})
				c()
				return
			case reasonStop, reasonSuperseded:
				return
			case reasonRewind:
				hs.mu.Lock()
				next := max(acked-hs.base, 0)
				hs.mu.Unlock()
				if next <= after {
					rewinds++
					if !sleep(ctx, h.backoff(min(rewinds, 6))) {
						return
					}
				} else {
					rewinds = 0
				}
				after = next
				attempt = 0
				continue
			}
			break // reasonReregister
		}
	}
}

// envelope converts a local event for the wire without touching the shared original.
func (hs *hostSession) envelope(ev *agentv1.SessionEvent, base int64) *relayv1.EventEnvelope {
	c := proto.Clone(ev).(*agentv1.SessionEvent)
	c.SessionId = hs.id
	c.Seq = base + ev.GetSeq()
	id := uuid.NewString()
	if !isDelta(ev) {
		id = uuid.NewSHA1(uuidNamespace, []byte(hs.h.o.InstanceID+"/"+hs.id+"/"+strconv.FormatInt(ev.GetSeq(), 10))).String()
	}
	return &relayv1.EventEnvelope{Uuid: id, Event: c}
}

func isDelta(ev *agentv1.SessionEvent) bool {
	_, ok := ev.GetEvent().(*agentv1.SessionEvent_AssistantDelta)
	return ok
}

// publish streams local events after local seq `after` upstream in batches
// (every FlushInterval or MaxBatch events). Deltas are sent once and dropped
// on retry. It returns why it stopped and, for reasonRewind, the server seq
// to resend after.
func (hs *hostSession) publish(ctx context.Context, hostID string, epoch, after int64) (stopReason, int64) {
	h := hs.h
	hs.mu.Lock()
	base := hs.base
	hs.mu.Unlock()
	replay, ch, unsubscribe := hs.feed.Subscribe(after)
	defer unsubscribe()

	var batch []*relayv1.EventEnvelope
	var batchLocal int64 // highest local durable seq in batch
	add := func(ev *agentv1.SessionEvent) {
		batch = append(batch, hs.envelope(ev, base))
		if !isDelta(ev) {
			batchLocal = max(batchLocal, ev.GetSeq())
		}
	}
	flush := func() (stopReason, int64, bool) {
		for attempt := 0; len(batch) > 0; attempt++ {
			res, err := call(ctx, h, h.rpc.PublishEvents, &relayv1.PublishEventsRequest{HostId: hostID, SessionId: hs.id, Epoch: epoch, Events: batch})
			if err == nil {
				batch = nil
				hs.mu.Lock()
				hs.lastLocal = max(hs.lastLocal, batchLocal)
				want := base + hs.lastLocal
				hs.mu.Unlock()
				if res.GetAckedSeq() < want {
					return reasonRewind, res.GetAckedSeq(), false
				}
				return 0, 0, true
			}
			if ctx.Err() != nil {
				return reasonStop, 0, false
			}
			switch codeOf(err) {
			case connect.CodeFailedPrecondition, connect.CodeNotFound:
				if hs.isSuperseded() {
					return reasonSuperseded, 0, false
				}
				return reasonReregister, 0, false
			}
			h.log.Debug("publish failed", "session", hs.id, "err", err)
			// Never resend deltas: the final AssistantMessage supersedes them.
			kept := batch[:0]
			for _, e := range batch {
				if !isDelta(e.GetEvent()) {
					kept = append(kept, e)
				}
			}
			batch = kept
			if attempt >= 4 {
				return reasonReregister, 0, false
			}
			if !sleep(ctx, h.backoff(attempt)) {
				return reasonStop, 0, false
			}
		}
		return 0, 0, true
	}

	for _, ev := range replay {
		add(ev)
		if len(batch) >= h.o.MaxBatch {
			if r, a, ok := flush(); !ok {
				return r, a
			}
		}
	}
	ticker := time.NewTicker(h.o.FlushInterval)
	defer ticker.Stop()
	for {
		if len(batch) >= h.o.MaxBatch {
			if r, a, ok := flush(); !ok {
				return r, a
			}
		}
		select {
		case <-ctx.Done():
			return reasonStop, 0
		case <-hs.stop:
			return reasonSuperseded, 0
		case ev, ok := <-ch:
			if !ok {
				if r, a, ok := flush(); !ok {
					return r, a
				}
				if hs.feed.Closed() {
					return reasonEnded, 0
				}
				// Dropped as a slow subscriber: resubscribe after what the server has.
				hs.mu.Lock()
				acked := hs.base + hs.lastLocal
				hs.mu.Unlock()
				return reasonRewind, acked
			}
			add(ev)
		case <-ticker.C:
			if r, a, ok := flush(); !ok {
				return r, a
			}
		}
	}
}
