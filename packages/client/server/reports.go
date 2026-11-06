package server

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/xlsft/harness/packages/client/insights"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/tools"
)

// Built-ins that report something about the session or this machine. They run in the client, so the desktop app, the
// terminal UI and a viewer through the relay read the same text.

func runUsageCommand(_ context.Context, _ *Server, ls *liveSession, _ string) slashOutcome {
	var main usageSum
	main.add(ls.sess.Transcript.Entries())
	text := "This session:\n" + main.text()
	if ls.sess.Subagents != nil {
		var agentsCost float64
		var in, out int64
		list := ls.sess.Subagents.List()
		for _, a := range list {
			agentsCost += a.CostUSD
			in += a.InputTokens
			out += a.OutputTokens
		}
		if len(list) > 0 {
			text += fmt.Sprintf("\nIts %d agents: %s in, %s out, $%.2f.\nTogether: $%.2f.", len(list), insights.Tokens(in), insights.Tokens(out), agentsCost, main.cost+agentsCost)
		}
	}
	return slashOutcome{text: text}
}

// usageSum adds up what a transcript's responses used.
type usageSum struct {
	requests                         int
	input, output, cacheRead, cacheW int64
	cost                             float64
	models                           map[string]float64
}

func (u *usageSum) add(entries []session.Entry) {
	seen := map[string]bool{}
	for i := range entries {
		e := &entries[i]
		if e.Type != session.TypeAssistant || e.Message == nil || e.Message.Usage == nil || (e.Message.ID != "" && seen[e.Message.ID]) {
			continue
		}
		seen[e.Message.ID] = true
		m := e.Message.Usage
		u.requests++
		u.input += int64(m.InputTokens)
		u.output += int64(m.OutputTokens)
		u.cacheRead += int64(m.CacheReadInputTokens)
		u.cacheW += int64(m.CacheCreationInputTokens)
		u.cost += e.Message.CostUSD
		if u.models == nil {
			u.models = map[string]float64{}
		}
		u.models[e.Message.Model] += e.Message.CostUSD
	}
}

func (u *usageSum) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d model requests: %s in, %s out, %s from cache, %s written to it.\n", u.requests, insights.Tokens(u.input), insights.Tokens(u.output), insights.Tokens(u.cacheRead), insights.Tokens(u.cacheW))
	fmt.Fprintf(&b, "Cost: $%.2f", u.cost)
	if len(u.models) > 1 {
		names := make([]string, 0, len(u.models))
		for n := range u.models {
			names = append(names, n)
		}
		sort.Strings(names)
		var parts []string
		for _, n := range names {
			parts = append(parts, fmt.Sprintf("%s $%.2f", n, u.models[n]))
		}
		fmt.Fprintf(&b, " (%s)", strings.Join(parts, ", "))
	}
	b.WriteString(". Flat-rate plans count as tokens only.")
	return b.String()
}

func runDebugConfig(_ context.Context, _ *Server, ls *liveSession, _ string) slashOutcome {
	st := ls.sess.SettingsNow()
	if st == nil {
		return slashError("The settings could not be read.")
	}
	return slashOutcome{text: st.DebugReport()}
}

func runFeatures(_ context.Context, _ *Server, ls *liveSession, _ string) slashOutcome {
	return slashOutcome{text: settings.FeatureReport(ls.sess.SettingsNow())}
}

func runInsightsCommand(_ context.Context, _ *Server, _ *liveSession, args string) slashOutcome {
	days := 30
	if a := strings.TrimSpace(args); a != "" {
		if _, err := fmt.Sscanf(a, "%d", &days); err != nil || days <= 0 {
			return slashError("/insights takes a number of days.")
		}
	}
	return slashOutcome{text: insights.Build(insights.Options{Days: days}).Text()}
}

// How many transcripts /fewer-prompts reads, and how often a rule must have been wanted to be offered.
const (
	fewerPromptsTranscripts = 40
	fewerPromptsMinCount    = 3
	fewerPromptsShown       = 15
)

// neverSuggest are rules nobody should get by a suggestion, however often they were approved: they delete, publish, run
// what they fetch or reach for other machines.
var neverSuggest = regexp.MustCompile(`(?i)^Bash\((sudo|su|doas|rm|rmdir|dd|mkfs|shred|chmod|chown|kill|pkill|killall|curl|wget|ssh|scp|rsync|nc|eval|exec|sh|bash|zsh|python\d*|node|ruby|perl|git push|git reset|git clean|git checkout|git rebase|gh|docker|kubectl|terraform|aws|gcloud|az)\b`)

// runFewerPrompts counts what the recent sessions of this folder were asked about, under the rules as they are now, and
// offers the rules that would have saved the most prompts. `apply` writes them to the folder's local settings, and only
// the user at this machine can do that.
func runFewerPrompts(ctx context.Context, _ *Server, ls *liveSession, args string) slashOutcome {
	counts := map[string]int{}
	dir := ls.sess.Dir()
	read := 0
	for _, f := range recentTranscripts(0) {
		if read >= fewerPromptsTranscripts {
			break
		}
		if cwd, _ := transcriptHead(f.path, true); cwd != dir {
			continue
		}
		entries, err := session.Load(f.path)
		if err != nil {
			continue
		}
		read++
		tallyPrompts(ls.sess.Permissions, entries, ls.sess.Tools.Get, counts)
	}
	rules := make([]string, 0, len(counts))
	for r, n := range counts {
		if n >= fewerPromptsMinCount && !neverSuggest.MatchString(r) {
			rules = append(rules, r)
		}
	}
	sort.Slice(rules, func(i, j int) bool {
		if counts[rules[i]] != counts[rules[j]] {
			return counts[rules[i]] > counts[rules[j]]
		}
		return rules[i] < rules[j]
	})
	if len(rules) > fewerPromptsShown {
		rules = rules[:fewerPromptsShown]
	}
	if len(rules) == 0 {
		return slashOutcome{text: fmt.Sprintf("Read %d sessions of this folder and found no rule worth suggesting.", read)}
	}
	if strings.TrimSpace(args) == "apply" {
		if ctx.Value(remoteOriginKey{}) != nil {
			return slashError("Rules that allow more can only be added at this machine.")
		}
		if err := ls.sess.Permissions.AllowAlways(rules); err != nil {
			return slashError("The rules could not be saved: %v", err)
		}
		return slashOutcome{text: fmt.Sprintf("Added %d rules to %s:\n  %s", len(rules), ls.sess.Permissions.LocalSettingsPath(), strings.Join(rules, "\n  "))}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From %d sessions of this folder, rules that would have saved prompts:\n", read)
	for _, r := range rules {
		fmt.Fprintf(&b, "  %4d  %s\n", counts[r], r)
	}
	b.WriteString("Check them: each lets those commands run without asking. /fewer-prompts apply adds them to this folder's local settings.")
	return slashOutcome{text: b.String()}
}

// tallyPrompts replays a transcript's tool calls through the engine, and counts the rule that would have answered each
// call that asks now. What only the user can answer is left out: no rule gets past it.
func tallyPrompts(e *permissions.Engine, entries []session.Entry, lookup func(string) (tools.Tool, bool), counts map[string]int) {
	for i := range entries {
		if entries[i].Type != session.TypeAssistant {
			continue
		}
		for _, b := range entries[i].Blocks() {
			if b.Type != llm.BlockToolUse || (b.Name != "Bash" && !strings.HasPrefix(b.Name, "mcp__") && b.Name != "WebFetch") {
				continue
			}
			req := permissions.Request{ToolName: b.Name, Input: b.Input}
			var in struct {
				Command string `json:"command"`
			}
			_ = json.Unmarshal(b.Input, &in)
			req.Command = in.Command
			if t, ok := lookup(b.Name); ok {
				req.ReadOnly = t.ReadOnly(b.Input)
			}
			d := e.Evaluate(req)
			if d.Behavior != permissions.Ask || d.UserOnly {
				continue
			}
			for _, r := range e.Suggest(req) {
				counts[r]++
			}
		}
	}
}

func runWorkflowsCommand(_ context.Context, _ *Server, ls *liveSession, args string) slashOutcome {
	if ls.sess.Workflows == nil {
		return slashError("Workflows aren't available in this session.")
	}
	fields := strings.Fields(args)
	if len(fields) == 0 || fields[0] == "list" {
		saved := ls.sess.Workflows.Saved()
		runs := ls.sess.Workflows.List()
		var b strings.Builder
		if len(saved) == 0 {
			b.WriteString("No saved workflows.")
		} else {
			b.WriteString("Saved, each a /command:\n")
			for _, w := range saved {
				fmt.Fprintf(&b, "  %s: %s\n", w.Name, w.Description)
			}
		}
		if len(runs) > 0 {
			b.WriteString("\nRuns of this session:\n")
			for _, r := range runs {
				fmt.Fprintf(&b, "  %s  %s  %s\n", r.ID, r.Name, r.State)
			}
			b.WriteString("/workflows save <run id> <name> [--project] keeps one.")
		}
		return slashOutcome{text: strings.TrimRight(b.String(), "\n")}
	}
	if fields[0] != "save" || len(fields) < 3 || len(fields) > 4 || (len(fields) == 4 && fields[3] != "--project") {
		return slashError("usage: /workflows [list] | save <run id> <name> [--project]")
	}
	path, err := ls.sess.Workflows.Save(fields[1], fields[2], len(fields) == 4)
	if err != nil {
		return slashError("%v", err)
	}
	return slashOutcome{text: fmt.Sprintf("Saved as %s. /%s runs it, once you approve the run.", path, fields[2])}
}
