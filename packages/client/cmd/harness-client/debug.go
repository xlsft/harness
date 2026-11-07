package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/clientlog"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/session"
)

const debugUsage = `usage:
  harness-client debug tools --session <id> [--url http://127.0.0.1:<port> [--token T] | --socket <path>] [--json]
  harness-client debug diagnostics [--url ... | --socket ...]
  harness-client debug logs        print the folder of the client's log files

With --url or --socket a running client answers (serve --listen, with its token; or serve --socket). The desktop app's
client has a token of its own: there, type /tools in the session instead. Without them, debug tools opens the session
here, read-only and without MCP servers, the way the app would open it.`

// runDebug is `harness-client debug`: what a session's model is offered, and a report of the client.
func runDebug(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, debugUsage)
		return 2
	}
	verb := args[0]
	fs := pflag.NewFlagSet("debug "+verb, pflag.ContinueOnError)
	sid := fs.String("session", "", "session id")
	url := fs.String("url", "", "AgentService address of a running client (serve --listen)")
	token := fs.String("token", "", "bearer token for --url (default: HARNESS_AGENT_TOKEN, else $HARNESS_HOME/agent-token)")
	socket := fs.String("socket", "", "unix socket of a running client (serve --socket)")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	client := debugClient(*url, *token, *socket)
	switch verb {
	case "logs":
		fmt.Println(clientlog.Dir())
		return 0
	case "diagnostics":
		if client == nil {
			return fail(errors.New("debug diagnostics asks a running client: pass --url or --socket"))
		}
		res, err := client.GetDiagnostics(ctx, connect.NewRequest(&agentv1.GetDiagnosticsRequest{}))
		if err != nil {
			return fail(err)
		}
		fmt.Print(res.Msg.GetText())
		return 0
	case "tools":
		if *sid == "" {
			return fail(errors.New("debug tools needs --session <id>"))
		}
		var list []bootstrap.ToolStatus
		syncState := ""
		if client != nil {
			res, err := client.ListSessionTools(ctx, connect.NewRequest(&agentv1.ListSessionToolsRequest{SessionId: *sid}))
			if err != nil {
				return fail(err)
			}
			for _, t := range res.Msg.GetTools() {
				list = append(list, bootstrap.ToolStatus{Name: t.GetName(), Source: t.GetSource(), Offered: t.GetOffered(), Reason: t.GetReason(), Detail: t.GetDetail()})
			}
			if off := res.Msg.GetSyncOff(); off != "" {
				syncState = "sync: off, " + off
			}
		} else {
			var err error
			if list, syncState, err = localTools(ctx, *sid); err != nil {
				return fail(err)
			}
			syncState = strings.TrimSpace(syncState + "\n(opened here, read-only and without MCP servers; pass --url to ask the running app)")
		}
		if *asJSON {
			jsonLine(map[string]any{"session": *sid, "tools": list, "sync": syncState})
			return 0
		}
		fmt.Print(bootstrap.FormatToolReport(list))
		if syncState != "" {
			fmt.Println("\n" + syncState)
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, debugUsage)
	return 2
}

// bearer adds the token to every request.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	rpc.SetBearer(r.Header, b.token)
	return b.next.RoundTrip(r)
}

func debugClient(url, token, socket string) agentv1connect.AgentServiceClient {
	switch {
	case socket != "":
		return agentv1connect.NewAgentServiceClient(rpc.UnixHTTPClient(socket), "http://agent", connect.WithGRPC())
	case url != "":
		if token == "" {
			token = os.Getenv("HARNESS_AGENT_TOKEN")
		}
		if token == "" {
			if b, err := os.ReadFile(tokenFile()); err == nil {
				token = strings.TrimSpace(string(b))
			}
		}
		hc := &http.Client{Transport: bearer{token: token, next: http.DefaultTransport}}
		return agentv1connect.NewAgentServiceClient(hc, strings.TrimRight(url, "/"))
	}
	return nil
}

// localTools opens the session here as serve would (its shared adapters and server connection), without writing to its
// transcript, starting MCP servers or running hooks, and reports its tools.
func localTools(ctx context.Context, id string) ([]bootstrap.ToolStatus, string, error) {
	path, err := session.Store{Root: config.ProjectsDir()}.Find("", id)
	if err != nil {
		return nil, "", fmt.Errorf("session %s not found", id)
	}
	entries, err := session.Load(path)
	if err != nil {
		return nil, "", err
	}
	cwd := ""
	for _, e := range entries {
		if e.Cwd != "" {
			cwd = e.Cwd
			break
		}
	}
	if cwd == "" {
		return nil, "", fmt.Errorf("session %s has no recorded folder", id)
	}
	_ = os.Setenv("HARNESS_NO_HOOKS", "1")
	base, _, err := sharedAdapters("")
	if err != nil {
		return nil, "", err
	}
	defer base.SyncHub.Close()
	base.Cwd, base.Resume, base.NoPersistence, base.NoMCP = cwd, id, true, true
	base.NoFolder = config.IsChatDir(cwd)
	base.Logger = newLogger(false)
	s, err := bootstrap.Open(ctx, base)
	if err != nil {
		return nil, "", err
	}
	defer s.Close()
	return s.ToolReport(), s.SyncState(), nil
}
