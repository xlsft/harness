// Package gateway defines the client's view of the harness LLM gateway
// (docs/protocol/gateway.md) as a transport-neutral interface, plus helpers
// that turn an event stream into an accumulated response.
//
// The current adapter is gateway/httpsse (HTTPS + SSE). A ConnectRPC adapter
// only needs to implement Gateway.
package gateway

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/xlsft/harness/packages/client/llm"
)

// EventStream is an iterator over stream events. Next returns io.EOF after
// the last event. Close releases the underlying connection.
type EventStream interface {
	Next() (llm.StreamEvent, error)
	Close() error
}

// Gateway is the LLM gateway API.
type Gateway interface {
	// Stream starts a streaming messages request.
	Stream(ctx context.Context, req *llm.Request) (EventStream, error)
	// Models returns the model catalog available to the user.
	Models(ctx context.Context) (*llm.Catalog, error)
	// CountTokens counts input tokens for a request.
	CountTokens(ctx context.Context, req *llm.Request) (*llm.TokenCount, error)
}

// ErrIdleTimeout is returned when the stream is silent for too long.
var ErrIdleTimeout = errors.New("gateway stream idle timeout")

// ErrIncomplete is returned when the stream ends without message_stop.
var ErrIncomplete = errors.New("gateway stream ended before message_stop")

// Handler observes every event (including ping and harness_retry).
type Handler func(llm.StreamEvent)

// Collect drains stream into a Response. The stream is closed on return.
func Collect(stream EventStream, onEvent Handler) (llm.Response, *llm.Accumulator, error) {
	defer stream.Close()
	acc := llm.NewAccumulator()
	for {
		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			if !acc.Done() {
				return acc.Response(), acc, ErrIncomplete
			}
			return acc.Response(), acc, nil
		}
		if err != nil {
			return acc.Response(), acc, err
		}
		if onEvent != nil {
			onEvent(ev)
		}
		if err := acc.Add(ev); err != nil {
			return acc.Response(), acc, err
		}
		if acc.Done() {
			return acc.Response(), acc, nil
		}
	}
}

// RetryPolicy controls client-side retries. The server retries upstream
// itself; the client only retries transport failures and retryable gateway
// errors that happen before any content block was received.
type RetryPolicy struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	// OnRetry is called before sleeping.
	OnRetry func(attempt int, delay time.Duration, err error)
}

// DefaultRetry is used when nil is passed to Send.
var DefaultRetry = RetryPolicy{MaxRetries: 3, BaseDelay: time.Second, MaxDelay: 20 * time.Second}

// Send performs a streaming request with retries and returns the accumulated
// response. On failure after content started, the partial accumulator is
// returned so the caller can keep completed blocks.
func Send(ctx context.Context, gw Gateway, req *llm.Request, onEvent Handler, policy *RetryPolicy) (llm.Response, *llm.Accumulator, error) {
	p := DefaultRetry
	if policy != nil {
		p = *policy
	}
	var lastAcc *llm.Accumulator
	for attempt := 0; ; attempt++ {
		var (
			resp llm.Response
			acc  *llm.Accumulator
			err  error
		)
		stream, err := gw.Stream(ctx, req)
		if err == nil {
			resp, acc, err = Collect(stream, onEvent)
			lastAcc = acc
			if err == nil {
				return resp, acc, nil
			}
		}
		if ctx.Err() != nil {
			return resp, lastAcc, ctx.Err()
		}
		if acc != nil && acc.SawContent() {
			return resp, acc, err
		}
		if attempt >= p.MaxRetries || !retryable(err) {
			return resp, lastAcc, err
		}
		delay := p.BaseDelay << attempt
		var ae *llm.APIError
		if errors.As(err, &ae) && ae.RetryAfterMs > 0 {
			delay = time.Duration(ae.RetryAfterMs) * time.Millisecond
		}
		if p.MaxDelay > 0 && delay > p.MaxDelay {
			delay = p.MaxDelay
		}
		if p.OnRetry != nil {
			p.OnRetry(attempt+1, delay, err)
		}
		select {
		case <-ctx.Done():
			return resp, lastAcc, ctx.Err()
		case <-time.After(delay):
		}
	}
}

func retryable(err error) bool {
	var ae *llm.APIError
	if errors.As(err, &ae) {
		if ae.Type == "quota_exceeded" || ae.Type == "authentication" || ae.Type == "invalid_request" || ae.Type == "permission" {
			return false
		}
		return ae.Retryable()
	}
	// transport errors, idle timeouts and truncated streams are retried
	return true
}
