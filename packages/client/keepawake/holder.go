// Package keepawake holds off the computer's sleep while a session that asked for it is working. One Holder serves
// the whole client: sessions turn the switch on and report when they are busy, and the operating system's inhibitor
// runs as long as one session has both. The Tools menu's toggle and the KeepAwake tool change the same switch.
package keepawake

import "sync"

// Inhibitor starts holding off sleep; release lets the computer sleep again.
type Inhibitor func() (release func(), err error)

type sessionState struct{ want, busy bool }

// Holder is the switch of every session on this machine.
type Holder struct {
	start Inhibitor
	// OnError receives inhibitor failures no caller is waiting for (a session became busy); may be nil.
	OnError func(error)

	mu       sync.Mutex
	sessions map[string]*sessionState
	release  func()
}

// New returns a Holder using start (nil: the platform's inhibitor).
func New(start Inhibitor) *Holder {
	if start == nil {
		start = Inhibit
	}
	return &Holder{start: start, sessions: map[string]*sessionState{}}
}

func (h *Holder) stateLocked(id string) *sessionState {
	st := h.sessions[id]
	if st == nil {
		st = &sessionState{}
		h.sessions[id] = st
	}
	return st
}

// Set turns the switch of a session on or off. A failure to hold off sleep right now leaves it off.
func (h *Holder) Set(id string, on bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.stateLocked(id)
	st.want = on
	if err := h.applyLocked(); err != nil {
		st.want = false
		return err
	}
	return nil
}

// SetBusy reports whether the session is working.
func (h *Holder) SetBusy(id string, busy bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stateLocked(id).busy = busy
	if err := h.applyLocked(); err != nil && h.OnError != nil {
		h.OnError(err)
	}
}

// Wanted reports whether the switch of the session is on.
func (h *Holder) Wanted(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.sessions[id]
	return st != nil && st.want
}

// Holding reports whether sleep is held off right now.
func (h *Holder) Holding() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.release != nil
}

// Forget drops a closed session: what it held is released.
func (h *Holder) Forget(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, id)
	if err := h.applyLocked(); err != nil && h.OnError != nil {
		h.OnError(err)
	}
}

func (h *Holder) applyLocked() error {
	needed := false
	for _, st := range h.sessions {
		needed = needed || (st.want && st.busy)
	}
	switch {
	case needed && h.release == nil:
		release, err := h.start()
		if err != nil {
			return err
		}
		h.release = release
	case !needed && h.release != nil:
		h.release()
		h.release = nil
	}
	return nil
}
