package permissions

import (
	"context"
	"testing"
)

func TestMCPRuleMatching(t *testing.T) {
	cases := []struct {
		rule, tool string
		want       bool
	}{
		{"mcp__github", "mcp__github__create_issue", true},
		{"mcp__github", "mcp__github", true},
		{"mcp__github", "mcp__githubber__x", false},
		{"mcp__github", "mcp__github_", false},
		{"mcp__github__create_issue", "mcp__github__create_issue", true},
		{"mcp__github__create_issue", "mcp__github__list_issues", false},
		{"mcp__github__*", "mcp__github__list_issues", true},
		{"mcp__github__*", "mcp__gitlab__list_issues", false},
		{"mcp__*", "mcp__slack__send", true},
		{"mcp__", "mcp__slack__send", false},
	}
	for _, c := range cases {
		if got := ParseRule(c.rule).toolMatches(c.tool, false); got != c.want {
			t.Errorf("rule %s vs %s = %v, want %v", c.rule, c.tool, got, c.want)
		}
	}
}

type countingAsker struct{ n int }

func (a *countingAsker) Ask(context.Context, Request) (Decision, error) {
	a.n++
	return Decision{Behavior: Allow}, nil
}

func TestMCPServerRules(t *testing.T) {
	e := New(Config{Cwd: t.TempDir(), Allow: []string{"mcp__github"}, Deny: []string{"mcp__evil"}, Ask: []string{"mcp__slack__send"}})
	check := func(tool string) Behavior { return e.Evaluate(Request{ToolName: tool}).Behavior }
	if check("mcp__github__create_issue") != Allow {
		t.Fatal("server-level allow rule did not apply")
	}
	if check("mcp__evil__anything") != Deny || !e.IsDisabled("mcp__evil__anything") || e.IsDisabled("mcp__github__x") {
		t.Fatal("server-level deny rule did not apply")
	}
	if d := e.Evaluate(Request{ToolName: "mcp__slack__send"}); d.Behavior != Ask || !d.UserOnly {
		t.Fatalf("ask rule = %+v", d)
	}
	if check("mcp__other__x") != Ask {
		t.Fatal("unlisted MCP tool must ask")
	}
}

func TestAlwaysAsk(t *testing.T) {
	e := New(Config{Cwd: t.TempDir(), DefaultMode: ModeBypassPermissions, Allow: []string{"mcp__bank"}, Deny: []string{"mcp__bank__wipe"}})
	if d := e.Evaluate(Request{ToolName: "mcp__bank__pay", AlwaysAsk: true}); d.Behavior != Ask || !d.UserOnly {
		t.Fatalf("AlwaysAsk in bypass mode with an allow rule = %+v", d)
	}
	if d := e.Evaluate(Request{ToolName: "mcp__bank__wipe", AlwaysAsk: true}); d.Behavior != Deny {
		t.Fatalf("deny rules still apply: %+v", d)
	}
	if d := e.Evaluate(Request{ToolName: "mcp__bank__pay"}); d.Behavior != Allow {
		t.Fatalf("without AlwaysAsk bypass allows: %+v", d)
	}
	// Non-interactive: an always-ask tool is denied.
	if d := e.Check(context.Background(), Request{ToolName: "mcp__bank__pay", AlwaysAsk: true}); d.Behavior != Deny {
		t.Fatalf("non-interactive = %+v", d)
	}
	a := &countingAsker{}
	e.SetAsker(a)
	if d := e.Check(context.Background(), Request{ToolName: "mcp__bank__pay", AlwaysAsk: true}); d.Behavior != Allow || a.n != 1 {
		t.Fatalf("interactive = %+v, asked %d", d, a.n)
	}
}
