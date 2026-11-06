package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/server"
	"github.com/xlsft/harness/packages/client/skills"
)

// trusted opens project files in the harness's sessions.
func trusted(mod ...func(*bootstrap.Options)) func(*bootstrap.Options) {
	return func(o *bootstrap.Options) {
		yes := true
		o.Trusted = &yes
		for _, m := range mod {
			m(o)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// settingsDoc is the user's settings document on a server.
type settingsDoc struct {
	mu  sync.Mutex
	doc *syncv1.Document
}

func (f *settingsDoc) GetDocument(context.Context, *syncv1.GetDocumentRequest) (*syncv1.GetDocumentResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.doc == nil {
		return nil, &llm.APIError{Status: http.StatusNotFound, Message: "not found"}
	}
	return &syncv1.GetDocumentResponse{Document: f.doc}, nil
}

func (f *settingsDoc) PutDocument(_ context.Context, req *syncv1.PutDocumentRequest) (*syncv1.PutDocumentResponse, error) {
	f.write(req.GetContent())
	f.mu.Lock()
	defer f.mu.Unlock()
	return &syncv1.PutDocumentResponse{Result: &syncv1.PutDocumentResponse_Document{Document: f.doc}}, nil
}

func (f *settingsDoc) write(content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.doc = &syncv1.Document{Id: "doc_1", Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, Key: "settings", Version: f.doc.GetVersion() + 1, Content: content}
}

func (f *settingsDoc) content() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.doc.GetContent()
}

// recording scripts the model, keeping every main-loop request.
type recording struct {
	mu   sync.Mutex
	reqs []*llm.Request
	next func(n int, r *llm.Request) fg.Reply
}

func (rec *recording) respond(_ int, r *llm.Request) fg.Reply {
	if r.Metadata != nil && r.Metadata.RequestClass != "main" && r.Metadata.RequestClass != "subagent" && r.Metadata.RequestClass != "compaction" {
		return fg.Reply{Events: fg.Message("aux", fg.Text("ok"))}
	}
	rec.mu.Lock()
	n := len(rec.reqs)
	rec.reqs = append(rec.reqs, r)
	next := rec.next
	rec.mu.Unlock()
	if next != nil {
		return next(n, r)
	}
	return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
}

func (rec *recording) all() []*llm.Request {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]*llm.Request(nil), rec.reqs...)
}

func (rec *recording) last(t *testing.T) *llm.Request {
	t.Helper()
	all := rec.all()
	if len(all) == 0 {
		t.Fatal("no model request")
	}
	return all[len(all)-1]
}

func (h *harness) commands(req *agentv1.ListSlashCommandsRequest) map[string]*agentv1.SlashCommand {
	h.t.Helper()
	res, err := h.client.ListSlashCommands(h.ctx, connect.NewRequest(req))
	if err != nil {
		h.t.Fatal(err)
	}
	out := map[string]*agentv1.SlashCommand{}
	for _, c := range res.Msg.GetCommands() {
		if c.GetShadowedBy() == "" || out[c.GetName()] == nil {
			out[c.GetName()] = c
		}
	}
	return out
}

// result waits for a built-in's answer.
func (h *harness) result(ch <-chan *agentv1.SessionEvent) *agentv1.SlashCommandResult {
	h.t.Helper()
	var r *agentv1.SlashCommandResult
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		r = ev.GetSlashCommandResult()
		return r != nil
	})
	return r
}

func TestSkillFromTheComposer(t *testing.T) {
	rec := &recording{}
	h := startHarness(t, rec.respond, trusted())
	writeFile(t, filepath.Join(h.proj, ".claude/skills/deploy/SKILL.md"), "---\ndescription: Ship it\nargument-hint: \"[env]\"\n---\nDeploy to $ARGUMENTS using the checklist in ${HARNESS_SKILL_DIR}/list.md.")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})

	h.send(sid, "hello")
	h.until(ch, isTurnFinished)
	first := allText(rec.last(t))
	if !strings.Contains(first, "- deploy [env]: Ship it") {
		t.Fatalf("the listing has the name and description: %s", first)
	}
	if strings.Contains(first, "Deploy to") {
		t.Fatalf("the body stays out until used: %s", first)
	}

	h.send(sid, "/deploy prod")
	evs := h.until(ch, isTurnFinished)
	r := rec.last(t)
	text := allText(r)
	want := "Deploy to prod using the checklist in " + filepath.Join(h.proj, ".claude/skills/deploy") + "/list.md."
	if !strings.Contains(text, want) || !strings.Contains(text, `<skill name="deploy"`) {
		t.Fatalf("the body with its arguments reaches the model: %s", text)
	}
	if strings.Count(text, "- deploy [env]") != 1 {
		t.Fatalf("an unchanged listing isn't sent again: %s", text)
	}
	var invoked *agentv1.SkillInvoked
	var shown []string
	for _, ev := range evs {
		if s := ev.GetSkillInvoked(); s != nil {
			invoked = s
		}
		if u := ev.GetUserMessage(); u != nil {
			shown = append(shown, resultText(u.GetContent()))
		}
	}
	if invoked == nil || invoked.GetName() != "deploy" || invoked.GetSource() != "project" || invoked.GetArguments() != "prod" || invoked.GetToolUseId() != "" {
		t.Fatalf("event: %+v", invoked)
	}
	if !slices.Equal(shown, []string{"/deploy prod"}) {
		t.Fatalf("the transcript shows what was typed, not the body: %q", shown)
	}

	// After a resume the history still shows it.
	h.closeSession(sid)
	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ResumeSessionId: sid, Cwd: h.proj}))
	if err != nil {
		t.Fatal(err)
	}
	stream := h.subscribe(cs.Msg.GetSession().GetId())
	found := false
	h.until(stream, func(ev *agentv1.SessionEvent) bool {
		if ev.GetSkillInvoked().GetName() == "deploy" {
			found = true
		}
		return found || ev.GetSessionUpdated() != nil && ev.GetSessionUpdated().GetStatus() == agentv1.SessionStatus_SESSION_STATUS_IDLE
	})
	if !found {
		t.Fatal("not in the replayed history")
	}
}

func TestPlainSlashWordsStayMessages(t *testing.T) {
	rec := &recording{}
	h := startHarness(t, rec.respond, trusted())
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "/usr/local is full")
	h.until(ch, isTurnFinished)
	h.send(sid, "/nosuchcommand please")
	h.until(ch, isTurnFinished)
	text := allText(rec.last(t))
	if !strings.Contains(text, "/usr/local is full") || !strings.Contains(text, "/nosuchcommand please") {
		t.Fatalf("%s", text)
	}
}

func TestSkillToolLoadsTheBodyAndItsFiles(t *testing.T) {
	personal := t.TempDir()
	rec := &recording{}
	dir := filepath.Join(personal, ".claude/skills/guide")
	rec.next = func(n int, r *llm.Request) fg.Reply {
		switch n {
		case 0:
			return fg.Reply{Events: fg.Message("m0", fg.ToolUse("s1", "Skill", map[string]any{"skill": "guide", "args": "topic"}))}
		case 1:
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("r1", "Read", map[string]any{"file_path": filepath.Join(dir, "more.md")}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
	}
	h := startHarness(t, rec.respond, trusted(func(o *bootstrap.Options) { o.SkillHome = personal }))
	writeFile(t, filepath.Join(dir, "SKILL.md"), "---\ndescription: The guide\n---\nGuide about $1.")
	writeFile(t, filepath.Join(dir, "more.md"), "SUPPORTING TEXT")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	evs := h.until(ch, isTurnFinished)
	for _, ev := range evs {
		if p := ev.GetPermissionRequest(); p != nil {
			t.Fatalf("a skill that only adds text, and its files, need no prompt: %+v", p)
		}
	}
	all := rec.all()
	if len(all) != 3 {
		t.Fatalf("requests %d", len(all))
	}
	if text := allText(all[1]); !strings.Contains(text, "Guide about topic.") || !strings.Contains(text, `directory="`+dir+`"`) {
		t.Fatalf("the body is in the next request: %s", text)
	}
	if got := lastResultText(all[2]); !strings.Contains(got, "SUPPORTING TEXT") {
		t.Fatalf("supporting file: %s", got)
	}
	var invoked *agentv1.SkillInvoked
	var started *agentv1.ToolCallStarted
	for _, ev := range evs {
		if s := ev.GetSkillInvoked(); s != nil {
			invoked = s
		}
		if s := ev.GetToolCallStarted(); s != nil && s.GetName() == "Skill" {
			started = s
		}
	}
	if invoked.GetToolUseId() != "s1" || invoked.GetSource() != "personal" || started.GetSummary() != "Skill guide" {
		t.Fatalf("%+v %+v", invoked, started)
	}
}

func TestSkillToolRefusesWhatItMayNotRun(t *testing.T) {
	rec := &recording{}
	calls := []string{"nosuch", "useronly", "denied"}
	rec.next = func(n int, r *llm.Request) fg.Reply {
		if n < len(calls) {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("c"+calls[n], "Skill", map[string]any{"skill": calls[n]}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
	}
	h := startHarness(t, rec.respond, trusted())
	writeFile(t, filepath.Join(h.proj, ".claude/skills/useronly/SKILL.md"), "---\ndescription: mine\ndisable-model-invocation: true\n---\nUSER ONLY BODY")
	writeFile(t, filepath.Join(h.proj, ".claude/skills/denied/SKILL.md"), "---\ndescription: forbidden\n---\nDENIED BODY")
	writeFile(t, filepath.Join(h.proj, ".claude/skills/fine/SKILL.md"), "---\ndescription: all right\n---\nFINE BODY")
	sid, ch := h.create(&agentv1.CreateSessionRequest{DisallowedTools: []string{"Skill(denied)"}})
	h.send(sid, "go")
	h.until(ch, isTurnFinished)
	all := rec.all()
	listing := allText(all[0])
	if !strings.Contains(listing, "fine") || strings.Contains(listing, "useronly") || strings.Contains(listing, "forbidden") {
		t.Fatalf("listing: %s", listing)
	}
	if got := lastResultText(all[1]); !strings.Contains(got, "no skill named") {
		t.Fatalf("unknown: %s", got)
	}
	if got := lastResultText(all[2]); !strings.Contains(got, "Only the user") {
		t.Fatalf("user only: %s", got)
	}
	if got := lastResultText(all[3]); !strings.Contains(got, "denied") {
		t.Fatalf("denied: %s", got)
	}
	if text := allText(rec.last(t)); strings.Contains(text, "BODY") {
		t.Fatalf("a body leaked: %s", text)
	}

	// The user can still type what the model may not start.
	h.send(sid, "/useronly")
	h.until(ch, isTurnFinished)
	if !strings.Contains(allText(rec.last(t)), "USER ONLY BODY") {
		t.Fatal("the user's own command")
	}
}

func TestAllowedToolsLastForTheTurn(t *testing.T) {
	rec := &recording{}
	var marker string
	rec.next = func(n int, r *llm.Request) fg.Reply {
		switch n {
		case 0, 2:
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("b"+string(rune('0'+n)), "Bash", map[string]any{"command": "touch " + marker, "description": "touch"}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
	}
	h := startHarness(t, rec.respond, trusted())
	marker = filepath.Join(h.proj, "marker")
	writeFile(t, filepath.Join(h.proj, ".claude/skills/toucher/SKILL.md"), "---\ndescription: touches\nallowed-tools: Bash(touch *)\n---\nTouch it.")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	// During the skill's turn the command runs without a prompt.
	h.send(sid, "/toucher")
	for _, ev := range h.until(ch, isTurnFinished) {
		if p := ev.GetPermissionRequest(); p != nil {
			t.Fatalf("asked during the skill: %+v", p)
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("not run")
	}
	// The next turn asks again.
	os.Remove(marker)
	h.send(sid, "again")
	asked := false
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			asked = true
			if _, err := h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: p.GetRequestId(), Decision: agentv1.RespondToPermissionRequest_DECISION_DENY})); err != nil {
				t.Error(err)
			}
		}
		return isTurnFinished(ev)
	})
	if !asked {
		t.Fatal("the grant outlived the turn")
	}
}

func TestSkillThatGrantsToolsAsksBeforeTheModelStartsIt(t *testing.T) {
	rec := &recording{}
	rec.next = func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("s", "Skill", map[string]any{"skill": "grants"}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
	}
	h := startHarness(t, rec.respond, trusted())
	writeFile(t, filepath.Join(h.proj, ".claude/skills/grants/SKILL.md"), "---\ndescription: g\nallowed-tools: Bash\n---\nGRANTS BODY")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	var asked *agentv1.PermissionRequest
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			asked = p
			if _, err := h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: p.GetRequestId(), Decision: agentv1.RespondToPermissionRequest_DECISION_DENY})); err != nil {
				t.Error(err)
			}
		}
		return isTurnFinished(ev)
	})
	if asked == nil || !slices.Contains(asked.GetSuggestedRules(), "Skill(grants)") {
		t.Fatalf("%+v", asked)
	}
	if strings.Contains(allText(rec.last(t)), "GRANTS BODY") {
		t.Fatal("refused, yet started")
	}

	// A rule says yes ahead of time.
	rec2 := &recording{next: rec.next}
	h2 := startHarness(t, rec2.respond, trusted())
	writeFile(t, filepath.Join(h2.proj, ".claude/skills/grants/SKILL.md"), "---\ndescription: g\nallowed-tools: Bash\n---\nGRANTS BODY")
	sid2, ch2 := h2.create(&agentv1.CreateSessionRequest{AllowedTools: []string{"Skill(grants)"}})
	h2.send(sid2, "go")
	for _, ev := range h2.until(ch2, isTurnFinished) {
		if ev.GetPermissionRequest() != nil {
			t.Fatal("the rule allows it")
		}
	}
	if !strings.Contains(allText(rec2.last(t)), "GRANTS BODY") {
		t.Fatal("not started")
	}
}

func TestSkillModelAndEffortLastForTheTurn(t *testing.T) {
	rec := &recording{}
	h := startHarnessGW(t, rec.respond, func(g *fg.Server) { g.Models = []string{"fake-model", "other-model"} }, trusted())
	writeFile(t, filepath.Join(h.proj, ".claude/skills/heavy/SKILL.md"), "---\ndescription: h\nmodel: other\neffort: high\n---\nHeavy.")
	writeFile(t, filepath.Join(h.proj, ".claude/skills/odd/SKILL.md"), "---\ndescription: o\nmodel: nosuchmodel\n---\nOdd.")
	sid, ch := h.create(&agentv1.CreateSessionRequest{Model: "fake-model"})
	h.send(sid, "/heavy")
	evs := h.until(ch, isTurnFinished)
	r := rec.last(t)
	if r.Model != "other-model" {
		t.Fatalf("the skill's turn: %s %s", r.Model, r.Effort)
	}
	for _, ev := range evs {
		if s := ev.GetSkillInvoked(); s != nil && s.GetModel() != "other-model" {
			t.Fatalf("%+v", s)
		}
	}
	h.send(sid, "next")
	h.until(ch, isTurnFinished)
	if r := rec.last(t); r.Model != "fake-model" || r.Effort == "high" {
		t.Fatalf("back after the turn: %s %s", r.Model, r.Effort)
	}
	// A model that isn't there: it says so and runs.
	h.send(sid, "/odd")
	warned := false
	for _, ev := range h.until(ch, isTurnFinished) {
		if n := ev.GetNotice(); n != nil && strings.Contains(n.GetText(), "nosuchmodel") {
			warned = true
		}
	}
	if !warned || rec.last(t).Model != "fake-model" || !strings.Contains(allText(rec.last(t)), "Odd.") {
		t.Fatalf("warned=%v model=%s", warned, rec.last(t).Model)
	}
}

func TestSkillCommandsInTheBody(t *testing.T) {
	rec := &recording{}
	h := startHarness(t, rec.respond, trusted())
	writeFile(t, filepath.Join(h.proj, ".claude/skills/cmds/SKILL.md"), "---\ndescription: c\nallowed-tools: Bash(echo *)\n---\nGot: !`echo from-the-shell`\nDenied: !`printf nope > "+filepath.Join(h.proj, "written")+"`\n")
	sid, ch := h.create(&agentv1.CreateSessionRequest{DisallowedTools: []string{"Bash(printf *)"}})
	h.send(sid, "/cmds")
	h.until(ch, isTurnFinished)
	text := allText(rec.last(t))
	if !strings.Contains(text, "Got: from-the-shell") {
		t.Fatalf("the skill's allowed command ran: %s", text)
	}
	if !strings.Contains(text, "Denied: [command not run:") {
		t.Fatalf("the rule refused the other: %s", text)
	}
	if _, err := os.Stat(filepath.Join(h.proj, "written")); err == nil {
		t.Fatal("a denied command ran")
	}
}

func TestCustomCommandsWithNamespaces(t *testing.T) {
	rec := &recording{}
	h := startHarness(t, rec.respond, trusted())
	writeFile(t, filepath.Join(h.proj, ".claude/commands/fe/test.md"), "---\ndescription: Frontend tests\nargument-hint: <suite>\n---\nRun suite $1 now.")
	writeFile(t, filepath.Join(h.proj, ".harness/commands/notes.md"), "Write notes about $ARGUMENTS.")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	list := h.commands(&agentv1.ListSlashCommandsRequest{SessionId: sid})
	fe := list["fe:test"]
	if fe == nil || fe.GetKind() != agentv1.SlashCommandKind_SLASH_COMMAND_KIND_COMMAND || fe.GetSource() != "project" || fe.GetArgumentHint() != "<suite>" || !strings.HasSuffix(fe.GetPath(), "test.md") {
		t.Fatalf("%+v", fe)
	}
	h.send(sid, "/fe:test unit")
	evs := h.until(ch, isTurnFinished)
	if !strings.Contains(allText(rec.last(t)), "Run suite unit now.") {
		t.Fatal(allText(rec.last(t)))
	}
	found := false
	for _, ev := range evs {
		if s := ev.GetSkillInvoked(); s != nil && s.GetKind() == agentv1.SlashCommandKind_SLASH_COMMAND_KIND_COMMAND {
			found = true
		}
	}
	if !found {
		t.Fatal("no event")
	}
	h.send(sid, "/notes the release")
	h.until(ch, isTurnFinished)
	if !strings.Contains(allText(rec.last(t)), "Write notes about the release.") {
		t.Fatal(allText(rec.last(t)))
	}
}

func TestListWithoutASession(t *testing.T) {
	personal := t.TempDir()
	writeFile(t, filepath.Join(personal, ".claude/skills/mine/SKILL.md"), "---\ndescription: personal\n---\nb")
	docs := skills.Documents{
		{Id: "doc_1", Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SKILL, Key: "imported-one", Content: "---\ndescription: from import\nhooks: {}\n---\nIMPORTED BODY",
			Files: []*syncv1.DocumentFile{{Path: "ref.md", Content: []byte("REF")}}},
		{Id: "doc_2", Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_COMMAND, Key: "ns:imp", Content: "command from import"},
	}
	rec := &recording{}
	h := startHarness(t, rec.respond, func(o *bootstrap.Options) { o.SkillHome, o.SkillDocs = personal, docs })
	writeFile(t, filepath.Join(h.proj, ".claude/skills/local/SKILL.md"), "---\ndescription: project\n---\nb")

	res, err := h.client.ListSlashCommands(h.ctx, connect.NewRequest(&agentv1.ListSlashCommandsRequest{Cwd: h.proj}))
	if err != nil {
		t.Fatal(err)
	}
	list := map[string]*agentv1.SlashCommand{}
	for _, c := range res.Msg.GetCommands() {
		list[c.GetName()] = c
	}
	if list["mine"].GetSource() != "personal" || list["imported-one"].GetSource() != "imported" || list["ns:imp"].GetKind() != agentv1.SlashCommandKind_SLASH_COMMAND_KIND_COMMAND {
		t.Fatalf("%+v", list)
	}
	if !slices.Equal(list["imported-one"].GetSkippedFields(), []string{"hooks"}) {
		t.Fatalf("skipped: %+v", list["imported-one"])
	}
	if list["local"] != nil || !res.Msg.GetProjectSkippedUntrusted() {
		t.Fatalf("an untrusted folder's files stay out and the answer says so: %+v", res.Msg)
	}
	for _, name := range []string{"compact", "clear", "model", "effort", "init", "code-review"} {
		c := list[name]
		if c == nil || c.GetKind() != agentv1.SlashCommandKind_SLASH_COMMAND_KIND_BUILTIN || c.GetSwitchable() {
			t.Fatalf("%s: %+v", name, c)
		}
	}
	if !list["compact"].GetNeedsSession() || list["model"].GetNeedsSession() {
		t.Fatal("needs_session")
	}
	if res.Msg.GetListingBudgetChars() == 0 || res.Msg.GetListingUsedChars() == 0 {
		t.Fatalf("%+v", res.Msg)
	}

	// An imported skill runs like any other, with its files on disk.
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "/imported-one")
	h.until(ch, isTurnFinished)
	text := allText(rec.last(t))
	if !strings.Contains(text, "IMPORTED BODY") {
		t.Fatal(text)
	}
	i := strings.Index(text, `directory="`)
	dir := text[i+len(`directory="`):]
	dir = dir[:strings.Index(dir, `"`)]
	if b, err := os.ReadFile(filepath.Join(dir, "ref.md")); err != nil || string(b) != "REF" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestCustomizeSwitch(t *testing.T) {
	rec := &recording{}
	calls := 0
	rec.next = func(n int, r *llm.Request) fg.Reply {
		calls++
		if strings.Contains(allText(r), "use the tool") && calls == 1 {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("s", "Skill", map[string]any{"skill": "toggled"}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
	}
	docs := &settingsDoc{}
	h := startHarnessSrv(t, rec.respond, nil, trusted(), func(s *server.Server) { s.Preferences = docs })
	writeFile(t, filepath.Join(h.proj, ".claude/skills/toggled/SKILL.md"), "---\ndescription: can be off\n---\nTOGGLED BODY")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	if c := h.commands(&agentv1.ListSlashCommandsRequest{SessionId: sid})["toggled"]; !c.GetEnabled() || !c.GetSwitchable() || !c.GetListed() {
		t.Fatalf("%+v", c)
	}

	up, err := h.client.UpdateUserPreferences(h.ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{SkillSwitch: &agentv1.SkillSwitch{Name: "toggled", Enabled: false}}))
	if err != nil || !slices.Equal(up.Msg.GetPreferences().GetDisabledSkills(), []string{"toggled"}) {
		t.Fatalf("%v %+v", err, up)
	}
	if c := h.commands(&agentv1.ListSlashCommandsRequest{SessionId: sid})["toggled"]; c.GetEnabled() || c.GetListed() {
		t.Fatalf("session list: %+v", c)
	}
	if c := h.commands(&agentv1.ListSlashCommandsRequest{Cwd: h.proj})["toggled"]; c != nil && c.GetEnabled() {
		t.Fatalf("list without a session: %+v", c)
	}

	// Off: the user's /name says so and starts nothing.
	h.send(sid, "/toggled")
	if r := h.result(ch); !r.GetIsError() || !strings.Contains(r.GetText(), "turned off") {
		t.Fatalf("%+v", r)
	}
	// The model neither sees it nor can start it.
	h.send(sid, "use the tool")
	h.until(ch, isTurnFinished)
	text := allText(rec.last(t))
	if strings.Contains(text, "TOGGLED BODY") || strings.Contains(text, "can be off") {
		t.Fatalf("%s", text)
	}
	if got := lastResultText(rec.last(t)); !strings.Contains(got, "turned the skill") {
		t.Fatalf("%s", got)
	}

	// On again: the list follows, and the settings keep the other keys.
	docs.write(`{"model":"keep"}`)
	if _, err := h.client.UpdateUserPreferences(h.ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{SkillSwitch: &agentv1.SkillSwitch{Name: "other", Enabled: false}})); err != nil {
		t.Fatal(err)
	}
	up, err = h.client.UpdateUserPreferences(h.ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{SkillSwitch: &agentv1.SkillSwitch{Name: "toggled", Enabled: true}}))
	if err != nil || !slices.Equal(up.Msg.GetPreferences().GetDisabledSkills(), []string{"other"}) {
		t.Fatalf("%v %+v", err, up)
	}
	if !strings.Contains(docs.content(), `"model"`) {
		t.Fatalf("%s", docs.content())
	}
	h.send(sid, "/toggled")
	h.until(ch, isTurnFinished)
	if !strings.Contains(allText(rec.last(t)), "TOGGLED BODY") {
		t.Fatal("on again")
	}
	if _, err := h.client.UpdateUserPreferences(h.ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{SkillSwitch: &agentv1.SkillSwitch{Name: " "}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatal(err)
	}
}

func TestBuiltinModelAndEffort(t *testing.T) {
	rec := &recording{}
	h := startHarnessGW(t, rec.respond, func(g *fg.Server) { g.Models = []string{"fake-model", "other-model"} }, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{Model: "fake-model"})

	id := "11111111-1111-4111-8111-111111111111"
	h.sendID(sid, id, "/model other", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	r := h.result(ch)
	if r.GetUuid() != id || r.GetIsError() || !strings.Contains(r.GetText(), "other-model") {
		t.Fatalf("%+v", r)
	}
	if got := h.info(sid).GetModel(); got != "other-model" {
		t.Fatalf("model %s", got)
	}
	// The same message again does nothing more.
	h.sendID(sid, id, "/model other", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)

	h.send(sid, "/model")
	if r := h.result(ch); r.GetAction() != agentv1.SlashCommandResult_ACTION_PICK_MODEL {
		t.Fatalf("%+v", r)
	}
	h.send(sid, "/model nosuch")
	if r := h.result(ch); !r.GetIsError() {
		t.Fatalf("%+v", r)
	}
	h.send(sid, "/effort")
	if r := h.result(ch); r.GetAction() != agentv1.SlashCommandResult_ACTION_PICK_EFFORT {
		t.Fatalf("%+v", r)
	}
	h.send(sid, "/effort huge")
	if r := h.result(ch); !r.GetIsError() {
		t.Fatalf("%+v", r)
	}
	if len(rec.all()) != 0 {
		t.Fatal("built-ins don't call the model")
	}
	h.send(sid, "/help")
	if r := h.result(ch); !strings.Contains(r.GetText(), "/compact") || !strings.Contains(r.GetText(), "/code-review") {
		t.Fatalf("%+v", r)
	}
	h.send(sid, "/skills")
	if r := h.result(ch); r.GetAction() != agentv1.SlashCommandResult_ACTION_OPEN_CUSTOMIZE {
		t.Fatalf("%+v", r)
	}
	h.send(sid, "/context")
	if r := h.result(ch); !strings.Contains(r.GetText(), "tokens in use") {
		t.Fatalf("%+v", r)
	}
	h.send(sid, "/rename A new name")
	h.result(ch)
	if got := h.info(sid).GetTitle(); got != "A new name" {
		t.Fatalf("title %q", got)
	}
	h.send(sid, "/plan")
	h.result(ch)
	if h.mode(sid) != agentv1.PermissionMode_PERMISSION_MODE_PLAN {
		t.Fatal("plan")
	}
	h.send(sid, "/plan off")
	h.result(ch)
	if h.mode(sid) == agentv1.PermissionMode_PERMISSION_MODE_PLAN {
		t.Fatal("plan off")
	}
}

func TestBuiltinClear(t *testing.T) {
	rec := &recording{}
	h := startHarness(t, rec.respond, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "remember the codeword")
	h.until(ch, isTurnFinished)
	h.send(sid, "/clear")
	r := h.result(ch)
	if r.GetAction() != agentv1.SlashCommandResult_ACTION_OPEN_SESSION || r.GetNewSessionId() == "" || r.GetNewSessionId() == sid {
		t.Fatalf("%+v", r)
	}
	if got := h.info(r.GetNewSessionId()).GetCwd(); got != h.proj {
		t.Fatalf("cwd %q", got)
	}
	next := h.subscribe(r.GetNewSessionId())
	h.send(r.GetNewSessionId(), "fresh start")
	h.until(next, isTurnFinished)
	if text := allText(rec.last(t)); strings.Contains(text, "codeword") || !strings.Contains(text, "fresh start") {
		t.Fatalf("empty context: %s", text)
	}
	// The old one stays.
	if h.info(sid).GetId() != sid {
		t.Fatal("the old session went away")
	}
}

func TestBuiltinCompact(t *testing.T) {
	rec := &recording{}
	h := startHarness(t, func(n int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.RequestClass == "compaction" {
			rec.mu.Lock()
			rec.reqs = append(rec.reqs, r)
			rec.mu.Unlock()
			return fg.Reply{Events: fg.Message("sum", fg.Text("SUMMARY"))}
		}
		return rec.respond(n, r)
	}, trusted())
	writeFile(t, filepath.Join(h.proj, ".claude/skills/kept/SKILL.md"), "---\ndescription: survives\n---\nKEPT BODY")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "/kept")
	h.until(ch, isTurnFinished)
	h.send(sid, "/compact keep the tests")
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetCompacted() != nil })
	if !strings.Contains(allText(rec.last(t)), "keep the tests") {
		t.Fatal("focus instructions")
	}
	// The next request has the summary, and the skill's text and the listing again.
	h.send(sid, "go on")
	h.until(ch, isTurnFinished)
	text := allText(rec.last(t))
	if !strings.Contains(text, "SUMMARY") || !strings.Contains(text, "KEPT BODY") || !strings.Contains(text, "- kept: survives") {
		t.Fatalf("after compaction: %s", text)
	}
	if strings.Count(text, "KEPT BODY") != 1 {
		t.Fatalf("once: %s", text)
	}
}

func TestBuiltinInit(t *testing.T) {
	rec := &recording{}
	h := startHarness(t, rec.respond, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "/init testing rules")
	evs := h.until(ch, isTurnFinished)
	text := allText(rec.last(t))
	if !strings.Contains(text, "CLAUDE.md in "+h.proj) || !strings.Contains(text, "existing none") || !strings.Contains(text, "focus testing rules") {
		t.Fatalf("%s", text)
	}
	found := false
	for _, ev := range evs {
		if s := ev.GetSkillInvoked(); s != nil && s.GetKind() == agentv1.SlashCommandKind_SLASH_COMMAND_KIND_BUILTIN && s.GetName() == "init" {
			found = true
		}
	}
	if !found {
		t.Fatal("event")
	}

	writeFile(t, filepath.Join(h.proj, "AGENTS.md"), "# rules")
	h.send(sid, "/init")
	h.until(ch, isTurnFinished)
	if !strings.Contains(allText(rec.last(t)), "AGENTS.md in "+h.proj+"; existing AGENTS.md") {
		t.Fatal(allText(rec.last(t)))
	}

	// A skill of the name replaces the prompt.
	writeFile(t, filepath.Join(h.proj, ".claude/skills/init/SKILL.md"), "My own init.")
}

func gitRepo(t *testing.T, dir string) {
	t.Helper()
	gittest.Isolate(t)
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "first"}} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestBuiltinCodeReview(t *testing.T) {
	rec := &recording{}
	rec.next = func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("f", "ReportFindings", map[string]any{"level": "standard", "findings": []any{
				map[string]any{"file": "a.txt", "line": 1, "summary": "The value is wrong", "failure_scenario": "Reading it gives the wrong value", "category": "correctness"}}}))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("One finding."))}
	}
	h := startHarness(t, rec.respond, nil)
	writeFile(t, filepath.Join(h.proj, "a.txt"), "one\n")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})

	// Not a repository yet: it says what it needs.
	h.send(sid, "/code-review")
	if r := h.result(ch); !r.GetIsError() || !strings.Contains(r.GetText(), "git repository") {
		t.Fatalf("%+v", r)
	}
	gitRepo(t, h.proj)

	// Nothing to review: no model request.
	h.send(sid, "/code-review")
	if r := h.result(ch); r.GetIsError() || !strings.Contains(r.GetText(), "Nothing to review") {
		t.Fatalf("%+v", r)
	}
	if len(rec.all()) != 0 {
		t.Fatal("a request for nothing")
	}

	writeFile(t, filepath.Join(h.proj, "a.txt"), "two\n")
	h.send(sid, "/code-review")
	evs := h.until(ch, isTurnFinished)
	first := rec.all()[0]
	if text := allText(first); !strings.Contains(text, "M a.txt") || !strings.Contains(text, "a.txt | 2") || !strings.Contains(text, "ReportFindings") {
		t.Fatalf("the prompt has the diff's shape: %s", text)
	}
	var structured string
	for _, ev := range evs {
		if f := ev.GetToolCallFinished(); f != nil && f.GetToolUseId() == "f" {
			structured = f.GetStructuredResultJson()
		}
	}
	var report struct {
		Findings []struct {
			Summary  string `json:"summary"`
			Severity string `json:"severity"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(structured), &report); err != nil || len(report.Findings) != 1 || report.Findings[0].Severity != "high" {
		t.Fatalf("the findings come through the tool the UI lists: %q %v", structured, err)
	}
}

func TestPromptBuiltinCanBeReplaced(t *testing.T) {
	rec := &recording{}
	h := startHarness(t, rec.respond, trusted())
	writeFile(t, filepath.Join(h.proj, ".claude/skills/code-review/SKILL.md"), "---\ndescription: our review\n---\nOUR REVIEW RULES")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	list := h.commands(&agentv1.ListSlashCommandsRequest{SessionId: sid})
	if c := list["code-review"]; c.GetKind() != agentv1.SlashCommandKind_SLASH_COMMAND_KIND_SKILL || c.GetDescription() != "our review" {
		t.Fatalf("%+v", c)
	}
	h.send(sid, "/code-review")
	h.until(ch, isTurnFinished)
	if !strings.Contains(allText(rec.last(t)), "OUR REVIEW RULES") {
		t.Fatal(allText(rec.last(t)))
	}
	// A control built-in isn't replaced, and the list says why.
	writeFile(t, filepath.Join(h.proj, ".claude/skills/clear/SKILL.md"), "---\ndescription: a clash\n---\nNOT RUN")
	res, err := h.client.ListSlashCommands(h.ctx, connect.NewRequest(&agentv1.ListSlashCommandsRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	clash := false
	for _, c := range res.Msg.GetCommands() {
		if c.GetName() == "clear" && c.GetKind() == agentv1.SlashCommandKind_SLASH_COMMAND_KIND_SKILL {
			clash = c.GetShadowedBy() == "builtin" && len(c.GetProblems()) == 1
		}
	}
	if !clash {
		t.Fatal("clash isn't reported")
	}
}

func TestForkedSkillRunsInASubagent(t *testing.T) {
	var mu sync.Mutex
	var lead, sub []*llm.Request
	h := startHarness(t, func(_ int, r *llm.Request) fg.Reply {
		mu.Lock()
		defer mu.Unlock()
		switch r.Metadata.RequestClass {
		case "subagent":
			sub = append(sub, r)
			return fg.Reply{Events: fg.Message("s", fg.Text("agent finished"))}
		case "main":
			lead = append(lead, r)
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	}, trusted())
	writeFile(t, filepath.Join(h.proj, ".claude/skills/forked/SKILL.md"), "---\ndescription: f\ncontext: fork\n---\nFORKED BODY with $ARGUMENTS")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "/forked thing")
	var invoked *agentv1.SkillInvoked
	done := false
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if s := ev.GetSkillInvoked(); s != nil {
			invoked = s
		}
		if a := ev.GetSubagentUpdated().GetAgent(); a != nil && a.GetState() == agentv1.SubagentState_SUBAGENT_STATE_FINISHED {
			done = true
		}
		return done && invoked != nil
	})
	if invoked.GetAgentId() == "" {
		t.Fatalf("%+v", invoked)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sub) == 0 || !strings.Contains(allText(sub[0]), "FORKED BODY with thing") {
		t.Fatalf("the agent has the text: %d", len(sub))
	}
	if strings.Contains(allText(lead[0]), "FORKED BODY") {
		t.Fatalf("the conversation doesn't: %s", allText(lead[0]))
	}
	if !strings.Contains(allText(lead[0]), invoked.GetAgentId()) {
		t.Fatalf("it hears where it runs: %s", allText(lead[0]))
	}
}

func TestQueuedSkillGoesWithItsMessage(t *testing.T) {
	release := make(chan struct{})
	rec := &recording{}
	rec.next = func(n int, r *llm.Request) fg.Reply {
		if n == 0 {
			<-release
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
	}
	h := startHarness(t, rec.respond, trusted())
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	writeFile(t, filepath.Join(h.proj, ".claude/skills/queued/SKILL.md"), "---\ndescription: q\n---\nQUEUED BODY")
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "first")
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetTurnStarted() != nil })
	id := "22222222-2222-4222-8222-222222222222"
	h.sendID(sid, id, "/queued", agentv1.SendUserMessageRequest_DELIVERY_QUEUE)
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return len(ev.GetQueueChanged().GetQueued()) == 1 })
	res, err := h.client.RemoveQueuedMessage(h.ctx, connect.NewRequest(&agentv1.RemoveQueuedMessageRequest{SessionId: sid, Uuid: id}))
	if err != nil || !res.Msg.GetRemoved() {
		t.Fatalf("%v %+v", err, res)
	}
	close(release)
	h.until(ch, isTurnFinished)
	h.send(sid, "next")
	h.until(ch, isTurnFinished)
	if strings.Contains(allText(rec.last(t)), "QUEUED BODY") {
		t.Fatal("a message taken back took its skill along, it should have")
	}
}
