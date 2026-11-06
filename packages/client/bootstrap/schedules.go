package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/config"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/syncclient"
)

// graphPath is where the session keeps its task graph: next to the transcript.
func graphPath(transcript string, noPersistence bool) string {
	if noPersistence || transcript == "" {
		return ""
	}
	return filepath.Join(strings.TrimSuffix(transcript, ".jsonl"), "task-graph.json")
}

// WatchSchedules follows the schedule documents of the caller's sync feed until ctx ends, with a cache of its own, so
// schedules reach this machine however long it was away. Nil without a server or with HARNESS_NO_SYNC=1.
func WatchSchedules(ctx context.Context, url string, hc *http.Client, tokens auth.TokenSource, user *auth.User, log *slog.Logger) *syncclient.Syncer {
	if os.Getenv("HARNESS_NO_SYNC") == "1" || url == "" || tokens == nil {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	identity := url
	if user != nil && user.ID != "" {
		identity += "|" + user.ID
	}
	cache, err := syncclient.OpenCache(filepath.Join(config.Home(), "cache", "schedules"), identity)
	if err != nil {
		log.Warn("schedule cache unavailable; keeping it in memory", "err", err)
		cache, _ = syncclient.OpenCache("", identity)
	}
	syncer := syncclient.New(syncclient.Options{
		Client: syncclient.NewClient(url, hc, tokens), Cache: cache, Logger: log,
		Kinds: []syncv1.DocumentKind{syncv1.DocumentKind_DOCUMENT_KIND_SCHEDULE, syncv1.DocumentKind_DOCUMENT_KIND_SCHEDULE_HISTORY, syncv1.DocumentKind_DOCUMENT_KIND_SCHEDULE_TRIGGER},
	})
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := syncer.CatchUp(cctx); err != nil {
		log.Debug("schedules: catch-up failed; using the cached documents", "err", err)
	}
	cancel()
	go func() { _ = syncer.Run(ctx) }()
	return syncer
}
