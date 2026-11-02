package main

import (
	"context"
	"os"
	"time"
)

// exitWithParent cancels ctx once the process that started us is gone (we get re-parented), so a sidecar
// does not outlive an app that was killed without a chance to stop it (a crash, a dev rebuild).
// Enabled by HARNESS_EXIT_WITH_PARENT=1, which the desktop shell sets.
func exitWithParent(ctx context.Context, cancel context.CancelFunc) {
	if os.Getenv("HARNESS_EXIT_WITH_PARENT") != "1" {
		return
	}
	parent := os.Getppid()
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if os.Getppid() != parent {
					cancel()
					return
				}
			}
		}
	}()
}
