package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

// A minimal Chrome DevTools Protocol client: JSON messages over a pipe (--remote-debugging-pipe, NUL-delimited)
// or a WebSocket (--remote-debugging-port), flat sessions (Target.attachToTarget flatten), events delivered in
// order on one goroutine. Only the handful of domains the browser uses are spoken, by method name.

// transport moves whole CDP messages.
type transport interface {
	send(msg []byte) error
	recv() ([]byte, error)
	close() error
}

// pipeTransport speaks --remote-debugging-pipe: Chrome reads from fd 3 and writes to fd 4, each message ending
// with a NUL byte.
type pipeTransport struct {
	w  io.WriteCloser
	r  *bufio.Reader
	rc io.Closer
	mu sync.Mutex
}

func newPipeTransport(w io.WriteCloser, r io.ReadCloser) *pipeTransport {
	return &pipeTransport{w: w, r: bufio.NewReaderSize(r, 1<<20), rc: r}
}

func (p *pipeTransport) send(msg []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.w.Write(append(msg, 0)); err != nil {
		return err
	}
	return nil
}

func (p *pipeTransport) recv() ([]byte, error) {
	b, err := p.r.ReadBytes(0)
	if err != nil {
		return nil, err
	}
	return b[:len(b)-1], nil
}

func (p *pipeTransport) close() error {
	_ = p.w.Close()
	return p.rc.Close()
}

// wsTransport speaks the DevTools WebSocket.
type wsTransport struct {
	c *websocket.Conn
}

func dialWS(ctx context.Context, url string) (*wsTransport, error) {
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(512 << 20)
	return &wsTransport{c: c}, nil
}

func (t *wsTransport) send(msg []byte) error {
	return t.c.Write(context.Background(), websocket.MessageText, msg)
}

func (t *wsTransport) recv() ([]byte, error) {
	_, b, err := t.c.Read(context.Background())
	return b, err
}

func (t *wsTransport) close() error { return t.c.CloseNow() }

// cdpError is an error answer of a CDP method.
type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *cdpError) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("%s (%s)", e.Message, e.Data)
	}
	return e.Message
}

// ErrConnClosed is returned by calls on a closed connection.
var ErrConnClosed = errors.New("the browser connection is closed")

type message struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
}

// event is a CDP event.
type event struct {
	Session string
	Method  string
	Params  json.RawMessage
}

// conn is a CDP connection to the browser.
type conn struct {
	t       transport
	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan message
	closed  chan struct{}
	err     error
	once    sync.Once

	events chan event
	queue  []event
	qmu    sync.Mutex
	qcond  *sync.Cond
}

// newConn starts reading t; onEvent is called for every event, in order, on one goroutine (it may make calls).
func newConn(t transport, onEvent func(event)) *conn {
	c := &conn{t: t, pending: map[int64]chan message{}, closed: make(chan struct{})}
	c.qcond = sync.NewCond(&c.qmu)
	go c.readLoop()
	go c.dispatch(onEvent)
	return c
}

func (c *conn) readLoop() {
	for {
		b, err := c.t.recv()
		if err != nil {
			c.shutdown(err)
			return
		}
		var m message
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		if m.ID != 0 {
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		if m.Method != "" {
			c.qmu.Lock()
			c.queue = append(c.queue, event{Session: m.SessionID, Method: m.Method, Params: m.Params})
			c.qcond.Signal()
			c.qmu.Unlock()
		}
	}
}

// dispatch delivers queued events in order. The queue is unbounded so a slow handler never stalls replies.
func (c *conn) dispatch(onEvent func(event)) {
	for {
		c.qmu.Lock()
		for len(c.queue) == 0 {
			select {
			case <-c.closed:
				c.qmu.Unlock()
				return
			default:
			}
			c.qcond.Wait()
		}
		ev := c.queue[0]
		c.queue[0] = event{}
		c.queue = c.queue[1:]
		c.qmu.Unlock()
		if onEvent != nil {
			onEvent(ev)
		}
	}
}

func (c *conn) shutdown(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		pending := c.pending
		c.pending = map[int64]chan message{}
		c.mu.Unlock()
		close(c.closed)
		for _, ch := range pending {
			ch <- message{Error: &cdpError{Message: ErrConnClosed.Error()}}
		}
		c.qmu.Lock()
		c.qcond.Broadcast()
		c.qmu.Unlock()
		_ = c.t.close()
	})
}

// Close closes the connection.
func (c *conn) Close() { c.shutdown(ErrConnClosed) }

// Done is closed when the connection ends.
func (c *conn) Done() <-chan struct{} { return c.closed }

// Call invokes method on a session ("" for the browser target) and decodes the result into out (may be nil).
func (c *conn) Call(ctx context.Context, session, method string, params, out any) error {
	select {
	case <-c.closed:
		return ErrConnClosed
	default:
	}
	id := c.nextID.Add(1)
	m := message{ID: id, Method: method, SessionID: session}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		m.Params = b
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	ch := make(chan message, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.t.send(b); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return fmt.Errorf("%s: %w", method, r.Error)
		}
		if out != nil && len(r.Result) > 0 {
			if err := json.Unmarshal(r.Result, out); err != nil {
				return fmt.Errorf("%s: decoding the result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}
