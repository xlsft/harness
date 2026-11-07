package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/cibot"
	"github.com/xlsft/harness/packages/client/vcs"
)

const ciUsage = `harness-client ci — the PR review bot and the @mention trigger, for CI runners (docs/ci.md)

Usage:
  harness-client ci review [flags]      Review the pull request of this CI run and post findings as review comments
  harness-client ci mention [flags]     Answer the comment of this CI run if it calls the bot and its author has write access
  harness-client ci login <server-url>  Register a device for CI with the device flow and print its token for a CI secret

Flags:
`

// ciFlags are the flags of `ci review` and `ci mention`.
type ciFlags struct {
	fs            *pflag.FlagSet
	server        *string
	model         *string
	effort        *string
	maxTurns      *int
	maxBudget     *float64
	allowed       *[]string
	trigger       *string
	prompt        *string
	maxComments   *int
	includeDrafts *bool
	failOn        *string
	verbose       *bool
}

func newCIFlags() *ciFlags {
	fs := pflag.NewFlagSet("harness-client ci", pflag.ContinueOnError)
	return &ciFlags{
		fs:            fs,
		server:        fs.String("server", "", "server URL (default: HARNESS_SERVER_URL or stored credentials)"),
		model:         fs.String("model", "", "model id (default: the server's main slot)"),
		effort:        fs.String("effort", "", "low | medium | high | xhigh | max"),
		maxTurns:      fs.Int("max-turns", 0, "maximum tool-use round trips (0 = unlimited)"),
		maxBudget:     fs.Float64("max-budget-usd", 0, "stop the agent once it has spent this much (0 = no limit)"),
		allowed:       fs.StringArray("allowed-tools", nil, `tools allowed on top of the read-only ones, e.g. "Bash(go test *)"`),
		trigger:       fs.String("trigger", cibot.DefaultTrigger, "mention: the word that calls the bot"),
		prompt:        fs.String("prompt", "", "review: what the repository's owners want kept in mind"),
		maxComments:   fs.Int("max-comments", cibot.DefaultMaxComments, "review: line comments per run; the rest go into the summary"),
		includeDrafts: fs.Bool("include-drafts", false, "review: review draft pull requests too"),
		failOn:        fs.String("fail-on", "", "review: fail the job on a finding of this severity or worse (high, medium or low)"),
		verbose:       fs.Bool("verbose", false, "debug logging"),
	}
}

func runCI(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, ciUsage+newCIFlags().fs.FlagUsages())
		return 2
	}
	if args[0] == "login" {
		return ciLogin(ctx, args[1:])
	}
	if args[0] != "review" && args[0] != "mention" {
		fmt.Fprint(os.Stderr, ciUsage+newCIFlags().fs.FlagUsages())
		return 2
	}
	f := newCIFlags()
	if err := f.fs.Parse(args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 2
	}
	switch *f.failOn {
	case "", "high", "medium", "low":
	default:
		return fail(errors.New("--fail-on must be high, medium or low"))
	}
	if *f.maxBudget < 0 {
		return fail(errors.New("--max-budget-usd must not be negative"))
	}
	// The adapters and the CLIs keep the secrets; what the agent starts does not inherit them.
	base, _, err := sharedAdapters(*f.server)
	if err != nil {
		return fail(err)
	}
	runner := &vcs.Runner{Timeout: 2 * time.Minute, Env: os.Environ()}
	for _, name := range cibot.SecretEnv {
		_ = os.Unsetenv(name)
	}
	base.Model, base.Effort, base.MaxTurns, base.MaxBudgetUSD = *f.model, *f.effort, *f.maxTurns, *f.maxBudget
	base.Logger = newLogger(*f.verbose)
	base.VCSProviders = nil
	// A review run keeps to the gateway and prompts: no sync, transcript upload or MCP servers in CI (as before serve
	// sessions got their server connection; docs/design/diagnostics.md).
	base.Tokens, base.User, base.SyncHub, base.MCP = nil, nil, nil, nil
	c := cibot.Config{
		Bootstrap: base,
		Runner:    runner,
		Getenv:    os.Getenv, ReadFile: os.ReadFile, Stdout: os.Stdout, Stderr: os.Stderr,
		Trigger: *f.trigger, AllowedTools: *f.allowed, Guidance: *f.prompt, MaxComments: *f.maxComments, IncludeDrafts: *f.includeDrafts, FailOn: *f.failOn,
	}
	if args[0] == "review" {
		return cibot.Review(ctx, c)
	}
	return cibot.Mention(ctx, c)
}

// ciLogin runs the device flow for a device of its own and prints only the token on stdout, for a CI secret. Nothing is
// stored: this machine's own sign-in stays as it is. The token lasts until the device is revoked in Settings > Account.
func ciLogin(ctx context.Context, args []string) int {
	if len(args) != 1 {
		return fail(errors.New("usage: harness-client ci login <server-url>"))
	}
	url := strings.TrimRight(args[0], "/")
	host, _ := os.Hostname()
	info := auth.DeviceInfo{ClientName: "harness-client ci", DeviceName: "ci (registered on " + host + ")", OS: runtime.GOOS, Arch: runtime.GOARCH}
	prompt := func(uri, code string) {
		fmt.Fprintf(os.Stderr, "To register this CI device, open %s\nand confirm the code: %s\n", uri, code)
	}
	_, grant, err := auth.LoginWith(ctx, auth.NewConnectService(url, nil), auth.NewMemoryStore(nil), url, info, prompt)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintln(os.Stderr, "Store this as a CI secret, HARNESS_DEVICE_TOKEN. It is not shown again.")
	fmt.Println(grant.DeviceToken)
	return 0
}
