package server

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/agent"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// /delegate: the work agreed in this conversation goes to helpers that work in parallel, in the background, while the user
// goes on here. The agent writes a brief from the conversation and starts a lead session with it (SessionStart with lead):
// the lead splits the brief into tasks, runs the helpers (a project run, projectruns.go) and reports back here. This
// session stays free. `/delegate status|pause|resume|stop` steer the lead's run; the agent can start a lead without the
// command too, when the user asks for one.

const maxDelegateNote = 4000

var delegateFlag = regexp.MustCompile(`^--(workers|budget)[= ](\S+)\s*`)

func runDelegate(ctx context.Context, s *Server, ls *liveSession, args string) slashOutcome {
	args = strings.TrimSpace(args)
	if id, coordinator := s.runOfSession(ls.sess.ID); id != "" && !coordinator {
		return slashError("This session is a helper of a delegation: its lead hands out the work.")
	}
	runID := s.delegatedRun(ls.sess.ID)
	var run *agentv1.ProjectRun
	if runID != "" {
		run = s.runSnapshot(runID)
	}
	active := run != nil && !runEnded(run.GetState())
	control := func(a agentv1.ControlProjectRunRequest_Action, done string) slashOutcome {
		if !active {
			return slashOutcome{text: "Nothing is delegated from this session."}
		}
		if _, err := s.ControlProjectRun(ctx, connect.NewRequest(&agentv1.ControlProjectRunRequest{Id: runID, Action: a})); err != nil {
			return slashError("%s", connectMessage(err))
		}
		return slashOutcome{text: done}
	}
	switch strings.ToLower(args) {
	case "status":
		if run == nil {
			return slashOutcome{text: "Nothing is delegated from this session. /delegate hands what you agreed here to helpers."}
		}
		return slashOutcome{text: delegateLine(run)}
	case "pause":
		return control(agentv1.ControlProjectRunRequest_ACTION_PAUSE, "Paused: no new helpers start; the ones working finish their turn. /delegate resume goes on.")
	case "resume", "continue":
		return control(agentv1.ControlProjectRunRequest_ACTION_RESUME, "Resumed.")
	case "stop":
		return control(agentv1.ControlProjectRunRequest_ACTION_STOP, "Stopped: the lead and its helpers were interrupted. Their sessions and branches stay.")
	}
	if active {
		return slashError("Work is already delegated from here: %s. /delegate stop ends it first.", delegateLine(run))
	}
	if st := ls.sess.Settings; st != nil && st.ProjectRunsDisabled {
		return slashError("Delegating is turned off by your organization's policy.")
	}
	var limits []string
	for {
		m := delegateFlag.FindStringSubmatch(args)
		if m == nil {
			break
		}
		switch m[1] {
		case "workers":
			n, err := strconv.Atoi(m[2])
			if err != nil || n < 1 {
				return slashError("--workers takes how many helpers may work at once, 1 or more.")
			}
			limits = append(limits, "max_helpers "+strconv.Itoa(n))
		case "budget":
			v, err := strconv.ParseFloat(strings.TrimPrefix(m[2], "$"), 64)
			if err != nil || v <= 0 {
				return slashError("--budget takes a sum in dollars, like --budget 20.")
			}
			limits = append(limits, "budget_usd "+strconv.FormatFloat(v, 'f', -1, 64))
		}
		args = strings.TrimSpace(args[len(m[0]):])
	}
	if len(args) > maxDelegateNote {
		return slashError("What you add is longer than %d characters; agree on the details in the conversation first.", maxDelegateNote)
	}
	note := ""
	if args != "" {
		note = "What the user added:\n<delegate-note>\n" + defuseGoal(args) + "\n</delegate-note>"
	}
	limitText := "none given: leave max_helpers and budget_usd out"
	if len(limits) > 0 {
		limitText = strings.Join(limits, ", ")
	}
	ls.sess.Agent.Remind(agent.ReminderDelegateStarted, map[string]string{"note": note, "limits": limitText})
	text := "Delegate the work we agreed on."
	if args != "" {
		text = "Delegate: " + args
	}
	if err := s.sendText(ctx, ls, text); err != nil {
		return slashError("%s", connectMessage(err))
	}
	return slashOutcome{text: "The agent writes the brief and starts a lead session, which runs the helpers in the background. /delegate status, pause or stop."}
}

// delegateLine is a run in one line: its state, tasks and spending.
func delegateLine(run *agentv1.ProjectRun) string {
	working, done := 0, 0
	for _, w := range run.GetWorkers() {
		switch {
		case workerActive(w.GetState()):
			working++
		case w.GetState() == agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_FINISHED || w.GetState() == agentv1.ProjectWorkerState_PROJECT_WORKER_STATE_MERGED:
			done++
		}
	}
	spent := fmt.Sprintf("$%.2f", run.GetUsage().GetCostUsd())
	if b := run.GetLimits().GetBudgetUsd(); b > 0 {
		spent += fmt.Sprintf(" of $%.2f", b)
	}
	state := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(run.GetState().String(), "PROJECT_RUN_STATE_"), "_", " "))
	line := fmt.Sprintf("%s, %d helper(s) working, %d done, %s spent", state, working, done, spent)
	if run.GetReason() != "" {
		line += " (" + run.GetReason() + ")"
	}
	return line
}
