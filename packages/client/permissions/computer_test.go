package permissions

import "testing"

func TestComputerTools(t *testing.T) {
	const name = "ComputerClick"
	check := func(cfg Config, tool string, readOnly bool) Decision {
		t.Helper()
		cfg.Cwd = t.TempDir()
		return New(cfg).Evaluate(Request{ToolName: tool, Input: []byte(`{}`), ReadOnly: readOnly})
	}
	// The access card is the question: the engine does not ask again.
	if d := check(Config{DefaultMode: ModeDefault, Trusted: true}, name, false); d.Behavior != Allow {
		t.Fatalf("default: %+v", d)
	}
	// Plan mode runs the read-only ones only.
	if d := check(Config{DefaultMode: ModePlan, Trusted: true}, name, false); d.Behavior == Allow {
		t.Fatalf("plan action: %+v", d)
	}
	if d := check(Config{DefaultMode: ModePlan, Trusted: true}, "ComputerScreenshot", true); d.Behavior != Allow {
		t.Fatalf("plan read: %+v", d)
	}
	// A deny rule takes them away, one tool or all of them.
	if d := check(Config{DefaultMode: ModeDefault, Trusted: true, Deny: []string{"Computer"}}, name, false); d.Behavior != Deny {
		t.Fatalf("deny Computer: %+v", d)
	}
	if d := check(Config{DefaultMode: ModeDefault, Trusted: true, Deny: []string{"ComputerType"}}, name, false); d.Behavior != Allow {
		t.Fatalf("deny another: %+v", d)
	}
	if d := check(Config{DefaultMode: ModeDefault, Trusted: true, Deny: []string{"ComputerType"}}, "ComputerType", false); d.Behavior != Deny {
		t.Fatalf("deny ComputerType: %+v", d)
	}
	if isComputerTool("Computer") || isComputerTool("Bash") || !isComputerTool("ComputerKey") {
		t.Fatal("isComputerTool")
	}
}
