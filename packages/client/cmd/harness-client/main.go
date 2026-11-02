// Command harness-client is the headless harness client: device login,
// headless runs (`-p`) and the AgentService server for frontends.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/sdk"
	"github.com/xlsft/harness/packages/client/server"
)

const usage = `harness-client — universal local client of harness

Usage:
  harness-client login <server-url>     Sign in with the device flow
  harness-client logout                 Revoke this device and delete credentials
  harness-client whoami                 Show the signed-in user
  harness-client serve --socket <path>  Serve AgentService (gRPC/Connect, h2c) on a unix socket
  harness-client -p "<prompt>" [flags]  Run one headless turn
  harness-client version

Headless flags:
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func newLogger(verbose bool) *slog.Logger {
	level := slog.LevelWarn
	if verbose || os.Getenv("HARNESS_DEBUG") == "1" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func run(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) > 0 {
		switch args[0] {
		case "login":
			return cmdLogin(ctx, args[1:])
		case "logout":
			return cmdLogout(ctx)
		case "whoami":
			return cmdWhoami(ctx)
		case "serve":
			return cmdServe(ctx, args[1:])
		case "version", "--version":
			fmt.Println(config.UserAgent())
			return 0
		}
	}
	return cmdPrint(ctx, args)
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "Error:", err)
	return 1
}

func cmdLogin(ctx context.Context, args []string) int {
	if len(args) != 1 {
		return fail(errors.New("usage: harness-client login <server-url>"))
	}
	url := strings.TrimRight(args[0], "/")
	host, _ := os.Hostname()
	svc := auth.NewConnectService(url, nil)
	store := auth.DefaultStore(config.CredentialsFile())
	_, grant, err := auth.Login(ctx, svc, store, url, auth.DeviceInfo{ClientName: "harness-client", DeviceName: host, OS: runtime.GOOS, Arch: runtime.GOARCH}, os.Stdout)
	if err != nil {
		return fail(err)
	}
	fmt.Printf("Signed in to %s as %s (device %s)\n", url, grant.User.Email, grant.DeviceID)
	return 0
}

func manager() (*auth.Manager, error) {
	store := auth.DefaultStore(config.CredentialsFile())
	c, err := store.Load()
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, auth.ErrNotLoggedIn
	}
	return auth.NewManager(auth.NewConnectService(c.Server, nil), store)
}

func cmdLogout(ctx context.Context) int {
	m, err := manager()
	if err != nil {
		return fail(err)
	}
	if err := m.Logout(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Warning: server logout failed:", err)
	}
	fmt.Println("Signed out; local credentials deleted.")
	return 0
}

func cmdWhoami(ctx context.Context) int {
	m, err := manager()
	if err != nil {
		return fail(err)
	}
	me, err := m.Me(ctx)
	if err != nil {
		return fail(err)
	}
	fmt.Printf("%s <%s> role=%s\nserver: %s\ndevice: %s %s\n", me.User.Name, me.User.Email, me.User.Role, m.Credentials().Server, me.Device.ID, me.Device.Name)
	return 0
}

// sharedAdapters creates one gateway and prompt source for all sessions.
func sharedAdapters(serverURL string) (bootstrap.Options, string, error) {
	tokens, url, user, err := bootstrap.Tokens(serverURL)
	if err != nil {
		return bootstrap.Options{}, "", err
	}
	hc := rpc.HTTPClient(url)
	email := ""
	if user != nil {
		email = user.Email
	}
	return bootstrap.Options{
		ServerURL: url,
		Gateway:   connectgw.New(url, hc, tokens),
		Prompts:   prompts.NewConnectSource(url, hc, tokens),
	}, email, nil
}

func cmdServe(ctx context.Context, args []string) int {
	fs := pflag.NewFlagSet("serve", pflag.ContinueOnError)
	socket := fs.String("socket", "", "unix socket path to listen on")
	serverURL := fs.String("server", "", "harness server URL (overrides stored credentials)")
	allowed := fs.StringArray("allowedTools", nil, "tools allowed without asking")
	disallowed := fs.StringArray("disallowedTools", nil, "tools that are always denied")
	verbose := fs.Bool("verbose", false, "debug logging")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *socket == "" {
		return fail(errors.New("--socket is required"))
	}
	log := newLogger(*verbose)
	base, email, err := sharedAdapters(*serverURL)
	if err != nil {
		return fail(err)
	}
	base.AllowedTools, base.DisallowedTools, base.Logger = *allowed, *disallowed, log
	srv := server.New(base)
	srv.UserEmail = email
	srv.Logger = log
	fmt.Fprintf(os.Stderr, "harness-client: serving AgentService on unix://%s\n", *socket)
	if err := srv.ServeUnix(ctx, *socket); err != nil {
		return fail(err)
	}
	return 0
}

func cmdPrint(ctx context.Context, args []string) int {
	fs := pflag.NewFlagSet("harness-client", pflag.ContinueOnError)
	fs.SetInterspersed(true)
	printMode := fs.BoolP("print", "p", false, "run one headless turn and print the result")
	outputFormat := fs.String("output-format", "text", "text | json | stream-json")
	partial := fs.Bool("include-partial-messages", false, "with stream-json: include raw stream events")
	model := fs.String("model", "", "model id (default: the server's main slot)")
	effort := fs.String("effort", "", "low | medium | high | xhigh | max")
	mode := fs.String("permission-mode", "default", "default | acceptEdits | plan | dontAsk | bypassPermissions")
	maxTurns := fs.Int("max-turns", 0, "maximum tool-use round trips (0 = unlimited)")
	var allowed, disallowed, addDirs []string
	fs.StringArrayVar(&allowed, "allowedTools", nil, `tools allowed without asking, e.g. "Bash(git *) Edit"`)
	fs.StringArrayVar(&allowed, "allowed-tools", nil, "alias of --allowedTools")
	fs.StringArrayVar(&disallowed, "disallowedTools", nil, "tools that are always denied")
	fs.StringArrayVar(&disallowed, "disallowed-tools", nil, "alias of --disallowedTools")
	fs.StringArrayVar(&addDirs, "add-dir", nil, "additional working directories")
	cont := fs.BoolP("continue", "c", false, "continue the most recent session in this directory")
	resume := fs.StringP("resume", "r", "", "resume a session by id")
	sessionID := fs.String("session-id", "", "use this id for a new session")
	noPersist := fs.Bool("no-session-persistence", false, "do not write a transcript")
	serverURL := fs.String("server", "", "harness server URL (overrides stored credentials)")
	verbose := fs.Bool("verbose", false, "debug logging")
	help := fs.BoolP("help", "h", false, "show help")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		fmt.Fprint(os.Stderr, usage+fs.FlagUsages())
		return 2
	}
	if *help || !*printMode {
		fmt.Fprint(os.Stderr, usage+fs.FlagUsages())
		if *help {
			return 0
		}
		return 2
	}
	pm, err := permissions.ParseMode(*mode)
	if err != nil {
		return fail(err)
	}
	switch *outputFormat {
	case sdk.FormatText, sdk.FormatJSON, sdk.FormatStreamJSON:
	default:
		return fail(fmt.Errorf("unknown --output-format %q", *outputFormat))
	}
	return sdk.Run(ctx, sdk.Options{
		Prompt:         strings.Join(fs.Args(), " "),
		OutputFormat:   *outputFormat,
		IncludePartial: *partial,
		Bootstrap: bootstrap.Options{
			ServerURL: *serverURL, Model: *model, Effort: *effort, MaxTurns: *maxTurns,
			PermissionMode: pm, AllowedTools: allowed, DisallowedTools: disallowed, AdditionalDirs: addDirs,
			Continue: *cont, Resume: *resume, SessionID: *sessionID, NoPersistence: *noPersist,
			Logger: newLogger(*verbose),
		},
	})
}
