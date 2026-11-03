// Package syncclient is the client side of harness.sync.v1.SyncService:
// a Connect client, a versioned document cache in ~/.harness/cache/sync/,
// a Syncer that catches up with ListChanges and follows WatchChanges
// (reconnect with backoff, resync handling, offline mode), CAS writes with
// conflict surfacing, and an Uploader that tails local JSONL transcripts
// into AppendSessionEvents.
package syncclient

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/sync/v1/syncv1connect"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/rpc"
)

// DefaultDir is the sync cache directory (~/.harness/cache/sync).
func DefaultDir() string { return filepath.Join(config.Home(), "cache", "sync") }

// Client calls SyncService with bearer auth (one refresh-and-retry on
// UNAUTHENTICATED). Errors of unary calls are *llm.APIError.
type Client struct {
	rpc    syncv1connect.SyncServiceClient
	tokens rpc.Tokens
}

// NewClient creates a client. httpClient may be nil; tokens may be nil for
// unauthenticated test servers.
func NewClient(baseURL string, httpClient *http.Client, tokens rpc.Tokens) *Client {
	if httpClient == nil {
		httpClient = rpc.HTTPClient(baseURL)
	}
	return &Client{rpc: syncv1connect.NewSyncServiceClient(httpClient, baseURL, rpc.ClientOptions()...), tokens: tokens}
}

func unary[Req, Res any](ctx context.Context, c *Client, msg *Req, call func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error)) (*Res, error) {
	resp, err := rpc.WithAuth(ctx, c.tokens, func(token string) (*connect.Response[Res], error) {
		req := connect.NewRequest(msg)
		rpc.SetBearer(req.Header(), token)
		return call(ctx, req)
	})
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c *Client) ListChanges(ctx context.Context, req *syncv1.ListChangesRequest) (*syncv1.ListChangesResponse, error) {
	return unary(ctx, c, req, c.rpc.ListChanges)
}

func (c *Client) GetDocument(ctx context.Context, req *syncv1.GetDocumentRequest) (*syncv1.GetDocumentResponse, error) {
	return unary(ctx, c, req, c.rpc.GetDocument)
}

func (c *Client) PutDocument(ctx context.Context, req *syncv1.PutDocumentRequest) (*syncv1.PutDocumentResponse, error) {
	return unary(ctx, c, req, c.rpc.PutDocument)
}

func (c *Client) BatchPutDocuments(ctx context.Context, req *syncv1.BatchPutDocumentsRequest) (*syncv1.BatchPutDocumentsResponse, error) {
	return unary(ctx, c, req, c.rpc.BatchPutDocuments)
}

func (c *Client) DeleteDocument(ctx context.Context, req *syncv1.DeleteDocumentRequest) (*syncv1.DeleteDocumentResponse, error) {
	return unary(ctx, c, req, c.rpc.DeleteDocument)
}

func (c *Client) UpsertSession(ctx context.Context, req *syncv1.UpsertSessionRequest) (*syncv1.UpsertSessionResponse, error) {
	return unary(ctx, c, req, c.rpc.UpsertSession)
}

func (c *Client) AppendSessionEvents(ctx context.Context, req *syncv1.AppendSessionEventsRequest) (*syncv1.AppendSessionEventsResponse, error) {
	return unary(ctx, c, req, c.rpc.AppendSessionEvents)
}

func (c *Client) GetSession(ctx context.Context, req *syncv1.GetSessionRequest) (*syncv1.GetSessionResponse, error) {
	return unary(ctx, c, req, c.rpc.GetSession)
}

func (c *Client) ListSessions(ctx context.Context, req *syncv1.ListSessionsRequest) (*syncv1.ListSessionsResponse, error) {
	return unary(ctx, c, req, c.rpc.ListSessions)
}

func (c *Client) GetSessionTranscript(ctx context.Context, req *syncv1.GetSessionTranscriptRequest) (*syncv1.GetSessionTranscriptResponse, error) {
	return unary(ctx, c, req, c.rpc.GetSessionTranscript)
}

func (c *Client) DeleteSession(ctx context.Context, req *syncv1.DeleteSessionRequest) (*syncv1.DeleteSessionResponse, error) {
	return unary(ctx, c, req, c.rpc.DeleteSession)
}

// watch opens a WatchChanges stream with the current token. Errors
// (including UNAUTHENTICATED) surface from the stream's Receive/Err.
func (c *Client) watch(ctx context.Context, req *syncv1.WatchChangesRequest) (*connect.ServerStreamForClient[syncv1.WatchChangesResponse], error) {
	r := connect.NewRequest(req)
	if c.tokens != nil {
		t, err := c.tokens.Token(ctx)
		if err != nil {
			return nil, err
		}
		rpc.SetBearer(r.Header(), t)
	}
	return c.rpc.WatchChanges(ctx, r)
}

func (c *Client) refresh(ctx context.Context) error {
	if c.tokens == nil {
		return nil
	}
	_, err := c.tokens.Refresh(ctx)
	return err
}

// IsNotFound reports whether err is a NOT_FOUND error from the server.
func IsNotFound(err error) bool {
	var ae *llm.APIError
	if errors.As(err, &ae) {
		return ae.Type == "not_found" || ae.Status == http.StatusNotFound
	}
	return connect.CodeOf(err) == connect.CodeNotFound
}
