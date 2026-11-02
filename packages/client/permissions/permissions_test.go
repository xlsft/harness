package permissions

import (
	"context"
	"testing"
)

func TestParseRules(t *testing.T) {
	rules := ParseRules([]string{"Bash(git diff *), Read", "Edit(/src/**) Bash(npm run test:*)"})
	want := []string{"Bash(git diff *)", "Read", "Edit(/src/**)", "Bash(npm run test:*)"}
	if len(rules) != len(want) {
		t.Fatalf("got %v", rules)
	}
	for i, r := range rules {
		if r.String() != want[i] {
			t.Fatalf("rule %d = %s, want %s", i, r, want[i])
		}
	}
}

func TestMatching(t *testing.T) {
	cases := []struct {
		rule string
		req  Request
		want bool
	}{
		{"Bash", Request{ToolName: "Bash", Command: "rm -rf /"}, true},
		{"Bash(git diff *)", Request{ToolName: "Bash", Command: "git diff HEAD"}, true},
		{"Bash(git diff *)", Request{ToolName: "Bash", Command: "git diff"}, true},
		{"Bash(git diff *)", Request{ToolName: "Bash", Command: "git difftool"}, false},
		{"Bash(npm run test:*)", Request{ToolName: "Bash", Command: "npm run test -- -u"}, true},
		{"Bash(ls)", Request{ToolName: "Bash", Command: "ls -la"}, false},
		{"Edit(/src/**)", Request{ToolName: "Edit", FilePath: "/src/a/b.go"}, true},
		{"Edit(/src/**)", Request{ToolName: "Edit", FilePath: "/other/b.go"}, false},
		{"Edit(/src)", Request{ToolName: "Edit", FilePath: "/src/x.go"}, true},
		{"Read", Request{ToolName: "Write"}, false},
	}
	for _, c := range cases {
		r := ParseRules([]string{c.rule})[0]
		if got := r.Matches(c.req); got != c.want {
			t.Errorf("%s vs %+v = %v, want %v", c.rule, c.req, got, c.want)
		}
	}
}

type fixedAsker struct{ d Decision }

func (f fixedAsker) Ask(context.Context, Request) (Decision, error) { return f.d, nil }

func TestModes(t *testing.T) {
	ctx := context.Background()
	write := Request{ToolName: "Write", FilePath: "/w/a.txt"}
	read := Request{ToolName: "Read", ReadOnly: true}
	e := NewEngine(ModeDefault, nil, ParseRules([]string{"Bash(rm *)"}), []string{"/w"})
	if e.Check(ctx, read).Behavior != Allow {
		t.Fatal("read-only must be allowed")
	}
	if d := e.Check(ctx, write); d.Behavior != Deny || d.Message == "" {
		t.Fatalf("non-interactive ask must deny: %+v", d)
	}
	e.SetAsker(fixedAsker{Decision{Behavior: Allow}})
	if e.Check(ctx, write).Behavior != Allow {
		t.Fatal("asker allow")
	}
	if e.Check(ctx, Request{ToolName: "Bash", Command: "rm -rf x"}).Behavior != Deny {
		t.Fatal("deny rule wins")
	}
	e.SetAsker(nil)
	e.SetMode(ModeAcceptEdits)
	if e.Check(ctx, write).Behavior != Allow {
		t.Fatal("acceptEdits allows edits in workdir")
	}
	if e.Check(ctx, Request{ToolName: "Write", FilePath: "/etc/x"}).Behavior == Allow {
		t.Fatal("acceptEdits outside workdir must not allow")
	}
	e.SetMode(ModePlan)
	if e.Check(ctx, write).Behavior != Deny || e.Check(ctx, read).Behavior != Allow {
		t.Fatal("plan mode is read-only")
	}
	e.SetMode(ModeBypassPermissions)
	if e.Check(ctx, Request{ToolName: "Bash", Command: "ls"}).Behavior != Allow {
		t.Fatal("bypass")
	}
	e.SetMode(ModeDontAsk)
	e.SetAsker(fixedAsker{Decision{Behavior: Allow}})
	if e.Check(ctx, Request{ToolName: "Bash", Command: "ls"}).Behavior != Deny {
		t.Fatal("dontAsk denies instead of asking")
	}
}
