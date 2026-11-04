package permissions

import (
	"fmt"
	"path/filepath"
	"strings"
)

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

// The computer use settings (the enable switch and the security profiles) are the user's alone. Writing their directory
// goes through a user-only prompt in every mode, bypass included, unless the active profile allows it. What this holds:
//
//   - Edit, Write and the other file tools: fully. Their path is known.
//   - Bash: only what can be seen in the command line, meaning write targets (redirections, cp, mv, tee, sed -i, ...) and,
//     for any command that is not read-only, a mention of the directory. It is not a boundary: a shell command can write
//     any file the user can, and a script file, a variable or an encoded command hides the path from any parser
//     (TestComputerSettingsBashLimits). Such a command gets the normal Bash check, which asks in default mode, and runs
//     without asking under bypass mode or a broad allow rule.
//
// So the second line is in computeruse.Manager: a running client reads the file once and ignores later edits, and access
// to an app still takes the user's answer on a card, which lives in memory. An edit made through Bash does hold after the
// client restarts.

// inComputerSettings reports whether path is in the settings directory. Case folded: a default macOS volume takes any case.
func inComputerSettings(path, dir string) bool {
	folded := func(p string) string { return strings.ToLower(evalPath(p)) }
	return within(strings.ToLower(path), strings.ToLower(dir)) || within(folded(path), folded(dir))
}

// computerSettingsGuarded reports whether writing path needs the user-only prompt.
func (e *Engine) computerSettingsGuarded(path string) bool {
	if e.computerSettings == nil || path == "" {
		return false
	}
	dir, protected := e.computerSettings()
	return protected && dir != "" && inComputerSettings(path, dir)
}

// computerSettingsUnguarded reports whether path is in the settings directory and the profile allows writing it, which
// also lifts the general rule on protected paths such as ~/.harness.
func (e *Engine) computerSettingsUnguarded(path string) bool {
	if e.computerSettings == nil {
		return false
	}
	dir, protected := e.computerSettings()
	return !protected && dir != "" && inComputerSettings(path, dir)
}

// bashTouchesComputerSettings returns what a command visibly writes in the settings directory ("" for nothing).
func (e *Engine) bashTouchesComputerSettings(command, cwd string, top []SimpleCommand) string {
	if e.computerSettings == nil {
		return ""
	}
	dir, protected := e.computerSettings()
	if !protected || dir == "" {
		return ""
	}
	for _, p := range CommandWritePaths(command, cwd, e.home) {
		if inComputerSettings(p, dir) {
			return p
		}
	}
	readOnly := len(top) > 0
	for _, c := range top {
		readOnly = readOnly && isReadOnlyCommand(c)
	}
	if readOnly {
		return ""
	}
	low := strings.ToLower(command)
	needles := []string{dir, filepath.ToSlash(filepath.Join(filepath.Base(dir), "settings"))}
	if rel, err := filepath.Rel(e.home, dir); err == nil && e.home != "" && !strings.HasPrefix(rel, "..") {
		needles = append(needles, "~/"+rel, "$HOME/"+rel, "${HOME}/"+rel)
	}
	for _, n := range needles {
		if strings.Contains(low, strings.ToLower(n)) {
			return dir
		}
	}
	return ""
}

func computerSettingsPrompt(path string) Decision {
	return Decision{Behavior: Ask, UserOnly: true, Message: fmt.Sprintf("%s holds the computer use settings, which only you can change. The active security profile keeps writes to it behind this prompt.", path)}
}
