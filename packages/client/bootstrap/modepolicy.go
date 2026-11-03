package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/config"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/syncclient"
)

// ModePolicyDir caches the POLICY documents the mode policy watcher follows, so the last known policy also
// applies when the client starts offline.
func ModePolicyDir() string { return filepath.Join(config.Home(), "cache", "policy") }

// WatchModePolicy returns the server's permission mode policy and keeps it current until ctx ends: it follows
// the POLICY documents of the caller's sync feed (WatchChanges limited to that kind, reconnecting like the
// session sync) and recomputes the disabled modes on every change (settings.PolicyDisabledModes). The initial
// catch-up is bounded; offline, the last cached policy applies. Without a server or with HARNESS_NO_SYNC=1 the
// policy stays empty (every mode allowed, as settings decide).
func WatchModePolicy(ctx context.Context, url string, hc *http.Client, tokens auth.TokenSource, user *auth.User, log *slog.Logger) *permissions.ModePolicy {
	p := permissions.NewModePolicy()
	if os.Getenv("HARNESS_NO_SYNC") == "1" || url == "" || tokens == nil {
		return p
	}
	if log == nil {
		log = slog.Default()
	}
	identity := url
	if user != nil && user.ID != "" {
		identity += "|" + user.ID
	}
	cache, err := syncclient.OpenCache(ModePolicyDir(), identity)
	if err != nil {
		log.Warn("mode policy cache unavailable; keeping it in memory", "err", err)
		cache, _ = syncclient.OpenCache("", identity)
	}
	kind := syncv1.DocumentKind_DOCUMENT_KIND_POLICY
	syncer := syncclient.New(syncclient.Options{
		Client: syncclient.NewClient(url, hc, tokens), Cache: cache, Kinds: []syncv1.DocumentKind{kind}, Logger: log,
	})
	apply := func() {
		var modes []permissions.Mode
		for _, m := range settings.PolicyDisabledModes(cache.List(kind)) {
			modes = append(modes, permissions.Mode(m))
		}
		if p.Set(modes) {
			log.Info("permission mode policy changed", "disabled", modes)
		}
	}
	apply()
	cache.Subscribe(func(syncclient.Event) { apply() })
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := syncer.CatchUp(cctx); err != nil {
		log.Warn("mode policy: catch-up failed; using the cached policy", "err", err)
	}
	cancel()
	go func() { _ = syncer.Run(ctx) }()
	return p
}
