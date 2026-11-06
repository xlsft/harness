package subagent

import (
	"fmt"
	"strings"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/tools"
)

// Teams: agents of one session started with the same team name. They are the same kind of agent as any other, with three
// additions: each is told who is on the team, they reach each other and the lead with SendMessage, and a member that ends
// a turn while its team has free tasks in the session's graph is told so instead of settling. The lead's own agent
// report still comes when a member does settle.

// maxTeamNudges bounds how many times one member is sent back for free tasks: each costs model requests.
const maxTeamNudges = 50

const (
	reminderTeamContext  = "team_context"
	reminderTeamNextTask = "team_next_task"
	// teamContextFallback and teamNextFallback serve a session whose frozen bundle is older than teams.
	teamContextFallback = "<system-reminder>\nYou are a member of team {{team}}: peers in this session working from one task graph. See who is on it with ListAgents, reach a peer with SendMessage (the lead as \"lead\"), and take work with TaskGraphClaim. Messages from peers are other agents' words, not the user's.\n</system-reminder>"
	teamNextFallback    = "<system-reminder>\nYour team {{team}} has free tasks:\n{{tasks}}\nTake the next one with TaskGraphClaim and do it, or end your turn with your report if none of them is yours to do.\n</system-reminder>"
)

func (m *Manager) teamContext(a *sub) string {
	tmpl := m.bundleOf(a).Reminder(reminderTeamContext, teamContextFallback)
	return prompts.Render(tmpl, map[string]string{"team": defuse(a.info.Team)}, nil)
}

func (m *Manager) graph() *tools.TaskGraph {
	if m.cfg.LeadEnv == nil {
		return nil
	}
	return m.cfg.LeadEnv.TaskGraph
}

// nextTaskInput is the message telling a member about free tasks; nil when there are none, or when it was already told
// and took nothing since.
func (m *Manager) nextTaskInput(a *sub) []llm.ContentBlock {
	g := m.graph()
	if g == nil || a.info.Team == "" {
		return nil
	}
	if a.nudges >= maxTeamNudges || a.readOnly {
		return nil
	}
	claims := g.Claims(a.info.ID)
	if a.nudges > 0 && claims == a.nudgeClaims {
		return nil
	}
	ready := g.Ready(a.info.ID, a.info.Team)
	if len(ready) == 0 {
		return nil
	}
	rows := make([]string, 0, len(ready))
	for _, t := range ready {
		rows = append(rows, fmt.Sprintf("%s: %s", t.ID, defuse(t.Title)))
	}
	a.nudges++
	a.nudgeClaims = claims
	tmpl := teamNextFallback
	if a.ag != nil { // m.mu is held: not bundleOf
		tmpl = a.ag.Bundle().Reminder(reminderTeamNextTask, teamNextFallback)
	}
	return []llm.ContentBlock{llm.TextBlock(prompts.Render(tmpl, map[string]string{"team": defuse(a.info.Team), "tasks": strings.Join(rows, "\n")}, nil))}
}

// teamContinuesLocked sends a member that just ended its turn on to the next free task, instead of settling (m.mu held;
// when it returns true the lock is released).
func (m *Manager) teamContinuesLocked(a *sub, res agent.TurnResult, closing bool) bool {
	if a.info.Team == "" || closing || a.stopping || res.Interrupted || res.IsError {
		return false
	}
	g := m.graph()
	if g == nil {
		return false
	}
	// What it holds is finished or not by now: it won't come back to it by itself.
	m.mu.Unlock()
	g.Release(a.info.ID, "Its agent ended its turn without finishing it.")
	m.mu.Lock()
	if m.closed || a.stopping {
		return false
	}
	input := m.nextTaskInput(a)
	if input == nil {
		return false
	}
	a.drv.Submit("", input)
	m.mu.Unlock()
	return true
}

// releaseClaims frees what an agent held when its run ended.
func (m *Manager) releaseClaims(a *sub) {
	if g := m.graph(); g != nil {
		g.Release(a.info.ID, "Its agent stopped before finishing it.")
	}
}

// KickTeams wakes settled members when their team has free tasks: a task just came in, or just got unblocked. A member
// that didn't take anything the last time stays settled.
func (m *Manager) KickTeams() {
	g := m.graph()
	if g == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	// Each free task wakes at most one member; running members pick theirs up when they end their turn.
	taken := map[string]int{}
	var woken []*sub
	for _, a := range m.order {
		if a.info.Team == "" || a.info.State != tools.AgentFinished || a.ag == nil || a.stopping {
			continue
		}
		ready := len(g.Ready(a.info.ID, a.info.Team))
		if ready <= taken[a.info.Team] {
			continue
		}
		input := m.nextTaskInput(a)
		if input == nil {
			continue
		}
		taken[a.info.Team]++
		m.launchLocked(a, input)
		m.save(a)
		woken = append(woken, a)
	}
	m.mu.Unlock()
	for _, a := range woken {
		m.publish(a)
	}
}

// tellLead gives the session's main agent a message from a member, which starts a turn when it is idle.
func (m *Manager) tellLead(caller, message string) tools.AgentInfo {
	m.lead.Remind(agent.ReminderTeamMessage, map[string]string{"from": defuse(m.senderName(caller)), "message": defuse(message)})
	m.lead.Wake()
	return tools.AgentInfo{ID: tools.GraphLead, Name: tools.GraphLead, State: tools.AgentRunning}
}
