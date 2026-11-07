package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/cibot"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/daemon"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/relay"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/sandbox"
	"github.com/xlsft/harness/packages/client/server"
)

const daemonUsage = `harness-client daemon — an always-on host: takes new sessions, schedules and auto-fix jobs (docs/design/hosts.md)

Usage:
  harness-client daemon [flags]               Run. The device token comes from HARNESS_DEVICE_TOKEN or HARNESS_DEVICE_TOKEN_FILE
                                              with HARNESS_SERVER_URL, or from a stored sign-in made with daemon login --store
  harness-client daemon login <server-url>    Register a host for yourself with the device flow and print its token
  harness-client daemon login --store <url>   The same, keeping the token on this machine

Flags:
`

type daemonFlags struct {
	fs             *pflag.FlagSet
	server         *string
	name           *string
	root           *string
	maxSessions    *int
	shared         *bool
	uidBase        *int
	uidCount       *int
	idleClose      *time.Duration
	keepWorkspaces *time.Duration
	allowed        *[]string
	verbose        *bool
}

func newDaemonFlags(name string) *daemonFlags {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	return &daemonFlags{
		fs:             fs,
		server:         fs.String("server", "", "server URL (default: HARNESS_SERVER_URL or the stored sign-in)"),
		name:           fs.String("name", "", "what the host is called (default: the machine's name)"),
		root:           fs.String("root", "", "where checkouts go (default: HARNESS_HOME/workspaces; with --shared, each user's home)"),
		maxSessions:    fs.Int("max-sessions", 0, "sessions working at once (default: what the app says, 4 if nothing)"),
		shared:         fs.Bool("shared", false, "a team host: run each user's work under a Unix user of its own. Needs Linux and root"),
		uidBase:        fs.Int("uid-base", 20000, "with --shared: the first Unix user id for workers. Nothing else on the machine may use this range"),
		uidCount:       fs.Int("uid-count", 1000, "with --shared: how many Unix users"),
		idleClose:      fs.Duration("idle-close", 24*time.Hour, "close a session idle for this long; its transcript stays, and a job or a schedule reopens it"),
		keepWorkspaces: fs.Duration("keep-workspaces", 7*24*time.Hour, "remove a closed session's checkout after this long"),
		allowed:        fs.StringArray("allowedTools", nil, `permission rules allowed without asking on this host, e.g. "Bash(go test *)"`),
		verbose:        fs.Bool("verbose", false, "debug logging"),
	}
}

func runDaemon(ctx context.Context, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "login":
			return daemonLogin(ctx, args[1:])
		case "worker":
			return daemonWorker(ctx, args[1:])
		case "help", "--help", "-h":
			fmt.Fprint(os.Stderr, daemonUsage+newDaemonFlags("daemon").fs.FlagUsages())
			return 0
		}
	}
	f := newDaemonFlags("harness-client daemon")
	if err := f.fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		fmt.Fprint(os.Stderr, daemonUsage+f.fs.FlagUsages())
		return 2
	}
	if err := tokenFromFile(); err != nil {
		return fail(err)
	}
	if *f.shared {
		return runSupervisor(ctx, f)
	}
	return runHost(ctx, f)
}

// tokenFromFile lets a container's secret be a file, which its environment does not show.
func tokenFromFile() error {
	path := os.Getenv("HARNESS_DEVICE_TOKEN_FILE")
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("HARNESS_DEVICE_TOKEN_FILE: %w", err)
	}
	if err := os.Unsetenv("HARNESS_DEVICE_TOKEN_FILE"); err != nil {
		return err
	}
	return os.Setenv("HARNESS_DEVICE_TOKEN", strings.TrimSpace(string(b)))
}

// daemonLogin registers a device for a personal host through the usual device flow. By default nothing is stored and only
// the token is printed, for a container's secret. The device becomes a host when the daemon first runs with it.
func daemonLogin(ctx context.Context, args []string) int {
	store := len(args) > 0 && args[0] == "--store"
	if store {
		args = args[1:]
	}
	if len(args) != 1 {
		return fail(errors.New("usage: harness-client daemon login [--store] <server-url>"))
	}
	url := strings.TrimRight(args[0], "/")
	host, _ := os.Hostname()
	info := auth.DeviceInfo{ClientName: "harness-client daemon", DeviceName: host, OS: runtime.GOOS, Arch: runtime.GOARCH}
	prompt := func(uri, code string) {
		fmt.Fprintf(os.Stderr, "To register this host, open %s\nand confirm the code: %s\n", uri, code)
	}
	var s auth.Store = auth.NewMemoryStore(nil)
	if store {
		s = auth.DefaultStore(config.CredentialsFile())
	}
	_, grant, err := auth.LoginWith(ctx, auth.NewConnectService(url, nil), s, url, info, prompt)
	if err != nil {
		return fail(err)
	}
	if store {
		fmt.Fprintf(os.Stderr, "Registered as %s. Run: harness-client daemon\n", grant.User.Email)
		return 0
	}
	fmt.Fprintln(os.Stderr, "Store this as HARNESS_DEVICE_TOKEN for the daemon. It is not shown again.")
	fmt.Println(grant.DeviceToken)
	return 0
}

// daemonWorker is one member's process on a team host, started by the supervisor under their Unix user. The token arrives
// on fd 3, so it is in no environment and no command line.
func daemonWorker(ctx context.Context, args []string) int {
	f := newDaemonFlags("harness-client daemon worker")
	if err := f.fs.Parse(args); err != nil {
		return 2
	}
	daemon.Harden()
	pipe := os.NewFile(3, "token")
	if pipe == nil {
		return fail(errors.New("a worker is started by a shared daemon"))
	}
	b, err := io.ReadAll(pipe)
	_ = pipe.Close()
	if err != nil {
		return fail(err)
	}
	if err := os.Setenv("HARNESS_DEVICE_TOKEN", strings.TrimSpace(string(b))); err != nil {
		return fail(err)
	}
	return runHost(ctx, f)
}

func sandboxDriverName() string {
	if info := sandbox.NewDriver().Info(); info.Supported {
		return info.Platform
	}
	return ""
}

// runSupervisor is a team host's main process: it runs no agents and reads no repository.
func runSupervisor(ctx context.Context, f *daemonFlags) int {
	if err := daemon.CheckShared(); err != nil {
		return fail(err)
	}
	log := newLogger(*f.verbose)
	tokens, url, _, err := bootstrap.Tokens(*f.server)
	if err != nil {
		return fail(err)
	}
	self, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	base := *f.root
	if base == "" {
		base = filepath.Join(config.Home(), "hosts")
	}
	name := *f.name
	if name == "" {
		name, _ = os.Hostname()
	}
	workerArgs := []string{"--idle-close", f.idleClose.String(), "--keep-workspaces", f.keepWorkspaces.String()}
	for _, rule := range *f.allowed {
		workerArgs = append(workerArgs, "--allowedTools", rule)
	}
	if *f.verbose {
		workerArgs = append(workerArgs, "--verbose")
	}
	s := daemon.NewSupervisor(daemon.SupervisorOptions{
		ServerURL: url, HTTP: rpc.HTTPClient(url), Tokens: tokens, Logger: log, Name: name, BaseDir: base,
		UIDBase: *f.uidBase, UIDCount: *f.uidCount, MaxSessions: *f.maxSessions, SandboxDriver: sandboxDriverName(),
		Launcher: daemon.UIDLauncher{Executable: self, ServerURL: url, Name: name, Args: workerArgs, Logger: log},
	})
	fmt.Fprintf(os.Stderr, "harness-client: team host %s on %s\n", name, url)
	if err := s.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fail(err)
	}
	return 0
}

// runHost is the session-running daemon: a personal host, or one member's worker.
func runHost(ctx context.Context, f *daemonFlags) int {
	log := newLogger(*f.verbose)
	base, email, err := sharedAdapters(*f.server)
	if err != nil {
		return fail(err)
	}
	base.Logger, base.AllowedTools = log, *f.allowed
	// Nobody may be at a keyboard: start in auto mode unless the work or the settings say otherwise.
	base.FallbackPermissionMode = permissions.ModeAuto
	srv := server.New(base)
	srv.UserEmail = email
	srv.Logger = log
	root := *f.root
	if root == "" {
		root = filepath.Join(config.Home(), "workspaces")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return fail(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fail(err)
	}
	wired := wireServer(ctx, srv, *f.server, true, log, func(o *relay.Options) { o.Daemon, o.AllowedRoots = true, []string{root} })
	if wired.tokens == nil {
		return fail(errors.New("no server to work for: set HARNESS_DEVICE_TOKEN and HARNESS_SERVER_URL, or run harness-client daemon login --store"))
	}
	// What the agent starts inherits nothing of this: the adapters hold the token now.
	for _, name := range cibot.SecretEnv {
		_ = os.Unsetenv(name)
	}
	name := *f.name
	if name == "" {
		name, _ = os.Hostname()
	}
	runner := daemon.New(daemon.Options{
		ServerURL: wired.url, HTTP: rpc.HTTPClient(wired.url), Tokens: wired.tokens, Sessions: daemon.ServerSessions{S: srv}, Logger: log,
		Name: name, Root: root, MaxSessions: *f.maxSessions, SandboxDriver: sandboxDriverName(), IdleClose: *f.idleClose, KeepWorkspaces: *f.keepWorkspaces,
	})
	srv.HostMode = runner.ScheduleHooks(wired.name, wired.email)
	srv.StartBackground()
	defer srv.Close()
	fmt.Fprintf(os.Stderr, "harness-client: host %s on %s, checkouts in %s\n", name, wired.url, root)
	if err := runner.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fail(err)
	}
	return 0
}
