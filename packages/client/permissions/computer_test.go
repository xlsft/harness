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

func TestDemoAndSimulatorTools(t *testing.T) {
	check := func(cfg Config, tool string, readOnly bool) Decision {
		t.Helper()
		cfg.Cwd = t.TempDir()
		return New(cfg).Evaluate(Request{ToolName: tool, Input: []byte(`{}`), ReadOnly: readOnly})
	}
	// The review card is the question.
	if d := check(Config{DefaultMode: ModeDefault, Trusted: true}, "DemoSkillSave", false); d.Behavior != Allow {
		t.Fatalf("DemoSkillSave: %+v", d)
	}
	if d := check(Config{DefaultMode: ModePlan, Trusted: true}, "DemoSkillSave", false); d.Behavior == Allow {
		t.Fatalf("DemoSkillSave in plan mode: %+v", d)
	}
	if d := check(Config{DefaultMode: ModeDefault, Trusted: true, Deny: []string{"DemoSkillSave"}}, "DemoSkillSave", false); d.Behavior != Deny {
		t.Fatalf("deny DemoSkillSave: %+v", d)
	}
	// The simulator's actions go through the usual check: ask, unless a rule allows them.
	if d := check(Config{DefaultMode: ModeDefault, Trusted: true}, "SimulatorTap", false); d.Behavior != Ask {
		t.Fatalf("SimulatorTap: %+v", d)
	}
	if d := check(Config{DefaultMode: ModeDefault, Trusted: true, Allow: []string{"Simulator*"}}, "SimulatorTap", false); d.Behavior != Allow {
		t.Fatalf("allow Simulator*: %+v", d)
	}
	if d := check(Config{DefaultMode: ModeDefault, Trusted: true}, "SimulatorScreenshot", true); d.Behavior != Allow {
		t.Fatalf("SimulatorScreenshot: %+v", d)
	}
}
