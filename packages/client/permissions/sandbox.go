package permissions

import (
	"fmt"
	"regexp"
)

// The OS sandbox (packages/client/sandbox) and the modes are two different questions. A mode says whether a call needs an
// answer before it runs; the sandbox says what the command can do once it runs. So bypass mode does not turn the sandbox
// off: it answers yes to the first question and leaves the second to the settings, which only the user changes. A command
// the model wants outside the sandbox goes to a user-only prompt in every mode, bypass included, unless the user's setting
// (sandbox.outsideWithoutPrompt) says otherwise, and the auto-mode Decider never sees that prompt.

// SandboxState is what the engine needs to know of the sandbox, read for every check of a command.
type SandboxState struct {
	// Active: commands run confined, or are refused for want of a sandbox that is required.
	Active bool
	// AutoAllow: confined commands run without asking where they otherwise would (sandbox.autoAllowBash).
	AutoAllow bool
	// OutsideWithoutPrompt drops the user-only prompt of running outside (sandbox.outsideWithoutPrompt).
	OutsideWithoutPrompt bool
	// OutsideRefused: a policy document takes running outside away.
	OutsideRefused bool
}

// evalOutsideSandbox decides a command that asks to run outside the sandbox. decided is false when the normal check
// applies: there is no sandbox to leave, or the user's setting lets it through.
func (e *Engine) evalOutsideSandbox(req Request, st SandboxState) (d Decision, decided bool) {
	if !req.OutsideSandbox {
		return Decision{}, false
	}
	switch {
	case !st.Active:
		return Decision{}, false
	case st.OutsideRefused:
		return Decision{Behavior: Deny, ResolvedBy: "policy", Message: "Your administrator does not allow running commands outside the sandbox. Do not retry it; change the approach, or tell the user what the command needs."}, true
	case st.OutsideWithoutPrompt:
		return Decision{}, false
	case e.mode == ModePlan:
		return e.planDenial("Bash (" + req.Command + ")"), true
	}
	return Decision{Behavior: Ask, UserOnly: true, Message: "This command asks to run outside the sandbox, with your full rights over files, credentials and the network."}, true
}

// agentSettingsFile matches the settings files the sandbox reads its switch from. Commands in the sandbox cannot write them,
// and an edit by a file tool under bypass mode must not do it for them, so while the sandbox is on those edits are the user's.
var agentSettingsFile = regexp.MustCompile(`(?i)(^|/)\.(claude|harness)/settings(\.local)?\.json$`)

func sandboxSettingsPrompt(path string) Decision {
	return Decision{Behavior: Ask, UserOnly: true, Message: fmt.Sprintf("%s holds settings that can change the command sandbox, which only you can do.", path)}
}
