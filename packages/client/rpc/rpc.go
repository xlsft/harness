// Package rpc holds the shared ConnectRPC plumbing of the client's server
// adapters: HTTP/2 transports (TLS or h2c), client options, bearer auth with
// one refresh-and-retry on UNAUTHENTICATED, and error mapping from
// harness.common.v1.ErrorInfo to *llm.APIError.
package rpc

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	"github.com/xlsft/harness/packages/client/llm"
)

// Tokens supplies bearer tokens (implemented by auth.Manager and auth.StaticToken).
type Tokens interface {
	Token(ctx context.Context) (string, error)
	Refresh(ctx context.Context) (string, error)
}

// HTTPClient returns an HTTP client for baseURL. With the default Connect
// protocol it speaks HTTP/1.1 or HTTP/2 (negotiated over TLS), which works on
// the server's main port and behind ordinary reverse proxies. With the gRPC
// protocol (HARNESS_RPC_PROTOCOL=grpc) it requires HTTP/2: TLS for https://,
// h2c with prior knowledge for http://.
func HTTPClient(baseURL string) *http.Client {
	t := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second,
		ForceAttemptHTTP2:   true,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
	}
	p := new(http.Protocols)
	switch {
	case useGRPC() && strings.HasPrefix(baseURL, "http://"):
		p.SetUnencryptedHTTP2(true)
	case useGRPC():
		p.SetHTTP2(true)
	default:
		p.SetHTTP1(true)
		p.SetHTTP2(true)
	}
	t.Protocols = p
	return &http.Client{Transport: t}
}

// useGRPC reports whether HARNESS_RPC_PROTOCOL selects plain gRPC instead of
// the default Connect protocol. Plain gRPC needs the server's h2c listener
// (HARNESS_GRPC_PORT on core); the main port serves Connect and gRPC-Web only.
func useGRPC() bool {
	return strings.EqualFold(os.Getenv("HARNESS_RPC_PROTOCOL"), "grpc")
}

// UnixHTTPClient returns an h2c client that dials a unix socket for any URL.
func UnixHTTPClient(socket string) *http.Client {
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	t := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		Protocols: p,
	}
	return &http.Client{Transport: t}
}

// ClientOptions are the Connect options used for all server calls: the
// Connect protocol (or gRPC when HARNESS_RPC_PROTOCOL=grpc) and the harness
// User-Agent.
func ClientOptions() []connect.ClientOption {
	opts := []connect.ClientOption{connect.WithInterceptors(userAgent{})}
	if useGRPC() {
		opts = append(opts, connect.WithGRPC())
	}
	return opts
}

type userAgent struct{}

func (userAgent) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("User-Agent", config.UserAgent())
		return next(ctx, req)
	}
}

func (userAgent) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("User-Agent", config.UserAgent())
		return conn
	}
}

func (userAgent) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// SetBearer sets the Authorization header.
func SetBearer(h http.Header, token string) {
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
}

// WithAuth runs call with a bearer token and, if it fails with
// UNAUTHENTICATED, refreshes the token once and retries.
func WithAuth[T any](ctx context.Context, tokens Tokens, call func(token string) (T, error)) (T, error) {
	var zero T
	token := ""
	if tokens != nil {
		t, err := tokens.Token(ctx)
		if err != nil {
			return zero, err
		}
		token = t
	}
	out, err := call(token)
	if err != nil && tokens != nil && connect.CodeOf(err) == connect.CodeUnauthenticated {
		t, rerr := tokens.Refresh(ctx)
		if rerr != nil {
			return zero, rerr
		}
		out, err = call(t)
	}
	if err != nil {
		return zero, ToAPIError(err)
	}
	return out, nil
}

var errorTypeNames = map[commonv1.ErrorType]string{
	commonv1.ErrorType_ERROR_TYPE_INVALID_ARGUMENT:   "invalid_request",
	commonv1.ErrorType_ERROR_TYPE_UNAUTHENTICATED:    "authentication",
	commonv1.ErrorType_ERROR_TYPE_PERMISSION_DENIED:  "permission",
	commonv1.ErrorType_ERROR_TYPE_NOT_FOUND:          "not_found",
	commonv1.ErrorType_ERROR_TYPE_RATE_LIMITED:       "rate_limit",
	commonv1.ErrorType_ERROR_TYPE_QUOTA_EXCEEDED:     "quota_exceeded",
	commonv1.ErrorType_ERROR_TYPE_OVERLOADED:         "overloaded",
	commonv1.ErrorType_ERROR_TYPE_UPSTREAM:           "upstream",
	commonv1.ErrorType_ERROR_TYPE_UNSUPPORTED_CLIENT: "unsupported_client",
	commonv1.ErrorType_ERROR_TYPE_INTERNAL:           "internal",
	commonv1.ErrorType_ERROR_TYPE_CONFLICT:           "conflict",
}

var codeTypeNames = map[connect.Code]string{
	connect.CodeInvalidArgument:    "invalid_request",
	connect.CodeUnauthenticated:    "authentication",
	connect.CodePermissionDenied:   "permission",
	connect.CodeNotFound:           "not_found",
	connect.CodeResourceExhausted:  "rate_limit",
	connect.CodeUnavailable:        "overloaded",
	connect.CodeFailedPrecondition: "unsupported_client",
	connect.CodeAborted:            "conflict",
	connect.CodeInternal:           "internal",
	connect.CodeUnknown:            "internal",
	connect.CodeDeadlineExceeded:   "internal",
}

// ErrorInfo extracts the harness ErrorInfo detail of a Connect error.
func ErrorInfo(err error) *commonv1.ErrorInfo {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return nil
	}
	for _, d := range ce.Details() {
		v, derr := d.Value()
		if derr != nil {
			continue
		}
		if info, ok := v.(*commonv1.ErrorInfo); ok {
			return info
		}
	}
	return nil
}

// ToAPIError converts Connect errors into *llm.APIError; other errors
// (context cancellation, transport) are returned unchanged.
func ToAPIError(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if !errors.As(err, &ce) || errors.Is(err, context.Canceled) {
		return err
	}
	if ce.Code() == connect.CodeCanceled {
		return context.Canceled
	}
	ae := &llm.APIError{Type: codeTypeNames[ce.Code()], Message: ce.Message(), Status: httpStatus(ce.Code())}
	if ae.Type == "" {
		ae.Type = "internal"
	}
	if info := ErrorInfo(err); info != nil {
		if n, ok := errorTypeNames[info.GetType()]; ok {
			ae.Type = n
		}
		ae.RetryAfterMs = int(info.GetRetryAfterMs())
	}
	return ae
}

func httpStatus(c connect.Code) int {
	switch c {
	case connect.CodeInvalidArgument:
		return 400
	case connect.CodeUnauthenticated:
		return 401
	case connect.CodePermissionDenied:
		return 403
	case connect.CodeNotFound:
		return 404
	case connect.CodeResourceExhausted:
		return 429
	case connect.CodeUnavailable:
		return 503
	case connect.CodeFailedPrecondition:
		return 412
	}
	return 500
}

// NewError builds a Connect error with an ErrorInfo detail (used by servers
// and test fakes).
func NewError(code connect.Code, typ commonv1.ErrorType, msg string, retryAfterMs int64) *connect.Error {
	ce := connect.NewError(code, errors.New(msg))
	if d, err := connect.NewErrorDetail(&commonv1.ErrorInfo{Type: typ, RetryAfterMs: retryAfterMs}); err == nil {
		ce.AddDetail(d)
	}
	return ce
}
