package server_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/acp/registry"
	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/server"
)

// acpModels is a client with the fake agent in its registry (not installed yet); the agent's models, failures and
// what it was asked are in files of the test.
type acpModels struct {
	t      *testing.T
	ctx    context.Context
	home   string
	client agentv1connect.AgentServiceClient
	srv    *server.Server
	models string
	effort string
	fail   string
	log    string
	// version is what the registry offers.
	version atomic.Value
}

func newAcpModels(t *testing.T) *acpModels {
	t.Helper()
	bin := fakeAgent(t)
	home := settledTempDir(t)
	dir := settledTempDir(t)
	t.Setenv("HARNESS_HOME", home)
	t.Setenv("HARNESS_KEYRING", "0")
	a := &acpModels{t: t, ctx: context.Background(), home: home, models: filepath.Join(dir, "models"), effort: filepath.Join(dir, "efforts"), fail: filepath.Join(dir, "fail"), log: filepath.Join(dir, "log")}
	a.version.Store("1.0.0")
	t.Setenv("FAKE_MODELS_FILE", a.models)
	t.Setenv("FAKE_EFFORTS_FILE", a.effort)
	t.Setenv("FAKE_FAIL_FILE", a.fail)
	t.Setenv("FAKE_LOG", a.log)
	body, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	var reg *httptest.Server
	reg = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/registry.json":
			fmt.Fprintf(w, `{"version":"1","agents":[{"id":"fake","name":"Fake Agent","version":%q,"description":"test",
			  "distribution":{"binary":{%q:{"archive":"%s/fakeagent","cmd":"./fakeagent"}}}}]}`, a.version.Load(), registry.Platform(), reg.URL)
		case "/fakeagent":
			w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(reg.Close)
	t.Setenv("HARNESS_TMPDIR", settledTempDir(t))
	t.Setenv("HARNESS_NO_SHELL_SNAPSHOT", "1")
	_, gw := fg.Start(t, fg.Sequence())
	tok := auth.StaticToken("hat_test")
	a.srv = server.New(bootstrap.Options{Gateway: connectgw.New(gw, nil, tok), Prompts: prompts.NewConnectSource(gw, nil, tok)})
	a.srv.SetAcpRegistry(&registry.Manager{Dir: filepath.Join(home, "acp"), URL: reg.URL + "/registry.json"})
	t.Cleanup(a.srv.Close)
	path, h := a.srv.Handler()
	mux := http.NewServeMux()
	mux.Handle(path, h)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	a.client = agentv1connect.NewAgentServiceClient(hs.Client(), hs.URL)
	return a
}

func (a *acpModels) setModels(lines ...string) {
	a.t.Helper()
	if err := os.WriteFile(a.models, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		a.t.Fatal(err)
	}
}

func (a *acpModels) install() *agentv1.AcpAgent {
	a.t.Helper()
	inst, err := a.client.InstallAcpAgent(a.ctx, connect.NewRequest(&agentv1.InstallAcpAgentRequest{AgentId: "fake"}))
	if err != nil {
		a.t.Fatal(err)
	}
	var last *agentv1.InstallAcpAgentResponse
	for inst.Receive() {
		last = inst.Msg()
	}
	if inst.Err() != nil || last == nil || !last.Done {
		a.t.Fatalf("install: %v %v", inst.Err(), last)
	}
	return last.Agent
}

func (a *acpModels) list(refresh bool) *agentv1.AcpAgent {
	a.t.Helper()
	res, err := a.client.ListAcpAgents(a.ctx, connect.NewRequest(&agentv1.ListAcpAgentsRequest{Refresh: refresh, CachedOnly: !refresh}))
	if err != nil {
		a.t.Fatal(err)
	}
	for _, x := range res.Msg.Agents {
		if x.Id == "fake" {
			return x
		}
	}
	a.t.Fatalf("no agent: %v", res.Msg)
	return nil
}

// settled lists until no probe is running, and returns the agent.
func (a *acpModels) settled() *agentv1.AcpAgent {
	a.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if x := a.list(false); x.ModelsStatus != agentv1.AcpModelsStatus_ACP_MODELS_STATUS_PROBING {
			return x
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.t.Fatal("the probe did not finish")
	return nil
}

func (a *acpModels) logged() []string {
	b, err := os.ReadFile(a.log)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (a *acpModels) count(prefix string) int {
	n := 0
	for _, l := range a.logged() {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// waitFor polls until cond holds.
func (a *acpModels) waitFor(what string, cond func() bool) {
	a.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.t.Fatalf("timed out waiting for %s (agent log: %v)", what, a.logged())
}

func modelIDs(ms []*agentv1.AcpModel) []string {
	out := []string{}
	for _, m := range ms {
		out = append(out, m.Id)
	}
	return out
}

func TestAcpProbeFindsModelsOnInstallWithoutAPrompt(t *testing.T) {
	a := newAcpModels(t)
	a.setModels("fast=Fast", "deep=Deep")
	if x := a.list(true); x.Selectable || x.ModelsStatus != agentv1.AcpModelsStatus_ACP_MODELS_STATUS_NOT_PROBED {
		t.Fatalf("not installed: %v", x)
	}
	if a.count("spawn") != 0 {
		t.Fatalf("an agent that is not installed was started: %v", a.logged())
	}
	if x := a.install(); x.ModelsStatus != agentv1.AcpModelsStatus_ACP_MODELS_STATUS_PROBING && x.ModelsStatus != agentv1.AcpModelsStatus_ACP_MODELS_STATUS_OK {
		t.Fatalf("after install: %v", x.ModelsStatus)
	}
	x := a.settled()
	if got := modelIDs(x.Models); !slices.Equal(got, []string{"fast", "deep"}) || x.CurrentModelId != "fast" {
		t.Fatalf("models: %v current %q", got, x.CurrentModelId)
	}
	if x.ModelsStatus != agentv1.AcpModelsStatus_ACP_MODELS_STATUS_OK || x.ModelsUpdatedAt == "" || !x.Selectable {
		t.Fatalf("agent: %v", x)
	}
	if x.CurrentModeId != "default" || len(x.Modes) != 3 {
		t.Fatalf("modes: %v %q", x.Modes, x.CurrentModeId)
	}
	if !slices.Contains(x.UnsupportedFeatures, agentv1.SessionFeature_SESSION_FEATURE_WORKTREE) {
		t.Fatalf("features: %v", x.UnsupportedFeatures)
	}
	// Closed, in a temp folder, with no prompt.
	if a.count("spawn") != 1 || a.count("session/close") != 1 || a.count("prompt") != 0 {
		t.Fatalf("log: %v", a.logged())
	}
	for _, l := range a.logged() {
		if cwd, ok := strings.CutPrefix(l, "session/new "); ok {
			if !strings.Contains(filepath.Base(cwd), "harness-acp-probe-") {
				t.Fatalf("probe folder: %s", cwd)
			}
			if _, err := os.Stat(cwd); !os.IsNotExist(err) {
				t.Fatalf("probe folder kept: %v", err)
			}
		}
	}
	// Cached in the device-local state; a listing does not start the agent again.
	b, err := os.ReadFile(filepath.Join(a.home, "acp", "state.json"))
	if err != nil || !strings.Contains(string(b), `"fast"`) {
		t.Fatalf("state: %v %s", err, b)
	}
	a.list(false)
	if a.count("spawn") != 1 {
		t.Fatalf("listing probed again: %v", a.logged())
	}
}

func TestAcpAgentWithoutModelOptionsHasDefault(t *testing.T) {
	a := newAcpModels(t)
	a.install()
	x := a.settled()
	if len(x.Models) != 1 || x.Models[0].Id != "default" || x.Models[0].Name != "Default" {
		t.Fatalf("models: %v", x.Models)
	}
	// The session starts on it, and takes it.
	cs, err := a.client.CreateSession(a.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: t.TempDir(), Engine: "acp:fake", Model: "default"}))
	if err != nil {
		t.Fatal(err)
	}
	sid := cs.Msg.Session.Id
	a.waitFor("the session to open", func() bool { return !a.sessionState(sid).GetStarting() })
	st := a.sessionState(sid)
	if !slices.Equal(modelIDs(st.Models), []string{"default"}) || st.CurrentModelId != "default" {
		t.Fatalf("session models: %v %q", st.Models, st.CurrentModelId)
	}
	if _, err := a.client.SetModel(a.ctx, connect.NewRequest(&agentv1.SetModelRequest{SessionId: sid, Model: "default"})); err != nil {
		t.Fatal(err)
	}
}

func TestAcpProbeRefreshesAndFailureKeepsTheList(t *testing.T) {
	a := newAcpModels(t)
	a.setModels("fast=Fast")
	a.install()
	a.settled()

	// A manual refresh sees new models.
	a.setModels("fast=Fast", "deep=Deep")
	a.list(true)
	x := a.settled()
	if !slices.Equal(modelIDs(x.Models), []string{"fast", "deep"}) {
		t.Fatalf("after refresh: %v", x.Models)
	}

	// A failed probe keeps them and says why; the agent is not started again until something changes.
	if err := os.WriteFile(a.fail, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a.setModels("other=Other")
	a.list(true)
	x = a.settled()
	if !slices.Equal(modelIDs(x.Models), []string{"fast", "deep"}) || x.ModelsStatus != agentv1.AcpModelsStatus_ACP_MODELS_STATUS_FAILED || x.ModelsError == "" {
		t.Fatalf("failure: %v %v %q", x.Models, x.ModelsStatus, x.ModelsError)
	}
	spawns := a.count("spawn")
	a.list(false)
	time.Sleep(50 * time.Millisecond)
	if a.count("spawn") != spawns {
		t.Fatalf("a listing retried a failed probe: %v", a.logged())
	}

	// Refreshing after the agent recovers clears it.
	os.Remove(a.fail)
	a.list(true)
	x = a.settled()
	if !slices.Equal(modelIDs(x.Models), []string{"other"}) || x.ModelsStatus != agentv1.AcpModelsStatus_ACP_MODELS_STATUS_OK || x.ModelsError != "" {
		t.Fatalf("recovered: %v %v %q", x.Models, x.ModelsStatus, x.ModelsError)
	}
}

func TestAcpUpdateProbesTheNewVersion(t *testing.T) {
	a := newAcpModels(t)
	a.setModels("fast=Fast")
	a.install()
	a.settled()
	a.setModels("next=Next")
	a.version.Store("2.0.0")
	up, err := a.client.UpdateAcpAgent(a.ctx, connect.NewRequest(&agentv1.UpdateAcpAgentRequest{AgentId: "fake"}))
	if err != nil {
		t.Fatal(err)
	}
	for up.Receive() {
	}
	if up.Err() != nil {
		t.Fatal(up.Err())
	}
	x := a.settled()
	if x.InstalledVersion != "2.0.0" || !slices.Equal(modelIDs(x.Models), []string{"next"}) {
		t.Fatalf("after update: %v %v", x.InstalledVersion, x.Models)
	}
}

func TestAcpUninstallForgetsTheModels(t *testing.T) {
	a := newAcpModels(t)
	a.setModels("fast=Fast")
	a.install()
	a.settled()
	if _, err := a.client.UninstallAcpAgent(a.ctx, connect.NewRequest(&agentv1.UninstallAcpAgentRequest{AgentId: "fake"})); err != nil {
		t.Fatal(err)
	}
	if x := a.list(false); len(x.Models) != 0 || x.Selectable || x.ModelsStatus != agentv1.AcpModelsStatus_ACP_MODELS_STATUS_NOT_PROBED {
		t.Fatalf("uninstalled: %v", x)
	}
}

func TestAcpProbeStopsAtSignInAndRunsAfterIt(t *testing.T) {
	t.Setenv("FAKE_AUTH", "1")
	a := newAcpModels(t)
	login := filepath.Join(t.TempDir(), "logged-in")
	t.Setenv("FAKE_AUTH_FILE", login)
	a.setModels("fast=Fast")
	a.install()
	x := a.settled()
	if x.Selectable || len(x.Models) != 0 || x.ModelsStatus != agentv1.AcpModelsStatus_ACP_MODELS_STATUS_NEEDS_AUTH || x.AuthState != agentv1.AcpAuthState_ACP_AUTH_STATE_REQUIRED {
		t.Fatalf("sign-in needed: %v", x)
	}
	// While it asks, a listing does not start it again.
	spawns := a.count("spawn")
	a.list(false)
	time.Sleep(50 * time.Millisecond)
	if a.count("spawn") != spawns {
		t.Fatalf("listing probed an agent that waits for a sign-in: %v", a.logged())
	}
	// The terminal login finished: the check starts a probe.
	if err := os.WriteFile(login, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := a.client.AuthenticateAcpAgent(a.ctx, connect.NewRequest(&agentv1.AuthenticateAcpAgentRequest{AgentId: "fake", Verify: true}))
	if err != nil || !res.Msg.Authenticated {
		t.Fatalf("verify: %v %v", err, res)
	}
	x = a.settled()
	if !x.Selectable || !slices.Equal(modelIDs(x.Models), []string{"fast"}) || x.ModelsStatus != agentv1.AcpModelsStatus_ACP_MODELS_STATUS_OK {
		t.Fatalf("after sign-in: %v", x)
	}
}

func (a *acpModels) sessionState(id string) *agentv1.AcpSessionState {
	a.t.Helper()
	ls, err := a.client.ListSessions(a.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{}))
	if err != nil {
		a.t.Fatal(err)
	}
	for _, s := range ls.Msg.Sessions {
		if s.Id == id {
			return s.Acp
		}
	}
	a.t.Fatalf("session %s not listed", id)
	return nil
}

func TestAcpSessionStartsWithTheChosenModelAndMode(t *testing.T) {
	a := newAcpModels(t)
	a.setModels("fast=Fast", "deep=Deep")
	a.install()
	a.settled()
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	cs, err := a.client.CreateSession(a.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: proj, Engine: "acp:fake", Model: "deep", AcpModeId: "plan"}))
	if err != nil {
		t.Fatal(err)
	}
	info := cs.Msg.Session
	// The choice shows from the first moment, and the list is the cached one until the agent answers.
	if info.Engine != "acp:fake" || info.Acp.CurrentModelId != "deep" || !slices.Equal(modelIDs(info.Acp.Models), []string{"fast", "deep"}) {
		t.Fatalf("session: %v", info)
	}
	sid := info.Id
	// A message sent right away reaches the agent on the chosen model.
	if _, err := a.client.SendUserMessage(a.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "u1", Content: text("hello")})); err != nil {
		t.Fatal(err)
	}
	a.waitFor("the prompt", func() bool { return a.count("prompt") > 0 })
	if !slices.Contains(a.logged(), "prompt model=deep") || !slices.Contains(a.logged(), "set_mode plan") {
		t.Fatalf("agent log: %v", a.logged())
	}
	st := a.sessionState(sid)
	if st.CurrentModelId != "deep" || st.CurrentModeId != "plan" {
		t.Fatalf("state: %v", st)
	}
	// Recorded: a resume goes back to them.
	if _, err := a.client.CloseSession(a.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	if _, err := a.client.CreateSession(a.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid})); err != nil {
		t.Fatal(err)
	}
	a.waitFor("the second set_model", func() bool { return a.count("set_model deep") == 2 })
	// The model a server catalog would know is not sent to the agent.
	cs, err = a.client.CreateSession(a.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: proj, Engine: "acp:fake", Model: "gone"}))
	if err != nil {
		t.Fatal(err)
	}
	a.waitFor("the session on its default", func() bool {
		st := a.sessionState(cs.Msg.Session.Id)
		return !st.Starting && st.CurrentModelId == "fast"
	})
	if a.count("set_model gone") != 0 {
		t.Fatalf("agent log: %v", a.logged())
	}
}

func TestAcpSessionReportingOtherModelsUpdatesTheCache(t *testing.T) {
	a := newAcpModels(t)
	a.setModels("fast=Fast")
	a.install()
	a.settled()
	spawns := a.count("spawn")
	cs, err := a.client.CreateSession(a.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: t.TempDir(), Engine: "acp:fake"}))
	if err != nil {
		t.Fatal(err)
	}
	sid := cs.Msg.Session.Id
	a.waitFor("the session to open", func() bool { return !a.sessionState(sid).GetStarting() })
	// The agent's list changes under a running session.
	a.setModels("fast=Fast", "new=New")
	if _, err := a.client.SendUserMessage(a.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "u1", Content: text("newmodels")})); err != nil {
		t.Fatal(err)
	}
	a.waitFor("the cache to change", func() bool { return slices.Equal(modelIDs(a.list(false).Models), []string{"fast", "new"}) })
	if !slices.Equal(modelIDs(a.sessionState(sid).Models), []string{"fast", "new"}) {
		t.Fatalf("session: %v", a.sessionState(sid).Models)
	}
	// One process for the session, none for the update.
	if got := a.count("spawn"); got != spawns+1 {
		t.Fatalf("spawns: %d, want %d: %v", got, spawns+1, a.logged())
	}
}

func TestAcpSessionsDoNotCrossEngines(t *testing.T) {
	a := newAcpModels(t)
	a.setModels("fast=Fast", "deep=Deep")
	a.install()
	a.settled()
	cs, err := a.client.CreateSession(a.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: t.TempDir(), Engine: "acp:fake"}))
	if err != nil {
		t.Fatal(err)
	}
	sid := cs.Msg.Session.Id
	a.waitFor("the session to open", func() bool { return !a.sessionState(sid).GetStarting() })
	set := func(model, engine string) error {
		_, err := a.client.SetModel(a.ctx, connect.NewRequest(&agentv1.SetModelRequest{SessionId: sid, Model: model, Engine: engine}))
		return err
	}
	for _, c := range []struct{ model, engine, want string }{
		{"server-model", "", "not one of its models"},
		{"fast", "harness", "the server's model"},
		{"fast", "acp:other", "another agent"},
	} {
		err := set(c.model, c.engine)
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "start a new session") {
			t.Fatalf("%s on %q: %v", c.model, c.engine, err)
		}
	}
	if a.count("set_model") != 0 {
		t.Fatalf("a refused model reached the agent: %v", a.logged())
	}
	// Its own model passes, with or without the engine.
	if err := set("deep", "acp:fake"); err != nil {
		t.Fatal(err)
	}
	a.waitFor("the switch", func() bool { return a.sessionState(sid).CurrentModelId == "deep" })
	if err := set("fast", ""); err != nil {
		t.Fatal(err)
	}
}

func TestHarnessSessionCannotPickAnAgentsModel(t *testing.T) {
	a := newAcpModels(t)
	a.setModels("agent-only=Agent only")
	a.install()
	a.settled()
	res, err := a.client.ListAcpAgents(a.ctx, connect.NewRequest(&agentv1.ListAcpAgentsRequest{CachedOnly: true}))
	if err != nil || res.Msg.SessionSwitchRefusal != "Start a new session to use this agent" {
		t.Fatalf("refusal: %v %v", err, res)
	}
	cs, err := a.client.CreateSession(a.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: t.TempDir()}))
	if err != nil {
		t.Fatal(err)
	}
	sid := cs.Msg.Session.Id
	for _, c := range []struct{ model, engine string }{{"agent-only", "acp:fake"}, {"agent-only", ""}} {
		_, err := a.client.SetModel(a.ctx, connect.NewRequest(&agentv1.SetModelRequest{SessionId: sid, Model: c.model, Engine: c.engine}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "start a new session") {
			t.Fatalf("%s on %q: %v", c.model, c.engine, err)
		}
	}
	// A model nobody has stays the catalog's plain error.
	_, err = a.client.SetModel(a.ctx, connect.NewRequest(&agentv1.SetModelRequest{SessionId: sid, Model: "nope"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown model: %v", err)
	}
}

// The probe's outcome is device-local: it reaches frontends only through ListAcpAgents.
func TestAcpModelsAreNotInTheCatalog(t *testing.T) {
	a := newAcpModels(t)
	a.setModels("agent-only=Agent only")
	a.install()
	a.settled()
	init, err := a.client.Initialize(a.ctx, connect.NewRequest(&agentv1.InitializeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(init.Msg.ModelIds, "agent-only") {
		t.Fatalf("the catalog lists an agent's model: %v", init.Msg.ModelIds)
	}
}

func (a *acpModels) setEfforts(lines ...string) {
	a.t.Helper()
	if err := os.WriteFile(a.effort, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		a.t.Fatal(err)
	}
}

func effortIDs(es []*agentv1.AcpEffort) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Id)
	}
	return out
}

func hasEffortFeature(fs []agentv1.SessionFeature) bool {
	return slices.Contains(fs, agentv1.SessionFeature_SESSION_FEATURE_EFFORT)
}

func TestAcpProbeFindsTheAgentsEffortLevels(t *testing.T) {
	a := newAcpModels(t)
	a.setModels("fast=Fast")
	a.setEfforts("think=Think", "ultra=Ultrathink")
	a.install()
	x := a.settled()
	if !slices.Equal(effortIDs(x.Efforts), []string{"think", "ultra"}) || x.Efforts[1].Name != "Ultrathink" || x.CurrentEffortId != "think" {
		t.Fatalf("efforts: %v %q", x.Efforts, x.CurrentEffortId)
	}
	if hasEffortFeature(x.UnsupportedFeatures) {
		t.Fatalf("features: %v", x.UnsupportedFeatures)
	}
}

func TestAcpSessionStartsWithTheChosenEffort(t *testing.T) {
	a := newAcpModels(t)
	a.setModels("fast=Fast")
	a.setEfforts("think=Think", "ultra=Ultrathink")
	a.install()
	a.settled()
	proj, _ := filepath.EvalSymlinks(t.TempDir())
	cs, err := a.client.CreateSession(a.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: proj, Engine: "acp:fake", AcpEffortId: "ultra"}))
	if err != nil {
		t.Fatal(err)
	}
	info := cs.Msg.Session
	// The choice shows from the first moment, and the levels are the cached ones until the agent answers.
	if info.Acp.CurrentEffortId != "ultra" || !slices.Equal(effortIDs(info.Acp.Efforts), []string{"think", "ultra"}) || hasEffortFeature(info.UnsupportedFeatures) {
		t.Fatalf("session: %v", info)
	}
	sid := info.Id
	// A message sent right away reaches the agent on the chosen effort.
	if _, err := a.client.SendUserMessage(a.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "u1", Content: text("hello")})); err != nil {
		t.Fatal(err)
	}
	a.waitFor("the prompt", func() bool { return a.count("prompt") > 0 })
	if !slices.Contains(a.logged(), "effort at prompt ultra") {
		t.Fatalf("agent log: %v", a.logged())
	}
	// Switched in the session, by the agent's own id; a level it does not have is refused.
	a.waitFor("the turn to end", func() bool { return a.sessionStatus(sid) == agentv1.SessionStatus_SESSION_STATUS_IDLE })
	if _, err := a.client.SetEffort(a.ctx, connect.NewRequest(&agentv1.SetEffortRequest{SessionId: sid, LevelId: "think"})); err != nil {
		t.Fatal(err)
	}
	if st := a.sessionState(sid); st.CurrentEffortId != "think" {
		t.Fatalf("state: %v", st)
	}
	_, err = a.client.SetEffort(a.ctx, connect.NewRequest(&agentv1.SetEffortRequest{SessionId: sid, LevelId: "medium"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), `no effort level "medium"`) {
		t.Fatalf("set: %v", err)
	}
	// Recorded: a resume goes back to it, not to the agent's default.
	if _, err := a.client.SetEffort(a.ctx, connect.NewRequest(&agentv1.SetEffortRequest{SessionId: sid, LevelId: "ultra"})); err != nil {
		t.Fatal(err)
	}
	a.waitFor("the record", func() bool { return strings.Contains(a.sessionMeta(sid), `"effort": "ultra"`) })
	if _, err := a.client.CloseSession(a.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	sets := a.count("set_effort ultra")
	if _, err := a.client.CreateSession(a.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid})); err != nil {
		t.Fatal(err)
	}
	a.waitFor("the effort restored", func() bool {
		return a.count("set_effort ultra") == sets+1 && strings.Contains(a.sessionMeta(sid), `"effort": "ultra"`)
	})
	if _, err := a.client.CloseSession(a.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	// The agent no longer offers it: the session runs on its default (the notice: TestEffortOption in package acp).
	a.setEfforts("think=Think", "deep=Deep")
	if _, err := a.client.CreateSession(a.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid})); err != nil {
		t.Fatal(err)
	}
	a.waitFor("the session on the default", func() bool {
		st := a.sessionState(sid)
		return !st.GetStarting() && st.GetCurrentEffortId() == "think" && slices.Equal(effortIDs(st.GetEfforts()), []string{"think", "deep"})
	})
}

func TestAcpAgentWithoutEffortRefusesIt(t *testing.T) {
	a := newAcpModels(t)
	a.setModels("fast=Fast")
	a.install()
	x := a.settled()
	if len(x.Efforts) != 0 || !hasEffortFeature(x.UnsupportedFeatures) {
		t.Fatalf("agent: %v", x)
	}
	cs, err := a.client.CreateSession(a.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: t.TempDir(), Engine: "acp:fake"}))
	if err != nil {
		t.Fatal(err)
	}
	sid := cs.Msg.Session.Id
	if !hasEffortFeature(cs.Msg.Session.UnsupportedFeatures) {
		t.Fatalf("session: %v", cs.Msg.Session)
	}
	a.waitFor("the session to open", func() bool { return !a.sessionState(sid).GetStarting() })
	_, err = a.client.SetEffort(a.ctx, connect.NewRequest(&agentv1.SetEffortRequest{SessionId: sid, LevelId: "ultra"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "Fake Agent does not offer effort levels") {
		t.Fatalf("set: %v", err)
	}
	if a.count("set_effort") != 0 {
		t.Fatalf("agent log: %v", a.logged())
	}
}

func TestAcpSessionReportingEffortsUpdatesTheCache(t *testing.T) {
	a := newAcpModels(t)
	a.setModels("fast=Fast")
	a.install()
	a.settled()
	cs, err := a.client.CreateSession(a.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: t.TempDir(), Engine: "acp:fake"}))
	if err != nil {
		t.Fatal(err)
	}
	sid := cs.Msg.Session.Id
	a.waitFor("the session to open", func() bool { return !a.sessionState(sid).GetStarting() })
	// The agent gains the option under a running session: it is known from then on, also for the next session.
	a.setEfforts("think=Think", "ultra=Ultrathink")
	if _, err := a.client.SendUserMessage(a.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "u1", Content: text("newmodels")})); err != nil {
		t.Fatal(err)
	}
	a.waitFor("the cache to change", func() bool {
		x := a.list(false)
		return slices.Equal(effortIDs(x.Efforts), []string{"think", "ultra"}) && !hasEffortFeature(x.UnsupportedFeatures)
	})
	ls, err := a.client.ListSessions(a.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ls.Msg.Sessions {
		if s.Id == sid && hasEffortFeature(s.UnsupportedFeatures) {
			t.Fatalf("session: %v", s)
		}
	}
	// Levels can go with a model: a session reporting none leaves the agent's cached ones.
	a.setEfforts()
	if _, err := a.client.SendUserMessage(a.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "u2", Content: text("newmodels")})); err != nil {
		t.Fatal(err)
	}
	a.waitFor("the option gone from the session", func() bool { return len(a.sessionState(sid).GetEfforts()) == 0 })
	_, err = a.client.SetEffort(a.ctx, connect.NewRequest(&agentv1.SetEffortRequest{SessionId: sid, LevelId: "ultra"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("set: %v", err)
	}
	if x := a.list(false); len(x.Efforts) != 2 {
		t.Fatalf("cache: %v", x.Efforts)
	}
}

func (a *acpModels) sessionStatus(id string) agentv1.SessionStatus {
	a.t.Helper()
	ls, err := a.client.ListSessions(a.ctx, connect.NewRequest(&agentv1.ListSessionsRequest{}))
	if err != nil {
		a.t.Fatal(err)
	}
	for _, s := range ls.Msg.Sessions {
		if s.Id == id {
			return s.Status
		}
	}
	return agentv1.SessionStatus_SESSION_STATUS_UNSPECIFIED
}

func (a *acpModels) sessionMeta(id string) string {
	b, _ := os.ReadFile(filepath.Join(a.home, "acp", "sessions", id, "meta.json"))
	return string(b)
}

// The level a probe finds is the agent's default: a session's level, whichever way it reaches the cache, does not change it.
func TestAcpDefaultEffortIsNotASessionsChoice(t *testing.T) {
	a := newAcpModels(t)
	a.setModels("fast=Fast")
	a.setEfforts("think=Think", "ultra=Ultrathink")
	a.install()
	if x := a.settled(); x.DefaultEffortId != "think" {
		t.Fatalf("probed: %q", x.DefaultEffortId)
	}
	cs, err := a.client.CreateSession(a.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: t.TempDir(), Engine: "acp:fake", AcpEffortId: "ultra"}))
	if err != nil {
		t.Fatal(err)
	}
	sid := cs.Msg.Session.Id
	a.waitFor("the session on its level", func() bool {
		st := a.sessionState(sid)
		return !st.GetStarting() && st.GetCurrentEffortId() == "ultra" && st.GetDefaultEffortId() == "think"
	})
	if x := a.list(false); x.DefaultEffortId != "think" {
		t.Fatalf("after a chosen level: %q", x.DefaultEffortId)
	}
	newLevels := func(id string, lines ...string) {
		t.Helper()
		a.setEfforts(lines...)
		if _, err := a.client.SendUserMessage(a.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: id, Content: text("newmodels")})); err != nil {
			t.Fatal(err)
		}
	}
	// The default is not offered any more: the cached current level becomes the session's, and no default shows.
	newLevels("u1", "ultra=Ultrathink", "deep=Deep")
	a.waitFor("the cache to take the session's", func() bool {
		x := a.list(false)
		return slices.Equal(effortIDs(x.Efforts), []string{"ultra", "deep"}) && x.CurrentEffortId == "ultra"
	})
	if x, st := a.list(false), a.sessionState(sid); x.DefaultEffortId != "" || st.GetDefaultEffortId() != "" {
		t.Fatalf("default not offered: %q %q", x.DefaultEffortId, st.GetDefaultEffortId())
	}
	// Offered again: the agent's default is what it was, while the current level still says the session's.
	newLevels("u2", "think=Think", "ultra=Ultrathink")
	a.waitFor("the levels back", func() bool {
		return slices.Equal(effortIDs(a.list(false).Efforts), []string{"think", "ultra"})
	})
	if x := a.list(false); x.DefaultEffortId != "think" || x.CurrentEffortId != "ultra" {
		t.Fatalf("default %q current %q", x.DefaultEffortId, x.CurrentEffortId)
	}
	// A refresh asks the agent again.
	a.setEfforts("deep=Deep", "ultra=Ultrathink")
	a.list(true)
	if x := a.settled(); x.DefaultEffortId != "deep" {
		t.Fatalf("refreshed: %q", x.DefaultEffortId)
	}
}

// A catalog from before the default was recorded has a current level that may be a session's: it is not taken for the
// default, one probe finds it, and a failing probe is not repeated on every listing.
func TestAcpCatalogWithoutDefaultGetsOneProbe(t *testing.T) {
	a := newAcpModels(t)
	a.setModels("fast=Fast")
	a.setEfforts("think=Think", "ultra=Ultrathink")
	a.install()
	a.settled()
	old := func() {
		t.Helper()
		if _, err := a.srv.AcpRegistry().UpdateCatalog("fake", "1.0.0", func(c *registry.Catalog) {
			c.CurrentEffort, c.DefaultEffort, c.DefaultProbed = "ultra", "", false
		}); err != nil {
			t.Fatal(err)
		}
	}
	old()
	a.list(false)
	if x := a.settled(); x.DefaultEffortId != "think" {
		t.Fatalf("after its probe: %q", x.DefaultEffortId)
	}
	spawns := a.count("spawn")
	a.settled()
	if a.count("spawn") != spawns {
		t.Fatal("probed again with the default known")
	}
	if err := os.WriteFile(a.fail, []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}
	old()
	a.list(false)
	a.settled()
	spawns = a.count("spawn")
	a.settled()
	if a.count("spawn") != spawns {
		t.Fatal("a failed probe runs again on every listing")
	}
}
