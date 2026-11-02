// Package connectgw is the ConnectRPC (gRPC over HTTP/2) adapter of the
// gateway interface, backed by harness.llm.v1.GatewayService.
package connectgw

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/gateway"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/llm/v1/llmv1connect"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/rpc"
)

// DefaultIdleTimeout drops a stream after 5 minutes without any event.
const DefaultIdleTimeout = 5 * time.Minute

// Client implements gateway.Gateway.
type Client struct {
	rpc         llmv1connect.GatewayServiceClient
	tokens      rpc.Tokens
	IdleTimeout time.Duration
}

var _ gateway.Gateway = (*Client)(nil)

// New creates a client for baseURL. httpClient may be nil (HTTP/2 client
// chosen from the URL scheme).
func New(baseURL string, httpClient *http.Client, tokens rpc.Tokens) *Client {
	if httpClient == nil {
		httpClient = rpc.HTTPClient(baseURL)
	}
	return &Client{rpc: llmv1connect.NewGatewayServiceClient(httpClient, baseURL, rpc.ClientOptions()...), tokens: tokens}
}

// Models implements gateway.Gateway.
func (c *Client) Models(ctx context.Context) (*llm.Catalog, error) {
	resp, err := rpc.WithAuth(ctx, c.tokens, func(token string) (*connect.Response[llmv1.ListModelsResponse], error) {
		req := connect.NewRequest(&llmv1.ListModelsRequest{})
		rpc.SetBearer(req.Header(), token)
		return c.rpc.ListModels(ctx, req)
	})
	if err != nil {
		return nil, err
	}
	return pbconv.FromCatalog(resp.Msg), nil
}

// CountTokens implements gateway.Gateway.
func (c *Client) CountTokens(ctx context.Context, r *llm.Request) (*llm.TokenCount, error) {
	resp, err := rpc.WithAuth(ctx, c.tokens, func(token string) (*connect.Response[llmv1.CountTokensResponse], error) {
		req := connect.NewRequest(pbconv.CountRequest(r))
		rpc.SetBearer(req.Header(), token)
		return c.rpc.CountTokens(ctx, req)
	})
	if err != nil {
		return nil, err
	}
	return &llm.TokenCount{InputTokens: int(resp.Msg.GetInputTokens()), Estimated: resp.Msg.GetEstimated()}, nil
}

// Stream implements gateway.Gateway. UNAUTHENTICATED on the first receive
// triggers one token refresh and a retry; once events flow the stream is
// never restarted.
func (c *Client) Stream(ctx context.Context, r *llm.Request) (gateway.EventStream, error) {
	msg := pbconv.StreamRequest(r)
	idle := c.IdleTimeout
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	open := func(token string) (*stream, error) {
		sctx, cancel := context.WithCancelCause(ctx)
		req := connect.NewRequest(msg)
		rpc.SetBearer(req.Header(), token)
		s, err := c.rpc.StreamMessage(sctx, req)
		if err != nil {
			cancel(nil)
			return nil, err
		}
		st := &stream{s: s, ctx: sctx, cancel: cancel, idle: idle}
		st.timer = time.AfterFunc(idle, func() { cancel(gateway.ErrIdleTimeout) })
		// Peek the first message so auth failures surface here.
		if !s.Receive() {
			err := s.Err()
			st.Close()
			if err == nil {
				return nil, gateway.ErrIncomplete
			}
			return nil, st.mapErr(err)
		}
		st.timer.Reset(idle)
		st.peeked = true
		return st, nil
	}
	token := ""
	if c.tokens != nil {
		t, err := c.tokens.Token(ctx)
		if err != nil {
			return nil, err
		}
		token = t
	}
	st, err := open(token)
	if err != nil {
		var ae *llm.APIError
		if c.tokens != nil && errors.As(err, &ae) && ae.Type == "authentication" {
			t, rerr := c.tokens.Refresh(ctx)
			if rerr != nil {
				return nil, rerr
			}
			return open(t)
		}
		return nil, err
	}
	return st, nil
}

type stream struct {
	s      *connect.ServerStreamForClient[llmv1.StreamMessageResponse]
	ctx    context.Context
	cancel context.CancelCauseFunc
	timer  *time.Timer
	idle   time.Duration
	peeked bool
	once   sync.Once
}

func (s *stream) mapErr(err error) error {
	if cause := context.Cause(s.ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return rpc.ToAPIError(err)
}

func (s *stream) Next() (llm.StreamEvent, error) {
	for {
		if s.peeked {
			s.peeked = false
		} else if !s.s.Receive() {
			if err := s.s.Err(); err != nil {
				return llm.StreamEvent{}, s.mapErr(err)
			}
			if cause := context.Cause(s.ctx); cause != nil {
				return llm.StreamEvent{}, cause
			}
			return llm.StreamEvent{}, io.EOF
		}
		s.timer.Reset(s.idle)
		ev := pbconv.StreamEvent(s.s.Msg())
		if ev.Type == "" {
			continue // unknown future event type
		}
		return ev, nil
	}
}

func (s *stream) Close() error {
	s.once.Do(func() {
		s.timer.Stop()
		s.cancel(nil)
		_ = s.s.Close()
	})
	return nil
}
