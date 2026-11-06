package server_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/fakemcp"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/mcp"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/server"
)

func (h *harness) plugins() agentv1connect.PluginServiceClient {
	return agentv1connect.NewPluginServiceClient(rpc.UnixHTTPClient(h.socket), "http://agent", connect.WithGRPC())
}

// startPlugged starts a harness whose hooks run, in a home of its own.
func startPlugged(t *testing.T, respond fg.Responder, isTrusted bool, mod ...func(*bootstrap.Options)) *harness {
	t.Helper()
	gittest.Isolate(t)
	h := startHarness(t, respond, func(o *bootstrap.Options) {
		o.Trusted = &isTrusted
		for _, m := range mod {
			m(o)
		}
	})
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HARNESS_NO_HOOKS", "")
	return h
}

// writePlugin writes a plugin with one of everything and returns its folder. Its hook and its skill leave marks in marks.
func writePlugin(t *testing.T, marks string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "kit")
	exe, _ := os.Executable()
	server, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"srv": map[string]any{"command": exe, "env": map[string]string{fakemcp.EnvMarker: "1"}}}})
	for rel, text := range map[string]string{
		".claude-plugin/plugin.json": `{"name":"kit","version":"1.0.0","description":"Everything"}`,
		"skills/fmt/SKILL.md":        "---\ndescription: Format things\n---\nFormat using ${CLAUDE_PLUGIN_ROOT}/tool and keep state in ${HARNESS_PLUGIN_DATA}; args $ARGUMENTS.",
		"commands/check.md":          "---\ndescription: Check things\n---\nCheck it.",
		"agents/helper.md":           "---\nname: helper\ndescription: A plugin's helper\n---\nHelp from ${CLAUDE_PLUGIN_ROOT}.",
		"output-styles/terse.md":     "---\nname: terse\ndescription: Short\n---\nBe terse.",
		"hooks/hooks.json":           `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"echo \"$CLAUDE_PLUGIN_ROOT\" > '` + marks + `/hook-ran'"}]}]}}`,
		".mcp.json":                  string(server),
	} {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(rel)), text)
	}
	return dir
}

func offeredTools(r *llm.Request) []string {
	var out []string
	for _, d := range r.Tools {
		out = append(out, d.Name)
	}
	return out
}

func fileExistsNow(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestPluginRunsOnlyOnceApprovedAndInATrustedFolder(t *testing.T) {
	rec := &recording{}
	h := startPlugged(t, rec.respond, true, func(o *bootstrap.Options) { o.MCP = mcp.NewClient(fakemcp.NewService().Start(t), nil, nil) })
	marks := t.TempDir()
	dir := writePlugin(t, marks)
	api := h.plugins()

	res, err := api.InstallPlugin(h.ctx, connect.NewRequest(&agentv1.InstallPluginRequest{Spec: dir, Cwd: h.proj}))
	if err != nil {
		t.Fatal(err)
	}
	p := res.Msg.GetPlugin()
	if p.GetId() != "kit@local" || p.GetStatus() != agentv1.PluginStatus_PLUGIN_STATUS_NEEDS_APPROVAL || p.GetVersion() != "1.0.0" || p.GetContentHash() == "" {
		t.Fatalf("%+v", p)
	}
	c := p.GetContents()
	if !slices.Contains(c.GetSkills(), "kit:fmt") || !slices.Contains(c.GetCommands(), "kit:check") || len(c.GetHooks()) != 1 || len(c.GetMcpServers()) != 1 {
		t.Fatalf("what the user reads before approving: %+v", c)
	}

	// Installed, not approved: nothing of it reaches a session.
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "hello")
	h.until(ch, isTurnFinished)
	first := rec.last(t)
	if strings.Contains(allText(first), "kit:fmt") {
		t.Fatalf("an unapproved plugin's skill is listed: %s", allText(first))
	}
	if fileExistsNow(filepath.Join(marks, "hook-ran")) {
		t.Fatal("an unapproved plugin's hook ran")
	}
	if slices.ContainsFunc(offeredTools(first), func(n string) bool { return strings.HasPrefix(n, "mcp__plugin") }) {
		t.Fatal("an unapproved plugin's server started")
	}
	if _, ok := h.commands(&agentv1.ListSlashCommandsRequest{SessionId: sid})["kit:fmt"]; ok {
		t.Fatal("an unapproved plugin's skill is a command")
	}

	// Approving needs what was shown.
	if _, err := api.SetPluginApproval(h.ctx, connect.NewRequest(&agentv1.SetPluginApprovalRequest{Id: "kit@local", Approved: true})); err == nil {
		t.Fatal("approved without saying what")
	}
	if _, err := api.SetPluginApproval(h.ctx, connect.NewRequest(&agentv1.SetPluginApprovalRequest{Id: "kit@local", Approved: true, ContentHash: "sha256:other"})); err == nil {
		t.Fatal("approved other content")
	}
	if _, err := api.SetPluginApproval(h.ctx, connect.NewRequest(&agentv1.SetPluginApprovalRequest{Id: "kit@local", Approved: true, ContentHash: p.GetContentHash()})); err != nil {
		t.Fatal(err)
	}

	// A new session has all of it, namespaced.
	sid2, ch2 := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid2, "/kit:fmt prod")
	h.until(ch2, isTurnFinished)
	r := rec.last(t)
	text := allText(r)
	cmds := h.commands(&agentv1.ListSlashCommandsRequest{SessionId: sid2})
	if cmds["kit:fmt"].GetSource() != "plugin" || cmds["kit:check"].GetKind() != agentv1.SlashCommandKind_SLASH_COMMAND_KIND_COMMAND {
		t.Fatalf("commands: %+v", cmds["kit:fmt"])
	}
	list, err := api.ListPlugins(h.ctx, connect.NewRequest(&agentv1.ListPluginsRequest{Cwd: h.proj}))
	if err != nil {
		t.Fatal(err)
	}
	got := list.Msg.GetPlugins()[0]
	root := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "Format using ") {
			root = strings.TrimSuffix(strings.TrimPrefix(strings.Fields(line)[2], ""), "/tool")
			break
		}
	}
	if root == "" || !strings.Contains(root, filepath.Join("plugins", "cache", "local", "kit")) {
		t.Fatalf("the skill's body names the installed folder: %q\n%s", root, text)
	}
	if !strings.Contains(text, "args prod") || strings.Contains(text, "${CLAUDE_PLUGIN_ROOT}") || strings.Contains(text, "${HARNESS_PLUGIN_DATA}") {
		t.Fatalf("placeholders stayed: %s", text)
	}
	if got.GetStatus() != agentv1.PluginStatus_PLUGIN_STATUS_ACTIVE {
		t.Fatalf("%+v", got)
	}
	if data, err := os.ReadFile(filepath.Join(marks, "hook-ran")); err != nil || strings.TrimSpace(string(data)) != root {
		t.Fatalf("the hook gets the plugin's folder in its environment: %q %v", data, err)
	}
	if !slices.Contains(offeredTools(r), "mcp__plugin_kit_srv__echo") {
		t.Fatalf("the plugin's server: %v", offeredTools(r))
	}
	styles, err := h.client.ListOutputStyles(h.ctx, connect.NewRequest(&agentv1.ListOutputStylesRequest{SessionId: sid2}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(styles.Msg.String(), "kit:terse") {
		t.Fatalf("the plugin's style: %s", styles.Msg)
	}
	hooksList, err := h.client.ListHooks(h.ctx, connect.NewRequest(&agentv1.ListHooksRequest{SessionId: sid2}))
	if err != nil {
		t.Fatal(err)
	}
	if len(hooksList.Msg.GetHooks()) != 1 || hooksList.Msg.GetHooks()[0].GetSource() != "plugin:kit@local" || hooksList.Msg.GetHooks()[0].GetStatus() != agentv1.ConfiguredHook_STATUS_ACTIVE {
		t.Fatalf("Settings > Hooks lists it: %+v", hooksList.Msg.GetHooks())
	}

	// Taken back: its skills go at once.
	if _, err := api.SetPluginApproval(h.ctx, connect.NewRequest(&agentv1.SetPluginApprovalRequest{Id: "kit@local"})); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.commands(&agentv1.ListSlashCommandsRequest{SessionId: sid2})["kit:fmt"]; ok {
		t.Fatal("a revoked plugin's skill stayed")
	}
}

func TestApprovedPluginStaysOutOfAnUntrustedFolder(t *testing.T) {
	rec := &recording{}
	h := startPlugged(t, rec.respond, false, func(o *bootstrap.Options) { o.MCP = mcp.NewClient(fakemcp.NewService().Start(t), nil, nil) })
	marks := t.TempDir()
	store := bootstrap.PluginStore()
	r, err := store.Install(h.ctx, writePlugin(t, marks))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Approve(r.ID, r.Hash); err != nil {
		t.Fatal(err)
	}
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "hello")
	h.until(ch, isTurnFinished)
	req := rec.last(t)
	if strings.Contains(allText(req), "kit:fmt") || fileExistsNow(filepath.Join(marks, "hook-ran")) {
		t.Fatal("a plugin ran in an untrusted folder")
	}
	if slices.ContainsFunc(offeredTools(req), func(n string) bool { return strings.HasPrefix(n, "mcp__plugin") }) {
		t.Fatal("a plugin's server started in an untrusted folder")
	}
	list, err := h.plugins().ListPlugins(h.ctx, connect.NewRequest(&agentv1.ListPluginsRequest{Cwd: h.proj}))
	if err != nil {
		t.Fatal(err)
	}
	if list.Msg.GetFolderTrusted() || list.Msg.GetPlugins()[0].GetStatus() != agentv1.PluginStatus_PLUGIN_STATUS_UNTRUSTED_FOLDER {
		t.Fatalf("%+v", list.Msg)
	}
	hooksList, err := h.client.ListHooks(h.ctx, connect.NewRequest(&agentv1.ListHooksRequest{Cwd: h.proj}))
	if err != nil {
		t.Fatal(err)
	}
	if hooksList.Msg.GetHooks()[0].GetStatus() != agentv1.ConfiguredHook_STATUS_NEEDS_WORKSPACE_TRUST {
		t.Fatalf("%+v", hooksList.Msg.GetHooks())
	}
}

func TestPluginAgentIsOffered(t *testing.T) {
	var mu sync.Mutex
	var prompts []string
	rec := &recording{next: func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("a", "Agent", map[string]any{"description": "d", "prompt": "go", "subagent_type": "kit:helper"}))}
		}
		mu.Lock()
		prompts = append(prompts, allText(r))
		mu.Unlock()
		return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
	}}
	h := startPlugged(t, rec.respond, true)
	store := bootstrap.PluginStore()
	r, err := store.Install(h.ctx, writePlugin(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Approve(r.ID, r.Hash); err != nil {
		t.Fatal(err)
	}
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "go")
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return isTurnFinished(ev) })
	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		found := slices.ContainsFunc(prompts, func(p string) bool {
			return strings.Contains(p, "Help from ") && !strings.Contains(p, "${CLAUDE_PLUGIN_ROOT}")
		})
		all := strings.Join(prompts, "\n---\n")
		mu.Unlock()
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the plugin's agent never ran with its text: %s", all)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSlashPluginWalkThrough(t *testing.T) {
	rec := &recording{}
	h := startPlugged(t, rec.respond, true)
	marks := t.TempDir()
	dir := writePlugin(t, marks)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	before := len(rec.all())

	run := func(line string) *agentv1.SlashCommandResult {
		t.Helper()
		h.send(sid, line)
		return h.result(ch)
	}
	r := run("/plugin")
	if r.GetAction() != agentv1.SlashCommandResult_ACTION_OPEN_PLUGINS || !strings.Contains(r.GetText(), "No plugins") {
		t.Fatalf("%+v", r)
	}
	r = run("/plugin install " + dir)
	if r.GetIsError() || !strings.Contains(r.GetText(), "Nothing of it runs yet") || !strings.Contains(r.GetText(), "/plugin approve kit@local") {
		t.Fatalf("%+v", r)
	}
	if _, ok := h.commands(&agentv1.ListSlashCommandsRequest{SessionId: sid})["kit:fmt"]; ok {
		t.Fatal("it runs before approval")
	}
	if r = run("/plugin approve kit"); r.GetIsError() || !strings.Contains(r.GetText(), "Approved") {
		t.Fatalf("%+v", r)
	}
	if _, ok := h.commands(&agentv1.ListSlashCommandsRequest{SessionId: sid})["kit:fmt"]; !ok {
		t.Fatal("skills apply to the running session")
	}
	if r = run("/plugin disable kit"); r.GetIsError() {
		t.Fatalf("%+v", r)
	}
	if r = run("/plugin remove kit@local"); r.GetIsError() {
		t.Fatalf("%+v", r)
	}
	if r = run("/plugin nonsense"); !r.GetIsError() {
		t.Fatalf("%+v", r)
	}
	if len(rec.all()) != before {
		t.Fatal("a /plugin command went to the model")
	}
	if _, ok := h.commands(&agentv1.ListSlashCommandsRequest{SessionId: sid})["plugin"]; !ok {
		t.Fatal("/plugin is not in the menu")
	}
}

func TestPluginChangesAreRefusedFromAnotherMachine(t *testing.T) {
	h := startPlugged(t, (&recording{}).respond, true)
	s := server.New(bootstrap.Options{})
	ctx := server.WithRemoteOrigin(h.ctx)
	store := bootstrap.PluginStore()
	dir := writePlugin(t, t.TempDir())
	if _, err := s.InstallPlugin(ctx, connect.NewRequest(&agentv1.InstallPluginRequest{Spec: dir})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatal(err)
	}
	r, err := store.Install(h.ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPluginApproval(ctx, connect.NewRequest(&agentv1.SetPluginApprovalRequest{Id: r.ID, Approved: true, ContentHash: r.Hash})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatal(err)
	}
	if _, err := s.ListPlugins(ctx, connect.NewRequest(&agentv1.ListPluginsRequest{})); err != nil {
		t.Fatal(err)
	}
}

func TestPluginStateStaysOffTheServer(t *testing.T) {
	doc := &settingsDoc{}
	h := startHarnessSrv(t, (&recording{}).respond, nil, nil, func(s *server.Server) { s.Preferences = doc })
	gittest.Isolate(t)
	store := bootstrap.PluginStore()
	if _, err := h.plugins().InstallPlugin(h.ctx, connect.NewRequest(&agentv1.InstallPluginRequest{Spec: writePlugin(t, t.TempDir())})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.plugins().SetPluginEnabled(h.ctx, connect.NewRequest(&agentv1.SetPluginEnabledRequest{Id: "kit@local"})); err != nil {
		t.Fatal(err)
	}
	if doc.content() != "" {
		t.Fatalf("the user's synced settings changed: %s", doc.content())
	}
	if !strings.HasPrefix(store.Root, os.Getenv("HARNESS_HOME")) {
		t.Fatal(store.Root)
	}
}

func TestSkillHooksRunWhileItIsAtWork(t *testing.T) {
	rec := &recording{}
	h := startPlugged(t, rec.respond, true)
	marks := t.TempDir()
	mark := filepath.Join(marks, "pre")
	writeFile(t, filepath.Join(h.proj, ".harness/skills/guarded/SKILL.md"), "---\ndescription: Guarded\nhooks:\n  Stop:\n    - hooks:\n        - type: command\n          command: echo ran >> '"+mark+"'\n---\nGo.")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "/guarded")
	h.until(ch, isTurnFinished)
	data, err := os.ReadFile(mark)
	if err != nil || strings.Count(string(data), "ran") != 1 {
		t.Fatalf("the skill's Stop hook: %q %v", data, err)
	}
	h.send(sid, "next")
	h.until(ch, isTurnFinished)
	data, _ = os.ReadFile(mark)
	if strings.Count(string(data), "ran") != 1 {
		t.Fatalf("it outlived the turn: %q", data)
	}
	if c := h.commands(&agentv1.ListSlashCommandsRequest{SessionId: sid})["guarded"]; slices.Contains(c.GetSkippedFields(), "hooks") {
		t.Fatal("hooks still reported as skipped")
	}
}

func TestAgentDefinitionHooksSeeOnlyThatAgent(t *testing.T) {
	marks := t.TempDir()
	mark := filepath.Join(marks, "stop")
	rec := &recording{next: func(n int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.RequestClass == "main" && n == 0 {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("a", "Agent", map[string]any{"description": "d", "prompt": "go", "subagent_type": "watched"}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
	}}
	h := startPlugged(t, rec.respond, true)
	writeFile(t, filepath.Join(h.proj, ".harness/agents/watched.md"), "---\nname: watched\ndescription: Watched\nhooks:\n  Stop:\n    - hooks:\n        - type: command\n          command: echo ran >> '"+mark+"'\n---\nWork.")
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "go")
	h.until(ch, isTurnFinished)
	deadline := time.Now().Add(15 * time.Second)
	for !fileExistsNow(mark) {
		if time.Now().After(deadline) {
			t.Fatal("the agent's Stop hook never ran as SubagentStop")
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.send(sid, "again")
	h.until(ch, isTurnFinished)
	data, _ := os.ReadFile(mark)
	if strings.Count(string(data), "ran") != 1 {
		t.Fatalf("it saw more than its agent: %q", data)
	}
}
