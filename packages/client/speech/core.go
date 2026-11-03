package speech

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	speechv1 "github.com/xlsft/harness/packages/client/gen/harness/speech/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/speech/v1/speechv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
)

// Core is the client of the server's harness.speech.v1.SpeechService (global providers). Errors keep their
// Connect codes, so the frontends see what the server said.
type Core struct {
	rpc    speechv1connect.SpeechServiceClient
	tokens rpc.Tokens
}

// NewCore creates the client. httpClient may be nil.
func NewCore(baseURL string, httpClient *http.Client, tokens rpc.Tokens) *Core {
	if httpClient == nil {
		httpClient = rpc.HTTPClient(baseURL)
	}
	return &Core{rpc: speechv1connect.NewSpeechServiceClient(httpClient, strings.TrimRight(baseURL, "/"), rpc.ClientOptions()...), tokens: tokens}
}

// withAuth calls with a bearer token and retries once with a refreshed one on UNAUTHENTICATED.
func withAuth[T any](ctx context.Context, tokens rpc.Tokens, call func(token string) (T, error)) (T, error) {
	var zero T
	token := ""
	if tokens != nil {
		t, err := tokens.Token(ctx)
		if err != nil {
			return zero, connect.NewError(connect.CodeUnauthenticated, err)
		}
		token = t
	}
	out, err := call(token)
	if err != nil && tokens != nil && connect.CodeOf(err) == connect.CodeUnauthenticated {
		t, rerr := tokens.Refresh(ctx)
		if rerr != nil {
			return zero, connect.NewError(connect.CodeUnauthenticated, rerr)
		}
		out, err = call(t)
	}
	return out, err
}

func req[T any](msg *T, token string) *connect.Request[T] {
	r := connect.NewRequest(msg)
	rpc.SetBearer(r.Header(), token)
	return r
}

// ListProviders lists the server's providers.
func (c *Core) ListProviders(ctx context.Context, kind speechv1.SpeechKind) (*speechv1.ListProvidersResponse, error) {
	res, err := withAuth(ctx, c.tokens, func(t string) (*connect.Response[speechv1.ListProvidersResponse], error) {
		return c.rpc.ListProviders(ctx, req(&speechv1.ListProvidersRequest{Kind: kind}, t))
	})
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// ListVoices lists a global TTS provider's voices.
func (c *Core) ListVoices(ctx context.Context, providerID, model string) ([]*speechv1.SpeechVoice, error) {
	res, err := withAuth(ctx, c.tokens, func(t string) (*connect.Response[speechv1.ListVoicesResponse], error) {
		return c.rpc.ListVoices(ctx, req(&speechv1.ListVoicesRequest{ProviderId: providerID, Model: model}, t))
	})
	if err != nil {
		return nil, err
	}
	return res.Msg.GetVoices(), nil
}

// Transcribe runs speech to text on the server.
func (c *Core) Transcribe(ctx context.Context, in *speechv1.TranscribeRequest) (*speechv1.TranscribeResponse, error) {
	res, err := withAuth(ctx, c.tokens, func(t string) (*connect.Response[speechv1.TranscribeResponse], error) {
		return c.rpc.Transcribe(ctx, req(in, t))
	})
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// Synthesize runs text to speech on the server and calls fn with each message in order.
func (c *Core) Synthesize(ctx context.Context, in *speechv1.SynthesizeRequest, fn func(*speechv1.SynthesizeResponse) error) error {
	_, err := withAuth(ctx, c.tokens, func(t string) (struct{}, error) {
		stream, err := c.rpc.Synthesize(ctx, req(in, t))
		if err != nil {
			return struct{}{}, err
		}
		defer stream.Close()
		for stream.Receive() {
			if err := fn(stream.Msg()); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, stream.Err()
	})
	return err
}
