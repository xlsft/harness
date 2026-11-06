package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/selfupdate"
)

const releaseRepo = "xlsft/harness"

// releasesWeb is where releases are looked for; a test points it at a fake.
var releasesWeb = "https://github.com"

func runUpdate(ctx context.Context, args []string) int {
	fs := pflag.NewFlagSet("update", pflag.ContinueOnError)
	check := fs.Bool("check", false, "only report whether an update is available")
	force := fs.Bool("force", false, "replace a development build too")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return fail(err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if selfupdate.ManagedExecutable(exe, config.Distribution) == "desktop" {
		fmt.Fprintf(os.Stderr, "This harness-client is the desktop app's sidecar. It is updated with the app, not by this command: use Settings → About in the app.\n")
		return 1
	}
	u := selfupdate.Updater{
		Web:       releasesWeb,
		Repo:      releaseRepo,
		PublicKey: selfupdate.PublicKey,
		HTTP:      &http.Client{Timeout: 10 * time.Minute},
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tag, err := u.LatestTag(checkCtx)
	if err != nil {
		return fail(fmt.Errorf("check for updates: %w", err))
	}
	if !selfupdate.Newer(tag, config.Version) {
		fmt.Printf("harness-client %s is the latest version\n", config.Version)
		return 0
	}
	if *check {
		fmt.Printf("harness-client %s is available (you have %s): %s\n", tag, config.Version, u.ReleaseURL(tag))
		return 0
	}
	if selfupdate.IsDevBuild(config.Version) && !*force {
		return fail(fmt.Errorf("this is a development build (%s); --force replaces it with %s", config.Version, tag))
	}
	if err := u.Install(ctx, tag, exe); err != nil {
		return fail(err)
	}
	fmt.Printf("Updated harness-client %s → %s. A running `harness-client serve` keeps the old version until it restarts.\n", config.Version, tag)
	return 0
}
