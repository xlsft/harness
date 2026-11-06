package agent

import (
	"context"
	"strings"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/session"
)

// Reminders of skills: what the model may run (names and descriptions, with every request that follows), and the full
// text of what has started.
const (
	ReminderSkillsListing = "skills_listing"
	ReminderSkillInvoked  = "skill_invoked"
)

// Used when a bundle older than these reminders is frozen into the session.
const (
	skillsListingFallback = "<system-reminder>\nSkills you can run with the Skill tool:\n{{listing}}\n</system-reminder>"
	skillInvokedFallback  = "<skill name=\"{{name}}\" directory=\"{{directory}}\" arguments=\"{{arguments}}\">\n{{content}}\n</skill>"
)

// After a compaction the latest skills come back, up to this many and this much text: the instructions the model was
// following must survive, an old pile of them must not fill the new context.
const (
	maxRestoredSkills = 5
	maxRestoredChars  = 40000
)

// SkillSource is the session's set of skills as the loop needs it (skills.Manager, through the session's wiring).
type SkillSource interface {
	// Listing is the text of the model's listing for a context window in tokens ("" for none); refresh reads the sources
	// again.
	Listing(window int, refresh bool) string
	// NoteFile hears about a file a tool is about to read or change.
	NoteFile(path string)
}

// TurnModel is the model a skill asks for, until the turn ends.
type TurnModel struct {
	ID, Display string
	Thinking    bool
}

// SkillActivation is a skill or command on its way into the conversation. It takes effect with the next model request of
// a turn, with the user's message that asked for it already in the conversation, not when it was asked for.
type SkillActivation struct {
	Use session.SkillUse
	// MessageID is the queued user message that asked for it: taking the message back takes this back.
	MessageID string
	// Model and Effort apply until the turn ends (nil, "": unchanged).
	Model  *TurnModel
	Effort string
	// Grant applies the skill's tool permissions until the returned function runs, at the end of the turn. May be nil.
	Grant func() (release func())
	// Prepare produces the text for the model, its folder and, when it forked, the subagent it runs in. It may run
	// commands and wait for a permission prompt, so it runs inside the turn; Grant has already applied.
	Prepare func(ctx context.Context) (text, directory, agentID string)
}

// skillState is the agent's part of the session's skills.
type skillState struct {
	// pending wait for the next model request; restore holds texts coming back after a compaction.
	pending []*SkillActivation
	restore []session.ActiveSkill
	// active are the texts the model has read, oldest first.
	active []session.ActiveSkill
	// Until the turn ends.
	turnModel  *TurnModel
	turnEffort string
	releases   []func()
	// listing is the text of the last listing sent; refresh: read the sources before the next request.
	listing string
	refresh bool
}

// thinking is the thinking request for a main-loop call on the session's model.
func (a *Agent) thinking() *llm.Thinking {
	a.mu.Lock()
	capable := a.thinkingCapable
	a.mu.Unlock()
	return a.thinkingFor(capable)
}

// ActivateSkill queues a skill or command for the next model request.
func (a *Agent) ActivateSkill(act *SkillActivation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.skills.pending = append(a.skills.pending, act)
}

// dropSkillsOfLocked takes back what a queued message asked for (a.mu held).
func (a *Agent) dropSkillsOfLocked(messageID string) {
	kept := a.skills.pending[:0]
	for _, p := range a.skills.pending {
		if p.MessageID != messageID {
			kept = append(kept, p)
		}
	}
	a.skills.pending = kept
}

func (a *Agent) startSkillTurn() {
	a.mu.Lock()
	a.skills.refresh = true
	a.mu.Unlock()
}

// finishSkillTurn ends what lasts for a turn. A skill the model started that an interrupt stopped before it took effect
// doesn't surprise the next turn; one the user asked for still comes with the message that asked.
func (a *Agent) finishSkillTurn(interrupted bool) {
	a.mu.Lock()
	releases := a.skills.releases
	a.skills.releases = nil
	a.skills.turnModel, a.skills.turnEffort = nil, ""
	if interrupted {
		kept := a.skills.pending[:0]
		for _, p := range a.skills.pending {
			if p.Use.ToolUseID == "" {
				kept = append(kept, p)
			}
		}
		a.skills.pending = kept
	}
	a.mu.Unlock()
	for _, r := range releases {
		r()
	}
}

// noteSkillListing sends the listing when it is new or changed. It rides on a user message like every reminder, so the
// system prompt and the tools stay as they are and the prompt cache holds.
func (a *Agent) noteSkillListing() {
	if a.cfg.Skills == nil || !a.skillToolOffered() {
		return
	}
	a.mu.Lock()
	refresh, window := a.skills.refresh, a.contextWindow
	a.skills.refresh = false
	sent := a.skills.listing
	a.mu.Unlock()
	text := a.cfg.Skills.Listing(window, refresh)
	if text == sent {
		return
	}
	vars := map[string]string{"listing": text}
	if text == "" {
		vars["listing"] = "(none: no skill can be run now)"
	}
	a.mu.Lock()
	a.skills.listing = text
	// Already in a resumed conversation: nothing to send.
	known := text != "" && a.hasTextLocked(prompts.Render(a.bundle.Reminder(ReminderSkillsListing, skillsListingFallback), vars, nil))
	a.mu.Unlock()
	if !known {
		a.Remind(ReminderSkillsListing, vars)
	}
}

// skillToolOffered: a session on a bundle older than the tool, or a subagent type without it, has no use for a listing.
func (a *Agent) skillToolOffered() bool {
	if a.cfg.DisabledTools["Skill"] {
		return false
	}
	if _, ok := a.cfg.Tools.Get("Skill"); !ok {
		return false
	}
	_, ok := a.Bundle().Tools["Skill"]
	return ok
}

func (a *Agent) hasTextLocked(text string) bool {
	for _, m := range a.messages {
		if m.Role != llm.RoleUser {
			continue
		}
		for _, b := range m.Content {
			if b.Type == llm.BlockText && b.Text == text {
				return true
			}
		}
	}
	return false
}

// startSkills puts what's pending into the conversation.
func (a *Agent) startSkills(ctx context.Context, promptID string) {
	a.mu.Lock()
	pending, restore := a.skills.pending, a.skills.restore
	a.skills.pending, a.skills.restore = nil, nil
	bundle := a.bundle
	a.mu.Unlock()
	for _, r := range restore {
		block := llm.TextBlock(r.Text)
		a.appendMessage(llm.RoleUser, []llm.ContentBlock{block})
		a.record(session.Entry{Type: session.TypeUser, Message: session.UserBlocks([]llm.ContentBlock{block}), IsMeta: true, PromptID: promptID, Skill: &session.SkillUse{Name: r.Name, Restored: true}})
	}
	for _, p := range pending {
		a.mu.Lock()
		if p.Model != nil {
			a.skills.turnModel = p.Model
		}
		if p.Effort != "" {
			a.skills.turnEffort = p.Effort
		}
		a.mu.Unlock()
		if p.Grant != nil {
			if release := p.Grant(); release != nil {
				a.mu.Lock()
				a.skills.releases = append(a.skills.releases, release)
				a.mu.Unlock()
			}
		}
		content, directory, agentID := p.Prepare(ctx)
		use := p.Use
		use.AgentID = agentID
		text := prompts.Render(bundle.Reminder(ReminderSkillInvoked, skillInvokedFallback), map[string]string{
			"name": use.Name, "directory": directory, "arguments": use.Args, "content": strings.TrimSpace(content)}, nil)
		block := llm.TextBlock(text)
		a.appendMessage(llm.RoleUser, []llm.ContentBlock{block})
		a.record(session.Entry{Type: session.TypeUser, Message: session.UserBlocks([]llm.ContentBlock{block}), IsMeta: true, PromptID: promptID, Skill: &use})
		a.mu.Lock()
		if agentID == "" {
			a.skills.active = session.NoteActiveSkill(a.skills.active, session.ActiveSkill{Name: use.Name, Text: text})
		}
		a.mu.Unlock()
		a.emit(Event{Kind: EventSkill, Skill: &use})
	}
}

// afterCompactSkills brings back what the summary replaced (a.mu held): the listing with the next request, and the
// text of the latest skills.
func (a *Agent) afterCompactSkills() {
	a.skills.listing = ""
	var keep []session.ActiveSkill
	total := 0
	for i := len(a.skills.active) - 1; i >= 0 && len(keep) < maxRestoredSkills; i-- {
		s := a.skills.active[i]
		if total+len(s.Text) > maxRestoredChars {
			break
		}
		total += len(s.Text)
		keep = append([]session.ActiveSkill{s}, keep...)
	}
	a.skills.active = keep
	a.skills.restore = append([]session.ActiveSkill(nil), keep...)
}

// afterReloadSkills follows a rewind (a.mu held): what started after the point rewound to is gone, and the listing is
// looked at again.
func (a *Agent) afterReloadSkills(active []session.ActiveSkill) {
	a.skills.active = active
	a.skills.listing = ""
}
