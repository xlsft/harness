package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/hooks"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/skills"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Skills and custom commands in a session: where they are looked for, and what starting one does. The format and the
// listing are package skills'; when the text reaches the model is package agent's.

// Built-in commands that are prompts: they work like a skill, with text of our own from the bundle. A user's skill of
// the same name replaces them.
const (
	CommandInit       = "init"
	CommandCodeReview = "code-review"

	reminderCommandInit       = "command_init"
	reminderCommandCodeReview = "command_code_review"
)

// commandGitTimeout bounds each git call that describes the diff to review.
const commandGitTimeout = 10 * time.Second

// SkillOptions says where to look for a folder, with or without a session. docs and disabled may be nil.
func SkillOptions(home, cwd string, trusted bool, docs skills.DocSource, disabled map[string]bool) skills.Options {
	gitRoot := ""
	if cwd != "" {
		ctx, cancel := context.WithTimeout(context.Background(), commandGitTimeout)
		defer cancel()
		gitRoot = workspace.GitRoot(ctx, cwd)
	}
	return skills.Options{Builtin: true, Home: home, HarnessHome: config.Home(), Cwd: cwd, GitRoot: gitRoot, Trusted: trusted, Docs: docs, Disabled: disabled, Plugins: pluginSkillSources(cwd, trusted)}
}

// DisabledSkills reads the user's switches from settings.
func DisabledSkills(st *settings.Settings) map[string]bool {
	out := map[string]bool{}
	if st == nil {
		return out
	}
	var names []string
	if raw, ok := st.Raw[settings.KeyDisabledSkills]; ok && json.Unmarshal(raw, &names) == nil {
		for _, n := range names {
			out[n] = true
		}
	}
	return out
}

// SkillHomeDir is the home folder whose .claude holds personal skills.
func SkillHomeDir(o Options) string {
	if o.SkillHome != "" {
		return o.SkillHome
	}
	return homeDir()
}

func (s *Session) loadSettings() *settings.Settings {
	var docs settings.DocSource
	if s.Sync != nil {
		docs = s.Sync.Cache()
	}
	st, err := settings.Load(context.Background(), settings.Options{Cwd: s.Dir(), Docs: docs, Trusted: s.IsTrusted()})
	if err != nil {
		return nil
	}
	return st
}

// openSkills sets up the session's skills; nothing is read until they are first needed.
func (s *Session) openSkills(o Options) {
	var docs skills.DocSource
	switch {
	case o.SkillDocs != nil:
		docs = o.SkillDocs
	case s.Sync != nil:
		docs = s.Sync.Cache()
	}
	home := SkillHomeDir(o)
	s.Skills = skills.NewManager(skills.ManagerOptions{
		Place: func() (string, bool) { return s.Dir(), s.IsTrusted() },
		Options: func() skills.Options {
			opt := SkillOptions(home, s.Dir(), s.IsTrusted(), docs, DisabledSkills(s.loadSettings()))
			opt.AdditionalDirs = s.ToolEnv.WorkingDirs()
			return opt
		},
		Hidden: func(name string) bool {
			if s.Permissions == nil {
				return false
			}
			input, _ := json.Marshal(map[string]string{"skill": name})
			return s.Permissions.Evaluate(permissions.Request{ToolName: "Skill", Input: input, ReadOnly: true}).Behavior == permissions.Deny
		},
	})
	s.ToolEnv.Skills = skillRunner{s}
}

// skillSource is the agent's view of the session's skills.
type skillSource struct{ m *skills.Manager }

func (x skillSource) Listing(window int, refresh bool) string {
	text, _ := x.m.Listing(window, refresh)
	return text
}

func (x skillSource) NoteFile(path string) { x.m.NoteFile(path) }

func (s *Session) skillSource() agent.SkillSource { return skillSource{s.Skills} }

// UserSkill returns what `/name` runs: the winner of that name, when the user can run it at all.
func (s *Session) UserSkill(name string) (skills.Skill, bool) {
	sk, ok := s.Skills.Catalog(true).Lookup(name)
	if !ok || !sk.UserInvocable {
		return skills.Skill{}, false
	}
	return sk, true
}

// SkillEnabled reports the user's switch.
func (s *Session) SkillEnabled(name string) bool { return s.Skills.Catalog(false).Enabled(name) }

// StartUserSkill starts what the user typed. messageID is the message it came with: the skill goes if the message is
// taken back.
func (s *Session) StartUserSkill(sk skills.Skill, args, messageID string) {
	act, warnings := s.skillActivation(sk, args, "", s.ToolEnv)
	act.MessageID = messageID
	s.warnAll(warnings)
	s.Agent.ActivateSkill(act)
}

func (s *Session) warnAll(warnings []string) {
	for _, w := range warnings {
		s.Agent.Emit(agent.Event{Kind: agent.EventNotice, Notice: &agent.Notice{Level: "warning", Kind: "skill", Text: w}})
	}
}

// ResolveModel finds a model of the catalog by what a person or a frontmatter calls it: its id, its display name, or a
// part of either that only it has. ambiguous reports several candidates.
func (s *Session) ResolveModel(query string) (m llm.Model, ok, ambiguous bool) {
	if s.Catalog == nil {
		return llm.Model{}, false, false
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return llm.Model{}, false, false
	}
	if m, ok := s.Catalog.Find(query); ok {
		return m, true, false
	}
	var partial []llm.Model
	for _, c := range s.Catalog.Models {
		id, name := strings.ToLower(c.ID), strings.ToLower(c.DisplayName)
		if id == q || name == q {
			return c, true, false
		}
		if strings.Contains(id, q) || strings.Contains(name, q) {
			partial = append(partial, c)
		}
	}
	if len(partial) == 1 {
		return partial[0], true, false
	}
	return llm.Model{}, false, len(partial) > 1
}

// EffortFor fits a frontmatter's effort to the model: the level when the model has it, "" otherwise, with why.
func effortFor(level string, m llm.Model) (string, string) {
	if level == "" {
		return "", ""
	}
	if !slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, level) {
		return "", fmt.Sprintf("effort %q isn't a level", level)
	}
	if len(m.Capabilities.Effort) > 0 && !slices.Contains(m.Capabilities.Effort, level) {
		return "", fmt.Sprintf("%s has no %q effort", m.ID, level)
	}
	return level, ""
}

// skillActivation prepares a skill to start. warnings say which of its wishes can't be met; it runs anyway.
func (s *Session) skillActivation(sk skills.Skill, args, toolUseID string, env *tools.Env) (*agent.SkillActivation, []string) {
	var warnings []string
	use := session.SkillUse{Name: sk.Name, Kind: string(sk.Kind), Source: sk.Source, Args: args, ToolUseID: toolUseID, AllowedTools: sk.AllowedTools}
	act := &agent.SkillActivation{}
	model, known := llm.Model{}, false
	if s.Catalog != nil {
		model, known = s.Catalog.Find(s.Agent.Model())
	}
	if sk.Model != "" {
		if m, ok, _ := s.ResolveModel(sk.Model); ok {
			act.Model = &agent.TurnModel{ID: m.ID, Display: m.DisplayName, Thinking: m.Capabilities.Thinking}
			use.Model = m.ID
			model, known = m, true
		} else {
			warnings = append(warnings, fmt.Sprintf("/%s asks for model %q, which isn't available; it runs on the session's model.", sk.Name, sk.Model))
		}
	}
	if sk.Effort != "" && known {
		level, why := effortFor(sk.Effort, model)
		if why != "" {
			warnings = append(warnings, fmt.Sprintf("/%s: %s; it runs on the session's effort.", sk.Name, why))
		}
		act.Effort, use.Effort = level, level
	}
	act.Use = use
	forks := sk.Fork && env.Agents != nil
	scoped, hookWarnings := s.skillHooks(sk)
	warnings = append(warnings, hookWarnings...)
	if !forks && (len(sk.AllowedTools) > 0 || len(scoped) > 0) {
		act.Grant = func() func() {
			releaseRules := s.Permissions.AddScopedAllow(sk.AllowedTools)
			releaseHooks := s.Hooks.AddScoped(scoped, "")
			return func() {
				releaseHooks()
				releaseRules()
			}
		}
	}
	act.Prepare = func(ctx context.Context) (string, string, string) {
		text, dir := s.skillText(ctx, sk, args, env)
		if !forks {
			return text, dir, ""
		}
		return s.forkSkill(ctx, sk, text, dir, toolUseID, use.Model, env, scoped)
	}
	return act, warnings
}

// skillText reads a skill's body and fills it in; its folder becomes readable.
func (s *Session) skillText(ctx context.Context, sk skills.Skill, args string, env *tools.Env) (text, dir string) {
	dir, err := sk.Materialize(filepath.Join(config.Home(), "skills-cache"))
	if err != nil {
		return "This skill's files could not be prepared: " + err.Error(), ""
	}
	if dir != "" {
		s.Permissions.AddReadableDir(dir)
	}
	if sk.PluginRoot != "" {
		s.Permissions.AddReadableDir(sk.PluginRoot)
	}
	body, err := sk.LoadBody()
	if err != nil {
		return "This skill's text could not be read: " + err.Error(), dir
	}
	opt := skills.ExpandOptions{Arguments: args, SessionID: s.ID, Dir: dir}
	if st := s.loadSettings(); st == nil || !settingFlag(st, settings.KeyDisableSkillShell) {
		opt.Shell = func(ctx context.Context, command string) string { return s.skillShell(ctx, sk, command, env) }
	}
	return skills.Expand(ctx, sk, body, opt), dir
}

func settingFlag(st *settings.Settings, key string) bool {
	var on bool
	raw, ok := st.Raw[key]
	return ok && json.Unmarshal(raw, &on) == nil && on
}

// skillShell runs a command of a skill's body, under the same check as the model's commands: the rules, the mode and
// the skill's allowed-tools decide, and the sandbox confines it. Refused, its place says so and the model reads on.
func (s *Session) skillShell(ctx context.Context, sk skills.Skill, command string, env *tools.Env) string {
	input, _ := json.Marshal(map[string]any{"command": command, "description": "Command in the skill " + sk.Name})
	id := "skill-shell-" + session.NewID()
	d := s.Permissions.Check(ctx, permissions.Request{ToolName: "Bash", ToolUseID: id, Input: input, Command: command, Cwd: env.Shell.Cwd(), SessionID: s.ID, Label: "Command in the skill " + sk.Name})
	if d.Behavior != permissions.Allow {
		return "[command not run: " + oneLine(d.Message) + "]"
	}
	r := (&tools.Bash{}).Run(ctx, env, tools.Call{ID: id, Input: input})
	var out []string
	for _, b := range r.Content {
		if b.Type == llm.BlockText {
			out = append(out, b.Text)
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// forkSkill runs a skill's text in a subagent. Its allowed-tools last until the agent ends: nothing else knows when
// that is, so the session's event stream releases them (releaseSkillFork).
func (s *Session) forkSkill(ctx context.Context, sk skills.Skill, text, dir, toolUseID, model string, env *tools.Env, scoped []*hooks.Hook) (string, string, string) {
	releaseRules := s.Permissions.AddScopedAllow(sk.AllowedTools)
	releaseHooks := s.Hooks.AddScoped(scoped, "")
	release := func() {
		releaseHooks()
		releaseRules()
	}
	if dir != "" {
		text = "This skill's folder is " + dir + ".\n\n" + text
	}
	info, err := env.Agents.Spawn(ctx, env.AgentID, tools.AgentSpec{Description: "Skill " + sk.Name, Prompt: text, Type: sk.Agent, Model: model, ToolUseID: toolUseID})
	if err != nil {
		release()
		return "This skill runs in a subagent, which could not start: " + err.Error(), dir, ""
	}
	s.skillForks.add(info.ID, release)
	if toolUseID != "" { // the Skill tool's call: the model started it; a command the user typed is not plan work
		tools.LinkTodoAgent(env, info.ID)
	}
	if got, ok := env.Agents.Get(info.ID); ok && !got.State.Active() {
		s.skillForks.done(info.ID)
	}
	return fmt.Sprintf("This skill runs in the subagent %s, which has its text. Its report reaches you when it ends; don't do its work yourself meanwhile.", info.ID), dir, info.ID
}

// skillForkGrants holds the permission grants of skills running in subagents, by agent id.
type skillForkGrants struct {
	mu sync.Mutex
	m  map[string]func()
}

func (g *skillForkGrants) add(id string, release func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = map[string]func(){}
	}
	g.m[id] = release
}

func (g *skillForkGrants) done(id string) {
	g.mu.Lock()
	release := g.m[id]
	delete(g.m, id)
	g.mu.Unlock()
	if release != nil {
		release()
	}
}

// watchSkillForks ends the grants of a forked skill with its agent.
func (s *Session) watchSkillForks(next func(agent.Event)) func(agent.Event) {
	return func(e agent.Event) {
		if e.Kind == agent.EventSubagent && e.Subagent != nil && !e.Subagent.State.Active() {
			s.skillForks.done(e.Subagent.ID)
			s.agentHookGrants.done(e.Subagent.ID)
		}
		if next != nil {
			next(e)
		}
	}
}

// skillRunner serves the Skill tool.
type skillRunner struct{ s *Session }

// skillHost is what the agent of a tool call offers.
type skillHost interface {
	ActivateSkill(*agent.SkillActivation)
}

func (r skillRunner) find(name string) (skills.Skill, bool) {
	c := r.s.Skills.Catalog(false)
	sk, ok := c.Lookup(name)
	if !ok {
		c = r.s.Skills.Catalog(true)
		if sk, ok = c.Lookup(name); !ok {
			return skills.Skill{}, false
		}
	}
	return sk, r.s.Skills.ModelInvocable(c, sk)
}

// SkillInfo implements tools.SkillRunner.
func (r skillRunner) SkillInfo(name string) tools.SkillInfo {
	sk, ok := r.find(name)
	return tools.SkillInfo{Runnable: ok, Harmless: ok && len(sk.AllowedTools) == 0 && !sk.Fork && len(sk.Hooks) == 0}
}

// RunSkill implements tools.SkillRunner.
func (r skillRunner) RunSkill(_ context.Context, env *tools.Env, toolUseID, name, args string) tools.Result {
	s := r.s
	sk, ok := r.find(name)
	if !ok {
		c := s.Skills.Catalog(false)
		if got, exists := c.Lookup(name); exists {
			switch {
			case !c.Enabled(name):
				return tools.ErrorResult("The user turned the skill %q off.", name)
			case got.DisableModelInvocation:
				return tools.ErrorResult("Only the user can run %q, with /%s.", name, name)
			}
			return tools.ErrorResult("Permission to use the skill %q has been denied.", name)
		}
		return tools.ErrorResult("There is no skill named %q. The skills you can run are in your listing.", name)
	}
	host, isHost := env.Host().(skillHost)
	if !isHost {
		return tools.ErrorResult("Skills can't start here.")
	}
	act, warnings := s.skillActivation(sk, args, toolUseID, env)
	s.warnAll(warnings)
	host.ActivateSkill(act)
	res := tools.TextResult(fmt.Sprintf("The skill %q has started; its instructions are in the next message. Follow them.", sk.Name), map[string]any{"skill": sk.Name, "source": sk.Source, "args": args})
	res.Summary = sk.Name
	return res
}

// StartSlashPrompt starts what a headless prompt's `/name args` asks for: a skill, a custom command or a built-in that is a
// prompt. Other text is left as a plain prompt. refusal, when set, is why no turn should run.
func (s *Session) StartSlashPrompt(ctx context.Context, text string) (refusal string) {
	name, args, ok := skills.ParseSlash(text)
	if !ok {
		return ""
	}
	if sk, found := s.UserSkill(name); found {
		if !s.SkillEnabled(sk.Name) {
			return "/" + sk.Name + " is turned off."
		}
		s.StartUserSkill(sk, args, "")
		return ""
	}
	if name == CommandInit || name == CommandCodeReview {
		return s.StartPromptCommand(ctx, name, args, "")
	}
	return ""
}

// StartPromptCommand starts one of the built-in commands that are prompts. refusal, when set, is why there is nothing to
// do: no model request is made.
func (s *Session) StartPromptCommand(ctx context.Context, name, args, messageID string) (refusal string) {
	var vars map[string]string
	id := ""
	switch name {
	case CommandInit:
		id = reminderCommandInit
		vars = s.initVars(args)
	case CommandCodeReview:
		id = reminderCommandCodeReview
		var why string
		if vars, why = s.codeReviewVars(ctx, args); why != "" {
			return why
		}
	default:
		return "Unknown command /" + name
	}
	tmpl, err := s.promptTemplate(ctx, id)
	if err != nil {
		return "/" + name + " can't run: " + err.Error()
	}
	s.Agent.ActivateSkill(&agent.SkillActivation{
		MessageID: messageID,
		Use:       session.SkillUse{Name: name, Kind: "builtin", Source: "builtin", Args: args},
		Prepare: func(context.Context) (string, string, string) {
			return prompts.Render(tmpl, vars, nil), "", ""
		},
	})
	return ""
}

// promptTemplate finds a command's text: in the session's frozen bundle or, for a session older than the command, in the
// current one.
func (s *Session) promptTemplate(ctx context.Context, id string) (string, error) {
	if t := s.Agent.Bundle().Reminder(id, ""); t != "" {
		return t, nil
	}
	if s.promptLoader != nil {
		if b, err := s.promptLoader.Get(ctx, s.Agent.Model()); err == nil {
			if t := b.Reminder(id, ""); t != "" {
				return t, nil
			}
		}
	}
	return "", errors.New("the xlsft`s harness server doesn't have its prompt yet; update the server")
}

func (s *Session) initVars(args string) map[string]string {
	dir := s.Dir()
	target, existing := "CLAUDE.md", "none"
	for _, n := range []string{"CLAUDE.md", "AGENTS.md", filepath.Join(".claude", "CLAUDE.md")} {
		if st, err := os.Stat(filepath.Join(dir, n)); err == nil && st.Mode().IsRegular() {
			target, existing = n, n
			break
		}
	}
	return map[string]string{"cwd": dir, "target": target, "existing": existing, "arguments": args}
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\n"), err
}

// codeReviewVars describes the diff to review. Nothing to look at, and nothing asked for instead, ends it here: there is
// no reason to spend a model request on saying so.
func (s *Session) codeReviewVars(ctx context.Context, args string) (map[string]string, string) {
	dir := s.Dir()
	status, err := runGit(ctx, dir, "status", "--short")
	if err != nil {
		if args == "" {
			return nil, "/code-review reviews the current diff, and this folder isn't a git repository. Say what to review: /code-review <files or a focus>."
		}
		return map[string]string{"cwd": dir, "status": "(not a git repository)", "diff_stat": "", "arguments": args}, ""
	}
	if status == "" && args == "" {
		return nil, "Nothing to review: the working tree has no changes. Name a range to review committed work: /code-review main..HEAD."
	}
	stat, err := runGit(ctx, dir, "diff", "HEAD", "--stat")
	if err != nil {
		stat, _ = runGit(ctx, dir, "diff", "--stat") // no commit yet
	}
	return map[string]string{"cwd": dir, "status": status, "diff_stat": stat, "arguments": args}, ""
}
