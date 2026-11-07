package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/selfupdate"
	"github.com/xlsft/harness/packages/client/sshhost"
)

const releaseRepo = "xlsft/harness"

// releasesWeb is where releases are looked for; a test points it at a fake.
var releasesWeb = "https://github.com"

func newUpdateFlags() (fs *pflag.FlagSet, check, force *bool) {
	fs = pflag.NewFlagSet("update", pflag.ContinueOnError)
	check = fs.Bool("check", false, "only report whether an update is available")
	force = fs.Bool("force", false, "replace a development build too")
	return fs, check, force
}

// policyBounds are the client versions the organization's managed policy allows, read without running a session.
var policyBounds = func(ctx context.Context) selfupdate.Bounds {
	m, _ := bootstrap.ReadManagedPolicy(ctx, "", 5*time.Second, nil)
	return selfupdate.Bounds{Min: m.MinClientVersion, Max: m.MaxClientVersion}
}

func runUpdate(ctx context.Context, args []string) int {
	fs, check, force := newUpdateFlags()
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
	latest, err := u.LatestTag(checkCtx)
	if err != nil {
		return fail(fmt.Errorf("check for updates: %w", err))
	}
	// The managed policy's maximum caps the update; its minimum only warns, since updating is how to meet it.
	bounds := policyBounds(ctx)
	if msg := bounds.Message("harness-client", config.Version); msg != "" {
		fmt.Fprintln(os.Stderr, "Warning:", msg)
	}
	tag := bounds.Target(latest, config.Version)
	if tag == "" {
		if selfupdate.Newer(latest, config.Version) {
			fmt.Printf("harness-client %s is available, but your administrator allows versions up to %s; you have %s\n", latest, bounds.Max, config.Version)
		} else {
			fmt.Printf("harness-client %s is the latest version\n", config.Version)
		}
		return 0
	}
	if tag != latest {
		fmt.Printf("harness-client %s is the latest release; your administrator allows up to %s\n", latest, strings.TrimPrefix(tag, "v"))
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

// sshBinaries finds a client to upload to an SSH host: this very program on the same platform, else the signed release of this
// version for the host's platform, verified like an update. A development build has no release to take.
func sshBinaries(ctx context.Context, goos, goarch string) (string, func(), error) {
	if goos == runtime.GOOS && goarch == runtime.GOARCH {
		return sshhost.OwnBinary(ctx, goos, goarch)
	}
	if selfupdate.IsDevBuild(config.Version) {
		return "", nil, fmt.Errorf("a development build has no release to upload for %s/%s", goos, goarch)
	}
	dir, err := os.MkdirTemp("", "harness-client-upload-*")
	if err != nil {
		return "", nil, err
	}
	done := func() { _ = os.RemoveAll(dir) }
	exe := filepath.Join(dir, "harness-client")
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		done()
		return "", nil, err
	}
	u := selfupdate.Updater{Web: releasesWeb, Repo: releaseRepo, PublicKey: selfupdate.PublicKey, HTTP: &http.Client{Timeout: 10 * time.Minute}, OS: goos, Arch: goarch}
	if err := u.Install(ctx, "v"+strings.TrimPrefix(config.Version, "v"), exe); err != nil {
		done()
		return "", nil, err
	}
	return exe, done, nil
}
