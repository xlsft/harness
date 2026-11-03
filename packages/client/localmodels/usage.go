package localmodels

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"connectrpc.com/connect"

	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/llm/v1/llmv1connect"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/rpc"
)

// UsageRecord is one request to a local model.
type UsageRecord struct {
	ID           string
	ModelID      string
	ProviderID   string
	SessionID    string
	RequestClass string
	AgentID      string
	Usage        llm.Usage
	Status       string
	StopReason   string
	DurationMs   int64
	CreatedAt    time.Time
}

// UsageSink receives batches of usage records (the server's UsageService.ReportLocalUsage).
type UsageSink interface {
	Report(ctx context.Context, recs []UsageRecord) error
}

// Reporter limits.
const (
	usageBatch      = 500
	usageMaxPending = 5000
	usageInterval   = 30 * time.Second
)

// UsageReporter queues usage records and sends them to a sink in the background. Records the server has not
// accepted yet are kept in memory (at most usageMaxPending; the oldest are dropped) and lost when the client
// exits.
type UsageReporter struct {
	sink UsageSink
	log  *slog.Logger

	mu      sync.Mutex
	pending []UsageRecord
	kick    chan struct{}
}

// NewUsageReporter starts a reporter that flushes until ctx ends.
func NewUsageReporter(ctx context.Context, sink UsageSink, log *slog.Logger) *UsageReporter {
	if log == nil {
		log = slog.Default()
	}
	u := &UsageReporter{sink: sink, log: log, kick: make(chan struct{}, 1)}
	go u.run(ctx)
	return u
}

// Add queues a record.
func (u *UsageReporter) Add(rec UsageRecord) {
	if rec.ID == "" {
		var b [10]byte
		_, _ = rand.Read(b[:])
		rec.ID = "lus_" + hex.EncodeToString(b[:])
	}
	u.mu.Lock()
	u.pending = append(u.pending, rec)
	if over := len(u.pending) - usageMaxPending; over > 0 {
		u.pending = append([]UsageRecord(nil), u.pending[over:]...)
	}
	u.mu.Unlock()
	select {
	case u.kick <- struct{}{}:
	default:
	}
}

// Pending returns the queued records (tests).
func (u *UsageReporter) Pending() []UsageRecord {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]UsageRecord(nil), u.pending...)
}

// Flush sends the queued records; on failure they stay queued.
func (u *UsageReporter) Flush(ctx context.Context) error {
	for {
		u.mu.Lock()
		n := min(len(u.pending), usageBatch)
		batch := append([]UsageRecord(nil), u.pending[:n]...)
		u.mu.Unlock()
		if n == 0 {
			return nil
		}
		if err := u.sink.Report(ctx, batch); err != nil {
			return err
		}
		u.mu.Lock()
		// Drop what was sent (records only ever append behind it, or the oldest were dropped by Add).
		sent := map[string]bool{}
		for _, r := range batch {
			sent[r.ID] = true
		}
		kept := u.pending[:0]
		for _, r := range u.pending {
			if !sent[r.ID] {
				kept = append(kept, r)
			}
		}
		u.pending = kept
		u.mu.Unlock()
	}
}

func (u *UsageReporter) run(ctx context.Context) {
	t := time.NewTicker(usageInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = u.Flush(fctx)
			cancel()
			return
		case <-u.kick:
		case <-t.C:
		}
		fctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if err := u.Flush(fctx); err != nil && ctx.Err() == nil {
			u.log.Debug("local usage not reported yet", "err", err)
		}
		cancel()
	}
}

// CoreUsageSink reports to the harness server's UsageService.
type CoreUsageSink struct {
	rpc    llmv1connect.UsageServiceClient
	tokens rpc.Tokens
}

// NewCoreUsageSink creates a sink for the server at baseURL.
func NewCoreUsageSink(baseURL string, hc *http.Client, tokens rpc.Tokens) *CoreUsageSink {
	if hc == nil {
		hc = rpc.HTTPClient(baseURL)
	}
	return &CoreUsageSink{rpc: llmv1connect.NewUsageServiceClient(hc, baseURL, rpc.ClientOptions()...), tokens: tokens}
}

// Report implements UsageSink.
func (s *CoreUsageSink) Report(ctx context.Context, recs []UsageRecord) error {
	msg := &llmv1.ReportLocalUsageRequest{}
	for _, r := range recs {
		msg.Records = append(msg.Records, &llmv1.LocalUsageRecord{
			Id: r.ID, ModelId: r.ModelID, ProviderId: r.ProviderID, RequestClass: r.RequestClass, SessionId: r.SessionID, AgentId: r.AgentID,
			InputTokens: int64(r.Usage.InputTokens), OutputTokens: int64(r.Usage.OutputTokens), CacheReadTokens: int64(r.Usage.CacheReadInputTokens),
			Status: r.Status, StopReason: r.StopReason, DurationMs: r.DurationMs, CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	_, err := rpc.WithAuth(ctx, s.tokens, func(token string) (*connect.Response[llmv1.ReportLocalUsageResponse], error) {
		req := connect.NewRequest(msg)
		rpc.SetBearer(req.Header(), token)
		return s.rpc.ReportLocalUsage(ctx, req)
	})
	return err
}

var reportOnce sync.Once

// StartUsageReporting makes the default registry report local usage to the server at baseURL (once per
// process; a no-op without tokens or with HARNESS_NO_SYNC=1).
func StartUsageReporting(ctx context.Context, baseURL string, hc *http.Client, tokens rpc.Tokens, log *slog.Logger) {
	if tokens == nil || baseURL == "" || Disabled() || os.Getenv("HARNESS_NO_SYNC") == "1" {
		return
	}
	reportOnce.Do(func() {
		Default().SetUsageReporter(NewUsageReporter(context.WithoutCancel(ctx), NewCoreUsageSink(baseURL, hc, tokens), log))
	})
}
