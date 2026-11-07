package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/completion"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/doctor"
	"github.com/xlsft/harness/packages/client/mcp"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/server"
)

func newDoctorFlags() (fs *pflag.FlagSet, asJSON, noMCP *bool, folder, serverURL *string) {
	fs = pflag.NewFlagSet("doctor", pflag.ContinueOnError)
	asJSON = fs.Bool("json", false, "print the report as JSON")
	noMCP = fs.Bool("no-mcp", false, "don't start MCP servers to check them")
	folder = fs.String("folder", "", "also check the MCP servers of this folder's .mcp.json (a trusted folder's are registered as a session would)")
	serverURL = fs.String("server", "", "harness server URL (overrides stored credentials)")
	completion.Dirs(fs, "folder")
	return
}

// cmdDoctor prints what works and what doesn't. Exit status 1 when a check failed, so scripts can use it.
func cmdDoctor(ctx context.Context, args []string) int {
	fs, asJSON, noMCP, folder, serverURL := newDoctorFlags()
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	o := doctorOptions(*serverURL)
	policy := sync.OnceValues(func() (permissions.ManagedPolicy, bool) {
		return bootstrap.ReadManagedPolicy(ctx, *serverURL, 5*time.Second, nil)
	})
	o.Policy = func(context.Context) (permissions.ManagedPolicy, bool) { return policy() }
	if !*noMCP && os.Getenv("HARNESS_NO_MCP") != "1" {
		m, _ := policy()
		inv := doctorInventory(*serverURL, m)
		dir := *folder
		if dir != "" {
			if abs, err := filepath.Abs(dir); err == nil {
				dir = abs
			}
		}
		o.MCP = func(ctx context.Context) ([]doctor.MCPResult, error) {
			return doctor.ProbeMCP(ctx, inv, dir, 20*time.Second)
		}
	}
	r := doctor.Run(ctx, o)
	if *asJSON {
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(b))
	} else {
		r.Text(os.Stdout)
	}
	if r.Status == doctor.Fail {
		return 1
	}
	return 0
}

// doctorOptions are the checks' view of this machine: the stored sign-in, its server, the keychain. Shared by the command
// and AgentService.RunDoctor (serve), which adds its live policy and MCP inventory.
func doctorOptions(serverOverride string) doctor.Options {
	o := doctor.Options{
		ClientVersion: config.Version,
		DataDir:       config.Home(),
		Credentials:   func() (string, error) { return auth.CredentialsWhere(config.CredentialsFile()) },
	}
	_, url, _, err := bootstrap.Tokens(serverOverride)
	if err != nil || url == "" {
		return o
	}
	o.Server = url
	o.HTTP = rpc.HTTPClient(url)
	if os.Getenv("HARNESS_ACCESS_TOKEN") != "" || os.Getenv("HARNESS_DEVICE_TOKEN") != "" {
		// A token from the environment (CI): there is no stored sign-in to confirm.
		return o
	}
	o.Me = func(ctx context.Context) (string, error) {
		m, err := manager()
		if err != nil {
			return "", err
		}
		me, err := m.Me(ctx)
		if err != nil {
			return "", err
		}
		return me.User.Email, nil
	}
	return o
}

func doctorInventory(serverOverride string, m permissions.ManagedPolicy) *mcp.Inventory {
	inv := &mcp.Inventory{}
	if tokens, url, _, err := bootstrap.Tokens(serverOverride); err == nil && url != "" && tokens != nil {
		inv.Client = mcp.NewClient(url, rpc.HTTPClient(url), tokens)
	}
	inv.PolicyAllows = m.McpServerAllowed
	return server.FillMCPInventory(inv)
}
