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
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/decision"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/relay"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/sdk"
	"github.com/xlsft/harness/packages/client/server"
	"github.com/xlsft/harness/packages/client/web"
)

const usage = `harness-client — universal local client of harness

Usage:
  harness-client login <server-url>     Sign in with the device flow
  harness-client logout                 Revoke this device and delete credentials
  harness-client whoami                 Show the signed-in user
  harness-client serve --socket <path>  Serve AgentService (gRPC/Connect, h2c) on a unix socket
  harness-client serve --listen 127.0.0.1:<port>
                                        Serve AgentService to browsers (Connect over HTTP/1.1 and h2c,
                                        bearer token, CORS allowlist); combinable with --socket
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
		case "import":
			return runImport(args[1:])
		case "sync":
			return runSync(args[1:])
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
		ServerURL:       url,
		Gateway:         connectgw.New(url, hc, tokens),
		Prompts:         prompts.NewConnectSource(url, hc, tokens),
		DecisionService: decision.NewClient(url, hc, tokens),
		Search:          web.NewSearchClient(url, hc, tokens),
	}, email, nil
}

func cmdServe(ctx context.Context, args []string) int {
	fs := pflag.NewFlagSet("serve", pflag.ContinueOnError)
	socket := fs.String("socket", "", "unix socket path to listen on")
	serverURL := fs.String("server", "", "harness server URL (overrides stored credentials)")
	allowed := fs.StringArray("allowedTools", nil, `permission rules allowed without asking, e.g. "Bash(npm test *) Edit"`)
	disallowed := fs.StringArray("disallowedTools", nil, "permission rules that are always denied")
	addDirs := fs.StringArray("add-dir", nil, "additional working directories for every session")
	outputStyle := fs.String("output-style", "", "output style for every session: a built-in id (default, proactive, concise, explanatory, learning) or a user/project style name")
	remote := fs.Bool("remote-control", true, "mirror sessions to the harness server so they can be watched and controlled from the web (HARNESS_REMOTE_CONTROL=0 disables)")
	verbose := fs.Bool("verbose", false, "debug logging")
	tcp := addTCPFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := tcp.validate(*socket); err != nil {
		return fail(err)
	}
	log := newLogger(*verbose)
	base, email, err := sharedAdapters(*serverURL)
	if err != nil {
		return fail(err)
	}
	base.AllowedTools, base.DisallowedTools, base.AdditionalDirs, base.Logger = *allowed, *disallowed, *addDirs, log
	base.OutputStyle = *outputStyle
	srv := server.New(base)
	srv.UserEmail = email
	srv.Logger = log
	if tokens, url, _, err := bootstrap.Tokens(*serverURL); err == nil && relay.Enabled(*remote, url, tokens != nil) {
		relay.Attach(ctx, srv, relay.Options{ServerURL: url, HTTP: rpc.HTTPClient(url), Tokens: tokens, Logger: log})
	}
	if err := serveListeners(ctx, srv, *socket, tcp); err != nil {
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
	mode := fs.String("permission-mode", "", "default | acceptEdits | plan | auto | dontAsk | bypassPermissions (default: settings, else default)")
	maxTurns := fs.Int("max-turns", 0, "maximum tool-use round trips (0 = unlimited)")
	outputStyle := fs.String("output-style", "", "output style: a built-in id (default, proactive, concise, explanatory, learning) or a user/project style name (default: settings outputStyle)")
	var allowed, disallowed, addDirs []string
	fs.StringArrayVar(&allowed, "allowedTools", nil, `tools allowed without asking, e.g. "Bash(git *) Edit"`)
	fs.StringArrayVar(&allowed, "allowed-tools", nil, "alias of --allowedTools")
	fs.StringArrayVar(&disallowed, "disallowedTools", nil, "tools that are always denied")
	fs.StringArrayVar(&disallowed, "disallowed-tools", nil, "alias of --disallowedTools")
	fs.StringArrayVar(&addDirs, "add-dir", nil, "additional working directories")
	cont := fs.BoolP("continue", "c", false, "continue the most recent session in this directory")
	resume := fs.StringP("resume", "r", "", "resume a session by id")
	forkSession := fs.Bool("fork-session", false, "with --continue/--resume: copy the session into a new id instead of appending to it")
	skipPerms := fs.Bool("dangerously-skip-permissions", false, "same as --permission-mode bypassPermissions")
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
	// An unset flag leaves the mode to settings (permissions.defaultMode).
	var pm permissions.Mode
	if *mode != "" {
		var err error
		if pm, err = permissions.ParseMode(*mode); err != nil {
			return fail(err)
		}
	}
	if *skipPerms {
		pm = permissions.ModeBypassPermissions
	}
	if *forkSession && !*cont && *resume == "" {
		return fail(errors.New("--fork-session requires --continue or --resume"))
	}
	for _, r := range permissions.ParseRules(append(append([]string(nil), allowed...), disallowed...)) {
		if err := permissions.ValidateRule(r.String()); err != nil {
			return fail(err)
		}
	}
	for i, d := range addDirs {
		abs, err := filepath.Abs(d)
		if err != nil {
			return fail(err)
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return fail(fmt.Errorf("--add-dir %s: not a directory", d))
		}
		addDirs[i] = abs
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
			ServerURL: *serverURL, Model: *model, Effort: *effort, MaxTurns: *maxTurns, OutputStyle: *outputStyle,
			PermissionMode: pm, AllowedTools: allowed, DisallowedTools: disallowed, AdditionalDirs: addDirs,
			Continue: *cont, Resume: *resume, SessionID: *sessionID, NoPersistence: *noPersist, Fork: *forkSession,
			Logger: newLogger(*verbose),
		},
	})
}
