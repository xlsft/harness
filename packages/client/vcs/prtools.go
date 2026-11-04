package vcs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xlsft/harness/packages/client/tools"
)

// PRTools are the session's pull request tools. Descriptions and schemas come from the prompt bundle.
func PRTools(t *Tracker) []tools.Tool {
	return []tools.Tool{&prStatusTool{t}, &prBindTool{t}, &prUnbindTool{t}, &prMonitorTool{t}, &prAutoMergeTool{t}}
}

// prInput reads the input with an optional pr (a string, or a number when the model sends one).
func prInput(input json.RawMessage) (string, error) {
	var in struct {
		PR json.RawMessage `json:"pr"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("InputValidationError: %v", err)
		}
	}
	raw := bytes.TrimSpace(in.PR)
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s), nil
	}
	return string(raw), nil
}

// statusText is a PR for the model.
func statusText(s *PRStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pull request #%d in %s: %s\n", s.Number, s.Repo, s.State)
	fmt.Fprintf(&b, "URL: %s\n", s.URL)
	fmt.Fprintf(&b, "Title (from the code host): %s\n", EscapeExternal(s.Title))
	switch s.ChecksState {
	case "", "none":
		b.WriteString("Checks: none\n")
	default:
		fmt.Fprintf(&b, "Checks: %s (%d total, %d passed, %d failed, %d running)\n", s.ChecksState, s.ChecksTotal, s.ChecksPass, s.ChecksFail, s.ChecksRun)
	}
	if len(s.FailedChecks) > 0 {
		fmt.Fprintf(&b, "Failed checks (names from the code host): %s\n", EscapeExternal(strings.Join(s.FailedChecks, ", ")))
	}
	switch {
	case s.Conflicts:
		b.WriteString("Mergeable: no, it conflicts with the base branch\n")
	case s.Mergeable:
		b.WriteString("Mergeable: yes\n")
	default:
		b.WriteString("Mergeable: not now or not known\n")
	}
	fmt.Fprintf(&b, "Review decision: %s\n", s.Review)
	if s.Unresolved < 0 {
		b.WriteString("Unresolved comments: could not be read\n")
	} else {
		fmt.Fprintf(&b, "Unresolved comments: %d\n", s.Unresolved)
	}
	fmt.Fprintf(&b, "Auto-merge: %s\n", onOff(s.AutoMerge))
	fmt.Fprintf(&b, "Bound to this session: %s, monitoring: %s", yesNo(s.Bound), onOff(s.Monitoring))
	if s.Marker != "" {
		fmt.Fprintf(&b, "\nEnd every comment you post on a pull request with this line, changing only kind (reply, review or status) if another fits:\n%s", s.Marker)
	}
	return b.String()
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// ---- PrStatus ----

type prStatusTool struct{ t *Tracker }

func (*prStatusTool) Name() string                  { return "PrStatus" }
func (*prStatusTool) ReadOnly(json.RawMessage) bool { return true }

func (p *prStatusTool) Run(ctx context.Context, _ *tools.Env, call tools.Call) tools.Result {
	ref, err := prInput(call.Input)
	if err != nil {
		return tools.ErrorResult("%v", err)
	}
	s, err := p.t.Status(ctx, ref)
	if err != nil {
		return tools.ErrorResult("%v", err)
	}
	r := tools.TextResult(statusText(s), s)
	r.Summary = fmt.Sprintf("PR #%d: %s", s.Number, s.State)
	return r
}

// ---- PrBind ----

// prBindTool only changes what this session follows, so it counts as read-only, like TodoWrite: no prompt.
type prBindTool struct{ t *Tracker }

func (*prBindTool) Name() string                  { return "PrBind" }
func (*prBindTool) ReadOnly(json.RawMessage) bool { return true }

func (p *prBindTool) Run(ctx context.Context, _ *tools.Env, call tools.Call) tools.Result {
	ref, err := prInput(call.Input)
	if err != nil {
		return tools.ErrorResult("%v", err)
	}
	if ref == "" {
		return tools.ErrorResult("InputValidationError: pr is required: a number or URL")
	}
	s, err := p.t.Bind(ctx, ref)
	if err != nil {
		return tools.ErrorResult("%v", err)
	}
	r := tools.TextResult("Bound to this session.\n"+statusText(s), s)
	r.Summary = fmt.Sprintf("Bound PR #%d", s.Number)
	return r
}

// ---- PrUnbind ----

type prUnbindTool struct{ t *Tracker }

func (*prUnbindTool) Name() string                  { return "PrUnbind" }
func (*prUnbindTool) ReadOnly(json.RawMessage) bool { return true }

func (p *prUnbindTool) Run(_ context.Context, _ *tools.Env, _ tools.Call) tools.Result {
	was, err := p.t.Unbind()
	if err != nil {
		return tools.ErrorResult("%v", err)
	}
	if !was {
		return tools.TextResult("No pull request is bound to this session.", nil)
	}
	r := tools.TextResult("Unbound. The client no longer follows that pull request for this session; monitoring is off.", nil)
	r.Summary = "Unbound PR"
	return r
}

// ---- PrMonitor ----

type prMonitorTool struct{ t *Tracker }

func (*prMonitorTool) Name() string                  { return "PrMonitor" }
func (*prMonitorTool) ReadOnly(json.RawMessage) bool { return false }

func (p *prMonitorTool) Run(ctx context.Context, _ *tools.Env, call tools.Call) tools.Result {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(orEmpty(call.Input), &in); err != nil {
		return tools.ErrorResult("InputValidationError: %v", err)
	}
	if in.Enabled == nil {
		return tools.ErrorResult("InputValidationError: enabled is required")
	}
	s, err := p.t.SetMonitor(ctx, *in.Enabled)
	if err != nil {
		return tools.ErrorResult("%v", err)
	}
	if !*in.Enabled {
		return tools.TextResult("Monitoring is off.", nil)
	}
	r := tools.TextResult(fmt.Sprintf("Monitoring pull request #%d (%s). End your turn when you have nothing else to do: the client wakes this session with a note on a CI failure, a merge conflict or a new comment. Do not poll.", s.Number, s.URL), s)
	r.Summary = fmt.Sprintf("Monitoring PR #%d", s.Number)
	return r
}

// ---- PrAutoMerge ----

// prAutoMergeTool always asks the user (tools.AlwaysAsker): neither rules nor a permissive mode nor the auto-mode
// decider can approve it.
type prAutoMergeTool struct{ t *Tracker }

var _ tools.AlwaysAsker = (*prAutoMergeTool)(nil)

func (*prAutoMergeTool) Name() string                  { return "PrAutoMerge" }
func (*prAutoMergeTool) ReadOnly(json.RawMessage) bool { return false }
func (*prAutoMergeTool) AlwaysAsk() bool               { return true }

func (p *prAutoMergeTool) Run(ctx context.Context, _ *tools.Env, call tools.Call) tools.Result {
	var in struct {
		Enabled *bool  `json:"enabled"`
		Method  string `json:"method"`
	}
	if err := json.Unmarshal(orEmpty(call.Input), &in); err != nil {
		return tools.ErrorResult("InputValidationError: %v", err)
	}
	if in.Enabled == nil {
		return tools.ErrorResult("InputValidationError: enabled is required")
	}
	s, err := p.t.SetAutoMerge(ctx, *in.Enabled, in.Method)
	if err != nil {
		return tools.ErrorResult("%v", err)
	}
	head := fmt.Sprintf("Auto-merge is %s.", onOff(s.AutoMerge))
	if *in.Enabled && s.State == "merged" {
		head = "The pull request met its requirements and was merged at once."
	}
	r := tools.TextResult(head+"\n"+statusText(s), s)
	r.Summary = fmt.Sprintf("Auto-merge %s on PR #%d", onOff(*in.Enabled), s.Number)
	return r
}

func orEmpty(input json.RawMessage) []byte {
	if len(input) == 0 {
		return []byte("{}")
	}
	return input
}
