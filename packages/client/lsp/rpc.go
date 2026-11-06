package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// maxMessageBytes bounds one message from a server; a larger one ends the connection.
	maxMessageBytes = 64 << 20
	// cancelNoticeWait bounds telling a server that a request is abandoned.
	cancelNoticeWait = 2 * time.Second
)

// JSON-RPC and LSP error codes the client looks at.
const (
	codeMethodNotFound   = -32601
	codeRequestCancelled = -32800
	codeContentModified  = -32801
	codeServerCancelled  = -32802
)

// errConnClosed reports that the server is gone: its process ended or its stream broke.
var errConnClosed = errors.New("language server connection closed")

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

// conn speaks JSON-RPC with Content-Length framing. Writes go through a queue, so a server that stopped reading its
// stdin can block neither a caller past its context nor the read loop.
type conn struct {
	out       chan []byte
	closed    chan struct{}
	closeOnce sync.Once
	// onNotify and onRequest run on the read loop and must not wait for the server.
	onNotify  func(method string, params json.RawMessage)
	onRequest func(method string, params json.RawMessage) (any, *rpcError)

	mu      sync.Mutex
	next    int64
	pending map[int64]chan rpcResult
}

func newConn(w io.Writer, r io.Reader, onNotify func(string, json.RawMessage), onRequest func(string, json.RawMessage) (any, *rpcError)) *conn {
	c := &conn{out: make(chan []byte, 64), closed: make(chan struct{}), onNotify: onNotify, onRequest: onRequest, pending: map[int64]chan rpcResult{}}
	go c.writeLoop(w)
	go c.readLoop(r)
	return c
}

func (c *conn) close() {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		close(c.closed)
		for id, ch := range c.pending {
			ch <- rpcResult{err: errConnClosed}
			delete(c.pending, id)
		}
	})
}

func (c *conn) writeLoop(w io.Writer) {
	for {
		select {
		case <-c.closed:
			return
		case data := <-c.out:
			if _, err := w.Write(data); err != nil {
				c.close()
				return
			}
		}
	}
}

func (c *conn) readLoop(r io.Reader) {
	defer c.close()
	br := bufio.NewReader(r)
	for {
		body, err := readFrame(br)
		if err != nil {
			return
		}
		var m rpcMessage
		if json.Unmarshal(body, &m) != nil {
			continue
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			c.answer(m)
		case m.Method != "":
			if c.onNotify != nil {
				c.onNotify(m.Method, m.Params)
			}
		case len(m.ID) > 0:
			c.deliver(m)
		}
	}
}

func readFrame(br *bufio.Reader) ([]byte, error) {
	size := -1
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			if size, err = strconv.Atoi(strings.TrimSpace(v)); err != nil {
				return nil, fmt.Errorf("bad Content-Length %q", v)
			}
		}
	}
	if size < 0 || size > maxMessageBytes {
		return nil, fmt.Errorf("bad message size %d", size)
	}
	body := make([]byte, size)
	_, err := io.ReadFull(br, body)
	return body, err
}

// answer replies to a request from the server. The reply waits in its own goroutine: the read loop must keep going
// while the write queue is full.
func (c *conn) answer(m rpcMessage) {
	result, rerr := any(nil), &rpcError{Code: codeMethodNotFound, Message: "not supported: " + m.Method}
	if c.onRequest != nil {
		result, rerr = c.onRequest(m.Method, m.Params)
	}
	reply := map[string]any{"jsonrpc": "2.0", "id": m.ID}
	if rerr != nil {
		reply["error"] = rerr
	} else {
		reply["result"] = result
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), cancelNoticeWait)
		defer cancel()
		_ = c.send(ctx, reply)
	}()
}

func (c *conn) deliver(m rpcMessage) {
	id, err := strconv.ParseInt(string(m.ID), 10, 64)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.pending[id]
	if !ok {
		return
	}
	delete(c.pending, id)
	if m.Error != nil {
		ch <- rpcResult{err: m.Error}
	} else {
		ch <- rpcResult{result: m.Result}
	}
}

func (c *conn) send(ctx context.Context, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	frame := append([]byte("Content-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"), body...)
	select {
	case c.out <- frame:
		return nil
	case <-c.closed:
		return errConnClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// call sends a request and waits for its response; when ctx ends first the server is told to cancel it. A nil out
// discards the result.
func (c *conn) call(ctx context.Context, method string, params, out any) error {
	ch := make(chan rpcResult, 1)
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return errConnClosed
	default:
	}
	c.next++
	id := c.next
	c.pending[id] = ch
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if err := c.send(ctx, msg); err != nil {
		forget()
		return err
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if out == nil || len(r.result) == 0 {
			return nil
		}
		return json.Unmarshal(r.result, out)
	case <-ctx.Done():
		forget()
		go func() {
			notice, cancel := context.WithTimeout(context.Background(), cancelNoticeWait)
			defer cancel()
			_ = c.notify(notice, "$/cancelRequest", map[string]any{"id": id})
		}()
		return ctx.Err()
	}
}

func (c *conn) notify(ctx context.Context, method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	return c.send(ctx, msg)
}
