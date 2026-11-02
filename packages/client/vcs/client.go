package vcs

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"connectrpc.com/connect"

	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/vcs/v1/vcsv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
)

// ErrUnsupported wraps errors of a server that does not implement the device provider RPCs.
var ErrUnsupported = errors.New("server does not support device providers")

// Reporter is the part of VcsService the device providers upload to (implemented by *Client).
type Reporter interface {
	ReportDeviceProviders(ctx context.Context, req *vcsv1.ReportDeviceProvidersRequest) (*vcsv1.ReportDeviceProvidersResponse, error)
	ReportDeviceSnapshot(ctx context.Context, req *vcsv1.ReportDeviceSnapshotRequest) (*vcsv1.ReportDeviceSnapshotResponse, error)
}

// Client calls VcsService with bearer auth (one refresh-and-retry on UNAUTHENTICATED). Errors are
// *llm.APIError (see rpc.ToAPIError).
type Client struct {
	rpc    vcsv1connect.VcsServiceClient
	tokens rpc.Tokens
}

// NewClient creates a client. httpClient may be nil; tokens may be nil for unauthenticated test
// servers.
func NewClient(baseURL string, httpClient *http.Client, tokens rpc.Tokens) *Client {
	if httpClient == nil {
		httpClient = rpc.HTTPClient(baseURL)
	}
	return &Client{rpc: vcsv1connect.NewVcsServiceClient(httpClient, baseURL, rpc.ClientOptions()...), tokens: tokens}
}

func unary[Req, Res any](ctx context.Context, c *Client, msg *Req, call func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error)) (*Res, error) {
	var code connect.Code
	resp, err := rpc.WithAuth(ctx, c.tokens, func(token string) (*connect.Response[Res], error) {
		req := connect.NewRequest(msg)
		rpc.SetBearer(req.Header(), token)
		r, err := call(ctx, req)
		code = connect.CodeOf(err)
		return r, err
	})
	if err != nil {
		if code == connect.CodeUnimplemented {
			return nil, fmt.Errorf("%w: %w", ErrUnsupported, err)
		}
		return nil, err
	}
	return resp.Msg, nil
}

func (c *Client) ReportDeviceProviders(ctx context.Context, req *vcsv1.ReportDeviceProvidersRequest) (*vcsv1.ReportDeviceProvidersResponse, error) {
	return unary(ctx, c, req, c.rpc.ReportDeviceProviders)
}

func (c *Client) ReportDeviceSnapshot(ctx context.Context, req *vcsv1.ReportDeviceSnapshotRequest) (*vcsv1.ReportDeviceSnapshotResponse, error) {
	return unary(ctx, c, req, c.rpc.ReportDeviceSnapshot)
}
