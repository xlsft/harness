package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/cloudrunner"
	"github.com/xlsft/harness/packages/client/rpc"
)

const runnerUsage = `harness-client runner — a runner node of cloud sessions: one container per session (docs/design/cloud-sessions.md)

Usage:
  harness-client runner [flags]   The runner's token comes from HARNESS_DEVICE_TOKEN or HARNESS_DEVICE_TOKEN_FILE, with
                                  HARNESS_SERVER_URL. An admin adds the runner in the settings (Hosts) and gets the token.

It needs a container engine: Docker, or Podman's Docker-compatible service (DOCKER_HOST, or the default sockets).

Flags:
`

func runRunner(ctx context.Context, args []string) int {
	fs := pflag.NewFlagSet("harness-client runner", pflag.ContinueOnError)
	server := fs.String("server", "", "server URL (default: HARNESS_SERVER_URL)")
	agentServer := fs.String("agent-server-url", "", "the server's address as the containers reach it (default: --server)")
	name := fs.String("name", "", "what the runner is called (default: the machine's name)")
	engine := fs.String("engine", "", "container engine address: unix:///var/run/docker.sock, unix:///run/podman/podman.sock, tcp://… (default: DOCKER_HOST, then those sockets)")
	cacheDir := fs.String("cache-dir", "/var/lib/harness-runner/cache", "the caches; a path on the engine's machine (a runner in a container mounts it at the same path)")
	network := fs.String("network", cloudrunner.DefaultNetwork, "the containers' internal network")
	self := fs.String("self-container", os.Getenv("HARNESS_RUNNER_CONTAINER"), "the runner's own container, when it runs in one: it joins the network to serve the proxy")
	proxyListen := fs.String("proxy-listen", "", "where the egress proxy listens (default: on the network's gateway, or :3128 with --self-container)")
	proxyURL := fs.String("proxy-url", "", "the proxy's URL as the containers see it")
	verbose := fs.Bool("verbose", false, "debug logging")
	if len(args) > 0 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(os.Stderr, runnerUsage+fs.FlagUsages())
		return 0
	}
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		fmt.Fprint(os.Stderr, runnerUsage+fs.FlagUsages())
		return 2
	}
	if err := tokenFromFile(false); err != nil {
		return fail(err)
	}
	log := newLogger(*verbose)
	tokens, url, _, err := bootstrap.Tokens(*server)
	if err != nil {
		return fail(err)
	}
	if tokens == nil {
		return fail(errors.New("no server to work for: set HARNESS_DEVICE_TOKEN (or HARNESS_DEVICE_TOKEN_FILE) and HARNESS_SERVER_URL"))
	}
	addr := *engine
	if addr == "" {
		addr = cloudrunner.DefaultEngine()
	}
	driver, err := cloudrunner.NewDocker(addr, nil)
	if err != nil {
		return fail(err)
	}
	r := cloudrunner.New(cloudrunner.Options{
		ServerURL: url, AgentServerURL: strings.TrimSpace(*agentServer), HTTP: rpc.HTTPClient(url), Tokens: tokens, Logger: log, Name: *name,
		Driver: driver, CacheDir: *cacheDir, Network: *network, SelfContainer: *self, ProxyListen: *proxyListen, ProxyURL: *proxyURL,
	})
	fmt.Fprintf(os.Stderr, "harness-client: runner on %s, engine %s\n", url, addr)
	if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fail(err)
	}
	return 0
}
