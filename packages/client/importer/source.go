package importer

import (
	"context"
	"path/filepath"
	"strings"
)

// The import engine: one scan → choose → run flow for every tool harness can import from. A tool is a Source (a reader):
// it only finds and parses its files into Found items. The engine (engine.go) decides where each item goes, whether it is
// already there, what looks like a secret, and does the writing, with the same machinery as the Claude Code imports
// (document upload through Putter, marked blocks in AGENTS.md, trust gating, .git/info/exclude). Readers live in
// package importer/sources; docs/design/import.md says how to add one.

// Kind is what a found item is; it decides where the item goes.
type Kind string

const (
	// KindInstructions is free text that always applies: folder → a marked block in <dir>/AGENTS.md; user → a RULE
	// document "<source>/<name>".
	KindInstructions Kind = "instructions"
	// KindRule is an instruction file of its own, optionally limited by `paths:` frontmatter: folder →
	// <dir>/.harness/rules/<name>.md; user → a RULE document "<source>/<name>.md".
	KindRule Kind = "rule"
	// KindCommand is a slash command / saved prompt (markdown with frontmatter): folder → .harness/commands/<name>.md;
	// user → a COMMAND document.
	KindCommand Kind = "command"
	// KindAgent is a subagent / custom mode (markdown with frontmatter): folder → .harness/agents/<name>.md; user → an
	// AGENT document.
	KindAgent Kind = "agent"
	// KindSkill is a skill directory (Text is SKILL.md, Files the rest): folder → .harness/skills/<name>/; user → a SKILL
	// document.
	KindSkill Kind = "skill"
	// KindMCP is an MCP server (MCP is set): folder → the folder's .mcp.json; user → a server on this device.
	KindMCP Kind = "mcp"
	// KindSettings is harness settings JSON (Settings is set) that maps cleanly: folder → merged into
	// .harness/settings.json (hooks and allow rules wait for folder trust); user → listed for manual setup.
	KindSettings Kind = "settings"
	// KindSession is a conversation transcript. The engine does not import them yet: listed for manual handling.
	KindSession Kind = "session"
)

// Section groups kinds for choosing; the frontend shows one group per section.
func (k Kind) Section() string {
	switch k {
	case KindInstructions, KindRule:
		return "instructions"
	case KindCommand:
		return "commands"
	case KindAgent:
		return "agents"
	case KindSkill:
		return "skills"
	case KindMCP:
		return "mcp"
	case KindSettings:
		return "settings"
	case KindSession:
		return "sessions"
	}
	return "other"
}

// SourceInfo describes a source for the list of sources.
type SourceInfo struct {
	// ID is stable and lowercase ("claude-desktop", "folder"); documents imported from it carry "import:<id>".
	ID string
	// Name is the tool's name; Description says what is imported from it.
	Name, Description string
	// Logo is a key of the UI's logo table (app/utils/providerLogos.ts); "" shows the name's initials.
	Logo string
	// User says the source reads user-level files (ScanUser); Folder that it reads a project folder (FolderPatterns).
	User, Folder bool
}

// Env is where a reader looks. Readers never call os.UserHomeDir or read the environment themselves, so tests point
// them at a temporary home.
type Env struct {
	// Home is the user's home directory.
	Home string
	// GOOS is the operating system the paths are for (runtime.GOOS outside tests).
	GOOS string
	// AppData is %APPDATA% on Windows; default Home\AppData\Roaming.
	AppData string
	// ConfigHome is $XDG_CONFIG_HOME on Linux; default Home/.config.
	ConfigHome string
	// CodexHome is $CODEX_HOME; default Home/.codex.
	CodexHome string
	// CopilotHome is $COPILOT_HOME; default Home/.copilot.
	CopilotHome string
}

// Codex is Codex CLI's home folder.
func (e Env) Codex() string {
	if e.CodexHome != "" {
		return e.CodexHome
	}
	return filepath.Join(e.Home, ".codex")
}

// Copilot is GitHub Copilot's user folder.
func (e Env) Copilot() string {
	if e.CopilotHome != "" {
		return e.CopilotHome
	}
	return filepath.Join(e.Home, ".copilot")
}

// UserConfigDir is the per-user application config folder of the OS: ~/Library/Application Support on macOS,
// %APPDATA% on Windows, $XDG_CONFIG_HOME (~/.config) elsewhere.
func (e Env) UserConfigDir() string {
	switch e.GOOS {
	case "darwin":
		return filepath.Join(e.Home, "Library", "Application Support")
	case "windows":
		if e.AppData != "" {
			return e.AppData
		}
		return filepath.Join(e.Home, "AppData", "Roaming")
	}
	if e.ConfigHome != "" {
		return e.ConfigHome
	}
	return filepath.Join(e.Home, ".config")
}

// UserConfigDirXDG is ~/.config (or $XDG_CONFIG_HOME) on every system: where CLI tools keep their files, macOS
// included.
func (e Env) UserConfigDirXDG() string {
	if e.ConfigHome != "" {
		return e.ConfigHome
	}
	return filepath.Join(e.Home, ".config")
}

// Display turns a path below Home into "~/…" for notes and item sources.
func (e Env) Display(p string) string {
	if e.Home != "" {
		if rel, err := filepath.Rel(e.Home, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(p)
}

// Source is one tool's reader. It only reads and parses; it never writes, uploads or decides about secrets.
type Source interface {
	Info() SourceInfo
	// Detect says cheaply whether the tool has user-level files on this machine (a config file or folder exists).
	Detect(env Env) bool
	// ScanUser reads the user-level files. A missing file is not an error: it yields nothing. An unreadable or
	// malformed one yields a Found with StatusError and a note, so the rest still imports.
	ScanUser(ctx context.Context, env Env) ([]Found, error)
	// FolderPatterns are the project files the tool reads. The engine walks a folder once and hands every match to
	// its pattern's Read; the Any folder source uses the patterns of every registered source.
	FolderPatterns() []FolderPattern
}

// FolderPattern is one kind of project file of a tool.
type FolderPattern struct {
	// Glob is a slash path relative to a directory of the project, each segment a filepath.Match pattern:
	// ".cursorrules", ".cursor/rules/*.mdc". It is tried in the root and in every subfolder the walk visits.
	Glob string
	// Read turns one matched file into items. Found.Dir and Found.Source default to the match's.
	Read func(f FolderFile) []Found
}

// FolderFile is a matched project file, already read (links that leave the folder are refused, size is bounded).
type FolderFile struct {
	// Rel is the file's slash path from the scanned root; Dir the directory the pattern matched in ("" for the root).
	Rel, Dir string
	Data     []byte
	// Tree reads the files of a folder of the project (slash path from the root), for a skill: relative path → file.
	// Links and oversized files are left out and named in skipped.
	Tree func(rel string) (files []File, skipped []string)
}

// File is one extra file of a skill.
type File struct {
	// Path is relative to the skill's folder, slash-separated.
	Path       string
	Data       []byte
	Executable bool
}

// Found is one thing a reader found.
type Found struct {
	Kind Kind
	// Name is the item's key within its kind: a rule's relative name without extension ("frontend/react"), a command
	// name ("git:commit"), an MCP server name, a skill name. For instructions, a short label ("GEMINI.md").
	Name string
	// Source is the file read, for display: "~/…" at user level (Env.Display), root-relative in a folder.
	Source string
	// Text is the markdown of instructions, rules, commands, agents and skills (SKILL.md).
	Text string
	// Files are a skill's other files.
	Files []File
	// MCP is set for KindMCP. Values are as written in the tool's file; the engine handles secrets and ${VAR}s.
	MCP *MCPServer
	// Settings is set for KindSettings: harness settings keys only.
	Settings map[string]any
	// Session is set for a KindSession the reader converted; without it the session is listed for manual setup.
	Session *SessionSource
	// Dir is, in a folder, the project directory the item applies to (slash, "" for the root).
	Dir string
	// Status is empty for an importable item. A reader may set StatusSkipped, StatusNoted (to set up by hand) or
	// StatusError, with Note saying why.
	Status Status
	// Note is shown with the item; the engine adds its own after it.
	Note string
}

// MCPServer is an MCP server as another tool configures it.
type MCPServer struct {
	// Transport: "stdio", "http" (Streamable HTTP), "sse" or "ws". Empty: stdio with a command, else http.
	Transport string
	Command   string
	Args      []string
	Env       map[string]string
	Cwd       string
	URL       string
	Headers   map[string]string
	// Disabled: the tool has it switched off; it is imported switched off.
	Disabled bool
}

// transport resolves an empty Transport.
func (m *MCPServer) transport() string {
	t := strings.ToLower(strings.TrimSpace(m.Transport))
	switch t {
	case "":
		if m.Command == "" && m.URL != "" {
			return "http"
		}
		return "stdio"
	case "streamable-http", "streamable_http", "streamablehttp", "http":
		return "http"
	case "websocket":
		return "ws"
	}
	return t
}
