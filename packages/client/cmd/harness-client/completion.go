package main

import (
	"fmt"
	"os"
	"slices"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/completion"
)

// runCompletion prints the completion script of a shell.
func runCompletion(args []string) int {
	if len(args) != 1 || !slices.Contains(completion.Shells, args[0]) {
		fmt.Fprint(os.Stderr, completion.Usage("harness-client"))
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

func boolFlags(name string, flags ...[2]string) *pflag.FlagSet {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	for _, f := range flags {
		fs.Bool(f[0], false, f[1])
	}
	return fs
}

// commandTree describes harness-client's commands for completion, with the flag sets they parse.
func commandTree() *completion.Command {
	printFS, _ := newPrintFlags()
	serveFS, _ := newServeFlags()
	updateFS, _, _ := newUpdateFlags()
	doctorFS, _, _, _, _ := newDoctorFlags()
	ciFS := newCIFlags().fs
	syncFS := pflag.NewFlagSet("sync", pflag.ContinueOnError)
	syncFS.Bool("watch", false, "keep following changes until interrupted")
	syncFS.Bool("resync", false, "drop the cursor and rebuild the cache from a full snapshot")
	syncFS.String("server", "", "harness server URL")
	syncFS.BoolP("verbose", "v", false, "log sync activity")
	importFS := boolFlags("import claude",
		[2]string{"dry-run", "show what would be imported"}, [2]string{"sessions", "also copy and upload sessions"},
		[2]string{"no-upload", "with --sessions: only queue sessions"}, [2]string{"include-secrets", "upload env values that look like secrets"},
		[2]string{"force", "overwrite documents that already exist on the server"}, [2]string{"project", "import a project folder"})
	checkFS := pflag.NewFlagSet("permissions check", pflag.ContinueOnError)
	checkFS.String("command", "", "the command line, for Bash")
	checkFS.String("file", "", "the path, for file tools")
	checkFS.String("input", "", "the tool's input as JSON")
	checkFS.String("mode", "", "judge it under this mode")
	completion.Choices(checkFS, "mode", "default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions")
	checkFS.Bool("read-only", false, "the tool says this call only reads")
	checkFS.String("permission-profile", "", "use this permission profile")
	jsonFS := boolFlags("json", [2]string{"json", "print JSON"})

	pluginVerbs := []*completion.Command{
		{Name: "list", Short: "Installed plugins"}, {Name: "info", Short: "What a plugin brings"},
		{Name: "install", Short: "Install from a marketplace, a folder or a repository"}, {Name: "approve", Short: "Let a plugin run on this device"},
		{Name: "revoke", Short: "Take back an approval"}, {Name: "enable", Short: "Turn a plugin on"}, {Name: "disable", Short: "Turn a plugin off"},
		{Name: "pin", Short: "Keep a plugin at its version"}, {Name: "unpin", Short: "Let a plugin update"}, {Name: "update", Short: "Update plugins"},
		{Name: "remove", Short: "Uninstall a plugin"}, {Name: "marketplace", Short: "Add, list and remove marketplaces"},
		{Name: "new", Short: "Start a plugin"}, {Name: "validate", Short: "Check a plugin folder"},
	}
	return &completion.Command{
		Name: "harness-client", Flags: printFS,
		Subcommands: []*completion.Command{
			{Name: "login", Short: "Sign in with the device flow", Flags: jsonFS},
			{Name: "logout", Short: "Revoke this device and delete credentials"},
			{Name: "whoami", Short: "Show the signed-in user", Flags: jsonFS},
			{Name: "serve", Short: "Serve AgentService to frontends", Flags: serveFS},
			{Name: "doctor", Short: "Check what harness-client needs on this machine", Flags: doctorFS},
			{Name: "update", Short: "Replace this binary with the newest release", Flags: updateFS},
			{Name: "completion", Short: "Print a shell completion script", Args: completion.Shells},
			{Name: "import", Short: "Import from Claude Code", Subcommands: []*completion.Command{{Name: "claude", Short: "Configuration and sessions", Flags: importFS}}},
			{Name: "sync", Short: "Sync documents with the server", Flags: syncFS},
			{Name: "plugin", Short: "Install, approve and manage plugins", Subcommands: pluginVerbs},
			{Name: "ci", Short: "The PR review bot and the @mention trigger", Subcommands: []*completion.Command{
				{Name: "review", Short: "Review a pull request", Flags: ciFS}, {Name: "mention", Short: "Answer an @mention", Flags: ciFS},
				{Name: "login", Short: "Sign in a CI runner"},
			}},
			{Name: "config", Short: "What the settings of this folder say", Subcommands: []*completion.Command{
				{Name: "debug", Short: "Every setting and where it comes from"}, {Name: "features", Short: "What is on and off"},
			}},
			{Name: "permissions", Short: "What the rules of this folder decide", Subcommands: []*completion.Command{
				{Name: "check", Short: "Judge one tool call without running it", Flags: checkFS},
			}},
			{Name: "insights", Short: "A report on past sessions"},
			{Name: "version", Short: "Print the version"},
		},
	}
}
