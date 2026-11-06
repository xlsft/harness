package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	"github.com/xlsft/harness/packages/client/internal/fakemcp"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/mcp"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/tools"
)

func TestMain(m *testing.M) {
	fakemcp.MaybeServe()
	os.Exit(m.Run())
}

func testBundle() *prompts.Bundle {
	return &prompts.Bundle{
		Version: "b1",
		Tools: map[string]prompts.ToolPrompt{
			mcp.ToolSearchName:    {Description: "Load deferred tools.", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"max_results":{"type":"integer"}},"required":["query"]}`)},
			mcp.AuthToolBundleKey: {Description: "Authorize the MCP server {{server}}.", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		},
		Reminders: map[string]string{mcp.ReminderDeferredTools: "deferred:\n{{tools}}"},
	}
}

func exe(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func stdioServer(t *testing.T, id, name string) *mcpv1.Server {
	return &mcpv1.Server{Id: id, Name: name, Scope: commonv1.Scope_SCOPE_USER, Transport: mcpv1.Transport_TRANSPORT_STDIO, Enabled: true,
		ConfigHash: "hash-" + id, Stdio: &mcpv1.StdioConfig{Command: exe(t)}}
}

func remoteServer(id, name string) *mcpv1.Server {
	return &mcpv1.Server{Id: id, Name: name, Scope: commonv1.Scope_SCOPE_USER, Transport: mcpv1.Transport_TRANSPORT_STREAMABLE_HTTP, Enabled: true,
		ConfigHash: "hash-" + id, Remote: &mcpv1.RemoteConfig{Url: "https://mcp.example/" + name},
		Auth: &mcpv1.AuthStatus{State: mcpv1.AuthState_AUTH_STATE_CONNECTED}}
}

func tool(name, desc string) *mcpv1.Tool {
	return &mcpv1.Tool{Name: name, Description: desc, InputSchemaJson: `{"type":"object","properties":{"q":{"type":"string"}}}`}
}

type harness struct {
	svc       *fakemcp.Service
	url       string
	approvals *mcp.Approvals
	cwd       string
}

func newHarness(t *testing.T) *harness {
	svc := fakemcp.NewService()
	dir := t.TempDir()
	return &harness{svc: svc, url: svc.Start(t), approvals: &mcp.Approvals{Path: filepath.Join(dir, "approvals.json")}, cwd: dir}
}

func (h *harness) open(t *testing.T, mod func(*mcp.Options)) *mcp.Manager {
	t.Helper()
	o := mcp.Options{Client: mcp.NewClient(h.url, nil, nil), Cwd: h.cwd, Approvals: h.approvals, Bundle: testBundle(), ToolSearch: "never", SessionID: "sess-1"}
	if mod != nil {
		mod(&o)
	}
	m, err := mcp.Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func findTool(t *testing.T, m *mcp.Manager, name string) tools.Tool {
	t.Helper()
	for _, tl := range m.Tools() {
		if tl.Name() == name {
			return tl
		}
	}
	t.Fatalf("tool %s not registered; have %v", name, toolNames(m))
	return nil
}

func toolNames(m *mcp.Manager) []string {
	var out []string
	for _, tl := range m.Tools() {
		out = append(out, tl.Name())
	}
	return out
}

func text(r tools.Result) string {
	var sb strings.Builder
	for _, b := range r.Content {
		sb.WriteString(b.Text)
	}
	return sb.String()
}

func run(t *testing.T, tl tools.Tool, env *tools.Env, input string) tools.Result {
	t.Helper()
	if env == nil {
		env = tools.NewEnv(t.TempDir(), t.TempDir())
		env.SessionID = "sess-1"
	}
	return tl.Run(context.Background(), env, tools.Call{ID: "toolu_1", Input: json.RawMessage(input)})
}

func TestToolNames(t *testing.T) {
	if got := mcp.ToolName("my server", "get.thing"); got != "mcp__my_server__get_thing" {
		t.Fatalf("ToolName = %q", got)
	}
	if got := mcp.ToolName("git-hub", "list_issues"); got != "mcp__git-hub__list_issues" {
		t.Fatalf("ToolName = %q", got)
	}
	if !mcp.IsToolName("mcp__a__b") || mcp.IsToolName("mcp__a") || mcp.IsToolName("Bash") {
		t.Fatal("IsToolName")
	}
}

func TestStdioAndRemoteTools(t *testing.T) {
	h := newHarness(t)
	h.svc.Servers = []*mcpv1.Server{remoteServer("mcp_r", "rem"), stdioServer(t, "mcp_s", "local")}
	h.svc.Catalog["mcp_r"] = []*mcpv1.Tool{tool("search_issues", "Search issues")}
	h.svc.Launch["mcp_s"] = fakemcp.SelfLaunch(map[string]string{"FAKE_MCP_SECRET": "s3"})
	m := h.open(t, func(o *mcp.Options) { o.AutoApprove = []string{"local"} })

	want := []string{"mcp__local__add", "mcp__local__echo", "mcp__local__fail", "mcp__rem__search_issues"}
	got := toolNames(m)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	if defs := m.ToolDefs(); len(defs) != 4 {
		t.Fatalf("ToolDefs = %d", len(defs))
	}
	reports, launches, _ := h.svc.Snapshot()
	if !slices.Equal(launches, []string{"mcp_s"}) {
		t.Fatalf("launches = %v", launches)
	}
	last := reports[len(reports)-1]
	if last.GetServerId() != "mcp_s" || last.GetApproval() != mcpv1.ApprovalState_APPROVAL_STATE_APPROVED || len(last.GetTools()) != 3 || last.GetConfigHash() != "hash-mcp_s" {
		t.Fatalf("last report = %v", last)
	}

	if r := run(t, findTool(t, m, "mcp__local__echo"), nil, `{"text":"hi","reveal":true}`); r.IsError || text(r) != "echo: hi secret=s3" {
		t.Fatalf("echo = %+v", r)
	}
	if r := run(t, findTool(t, m, "mcp__local__add"), nil, `{"a":2,"b":3}`); r.IsError || text(r) != "5" {
		t.Fatalf("add = %+v", r)
	}
	if r := run(t, findTool(t, m, "mcp__local__fail"), nil, `{}`); !r.IsError || !strings.Contains(text(r), "it failed") {
		t.Fatalf("fail = %+v", r)
	}
	r := run(t, findTool(t, m, "mcp__rem__search_issues"), nil, `{"q":"x"}`)
	if r.IsError || text(r) != `remote search_issues {"q":"x"}` {
		t.Fatalf("remote = %+v", r)
	}
	_, _, calls := h.svc.Snapshot()
	if len(calls) != 1 || calls[0].GetServerId() != "mcp_r" || calls[0].GetSessionId() != "sess-1" || calls[0].GetToolUseId() != "toolu_1" {
		t.Fatalf("proxy calls = %v", calls)
	}
}

type fakeHost struct {
	answer string
	asked  []tools.Question
}

func (h *fakeHost) PermissionMode() string           { return "default" }
func (h *fakeHost) SetPermissionMode(string) error   { return nil }
func (h *fakeHost) Remind(string, map[string]string) {}
func (h *fakeHost) Todos() []tools.Todo              { return nil }
func (h *fakeHost) SetTodos([]tools.Todo)            {}
func (h *fakeHost) Interactor() tools.Interactor     { return h }
func (h *fakeHost) ProposePlan(context.Context, string, string, string) (tools.PlanResponse, error) {
	return tools.PlanResponse{}, errors.New("no")
}
func (h *fakeHost) AskQuestions(_ context.Context, _ string, qs []tools.Question) (tools.QuestionResponse, error) {
	h.asked = append(h.asked, qs...)
	return tools.QuestionResponse{Outcome: tools.QuestionAnswered, Answers: []tools.QuestionAnswer{{Selected: []string{h.answer}}}}, nil
}

func TestStdioApprovalOnFirstUse(t *testing.T) {
	h := newHarness(t)
	h.svc.Servers = []*mcpv1.Server{stdioServer(t, "mcp_s", "local")}
	h.svc.Catalog["mcp_s"] = []*mcpv1.Tool{tool("echo", "Echo")} // reported by another device
	h.svc.Launch["mcp_s"] = fakemcp.SelfLaunch(nil)
	m := h.open(t, nil)

	reports, launches, _ := h.svc.Snapshot()
	if len(launches) != 0 || len(reports) != 1 || reports[0].GetApproval() != mcpv1.ApprovalState_APPROVAL_STATE_PENDING {
		t.Fatalf("expected a pending report and no launch; reports=%v launches=%v", reports, launches)
	}
	echo := findTool(t, m, "mcp__local__echo")

	// Nobody to ask: a clear error.
	r := run(t, echo, nil, `{"text":"x"}`)
	if !r.IsError || !strings.Contains(text(r), "HARNESS_MCP_APPROVE=local") {
		t.Fatalf("non-interactive = %+v", r)
	}

	env := tools.NewEnv(t.TempDir(), t.TempDir())
	host := &fakeHost{answer: "Allow"}
	env.SetHost(host)
	r = run(t, echo, env, `{"text":"x"}`)
	if r.IsError || text(r) != "echo: x" {
		t.Fatalf("approved call = %+v", r)
	}
	if len(host.asked) != 1 || !strings.Contains(host.asked[0].Question, "local") {
		t.Fatalf("asked = %+v", host.asked)
	}
	if st := h.approvals.State("mcp_s", "hash-mcp_s"); st != mcpv1.ApprovalState_APPROVAL_STATE_APPROVED {
		t.Fatalf("stored approval = %v", st)
	}
	if st := h.approvals.State("mcp_s", "other-hash"); st != mcpv1.ApprovalState_APPROVAL_STATE_PENDING {
		t.Fatalf("approval must be bound to the config hash, got %v", st)
	}
	if st := m.Reported("mcp_s"); st.GetApproval() != mcpv1.ApprovalState_APPROVAL_STATE_APPROVED || len(st.GetTools()) == 0 {
		t.Fatalf("reported = %v", st)
	}
	m.Close()

	// The next session starts it right away.
	h.open(t, nil)
	if _, launches, _ := h.svc.Snapshot(); len(launches) != 2 {
		t.Fatalf("launches after reopen = %v", launches)
	}
}

func TestStdioDenied(t *testing.T) {
	h := newHarness(t)
	h.svc.Servers = []*mcpv1.Server{stdioServer(t, "mcp_s", "local")}
	h.svc.Catalog["mcp_s"] = []*mcpv1.Tool{tool("echo", "Echo")}
	h.svc.Launch["mcp_s"] = fakemcp.SelfLaunch(nil)
	m := h.open(t, nil)
	env := tools.NewEnv(t.TempDir(), t.TempDir())
	env.SetHost(&fakeHost{answer: "Deny"})
	if r := run(t, findTool(t, m, "mcp__local__echo"), env, `{"text":"x"}`); !r.IsError || !strings.Contains(text(r), "did not allow") {
		t.Fatalf("denied = %+v", r)
	}
	if st := h.approvals.State("mcp_s", "hash-mcp_s"); st != mcpv1.ApprovalState_APPROVAL_STATE_DENIED {
		t.Fatalf("stored = %v", st)
	}
	// A denied server exposes no tools next time.
	m2 := h.open(t, nil)
	if n := len(m2.Tools()); n != 0 {
		t.Fatalf("tools after deny = %v", toolNames(m2))
	}
}

func TestStdioPendingWithoutToolsAndUnavailable(t *testing.T) {
	h := newHarness(t)
	missing := stdioServer(t, "mcp_x", "missing")
	missing.Stdio.Command = "definitely-not-a-command-harness"
	h.svc.Servers = []*mcpv1.Server{stdioServer(t, "mcp_s", "fresh"), missing}
	m := h.open(t, nil)
	if len(m.Tools()) != 0 {
		t.Fatalf("tools = %v", toolNames(m))
	}
	w := strings.Join(m.Warnings(), "\n")
	if !strings.Contains(w, "fresh needs approval") || !strings.Contains(w, "missing is unavailable") {
		t.Fatalf("warnings = %q", w)
	}
	reports, _, _ := h.svc.Snapshot()
	var found bool
	for _, r := range reports {
		if r.GetServerId() == "mcp_x" {
			found = r.GetAvailability() == mcpv1.Availability_AVAILABILITY_UNAVAILABLE_ON_DEVICE && strings.Contains(r.GetReason(), "command not found")
		}
	}
	if !found {
		t.Fatalf("no unavailable report: %v", reports)
	}
}

func TestNeedsAuth(t *testing.T) {
	h := newHarness(t)
	slack := remoteServer("mcp_sl", "slack")
	slack.Auth.State = mcpv1.AuthState_AUTH_STATE_NEEDS_AUTH
	h.svc.Servers = []*mcpv1.Server{remoteServer("mcp_gh", "gh"), slack}
	h.svc.Catalog["mcp_gh"] = []*mcpv1.Tool{tool("create_issue", "Create an issue")}
	h.svc.Call = func(*mcpv1.CallToolRequest) (string, error) { return "", fakemcp.NeedsAuthError() }
	m := h.open(t, nil)

	r := run(t, findTool(t, m, "mcp__gh__create_issue"), nil, `{}`)
	if !r.IsError || !strings.Contains(text(r), "MCP server gh needs authorization") || !strings.Contains(text(r), "Ask the user to authorize it") {
		t.Fatalf("needs auth = %+v", r)
	}

	auth := findTool(t, m, "mcp__slack__authenticate")
	if d := m.ToolDefs(); !slices.ContainsFunc(d, func(d llm.ToolDef) bool {
		return d.Name == "mcp__slack__authenticate" && d.Description == "Authorize the MCP server slack."
	}) {
		t.Fatalf("authenticate def missing: %+v", d)
	}
	r = run(t, auth, nil, `{}`)
	if r.IsError || !strings.Contains(text(r), "https://auth.example/authorize?server=mcp_sl") {
		t.Fatalf("authenticate = %+v", r)
	}
	if w := strings.Join(m.Warnings(), "\n"); !strings.Contains(w, "slack needs authorization") {
		t.Fatalf("warnings = %q", w)
	}
}

func TestRequiresUserInteraction(t *testing.T) {
	h := newHarness(t)
	cfg := remoteServer("mcp_a", "bank")
	cfg.Remote.RequiresUserInteraction = true
	h.svc.Servers = []*mcpv1.Server{cfg, remoteServer("mcp_b", "plain"), remoteServer("mcp_c", "annotated")}
	h.svc.Catalog["mcp_a"] = []*mcpv1.Tool{tool("pay", "Pay")}
	h.svc.Catalog["mcp_b"] = []*mcpv1.Tool{tool("read", "Read")}
	h.svc.Catalog["mcp_c"] = []*mcpv1.Tool{{Name: "go", AnnotationsJson: `{"requiresUserInteraction":true}`}}
	m := h.open(t, nil)
	ask := func(n string) bool { return findTool(t, m, n).(tools.AlwaysAsker).AlwaysAsk() }
	if !ask("mcp__bank__pay") || ask("mcp__plain__read") || !ask("mcp__annotated__go") {
		t.Fatal("AlwaysAsk does not follow requires_user_interaction")
	}
	if findTool(t, m, "mcp__plain__read").ReadOnly(nil) {
		t.Fatal("MCP tools must not be read-only")
	}
}

func deferHarness(t *testing.T) *harness {
	h := newHarness(t)
	always := remoteServer("mcp_al", "core")
	always.AlwaysLoad = true
	h.svc.Servers = []*mcpv1.Server{remoteServer("mcp_gh", "gh"), remoteServer("mcp_sl", "slack"), always}
	h.svc.Catalog["mcp_gh"] = []*mcpv1.Tool{tool("create_issue", "Create a GitHub issue"), tool("list_issues", "List issues of a repository"), tool("get_file", "Read a file from a repository")}
	h.svc.Catalog["mcp_sl"] = []*mcpv1.Tool{tool("send_message", "Send a Slack message to a channel")}
	h.svc.Catalog["mcp_al"] = []*mcpv1.Tool{tool("ping", "Ping")}
	return h
}

func TestToolSearchAndDeferredTools(t *testing.T) {
	h := deferHarness(t)
	m := h.open(t, func(o *mcp.Options) { o.ToolSearch = "always" })

	if got := m.Deferred(); !slices.Equal(got, []string{"mcp__gh__create_issue", "mcp__gh__get_file", "mcp__gh__list_issues", "mcp__slack__send_message"}) {
		t.Fatalf("deferred = %v", got)
	}
	if v := m.DeferredReminder(); !strings.Contains(v["tools"], "mcp__slack__send_message\n") && !strings.HasSuffix(v["tools"], "mcp__slack__send_message") {
		t.Fatalf("reminder vars = %v", v)
	}
	defNames := func() []string {
		var out []string
		for _, d := range m.ToolDefs() {
			out = append(out, d.Name)
		}
		return out
	}
	if got := defNames(); !slices.Equal(got, []string{"mcp__core__ping"}) {
		t.Fatalf("initial defs = %v (always_load only)", got)
	}
	search := findTool(t, m, mcp.ToolSearchName)
	if !search.ReadOnly(nil) {
		t.Fatal("ToolSearch must be read-only")
	}

	create := findTool(t, m, "mcp__gh__create_issue")
	if r := run(t, create, nil, `{}`); !r.IsError || !strings.Contains(text(r), `select:mcp__gh__create_issue`) {
		t.Fatalf("unloaded call = %+v", r)
	}

	r := run(t, search, nil, `{"query":"select:mcp__gh__create_issue,list_issues"}`)
	if r.IsError || !strings.Contains(text(r), "<functions>") || !strings.Contains(text(r), `"name":"mcp__gh__create_issue"`) || !strings.Contains(text(r), `"name":"mcp__gh__list_issues"`) {
		t.Fatalf("select = %q", text(r))
	}
	if !strings.Contains(text(r), `"parameters":{"type":"object"`) {
		t.Fatalf("select result lacks the schema: %q", text(r))
	}
	if got := defNames(); !slices.Equal(got, []string{"mcp__core__ping", "mcp__gh__create_issue", "mcp__gh__list_issues"}) {
		t.Fatalf("defs after select = %v", got)
	}
	if r := run(t, create, nil, `{"q":"x"}`); r.IsError {
		t.Fatalf("loaded call = %+v", r)
	}
	if !slices.Contains(m.Names(), mcp.ToolSearchName) {
		t.Fatalf("Names = %v", m.Names())
	}

	// Keyword search.
	r = run(t, search, nil, `{"query":"slack message"}`)
	if !strings.Contains(text(r), "mcp__slack__send_message") || strings.Contains(text(r), "mcp__gh__get_file") {
		t.Fatalf("keyword = %q", text(r))
	}
	r = run(t, search, nil, `{"query":"+gh file"}`)
	if st, _ := r.Structured.(map[string]any); !slices.Equal(st["matches"].([]string), []string{"mcp__gh__get_file"}) {
		t.Fatalf("+gh file matches = %v", r.Structured)
	}
	r = run(t, search, nil, `{"query":"issue", "max_results": 1}`)
	if st, _ := r.Structured.(map[string]any); len(st["matches"].([]string)) != 1 {
		t.Fatalf("max_results ignored: %v", r.Structured)
	}
	r = run(t, search, nil, `{"query":"zebra"}`)
	if !strings.Contains(text(r), "No deferred tools match") {
		t.Fatalf("no match = %q", text(r))
	}
	if r := run(t, search, nil, `{}`); !r.IsError {
		t.Fatal("empty query must fail")
	}
}

func TestAutoDeferralThreshold(t *testing.T) {
	h := deferHarness(t)
	small := h.open(t, func(o *mcp.Options) { o.ToolSearch = ""; o.ContextWindow = 500 })
	if len(small.Deferred()) != 4 {
		t.Fatalf("small window: deferred = %v", small.Deferred())
	}
	big := h.open(t, func(o *mcp.Options) { o.ToolSearch = "auto"; o.ContextWindow = 1_000_000 })
	if len(big.Deferred()) != 0 || slices.Contains(toolNames(big), mcp.ToolSearchName) {
		t.Fatalf("big window: deferred = %v", big.Deferred())
	}
	never := h.open(t, func(o *mcp.Options) { o.ToolSearch = "never"; o.ContextWindow = 500 })
	if len(never.Deferred()) != 0 {
		t.Fatal("never mode deferred tools")
	}
	noBundle := h.open(t, func(o *mcp.Options) {
		o.ToolSearch = "always"
		o.Bundle = &prompts.Bundle{Tools: map[string]prompts.ToolPrompt{}}
	})
	if len(noBundle.Deferred()) != 0 {
		t.Fatal("deferred without a ToolSearch bundle entry")
	}
	disabled := h.open(t, func(o *mcp.Options) {
		o.ToolSearch = "always"
		o.Disabled = func(n string) bool { return strings.HasPrefix(n, "mcp__slack__") }
	})
	if slices.Contains(disabled.Deferred(), "mcp__slack__send_message") {
		t.Fatal("disabled tools must not be listed")
	}
}

func TestRestoreLoaded(t *testing.T) {
	h := deferHarness(t)
	m := h.open(t, func(o *mcp.Options) { o.ToolSearch = "always" })
	m.RestoreLoaded([]llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: "t1", Name: mcp.ToolSearchName, Input: json.RawMessage(`{"query":"select:mcp__slack__send_message"}`)},
			{Type: llm.BlockToolUse, ID: "t2", Name: "mcp__gh__get_file", Input: json.RawMessage(`{}`)},
		}},
	})
	var got []string
	for _, d := range m.ToolDefs() {
		got = append(got, d.Name)
	}
	if !slices.Equal(got, []string{"mcp__core__ping", "mcp__slack__send_message", "mcp__gh__get_file"}) {
		t.Fatalf("restored defs = %v", got)
	}
}

func TestCatalogSnapshot(t *testing.T) {
	h := deferHarness(t)
	m := h.open(t, func(o *mcp.Options) { o.CatalogVersion = "cat_expired" })
	if m.CatalogVersion() != "cat_1" {
		t.Fatalf("catalog = %q", m.CatalogVersion())
	}
	h.svc.Catalogs = nil
	h.open(t, func(o *mcp.Options) { o.CatalogVersion = "cat_1"; o.ProjectKey = "github.com/a/b" })
	if c := h.svc.Catalogs; len(c) != 1 || c[0].GetVersion() != "cat_1" || c[0].GetProjectKey() != "github.com/a/b" {
		t.Fatalf("catalog requests = %v", c)
	}
}

func TestListWithoutProjectKeyExcludesProjects(t *testing.T) {
	h := newHarness(t)
	h.open(t, nil)
	l := h.svc.Lists[0]
	if !slices.Equal(l.GetScopes(), []commonv1.Scope{commonv1.Scope_SCOPE_USER, commonv1.Scope_SCOPE_ORG}) || !l.GetIncludeDisabled() {
		t.Fatalf("list request = %v", l)
	}
}

func TestProjectConfigRegistration(t *testing.T) {
	h := newHarness(t)
	existing := stdioServer(t, "mcp_p", "lint")
	existing.Scope, existing.ProjectKey, existing.Version = commonv1.Scope_SCOPE_PROJECT, "github.com/a/b", 7
	existing.Stdio.Args = []string{"--old"}
	existing.AlwaysLoad = true
	h.svc.Servers = []*mcpv1.Server{existing}
	cfg := filepath.Join(h.cwd, ".mcp.json")
	os.WriteFile(cfg, []byte(`{"mcpServers":{
		"lint":{"description":"test","command":"`+exe(t)+`","args":["--new"]},
		"docs":{"type":"http","url":"https://docs.example/mcp","headers":{"X-Key":"${secret:KEY}"}},
		"weird":{"type":"carrier-pigeon"}
	}}`), 0o644)
	h.open(t, func(o *mcp.Options) { o.ProjectKey = "github.com/a/b"; o.ProjectConfig = cfg })

	if len(h.svc.Created) != 1 || h.svc.Created[0].GetName() != "docs" || h.svc.Created[0].GetScope() != commonv1.Scope_SCOPE_PROJECT ||
		h.svc.Created[0].GetProjectKey() != "github.com/a/b" || h.svc.Created[0].GetTransport() != mcpv1.Transport_TRANSPORT_STREAMABLE_HTTP {
		t.Fatalf("created = %v", h.svc.Created)
	}
	if len(h.svc.Updated) != 1 || !slices.Equal(h.svc.Updated[0].GetStdio().GetArgs(), []string{"--new"}) || !h.svc.Updated[0].GetAlwaysLoad() {
		t.Fatalf("updated = %v", h.svc.Updated)
	}
	if len(h.svc.Lists) != 2 {
		t.Fatalf("expected a re-list after changes, got %d lists", len(h.svc.Lists))
	}
}

func TestProjectConfigObeysManagedPolicy(t *testing.T) {
	h := newHarness(t)
	cfg := filepath.Join(h.cwd, ".mcp.json")
	os.WriteFile(cfg, []byte(`{"mcpServers":{
		"allowed":{"type":"http","url":"https://a.example/mcp"},
		"other":{"type":"http","url":"https://b.example/mcp"}
	}}`), 0o644)
	h.open(t, func(o *mcp.Options) {
		o.ProjectKey, o.ProjectConfig = "github.com/a/b", cfg
		o.PolicyAllows = func(name string) bool { return name == "allowed" }
	})
	if len(h.svc.Created) != 1 || h.svc.Created[0].GetName() != "allowed" {
		t.Fatalf("created = %v", h.svc.Created)
	}
}

func TestResultFromJSON(t *testing.T) {
	r := mcp.ResultFromJSON([]byte(`{"content":[{"type":"text","text":"a"},{"type":"image","data":"aGk=","mimeType":"image/png"},{"type":"resource","resource":{"uri":"file:///x","text":"body"}},{"type":"audio","data":"x","mimeType":"audio/wav"}]}`))
	if r.IsError || len(r.Content) != 4 || r.Content[1].Type != llm.BlockImage || r.Content[1].Source.MediaType != "image/png" || r.Content[2].Text != "body" || !strings.Contains(r.Content[3].Text, "audio omitted") {
		t.Fatalf("mixed = %+v", r)
	}
	r = mcp.ResultFromJSON([]byte(`{"content":[],"structuredContent":{"sum":5}}`))
	if text(r) != `{"sum":5}` {
		t.Fatalf("structured = %q", text(r))
	}
	r = mcp.ResultFromJSON([]byte(`{"content":[{"type":"text","text":"bad"}],"isError":true}`))
	if !r.IsError || text(r) != "bad" {
		t.Fatalf("error = %+v", r)
	}
	big := strings.Repeat("x", mcp.MaxOutputChars+10)
	b, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": big}}})
	if r := mcp.ResultFromJSON(b); len(text(r)) > mcp.MaxOutputChars+200 || !strings.Contains(text(r), "truncated") {
		t.Fatalf("truncation: %d chars", len(text(r)))
	}
	if r := mcp.ResultFromJSON([]byte(`not json`)); !r.IsError {
		t.Fatal("invalid JSON must be an error")
	}
}
