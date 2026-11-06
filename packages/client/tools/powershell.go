package tools

import (
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// PowerShell runs a command in PowerShell, the shell of a machine without bash. It shares the session's Shell: the working
// directory, the time limits, the background tasks and the output rules are those of Bash. It does not share Bash's permission
// check, which reads POSIX shell: `echo (Remove-Item x)` looks like an echo there. So a Bash rule or the read-only allowlist never
// approves a PowerShell line; it asks, unless a rule names PowerShell itself (docs/design/platforms.md).
type PowerShell struct{}

// PowerShellToolName is the tool's name.
const PowerShellToolName = "PowerShell"

// powerShellFind is powerShellPath's search; a test replaces it.
var powerShellFind = sync.OnceValue(func() string {
	for _, name := range []string{"pwsh", "powershell"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
})

func powerShellPath() string { return powerShellFind() }

// powerShellFlags come first on its command line. No profile: a profile can do anything, and a command should behave the same
// on every call. The execution policy stays the machine's; -Command does not run into it.
var powerShellFlags = []string{"-NoLogo", "-NoProfile", "-NonInteractive"}

// powerShellScript runs the command handed over in the environment, which needs no quoting, leaves where it ended up in the
// file named there, and exits with the command's status: a native program's exit code, or 1 for an error of PowerShell's.
const powerShellScript = `$ProgressPreference = 'SilentlyContinue'
$__utf8 = New-Object System.Text.UTF8Encoding $false
[Console]::OutputEncoding = $__utf8
$OutputEncoding = $__utf8
$__harnessCmd = $env:__HARNESS_CMD
$__harnessCwd = $env:__HARNESS_CWD_FILE
Remove-Item Env:__HARNESS_CMD, Env:__HARNESS_CWD_FILE -ErrorAction SilentlyContinue
$global:LASTEXITCODE = $null
$__harnessFailed = $false
try {
  Invoke-Expression $__harnessCmd
  if (-not $?) { $__harnessFailed = $true }
} catch {
  [Console]::Error.WriteLine($_)
  $__harnessFailed = $true
}
if ($__harnessCwd) { [IO.File]::WriteAllText($__harnessCwd, (Get-Location -PSProvider FileSystem).ProviderPath) }
if ($null -ne $global:LASTEXITCODE -and $global:LASTEXITCODE -ne 0) { exit $global:LASTEXITCODE }
if ($__harnessFailed) { exit 1 }
exit 0
`

// powerShellArgv is the command line of a run.
func powerShellArgv(exe string) []string {
	return append(append([]string{exe}, powerShellFlags...), "-Command", powerShellScript)
}

// powerShellRefusal is what runs in place of a command the sandbox refuses: the reason comes in the environment.
func powerShellRefusal(exe string) []string {
	return append(append([]string{exe}, powerShellFlags...), "-Command", `[Console]::Error.WriteLine('harness: ' + $env:__HARNESS_REFUSED); exit 126`)
}

// Name implements Tool.
func (*PowerShell) Name() string { return PowerShellToolName }

// HiddenFromModel implements ModelHider: elsewhere Bash is the shell, and without PowerShell there is nothing to offer.
func (*PowerShell) HiddenFromModel() bool { return runtime.GOOS != "windows" || powerShellPath() == "" }

// ReadOnly implements Tool: nothing here can tell what a line of PowerShell does.
func (*PowerShell) ReadOnly(json.RawMessage) bool { return false }

type powerShellInput struct {
	Command         string   `json:"command"`
	Description     string   `json:"description"`
	Timeout         *float64 `json:"timeout"`
	RunInBackground bool     `json:"run_in_background"`
}

// Run implements Tool.
func (*PowerShell) Run(ctx context.Context, env *Env, call Call) Result {
	var in powerShellInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return ErrorResult("InputValidationError: command is required")
	}
	if strings.TrimSpace(in.Description) == "" {
		return ErrorResult("InputValidationError: description is required: a noun phrase of 2 to 6 words naming what the command works on or produces")
	}
	exe := powerShellPath()
	if exe == "" {
		return ErrorResult("PowerShell is not available: neither pwsh nor powershell is on PATH")
	}
	timeout, requested := shellTimeout(in.Timeout)
	return runShellTool(ctx, env, call, shellRun{Command: in.Command, Description: in.Description, Timeout: timeout, Requested: requested, Background: in.RunInBackground, Interpreter: exe})
}
