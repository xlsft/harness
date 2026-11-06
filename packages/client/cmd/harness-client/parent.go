package main

import (
	"context"
	"io"
	"os"
	"time"
)

// exitWithStdin cancels ctx once stdin closes, enabled by HARNESS_EXIT_WITH_STDIN=1: a client started over SSH goes away with the
// connection that started it, whatever else happens to the shell around it.
func exitWithStdin(ctx context.Context, cancel context.CancelFunc) {
	if os.Getenv("HARNESS_EXIT_WITH_STDIN") != "1" {
		return
	}
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
	}()
}

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
