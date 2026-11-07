// Command harness-client is the headless harness client: device login,
// headless runs (`-p`) and the AgentService server for frontends.
package main

import (
	"context"
	"encoding/json"
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
	"github.com/xlsft/harness/packages/client/completion"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/decision"
	"github.com/xlsft/harness/packages/client/doctor"
	"github.com/xlsft/harness/packages/client/extensions"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	"github.com/xlsft/harness/packages/client/localmodels"
	"github.com/xlsft/harness/packages/client/mcp"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/relay"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/sdk"
	"github.com/xlsft/harness/packages/client/selfupdate"
	"github.com/xlsft/harness/packages/client/server"
	"github.com/xlsft/harness/packages/client/speech"
	"github.com/xlsft/harness/packages/client/subagent"
	"github.com/xlsft/harness/packages/client/syncclient"
	"github.com/xlsft/harness/packages/client/tracker"
	"github.com/xlsft/harness/packages/client/vcs"
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
  harness-client ci <review|mention>    The PR review bot and the @mention trigger, for CI runners (docs/ci.md)
  harness-client update [--check]       Replace this binary with the newest release (verified)
  harness-client doctor [--json]        Check sign-in, the server, versions, keychain, sandbox, MCP, disk, git, proxy
  harness-client completion bash|zsh|fish
                                        Print a shell completion script
  harness-client plugin [help|...]      Install, approve and manage plugins and marketplaces
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
			return cmdWhoami(ctx, args[1:])
		case "serve":
			return cmdServe(ctx, args[1:])
		case "import":
			return runImport(args[1:])
		case "sync":
			return runSync(args[1:])
		case "update":
			return runUpdate(ctx, args[1:])
		case "doctor":
			return cmdDoctor(ctx, args[1:])
		case "completion":
			return runCompletion(args[1:])
		case "plugin":
			return runPlugin(args[1:])
		case "ci":
			return runCI(ctx, args[1:])
		case "config":
			return runConfig(ctx, args[1:])
		case "permissions":
			return runPermissions(ctx, args[1:])
		case "insights":
			return runInsights(args[1:])
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

// jsonLine prints one JSON object per line (the --json output of login and whoami, read by the desktop app).
func jsonLine(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func cmdLogin(ctx context.Context, args []string) int {
	asJSON := len(args) > 0 && args[0] == "--json"
	if asJSON {
		args = args[1:]
	}
	if len(args) != 1 {
		return fail(errors.New("usage: harness-client login [--json] <server-url>"))
	}
	url := strings.TrimRight(args[0], "/")
	host, _ := os.Hostname()
	svc := auth.NewConnectService(url, nil)
	store := auth.DefaultStore(config.CredentialsFile())
	info := auth.DeviceInfo{ClientName: "harness-client", DeviceName: host, OS: runtime.GOOS, Arch: runtime.GOARCH}
	prompt := func(uri, code string) { fmt.Printf("To sign in, open %s\nand confirm the code: %s\n", uri, code) }
	if asJSON {
		prompt = func(uri, code string) {
			jsonLine(map[string]string{"event": "code", "verification_uri": uri, "user_code": code})
		}
	}
	_, grant, err := auth.LoginWith(ctx, svc, store, url, info, prompt)
	if err != nil {
		if asJSON {
			jsonLine(map[string]string{"event": "error", "message": err.Error()})
			return 1
		}
		return fail(err)
	}
	if asJSON {
		jsonLine(map[string]string{"event": "signed_in", "server": url, "email": grant.User.Email, "device_id": grant.DeviceID})
		return 0
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

func cmdWhoami(ctx context.Context, args []string) int {
	asJSON := len(args) > 0 && args[0] == "--json"
	m, err := manager()
	if err == nil {
		var me *auth.Me
		if me, err = m.Me(ctx); err == nil && asJSON {
			jsonLine(map[string]any{"signed_in": true, "server": m.Credentials().Server, "email": me.User.Email, "name": me.User.Name, "role": me.User.Role, "device": me.Device.Name})
			return 0
		}
		if err == nil {
			fmt.Printf("%s <%s> role=%s\nserver: %s\ndevice: %s %s\n", me.User.Name, me.User.Email, me.User.Role, m.Credentials().Server, me.Device.ID, me.Device.Name)
			return 0
		}
	}
	if asJSON {
		// Not signed in (or the session is no longer valid): a normal answer, not a failure.
		jsonLine(map[string]any{"signed_in": false, "error": err.Error()})
		return 0
	}
	return fail(err)
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
	// Requests to local models (Ollama, LM Studio, ...) report their token usage to the server.
	localmodels.StartUsageReporting(context.Background(), url, hc, tokens, nil)
	return bootstrap.Options{
		ServerURL:       url,
		Gateway:         localmodels.Wrap(connectgw.New(url, hc, tokens)),
		Prompts:         prompts.NewConnectSource(url, hc, tokens),
		DecisionService: decision.NewClient(url, hc, tokens),
		Search:          web.NewSearchClient(url, hc, tokens),
		VCSProviders:    vcs.WithServerProvider(vcs.SharedServerProvider(url, hc, tokens)),
	}, email, nil
}

// serveFlags are the flags of `serve`.
type serveFlags struct {
	socket      *string
	serverURL   *string
	allowed     *[]string
	disallowed  *[]string
	addDirs     *[]string
	outputStyle *string
	agents      *string
	profile     *string
	bare        *bool
	safe        *bool
	remote      *bool
	verbose     *bool
	tcp         *tcpFlags
}

func newServeFlags() (*pflag.FlagSet, *serveFlags) {
	fs := pflag.NewFlagSet("serve", pflag.ContinueOnError)
	f := &serveFlags{}
	f.socket = fs.String("socket", "", "unix socket path to listen on")
	f.serverURL = fs.String("server", "", "harness server URL (overrides stored credentials)")
	f.allowed = fs.StringArray("allowedTools", nil, `permission rules allowed without asking, e.g. "Bash(npm test *) Edit"`)
	f.disallowed = fs.StringArray("disallowedTools", nil, "permission rules that are always denied")
	f.addDirs = fs.StringArray("add-dir", nil, "additional working directories for every session")
	f.outputStyle = fs.String("output-style", "", "output style for every session: a built-in id (default, proactive, concise, explanatory, learning) or a user/project style name")
	f.agents = fs.String("agents", "", `subagent types for every session of this run, as JSON: {"name": {"description": "...", "prompt": "...", "tools": [...], ...}}`)
	f.profile = fs.String("permission-profile", "", "the permission profile for every session: a name under permissionProfiles in settings")
	f.bare = fs.Bool("bare", false, bareHelp)
	f.safe = fs.Bool("safe-mode", false, safeHelp)
	f.remote = fs.Bool("remote-control", true, "mirror sessions to the harness server so they can be watched and controlled from the web (HARNESS_REMOTE_CONTROL=0 disables)")
	f.verbose = fs.Bool("verbose", false, "debug logging")
	f.tcp = addTCPFlags(fs)
	completion.Dirs(fs, "add-dir")
	return fs, f
}

func cmdServe(ctx context.Context, args []string) int {
	fs, f := newServeFlags()
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := f.tcp.validate(*f.socket); err != nil {
		return fail(err)
	}
	log := newLogger(*f.verbose)
	base, email, err := sharedAdapters(*f.serverURL)
	if err != nil {
		return fail(err)
	}
	base.AllowedTools, base.DisallowedTools, base.AdditionalDirs, base.Logger = *f.allowed, *f.disallowed, *f.addDirs, log
	if base.Agents, err = parseAgents(*f.agents); err != nil {
		return fail(err)
	}
	base.PermissionProfile = *f.profile
	applyStartup(*f.bare, *f.safe)
	base.OutputStyle = *f.outputStyle
	// Interactive frontends start sessions in auto mode unless the request or settings say otherwise.
	base.FallbackPermissionMode = permissions.ModeAuto
	srv := server.New(base)
	srv.UserEmail = email
	srv.SSHBinaries = sshBinaries
	srv.DoctorOptions = func(context.Context) doctor.Options { return doctorOptions(*f.serverURL) }
	srv.Logger = log
	// A web UI that opens sessions by ?session=<id> (Initialize web_url); without one, the server's own UI (ServerHealth).
	srv.WebURL = strings.TrimRight(strings.TrimSpace(os.Getenv("HARNESS_WEB_URL")), "/")
	// Why sessions are not mirrored (the session relay switch shows it); cleared when a relay host attaches.
	srv.RelayOffReason = relay.DisabledReason(*f.remote, "", false)
	if tokens, url, user, err := bootstrap.Tokens(*f.serverURL); err == nil {
		// Modes the server's policy disables: not offered, refused, and live sessions in one are switched.
		policy := bootstrap.WatchModePolicy(ctx, url, rpc.HTTPClient(url), tokens, user, log)
		srv.SetModePolicy(policy)
		// A version outside what the organization allows still runs; the user is told here and in Settings > About.
		m := policy.Managed()
		if msg := (selfupdate.Bounds{Min: m.MinClientVersion, Max: m.MaxClientVersion}).Message("harness-client", config.Version); msg != "" {
			fmt.Fprintln(os.Stderr, "Warning:", msg)
		}
		// The server's version, and its web UI when HARNESS_WEB_URL names none (InitializeResponse.web_url).
		srv.ServerHealth = func(ctx context.Context) server.Health { return server.ReadServerHealth(ctx, rpc.HTTPClient(url), url) }
		// Settings screens (providers, classifier) reach the server through this client.
		srv.Core = &server.CoreProxy{URL: url, HTTP: rpc.HTTPClient(url), Tokens: tokens}
		srv.Trackers = tracker.New(config.Home(), syncclient.NewClient(url, rpc.HTTPClient(url), tokens))
		srv.MCPClient = mcp.NewClient(url, rpc.HTTPClient(url), tokens)
		srv.SkillSearch = extensions.NewSkillSearch(url, rpc.HTTPClient(url), tokens)
		// Frontends read the object store through this client, with a ticket instead of the agent token (<img>, <video>).
		srv.Objects = server.NewObjectProxy(url, rpc.HTTPClient(url), tokens)
		// Dictation and read-aloud with the server's global speech providers (local engines need no server).
		srv.SpeechCore = speech.NewCore(url, rpc.HTTPClient(url), tokens)
		if srv.RelayOffReason = relay.DisabledReason(*f.remote, url, tokens != nil); srv.RelayOffReason == "" {
			o := relay.Options{ServerURL: url, HTTP: rpc.HTTPClient(url), Tokens: tokens, Logger: log}
			if os.Getenv("HARNESS_NO_SYNC") != "1" && tokens != nil {
				o.Objects = syncclient.SharedObjectQueue(syncclient.NewClient(url, rpc.HTTPClient(url), tokens), log)
			}
			relay.Attach(ctx, srv, o)
		}
		// Per-user session state (pin, unread, archive, rename) and session deletes go to the server.
		if os.Getenv("HARNESS_NO_SYNC") != "1" && url != "" && tokens != nil {
			sc := syncclient.NewClient(url, rpc.HTTPClient(url), tokens)
			srv.Sync, srv.Preferences, srv.ImportTo, srv.SkillDocs = sc, sc, sc, sc
			srv.AutoMemory, srv.Transcripts = syncclient.AutoMemoryDocs{API: sc}, sc
			srv.CoreURL = url
			srv.ScheduleSync = bootstrap.WatchSchedules(ctx, url, rpc.HTTPClient(url), tokens, user, log)
			// The session list also shows the sessions of the user's other machines; the relay viewer adds their
			// presence and drives the ones open there (HARNESS_REMOTE_CONTROL=0 turns it off with the host side).
			if creds, err := auth.DefaultStore(config.CredentialsFile()).Load(); err == nil && creds != nil {
				srv.DeviceID = creds.DeviceID
			}
			if relay.Enabled(true, url, true) {
				srv.Relay = server.NewRelayViewer(url, rpc.HTTPClient(url), tokens)
			}
		}
	}
	if err := serveListeners(ctx, srv, *f.socket, f.tcp); err != nil {
		return fail(err)
	}
	return 0
}

// printFlags are the flags of a headless run (and of the root command, for completion).
type printFlags struct {
	printMode                    *bool
	outputFormat                 *string
	inputFormat                  *string
	jsonSchema                   *string
	maxBudget                    *float64
	systemPrompt                 *string
	systemPromptFile             *string
	appendSystemPrompt           *string
	appendSystemPromptFile       *string
	partial                      *bool
	model                        *string
	effort                       *string
	mode                         *string
	maxTurns                     *int
	agents                       *string
	profile                      *string
	bare                         *bool
	safe                         *bool
	outputStyle                  *string
	cont                         *bool
	resume                       *string
	forkSession                  *bool
	skipPerms                    *bool
	sessionID                    *string
	noPersist                    *bool
	serverURL                    *string
	verbose                      *bool
	help                         *bool
	allowed, disallowed, addDirs []string
}

func newPrintFlags() (*pflag.FlagSet, *printFlags) {
	fs := pflag.NewFlagSet("harness-client", pflag.ContinueOnError)
	f := &printFlags{}
	fs.SetInterspersed(true)
	f.printMode = fs.BoolP("print", "p", false, "run one headless turn and print the result")
	f.outputFormat = fs.String("output-format", "text", "text | json | stream-json")
	f.inputFormat = fs.String("input-format", "text", "text | stream-json (user messages on stdin, one JSON object per line; needs --output-format stream-json)")
	f.jsonSchema = fs.String("json-schema", "", "make the final message one JSON value valid against this JSON schema (inline JSON or a file path); it comes back as structured_output")
	f.maxBudget = fs.Float64("max-budget-usd", 0, "stop before the next model request once the run has spent this much, subagents included (0 = no limit)")
	f.systemPrompt = fs.String("system-prompt", "", "replace the system prompt for this run")
	f.systemPromptFile = fs.String("system-prompt-file", "", "replace the system prompt with this file's text")
	f.appendSystemPrompt = fs.String("append-system-prompt", "", "add this text after the system prompt")
	f.appendSystemPromptFile = fs.String("append-system-prompt-file", "", "add this file's text after the system prompt")
	f.partial = fs.Bool("include-partial-messages", false, "with stream-json: include raw stream events")
	f.model = fs.String("model", "", "model id (default: the server's main slot)")
	f.effort = fs.String("effort", "", "low | medium | high | xhigh | max")
	f.mode = fs.String("permission-mode", "", "default | acceptEdits | plan | auto | dontAsk | bypassPermissions (default: settings, else default)")
	f.maxTurns = fs.Int("max-turns", 0, "maximum tool-use round trips (0 = unlimited)")
	f.agents = fs.String("agents", "", `subagent types for this run, as JSON: {"name": {"description": "...", "prompt": "...", "tools": [...], ...}}`)
	f.profile = fs.String("permission-profile", "", "the permission profile for this run: a name under permissionProfiles in settings")
	f.bare = fs.Bool("bare", false, bareHelp)
	f.safe = fs.Bool("safe-mode", false, safeHelp)
	f.outputStyle = fs.String("output-style", "", "output style: a built-in id (default, proactive, concise, explanatory, learning) or a user/project style name (default: settings outputStyle)")
	fs.StringArrayVar(&f.allowed, "allowedTools", nil, `tools allowed without asking, e.g. "Bash(git *) Edit"`)
	fs.StringArrayVar(&f.allowed, "allowed-tools", nil, "alias of --allowedTools")
	fs.StringArrayVar(&f.disallowed, "disallowedTools", nil, "tools that are always denied")
	fs.StringArrayVar(&f.disallowed, "disallowed-tools", nil, "alias of --disallowedTools")
	fs.StringArrayVar(&f.addDirs, "add-dir", nil, "additional working directories")
	f.cont = fs.BoolP("continue", "c", false, "continue the most recent session in this directory")
	f.resume = fs.StringP("resume", "r", "", "resume a session by id")
	f.forkSession = fs.Bool("fork-session", false, "with --continue/--resume: copy the session into a new id instead of appending to it")
	f.skipPerms = fs.Bool("dangerously-skip-permissions", false, "same as --permission-mode bypassPermissions")
	f.sessionID = fs.String("session-id", "", "use this id for a new session")
	f.noPersist = fs.Bool("no-session-persistence", false, "do not write a transcript")
	f.serverURL = fs.String("server", "", "harness server URL (overrides stored credentials)")
	f.verbose = fs.Bool("verbose", false, "debug logging")
	f.help = fs.BoolP("help", "h", false, "show help")
	completion.Choices(fs, "output-format", sdk.FormatText, sdk.FormatJSON, sdk.FormatStreamJSON)
	completion.Choices(fs, "input-format", sdk.FormatText, sdk.FormatStreamJSON)
	completion.Choices(fs, "effort", "low", "medium", "high", "xhigh", "max")
	completion.Choices(fs, "permission-mode", "default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions")
	completion.Dirs(fs, "add-dir")
	return fs, f
}

func cmdPrint(ctx context.Context, args []string) int {
	fs, f := newPrintFlags()
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		fmt.Fprint(os.Stderr, usage+fs.FlagUsages())
		return 2
	}
	if *f.help || !*f.printMode {
		fmt.Fprint(os.Stderr, usage+fs.FlagUsages())
		if *f.help {
			return 0
		}
		return 2
	}
	// An unset flag leaves the mode to settings (permissions.defaultMode).
	var pm permissions.Mode
	if *f.mode != "" {
		var err error
		if pm, err = permissions.ParseMode(*f.mode); err != nil {
			return fail(err)
		}
	}
	agentTypes, err := parseAgents(*f.agents)
	if err != nil {
		return fail(err)
	}
	applyStartup(*f.bare, *f.safe)
	if *f.skipPerms {
		pm = permissions.ModeBypassPermissions
	}
	if *f.forkSession && !*f.cont && *f.resume == "" {
		return fail(errors.New("--fork-session requires --continue or --resume"))
	}
	for _, r := range permissions.ParseRules(append(append([]string(nil), f.allowed...), f.disallowed...)) {
		if err := permissions.ValidateRule(r.String()); err != nil {
			return fail(err)
		}
	}
	for i, d := range f.addDirs {
		abs, err := filepath.Abs(d)
		if err != nil {
			return fail(err)
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return fail(fmt.Errorf("--add-dir %s: not a directory", d))
		}
		f.addDirs[i] = abs
	}
	switch *f.outputFormat {
	case sdk.FormatText, sdk.FormatJSON, sdk.FormatStreamJSON:
	default:
		return fail(fmt.Errorf("unknown --output-format %q", *f.outputFormat))
	}
	switch *f.inputFormat {
	case sdk.FormatText:
	case sdk.FormatStreamJSON:
		if *f.outputFormat != sdk.FormatStreamJSON {
			return fail(errors.New("--input-format stream-json requires --output-format stream-json"))
		}
	default:
		return fail(fmt.Errorf("unknown --input-format %q", *f.inputFormat))
	}
	if *f.maxBudget < 0 {
		return fail(errors.New("--max-budget-usd must not be negative"))
	}
	schema, err := sdk.ReadSchemaFlag(*f.jsonSchema)
	if err != nil {
		return fail(err)
	}
	sysPrompt, err := sdk.PromptFlag("--system-prompt", *f.systemPrompt, *f.systemPromptFile)
	if err != nil {
		return fail(err)
	}
	appendPrompt, err := sdk.PromptFlag("--append-system-prompt", *f.appendSystemPrompt, *f.appendSystemPromptFile)
	if err != nil {
		return fail(err)
	}
	return sdk.Run(ctx, sdk.Options{
		Prompt:         strings.Join(fs.Args(), " "),
		OutputFormat:   *f.outputFormat,
		InputFormat:    *f.inputFormat,
		IncludePartial: *f.partial,
		JSONSchema:     schema,
		Bootstrap: bootstrap.Options{
			ServerURL: *f.serverURL, Model: *f.model, Effort: *f.effort, MaxTurns: *f.maxTurns, OutputStyle: *f.outputStyle,
			MaxBudgetUSD: *f.maxBudget, SystemPrompt: sysPrompt, AppendSystemPrompt: appendPrompt,
			PermissionMode: pm, AllowedTools: f.allowed, DisallowedTools: f.disallowed, AdditionalDirs: f.addDirs,
			Continue: *f.cont, Resume: *f.resume, SessionID: *f.sessionID, NoPersistence: *f.noPersist, Fork: *f.forkSession,
			Logger: newLogger(*f.verbose), Agents: agentTypes, PermissionProfile: *f.profile,
		},
	})
}

// parseAgents reads --agents ("": none).
func parseAgents(raw string) ([]subagent.Type, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	return subagent.InlineDefinitions(raw)
}
