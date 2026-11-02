package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// checkAvailable reports why a stdio server cannot run on this device ("" when it can, or when
// that cannot be known before the launch data is issued).
func checkAvailable(s *mcpv1.Server) string {
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

// stdioConn is a running stdio server.
type stdioConn struct {
	session *sdk.ClientSession
	stderr  *tail
}

// startStdio spawns a stdio server from issued launch data and initializes the MCP session. The
// launch data (which holds secrets) is only used to build the process and is not retained.
func startStdio(ctx context.Context, launch *mcpv1.IssueStdioLaunchResponse, defaultCwd string) (*stdioConn, error) {
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
	client := sdk.NewClient(&sdk.Implementation{Name: "harness", Version: config.Version}, nil)
	cs, err := client.Connect(ctx, &sdk.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}, nil)
	if err != nil {
		if s := stderr.String(); s != "" {
			return nil, fmt.Errorf("%w: %s", err, lastLine(s))
		}
		return nil, err
	}
	return &stdioConn{session: cs, stderr: stderr}, nil
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// listTools lists all tools of the running server in catalog form.
func (c *stdioConn) listTools(ctx context.Context) ([]*mcpv1.Tool, error) {
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

// call runs a tool and returns the MCP CallToolResult JSON.
func (c *stdioConn) call(ctx context.Context, tool string, args json.RawMessage) ([]byte, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	res, err := c.session.CallTool(ctx, &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return nil, err
	}
	return json.Marshal(res)
}

func (c *stdioConn) close() {
	if c != nil && c.session != nil {
		_ = c.session.Close()
	}
}
