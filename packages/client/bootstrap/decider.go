package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/decision"
)

// DecisionCacheDir holds the cached rule options of each server and user.
func DecisionCacheDir() string { return filepath.Join(config.Home(), "cache", "decision") }

// newDecider builds the auto-mode decider of a session. With a
// DecisionService client the local rules follow the administrator's rule
// options: cached on disk per server and user, fetched at session start and
// whenever the server reports a different rules_version.
func newDecider(ctx context.Context, o Options, user *auth.User, log *slog.Logger) *decision.Cache {
	dc, ok := o.DecisionService.(*decision.Client)
	if !ok {
		return decision.New(o.DecisionService, log)
	}
	server := dc.BaseURL()
	who := rulesUser(user, o.ServerURL)
	sync := decision.NewRulesSync(dc, decision.RulesCachePath(DecisionCacheDir(), server, who), server, who, log)
	cache := decision.NewWithRulesSync(o.DecisionService, sync, log)
	sync.Start(ctx)
	return cache
}

// rulesUser identifies the user the rule options are cached for: the signed-in
// user, else a digest of a static access token (never the token itself).
func rulesUser(user *auth.User, serverURL string) string {
	if user == nil || user.ID == "" {
		if _, _, u, err := Tokens(serverURL); err == nil && u != nil {
			user = u
		}
	}
	if user != nil && user.ID != "" {
		return user.ID
	}
	if t := os.Getenv("HARNESS_ACCESS_TOKEN"); t != "" {
		sum := sha256.Sum256([]byte(t))
		return "token:" + hex.EncodeToString(sum[:8])
	}
	return "unknown"
}
