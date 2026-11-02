package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/vcs"
)

// startVCS runs the gh/glab device providers for the session's directory in the background. It runs
// only when the session is synced with a server (startSync succeeded) and HARNESS_NO_VCS is not 1;
// the returned cancel func stops it (Session.Close).
func startVCS(s *Session, url string, hc *http.Client, tokens auth.TokenSource, log *slog.Logger) (*vcs.Watcher, context.CancelFunc) {
	if s.Sync == nil || url == "" || tokens == nil || os.Getenv("HARNESS_NO_VCS") == "1" {
		return nil, nil
	}
	w := vcs.NewWatcher(vcs.Options{Reporter: vcs.NewClient(url, hc, tokens), Dir: s.Cwd, Logger: log})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = w.Run(ctx) }()
	return w, cancel
}
