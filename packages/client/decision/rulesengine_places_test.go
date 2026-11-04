package decision

import "testing"

func TestPlaceToolRules(t *testing.T) {
	for _, c := range []struct {
		tool, input string
		want        RuleDecision
	}{
		{"EnterWorktree", `{}`, RuleAllow},
		{"ChangeDirectory", `{"path":"/x"}`, RuleAllow},
		{"RequestDirectory", `{"path":"/x","reason":"r"}`, RuleAllow},
		{"ProposeBranch", `{"name":"b"}`, RuleAllow},
		{"SyncWithBase", `{}`, RuleAllow},
		{"ExitWorktree", `{"action":"keep"}`, RuleAllow},
		{"ExitWorktree", `{"action":"remove"}`, RuleAllow},
		{"ExitWorktree", `{"action":"remove","discard_changes":true}`, RuleAsk},
	} {
		got := EvaluateRules(RulesInput{ToolName: c.tool, Input: []byte(c.input), Cwd: "/w"}, RulesOptions{})
		if got.Decision != c.want {
			t.Errorf("%s %s: %s (%s)", c.tool, c.input, got.Decision, got.Reason)
		}
	}
}
