package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xlsft/harness/packages/client/config"
	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
)

// varRef matches local environment references `${VAR}` and `${VAR:-default}`. `${secret:NAME}`
// references are substituted by the server and never match.
var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// expandVars expands local environment references.
func expandVars(s string, lookup func(string) (string, bool)) string {
	return varRef.ReplaceAllStringFunc(s, func(m string) string {
		sub := varRef.FindStringSubmatch(m)
		if v, ok := lookup(sub[1]); ok && v != "" {
			return v
		}
		return sub[2]
	})
}

// stdioCommand returns the command this device would run for s: the base command with the
// overrides matching this OS applied (device-specific overrides are resolved by the server).
func stdioCommand(s *mcpv1.Server) string {
	cmd := s.GetStdio().GetCommand()
	for _, o := range s.GetOverrides() {
		if o.GetDeviceId() != "" {
			continue
		}
		if o.GetOs() != "" && !strings.EqualFold(o.GetOs(), runtime.GOOS) {
			continue
		}
		if o.GetCommand() != "" {
			cmd = o.GetCommand()
		}
	}
	return cmd
}

// checkAvailable reports why a server cannot run on this device ("" when it can, or when
// that cannot be known before the launch data is issued).
func checkAvailable(s *mcpv1.Server) string {
	if s.GetTransport() != mcpv1.Transport_TRANSPORT_STDIO {
		return ""
	}
	cmd := expandVars(stdioCommand(s), os.LookupEnv)
	if cmd == "" {
		return "no command configured"
	}
	if strings.Contains(cmd, "${") {
		return "" // resolved by the server at launch
	}
	if strings.ContainsRune(cmd, filepath.Separator) || strings.ContainsRune(cmd, '/') {
		if st, err := os.Stat(cmd); err != nil || st.IsDir() {
			return "command not found: " + cmd
		}
		return ""
	}
	if _, err := exec.LookPath(cmd); err != nil {
		return "command not found: " + cmd
	}
	return ""
}

// tail keeps the last bytes written to it (the stderr of a stdio server, for error messages).
type tail struct {
	mu  sync.Mutex
	buf []byte
}

const tailSize = 4096

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > tailSize {
		t.buf = t.buf[len(t.buf)-tailSize:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// connHooks are what a direct connection tells its owner. All optional.
type connHooks struct {
	elicit      func(context.Context, *sdk.ElicitParams) (*sdk.ElicitResult, error)
	listChanged func(kind string)
	progress    func(token any, progress, total float64, message string)
	root        string
}

// directConn is a running server this device talks to itself: a stdio process, or a device-local HTTP or SSE server.
type directConn struct {
	session *sdk.ClientSession
	stderr  *tail
	cancel  context.CancelFunc
}

func newClient(h connHooks) *sdk.Client {
	opts := &sdk.ClientOptions{
		// Answering elicitation/create without a handler would be a protocol error, so it is always advertised;
		// the handler declines when nobody can be asked.
		Capabilities: &sdk.ClientCapabilities{RootsV2: &sdk.RootCapabilities{}, Elicitation: &sdk.ElicitationCapabilities{Form: &sdk.FormElicitationCapabilities{}, URL: &sdk.URLElicitationCapabilities{}}},
		ElicitationHandler: func(ctx context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
			if h.elicit == nil {
				return &sdk.ElicitResult{Action: "cancel"}, nil
			}
			return h.elicit(ctx, req.Params)
		},
		ToolListChangedHandler: func(context.Context, *sdk.ToolListChangedRequest) { h.changed("tools") },
		PromptListChangedHandler: func(context.Context, *sdk.PromptListChangedRequest) {
			h.changed("prompts")
		},
		ResourceListChangedHandler: func(context.Context, *sdk.ResourceListChangedRequest) { h.changed("resources") },
		ProgressNotificationHandler: func(_ context.Context, req *sdk.ProgressNotificationClientRequest) {
			if h.progress != nil && req.Params != nil {
				h.progress(req.Params.ProgressToken, req.Params.Progress, req.Params.Total, req.Params.Message)
			}
		},
	}
	c := sdk.NewClient(&sdk.Implementation{Name: "harness", Version: config.Version}, opts)
	if h.root != "" {
		c.AddRoots(&sdk.Root{URI: "file://" + h.root, Name: filepath.Base(h.root)})
	}
	return c
}

func (h connHooks) changed(kind string) {
	if h.listChanged != nil {
		h.listChanged(kind)
	}
}

// connectDirect starts the connection of a direct server: resolves its launch data (from the xlsft`s harness server, or from
// this device's list) and connects. The connection outlives ctx, which only bounds starting it.
func connectDirect(ctx context.Context, cfg *mcpv1.Server, local bool, client *Client, store *LocalStore, cwd string, hooks connHooks) (*directConn, error) {
	if cfg.GetTransport() != mcpv1.Transport_TRANSPORT_STDIO {
		if !local {
			return nil, errors.New("only device-local servers connect directly")
		}
		u, headers, err := store.remoteData(cfg.GetId())
		if err != nil {
			return nil, err
		}
		if helper := cfg.GetRemote().GetHeadersHelper(); helper != "" {
			extra, err := runHeadersHelper(ctx, helper, cwd)
			if err != nil {
				return nil, err
			}
			for k, v := range extra {
				headers[k] = v
			}
		}
		return startHTTP(ctx, cfg.GetTransport(), u, headers, hooks)
	}
	var launch *mcpv1.IssueStdioLaunchResponse
	var err error
	if local {
		launch, err = store.launchData(cfg.GetId())
	} else {
		launch, err = client.IssueStdioLaunch(ctx, cfg.GetId(), cfg.GetConfigHash())
		if err != nil {
			err = errors.New("launch data not issued: " + errorMessage(err))
		}
	}
	if err != nil {
		return nil, err
	}
	return startStdio(ctx, launch, cwd, hooks)
}

// connectPlugin starts a server of a plugin, whose launch data is its config.
func connectPlugin(ctx context.Context, cfg *mcpv1.Server, cwd string, hooks connHooks) (*directConn, error) {
	if cfg.GetTransport() != mcpv1.Transport_TRANSPORT_STDIO {
		return startHTTP(ctx, cfg.GetTransport(), cfg.GetRemote().GetUrl(), cfg.GetRemote().GetHeaders(), hooks)
	}
	s := cfg.GetStdio()
	return startStdio(ctx, &mcpv1.IssueStdioLaunchResponse{Command: s.GetCommand(), Args: s.GetArgs(), Env: s.GetEnv()}, cwd, hooks)
}

// startSession connects a transport with a context that lives as long as the connection; ctx only bounds the start.
func startSession(ctx context.Context, t sdk.Transport, hooks connHooks) (*sdk.ClientSession, context.CancelFunc, error) {
	life, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, cancel)
	cs, err := newClient(hooks).Connect(life, t, nil)
	if !stop() && err == nil {
		err = ctx.Err()
		_ = cs.Close()
	}
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return cs, cancel, nil
}

// startStdio spawns a stdio server from issued launch data and initializes the MCP session. The
// launch data (which holds secrets) is only used to build the process and is not retained.
func startStdio(ctx context.Context, launch *mcpv1.IssueStdioLaunchResponse, defaultCwd string, hooks connHooks) (*directConn, error) {
	if exp, err := time.Parse(time.RFC3339, launch.GetExpiresAt()); err == nil && time.Now().After(exp) {
		return nil, errors.New("the issued launch data expired before the process could start")
	}
	look := os.LookupEnv
	command := expandVars(launch.GetCommand(), look)
	if command == "" {
		return nil, errors.New("the server issued an empty command")
	}
	args := make([]string, len(launch.GetArgs()))
	for i, a := range launch.GetArgs() {
		args[i] = expandVars(a, look)
	}
	cmd := exec.Command(command, args...)
	cmd.Env = os.Environ()
	for k, v := range launch.GetEnv() {
		cmd.Env = append(cmd.Env, k+"="+expandVars(v, look))
	}
	cmd.Dir = expandVars(launch.GetCwd(), look)
	if cmd.Dir == "" {
		cmd.Dir = defaultCwd
	}
	stderr := &tail{}
	cmd.Stderr = stderr
	if hooks.root == "" {
		hooks.root = cmd.Dir
	}
	cs, cancel, err := startSession(ctx, &sdk.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}, hooks)
	if err != nil {
		if s := stderr.String(); s != "" {
			return nil, fmt.Errorf("%w: %s", err, lastLine(s))
		}
		return nil, err
	}
	return &directConn{session: cs, stderr: stderr, cancel: cancel}, nil
}

// startHTTP connects to a device-local Streamable HTTP or SSE server.
func startHTTP(ctx context.Context, transport mcpv1.Transport, url string, headers map[string]string, hooks connHooks) (*directConn, error) {
	hc := &http.Client{Transport: headerTransport{headers: headers, base: http.DefaultTransport}}
	var t sdk.Transport
	if transport == mcpv1.Transport_TRANSPORT_SSE {
		t = &sdk.SSEClientTransport{Endpoint: url, HTTPClient: hc}
	} else {
		t = &sdk.StreamableClientTransport{Endpoint: url, HTTPClient: hc}
	}
	cs, cancel, err := startSession(ctx, t, hooks)
	if err != nil {
		return nil, err
	}
	return &directConn{session: cs, stderr: &tail{}, cancel: cancel}, nil
}

// headerTransport sets the configured headers on every request. They are never logged.
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	for k, v := range t.headers {
		c.Header.Set(k, v)
	}
	return t.base.RoundTrip(c)
}

// headersHelperTimeout bounds one run of a headers helper.
const headersHelperTimeout = 15 * time.Second

// runHeadersHelper runs a server's helper command with the shell; its stdout is a JSON object of string headers.
// The output holds credentials: an error message never includes it.
func runHeadersHelper(ctx context.Context, command, cwd string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, headersHelperTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	}
	cmd.Dir = cwd
	var out bytes.Buffer
	stderr := &tail{}
	cmd.Stdout, cmd.Stderr = &out, stderr
	if err := cmd.Run(); err != nil {
		if s := stderr.String(); s != "" {
			return nil, fmt.Errorf("the headers helper failed: %w: %s", err, lastLine(s))
		}
		return nil, fmt.Errorf("the headers helper failed: %w", err)
	}
	var headers map[string]string
	if err := json.Unmarshal(out.Bytes(), &headers); err != nil {
		return nil, errors.New("the headers helper must print a JSON object of string values")
	}
	return headers, nil
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// listTools lists all tools of the running server in catalog form.
func (c *directConn) listTools(ctx context.Context) ([]*mcpv1.Tool, error) {
	if c.session.InitializeResult().Capabilities.Tools == nil {
		return nil, nil
	}
	var out []*mcpv1.Tool
	for t, err := range c.session.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		pt := &mcpv1.Tool{Name: t.Name, Title: t.Title, Description: t.Description}
		if t.InputSchema != nil {
			b, _ := json.Marshal(t.InputSchema)
			pt.InputSchemaJson = string(b)
		}
		if t.OutputSchema != nil {
			b, _ := json.Marshal(t.OutputSchema)
			pt.OutputSchemaJson = string(b)
		}
		if t.Annotations != nil {
			b, _ := json.Marshal(t.Annotations)
			pt.AnnotationsJson = string(b)
		}
		out = append(out, pt)
	}
	return out, nil
}

func (c *directConn) hasPrompts() bool {
	return c.session.InitializeResult().Capabilities.Prompts != nil
}

func (c *directConn) hasResources() bool {
	return c.session.InitializeResult().Capabilities.Resources != nil
}

// listPrompts lists the prompts of the running server in catalog form.
func (c *directConn) listPrompts(ctx context.Context) ([]*mcpv1.Prompt, error) {
	if !c.hasPrompts() {
		return nil, nil
	}
	var out []*mcpv1.Prompt
	for p, err := range c.session.Prompts(ctx, nil) {
		if err != nil {
			return nil, err
		}
		pp := &mcpv1.Prompt{Name: p.Name, Title: p.Title, Description: p.Description}
		if len(p.Arguments) > 0 {
			b, _ := json.Marshal(p.Arguments)
			pp.ArgumentsJson = string(b)
		}
		out = append(out, pp)
	}
	return out, nil
}

// listResources lists the resources and templates of the running server in catalog form.
func (c *directConn) listResources(ctx context.Context) ([]*mcpv1.Resource, []*mcpv1.ResourceTemplate, error) {
	if !c.hasResources() {
		return nil, nil, nil
	}
	var res []*mcpv1.Resource
	for r, err := range c.session.Resources(ctx, nil) {
		if err != nil {
			return nil, nil, err
		}
		res = append(res, &mcpv1.Resource{Uri: r.URI, Name: r.Name, Title: r.Title, Description: r.Description, MimeType: r.MIMEType, Size: r.Size})
	}
	var tpl []*mcpv1.ResourceTemplate
	for t, err := range c.session.ResourceTemplates(ctx, nil) {
		if err != nil {
			break // templates are optional
		}
		tpl = append(tpl, &mcpv1.ResourceTemplate{UriTemplate: t.URITemplate, Name: t.Name, Title: t.Title, Description: t.Description, MimeType: t.MIMEType})
	}
	return res, tpl, nil
}

// instructions returns the text the server offers the model.
func (c *directConn) instructions() string {
	return c.session.InitializeResult().Instructions
}

// call runs a tool and returns the MCP CallToolResult JSON. token (optional) asks for progress notifications.
func (c *directConn) call(ctx context.Context, tool string, args json.RawMessage, token string) ([]byte, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	params := &sdk.CallToolParams{Name: tool, Arguments: args}
	if token != "" {
		params.SetProgressToken(token)
	}
	res, err := c.session.CallTool(ctx, params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(res)
}

func (c *directConn) close() {
	if c != nil && c.session != nil {
		_ = c.session.Close()
	}
	if c != nil && c.cancel != nil {
		c.cancel()
	}
}
