package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/skills"
	"github.com/xlsft/harness/packages/client/workflow"
)

// Slash commands: a message whose text starts with `/name` may run something instead of, or on top of, going to the model.
// The frontends only send text, so the terminal UI, the desktop app and a viewer through the relay behave alike.
//
//   - A built-in acts here and answers with SlashCommandResult; no turn starts.
//   - A skill or custom command goes to the model with its text.
//   - An ACP agent's own commands go to the agent: it advertises them, and wins on a name.
//   - Any other /word stays a plain message, so a sentence starting with a path keeps working.

// Only with a text of its own: an attachment or a second block makes it a message.
type builtinCommand struct {
	name, description, hint string
	// needsSession: a conversation to act on. Not offered before one exists.
	needsSession bool
	// prompt: it starts a turn, with text of ours (package bootstrap); a user's skill of the name replaces it.
	prompt bool
	// acp: also on an ACP agent.
	acp bool
	// async: it takes a while. It runs apart from the message, and its result comes as an event when it's done.
	async bool
	run   func(ctx context.Context, s *Server, ls *liveSession, args string) slashOutcome
}

// slashOutcome is what a built-in did.
type slashOutcome struct {
	text    string
	isError bool
	action  agentv1.SlashCommandResult_Action
	session string
}

func slashError(format string, args ...any) slashOutcome {
	return slashOutcome{text: fmt.Sprintf(format, args...), isError: true}
}

// builtinCommands is the menu's order.
func builtinCommands() []builtinCommand {
	return []builtinCommand{
		{name: "compact", description: "Summarize the conversation to free up context", hint: "[what to focus on]", needsSession: true, run: runCompact},
		{name: "clear", description: "Start a new session here with an empty context", needsSession: true, acp: true, run: runClear},
		{name: "model", description: "Switch the session's model", hint: "[model]", acp: true, run: runModel},
		{name: "effort", description: "Set how hard the model thinks", hint: "[low|medium|high|xhigh|max|auto]", acp: true, run: runEffort},
		{name: bootstrap.CommandInit, description: "Write an instructions file for this repository", hint: "[what to emphasize]", prompt: true},
		{name: bootstrap.CommandCodeReview, description: "Review the current diff for bugs and list the findings", hint: "[range, files or focus]", prompt: true},
		{name: bootstrap.CommandDeepResearch, description: "Research a question across the code and the web, with sources", hint: "<question>", prompt: true},
		{name: bootstrap.CommandOrchestrate, description: "Split a large task, run the pieces in parallel agents, check the result", hint: "<task>", prompt: true},
		{name: bootstrap.CommandInterview, description: "Have the agent ask what it needs to know, then write it down", hint: "<what to work out>", prompt: true},
		{name: bootstrap.CommandRecap, description: "Say where this session stands: done, in progress, next", hint: "[what to focus on]", needsSession: true, prompt: true},
		{name: "plan", description: "Enter plan mode: read and propose, change nothing", hint: "[off]", needsSession: true, run: runPlan},
		{name: "goal", description: "Keep working until a condition holds, with a cap on the turns", hint: "[--max N] <condition> | stop | resume", needsSession: true, run: runGoal},
		{name: "loop", description: "Repeat a prompt or command here, on an interval or at the model's pace", hint: "[interval] <prompt or /command> | stop | list", needsSession: true, run: runLoop},
		{name: "schedule", description: "Open Scheduled: tasks that run by themselves", run: runScheduleCommand},
		{name: "context", description: "Show how much of the context window is in use", needsSession: true, run: runContext},
		{name: "usage", description: "Show the tokens and cost of this session and its agents", needsSession: true, run: runUsageCommand},
		{name: "insights", description: "Report how the agent was used on this machine", hint: "[days]", needsSession: true, run: runInsightsCommand},
		{name: "fewer-prompts", description: "Suggest rules from what past sessions of this folder were asked", hint: "[apply]", needsSession: true, run: runFewerPrompts},
		{name: "debug-config", description: "Show where this session's settings come from", needsSession: true, run: runDebugConfig},
		{name: "features", description: "List the client's switches and how they stand", needsSession: true, run: runFeatures},
		{name: "workflows", description: "List saved workflows, or save a run of this session as a command", hint: "[list] | save <run id> <name> [--project]", needsSession: true, run: runWorkflowsCommand},
		{name: "rename", description: "Rename this session", hint: "<title>", needsSession: true, run: runRename},
		{name: "skills", description: "Open Extensions: skills and commands, with their switches", acp: true, run: runSkills},
		{name: "plugin", description: "Install, approve and manage plugins and marketplaces", hint: "[list|info|install|approve|enable|disable|update|pin|remove|marketplace|new|validate]", acp: true, async: true, run: runPlugin},
		{name: "help", description: "List the commands of this session", acp: true, run: runHelp},
	}
}

func findBuiltin(name string) (builtinCommand, bool) {
	for _, b := range builtinCommands() {
		if b.name == name {
			return b, true
		}
	}
	return builtinCommand{}, false
}

// singleText returns the text of a message of one text block.
func singleText(content []llm.ContentBlock) (string, bool) {
	if len(content) == 1 && content[0].Type == llm.BlockText {
		return content[0].Text, true
	}
	return "", false
}

// runSlash handles a message the user sent. handled: nothing more is to be done with it. A skill leaves it unhandled, as
// the message itself still goes to the model, and gets a uuid for that.
func (s *Server) runSlash(ctx context.Context, ls *liveSession, req *connect.Request[agentv1.SendUserMessageRequest]) (handled bool) {
	content := pbconv.FromBlocks(req.Msg.GetContent())
	if len(content) == 0 || content[0].Type != llm.BlockText {
		return false
	}
	name, args, ok := skills.ParseSlash(content[0].Text)
	if !ok {
		return false
	}
	_, plain := singleText(content)
	id := req.Msg.GetUuid()
	if id != "" {
		ls.mu.Lock()
		dup := ls.seen[id]
		ls.mu.Unlock()
		if dup {
			return false // the same message again: submitMessage knows what that means
		}
	}
	finish := func(o slashOutcome) bool {
		if id != "" {
			ls.mu.Lock()
			ls.seen[id] = true
			ls.mu.Unlock()
		}
		ls.publishSlashResult(id, name, o)
		return true
	}
	b, isBuiltin := findBuiltin(name)
	if isBuiltin && !b.prompt {
		if !plain {
			return false
		}
		if !localPeer(req.Peer().Addr) {
			ctx = WithRemoteOrigin(ctx)
		}
		if b.async {
			ctx := context.WithoutCancel(ctx)
			if id != "" {
				ls.mu.Lock()
				ls.seen[id] = true
				ls.mu.Unlock()
			}
			go func() { ls.publishSlashResult(id, name, b.run(ctx, s, ls, args)) }()
			return true
		}
		return finish(b.run(ctx, s, ls, args))
	}
	if id == "" {
		id = uuid.NewString()
		req.Msg.Uuid = id
	}
	if sk, found := ls.sess.UserSkill(name); found {
		if !ls.sess.SkillEnabled(sk.Name) {
			return finish(slashError("/%s is turned off in Extensions.", sk.Name))
		}
		ls.sess.StartUserSkill(sk, args, id)
		return false
	}
	if isBuiltin && plain {
		if refusal := ls.sess.StartPromptCommand(ctx, name, args, id); refusal != "" {
			return finish(slashOutcome{text: refusal, isError: !strings.HasPrefix(refusal, "Nothing to review")})
		}
	}
	if !isBuiltin && plain && ls.sess.Workflows != nil && ls.sess.Workflows.HasSaved(name) {
		if refusal := ls.sess.StartWorkflowCommand(ctx, name, args, id); refusal != "" {
			return finish(slashOutcome{text: refusal, isError: true})
		}
	}
	return false
}

func (ls *liveSession) publishSlashResult(id, name string, o slashOutcome) {
	ls.publish(&agentv1.SessionEvent{Event: slashResultEvent(id, name, o)})
}

func slashResultEvent(id, name string, o slashOutcome) *agentv1.SessionEvent_SlashCommandResult {
	return &agentv1.SessionEvent_SlashCommandResult{SlashCommandResult: &agentv1.SlashCommandResult{
		Uuid: id, Name: name, Text: o.text, IsError: o.isError, Action: o.action, NewSessionId: o.session}}
}

func runCompact(_ context.Context, _ *Server, ls *liveSession, args string) slashOutcome {
	if err := ls.compactCommand(args); err != nil {
		return slashError("%s", connectMessage(err))
	}
	return slashOutcome{}
}

func connectMessage(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return err.Error()
}

func runClear(ctx context.Context, s *Server, ls *liveSession, _ string) slashOutcome {
	if ls.driver.Busy() {
		return slashError("/clear starts a new session when this one is idle; wait for the current turn to finish.")
	}
	req := &agentv1.CreateSessionRequest{Model: ls.sess.Agent.Model(), PermissionMode: modeToProto(ls.sess.Permissions.Mode()), Effort: pbconv.Effort(ls.sess.Agent.Effort())}
	if !isNoFolder(ls.sess.ID, ls.sess.Dir()) {
		req.Cwd = ls.sess.Dir()
	}
	res, err := s.CreateSession(ctx, connect.NewRequest(req))
	if err != nil {
		return slashError("%s", connectMessage(err))
	}
	return slashOutcome{text: "Started a new session; this one stays in the list.", action: agentv1.SlashCommandResult_ACTION_OPEN_SESSION, session: res.Msg.GetSession().GetId()}
}

func runModel(ctx context.Context, s *Server, ls *liveSession, args string) slashOutcome {
	if args == "" {
		return slashOutcome{action: agentv1.SlashCommandResult_ACTION_PICK_MODEL}
	}
	m, ok, ambiguous := ls.sess.ResolveModel(args)
	id := m.ID
	if !ok {
		if ambiguous {
			return slashError("%q matches several models; name one exactly.", args)
		}
		id = args // a local model detected after the session opened: switchModel knows
	}
	display, err := s.switchModel(ctx, ls, id)
	if err != nil {
		return slashError("%s", connectMessage(err))
	}
	return slashOutcome{text: "Model: " + display}
}

// effortLevels are the levels a person can name.
var effortLevels = []string{"low", "medium", "high", "xhigh", "max"}

func runEffort(_ context.Context, _ *Server, ls *liveSession, args string) slashOutcome {
	if args == "" {
		return slashOutcome{action: agentv1.SlashCommandResult_ACTION_PICK_EFFORT}
	}
	level := strings.ToLower(strings.TrimSpace(args))
	if level == "auto" || level == "default" {
		ls.sess.Agent.SetEffort("")
		ls.publishInfo()
		return slashOutcome{text: "Effort: the model's default"}
	}
	if !slices.Contains(effortLevels, level) {
		return slashError("%q isn't an effort level: %s or auto.", args, strings.Join(effortLevels, ", "))
	}
	if ls.sess.Catalog != nil {
		if m, ok := ls.sess.Catalog.Find(ls.sess.Agent.Model()); ok && !slices.Contains(m.Capabilities.Effort, level) {
			if len(m.Capabilities.Effort) == 0 {
				return slashError("%s has no effort levels.", m.ID)
			}
			return slashError("%s has no %s effort: %s.", m.ID, level, strings.Join(m.Capabilities.Effort, ", "))
		}
	}
	ls.sess.Agent.SetEffort(level)
	ls.publishInfo()
	return slashOutcome{text: "Effort: " + level}
}

func runPlan(_ context.Context, _ *Server, ls *liveSession, args string) slashOutcome {
	mode := permissions.ModePlan
	text := "Plan mode is on: the model reads and proposes, and changes nothing."
	if strings.EqualFold(strings.TrimSpace(args), "off") {
		mode, text = ls.sess.Permissions.PrePlanMode(), "Plan mode is off."
		if mode == "" || mode == permissions.ModePlan {
			mode = permissions.ModeDefault
		}
	}
	if err := ls.sess.Permissions.SetMode(mode); err != nil {
		return slashError("%v", err)
	}
	return slashOutcome{text: text}
}

func runContext(_ context.Context, _ *Server, ls *liveSession, _ string) slashOutcome {
	u := ls.sess.Agent.ContextUsage()
	used := u.SystemPrompt + u.SystemTools + u.MCPTools + u.Messages
	if u.Window > 0 {
		return slashOutcome{text: fmt.Sprintf("Context: %d of %d tokens in use (%d%%).", used, u.Window, used*100/u.Window)}
	}
	return slashOutcome{text: fmt.Sprintf("Context: %d tokens in use.", used)}
}

// sendText sends text to the session as a message of the user (a command's own words: what the user typed goes on as it).
func (s *Server) sendText(ctx context.Context, ls *liveSession, text string) error {
	_, err := s.SendUserMessage(ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: ls.sess.ID, Uuid: uuid.NewString(), Content: pbconv.Blocks([]llm.ContentBlock{llm.TextBlock(text)})}))
	return err
}

func runRename(ctx context.Context, s *Server, ls *liveSession, args string) slashOutcome {
	if args == "" {
		return slashError("Say the new title: /rename <title>.")
	}
	if _, err := s.UpdateSession(ctx, connect.NewRequest(&agentv1.UpdateSessionRequest{SessionId: ls.sess.ID, Title: &args})); err != nil {
		return slashError("%s", connectMessage(err))
	}
	return slashOutcome{text: "Renamed."}
}

func runSkills(context.Context, *Server, *liveSession, string) slashOutcome {
	return slashOutcome{action: agentv1.SlashCommandResult_ACTION_OPEN_CUSTOMIZE}
}

func runHelp(ctx context.Context, s *Server, ls *liveSession, _ string) slashOutcome {
	res, err := s.ListSlashCommands(ctx, connect.NewRequest(&agentv1.ListSlashCommandsRequest{SessionId: ls.sess.ID}))
	if err != nil {
		return slashError("%s", connectMessage(err))
	}
	return slashOutcome{text: helpText(res.Msg.GetCommands())}
}

func helpText(list []*agentv1.SlashCommand) string {
	var b strings.Builder
	b.WriteString("Commands:")
	for _, c := range list {
		if !c.GetEnabled() || !c.GetUserInvocable() || c.GetShadowedBy() != "" {
			continue
		}
		b.WriteString("\n/" + c.GetName())
		if c.GetArgumentHint() != "" {
			b.WriteString(" " + c.GetArgumentHint())
		}
		if c.GetDescription() != "" {
			b.WriteString(": " + c.GetDescription())
		}
	}
	return b.String()
}

// ---- the list ----

// skillDocs is what the server stores for the user, for a listing without a session: read at most this often, and again
// after an import.
const skillDocsTTL = 15 * time.Second

// SkillDocsSource reads documents from the server (implemented by *syncclient.Client).
type SkillDocsSource interface {
	ListChanges(ctx context.Context, req *syncv1.ListChangesRequest) (*syncv1.ListChangesResponse, error)
}

func (s *Server) serverSkillDocs(ctx context.Context) skills.DocSource {
	if s.Base.SkillDocs != nil {
		return s.Base.SkillDocs
	}
	if s.SkillDocs == nil {
		return nil
	}
	s.skillDocsMu.Lock()
	defer s.skillDocsMu.Unlock()
	if s.skillDocsCache != nil && time.Since(s.skillDocsAt) < skillDocsTTL {
		return s.skillDocsCache
	}
	ctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
	defer cancel()
	docs := skills.Documents{}
	token := ""
	for {
		res, err := s.SkillDocs.ListChanges(ctx, &syncv1.ListChangesRequest{PageSize: 200, PageToken: token,
			Kinds: []syncv1.DocumentKind{syncv1.DocumentKind_DOCUMENT_KIND_SKILL, syncv1.DocumentKind_DOCUMENT_KIND_COMMAND}})
		if err != nil {
			// Nothing new: what's known stays, and files on disk still list.
			s.log().Debug("skills on the server unavailable", "err", err)
			if s.skillDocsCache != nil {
				return s.skillDocsCache
			}
			return nil
		}
		for _, c := range res.GetChanges() {
			if d := c.GetDocument(); d != nil {
				docs = append(docs, d)
			}
		}
		if !res.GetHasMore() || res.GetNextPageToken() == "" {
			break
		}
		token = res.GetNextPageToken()
	}
	s.skillDocsCache, s.skillDocsAt = docs, time.Now()
	return docs
}

// forgetSkillDocs makes the next listing read the server again.
func (s *Server) forgetSkillDocs() {
	s.skillDocsMu.Lock()
	s.skillDocsCache = nil
	s.skillDocsMu.Unlock()
}

func disabledFromDocument(doc *syncv1.Document) map[string]bool {
	out := map[string]bool{}
	for _, n := range preferencesOf(doc).GetDisabledSkills() {
		out[n] = true
	}
	return out
}

// folderSkills reads the skills of a folder without a session. Empty: only what isn't tied to one.
func (s *Server) folderSkills(ctx context.Context, cwd string) *skills.Manager {
	trusted := cwd != "" && permissions.IsTrusted(cwd)
	if s.Base.Trusted != nil {
		trusted = cwd != "" && *s.Base.Trusted
	}
	disabled := map[string]bool{}
	if s.Preferences != nil {
		pctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
		if doc, err := s.userSettings(pctx); err == nil {
			disabled = disabledFromDocument(doc)
		}
		cancel()
	}
	opt := bootstrap.SkillOptions(bootstrap.SkillHomeDir(s.Base), cwd, trusted, s.serverSkillDocs(ctx), disabled)
	opt.Allowed = s.Base.ModePolicy.Managed().SkillAllowed
	return skills.NewManager(skills.ManagerOptions{Place: func() (string, bool) { return cwd, trusted }, Options: func() skills.Options { return opt }})
}

// ListSlashCommands implements AgentService.ListSlashCommands.
func (s *Server) ListSlashCommands(ctx context.Context, req *connect.Request[agentv1.ListSlashCommandsRequest]) (*connect.Response[agentv1.ListSlashCommandsResponse], error) {
	var m *skills.Manager
	var saved []workflow.SavedWorkflow
	window := 0
	if id := req.Msg.GetSessionId(); id != "" {
		ls, err := s.get(id)
		if err != nil {
			return nil, err
		}
		m = ls.sess.Skills
		if ls.sess.Workflows != nil {
			saved = ls.sess.Workflows.Saved()
		}
		if info, ok := ls.sess.Catalog.Find(ls.sess.Agent.Model()); ok {
			window = info.ContextWindow
		}
	} else {
		m = s.folderSkills(ctx, req.Msg.GetCwd())
	}
	_, report := m.Listing(window, true)
	return connect.NewResponse(slashCatalog(m, report, saved)), nil
}

func skillKind(k skills.Kind) agentv1.SlashCommandKind {
	if k == skills.KindCommand {
		return agentv1.SlashCommandKind_SLASH_COMMAND_KIND_COMMAND
	}
	return agentv1.SlashCommandKind_SLASH_COMMAND_KIND_SKILL
}

func slashCatalog(m *skills.Manager, report skills.Report, saved []workflow.SavedWorkflow) *agentv1.ListSlashCommandsResponse {
	cat := m.Catalog(false)
	out := &agentv1.ListSlashCommandsResponse{ProjectSkippedUntrusted: cat.UntrustedProject, ListingBudgetChars: int32(report.Budget), ListingUsedChars: int32(report.Used)}
	replaced := func(b builtinCommand) bool {
		sk, ok := cat.Lookup(b.name)
		return ok && b.prompt && sk.UserInvocable
	}
	for _, b := range builtinCommands() {
		if !replaced(b) {
			out.Commands = append(out.Commands, builtinProto(b))
		}
	}
	for _, w := range saved {
		if _, taken := cat.Lookup(w.Name); taken {
			continue
		}
		if _, taken := findBuiltin(w.Name); taken {
			continue
		}
		out.Commands = append(out.Commands, &agentv1.SlashCommand{Name: w.Name, Description: w.Description, Kind: agentv1.SlashCommandKind_SLASH_COMMAND_KIND_COMMAND, Source: "workflow", ArgumentHint: "[arguments]", Path: w.Path, Enabled: true, UserInvocable: true, NeedsSession: true})
	}
	for _, sk := range cat.All {
		c := &agentv1.SlashCommand{Name: sk.Name, Description: sk.Description, Kind: skillKind(sk.Kind), Source: sk.Source, ArgumentHint: sk.ArgumentHint, Path: sk.Path,
			Enabled: cat.Enabled(sk.Name), UserInvocable: sk.UserInvocable, ModelInvocable: !sk.DisableModelInvocation, AllowedTools: sk.AllowedTools,
			Model: sk.Model, Effort: sk.Effort, Fork: sk.Fork, Paths: sk.Paths, SkippedFields: sk.Skipped, Problems: sk.Problems, Listed: report.Listed[sk.Name] && sk.ShadowedBy == "", ShadowedBy: sk.ShadowedBy, Switchable: true}
		if b, ok := findBuiltin(sk.Name); ok && !b.prompt {
			c.ShadowedBy, c.Listed = "builtin", false
			c.Problems = append(c.Problems, "A built-in command has this name, so typing it runs the built-in. The model can still run this one.")
		}
		out.Commands = append(out.Commands, c)
	}
	return out
}

func skillInvokedProto(u *session.SkillUse) *agentv1.SkillInvoked {
	kind := agentv1.SlashCommandKind_SLASH_COMMAND_KIND_SKILL
	switch u.Kind {
	case string(skills.KindCommand):
		kind = agentv1.SlashCommandKind_SLASH_COMMAND_KIND_COMMAND
	case "builtin":
		kind = agentv1.SlashCommandKind_SLASH_COMMAND_KIND_BUILTIN
	}
	return &agentv1.SkillInvoked{Name: u.Name, Kind: kind, Source: u.Source, Arguments: u.Args, ToolUseId: u.ToolUseID,
		AllowedTools: u.AllowedTools, Model: u.Model, Effort: u.Effort, AgentId: u.AgentID}
}

func builtinProto(b builtinCommand) *agentv1.SlashCommand {
	return &agentv1.SlashCommand{Name: b.name, Description: b.description, Kind: agentv1.SlashCommandKind_SLASH_COMMAND_KIND_BUILTIN, Source: "builtin",
		ArgumentHint: b.hint, Enabled: true, UserInvocable: true, NeedsSession: b.needsSession}
}

// ListSlashCommands routes a session on another machine to its host. Without a session it's this machine's folder.
func (r routed) ListSlashCommands(ctx context.Context, req *connect.Request[agentv1.ListSlashCommandsRequest]) (*connect.Response[agentv1.ListSlashCommandsResponse], error) {
	if req.Msg.GetSessionId() == "" {
		return r.Server.ListSlashCommands(ctx, req)
	}
	return forward(ctx, r, req.Msg.GetSessionId(), req, r.Server.ListSlashCommands, relayv1connect.RelayServiceClient.ListSlashCommands,
		func(m *agentv1.ListSlashCommandsRequest) *relayv1.ListSlashCommandsRequest {
			return &relayv1.ListSlashCommandsRequest{Request: m}
		},
		func(res *relayv1.ListSlashCommandsResponse) *agentv1.ListSlashCommandsResponse {
			return res.GetResponse()
		})
}

// ListSlashCommands of a session on an ACP agent: the agent's commands, and the built-ins that act on the session rather
// than on the agent's conversation. Skills belong to the client's own loop: the agent has its own.
func (r acpRouted) ListSlashCommands(ctx context.Context, req *connect.Request[agentv1.ListSlashCommandsRequest]) (*connect.Response[agentv1.ListSlashCommandsResponse], error) {
	l, err := r.acpTarget(req.Msg.GetSessionId())
	if req.Msg.GetSessionId() == "" || err != nil || l == nil {
		if err != nil && req.Msg.GetSessionId() != "" {
			return nil, err
		}
		return r.routed.ListSlashCommands(ctx, req)
	}
	out := &agentv1.ListSlashCommandsResponse{}
	own := map[string]bool{}
	for _, c := range l.sess.State().Commands {
		own[c.Name] = true
		out.Commands = append(out.Commands, &agentv1.SlashCommand{Name: c.Name, Description: c.Description, Kind: agentv1.SlashCommandKind_SLASH_COMMAND_KIND_AGENT, Source: "agent",
			ArgumentHint: c.InputHint, Enabled: true, UserInvocable: true, NeedsSession: true})
	}
	for _, b := range builtinCommands() {
		if b.acp && !own[b.name] {
			out.Commands = append(out.Commands, builtinProto(b))
		}
	}
	return connect.NewResponse(out), nil
}

// acpSlash runs the built-ins of a session on an ACP agent. Anything else goes to the agent, as the prompt's text.
func (r acpRouted) acpSlash(ctx context.Context, l *acpLive, req *connect.Request[agentv1.SendUserMessageRequest]) bool {
	content := pbconv.FromBlocks(req.Msg.GetContent())
	text, ok := singleText(content)
	if !ok {
		return false
	}
	name, args, ok := skills.ParseSlash(text)
	if !ok {
		return false
	}
	b, isBuiltin := findBuiltin(name)
	if !isBuiltin || !b.acp {
		return false
	}
	for _, c := range l.sess.State().Commands {
		if c.Name == name {
			return false
		}
	}
	id := req.Msg.GetUuid() // SendUserMessage has already dealt with the same message arriving again
	var o slashOutcome
	switch name {
	case "clear":
		o = r.acpClear(ctx, l)
	case "model":
		o = r.acpSetting(ctx, l, args, agentv1.SlashCommandResult_ACTION_PICK_MODEL, func() error {
			_, err := r.SetModel(ctx, connect.NewRequest(&agentv1.SetModelRequest{SessionId: l.id, Model: args}))
			return err
		}, "Model: "+args)
	case "effort":
		o = r.acpSetting(ctx, l, args, agentv1.SlashCommandResult_ACTION_PICK_EFFORT, func() error {
			_, err := r.SetEffort(ctx, connect.NewRequest(&agentv1.SetEffortRequest{SessionId: l.id, Effort: pbconv.Effort(args)}))
			return err
		}, "Effort: "+args)
	case "skills":
		o = slashOutcome{action: agentv1.SlashCommandResult_ACTION_OPEN_CUSTOMIZE}
	case "plugin":
		if !localPeer(req.Peer().Addr) {
			ctx = WithRemoteOrigin(ctx)
		}
		ctx := context.WithoutCancel(ctx)
		go func() {
			l.Publish(&agentv1.SessionEvent{Event: slashResultEvent(id, name, pluginCommand(ctx, r.Server, l.info().GetCwd(), args))})
		}()
		return true
	case "help":
		res, err := r.ListSlashCommands(ctx, connect.NewRequest(&agentv1.ListSlashCommandsRequest{SessionId: l.id}))
		if err != nil {
			o = slashError("%s", connectMessage(err))
		} else {
			o = slashOutcome{text: helpText(res.Msg.GetCommands())}
		}
	}
	l.Publish(&agentv1.SessionEvent{Event: slashResultEvent(id, name, o)})
	return true
}

func (r acpRouted) acpSetting(_ context.Context, _ *acpLive, args string, pick agentv1.SlashCommandResult_Action, set func() error, done string) slashOutcome {
	if args == "" {
		return slashOutcome{action: pick}
	}
	if err := set(); err != nil {
		return slashError("%s", connectMessage(err))
	}
	return slashOutcome{text: done}
}

func (r acpRouted) acpClear(ctx context.Context, l *acpLive) slashOutcome {
	info := l.info()
	req := &agentv1.CreateSessionRequest{Engine: info.GetEngine(), Model: info.GetModel()}
	if !info.GetNoFolder() {
		req.Cwd = info.GetCwd()
	}
	res, err := r.CreateSession(ctx, connect.NewRequest(req))
	if err != nil {
		return slashError("%s", connectMessage(err))
	}
	return slashOutcome{text: "Started a new session; this one stays in the list.", action: agentv1.SlashCommandResult_ACTION_OPEN_SESSION, session: res.Msg.GetSession().GetId()}
}

// ---- the switch ----

// flipDisabled is the settings value after one switch: the list as it stands, with name added or removed. It reads the
// current value on every try of the merge, so another machine's flip isn't undone.
func flipDisabled(name string, enabled bool) func(json.RawMessage) any {
	return func(current json.RawMessage) any {
		var list []string
		_ = json.Unmarshal(current, &list)
		list = slices.DeleteFunc(list, func(n string) bool { return n == name })
		if !enabled {
			list = append(list, name)
		}
		slices.Sort(list)
		return nonNil(list)
	}
}

// applySkillSwitch tells the open sessions at once: their synced settings follow later.
func (s *Server) applySkillSwitch(name string, enabled bool) {
	s.mu.Lock()
	open := make([]*liveSession, 0, len(s.live))
	for _, ls := range s.live {
		open = append(open, ls)
	}
	s.mu.Unlock()
	for _, ls := range open {
		ls.sess.Skills.SetEnabled(name, enabled)
	}
}
