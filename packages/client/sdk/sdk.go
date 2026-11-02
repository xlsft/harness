// Package sdk implements headless mode (`harness-client -p`): the final
// result as text or json, or a one-way stream-json NDJSON event log for
// debugging. Interactive frontends use harness.agent.v1.AgentService
// (see the server package) instead.
package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/llm"
)

// Output and input formats.
const (
	FormatText       = "text"
	FormatJSON       = "json"
	FormatStreamJSON = "stream-json"
)

// Options configure headless mode.
type Options struct {
	Bootstrap      bootstrap.Options
	Prompt         string
	OutputFormat   string
	IncludePartial bool
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
}

// writer serializes NDJSON lines.
type writer struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *writer) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.w.Write(append(data, '\n'))
}

// runner holds the state of one headless run.
type runner struct {
	o    Options
	out  *writer
	sess *bootstrap.Session
	sid  atomic.Value
}

func (r *runner) sessionID() string {
	if v, ok := r.sid.Load().(string); ok {
		return v
	}
	return ""
}

// Run executes headless mode and returns the process exit code.
func Run(ctx context.Context, o Options) int {
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
	if o.OutputFormat == "" {
		o.OutputFormat = FormatText
	}
	r := &runner{o: o, out: &writer{w: o.Stdout}}
	if o.OutputFormat == FormatStreamJSON {
		o.Bootstrap.OnEvent = r.onEvent
	}
	if o.Bootstrap.Entrypoint == "" {
		o.Bootstrap.Entrypoint = "sdk-cli"
	}
	sess, err := bootstrap.Open(ctx, o.Bootstrap)
	if err != nil {
		return r.fatal(err)
	}
	defer sess.Close()
	r.sess = sess
	r.sid.Store(sess.ID)
	if o.OutputFormat == FormatStreamJSON {
		r.out.send(r.initMessage())
	}

	prompt := o.Prompt
	if prompt == "" {
		data, _ := io.ReadAll(o.Stdin)
		prompt = strings.TrimSpace(string(data))
	}
	if prompt == "" {
		return r.fatal(errors.New("no prompt given: pass it as an argument or on stdin"))
	}
	res := sess.Agent.RunTurn(ctx, []llm.ContentBlock{llm.TextBlock(prompt)})
	r.emitResult(res)
	if res.IsError {
		return 1
	}
	return 0
}

func (r *runner) fatal(err error) int {
	switch r.o.OutputFormat {
	case FormatText:
		fmt.Fprintln(r.o.Stderr, "Error:", err)
	default:
		r.out.send(map[string]any{
			"type": "result", "subtype": agent.SubtypeErrorDuringExecution, "is_error": true,
			"errors": []string{err.Error()}, "session_id": r.sessionID(), "uuid": uuid.NewString(),
			"num_turns": 0, "duration_ms": 0, "duration_api_ms": 0, "total_cost_usd": 0, "usage": llm.Usage{},
		})
	}
	return 1
}

func (r *runner) initMessage() map[string]any {
	s := r.sess
	return map[string]any{
		"type": "system", "subtype": "init", "uuid": uuid.NewString(),
		"session_id": s.ID, "cwd": s.Cwd, "model": s.Model, "tools": s.ToolNames(),
		"permissionMode": string(s.Permissions.Mode()), "mcp_servers": []any{}, "slash_commands": []string{},
		"apiKeySource": "none", "output_style": "default", "version": config.Version,
		"prompt_bundle_version": s.Bundle.Version, "resumed": s.Resumed,
	}
}

func (r *runner) onEvent(e agent.Event) {
	base := map[string]any{"session_id": r.sessionID(), "uuid": e.UUID, "parent_tool_use_id": nil}
	if e.UUID == "" {
		base["uuid"] = uuid.NewString()
	}
	switch e.Kind {
	case agent.EventAssistant:
		msg := map[string]any{"type": "message", "role": "assistant", "content": e.Message.Content}
		if e.Response != nil {
			msg["id"], msg["model"], msg["usage"] = e.Response.ID, e.Response.Model, e.Response.Usage
			msg["stop_reason"] = nilIfEmpty(e.Response.StopReason)
		}
		base["type"], base["message"] = "assistant", msg
	case agent.EventUser:
		base["type"] = "user"
		base["message"] = map[string]any{"role": "user", "content": e.Message.Content}
		if len(e.Raw) > 0 {
			base["tool_use_result"] = e.Raw
		}
	case agent.EventStream:
		if !r.o.IncludePartial {
			return
		}
		base["type"], base["event"] = "stream_event", e.Raw
	case agent.EventRetry:
		base["type"], base["subtype"] = "system", "api_retry"
		base["attempt"], base["max_retries"], base["retry_delay_ms"] = e.Retry.Attempt, e.Retry.MaxRetries, e.Retry.RetryDelayMs
		base["error"] = e.Retry.Error
		delete(base, "parent_tool_use_id")
	default:
		return
	}
	r.out.send(base)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ResultMessage builds the result object for a turn.
func ResultMessage(sessionID string, res agent.TurnResult) map[string]any {
	m := map[string]any{
		"type": "result", "subtype": res.Subtype, "is_error": res.IsError,
		"duration_ms": res.Duration.Milliseconds(), "duration_api_ms": res.APIDuration.Milliseconds(),
		"num_turns": res.NumTurns, "session_id": sessionID, "total_cost_usd": res.CostUSD,
		"usage": res.Usage, "modelUsage": res.ModelUsage, "stop_reason": nilIfEmpty(res.StopReason),
		"permission_denials": nonNil(res.PermissionDenials), "uuid": uuid.NewString(),
	}
	if res.Subtype == agent.SubtypeSuccess {
		m["result"] = res.Result
	} else {
		m["errors"] = nonNilS(res.Errors)
	}
	if res.Interrupted {
		m["interrupted"] = true
	}
	return m
}

func nonNil(d []agent.PermissionDenial) []agent.PermissionDenial {
	if d == nil {
		return []agent.PermissionDenial{}
	}
	return d
}

func nonNilS(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (r *runner) emitResult(res agent.TurnResult) {
	switch r.o.OutputFormat {
	case FormatText:
		if res.IsError {
			msg := res.Subtype
			if len(res.Errors) > 0 {
				msg = strings.Join(res.Errors, "; ")
			}
			if res.Interrupted {
				msg = "interrupted"
			}
			fmt.Fprintln(r.o.Stderr, "Error:", msg)
			return
		}
		fmt.Fprintln(r.o.Stdout, res.Result)
	default:
		r.out.send(ResultMessage(r.sessionID(), res))
	}
}
