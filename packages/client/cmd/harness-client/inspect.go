package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/insights"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/settings"
)

const (
	bareHelp = "start with nothing of what normally loads by itself: no hooks, MCP servers, plugins, skills, instructions or memory. A policy of your organization stays in force"
	safeHelp = "start with nothing that runs by itself: no hooks, MCP servers, plugins or skills. Instructions and memory stay. For finding out what is breaking a session"
)

// applyStartup sets the switches of --bare and --safe-mode for this process and what it starts.
func applyStartup(bare, safe bool) {
	if !bare && !safe {
		return
	}
	for _, name := range []string{"HARNESS_NO_HOOKS", "HARNESS_NO_MCP", "HARNESS_NO_PLUGINS", "HARNESS_NO_SKILLS"} {
		_ = os.Setenv(name, "1")
	}
	if bare {
		_ = os.Setenv("HARNESS_NO_INSTRUCTIONS", "1")
	}
}

// localSettings reads the settings this folder sees without a running session: the files and the machine's own policy.
// What the server delivers shows in a session, with /debug-config.
func localSettings(ctx context.Context, profile string) (*settings.Settings, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	st, err := settings.Load(ctx, settings.Options{Cwd: cwd, Trusted: permissions.IsTrusted(cwd), CLI: settings.CLI{PermissionProfile: profile}})
	return st, cwd, err
}

func runConfig(ctx context.Context, args []string) int {
	verb := "debug"
	if len(args) > 0 {
		verb = args[0]
	}
	st, _, err := localSettings(ctx, "")
	if err != nil {
		return fail(err)
	}
	switch verb {
	case "debug":
		fmt.Println(st.DebugReport())
		fmt.Println("\nDocuments from the server are not read here; /debug-config in a session shows them too.")
	case "features":
		fmt.Println(settings.FeatureReport(st))
	default:
		fmt.Fprintln(os.Stderr, "usage: harness-client config [debug|features]")
		return 2
	}
	return 0
}

// runPermissions: `permissions check` says what the rules of this folder decide about a call, without running anything.
func runPermissions(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(os.Stderr, `usage: harness-client permissions check <tool> [--command "git push"] [--file path] [--input json] [--mode mode] [--permission-profile name]`)
		return 2
	}
	fs := pflag.NewFlagSet("permissions check", pflag.ContinueOnError)
	command := fs.String("command", "", "the command line, for Bash")
	file := fs.String("file", "", "the path, for file tools")
	input := fs.String("input", "", "the tool's input as JSON")
	mode := fs.String("mode", "", "judge it under this mode (default: settings, else default)")
	readOnly := fs.Bool("read-only", false, "the tool says this call only reads")
	profile := fs.String("permission-profile", "", "use this permission profile")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "name one tool, as a rule would: Bash, Read, WebFetch, mcp__server__tool")
		return 2
	}
	st, cwd, err := localSettings(ctx, *profile)
	if err != nil {
		return fail(err)
	}
	var pm permissions.Mode
	if *mode != "" {
		if pm, err = permissions.ParseMode(*mode); err != nil {
			return fail(err)
		}
	} else if st.Permissions.DefaultMode != "" {
		pm = permissions.Mode(st.Permissions.DefaultMode)
	}
	cfg := permissions.Config{Cwd: cwd, DefaultMode: pm, Trusted: st.Trusted, RestrictReads: st.Extras.RestrictReads}
	for _, r := range st.Permissions.Allow {
		cfg.Allow = append(cfg.Allow, r.Rule)
	}
	for _, r := range st.Permissions.Ask {
		cfg.Ask = append(cfg.Ask, r.Rule)
	}
	for _, r := range st.Permissions.Deny {
		cfg.Deny = append(cfg.Deny, r.Rule)
	}
	for _, d := range st.Permissions.AdditionalDirectories {
		cfg.AdditionalDirectories = append(cfg.AdditionalDirectories, d.Abs)
	}
	req := permissions.Request{ToolName: fs.Arg(0), Command: *command, FilePath: *file, ReadOnly: *readOnly, Cwd: cwd}
	if *input != "" {
		req.Input = json.RawMessage(*input)
	} else {
		raw := map[string]string{}
		if *command != "" {
			raw["command"] = *command
		}
		if *file != "" {
			raw["file_path"] = *file
		}
		if len(raw) > 0 {
			req.Input, _ = json.Marshal(raw)
		}
	}
	e := permissions.New(cfg)
	if err := e.SetMode(pm); pm != "" && err != nil {
		return fail(err)
	}
	d := e.Evaluate(req)
	fmt.Printf("%s under %s mode\n", d.Behavior, e.Mode())
	if d.Rule != "" {
		fmt.Printf("rule: %s\n", d.Rule)
	}
	if d.ResolvedBy != "" {
		fmt.Printf("by: %s\n", d.ResolvedBy)
	}
	if d.Message != "" {
		fmt.Printf("%s\n", d.Message)
	}
	if d.UserOnly {
		fmt.Println("Only the user can answer this one: no rule or mode gets past it.")
	}
	if d.Behavior == permissions.Ask {
		for _, s := range e.Suggest(req) {
			fmt.Printf("a rule that would allow it: %s\n", s)
		}
	}
	// Scripts can branch on the answer: 0 allow, 1 deny, 3 ask.
	switch d.Behavior {
	case permissions.Allow:
		return 0
	case permissions.Deny:
		return 1
	}
	return 3
}

func runInsights(args []string) int {
	fs := pflag.NewFlagSet("insights", pflag.ContinueOnError)
	days := fs.Int("days", 30, "how far back to look")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	r := insights.Build(insights.Options{Days: *days})
	if *asJSON {
		data, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return fail(err)
		}
		fmt.Println(string(data))
		return 0
	}
	fmt.Println(r.Text())
	return 0
}
