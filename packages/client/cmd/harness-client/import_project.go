package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/importer"
	"github.com/xlsft/harness/packages/client/permissions"
)

const importProjectUsage = `Usage: harness-client import --project [folder] [flags]

Brings a folder's Claude Code files into its .harness and AGENTS.md: CLAUDE.md,
.claude/rules, settings, skills, commands, agents, output styles and workflows.
Nothing under .claude changes. Without this they are not read. Hooks and allow
rules apply only once the folder is trusted; local settings wait for that.

Flags:
`

// runImportProject implements `harness-client import --project`. Running the command is the consent.
func runImportProject(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fs := pflag.NewFlagSet("import --project", pflag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, importProjectUsage); fs.PrintDefaults() }
	_ = fs.Bool("project", true, "import a folder's files")
	dryRun := fs.Bool("dry-run", false, "show what would be imported without writing anything")
	force := fs.Bool("force", false, "replace what differs (an earlier import, a file already in .harness)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	abs, err := filepath.Abs(dir)
	if err == nil {
		var st os.FileInfo
		if st, err = os.Stat(abs); err == nil && !st.IsDir() {
			err = fmt.Errorf("%s is not a folder", dir)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	home, _ := os.UserHomeDir()
	root := importer.ProjectRoot(abs, home)
	rep, err := importer.RunProject(ctx, importer.ProjectOptions{Root: root, Home: home, Trusted: permissions.IsTrusted(root), DryRun: *dryRun, Force: *force})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	if len(rep.Items) == 0 {
		fmt.Println("Nothing to import in " + root)
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tFROM\tTO\tSTATUS\tNOTE")
	for _, it := range rep.Items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", it.Kind, it.Source, it.Target, it.Status, it.Note)
	}
	tw.Flush()
	if rep.HasErrors() {
		return 1
	}
	return 0
}
