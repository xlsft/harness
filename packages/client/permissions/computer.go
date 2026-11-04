package permissions

import "strings"

// Computer tools (packages/client/computeruse) control apps of this machine. They need no answer from the permission
// check: the user approves each app in the access card (ComputerAccess), and the tools refuse without that grant,
// whatever the mode, bypass included, since the checks are in the tools. Asking on each click on top of it would only
// train the user to approve. Rules still work: deny Computer (or ComputerType) takes them away, and in plan mode only
// the read-only ones run.

// ComputerRuleTool is the rule tool name that covers every computer tool.
const ComputerRuleTool = "Computer"

func isComputerTool(name string) bool {
	return strings.HasPrefix(name, ComputerRuleTool) && len(name) > len(ComputerRuleTool)
}
