# Hooks

Hooks are programs that xlsft\`s harness runs at points of a session, as configured in settings files. They see each point as JSON and can answer it. The runtime is `packages/client/hooks`; the agent loop and the session decide when each event fires. The format is the `hooks` key users already have in `.claude/settings.json`.

```json
{
  "hooks": {
    "PostToolUse": [
      { "matcher": "Edit|Write", "hooks": [{ "type": "command", "command": "\"$HARNESS_PROJECT_DIR\"/.harness/hooks/format.sh", "timeout": 30 }] }
    ]
  }
}
```

## Where they come from

Every level of the settings hierarchy that has a `hooks` block contributes; nothing overrides. Handlers with the same type and command run once per event.

| Source | Runs when |
|---|---|
| `.claude/settings.json`, `.harness/settings.json`, and their `.local.json` | the folder is trusted on this machine (`~/.harness/trust.json`): a repository can commit all four |
| `~/.claude/settings.json`, used until the first sync | always |
| `--settings` | always |
| What the server delivers: the user's `HOOK` document, `hooks` in a `SETTINGS` or `POLICY` document | this machine trusted that exact content (`~/.harness/hook-trust.json`, `SetHooksTrust`). The file is device-local and never syncs, so a compromised server or account cannot run anything here |

A policy document with `allowManagedHooksOnly` leaves only the policy's hooks. `disableAllHooks` turns hooks off: any level can say so, none can turn back on what another turned off, and hooks in a policy keep running unless a policy says it. The per-user switch in Settings > Hooks writes this key. `HARNESS_NO_HOOKS=1` turns them off for one client.

A session reads its hooks when it opens and when it moves to another folder, then not again: a file written mid-session, by the model or anyone else, applies to the next session. Only the switch is read for every event.

## Why they run outside the OS sandbox

On purpose, like MCP servers. The sandbox confines what the model chooses to run; a hook is what the user or an administrator chose. Its job often is what the sandbox refuses (a formatter writing where it likes, a notifier reaching the network), and a hook that polices the model has to stand outside what it polices. The gates are on who can configure one: the trust rules above, plus the sandbox and the permission engine, which both keep the model from writing to `.claude` and `.harness`. A hook that runs a script outside those folders runs whatever that script holds by then, and the model may be able to edit it: keep hook scripts in `.harness/hooks` or `~/.harness`.

## Handlers

| Type | What it does |
|---|---|
| `command` | Runs under `bash -c` (`sh` without bash) in the session's current folder, with the JSON input on stdin. The environment is the client's, plus the folder's environment and `HARNESS_PROJECT_DIR`, also as `CLAUDE_PROJECT_DIR` |
| `http` | POSTs the JSON input. A 2xx body reads as a command's stdout; anything else is a failure that stops nothing. `headers` may name variables with `$NAME`, only those listed in `allowedEnvVars` |

Common fields: `timeout` in seconds (default 10 minutes for a command, 30 seconds for http, 10 seconds for either at `SessionEnd`) and `async: true`, which nothing waits for and whose answer changes nothing. Handlers of one event run in parallel; an interrupt kills them with everything they started. Other types (`prompt`, `agent`, `mcp_tool`) are listed in Settings as not running.

## The contract

Input always has `session_id`, `transcript_path`, `cwd`, `permission_mode`, `hook_event_name`, and `agent_id` and `agent_type` in a subagent. Each event adds its own fields below.

Exit codes of a command: **0** reads stdout; **2** refuses, with stderr as the reason, where the event can refuse, and is a failure elsewhere; **anything else** is a failure, shown in the transcript and otherwise ignored. So are a timeout and stdout that starts with `{` and is not JSON.

Stdout on exit 0 is plain text, or JSON with any of: `continue: false` with `stopReason` (the agent stops working after this point, the reason goes to the user), `systemMessage` (for the user), `suppressOutput`, `decision: "block"` with `reason`, `hookSpecificOutput`. Text and context for the model reach it marked as not the user's words, 10,000 characters per handler at most.

| Event | Matcher looks at | Input adds | What it can do |
|---|---|---|---|
| `SessionStart` | `startup`, `resume`, `compact` | `source`, `model` | Add context (`additionalContext`, or plain stdout). The session opens without waiting; the first turn waits for it |
| `SessionEnd` | why it closed | `reason` | Nothing |
| `UserPromptSubmit` | | `prompt` | Refuse the prompt: it stays out of the conversation. Add context, or plain stdout |
| `PreToolUse` | tool name | `tool_name`, `tool_input`, `tool_use_id` | `permissionDecision`: `allow`, `deny` or `ask`, with `permissionDecisionReason`; `updatedInput` replaces the input whole. Add context |
| `PermissionRequest` | tool name | `tool_name`, `tool_input`, `tool_use_id`, `permission_suggestions`, `reason` | Answer for the user: `decision: { behavior, message, updatedInput, interrupt }` |
| `PermissionDenied` | tool name | `tool_name`, `tool_input`, `tool_use_id`, `reason` | Nothing. Fires when auto mode refuses a call |
| `PostToolUse` | tool name | `tool_input`, `tool_response` | Tell the model: `decision: "block"` with `reason`, or context. `updatedMCPToolOutput` replaces what an MCP tool returned. The tool has run |
| `PostToolUseFailure` | tool name | `tool_input`, `error` | Tell the model |
| `PostToolBatch` | | `tool_calls` | Tell the model, after all the calls of one reply |
| `Stop`, `SubagentStop` | agent type | `stop_hook_active`, `last_assistant_message`; `agent_transcript_path` | Send the agent back to work: exit 2 or `decision: "block"`, the reason is its instruction. At most 16 times in a turn; `stop_hook_active` says it already happened |
| `StopFailure` | error type | `error` | Nothing. Fires when a model request ended the turn |
| `SubagentStart` | agent type | | Add context for the agent |
| `PreCompact` | `manual`, `auto`, `idle` | `trigger`, `custom_instructions` | Refuse the compaction. Plain stdout or context joins its instructions |
| `PostCompact` | as above | `trigger`, `compact_summary` | Nothing |
| `Notification` | `permission_prompt`, `question`, `plan_approval`, `push` | `notification_type`, `title`, `message` | Nothing. Never holds anything up |
| `CwdChanged` | | `old_cwd`, `new_cwd` | Nothing. Fires from the new folder's set when the session moves |

Where handlers disagree the strictest wins (deny, then ask, then allow), whichever finished first; a rewrite comes from the last handler in configuration order, which is the highest settings level.

## Hooks and permissions

A hook widens only what an ordinary prompt would have asked:

- `PreToolUse` runs first, so a rewritten input is what the rules judge and what runs.
- Its `deny` refuses, whatever the rules allow.
- Its `ask` prompts the user even for what would run, in bypass mode too.
- Its `allow` passes an ordinary prompt. A deny rule, plan mode, a read-only agent type, and prompts only the user may answer (ask rules, protected paths, password fields, leaving the sandbox, tools that confirm every call) still decide.
- `PermissionRequest` runs only when a prompt is about to reach the user, so deny rules have already spoken. It also runs for a prompt only the user may answer, whose answer it cannot give. A rewrite it makes goes through the rules again.

## In the transcript

Each handler appears as one row, `HookRun` in `SessionEvent`: published when it starts, again when it ends, kept in the transcript when it ends, so a resumed session shows it. Hooks of subagents show in the session. Settings > Hooks lists what is configured and whether each runs.

## ACP sessions

A session of an external ACP agent runs none of these. That agent has its own loop and tools, xlsft\`s harness only shows what it reports, and an agent that reads the same settings files (Claude Code does) runs their hooks itself: running them here too would run each twice, and a `PreToolUse` here would be too late to stop anything. What does apply is the transcript, which shows whatever the agent reports. A hook meant for those sessions belongs in that agent's own configuration.
