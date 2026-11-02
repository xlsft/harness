package fakegateway

import (
	"path/filepath"
	"strings"

	"github.com/xlsft/harness/packages/client/llm"
)

// FixTestDemo scripts a "fix the failing test" session against a project
// containing calc.go with `return a - b` in Add (see WriteDemoProject):
// parallel Read + Grep, Edit, Bash `go test`, final summary.
func FixTestDemo() Responder {
	return Sequence(
		func(req *llm.Request) Reply {
			cwd := Cwd(req)
			return Reply{Events: Message("msg_demo_1",
				Text("Let me look at the code and find the Add function."),
				ToolUse("toolu_read", "Read", map[string]any{"file_path": filepath.Join(cwd, "calc.go")}),
				ToolUse("toolu_grep", "Grep", map[string]any{"pattern": "func Add", "path": cwd, "output_mode": "content"}),
			)}
		},
		func(req *llm.Request) Reply {
			cwd := Cwd(req)
			return Reply{Events: Message("msg_demo_2",
				Text("Add subtracts instead of adding. Fixing it."),
				ToolUse("toolu_edit", "Edit", map[string]any{"file_path": filepath.Join(cwd, "calc.go"), "old_string": "return a - b", "new_string": "return a + b"}),
			)}
		},
		func(req *llm.Request) Reply {
			return Reply{Events: Message("msg_demo_3",
				ToolUse("toolu_test", "Bash", map[string]any{"command": "go test ./...", "description": "Run the tests"}),
			)}
		},
		func(req *llm.Request) Reply {
			summary := "Fixed Add in calc.go."
			if rs := LastToolResults(req); len(rs) > 0 {
				first := strings.SplitN(strings.TrimSpace(ResultText(rs[0])), "\n", 2)[0]
				if rs[0].IsError {
					summary = "Tests still fail: " + first
				} else {
					summary += " Tests pass: " + first
				}
			}
			return Reply{Events: Message("msg_demo_4", Text(summary))}
		},
	)
}

// DemoFiles is the project used by FixTestDemo.
var DemoFiles = map[string]string{
	"go.mod":       "module demo\n\ngo 1.22\n",
	"calc.go":      "package demo\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a - b\n}\n",
	"calc_test.go": "package demo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"Add(2, 3) = %d, want 5\", got)\n\t}\n}\n",
	"CLAUDE.md":    "# Demo project\n\nRun `go test ./...` to check changes.\n",
}
