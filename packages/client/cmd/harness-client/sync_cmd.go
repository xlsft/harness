package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/bootstrap"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/syncclient"
)

const syncUsage = `Usage: harness-client sync [flags]

Catches up the local sync cache (~/.harness/cache/sync) with the server,
prints what it holds and uploads pending session transcripts. With --watch it
keeps following server changes and uploading sessions until interrupted.

Flags:
`

// runSync implements `harness-client sync`.
func runSync(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fs := pflag.NewFlagSet("sync", pflag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, syncUsage); fs.PrintDefaults() }
	watch := fs.Bool("watch", false, "keep following changes until interrupted")
	resync := fs.Bool("resync", false, "drop the cursor and rebuild the cache from a full snapshot")
	server := fs.String("server", "", "harness server URL (defaults to the signed-in server)")
	verbose := fs.BoolP("verbose", "v", false, "log sync activity")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	level := slog.LevelWarn
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	tokens, url, user, err := bootstrap.Tokens(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	identity := url
	if user != nil && user.ID != "" {
		identity += "|" + user.ID
	}
	deviceID, deviceName := deviceIdentity()
	s, err := syncclient.Open(ctx, syncclient.OpenOptions{ServerURL: url, Tokens: tokens, Identity: identity, Source: "client:" + deviceID, Logger: log})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	if *resync {
		if err := s.Resync(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
	}
	state := "online"
	if !s.Online() {
		state = "offline (serving the cache)"
	}
	fmt.Printf("server: %s (%s)\ncache: %s, version %d\n", url, state, s.Cache().Dir(), s.Cache().Cursor())
	counts := map[string]int{}
	for _, d := range s.Cache().List(syncv1.DocumentKind_DOCUMENT_KIND_UNSPECIFIED) {
		k := strings.ToLower(strings.TrimPrefix(d.GetKind().String(), "DOCUMENT_KIND_"))
		scope := strings.ToLower(strings.TrimPrefix(d.GetScope().String(), "SCOPE_"))
		counts[k+" ("+scope+")"]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %-28s %d\n", k, counts[k])
	}

	up := syncclient.NewUploader(syncclient.UploaderOptions{API: s.Client(), DeviceID: deviceID, DeviceName: deviceName, Logger: log})
	failed := 0
	pending := up.Pending()
	for _, id := range pending {
		if err := up.Flush(ctx, id); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "session %s: upload failed: %v\n", id, err)
		}
	}
	if len(pending) > 0 {
		fmt.Printf("sessions: %d uploaded, %d pending\n", len(pending)-failed, failed)
	}
	if !*watch {
		if failed > 0 {
			return 1
		}
		return 0
	}
	s.Cache().Subscribe(func(e syncclient.Event) {
		switch {
		case e.Document != nil:
			fmt.Printf("changed %s/%s v%d\n", e.Document.GetKind(), e.Document.GetKey(), e.Document.GetVersion())
		case e.Removed != nil:
			fmt.Printf("deleted %s/%s\n", e.Removed.GetKind(), e.Removed.GetKey())
		case e.Resync:
			fmt.Println("resynced")
		}
	})
	go up.Run(ctx)
	_ = s.Run(ctx)
	return 0
}
