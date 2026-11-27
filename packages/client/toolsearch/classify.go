// Package toolsearch defers tool definitions: a deferred tool is listed to the model by name and a one-line summary,
// and its full definition is sent only once the model loaded it with ToolSearch (or called it). Built-in tools and
// MCP tools share one loader per session, so loaded tools are appended to the request's tool list in load order.
// The design is in docs/design/tool-deferral.md.
package toolsearch

// Tier says whether a built-in tool's definition goes with every request.
type Tier int

const (
	// Always: the definition goes with every request. Also the tier of a tool this table does not name, so a new
	// tool is never hidden by accident.
	Always Tier = iota
	// Deferred: listed by name and summary until the model loads it.
	Deferred
)

// builtinTiers classifies the built-in tools (the keys of packages/core/prompts/tools). Always: the core coding set
// and what nearly every session or turn uses. Deferred: tools for one kind of task, which a session needs rarely or
// never. TestEveryBundleToolIsClassified keeps this table complete.
var builtinTiers = map[string]Tier{
	// Files, search and commands.
	"Read": Always, "Write": Always, "Edit": Always, "MultiEdit": Always, "Glob": Always, "Grep": Always,
	"Bash": Always, "PowerShell": Always,
	// Background work the system prompt steers to.
	"TaskOutput": Always, "TaskStop": Always, "Wait": Always, "Monitor": Always,
	// The web.
	"WebFetch": Always, "WebSearch": Always,
	// Planning, questions and the end of a turn.
	"TodoWrite": Always, "AskUserQuestion": Always, "EnterPlanMode": Always, "ExitPlanMode": Always, "WrapUp": Always,
	// Agents and skills; Advisor exists only when the user configured an advisor model.
	"Agent": Always, "Skill": Always, "Advisor": Always,
	// The loader itself.
	Name: Always,

	// Code intelligence and notebooks.
	"LSP": Deferred, "LS": Deferred, "NotebookEdit": Deferred, "NotebookRun": Deferred,
	// Agents beyond starting one, teams and workflows.
	"SendMessage": Deferred, "ListAgents": Deferred, "TaskInput": Deferred, "Workflow": Deferred,
	"TaskGraphAdd": Deferred, "TaskGraphClaim": Deferred, "TaskGraphList": Deferred, "TaskGraphUpdate": Deferred,
	// Where the session works.
	"EnterWorktree": Deferred, "ExitWorktree": Deferred, "ChangeDirectory": Deferred, "RequestDirectory": Deferred,
	"ProposeBranch": Deferred, "SyncWithBase": Deferred,
	// Plan mode helpers and reviews.
	"ReportFindings": Deferred, "MarkChapter": Deferred,
	// Reaching the user.
	"PushNotification": Deferred, "SendUserFile": Deferred, "Show": Deferred, "ReadNotifications": Deferred,
	"OpenInEditor": Deferred, "RevealPath": Deferred, "KeepAwake": Deferred, "Visual": Deferred,
	// The user's library: memory, knowledge, issues, documents, artifacts.
	"Memory": Deferred, "KnowledgeSearch": Deferred, "KnowledgeRead": Deferred, "Issues": Deferred,
	"DocumentRead": Deferred, "DocumentEdit": Deferred, "ArtifactPublish": Deferred,
	// Pull requests and CI.
	"PrStatus": Deferred, "PrBind": Deferred, "PrUnbind": Deferred, "PrMonitor": Deferred, "PrAutoMerge": Deferred,
	// Schedules.
	"ScheduleWakeup": Deferred, "ScheduleList": Deferred, "ScheduleCancel": Deferred,
	// Other sessions.
	"SessionStart": Deferred, "SessionSend": Deferred, "SessionList": Deferred, "SessionArchive": Deferred,
	"SessionDismiss": Deferred, "SessionHandoff": Deferred,
	// This session's settings and the user's preferences.
	"SessionSetModel": Deferred, "SessionSetEffort": Deferred, "SessionSetMode": Deferred, "SessionSetTitle": Deferred,
	"SettingsGet": Deferred, "SettingsSet": Deferred,
	// The user's terminal panes.
	"TerminalList": Deferred, "TerminalRead": Deferred, "TerminalOpen": Deferred, "TerminalRun": Deferred,
	"TerminalStop": Deferred,
	// Dev server previews.
	"PreviewStart": Deferred, "PreviewStop": Deferred, "PreviewLogs": Deferred, "PreviewList": Deferred,
	// The browser.
	"BrowserNavigate": Deferred, "BrowserSnapshot": Deferred, "BrowserClick": Deferred, "BrowserType": Deferred,
	"BrowserKey": Deferred, "BrowserScroll": Deferred, "BrowserWait": Deferred, "BrowserScreenshot": Deferred,
	"BrowserTabs": Deferred, "BrowserEval": Deferred, "BrowserConsole": Deferred, "BrowserNetwork": Deferred,
	"BrowserRecord": Deferred,
	"EditorDiagnostics": Deferred, "ImageGenerate": Deferred,
	"ProjectStatus": Deferred, "ProjectFinish": Deferred, "ProjectWorkerStart": Deferred, "ProjectWorkerMerge": Deferred,
	"SimulatorList": Deferred, "SimulatorBoot": Deferred, "SimulatorButton": Deferred, "SimulatorInstall": Deferred,
	"SimulatorLaunch": Deferred, "SimulatorScreenshot": Deferred, "SimulatorSwipe": Deferred, "SimulatorTap": Deferred,
	"SimulatorType": Deferred, "DemoRecording": Deferred, "DemoSkillSave": Deferred,
	// Computer use.
	"ComputerAccess": Deferred, "ComputerActivate": Deferred, "ComputerApps": Deferred, "ComputerBatch": Deferred,
	"ComputerClick": Deferred, "ComputerDrag": Deferred, "ComputerFind": Deferred, "ComputerKey": Deferred,
	"ComputerMenu": Deferred, "ComputerScreenshot": Deferred, "ComputerScroll": Deferred, "ComputerType": Deferred,
	// MCP resources (only registered with MCP servers that have resources).
	"ListMcpResources": Deferred, "ReadMcpResource": Deferred,
	// Bundle entries that are not tools of their own: the per-server authenticate tool of MCP.
	"McpAuthenticate": Always,
}

// Classify returns the tier of a built-in tool; Always for a name the table does not know.
func Classify(name string) Tier { return builtinTiers[name] }

// Classified reports whether the table names the tool.
func Classified(name string) bool {
	_, ok := builtinTiers[name]
	return ok
}
