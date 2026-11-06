package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/server"
)

// tcpFlags are the `serve` flags of the TCP listener for browser frontends.
type tcpFlags struct {
	listen           *string
	allowNonLoopback *bool
	token            *string
	corsOrigins      *[]string
}

func addTCPFlags(fs *pflag.FlagSet) *tcpFlags {
	return &tcpFlags{
		listen: fs.String("listen", os.Getenv("HARNESS_AGENT_LISTEN"),
			"TCP host:port to serve AgentService to browsers on, loopback only by default, e.g. 127.0.0.1:7801 (env HARNESS_AGENT_LISTEN)"),
		allowNonLoopback: fs.Bool("allow-non-loopback", false,
			"allow --listen on a non-loopback address (exposes the agent to the network; the token is the only protection)"),
		token: fs.String("token", "",
			"bearer token for --listen (env HARNESS_AGENT_TOKEN; default: random per start, written to $HARNESS_HOME/agent-token)"),
		corsOrigins: fs.StringArray("cors-origin", nil,
			"browser origin allowed to call --listen, repeatable (env HARNESS_AGENT_CORS_ORIGINS, comma-separated; default http://localhost:3210 and http://127.0.0.1:3210)"),
	}
}

// validate checks the listener flags before any network setup.
func (t *tcpFlags) validate(socket string) error {
	if socket == "" && *t.listen == "" {
		return errors.New("nothing to serve on: pass --socket <path> and/or --listen 127.0.0.1:<port>")
	}
	if *t.listen == "" {
		return nil
	}
	loopback, err := server.IsLoopbackAddr(*t.listen)
	if err != nil {
		return fmt.Errorf("--listen %q: %w", *t.listen, err)
	}
	if !loopback && !*t.allowNonLoopback {
		return fmt.Errorf("--listen %q is not a loopback address; use 127.0.0.1:<port>, or pass --allow-non-loopback to expose the agent to the network", *t.listen)
	}
	if tok := t.resolvedToken(); tok != "" && len(tok) < server.MinTokenLength {
		return fmt.Errorf("--token must be at least %d characters", server.MinTokenLength)
	}
	return nil
}

func (t *tcpFlags) resolvedToken() string {
	if *t.token != "" {
		return *t.token
	}
	return os.Getenv("HARNESS_AGENT_TOKEN")
}

func (t *tcpFlags) origins() []string {
	if len(*t.corsOrigins) > 0 {
		return *t.corsOrigins
	}
	if env := os.Getenv("HARNESS_AGENT_CORS_ORIGINS"); env != "" {
		var out []string
		for _, o := range strings.Split(env, ",") {
			if o = strings.TrimSpace(o); o != "" {
				out = append(out, o)
			}
		}
		return out
	}
	return server.DefaultCORSOrigins
}

// tokenFile is where a generated token is written for local frontends.
func tokenFile() string { return filepath.Join(config.Home(), "agent-token") }

// serveListeners serves srv on the unix socket and/or the TCP address until
// ctx ends or one of them fails.
func serveListeners(ctx context.Context, srv *server.Server, socket string, t *tcpFlags) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	exitWithParent(ctx, cancel)
	exitWithStdin(ctx, cancel)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	run := func(f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f(); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
			cancel()
		}()
	}

	if socket != "" {
		fmt.Fprintf(os.Stderr, "harness-client: serving AgentService on unix://%s\n", socket)
		run(func() error { return srv.ServeUnix(ctx, socket) })
	}
	if *t.listen != "" {
		opts, cleanup, err := t.options()
		if err != nil {
			cancel()
			wg.Wait()
			return err
		}
		defer cleanup()
		run(func() error { return srv.ServeTCP(ctx, opts) })
	}
	wg.Wait()
	return firstErr
}

// options resolves the TCP options: the token (generating and writing one
// if none was given) and the CORS allowlist.
func (t *tcpFlags) options() (server.TCPOptions, func(), error) {
	cleanup := func() {}
	token := t.resolvedToken()
	generated := token == ""
	if generated {
		var err error
		if token, err = server.GenerateToken(); err != nil {
			return server.TCPOptions{}, cleanup, err
		}
		path := tokenFile()
		if err := server.WriteTokenFile(path, token); err != nil {
			return server.TCPOptions{}, cleanup, fmt.Errorf("write token file: %w", err)
		}
		cleanup = func() { removeIfContains(path, token) }
	}
	origins := t.origins()
	opts := server.TCPOptions{
		Addr:             *t.listen,
		Token:            token,
		AllowedOrigins:   origins,
		AllowNonLoopback: *t.allowNonLoopback,
	}
	opts.OnListen = func(addr net.Addr) {
		url := "http://" + addr.String()
		if loopback, _ := server.IsLoopbackAddr(addr.String()); !loopback {
			fmt.Fprintln(os.Stderr, "WARNING: ==================================================================")
			fmt.Fprintf(os.Stderr, "WARNING: AgentService is listening on a NON-LOOPBACK address (%s).\n", addr)
			fmt.Fprintln(os.Stderr, "WARNING: Anyone who can reach it and has the token can run tools on this machine.")
			fmt.Fprintln(os.Stderr, "WARNING: Traffic is plain HTTP: the token and all session data are unencrypted.")
			fmt.Fprintln(os.Stderr, "WARNING: ==================================================================")
		}
		fmt.Fprintf(os.Stderr, "harness-client: serving AgentService on %s (Connect over HTTP/1.1 and h2c)\n", url)
		fmt.Fprintf(os.Stderr, "harness-client: allowed browser origins: %s\n", strings.Join(origins, ", "))
		if generated {
			fmt.Fprintf(os.Stderr, "harness-client: bearer token (also in %s, mode 0600): %s\n", tokenFile(), token)
		} else {
			fmt.Fprintln(os.Stderr, "harness-client: bearer token: from --token / HARNESS_AGENT_TOKEN")
		}
	}
	return opts, cleanup, nil
}

// removeIfContains deletes the token file on shutdown unless another
// process has replaced it since.
func removeIfContains(path, token string) {
	if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) == token {
		_ = os.Remove(path)
	}
}
