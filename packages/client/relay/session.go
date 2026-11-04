package relay

import (
	"context"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
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
	lastSent   int64 // highest local durable seq sent upstream (maybe stored without an answer)
	superseded bool
	stop       chan struct{}
	// hostID is the host the current epoch was registered with.
	hostID string
	// removed is set when remote control was turned off for the session (Host.RemoveSession): run stops,
	// unregisters the session and closes done.
	removed bool
	quit    chan struct{}
	done    chan struct{}
}

func newHostSession(h *Host, id string, feed Feed) *hostSession {
	return &hostSession{h: h, id: id, feed: feed, cmds: make(chan *relayv1.Command, 256), stop: make(chan struct{}),
		quit: make(chan struct{}), done: make(chan struct{})}
}

// remove stops mirroring the session (remote control turned off for it).
func (hs *hostSession) remove() {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	if !hs.removed {
		hs.removed = true
		close(hs.quit)
	}
}

// inherit takes over the seq mapping of an earlier mirror of the same session in this process (remote control
// turned off and on again), so the server's seq keeps counting where it stopped and the events of the pause are
// published on the next registration.
func (hs *hostSession) inherit(prev *hostSession) {
	prev.mu.Lock()
	base, baseSet, last, sent := prev.base, prev.baseSet, prev.lastLocal, prev.lastSent
	prev.mu.Unlock()
	hs.mu.Lock()
	hs.base, hs.baseSet, hs.lastLocal, hs.lastSent = base, baseSet, last, sent
	hs.mu.Unlock()
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

func (hs *hostSession) isRemoved() bool {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	return hs.removed
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
		// A resumed session's log starts with the history replayed from its transcript;
		// when the server already stores events for the session it has that history, so
		// the replay is skipped and the first live event follows acked_seq.
		var skip int64
		if hf, ok := hs.feed.(historyFeed); ok && acked > 0 {
			skip = hf.HistoryLen()
		}
		hs.base, hs.baseSet = acked-skip, true
		hs.lastLocal = max(hs.lastLocal, skip)
	case acked > hs.base+hs.lastLocal && acked <= hs.base+hs.lastSent:
		// Our own events were stored but the answer was lost (a call cancelled or timed out).
		hs.lastLocal = acked - hs.base
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
// superseded, removed or ctx is done. A removed session is unregistered.
func (hs *hostSession) run(ctx context.Context) {
	defer close(hs.done)
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// qctx also ends when the session is removed: waits end at once, while calls in flight (a registration, a
	// publish) complete, so the epoch to unregister and the acknowledged seq stay known.
	qctx, qcancel := context.WithCancel(ctx)
	defer qcancel()
	go func() {
		select {
		case <-hs.quit:
			qcancel()
		case <-qctx.Done():
		}
	}()
	unregistered := false
	defer func() {
		hs.mu.Lock()
		removed, superseded, hostID, epoch := hs.removed, hs.superseded, hs.hostID, hs.epoch
		hs.mu.Unlock()
		if !removed || superseded || unregistered || epoch == 0 || parent.Err() != nil {
			return
		}
		uctx, c := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer c()
		if _, err := call(uctx, hs.h, hs.h.rpc.UnregisterSession, &relayv1.UnregisterSessionRequest{HostId: hostID, SessionId: hs.id, Epoch: epoch}); err != nil {
			hs.h.log.Debug("unregister session failed", "session", hs.id, "err", err)
		}
	}()
	go hs.worker(ctx)
	h := hs.h
	attempt := 0
	for qctx.Err() == nil && !hs.isSuperseded() {
		if attempt > 0 && !sleep(qctx, h.backoff(attempt-1)) {
			return
		}
		attempt++
		hostID, err := h.waitHost(qctx)
		if err != nil {
			return
		}
		res, err := call(ctx, h, h.rpc.RegisterSession, &relayv1.RegisterSessionRequest{HostId: hostID, Session: hs.feed.Info()})
		if err != nil {
			h.log.Debug("register session failed", "session", hs.id, "err", err)
			continue
		}
		epoch := res.GetEpoch()
		hs.mu.Lock()
		hs.hostID = hostID
		hs.mu.Unlock()
		after := hs.rebase(epoch, res.GetAckedSeq())
		rewinds := 0
		for {
			reason, acked := hs.publish(ctx, hostID, epoch, after)
			switch reason {
			case reasonEnded:
				uctx, c := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				_, _ = call(uctx, h, h.rpc.UnregisterSession, &relayv1.UnregisterSessionRequest{HostId: hostID, SessionId: hs.id, Epoch: epoch})
				c()
				unregistered = true
				return
			case reasonStop, reasonSuperseded:
				return
			case reasonRewind:
				hs.mu.Lock()
				next := max(acked-hs.base, 0)
				hs.mu.Unlock()
				if next <= after {
					rewinds++
					if !sleep(qctx, h.backoff(min(rewinds, 6))) {
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
	scrubDeviceLocal(c, hs.h.o.Objects, hs.id)
	c.SessionId = hs.id
	c.Seq = base + ev.GetSeq()
	id := uuid.NewString()
	if !isDelta(ev) {
		id = uuid.NewSHA1(uuidNamespace, []byte(hs.h.o.InstanceID+"/"+hs.id+"/"+strconv.FormatInt(ev.GetSeq(), 10))).String()
	}
	return &relayv1.EventEnvelope{Uuid: id, Event: c}
}

// ObjectSink takes the bytes of an event's images and documents to the object store (syncclient.ObjectQueue).
type ObjectSink interface {
	Available() bool
	AddBytes(sessionID, kind, name, mediaType string, data []byte) (*commonv1.ObjectRef, error)
}

// referObjects replaces the images and documents in blocks by references to the store, so events stay small and viewers
// fetch them. A block that cannot be queued goes as a sentence saying so.
func referObjects(blocks []*llmv1.ContentBlock, sink ObjectSink, sessionID, kind string) {
	for _, b := range blocks {
		var mediaType, name string
		var data []byte
		switch v := b.GetBlock().(type) {
		case *llmv1.ContentBlock_Image:
			mediaType, data = v.Image.GetMediaType(), v.Image.GetData()
		case *llmv1.ContentBlock_Document:
			mediaType, name, data = v.Document.GetMediaType(), v.Document.GetTitle(), v.Document.GetData()
		default:
			continue
		}
		ref, err := sink.AddBytes(sessionID, kind, name, mediaType, data)
		text := &llmv1.TextBlock{Text: "[a file kept on the machine that made it]"}
		if err == nil {
			text = &llmv1.TextBlock{Text: "[" + mediaType + " stored as object " + ref.GetSha256() + "]", Object: ref}
		}
		b.Block = &llmv1.ContentBlock_Text{Text: text}
	}
}

// scrubDeviceLocal takes computer use off an event going to the server: its cards and state are empty there. An emptied
// event keeps its seq, so viewers see no gap in the log. With an object store, the images and documents of tool results
// and user messages go there, and the event carries references; without one, computer use screenshots stay on this
// machine and the rest travels inline.
func scrubDeviceLocal(c *agentv1.SessionEvent, sink ObjectSink, sessionID string) {
	stored := sink != nil && sink.Available()
	switch e := c.GetEvent().(type) {
	case *agentv1.SessionEvent_ComputerAccessRequested, *agentv1.SessionEvent_ComputerAccessResolved, *agentv1.SessionEvent_ComputerUseState:
		c.Event = nil
	case *agentv1.SessionEvent_UserMessage:
		if stored {
			referObjects(e.UserMessage.GetContent(), sink, sessionID, "attachment")
		}
	case *agentv1.SessionEvent_ToolCallFinished:
		tf := e.ToolCallFinished
		if stored {
			referObjects(tf.GetContent(), sink, sessionID, "image")
			return
		}
		if !strings.Contains(tf.GetStructuredResultJson(), `"computer_use":true`) {
			return
		}
		tf.Content = slices.DeleteFunc(tf.GetContent(), func(b *llmv1.ContentBlock) bool { return b.GetImage() != nil })
	}
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
		hs.mu.Lock()
		hs.lastSent = max(hs.lastSent, batchLocal)
		hs.mu.Unlock()
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
			if ctx.Err() != nil || hs.isRemoved() {
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
		case <-hs.quit:
			return reasonStop, 0
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
