package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/plugins"
)

// Plugin UI (docs/design/plugin-ui.md): the panes and status-bar items of the plugins that may run in a session's folder, the
// documents of the panes, and the message API they call through the frontend that hosts them. The client decides everything
// here: whether a plugin may show anything, what a pane's CSP allows, and whether a call's capability was declared and
// approved on this device. The frontend only frames the page, asks the user, and shows what a call returns.

// pluginPromptGap is the least time between two prompts one plugin sends to one session.
const pluginPromptGap = 2 * time.Second

// Limits of what a pane reads and sends.
const (
	pluginPromptMax     = 4000
	pluginSummaryCount  = 6
	pluginSummaryLength = 400
)

var pluginPromptLast sync.Map // session id + "\x00" + plugin id → time.Time

// pluginUIAllowed refuses plugin UI to another machine: panes, grants and calls are this device's.
func pluginUIAllowed(ctx context.Context, peer string) error {
	remote, _ := ctx.Value(remoteOriginKey{}).(bool)
	if remote || !localPeer(peer) {
		return connect.NewError(connect.CodePermissionDenied, errors.New("plugin panes run on the device that has the plugin"))
	}
	return nil
}

// sessionPlugin returns a plugin that may run in a session's folder and show UI there.
func (s *Server) sessionPlugin(sessionID, pluginID string) (*liveSession, *plugins.Plugin, error) {
	ls, err := s.get(sessionID)
	if err != nil {
		return nil, nil, err
	}
	store := bootstrap.PluginStore()
	if why := store.Policy().UIBlocked(); why != "" {
		return nil, nil, connect.NewError(connect.CodePermissionDenied, errors.New(why))
	}
	for _, p := range store.Active(s.pluginPlace(ls.sess.Dir())) {
		if p.ID == pluginID {
			if p.UI.Empty() {
				break
			}
			return ls, p, nil
		}
	}
	return nil, nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s does not run in this session's folder", pluginID))
}

func paneToProto(pluginID string, p plugins.UIPane) *agentv1.PluginPane {
	return &agentv1.PluginPane{PluginId: pluginID, Id: p.ID, Title: p.Title, Icon: p.Icon, Page: p.Page}
}

func statusItemToProto(pluginID string, i plugins.UIStatusItem) *agentv1.PluginStatusItem {
	return &agentv1.PluginStatusItem{PluginId: pluginID, Id: i.ID, Text: i.Text, Icon: i.Icon, Tooltip: i.Tooltip, Pane: i.Pane, Prompt: i.Prompt}
}

func pluginDisplayName(p *plugins.Plugin) string {
	if p.Manifest.DisplayName != "" {
		return p.Manifest.DisplayName
	}
	return p.Name
}

// ListPluginUi implements PluginService.ListPluginUi.
func (s *Server) ListPluginUi(ctx context.Context, req *connect.Request[agentv1.ListPluginUiRequest]) (*connect.Response[agentv1.ListPluginUiResponse], error) {
	if err := pluginUIAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	store := bootstrap.PluginStore()
	res := &agentv1.ListPluginUiResponse{PluginNames: map[string]string{}}
	if why := store.Policy().UIBlocked(); why != "" {
		res.Blocked = why
		return connect.NewResponse(res), nil
	}
	for _, p := range store.Active(s.pluginPlace(ls.sess.Dir())) {
		if p.UI.Empty() {
			continue
		}
		res.PluginNames[p.ID] = pluginDisplayName(p)
		for _, pane := range p.UI.Panes {
			res.Panes = append(res.Panes, paneToProto(p.ID, pane))
		}
		for _, it := range p.UI.StatusItems {
			res.StatusItems = append(res.StatusItems, statusItemToProto(p.ID, it))
		}
	}
	return connect.NewResponse(res), nil
}

// GetPluginPane implements PluginService.GetPluginPane.
func (s *Server) GetPluginPane(ctx context.Context, req *connect.Request[agentv1.GetPluginPaneRequest]) (*connect.Response[agentv1.GetPluginPaneResponse], error) {
	if err := pluginUIAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	_, p, err := s.sessionPlugin(req.Msg.GetSessionId(), req.Msg.GetPluginId())
	if err != nil {
		return nil, err
	}
	pane, ok := p.UI.Pane(req.Msg.GetPaneId())
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%s has no pane %q", p.ID, req.Msg.GetPaneId()))
	}
	store := bootstrap.PluginStore()
	var network, pending []string
	if !store.Policy().UINetworkDisabled && len(p.UI.Network) > 0 {
		if store.UIGranted(p.ID, plugins.CapNetwork) {
			network = p.UI.Network
		} else {
			pending = p.UI.Network
		}
	}
	doc, csp, err := plugins.PaneDocument(p, pane, network)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	info, err := store.Find(p.ID, plugins.Place{})
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&agentv1.GetPluginPaneResponse{Pane: paneToProto(p.ID, pane), Html: doc, Csp: csp, NetworkPending: pending,
		PluginName: pluginDisplayName(p), ContentHash: info.Hash}), nil
}

// CallPluginUi implements PluginService.CallPluginUi: one call of the message API. A capability the plugin didn't declare is
// refused; one this device hasn't approved comes back as needs_approval, for the frontend to ask the user.
func (s *Server) CallPluginUi(ctx context.Context, req *connect.Request[agentv1.CallPluginUiRequest]) (*connect.Response[agentv1.CallPluginUiResponse], error) {
	if err := pluginUIAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	if v := req.Msg.GetVersion(); v != plugins.UIAPIVersion {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("this client speaks version %d of the message API, not %d", plugins.UIAPIVersion, v))
	}
	ls, p, err := s.sessionPlugin(req.Msg.GetSessionId(), req.Msg.GetPluginId())
	if err != nil {
		return nil, err
	}
	method := req.Msg.GetMethod()
	if method == plugins.CapNetwork || !containsCap(method) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown method %q", method))
	}
	if !p.UI.Declares(method) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s does not declare %s in its manifest", p.ID, method))
	}
	if !bootstrap.PluginStore().UIGranted(p.ID, method) {
		return connect.NewResponse(&agentv1.CallPluginUiResponse{NeedsApproval: method}), nil
	}
	var params map[string]any
	if raw := strings.TrimSpace(req.Msg.GetParamsJson()); raw != "" {
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("params must be a JSON object"))
		}
	}
	text := func(key string) string { v, _ := params[key].(string); return v }
	switch method {
	case plugins.CapSessionRead:
		data, err := json.Marshal(ls.pluginView())
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return connect.NewResponse(&agentv1.CallPluginUiResponse{ResultJson: string(data)}), nil
	case plugins.CapSessionPrompt:
		body := strings.TrimSpace(text("text"))
		if body == "" || len([]rune(body)) > pluginPromptMax {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("text must hold 1 to %d characters", pluginPromptMax))
		}
		key := ls.sess.ID + "\x00" + p.ID
		now := time.Now()
		if last, ok := pluginPromptLast.Load(key); ok && now.Sub(last.(time.Time)) < pluginPromptGap {
			return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("one prompt every two seconds"))
		}
		pluginPromptLast.Store(key, now)
		// Straight to the session, past slash commands: what a plugin sends is a message, never a command, and the prefix
		// tells the model it came from the plugin, not from the user.
		msg := connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: ls.sess.ID,
			Content: []*llmv1.ContentBlock{{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{Text: PluginPromptPrefix(p.Name) + body}}}}})
		if _, err := s.submitMessage(ls, msg); err != nil {
			return nil, err
		}
		return connect.NewResponse(&agentv1.CallPluginUiResponse{ResultJson: `{"sent":true}`}), nil
	case plugins.CapFilesOpen:
		sc, err := s.fileScope(ls.sess.ID)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(text("path")) == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("path is required"))
		}
		clean, real, err := sc.resolve(text("path"))
		if err != nil {
			return nil, err
		}
		if !isRegular(real) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is not a file", clean))
		}
		data, _ := json.Marshal(map[string]string{"path": clean})
		return connect.NewResponse(&agentv1.CallPluginUiResponse{ResultJson: string(data), OpenFile: clean}), nil
	case plugins.CapThemeRead:
		return connect.NewResponse(&agentv1.CallPluginUiResponse{Theme: true}), nil
	}
	return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown method %q", method))
}

// PluginPromptPrefix starts a message a plugin sends: "[from plugin cost-meter] ...".
func PluginPromptPrefix(name string) string { return "[from plugin " + name + "] " }

func isRegular(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func containsCap(c string) bool {
	for _, k := range plugins.UICapabilities {
		if k == c {
			return true
		}
	}
	return false
}

// SetPluginUiGrant implements PluginService.SetPluginUiGrant.
func (s *Server) SetPluginUiGrant(ctx context.Context, req *connect.Request[agentv1.SetPluginUiGrantRequest]) (*connect.Response[agentv1.SetPluginUiGrantResponse], error) {
	if err := changesAllowed(ctx, req.Peer().Addr); err != nil {
		return nil, err
	}
	m := req.Msg
	if err := bootstrap.PluginStore().SetUIGrant(m.GetPluginId(), m.GetCapability(), m.GetGranted(), m.GetContentHash()); err != nil {
		return nil, pluginError(err)
	}
	return connect.NewResponse(&agentv1.SetPluginUiGrantResponse{}), nil
}

// pluginSession is what session.read returns: version 1 of the message API.
type pluginSession struct {
	ID           string           `json:"id"`
	Title        string           `json:"title"`
	Model        string           `json:"model"`
	Mode         string           `json:"mode"`
	Cwd          string           `json:"cwd"`
	Status       string           `json:"status"`
	CostUSD      float64          `json:"costUsd"`
	InputTokens  int              `json:"inputTokens"`
	OutputTokens int              `json:"outputTokens"`
	CacheRead    int              `json:"cacheReadTokens"`
	CacheWrite   int              `json:"cacheWriteTokens"`
	Messages     []pluginMessage  `json:"messages"`
	Usage        *pluginTurnUsage `json:"lastTurn,omitempty"`
}

type pluginMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type pluginTurnUsage struct {
	CostUSD      float64 `json:"costUsd"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	DurationMs   int64   `json:"durationMs"`
}

var pluginStatusNames = map[agentv1.SessionStatus]string{
	agentv1.SessionStatus_SESSION_STATUS_IDLE: "idle", agentv1.SessionStatus_SESSION_STATUS_WORKING: "working",
	agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT: "needs_input",
}

// pluginView is the session as a pane may read it: what it is about, and a summary of the last messages, text only.
func (ls *liveSession) pluginView() pluginSession {
	info := ls.info()
	cost, used := ls.sess.Agent.Spent()
	v := pluginSession{ID: info.GetId(), Title: info.GetTitle(), Model: info.GetModel(), Cwd: info.GetCwd(), Status: pluginStatusNames[info.GetStatus()],
		CostUSD: cost, InputTokens: used.InputTokens, OutputTokens: used.OutputTokens, CacheRead: used.CacheReadInputTokens, CacheWrite: used.CacheCreationInputTokens,
		Messages: []pluginMessage{}}
	if v.Status == "" {
		v.Status = "idle"
	}
	if ls.sess.Permissions != nil {
		v.Mode = string(ls.sess.Permissions.Mode())
	}
	ls.mu.Lock()
	last := ls.lastResult
	ls.mu.Unlock()
	if last.NumTurns > 0 || last.Usage.InputTokens > 0 {
		v.Usage = &pluginTurnUsage{CostUSD: last.CostUSD, InputTokens: last.Usage.InputTokens, OutputTokens: last.Usage.OutputTokens, DurationMs: last.Duration.Milliseconds()}
	}
	msgs := ls.sess.Agent.Messages()
	for i := len(msgs) - 1; i >= 0 && len(v.Messages) < pluginSummaryCount; i-- {
		if t := summaryText(msgs[i]); t != "" {
			v.Messages = append([]pluginMessage{{Role: msgs[i].Role, Text: t}}, v.Messages...)
		}
	}
	return v
}

// summaryText is a message's own text, cut short: no tool calls or results, attached files or reminders.
func summaryText(m llm.Message) string {
	if m.Role != "user" && m.Role != "assistant" {
		return ""
	}
	var parts []string
	for _, b := range m.Content {
		t := strings.TrimSpace(b.Text)
		if b.Type != llm.BlockText || t == "" || strings.HasPrefix(t, "<") {
			continue
		}
		parts = append(parts, t)
	}
	out := strings.Join(parts, "\n")
	if r := []rune(out); len(r) > pluginSummaryLength {
		out = string(r[:pluginSummaryLength]) + "…"
	}
	return out
}
