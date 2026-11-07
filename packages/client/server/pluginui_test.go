package server_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/plugins"
)

// writeUIPlugin writes a plugin with a pane and two status-bar items, declaring every capability but files.open unless asked.
func writeUIPlugin(t *testing.T, caps ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "meter")
	ui, _ := json.Marshal(map[string]any{
		"panes":        []any{map[string]any{"id": "meter", "title": "Session cost", "page": "./ui/meter.html"}},
		"statusItems":  []any{map[string]any{"id": "cost", "text": "Cost", "pane": "meter"}, map[string]any{"id": "ask", "text": "Explain", "prompt": "Explain the cost"}},
		"capabilities": caps,
		"network":      []string{"https://api.example.com"},
	})
	writeFile(t, filepath.Join(dir, ".harness-plugin", "plugin.json"), `{"name":"meter","displayName":"Cost meter","ui":`+string(ui)+`}`)
	writeFile(t, filepath.Join(dir, "ui", "meter.html"), `<!doctype html><title>x</title><script src="./meter.js"></script>`)
	writeFile(t, filepath.Join(dir, "ui", "meter.js"), `harness.call("session.read")`)
	return dir
}

func TestPluginPaneCallsNeedTheDeclarationAndThisDevicesApproval(t *testing.T) {
	rec := &recording{}
	h := startPlugged(t, rec.respond, true)
	api := h.plugins()
	store := bootstrap.PluginStore()
	r, err := store.Install(h.ctx, writeUIPlugin(t, "session.read", "session.prompt", "theme.read", "files.open"))
	if err != nil {
		t.Fatal(err)
	}
	sid, ch := h.create(&agentv1.CreateSessionRequest{})

	// Not approved: no UI.
	list, err := api.ListPluginUi(h.ctx, connect.NewRequest(&agentv1.ListPluginUiRequest{SessionId: sid}))
	if err != nil || len(list.Msg.GetPanes()) != 0 {
		t.Fatalf("an unapproved plugin shows a pane: %v %v", list, err)
	}
	if err := store.Approve(r.ID, r.Hash); err != nil {
		t.Fatal(err)
	}
	list, err = api.ListPluginUi(h.ctx, connect.NewRequest(&agentv1.ListPluginUiRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.GetPanes()) != 1 || len(list.Msg.GetStatusItems()) != 2 || list.Msg.GetPluginNames()["meter@local"] != "Cost meter" {
		t.Fatalf("%+v", list.Msg)
	}

	pane, err := api.GetPluginPane(h.ctx, connect.NewRequest(&agentv1.GetPluginPaneRequest{PluginId: "meter@local", PaneId: "meter", SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pane.Msg.GetHtml(), `harness.call("session.read")`) || !strings.Contains(pane.Msg.GetCsp(), "connect-src 'none'") ||
		len(pane.Msg.GetNetworkPending()) != 1 || pane.Msg.GetContentHash() != r.Hash {
		t.Fatalf("%+v", pane.Msg)
	}

	call := func(method, params string) (*agentv1.CallPluginUiResponse, error) {
		res, err := api.CallPluginUi(h.ctx, connect.NewRequest(&agentv1.CallPluginUiRequest{PluginId: "meter@local", SessionId: sid, Version: 1, Method: method, ParamsJson: params}))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	grant := func(c string) {
		t.Helper()
		if _, err := api.SetPluginUiGrant(h.ctx, connect.NewRequest(&agentv1.SetPluginUiGrantRequest{PluginId: "meter@local", Capability: c, Granted: true, ContentHash: r.Hash})); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := api.CallPluginUi(h.ctx, connect.NewRequest(&agentv1.CallPluginUiRequest{PluginId: "meter@local", SessionId: sid, Version: 2, Method: "session.read"})); err == nil {
		t.Fatal("a version this client doesn't speak was answered")
	}
	if _, err := call("tools.run", ""); err == nil {
		t.Fatal("an unknown method was answered")
	}
	res, err := call("session.read", "")
	if err != nil || res.GetNeedsApproval() != "session.read" || res.GetResultJson() != "" {
		t.Fatalf("read before approval: %+v %v", res, err)
	}

	h.send(sid, "hello there")
	h.until(ch, isTurnFinished)
	grant("session.read")
	res, err = call("session.read", "")
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		ID, Cwd, Mode, Status string
		Messages              []struct{ Role, Text string }
	}
	if err := json.Unmarshal([]byte(res.GetResultJson()), &view); err != nil {
		t.Fatal(err)
	}
	if view.ID != sid || view.Cwd == "" || view.Status != "idle" || len(view.Messages) == 0 || view.Messages[0].Text != "hello there" {
		t.Fatalf("%s", res.GetResultJson())
	}

	res, _ = call("theme.read", "")
	if res.GetNeedsApproval() != "theme.read" {
		t.Fatal("theme before approval")
	}
	grant("theme.read")
	if res, _ = call("theme.read", ""); !res.GetTheme() {
		t.Fatal("theme")
	}

	grant("files.open")
	writeFile(t, filepath.Join(h.proj, "notes.md"), "x")
	if res, err = call("files.open", `{"path":"notes.md"}`); err != nil || res.GetOpenFile() != filepath.Join(h.proj, "notes.md") {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err = call("files.open", `{"path":"/etc/hosts"}`); err == nil {
		t.Fatal("a file outside the session opened")
	}

	grant("session.prompt")
	if _, err = call("session.prompt", `{"text":"/compact"}`); err != nil {
		t.Fatal(err)
	}
	h.until(ch, isTurnFinished)
	if text := allText(rec.last(t)); !strings.Contains(text, "[from plugin meter] /compact") {
		t.Fatalf("the plugin's prompt didn't reach the model as a message: %s", text)
	}
	if _, err = call("session.prompt", `{"text":"again"}`); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("a second prompt at once: %v", err)
	}

	// Revoked: asked again.
	if _, err := api.SetPluginUiGrant(h.ctx, connect.NewRequest(&agentv1.SetPluginUiGrantRequest{PluginId: "meter@local", Capability: "session.read"})); err != nil {
		t.Fatal(err)
	}
	if res, _ = call("session.read", ""); res.GetNeedsApproval() != "session.read" {
		t.Fatal("a revoked capability still answers")
	}
	ps, err := api.ListPlugins(h.ctx, connect.NewRequest(&agentv1.ListPluginsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := ps.Msg.GetPlugins()[0]
	if len(got.GetContents().GetPanes()) != 1 || len(got.GetUiGrants()) != 5 || got.GetUiGrants()[0].GetGranted() || !got.GetUiGrants()[1].GetGranted() {
		t.Fatalf("Extensions: %+v", got)
	}

	// Network approved: the pane's CSP names the origin.
	grant("network")
	pane, _ = api.GetPluginPane(h.ctx, connect.NewRequest(&agentv1.GetPluginPaneRequest{PluginId: "meter@local", PaneId: "meter", SessionId: sid}))
	if !strings.Contains(pane.Msg.GetCsp(), "connect-src https://api.example.com") || len(pane.Msg.GetNetworkPending()) != 0 {
		t.Fatal(pane.Msg.GetCsp())
	}

	// Policy turns it all off.
	plugins.SetPolicy("", plugins.Policy{UIDisabled: true})
	t.Cleanup(plugins.ResetPolicy)
	list, err = api.ListPluginUi(h.ctx, connect.NewRequest(&agentv1.ListPluginUiRequest{SessionId: sid}))
	if err != nil || list.Msg.GetBlocked() == "" || len(list.Msg.GetPanes()) != 0 {
		t.Fatalf("%+v %v", list, err)
	}
	if _, err := call("theme.read", ""); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a call under a policy that turns plugin UI off: %v", err)
	}
	prefs, err := h.client.GetUserPreferences(h.ctx, connect.NewRequest(&agentv1.GetUserPreferencesRequest{}))
	if err != nil || !prefs.Msg.GetPreferences().GetManagedPolicy().GetPluginUiDisabled() {
		t.Fatalf("the policy view: %v", err)
	}
}

func TestPluginPaneWithoutTheCapabilityIsRefused(t *testing.T) {
	h := startPlugged(t, (&recording{}).respond, true)
	store := bootstrap.PluginStore()
	r, err := store.Install(h.ctx, writeUIPlugin(t, "session.read"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Approve(r.ID, r.Hash); err != nil {
		t.Fatal(err)
	}
	sid, _ := h.create(&agentv1.CreateSessionRequest{})
	_, err = h.plugins().CallPluginUi(h.ctx, connect.NewRequest(&agentv1.CallPluginUiRequest{PluginId: "meter@local", SessionId: sid, Version: 1, Method: "files.open", ParamsJson: `{"path":"x"}`}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an undeclared capability: %v", err)
	}
	if _, err := h.plugins().SetPluginUiGrant(h.ctx, connect.NewRequest(&agentv1.SetPluginUiGrantRequest{PluginId: "meter@local", Capability: "files.open", Granted: true, ContentHash: r.Hash})); err == nil {
		t.Fatal("granted what isn't declared")
	}
}
