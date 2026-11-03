package acp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/xlsft/harness/packages/client/acp/registry"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	"github.com/xlsft/harness/packages/client/tools"
)

// toolState is what is known about one ACP tool call.
type toolState struct {
	id        string
	title     string
	kind      sdk.ToolKind
	status    sdk.ToolCallStatus
	rawInput  any
	rawOutput any
	locations []sdk.ToolCallLocation
	content   []sdk.ToolCallContent
	started   bool
	finished  bool
	startedAt int64 // unix ms
}

// mergeStart applies a tool_call notification.
func (t *toolState) mergeStart(u *sdk.SessionUpdateToolCall) {
	t.title = u.Title
	if u.Kind != "" {
		t.kind = u.Kind
	}
	if u.Status != "" {
		t.status = u.Status
	}
	if u.RawInput != nil {
		t.rawInput = u.RawInput
	}
	if u.RawOutput != nil {
		t.rawOutput = u.RawOutput
	}
	if u.Locations != nil {
		t.locations = u.Locations
	}
	if u.Content != nil {
		t.content = u.Content
	}
}

// mergeUpdate applies tool_call_update fields (also those of a permission request's tool call).
func (t *toolState) mergeUpdate(title *string, kind *sdk.ToolKind, status *sdk.ToolCallStatus, rawInput, rawOutput any, locations []sdk.ToolCallLocation, content []sdk.ToolCallContent) {
	if title != nil && *title != "" {
		t.title = *title
	}
	if kind != nil && *kind != "" {
		t.kind = *kind
	}
	if status != nil && *status != "" {
		t.status = *status
	}
	if rawInput != nil {
		t.rawInput = rawInput
	}
	if rawOutput != nil {
		t.rawOutput = rawOutput
	}
	if locations != nil {
		t.locations = locations
	}
	if content != nil {
		t.content = content
	}
}

// ready says the call has enough detail to show: it left "pending" or carries its input.
func (t *toolState) ready() bool {
	return (t.status != "" && t.status != sdk.ToolCallStatusPending) || t.rawInput != nil
}

func (t *toolState) firstPath() string {
	for _, l := range t.locations {
		if l.Path != "" {
			return l.Path
		}
	}
	for _, c := range t.content {
		if c.Diff != nil && c.Diff.Path != "" {
			return c.Diff.Path
		}
	}
	return ""
}

func (t *toolState) diffs() []*sdk.ToolCallContentDiff {
	var out []*sdk.ToolCallContentDiff
	for _, c := range t.content {
		if c.Diff != nil {
			out = append(out, c.Diff)
		}
	}
	return out
}

// rawMap returns the raw input as an object (empty when it is not one).
func rawMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if v == nil {
		return map[string]any{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil || m == nil {
		return map[string]any{}
	}
	return m
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// view maps an ACP tool call onto a harness tool, so frontends render it with their usual tool rows: read ->
// Read, edit -> Edit/Write, execute -> Bash, search -> Grep, fetch -> WebFetch; other kinds keep the agent's
// title as the summary. The input carries the agent's raw input plus the fields the harness tool has, and an
// "acp" object with the kind, title and locations.
func (t *toolState) view() (name string, input map[string]any, summary string) {
	raw := rawMap(t.rawInput)
	input = map[string]any{}
	for k, v := range raw {
		input[k] = v
	}
	summary = t.title
	path := firstString(raw, "file_path", "path", "abs_path", "filePath")
	if path == "" {
		path = t.firstPath()
	}
	switch t.kind {
	case sdk.ToolKindRead:
		name = "Read"
		if path != "" {
			input["file_path"] = path
		}
	case sdk.ToolKindEdit:
		name = "Edit"
		if d := t.diffs(); len(d) == 1 && d[0].OldText == nil {
			name = "Write"
		}
		if path != "" {
			input["file_path"] = path
		}
	case sdk.ToolKindExecute:
		name = "Bash"
		cmd := firstString(raw, "command", "cmd")
		if cmd == "" {
			if a, ok := raw["command"].([]any); ok {
				parts := make([]string, 0, len(a))
				for _, p := range a {
					parts = append(parts, fmt.Sprint(p))
				}
				cmd = strings.Join(parts, " ")
			}
		}
		if cmd == "" {
			cmd = t.title
		}
		input["command"] = cmd
		if t.title != "" && t.title != cmd {
			input["description"] = t.title
		}
	case sdk.ToolKindSearch:
		name = "Grep"
		if p := firstString(raw, "pattern", "query", "regex"); p != "" {
			input["pattern"] = p
		} else {
			input["pattern"] = t.title
		}
	case sdk.ToolKindFetch:
		name = "WebFetch"
		if u := firstString(raw, "url", "uri"); u != "" {
			input["url"] = u
		} else if q := firstString(raw, "query"); q != "" {
			name = "WebSearch"
			input["query"] = q
		}
	case sdk.ToolKindThink:
		name = "Think"
	case sdk.ToolKindDelete:
		name = "Delete"
	case sdk.ToolKindMove:
		name = "Move"
	case sdk.ToolKindSwitchMode:
		name = "SwitchMode"
	default:
		name = "Tool"
	}
	meta := map[string]any{"kind": string(t.kind), "title": t.title}
	if len(t.locations) > 0 {
		locs := make([]map[string]any, 0, len(t.locations))
		for _, l := range t.locations {
			m := map[string]any{"path": l.Path}
			if l.Line != nil {
				m["line"] = *l.Line
			}
			locs = append(locs, m)
		}
		meta["locations"] = locs
	}
	input["acp"] = meta
	if summary == "" {
		summary = name
	}
	return name, input, summary
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// result builds ToolCallFinished for a finished (or interrupted) tool call. term resolves terminal output;
// written holds the original content of files the agent wrote during the turn (for edits without a diff).
func (t *toolState) result(term func(id string) (string, *sdk.TerminalExitStatus, bool), written map[string]writeRecord, interrupted bool, durationMs int64) *agentv1.ToolCallFinished {
	var texts []string
	var stdout string
	exitCode := -1
	for _, c := range t.content {
		switch {
		case c.Content != nil:
			if s := blockText(c.Content.Content); s != "" {
				texts = append(texts, s)
			}
		case c.Terminal != nil:
			if out, st, ok := term(c.Terminal.TerminalId); ok {
				stdout += out
				if st != nil && st.ExitCode != nil {
					exitCode = *st.ExitCode
				}
			}
		case c.Diff != nil:
			texts = append(texts, "Edited "+c.Diff.Path)
		}
	}
	if stdout != "" {
		texts = append([]string{stdout}, texts...)
	}
	if len(texts) == 0 && t.rawOutput != nil {
		switch v := t.rawOutput.(type) {
		case string:
			texts = append(texts, v)
		default:
			texts = append(texts, mustJSON(v))
		}
	}
	text := strings.Join(texts, "\n")
	f := &agentv1.ToolCallFinished{
		ToolUseId:   t.id,
		IsError:     t.status == sdk.ToolCallStatusFailed,
		Interrupted: interrupted,
		DurationMs:  durationMs,
	}
	if text != "" {
		f.Content = []*llmv1.ContentBlock{{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{Text: text}}}}
	}
	var structured map[string]any
	switch {
	case len(t.diffs()) > 0:
		d := t.diffs()[0]
		old := ""
		var original any
		kind := "create"
		if d.OldText != nil {
			old, original, kind = *d.OldText, *d.OldText, "update"
		}
		structured = map[string]any{"type": kind, "filePath": d.Path, "content": d.NewText, "originalFile": original,
			"structuredPatch": nonNilHunks(tools.DiffLines(old, d.NewText, 3)), "userModified": false}
	case t.kind == sdk.ToolKindEdit:
		// No diff content: use what the agent wrote through fs/write_text_file.
		if p := t.firstPath(); p != "" {
			if w, ok := written[filepath.Clean(p)]; ok {
				var original any
				kind := "create"
				if w.existed {
					original, kind = w.original, "update"
				}
				structured = map[string]any{"type": kind, "filePath": p, "content": w.current, "originalFile": original,
					"structuredPatch": nonNilHunks(tools.DiffLines(w.original, w.current, 3)), "userModified": false}
			}
		}
	case t.kind == sdk.ToolKindExecute:
		structured = map[string]any{"stdout": stdout, "stderr": "", "interrupted": interrupted}
		if exitCode >= 0 {
			structured["exitCode"] = exitCode
			f.Summary = fmt.Sprintf("exit %d", exitCode)
		}
	}
	if structured != nil {
		f.StructuredResultJson = mustJSON(structured)
	}
	if f.IsError && f.Summary == "" {
		f.Summary = "failed"
	}
	if interrupted {
		f.Summary = "interrupted"
	}
	return f
}

func nonNilHunks(h []tools.Hunk) []tools.Hunk {
	if h == nil {
		return []tools.Hunk{}
	}
	return h
}

// blockText renders an ACP content block as text (images and audio are noted, resources linked).
func blockText(b sdk.ContentBlock) string {
	switch {
	case b.Text != nil:
		return b.Text.Text
	case b.ResourceLink != nil:
		name := b.ResourceLink.Name
		if name == "" {
			name = b.ResourceLink.Uri
		}
		return fmt.Sprintf("[%s](%s)", name, b.ResourceLink.Uri)
	case b.Resource != nil:
		if r := b.Resource.Resource.TextResourceContents; r != nil {
			return r.Text
		}
		return ""
	case b.Image != nil:
		return "[image]"
	case b.Audio != nil:
		return "[audio]"
	}
	return ""
}

// promptBlocks converts a harness prompt into ACP content blocks. Images go along only when the agent accepts
// them; dropped is true when something was left out.
func promptBlocks(in []*llmv1.ContentBlock, caps sdk.PromptCapabilities) (out []sdk.ContentBlock, dropped bool) {
	for _, b := range in {
		switch v := b.GetBlock().(type) {
		case *llmv1.ContentBlock_Text:
			if v.Text.GetText() != "" {
				out = append(out, sdk.TextBlock(v.Text.GetText()))
			}
		case *llmv1.ContentBlock_Image:
			if caps.Image && len(v.Image.GetData()) > 0 {
				out = append(out, sdk.ImageBlock(base64.StdEncoding.EncodeToString(v.Image.GetData()), v.Image.GetMediaType()))
			} else {
				dropped = true
			}
		default:
			dropped = true
		}
	}
	return out, dropped
}

// authMethods converts the agent's auth methods.
func authMethods(in []sdk.AuthMethod) []registry.AuthMethod {
	out := []registry.AuthMethod{}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	for _, m := range in {
		switch {
		case m.Agent != nil:
			out = append(out, registry.AuthMethod{ID: m.Agent.Id, Name: m.Agent.Name, Description: str(m.Agent.Description), Kind: "agent"})
		case m.Terminal != nil:
			env := map[string]string{}
			for k, v := range m.Terminal.Env {
				env[k] = fmt.Sprint(v)
			}
			out = append(out, registry.AuthMethod{ID: m.Terminal.Id, Name: m.Terminal.Name, Description: str(m.Terminal.Description), Kind: "terminal",
				Args: m.Terminal.Args, Env: env})
		case m.EnvVar != nil:
			am := registry.AuthMethod{ID: m.EnvVar.Id, Name: m.EnvVar.Name, Description: str(m.EnvVar.Description), Kind: "env_var", Link: str(m.EnvVar.Link)}
			for _, v := range m.EnvVar.Vars {
				am.EnvVars = append(am.EnvVars, v.Name)
			}
			out = append(out, am)
		}
	}
	return out
}

// AuthMethodsToProto converts stored auth methods for frontends.
func AuthMethodsToProto(in []registry.AuthMethod) []*agentv1.AcpAuthMethod {
	out := make([]*agentv1.AcpAuthMethod, 0, len(in))
	for _, m := range in {
		k := agentv1.AcpAuthMethod_KIND_AGENT
		switch m.Kind {
		case "terminal":
			k = agentv1.AcpAuthMethod_KIND_TERMINAL
		case "env_var":
			k = agentv1.AcpAuthMethod_KIND_ENV_VAR
		}
		out = append(out, &agentv1.AcpAuthMethod{Id: m.ID, Name: m.Name, Description: m.Description, Kind: k, EnvVars: m.EnvVars, Link: m.Link})
	}
	return out
}

// harnessModes maps ACP mode ids onto harness permission modes (claude-acp uses Claude Code's mode names).
var harnessModes = map[string]agentv1.PermissionMode{
	"default":           agentv1.PermissionMode_PERMISSION_MODE_DEFAULT,
	"acceptEdits":       agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS,
	"accept_edits":      agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS,
	"plan":              agentv1.PermissionMode_PERMISSION_MODE_PLAN,
	"dontAsk":           agentv1.PermissionMode_PERMISSION_MODE_DONT_ASK,
	"bypassPermissions": agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS,
	"bypass":            agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS,
}

// PermissionModeOf is the harness mode shown for an ACP mode id (DEFAULT when it has none).
func PermissionModeOf(modeID string) agentv1.PermissionMode {
	if m, ok := harnessModes[modeID]; ok {
		return m
	}
	return agentv1.PermissionMode_PERMISSION_MODE_DEFAULT
}

// modeFor returns the agent's mode id for a harness permission mode ("" when the agent has none).
func modeFor(pm agentv1.PermissionMode, modes []Mode) string {
	if pm == agentv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED {
		pm = agentv1.PermissionMode_PERMISSION_MODE_DEFAULT
	}
	for _, m := range modes {
		if hm, ok := harnessModes[m.ID]; ok && hm == pm {
			return m.ID
		}
	}
	return ""
}

func todoStatus(s sdk.PlanEntryStatus) agentv1.Todo_Status {
	switch s {
	case sdk.PlanEntryStatusInProgress:
		return agentv1.Todo_STATUS_IN_PROGRESS
	case sdk.PlanEntryStatusCompleted:
		return agentv1.Todo_STATUS_COMPLETED
	}
	return agentv1.Todo_STATUS_PENDING
}

// turnResult maps an ACP stop reason.
func turnResult(r sdk.StopReason) (agentv1.TurnFinished_Result, string) {
	switch r {
	case sdk.StopReasonCancelled:
		return agentv1.TurnFinished_RESULT_INTERRUPTED, ""
	case sdk.StopReasonMaxTurnRequests:
		return agentv1.TurnFinished_RESULT_ERROR_MAX_TURNS, "the agent reached its maximum number of requests for this turn"
	case sdk.StopReasonRefusal:
		return agentv1.TurnFinished_RESULT_ERROR_DURING_EXECUTION, "the agent refused to continue"
	}
	return agentv1.TurnFinished_RESULT_SUCCESS, ""
}

func stopReason(r sdk.StopReason) llmv1.StopReason {
	switch r {
	case sdk.StopReasonMaxTokens:
		return llmv1.StopReason_STOP_REASON_MAX_TOKENS
	case sdk.StopReasonRefusal:
		return llmv1.StopReason_STOP_REASON_REFUSAL
	}
	return llmv1.StopReason_STOP_REASON_END_TURN
}

func usageProto(u *sdk.Usage) *llmv1.Usage {
	if u == nil {
		return nil
	}
	out := &llmv1.Usage{InputTokens: int64(u.InputTokens), OutputTokens: int64(u.OutputTokens)}
	if u.CachedReadTokens != nil {
		out.CacheReadInputTokens = int64(*u.CachedReadTokens)
	}
	if u.CachedWriteTokens != nil {
		out.CacheCreationInputTokens = int64(*u.CachedWriteTokens)
	}
	return out
}
