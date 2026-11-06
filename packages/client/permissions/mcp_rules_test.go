package permissions

import "testing"

func TestMCPRulesByServerAndTool(t *testing.T) {
	for _, tc := range []struct {
		name             string
		allow, ask, deny []string
		tool             string
		want             Behavior
	}{
		{"exact tool allowed", []string{"mcp__docs__search"}, nil, nil, "mcp__docs__search", Allow},
		{"another tool of the server is not", []string{"mcp__docs__search"}, nil, nil, "mcp__docs__fetch", Ask},
		{"whole server", []string{"mcp__docs"}, nil, nil, "mcp__docs__fetch", Allow},
		{"a server name is not a prefix match", []string{"mcp__docs"}, nil, nil, "mcp__docs2__fetch", Ask},
		{"glob over tool names", []string{"mcp__docs__read_*"}, nil, nil, "mcp__docs__read_page", Allow},
		{"ask beats allow", []string{"mcp__docs"}, []string{"mcp__docs__fetch"}, nil, "mcp__docs__fetch", Ask},
		{"deny of a tool beats allow of the server", []string{"mcp__docs"}, nil, []string{"mcp__docs__fetch"}, "mcp__docs__fetch", Deny},
		{"deny of the server beats allow of a tool", []string{"mcp__docs__fetch"}, nil, []string{"mcp__docs"}, "mcp__docs__fetch", Deny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(Config{Cwd: t.TempDir(), DefaultMode: ModeDefault, Allow: tc.allow, Ask: tc.ask, Deny: tc.deny, Trusted: true})
			if got := e.Evaluate(Request{ToolName: tc.tool}); got.Behavior != tc.want {
				t.Fatalf("%v: got %v, want %v", tc.tool, got.Behavior, tc.want)
			}
		})
	}
	e := New(Config{Cwd: t.TempDir(), Deny: []string{"mcp__docs"}, Trusted: true})
	if !e.IsDisabled("mcp__docs__read_resource") {
		t.Fatal("denying a server must also remove reading its resources")
	}
	e = New(Config{Cwd: t.TempDir(), Deny: []string{"mcp__docs__read_resource"}, Trusted: true})
	if !e.IsDisabled("mcp__docs__read_resource") || e.IsDisabled("mcp__docs__other") {
		t.Fatal("a rule on read_resource names only that")
	}
}
