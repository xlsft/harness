package syncclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/rpc"
)

// Options configure a Syncer.
type Options struct {
	Client *Client
	Cache  *Cache
	// Kinds limits the feed; empty means all kinds.
	Kinds []syncv1.DocumentKind
	// Source labels writes, e.g. "client:dev_123".
	Source   string
	PageSize int32
	// MinBackoff / MaxBackoff bound the reconnect delay (defaults 1 s / 60 s).
	MinBackoff, MaxBackoff time.Duration
	// IdleTimeout reconnects a watch stream that delivered nothing (not even a
	// heartbeat) for this long. Default 60 s (three missed heartbeats).
	IdleTimeout time.Duration
	Logger      *slog.Logger
}

// Syncer keeps a Cache in sync with the server.
type Syncer struct {
	o      Options
	log    *slog.Logger
	online atomic.Bool
	// syncMu serializes catch-up / resync against each other.
	syncMu sync.Mutex
}

// New creates a Syncer.
func New(o Options) *Syncer {
	if o.MinBackoff <= 0 {
		o.MinBackoff = time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = time.Minute
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = time.Minute
	}
	if o.PageSize <= 0 {
		o.PageSize = 500
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Syncer{o: o, log: log}
}

// Cache returns the document cache.
func (s *Syncer) Cache() *Cache { return s.o.Cache }

// Client returns the SyncService client.
func (s *Syncer) Client() *Client { return s.o.Client }

// Online reports whether the last server interaction succeeded. While
// offline the cache keeps serving the last known state.
func (s *Syncer) Online() bool { return s.online.Load() }

// CatchUp pages through ListChanges from the cached cursor until the feed
// is exhausted. On resync_required it performs a full resync. When the
// server is unreachable it returns the error and the cache stays usable.
func (s *Syncer) CatchUp(ctx context.Context) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	err := s.catchUp(ctx)
	s.online.Store(err == nil)
	return err
}

func (s *Syncer) catchUp(ctx context.Context) error {
	since := s.o.Cache.Cursor()
	for {
		resp, err := s.o.Client.ListChanges(ctx, &syncv1.ListChangesRequest{SinceVersion: since, Kinds: s.o.Kinds, PageSize: s.o.PageSize})
		if err != nil {
			return err
		}
		if resp.GetResyncRequired() {
			return s.fullResync(ctx)
		}
		next := max(resp.GetNextVersion(), since)
		if err := s.o.Cache.Apply(resp.GetChanges(), next, !resp.GetHasMore()); err != nil {
			return err
		}
		if !resp.GetHasMore() {
			return nil
		}
		if next == since && len(resp.GetChanges()) == 0 {
			return errors.New("sync: ListChanges reported has_more without progress")
		}
		since = next
	}
}

// Resync drops the cursor and rebuilds the cache from a full snapshot.
func (s *Syncer) Resync(ctx context.Context) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	err := s.fullResync(ctx)
	s.online.Store(err == nil)
	return err
}

func (s *Syncer) fullResync(ctx context.Context) error {
	docs := map[string]*syncv1.Document{}
	collect := func(chs []*syncv1.Change) {
		for _, ch := range chs {
			switch v := ch.GetChange().(type) {
			case *syncv1.Change_Document:
				docs[v.Document.GetId()] = v.Document
			case *syncv1.Change_Tombstone:
				delete(docs, v.Tombstone.GetId())
			}
		}
	}
	install := func(version int64) error {
		list := make([]*syncv1.Document, 0, len(docs))
		for _, d := range docs {
			list = append(list, d)
		}
		s.log.Info("sync: full resync", "documents", len(list), "version", version)
		return s.o.Cache.Replace(list, version)
	}
	var since int64
	for {
		resp, err := s.o.Client.ListChanges(ctx, &syncv1.ListChangesRequest{SinceVersion: since, Kinds: s.o.Kinds, PageSize: s.o.PageSize})
		if err != nil {
			return err
		}
		if resp.GetResyncRequired() {
			if since == 0 {
				return errors.New("sync: server requires a resync from version 0")
			}
			// An intermediate paging cursor fell below the server's tombstone
			// horizon. The WatchChanges catch-up is not paged, so a snapshot
			// from version 0 over the stream cannot hit that horizon.
			clear(docs)
			version, err := s.snapshotViaWatch(ctx, collect)
			if err != nil {
				return err
			}
			return install(version)
		}
		collect(resp.GetChanges())
		next := max(resp.GetNextVersion(), since)
		if !resp.GetHasMore() {
			return install(next)
		}
		if next == since && len(resp.GetChanges()) == 0 {
			return errors.New("sync: ListChanges reported has_more without progress")
		}
		since = next
	}
}

// snapshotViaWatch reads the feed from version 0 over WatchChanges until
// caught_up and returns the caught-up version.
func (s *Syncer) snapshotViaWatch(ctx context.Context, collect func([]*syncv1.Change)) (int64, error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := s.o.Client.watch(sctx, &syncv1.WatchChangesRequest{Kinds: s.o.Kinds})
	if err != nil {
		return 0, err
	}
	defer stream.Close()
	var version int64
	for stream.Receive() {
		switch ev := stream.Msg().GetEvent().(type) {
		case *syncv1.WatchChangesResponse_Change:
			collect([]*syncv1.Change{ev.Change})
			version = max(version, changeVersion(ev.Change))
		case *syncv1.WatchChangesResponse_CaughtUp:
			return max(version, ev.CaughtUp.GetVersion()), nil
		case *syncv1.WatchChangesResponse_ResyncRequired:
			return 0, errors.New("sync: server requires a resync from version 0")
		}
	}
	if err := stream.Err(); err != nil {
		return 0, err
	}
	return 0, errors.New("sync: snapshot stream ended before caught_up")
}

// Run follows WatchChanges until ctx is done, reconnecting with jittered
// exponential backoff from the last applied version. It handles
// resync_required with a full resync and refreshes the token once per
// UNAUTHENTICATED failure. It returns ctx.Err().
func (s *Syncer) Run(ctx context.Context) error {
	backoff := s.o.MinBackoff
	for {
		caughtUp, err := s.watchOnce(ctx)
		if ctx.Err() != nil {
			s.online.Store(false)
			return ctx.Err()
		}
		if caughtUp {
			backoff = s.o.MinBackoff
		}
		if err != nil {
			s.online.Store(false)
			s.log.Debug("sync: watch ended", "err", err)
			if connect.CodeOf(err) == connect.CodeUnauthenticated {
				if rerr := s.o.Client.refresh(ctx); rerr != nil {
					s.log.Warn("sync: token refresh failed", "err", rerr)
				}
			}
		}
		delay := backoff/2 + rand.N(backoff/2+1)
		backoff = min(backoff*2, s.o.MaxBackoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

var errIdle = errors.New("sync: watch stream idle")

// watchOnce runs one WatchChanges stream. caughtUp reports whether the
// stream reached the live phase.
func (s *Syncer) watchOnce(ctx context.Context) (caughtUp bool, err error) {
	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stream, err := s.o.Client.watch(sctx, &syncv1.WatchChangesRequest{SinceVersion: s.o.Cache.Cursor(), Kinds: s.o.Kinds})
	if err != nil {
		return false, err
	}
	defer stream.Close()

	idle := time.AfterFunc(s.o.IdleTimeout, func() { cancel(errIdle) })
	defer idle.Stop()

	for stream.Receive() {
		idle.Reset(s.o.IdleTimeout)
		msg := stream.Msg()
		switch ev := msg.GetEvent().(type) {
		case *syncv1.WatchChangesResponse_Change:
			if err := s.o.Cache.Apply([]*syncv1.Change{ev.Change}, changeVersion(ev.Change), false); err != nil {
				s.log.Warn("sync: cache write failed", "err", err)
			}
		case *syncv1.WatchChangesResponse_CaughtUp:
			caughtUp = true
			s.online.Store(true)
			if err := s.o.Cache.Apply(nil, ev.CaughtUp.GetVersion(), true); err != nil {
				s.log.Warn("sync: cache write failed", "err", err)
			}
		case *syncv1.WatchChangesResponse_Heartbeat:
			if caughtUp {
				_ = s.o.Cache.Apply(nil, ev.Heartbeat.GetVersion(), false)
			}
		case *syncv1.WatchChangesResponse_ResyncRequired:
			stream.Close()
			if err := s.Resync(ctx); err != nil {
				return caughtUp, err
			}
			return true, nil
		}
	}
	if cause := context.Cause(sctx); errors.Is(cause, errIdle) {
		return caughtUp, errIdle
	}
	if err := stream.Err(); err != nil {
		return caughtUp, err
	}
	return caughtUp, errors.New("sync: watch stream closed by server")
}

func changeVersion(ch *syncv1.Change) int64 {
	switch v := ch.GetChange().(type) {
	case *syncv1.Change_Document:
		return v.Document.GetVersion()
	case *syncv1.Change_Tombstone:
		return v.Tombstone.GetVersion()
	}
	return 0
}

// ConflictError is returned by Put and Delete when base_version did not
// match. Current (or Tombstone, when the document was deleted) is the
// server state to merge against; retry with BaseVersion().
type ConflictError struct {
	Current   *syncv1.Document
	Tombstone *syncv1.Tombstone
}

func (e *ConflictError) Error() string {
	if e.Current != nil {
		return fmt.Sprintf("sync: conflict: %s/%s is at version %d", e.Current.GetKind(), e.Current.GetKey(), e.Current.GetVersion())
	}
	return "sync: conflict: document was deleted"
}

// BaseVersion is the base_version to retry with.
func (e *ConflictError) BaseVersion() int64 {
	if e.Current != nil {
		return e.Current.GetVersion()
	}
	return 0
}

func (s *Syncer) conflict(c *syncv1.Conflict) *ConflictError {
	ce := &ConflictError{Current: c.GetDocument(), Tombstone: c.GetTombstone()}
	// The conflict carries newer server state: keep the cache current.
	if ce.Current != nil {
		_ = s.o.Cache.Apply([]*syncv1.Change{{Change: &syncv1.Change_Document{Document: ce.Current}}}, 0, false)
	} else if ce.Tombstone != nil {
		_ = s.o.Cache.Apply([]*syncv1.Change{{Change: &syncv1.Change_Tombstone{Tombstone: ce.Tombstone}}}, 0, false)
	}
	return ce
}

// Put writes a document. baseVersion nil writes unconditionally, 0 creates
// only, N writes only over version N. A version mismatch returns
// *ConflictError. The stored document is applied to the cache.
func (s *Syncer) Put(ctx context.Context, ref *syncv1.DocumentRef, content string, files []*syncv1.DocumentFile, baseVersion *int64) (*syncv1.Document, error) {
	resp, err := s.o.Client.PutDocument(ctx, &syncv1.PutDocumentRequest{Ref: ref, Content: content, Files: files, BaseVersion: baseVersion, Source: s.o.Source})
	if err != nil {
		if isTransport(err) {
			s.online.Store(false)
		}
		return nil, err
	}
	s.online.Store(true)
	if c := resp.GetConflict(); c != nil {
		return nil, s.conflict(c)
	}
	d := resp.GetDocument()
	if d != nil {
		_ = s.o.Cache.Apply([]*syncv1.Change{{Change: &syncv1.Change_Document{Document: d}}}, 0, false)
	}
	return d, nil
}

// Update performs a read-modify-write of one document with CAS: it calls
// merge with the cached current document (nil when absent) and retries on
// conflict with the server's state, up to attempts times (default 3).
func (s *Syncer) Update(ctx context.Context, ref *syncv1.DocumentRef, attempts int, merge func(current *syncv1.Document) (string, []*syncv1.DocumentFile, error)) (*syncv1.Document, error) {
	if attempts <= 0 {
		attempts = 3
	}
	cur, _ := s.o.Cache.Lookup(ref.GetScope(), ref.GetKind(), ref.GetKey(), ref.GetProjectKey())
	var lastErr error
	for i := 0; i < attempts; i++ {
		content, files, err := merge(cur)
		if err != nil {
			return nil, err
		}
		base := int64(0)
		if cur != nil {
			base = cur.GetVersion()
		}
		d, err := s.Put(ctx, ref, content, files, proto.Int64(base))
		var ce *ConflictError
		if !errors.As(err, &ce) {
			return d, err
		}
		lastErr = err
		cur = ce.Current
	}
	return nil, lastErr
}

// Delete deletes a document by id with CAS on baseVersion.
func (s *Syncer) Delete(ctx context.Context, id string, baseVersion int64) error {
	resp, err := s.o.Client.DeleteDocument(ctx, &syncv1.DeleteDocumentRequest{Id: id, BaseVersion: proto.Int64(baseVersion)})
	if err != nil {
		return err
	}
	if c := resp.GetConflict(); c != nil {
		return s.conflict(c)
	}
	if t := resp.GetTombstone(); t != nil {
		_ = s.o.Cache.Apply([]*syncv1.Change{{Change: &syncv1.Change_Tombstone{Tombstone: t}}}, 0, false)
	}
	return nil
}

func isTransport(err error) bool {
	var ce *connect.Error
	return !errors.As(err, &ce) && !errors.Is(err, context.Canceled)
}

// OpenOptions configure Open.
type OpenOptions struct {
	ServerURL string
	HTTP      *http.Client
	Tokens    rpc.Tokens
	// Identity scopes the cache (server URL + user id); defaults to ServerURL.
	Identity string
	// Dir defaults to DefaultDir().
	Dir    string
	Source string
	// CatchUpTimeout bounds the initial catch-up (default 5 s). On timeout
	// or error the Syncer starts offline and serves the cache.
	CatchUpTimeout time.Duration
	Logger         *slog.Logger
}

// Open opens the cache and runs the initial catch-up. A failed catch-up is
// not an error: the returned Syncer serves the cache (offline mode) and the
// caller should still start Run in the background to reconnect.
func Open(ctx context.Context, o OpenOptions) (*Syncer, error) {
	dir := o.Dir
	if dir == "" {
		dir = DefaultDir()
	}
	id := o.Identity
	if id == "" {
		id = o.ServerURL
	}
	cache, err := OpenCache(dir, id)
	if err != nil {
		return nil, err
	}
	s := New(Options{Client: NewClient(o.ServerURL, o.HTTP, o.Tokens), Cache: cache, Source: o.Source, Logger: o.Logger})
	timeout := o.CatchUpTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := s.CatchUp(cctx); err != nil {
		s.log.Warn("sync: catch-up failed; using cached data", "err", err, "cached", cache.Len())
	}
	return s, nil
}
