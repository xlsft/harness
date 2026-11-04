package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/importer"
	"github.com/xlsft/harness/packages/client/syncclient"
)

const importUsage = `Usage: harness-client import claude [flags]

Imports the user-level Claude Code configuration (~/.claude: CLAUDE.md,
settings.json, skills, agents, commands, output styles) into the harness
server. MCP servers from ~/.claude.json are listed for manual setup.

Flags:
`

// runImport implements `harness-client import claude`.
func runImport(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) == 0 || args[0] != "claude" {
		fmt.Fprint(os.Stderr, importUsage)
		return 2
	}
	home, _ := os.UserHomeDir()
	fs := pflag.NewFlagSet("import claude", pflag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, importUsage); fs.PrintDefaults() }
	dryRun := fs.Bool("dry-run", false, "show what would be imported without uploading or copying anything")
	sessions := fs.Bool("sessions", false, "also copy ~/.claude/projects/**.jsonl to ~/.harness/projects and upload them")
	noUpload := fs.Bool("no-upload", false, "with --sessions: only queue sessions; the next session start uploads them")
	secrets := fs.Bool("include-secrets", false, "upload env values that look like secrets (API keys, tokens)")
	force := fs.Bool("force", false, "overwrite documents that already exist on the server")
	claudeDir := fs.String("claude-dir", filepath.Join(home, ".claude"), "Claude Code configuration directory")
	claudeJSON := fs.String("claude-json", filepath.Join(home, ".claude.json"), "Claude Code global state file (MCP servers)")
	server := fs.String("server", "", "harness server URL (defaults to the signed-in server)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}

	opts := importer.Options{
		ClaudeHome: *claudeDir, ClaudeJSON: *claudeJSON, ProjectsDir: config.ProjectsDir(),
		DryRun: *dryRun, Sessions: *sessions, IncludeSecrets: *secrets, Force: *force,
	}
	var objects *syncclient.ObjectQueue
	if !*dryRun {
		tokens, url, _, err := bootstrap.Tokens(*server)
		if err != nil {
			if errors.Is(err, auth.ErrNotLoggedIn) {
				fmt.Fprintln(os.Stderr, "Error: not signed in; run `harness-client login <server-url>` or use --dry-run")
				return 1
			}
			fmt.Fprintln(os.Stderr, "Error:", err)
			return 1
		}
		client := syncclient.NewClient(url, nil, tokens)
		opts.Putter = client
		deviceID, deviceName := deviceIdentity()
		// Images and documents in transcripts go to the object store, when the server has one.
		objects = syncclient.SharedObjectQueue(client, nil)
		opts.Queue = syncclient.NewUploader(syncclient.UploaderOptions{API: client, DeviceID: deviceID, DeviceName: deviceName, Objects: objects})
		opts.UploadSessions = !*noUpload
	}
	rep, err := importer.Run(ctx, opts)
	if objects != nil && *sessions && !*noUpload {
		objects.Flush(ctx)
	}
	if rep != nil {
		rep.Print(os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	if rep.HasErrors() {
		return 1
	}
	return 0
}

// deviceIdentity returns the signed-in device id (if any) and the host name.
func deviceIdentity() (id, name string) {
	name, _ = os.Hostname()
	if c, err := auth.DefaultStore(config.CredentialsFile()).Load(); err == nil && c != nil {
		id = c.DeviceID
	}
	return id, name
}
