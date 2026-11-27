// Command harness is the terminal frontend. With no arguments it starts an interactive session in
// the current directory. `login`, `logout`, `whoami` and `-p` (headless) delegate to the client
// packages, like harness-client; `demo` renders the brand primitives.
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
	"slices"
	"strings"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/completion"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/sdk"

	"github.com/charmbracelet/x/term"
	"github.com/xlsft/harness/packages/cli/brand"

	"github.com/xlsft/harness/packages/cli/internal/client"
	"github.com/xlsft/harness/packages/cli/internal/render"
	"github.com/xlsft/harness/packages/cli/internal/tui"
)

const usage = `harness — the terminal frontend of harness

Usage:
  harness [flags]                 Start an interactive session in the current directory
  harness -p "<prompt>" [flags]   Run one headless turn and print the result
  harness login <server-url>      Sign in with the device flow
  harness logout                  Revoke this device and delete credentials
  harness whoami                  Show the signed-in user
  harness demo                    Render the terminal identity (colors, logo, loaders)
  harness update                  Says how this binary is updated (no release of it yet)
  harness completion bash|zsh|fish  Print a shell completion script
  harness version

Flags:
`

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	if len(args) > 0 {
		switch args[0] {
		case "login":
			return cmdLogin(ctx, args[1:])
		case "logout":
			return cmdLogout(ctx)
		case "whoami":
			return cmdWhoami(ctx)
		case "demo":
			demo()
			return 0
		case "version", "--version":
			fmt.Println("harness " + config.Version)
			return 0
		case "completion":
			return runCompletion(args[1:])
		case "update":
			// Releases carry harness-client and the desktop app, not this terminal frontend (docs/release.md).
			return fail(errors.New("harness has no release builds yet, so it can't update itself: rebuild it from source. `harness-client update` updates the client"))
		}
	}
	return cmdMain(ctx, args)
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "Error:", err)
	return 1
}

func cmdLogin(ctx context.Context, args []string) int {
	if len(args) != 1 {
		return fail(errors.New("usage: harness login <server-url>"))
	}
	url := strings.TrimRight(args[0], "/")
	host, _ := os.Hostname()
	svc := auth.NewConnectService(url, nil)
	store := auth.DefaultStore(config.CredentialsFile())
	_, grant, err := auth.Login(ctx, svc, store, url, auth.DeviceInfo{ClientName: "harness", DeviceName: host, OS: runtime.GOOS, Arch: runtime.GOARCH}, os.Stdout)
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

func protoMode(m permissions.Mode) agentv1.PermissionMode {
	switch m {
	case "":
		return agentv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED
	case permissions.ModeAcceptEdits:
		return agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS
	case permissions.ModePlan:
		return agentv1.PermissionMode_PERMISSION_MODE_PLAN
	case permissions.ModeDontAsk:
		return agentv1.PermissionMode_PERMISSION_MODE_DONT_ASK
	case permissions.ModeBypassPermissions:
		return agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS
	case permissions.ModeAuto:
		return agentv1.PermissionMode_PERMISSION_MODE_AUTO
	}
	return agentv1.PermissionMode_PERMISSION_MODE_DEFAULT
}

// mainFlags are the flags of the interactive session and of -p.
type mainFlags struct {
	printMode                    *bool
	outputFormat                 *string
	model                        *string
	mode                         *string
	outputStyle                  *string
	resume                       *string
	cont                         *bool
	serverURL                    *string
	verbose                      *bool
	help                         *bool
	allowed, disallowed, addDirs []string
}

func newMainFlags() (*pflag.FlagSet, *mainFlags) {
	fs := pflag.NewFlagSet("harness", pflag.ContinueOnError)
	f := &mainFlags{}
	fs.SetInterspersed(true)
	f.printMode = fs.BoolP("print", "p", false, "run one headless turn and print the result")
	f.outputFormat = fs.String("output-format", "text", "with -p: text | json | stream-json")
	f.model = fs.String("model", "", "model id (default: the server's main slot)")
	f.mode = fs.String("permission-mode", "", "default | acceptEdits | plan | auto | dontAsk | bypassPermissions (unset: settings, else auto)")
	f.outputStyle = fs.String("output-style", "", "output style: a built-in id (default, proactive, concise, explanatory, learning) or a user/project style name (default: settings outputStyle)")
	fs.StringArrayVar(&f.allowed, "allowedTools", nil, `tools allowed without asking, e.g. "Bash(git *) Edit"`)
	fs.StringArrayVar(&f.disallowed, "disallowedTools", nil, "tools that are always denied")
	fs.StringArrayVar(&f.addDirs, "add-dir", nil, "additional working directories")
	f.resume = fs.StringP("resume", "r", "", "resume a session by id")
	f.cont = fs.BoolP("continue", "c", false, "with -p: continue the most recent session in this directory")
	f.serverURL = fs.String("server", "", "harness server URL (overrides stored credentials)")
	f.verbose = fs.Bool("verbose", false, "debug logging (to $HARNESS_HOME/cli.log in the TUI)")
	f.help = fs.BoolP("help", "h", false, "show help")
	completion.Choices(fs, "output-format", "text", "json", "stream-json")
	completion.Choices(fs, "permission-mode", "default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions")
	completion.Dirs(fs, "add-dir")
	return fs, f
}

func cmdMain(ctx context.Context, args []string) int {
	fs, f := newMainFlags()
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		fmt.Fprint(os.Stderr, usage+fs.FlagUsages())
		return 2
	}
	if *f.help {
		fmt.Print(usage + fs.FlagUsages())
		return 0
	}
	// Unset leaves the mode to settings (permissions.defaultMode), else auto.
	var pm permissions.Mode
	if *f.mode != "" {
		var err error
		if pm, err = permissions.ParseMode(*f.mode); err != nil {
			return fail(err)
		}
	}
	if *f.printMode {
		level := slog.LevelWarn
		if *f.verbose {
			level = slog.LevelDebug
		}
		return sdk.Run(ctx, sdk.Options{
			Prompt: strings.Join(fs.Args(), " "), OutputFormat: *f.outputFormat,
			Bootstrap: bootstrap.Options{
				ServerURL: *f.serverURL, Model: *f.model, OutputStyle: *f.outputStyle, PermissionMode: pm, AllowedTools: f.allowed, DisallowedTools: f.disallowed,
				AdditionalDirs: f.addDirs, Continue: *f.cont, Resume: *f.resume, Entrypoint: "cli",
				Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})),
			},
		})
	}
	if len(fs.Args()) > 0 {
		return fail(fmt.Errorf("unexpected arguments %q (use -p for a headless prompt)", strings.Join(fs.Args(), " ")))
	}

	cwd, _ := os.Getwd()
	var logger *slog.Logger
	if *f.verbose || os.Getenv("HARNESS_DEBUG") == "1" {
		if f, err := os.OpenFile(filepath.Join(config.Home(), "cli.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			defer f.Close()
			logger = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
		}
	}
	env := brand.Detect()
	fmt.Fprint(os.Stderr, "Starting harness…\r")
	conn, err := client.Open(ctx, client.Options{
		Cwd: cwd, Model: *f.model, OutputStyle: *f.outputStyle, PermissionMode: protoMode(pm), AllowedTools: f.allowed, DisallowedTools: f.disallowed,
		AddDirs: f.addDirs, Resume: *f.resume, ServerURL: *f.serverURL, Logger: logger,
		Trust: func(tr *agentv1.GetWorkspaceTrustResponse) bool { return askTrust(env, cwd, tr) },
	})
	fmt.Fprint(os.Stderr, "\x1b[2K\r")
	if errors.Is(err, client.ErrUntrusted) {
		return 1
	}
	if err != nil {
		return fail(err)
	}
	defer conn.Close()
	if offer := conn.ProjectImportOffer(ctx); offer != "" {
		fmt.Fprintln(os.Stderr, offer)
	}
	if err := tui.Run(ctx, tui.Options{Conn: conn, Env: env, Cwd: cwd}); err != nil {
		return fail(err)
	}
	return 0
}

// askTrust is the workspace trust prompt shown before the first session in a folder (reference
// §1a). It runs before the TUI starts, on the plain terminal.
func askTrust(env brand.Env, cwd string, tr *agentv1.GetWorkspaceTrustResponse) bool {
	fmt.Fprint(os.Stderr, "\x1b[2K\r")
	s := render.New(env)
	width := 80
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 20 {
		width = w
	}
	var enables []string
	if tr.GetHasProjectHooks() {
		enables = append(enables, "project hooks")
	}
	if tr.GetHasProjectMcpServers() {
		enables = append(enables, "MCP servers from .mcp.json")
	}
	if tr.GetHasCommittedAllowRules() {
		enables = append(enables, "allow rules from committed settings")
	}
	body := []string{s.Bold.Render(cwd), "", "Quick safety check: is this a project you created or one you trust? If not, review what is in this folder first.", "", "harness will be able to read, edit, and execute files here."}
	if len(enables) > 0 {
		body = append(body, "Trusting it also enables "+strings.Join(enables, ", ")+".")
	}
	var wrapped []string
	for _, b := range body {
		wrapped = append(wrapped, render.Wrap(b, width-4)...)
	}
	fmt.Println(s.PromptBox(render.Prompt{
		Title: "Accessing workspace", Body: wrapped,
		Options: []render.Option{{Label: "Yes, I trust this folder"}, {Label: "No, exit"}},
		Hint:    "Type 1 or 2, then Enter",
	}, width))
	fmt.Print(s.Accent.Render("❯") + " ")
	var answer string
	_, _ = fmt.Scanln(&answer)
	return strings.TrimSpace(answer) == "1" || strings.EqualFold(strings.TrimSpace(answer), "y")
}

// runCompletion prints the completion script of a shell.
func runCompletion(args []string) int {
	if len(args) != 1 || !slices.Contains(completion.Shells, args[0]) {
		fmt.Fprint(os.Stderr, completion.Usage("harness"))
		if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
			return 0
		}
		return 2
	}
	if err := completion.Write(os.Stdout, args[0], commandTree()); err != nil {
		return fail(err)
	}
	return 0
}

// commandTree describes harness's commands for completion, with the flag sets they parse.
func commandTree() *completion.Command {
	fs, _ := newMainFlags()
	return &completion.Command{
		Name: "harness", Flags: fs,
		Subcommands: []*completion.Command{
			{Name: "login", Short: "Sign in with the device flow"},
			{Name: "logout", Short: "Revoke this device and delete credentials"},
			{Name: "whoami", Short: "Show the signed-in user"},
			{Name: "demo", Short: "Render the terminal identity"},
			{Name: "update", Short: "Say how this binary is updated"},
			{Name: "completion", Short: "Print a shell completion script", Args: completion.Shells},
			{Name: "version", Short: "Print the version"},
		},
	}
}
